package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const aiRunColumns = `id, dashboard_user_id, week, tz, since, until,
 scope_profile_email, scope_user_id, started_by, status, error_code, error_message,
 runtime, model, auth_mode, tokens_reported, input_tokens, cached_input_tokens,
 output_tokens, items_dropped, started_at, finished_at`

// Unreported usage stays SQL NULL, including when the caller carries stale counts.
func aiTokenValues(u AIUsage) (any, any, any) {
	if !u.Reported {
		return nil, nil, nil
	}
	return u.InputTokens, u.CachedInputTokens, u.OutputTokens
}

func (s *PgStore) CreateAIReportRun(ctx context.Context, r *AIReportRun) (int64, error) {
	in, cached, out := aiTokenValues(r.Usage)
	status := r.Status
	if status == "" {
		status = AIRunRunning
	}
	// A caller that says nothing started it by hand -- every path but the
	// scheduler, and every row written before the column existed.
	startedBy := r.StartedBy
	if startedBy == "" {
		startedBy = AIRunStartedByManual
	}
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO ai_report_runs
 (dashboard_user_id,week,tz,since,until,scope_profile_email,scope_user_id,started_by,status,runtime,model,auth_mode,tokens_reported,input_tokens,cached_input_tokens,output_tokens)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING id`,
		r.DashboardUserID, r.Week, r.TZ, r.Since, r.Until, r.ScopeProfileEmail, r.ScopeUserID, startedBy, status, r.Runtime, r.Model, r.AuthMode, r.Usage.Reported, in, cached, out).Scan(&id)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "uq_ai_report_runs_one_running" {
		return 0, ErrAIRunAlreadyRunning
	}
	return id, err
}

func (s *PgStore) AppendAIToolCall(ctx context.Context, runID int64, c AIToolCall) error {
	args := c.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	status := c.Status
	if status == "" {
		status = AIToolCallRunning
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO ai_report_tool_calls (run_id,seq,tool,args,status) VALUES ($1,$2,$3,$4,$5)`, runID, c.Seq, c.Tool, args, status)
	return err
}

func (s *PgStore) FinishAIToolCall(ctx context.Context, runID int64, seq int, status string, rows, bytes, durMs int) error {
	_, err := s.pool.Exec(ctx, `UPDATE ai_report_tool_calls SET status=$3,result_rows=$4,result_bytes=$5,duration_ms=$6 WHERE run_id=$1 AND seq=$2`, runID, seq, status, rows, bytes, durMs)
	return err
}

func (s *PgStore) UpdateAIRunUsage(ctx context.Context, runID int64, u AIUsage) error {
	in, cached, out := aiTokenValues(u)
	_, err := s.pool.Exec(ctx, `UPDATE ai_report_runs SET tokens_reported=$2,input_tokens=$3,cached_input_tokens=$4,output_tokens=$5 WHERE id=$1`, runID, u.Reported, in, cached, out)
	return err
}

func (s *PgStore) CompleteAIReportRun(ctx context.Context, runID int64, r AIReport, u AIUsage, dropped int) error {
	items := r.Items
	if items == nil {
		items = []AIReportItem{}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	in, cached, out := aiTokenValues(u)
	// The run identity must match the report, and a terminal run cannot publish again.
	tag, err := tx.Exec(ctx, `UPDATE ai_report_runs SET status='completed',finished_at=now(),tokens_reported=$3,input_tokens=$4,cached_input_tokens=$5,output_tokens=$6,items_dropped=$7
 WHERE id=$1 AND dashboard_user_id=$2 AND week=$8 AND status='running'`, runID, r.DashboardUserID, u.Reported, in, cached, out, dropped, r.Week)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("complete AI report run: running run not found or report identity mismatch")
	}
	_, err = tx.Exec(ctx, `INSERT INTO ai_reports (dashboard_user_id,week,run_id,tz,since,until,summary,items)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (dashboard_user_id,week) DO UPDATE SET
 run_id=EXCLUDED.run_id,tz=EXCLUDED.tz,since=EXCLUDED.since,until=EXCLUDED.until,summary=EXCLUDED.summary,items=EXCLUDED.items,generated_at=now()`, r.DashboardUserID, r.Week, runID, r.TZ, r.Since, r.Until, r.Summary, encoded)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) FailAIReportRun(ctx context.Context, runID int64, status, code, msg string) error {
	if status != AIRunFailed && status != AIRunCanceled {
		return fmt.Errorf("invalid AI failure status %q", status)
	}
	_, err := s.pool.Exec(ctx, `UPDATE ai_report_runs SET status=$2,error_code=$3,error_message=$4,finished_at=now() WHERE id=$1 AND status='running'`, runID, status, code, msg)
	return err
}

func (s *PgStore) FailRunningAIReportRuns(ctx context.Context, code string) (int, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE ai_report_runs SET status='failed',error_code=$1,finished_at=now() WHERE status='running'`, code)
	return int(tag.RowsAffected()), err
}

func scanAIRun(row pgx.Row) (*AIReportRun, error) {
	r := &AIReportRun{}
	var in, cached, out *int64
	err := row.Scan(&r.ID, &r.DashboardUserID, &r.Week, &r.TZ, &r.Since, &r.Until, &r.ScopeProfileEmail, &r.ScopeUserID, &r.StartedBy, &r.Status, &r.ErrorCode, &r.ErrorMessage, &r.Runtime, &r.Model, &r.AuthMode, &r.Usage.Reported, &in, &cached, &out, &r.ItemsDropped, &r.StartedAt, &r.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if in != nil {
		r.Usage.InputTokens = *in
	}
	if cached != nil {
		r.Usage.CachedInputTokens = *cached
	}
	if out != nil {
		r.Usage.OutputTokens = *out
	}
	return r, nil
}

func (s *PgStore) GetAIReportRun(ctx context.Context, runID int64) (*AIReportRun, error) {
	return scanAIRun(s.pool.QueryRow(ctx, `SELECT `+aiRunColumns+` FROM ai_report_runs WHERE id=$1`, runID))
}

func (s *PgStore) LatestAIReportRun(ctx context.Context, userID int64, week string) (*AIReportRun, error) {
	return scanAIRun(s.pool.QueryRow(ctx, `SELECT `+aiRunColumns+` FROM ai_report_runs WHERE dashboard_user_id=$1 AND week=$2 ORDER BY started_at DESC,id DESC LIMIT 1`, userID, week))
}

// RunningAIReportRun returns the user's running run in any week, or nil.
func (s *PgStore) RunningAIReportRun(ctx context.Context, userID int64) (*AIReportRun, error) {
	return scanAIRun(s.pool.QueryRow(ctx, `SELECT `+aiRunColumns+` FROM ai_report_runs WHERE dashboard_user_id=$1 AND status='running' ORDER BY started_at DESC,id DESC LIMIT 1`, userID))
}

// SetAIRunModel records the model the runtime actually used.
func (s *PgStore) SetAIRunModel(ctx context.Context, runID int64, model string) error {
	_, err := s.pool.Exec(ctx, `UPDATE ai_report_runs SET model=$2 WHERE id=$1`, runID, model)
	return err
}

func (s *PgStore) GetAIReport(ctx context.Context, userID int64, week string) (*AIReport, error) {
	r := &AIReport{}
	var items []byte
	err := s.pool.QueryRow(ctx, `SELECT dashboard_user_id,week,run_id,tz,since,until,summary,items,generated_at FROM ai_reports WHERE dashboard_user_id=$1 AND week=$2`, userID, week).Scan(&r.DashboardUserID, &r.Week, &r.RunID, &r.TZ, &r.Since, &r.Until, &r.Summary, &items, &r.GeneratedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(items, &r.Items); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *PgStore) ListAIToolCalls(ctx context.Context, runID int64, afterSeq int) ([]AIToolCall, error) {
	rows, err := s.pool.Query(ctx, `SELECT run_id,seq,tool,args,status,result_rows,result_bytes,duration_ms,started_at FROM ai_report_tool_calls WHERE run_id=$1 AND seq>$2 ORDER BY seq`, runID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []AIToolCall{}
	for rows.Next() {
		var c AIToolCall
		if err = rows.Scan(&c.RunID, &c.Seq, &c.Tool, &c.Args, &c.Status, &c.ResultRows, &c.ResultBytes, &c.DurationMs, &c.StartedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *PgStore) GetAIConsent(ctx context.Context, userID int64, runtimeKey string) (*AIConsent, error) {
	c := &AIConsent{}
	err := s.pool.QueryRow(ctx, `SELECT dashboard_user_id,runtime_key,disclosure_version,consented_at FROM ai_consents WHERE dashboard_user_id=$1 AND runtime_key=$2`, userID, runtimeKey).Scan(&c.DashboardUserID, &c.RuntimeKey, &c.DisclosureVersion, &c.ConsentedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *PgStore) UpsertAIConsent(ctx context.Context, userID int64, runtimeKey, version string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO ai_consents (dashboard_user_id,runtime_key,disclosure_version) VALUES ($1,$2,$3) ON CONFLICT (dashboard_user_id,runtime_key) DO UPDATE SET disclosure_version=EXCLUDED.disclosure_version,consented_at=now()`, userID, runtimeKey, version)
	return err
}

func (s *PgStore) AIUsageSince(ctx context.Context, since time.Time) (*AIUsageSummary, error) {
	u := &AIUsageSummary{}
	err := s.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE status='failed'),COALESCE(sum(input_tokens) FILTER (WHERE tokens_reported),0),COALESCE(sum(output_tokens) FILTER (WHERE tokens_reported),0) FROM ai_report_runs WHERE started_at >= $1`, since).Scan(&u.Runs, &u.Failed, &u.InputTokens, &u.OutputTokens)
	if err != nil {
		return nil, err
	}
	return u, nil
}
