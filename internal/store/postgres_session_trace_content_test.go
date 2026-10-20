package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTraceClaudeTopLevelContentPreservesSource(t *testing.T) {
	// Shared database fixtures must remain sequential.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	body := strings.Repeat("Recorded system notice. ", 12)
	raw, err := json.Marshal(map[string]any{"type": "system", "subtype": "informational", "content": body})
	if err != nil {
		t.Fatal(err)
	}
	record := &SessionRecord{SessionID: "top-level-content", UUID: "notice", RecordType: "system", Agent: "claude", UserID: "owner", ProfileEmail: "p", SourceFile: "main", Ts: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), Raw: raw}
	if err = s.InsertSessionRecords(ctx, []*SessionRecord{record}); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p"}
	page, err := s.TraceNodes(ctx, record.SessionID, a, TraceNodesRequest{View: "flow", Limit: 20})
	if err != nil || len(page.Nodes) != 1 {
		t.Fatalf("system source missing: nodes=%d err=%v", len(page.Nodes), err)
	}
	detail, err := s.TraceNodeDetail(ctx, record.SessionID, a, page.Nodes[0].NodeID)
	if err != nil || detail.Input.State != "observed" || detail.Input.Value != body {
		t.Fatalf("top-level content was not interpreted: state=%s value=%v err=%v", detail.Input.State, detail.Input.Value, err)
	}
	if len(detail.RawRecords) != 1 || len(detail.SourceLocations) == 0 || string(detail.RawRecords[0].Raw) == "" {
		t.Fatal("interpreted content lost its exact raw source")
	}
}

func TestTraceClaudeContentEnvelopePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"raw", `{"content":"raw"}`, "raw"},
		{"null-message", `{"message":null,"content":"raw"}`, "raw"},
		{"message", `{"message":{"content":"message"},"payload":{"content":"payload"},"content":"raw"}`, "message"},
		{"payload", `{"payload":{"content":"payload"},"content":"raw"}`, "payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &raw); err != nil {
				t.Fatal(err)
			}
			row := traceNodeRow{record: &SessionRecord{Agent: "claude"}, raw: raw}
			if got := nodeMessage(row)["content"]; got != tc.want {
				t.Fatalf("content=%v want=%s", got, tc.want)
			}
		})
	}
}
