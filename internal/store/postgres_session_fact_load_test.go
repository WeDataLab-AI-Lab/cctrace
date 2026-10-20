package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSessionFactLoadClaudeSources(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	const id = "session-fact-claude"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,reasons,scan_from_id) VALUES ($1,5,0)`, id)
	if err != nil {
		t.Fatal(err)
	}
	var userID, headID, lateID int64
	err = s.pool.QueryRow(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,uuid,user_id,profile_email,raw,prompt_source)
 VALUES ($1,$2,'user','claude','u','owner','profile', $3::jsonb,'typed') RETURNING id`, ts, id,
		`{"promptId":"prompt","message":{"content":"first request"},"cctraceCollect":{"version":1}}`).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,uuid,user_id,profile_email,model,message_id,source_file,input_tokens,output_tokens,raw)
 VALUES ($1,$2,'assistant','claude','a','owner','profile','claude-sonnet-5','msg','subagent/x',10,1,$3::jsonb) RETURNING id`, ts.Add(time.Second), id,
		`{"message":{"id":"msg","content":[{"type":"tool_use","id":"tool","name":"Write","input":{"file_path":"safe.go","content":"SECRET-WRITE-BODY"}}]}}`).Scan(&headID)
	if err != nil {
		t.Fatal(err)
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,uuid,user_id,profile_email,model,message_id,source_file,input_tokens,output_tokens,raw)
 VALUES ($1,$2,'assistant','claude','b','owner','profile','claude-sonnet-5','msg','subagent/x',8,20,'{}'::jsonb) RETURNING id`, ts.Add(2*time.Second), id).Scan(&lateID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO claude_imputed_cost(srec_id,session_id,ts,cost_usd) VALUES ($1,$2,$3,0.05)`, headID, id, ts.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO otel_events(ts,event_name,session_id,user_id,profile_email,prompt_id,cost_usd) VALUES ($1,'api_request',$2,'owner','profile','prompt',0.10)`, ts.Add(3*time.Second), id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,observed_at,snapshot)
 VALUES ($1,'owner','profile',$2,$3::jsonb)`, id, ts, `{"custom_title":"","ai_title":"[redacted by cctrace client]","cost_states":[{"start_time_ms":1,"lines_added":4}],"truncated":["pr_links"]}`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_overview_retention_state(table_name,last_run_finished,oldest_source_ts)
 VALUES ('session_records',$1,$2),('otel_events',$1,NULL)`, ts, ts.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if state, ok := load.Retention["session_records"]; !ok || state.LastRunFinished == nil || !state.LastRunFinished.Equal(ts) || state.OldestSourceTS == nil || !state.OldestSourceTS.Equal(ts.Add(-time.Hour)) {
		t.Fatalf("session record retention state: %+v", load.Retention)
	}
	if state, ok := load.Retention["otel_events"]; !ok || state.LastRunFinished == nil || !state.LastRunFinished.Equal(ts) || state.OldestSourceTS != nil {
		t.Fatalf("otel retention state: %+v", load.Retention)
	}
	if load.Truncated != nil || load.Retention == nil || load.Reasons != 5 || load.ScanFromID == nil || *load.ScanFromID != 0 {
		t.Fatalf("queue metadata: %+v", load)
	}
	in := load.Input
	if len(in.Rows) != 3 {
		t.Fatalf("claude row count: %d", len(in.Rows))
	}
	byID := make(map[int64]int)
	for i, row := range in.Rows {
		byID[row.ID] = i
	}
	if in.Rows[byID[userID]].PromptID != "prompt" || in.Rows[byID[userID]].TextHead != "first request" {
		t.Fatalf("user projection: %+v", in.Rows[byID[userID]])
	}
	if len(in.Heads) != 1 || in.Heads[0].ID != headID || in.Heads[0].OutputTokens != 20 || in.Heads[0].InputTokens != 10 || in.Heads[0].SourceFile != "subagent/x" || !in.Heads[0].Imputed || !in.Heads[0].HasRate {
		t.Fatalf("folded head: %+v", in.Heads)
	}
	if len(in.Rows[byID[headID]].Blocks) == 0 || strings.Contains(string(in.Rows[byID[headID]].Blocks), "SECRET-WRITE-BODY") {
		t.Fatalf("slim projection leaked Write body: %s", in.Rows[byID[headID]].Blocks)
	}
	if !in.HasOTEL || len(in.OTEL) != 1 || in.OTELOldest.IsZero() {
		t.Fatalf("telemetry: %+v", in)
	}
	if len(in.Snapshots) != 1 || in.Snapshots[0].CustomTitle == nil || *in.Snapshots[0].CustomTitle != "" || len(in.Snapshots[0].CostStates) != 1 || in.Snapshots[0].CostStates[0].LinesAdded != 4 {
		t.Fatalf("snapshot: %+v", in.Snapshots)
	}
}

func TestSessionFactFallbackEligibility(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err, readErr error
		want         bool
	}{
		{"row cap", errSessionFactGuard, nil, true},
		{"statement timeout", &pgconn.PgError{Code: "57014"}, nil, true},
		{"internal deadline error", context.DeadlineExceeded, context.DeadlineExceeded, true},
		{"internal deadline during query", errors.New("connection closed"), context.DeadlineExceeded, true},
		{"caller cancellation", context.Canceled, context.Canceled, false},
		{"other SQL failure", errors.New("syntax error"), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionFactFallbackEligible(tc.err, tc.readErr); got != tc.want {
				t.Fatalf("eligible(%v, %v) = %v, want %v", tc.err, tc.readErr, got, tc.want)
			}
		})
	}
}

func TestSessionFactLoadFullDeadlineUsesFreshFallback(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "session-fact-deadline"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_records(session_id,agent,record_type,user_id,profile_email)
 VALUES ($1,'claude','user','owner','profile')`, id)
	if err != nil {
		t.Fatal(err)
	}
	options := sessionFactReadOptions{
		fullBudget: 50 * time.Millisecond, fallbackBudget: 5 * time.Second,
		statementSQL: sessionFactStatementLimit,
		countSQL:     `SELECT count(*) FROM session_records WHERE session_id = $1 AND pg_sleep(0.2) IS NULL`,
		fallbackSQL:  sessionFactFallbackSQL,
	}
	load, err := s.loadSessionFactsInputWithOptions(ctx, id, "claude", sessionFactRowLimit, options)
	if err != nil {
		t.Fatal(err)
	}
	if load.Truncated == nil || load.Truncated.Session.OwnerUserID == nil || *load.Truncated.Session.OwnerUserID != "owner" {
		t.Fatalf("fresh fallback after internal deadline: %+v", load)
	}
	options.fullBudget = 5 * time.Second
	options.statementSQL = `SET LOCAL statement_timeout = '10ms'`
	options.countSQL = `SELECT count(*) FROM session_records WHERE session_id = $1 AND pg_sleep(0.1) IS NULL`
	load, err = s.loadSessionFactsInputWithOptions(ctx, id, "claude", sessionFactRowLimit, options)
	if err != nil || load.Truncated == nil || load.Truncated.Session.OwnerUserID == nil || *load.Truncated.Session.OwnerUserID != "owner" {
		t.Fatalf("SQL statement timeout did not enter fresh fallback: load=%+v err=%v", load, err)
	}
	options.statementSQL = sessionFactStatementLimit
	options.countSQL = `SELECT count(*) FROM session_records WHERE session_id = $1`
	options.fallbackSQL = `SELECT 1/0`
	load, err = s.loadSessionFactsInputWithOptions(ctx, id, "claude", 0, options)
	if err == nil || load.Truncated != nil {
		t.Fatalf("fallback SQL failure must not publish truncated fact: load=%+v err=%v", load, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	load, err = s.loadSessionFactsInputWithBudgets(canceled, id, "claude", sessionFactRowLimit, time.Nanosecond, 5*time.Second)
	if !errors.Is(err, context.Canceled) || load.Truncated != nil {
		t.Fatalf("caller cancellation must not publish fallback: load=%+v err=%v", load, err)
	}
}

func TestSessionFactLoadGuardUsesFreshFallback(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "session-fact-fallback"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,reasons) VALUES ($1,2)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email) VALUES (now(),$1,'user','claude','owner','profile')`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,observed_at,snapshot) VALUES ($1,'owner','profile',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	retainedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	_, err = s.pool.Exec(ctx, `INSERT INTO session_overview_retention_state(table_name,last_run_finished)
 VALUES ('session_records',$1)`, retainedAt)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.loadSessionFactsInput(ctx, id, "claude", 0)
	if err != nil {
		t.Fatal(err)
	}
	if state, ok := load.Retention["session_records"]; !ok || state.LastRunFinished == nil || !state.LastRunFinished.Equal(retainedAt) || state.OldestSourceTS != nil {
		t.Fatalf("fallback retention state: %+v", load.Retention)
	}
	if _, ok := load.Retention["otel_events"]; ok {
		t.Fatalf("missing retention row must stay missing: %+v", load.Retention)
	}
	if load.Retention == nil || load.Truncated == nil || load.Truncated.Session.Coverage != "truncated" || load.Truncated.Session.OwnerUserID == nil || *load.Truncated.Session.OwnerUserID != "owner" || load.Truncated.Session.FirstActivityAt == nil || load.Truncated.Session.FirstRecordAt == nil || !load.Truncated.Session.Transcript || len(load.Truncated.Session.AccountKeys) == 0 || len(load.Truncated.Turns) != 0 || len(load.Truncated.Hash) == 0 {
		t.Fatalf("fallback: %+v", load)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,user_id,profile_email)
 VALUES (now() + interval '1 minute',$1,'assistant','owner','profile')`, id)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := s.loadSessionFactsInput(ctx, id, "claude", 0)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Truncated == nil || legacy.Truncated.Session.LastActivityAt == nil || !legacy.Truncated.Session.LastActivityAt.After(*load.Truncated.Session.LastActivityAt) {
		t.Fatalf("legacy agent fallback activity: %+v", legacy)
	}
}
