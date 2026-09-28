package store

import (
	"context"
	"testing"
)

// TestCompressionEnabled verifies the migration actually enables compression on
// the otel hypertables (historically it only called add_compression_policy
// without first ALTERing SET compress, so compression was silently off) and that
// the 30-day policy exists. Also checks replay idempotency.
func TestCompressionEnabled(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	cases := []struct {
		table     string
		segmentby string
	}{
		{"otel_events", "profile_email"},
		{"otel_metrics", "metric_name"},
	}

	check := func(t *testing.T) {
		for _, c := range cases {
			var enabled bool
			if err := s.pool.QueryRow(ctx,
				`SELECT compression_enabled FROM timescaledb_information.hypertables WHERE hypertable_name=$1`,
				c.table).Scan(&enabled); err != nil {
				t.Fatalf("%s compression_enabled: %v", c.table, err)
			}
			if !enabled {
				t.Errorf("%s: compression not enabled", c.table)
			}

			var seg string
			if err := s.pool.QueryRow(ctx,
				`SELECT attname FROM timescaledb_information.compression_settings
				 WHERE hypertable_name=$1 AND segmentby_column_index IS NOT NULL`,
				c.table).Scan(&seg); err != nil {
				t.Fatalf("%s segmentby: %v", c.table, err)
			}
			if seg != c.segmentby {
				t.Errorf("%s segmentby = %q, want %q", c.table, seg, c.segmentby)
			}

			// orderby must be ts DESC (orderby_asc = false).
			var obCol string
			var obAsc bool
			if err := s.pool.QueryRow(ctx,
				`SELECT attname, orderby_asc FROM timescaledb_information.compression_settings
				 WHERE hypertable_name=$1 AND orderby_column_index IS NOT NULL`,
				c.table).Scan(&obCol, &obAsc); err != nil {
				t.Fatalf("%s orderby: %v", c.table, err)
			}
			if obCol != "ts" || obAsc {
				t.Errorf("%s orderby = %q asc=%v, want ts DESC", c.table, obCol, obAsc)
			}

			// retention must coexist (compress after 30d, drop after 90d).
			var retExists bool
			if err := s.pool.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM timescaledb_information.jobs
				 WHERE proc_name='policy_retention' AND hypertable_name=$1)`,
				c.table).Scan(&retExists); err != nil {
				t.Fatalf("%s retention coexistence: %v", c.table, err)
			}
			if !retExists {
				t.Errorf("%s: retention policy should coexist with compression", c.table)
			}

			// The retention interval is load-bearing beyond storage cost: the
			// session_records login_email backfill (#140) reads otel_events as its
			// only source, and session_records has no retention of its own. So this
			// number is how long a session stays attributable after the fact, and
			// shortening it silently shrinks that window.
			var retDays *int
			if err := s.pool.QueryRow(ctx,
				`SELECT (EXTRACT(epoch FROM (config->>'drop_after')::interval)/86400)::int
				 FROM timescaledb_information.jobs
				 WHERE proc_name='policy_retention' AND hypertable_name=$1`,
				c.table).Scan(&retDays); err != nil {
				t.Fatalf("%s retention policy: %v", c.table, err)
			}
			if retDays == nil || *retDays != 90 {
				t.Errorf("%s retention policy = %v days, want 90", c.table, retDays)
			}

			var days *int
			if err := s.pool.QueryRow(ctx,
				`SELECT (EXTRACT(epoch FROM (config->>'compress_after')::interval)/86400)::int
				 FROM timescaledb_information.jobs
				 WHERE proc_name='policy_compression' AND hypertable_name=$1`,
				c.table).Scan(&days); err != nil {
				t.Fatalf("%s compression policy: %v", c.table, err)
			}
			if days == nil || *days != 30 {
				t.Errorf("%s compression policy = %v days, want 30", c.table, days)
			}
		}
	}

	// State established by Migrate at container init.
	check(t)

	// Replaying migrations must be idempotent: no error, compression still on.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("replay Migrate: %v", err)
	}
	check(t)
}
