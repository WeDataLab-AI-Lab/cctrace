package store

import (
	"context"
	"testing"
	"time"
)

// The name is derivable from the id, and storing it alongside let the two
// disagree: the client fills it from the checkout directory before it resolves
// the remote, so a worktree could leave its own codename in the repository's name
// (#691).

func TestUpsertProject_DerivesTheNameFromAnAuthoritativeID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	// The caller insists on the worktree directory. A resolved id makes that name
	// a derived value, so the caller does not get to be wrong about it.
	if err := s.UpsertProject(ctx, "claude", "h-worktree", "seaslug", "",
		"example.com/org/engine", "seaslug", "", now); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	var stored string
	if err := s.pool.QueryRow(ctx,
		`SELECT repository_name FROM projects WHERE agent='claude' AND project_hash='h-worktree'`).
		Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "engine" {
		t.Fatalf("repository_name = %q, want engine", stored)
	}
}

func TestUpsertProject_LeavesALocalFallbackNameAlone(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	// No remote to derive from. The id carries a name of its own, so deriving
	// would rewrite the stored one -- but a local fallback has no authority to do
	// that, and the checkout directory is the best answer there is.
	if err := s.UpsertProject(ctx, "claude", "h-local", "scratch", "",
		"local:engine:0f1e2d3c", "scratch", "", now); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}

	var stored string
	if err := s.pool.QueryRow(ctx,
		`SELECT repository_name FROM projects WHERE agent='claude' AND project_hash='h-local'`).
		Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "scratch" {
		t.Fatalf("repository_name = %q, want scratch left as stored", stored)
	}
}

func TestBackfillDerivedRepositoryNames_CorrectsRowsAlreadyOnDisk(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 14, 0, 0, 0, time.UTC)

	// Written the way the pre-fix rows were: a real id beside a directory name.
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO projects (agent, project_hash, project_name, repository_id, repository_name, last_session_at, updated_at)
		 VALUES ('claude','h-stale','seaslug','example.com/org/engine','seaslug',$1, now()),
		        ('claude','h-local','scratch','local:engine:0f1e2d3c','scratch',$1, now())`, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.BackfillDerivedRepositoryNames(ctx); err != nil {
		t.Fatalf("BackfillDerivedRepositoryNames: %v", err)
	}

	var corrected, local string
	if err := s.pool.QueryRow(ctx,
		`SELECT (SELECT repository_name FROM projects WHERE project_hash='h-stale'),
		        (SELECT repository_name FROM projects WHERE project_hash='h-local')`).
		Scan(&corrected, &local); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if corrected != "engine" {
		t.Fatalf("stale row = %q, want engine", corrected)
	}
	// The local fallback is not the backfill's business.
	if local != "scratch" {
		t.Fatalf("local row = %q, want scratch untouched", local)
	}

	// One-time: the marker stops it, so a later hand edit is not undone on the
	// next restart.
	if _, err := s.pool.Exec(ctx,
		`UPDATE projects SET repository_name = 'renamed' WHERE project_hash='h-stale'`); err != nil {
		t.Fatalf("hand edit: %v", err)
	}
	if err := s.BackfillDerivedRepositoryNames(ctx); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT repository_name FROM projects WHERE project_hash='h-stale'`).Scan(&corrected); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if corrected != "renamed" {
		t.Fatalf("second run rewrote a row: %q", corrected)
	}
}
