package syncer

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"cctrace/internal/gitctx"
	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
)

// ReenrichOnce scans all Claude JSONL records from the beginning and asks the
// server to update enrichment columns on existing rows, and to merge in the raw
// fields a newer client keeps, without changing offsets. It revisits only what
// sync collected (see reenrichFile).
//
// Only the signal fields go unless includeContent is set: a stored row may have
// been collected while redaction was on, and the policy in force now is not the
// one that applied to it (see reenrichFile).
//
// It also backfills each main transcript's session snapshot from the same
// collected part (backfillSessionSnapshot) and saves the state once at the end,
// even when it stops early. The snapshots are marked dirty, not sent: the caller
// flushes them (FlushObservations), and SnapshotsBackfilled says how many.
func (s *Syncer) ReenrichOnce(ctx context.Context, includeContent bool) (int, error) {
	files, err := sessionlog.FindJSONLFiles(s.claudeDir)
	if err != nil {
		return 0, fmt.Errorf("find jsonl files: %w", err)
	}
	// Ask the exclusion question afresh, as a sync pass does: a query that
	// failed in an earlier pass on this Syncer is not an answer.
	s.accounts.BeginPass()

	// Once for the run rather than per file: sync-state.json is rewritten whole
	// on every Save, and a run visits every file.
	s.backfilled = 0
	stateChanged := false
	defer func() {
		if !stateChanged {
			return
		}
		if err := s.state.Save(); err != nil {
			log.Printf("[syncer] failed to save state: %v", err)
		}
	}()

	total := 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, changed, err := s.reenrichFile(ctx, file, includeContent)
		stateChanged = stateChanged || changed
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return total, ctxErr
			}
			return total, fmt.Errorf("reenrich %s: %w", file, err)
		}
		total += n
	}
	return total, nil
}

// SnapshotsBackfilled is how many session snapshots the last ReenrichOnce
// created or changed.
func (s *Syncer) SnapshotsBackfilled() int {
	return s.backfilled
}

// reenrichFile re-enriches one file's collected records and backfills its
// session snapshot, reporting whether the snapshot state changed.
func (s *Syncer) reenrichFile(ctx context.Context, filePath string, includeContent bool) (int, bool, error) {
	// Only the collection ledger establishes a range this client may revisit.
	// A server-side row match is not consent to read or transmit an unknown file.
	fs := s.state.Files[filePath]
	if fs == nil {
		return 0, false, nil
	}
	records, _, skipped, err := sessionlog.ScanFileWithSkips(ctx, filePath, 0)
	if err != nil {
		return 0, false, err
	}
	if len(records) == 0 {
		return 0, false, nil
	}

	// Before the backfills below as well as the records: they read the same
	// lines again, and a file any part of which was kept back is not read again
	// for either.
	if len(s.collectPrefixes) > 0 && !s.reenrichAllowed(filePath, records, skipped) {
		return 0, false, nil
	}

	projectHash := sessionlog.ProjectHash(s.claudeDir, filePath)
	var meta projectMeta
	for _, r := range records {
		if r.CWD != "" {
			meta = *s.resolveProjectMeta(r.CWD)
			break
		}
	}
	if meta.projectName == "" {
		if cwd := sessionlog.PeekCWD(filePath); cwd != "" {
			meta = *s.resolveProjectMeta(cwd)
		}
	}
	if !gitctx.AllowsRepository(meta.repositoryID, s.collectPrefixes) {
		return 0, false, nil
	}

	// Re-enrichment reads from byte zero, but the prefix the first sync skipped
	// (syncFile's first-run skip) was deliberately never collected, and neither
	// was the remnant a file had left when sync found it shrunk (CollectedFrom).
	// Now that the update carries raw content, re-enrichment must not be a way
	// around either: it revisits only what sync collected, rather than relying on
	// the server finding no row to match. Skipped before anything else so the
	// attribution state starts where sync's did, at the end of that prefix.
	start := collectedStart(fs)
	// Before the records below go through WithholdContent and exclusion: the
	// snapshot reads the lines themselves, holds back its own content, and is
	// filtered by account when it is sent. A file some lines of which were kept
	// back for good fills none of the three, with an allowlist or without one:
	// what they would read again includes lines that were never sent, and the
	// server takes a snapshot whole (mayBackfill).
	backfilled := false
	if mayBackfill(fs) {
		var snapshotChanged bool
		backfilled, snapshotChanged = s.backfillSessionSnapshot(fs, filePath, projectHash, records, includeContent)
		if snapshotChanged {
			s.backfilled++
		}
		// The edits of a session still within its counting window, from the same
		// part, so its counts are not limited to what was read since this client.
		if s.backfillSessionEdits(filePath, records) {
			backfilled = true
		}
		if s.backfillSessionCommits(filePath, records, includeContent) {
			backfilled = true
		}
	}

	storeRecords := make([]*store.SessionRecord, 0, len(records))
	home := normalizeHome(s.claudeDir)
	activeAttributionSkill := ""
	sourceFile := filepath.Base(filePath)
	toolUseID := subagentToolUseID(filePath)
	captureReferences := s.newReferenceCapture(start)
	for _, r := range records {
		// A tail may have grown since sync last uploaded this file. Reenrich
		// must not send its content ahead of the ordinary collection policy.
		if r.Offset < start || r.Offset >= fs.Offset {
			continue
		}
		// The server keeps every key a stored row already has, so a row collected
		// while redaction was on keeps its markers -- but it lacks these fields
		// altogether if it was collected before them, and adding them now would
		// attach the content under whatever policy is in force today. So content
		// waits for --include-content, and only the signals go by default.
		//
		// The row is still marked cctraceCollect, and the mark still holds. An
		// absent signal field means 0, because the line was read again under the
		// same rules. A withheld content field is named in cctraceWithheld, so
		// derived code can tell "withheld" from "none".
		captureReferences(r)
		if !includeContent {
			r.WithholdContent()
		}
		attributionCommand := attributionSkillInvocationName(r, &activeAttributionSkill)
		sr := toStoreRecord(r, s.profileEmail, s.userID, projectHash, attributionCommand)
		s.classifyCommand(sr, r.CWD)
		if sr == nil {
			continue
		}
		sr.SourceFile = sourceFile
		if toolUseID != "" {
			sr.ToolUseID = toolUseID
		}
		// Stamped and filtered exactly as syncFile does, so an excluded account's
		// rows are not touched by an update any more than they are inserted.
		sr.AccountID = s.state.ClaudeAccountAt(home, sr.Ts)
		// Raw goes as the collected fields alone. The server merges nothing else
		// into a stored row, and no column the update fills is read from raw:
		// every one comes from the record, set above. So the conversation itself
		// stays on the machine, whatever the policy and with or without content.
		sr.Raw = r.CollectedRaw()
		storeRecords = append(storeRecords, sr)
	}
	storeRecords, err = s.accounts.Drop(ctx, "anthropic", storeRecords)
	if err != nil {
		return 0, backfilled, err
	}
	if len(storeRecords) == 0 {
		return 0, backfilled, nil
	}

	updated := 0
	for i := 0; i < len(storeRecords); i += batchSize {
		end := i + batchSize
		if end > len(storeRecords) {
			end = len(storeRecords)
		}
		batch := storeRecords[i:end]
		n, err := s.client.SendReenrich(ctx, SyncPayload{
			Agent:              "claude",
			ProfileEmail:       s.profileEmail,
			UserID:             s.userID,
			ProjectHash:        projectHash,
			ProjectName:        meta.projectName,
			GitRemoteURL:       meta.gitRemoteURL,
			RepositoryID:       meta.repositoryID,
			RepositoryIDSource: meta.repositoryIDSource,
			RepositoryName:     meta.repositoryName,
			RepoSubpath:        meta.repoSubpath,
			RepoSubpathPresent: meta.repoSubpathPresent,
			CommitSHA:          meta.commitSHA,
			Branch:             meta.branch,
			Records:            batch,
		})
		if err != nil {
			return updated, backfilled, fmt.Errorf("send reenrich batch: %w", err)
		}
		updated += n
	}
	return updated, backfilled, nil
}

// reenrichAllowed reports whether a whole file may be re-sent under an
// allowlist. Re-enrichment reads from the first byte and posts everything
// under one identity, so nothing in the file may have been kept back, or be
// waiting to be:
//
//   - no record consumed unsent (FileState.ExcludedSeen);
//   - no run held now (FileState.Held) and no unknown last cwd
//     (FileState.CWDUnknown) -- a hold is not a verdict yet, and the lines it
//     covers may name no cwd for a lookup to speak for;
//   - every record's cwd known, followed from line to line as a sync pass
//     follows it (tailCWDs), and certainly allowed now;
//   - no taint above the offset.
//
// The taints are read after the lookups. A lookup is where a replaced
// repository is noticed, and re-enrichment can be the first to look -- at a
// file no sync pass has reached since.
//
// This only knows what this version recorded. A file consumed by an earlier
// cctrace, and content skipped at first sync, carry no record of what was
// kept back.
func (s *Syncer) reenrichAllowed(filePath string, records []*sessionlog.Record, skipped []sessionlog.SkippedSpan) bool {
	if fs := s.state.Files[filePath]; fs != nil && (fs.ExcludedSeen || fs.CWDUnknown || len(fs.Held) > 0) {
		return false
	}
	cwds, ok := s.tailCWDs(filePath, 0, records, skipped)
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for _, cwd := range cwds {
		if cwd == "" {
			return false
		}
		if seen[cwd] {
			continue
		}
		seen[cwd] = true
		if m := s.resolveProjectMeta(cwd); m.hold != "" || !gitctx.AllowsRepository(m.repositoryID, s.collectPrefixes) {
			return false
		}
	}
	offset := s.state.GetOffset(filePath)
	for _, t := range s.state.Taints[filePath] {
		if t.Until > offset {
			return false
		}
	}
	return true
}
