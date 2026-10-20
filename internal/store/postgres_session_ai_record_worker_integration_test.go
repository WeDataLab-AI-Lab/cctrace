package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/chatruntime"
	"cctrace/internal/sessionfacts"
	"cctrace/internal/sessionrecord"
)

type fixedSessionRecordRuntime struct{ config sessionrecord.RuntimeConfig }

func (r fixedSessionRecordRuntime) ResolveSessionRecordRuntime(context.Context, string) (sessionrecord.RuntimeConfig, error) {
	return r.config, nil
}

func TestSessionRecordProductionWorkerUsesSyntheticLocalProviderAndPublishes(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	turnOutput := `{"headline":"Synthetic endpoint work","bullets":[{"text":"Added the synthetic endpoint","turns":["t1"]}],"review":[],"kinds":["implementation"],"components":["API"],"screens":[],"new_component":"API","new_screen":null,"work":[{"turn":"t1","kind":"request","text":"Add the synthetic endpoint","done":true,"decision":null,"note":null,"children":[]}],"skipped":[],"work_updates":[]}`
	var requestBody []byte
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-local","model":"synthetic-model","choices":[{"index":0,"message":{"role":"assistant","content":`+mustJSONString(t, turnOutput)+`},"finish_reason":"stop"}],"usage":{"prompt_tokens":1200,"completion_tokens":100}}`)
	}))
	defer provider.Close()
	runtime := chatruntime.New(chatruntime.Config{RuntimeKey: "litellm-api", ProviderName: "litellm", DefaultModel: "synthetic-model",
		APIKey: func(context.Context) (string, error) { return "synthetic-local-key", nil }, BaseURL: func(context.Context) (string, error) { return provider.URL, nil }, HTTPClient: &http.Client{Transport: sessionrecord.GatedRoundTripper{Base: http.DefaultTransport}}})
	userID := "p5-synthetic-worker"
	var dashboardUserID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO dashboard_users(email,password_hash,role,cctrace_user_id)
		VALUES('p5-worker@example.test','synthetic','user',$1) RETURNING id`, userID).Scan(&dashboardUserID); err != nil {
		t.Fatal(err)
	}
	const sessionID = "p5-synthetic-worker-session"
	now := time.Now().UTC().Add(-45 * time.Minute).Truncate(time.Second)
	factHash := sha256.Sum256([]byte("synthetic-worker-facts"))
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_analysis_facts
		(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,project_hash,repository_key,transcript,
		title_source,title_state,first_activity_at,last_activity_at,first_record_at,turn_boundaries,turns,requests,inputs,cost_source,detail,
		output_hash,source_generation,rules_version,computed_at)
		VALUES($1,'claude','full',ARRAY[$2],ARRAY['p5-worker@example.test'],$2,'p5-worker@example.test','worker-project','worker-repo',true,
		'first_request','ok',$3,$3,$3,'observed',1,1,1,'none',
		'{"edited_files":{"state":"observed","items":[]},"plans":[]}'::jsonb,$4,1,$5,$3)`, sessionID, userID, now, factHash[:], sessionfacts.RulesVersion); err != nil {
		t.Fatalf("seed synthetic facts: %v", err)
	}
	detail, _ := json.Marshal(sessionfacts.TurnDetail{Inputs: []sessionfacts.TurnInput{{UUID: "worker-input", TS: now, Kind: "typed", N: 1}}})
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,branch,started_at,ended_at,input_kind,is_request,status,preview_state,
		last_assistant_message_id,cost_source,context_source,detail,rules_version)
		VALUES($1,'worker-turn','claude',1,'active',$2,$2,'typed',true,'ended','ok','worker-final','none','unknown',$3,$4)`, sessionID, now, detail, sessionfacts.RulesVersion); err != nil {
		t.Fatalf("seed synthetic turn: %v", err)
	}
	for _, row := range []struct {
		ts                        time.Time
		typ, uuid, messageID, raw string
	}{
		{now, "user", "worker-input", "", `{"message":{"content":[{"type":"text","text":"Add the synthetic endpoint"}]}}`},
		{now.Add(time.Second), "assistant", "", "worker-final", `{"message":{"content":[{"type":"text","text":"Added the synthetic endpoint"}]}}`},
	} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,project_hash,record_type,profile_email,user_id,agent,uuid,message_id,raw)
		VALUES($1,$2,'worker-project',$3,'p5-worker@example.test',$4,'claude',$5,$6,$7::jsonb)`, row.ts, sessionID, row.typ, userID, row.uuid, row.messageID, row.raw); err != nil {
			t.Fatalf("seed synthetic source: %v", err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_ai_record_consents(dashboard_user_id,runtime_key,disclosure_version)
		VALUES($1,'litellm-api:api_key',$2)`, dashboardUserID, sessionrecord.RecordDisclosureVersion); err != nil {
		t.Fatal(err)
	}
	cap := int64(200000)
	if err := s.SetSessionAIRecordSettings(ctx, SessionAIRecordSettings{Enabled: true, Runtime: "litellm-api", DailyTokenCap: &cap}, "synthetic-admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAIRecordRuntimeSettings(ctx, "litellm-api", "synthetic-model", "", "synthetic-admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestSessionRecord(ctx, sessionID, userID, "p5-worker@example.test", true, dashboardUserID, "litellm-api:api_key", sessionrecord.RecordDisclosureVersion, true, time.Now().UTC()); err != nil {
		t.Fatalf("queue synthetic manual run: %v", err)
	}
	gate := &sessionrecord.TransmissionGate{}
	worker := sessionrecord.NewWorker(s, fixedSessionRecordRuntime{config: sessionrecord.RuntimeConfig{Runtime: runtime, Key: "litellm-api", RuntimeKey: "litellm-api:api_key", Model: "synthetic-model"}}, true, gate)
	worker.WithUserIDAccessControl(true)
	worked, lane, err := worker.RunOne(ctx)
	if err != nil || !worked || lane != "manual" {
		t.Fatalf("worker run worked=%v lane=%q err=%v", worked, lane, err)
	}
	if len(requestBody) == 0 {
		t.Fatal("local provider did not receive the synthetic reduced input")
	}
	if !strings.Contains(string(requestBody), "Add the synthetic endpoint") || strings.Contains(string(requestBody), "p5-worker@example.test") || strings.Contains(string(requestBody), userID) {
		t.Fatalf("synthetic provider payload leaked identity or omitted allowed text: %s", requestBody)
	}
	var request map[string]interface{}
	if err := json.Unmarshal(requestBody, &request); err != nil {
		t.Fatal(err)
	}
	if _, ok := request["tools"]; ok {
		t.Fatalf("production worker enabled tools in provider request: %v", request)
	}
	if request["max_tokens"] != float64(sessionrecord.MaxOutputTokens) {
		t.Fatalf("output token cap=%v", request["max_tokens"])
	}
	var outcome, headline string
	if err := s.pool.QueryRow(ctx, `SELECT outcome,headline FROM session_ai_records WHERE session_id=$1`, sessionID).Scan(&outcome, &headline); err != nil {
		t.Fatal(err)
	}
	if outcome != "ok" || headline != "Synthetic endpoint work" {
		t.Fatalf("published synthetic record outcome=%q headline=%q", outcome, headline)
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
