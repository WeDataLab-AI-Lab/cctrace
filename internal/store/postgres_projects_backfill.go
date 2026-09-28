package store

import (
	"context"

	"cctrace/internal/gitctx"
)

const repositoryNameDerivedBackfill = "projects_repository_name_derived_v1"

// BackfillDerivedRepositoryNames rewrites repository_name from repository_id for
// every row whose id names a real remote.
//
// The two were stored side by side and could disagree. The client fills the name
// from the checkout directory before it resolves the remote (gitctx.go:58, then
// :68 corrects it), so whichever write landed first stuck -- and on a production
// copy 17 repositories carried two names each, one of them a worktree's own
// directory. The read paths pick with MAX(), so a repository could display under a
// worktree codename (#691).
//
// Writes are now derived (UpsertProjectWithMetadata), which leaves the rows already
// on disk. This is that one-time correction.
//
// Only rows with an authoritative id are touched. A `local:` fallback has no remote
// to derive from and its stored name is the best answer available, so it is left
// exactly as it is -- including the anonymous `local:<hash>` form, which derives to
// the empty string and would otherwise be wiped.
func (s *PgStore) BackfillDerivedRepositoryNames(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM schema_backfills WHERE name = $1)`, repositoryNameDerivedBackfill).Scan(&done); err != nil {
		return err
	}
	if done {
		return tx.Commit(ctx)
	}

	// Read the candidates and derive in Go rather than mirroring the rule in SQL.
	// A SQL twin of RepositoryNameFromID is exactly the shape of the defect being
	// corrected: one rule, two implementations, free to drift.
	rows, err := tx.Query(ctx, `SELECT agent, project_hash, repository_id, repository_name
		FROM projects
		WHERE BTRIM(COALESCE(repository_id, '')) <> ''
		  AND LOWER(BTRIM(repository_id)) NOT LIKE 'local:%'`)
	if err != nil {
		return err
	}
	type fix struct{ agent, hash, name string }
	var fixes []fix
	for rows.Next() {
		var agent, hash, id, stored string
		if err := rows.Scan(&agent, &hash, &id, &stored); err != nil {
			rows.Close()
			return err
		}
		derived := gitctx.RepositoryNameFromID(id)
		if derived != "" && derived != stored {
			fixes = append(fixes, fix{agent, hash, derived})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, f := range fixes {
		if _, err := tx.Exec(ctx,
			`UPDATE projects SET repository_name = $3, updated_at = now()
			 WHERE agent = $1 AND project_hash = $2`, f.agent, f.hash, f.name); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, repositoryNameDerivedBackfill); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
