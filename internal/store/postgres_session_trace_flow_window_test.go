package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"cctrace/internal/sessiontrace"
)

func TestTraceFlowSourceWindowBoundsEveryPageWithoutLosingNodes(t *testing.T) {
	// The migrated database fixture is shared, so these reads run sequentially.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(3)
	var records []*SessionRecord
	add := func(sid, uuid, kind string, offset int, raw string) {
		records = append(records, &SessionRecord{SessionID: sid, UUID: uuid, RecordType: kind,
			Ts: base.Add(time.Duration(offset) * time.Second), Agent: "claude", UserID: "owner",
			ProfileEmail: "p", SourceFile: "window.jsonl", Raw: json.RawMessage(raw)})
	}
	for i := 0; i < 50; i++ {
		add("flow-window-roots", fmt.Sprint("input-", i), "user", i,
			fmt.Sprintf(`{"message":{"role":"user","content":"Request %d"}}`, i))
	}
	add("flow-window-fragments", "input", "user", 0, `{"message":{"role":"user","content":"Inspect the full execution"}}`)
	for i := 0; i < 120; i++ {
		content := `[{"type":"thinking","thinking":"Recorded reasoning"}]`
		if i == 60 {
			content = `[{"type":"tool_use","id":"window-call","name":"Read","input":{"file_path":"example.txt"}}]`
		}
		add("flow-window-fragments", fmt.Sprint("fragment-", i), "assistant", i+1,
			`{"message":{"id":"window-response","content":`+content+`}}`)
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p"}
	read := func(sid string, q TraceNodesRequest) TraceNodesResponse {
		t.Helper()
		page, err := s.TraceNodes(ctx, sid, access, q)
		if err != nil {
			t.Fatal(err)
		}
		if page.Coverage.ProcessedRows > 22 || page.Coverage.RowLimit != 22 {
			t.Fatalf("source work exceeds the bounded page: processed=%d limit=%d", page.Coverage.ProcessedRows, page.Coverage.RowLimit)
		}
		if len(page.Nodes) > q.Limit {
			t.Fatalf("node upper bound exceeded: %d > %d", len(page.Nodes), q.Limit)
		}
		return page
	}
	walk := func(sid string, q TraceNodesRequest) ([]sessiontrace.Node, TraceNodesResponse, int) {
		t.Helper()
		var nodes []sessiontrace.Node
		seenNodes, seenCursors := map[string]bool{}, map[string]bool{}
		emptyPages := 0
		for {
			page := read(sid, q)
			if len(page.Nodes) == 0 {
				emptyPages++
			}
			for _, node := range page.Nodes {
				if seenNodes[node.NodeID] {
					t.Fatal("a source boundary repeated a visible node")
				}
				seenNodes[node.NodeID] = true
				nodes = append(nodes, node)
			}
			if page.NextCursor == nil {
				if page.Coverage.State != "complete" {
					t.Fatal("terminal page is not complete")
				}
				return nodes, page, emptyPages
			}
			q.Cursor = *page.NextCursor
			if seenCursors[q.Cursor] || len(seenCursors) > 20 {
				t.Fatal("source cursor stopped advancing")
			}
			seenCursors[q.Cursor] = true
		}
	}
	roots, _, _ := walk("flow-window-roots", TraceNodesRequest{View: "flow", Limit: 100})
	if len(roots) != 50 {
		t.Fatalf("root traversal lost requests: got %d want 50", len(roots))
	}
	fragmentRoot := read("flow-window-fragments", TraceNodesRequest{View: "flow", Limit: 100})
	if len(fragmentRoot.Nodes) != 1 || fragmentRoot.Nodes[0].Kind != "turn" {
		t.Fatalf("expected one input group, got %v", fragmentRoot.Nodes)
	}
	group := fragmentRoot.Nodes[0].NodeID
	children, _, _ := walk("flow-window-fragments", TraceNodesRequest{View: "flow", ParentID: group, Limit: 100})
	response := ""
	for _, node := range children {
		if node.Kind == "response" && node.HasChildren {
			response = node.NodeID
		}
	}
	if response == "" {
		t.Fatal("later tool fragment did not preserve an expandable canonical response")
	}
	tools, terminal, emptyPages := walk("flow-window-fragments", TraceNodesRequest{View: "flow", ParentID: response, Limit: 100})
	if len(tools) != 1 || tools[0].Kind != "tool" || emptyPages < 2 {
		t.Fatalf("fragment traversal lost its tool or filtered pages: tools=%d empty=%d", len(tools), emptyPages)
	}
	if len(terminal.Nodes) != 0 || terminal.PrevCursor == nil {
		t.Fatal("an empty child terminal must retain its reverse cursor")
	}
	back := TraceNodesRequest{View: "flow", ParentID: response, Limit: 100, Cursor: *terminal.PrevCursor}
	found := false
	seenBack, reachedStart := map[string]bool{}, false
	for i := 0; i < 20; i++ {
		if seenBack[back.Cursor] {
			t.Fatal("reverse source cursor stopped advancing")
		}
		seenBack[back.Cursor] = true
		page := read("flow-window-fragments", back)
		for _, node := range page.Nodes {
			found = found || node.NodeID == tools[0].NodeID
		}
		if page.PrevCursor == nil {
			reachedStart = true
			break
		}
		back.Cursor = *page.PrevCursor
	}
	if !found || !reachedStart {
		t.Fatal("reverse traversal cannot recover the tool through empty fragment pages")
	}
	timed, _, _ := walk("flow-window-fragments", TraceNodesRequest{View: "timing", GroupID: group, Limit: 100})
	found = false
	for _, node := range timed {
		found = found || node.NodeID == tools[0].NodeID
	}
	if !found {
		t.Fatal("timing traversal lost the tool beyond its first source window")
	}
}
