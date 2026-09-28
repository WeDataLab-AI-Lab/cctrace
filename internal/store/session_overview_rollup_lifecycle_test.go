package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func overviewFor(t *testing.T, s *PgStore, filter SessionOverviewFilter, sessionID string) *SessionOverview {
	t.Helper()
	rows, err := s.ListSessionOverviews(context.Background(), filter)
	if err != nil {
		t.Fatalf("ListSessionOverviews: %v", err)
	}
	for _, row := range rows {
		if row.SessionID == sessionID {
			return row
		}
	}
	return nil
}

func TestSessionOverviewRollup_ReenrichRefreshesTouchedSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	record := &SessionRecord{Ts: ts, SessionID: "reenrich-rollup", ProfileEmail: "p@example.com", RecordType: "user", UUID: "", Raw: []byte(`{}`)}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{record}); err != nil {
		t.Fatal(err)
	}
	if got := overviewFor(t, s, SessionOverviewFilter{ProfileEmail: "p@example.com"}, record.SessionID); got == nil || got.HasEnriched {
		t.Fatalf("before reenrich: %+v", got)
	}
	record.UUID, record.SourceFile, record.PromptSource = "u1", "session.jsonl", "typed"
	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{record}); err != nil {
		t.Fatal(err)
	}
	if got := overviewFor(t, s, SessionOverviewFilter{ProfileEmail: "p@example.com"}, record.SessionID); got == nil || !got.HasEnriched {
		t.Fatalf("after reenrich: %+v", got)
	}
}

func TestSessionOverviewRollup_LoginBackfillRefreshesTouchedSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	in := 50
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: ts, SessionID: "backfill-rollup", ProfileEmail: "p@example.com", InputTokens: &in, UUID: "r1", Raw: []byte(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: ts.Add(time.Minute), EventName: "api_request", SessionID: "backfill-rollup", ProfileEmail: "p@example.com", LoginEmail: "login@example.com"}}); err != nil {
		t.Fatal(err)
	}
	before := overviewFor(t, s, SessionOverviewFilter{LoginEmail: "login@example.com"}, "backfill-rollup")
	if before == nil || before.HasSync || before.InputTokens != 0 {
		t.Fatalf("before backfill: %+v", before)
	}
	if _, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatal(err)
	}
	after := overviewFor(t, s, SessionOverviewFilter{LoginEmail: "login@example.com"}, "backfill-rollup")
	if after == nil || !after.HasSync || after.InputTokens != 50 {
		t.Fatalf("after backfill: %+v", after)
	}
}

func TestSessionOverviewRollup_ImputedCostRefreshesTouchedSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Now().UTC().Add(-time.Hour)
	in, out := 1000, 100
	for i := 0; i < 20; i++ {
		if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events (ts,event_name,session_id,model,cost_usd,input_tokens,agent,billing_provider)
			VALUES ($1,'api_request',$2,'claude-test-model',1,1000,'claude','anthropic')`, ts, "rate-session-"+time.Duration(i).String()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "claude-imputed-rollup", RecordType: "assistant", ProfileEmail: "p@example.com", Model: "claude-test-model", InputTokens: &in, OutputTokens: &out, Agent: "claude", UUID: "c1", Raw: []byte(`{}`)},
		codexUsageRecord("codex-imputed-rollup", "gpt-5.5", ts, in, out, 0),
	}); err != nil {
		t.Fatal(err)
	}
	// The rate is derived into a table now, not read from a live view, so it has
	// to exist before anything can be priced with it.
	if err := s.RefreshModelRateBuckets(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatal(err)
	}
	if got := overviewFor(t, s, SessionOverviewFilter{}, "claude-imputed-rollup"); got == nil || got.CostUSD <= 0 {
		t.Fatalf("claude imputed overview: %+v", got)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatal(err)
	}
	got := overviewFor(t, s, SessionOverviewFilter{}, "codex-imputed-rollup")
	if got == nil || got.CostUSD <= 0 {
		t.Fatalf("codex imputed overview: %+v", got)
	}
	beforeIncremental := got.CostUSD
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		codexUsageRecord("codex-imputed-rollup", "gpt-5.5", ts.Add(time.Minute), in*2, out*2, 0),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshCodexImputedCostIncremental(ctx); err != nil {
		t.Fatal(err)
	}
	if got := overviewFor(t, s, SessionOverviewFilter{}, "codex-imputed-rollup"); got == nil || got.CostUSD <= beforeIncremental {
		t.Fatalf("codex incremental overview: before=%f after=%+v", beforeIncremental, got)
	}
}

func TestSessionOverviewRollup_AdminMutationsRefreshTouchedSessions(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		s := acquireTestStore(t)
		truncateTables(t, s)
		if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{{Ts: time.Now(), SessionID: "delete-rollup", ProfileEmail: "old@example.com", UUID: "d1", Raw: []byte(`{}`)}}); err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteUserData(context.Background(), "old@example.com", ""); err != nil {
			t.Fatal(err)
		}
		if got := overviewFor(t, s, SessionOverviewFilter{}, "delete-rollup"); got != nil {
			t.Fatalf("deleted session remains: %+v", got)
		}
	})
	t.Run("merge", func(t *testing.T) {
		s := acquireTestStore(t)
		truncateTables(t, s)
		if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{{Ts: time.Now(), SessionID: "merge-rollup", ProfileEmail: "old@example.com", UUID: "m1", Raw: []byte(`{}`)}}); err != nil {
			t.Fatal(err)
		}
		if err := s.MergeUsers(context.Background(), "old@example.com", "", "new@example.com"); err != nil {
			t.Fatal(err)
		}
		if got := overviewFor(t, s, SessionOverviewFilter{ProfileEmail: "old@example.com"}, "merge-rollup"); got != nil {
			t.Fatalf("old scope remains: %+v", got)
		}
		if got := overviewFor(t, s, SessionOverviewFilter{ProfileEmail: "new@example.com"}, "merge-rollup"); got == nil {
			t.Fatal("new scope missing")
		}
	})
	t.Run("orphan cleanup", func(t *testing.T) {
		s := acquireTestStore(t)
		truncateTables(t, s)
		if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{{Ts: time.Now(), SessionID: "orphan-rollup", ProfileEmail: "p@example.com", UUID: "o1", Raw: []byte(`{}`)}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CleanupOrphanSessionRecords(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := overviewFor(t, s, SessionOverviewFilter{}, "orphan-rollup"); got != nil {
			t.Fatalf("orphan remains: %+v", got)
		}
	})
}

type overviewRetentionReconciler interface {
	ReconcileSessionOverviewRollupsForRetention(context.Context) (int, error)
}

func TestRetentionJobFinishScannerTreatsInfinityAsUnrun(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	finish, err := scanRetentionJobFinish(s.pool.QueryRow(ctx, `SELECT '-infinity'::timestamptz`))
	if err != nil {
		t.Fatalf("scan -infinity: %v", err)
	}
	if finish != nil {
		t.Fatalf("-infinity finish = %v, want nil (unrun)", finish)
	}

	want := time.Date(2026, 8, 21, 9, 30, 0, 0, time.UTC)
	finish, err = scanRetentionJobFinish(s.pool.QueryRow(ctx, `SELECT $1::timestamptz`, want))
	if err != nil {
		t.Fatalf("scan finite finish: %v", err)
	}
	if finish == nil || !finish.Equal(want) {
		t.Fatalf("finite finish = %v, want %v", finish, want)
	}
}

func TestSessionOverviewRollup_RetentionReconciliationRemovesDroppedSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	r, ok := any(s).(overviewRetentionReconciler)
	if !ok {
		t.Fatal("PgStore must implement retention rollup reconciliation")
	}
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -100)
	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: old, EventName: "api_request", SessionID: "retained-rollup", ProfileEmail: "p@example.com"}}); err != nil {
		t.Fatal(err)
	}
	p := func(v int) *int { return &v }
	forceRetention(t, s, "otel_events", p(30))
	defer forceRetention(t, s, "otel_events", p(90))
	jobID := retentionJobID(t, s, "otel_events")
	if jobID == nil {
		t.Fatal("retention job missing")
	}
	if _, err := s.pool.Exec(ctx, `CALL run_job($1)`, *jobID); err != nil {
		t.Fatal(err)
	}
	var sourceRows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM otel_events WHERE session_id='retained-rollup'`).Scan(&sourceRows); err != nil {
		t.Fatal(err)
	}
	if sourceRows != 0 {
		t.Fatalf("retention source rows = %d, want 0", sourceRows)
	}
	var stateRows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_overview_retention_state`).Scan(&stateRows); err != nil || stateRows != 0 {
		t.Fatalf("unexpected retention state rows=%d err=%v", stateRows, err)
	}
	if _, err := r.ReconcileSessionOverviewRollupsForRetention(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := overviewFor(t, s, SessionOverviewFilter{}, "retained-rollup"); got != nil {
		t.Fatalf("dropped session remains: %+v", got)
	}
}

func TestSessionOverviewRollup_RetentionReconciliationRemovesOrphanedImputedRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -100)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{codexUsageRecord("retained-imputed-rollup", "gpt-5.5", old, 1000, 100, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatal(err)
	}
	p := func(v int) *int { return &v }
	forceRetention(t, s, "session_records", p(30))
	defer forceRetention(t, s, "session_records", nil)
	jobID := retentionJobID(t, s, "session_records")
	if jobID == nil {
		t.Fatal("session retention job missing")
	}
	if _, err := s.pool.Exec(ctx, `CALL run_job($1)`, *jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileSessionOverviewRollupsForRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if got := overviewFor(t, s, SessionOverviewFilter{}, "retained-imputed-rollup"); got != nil {
		t.Fatalf("orphaned imputed session remains: %+v", got)
	}
	var orphaned int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM codex_imputed_cost WHERE session_id='retained-imputed-rollup'`).Scan(&orphaned); err != nil {
		t.Fatal(err)
	}
	if orphaned != 0 {
		t.Fatalf("orphaned imputed rows = %d", orphaned)
	}
}

func TestSessionOverviewRollup_RetentionReconciliationCleansExcludedImputedOrphans(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -100)
	record := codexUsageRecord("excluded-retention-rollup", "gpt-5.5", old, 1000, 100, 0)
	record.LoginEmail = "hidden@example.com"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{record}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExcludeAccount(ctx, "hidden@example.com", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if got := overviewFor(t, s, SessionOverviewFilter{}, record.SessionID); got != nil {
		t.Fatalf("excluded session remains visible: %+v", got)
	}
	p := func(v int) *int { return &v }
	forceRetention(t, s, "session_records", p(30))
	defer forceRetention(t, s, "session_records", nil)
	jobID := retentionJobID(t, s, "session_records")
	if jobID == nil {
		t.Fatal("session retention job missing")
	}
	if _, err := s.pool.Exec(ctx, `CALL run_job($1)`, *jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileSessionOverviewRollupsForRetention(ctx); err != nil {
		t.Fatal(err)
	}
	var orphaned int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM codex_imputed_cost WHERE session_id=$1`, record.SessionID).Scan(&orphaned); err != nil {
		t.Fatal(err)
	}
	if orphaned != 0 {
		t.Fatalf("excluded orphaned imputed rows = %d, want 0", orphaned)
	}
}

func TestSessionOverviewRollup_IngestionLocksSourceBeforeMutation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION require_shared_source_lock() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid() AND locktype = 'advisory'
			AND classid = hashtext('session_overview_source')::oid AND objid = 0 AND objsubid = 2
			AND mode IN ('ShareLock', 'ExclusiveLock') AND granted) THEN
			RAISE EXCEPTION 'shared source lock missing before ingestion';
		END IF;
		RETURN NEW;
	END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `CREATE TRIGGER require_shared_source_lock BEFORE INSERT ON session_records FOR EACH ROW EXECUTE FUNCTION require_shared_source_lock()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS require_shared_source_lock ON session_records`)
		_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS require_shared_source_lock()`)
	})
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: time.Now(), SessionID: "source-lock", ProfileEmail: "race@example.com", UUID: "r1", Raw: []byte(`{}`)}}); err != nil {
		t.Fatalf("ingestion must lock before source insert: %v", err)
	}
}

func TestSessionOverviewRollup_AdminMutationsLockSessionsBeforeSourceMutation(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"delete", func() error { return s.DeleteUserData(ctx, "race@example.com", "") }},
		{"merge", func() error { return s.MergeUsers(ctx, "race@example.com", "", "merged@example.com") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t, s)
			if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: time.Now(), SessionID: "admin-race-" + tc.name, ProfileEmail: "race@example.com", UUID: "r1", Raw: []byte(`{}`)}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION require_admin_session_lock() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid() AND locktype = 'advisory'
					AND classid = hashtext('session_overview_source')::oid AND objid = 0 AND objsubid = 2
					AND mode = 'ExclusiveLock' AND granted) THEN
					RAISE EXCEPTION 'exclusive source lock missing before admin mutation';
				END IF;
				RETURN OLD;
			END $$`); err != nil {
				t.Fatal(err)
			}
			operation := "DELETE"
			if tc.name == "merge" {
				operation = "UPDATE"
			}
			if _, err := s.pool.Exec(ctx, `CREATE TRIGGER require_admin_session_lock BEFORE `+operation+` ON session_records FOR EACH ROW EXECUTE FUNCTION require_admin_session_lock()`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS require_admin_session_lock ON session_records`)
				_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS require_admin_session_lock()`)
			})
			if err := tc.run(); err != nil {
				t.Fatalf("%s must lock before mutation: %v", tc.name, err)
			}
		})
	}
}

func TestSessionOverviewRollup_ExcludedSessionMutationsLockRollupBeforeTable(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	installGuard := func(t *testing.T, operation, requiredMode string) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION require_rollup_maintenance_lock() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_locks WHERE pid = pg_backend_pid() AND locktype = 'advisory'
				AND classid = hashtext('session_overview_rollups')::oid AND objid = 0 AND objsubid = 2
				AND mode IN (`+requiredMode+`) AND granted) THEN
				RAISE EXCEPTION 'rollup maintenance lock missing before excluded_sessions mutation';
			END IF;
			RETURN NULL;
		END $$`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `CREATE TRIGGER require_rollup_maintenance_lock BEFORE `+operation+` ON excluded_sessions FOR EACH STATEMENT EXECUTE FUNCTION require_rollup_maintenance_lock()`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS require_rollup_maintenance_lock ON excluded_sessions`)
			_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS require_rollup_maintenance_lock()`)
		})
	}

	t.Run("full rebuild takes exclusive lock before truncate", func(t *testing.T) {
		truncateTables(t, s)
		installGuard(t, "TRUNCATE", "'ExclusiveLock'")
		if err := s.RecomputeExcludedSessions(ctx); err != nil {
			t.Fatalf("RecomputeExcludedSessions lock order: %v", err)
		}
	})
	t.Run("incremental takes shared lock before insert", func(t *testing.T) {
		truncateTables(t, s)
		if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_accounts (login_email) VALUES ('lock-order@example.com')`); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: time.Now(), EventName: "api_request", SessionID: "lock-order", LoginEmail: "lock-order@example.com"}}); err != nil {
			t.Fatal(err)
		}
		installGuard(t, "INSERT", "'ShareLock', 'ExclusiveLock'")
		if err := s.RefreshExcludedSessionsIncremental(ctx); err != nil {
			t.Fatalf("RefreshExcludedSessionsIncremental lock order: %v", err)
		}
	})
}

func TestSessionOverviewRollup_ExcludedAccountMutationRollsBackWhenRollupRefreshFails(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	installFailure := func(t *testing.T) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION fail_rollup_refresh() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced rollup refresh failure'; END $$`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `CREATE TRIGGER fail_rollup_refresh BEFORE DELETE ON session_overview_rollups FOR EACH STATEMENT EXECUTE FUNCTION fail_rollup_refresh()`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS fail_rollup_refresh ON session_overview_rollups`)
			_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS fail_rollup_refresh()`)
		})
	}

	t.Run("add", func(t *testing.T) {
		truncateTables(t, s)
		if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: time.Now(), EventName: "api_request", SessionID: "exclude-add", LoginEmail: "atomic@example.com"}}); err != nil {
			t.Fatal(err)
		}
		installFailure(t)
		if _, err := s.ExcludeAccount(ctx, "atomic@example.com", "test", "test"); err == nil {
			t.Fatal("ExcludeAccount succeeded despite forced rollup failure")
		}
		var excluded int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM excluded_accounts WHERE login_email='atomic@example.com'`).Scan(&excluded); err != nil {
			t.Fatal(err)
		}
		if excluded != 0 {
			t.Fatalf("excluded account committed after failed refresh: %d", excluded)
		}
	})

	t.Run("remove", func(t *testing.T) {
		truncateTables(t, s)
		if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: time.Now(), EventName: "api_request", SessionID: "exclude-remove", LoginEmail: "atomic@example.com"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ExcludeAccount(ctx, "atomic@example.com", "test", "test"); err != nil {
			t.Fatal(err)
		}
		installFailure(t)
		if err := s.RemoveExcludedAccount(ctx, "atomic@example.com"); err == nil {
			t.Fatal("RemoveExcludedAccount succeeded despite forced rollup failure")
		}
		var excluded int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM excluded_accounts WHERE login_email='atomic@example.com'`).Scan(&excluded); err != nil {
			t.Fatal(err)
		}
		if excluded != 1 {
			t.Fatalf("excluded account removal committed after failed refresh: %d", excluded)
		}
	})
}

func TestSessionOverviewRollup_UnboundedPlanDoesNotScanBaseViews(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	countQuery, countArgs := sessionOverviewRollupCountQuery(SessionOverviewFilter{})
	listQuery, listArgs := sessionOverviewRollupListQuery(SessionOverviewFilter{})
	for _, planned := range []struct {
		query string
		args  []interface{}
	}{{countQuery, countArgs}, {listQuery, listArgs}} {
		var plan string
		if err := s.pool.QueryRow(ctx, `EXPLAIN (FORMAT JSON) `+planned.query, planned.args...).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"otel_events", "session_records", "unified_events", "visible_events", "visible_session_records"} {
			if strings.Contains(plan, forbidden) {
				t.Errorf("plan scans %s: %s", forbidden, plan)
			}
		}
		if !strings.Contains(plan, "session_overview_rollups") {
			t.Errorf("plan does not scan rollup: %s", plan)
		}
	}
}

func TestSessionOverviewRollup_ExplicitFullRebuildCallers(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"recompute", "backfill"} {
		t.Run(name, func(t *testing.T) {
			truncateTables(t, s)
			ts := time.Now()
			if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events (ts, event_name, session_id) VALUES ($1, 'api_request', 'full-source')`, ts); err != nil {
				t.Fatal(err)
			}
			if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups (scope_type, scope_value, session_id, start_time, end_time, event_count) VALUES ('all', '', 'stale-rollup', $1, $1, 99)`, ts); err != nil {
				t.Fatal(err)
			}
			if name == "recompute" {
				if err := s.RecomputeExcludedSessions(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name=$1`, sessionOverviewRollupBackfill); err != nil {
					t.Fatal(err)
				}
				if err := s.BackfillSessionOverviewRollups(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var sourceRows, staleRows int
			if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE session_id='full-source'), count(*) FILTER (WHERE session_id='stale-rollup') FROM session_overview_rollups WHERE scope_type='all'`).Scan(&sourceRows, &staleRows); err != nil {
				t.Fatal(err)
			}
			if sourceRows != 1 || staleRows != 0 {
				t.Fatalf("full rebuild rows: source=%d stale=%d, want 1 and 0", sourceRows, staleRows)
			}
		})
	}
}
