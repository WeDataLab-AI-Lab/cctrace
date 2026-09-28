package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestLoginEmailDerivedRefresh_NoUpdatesPreservesAllTables(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func(context.Context, time.Time) (int64, error)
	}{
		{"backfill", s.BackfillSessionRecordLoginEmail},
		{"inference", s.InferSessionRecordLoginEmail},
		{"quota account", s.FillCodexAccountFromQuota},
		{"quota attribution", s.AttributeCodexLoginEmailFromQuota},
	} {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t, s)
			// The shared reset does not include task_segment_facts. These orphan
			// fixtures must not survive into another subtest or repair fixture.
			if _, err := s.pool.Exec(ctx, `TRUNCATE task_segment_facts`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := s.pool.Exec(ctx, `DELETE FROM task_segment_facts WHERE session_id='untouched'`); err != nil {
					t.Error(err)
				}
			})
			ts := time.Now()
			// Orphan derived rows distinguish a no-op from a full delete/reinsert
			// even when there are no source rows eligible for login attribution.
			for _, query := range []string{
				`INSERT INTO session_overview_rollups (scope_type, scope_value, session_id, start_time, end_time) VALUES ('all', '', 'untouched', $1, $1)`,
				`INSERT INTO plugin_invocation_facts (source_record_id, session_id, command_ts, command_name) VALUES (1, 'untouched', $1, 'test')`,
				`INSERT INTO task_segment_facts (boundary_record_id, session_id, start_ts) VALUES (1, 'untouched', $1)`,
			} {
				if _, err := s.pool.Exec(ctx, query, ts); err != nil {
					t.Fatal(err)
				}
			}
			updated, err := tc.run(ctx, ts.Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if updated != 0 {
				t.Fatalf("updated=%d, want 0", updated)
			}
			for _, table := range []string{"session_overview_rollups", "plugin_invocation_facts", "task_segment_facts"} {
				var count int
				if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE session_id='untouched'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Errorf("%s rows=%d, want preserved 1 after zero updates", table, count)
				}
			}
		})
	}
}

func TestLoginEmailDerivedRefresh_EmptyInputsDoNotAccessDatabase(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, pgx.Tx, []string) error
	}{
		{"overview", refreshSessionOverviewRollups},
		{"plugin", refreshPluginInvocationFacts},
		{"task", refreshTaskSegmentFacts},
		{"combined", refreshLoginEmailDerivedRows},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, ids := range [][]string{nil, {}} {
				if err := tc.run(context.Background(), nil, ids); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestLoginEmailDerivedRefresh_MaintenanceLocksBeforeMutation(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, mode := range []string{"ShareLock", "ExclusiveLock"} {
		t.Run(mode, func(t *testing.T) {
			truncateTables(t, s)
			var required []string
			for _, table := range []struct{ name, lock string }{
				{"session_overview_rollups", "classid = hashtext('session_overview_rollups')::oid AND objid = 0 AND objsubid = 2"},
				{"plugin_invocation_facts", "objid = hashtext('plugin_invocation_facts_v1')::oid AND objsubid = 1"},
				{"task_segment_facts", "objid = hashtext('task_segment_facts_v1')::oid AND objsubid = 1"},
			} {
				// Each table requires its own maintenance lock and those of all earlier
				// tables: overview -> plugin -> task, for both shared and exclusive paths.
				required = append(required, `EXISTS (SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND mode='`+mode+`' AND `+table.lock+`)`)
				fn := "require_" + table.name + "_lock_order"
				if _, err := s.pool.Exec(ctx, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
     IF NOT (`+strings.Join(required, " AND ")+`) THEN RAISE EXCEPTION 'derived maintenance lock missing or out of order'; END IF;
     RETURN NULL; END $$`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS require_derived_lock_order ON `+table.name)
					_, _ = s.pool.Exec(ctx, `DROP FUNCTION IF EXISTS `+fn+`() `)
				})
				if _, err := s.pool.Exec(ctx, `CREATE TRIGGER require_derived_lock_order BEFORE DELETE OR INSERT ON `+table.name+` FOR EACH STATEMENT EXECUTE FUNCTION `+fn+`() `); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := s.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx) //nolint:errcheck // test transaction
			if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
				t.Fatal(err)
			}
			if mode == "ShareLock" {
				err = refreshLoginEmailDerivedRows(ctx, tx, []string{"touched"})
			} else {
				err = rebuildAllLoginEmailDerivedRows(ctx, tx)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
