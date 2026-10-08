package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"cctrace/internal/codexlog"
	"cctrace/internal/codexsyncer"
	"cctrace/internal/store"
	"cctrace/internal/syncer"

	"github.com/spf13/cobra"
)

// backfillChunk bounds how many readings are buffered before an upload. A
// machine with a long Codex history has tens of thousands of readings.
const backfillChunk = syncer.MaxQuotaSamplesPerRequest

// backfillQuotaCmd reconstructs Codex rate-limit history from session logs.
//
// This exists because normal sync cannot do it. On its first pass the Codex
// syncer skips every existing file to EOF — deliberately, so installing cctrace
// does not upload a machine's entire back catalogue as if it were new activity.
// The readings sitting in those files are therefore never seen, and unlike
// Claude, whose usage exists only at the instant it is polled, they are
// genuinely recoverable: Codex records the account's meter in the session log
// on every turn.
//
// It is safe to re-run. Every row is keyed by account, window and instant, and
// the server drops conflicts, so a second pass inserts nothing rather than
// duplicating the first.
func backfillQuotaCmd() *cobra.Command {
	var (
		profileName string
		endpoint    string
		local       bool
		dryRun      bool
	)

	cmd := &cobra.Command{
		Use:   "backfill-quota",
		Short: "Reconstruct Codex rate-limit history from existing session logs",
		Long: "Walks every Codex session file and uploads the rate-limit readings they " +
			"already contain.\n\nOrdinary sync cannot do this: its first pass skips " +
			"existing files to EOF, so historical readings are never collected. " +
			"Re-running is safe — readings already held are dropped by the server, not " +
			"duplicated.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackfillQuota(cmd.Context(), profileName, endpoint, local, dryRun)
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Override server HTTP endpoint")
	cmd.Flags().BoolVar(&local, "local", false, "Use local cctraced endpoints")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would be uploaded without sending")
	return cmd
}

func runBackfillQuota(ctx context.Context, profileName, endpointOverride string, local, dryRun bool) error {
	if profileName == "" {
		profileName = os.Getenv("CCTRACE_PROFILE")
	}
	p, err := loadSyncProfile(profileName)
	if err != nil {
		return err
	}

	resolvedEndpointOverride := endpointOverride
	if local {
		applyLocalDevEndpoints(p, &resolvedEndpointOverride)
	}
	ep := profileHTTPAPIEndpoint(p)
	if resolvedEndpointOverride != "" {
		ep = resolvedEndpointOverride
	}
	if ep == "" && !dryRun {
		return fmt.Errorf("no server endpoint configured; run 'cctrace init' first")
	}

	// The account log lives in the Codex syncer's state, which is where the
	// home-to-account observations were recorded.
	state, err := syncer.LoadState(codexsyncer.StatePathForProfile(profileName))
	if err != nil {
		return fmt.Errorf("load codex sync state: %w", err)
	}

	dirs := resolveCodexScanDirs(os.Stdout, p)
	if len(dirs) == 0 {
		return fmt.Errorf("no Codex home directories found")
	}

	profileEmail := resolveProfileEmail(p, "")

	var client *syncer.Client
	if !dryRun {
		client, err = newSyncClient(p, ep, version, profileName)
		if err != nil {
			return err
		}
	}

	// Readings are uploaded as they are found rather than collected first.
	//
	// A machine with a long Codex history holds hundreds of thousands of them,
	// and holding every one in a slice to sort and count before sending the
	// first is memory spent for nothing: the server does not care what order
	// they arrive in, and the counts are just as easily kept as counters. What
	// stays resident is the dedup key set and one chunk.
	sender := &backfillSender{client: client, dryRun: dryRun, seen: map[quotaKey]bool{}}

	for _, dir := range dirs {
		paths, err := codexlog.FindJSONLFiles(dir)
		if err != nil {
			fmt.Printf("  [backfill] %s: %v\n", dir, err)
			continue
		}
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Whole file, from zero: the point is the history the incremental
			// offset was built to skip.
			//
			// ScanRateLimits rather than the full scan: the records the full
			// scan builds are all discarded here, and building them doubles
			// the allocation — 1,684MB against 759MB on one 157MB session,
			// for the same few hundred readings.
			samples, err := codexlog.ScanRateLimits(ctx, path)
			if err != nil {
				fmt.Printf("  [backfill] %s: %v\n", path, err)
				continue
			}
			sender.files++
			if len(samples) == 0 {
				continue
			}
			built := codexsyncer.BuildQuotaSamples(state, dir, profileEmail, path, samples)
			sender.unattributed += countWindows(samples) - len(built)
			if err := sender.add(ctx, built); err != nil {
				return err
			}
		}
	}
	if err := sender.flush(ctx); err != nil {
		return err
	}

	fmt.Printf("  scanned %d session files\n", sender.files)
	fmt.Printf("  found   %d readings\n", sender.found)
	if sender.duplicates > 0 {
		// Codex homes overlap on real machines — a runtime home can hold copies
		// of the same session files. The same meter read twice is one reading,
		// and the server would reject the repeat anyway; dropping it here just
		// avoids uploading it first.
		fmt.Printf("  of which %d were the same reading seen in more than one home\n", sender.duplicates)
	}
	fmt.Printf("  unique  %d readings\n", sender.unique)
	// Reported rather than left implicit: an inferred row is one whose account
	// was chosen across a gap in the observation log, and on a machine where
	// that log starts today every historical row is in that state. Saying so is
	// the difference between history and history-shaped guesswork.
	fmt.Printf("  of which %d have an inferred account\n", sender.inferred)
	if sender.unattributed > 0 {
		fmt.Printf("  skipped %d readings with no attributable account\n", sender.unattributed)
	}
	if dryRun {
		fmt.Println("  (dry run — nothing sent)")
		return nil
	}
	if sender.unique == 0 {
		return nil
	}
	fmt.Printf("  sent %d readings\n  done\n", sender.unique)
	return nil
}

// quotaKey is the history table's primary key, mirrored here so what this
// drops locally is exactly what the server would have dropped.
type quotaKey struct {
	provider, account, window string
	at                        int64
}

// backfillSender buffers readings to a chunk and uploads as it goes.
//
// The alternative — collect everything, then sort, then send — holds every
// reading on a machine that may have hundreds of thousands of them, and buys
// nothing: the server keys each row independently, so arrival order carries no
// meaning. Only the dedup set has to outlive a chunk.
type backfillSender struct {
	client *syncer.Client
	dryRun bool
	seen   map[quotaKey]bool
	buf    []*store.QuotaSample

	files, found, unique, duplicates, inferred, unattributed int
}

func (b *backfillSender) add(ctx context.Context, rows []*store.QuotaSample) error {
	for _, r := range rows {
		b.found++
		k := quotaKey{r.BillingProvider, r.AccountID, r.WindowKey, r.SampledAt.UnixNano()}
		if b.seen[k] {
			b.duplicates++
			continue
		}
		b.seen[k] = true
		b.unique++
		if r.Attribution == store.AttributionInferred {
			b.inferred++
		}
		b.buf = append(b.buf, r)
		if len(b.buf) >= backfillChunk {
			if err := b.flush(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *backfillSender) flush(ctx context.Context) error {
	if len(b.buf) == 0 || b.dryRun {
		b.buf = b.buf[:0]
		return nil
	}
	if err := b.client.SendQuotaSamples(ctx, b.buf); err != nil {
		if errors.Is(err, syncer.ErrQuotaSamplesUnsupported) {
			return fmt.Errorf("server does not accept quota history; upgrade the server first")
		}
		return fmt.Errorf("send readings: %w", err)
	}
	fmt.Printf("  sent %d\n", b.unique)
	b.buf = b.buf[:0]
	return nil
}

func countWindows(samples []codexlog.RateLimitSample) int {
	n := 0
	for _, s := range samples {
		n += len(s.Windows)
	}
	return n
}
