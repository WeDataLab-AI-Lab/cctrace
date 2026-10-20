package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestTraceFlowRootPagesPastAgentChildren(t *testing.T) {
	// All requests share this database fixture, so they must remain sequential.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var records []*SessionRecord
	add := func(uuid, kind, file, call string, offset int, raw string) {
		records = append(records, &SessionRecord{SessionID: "root-agent-pages", UUID: uuid, RecordType: kind, Ts: base.Add(time.Duration(offset) * time.Second), Agent: "claude", UserID: "owner", ProfileEmail: "p", SourceFile: file, ToolUseID: call, Raw: json.RawMessage(raw)})
	}
	// Real notice content keeps this fixture independent of empty-metadata filtering.
	for i := 0; i < 4; i++ {
		add(fmt.Sprint("notice-", i), "system", "main", "", i, `{"subtype":"observed_notice","message":{"content":"Recorded observation"}}`)
	}
	add("first-input", "user", "main", "", 4, `{"message":{"role":"user","content":"First observation request"}}`)
	add("dispatch", "assistant", "main", "", 5, `{"message":{"id":"dispatch-message","content":[{"type":"tool_use","id":"child-dispatch","name":"Agent","input":{"prompt":"Work"}}]}}`)
	for i := 0; i < 50; i++ {
		add(fmt.Sprint("child-", i), "system", "child", "child-dispatch", 6+i, `{"subtype":"observed_notice","message":{"content":"Child observation"}}`)
	}
	for i := 0; i < 3; i++ {
		add(fmt.Sprint("later-input-", i), "user", "main", "", 100+i, `{"message":{"role":"user","content":"Later observation request"}}`)
	}
	for i := 0; i < 27; i++ {
		add(fmt.Sprint("child-tail-", i), "system", "child", "child-dispatch", 110+i, `{"subtype":"observed_notice","message":{"content":"Child observation"}}`)
	}
	// Exceed the exact-parent prefilter's reference cap. Its safe fallback must
	// still paginate correctly through rows removed by node parent resolution.
	var dispatches []any
	for i := 0; i < 129; i++ {
		call := fmt.Sprint("overflow-dispatch-", i)
		dispatches = append(dispatches, map[string]any{"type": "tool_use", "id": call, "name": "Agent"})
		add(fmt.Sprint("overflow-child-", i), "system", "child", call, 150+i, `{"subtype":"observed_notice","message":{"content":"Child observation"}}`)
	}
	extra, err := json.Marshal(map[string]any{"message": map[string]any{"content": dispatches}})
	if err != nil {
		t.Fatal(err)
	}
	add("overflow-parents", "assistant", "main", "", 5, string(extra))
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "observation"} {
		t.Run("query="+query, func(t *testing.T) {
			access := SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p"}
			read := func(cursor string) TraceNodesResponse {
				t.Helper()
				page, err := s.TraceNodes(ctx, "root-agent-pages", access, TraceNodesRequest{View: "flow", Limit: 20, Cursor: cursor, Query: query})
				if err != nil {
					t.Fatal(err)
				}
				return page
			}
			ids := func(page TraceNodesResponse) []string {
				out := []string{}
				for _, node := range page.Nodes {
					out = append(out, node.NodeID)
				}
				return out
			}
			first := read("")
			if len(first.Nodes) != 5 || first.Coverage.ProcessedRows != 22 || first.NextCursor == nil || first.Coverage.State == "complete" {
				t.Fatalf("filtered 22-row root page lost continuation: nodes=%d rows=%d next=%v coverage=%s", len(first.Nodes), first.Coverage.ProcessedRows, first.NextCursor, first.Coverage.State)
			}
			if first.Coverage.RowLimit != 22 {
				t.Fatalf("root source budget is misreported: got %d want 22", first.Coverage.RowLimit)
			}
			forward := ids(first)
			page := first
			emptyPages := 0
			seen := map[string]bool{}
			for page.NextCursor != nil {
				cursor := *page.NextCursor
				if seen[cursor] || len(seen) > len(records) {
					t.Fatal("forward source cursor failed to advance")
				}
				seen[cursor] = true
				page = read(cursor)
				if len(page.Nodes) == 0 {
					emptyPages++
					if page.NextCursor == nil && page.Coverage.State != "complete" {
						t.Fatal("empty terminal page is not complete")
					}
				}
				forward = append(forward, ids(page)...)
			}
			if len(forward) != 8 || emptyPages == 0 || page.Coverage.State != "complete" {
				t.Fatalf("root traversal: nodes=%d empty_pages=%d coverage=%s", len(forward), emptyPages, page.Coverage.State)
			}
			if len(page.Nodes) != 0 || page.PrevCursor == nil {
				t.Fatal("filtered terminal page must retain a path back to the previous roots")
			}
			unique := map[string]bool{}
			for _, id := range forward {
				if unique[id] {
					t.Fatal("forward traversal repeated a root node")
				}
				unique[id] = true
			}
			backward := ids(page)
			seen = map[string]bool{}
			for page.PrevCursor != nil {
				cursor := *page.PrevCursor
				if seen[cursor] || len(seen) > len(records) {
					t.Fatal("backward source cursor failed to advance")
				}
				seen[cursor] = true
				page = read(cursor)
				backward = append(ids(page), backward...)
			}
			if !reflect.DeepEqual(backward, forward) {
				t.Fatalf("backward root traversal differs: got %d nodes want %d", len(backward), len(forward))
			}
		})
	}
}
