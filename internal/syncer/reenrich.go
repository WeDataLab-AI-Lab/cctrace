package syncer

import (
	"context"
	"fmt"
	"path/filepath"

	"cctrace/internal/gitctx"
	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
)

// ReenrichOnce scans all Claude JSONL records from the beginning and asks the
// server to update enrichment columns on existing rows without changing offsets.
func (s *Syncer) ReenrichOnce(ctx context.Context) (int, error) {
	files, err := sessionlog.FindJSONLFiles(s.claudeDir)
	if err != nil {
		return 0, fmt.Errorf("find jsonl files: %w", err)
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

func (s *Syncer) reenrichFile(ctx context.Context, filePath string) (int, error) {
	records, _, skipped, err := sessionlog.ScanFileWithSkips(ctx, filePath, 0)
	if err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}

	if len(s.collectPrefixes) > 0 && !s.reenrichAllowed(filePath, records, skipped) {
		return 0, nil
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
		return 0, nil
	}

	storeRecords := make([]*store.SessionRecord, 0, len(records))
	activeAttributionSkill := ""
	sourceFile := filepath.Base(filePath)
	toolUseID := subagentToolUseID(filePath)
	for _, r := range records {
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
		storeRecords = append(storeRecords, sr)
	}
	if len(storeRecords) == 0 {
		return 0, nil
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
			return updated, fmt.Errorf("send reenrich batch: %w", err)
		}
		updated += n
	}
	return updated, nil
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
