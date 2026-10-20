package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"cctrace/internal/sessiontrace"
)

func TestTraceNodesReadPartialSourcesAndExactDetails(t *testing.T) {
	// Shared test database fixtures are serialized by acquireTestStore.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	// The shared source reset does not clear independent trace work/leases.
	// An earlier range demand would otherwise consume our single worker chunk.
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{Ts: base, SessionID: "nodes", UUID: "request", RecordType: "api-request", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: json.RawMessage(`{"type":"api-request","id":"local-request","keep":0,"tail":["blob"],"params":{"model":"claude"}}`)},
		{Ts: time.Unix(0, 0), SessionID: "nodes", UUID: "blob", RecordType: "api-request-blob", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: json.RawMessage(`{"type":"api-request-blob","hash":"blob","message":{"role":"user","content":"PRIVATE INPUT"}}`)},
		{Ts: base.Add(time.Second), SessionID: "nodes", UUID: "response", RecordType: "assistant", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: json.RawMessage(`{"type":"assistant","requestRef":"local-request","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"Inspect file"},{"type":"tool_use","id":"call","name":"Read","input":{"file_path":"/private"}}],"usage":{"input_tokens":12,"cache_read_input_tokens":7}}}`)},
		{Ts: base.Add(2 * time.Second), SessionID: "nodes", UUID: "result", RecordType: "user", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: json.RawMessage(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","is_error":false,"content":"PRIVATE OUTPUT"}]}}`)},
		{Ts: base.Add(1500 * time.Millisecond), SessionID: "nodes", UUID: "hook", RecordType: "attachment", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: json.RawMessage(`{"type":"attachment","attachment":{"type":"hook_success","hookName":"policy","toolUseID":"call","stdout":"allowed"}}`)},
	}
	for _, record := range records {
		if record.RecordType != "api-request-blob" {
			record.AccountID = "acct"
		}
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 1}
	roots, err := s.TraceNodes(ctx, "nodes", a, TraceNodesRequest{Limit: 100})
	if err != nil || len(roots.Nodes) != 1 || roots.Nodes[0].Kind != "request" {
		t.Fatalf("roots=%+v err=%v", roots, err)
	}
	children, err := s.TraceNodes(ctx, "nodes", a, TraceNodesRequest{ParentID: roots.Nodes[0].NodeID, Limit: 100})
	if err != nil || len(children.Nodes) != 1 || children.Nodes[0].Kind != "response" {
		t.Fatalf("children=%+v err=%v", children, err)
	}
	tools, err := s.TraceNodes(ctx, "nodes", a, TraceNodesRequest{ParentID: children.Nodes[0].NodeID, Limit: 100})
	if err != nil || len(tools.Nodes) != 1 || tools.Nodes[0].Kind != "tool" {
		t.Fatalf("tools=%+v err=%v", tools, err)
	}
	if !tools.Nodes[0].HasChildren {
		t.Fatal("tool children hidden despite observed hook/result")
	}
	tool := tools.Nodes[0]
	if tool.StartedAt == nil || tool.EndedAt == nil || !tool.StartedAt.Equal(base.Add(time.Second)) || !tool.EndedAt.Equal(base.Add(2*time.Second)) || tool.Status != "completed" {
		t.Fatalf("observed tool interval=%+v", tool)
	}
	if roots.Nodes[0].StartedAt != nil || roots.Nodes[0].EndedAt != nil {
		t.Fatal("request execution interval was inferred")
	}
	nested, e := s.TraceNodes(ctx, "nodes", a, TraceNodesRequest{ParentID: tools.Nodes[0].NodeID, Limit: 100})
	if e != nil || len(nested.Nodes) != 2 {
		t.Fatalf("tool subtree=%+v %v", nested, e)
	}
	for _, child := range nested.Nodes {
		selected, err := s.TraceNodeDetail(ctx, "nodes", a, child.NodeID)
		if err != nil || selected.Node.ParentID == nil || *selected.Node.ParentID != tools.Nodes[0].NodeID {
			t.Fatalf("subtree detail=%+v err=%v", selected, err)
		}
		if child.Kind == "event" && selected.Output.State != "observed" {
			t.Fatalf("result output=%+v", selected.Output)
		}
	}
	detail, err := s.TraceNodeDetail(ctx, "nodes", a, tools.Nodes[0].NodeID)
	if err != nil || detail.Input.State != "observed" || detail.Output.State != "observed" {
		t.Fatalf("tool detail=%+v err=%v", detail, err)
	}
	if detail.Node.Title != tools.Nodes[0].Title {
		t.Fatalf("tool detail title=%q list=%q", detail.Node.Title, tools.Nodes[0].Title)
	}
	if detail.Node.StartedAt == nil || detail.Node.EndedAt == nil || !detail.Node.StartedAt.Equal(*tool.StartedAt) || !detail.Node.EndedAt.Equal(*tool.EndedAt) || detail.Node.Status != tool.Status {
		t.Fatalf("list/detail interval differs: list=%+v detail=%+v", tool, detail.Node)
	}
	native, err := s.TraceNodeDetail(ctx, "nodes", a, children.Nodes[0].NodeID)
	if err != nil || native.Request == nil || native.Request.State != "observed" {
		t.Fatalf("native detail=%+v err=%v", native.Request, err)
	}
	if _, err = s.TraceNodeDetail(ctx, "nodes", SessionAnalysisAccess{User: true, UserID: "other"}, tools.Nodes[0].NodeID); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("foreign access=%v", err)
	}
	lease, e := s.TraceDemand(ctx, "nodes", a, TraceDemandRequest{Mode: "overview"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunTraceChunk(ctx); e != nil {
		t.Fatal(e)
	}
	from, to := base.Add(-time.Second), base.Add(time.Minute)
	legacy, e := s.TraceEvents(ctx, "nodes", a, TraceEventsRequest{SnapshotID: lease.SnapshotID, From: &from, To: &to, Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	legacyFound := false
	for _, event := range legacy.Events {
		if event.Kind == "tool" {
			selected, e := s.TraceNodeDetail(ctx, "nodes", a, event.EventID)
			if e != nil || selected.Node.Kind != "tool" || selected.Output.State != "observed" {
				t.Fatalf("legacy event detail=%+v %v", selected, e)
			}
			legacyFound = true
		}
	}
	if !legacyFound {
		t.Fatal("legacy tool fixture absent")
	}
	// A request can reference an immutable accumulated assistant prefix rather
	// than one transcript row. The index carries source keys, never copied text.
	message := map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "text", "text": "b"}}}
	hash, _ := sessiontrace.MessageHash(message)
	secondRaw, _ := json.Marshal(map[string]any{"message": map[string]any{"id": "parts", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "b"}}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 19}}, "cctraceReferences": map[string]any{"version": 1, "messages": []any{map[string]any{"hash": hash, "records": []any{map[string]any{"uuid": "part1", "block_start": 0, "block_end": 1}, map[string]any{"uuid": "part2", "block_start": 0, "block_end": 1}}}}}})
	newRecipe, _ := json.Marshal(map[string]any{"type": "api-request", "id": "next", "base": "local-request", "keep": 1, "tail": []any{hash}, "params": map[string]any{"model": "claude"}})
	more := []*SessionRecord{
		{Ts: base.Add(3 * time.Second), SessionID: "nodes", UUID: "part1", RecordType: "assistant", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: json.RawMessage(`{"message":{"id":"parts","role":"assistant","content":[{"type":"text","text":"a"}],"usage":{"input_tokens":20,"output_tokens":1}}}`)},
		{Ts: base.Add(4 * time.Second), SessionID: "nodes", UUID: "part2", RecordType: "assistant", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: secondRaw},
		{Ts: base.Add(5 * time.Second), SessionID: "nodes", UUID: "next", RecordType: "api-request", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: newRecipe},
	}
	for _, record := range more {
		record.AccountID = "acct"
	}
	if err = s.InsertSessionRecords(ctx, more); err != nil {
		t.Fatal(err)
	}
	page, err := s.TraceNodes(ctx, "nodes", a, TraceNodesRequest{Limit: 1})
	if err != nil || page.NextCursor == nil {
		t.Fatalf("first page=%+v %v", page, err)
	}
	page2, err := s.TraceNodes(ctx, "nodes", a, TraceNodesRequest{Limit: 1, Cursor: *page.NextCursor})
	if err != nil || len(page2.Nodes) != 1 || page2.Nodes[0].NodeID == page.Nodes[0].NodeID {
		t.Fatalf("second page=%+v %v", page2, err)
	}
	all, err := s.TraceNodes(ctx, "nodes", a, TraceNodesRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range all.Nodes {
		if node.Kind == "request" && node.NodeID != roots.Nodes[0].NodeID {
			detail, e := s.TraceNodeDetail(ctx, "nodes", a, node.NodeID)
			if e != nil || detail.Request == nil || detail.Request.State != "observed" || len(detail.Request.Body["messages"].([]any)) != 2 {
				t.Fatalf("prefix request=%+v %v", detail.Request, e)
			}
		}
		if node.Kind == "response" {
			detail, e := s.TraceNodeDetail(ctx, "nodes", a, node.NodeID)
			if e != nil {
				t.Fatal(e)
			}
			values := detail.Usage["values"].(map[string]any)
			if values["output_tokens"] != json.Number("19") {
				t.Fatalf("fragment usage=%v", values)
			}
			if detail.Usage["previous"] == nil {
				t.Fatal("previous response absent")
			}
		}
	}
	if err = s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: base.Add(time.Minute), SessionID: "nodes", UUID: "change", RecordType: "user", Agent: "claude", ProfileEmail: "p", UserID: "owner", SourceFile: "main", Raw: json.RawMessage(`{"message":{"role":"user","content":"new input"}}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TraceNodes(ctx, "nodes", a, TraceNodesRequest{Limit: 1, Cursor: *page.NextCursor}); !errors.Is(err, ErrTraceSnapshot) {
		t.Fatalf("stale cursor=%v", err)
	}
}
