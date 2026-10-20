package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type traceNodesMock struct {
	*traceAPIMock
	pages, details int
	selection      string
	view           string
	request        store.TraceNodesRequest
	options        store.TraceNodeDetailOptions
	rawRecords     []*store.SessionRecord
}

func (m *traceNodesMock) TraceNodes(_ context.Context, id string, a store.SessionAnalysisAccess, q store.TraceNodesRequest) (store.TraceNodesResponse, error) {
	m.pages++
	m.request = q
	m.access = a
	m.selection = q.ParentID
	m.view = q.View
	return store.TraceNodesResponse{SessionID: id}, nil
}
func (m *traceNodesMock) TraceNodeDetail(_ context.Context, id string, a store.SessionAnalysisAccess, node string) (store.TraceNodeDetail, error) {
	m.details++
	m.access = a
	m.selection = node
	return store.TraceNodeDetail{SessionID: id, RawRecords: m.rawRecords}, nil
}

func TestTraceNodesAPIIsImmediateScopedAndValidatesPage(t *testing.T) {
	t.Parallel()
	m := &traceNodesMock{traceAPIMock: &traceAPIMock{mockStore: &mockStore{}}}
	s := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
	for _, test := range []struct {
		path   string
		status int
	}{
		{"nodes?parent_id=parent&limit=10&user_id=foreign", 200}, {"nodes?view=context", 200}, {"nodes?view=metadata", 200}, {"nodes?view=invalid", 400}, {"nodes/old-event", 200}, {"nodes?limit=101", 400}, {"nodes?limit=0", 400},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s/trace/"+test.path, nil)
		req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{ID: 1, Role: "user", CctraceUserID: "owner"}))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != test.status {
			t.Fatalf("%s status=%d body=%s", test.path, w.Code, w.Body.String())
		}
	}
	if m.pages != 3 || m.details != 1 || m.demands != 0 || m.access.UserID != "owner" || m.selection != "old-event" || m.view != "metadata" {
		t.Fatalf("unexpected access %+v", m)
	}
}

func (m *traceNodesMock) TraceNodeDetailWithOptions(_ context.Context, id string, a store.SessionAnalysisAccess, node string, q store.TraceNodeDetailOptions) (store.TraceNodeDetail, error) {
	m.details++
	m.options = q
	m.access = a
	m.selection = node
	return store.TraceNodeDetail{SessionID: id, RawRecords: m.rawRecords}, nil
}

func TestTraceFlowTimingAPIValidatesAndForwardsBoundedScope(t *testing.T) {
	t.Parallel()
	m := &traceNodesMock{traceAPIMock: &traceAPIMock{mockStore: &mockStore{}}}
	s := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
	for _, test := range []struct {
		path   string
		status int
	}{
		{"nodes?view=flow&q=Read", 200},
		{"nodes?view=timing&group_id=g&from=2026-10-19T00:00:00Z&to=2026-10-19T00:01:00Z", 200},
		{"nodes?view=timing", 400},
		{"nodes?view=timing&group_id=g&from=bad", 400},
		{"nodes?view=flow&from=2026-10-19T00:00:00Z", 400},
		{"nodes?view=timing&group_id=g&from=2026-10-19T00:01:00Z&to=2026-10-19T00:00:00Z", 400},
		{"nodes/n?request_parts=page&input_cursor=c&input_limit=10&compare_to=previous&source_ref=source", 200},
		{"nodes/n?request_parts=invalid", 400}, {"nodes/n?input_limit=101", 400},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/s/trace/"+test.path, nil)
		req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{ID: 1, Role: "user", CctraceUserID: "owner"}))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != test.status {
			t.Fatalf("%s status=%d body=%s", test.path, w.Code, w.Body.String())
		}
	}
	if m.pages != 2 || m.request.Limit != 20 || m.request.View != "timing" || m.request.GroupID != "g" || m.request.From == nil || m.request.To == nil || m.demands != 0 || m.access.UserID != "owner" {
		t.Fatalf("page scope=%+v access=%+v", m.request, m.access)
	}
	if m.details != 1 || m.options.RequestParts != "page" || m.options.InputCursor != "c" || m.options.InputLimit != 10 || m.options.CompareTo != "previous" || m.options.SourceRef != "source" {
		t.Fatalf("detail options=%+v", m.options)
	}
}

func TestTraceNodeDetailRawRecordsShareConversationViews(t *testing.T) {
	t.Parallel()
	records := []*store.SessionRecord{
		{SessionID: "s", UUID: "actual-claude-row", SourceFile: "/source/claude.jsonl", Agent: "claude", RecordType: "assistant", UserID: "owner", Raw: json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"text","text":"Actual response"},{"type":"tool_use","id":"actual-claude-call","name":"Read","input":{"file_path":"/actual/file"}}]}}`)},
		{SessionID: "s", UUID: "actual-codex-row", SourceFile: "/source/codex.jsonl", Agent: "codex", RecordType: "tool_call", UserID: "owner", ToolCallID: "actual-codex-call", Raw: json.RawMessage(`{"payload":{"type":"function_call","call_id":"actual-codex-call","name":"exec_command","arguments":"{}"}}`)},
		{SessionID: "s", UUID: "actual-hidden-row", SourceFile: "/source/claude.jsonl", Agent: "claude", RecordType: "api-request", UserID: "owner", Raw: json.RawMessage(`{"type":"api-request","id":"recipe"}`)},
	}
	for _, suffix := range []string{"", "?request_parts=page"} {
		t.Run(suffix, func(t *testing.T) {
			m := &traceNodesMock{traceAPIMock: &traceAPIMock{mockStore: &mockStore{}}, rawRecords: records}
			s := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
			req := httptest.NewRequest(http.MethodGet, "/api/sessions/s/trace/nodes/opaque-selected-node"+suffix, nil)
			req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{ID: 1, Role: "user", CctraceUserID: "owner"}))
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var response struct {
				SessionID string `json:"session_id"`
				Records   []struct {
					UUID       string          `json:"uuid"`
					SourceFile string          `json:"source_file"`
					Raw        json.RawMessage `json:"raw"`
					View       struct {
						Kind   string `json:"kind"`
						Role   string `json:"role"`
						Anchor string `json:"anchor_id"`
						Calls  []struct {
							ID string `json:"id"`
						} `json:"tool_calls"`
					} `json:"view"`
				} `json:"raw_records"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.SessionID != "s" || len(response.Records) != 3 || m.access.UserID != "owner" || m.selection != "opaque-selected-node" {
				t.Fatalf("scope=%+v access=%+v", response, m.access)
			}
			for i, record := range response.Records {
				if record.UUID != records[i].UUID || record.SourceFile != records[i].SourceFile || record.View.Anchor != records[i].UUID {
					t.Fatalf("source identity=%+v", record)
				}
				var got, want any
				if json.Unmarshal(record.Raw, &got) != nil || json.Unmarshal(records[i].Raw, &want) != nil {
					t.Fatal("raw record missing")
				}
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				if string(gotJSON) != string(wantJSON) {
					t.Fatalf("raw changed: %s", record.Raw)
				}
			}
			if response.Records[0].View.Kind != "message" || response.Records[0].View.Role != "assistant" || len(response.Records[0].View.Calls) != 1 || response.Records[0].View.Calls[0].ID != "actual-claude-call" {
				t.Fatalf("Claude view=%+v", response.Records[0].View)
			}
			if response.Records[1].View.Kind != "tool_call" || len(response.Records[1].View.Calls) != 1 || response.Records[1].View.Calls[0].ID != "actual-codex-call" {
				t.Fatalf("Codex view=%+v", response.Records[1].View)
			}
			if response.Records[2].View.Kind != "hidden" {
				t.Fatalf("native recipe invented conversation role=%+v", response.Records[2].View)
			}
		})
	}
}
