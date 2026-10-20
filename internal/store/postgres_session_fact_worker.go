package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cctrace/internal/sessionfacts"
)

const sessionFactWorkerLock = "session_facts_worker"

type SessionFactRun struct {
	SessionID string
	ComputeMS int
	Outcome   string
	Attempts  int
}

// RunSessionFactWorker holds one session-level advisory lock on a dedicated
// connection. Other instances do not compute concurrently with this one.
func (s *PgStore) RunSessionFactWorker(ctx context.Context, after func(time.Duration) <-chan time.Time, report func(SessionFactRun, error)) {
	for ctx.Err() == nil {
		conn, err := s.pool.Acquire(ctx)
		if err == nil {
			var locked bool
			err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, sessionFactWorkerLock).Scan(&locked)
			if err == nil && locked {
				s.runSessionFactLocked(ctx, conn, after, report)
				unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext($1))`, sessionFactWorkerLock)
				cancel()
			} else if err != nil && ctx.Err() == nil {
				report(SessionFactRun{Outcome: "worker"}, err)
			}
			conn.Release()
		}
		select {
		case <-ctx.Done():
			return
		case <-after(30 * time.Second):
		}
	}
}

func (s *PgStore) runSessionFactLocked(ctx context.Context, conn *pgxpool.Conn, after func(time.Duration) <-chan time.Time, report func(SessionFactRun, error)) {
	// Prerequisite preparation is serialized by the worker advisory lock. Each
	// claim batch retains its existing bounded transaction budget and durable
	// coverage cursor, so a restart resumes without enqueuing a partial build.
	prepared := false
	for ctx.Err() == nil {
		if err := conn.Ping(ctx); err != nil {
			report(SessionFactRun{Outcome: "worker"}, err)
			return
		}
		if !prepared {
			var ready bool
			err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name='request_set_v1')
				AND EXISTS(SELECT 1 FROM schema_backfills WHERE name=$1)`, sessionOverviewRollupBackfill).Scan(&ready)
			if err == nil && ready {
				ready, err = s.BackfillSessionCommitClaims(ctx, 100)
				if err == nil && ready {
					err = s.enqueueSessionFactBuild(ctx)
					if err == nil {
						err = s.enqueueCodexTurnAttributionRebuild(ctx)
					}
					prepared = err == nil
				} else if err == nil {
					continue
				}
			}
			if err != nil && ctx.Err() == nil {
				report(SessionFactRun{Outcome: "prepare"}, err)
			}
			if !prepared {
				select {
				case <-ctx.Done():
					return
				case <-after(30 * time.Second):
				}
				continue
			}
		}
		id, generation, err := s.pickDueSessionFact(ctx)
		if err == nil {
			run, runErr := s.processSessionFact(ctx, id, generation)
			if runErr == nil {
				if sessionFactAborted(run.Outcome) {
					if deferErr := s.deferSessionFact(ctx, id, generation); deferErr != nil {
						report(run, deferErr)
					} else {
						report(run, nil)
					}
				} else {
					report(run, nil)
				}
			} else if ctx.Err() == nil {
				if attempts, failureErr := s.failSessionFact(ctx, id, generation, runErr); failureErr == nil {
					run.Attempts = attempts
					if attempts > 0 && (attempts == 1 || attempts&(attempts-1) == 0) {
						report(run, runErr)
					}
				} else {
					report(run, failureErr)
				}
			}
			if ctx.Err() == nil {
				if err := s.finishSessionFactBuild(ctx); err != nil {
					report(SessionFactRun{Outcome: "finish"}, err)
				}
			}
			continue
		}
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		if err != nil && ctx.Err() == nil {
			report(SessionFactRun{Outcome: "worker"}, err)
		}
		if err == nil {
			if finishErr := s.finishSessionFactBuild(ctx); finishErr != nil && ctx.Err() == nil {
				report(SessionFactRun{Outcome: "finish"}, finishErr)
			}
		}
		wait := 2 * time.Second
		if err != nil {
			wait = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-after(wait):
		}
	}
}

func sessionFactAborted(outcome string) bool {
	switch outcome {
	case "stale", "removal", "retention", "copy_gate", "excluded", "identity", "source", "timeline_source", "commit_claims_source", "copy_source":
		return true
	}
	return false
}

// Preserve the existing reasons, scan watermark, failures and later retry when
// an optimistic publish check aborts; another due session can now make progress.
func (s *PgStore) deferSessionFact(ctx context.Context, id string, generation int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE session_fact_dirty SET
		not_before = GREATEST(not_before, now() + interval '30 seconds'),
		retry_at = GREATEST(COALESCE(retry_at, now()), now() + interval '30 seconds')
		WHERE session_id = $1 AND generation = $2`, id, generation)
	return err
}

func (s *PgStore) pickDueSessionFact(ctx context.Context) (string, int64, error) {
	var id string
	var generation int64
	err := s.pool.QueryRow(ctx, `SELECT session_id, generation FROM session_fact_dirty
		WHERE not_before <= now() AND (retry_at IS NULL OR retry_at <= now())
		ORDER BY priority, first_dirty_at, session_id LIMIT 1`).Scan(&id, &generation)
	return id, generation, err
}

var sessionFactAgentSQL = `SELECT agent FROM (
	SELECT COALESCE(NULLIF(agent,''),'claude') AS agent FROM visible_session_records WHERE session_id = $1
	UNION ALL SELECT agent FROM visible_events WHERE session_id = $1 AND event_name = 'api_request'
		AND NOT EXISTS (SELECT 1 FROM excluded_sessions WHERE session_id = $1)
	UNION ALL SELECT ss.agent FROM session_snapshots ss WHERE ` + sessionFactSnapshotVisibleSQL + `
) a ORDER BY CASE WHEN agent IN ('claude','codex') THEN 0 ELSE 1 END, agent COLLATE "C" LIMIT 1`

func (s *PgStore) processSessionFact(ctx context.Context, id string, generation int64) (SessionFactRun, error) {
	return s.processSessionFactWithSnapshotCompute(ctx, id, generation, s.computeSessionFactSnapshot)
}

// sessionFactTimeline carries the timeline and P4 rows from one computation.
// HasTimeline is false for an r2-only or fallback result, which leaves the
// mapping absent: not computed, not empty.
type sessionFactTimeline struct {
	Facts       sessionfacts.TimelineFacts
	HasTimeline bool
	Commits     sessionfacts.CommitFacts
	Tasks       sessionfacts.TaskFacts
	Hash        []byte
}

type sessionFactSnapshotCompute func(context.Context, string, string) (SessionFactLoad, sessionfacts.TimelineCommitOutput, error)

// A function argument keeps failure/cancellation tests local to their run;
// production always computes the entire session in the batched snapshot reader.
func (s *PgStore) processSessionFactWithSnapshotCompute(ctx context.Context, id string, generation int64, compute sessionFactSnapshotCompute) (SessionFactRun, error) {
	run := SessionFactRun{SessionID: id}
	var agent string
	err := s.pool.QueryRow(ctx, sessionFactAgentSQL, id).Scan(&agent)
	if errors.Is(err, pgx.ErrNoRows) {
		agent = ""
	} else if err != nil {
		return run, fmt.Errorf("select agent: %w", err)
	}
	if agent != "claude" && agent != "codex" {
		// copy_only also means no supported visible source; it does not claim
		// that the complete session was copied from another session.
		run.Outcome, err = s.processSessionFactCopyOnly(ctx, id, generation)
		return run, err
	}
	start := time.Now()
	load, output, err := compute(ctx, id, agent)
	if err != nil {
		return run, err
	}
	if err := ctx.Err(); err != nil {
		return run, err
	}
	if load.Generation != generation || load.SourceAgent != agent {
		run.Outcome = "stale"
		return run, nil
	}
	if load.Truncated != nil || output.Session.Coverage == "truncated" {
		return run, errors.New("incomplete session fact computation")
	}
	if len(load.LoadedKeys) == 0 && len(load.Input.Rows) == 0 && len(load.Input.CodexRows) == 0 && !load.Input.HasOTEL && len(load.Input.OTEL) == 0 && len(load.Input.Snapshots) == 0 {
		run.Outcome, err = s.publishEmptySessionFact(ctx, id, generation)
		return run, err
	}
	timeline := &sessionFactTimeline{
		Facts: output.Timeline, HasTimeline: true, Commits: output.Commits, Tasks: output.Tasks, Hash: output.TimelineHash,
	}
	run.ComputeMS = int(time.Since(start).Milliseconds())
	run.Outcome, err = s.publishSessionFactWithTimeline(ctx, id, generation, load, output.Output, timeline, run.ComputeMS)
	return run, err
}

func (s *PgStore) failSessionFact(ctx context.Context, id string, generation int64, cause error) (int, error) {
	// Never keep source data or identity in a queue error that can outlive deletion.
	stage := "compute_or_publish"
	if errors.Is(cause, context.DeadlineExceeded) {
		stage = "timeout"
	}
	var attempts int
	err := s.pool.QueryRow(ctx, `UPDATE session_fact_dirty SET attempts = attempts + 1,
		retry_at = now() + make_interval(secs => LEAST(5.0 * power(2.0, LEAST(attempts, 10)), 3600.0)),
		last_error = $3 WHERE session_id = $1 AND generation = $2 RETURNING attempts`, id, generation, stage).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return attempts, err
}
