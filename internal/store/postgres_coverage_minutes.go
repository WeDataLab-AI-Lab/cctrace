package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// coverageMinutesBackfill names the one-shot history scan in schema_backfills.
const coverageMinutesBackfill = "coverage_measured_minutes_v1"

// CoverageMinutesWindow is how far back a periodic refresh rebuilds.
//
// Wider than the hourly rollup's for one reason: the coverage fit reads a
// 28-day window, so a minute the refresh never revisits stays wrong inside the
// fit for four weeks rather than until it falls off a chart. Late arrivals --
// session_records land on import rather than at ts, and the imputed-cost tables
// are themselves refreshed on a loop -- are the case this absorbs.
const CoverageMinutesWindow = 72 * time.Hour

// CoverageMinutesFreshWindow is the tail a frequent tick rebuilds, for the same
// reason the usage rollup has one: the coverage estimate is read live, and a
// measured side that trails ingest reports coverage lower than it is -- which is
// exactly the direction this estimate is supposed to be trusted in.
const CoverageMinutesFreshWindow = 2 * time.Hour

const coverageMinutesDeleteAllSQL = `DELETE FROM coverage_measured_minutes`
const coverageMinutesDeleteSQL = `DELETE FROM coverage_measured_minutes WHERE minute >= $1`

// Built from visible_events, so the three account-key exclusions it applies
// (excludedAccountPredicateSQL) are baked in at refresh time, into the minutes
// the refresh rewrites. The reader filters excluded_accounts once more, so an
// address excluded today leaves the measured side at once rather than at the next
// refresh -- and it has to, because the readings side excludes it at once too,
// and an estimate whose numerator and denominator disagree about who counts is
// worse than a slow one. Minutes stored before an exclusion stay, hidden at read
// time, until a full recount drops them (step 6 of the P1t transition); minutes a
// refresh dropped come back after the exclusion is lifted only as far back as the
// next refresh reaches, the older ones at the next full recount if their source
// is still there (p1t-design §6.5).
const coverageMinutesInsertSQL = `
INSERT INTO coverage_measured_minutes (login_email, minute, tokens, chart_tokens, cost_usd)
SELECT lower(login_email), date_trunc('minute', ts),
	sum(COALESCE(input_tokens,0) + COALESCE(output_tokens,0)
	  + COALESCE(cache_read_tokens,0) + COALESCE(cache_create_tokens,0))::float8,
	sum(COALESCE(input_tokens,0) + COALESCE(output_tokens,0))::float8,
	COALESCE(sum(cost_usd),0)::float8
FROM visible_events
WHERE billing_provider = 'anthropic'
  AND login_email <> ''
  %s
GROUP BY 1, 2`

// RefreshCoverageMeasuredMinutes rebuilds the pre-aggregated measured side.
//
// A nil `from` rebuilds everything; otherwise only minutes at or after it.
// DELETE-then-INSERT rather than upsert, for the same reason the hourly rollup
// uses it: an upsert leaves behind rows whose source has since gone -- a deleted
// session, a newly excluded one -- and those keep contributing to a total nobody
// can trace back.
func (s *PgStore) RefreshCoverageMeasuredMinutes(ctx context.Context, from *time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := refreshCoverageMeasuredMinutes(ctx, tx, from); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func refreshCoverageMeasuredMinutes(ctx context.Context, tx pgx.Tx, from *time.Time) error {
	// Writers take turns, first thing, as the hourly rollup's do. Each deletes a
	// range and reinserts it, and two overlapping collide on the primary key: the
	// second DELETE cannot see rows the first inserted after its snapshot, so its
	// INSERT fails 23505. The 60s tail, the 72h window, the boot backfill and step 6
	// of the P1t transition all write here.
	if err := lockCoverageMeasuredMinutesWriters(ctx, tx); err != nil {
		return err
	}
	if from == nil {
		if _, err := tx.Exec(ctx, coverageMinutesDeleteAllSQL); err != nil {
			return fmt.Errorf("delete coverage measured minutes: %w", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(coverageMinutesInsertSQL, "")); err != nil {
			return fmt.Errorf("insert coverage measured minutes: %w", err)
		}
		return nil
	}
	start := from.UTC().Truncate(time.Minute)
	if _, err := tx.Exec(ctx, coverageMinutesDeleteSQL, start); err != nil {
		return fmt.Errorf("delete coverage measured minutes: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(coverageMinutesInsertSQL, "AND ts >= $1"), start); err != nil {
		return fmt.Errorf("insert coverage measured minutes: %w", err)
	}
	return nil
}

// lockCoverageMeasuredMinutesWriters takes the lock the writers of
// coverage_measured_minutes take turns on (refreshCoverageMeasuredMinutes says
// why). A key of its own, not usage_hourly_rollups_writers: sharing one would only
// share a database lock for no need (p1t-design §6.5). The periodic ticks call
// coverage right after hourly, so a coverage refresh still starts late whenever
// the hourly one before it waits. It is a transaction-level advisory lock, so a
// transaction that already holds it is granted it again at once.
func lockCoverageMeasuredMinutesWriters(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('coverage_measured_minutes_writers'))`); err != nil {
		return fmt.Errorf("lock coverage measured minutes: %w", err)
	}
	return nil
}

// BackfillCoverageMeasuredMinutes builds the aggregate once, off the listener
// startup path. Marker and data commit together, so a crash leaves both absent
// (safe to retry) or both present (the history scan never repeats).
func (s *PgStore) BackfillCoverageMeasuredMinutes(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, coverageMinutesBackfill); err != nil {
		return err
	}
	var done bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`,
		coverageMinutesBackfill).Scan(&done); err != nil {
		return err
	}
	if done {
		return tx.Commit(ctx)
	}
	if err := refreshCoverageMeasuredMinutes(ctx, tx, nil); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, coverageMinutesBackfill); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// coverageMinutesReady reports whether the one-shot backfill has finished.
//
// Until it has, the reader falls back to aggregating raw events. A half-built
// table would not fail -- it would quietly report less measured usage than there
// was, which is exactly the shape of answer this estimate is supposed to expose
// rather than produce.
func (s *PgStore) coverageMinutesReady(ctx context.Context) bool {
	var ready bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`,
		coverageMinutesBackfill).Scan(&ready); err != nil {
		return false
	}
	return ready
}
