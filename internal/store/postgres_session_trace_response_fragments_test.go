package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestTraceResponseChildrenAcrossFragments(t *testing.T) {
	// Requests use the shared fixture serially; no parallel subtests.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var records []*SessionRecord
	add := func(uuid, kind, account, file, login string, offset int, raw string) {
		records = append(records, &SessionRecord{SessionID: "fragment-children", UUID: uuid, RecordType: kind, Agent: "claude", UserID: "owner", ProfileEmail: "p", LoginEmail: login, AccountID: account, SourceFile: file, Ts: base.Add(time.Duration(offset) * time.Second), Raw: json.RawMessage(raw)})
	}
	add("request", "api-request", "acct", "main", "visible", 0, `{"id":"request"}`)
	for i := 0; i < 120; i++ {
		add(fmt.Sprint("thinking-", i), "assistant", "acct", "main", "visible", i+1, `{"requestRef":"request","message":{"id":"shared-message","content":[{"type":"thinking","thinking":"Recorded thought"}]}}`)
	}
	add("tool-fragment", "assistant", "acct", "main", "visible", 121, `{"requestRef":"request","message":{"id":"shared-message","content":[{"type":"tool_use","id":"read-file","name":"Read","input":{"file_path":"/recorded/path"}}]}}`)
	add("tool-result", "user", "acct", "main", "visible", 122, `{"message":{"content":[{"type":"tool_result","tool_use_id":"read-file","content":"Recorded result"}]}}`)
	add("plain-request", "api-request", "other-account", "other-file", "visible", 130, `{"id":"plain-request"}`)
	add("plain-response", "assistant", "other-account", "other-file", "visible", 131, `{"requestRef":"plain-request","message":{"id":"shared-message","content":[{"type":"text","text":"Plain response"}]}}`)
	add("hidden-tool", "assistant", "other-account", "other-file", "hidden", 132, `{"requestRef":"plain-request","message":{"id":"shared-message","content":[{"type":"tool_use","id":"hidden-call","name":"Read"}]}}`)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p", LoginEmail: "visible"}
	roots, err := s.TraceNodes(ctx, "fragment-children", a, TraceNodesRequest{Limit: 100})
	if err != nil || len(roots.Nodes) != 1 {
		t.Fatalf("request roots=%d err=%v", len(roots.Nodes), err)
	}
	children, err := s.TraceNodes(ctx, "fragment-children", a, TraceNodesRequest{ParentID: roots.Nodes[0].NodeID, Limit: 1})
	if err != nil || len(children.Nodes) != 1 || !children.Nodes[0].HasChildren {
		t.Fatalf("canonical first fragment hid a later tool: nodes=%+v err=%v", children.Nodes, err)
	}
	responseID := children.Nodes[0].NodeID
	tools, err := s.TraceNodes(ctx, "fragment-children", a, TraceNodesRequest{ParentID: responseID, Limit: 100})
	seen := map[string]bool{}
	for err == nil && len(tools.Nodes) == 0 && tools.NextCursor != nil {
		cursor := *tools.NextCursor
		if seen[cursor] || len(seen) > len(records) {
			t.Fatal("fragment child cursor did not advance")
		}
		seen[cursor] = true
		tools, err = s.TraceNodes(ctx, "fragment-children", a, TraceNodesRequest{ParentID: responseID, Cursor: cursor, Limit: 100})
	}
	if err != nil || len(tools.Nodes) != 1 || tools.Nodes[0].ParentID == nil || *tools.Nodes[0].ParentID != responseID {
		t.Fatalf("fragment tool is not reachable: nodes=%+v err=%v", tools.Nodes, err)
	}
	detail, err := s.TraceNodeDetail(ctx, "fragment-children", a, tools.Nodes[0].NodeID)
	if err != nil || detail.Input.State != "observed" || detail.Output.State != "observed" || detail.Output.Value != "Recorded result" {
		t.Fatalf("exact tool sources were lost: input=%+v output=%+v sources=%d err=%v", detail.Input, detail.Output, len(detail.RawRecords), err)
	}
	input, _ := detail.Input.Value.(map[string]any)
	sources := map[string]bool{}
	for _, source := range detail.RawRecords {
		sources[source.UUID] = true
	}
	if input["file_path"] != "/recorded/path" || !sources["tool-fragment"] || !sources["tool-result"] || sources["hidden-tool"] {
		t.Fatal("exact tool input/result source identity was not preserved")
	}
	var plainRequest traceNodeRow
	plainRequest.record = records[len(records)-3]
	if err = s.pool.QueryRow(ctx, `SELECT id FROM session_records WHERE session_id=$1 AND uuid='plain-request'`, "fragment-children").Scan(&plainRequest.id); err != nil {
		t.Fatal(err)
	}
	plain, err := s.TraceNodes(ctx, "fragment-children", a, TraceNodesRequest{ParentID: encodeNodeKey(plainRequest.key("request", -1)), Limit: 20})
	if err != nil || len(plain.Nodes) != 1 || plain.Nodes[0].HasChildren {
		t.Fatalf("foreign source/account/login tool leaked into response: nodes=%+v err=%v", plain.Nodes, err)
	}
}
