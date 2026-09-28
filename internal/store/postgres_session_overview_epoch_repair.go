package store

import "context"

const sessionOverviewEpochStartRepair = "session_overview_epoch_start_v1"

// RepairEpochSessionOverviewStarts rebuilds the overview rows that were aggregated
// before start_time stopped being taken from timestamp-less state records. Those rows
// keep the wrong 1970-01-01 start until something refreshes the session, which for a
// finished session is never, so the correction has to be made once against the rows
// that already exist.
//
// It is scoped to the sessions that actually show the fault -- 979 of 15,114 on the
// production copy -- rather than rebuilding the whole overview, which takes minutes
// there. That keeps it to a single transaction, so the marker and the repaired rows
// commit together: a crash leaves both absent and the next boot retries.
//
// Like the other one-time repairs it belongs off the boot path (see startManagedRepairs
// in cmd/cctraced), and it must run after BackfillSessionOverviewRollups: on a database
// that has no overview yet, that backfill already builds the corrected values and this
// repair correctly finds nothing to do.
func (s *PgStore) RepairEpochSessionOverviewStarts(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, sessionOverviewEpochStartRepair); err != nil {
		return err
	}
	var done bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, sessionOverviewEpochStartRepair).Scan(&done); err != nil {
		return err
	}
	if done {
		return tx.Commit(ctx)
	}
	// The exclusive maintenance lock, then replaceSessionOverviewRollups -- the path a
	// holder of that lock uses. refreshSessionOverviewRollups would instead take one
	// advisory lock per session, and the affected set has no bound: 979 sessions on the
	// production copy, but a larger deployment could exhaust the lock table, fail the
	// repair, leave the marker unwritten and retry the whole thing on every boot. One
	// lock costs concurrent overview maintenance for the length of the repair, which is
	// what the full rebuild in rebuildAllSessionOverviewRollups already spends.
	//
	// It is taken before the read below, because the lock has to precede any relation
	// lock this transaction acquires.
	if err := lockSessionOverviewMaintenance(ctx, tx, true); err != nil {
		return err
	}
	// start_time alone names the affected set: the zero instant is the smallest value
	// any record can carry, so a session is mis-dated exactly when its start sits there.
	// A session whose rows are all state records also reports that start, and rebuilding
	// it simply reproduces the same value.
	ids, err := querySessionIDs(ctx, tx,
		`SELECT DISTINCT session_id FROM session_overview_rollups WHERE start_time = 'epoch'`)
	if err != nil {
		return err
	}
	if err := replaceSessionOverviewRollups(ctx, tx, ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, sessionOverviewEpochStartRepair); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
