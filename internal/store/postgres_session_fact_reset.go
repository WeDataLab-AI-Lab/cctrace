package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrSessionFactsWorkerActive = errors.New("session facts worker is active; stop cctraced before reset")

type SessionFactsResetReport struct {
	CopyGateClosed bool  `json:"copy_gate_closed"`
	AnalysisRows   int64 `json:"analysis_rows"`
	TurnRows       int64 `json:"turn_rows"`
	ActivityRows   int64 `json:"activity_rows"`
	CommitRows     int64 `json:"commit_rows"`
	TaskRows       int64 `json:"task_rows"`
	CopyRows       int64 `json:"copy_rows"`
	TimelineRows   int64 `json:"timeline_rows"`
	DirtyRows      int64 `json:"dirty_rows"`
	BuildMarkers   int64 `json:"build_markers"`
}

// ResetSessionFacts closes the shared copy predicate, rebuilds derived values
// under that policy, and clears P1b facts while holding the worker's session
// advisory lock. It is safe to retry after a failed phase.
func (s *PgStore) ResetSessionFacts(ctx context.Context) (SessionFactsResetReport, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return SessionFactsResetReport{}, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1))`, sessionFactWorkerLock).Scan(&locked); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("acquire session facts worker lock: %w", err)
	}
	if !locked {
		return SessionFactsResetReport{}, ErrSessionFactsWorkerActive
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext($1))`, sessionFactWorkerLock)
	}()

	if err := s.closeSessionFactCopyGate(ctx); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("close copy gate: %w", err)
	}
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("rebuild Claude imputed cost: %w", err)
	}
	if err := s.RefreshCodexImputedCost(ctx); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("rebuild Codex imputed cost: %w", err)
	}
	if err := s.runSessionFactsResetTx(ctx, rebuildAllSessionOverviewRollups); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("rebuild session overview rollups: %w", err)
	}
	if err := s.runSessionFactsResetTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return refreshUsageHourlyRollups(ctx, tx, nil)
	}); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("rebuild hourly rollups: %w", err)
	}
	if err := s.runSessionFactsResetTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return refreshCoverageMeasuredMinutes(ctx, tx, nil)
	}); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("rebuild coverage minutes: %w", err)
	}
	if err := s.runSessionFactsResetTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return rebuildAllPluginInvocationFacts(ctx, tx)
	}); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("rebuild plugin invocation facts: %w", err)
	}
	if err := s.prepareSessionFactTokenRecount(ctx); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("prepare task segment token recount: %w", err)
	}
	if _, err := s.recountTaskSegmentTokensStep(ctx); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("recount task segment tokens: %w", err)
	}
	if err := s.clearSessionFactsForReset(ctx); err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("clear session facts: %w", err)
	}
	report, err := s.sessionFactsResetCounts(ctx)
	if err != nil {
		return SessionFactsResetReport{}, err
	}
	if report.AnalysisRows != 0 || report.TurnRows != 0 || report.ActivityRows != 0 || report.CommitRows != 0 ||
		report.TaskRows != 0 || report.CopyRows != 0 || report.TimelineRows != 0 || report.DirtyRows != 0 || report.BuildMarkers != 0 || !report.CopyGateClosed {
		return report, errors.New("session facts reset verification found remaining rows or markers")
	}
	return report, nil
}

func (s *PgStore) closeSessionFactCopyGate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) runSessionFactsResetTx(ctx context.Context, run func(context.Context, pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	if err := run(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) prepareSessionFactTokenRecount(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	if err := lockTaskSegmentFacts(ctx, tx, false); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM schema_backfills WHERE name=$1`, taskSegmentTokensRecount); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_segment_tokens_cursor`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) clearSessionFactsForReset(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `DELETE FROM session_turn_facts`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_activity_segments`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_commit_facts`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_task_facts`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_analysis_facts`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_timeline_members`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_timeline_provenance`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_timeline_revisions`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_timeline_policy_epoch`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_record_copies`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_fact_dirty`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM schema_backfills WHERE name LIKE 'session_facts_r%'`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) sessionFactsResetCounts(ctx context.Context) (SessionFactsResetReport, error) {
	var report SessionFactsResetReport
	err := s.pool.QueryRow(ctx, `SELECT
		NOT EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1'),
		(SELECT count(*) FROM session_analysis_facts),
		(SELECT count(*) FROM session_turn_facts),
		(SELECT count(*) FROM session_activity_segments),
		(SELECT count(*) FROM session_commit_facts),
		(SELECT count(*) FROM session_task_facts),
		(SELECT count(*) FROM session_record_copies),
		(SELECT (SELECT count(*) FROM session_timeline_members)+(SELECT count(*) FROM session_timeline_provenance)+
			(SELECT count(*) FROM session_timeline_revisions)+(SELECT count(*) FROM session_timeline_policy_epoch)),
		(SELECT count(*) FROM session_fact_dirty),
		(SELECT count(*) FROM schema_backfills WHERE name LIKE 'session_facts_r%')`).Scan(
		&report.CopyGateClosed, &report.AnalysisRows, &report.TurnRows, &report.ActivityRows,
		&report.CommitRows, &report.TaskRows, &report.CopyRows, &report.TimelineRows, &report.DirtyRows, &report.BuildMarkers)
	if err != nil {
		return SessionFactsResetReport{}, fmt.Errorf("verify session facts reset: %w", err)
	}
	return report, nil
}
