package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestTraceFlowNavigationLocatesChildrenBeyondFirstPage(t *testing.T) {
	// The integration fixture shares tables with other PostgreSQL tests.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	const sid = "flow-child-locate"
	rows := []*SessionRecord{{SessionID: sid, Ts: base, UUID: "codex-input", RecordType: "user", Agent: "codex", UserID: "owner", ProfileEmail: "p", SourceFile: "codex", PromptSource: "typed", Raw: json.RawMessage(`{"payload":{"role":"user","content":"Inspect 500 calls"}}`)}}
	for i := 0; i < 500; i++ {
		raw, _ := json.Marshal(map[string]any{"payload": map[string]any{"type": "function_call", "call_id": fmt.Sprintf("flat-%03d", i), "name": "Read", "arguments": "{}"}})
		rows = append(rows, &SessionRecord{SessionID: sid, Ts: base.Add(time.Duration(i+1) * time.Millisecond), UUID: fmt.Sprintf("flat-%03d", i), RecordType: "tool_call", ToolCallID: fmt.Sprintf("flat-%03d", i), ToolName: "Read", Agent: "codex", UserID: "owner", ProfileEmail: "p", SourceFile: "codex", Raw: raw})
	}
	blocks := make([]any, 500)
	for i := range blocks {
		blocks[i] = map[string]any{"type": "tool_use", "id": fmt.Sprintf("block-%03d", i), "name": "Read", "input": map[string]any{"file_path": fmt.Sprintf("/fixture/%03d", i)}}
	}
	response, _ := json.Marshal(map[string]any{"message": map[string]any{"id": "many-tools", "role": "assistant", "content": blocks}})
	rows = append(rows, &SessionRecord{SessionID: sid, Ts: base, UUID: "claude-input", RecordType: "user", Agent: "claude", UserID: "owner", ProfileEmail: "p", SourceFile: "claude", PromptSource: "typed", Raw: json.RawMessage(`{"message":{"role":"user","content":"Inspect 500 blocks"}}`)}, &SessionRecord{SessionID: sid, Ts: base.Add(time.Second), UUID: "many-tools", RecordType: "assistant", Agent: "claude", UserID: "owner", ProfileEmail: "p", SourceFile: "claude", MessageID: "many-tools", Raw: response})
	for _, row := range rows {
		if row.Agent == "codex" {
			row.CodexTurnID = "exact-native-turn"
		}
	}
	if err := s.InsertSessionRecords(ctx, rows); err != nil {
		t.Fatal(err)
	}
	sourceID := func(uuid, kind string, index int) string {
		var row traceNodeRow
		var wire []byte
		err := s.pool.QueryRow(ctx, `SELECT r.id,`+traceNodeSourceOrder+`,to_jsonb(r) FROM session_records r WHERE r.session_id=$1 AND r.uuid=$2`, sid, uuid).Scan(&row.id, &row.order, &wire)
		if err != nil {
			t.Fatal(err)
		}
		row.record = &SessionRecord{}
		if err = json.Unmarshal(wire, row.record); err != nil {
			t.Fatal(err)
		}
		return encodeNodeKey(row.key(kind, index))
	}
	a := SessionAnalysisAccess{User: true, UserID: "owner"}
	search, err := s.TraceNodes(ctx, sid, a, TraceNodesRequest{View: "flow", Query: "Read", Limit: 1})
	if err != nil || len(search.Nodes) != 1 || search.NextCursor == nil {
		t.Fatalf("matched root page=%+v %v", search, err)
	}
	second, err := s.TraceNodes(ctx, sid, a, TraceNodesRequest{View: "flow", Query: "Read", Limit: 1, Cursor: *search.NextCursor})
	if err != nil || len(second.Nodes) != 1 || second.Nodes[0].NodeID == search.Nodes[0].NodeID || second.NextCursor != nil || second.PrevCursor == nil {
		t.Fatalf("deduped next root=%+v %v", second, err)
	}
	previous, err := s.TraceNodes(ctx, sid, a, TraceNodesRequest{View: "flow", Query: "Read", Limit: 1, Cursor: *second.PrevCursor})
	if err != nil || len(previous.Nodes) != 1 || previous.Nodes[0].NodeID != search.Nodes[0].NodeID {
		t.Fatalf("matched previous root=%+v %v", previous, err)
	}
	for _, selected := range []string{sourceID("flat-450", "tool", -1), sourceID("many-tools", "tool", 450)} {
		detail, err := s.TraceNodeDetail(ctx, sid, a, selected)
		if err != nil || detail.Navigation == nil {
			t.Fatalf("detail=%+v %v", detail.Navigation, err)
		}
		nav := detail.Navigation
		expected := selected
		for i := len(nav.AncestorIDs) - 1; i >= -1; i-- {
			parent := nav.GroupID
			if i >= 0 {
				parent = nav.AncestorIDs[i]
			}
			cursor := nav.ChildCursors[parent]
			if cursor == "" {
				t.Fatalf("missing locator parent=%s navigation=%+v", parent, nav)
			}
			page, err := s.TraceNodes(ctx, sid, a, TraceNodesRequest{View: "flow", ParentID: parent, Cursor: cursor, Limit: 20})
			if err != nil || len(page.Nodes) == 0 || page.Nodes[0].NodeID != expected {
				t.Fatalf("child page count=%d first=%+v expected=%s err=%v", len(page.Nodes), page.Nodes, expected, err)
			}
			// A locate cursor must retain the exact parent and authorized identity.
			if _, err = s.TraceNodes(ctx, sid, a, TraceNodesRequest{View: "flow", ParentID: "another-parent", Cursor: cursor}); !errors.Is(err, ErrTraceInvalid) {
				t.Fatalf("parent binding=%v", err)
			}
			if _, err = s.TraceNodes(ctx, sid, SessionAnalysisAccess{User: true, UserID: "foreign"}, TraceNodesRequest{View: "flow", ParentID: parent, Cursor: cursor}); !errors.Is(err, ErrSessionAnalysisNotFound) {
				t.Fatalf("foreign locate=%v", err)
			}
			expected = parent
		}
	}
}

func TestTraceFlowSearchPreservesExactNativeOrphanRootAndAmbiguity(t *testing.T) {
	// Shared PostgreSQL fixture; the epoch request has no account-at-time claim.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{SessionID: "search-native-orphan", Ts: time.Unix(0, 0), UUID: "recipe", RecordType: "api-request", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: json.RawMessage(`{"type":"api-request","id":"exact-request","keep":0,"tail":[],"params":{"model":"fixture"}}`)},
		{SessionID: "search-native-orphan", Ts: base, UUID: "response", RecordType: "assistant", Agent: "claude", AccountID: "observed-account", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: json.RawMessage(`{"type":"assistant","requestRef":"exact-request","message":{"id":"m","role":"assistant","content":[{"type":"tool_use","id":"read","name":"Read","input":{}}]}}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{User: true, UserID: "owner"}
	page, err := s.TraceNodes(ctx, "search-native-orphan", a, TraceNodesRequest{View: "flow", Query: "Read"})
	if err != nil || len(page.Nodes) != 1 || page.Nodes[0].Kind != "request" {
		t.Fatalf("exact native root=%+v %v", page, err)
	}
	duplicate := *records[0]
	duplicate.UUID = "duplicate-recipe"
	if err = s.InsertSessionRecords(ctx, []*SessionRecord{&duplicate}); err != nil {
		t.Fatal(err)
	}
	ambiguous, err := s.TraceNodes(ctx, "search-native-orphan", a, TraceNodesRequest{View: "flow", Query: "Read"})
	if err != nil || len(ambiguous.Nodes) != 1 || ambiguous.Nodes[0].Kind != "response" {
		t.Fatalf("ambiguous root=%+v %v", ambiguous, err)
	}
	if ambiguous.Nodes[0].ParentID != nil {
		t.Fatalf("ambiguous request silently chosen: %+v", ambiguous.Nodes[0])
	}
}
