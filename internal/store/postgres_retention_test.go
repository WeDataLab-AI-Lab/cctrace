package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// NOTE: these tests mutate the shared testcontainers instance's global retention
// policy state (timescaledb_information.jobs is not truncated between tests).
// They are correct only because the package runs DB tests serially — do NOT add
// t.Parallel() here or to siblings. t.Cleanup restores the post-migrate baseline.

// retentionDays returns the retention policy interval in whole days, computed
// via EXTRACT(epoch)/86400 to mirror the impl's interval-equality semantics
// (Postgres normalizes months to 30d); nil if no retention policy exists.
func retentionDays(t *testing.T, s *PgStore, table string) *int {
	t.Helper()
	var d *int
	err := s.pool.QueryRow(context.Background(), `
		SELECT (EXTRACT(epoch FROM (config->>'drop_after')::interval) / 86400)::int
		FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND hypertable_name = $1`, table).Scan(&d)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("retentionDays(%s): %v", table, err)
	}
	return d
}

// retentionJobID returns the retention job id for a hypertable, or nil. Used to
// prove a no-op reconcile does not churn (remove+add) an unchanged policy.
func retentionJobID(t *testing.T, s *PgStore, table string) *int {
	t.Helper()
	var id *int
	err := s.pool.QueryRow(context.Background(), `
		SELECT job_id FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND hypertable_name = $1`, table).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("retentionJobID(%s): %v", table, err)
	}
	return id
}

// retentionScheduled returns whether the retention job is scheduled (enabled),
// or nil if no policy exists. A policy that exists but is disabled would never
// drop chunks, so a passing "add" test must confirm scheduled=true.
func retentionScheduled(t *testing.T, s *PgStore, table string) *bool {
	t.Helper()
	var sched *bool
	err := s.pool.QueryRow(context.Background(), `
		SELECT scheduled FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND hypertable_name = $1`, table).Scan(&sched)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("retentionScheduled(%s): %v", table, err)
	}
	return sched
}

// forceRetention sets a known starting policy (nil = no policy), bypassing
// ReconcileRetention so setup is independent of the code under test.
func forceRetention(t *testing.T, s *PgStore, table string, days *int) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `SELECT remove_retention_policy($1, if_exists => true)`, table); err != nil {
		t.Fatalf("forceRetention remove %s: %v", table, err)
	}
	if days != nil {
		if _, err := s.pool.Exec(ctx, `SELECT add_retention_policy($1, drop_after => make_interval(days => $2))`, table, *days); err != nil {
			t.Fatalf("forceRetention add %s: %v", table, err)
		}
	}
}

func insertSessionTs(t *testing.T, s *PgStore, ts time.Time) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO session_records (ts, session_id, record_type) VALUES ($1, 'test', 'test')`, ts); err != nil {
		t.Fatalf("insertSessionTs: %v", err)
	}
}

func TestReconcileRetention(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	p := func(n int) *int { return &n }

	// Restore the post-migrate baseline so sibling tests are unaffected.
	t.Cleanup(func() {
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		forceRetention(t, s, "session_records", nil)
	})

	// (a) core safety invariant: nil config touches NOTHING. Asserts all three
	// targets, including otel_metrics (the second table on the OtelDays axis).
	t.Run("opt-in nil is a no-op", func(t *testing.T) {
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		forceRetention(t, s, "session_records", nil)
		if err := s.ReconcileRetention(ctx, RetentionConfig{}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		for _, tbl := range []string{"otel_events", "otel_metrics"} {
			if d := retentionDays(t, s, tbl); d == nil || *d != 90 {
				t.Errorf("%s changed by no-op: %v", tbl, d)
			}
		}
		if d := retentionDays(t, s, "session_records"); d != nil {
			t.Errorf("session_records policy added by no-op: %v", d)
		}
	})

	// (b) add a policy where none existed, and confirm it is scheduled.
	t.Run("fresh add on session_records", func(t *testing.T) {
		forceRetention(t, s, "session_records", nil)
		if err := s.ReconcileRetention(ctx, RetentionConfig{SessionDays: p(30)}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if d := retentionDays(t, s, "session_records"); d == nil || *d != 30 {
			t.Errorf("session_records = %v, want 30", d)
		}
		if sched := retentionScheduled(t, s, "session_records"); sched == nil || !*sched {
			t.Errorf("session_records policy not scheduled: %v", sched)
		}
	})

	// (c) change interval; OtelDays applies to BOTH otel hypertables.
	t.Run("change interval on both otel tables", func(t *testing.T) {
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		if err := s.ReconcileRetention(ctx, RetentionConfig{OtelDays: p(30)}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		for _, tbl := range []string{"otel_events", "otel_metrics"} {
			if d := retentionDays(t, s, tbl); d == nil || *d != 30 {
				t.Errorf("%s = %v, want 30", tbl, d)
			}
		}
	})

	// (d) same value must not churn the job (no remove+add).
	t.Run("idempotent same value keeps job id", func(t *testing.T) {
		forceRetention(t, s, "otel_events", p(30))
		before := retentionJobID(t, s, "otel_events")
		if err := s.ReconcileRetention(ctx, RetentionConfig{OtelDays: p(30)}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		after := retentionJobID(t, s, "otel_events")
		if before == nil || after == nil || *before != *after {
			t.Errorf("job id churned on no-op reconcile: before=%v after=%v", before, after)
		}
	})

	// (e) 0 means "remove policy" = permanent (safe direction).
	t.Run("session=0 removes policy", func(t *testing.T) {
		forceRetention(t, s, "session_records", p(30))
		if err := s.ReconcileRetention(ctx, RetentionConfig{SessionDays: p(0)}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if d := retentionDays(t, s, "session_records"); d != nil {
			t.Errorf("session_records policy not removed: %v", d)
		}
	})

	// (g) fat-finger guard: negative days changes nothing on EITHER otel table.
	t.Run("negative days is skipped", func(t *testing.T) {
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		if err := s.ReconcileRetention(ctx, RetentionConfig{OtelDays: p(-5)}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		for _, tbl := range []string{"otel_events", "otel_metrics"} {
			if d := retentionDays(t, s, tbl); d == nil || *d != 90 {
				t.Errorf("%s changed by negative days: %v", tbl, d)
			}
		}
	})

	// (h) a policy with the right interval but disabled must be re-enabled: a
	// matching-but-scheduled=false policy never drops chunks, so it is not
	// "already correct".
	t.Run("re-enables a disabled policy with matching interval", func(t *testing.T) {
		forceRetention(t, s, "otel_events", p(30))
		forceRetention(t, s, "otel_metrics", p(30)) // stays scheduled -> no-op on this axis
		var jobID int
		if err := s.pool.QueryRow(ctx, `SELECT job_id FROM timescaledb_information.jobs
			WHERE proc_name='policy_retention' AND hypertable_name='otel_events'`).Scan(&jobID); err != nil {
			t.Fatalf("get job id: %v", err)
		}
		if _, err := s.pool.Exec(ctx, `SELECT alter_job($1, scheduled => false)`, jobID); err != nil {
			t.Fatalf("disable job: %v", err)
		}
		if sched := retentionScheduled(t, s, "otel_events"); sched == nil || *sched {
			t.Fatalf("precondition: otel_events job should be disabled, got %v", sched)
		}
		if err := s.ReconcileRetention(ctx, RetentionConfig{OtelDays: p(30)}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if d := retentionDays(t, s, "otel_events"); d == nil || *d != 30 {
			t.Errorf("otel_events interval changed: %v, want 30", d)
		}
		if sched := retentionScheduled(t, s, "otel_events"); sched == nil || !*sched {
			t.Errorf("disabled policy not re-enabled: %v", sched)
		}
	})
}

// TestRetentionBlastRadius exercises the pre-drop safety probe with real rows —
// the most data-relevant path, which the reconcile subtests (empty tables)
// cannot cover.
func TestRetentionBlastRadius(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	truncateTables(t, s)
	t.Cleanup(func() { truncateTables(t, s) })

	// Old row predates a 90-day cutoff; recent row does not.
	insertSessionTs(t, s, time.Now().AddDate(0, 0, -400))
	insertSessionTs(t, s, time.Now().AddDate(0, 0, -1))

	oldest, willDrop, err := s.retentionBlastRadius(ctx, "session_records", 90)
	if err != nil {
		t.Fatalf("blast radius: %v", err)
	}
	if !willDrop {
		t.Errorf("expected willDrop=true: a 400d-old row under 90d retention")
	}
	if oldest == nil || !oldest.Before(time.Now().AddDate(0, 0, -90)) {
		t.Errorf("oldest = %v, want < now-90d", oldest)
	}

	// A generous 500d retention drops nothing (oldest row is only 400d old).
	if _, willDrop, err := s.retentionBlastRadius(ctx, "session_records", 500); err != nil || willDrop {
		t.Errorf("500d retention: willDrop=%v err=%v, want false/nil", willDrop, err)
	}

	// Empty table never flags a drop.
	if _, willDrop, err := s.retentionBlastRadius(ctx, "otel_events", 90); err != nil || willDrop {
		t.Errorf("empty table: willDrop=%v err=%v, want false/nil", willDrop, err)
	}

	// Unknown table is rejected (guards the identifier-formatting path).
	if _, _, err := s.retentionBlastRadius(ctx, "not_a_table", 90); err == nil {
		t.Errorf("expected error for unknown hypertable")
	}
}

// TestRetentionInfo covers the read-only admin snapshot: all three hypertables,
// retention days reflecting current policies, session_records having none, and
// the size/compression queries running without error.
func TestRetentionInfo(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	p := func(n int) *int { return &n }

	forceRetention(t, s, "otel_events", p(90))
	forceRetention(t, s, "otel_metrics", p(45))
	forceRetention(t, s, "session_records", nil)
	t.Cleanup(func() {
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		forceRetention(t, s, "session_records", nil)
	})

	rep, err := s.RetentionInfo(ctx)
	if err != nil {
		t.Fatalf("RetentionInfo: %v", err)
	}
	if len(rep.Tables) != 3 {
		t.Fatalf("tables = %d, want 3", len(rep.Tables))
	}

	byName := map[string]*TableRetention{}
	for _, tr := range rep.Tables {
		byName[tr.Table] = tr
	}

	if oe := byName["otel_events"]; oe == nil || oe.RetentionDays == nil || *oe.RetentionDays != 90 {
		t.Errorf("otel_events retention = %v, want 90", byName["otel_events"])
	}
	if om := byName["otel_metrics"]; om == nil || om.RetentionDays == nil || *om.RetentionDays != 45 {
		t.Errorf("otel_metrics retention = %v, want 45", byName["otel_metrics"])
	}
	sr := byName["session_records"]
	if sr == nil {
		t.Fatalf("session_records missing from report")
	}
	if sr.RetentionDays != nil {
		t.Errorf("session_records retention = %v, want nil (permanent)", sr.RetentionDays)
	}
	// compression, if present from migrations, must read as a sane day count.
	if oe := byName["otel_events"]; oe.CompressionDays != nil && *oe.CompressionDays <= 0 {
		t.Errorf("otel_events compression = %d, want >0 or nil", *oe.CompressionDays)
	}
	// size/rows queries must run and return non-negative values for every table.
	for _, tr := range rep.Tables {
		if tr.SizeBytes < 0 || tr.RowsApprox < 0 {
			t.Errorf("%s size=%d rows=%d, want non-negative", tr.Table, tr.SizeBytes, tr.RowsApprox)
		}
	}

	// A disabled policy is NOT active retention: it must report nil, matching
	// reconcile's treatment of scheduled=false as not-correct.
	var jobID int
	if err := s.pool.QueryRow(ctx, `SELECT job_id FROM timescaledb_information.jobs
		WHERE proc_name='policy_retention' AND hypertable_name='otel_events'`).Scan(&jobID); err != nil {
		t.Fatalf("get job id: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `SELECT alter_job($1, scheduled => false)`, jobID); err != nil {
		t.Fatalf("disable job: %v", err)
	}
	rep2, err := s.RetentionInfo(ctx)
	if err != nil {
		t.Fatalf("RetentionInfo after disable: %v", err)
	}
	for _, tr := range rep2.Tables {
		if tr.Table == "otel_events" && tr.RetentionDays != nil {
			t.Errorf("disabled otel_events policy reported as active: %v", tr.RetentionDays)
		}
	}
}
