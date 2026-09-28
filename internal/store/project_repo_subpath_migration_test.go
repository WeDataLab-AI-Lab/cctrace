package store

import (
	"context"
	"testing"
	"time"
)

func TestMigration_ProjectRepoSubpath(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'projects' AND column_name = 'repo_subpath'
		)`,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("query projects.repo_subpath: %v", err)
	}
	if !exists {
		t.Fatalf("column projects.repo_subpath does not exist")
	}
}

func TestUpsertProject_StoresRepoSubpath(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if err := s.UpsertProject(ctx, "claude", "proj-subpath", "cctrace", "", "gh:org/repo", "repo", "internal/store/", time.Time{}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var repoSubpath string
	if err := s.pool.QueryRow(ctx,
		`SELECT repo_subpath FROM projects WHERE agent = 'claude' AND project_hash = 'proj-subpath'`,
	).Scan(&repoSubpath); err != nil {
		t.Fatalf("query repo_subpath: %v", err)
	}
	if repoSubpath != "internal/store/" {
		t.Fatalf("repo_subpath: got %q, want %q", repoSubpath, "internal/store/")
	}
}

// TestListProjects_IdentityKey_MergesWorktrees is the backend half of the merge this
// issue exists for: two projects sharing repository_id with an empty repo_subpath
// (a worktree of the main checkout) both return repo_subpath = "" from ListProjects
// so the frontend's projectIdentityKey (repository_id + repo_subpath) collapses them
// into one identity. A monorepo subdirectory keeps a non-empty repo_subpath and so
// stays a distinct identity.
func TestListProjects_IdentityKey_MergesWorktrees(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	cases := []struct {
		hash       string
		subpath    string
		repoID     string
		wantMerged bool
	}{
		{"main-checkout", "", "gh:org/repo", true},
		{"worktree", "", "gh:org/repo", true},
		{"monorepo-subdir", "internal/store/", "gh:org/repo", false},
	}
	for _, c := range cases {
		if err := s.UpsertProject(ctx, "claude", c.hash, c.hash, "", c.repoID, "repo", c.subpath, now); err != nil {
			t.Fatalf("upsert %s: %v", c.hash, err)
		}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
			Ts: now, SessionID: "seed-" + c.hash, RecordType: "user", Agent: "claude", ProjectHash: c.hash,
		}}); err != nil {
			t.Fatalf("seed session record for %s: %v", c.hash, err)
		}
	}

	projects, err := s.ListProjects(ctx, ProjectFilter{})
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	byHash := map[string]*Project{}
	for _, p := range projects {
		byHash[p.ProjectHash] = p
	}
	if byHash["main-checkout"].RepoSubpath != "" || byHash["worktree"].RepoSubpath != "" {
		t.Fatalf("main-checkout and worktree must both report empty repo_subpath so they merge: got %q, %q",
			byHash["main-checkout"].RepoSubpath, byHash["worktree"].RepoSubpath)
	}
	if byHash["monorepo-subdir"].RepoSubpath != "internal/store/" {
		t.Fatalf("monorepo-subdir must keep its repo_subpath so it stays separate: got %q", byHash["monorepo-subdir"].RepoSubpath)
	}
}

// TestBackfillGuard_RepoSubpath_ScopedToColumnCreation exercises the actual migration
// (via s.Migrate) to prove the repo_subpath backfill only runs when the column is
// first added. Unlike last_session_at, the empty-string default is also a legitimate
// post-backfill result (a worktree root's own prefix is empty), so there is no
// "not yet backfilled" row-level sentinel to gate a standalone UPDATE on — the
// backfill has to live inside the ADD COLUMN branch and be skipped by the
// duplicate_column exception on every later boot.
func TestBackfillGuard_RepoSubpath_ScopedToColumnCreation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// proj-late has session history with a non-empty repo_subpath, but its
	// projects row was created after the one-time backfill (column already
	// exists), so it must NOT be swept up by a later Migrate() run.
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO projects (agent, project_hash, project_name, updated_at) VALUES ($1, $2, $3, now())`,
		"claude", "proj-late", "proj-late"); err != nil {
		t.Fatalf("insert proj-late: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: time.Now().UTC(), SessionID: "seed-proj-late", RecordType: "user", Agent: "claude",
		ProjectHash: "proj-late", RepoSubpath: "deliverable/analyzer/",
	}}); err != nil {
		t.Fatalf("seed session record for proj-late: %v", err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("re-run Migrate: %v", err)
	}

	var repoSubpath string
	if err := s.pool.QueryRow(ctx, `SELECT repo_subpath FROM projects WHERE project_hash = 'proj-late'`).Scan(&repoSubpath); err != nil {
		t.Fatalf("query proj-late repo_subpath: %v", err)
	}
	if repoSubpath != "" {
		t.Fatalf("proj-late.repo_subpath was backfilled on a later Migrate() run (got %q) — backfill is not scoped to column creation", repoSubpath)
	}
}
