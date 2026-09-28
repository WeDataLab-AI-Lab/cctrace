package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cctrace/internal/store"
)

const codexToolCallRaw = `{"timestamp":"2026-09-10T00:00:00Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls\"}","call_id":"call_1"}}`

func codexToolCallStore() *mockStore {
	return &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("admin-token", &store.DashboardUser{Role: "admin", IsActive: true}),
		listSessionRecordsFn: func(context.Context, store.SessionRecordFilter) ([]*store.SessionRecord, error) {
			return []*store.SessionRecord{{
				SessionID: "s1", RecordType: "tool_call", Agent: "codex",
				ToolName: "exec_command", ToolCallID: "call_1", Raw: json.RawMessage(codexToolCallRaw),
			}}, nil
		},
	}
}

// The viewer renders from view alone, so a Codex tool call has to arrive already
// shaped as one -- raw stays only for the individual raw-record list.
func TestDashboardSessionResponseCarriesView(t *testing.T) {
	rec := openAPIRequest(t, newTestServer(codexToolCallStore(), nil), http.MethodGet, "/api/sessions?session_id=s1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var body []struct {
		Agent string          `json:"agent"`
		Raw   json.RawMessage `json:"raw"`
		View  *struct {
			Kind      string `json:"kind"`
			AnchorID  string `json:"anchor_id"`
			ToolCalls []struct {
				ID, Name, Input string
			} `json:"tool_calls"`
		} `json:"view"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body[0].View == nil {
		t.Fatalf("view missing: %s", rec.Body.String())
	}
	v := body[0].View
	if v.Kind != "tool_call" || v.AnchorID == "" || len(v.ToolCalls) != 1 || v.ToolCalls[0].ID != "call_1" || v.ToolCalls[0].Name != "exec_command" {
		t.Fatalf("view = %+v", v)
	}
	if body[0].Agent != "codex" || len(body[0].Raw) == 0 {
		t.Fatalf("agent/raw lost: %s", rec.Body.String())
	}
}

// The assembled viewer renders from view alone and polls every loaded page, so it
// asks for records without raw; everything else still gets raw by default.
func TestDashboardSessionResponseOmitsRawOnRequest(t *testing.T) {
	srv := newTestServer(codexToolCallStore(), nil)
	rec := openAPIRequest(t, srv, http.MethodGet, "/api/sessions?session_id=s1&raw=0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var body []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body[0]["view"] == nil {
		t.Fatalf("view missing: %s", rec.Body.String())
	}
	if _, ok := body[0]["raw"]; ok {
		t.Fatalf("raw present despite raw=0: %s", rec.Body.String())
	}
}

// kind is a display classification: tool calls live inside message records for
// Claude/omo/gjc, and gjc's sibling tool_call rows are hidden duplicates. A consumer
// counting calls across agents sums tool_call_count, which counts each call once.
func TestOpenAPIToolCallCountIsAgentNeutral(t *testing.T) {
	gjcLine := `{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","id":"tc_1","name":"read","arguments":{}},{"type":"toolCall","id":"tc_2","name":"grep","arguments":{}}]}}`
	m := &mockStore{
		getDashboardUserByAPITokenFn: apiUserTokenLookup("admin-token", &store.DashboardUser{Role: "admin", IsActive: true}),
		listSessionRecordsFn: func(context.Context, store.SessionRecordFilter) ([]*store.SessionRecord, error) {
			return []*store.SessionRecord{
				{SessionID: "s", RecordType: "assistant", Agent: "claude", Raw: json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}},{"type":"tool_use","id":"toolu_2","name":"Read","input":{}}]}}`)},
				{SessionID: "s", RecordType: "tool_call", Agent: "codex", Raw: json.RawMessage(codexToolCallRaw)},
				{SessionID: "s", RecordType: "assistant", Agent: "gjc", Raw: json.RawMessage(gjcLine)},
				{SessionID: "s", RecordType: "tool_call", Agent: "gjc", ToolName: "read", ToolCallID: "tc_1", Raw: json.RawMessage(gjcLine)},
				{SessionID: "s", RecordType: "tool_call", Agent: "gjc", ToolName: "grep", ToolCallID: "tc_2", Raw: json.RawMessage(gjcLine)},
			}, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/open/v1/sessions/s", "Bearer admin-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var body []struct {
		Agent         string `json:"agent"`
		Kind          string `json:"kind"`
		ToolCallCount int    `json:"tool_call_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	perAgent := map[string]int{}
	for _, r := range body {
		perAgent[r.Agent] += r.ToolCallCount
	}
	if perAgent["claude"] != 2 || perAgent["codex"] != 1 || perAgent["gjc"] != 2 {
		t.Fatalf("tool calls per agent = %v, want claude 2 codex 1 gjc 2; body=%s", perAgent, rec.Body.String())
	}
}

// The Open API reports what a record is in one vocabulary for every agent, and
// still never the body: no text, arguments or output.
func TestOpenAPISessionRecordReportsKindWithoutBody(t *testing.T) {
	rec := openAPIRequest(t, newTestServer(codexToolCallStore(), nil), http.MethodGet, "/api/open/v1/sessions/s1", "Bearer admin-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var body []map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 {
		t.Fatalf("records = %d", len(body))
	}
	got := body[0]
	for key, want := range map[string]string{"agent": "codex", "kind": "tool_call", "tool_name": "exec_command", "tool_call_id": "call_1", "record_type": "tool_call"} {
		if got[key] != want {
			t.Fatalf("%s = %v, want %q; body=%s", key, got[key], want, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "cmd") {
		t.Fatalf("tool arguments leaked: %s", rec.Body.String())
	}
}
