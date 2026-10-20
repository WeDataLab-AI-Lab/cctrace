package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

type SessionFactStatus struct {
	Enabled                bool                     `json:"enabled"`
	Mode                   string                   `json:"mode"`
	RulesVersion           int                      `json:"rules_version"`
	Ready                  bool                     `json:"ready"`
	CopyGate               bool                     `json:"copy_gate"`
	WaitingForRequestSet   bool                     `json:"waiting_for_request_set"`
	WaitingForCommitClaims bool                     `json:"waiting_for_commit_claims"`
	Build                  SessionFactBuildStatus   `json:"build"`
	Queue                  SessionFactQueueStatus   `json:"queue"`
	ComputeMS              SessionFactComputeStatus `json:"compute_ms"`
	SkippedLargeGroups     int                      `json:"skipped_large_groups"`
}

type SessionFactBuildStatus struct {
	State          string                     `json:"state"`
	Remaining      int                        `json:"remaining"`
	Failed         int                        `json:"failed"`
	FailedSessions []SessionFactFailedSession `json:"failed_sessions"`
}

type SessionFactFailedSession struct {
	SessionID string `json:"session_id"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
}

type SessionFactQueueStatus struct {
	Depth           int `json:"depth"`
	OldestDirtyAgeS int `json:"oldest_dirty_age_s"`
}

type SessionFactComputeStatus struct {
	P50 int `json:"p50"`
	P99 int `json:"p99"`
}

func (s *PgStore) SessionFactStatus(ctx context.Context) (SessionFactStatus, error) {
	enqueued, done := sessionFactBuildMarkers()
	status := SessionFactStatus{RulesVersion: sessionfacts.RulesVersion, Mode: "build"}
	var enqueuedExists, finished bool
	err := s.pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM schema_backfills WHERE name = $1),
		EXISTS(SELECT 1 FROM schema_backfills WHERE name = $2),
		EXISTS(SELECT 1 FROM schema_backfills WHERE name = 'session_fact_copies_v1'),
		NOT EXISTS(SELECT 1 FROM schema_backfills WHERE name = 'request_set_v1'),
		NOT EXISTS(SELECT 1 FROM schema_backfills WHERE name = 'session_commit_claims_v1'),
		(SELECT count(*) FROM session_fact_dirty WHERE reasons & 1536 <> 0 AND attempts < 10),
		(SELECT count(*) FROM session_fact_dirty WHERE reasons & 1536 <> 0 AND attempts >= 10),
		(SELECT count(*) FROM session_fact_dirty),
		COALESCE((SELECT greatest(0, extract(epoch FROM now() - min(first_dirty_at)))::int FROM session_fact_dirty), 0),
		COALESCE((SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY compute_ms)::int FROM session_analysis_facts), 0),
		COALESCE((SELECT percentile_cont(0.99) WITHIN GROUP (ORDER BY compute_ms)::int FROM session_analysis_facts), 0),
		COALESCE((SELECT count(*) FROM session_analysis_facts WHERE detail->'copy'->>'state'='skipped_large_group'), 0)`,
		enqueued, done).Scan(&enqueuedExists, &finished, &status.CopyGate, &status.WaitingForRequestSet, &status.WaitingForCommitClaims,
		&status.Build.Remaining, &status.Build.Failed, &status.Queue.Depth, &status.Queue.OldestDirtyAgeS,
		&status.ComputeMS.P50, &status.ComputeMS.P99, &status.SkippedLargeGroups)
	if err != nil {
		return SessionFactStatus{}, fmt.Errorf("read session fact status: %w", err)
	}
	status.Ready = enqueuedExists && finished && status.Build.Remaining == 0 && status.CopyGate && !status.WaitingForRequestSet && !status.WaitingForCommitClaims
	if status.Build.Remaining > 0 {
		status.Build.State = "building"
	} else if finished && status.Build.Failed > 0 {
		status.Build.State = "done_with_failures"
	} else if finished {
		status.Build.State = "done"
	} else if enqueuedExists {
		status.Build.State = "building"
	} else {
		status.Build.State = "waiting"
	}
	rows, err := s.pool.Query(ctx, `SELECT session_id, attempts, last_error FROM session_fact_dirty
		WHERE reasons & 1536 <> 0 AND attempts >= 10 ORDER BY session_id LIMIT 20`)
	if err != nil {
		return SessionFactStatus{}, err
	}
	status.Build.FailedSessions, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (SessionFactFailedSession, error) {
		var failed SessionFactFailedSession
		err := row.Scan(&failed.SessionID, &failed.Attempts, &failed.LastError)
		return failed, err
	})
	if err != nil {
		return SessionFactStatus{}, fmt.Errorf("read failed session facts: %w", err)
	}
	if status.Build.FailedSessions == nil {
		status.Build.FailedSessions = []SessionFactFailedSession{}
	}
	return status, nil
}
