package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// loginEmailHistoryBackfill identifies the one-time full-history login-email repair.
const loginEmailHistoryBackfill = "session_records_login_email_history_v1"

// loginEmailHistoryBatchSize bounds source rows changed by one committed repair
// transaction. Derived rows for at most this many source rows' sessions are rebuilt
// in the same transaction, so source, derived data, and progress never disagree.
const loginEmailHistoryBatchSize = 500

// Each window/sort node may consume up to work_mem, and parallel workers multiply
// that allowance. The snapshot intentionally trades temporary-file I/O for a
// predictable memory ceiling: it runs once, off the listener startup path.
const loginEmailHistorySnapshotWorkMem = "4MB"
const loginEmailHistorySnapshotParallelWorkers = "0"

type loginEmailHistoryBatchResult struct {
	SourceUpdated    int64
	InferenceUpdated int64
	Complete         bool
}

// BackfillSessionRecordLoginEmailHistoryOnce repairs retained history once without
// holding one transaction over the session_records hypertable. A repeatable-read
// setup transaction snapshots the complete session and user OTEL timelines into
// durable interval tables. Subsequent transactions apply at most one bounded batch,
// refresh derived data for exactly the touched sessions, and advance durable
// progress atomically. The completion marker is inserted only after both phases
// have no candidates left.
func (s *PgStore) BackfillSessionRecordLoginEmailHistoryOnce(ctx context.Context) (int64, int64, error) {
	for {
		// Acquire the repair lock per committed batch, not for the whole repair. A
		// strict deletion queued between batches can then cancel all staging and the
		// next iteration safely snapshots again without the deleted session.
		result, err := s.backfillSessionRecordLoginEmailHistoryBatch(ctx, loginEmailHistoryBatchSize)
		if err != nil {
			return 0, 0, err
		}
		if result.Complete {
			return result.SourceUpdated, result.InferenceUpdated, nil
		}
	}
}

// backfillSessionRecordLoginEmailHistoryBatch is intentionally package-private:
// integration tests use it to stop at deterministic commit boundaries and prove
// resume behavior. Production drains all batches through the public method above.
func (s *PgStore) backfillSessionRecordLoginEmailHistoryBatch(ctx context.Context, batchSize int) (loginEmailHistoryBatchResult, error) {
	if batchSize <= 0 {
		return loginEmailHistoryBatchResult{}, fmt.Errorf("login_email history batch size must be positive")
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return loginEmailHistoryBatchResult{}, err
	}
	defer conn.Release()
	if err := lockLoginEmailHistoryRepair(ctx, conn); err != nil {
		return loginEmailHistoryBatchResult{}, err
	}
	defer unlockLoginEmailHistoryRepair(conn)
	if _, err := ensureLoginEmailHistorySnapshot(ctx, conn); err != nil {
		return loginEmailHistoryBatchResult{}, err
	}
	return runLoginEmailHistoryBatch(ctx, conn, batchSize)
}

func lockLoginEmailHistoryRepair(ctx context.Context, conn *pgxpool.Conn) error {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, loginEmailHistoryBackfill); err != nil {
		return fmt.Errorf("lock login_email history repair: %w", err)
	}
	return nil
}

func unlockLoginEmailHistoryRepair(conn *pgxpool.Conn) {
	// Use a fresh context: cancellation of the caller must not return a pooled
	// connection while it still owns a session advisory lock.
	_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, loginEmailHistoryBackfill)
}

func setLoginEmailHistorySnapshotResourceLimits(ctx context.Context, tx pgx.Tx) error {
	// set_config(..., true) is the parameterized form of SET LOCAL: both values
	// disappear at commit/rollback and cannot leak through the pooled connection.
	if _, err := tx.Exec(ctx, `SELECT
		set_config('work_mem', $1, true),
		set_config('max_parallel_workers_per_gather', $2, true)`,
		loginEmailHistorySnapshotWorkMem, loginEmailHistorySnapshotParallelWorkers); err != nil {
		return fmt.Errorf("limit login_email history snapshot resources: %w", err)
	}
	return nil
}

// ensureLoginEmailHistorySnapshot creates both immutable timelines and the target
// high-water mark in one repeatable-read transaction. A crash either leaves all of
// the snapshot durable or none of it; a retry never derives intervals from a later
// (possibly retention-truncated) source timeline.
func ensureLoginEmailHistorySnapshot(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
	var done bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, loginEmailHistoryBackfill).Scan(&done); err != nil {
		return false, fmt.Errorf("read login_email history marker: %w", err)
	}
	if done {
		return false, nil
	}
	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM login_email_history_repair_state WHERE name = $1)`, loginEmailHistoryBackfill).Scan(&exists); err != nil {
		return false, fmt.Errorf("read login_email history progress: %w", err)
	}
	if exists {
		return false, nil
	}

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := setLoginEmailHistorySnapshotResourceLimits(ctx, tx); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO login_email_history_repair_state (name, phase, max_record_id)
		SELECT $1, 'source', COALESCE(MAX(id), 0) FROM session_records`, loginEmailHistoryBackfill); err != nil {
		return false, fmt.Errorf("create login_email history progress: %w", err)
	}
	if _, err := tx.Exec(ctx, backfillEdgesFullCTE+backfillEdgesCTESuffix+`
		INSERT INTO login_email_history_session_intervals
			(repair_name, session_id, interval_no, login_email, from_ts, to_ts, first_interval)
		SELECT $1, session_id, rn, login_email, from_ts, to_ts, rn = 1
		FROM edges`, loginEmailHistoryBackfill); err != nil {
		return false, fmt.Errorf("snapshot session login_email timeline: %w", err)
	}
	if _, err := tx.Exec(ctx, inferEdgesFullCTE+inferEdgesCTESuffix+`
		INSERT INTO login_email_history_user_intervals
			(repair_name, user_id, interval_no, login_email, from_ts, last_obs_ts,
			 to_ts, next_login_email, pick_next)
		SELECT $1, user_id,
		       ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY from_ts, login_email),
		       login_email, from_ts, last_obs_ts, to_ts, next_login_email,
		       COALESCE(pick_next, false)
		FROM intervals`, loginEmailHistoryBackfill); err != nil {
		return false, fmt.Errorf("snapshot user login_email timeline: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func runLoginEmailHistoryBatch(ctx context.Context, conn *pgxpool.Conn, batchSize int) (loginEmailHistoryBatchResult, error) {
	if batchSize <= 0 {
		return loginEmailHistoryBatchResult{}, fmt.Errorf("login_email history batch size must be positive")
	}
	var done bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, loginEmailHistoryBackfill).Scan(&done); err != nil {
		return loginEmailHistoryBatchResult{}, err
	}
	if done {
		return loginEmailHistoryBatchResult{Complete: true}, nil
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return loginEmailHistoryBatchResult{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return loginEmailHistoryBatchResult{}, err
	}

	var phase string
	var maxID, sourceCursor, inferenceCursor, sourceTotal, inferenceTotal int64
	if err := tx.QueryRow(ctx, `SELECT phase, max_record_id, source_cursor, inference_cursor,
			source_updated, inference_updated
		FROM login_email_history_repair_state WHERE name = $1 FOR UPDATE`, loginEmailHistoryBackfill).
		Scan(&phase, &maxID, &sourceCursor, &inferenceCursor, &sourceTotal, &inferenceTotal); err != nil {
		return loginEmailHistoryBatchResult{}, fmt.Errorf("lock login_email history progress: %w", err)
	}

	var cursor, updated int64
	var sessionIDs []string
	switch phase {
	case "source":
		cursor, updated, sessionIDs, err = applyLoginEmailHistorySourceBatch(ctx, tx, maxID, sourceCursor, batchSize)
	case "inference":
		cursor, updated, sessionIDs, err = applyLoginEmailHistoryInferenceBatch(ctx, tx, maxID, inferenceCursor, batchSize)
	default:
		err = fmt.Errorf("unknown login_email history phase %q", phase)
	}
	if err != nil {
		return loginEmailHistoryBatchResult{}, err
	}
	if updated > 0 {
		if err := refreshLoginEmailDerivedRows(ctx, tx, sessionIDs); err != nil {
			return loginEmailHistoryBatchResult{}, err
		}
	}

	if cursor != 0 {
		if phase == "source" {
			sourceTotal += updated
			_, err = tx.Exec(ctx, `UPDATE login_email_history_repair_state
				SET source_cursor = $2, source_updated = $3, updated_at = now() WHERE name = $1`,
				loginEmailHistoryBackfill, cursor, sourceTotal)
		} else {
			inferenceTotal += updated
			_, err = tx.Exec(ctx, `UPDATE login_email_history_repair_state
				SET inference_cursor = $2, inference_updated = $3, updated_at = now() WHERE name = $1`,
				loginEmailHistoryBackfill, cursor, inferenceTotal)
		}
		if err != nil {
			return loginEmailHistoryBatchResult{}, fmt.Errorf("advance login_email history progress: %w", err)
		}
	} else if phase == "source" {
		if _, err := tx.Exec(ctx, `UPDATE login_email_history_repair_state
			SET phase = 'inference', updated_at = now() WHERE name = $1`, loginEmailHistoryBackfill); err != nil {
			return loginEmailHistoryBatchResult{}, fmt.Errorf("advance login_email history phase: %w", err)
		}
	} else {
		// No inference candidate remains at or below the snapshotted high-water mark.
		// Marker insertion and staging cleanup are one final transaction: a failed
		// marker leaves progress and intervals intact and therefore resumable.
		if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, loginEmailHistoryBackfill); err != nil {
			return loginEmailHistoryBatchResult{}, fmt.Errorf("record login_email history marker: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM login_email_history_repair_state WHERE name = $1`, loginEmailHistoryBackfill); err != nil {
			return loginEmailHistoryBatchResult{}, fmt.Errorf("clean login_email history staging: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return loginEmailHistoryBatchResult{}, err
		}
		return loginEmailHistoryBatchResult{SourceUpdated: sourceTotal, InferenceUpdated: inferenceTotal, Complete: true}, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return loginEmailHistoryBatchResult{}, err
	}
	return loginEmailHistoryBatchResult{SourceUpdated: sourceTotal, InferenceUpdated: inferenceTotal}, nil
}

func applyLoginEmailHistorySourceBatch(ctx context.Context, tx pgx.Tx, maxID, cursor int64, batchSize int) (int64, int64, []string, error) {
	return applyLoginEmailHistoryBatch(ctx, tx, loginEmailHistoryBackfill, `WITH candidates AS MATERIALIZED (
		SELECT sr.id, sr.session_id, i.login_email
		FROM session_records sr
		JOIN login_email_history_session_intervals i
		  ON i.repair_name = $1 AND i.session_id = sr.session_id
		 AND (i.first_interval OR sr.ts >= i.from_ts)
		 AND sr.ts < COALESCE(i.to_ts, 'infinity'::timestamptz)
		WHERE sr.id > $2 AND sr.id <= $3 AND sr.session_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
		  AND (sr.login_email = '' OR sr.login_email_source = 'inferred')
		ORDER BY sr.id
		LIMIT $4
	), updated AS (
		UPDATE session_records sr
		SET login_email = c.login_email, login_email_source = 'otel'
		FROM candidates c
		WHERE sr.id = c.id AND (sr.login_email = '' OR sr.login_email_source = 'inferred')
		RETURNING sr.id, sr.session_id
	)
	SELECT COALESCE((SELECT MAX(id) FROM candidates), 0), COUNT(*),
	       COALESCE(array_agg(DISTINCT session_id), ARRAY[]::text[])
	FROM updated`, maxID, cursor, batchSize)
}

// loginEmailHistoryInferenceSQL is the one-time repair's copy of the periodic
// inference pass. It is a literal duplicate of inferMatch's predicate against a
// staged interval table rather than a live CTE, so nothing at runtime forces the
// two to agree -- postgres_backfill_sql_shape_test.go is what does. It is a named
// constant only so that test can read it.
//
// The codex exclusion is here for the same reason it is in inferMatch, and
// omitting it would matter more here: a database that has not run this repair yet
// runs it before the periodic pass ever fires, so it would write #524's wrong
// attribution across all of retained history in one go.
const loginEmailHistoryInferenceSQL = `WITH candidates AS MATERIALIZED (
		SELECT sr.id, sr.session_id,
		       CASE WHEN i.pick_next AND sr.ts > i.last_obs_ts
		            THEN i.next_login_email ELSE i.login_email END AS login_email
		FROM session_records sr
		JOIN login_email_history_user_intervals i
		  ON i.repair_name = $1 AND i.user_id = sr.user_id
		 AND sr.ts >= i.from_ts
		 AND sr.ts < COALESCE(i.to_ts, 'infinity'::timestamptz)
		WHERE sr.id > $2 AND sr.id <= $3 AND sr.user_id <> '' AND sr.login_email = ''
		  AND COALESCE(NULLIF(sr.agent, ''), 'claude') <> 'codex'
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
		ORDER BY sr.id
		LIMIT $4
	), updated AS (
		UPDATE session_records sr
		SET login_email = c.login_email, login_email_source = 'inferred'
		FROM candidates c
		WHERE sr.id = c.id AND sr.login_email = ''
		RETURNING sr.id, sr.session_id
	)
	SELECT COALESCE((SELECT MAX(id) FROM candidates), 0), COUNT(*),
	       COALESCE(array_agg(DISTINCT session_id), ARRAY[]::text[])
	FROM updated`

func applyLoginEmailHistoryInferenceBatch(ctx context.Context, tx pgx.Tx, maxID, cursor int64, batchSize int) (int64, int64, []string, error) {
	return applyLoginEmailHistoryBatch(ctx, tx, loginEmailHistoryBackfill, loginEmailHistoryInferenceSQL, maxID, cursor, batchSize)
}

// applyLoginEmailHistoryBatch takes repairName rather than closing over
// loginEmailHistoryBackfill: the staged-batch shape -- bounded candidate set,
// cursor returned with the touched sessions -- is reused by other one-time repairs
// keyed on their own marker.
func applyLoginEmailHistoryBatch(ctx context.Context, tx pgx.Tx, repairName, query string, maxID, cursor int64, batchSize int) (int64, int64, []string, error) {
	var nextCursor, updated int64
	var sessionIDs []string
	if err := tx.QueryRow(ctx, query, repairName, cursor, maxID, batchSize).
		Scan(&nextCursor, &updated, &sessionIDs); err != nil {
		return 0, 0, nil, err
	}
	return nextCursor, updated, sessionIDs, nil
}
