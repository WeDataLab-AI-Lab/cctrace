package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// usageHourlyRollupBackfill names the one-shot history scan in schema_backfills,
// so a restart does not re-aggregate every chunk on every boot.
//
// v2 because v1 shipped with the multiplying join below. The marker is what makes
// the backfill exactly-once, so correcting the query is not enough on its own --
// an installation that already recorded v1 would keep the inflated rows forever.
//
// Reads gate on this marker, so bumping it sends every trend read back to the raw
// view until a full rebuild finishes. A change that only adds rows does not need
// that: see usageHourlyRollupWeeklyBackfill.
const usageHourlyRollupBackfill = "usage_hourly_rollups_v2"

// usageHourlyRollupWeeklyBackfill adds the weekly report arm's history to rollups
// that were built before unified_events had it. The periodic refresh only reaches
// back UsageRollupWindow, so older runs would otherwise never enter the aggregate.
// Weekly rows are agent='weekly', so they share no key with the rows already there
// and can be added without rebuilding anything else.
const usageHourlyRollupWeeklyBackfill = "usage_hourly_rollups_weekly_v1"

// UsageRollupWindow is how far back a periodic refresh rebuilds.
//
// Rows do arrive late: session_records land on import rather than at ts, and the
// imputed-cost tables are themselves refreshed on a loop, so a bucket's total is
// not final the moment the hour ends. A trailing window absorbs that without
// rebuilding history every tick. It cannot absorb an arrival older than itself --
// that is what the full refresh is for, and why the backfill is kept re-runnable.
const UsageRollupWindow = 48 * time.Hour

// UsageRollupFreshWindow is the tail a frequent tick rebuilds.
//
// The trend charts read this table now, so whatever the newest bucket is missing
// is missing from the screen. On the 30-minute maintenance pass alone the
// current bucket under-reported by up to half an hour of ingest -- invisible on
// a month, but "today so far" is a number people check against what they just
// did. Rebuilding only the tail keeps that within one dashboard poll without
// re-deriving two days every minute.
const UsageRollupFreshWindow = 2 * time.Hour

// The aggregate is written by DELETE-then-INSERT over a bucket range rather than
// by upsert. An upsert would leave behind rows whose source has since gone --
// a deleted session, a newly excluded account -- and those rows are invisible in
// any total: they simply keep contributing a number nobody can trace.
const usageHourlyRollupDeleteSQL = `DELETE FROM usage_hourly_rollups WHERE bucket >= $1`

const usageHourlyRollupDeleteAllSQL = `DELETE FROM usage_hourly_rollups`

// project_hash is joined in rather than selected: it lives on session_records
// only (visible_events has no such column), and the project filter reaches events
// the same way. A session with no project contributes an empty hash and still
// counts.
//
// DISTINCT ON, not DISTINCT. A session is not guaranteed one project hash --
// measured on a copy of prod, 337 sessions carry more than one, and the rows
// whose session_id is empty carry thirty between them. A plain DISTINCT turns
// those into several join partners and multiplies every event of that session,
// which is silent: the totals stay plausible and only a comparison against the
// raw view shows them. It did exactly that here, inflating event_count by 29,605
// (2,278,972 against 2,249,367) before the reads were ever switched over.
//
// Newest wins, and the empty session_id is excluded outright: it is not a
// session, so it can have no project.
//
// Buckets are UTC hours whatever the session TimeZone, because trend reads cut
// their windows at UTC hours in Go and read the partial ends raw (#768). Prod
// runs with TimeZone=UTC and every stored bucket is on a UTC hour, so pinning it
// changes no existing row.
const usageHourlyRollupInsertSQL = `
INSERT INTO usage_hourly_rollups (
	bucket, model, user_id, profile_email, login_email, user_team, agent,
	billing_provider, project_hash,
	cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, event_count)
SELECT date_trunc('hour', v.ts, 'UTC'),
	COALESCE(v.model, ''), COALESCE(v.user_id, ''), COALESCE(v.profile_email, ''),
	COALESCE(v.login_email, ''), COALESCE(v.user_team, ''), COALESCE(v.agent, ''),
	COALESCE(v.billing_provider, ''), COALESCE(sr.project_hash, ''),
	COALESCE(sum(v.cost_usd), 0)::double precision,
	COALESCE(sum(v.input_tokens), 0)::bigint,
	COALESCE(sum(v.output_tokens), 0)::bigint,
	COALESCE(sum(v.cache_read_tokens), 0)::bigint,
	COALESCE(sum(v.cache_create_tokens), 0)::bigint,
	count(*)::bigint
FROM visible_events v
LEFT JOIN (
	SELECT DISTINCT ON (session_id) session_id, project_hash
	FROM session_records
	WHERE project_hash <> '' AND session_id <> ''
	ORDER BY session_id, ts DESC
) sr ON sr.session_id = v.session_id
%s
GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9`

// RefreshUsageHourlyRollups rebuilds the aggregate.
//
// A nil `from` rebuilds everything; otherwise only buckets at or after the hour
// containing `from`. The caller's instant is floored to its hour because a
// half-rebuilt bucket is worse than a stale one: deleting from the middle of an
// hour and reinserting only that hour's tail would silently drop the earlier part.
func (s *PgStore) RefreshUsageHourlyRollups(ctx context.Context, from *time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := refreshUsageHourlyRollups(ctx, tx, from); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func refreshUsageHourlyRollups(ctx context.Context, tx pgx.Tx, from *time.Time) error {
	// Writers take turns. Each deletes a range and reinserts it, and two
	// overlapping collide on the primary key: the second DELETE cannot see rows
	// the first inserted after its snapshot, so its INSERT fails 23505. The 60s
	// tail, the 48h window, the boot backfill and an exclusion change all write
	// here; inside an exclusion change the collision rolled the change back.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('usage_hourly_rollups_writers'))`); err != nil {
		return fmt.Errorf("lock usage hourly rollups: %w", err)
	}
	if from == nil {
		if _, err := tx.Exec(ctx, usageHourlyRollupDeleteAllSQL); err != nil {
			return fmt.Errorf("delete usage hourly rollups: %w", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(usageHourlyRollupInsertSQL, "")); err != nil {
			return fmt.Errorf("insert usage hourly rollups: %w", err)
		}
		return nil
	}
	start := from.UTC().Truncate(time.Hour)
	if _, err := tx.Exec(ctx, usageHourlyRollupDeleteSQL, start); err != nil {
		return fmt.Errorf("delete usage hourly rollups: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(usageHourlyRollupInsertSQL, "WHERE v.ts >= $1"), start); err != nil {
		return fmt.Errorf("insert usage hourly rollups: %w", err)
	}
	return nil
}

// usageHourlyRollupWeeklyDeleteSQL clears what the weekly backfill is about to
// write, so a re-run replaces its rows rather than colliding with them.
const usageHourlyRollupWeeklyDeleteSQL = `DELETE FROM usage_hourly_rollups WHERE agent = 'weekly' AND bucket < $1`

// weeklyBackfillCutoff is where the weekly backfill stops and the periodic refresh
// takes over. An extra hour below the refresh window keeps the two off the same
// buckets: they do not share a lock, and both inserting one weekly key would fail
// the later transaction on the primary key.
func weeklyBackfillCutoff(now time.Time) time.Time {
	return now.UTC().Add(-UsageRollupWindow - time.Hour).Truncate(time.Hour)
}

// BackfillUsageHourlyRollups builds the aggregate once, off the listener startup
// path: the first run over an existing database scans every chunk.
//
// Marker and data commit together, so a crash leaves both absent (safe to retry)
// or both present (the history scan never repeats). An installation built before
// the weekly arm gets only the weekly rows below the refresh window, under its own
// marker; reads keep using the rollup meanwhile.
func (s *PgStore) BackfillUsageHourlyRollups(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, usageHourlyRollupBackfill); err != nil {
		return err
	}
	built, err := backfillMarked(ctx, tx, usageHourlyRollupBackfill)
	if err != nil {
		return err
	}
	weeklyDone, err := backfillMarked(ctx, tx, usageHourlyRollupWeeklyBackfill)
	if err != nil {
		return err
	}
	if built && weeklyDone {
		return tx.Commit(ctx)
	}
	if !built {
		// A full build reads the weekly arm like any other, so it settles both markers.
		if err := refreshUsageHourlyRollups(ctx, tx, nil); err != nil {
			return err
		}
	} else {
		cutoff := weeklyBackfillCutoff(time.Now())
		if _, err := tx.Exec(ctx, usageHourlyRollupWeeklyDeleteSQL, cutoff); err != nil {
			return fmt.Errorf("delete weekly usage hourly rollups: %w", err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(usageHourlyRollupInsertSQL, "WHERE v.agent = 'weekly' AND v.ts < $1"), cutoff); err != nil {
			return fmt.Errorf("insert weekly usage hourly rollups: %w", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_backfills (name) VALUES ($1), ($2) ON CONFLICT DO NOTHING`,
		usageHourlyRollupBackfill, usageHourlyRollupWeeklyBackfill); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func backfillMarked(ctx context.Context, tx pgx.Tx, name string) (bool, error) {
	var done bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, name).Scan(&done)
	return done, err
}
