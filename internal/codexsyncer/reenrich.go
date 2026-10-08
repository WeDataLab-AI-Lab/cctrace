package codexsyncer

import (
	"context"
	"fmt"
	"path/filepath"

	"cctrace/internal/codexlog"
	"cctrace/internal/gitctx"
	"cctrace/internal/projecthash"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// ReenrichOnce scans all Codex JSONL records from the beginning and asks the
// server to update enrichment columns on existing rows without changing offsets.
func (s *CodexSyncer) ReenrichOnce(ctx context.Context) (int, error) {
	files, err := s.findAllFiles()
	if err != nil {
		return 0, fmt.Errorf("find codex jsonl files: %w", err)
	}

	total := 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := s.reenrichFile(ctx, file)
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

func (s *CodexSyncer) reenrichFile(ctx context.Context, filePath string) (int, error) {
	sessionID := codexlog.SessionIDFromPath(filePath)
	records, _, meta, err := codexlog.ScanFileWithMetadataContext(ctx, filePath, 0, sessionID, codexlog.Metadata{})
	if err != nil {
		return 0, err
	}

	cwd, model := meta.CWD, meta.Model
	storeRecords := make([]*store.SessionRecord, 0, len(records))
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
		nudger.Apply(sr)
		storeRecords = append(storeRecords, sr)
	}
	if len(storeRecords) == 0 {
		return 0, nil
	}

	projectHash := projecthash.FromPath(cwd)
	if projectHash == "" {
		projectHash = projectHashFromPath(filePath)
	}
	projectName := projecthash.NameFromPath(cwd)
	gitMeta := s.freshGitMeta(cwd)
	if !gitctx.AllowsRepository(gitMeta.RepositoryID, s.collectPrefixes) {
		return 0, nil
	}

	updated, err := s.client.SendReenrich(ctx, syncer.SyncPayload{
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
		return 0, err
	}
	return updated, nil
}
