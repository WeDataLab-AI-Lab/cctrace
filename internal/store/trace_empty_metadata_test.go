package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"cctrace/internal/sessiontrace"
)

func TestTraceNodeViewClassifiesOnlyEmptyLegacyClaudeRowsAsMetadata(t *testing.T) {
	t.Parallel()

	epoch := time.Unix(0, 0).UTC()
	current := epoch.Add(time.Hour)
	cases := []struct {
		name, agent, recordType, raw, want string
		ts                                 time.Time
	}{
		{name: "empty informational system", agent: "claude", recordType: "system", raw: `{"type":"system","subtype":"informational","message":null}`, ts: current, want: "metadata"},
		{name: "empty generic attachment", agent: "claude", recordType: "attachment", raw: `{"type":"attachment","message":null}`, ts: current, want: "metadata"},
		{name: "legacy empty attachment defaults to claude", recordType: "attachment", raw: `{"type":"attachment","message":null}`, ts: current, want: "metadata"},
		{name: "codex empty attachment", agent: "codex", recordType: "attachment", raw: `{"type":"attachment","message":null}`, ts: current, want: "execution"},
		{name: "system body", agent: "claude", recordType: "system", raw: `{"type":"system","subtype":"observed_notice","message":{"role":"system","content":"NOTICE BODY"}}`, ts: current, want: "execution"},
		{name: "system top-level content", agent: "claude", recordType: "system", raw: `{"type":"system","subtype":"informational","message":null,"content":"SYSTEM BODY"}`, ts: current, want: "execution"},
		{name: "system payload", agent: "claude", recordType: "system", raw: `{"type":"system","subtype":"informational","message":null,"payload":{"content":"SYSTEM BODY"}}`, ts: current, want: "execution"},
		{name: "system content null key", agent: "claude", recordType: "system", raw: `{"type":"system","subtype":"informational","message":null,"content":null}`, ts: current, want: "execution"},
		{name: "system payload null key", agent: "claude", recordType: "system", raw: `{"type":"system","subtype":"informational","message":null,"payload":null}`, ts: current, want: "execution"},
		{name: "system attachment null key", agent: "claude", recordType: "system", raw: `{"type":"system","subtype":"informational","message":null,"attachment":null}`, ts: current, want: "execution"},
		{name: "system primitive message", agent: "claude", recordType: "system", raw: `{"type":"system","subtype":"informational","message":"SYSTEM BODY"}`, ts: current, want: "execution"},
		{name: "typed attachment", agent: "claude", recordType: "attachment", raw: `{"type":"attachment","message":null,"attachment":{"type":"file","path":"report.txt"}}`, ts: current, want: "execution"},
		{name: "attachment body", agent: "claude", recordType: "attachment", raw: `{"type":"attachment","message":null,"content":"ATTACHMENT BODY"}`, ts: current, want: "execution"},
		{name: "attachment content null key", agent: "claude", recordType: "attachment", raw: `{"type":"attachment","message":null,"content":null}`, ts: current, want: "execution"},
		{name: "attachment payload null key", agent: "claude", recordType: "attachment", raw: `{"type":"attachment","message":null,"payload":null}`, ts: current, want: "execution"},
		{name: "attachment null typed key", agent: "claude", recordType: "attachment", raw: `{"type":"attachment","message":null,"attachment":null}`, ts: current, want: "execution"},
		{name: "instruction attachment at epoch", agent: "claude", recordType: "attachment", raw: `{"attachment":{"type":"instructions","files":[{"content":"RULE BODY"}]}}`, ts: epoch, want: "context"},
		{name: "developer instruction at epoch", agent: "claude", recordType: "developer", raw: `{"message":{"role":"developer","content":"DEVELOPER BODY"}}`, ts: epoch, want: "context"},
		{name: "hook attachment at epoch", agent: "claude", recordType: "attachment", raw: `{"attachment":{"type":"hook_success","stdout":"HOOK BODY"}}`, ts: epoch, want: "execution"},
		{name: "assistant runtime at epoch", agent: "claude", recordType: "assistant", raw: `{"message":{"role":"assistant","content":"ANSWER"}}`, ts: epoch, want: "execution"},
		{name: "ordinary metadata remains metadata", agent: "claude", recordType: "last-prompt", raw: `{}`, ts: current, want: "metadata"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal([]byte(tc.raw), &raw); err != nil {
				t.Fatal(err)
			}
			row := traceNodeRow{record: &SessionRecord{Agent: tc.agent, RecordType: tc.recordType, Ts: tc.ts}, raw: raw}
			if got := traceNodeView(row); got != tc.want {
				t.Fatalf("traceNodeView()=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestTraceEmptyClaudeMetadataRemainsManuallyAccessible(t *testing.T) {
	// This store test database is shared; truncateTables resets all sessions.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	epoch := time.Unix(0, 0).UTC()
	current := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	const sessionID = "trace-empty-metadata"
	const codexSessionID = "trace-empty-metadata-codex"
	var records []*SessionRecord
	add := func(sessionID, id, agent, recordType string, ts time.Time, raw string) {
		records = append(records, &SessionRecord{
			SessionID: sessionID, UUID: id, Agent: agent, UserID: "owner", ProfileEmail: "p",
			SourceFile: "main", RecordType: recordType, Ts: ts, Raw: json.RawMessage(raw),
		})
	}
	add(sessionID, "empty-system", "claude", "system", current, `{"type":"system","subtype":"informational","message":null}`)
	add(sessionID, "empty-attachment", "claude", "attachment", current, `{"type":"attachment","message":null}`)
	add(sessionID, "legacy-metadata", "claude", "last-prompt", epoch, `{"type":"last-prompt","value":"internal state"}`)
	add(sessionID, "system-null-content", "claude", "system", current, `{"type":"system","subtype":"informational","message":null,"content":null}`)
	add(sessionID, "system-null-payload", "claude", "system", current, `{"type":"system","subtype":"informational","message":null,"payload":null}`)
	add(sessionID, "system-null-attachment", "claude", "system", current, `{"type":"system","subtype":"informational","message":null,"attachment":null}`)
	add(sessionID, "system-primitive-message", "claude", "system", current, `{"type":"system","subtype":"informational","message":"SYSTEM BODY"}`)
	add(sessionID, "attachment-null-content", "claude", "attachment", current, `{"type":"attachment","message":null,"content":null}`)
	add(sessionID, "attachment-null-payload", "claude", "attachment", current, `{"type":"attachment","message":null,"payload":null}`)
	add(sessionID, "attachment-null-value", "claude", "attachment", current, `{"type":"attachment","message":null,"attachment":null}`)
	for i := 0; i < 4; i++ {
		add(sessionID, fmt.Sprintf("notice-%d", i), "claude", "system", current,
			`{"type":"system","subtype":"observed_notice","message":{"role":"system","content":"NOTICE BODY"}}`)
	}
	add(sessionID, "typed-file", "claude", "attachment", current, `{"type":"attachment","message":null,"attachment":{"type":"file","path":"report.txt"}}`)
	add(sessionID, "attachment-body", "claude", "attachment", current, `{"type":"attachment","message":null,"content":"ATTACHMENT BODY"}`)
	add(sessionID, "instructions", "claude", "attachment", epoch, `{"attachment":{"type":"instructions","files":[{"path":"/qa/CLAUDE.md","content":"RULE BODY"}]}}`)
	add(sessionID, "developer", "claude", "developer", epoch, `{"message":{"role":"developer","content":"DEVELOPER BODY"}}`)
	add(sessionID, "hook", "claude", "attachment", epoch, `{"attachment":{"type":"hook_success","stdout":"HOOK BODY"}}`)
	add(sessionID, "assistant", "claude", "assistant", epoch, `{"message":{"role":"assistant","content":"ANSWER"}}`)
	add(codexSessionID, "codex-empty-attachment", "codex", "attachment", current, `{"type":"attachment","message":null}`)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	access := SessionAnalysisAccess{User: true, UserID: "owner"}
	metadata, err := s.TraceNodes(ctx, sessionID, access, TraceNodesRequest{View: "metadata", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	metadataUUIDs := map[string]bool{}
	for _, node := range metadata.Nodes {
		if node.Kind != "metadata" {
			t.Fatalf("metadata view returned kind %q: %+v", node.Kind, node)
		}
		detail, err := s.TraceNodeDetail(ctx, sessionID, access, node.NodeID)
		if err != nil {
			t.Fatalf("detail for %s: %v", node.NodeID, err)
		}
		if len(detail.RawRecords) != 1 || len(detail.SourceLocations) != 1 {
			t.Fatalf("metadata source is not directly accessible: records=%d sources=%d", len(detail.RawRecords), len(detail.SourceLocations))
		}
		metadataUUIDs[detail.RawRecords[0].UUID] = true
		if detail.SourceLocations[0].UUID != detail.RawRecords[0].UUID {
			t.Fatalf("source location lost record identity: %+v", detail.SourceLocations[0])
		}
	}
	for _, id := range []string{"empty-system", "empty-attachment", "legacy-metadata"} {
		if !metadataUUIDs[id] {
			t.Errorf("metadata view omitted %q: %+v", id, metadataUUIDs)
		}
	}
	if len(metadataUUIDs) != 3 {
		t.Fatalf("unexpected rows classified as metadata: %+v", metadataUUIDs)
	}

	execution, err := s.TraceNodes(ctx, sessionID, access, TraceNodesRequest{View: "execution", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	executionUUIDs := traceDetailUUIDs(t, s, ctx, sessionID, access, execution.Nodes)
	for _, id := range []string{"notice-0", "notice-1", "notice-2", "notice-3", "typed-file", "attachment-body", "system-null-content", "system-null-payload", "system-null-attachment", "system-primitive-message", "attachment-null-content", "attachment-null-payload", "attachment-null-value", "hook", "assistant"} {
		if !executionUUIDs[id] {
			t.Errorf("execution view omitted real source %q: %+v", id, executionUUIDs)
		}
	}

	contexts, err := s.TraceNodes(ctx, sessionID, access, TraceNodesRequest{View: "context", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	contextUUIDs := traceDetailUUIDs(t, s, ctx, sessionID, access, contexts.Nodes)
	for _, id := range []string{"instructions", "developer"} {
		if !contextUUIDs[id] {
			t.Errorf("context view omitted real instructions %q: %+v", id, contextUUIDs)
		}
	}

	codexMetadata, err := s.TraceNodes(ctx, codexSessionID, access, TraceNodesRequest{View: "metadata", Limit: 100})
	if err != nil || len(codexMetadata.Nodes) != 0 {
		t.Fatalf("Codex empty attachment was classified as Claude metadata: %+v err=%v", codexMetadata, err)
	}
}

func traceDetailUUIDs(t *testing.T, s *PgStore, ctx context.Context, sessionID string, access SessionAnalysisAccess, nodes []sessiontrace.Node) map[string]bool {
	t.Helper()
	uuids := map[string]bool{}
	for _, node := range nodes {
		detail, err := s.TraceNodeDetail(ctx, sessionID, access, node.NodeID)
		if err != nil {
			t.Fatalf("detail for %s: %v", node.NodeID, err)
		}
		for _, record := range detail.RawRecords {
			uuids[record.UUID] = true
		}
	}
	return uuids
}
