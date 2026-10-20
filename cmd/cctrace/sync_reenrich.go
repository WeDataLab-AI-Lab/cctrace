package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"cctrace/internal/codexsyncer"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// includeContentWarning is printed once by `sync reenrich --include-content`,
// before anything is sent. There is no prompt -- the flag is the decision, and
// a run must not wait on a terminal that may not be there -- so this has to say
// the whole of it: the content goes onto records already on the server, which
// may have been collected under a stricter policy than the one in force now.
const includeContentWarning = `  Warning: --include-content attaches content to records already on the server:
  AskUserQuestion answers, prompts queued while Claude was busy, background task
  results queued the same way, plan files and their paths, task subjects, and
  session recaps, injected instruction files, hook output and system messages.
  Codex instruction context is restored only within the recorded collection
  range; pre-existing history skipped at first sync stays on the machine.
  It also sends session titles and agent names with the session
  snapshots it backfills, including verified commit subjects. Either redaction
  option hides commit subjects; subjects previously hidden stay hidden.
  That includes records collected while redaction was on, if it has since been
  turned off: the content is sent under the redaction options in force now
  (redact_user_prompts=%v, redact_tool_details=%v). A value a record already
  holds, a redaction marker included, is never replaced, and the conversation
  itself is never sent.
`

func runSyncReenrich(claudeDir string, profileName string, profileEmail string, endpointOverride string, local bool, includeContent bool) error {
	if profileName == "" {
		profileName = os.Getenv("CCTRACE_PROFILE")
	}

	p, err := loadSyncProfile(profileName)
	if err != nil {
		return err
	}
	if !p.CollectionEnabled("claude") {
		fmt.Println("  Session log sync is disabled in profile. Run 'cctrace init' to enable.")
		return nil
	}

	if p.HomesVersion != 0 || p.Homes != nil {
		resolved := resolveClaudeDir(p, claudeDir)
		if resolved == "" {
			return fmt.Errorf("explicit home transmission policy does not permit this re-enrichment source")
		}
		claudeDir = resolved
	}
	runtimeEndpoint := p.Server.Endpoint
	if local {
		runtimeEndpoint = applyLocalDevEndpoints(p, &endpointOverride)
	}
	if runtimeEndpoint == "" {
		return fmt.Errorf("no server endpoint configured; run 'cctrace init' to set one")
	}
	autoMigrateCodex(p, profileName, runtimeEndpoint)

	fl, err := acquireSyncLock(profileName, syncLockWaitDuration)
	if err != nil {
		if errors.Is(err, errSyncAlreadyRunning) {
			return errSyncAlreadyRunning
		}
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer fl.Unlock()

	// A re-enrichment reads the sources too, so it reads only those the daemon
	// allows the profile.
	gate := newLineGate(profileName, false)
	defer gate.close()

	state, err := syncer.LoadState(syncStatePathForProfile(profileName))
	if err != nil {
		return fmt.Errorf("load sync state: %w", err)
	}
	ep := profileHTTPAPIEndpoint(p)
	if endpointOverride != "" {
		ep = endpointOverride
	}
	client, err := newSyncClient(p, ep, version, profileName)
	if err != nil {
		return err
	}
	ctx := context.Background()

	resolvedClaudeDir := resolveClaudeDir(p, claudeDir)
	resolvedEmail := resolveProfileEmail(p, profileEmail)
	resolvedUserID := resolveUserID(p)
	if includeContent {
		fmt.Fprintf(os.Stderr, includeContentWarning, p.Options.RedactUserPrompts, p.Options.RedactToolDetails)
	}
	claude := syncer.New(resolvedClaudeDir, resolvedEmail, resolvedUserID, state, client, p.Options.CollectRepositoryPrefixes)
	claudeHeld := len(gate.apply(ctx, "claude", []string{resolvedClaudeDir})) == 0
	var total, backfilled int
	if !claudeHeld {
		n, err := claude.ReenrichOnce(ctx, includeContent)
		if err != nil {
			return fmt.Errorf("reenrich: %w", err)
		}
		total, backfilled = n, claude.SnapshotsBackfilled()
	}

	var codexErr error
	if os.Getenv("CCTRACE_CODEX_SYNC") == "true" || p.CollectionEnabled("codex") {
		codexDirs := resolveCodexScanDirs(os.Stderr, p)
		codexHeld := false
		if all := codexDirs; gate != nil {
			codexDirs = gate.apply(ctx, "codex", all)
			codexHeld = len(codexDirs) == 0 && len(all) > 0
		}
		codexState, cerr := syncer.LoadState(codexsyncer.StatePathForProfile(profileName))
		if codexHeld {
			// Every Codex folder is another profile's to read.
		} else if cerr != nil {
			codexErr = fmt.Errorf("codex reenrich load sync state: %w", cerr)
		} else {
			cs := codexsyncer.New(codexDirs, resolvedEmail, resolvedUserID, codexState, client, p.Options.CollectRepositoryPrefixes)
			// Its backfilled snapshots go in the flush below, with Claude's.
			claude.ServeCodexSnapshots(codexState, codexDirs)
			cn, cerr := cs.ReenrichOnce(ctx, includeContent)
			total += cn
			backfilled += cs.SnapshotsBackfilled()
			if cerr != nil {
				codexErr = fmt.Errorf("codex reenrich: %w", cerr)
			}
		}
	}

	// The backfilled snapshots go now rather than at the daemon's next flush,
	// also when the Codex part failed: what was backfilled stays backfilled.
	// This command holds the sync lock, so no daemon is flushing meanwhile.
	if claudeHeld {
		claude.LeaveClaudeHome()
	}
	flushSessionObservationsBeforeExit(ctx, claude)
	if codexErr != nil {
		return codexErr
	}

	fmt.Printf("  Re-enriched %d records, backfilled %d session snapshots\n", total, backfilled)
	return nil
}

func syncStatePathForProfile(profileName string) string {
	if profileName != "" {
		if dir, err := profile.NamedDir(profileName); err == nil {
			return filepath.Join(dir, "sync-state.json")
		}
	}
	return syncer.DefaultStatePath()
}
