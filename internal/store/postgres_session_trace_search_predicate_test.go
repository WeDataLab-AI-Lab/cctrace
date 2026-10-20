package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestTraceFlowSearchVisibleMetadataAndLiteralQueries(t *testing.T) {
	// The container fixture is shared; source/login cases must remain sequential.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	var records []*SessionRecord
	add := func(agent, kind, file, login string, raw map[string]any) {
		body, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, &SessionRecord{SessionID: "search-visible-metadata", UUID: fmt.Sprint("row-", len(records)), Ts: time.Date(2026, 10, 19, 0, 0, len(records), 0, time.UTC), RecordType: kind, Agent: agent, UserID: "owner", ProfileEmail: "p", AccountID: "acct", LoginEmail: login, SourceFile: file, Raw: body})
	}
	literal := `Literal%_.*[]()\Marker"{}+$?^|`
	add("claude", "user", "claude", "visible", map[string]any{"message": map[string]any{"role": "user", "content": "StringTitle " + literal}})
	add("claude", "assistant", "claude", "visible", map[string]any{"message": map[string]any{"id": "response", "content": []any{
		map[string]any{"type": "text", "text": "BlockTitle 공개 미러 MixedCase " + literal},
		map[string]any{"type": "thinking", "thinking": "HIDDEN_THINKING_ONLY"},
		map[string]any{"type": "tool_use", "id": "tool", "name": "WidgetName", "input": map[string]any{"command": "HIDDEN_ARGUMENT_ONLY"}},
	}}})
	add("claude", "user", "claude", "visible", map[string]any{"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool", "content": "HIDDEN_RESULT_ONLY"}}}})
	add("claude", "system", "top", "visible", map[string]any{"content": "TopString"})
	add("claude", "system", "top", "visible", map[string]any{"content": []any{map[string]any{"type": "text", "text": "TopBlock"}}})
	add("codex", "user", "codex", "visible", map[string]any{"payload": map[string]any{"role": "user", "content": "PayloadString"}})
	add("codex", "assistant", "codex", "visible", map[string]any{"payload": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "input_text", "text": "PayloadInput"}, map[string]any{"type": "output_text", "text": "PayloadOutput"},
	}}})
	add("codex", "tool_call", "codex", "visible", map[string]any{"payload": map[string]any{"type": "function_call", "call_id": "payload-call", "name": "PayloadName", "arguments": "HIDDEN_PAYLOAD_ARGS_ONLY"}})
	records[len(records)-1].ToolName = "ColumnOnlyTool"
	add("claude", "user", "foreign", "hidden", map[string]any{"message": map[string]any{"content": "FOREIGN_LOGIN_ONLY"}})
	add("claude", "attachment", "claude", "visible", map[string]any{"type": "attachment", "attachment": map[string]any{"type": "file-history-snapshot", "content": "ATTACHMENT_ONLY"}})
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p", LoginEmail: "visible"}
	for _, tc := range []struct {
		query string
		match bool
	}{
		{"HIDDEN_ARGUMENT_ONLY", false}, {"HIDDEN_RESULT_ONLY", false}, {"HIDDEN_THINKING_ONLY", false}, {"HIDDEN_PAYLOAD_ARGS_ONLY", false},
		{"StringTitle", true}, {"BlockTitle", true}, {"공개 미러", true}, {"mIxEdCaSe", true}, {"WidgetName", true}, {"ATTACHMENT_ONLY", false},
		{"TopString", true}, {"TopBlock", true}, {"PayloadString", true}, {"PayloadInput", true}, {"PayloadOutput", true}, {"PayloadName", true}, {"ColumnOnlyTool", true},
		{literal, true}, {"%_.*[]()\\", true}, {"LiteralZZ", false}, {"[absent]", false}, {"Widget.*Name", false}, {"String_Title", false}, {".*missing", false}, {"FOREIGN_LOGIN_ONLY", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			page, err := s.TraceNodes(ctx, "search-visible-metadata", access, TraceNodesRequest{View: "flow", Limit: 20, Query: tc.query})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(page.Nodes) > 0; got != tc.match {
				t.Fatalf("search match=%t want=%t nodes=%d", got, tc.match, len(page.Nodes))
			}
		})
	}
	roots, err := s.TraceNodes(ctx, "search-visible-metadata", access, TraceNodesRequest{View: "flow", Query: "StringTitle"})
	if err != nil || len(roots.Nodes) != 1 || roots.Nodes[0].Kind != "turn" {
		t.Fatalf("search source group is not exact: nodes=%+v err=%v", roots.Nodes, err)
	}
	for _, tc := range []struct {
		query string
		match bool
	}{{"WidgetName", true}, {"HIDDEN_ARGUMENT_ONLY", false}} {
		page, err := s.TraceNodes(ctx, "search-visible-metadata", access, TraceNodesRequest{View: "flow", ParentID: roots.Nodes[0].NodeID, Query: tc.query})
		if err != nil || (len(page.Nodes) > 0) != tc.match {
			t.Fatalf("scoped search %q: nodes=%d match=%t err=%v", tc.query, len(page.Nodes), tc.match, err)
		}
	}
}
