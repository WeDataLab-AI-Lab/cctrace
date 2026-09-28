package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestPgStore_SessionOverviewRollup_matchesLiveAggregation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	one, two := 100, 20

	if _, err := s.pool.Exec(ctx, `INSERT INTO projects (agent, project_hash, project_name, updated_at)
		VALUES ('claude', 'project-a', 'Project A', now()), ('codex', 'project-b', 'Project B', now())`); err != nil {
		t.Fatalf("insert projects: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "claude-main", UserID: "user-1", ProfileEmail: "profile-1@example.com", LoginEmail: "login-1@example.com", Model: "sonnet", Agent: "claude", InputTokens: &one, OutputTokens: &two},
		{Ts: ts.Add(time.Minute), EventName: "api_request", SessionID: "branch", UserID: "user-1", ProfileEmail: "profile-1@example.com", LoginEmail: "login-1@example.com", Agent: "claude"},
		{Ts: ts.Add(2 * time.Minute), EventName: "api_request", SessionID: "shell-other", UserID: "user-2", ProfileEmail: "profile-2@example.com", LoginEmail: "login-2@example.com", Agent: "gjc"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "claude-main", UserID: "user-1", ProfileEmail: "profile-1@example.com", LoginEmail: "login-1@example.com", ProjectHash: "project-a", Agent: "claude", UUID: "c1", SourceFile: "session.jsonl", PromptSource: "typed", CctraceVersion: "1.2.3", Raw: []byte(`{}`)},
		{Ts: ts.Add(time.Minute), SessionID: "branch", UserID: "user-1", ProfileEmail: "profile-1@example.com", LoginEmail: "login-1@example.com", ProjectHash: "project-a", Agent: "claude", UUID: "b1", ForkedFromSession: "claude-main", Raw: []byte(`{}`)},
		{Ts: ts.Add(2 * time.Minute), SessionID: "codex-main", UserID: "user-2", ProfileEmail: "profile-2@example.com", ProjectHash: "project-b", Agent: "codex", AccountID: "codex-account", Entrypoint: "cli", UUID: "x1", Raw: []byte(`{}`)},
		{Ts: ts.Add(3 * time.Minute), SessionID: "unattributed", ProfileEmail: "profile-3@example.com", Agent: "claude", UUID: "u1", Raw: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	since, until := ts.Add(-time.Hour), ts.Add(time.Hour)
	filters := []SessionOverviewFilter{
		{Limit: 100},
		{UserID: "user-1", Limit: 100},
		{ProfileEmail: "profile-1@example.com", Limit: 100},
		{LoginEmail: "login-1@example.com", Limit: 100},
		{ProjectHashes: []string{"project-a"}, Limit: 100},
		{Agent: "other", Limit: 100},
		{Source: "interactive", Limit: 100},
		{FoldLineage: true, Limit: 100},
		{OnlyUnattributed: true, LoginEmail: "login-1@example.com", Limit: 100},
	}
	for _, fastFilter := range filters {
		liveFilter := fastFilter
		liveFilter.Since, liveFilter.Until = &since, &until
		fast, err := s.ListSessionOverviews(ctx, fastFilter)
		if err != nil {
			t.Fatalf("fast list %+v: %v", fastFilter, err)
		}
		live, err := s.ListSessionOverviews(ctx, liveFilter)
		if err != nil {
			t.Fatalf("live list %+v: %v", fastFilter, err)
		}
		if !reflect.DeepEqual(fast, live) {
			t.Errorf("filter %+v: fast list = %#v, live list = %#v", fastFilter, fast, live)
		}
		fastCount, err := s.CountSessionOverviews(ctx, fastFilter)
		if err != nil {
			t.Fatalf("fast count %+v: %v", fastFilter, err)
		}
		liveCount, err := s.CountSessionOverviews(ctx, liveFilter)
		if err != nil {
			t.Fatalf("live count %+v: %v", fastFilter, err)
		}
		if fastCount != liveCount {
			t.Errorf("filter %+v: fast count = %d, live count = %d", fastFilter, fastCount, liveCount)
		}
	}
}

func TestPgStore_SessionOverviewRollup_hasReadYourWrite(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	in, out := 40, 2

	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: ts, EventName: "api_request", SessionID: "fresh", LoginEmail: "fresh@example.com", InputTokens: &in, OutputTokens: &out}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	got, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: "fresh@example.com"})
	if err != nil || len(got) != 1 || got[0].InputTokens != 40 || got[0].HasSync {
		t.Fatalf("after event write: got %+v, err %v", got, err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: ts.Add(time.Minute), SessionID: "fresh", LoginEmail: "fresh@example.com", ProjectHash: "fresh-project", UUID: "r1", Raw: []byte(`{}`)}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	got, err = s.ListSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: "fresh@example.com"})
	if err != nil || len(got) != 1 || !got[0].HasSync || got[0].ProjectHash != "fresh-project" || !got[0].EndTime.Equal(ts.Add(time.Minute)) {
		t.Fatalf("after session-record write: got %+v, err %v", got, err)
	}
}

func BenchmarkPgStore_SessionOverviewRollupReads(b *testing.B) {
	// Benchmarks run after tests in the same package process. Keeping setup here avoids
	// timing container startup or write maintenance; this measures only the read path.
	if pgShared == nil {
		b.Skip("run with at least one store integration test so the shared database exists")
	}
	ctx := context.Background()
	if _, err := pgShared.pool.Exec(ctx, `TRUNCATE session_overview_rollups`); err != nil {
		b.Fatal(err)
	}
	if _, err := pgShared.pool.Exec(ctx, `INSERT INTO session_overview_rollups
		(scope_type, scope_value, session_id, start_time, end_time, has_api_request)
		SELECT 'all', '', 'benchmark-' || n, now() - n * interval '1 second',
			now() - n * interval '1 second', true
		FROM generate_series(1, 100000) n`); err != nil {
		b.Fatal(err)
	}

	b.Run("list-200-of-100k", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := pgShared.ListSessionOverviews(ctx, SessionOverviewFilter{Limit: 200}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("count-100k", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := pgShared.CountSessionOverviews(ctx, SessionOverviewFilter{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func TestPgStore_Migrate_doesNotWaitForSessionOverviewBackfill(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, sessionOverviewRollupBackfill); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events (ts, event_name, session_id) VALUES (now(), 'api_request', 'startup-history')`); err != nil {
		t.Fatalf("insert history: %v", err)
	}

	// Hold the lock needed only by the full rollup rebuild. Schema migration does
	// not need it, so Migrate must still finish while this transaction is open.
	blocker, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	defer blocker.Rollback(ctx) //nolint:errcheck
	if err := lockSessionOverviewMaintenance(ctx, blocker, false); err != nil {
		t.Fatalf("hold maintenance lock: %v", err)
	}
	migrateCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.Migrate(migrateCtx); err != nil {
		t.Fatalf("Migrate waited for the blocked rollup backfill: %v", err)
	}

	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release maintenance lock: %v", err)
	}
	if err := s.BackfillSessionOverviewRollups(ctx); err != nil {
		t.Fatalf("background backfill: %v", err)
	}
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_overview_rollups WHERE scope_type = 'all' AND session_id = 'startup-history'`).Scan(&rows); err != nil {
		t.Fatalf("query completed rollup: %v", err)
	}
	if rows != 1 {
		t.Fatalf("completed rollup rows = %d, want 1", rows)
	}
}

func TestPgStore_SessionOverviewRollup_readsLiveUntilBackfillCompletes(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, sessionOverviewRollupBackfill); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events (ts, event_name, session_id, login_email) VALUES (now(), 'api_request', 'pre-backfill', 'old@example.com')`); err != nil {
		t.Fatalf("insert history: %v", err)
	}

	got, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: "old@example.com"})
	if err != nil || len(got) != 1 || got[0].SessionID != "pre-backfill" {
		t.Fatalf("pre-backfill live read = %+v, err %v", got, err)
	}
	if err := s.BackfillSessionOverviewRollups(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	got, err = s.ListSessionOverviews(ctx, SessionOverviewFilter{LoginEmail: "old@example.com"})
	if err != nil || len(got) != 1 || got[0].SessionID != "pre-backfill" {
		t.Fatalf("post-backfill rollup read = %+v, err %v", got, err)
	}
}

func TestPgStore_SessionOverviewRollup_backfillsExactlyOnce(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, sessionOverviewRollupBackfill); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events (ts, event_name, session_id, login_email) VALUES ($1, 'api_request', 'historical', 'old@example.com')`, ts); err != nil {
		t.Fatalf("insert historical event: %v", err)
	}
	if err := s.BackfillSessionOverviewRollups(ctx); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events (ts, event_name, session_id, login_email) VALUES ($1, 'api_request', 'late-direct', 'old@example.com')`, ts.Add(time.Minute)); err != nil {
		t.Fatalf("insert late direct event: %v", err)
	}
	if err := s.BackfillSessionOverviewRollups(ctx); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	var historical, late int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE session_id = 'historical'), count(*) FILTER (WHERE session_id = 'late-direct') FROM session_overview_rollups WHERE scope_type = 'all'`).Scan(&historical, &late); err != nil {
		t.Fatalf("query rollups: %v", err)
	}
	if historical != 1 || late != 0 {
		t.Fatalf("all-scope rows: historical=%d late-direct=%d, want 1 and 0", historical, late)
	}
}
