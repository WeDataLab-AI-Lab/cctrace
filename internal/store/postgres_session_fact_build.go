package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

func sessionFactBuildMarkers() (string, string) {
	name := fmt.Sprintf("session_facts_r%d_w%d", sessionfacts.RulesVersion, sessionTimelineSourceWitnessVersion)
	return name + "_enqueued", name
}

// enqueueSessionFactBuild records the queue and its marker atomically. Retrying
// after a crash never changes the generation of a previously queued session.
func (s *PgStore) enqueueSessionFactBuild(ctx context.Context) error {
	enqueued, _ := sessionFactBuildMarkers()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	// Across worker restarts the marker and queue commit together. A second
	// server cannot enqueue without first acquiring the worker advisory lock.
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name = $1)`, enqueued).Scan(&done); err != nil {
		return err
	}
	if done {
		return tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `SELECT session_id FROM (
		SELECT session_id FROM session_overview_rollups WHERE scope_type = 'all' AND agent IN ('claude','codex')
		UNION SELECT session_id FROM session_snapshots WHERE agent IN ('claude','codex')
		UNION SELECT session_id FROM session_analysis_facts
		UNION SELECT session_id FROM session_timeline_provenance
	) u WHERE session_id <> '' AND NOT EXISTS (
		SELECT 1 FROM deleted_sessions d WHERE d.session_id = u.session_id)
	ORDER BY session_id COLLATE "C"`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if err := markSessionFactsDirty(ctx, tx, ids, nil, dirtyBuild); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ($1)`, enqueued); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// A gate transition invalidates every published witness, including originals
// that have no copy marker. Include the whole build population in the same
// source-locked transaction so unbuilt and empty sessions are covered as well.
func requeueSessionFactPolicyChange(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT session_id FROM (
		SELECT session_id FROM session_overview_rollups WHERE scope_type='all' AND agent IN ('claude','codex')
		UNION SELECT session_id FROM session_snapshots WHERE agent IN ('claude','codex')
		UNION SELECT session_id FROM session_analysis_facts
		UNION SELECT session_id FROM session_timeline_provenance
		UNION SELECT session_id FROM session_timeline_members
		UNION SELECT session_id FROM session_fact_dirty WHERE reasons & 512<>0
	) u WHERE session_id<>'' AND NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=u.session_id)
	ORDER BY session_id COLLATE "C"`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if err := bumpSessionTimelineSourceRevisions(ctx, tx, ids); err != nil {
		return err
	}
	// The marker itself is a global input even when the population is empty.
	if err := bumpSessionTimelinePolicyEpoch(ctx, tx); err != nil {
		return err
	}
	_, done := sessionFactBuildMarkers()
	if _, err := tx.Exec(ctx, `DELETE FROM schema_backfills WHERE name=$1`, done); err != nil {
		return err
	}
	return queueSessionFactsDirty(ctx, tx, ids, nil, dirtyCopyGate)
}

const sessionFactBuildReasons = 512 | 1024

func (s *PgStore) finishSessionFactBuild(ctx context.Context) error {
	enqueued, done := sessionFactBuildMarkers()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	var built, pending, gate bool
	if err := tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM schema_backfills WHERE name=$1),
		EXISTS(SELECT 1 FROM session_fact_dirty WHERE reasons & $2<>0 AND attempts<10),
		EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1')`, enqueued, sessionFactBuildReasons).Scan(&built, &pending, &gate); err != nil {
		return err
	}
	if !built || pending {
		return tx.Commit(ctx)
	}
	if !gate {
		if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES('session_fact_copies_v1')`); err != nil {
			return err
		}
		if err := requeueSessionFactPolicyChange(ctx, tx); err != nil {
			return err
		}
		// A second pass must observe the gate-on population drained (or failed)
		// before publishing the current version's completion marker.
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, done); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// codexTurnAttributionRequeue marks the one-time rebuild of Codex sessions whose
// stored facts placed user messages by time. Their messages record their own
// turn (metadata.retained_source.id.turn_id), which the loader now reads.
const codexTurnAttributionRequeue = "codex_turn_attribution_v1_enqueued"

// enqueueCodexTurnAttributionRebuild queues those sessions once, at bulk
// priority. The candidates are read before the source lock: the scan walks each
// session's rows and must not hold up ingestion. A session deleted in between is
// skipped by the queue write itself.
func (s *PgStore) enqueueCodexTurnAttributionRebuild(ctx context.Context) error {
	var done bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name = $1)`, codexTurnAttributionRequeue).Scan(&done); err != nil || done {
		return err
	}
	rows, err := s.pool.Query(ctx, `SELECT f.session_id FROM session_analysis_facts f WHERE f.agent = 'codex'
	AND EXISTS (SELECT 1 FROM session_records r WHERE r.session_id = f.session_id AND r.record_type = 'user'
		AND r.raw #> '{metadata,retained_source,id,turn_id}' IS NOT NULL)
	ORDER BY f.session_id COLLATE "C"`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name = $1)`, codexTurnAttributionRequeue).Scan(&done); err != nil || done {
		return err
	}
	if len(ids) > 0 {
		if err := markSessionFactsDirty(ctx, tx, ids, nil, dirtyBuild); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ($1)`, codexTurnAttributionRequeue); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
