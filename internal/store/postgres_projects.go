package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cctrace/internal/gitctx"
)

func (s *PgStore) UpsertProject(ctx context.Context, agent, projectHash, projectName, gitRemoteURL, repositoryID, repositoryName, repoSubpath string, lastSessionAt time.Time) error {
	return s.UpsertProjectWithMetadata(ctx, agent, projectHash, projectName, ProjectIdentityMetadata{
		GitRemoteURL:   gitRemoteURL,
		RepositoryID:   repositoryID,
		RepositoryName: repositoryName,
		RepoSubpath:    repoSubpath,
	}, lastSessionAt)
}

// UpsertProjectWithMetadata stores project identity while respecting the
// authority carried by the sync envelope. Remote Git metadata can replace a
// stale local value, but a deleted/unavailable worktree's local fallback cannot
// replace metadata already established from a remote. RepoSubpathPresent is
// what makes an empty resolved root distinguishable from a legacy omission.
func (s *PgStore) UpsertProjectWithMetadata(ctx context.Context, agent, projectHash, projectName string, metadata ProjectIdentityMetadata, lastSessionAt time.Time) error {
	if projectHash == "" {
		return nil
	}
	if agent == "" {
		agent = "claude"
	}
	// Defense in depth: strip any credentials before storing, so URLs sent by
	// older clients that predate client-side sanitization don't leak secrets.
	metadata.GitRemoteURL = gitctx.SanitizeRemoteURL(metadata.GitRemoteURL)
	// Only a legacy or resolved envelope may derive an ID from a URL. A
	// fallback envelope is deliberately low confidence even if a caller has
	// supplied inconsistent extra fields alongside it.
	source := strings.ToLower(strings.TrimSpace(metadata.RepositoryIDSource))
	if metadata.RepositoryID == "" && metadata.GitRemoteURL != "" && (source == "" || source == RepositoryIDSourceResolved) {
		metadata.RepositoryID = gitctx.NormalizeRemoteURL(metadata.GitRemoteURL)
	}
	authoritative := projectIdentityRemoteAuthority(metadata)
	// A resolved remote id makes the name a derived value, so it is derived here
	// rather than believed. A caller's name for such an id can only be wrong, and
	// on prod 17 repositories ended up carrying two names each because the client
	// sends the worktree's own directory before it resolves the remote
	// (gitctx.go:58 then :68) and whichever write landed first stuck (#691).
	//
	// Only for authoritative ids. A local: fallback has no remote to derive from,
	// and its name -- the checkout directory -- is the best available answer.
	if authoritative {
		if derived := gitctx.RepositoryNameFromID(metadata.RepositoryID); derived != "" {
			metadata.RepositoryName = derived
		}
	} else if metadata.RepositoryName == "" {
		metadata.RepositoryName = gitctx.RepositoryNameFromID(metadata.RepositoryID)
	}
	clearSubpath, fillSubpath := projectIdentitySubpathFlags(metadata)
	// lastSessionAt is nil (not now()) when zero: NULL is a no-op to GREATEST, so an
	// empty-records/reenrich call preserves the existing value instead of clobbering it.
	var lastSessionAtParam *time.Time
	if !lastSessionAt.IsZero() {
		lastSessionAtParam = &lastSessionAt
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO projects (agent, project_hash, project_name, git_remote_url, repository_id, repository_name, repo_subpath, last_session_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		 ON CONFLICT (agent, project_hash) DO UPDATE SET
		   project_name = CASE WHEN EXCLUDED.project_name != '' AND ($9 OR projects.project_name = '') THEN EXCLUDED.project_name ELSE projects.project_name END,
		   git_remote_url = CASE WHEN EXCLUDED.git_remote_url != '' AND ($9 OR projects.git_remote_url = '') THEN EXCLUDED.git_remote_url ELSE projects.git_remote_url END,
		   repository_id = CASE WHEN EXCLUDED.repository_id != '' AND ($9 OR projects.repository_id = '') THEN EXCLUDED.repository_id ELSE projects.repository_id END,
		   repository_name = CASE WHEN EXCLUDED.repository_name != '' AND ($9 OR projects.repository_name = '') THEN EXCLUDED.repository_name ELSE projects.repository_name END,
		   repo_subpath = CASE
		     WHEN $10 THEN EXCLUDED.repo_subpath
		     WHEN $11 AND EXCLUDED.repo_subpath != '' AND projects.repo_subpath = '' THEN EXCLUDED.repo_subpath
		     ELSE projects.repo_subpath
		   END,
		   last_session_at = GREATEST(projects.last_session_at, EXCLUDED.last_session_at),
		   updated_at = now()`,
		agent, projectHash, projectName, metadata.GitRemoteURL, metadata.RepositoryID, metadata.RepositoryName, metadata.RepoSubpath, lastSessionAtParam,
		authoritative, clearSubpath, fillSubpath,
	)
	return err
}

// ListProjects returns the projects that still have at least one session matching f.
//
// The subquery requires a non-empty session_id as well as a project_hash. Metadata rows
// carry a project_hash under an EMPTY session_id, and tombstones are keyed on
// session_id, so those rows survive every delete. Without this condition a project
// whose sessions were all deleted stayed in the picker forever, and choosing it
// opened a header reading "0 sessions" over an empty list -- the session list
// already counted with this condition, so the two disagreed.
//
// The empty-session_id rows are legitimate data, not residue, so they are filtered
// here rather than deleted: on prod otel_metrics held 3.06M rows under an empty
// session id, belonging to every user.
func (s *PgStore) ListProjects(ctx context.Context, f ProjectFilter) ([]*Project, error) {
	var args []interface{}
	n := 0
	// The account scope stays a row-level predicate, and the session filters below can
	// only narrow what it leaves: a restricted user must not gain sessions by picking a
	// source or an agent. The order of the three keys mirrors the else-if chain in
	// CountSessionOverviews, so the picker and the list agree on which one wins.
	recordConds := []string{"session_id != ''"}
	switch {
	case f.UserID != "":
		n++
		recordConds = append(recordConds, fmt.Sprintf("user_id = $%d", n))
		args = append(args, f.UserID)
	case f.ProfileEmail != "":
		n++
		recordConds = append(recordConds, fmt.Sprintf("profile_email = $%d", n))
		args = append(args, f.ProfileEmail)
	case f.LoginEmail != "":
		// Row-level, exactly as the session list applies it to its own
		// session_records arm: a session counts as the account's if any of its
		// records carries that login_email.
		n++
		recordConds = append(recordConds, fmt.Sprintf("login_email = $%d", n))
		args = append(args, f.LoginEmail)
	}
	// sessionOverviewFilters/appendAgentFilter are reused rather than restated: source
	// is a per-session classification, so a copied predicate would drift the day one
	// side is edited and put the picker back out of step with the list.
	sessionConds := sessionOverviewFilters(f.Source)
	if f.FoldLineage {
		// assembled=1 folds a branch/clear child into its root, so the child is not a
		// row of its own -- same predicate the session list uses.
		sessionConds = append(sessionConds, "NOT has_fork")
	}
	if clause := appendAgentFilter(f.Agent, &n, &args); clause != "" {
		sessionConds = append(sessionConds, clause)
	}
	recordWhere := strings.Join(recordConds, " AND ")
	// With no session-level predicate the aggregates cannot change the answer, so the
	// cheap DISTINCT stays the query for every unfiltered caller (the OpenAPI resource,
	// the rules page) rather than paying for a full GROUP BY over every record.
	sub := "SELECT DISTINCT project_hash FROM visible_session_records WHERE project_hash != '' AND " + recordWhere
	if len(sessionConds) > 0 {
		// Grouped by session_id because the classification is per session, not per
		// record: entrypoint and the enriched/genuine/fork flags only mean anything
		// once the whole session is folded, exactly as the session list folds it.
		// project_hash is NOT filtered per record here -- dropping the records that
		// carry no project_hash would fold a different session than the list does.
		// The outer IN discards the empty hash on its own, since no project has one.
		sub = fmt.Sprintf(`SELECT project_hash FROM (
				SELECT COALESCE(max(project_hash) FILTER (WHERE project_hash <> ''), '') AS project_hash,
					bool_or(COALESCE(source_file,'') <> '') AS has_enriched,
					bool_or(COALESCE(prompt_source,'') IN ('typed','paste','queued','suggestion_accepted')) AS has_genuine,
					bool_or(COALESCE(forked_from_session,'') <> '') AS has_fork,
					COALESCE(max(entrypoint) FILTER (WHERE entrypoint <> ''), '') AS entrypoint,
					COALESCE(max(agent), 'claude') AS agent
				FROM visible_session_records WHERE %s GROUP BY session_id
			) g WHERE %s`, recordWhere, strings.Join(sessionConds, " AND "))
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(
		`SELECT project_hash, project_name, git_remote_url, repository_id, repository_name, repo_subpath, last_session_at, updated_at FROM projects
		WHERE project_hash IN (%s)
		ORDER BY last_session_at DESC NULLS LAST, updated_at DESC`, sub), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*Project, 0)
	for rows.Next() {
		p := &Project{}
		if err := rows.Scan(&p.ProjectHash, &p.ProjectName, &p.GitRemoteURL, &p.RepositoryID, &p.RepositoryName, &p.RepoSubpath, &p.LastSessionAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
