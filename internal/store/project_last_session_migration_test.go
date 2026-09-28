package store

import (
	"context"
	"testing"
	"time"
)

func TestMigration_ProjectLastSessionAt(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'projects' AND column_name = 'last_session_at'
		)`,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("query projects.last_session_at: %v", err)
	}
	if !exists {
		t.Fatalf("column projects.last_session_at does not exist")
	}
}

func TestUpsertProject_LastSessionAt_Monotonic(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	agent := "claude"
	projectHash := "proj-monotonic"

	later := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := s.UpsertProject(ctx, agent, projectHash, "proj", "", "", "", "", later); err != nil {
		t.Fatalf("upsert (later): %v", err)
	}
	if err := s.UpsertProject(ctx, agent, projectHash, "proj", "", "", "", "", earlier); err != nil {
		t.Fatalf("upsert (earlier): %v", err)
	}

	var lastSessionAt time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT last_session_at FROM projects WHERE agent = $1 AND project_hash = $2`,
		agent, projectHash).Scan(&lastSessionAt); err != nil {
		t.Fatalf("query last_session_at: %v", err)
	}
	if !lastSessionAt.Equal(later) {
		t.Fatalf("last_session_at regressed: got %v, want %v (monotonic max)", lastSessionAt, later)
	}
}

func TestUpsertProject_LastSessionAt_ZeroPreservesExisting(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	agent := "claude"
	projectHash := "proj-zero"
	known := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	if err := s.UpsertProject(ctx, agent, projectHash, "proj", "", "", "", "", known); err != nil {
		t.Fatalf("upsert (known): %v", err)
	}
	if err := s.UpsertProject(ctx, agent, projectHash, "proj", "", "", "", "", time.Time{}); err != nil {
		t.Fatalf("upsert (zero): %v", err)
	}

	var lastSessionAt time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT last_session_at FROM projects WHERE agent = $1 AND project_hash = $2`,
		agent, projectHash).Scan(&lastSessionAt); err != nil {
		t.Fatalf("query last_session_at: %v", err)
	}
	if !lastSessionAt.Equal(known) {
		t.Fatalf("last_session_at was clobbered by zero value: got %v, want %v", lastSessionAt, known)
	}
}

// TestListProjects_OrdersByLastSessionAt verifies the fix this issue exists for:
// the project list is ordered by last activity, not by when the syncer last
// touched the row, and projects with no session history (NULL) sort last.
func TestListProjects_OrdersByLastSessionAt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	recent := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := s.UpsertProject(ctx, "claude", "proj-recent", "recent", "", "", "", "", recent); err != nil {
		t.Fatalf("upsert proj-recent: %v", err)
	}
	if err := s.UpsertProject(ctx, "claude", "proj-old", "old", "", "", "", "", old); err != nil {
		t.Fatalf("upsert proj-old: %v", err)
	}
	if err := s.UpsertProject(ctx, "claude", "proj-null", "no-history", "", "", "", "", time.Time{}); err != nil {
		t.Fatalf("upsert proj-null: %v", err)
	}

	// ListProjects only returns projects with a row in visible_session_records, so
	// each project needs a seeded session record to be visible at all.
	for _, hash := range []string{"proj-recent", "proj-old", "proj-null"} {
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
			Ts:          time.Now().UTC(),
			SessionID:   "seed-" + hash,
			RecordType:  "user",
			Agent:       "claude",
			ProjectHash: hash,
		}}); err != nil {
			t.Fatalf("seed session record for %s: %v", hash, err)
		}
	}

	projects, err := s.ListProjects(ctx, ProjectFilter{})
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	var order []string
	for _, p := range projects {
		order = append(order, p.ProjectHash)
	}
	want := []string{"proj-recent", "proj-old", "proj-null"}
	if len(order) != len(want) {
		t.Fatalf("unexpected project set: got %v, want %v", order, want)
	}
	for i, hash := range want {
		if order[i] != hash {
			t.Fatalf("wrong order: got %v, want %v (NULL last_session_at must sort last)", order, want)
		}
	}
	// The NULL row must carry a nil LastSessionAt, not a zero time, so the
	// frontend can distinguish "no history" from an actual timestamp.
	if projects[2].LastSessionAt != nil {
		t.Fatalf("proj-null: expected nil LastSessionAt, got %v", projects[2].LastSessionAt)
	}
}

// TestBackfillGuard_StatementLevel_SkipsRerun exercises the actual migration
// statement (via s.Migrate, not a reimplementation of it) to prove the backfill
// guard is statement-level, not row-level. A row-level "WHERE last_session_at IS
// NULL" guard on the UPDATE only filters which rows get written — it does not skip
// building the max(ts)-per-project_hash subquery, so a project that legitimately has
// no session history (and so stays NULL forever) would cause the backfill to rescan
// session_records on every single migration run, forever. The fix wraps the whole
// UPDATE in "IF NOT EXISTS (SELECT 1 FROM projects WHERE last_session_at IS NOT
// NULL)", which only needs one already-backfilled project anywhere in the table to
// skip the statement entirely on the next run.
func TestBackfillGuard_StatementLevel_SkipsRerun(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// proj-a already has last_session_at set (as if backfilled by a prior run) —
	// this alone should trip the statement-level guard for all future runs.
	if err := s.UpsertProject(ctx, "claude", "proj-a", "proj-a", "", "", "", "", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("upsert proj-a: %v", err)
	}

	// proj-b has session history but its last_session_at is still NULL (e.g. the
	// column was reset, or it was inserted after the one-time backfill ran). A
	// row-level guard would happily backfill it on the next migration pass; the
	// statement-level guard must not, because the statement runs once, ever.
	projB := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO projects (agent, project_hash, project_name, updated_at) VALUES ($1, $2, $3, now())`,
		"claude", "proj-b", "proj-b"); err != nil {
		t.Fatalf("insert proj-b: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts:          projB,
		SessionID:   "seed-proj-b",
		RecordType:  "user",
		Agent:       "claude",
		ProjectHash: "proj-b",
	}}); err != nil {
		t.Fatalf("seed session record for proj-b: %v", err)
	}

	var before *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT last_session_at FROM projects WHERE project_hash = 'proj-b'`).Scan(&before); err != nil {
		t.Fatalf("query proj-b before rerun: %v", err)
	}
	if before != nil {
		t.Fatalf("proj-b.last_session_at should start NULL, got %v", before)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("re-run Migrate: %v", err)
	}

	var after *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT last_session_at FROM projects WHERE project_hash = 'proj-b'`).Scan(&after); err != nil {
		t.Fatalf("query proj-b after rerun: %v", err)
	}
	if after != nil {
		t.Fatalf("proj-b.last_session_at was backfilled on a second Migrate() run (got %v) — the guard is not statement-level", *after)
	}
}
