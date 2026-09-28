package codexsyncer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"

	"cctrace/internal/codexappserver"
	"cctrace/internal/codexauth"
	"cctrace/internal/codexlog"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// billingProviderCodex is the canonical provider for Codex subscription burn.
//
// That this is "openai" and not the agent name matters: the ingest paths
// normalise to this vocabulary, and a bare "openai" from other sources means
// metered API-key usage, which burns no subscription at all. The chart's left
// axis is only meaningful because the two are already kept apart.
const billingProviderCodex = "openai"

// BuildQuotaSamples converts scanned readings into history rows.
//
// The account comes from the home the file lives under, resolved at the
// reading's own instant. Codex writes no account into the session log, so the
// home-to-account observation log is the only thing that can say whose meter
// this was — and for anything predating that log the answer is an inference,
// which travels with the row rather than being quietly passed off as measured.
//
// window_key comes from windowKey below: the window's length, qualified by the
// bucket it was metered against when that is not the canonical one.
func BuildQuotaSamples(state *syncer.State, home, profileEmail, sourcePath string, samples []codexlog.RateLimitSample) []*store.QuotaSample {
	// The session UUID, not the path: a path embeds a home directory and would
	// carry a person's account name into every row. The UUID identifies the same
	// file to anyone who has it and matches session_records.session_id, which is
	// what makes an inferred attribution checkable against its source.
	sourceSessionID := codexlog.SessionIDFromPath(sourcePath)
	var out []*store.QuotaSample
	for _, s := range samples {
		accountID, inferred := state.QuotaAccountAt(home, s.Timestamp)
		if accountID == "" {
			// Unattributable rather than broken. Crediting it to whichever
			// account happens to be current would put this reading on another
			// account's line, which is worse than not drawing it.
			continue
		}
		attribution := store.AttributionObserved
		if inferred {
			attribution = store.AttributionInferred
		}
		keys := map[string]bool{}
		for i, w := range s.Windows {
			minutes := w.WindowMinutes
			out = append(out, &store.QuotaSample{
				BillingProvider: billingProviderCodex,
				AccountID:       accountID,
				WindowKey:       uniqueWindowKey(keys, s.LimitID, minutes, i),
				SampledAt:       s.Timestamp,
				UsedPct:         w.UsedPercent,
				ResetsAt:        w.ResetsAt,
				WindowMinutes:   &minutes,
				ScopeLabel:      s.LimitName,
				Plan:            s.PlanType,
				// Historical session payloads do not carry the ChatGPT login
				// email. Do not derive it from auth token claims; a later live
				// app-server sample can safely enrich this series.
				ProfileEmail:    profileEmail,
				Attribution:     attribution,
				SourceSessionID: sourceSessionID,
			})
		}
	}
	return out
}

// BuildAppServerQuotaSamples converts one live app-server reading into history
// rows.
//
// The account is passed in, read from the same home the reading was taken for,
// and the rows are marked observed. That is the difference from
// BuildQuotaSamples above, which walks files written before the home-to-account
// observation log existed and has to infer across the gaps. Nothing about a
// live read is inferred: the account standing at the instant of the read is the
// account the reading describes.
func BuildAppServerQuotaSamples(accountID, profileEmail string, snap *codexappserver.Snapshot) []*store.QuotaSample {
	if snap == nil || snap.FetchedAt.IsZero() || accountID == "" {
		// Unattributable or unplaceable rather than broken. A reading credited
		// to whichever account happens to be current lands on another account's
		// line, which is worse than not drawing it.
		return nil
	}
	keys := map[string]bool{}
	out := make([]*store.QuotaSample, 0, len(snap.Readings))
	for i, r := range snap.Readings {
		minutes := r.WindowMinutes
		out = append(out, &store.QuotaSample{
			BillingProvider: billingProviderCodex,
			AccountID:       accountID,
			WindowKey:       uniqueWindowKey(keys, r.LimitID, minutes, i),
			SampledAt:       snap.FetchedAt,
			UsedPct:         r.UsedPercent,
			ResetsAt:        r.ResetsAt,
			WindowMinutes:   &minutes,
			ScopeLabel:      r.LimitName,
			Plan:            r.PlanType,
			// account/read is the existing non-secret source for the actual
			// ChatGPT login. It avoids parsing token claims from auth.json.
			LoginEmail:   snap.LoginEmail,
			ProfileEmail: profileEmail,
			Attribution:  store.AttributionObserved,
		})
	}
	return out
}

// canonicalLimitID is the bucket the session log has always reported, and the
// only one it ever names.
const canonicalLimitID = "codex"

// windowKey names one series in quota_samples.
//
// The length used to be the whole key, because Codex reports the number
// directly and does not fix which length appears in primary versus secondary —
// filing by position would put weekly percentages on the five-hour line. The
// multi-bucket view breaks that: the account meters several buckets at once and
// two of them report a 10080-minute window. Sharing a key those two rows share
// (billing_provider, account_id, window_key, sampled_at), and the insert's
// ON CONFLICT DO NOTHING then discards one of them with no error and no log.
//
// So the id qualifies the key — but only when it has to. The canonical bucket
// keeps the bare length, and so does a reading whose payload predates the id,
// because those are the same meter and there are already six figures of rows
// filed under the bare form. Qualifying them too would fork every existing
// series into a before and an after with nothing joining them, and stop the
// stored history from being extended, to distinguish a bucket from itself.
func windowKey(limitID string, minutes int) string {
	n := strconv.Itoa(minutes)
	if limitID == "" || limitID == canonicalLimitID {
		return n
	}
	return limitID + ":" + n
}

// uniqueWindowKey is windowKey with a guard against one bucket reporting two
// windows of the same length. That has not been observed — primary and
// secondary have always differed — but the schema permits it, and handing the
// collision to the database means it is resolved by silently dropping a row.
// The position breaks the tie only in that case, where it is the sole thing
// left that tells the two apart.
func uniqueWindowKey(seen map[string]bool, limitID string, minutes, position int) string {
	key := windowKey(limitID, minutes)
	if seen[key] {
		key = fmt.Sprintf("%s#%d", key, position)
	}
	seen[key] = true
	return key
}

// sendQuotaSamples posts readings and reports whether the send failed.
//
// Quota history is a side channel to session sync: a server that cannot take it
// must not stop session records from being collected, so the caller keeps going
// either way. What it must not do is acknowledge the bytes -- the reading exists
// only there, and nothing else on disk can reconstruct the window. A returned
// error therefore means "do not advance past these bytes", not "stop".
//
// A 404 is not a failed send and returns nil: an older server has no endpoint to
// fail, and holding the offset for it would stall collection forever. It latches
// so that server is asked once rather than on every pass for the life of the
// daemon.
func (s *CodexSyncer) sendQuotaSamples(ctx context.Context, home, sourcePath string, samples []codexlog.RateLimitSample) error {
	if len(samples) == 0 || s.quotaUnsupported {
		return nil
	}
	rows := BuildQuotaSamples(s.state, home, s.profileEmail, sourcePath, samples)
	if len(rows) == 0 {
		return nil
	}
	switch err := s.client.SendQuotaSamples(ctx, rows); {
	case err == nil:
		return nil
	case errors.Is(err, syncer.ErrQuotaSamplesUnsupported):
		s.quotaUnsupported = true
		log.Printf("[codex-syncer] server has no quota history endpoint; skipping")
		return nil
	default:
		log.Printf("[codex-syncer] quota history: %v", err)
		return err
	}
}

// PollAppServerQuota reads each Codex home's live rate-limit buckets and posts
// them as history rows.
//
// This is not a replacement for the session-log path, which reads what is
// already on disk and can reconstruct history backwards. It is an addition,
// because what is on disk is one bucket: model-scoped buckets are metered under
// limit ids the session payload never names, so no amount of reading files will
// produce them.
//
// It costs a child process per home per call, which is why the order of the
// checks below matters. Everything that can rule a home out — a latched 404
// from the server, a home nobody has logged into — is answered from local state
// first, so a process is started only when there is somewhere for its answer to
// go. codexappserver.Fetch holds its own cache over both outcomes, so calling
// this more often than intended costs a map lookup rather than a process.
//
// Nothing is reported upward. Quota history is a side channel to session sync:
// a failure here is worth a log line, and returning an error would invite a
// caller to branch on it and retry — which is exactly how the Claude poller's
// five-minute interval once collapsed into a request per second.
func (s *CodexSyncer) PollAppServerQuota(ctx context.Context) {
	if s.quotaUnsupported {
		return
	}
	for _, home := range s.codexDirs {
		accountID, err := codexauth.ReadAccountID(home)
		if err != nil {
			log.Printf("[codex-syncer] app-server quota: read account for %s: %v", home, err)
			continue
		}
		if accountID == "" {
			// A home nobody ever logged into with ChatGPT. Ordinary state, not
			// a failure, and there is no meter to read.
			continue
		}
		snap, err := codexappserver.Fetch(ctx, home)
		if err != nil {
			log.Printf("[codex-syncer] app-server quota: %v", err)
			continue
		}
		rows := BuildAppServerQuotaSamples(accountID, s.profileEmail, snap)
		if len(rows) == 0 {
			continue
		}
		switch err := s.client.SendQuotaSamples(ctx, rows); {
		case err == nil:
		case errors.Is(err, syncer.ErrQuotaSamplesUnsupported):
			s.quotaUnsupported = true
			log.Printf("[codex-syncer] server has no quota history endpoint; skipping")
			return
		default:
			log.Printf("[codex-syncer] app-server quota history: %v", err)
		}
	}
}
