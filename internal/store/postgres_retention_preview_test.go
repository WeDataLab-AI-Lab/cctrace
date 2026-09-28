package store

import (
	"context"
	"testing"
	"time"
)

// TestRetentionAxesCoverKnownHypertables locks the single-source-of-truth
// invariant: the guard/preview axis map, the reconcile targets(), and the
// knownHypertables allowlist must all cover the same table set, so the confirm
// guard can never preview fewer tables than reconcile actually mutates.
func TestRetentionAxesCoverKnownHypertables(t *testing.T) {
	inAxes := map[string]bool{}
	for _, tables := range retentionAxes {
		for _, tbl := range tables {
			inAxes[tbl] = true
		}
	}
	for tbl := range knownHypertables {
		if !inAxes[tbl] {
			t.Errorf("%q is a known hypertable but not in any retention axis", tbl)
		}
	}
	for tbl := range inAxes {
		if !knownHypertables[tbl] {
			t.Errorf("retention axis references unknown hypertable %q", tbl)
		}
	}
	if got := len(RetentionConfig{}.targets()); got != len(inAxes) {
		t.Errorf("targets() covers %d tables, retentionAxes covers %d — drift", got, len(inAxes))
	}
	if got := len(managedHypertables()); got != len(inAxes) {
		t.Errorf("managedHypertables() covers %d tables, retentionAxes covers %d — drift", got, len(inAxes))
	}
}

func insertOtelEventTs(t *testing.T, s *PgStore, ts time.Time) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO otel_events (ts, event_name) VALUES ($1, 'test')`, ts); err != nil {
		t.Fatalf("insertOtelEventTs: %v", err)
	}
}

func insertOtelMetricTs(t *testing.T, s *PgStore, ts time.Time) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO otel_metrics (ts, metric_name) VALUES ($1, 'test')`, ts); err != nil {
		t.Fatalf("insertOtelMetricTs: %v", err)
	}
}

// TestRetentionPreviewAxis checks the dry-run row counts against synthetic data:
// a decrease/introduce counts drop-eligible rows; an increase is safe (0).
func TestRetentionPreviewAxis(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	p := func(n int) *int { return &n }
	truncateTables(t, s)
	t.Cleanup(func() {
		truncateTables(t, s)
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		forceRetention(t, s, "session_records", nil)
	})

	old := time.Now().AddDate(0, 0, -100)
	recent := time.Now().AddDate(0, 0, -1)
	insertOtelEventTs(t, s, old)
	insertOtelEventTs(t, s, recent)
	insertOtelMetricTs(t, s, old)
	insertSessionTs(t, s, old)
	insertSessionTs(t, s, recent)

	byTable := func(ps []*RetentionPreview) map[string]*RetentionPreview {
		m := map[string]*RetentionPreview{}
		for _, x := range ps {
			m[x.Table] = x
		}
		return m
	}

	t.Run("otel decrease drops old rows on both tables", func(t *testing.T) {
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		prev, err := s.RetentionPreviewAxis(ctx, "otel", 30)
		if err != nil {
			t.Fatalf("preview: %v", err)
		}
		if len(prev) != 2 {
			t.Fatalf("axis otel returned %d tables, want 2", len(prev))
		}
		m := byTable(prev)
		if e := m["otel_events"]; e == nil || e.CurrentDays == nil || *e.CurrentDays != 90 || e.RowsToDrop != 1 || e.OldestTs == nil {
			t.Errorf("otel_events preview = %+v, want current=90 rows_to_drop=1 oldest!=nil", e)
		}
		if e := m["otel_metrics"]; e == nil || e.RowsToDrop != 1 {
			t.Errorf("otel_metrics rows_to_drop = %+v, want 1", e)
		}
	})

	t.Run("increase is safe (rows_to_drop 0)", func(t *testing.T) {
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		prev, err := s.RetentionPreviewAxis(ctx, "otel", 120)
		if err != nil {
			t.Fatalf("preview: %v", err)
		}
		for _, e := range prev {
			if e.RowsToDrop != 0 {
				t.Errorf("%s increase rows_to_drop = %d, want 0", e.Table, e.RowsToDrop)
			}
		}
	})

	t.Run("session introduce (no current policy) drops old rows", func(t *testing.T) {
		forceRetention(t, s, "session_records", nil)
		prev, err := s.RetentionPreviewAxis(ctx, "session", 30)
		if err != nil {
			t.Fatalf("preview: %v", err)
		}
		if len(prev) != 1 {
			t.Fatalf("axis session returned %d tables, want 1", len(prev))
		}
		e := prev[0]
		if e.CurrentDays != nil || e.RowsToDrop != 1 {
			t.Errorf("session preview = %+v, want current=nil rows_to_drop=1", e)
		}
	})

	t.Run("unknown axis errors", func(t *testing.T) {
		if _, err := s.RetentionPreviewAxis(ctx, "bogus", 30); err == nil {
			t.Errorf("expected error for unknown axis")
		}
	})
}

// TestRetentionApplyDropsData is the deterministic, zero-risk e2e of the
// destructive path: on a throwaway container we insert known old+recent rows,
// apply a decrease via ReconcileRetention, force the async retention job to run
// synchronously (CALL run_job), and assert the old chunk is dropped while the
// recent one survives. Real data is never touched.
func TestRetentionApplyDropsData(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	p := func(n int) *int { return &n }
	truncateTables(t, s)
	t.Cleanup(func() {
		truncateTables(t, s)
		forceRetention(t, s, "otel_events", p(90))
		forceRetention(t, s, "otel_metrics", p(90))
		forceRetention(t, s, "session_records", nil)
	})

	old := time.Now().AddDate(0, 0, -100) // in a chunk entirely older than 30d
	recent := time.Now().AddDate(0, 0, -1)
	insertOtelEventTs(t, s, old)
	insertOtelEventTs(t, s, recent)

	forceRetention(t, s, "otel_events", p(90))
	if err := s.ReconcileRetention(ctx, RetentionConfig{OtelDays: p(30)}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if d := retentionDays(t, s, "otel_events"); d == nil || *d != 30 {
		t.Fatalf("policy not set to 30d: %v", d)
	}

	// Force the retention background job to run now instead of on schedule.
	var jobID int
	if err := s.pool.QueryRow(ctx,
		`SELECT job_id FROM timescaledb_information.jobs
		 WHERE proc_name='policy_retention' AND hypertable_name='otel_events'`).Scan(&jobID); err != nil {
		t.Fatalf("get job id: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `CALL run_job($1)`, jobID); err != nil {
		t.Fatalf("run_job: %v", err)
	}

	var oldRemaining, recentRemaining int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM otel_events WHERE ts < now() - make_interval(days => 30)`).Scan(&oldRemaining); err != nil {
		t.Fatalf("count old: %v", err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM otel_events WHERE ts >= now() - make_interval(days => 30)`).Scan(&recentRemaining); err != nil {
		t.Fatalf("count recent: %v", err)
	}
	if oldRemaining != 0 {
		t.Errorf("old rows not dropped by retention job: %d remain", oldRemaining)
	}
	if recentRemaining != 1 {
		t.Errorf("recent rows should survive: got %d, want 1", recentRemaining)
	}
}
