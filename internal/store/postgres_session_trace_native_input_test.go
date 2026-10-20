package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"cctrace/internal/sessiontrace"
)

func TestTraceNativeInputPagesPreserveSourceCandidatesAndFenceAccess(t *testing.T) {
	// acquireTestStore serializes shared PostgreSQL fixture mutation.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	records := []*SessionRecord{
		{Ts: base, UUID: "request", RecordType: "api-request", Raw: json.RawMessage(`{"id":"request","keep":0,"tail":["message"],"params":{"model":"test"}}`)},
		{Ts: time.Unix(0, 0), UUID: "blob-a", RecordType: "api-request-blob", Raw: json.RawMessage(`{"hash":"message","message":{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}`)},
		{Ts: time.Unix(0, 0), UUID: "blob-b", RecordType: "api-request-blob", Raw: json.RawMessage(`{"hash":"message","message":{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}`)},
	}
	for _, record := range records {
		record.SessionID = "native-input"
		record.ProfileEmail = "profile"
		record.UserID = "owner"
		record.Agent = "claude"
		record.SourceFile = "main"
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 1}
	nodes, err := s.TraceNodes(ctx, "native-input", access, TraceNodesRequest{View: "flow", Limit: 20})
	if err != nil || len(nodes.Nodes) != 1 {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	node := nodes.Nodes[0].NodeID
	first, err := s.TraceNodeDetailWithOptions(ctx, "native-input", access, node, TraceNodeDetailOptions{RequestParts: "page", InputLimit: 1})
	if err != nil || first.Request == nil || first.Request.InputPage == nil || len(first.Request.Body) != 0 || first.Request.InputPage.NextCursor == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	next, err := s.TraceNodeDetailWithOptions(ctx, "native-input", access, node, TraceNodeDetailOptions{RequestParts: "page", InputLimit: 1, InputCursor: *first.Request.InputPage.NextCursor})
	if err != nil || len(next.Request.Provenance) < 2 {
		t.Fatalf("duplicate source provenance=%+v err=%v", next.Request, err)
	}
	seen := map[string]bool{}
	for _, source := range next.SourceLocations {
		seen[source.UUID] = true
	}
	if !seen["blob-a"] || !seen["blob-b"] {
		t.Fatalf("lost candidates=%+v", next.SourceLocations)
	}
	source, err := s.TraceNodeDetail(ctx, "native-input", access, next.SourceLocations[0].NodeID)
	if err != nil || source.Context.State != "observed" || len(source.RawRecords) != 1 {
		t.Fatalf("exact source=%+v err=%v", source, err)
	}
	legacy, err := s.TraceNodeDetail(ctx, "native-input", access, node)
	if err != nil || legacy.Request == nil || legacy.Request.Body["messages"] == nil || legacy.Request.InputPage != nil {
		t.Fatalf("legacy=%+v err=%v", legacy.Request, err)
	}
	if _, err = s.TraceNodeDetailWithOptions(ctx, "native-input", SessionAnalysisAccess{User: true, UserID: "foreign"}, node, TraceNodeDetailOptions{RequestParts: "page", SourceRef: next.SourceLocations[0].SourceRef}); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("foreign locator=%v", err)
	}
	if _, err = s.TraceNodeDetailWithOptions(ctx, "native-input", access, node, TraceNodeDetailOptions{RequestParts: "page", SourceRef: "not-a-uuid-or-path"}); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("forged locator=%v", err)
	}
	records[0].Ts = base.Add(time.Second)
	records[0].UUID = "new-request"
	records[0].Raw = json.RawMessage(`{"id":"request2","keep":0,"tail":[]}`)
	if err = s.InsertSessionRecords(ctx, records[:1]); err != nil {
		t.Fatal(err)
	}
	all, err := s.TraceNodes(ctx, "native-input", access, TraceNodesRequest{View: "flow", Limit: 20})
	if err != nil || len(all.Nodes) != 2 {
		t.Fatalf("comparison nodes=%+v err=%v", all, err)
	}
	compared, err := s.TraceNodeDetailWithOptions(ctx, "native-input", access, all.Nodes[1].NodeID, TraceNodeDetailOptions{RequestParts: "page", CompareTo: node})
	if err != nil || compared.Request == nil || compared.Request.Comparison == nil || compared.Request.Comparison.CompareTo != node || compared.Request.Comparison.State != "observed" {
		t.Fatalf("comparison=%+v err=%v", compared.Request, err)
	}
	removed := false
	for _, item := range compared.Request.Comparison.Items {
		if item.Change == "removed" {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("missing removed input=%+v", compared.Request.Comparison)
	}
	if _, err = s.TraceNodeDetailWithOptions(ctx, "native-input", access, node, TraceNodeDetailOptions{RequestParts: "page", InputCursor: *first.Request.InputPage.NextCursor}); !errors.Is(err, ErrTraceSnapshot) {
		t.Fatalf("stale input cursor=%v", err)
	}
}

func TestTraceNativeRequestDetailToleratesMissingRequestID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 10, 15, 3, 8, 39, 0, time.UTC)
	messageHash, err := sessiontrace.MessageHash(map[string]any{"role": "user", "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	records := []*SessionRecord{
		{Ts: base, UUID: "request-without-id", RecordType: "api-request", Raw: json.RawMessage(fmt.Sprintf(`{"keep":0,"tail":[%q],"params":{"model":"test"}}`, messageHash))},
		{Ts: time.Unix(0, 0), UUID: "blob-without-id", RecordType: "api-request-blob", Raw: json.RawMessage(`{"hash":"message","message":{"role":"user","content":"hello"}}`)},
	}
	for _, record := range records {
		record.SessionID = "native-missing-id"
		record.ProfileEmail = "profile"
		record.UserID = "owner"
		record.Agent = "claude"
		record.SourceFile = "main"
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	base = base.Add(time.Minute)
	records = []*SessionRecord{
		{SessionID: "native-missing-id", Ts: base, UUID: "second-request-without-id", RecordType: "api-request", Raw: json.RawMessage(`{"keep":0,"tail":[]}`), ProfileEmail: "profile", UserID: "owner", Agent: "claude", SourceFile: "main"},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: 1}
	nodes, err := s.TraceNodes(ctx, "native-missing-id", access, TraceNodesRequest{View: "flow", Limit: 20})
	if err != nil || len(nodes.Nodes) != 2 {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
	for _, node := range nodes.Nodes {
		detail, err := s.TraceNodeDetail(ctx, "native-missing-id", access, node.NodeID)
		if err != nil || detail.Request == nil {
			t.Fatalf("node=%s detail=%+v err=%v", node.NodeID, detail.Request, err)
		}
	}
}
