package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cctrace/internal/sessiontrace"
)

func TestTraceInputGroupsLegacyPagingContextAndSourceScope(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var records []*SessionRecord
	add := func(id, kind string, offset int, raw string) {
		records = append(records, &SessionRecord{SessionID: "groups", UUID: id, RecordType: kind, Ts: at.Add(time.Duration(offset) * time.Second), Agent: "codex", UserID: "owner", ProfileEmail: "p", AccountID: "acct", SourceFile: "main", Raw: json.RawMessage(raw)})
	}
	add("agents", "user", 0, `{"payload":{"role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions\nACTUAL RULES"}]}}`)
	add("settings", "context", 1, `{"payload":{"model":"test","effort":"high","history_mode":"fork"}}`)
	add("claude-instructions", "attachment", 1, `{"attachment":{"type":"instructions","files":[{"path":"/qa/CLAUDE.md","content":"CLAUDE RULES"}]}}`)
	records[len(records)-1].Agent = "claude"
	records[len(records)-1].Ts = time.Unix(0, 0)
	add("first", "user", 2, `{"payload":{"role":"user","content":[{"type":"input_text","text":"first request"}]}}`)
	add("tool", "tool_call", 3, `{"payload":{"type":"function_call","name":"exec_command","call_id":"call","arguments":"{}"}}`)
	records[len(records)-1].ToolCallID = "call"
	add("usage", "usage", 4, `{"payload":{"response_id":"unjoined-response","usage":{"input_tokens":10}}}`)
	add("result", "tool_output", 5, `{"payload":{"type":"function_call_output","call_id":"call","output":"RESULT"}}`)
	records[len(records)-1].ToolCallID = "call"
	add("automatic", "user", 120, `{"payload":{"role":"user","content":[{"type":"input_text","text":"automatic context"}]}}`)
	records[len(records)-1].PromptSource = "system"
	add("untimed-hook", "attachment", -1, `{"attachment":{"type":"hook_success","hookName":"policy","toolUseID":"call","stdout":"ok"}}`)
	records[len(records)-1].Ts = time.Unix(0, 0)
	for i := 0; i < 105; i++ {
		raw := `{"payload":{"type":"reasoning"}}`
		if i == 0 {
			raw = `{"payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"RECORDED SUMMARY"}],"encrypted_content":"OPAQUE"}}`
		}
		add(fmt.Sprintf("reason-%d", i), "reasoning", 6+i, raw)
	}
	add("second", "user", 200, `{"payload":{"role":"user","content":[{"type":"input_text","text":"second request"}]}}`)
	add("answer", "assistant", 201, `{"payload":{"role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`)
	add("foreign", "reasoning", 202, `{"payload":{"type":"reasoning"}}`)
	records[len(records)-1].ProfileEmail = "foreign"
	for _, scope := range []string{"account", "file"} {
		add("other-"+scope, "tool_output", 5, `{"payload":{"type":"function_call_output","call_id":"call","output":"OTHER SCOPE"}}`)
		r := records[len(records)-1]
		r.ToolCallID = "call"
		if scope == "account" {
			r.AccountID = "other"
		} else {
			r.SourceFile = "other"
		}
	}
	add("bookkeeping", "last-prompt", -10, `{}`)
	records[len(records)-1].Agent = "claude"
	records[len(records)-1].Ts = time.Unix(0, 0)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	// Mixed-profile sessions are intentionally unavailable to personal access.
	// An explicitly scoped administrator still must not cross the selected source.
	a := SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p"}
	root, err := s.TraceNodes(ctx, "groups", a, TraceNodesRequest{Limit: 100})
	var groups []sessiontrace.Node
	for _, node := range root.Nodes {
		if node.Kind == "turn" {
			groups = append(groups, node)
		} else if node.ParentID != nil {
			t.Fatalf("cross-scope row grouped: %+v", node)
		}
	}
	if err != nil || len(root.Nodes) != 4 || len(groups) != 2 {
		t.Fatalf("input groups=%+v err=%v", root, err)
	}
	if groups[0].GroupingBasis != "source_sequence" {
		t.Fatalf("legacy grouping=%+v", groups[0])
	}
	page, err := s.TraceNodes(ctx, "groups", a, TraceNodesRequest{ParentID: groups[0].NodeID, Limit: 100})
	if err != nil || len(page.Nodes) == 0 || len(page.Nodes) > 100 || page.NextCursor == nil {
		t.Fatalf("first group page=%+v err=%v", page, err)
	}
	all := append([]sessiontrace.Node{}, page.Nodes...)
	if page.Coverage.ProcessedRows > 20 {
		t.Fatalf("execution source page exceeds budget: %d", page.Coverage.ProcessedRows)
	}
	for cursor := page.NextCursor; cursor != nil; {
		next, err := s.TraceNodes(ctx, "groups", a, TraceNodesRequest{ParentID: groups[0].NodeID, Limit: 100, Cursor: *cursor})
		if err != nil {
			t.Fatal(err)
		}
		if next.Coverage.ProcessedRows > 20 {
			t.Fatalf("execution source page exceeds budget: %d", next.Coverage.ProcessedRows)
		}
		all = append(all, next.Nodes...)
		cursor = next.NextCursor
	}
	if len(all) != 109 {
		t.Fatalf("group members=%d want109", len(all))
	}
	summaryChecked := false
	for _, node := range all {
		if node.ParentRelation != "source_sequence" {
			t.Fatalf("invented causal parent=%+v", node)
		}
		if node.Kind == "tool" {
			children, err := s.TraceNodes(ctx, "groups", a, TraceNodesRequest{ParentID: node.NodeID, Limit: 100})
			if err != nil || len(children.Nodes) != 2 {
				t.Fatalf("exact result children=%+v %v", children, err)
			}
		}
		if node.Title == "기록된 추론" && !summaryChecked {
			detail, err := s.TraceNodeDetail(ctx, "groups", a, node.NodeID)
			if err != nil || detail.Output.State != "observed" {
				t.Fatalf("recorded reasoning summary missing: %+v %v", detail.Output, err)
			}
			encoded, _ := json.Marshal(detail.Output.Value)
			if !strings.Contains(string(encoded), "RECORDED SUMMARY") || strings.Contains(string(encoded), "OPAQUE") {
				t.Fatalf("reasoning plaintext/encrypted confused: %s", encoded)
			}
			summaryChecked = true
		}
	}
	contexts, err := s.TraceNodes(ctx, "groups", a, TraceNodesRequest{View: "context", Limit: 100})
	if err != nil || len(contexts.Nodes) != 3 {
		t.Fatalf("context list=%+v %v", contexts, err)
	}
	for _, node := range contexts.Nodes {
		d, err := s.TraceNodeDetail(ctx, "groups", a, node.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Node.Kind != node.Kind || d.Node.NodeID != node.NodeID {
			t.Fatalf("list/detail classification differs: %s/%s", node.Kind, d.Node.Kind)
		}
		if node.Kind == "instructions" {
			value := d.Context.Value.(map[string]any)
			instructions := value["instructions"].([]any)
			if len(instructions) != 1 || instructions[0].(map[string]any)["body"].(sessiontrace.AuditValue).State != "observed" {
				t.Fatalf("instruction body lost: %+v", value)
			}
		}
		if node.Kind == "context" {
			oldKey, _ := decodeNodeKey(node.NodeID)
			oldKey.Kind = "instructions"
			oldID := encodeNodeKey(oldKey)
			oldDetail, err := s.TraceNodeDetail(ctx, "groups", a, oldID)
			if err != nil || oldDetail.Node.Kind != "context" || oldDetail.Node.NodeID != oldID {
				t.Fatalf("old context link lost: kind=%s %v", oldDetail.Node.Kind, err)
			}
		}
		if d.Output.State != "not_applicable" {
			t.Fatalf("irrelevant IO=%+v", d.Output)
		}
	}
	if _, err := s.TraceNodes(ctx, "groups", a, TraceNodesRequest{View: "context", ParentID: groups[0].NodeID, Cursor: *page.NextCursor, Limit: 100}); err != ErrTraceInvalid {
		t.Fatalf("view cursor scope=%v", err)
	}
	metadata, err := s.TraceNodes(ctx, "groups", a, TraceNodesRequest{View: "metadata", Limit: 100})
	if err != nil || len(metadata.Nodes) != 1 || metadata.Nodes[0].Kind != "metadata" {
		t.Fatalf("metadata=%+v %v", metadata, err)
	}
}

func TestTraceLegacyFlatToolKeepsRecordedInputOutput(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	rows := []*SessionRecord{
		{UUID: "flat-agents", RecordType: "user", Raw: json.RawMessage(`{"type":"message","role":"user","content":"# AGENTS.md instructions\nFLAT RULES"}`)},
		{UUID: "flat-call", RecordType: "tool_call", ToolName: "exec_command", Raw: json.RawMessage(`{"type":"function_call","name":"exec_command","call_id":"flat-call","arguments":{"cmd":"go test ./internal/sessiontrace"}}`)},
		{UUID: "flat-result", RecordType: "tool_output", Raw: json.RawMessage(`{"type":"function_call_output","call_id":"flat-call","output":"FLAT RESULT"}`)},
	}
	for i, row := range rows {
		row.SessionID = "flat"
		row.Ts = base.Add(time.Duration(i) * time.Second)
		if row.RecordType != "user" {
			row.ToolCallID = "flat-call"
		}
		row.Agent = "codex"
		row.UserID = "owner"
		row.ProfileEmail = "p"
		row.SourceFile = "flat"
	}
	rows[0].ProjectHash = "trace-root-blocked"
	if err := s.InsertSessionRecords(ctx, rows); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 1}
	root, err := s.TraceNodes(ctx, "flat", a, TraceNodesRequest{Limit: 100})
	if err != nil || len(root.Nodes) != 1 {
		t.Fatalf("flat root count=%d err=%v", len(root.Nodes), err)
	}
	if !strings.Contains(root.Nodes[0].Title, "go test ./internal/sessiontrace") {
		t.Fatalf("flat title lost arguments: %q", root.Nodes[0].Title)
	}
	contexts, err := s.TraceNodes(ctx, "flat", a, TraceNodesRequest{View: "context", Limit: 100})
	if err != nil || len(contexts.Nodes) != 1 || contexts.Nodes[0].Kind != "instructions" {
		t.Fatalf("flat instructions=%+v %v", contexts, err)
	}
	instruction, err := s.TraceNodeDetail(ctx, "flat", a, contexts.Nodes[0].NodeID)
	if err != nil || instruction.Context.State != "observed" {
		t.Fatalf("flat instruction body lost: %+v %v", instruction.Context, err)
	}
	d, err := s.TraceNodeDetail(ctx, "flat", a, root.Nodes[0].NodeID)
	if err != nil || d.Input.State != "observed" || d.Output.State != "observed" || d.Output.Value != "FLAT RESULT" {
		t.Fatalf("flat detail input=%+v output=%+v err=%v", d.Input, d.Output, err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO blocked_projects(project_hash) VALUES('trace-root-blocked')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM blocked_projects WHERE project_hash='trace-root-blocked'`)
	})
	for _, access := range []SessionAnalysisAccess{a, {UserID: "owner"}} {
		if _, err = s.TraceNodes(ctx, "flat", access, TraceNodesRequest{Limit: 100}); err != ErrSessionAnalysisNotFound {
			t.Fatalf("blocked nonexecution source escaped root authorization: %v", err)
		}
		if _, err = s.TraceNodeDetail(ctx, "flat", access, root.Nodes[0].NodeID); err != ErrSessionAnalysisNotFound {
			t.Fatalf("blocked source escaped detail authorization: %v", err)
		}
	}
}

func TestTraceInputGroupsRecoverNestedNativeTurnIdentity(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	rows := []*SessionRecord{
		{UUID: "input-a", RecordType: "user", Raw: json.RawMessage(`{"payload":{"role":"user","content":[{"type":"input_text","text":"first"}],"internal_chat_message_metadata_passthrough":{"turn_id":"turn-a"}}}`)},
		{UUID: "000-canonical-input", RecordType: "user", Raw: json.RawMessage(`{"payload":{"role":"user","content":[{"type":"input_text","text":"canonical input"}],"internal_chat_message_metadata_passthrough":{"turn_id":"turn-a"}}}`)},
		{UUID: "input-a-followup", RecordType: "user", Raw: json.RawMessage(`{"payload":{"role":"user","content":[{"type":"input_text","text":"more first-turn input"}],"internal_chat_message_metadata_passthrough":{"turn_id":"turn-a"}}}`)},
		{UUID: "input-b", RecordType: "user", Raw: json.RawMessage(`{"payload":{"role":"user","content":[{"type":"input_text","text":"second"}],"internal_chat_message_metadata_passthrough":{"turn_id":"turn-b"}}}`)},
		{UUID: "late-usage-a", RecordType: "usage", Raw: json.RawMessage(`{"payload":{"turn_id":"turn-a","response_id":"not-a-message-id","usage":{"input_tokens":20}}}`)},
		{UUID: "answer-b", RecordType: "assistant", Raw: json.RawMessage(`{"payload":{"role":"assistant","content":[{"type":"output_text","text":"second answer"}],"internal_chat_message_metadata_passthrough":{"turn_id":"turn-b"}}}`)},
		{UUID: "early-usage-a", RecordType: "usage", Raw: json.RawMessage(`{"payload":{"turn_id":"turn-a","usage":{"input_tokens":5}}}`)},
		{UUID: "unmatched-usage", RecordType: "usage", Raw: json.RawMessage(`{"payload":{"turn_id":"unknown-turn","usage":{"input_tokens":1}}}`)},
	}
	for i, r := range rows {
		r.SessionID = "exact-groups"
		r.Ts = base.Add(time.Duration(i) * time.Second)
		r.Agent = "codex"
		r.UserID = "owner"
		r.ProfileEmail = "p"
		r.SourceFile = "main"
		r.AccountID = "a"
	}
	rows[1].Ts = base
	rows[len(rows)-2].Ts = base.Add(-time.Second)
	if err := s.InsertSessionRecords(ctx, rows); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 1}
	root, err := s.TraceNodes(ctx, "exact-groups", a, TraceNodesRequest{Limit: 100})
	if err != nil || len(root.Nodes) != 3 {
		t.Fatalf("exact roots=%+v %v", root, err)
	}
	for i, parent := range root.Nodes {
		if parent.Kind == "usage" {
			if parent.ParentID != nil || parent.GroupingBasis != "" {
				t.Fatalf("unmatched ID guessed: %+v", parent)
			}
			continue
		}
		if parent.GroupingBasis != "exact_turn_id" {
			t.Fatalf("nested identity missing: %+v", parent)
		}
		if !strings.HasPrefix(parent.Title, "턴 · ") {
			t.Fatalf("native turn title=%q", parent.Title)
		}
		detail, err := s.TraceNodeDetail(ctx, "exact-groups", a, parent.NodeID)
		if err != nil || detail.Input.State != "observed" || detail.Node.Kind != "turn" {
			t.Fatalf("canonical group detail: %+v %v", detail.Node, err)
		}
		page, err := s.TraceNodes(ctx, "exact-groups", a, TraceNodesRequest{ParentID: parent.NodeID, Limit: 100})
		want := 2
		if i == 0 {
			want = 5
		}
		if err != nil || len(page.Nodes) != want {
			t.Fatalf("exact children=%+v %v", page, err)
		}
		for _, node := range page.Nodes {
			if node.ParentRelation != "turn_membership" {
				t.Fatalf("exact membership=%+v", node)
			}
			if node.Kind == "usage" && i != 0 {
				t.Fatal("usage attached by response/time proximity")
			}
		}
	}
}

func TestTraceDispatchParentsBatchCacheKeepsScopeAndCopies(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var records []*SessionRecord
	add := func(uuid, file, account, call string) {
		raw, _ := json.Marshal(map[string]any{"message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": call, "name": "Agent"}}}})
		records = append(records, &SessionRecord{SessionID: "dispatch-cache", UUID: uuid, RecordType: "assistant", Agent: "claude", ProfileEmail: "p", UserID: "owner", AccountID: account, SourceFile: file, Ts: at, Raw: raw})
	}
	add("unique", "main", "acct", "one")
	add("foreign-account", "main", "other", "one")
	add("ambiguous-a", "main", "acct", "two")
	add("ambiguous-b", "copy", "acct", "two")
	add("non-array", "main", "acct", "missing")
	records[len(records)-1].Raw = json.RawMessage(`{"message":{"content":{"type":"tool_use","id":"missing","name":"Agent"}}}`)
	add("null-content", "main", "acct", "missing")
	records[len(records)-1].Raw = json.RawMessage(`{"message":{"content":null}}`)
	records[0].RecordType = "tool_call" // This source appears in both identity branches.
	records[0].ToolCallID = "one"
	records[2].RecordType = "system" // Preserve the existing parent source-type range.
	records[0].Raw = json.RawMessage(`{"message":{"content":[{"type":"text","text":"ignored"},{"type":"tool_use","id":"one","name":"Agent"},{"type":"tool_use","id":"unrelated","name":"Read"}]}}`)

	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	tx, cleanup, err := s.turnContextTx(ctx, "dispatch-cache")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	reader := traceNodeReader{ctx: ctx, tx: tx, session: "dispatch-cache", metadata: true, access: SessionAnalysisAccess{UserID: "owner", ProfileEmail: "p"}, relations: map[string]traceToolRelation{}, agentParents: map[string]*traceNodeKey{}}
	var page []traceNodeRow
	for _, call := range []string{"one", "two", "missing"} {
		page = append(page, traceNodeRow{record: &SessionRecord{SessionID: "dispatch-cache", ProfileEmail: "p", UserID: "owner", AccountID: "acct", Agent: "claude", SourceFile: "sidechain", ToolUseID: call}, raw: map[string]any{}})
	}
	if err = reader.primeToolRelations(page); err != nil {
		t.Fatal(err)
	}
	if len(reader.agentParents) != 3 {
		t.Fatalf("dispatch parents were not batched: %d", len(reader.agentParents))
	}
	// The primed path must require no additional lookup; return copies because
	// node projection changes the returned parent kind to the synthetic agent.
	reader.tx = nil
	parent, err := reader.toolParent(page[0], "one", false)
	if err != nil || parent == nil {
		t.Fatalf("unique parent=%+v %v", parent, err)
	}
	if parent.Index != 1 {
		t.Fatalf("multiple blocks lost exact tool ordinal: %+v", parent)
	}
	parent.Kind = "agent"
	again, err := reader.toolParent(page[0], "one", false)
	if err != nil || again == nil || again.Kind != "tool" {
		t.Fatalf("cached parent was mutated: %+v %v", again, err)
	}
	for i, call := range []string{"two", "missing"} {
		parent, err = reader.toolParent(page[i+1], call, false)
		if err != nil || parent != nil {
			t.Fatalf("unconfirmed parent %s=%+v %v", call, parent, err)
		}
	}
}

func TestTraceRootPageFollowsSourceTimeOrderAcrossOrphans(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	var records []*SessionRecord
	for i := 104; i >= 0; i-- {
		records = append(records, &SessionRecord{SessionID: "root-order", UUID: fmt.Sprintf("orphan-%03d", i), Ts: at, RecordType: "agent_task", Agent: "codex", UserID: "owner", ProfileEmail: "p", SourceFile: "orphan", Raw: json.RawMessage(fmt.Sprintf(`{"payload":{"content":"orphan-%03d"}}`, i))})
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 1}
	cursor := ""
	count := 0
	for {
		page, err := s.TraceNodes(ctx, "root-order", a, TraceNodesRequest{Limit: 100, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range page.Nodes {
			if node.Title != fmt.Sprintf("orphan-%03d", count) {
				t.Fatalf("root source order[%d]=%s", count, node.Title)
			}
			if node.ParentID != nil {
				t.Fatal("orphan acquired an inferred parent")
			}
			count++
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if count != 105 {
		t.Fatalf("root rows lost across bounded page: %d", count)
	}
}

func TestTraceTurnChildrenUsesBoundedSourceInterval(t *testing.T) {
	at := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	start := traceNodeRow{id: 1, record: &SessionRecord{Ts: at, UUID: "start", Agent: "claude"}, raw: map[string]any{}}
	next := traceNodeRow{id: 2, record: &SessionRecord{Ts: at.Add(time.Minute), UUID: "next", Agent: "claude"}, raw: map[string]any{}}
	predicate, args := traceTurnChildren(start, &next)
	if strings.Contains(predicate, "NOT EXISTS") || !strings.Contains(predicate, "(r.ts,r.uuid,r.id)<($13,$14,$15)") || len(args) != 11 {
		t.Fatalf("source interval is not bounded once: %s (%d args)", predicate, len(args))
	}
	if args[8] != next.record.Ts || args[9] != next.record.UUID || args[10] != next.id {
		t.Fatalf("next source key lost: %v", args)
	}
	predicate, args = traceTurnChildren(start, nil)
	if strings.Contains(predicate, "NOT EXISTS") || len(args) != 8 {
		t.Fatalf("open interval still repeats anchor scans: %s", predicate)
	}
	start.record.Agent = "codex"
	start.raw = map[string]any{"payload": map[string]any{"turn_id": "native-turn"}}
	predicate, args = traceTurnChildren(start, &next)
	if len(args) != 12 || args[11] != "native-turn" || !strings.Contains(predicate, "=$16 OR (") {
		t.Fatalf("native membership lost precedence: %s %v", predicate, args)
	}
}

func TestTraceDispatchParentSQLUsesOnlyIndexedToolIDs(t *testing.T) {
	predicate := traceToolParentsSQL("$5")
	if strings.Contains(predicate, "EXISTS") || !strings.Contains(predicate, "jsonb_path_query_array") || !strings.Contains(predicate, "?| $5::text[]") || !strings.Contains(predicate, "jsonb_typeof(r.raw->'message'->'content')='array'") {
		t.Fatalf("dispatch lookup still expands full blocks: %s", predicate)
	}
}

func TestTraceDispatchParentKeyBranchesRemainVisibleAndBounded(t *testing.T) {
	selectSQL := `SELECT r.ts,r.id,r.uuid,r.record_type,r.profile_email,md5(r.uuid) AS source_order FROM visible_session_records r WHERE r.session_id=$1 AND r.profile_email=$3 AND ` + traceToolParentsSQL("$5")
	query := traceParentKeyQuery(selectSQL, "r.ts,r.id,source_order", "$11")
	if strings.Count(query, "FROM session_records") != 2 || strings.Count(query, "FROM visible_session_records") != 1 || strings.Count(query, "LIMIT $11") != 1 || !strings.Contains(query, "JOIN trace_parent_candidates") || !strings.Contains(query, "MATERIALIZED") {
		t.Fatalf("parent keys are not isolated before visible validation: %s", query)
	}
	visibility := strings.LastIndex(query, "FROM visible_session_records")
	if strings.Contains(query[:visibility], "LIMIT") || !strings.Contains(query[visibility:], "r.session_id=$1 AND r.profile_email=$3") {
		t.Fatalf("candidate limit precedes authorization or lost scope: %s", query)
	}
}

func TestTraceRootCandidateKeysUseAuthorizedSourceIndex(t *testing.T) {
	t.Parallel()
	query := traceRootCandidatesCTE(21, true)
	if strings.Contains(query, "FROM visible_session_records") || strings.Count(query, "FROM session_records r") != 1 || strings.Contains(query, "trace_scopes") || strings.Contains(query, "LATERAL") {
		t.Fatalf("root key candidates repeat visibility before bounded key projection: %s", query)
	}
	for _, scope := range []string{"r.session_id=$1", "r.user_id=$2", "r.profile_email=$3", "r.login_email=$4", traceSameSourceSQL("r", "first_input"), "(r.ts,r.uuid,r.id," + traceNodeSourceOrder + ")>=($5,$6,$7,$8)", "ORDER BY r.ts,r.uuid,r.id,source_order LIMIT 21"} {
		if !strings.Contains(query, scope) {
			t.Fatalf("root key source boundary lost: %s", scope)
		}
	}
}
