package codexsyncer

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"cctrace/internal/codexlog"
	"cctrace/internal/gitctx"
	"cctrace/internal/projecthash"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// ReenrichOnce scans all Codex JSONL records from the beginning and asks the
// server to update enrichment columns on existing rows without changing offsets.
//
// It also backfills each rollout's session snapshot from the turn lines of the
// part sync collected (syncer.BackfillCodexSnapshot) and saves the state once
// at the end, even when it stops early. The snapshots are marked dirty, not
// sent: the caller flushes them (syncer.Syncer.FlushObservations, after
// ServeCodexSnapshots), and SnapshotsBackfilled says how many.
// includeContent also restores context and supplemental usage rows an older
// parser never emitted. Usage inserts additionally require an anchored conflict
// ledger. Only the already-collected range is eligible; the normal tail offset
// and every pre-consent skip remain unchanged.
func (s *CodexSyncer) ReenrichOnce(ctx context.Context, includeContent ...bool) (int, error) {
	files, err := s.findAllFiles()
	if err != nil {
		return 0, fmt.Errorf("find codex jsonl files: %w", err)
	}

	s.backfilled = 0
	s.accounts.BeginPass()
	stateChanged := false
	defer func() {
		if !stateChanged {
			return
		}
		if err := s.state.Save(); err != nil {
			log.Printf("[codex-syncer] save state: %v", err)
		}
	}()

	total := 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, changed, err := s.reenrichFile(ctx, file, len(includeContent) != 0 && includeContent[0])
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
func (s *CodexSyncer) SnapshotsBackfilled() int {
	return s.backfilled
}

// reenrichFile re-enriches one rollout's records and backfills its session
// snapshot, reporting whether the snapshot state changed.
func (s *CodexSyncer) reenrichFile(ctx context.Context, filePath string, includeContent bool) (int, bool, error) {
	sessionID := codexlog.SessionIDFromPath(filePath)
	records, _, turns, scannedOffset, meta, err := codexlog.ScanFileWithTurnsContext(ctx, filePath, 0, sessionID, codexlog.Metadata{})
	if err != nil {
		return 0, false, err
	}

	cwd, model := meta.CWD, meta.Model
	storeRecords := make([]*store.SessionRecord, 0, len(records))
	var restoreRecords []*store.SessionRecord
	fs := s.state.Files[filePath]
	canRestore := includeContent && fs != nil && fs.Offset <= scannedOffset && !fs.ExcludedSeen && fs.HoldExpired == nil && !fs.CWDUnknown && len(fs.Held) == 0
	// Only a ledger anchored to this exact consumed prefix can authorize a
	// newly inserted usage row. An absent/stale ledger retains metadata-only
	// recovery; it cannot prove which old timestamp ordinals are occupied.
	canRestoreUsage := false
	if canRestore {
		_, canRestoreUsage = s.state.ConflictSeed(filePath, fs.Offset)
	}
	var collectedFrom int64
	if fs != nil {
		collectedFrom = max(fs.SkippedAtFirstSync, fs.CollectedFrom)
	}
	// Reenrich always scans from byte zero, so it starts the count at zero.
	// Seeding it from the file's ledger would instead nudge every record past a
	// prefix it has already counted itself.
	//
	// That reproduces the keys the incremental path assigned for any file the
	// incremental path counted from zero too. A file whose ledger was refused
	// (see State.ConflictSeed) was nudged per scan instead, and this scan's keys
	// will not all match its rows -- the same mismatch the pre-ledger code had.
	// It costs an UPDATE that matches nothing, never a duplicate row: this path
	// updates, it does not insert.
	nudger := syncer.NewConflictNudger(nil)
	sourceFile := filepath.Base(filePath)
	home := s.homeForFile(filePath)
	for _, r := range records {
		if r.CWD != "" {
			cwd = r.CWD
		}
		if r.Model != "" {
			model = r.Model
		}
		if r.CWD == "" {
			r.CWD = cwd
		}
		if r.Model == "" {
			r.Model = model
		}
		sr := toStoreRecord(r, s.profileEmail, s.userID, sessionID, sourceFile, entrypointFromOriginator(meta.Originator, meta.IsSubagentFork))
		if sr == nil {
			continue
		}
		// Attribute by the record's own home and timestamp, same as a live sync.
		sr.AccountID = s.state.CodexAccountAt(home, sr.Ts)
		if r.RecordType == "context" || r.SupplementalUsage {
			// UPDATE cannot recover a row an older parser never emitted.
			// Insert only this new, explicitly requested payload, and only where
			// the local ledger proves collection had already been permitted.
			if canRestore && (!r.SupplementalUsage || canRestoreUsage) && r.Offset >= collectedFrom && r.Offset < fs.Offset &&
				gitctx.AllowsRepository(s.freshGitMeta(r.CWD).RepositoryID, s.collectPrefixes) {
				restoreRecords = append(restoreRecords, sr)
			}
			continue
		}
		nudger.Apply(sr)
		// Existing Codex rows need metadata enrichment only: the server never
		// merges their original message payload. Re-sending it gains nothing
		// and would contradict the content opt-in of this command.
		sr.Raw = nil
		storeRecords = append(storeRecords, sr)
	}
	if len(storeRecords) == 0 && len(restoreRecords) == 0 {
		return 0, false, nil
	}

	projectHash := projecthash.FromPath(cwd)
	if projectHash == "" {
		projectHash = projectHashFromPath(filePath)
	}
	projectName := projecthash.NameFromPath(cwd)
	gitMeta := s.freshGitMeta(cwd)
	if !gitctx.AllowsRepository(gitMeta.RepositoryID, s.collectPrefixes) {
		return 0, false, nil
	}

	// Before the records are sent, and whatever becomes of them: the snapshot
	// reads the turn lines themselves, and is filtered by account when it is
	// sent.
	changed, snapshotChanged := syncer.BackfillCodexSnapshot(s.state.Files[filePath], projectHash, turns)
	if snapshotChanged {
		s.backfilled++
	}

	updated := 0
	if len(restoreRecords) != 0 {
		restoreRecords, err = s.accounts.Drop(ctx, "openai", restoreRecords)
		if err != nil {
			return 0, changed, err
		}
		if len(restoreRecords) != 0 {
			updated, err = s.client.Send(ctx, "codex", s.profileEmail, s.userID, projectHash, projectName, syncer.ProjectIdentity{
				GitRemoteURL: gitMeta.GitRemoteURL, RepositoryID: gitMeta.RepositoryID,
				RepositoryIDSource: gitMeta.RepositoryIDSource, RepositoryName: gitMeta.RepositoryName,
				RepoSubpath: gitMeta.RepoSubpath, RepoSubpathPresent: gitMeta.RepoSubpathPresent,
				CommitSHA: gitMeta.CommitSHA, Branch: gitMeta.Branch,
			}, restoreRecords)
			if err != nil {
				return 0, changed, err
			}
		}
	}
	if len(storeRecords) == 0 {
		return updated, changed, nil
	}
	n, err := s.client.SendReenrich(ctx, syncer.SyncPayload{
		Agent:              "codex",
		ProfileEmail:       s.profileEmail,
		UserID:             s.userID,
		ProjectHash:        projectHash,
		ProjectName:        projectName,
		GitRemoteURL:       gitMeta.GitRemoteURL,
		RepositoryID:       gitMeta.RepositoryID,
		RepositoryIDSource: gitMeta.RepositoryIDSource,
		RepositoryName:     gitMeta.RepositoryName,
		RepoSubpath:        gitMeta.RepoSubpath,
		RepoSubpathPresent: gitMeta.RepoSubpathPresent,
		CommitSHA:          gitMeta.CommitSHA,
		Branch:             gitMeta.Branch,
		Records:            storeRecords,
	})
	if err != nil {
		return updated, changed, err
	}
	return updated + n, changed, nil
}
