package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func refreshOverviewRollupsForTest(t *testing.T, s *PgStore, sessionIDs []string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rollup refresh: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := refreshSessionOverviewRollups(ctx, tx, sessionIDs); err != nil {
		t.Fatalf("refresh rollups: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit rollup refresh: %v", err)
	}
}

func insertOverviewSourceEvent(t *testing.T, tx pgx.Tx, ts time.Time, sessionID string) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), `INSERT INTO otel_events (ts, event_name, session_id)
		VALUES ($1, 'api_request', $2)`, ts, sessionID); err != nil {
		t.Fatalf("insert source event %q: %v", sessionID, err)
	}
}

func rollupEventCount(t *testing.T, s *PgStore, sessionID string) int64 {
	t.Helper()
	var count int64
	if err := s.pool.QueryRow(context.Background(), `SELECT event_count
		FROM session_overview_rollups
		WHERE scope_type = 'all' AND scope_value = '' AND session_id = $1`, sessionID).Scan(&count); err != nil {
		t.Fatalf("read rollup %q: %v", sessionID, err)
	}
	return count
}

func TestSessionOverviewRollupRefreshScopedIsolation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertOverviewSourceEvent(t, tx, ts, "scoped-a")
	insertOverviewSourceEvent(t, tx, ts, "scoped-b")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{"scoped-a", "scoped-b"})

	tx, err = s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertOverviewSourceEvent(t, tx, ts.Add(time.Minute), "scoped-a")
	insertOverviewSourceEvent(t, tx, ts.Add(time.Minute), "scoped-b")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{"scoped-a"})

	if got := rollupEventCount(t, s, "scoped-a"); got != 2 {
		t.Fatalf("refreshed session event_count = %d, want 2", got)
	}
	if got := rollupEventCount(t, s, "scoped-b"); got != 1 {
		t.Fatalf("out-of-scope session event_count = %d, want unchanged 1", got)
	}
}

func TestSessionOverviewRollupRefreshFullRebuild(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events (ts, event_name, session_id)
		VALUES ($1, 'api_request', 'full-source')`, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups
		(scope_type, scope_value, session_id, start_time, end_time, event_count)
		VALUES ('all', '', 'stale-rollup', $1, $1, 99)`, ts); err != nil {
		t.Fatal(err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := rebuildAllSessionOverviewRollups(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var sourceRows, staleRows int
	if err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE session_id = 'full-source'),
		count(*) FILTER (WHERE session_id = 'stale-rollup')
		FROM session_overview_rollups WHERE scope_type = 'all'`).Scan(&sourceRows, &staleRows); err != nil {
		t.Fatal(err)
	}
	if sourceRows != 1 || staleRows != 0 {
		t.Fatalf("full rebuild rows: source=%d stale=%d, want 1 and 0", sourceRows, staleRows)
	}
}

func TestSessionOverviewRollupRefreshEmptySliceIsNoOp(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 26, 11, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups
		(scope_type, scope_value, session_id, start_time, end_time, event_count)
		VALUES ('all', '', 'empty-no-op', $1, $1, 7)`, ts); err != nil {
		t.Fatal(err)
	}

	refreshOverviewRollupsForTest(t, s, []string{})

	if got := rollupEventCount(t, s, "empty-no-op"); got != 7 {
		t.Fatalf("empty scoped refresh changed event_count to %d, want 7", got)
	}
}

func TestSessionOverviewRollupRefreshExcludesDeletedSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events (ts, event_name, session_id)
		VALUES ($1, 'api_request', 'deleted-refresh')`, ts); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records (ts, session_id, uuid, raw)
		VALUES ($1, 'deleted-refresh', 'deleted-record', '{}'::jsonb)`, ts); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{"deleted-refresh"})
	if got := rollupEventCount(t, s, "deleted-refresh"); got != 2 {
		t.Fatalf("pre-delete event_count = %d, want 2", got)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO deleted_sessions (session_id, deleted_by, reason)
		VALUES ('deleted-refresh', 'test', 'test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions (session_id) VALUES ('deleted-refresh')`); err != nil {
		t.Fatal(err)
	}

	refreshOverviewRollupsForTest(t, s, []string{"deleted-refresh"})

	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_overview_rollups
		WHERE session_id = 'deleted-refresh'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("deleted session retained %d scope rows, want 0", rows)
	}
}

func TestSessionOverviewRollupRefreshSQLShapes(t *testing.T) {
	if sessionOverviewRollupDeleteFullSQL == sessionOverviewRollupDeleteScopedSQL {
		t.Fatal("full and scoped deletes use the same SQL shape")
	}
	if strings.Contains(sessionOverviewRollupDeleteFullSQL, "$1") {
		t.Fatalf("full delete is parameterized: %q", sessionOverviewRollupDeleteFullSQL)
	}
	if got := strings.Count(sessionOverviewRollupDeleteScopedSQL, "session_id = ANY($1::text[])"); got != 1 {
		t.Fatalf("scoped delete has %d exact session predicates, want 1: %q", got, sessionOverviewRollupDeleteScopedSQL)
	}

	if sessionOverviewRollupInsertFullSQL == sessionOverviewRollupInsertScopedSQL {
		t.Fatal("full and scoped inserts use the same SQL shape")
	}
	if strings.Contains(sessionOverviewRollupInsertFullSQL, "$1") {
		t.Fatalf("full insert is parameterized: %q", sessionOverviewRollupInsertFullSQL)
	}
	for _, query := range []string{
		sessionOverviewRollupDeleteFullSQL,
		sessionOverviewRollupDeleteScopedSQL,
		sessionOverviewRollupInsertFullSQL,
		sessionOverviewRollupInsertScopedSQL,
	} {
		if strings.Contains(strings.ToUpper(query), "IS NULL OR") {
			t.Fatalf("rollup statement contains nullable OR plan predicate: %q", query)
		}
	}
	for _, predicate := range []string{
		"e.session_id = ANY($1::text[])",
		"sr.session_id = ANY($1::text[])",
	} {
		if got := strings.Count(sessionOverviewRollupInsertScopedSQL, predicate); got != 1 {
			t.Fatalf("scoped insert has %d %q predicates, want 1", got, predicate)
		}
		if strings.Contains(sessionOverviewRollupInsertFullSQL, predicate) {
			t.Fatalf("full insert contains scoped predicate %q", predicate)
		}
	}
	for _, query := range []string{sessionOverviewRollupInsertFullSQL, sessionOverviewRollupInsertScopedSQL} {
		if got := strings.Count(query, "('all'::text, ''::text)"); got != 2 {
			t.Fatalf("insert shape has %d all-scope arms, want 2", got)
		}
		if !strings.Contains(query, "NOT EXISTS (SELECT 1 FROM excluded_sessions xs WHERE xs.session_id = e.session_id)") {
			t.Fatal("insert shape lost the event deleted-session exclusion")
		}
		if !strings.Contains(query, "FROM visible_session_records sr") {
			t.Fatal("insert shape lost the filtered session-record source")
		}
	}
}
