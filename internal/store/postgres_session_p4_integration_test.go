package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/sessionfacts"
)

// publishSessionP4ForTest writes P4 facts without a timeline mapping for tests
// whose fixture is about the analysis API rather than timeline persistence.
func publishSessionP4ForTest(ctx context.Context, s *PgStore, id string, load SessionFactLoad, computeMS int) (string, error) {
	extended := sessionfacts.ComputeWithTimelineAndCommits(load.Input)
	p4 := &sessionFactTimeline{Commits: extended.Commits, Tasks: extended.Tasks}
	return s.publishSessionFactWithTimeline(ctx, id, load.Generation, load, extended.Output, p4, computeMS)
}

func TestSessionP4_WorkerPersistsAndReadsCommitTaskTurnFacts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "session-p4-worker"
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	type transcriptRow struct {
		kind, uuid, parent string
		sec                int
		raw                string
	}
	rows := []transcriptRow{
		{"user", "u", "", 1, `{"cctraceCollect":2,"promptId":"p","origin":{"kind":"human"},"message":{"content":"implement issue"}}`},
		{"assistant", "a1", "u", 2, `{"cctraceCollect":2,"message":{"content":[{"type":"tool_use","id":"task-create","name":"TaskCreate","input":{"subject":"Fix issue","description":"Close the bug"}}]}}`},
		{"user", "r1", "a1", 3, `{"cctraceCollect":2,"message":{"content":[{"type":"tool_result","tool_use_id":"task-create","content":"Task #12 created"}]},"toolUseResult":{"task":{"id":"12"}}}`},
		{"assistant", "a2", "r1", 4, `{"cctraceCollect":2,"message":{"content":[{"type":"tool_use","id":"task-update","name":"TaskUpdate","input":{"taskId":"12","status":"completed"}}]}}`},
		{"user", "r2", "a2", 5, `{"cctraceCollect":2,"message":{"content":[{"type":"tool_result","tool_use_id":"task-update"}]}}`},
		{"assistant", "a3", "r2", 6, `{"cctraceCollect":2,"message":{"content":[{"type":"tool_use","id":"issue-create","name":"Bash","input":{"command":"gh issue create --title 'Fix issue'"}}]}}`},
		{"user", "r3", "a3", 7, `{"cctraceCollect":2,"message":{"content":[{"type":"tool_result","tool_use_id":"issue-create","content":"https://github.com/acme/app/issues/12"}]}}`},
		{"assistant", "a4", "r3", 8, `{"cctraceCollect":2,"message":{"content":[{"type":"tool_use","id":"git-commit","name":"Bash","input":{"command":"git commit -m 'fix #12'"}}]}}`},
		{"user", "r4", "a4", 9, `{"cctraceCollect":2,"message":{"content":[{"type":"tool_result","tool_use_id":"git-commit","content":"[main abcdef1] fix #12"}]}}`},
		{"assistant", "a5", "r4", 10, `{"cctraceCollect":2,"message":{"content":[{"type":"text","text":"done"}]}}`},
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'owner','owner@example.com','claude',$2,'{}'::jsonb)`, id, base); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,source_file,uuid,parent_uuid,model,project_hash,repository_id,raw)
			VALUES($1,$2,$3,'claude','owner','owner@example.com','session.jsonl',$4,$5,'claude-opus-5','proj','github.com/acme/app',$6::jsonb)`,
			base.Add(time.Duration(row.sec)*time.Second), id, row.kind, row.uuid, row.parent, row.raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	run, err := s.processSessionFact(ctx, id, generation)
	if err != nil || run.Outcome != "written" {
		t.Fatalf("worker run=%+v err=%v", run, err)
	}
	var commits, tasks int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_commit_facts WHERE session_id=$1`, id).Scan(&commits); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_task_facts WHERE session_id=$1`, id).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if commits != 1 || tasks != 2 {
		t.Fatalf("persisted commits/tasks=%d/%d, want 1/2", commits, tasks)
	}
	response, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	commitsResponse := response["commits"].(map[string]interface{})
	if commitsResponse["state"] != "observed" || commitsResponse["total"] != 1 {
		t.Fatalf("commits response=%v", commitsResponse)
	}
	commitItems := commitsResponse["items"].([]interface{})
	if len(commitItems) != 1 || commitItems[0].(map[string]interface{})["short"] != "abcdef1" {
		t.Fatalf("commit items=%v", commitItems)
	}
	if refs := commitItems[0].(map[string]interface{})["tasks"].([]interface{}); len(refs) != 1 || refs[0] != "#12" {
		t.Fatalf("commit refs=%v", refs)
	}
	taskResponse := response["tasks"].(map[string]interface{})
	taskItems := taskResponse["items"].([]interface{})
	if taskResponse["state"] != "observed" || len(taskItems) != 2 {
		t.Fatalf("tasks response=%v", taskResponse)
	}
	issueLinked := false
	for _, value := range taskItems {
		item := value.(map[string]interface{})
		if item["kind"] == "issue" {
			actions := item["actions"].(map[string]interface{})
			issueLinked = item["id"] == "#12" && actions["added"] == true && actions["completed"] == false && actions["commit_linked"] == true
		}
	}
	if !issueLinked {
		t.Fatalf("issue actions=%v", taskItems)
	}
	turns := response["turns"].([]interface{})
	if len(turns) != 1 {
		t.Fatalf("turns=%v", turns)
	}
	turn := turns[0].(map[string]interface{})
	if len(turn["commits"].([]interface{})) != 1 || len(turn["tasks"].([]string)) != 2 {
		t.Fatalf("turn P4 chips=%v", turns)
	}
	rollupTx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshSessionOverviewRollups(ctx, rollupTx, []string{id}); err != nil {
		_ = rollupTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := rollupTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{IncludeAnalysis: true, UserID: "owner", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range list {
		if row.SessionID == id {
			found = true
			if row.Analysis["commits"] != float64(1) && row.Analysis["commits"] != int64(1) {
				t.Fatalf("session list commits=%v", row.Analysis["commits"])
			}
		}
	}
	if !found {
		t.Fatal("session with P4 facts missing from include=analysis list")
	}
	if _, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "other", OwnerUserID: "other"}); err != ErrSessionAnalysisNotFound {
		t.Fatalf("foreign user read P4 facts: %v", err)
	}
	admin, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{OwnerUserID: "admin"})
	if err != nil || len(admin["commits"].(map[string]interface{})["items"].([]interface{})) != 1 || len(admin["tasks"].(map[string]interface{})["items"].([]interface{})) != 2 {
		t.Fatalf("admin P4 read commits/tasks=%v/%v err=%v", admin["commits"], admin["tasks"], err)
	}
	encoded, err := json.Marshal(response)
	if err != nil || !json.Valid(encoded) {
		t.Fatalf("analysis response is not valid JSON: %v", err)
	}
	if _, err := s.DeleteSession(ctx, id, "admin", "p4 privacy test", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"}); err != ErrSessionAnalysisNotFound {
		t.Fatalf("tombstoned P4 facts remained readable: %v", err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_commit_facts WHERE session_id=$1)
		+ (SELECT count(*) FROM session_task_facts WHERE session_id=$1)`, id).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("P4 rows after privacy sweep=%d err=%v", remaining, err)
	}
}

func TestSessionP4_CodexLoaderAttributesShellCommitAndPRToRootTurn(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "session-p4-codex"
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	snapshot := `{"codex_turns":[{"turn_id":"turn-1","root_turn_id":"turn-1","started_at":"2026-09-01T09:00:05Z","ended_at":"2026-09-01T09:00:25Z","end":"complete"}]}`
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'owner','owner@example.com','codex',$2,$3::jsonb)`, id, base, snapshot); err != nil {
		t.Fatal(err)
	}
	type record struct {
		sec                     int
		typ, uuid, name, callID string
		raw                     string
	}
	records := []record{
		{1, "user", "u1", "", "", `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"ship"}],"turn_id":"turn-1","root_turn_id":"turn-1"}}`},
		{6, "usage", "use1", "", "", `{"type":"event_msg","payload":{"type":"token_count","turn_id":"turn-1","root_turn_id":"turn-1","info":{"last_token_usage":{"input_tokens":10,"output_tokens":2}}}}`},
		{12, "tool_call", "call1", "exec_command", "commit-call", `{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"commit-call","arguments":"{\"cmd\":\"git commit -m 'ship #34'\",\"workdir\":\"/repo\"}"}}`},
		{13, "tool_output", "out1", "", "commit-call", `{"type":"response_item","payload":{"type":"function_call_output","call_id":"commit-call","output":"[main abcdef1] ship #34"}}`},
		{14, "tool_call", "call2", "exec_command", "pr-call", `{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"pr-call","arguments":"{\"cmd\":\"gh pr create --title 'Release'\",\"workdir\":\"/repo\"}"}}`},
		{15, "tool_output", "out2", "", "pr-call", `{"type":"response_item","payload":{"type":"function_call_output","call_id":"pr-call","output":"https://github.com/acme/app/pull/34"}}`},
	}
	for _, item := range records {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,source_file,uuid,model,project_hash,repository_id,tool_name,tool_call_id,raw)
			VALUES($1,$2,$3,'codex','owner','owner@example.com','rollout.jsonl',$4,'codex-model','proj','github.com/acme/app',$5,$6,$7::jsonb)`,
			base.Add(time.Duration(item.sec)*time.Second), id, item.typ, item.uuid, item.name, item.callID, item.raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	run, err := s.processSessionFact(ctx, id, generation)
	if err != nil || run.Outcome != "written" {
		t.Fatalf("worker run=%+v err=%v", run, err)
	}
	var source, short, turnKey string
	var refs []string
	if err := s.pool.QueryRow(ctx, `SELECT source,short,turn_key,refs FROM session_commit_facts WHERE session_id=$1`, id).Scan(&source, &short, &turnKey, &refs); err != nil {
		t.Fatal(err)
	}
	if source != "output" || short != "abcdef1" || turnKey != "turn-1" || len(refs) != 1 || refs[0] != "pr:github.com/acme/app#34" {
		t.Fatalf("Codex commit source/short/turn/refs=%q/%q/%q/%v", source, short, turnKey, refs)
	}
	var kind, displayID, turn string
	var commitLinked bool
	if err := s.pool.QueryRow(ctx, `SELECT kind,display_id,(touches->0->>'turn_key'),commit_linked FROM session_task_facts WHERE session_id=$1`, id).Scan(&kind, &displayID, &turn, &commitLinked); err != nil {
		t.Fatal(err)
	}
	if kind != "pr" || displayID != "#34" || turn != "turn-1" || !commitLinked {
		t.Fatalf("Codex PR kind/id/turn/linked=%q/%q/%q/%v", kind, displayID, turn, commitLinked)
	}
}
