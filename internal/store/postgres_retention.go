package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// knownHypertables is the allowlist of tables reconcile may manage. It also
// guards the blast-radius probe below, which is the only place a table name is
// formatted into SQL text rather than passed as a bind parameter.
var knownHypertables = map[string]bool{
	"otel_events":     true,
	"otel_metrics":    true,
	"session_records": true,
}

type retentionTarget struct {
	table string
	days  *int
}

// axisDays returns the configured interval for an axis (matches retentionAxes).
func (cfg RetentionConfig) axisDays(axis string) *int {
	switch axis {
	case "otel":
		return cfg.OtelDays
	case "session":
		return cfg.SessionDays
	default:
		return nil
	}
}

// targets derives (table, days) pairs from retentionAxes so the apply path
// (ReconcileRetention) and the guard path (RetentionPreviewAxis) share ONE
// source of truth for which tables an axis governs — they cannot drift.
func (cfg RetentionConfig) targets() []retentionTarget {
	var t []retentionTarget
	for _, axis := range retentionAxisOrder {
		for _, table := range retentionAxes[axis] {
			t = append(t, retentionTarget{table, cfg.axisDays(axis)})
		}
	}
	return t
}

// ReconcileRetention brings TimescaleDB retention (drop-chunk) policies in line
// with an opt-in RetentionConfig. It is deliberately conservative:
//
//	nil days  -> skip the table (no-op). This is the safety invariant: an unset
//	             env var means a code deploy never changes any retention policy.
//	days < 0  -> skip + warn (guards against a fat-finger value).
//	days == 0 -> ensure NO policy (remove an existing one) = permanent retention.
//	days > 0  -> ensure a policy with exactly that interval (add, or remove+add
//	             to change). Only this path can introduce/shorten deletion, so it
//	             always logs an audit WARNING before applying.
//
// All governed tables are applied in a SINGLE transaction, so an axis change is
// atomic: if any table fails, none is committed (no partial application that the
// PATCH API would report as failure while one policy already changed and its
// async job later drops data). Actual chunk drops run asynchronously in a
// TimescaleDB background job, never synchronously in this call.
func (s *PgStore) ReconcileRetention(ctx context.Context, cfg RetentionConfig) error {
	// Retention jobs delete chunks outside application transactions. Reconcile their
	// already-completed deletions before changing policy state, even when the requested
	// policy itself is a no-op.
	if _, err := s.ReconcileSessionOverviewRollupsForRetention(ctx); err != nil {
		return fmt.Errorf("reconcile session overview retention: %w", err)
	}
	// Phase 1 — plan (reads only): decide per table whether to remove and/or add.
	type retentionPlan struct {
		table   string
		remove  bool // drop the existing policy first
		setDays *int // add a policy with this interval; nil = remove-only (permanent)
	}
	var plans []retentionPlan
	for _, t := range cfg.targets() {
		if t.days == nil {
			continue // opt-in: leave the policy exactly as-is
		}
		if *t.days < 0 {
			log.Printf("[cctraced] retention: %s configured with negative days (%d); ignoring", t.table, *t.days)
			continue
		}
		exists, current, scheduled, err := s.currentRetention(ctx, t.table)
		if err != nil {
			return err
		}
		if *t.days == 0 {
			// 0 = retain forever: remove any existing policy. Keying on exists (not
			// the interval) also removes a policy with a NULL drop_after.
			if exists {
				plans = append(plans, retentionPlan{table: t.table, remove: true})
			}
			continue
		}
		// days > 0: skip only if an identical policy already exists AND is enabled.
		// A matching interval that is scheduled=false would never drop chunks, so
		// re-create it instead of treating it as already-correct.
		if exists && current != nil && scheduled {
			same, err := s.intervalEqualsDays(ctx, *current, *t.days)
			if err != nil {
				return err
			}
			if same {
				continue
			}
		}
		d := *t.days
		plans = append(plans, retentionPlan{table: t.table, remove: exists, setDays: &d})
	}
	if len(plans) == 0 {
		return nil
	}

	// Phase 2 — apply ALL changes in ONE transaction (atomic across the axis). If
	// any statement fails, the tx rolls back and no policy is changed.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	for _, p := range plans {
		if p.remove {
			if _, err := tx.Exec(ctx, `SELECT remove_retention_policy($1, if_exists => true)`, p.table); err != nil {
				return fmt.Errorf("%s remove policy: %w", p.table, err)
			}
		}
		if p.setDays != nil {
			if _, err := tx.Exec(ctx, `SELECT add_retention_policy($1, drop_after => make_interval(days => $2))`, p.table, *p.setDays); err != nil {
				return fmt.Errorf("%s add policy: %w", p.table, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// Phase 3 — audit AFTER commit (drops run later in the async job).
	for _, p := range plans {
		if p.setDays != nil {
			s.logRetentionApplied(ctx, p.table, *p.setDays)
		} else {
			log.Printf("[cctraced] retention: removed policy on %s (now permanent)", p.table)
		}
	}
	return nil
}

// ReconcileSessionOverviewRollupsForRetention repairs sessions whose source rows may
// have been removed by asynchronous Timescale retention jobs. Any dropped row is older
// than its policy cutoff, so a session it touched necessarily has start_time before that
// cutoff. This uses the rollup to find a bounded superset and rebuilds only those session
// IDs transactionally; permanent-retention tables add no candidates.
func scanRetentionJobFinish(row pgx.Row) (*time.Time, error) {
	var finish pgtype.Timestamptz
	if err := row.Scan(&finish); err != nil {
		return nil, err
	}
	// Timescale represents a never-run job as -infinity on some versions. pgx
	// cannot assign that sentinel to time.Time, so preserve the existing unrun=nil
	// semantics explicitly. Positive infinity is likewise not a completed run.
	if !finish.Valid || finish.InfinityModifier != pgtype.Finite {
		return nil, nil
	}
	return &finish.Time, nil
}

func (s *PgStore) ReconcileSessionOverviewRollupsForRetention(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('session_overview_retention'))`); err != nil {
		return 0, err
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}

	type completedRun struct {
		table  string
		finish *time.Time
		oldest *time.Time
		cutoff time.Time
	}
	var completed []completedRun
	for _, table := range []string{"otel_events", "session_records"} {
		exists, _, scheduled, err := s.currentRetention(ctx, table)
		if err != nil {
			return 0, err
		}
		if !exists || !scheduled {
			continue
		}
		days, err := s.policyIntervalDays(ctx, "policy_retention", "drop_after", table)
		if err != nil {
			return 0, err
		}
		if days == nil || *days <= 0 {
			continue
		}
		finish, err := scanRetentionJobFinish(tx.QueryRow(ctx, `SELECT js.last_successful_finish
			FROM timescaledb_information.jobs j
			JOIN timescaledb_information.job_stats js USING (job_id)
			WHERE j.proc_name = 'policy_retention' AND j.hypertable_name = $1`, table))
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return 0, err
			}
			finish = nil
		}
		var oldest *time.Time
		if err := tx.QueryRow(ctx, "SELECT min(ts) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&oldest); err != nil {
			return 0, err
		}
		var recordedFinish, recordedOldest *time.Time
		found := true
		if err := tx.QueryRow(ctx, `SELECT last_run_finished, oldest_source_ts
			FROM session_overview_retention_state WHERE table_name = $1`, table).Scan(&recordedFinish, &recordedOldest); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return 0, err
			}
			found = false
		}
		finishChanged := finish != nil && (recordedFinish == nil || finish.After(*recordedFinish))
		oldestChanged := (oldest == nil) != (recordedOldest == nil) ||
			(oldest != nil && recordedOldest != nil && !oldest.Equal(*recordedOldest))
		if found && !finishChanged && !oldestChanged {
			continue
		}
		completed = append(completed, completedRun{table: table, finish: finish, oldest: oldest, cutoff: time.Now().AddDate(0, 0, -*days)})
	}
	if len(completed) == 0 {
		return 0, tx.Commit(ctx)
	}
	cutoff := completed[0].cutoff
	for _, run := range completed[1:] {
		if run.cutoff.After(cutoff) {
			cutoff = run.cutoff
		}
	}
	ids, err := querySessionIDs(ctx, tx, `SELECT DISTINCT session_id
		FROM session_overview_rollups WHERE start_time < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	// Excluded sessions deliberately have no rollup row, so rollup cutoff candidates
	// alone cannot find their derived rows after Timescale drops the source chunk.
	// Discover imputed orphans directly and include their session IDs in maintenance.
	orphanIDs, err := querySessionIDs(ctx, tx, `
		SELECT i.session_id FROM claude_imputed_cost i
		WHERE i.session_id <> '' AND NOT EXISTS (SELECT 1 FROM session_records sr WHERE sr.id = i.srec_id)
		UNION
		SELECT i.session_id FROM codex_imputed_cost i
		WHERE i.session_id <> '' AND NOT EXISTS (SELECT 1 FROM session_records sr WHERE sr.id = i.srec_id)`)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]struct{}, len(ids)+len(orphanIDs))
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	for _, id := range orphanIDs {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if err := lockSessionOverviewRollups(ctx, tx, ids); err != nil {
		return 0, err
	}
	for _, table := range []string{"claude_imputed_cost", "codex_imputed_cost"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` i WHERE NOT EXISTS (
			SELECT 1 FROM session_records sr WHERE sr.id = i.srec_id)`); err != nil {
			return 0, err
		}
	}
	if err := refreshSessionOverviewRollups(ctx, tx, ids); err != nil {
		return 0, err
	}
	for _, run := range completed {
		if _, err := tx.Exec(ctx, `INSERT INTO session_overview_retention_state
			(table_name, last_run_finished, oldest_source_ts) VALUES ($1, $2, $3)
			ON CONFLICT (table_name) DO UPDATE SET
				last_run_finished = EXCLUDED.last_run_finished,
				oldest_source_ts = EXCLUDED.oldest_source_ts`, run.table, run.finish, run.oldest); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// logRetentionApplied emits the mandatory audit line for a committed policy
// change, attaching blast-radius detail (data now eligible for the async drop
// job) when the probe succeeds.
func (s *PgStore) logRetentionApplied(ctx context.Context, table string, days int) {
	oldest, willDrop, err := s.retentionBlastRadius(ctx, table, days)
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	switch {
	case err != nil:
		log.Printf("[cctraced] retention: applied %dd retention to %s; blast-radius probe failed (%v)", days, table, err)
	case willDrop:
		log.Printf("[cctraced] retention WARNING: applied %dd retention to %s; chunks with data older than %s are now eligible for drop (oldest row: %s)", days, table, cutoff, oldest.Format(time.RFC3339))
	default:
		log.Printf("[cctraced] retention: applied %dd retention to %s; no data older than cutoff %s", days, table, cutoff)
	}
}

// retentionBlastRadius reports the oldest ts in a hypertable and whether it
// predates the cutoff implied by an N-day retention policy (i.e. whether
// applying the policy will drop existing data). willDrop is false for an empty
// table. Uses min(ts) only (time-dimension optimized) to avoid a full scan.
func (s *PgStore) retentionBlastRadius(ctx context.Context, table string, days int) (oldest *time.Time, willDrop bool, err error) {
	if !knownHypertables[table] {
		return nil, false, fmt.Errorf("unknown hypertable %q", table)
	}
	q := "SELECT min(ts) FROM " + pgx.Identifier{table}.Sanitize()
	if err := s.pool.QueryRow(ctx, q).Scan(&oldest); err != nil {
		return nil, false, err
	}
	if oldest == nil {
		return nil, false, nil // empty table, nothing to drop
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	return oldest, oldest.Before(cutoff), nil
}

// retentionAxes is the single source of truth mapping a UI/API axis to the
// hypertables it governs. Both the apply path (RetentionConfig.targets) and the
// guard/preview path (RetentionPreviewAxis) derive from it so they cannot drift.
var retentionAxes = map[string][]string{
	"otel":    {"otel_events", "otel_metrics"},
	"session": {"session_records"},
}

// retentionAxisOrder gives targets() a deterministic table order.
var retentionAxisOrder = []string{"otel", "session"}

// managedHypertables is the ordered list of hypertables under retention
// management, derived from the axis SSOT so RetentionInfo cannot drift from
// reconcile/preview (which also derive from retentionAxes).
func managedHypertables() []string {
	var t []string
	for _, axis := range retentionAxisOrder {
		t = append(t, retentionAxes[axis]...)
	}
	return t
}

// RetentionPreviewAxis dry-runs an N-day retention change for an axis, reporting
// per table the current policy days, oldest ts, and how many rows a decrease or
// introduce would make drop-eligible. An increase or 0 (permanent) is safe, so
// RowsToDrop stays 0 there. Read-only.
func (s *PgStore) RetentionPreviewAxis(ctx context.Context, axis string, days int) ([]*RetentionPreview, error) {
	tables, ok := retentionAxes[axis]
	if !ok {
		return nil, fmt.Errorf("unknown retention axis %q", axis)
	}
	out := make([]*RetentionPreview, 0, len(tables))
	for _, table := range tables {
		p := &RetentionPreview{Table: table, NewDays: days}

		cur, err := s.policyIntervalDays(ctx, "policy_retention", "drop_after", table)
		if err != nil {
			return nil, err
		}
		p.CurrentDays = cur

		oldest, _, err := s.retentionBlastRadius(ctx, table, days)
		if err != nil {
			return nil, err
		}
		p.OldestTs = oldest

		// Only a decrease or an introduce (no current policy) drops NEW data.
		if days > 0 && (cur == nil || days < *cur) {
			cnt, err := s.countOlderThan(ctx, table, days)
			if err != nil {
				return nil, err
			}
			p.RowsToDrop = cnt
		}
		out = append(out, p)
	}
	return out, nil
}

// countOlderThan counts rows in a hypertable older than N days. TimescaleDB
// chunk exclusion limits the scan to chunks predating the cutoff.
func (s *PgStore) countOlderThan(ctx context.Context, table string, days int) (int64, error) {
	if !knownHypertables[table] {
		return 0, fmt.Errorf("unknown hypertable %q", table)
	}
	q := "SELECT count(*) FROM " + pgx.Identifier{table}.Sanitize() + " WHERE ts < now() - make_interval(days => $1)"
	var n int64
	if err := s.pool.QueryRow(ctx, q, days).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// currentRetention reports whether a retention policy exists for a table and,
// if so, its drop_after interval text and whether the job is scheduled. exists
// distinguishes "no policy" from "policy present but with a NULL drop_after"
// (interval==nil) for the days==0 remove path; scheduled lets the days>0 path
// re-enable a policy that has the right interval but was disabled.
func (s *PgStore) currentRetention(ctx context.Context, table string) (exists bool, interval *string, scheduled bool, err error) {
	var iv *string
	var sched *bool
	e := s.pool.QueryRow(ctx, `
		SELECT config->>'drop_after', scheduled
		FROM timescaledb_information.jobs
		WHERE proc_name = 'policy_retention' AND hypertable_name = $1`, table).Scan(&iv, &sched)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil, false, nil
	}
	if e != nil {
		return false, nil, false, fmt.Errorf("query current policy: %w", e)
	}
	return true, iv, sched != nil && *sched, nil
}

// RetentionInfo returns a read-only snapshot of retention/compression policies
// and sizes for the managed hypertables. It never mutates state.
func (s *PgStore) RetentionInfo(ctx context.Context) (*StorageReport, error) {
	rep := &StorageReport{}
	for _, table := range managedHypertables() {
		tr := &TableRetention{Table: table}

		retDays, err := s.policyIntervalDays(ctx, "policy_retention", "drop_after", table)
		if err != nil {
			return nil, fmt.Errorf("%s retention: %w", table, err)
		}
		tr.RetentionDays = retDays

		compDays, err := s.policyIntervalDays(ctx, "policy_compression", "compress_after", table)
		if err != nil {
			return nil, fmt.Errorf("%s compression: %w", table, err)
		}
		tr.CompressionDays = compDays

		// approximate_row_count avoids a full scan; hypertable_size is total bytes.
		if err := s.pool.QueryRow(ctx,
			`SELECT approximate_row_count($1::regclass), hypertable_size($1::regclass)`, table).
			Scan(&tr.RowsApprox, &tr.SizeBytes); err != nil {
			return nil, fmt.Errorf("%s size: %w", table, err)
		}
		rep.Tables = append(rep.Tables, tr)
	}
	return rep, nil
}

// policyIntervalDays returns the interval (whole days) of an ENABLED TimescaleDB
// policy job for a hypertable, or nil when no such active policy exists.
// configKey is the job config field ("drop_after" for retention,
// "compress_after" for compression). A scheduled=false job is deliberately
// treated as absent: it drops/compresses nothing, so surfacing its interval as
// active retention would mislead the admin (and matches reconcile, which treats
// a disabled policy as not-correct).
func (s *PgStore) policyIntervalDays(ctx context.Context, procName, configKey, table string) (*int, error) {
	var d *int
	err := s.pool.QueryRow(ctx, `
		SELECT (EXTRACT(epoch FROM (config->>$1)::interval) / 86400)::int
		FROM timescaledb_information.jobs
		WHERE proc_name = $2 AND hypertable_name = $3 AND scheduled`, configKey, procName, table).Scan(&d)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// intervalEqualsDays reports whether an interval text (e.g. "90 days") equals
// N days, evaluated by Postgres to avoid brittle string parsing. Postgres
// interval equality normalizes months to 30 days; since every policy we create
// uses pure-day intervals this is exact, and a legacy month-based policy that
// compares equal to its day-equivalent is treated as already-correct (benign).
func (s *PgStore) intervalEqualsDays(ctx context.Context, intervalText string, days int) (bool, error) {
	var eq bool
	if err := s.pool.QueryRow(ctx, `SELECT $1::interval = make_interval(days => $2)`, intervalText, days).Scan(&eq); err != nil {
		return false, fmt.Errorf("compare interval: %w", err)
	}
	return eq, nil
}
