package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cctrace/internal/sessiontrace"
)

func TestTraceFlowSeparatesMembershipAndExactParents(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{Ts: time.Unix(0, 0), UUID: "routine-meta", RecordType: "last-prompt", Raw: json.RawMessage(`{"type":"last-prompt","value":"internal state"}`)},
		{Ts: base, UUID: "input", RecordType: "user", Raw: json.RawMessage(`{"message":{"role":"user","content":"Inspect storage"}}`)},
		{Ts: base.Add(time.Second), UUID: "notice", RecordType: "developer", Raw: json.RawMessage(`{"payload":{"role":"developer","content":"The user interrupted this request"}}`)},
		{Ts: base.Add(2 * time.Second), UUID: "call", RecordType: "tool_call", ToolCallID: "call", ToolName: "Read", Raw: json.RawMessage(`{"payload":{"name":"Read","arguments":"{}"}}`)},
		{Ts: base.Add(3 * time.Second), UUID: "routine-child-meta", RecordType: "permission-mode", Raw: json.RawMessage(`{"type":"permission-mode","value":"internal state"}`)},
		{Ts: base.Add(10 * time.Second), UUID: "result", RecordType: "tool_output", ToolCallID: "call", Raw: json.RawMessage(`{"payload":{"output":"done"}}`)},
		{Ts: base.Add(20 * time.Second), UUID: "second", RecordType: "user", Raw: json.RawMessage(`{"message":{"role":"user","content":"Other task"}}`)},
	}
	for _, r := range records {
		r.SessionID = "flow"
		r.Agent = "codex"
		r.UserID = "owner"
		r.ProfileEmail = "p"
		r.AccountID = "a"
		r.SourceFile = "main"
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	a := SessionAnalysisAccess{User: true, UserID: "owner"}
	roots, err := s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "flow", Limit: 1})
	if err != nil || len(roots.Nodes) != 1 || roots.NextCursor == nil {
		t.Fatalf("roots=%+v %v", roots, err)
	}
	if roots.Nodes[0].Kind != "turn" {
		t.Fatalf("routine metadata displaced request: %+v", roots.Nodes[0])
	}
	group := roots.Nodes[0].NodeID
	children, err := s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "flow", ParentID: group})
	if err != nil {
		t.Fatal(err)
	}
	var callID string
	noticeFound := false
	for _, n := range children.Nodes {
		if n.Kind == "metadata" {
			t.Fatalf("routine metadata in execution flow: %+v", n)
		}
		if n.GroupID != group {
			t.Fatalf("missing group %+v", n)
		}
		if n.ParentID != nil {
			t.Fatalf("group promoted to parent %+v", n)
		}
		if n.Role == "developer" {
			noticeFound = true
			if n.Semantic == "instructions" {
				t.Fatalf("notice misclassified %+v", n)
			}
		}
		if n.Kind == "tool" {
			callID = n.NodeID
		}
	}
	detail, err := s.TraceNodeDetail(ctx, "flow", a, callID)
	if err != nil || detail.Navigation == nil || detail.Navigation.GroupID != group || detail.Navigation.RootNodeID != group || detail.Navigation.RootCursor == "" {
		t.Fatalf("navigation=%+v %v", detail.Navigation, err)
	}
	located, err := s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "flow", Cursor: detail.Navigation.RootCursor, Limit: 1})
	if err != nil || len(located.Nodes) != 1 || located.Nodes[0].NodeID != group {
		t.Fatalf("located=%+v %v", located, err)
	}
	if !noticeFound || callID == "" {
		t.Fatalf("children=%+v", children)
	}
	from, to := base.Add(3*time.Second), base.Add(4*time.Second)
	timing, err := s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "timing", GroupID: group, From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range timing.Nodes {
		if n.NodeID == callID {
			found = true
			if n.EndedAt == nil || !n.EndedAt.Equal(base.Add(10*time.Second)) || n.TimeBasis != "call_result_observation" {
				t.Fatalf("interval=%+v", n)
			}
		}
	}
	if timing.Coverage.From == nil || timing.Coverage.To == nil || !timing.Coverage.From.Equal(from) || !timing.Coverage.To.Equal(to) {
		t.Fatalf("explicit range=%+v", timing.Coverage)
	}
	allTiming, err := s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "timing", GroupID: group})
	if err != nil || allTiming.Coverage.From == nil || allTiming.Coverage.To == nil || !allTiming.Coverage.From.Equal(base) || !allTiming.Coverage.To.Equal(base.Add(10*time.Second)) {
		t.Fatalf("group range=%+v %v", allTiming.Coverage, err)
	}
	if !found {
		t.Fatalf("overlapping invocation missing %+v", timing)
	}
	page2, err := s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "flow", Cursor: *roots.NextCursor, Limit: 1})
	if err != nil || len(page2.Nodes) != 1 || page2.PrevCursor == nil {
		t.Fatalf("page2=%+v %v", page2, err)
	}
	back, err := s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "flow", Cursor: *page2.PrevCursor, Limit: 1})
	if err != nil || len(back.Nodes) != 1 || back.Nodes[0].NodeID != group {
		t.Fatalf("back=%+v %v", back, err)
	}
	search, err := s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "flow", Query: "Read"})
	if err != nil || len(search.Nodes) != 1 || search.Nodes[0].NodeID != group {
		t.Fatalf("search=%+v %v", search, err)
	}
	if _, err = s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "flow", Cursor: *roots.NextCursor, Query: "different"}); !errors.Is(err, ErrTraceInvalid) {
		t.Fatalf("cursor query binding=%v", err)
	}
	if _, err = s.TraceNodes(ctx, "flow", SessionAnalysisAccess{User: true, UserID: "foreign"}, TraceNodesRequest{View: "timing", GroupID: group}); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("foreign group=%v", err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO session_timeline_revisions(session_id,revision) VALUES('flow',1) ON CONFLICT(session_id) DO UPDATE SET revision=session_timeline_revisions.revision+1`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "flow", Cursor: *roots.NextCursor}); !errors.Is(err, ErrTraceSnapshot) {
		t.Fatalf("stale cursor=%v", err)
	}
	if _, err = s.TraceNodes(ctx, "flow", a, TraceNodesRequest{View: "timing"}); !errors.Is(err, ErrTraceInvalid) {
		t.Fatalf("unscoped timing=%v", err)
	}
}

func TestTraceFlowNumbersRequireCurrentExactCanonicalFacts(t *testing.T) {
	// Shared PostgreSQL fixtures mutate the same source tables.
	s := acquireTestStore(t)
	truncateTables(t, s)
	const id = "flow-verified-numbers"
	timelineCleanup(t, s, id)
	seedTimelineClaude(t, s, id)
	ctx := context.Background()
	a := SessionAnalysisAccess{}
	before, err := s.TraceNodes(ctx, id, a, TraceNodesRequest{View: "flow"})
	if err != nil || len(before.Nodes) != 1 || before.Nodes[0].RequestNo != nil || before.RequestTotal == nil || before.RequestTotal.Value != nil {
		t.Fatalf("unbuilt=%+v %v", before, err)
	}
	generation := dirtyGeneration(t, s, id)
	run, err := s.processSessionFact(ctx, id, generation)
	if err != nil || run.Outcome != "written" {
		t.Fatalf("publish=%+v %v", run, err)
	}
	page, err := s.TraceNodes(ctx, id, a, TraceNodesRequest{View: "flow"})
	if err != nil || len(page.Nodes) != 1 || page.Nodes[0].RequestNo == nil || page.Nodes[0].RequestNo.Value == nil || *page.Nodes[0].RequestNo.Value != 1 || page.RequestTotal == nil || page.RequestTotal.Value == nil || *page.RequestTotal.Value != 1 {
		t.Fatalf("verified=%+v %v", page, err)
	}
	jump, err := s.TraceNodes(ctx, id, a, TraceNodesRequest{View: "flow", RequestNo: 1})
	if err != nil || len(jump.Nodes) != 1 || jump.Nodes[0].NodeID != page.Nodes[0].NodeID {
		t.Fatalf("number jump=%+v %v", jump, err)
	}
	if jump.Nodes[0].RequestNo == nil || jump.Nodes[0].RequestNo.Value == nil || *jump.Nodes[0].RequestNo.Value != 1 {
		t.Fatalf("number jump lost its verified request identity: %+v", jump.Nodes[0])
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_timeline_provenance SET source_revision=source_revision+1 WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	stale, err := s.TraceNodes(ctx, id, a, TraceNodesRequest{View: "flow"})
	if err != nil || len(stale.Nodes) != 1 || stale.Nodes[0].RequestNo != nil || stale.RequestTotal.Value != nil {
		t.Fatalf("stale=%+v %v", stale, err)
	}
	if _, err = s.TraceNodes(ctx, id, a, TraceNodesRequest{View: "flow", RequestNo: 1}); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("stale number=%v", err)
	}
}

func TestTraceFlowRequestNumberUsesOneVerifiedSet(t *testing.T) {
	for _, query := range []string{traceFlowNumberSQL(), traceFlowNumberPageSQL(), traceFlowNumberRequestSQL()} {
		if strings.Count(query, "FROM visible_session_records r") != 1 {
			t.Fatalf("verified set must read its source once: %s", query)
		}
	}
	if strings.Count(traceFlowNumberPageSQL(), "trace_flow_numbers") < 2 || strings.Count(traceFlowNumberRequestSQL(), "trace_flow_numbers") < 2 {
		t.Fatal("page and request lookups must reuse the materialized verified set")
	}
}

func TestTraceFlowClassificationKeepsOriginalRoleAndUnknownMeaning(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ text, semantic string }{
		{"The user interrupted the request", "runtime_notice"},
		{"Daemon notification: child completed", "runtime_notice"},
		{"developer body with no semantic marker", "message"},
	} {
		row := traceNodeRow{record: &SessionRecord{RecordType: "developer", Agent: "codex"}, raw: map[string]any{"payload": map[string]any{"role": "developer", "content": tc.text}}}
		n := sessiontrace.Node{Kind: "instructions", Role: "developer"}
		traceNodeMeaning(&n, row)
		if n.Role != "developer" || n.Semantic != tc.semantic {
			t.Fatalf("classification=%+v", n)
		}
	}
}
