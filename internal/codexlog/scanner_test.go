package codexlog

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeCodexFile(t *testing.T, lines []string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "rollout-*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	f.Close()
	return f.Name()
}

func TestScanFile_Empty(t *testing.T) {
	path := writeCodexFile(t, nil)
	recs, offset, err := ScanFile(path, 0, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 records, got %d", len(recs))
	}
	if offset != 0 {
		t.Errorf("expected offset 0, got %d", offset)
	}
}

func TestScanFile_NewFormat(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"id":"abc","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/myproject","model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}}`,
		`{"type":"event_msg","timestamp":"2026-04-23T11:30:14.000Z","payload":{"cwd":"/myproject","command":"ls"}}`,
	}
	path := writeCodexFile(t, lines)

	recs, offset, err := ScanFile(path, 0, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	// session_meta, turn_context, event_msg are skipped; 2 response_items returned
	if len(recs) != 2 {
		t.Errorf("expected 2 records, got %d", len(recs))
	}
	if recs[0].RecordType != "user" {
		t.Errorf("recs[0].RecordType = %q, want user", recs[0].RecordType)
	}
	if recs[0].CWD != "/myproject" {
		t.Errorf("recs[0].CWD = %q, want /myproject", recs[0].CWD)
	}
	if recs[0].Model != "gpt-5" {
		t.Errorf("recs[0].Model = %q, want gpt-5", recs[0].Model)
	}
	if recs[1].RecordType != "assistant" {
		t.Errorf("recs[1].RecordType = %q, want assistant", recs[1].RecordType)
	}
	fi, _ := os.Stat(path)
	if offset != fi.Size() {
		t.Errorf("offset = %d, want %d (file size)", offset, fi.Size())
	}
}

func TestScanMetadata_NewFormat(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"id":"abc","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/myproject","model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"type":"event_msg","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}
	path := writeCodexFile(t, lines)

	meta, err := ScanMetadata(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if meta.CWD != "/myproject" {
		t.Errorf("CWD = %q, want /myproject", meta.CWD)
	}
	if meta.Model != "gpt-5" {
		t.Errorf("Model = %q, want gpt-5", meta.Model)
	}
	if !meta.TokenUsageScanned || !meta.HasTotalTokenUsage {
		t.Fatalf("expected token usage metadata to be captured")
	}
	if meta.TotalInputTokens != 100 || meta.TotalCachedInputTokens != 40 || meta.TotalOutputTokens != 20 {
		t.Fatalf("token totals = %d/%d/%d, want 100/40/20", meta.TotalInputTokens, meta.TotalCachedInputTokens, meta.TotalOutputTokens)
	}
}

func TestScanFile_DedupesRepeatedTokenCountTotal(t *testing.T) {
	lines := []string{
		`{"type":"turn_context","timestamp":"2026-05-15T09:24:00.000Z","payload":{"cwd":"/myproject","model":"gpt-5.5"}}`,
		`{"type":"event_msg","timestamp":"2026-05-15T09:26:02.304Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
		`{"type":"event_msg","timestamp":"2026-05-15T09:26:46.078Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
		`{"type":"event_msg","timestamp":"2026-05-15T09:29:59.139Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":175,"cached_input_tokens":70,"output_tokens":25},"last_token_usage":{"input_tokens":75,"cached_input_tokens":30,"output_tokens":5}}}}`,
	}
	path := writeCodexFile(t, lines)

	recs, _, err := ScanFile(path, 0, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected 2 usage records, got %d", len(recs))
	}
	if recs[0].InputTokens == nil || *recs[0].InputTokens != 100 {
		t.Fatalf("first input tokens = %v, want 100", recs[0].InputTokens)
	}
	if recs[1].InputTokens == nil || *recs[1].InputTokens != 75 {
		t.Fatalf("second input tokens = %v, want 75", recs[1].InputTokens)
	}
	if recs[1].CacheReadTokens == nil || *recs[1].CacheReadTokens != 30 {
		t.Fatalf("second cache tokens = %v, want 30", recs[1].CacheReadTokens)
	}
	if recs[1].OutputTokens == nil || *recs[1].OutputTokens != 5 {
		t.Fatalf("second output tokens = %v, want 5", recs[1].OutputTokens)
	}
}

func TestScanFile_ForkedSessionSkipsInheritedHistory(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-07-27T06:29:17.513Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-07-27T06:29:17.513Z","payload":{"id":"parent-session","thread_source":"user","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:17.513Z","payload":{"type":"task_started","turn_id":"parent-turn"}}`,
		`{"type":"turn_context","timestamp":"2026-07-27T06:29:17.513Z","payload":{"cwd":"/myproject","model":"gpt-5.6-sol"}}`,
		`{"type":"response_item","timestamp":"2026-07-27T06:29:17.513Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"inherited parent prompt"}]}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:17.514Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:17.515Z","payload":{"type":"task_started","turn_id":"another-parent-turn"}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:17.516Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150,"cached_input_tokens":60,"output_tokens":23},"last_token_usage":{"input_tokens":50,"cached_input_tokens":20,"output_tokens":3}}}}`,
		`{"type":"inter_agent_communication_metadata","timestamp":"2026-07-27T06:29:19.131Z","payload":{"trigger_turn":true}}`,
		`{"type":"response_item","timestamp":"2026-07-27T06:29:19.131Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"new child task"}]}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:26.820Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":175,"cached_input_tokens":70,"output_tokens":25},"last_token_usage":{"input_tokens":75,"cached_input_tokens":30,"output_tokens":5}}}}`,
	}
	path := writeCodexFile(t, lines)

	recs, _, err := ScanFile(path, 0, "child-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want child message and usage only", len(recs))
	}
	if recs[0].RecordType != "user" || recs[0].Content != "new child task" {
		t.Fatalf("first record = %+v, want new child task", recs[0])
	}
	if recs[1].RecordType != "usage" {
		t.Fatalf("second record type = %q, want usage", recs[1].RecordType)
	}
	if recs[1].InputTokens == nil || *recs[1].InputTokens != 25 {
		t.Fatalf("child input tokens = %v, want 25", recs[1].InputTokens)
	}
	if recs[1].CacheReadTokens == nil || *recs[1].CacheReadTokens != 10 {
		t.Fatalf("child cache tokens = %v, want 10", recs[1].CacheReadTokens)
	}
	if recs[1].OutputTokens == nil || *recs[1].OutputTokens != 2 {
		t.Fatalf("child output tokens = %v, want 2", recs[1].OutputTokens)
	}
}

func TestScanFile_LegacyForkWithoutCopiedHistoryKeepsChildRecords(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-06-01T05:29:14.000Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-06-01T05:29:14.001Z","payload":{"type":"task_started","turn_id":"child-turn"}}`,
		`{"type":"turn_context","timestamp":"2026-06-01T05:29:14.002Z","payload":{"cwd":"/myproject","model":"gpt-5.5"}}`,
		`{"type":"response_item","timestamp":"2026-06-01T05:29:14.003Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"legacy child task"}]}}`,
		`{"type":"event_msg","timestamp":"2026-06-01T05:29:14.004Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}
	path := writeCodexFile(t, lines)

	recs, _, meta, err := ScanFileWithMetadata(path, 0, "child-session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want child message and usage", len(recs))
	}
	if recs[0].RecordType != "user" || recs[0].Content != "legacy child task" {
		t.Fatalf("first record = %+v, want legacy child task", recs[0])
	}
	if recs[1].RecordType != "usage" {
		t.Fatalf("second record type = %q, want usage", recs[1].RecordType)
	}
	if recs[1].InputTokens == nil || *recs[1].InputTokens != 100 {
		t.Fatalf("child input tokens = %v, want 100", recs[1].InputTokens)
	}
	if recs[1].CacheReadTokens == nil || *recs[1].CacheReadTokens != 40 {
		t.Fatalf("child cache tokens = %v, want 40", recs[1].CacheReadTokens)
	}
	if recs[1].OutputTokens == nil || *recs[1].OutputTokens != 20 {
		t.Fatalf("child output tokens = %v, want 20", recs[1].OutputTokens)
	}
	if !meta.IsSubagentFork || meta.ForkHistoryCopied || !meta.ForkBoundaryReached {
		t.Fatalf("fork metadata = %+v, want resolved legacy fork", meta)
	}
}

func TestScanFile_LegacyForkWithoutCopiedHistoryAcrossIncrementalScans(t *testing.T) {
	path := writeCodexFile(t, []string{
		`{"type":"session_meta","timestamp":"2026-06-01T05:29:14.000Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
	})

	recs, offset, meta, err := ScanFileWithMetadata(path, 0, "child-session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("initial records = %d, want 0", len(recs))
	}
	if !meta.IsSubagentFork || meta.ForkHistoryCopied || meta.ForkBoundaryReached {
		t.Fatalf("initial fork metadata = %+v, want unresolved fork", meta)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-06-01T05:29:14.001Z","payload":{"type":"task_started","turn_id":"child-turn"}}`)
	fmt.Fprintln(f, `{"type":"response_item","timestamp":"2026-06-01T05:29:14.002Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"incremental legacy child task"}]}}`)
	fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-06-01T05:29:14.003Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`)
	f.Close()

	recs, _, meta, err = ScanFileWithMetadata(path, offset, "child-session", meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("incremental records = %d, want child message and usage", len(recs))
	}
	if recs[0].Content != "incremental legacy child task" {
		t.Fatalf("first record = %+v, want incremental child task", recs[0])
	}
	if recs[1].InputTokens == nil || *recs[1].InputTokens != 100 {
		t.Fatalf("child input tokens = %v, want 100", recs[1].InputTokens)
	}
	if meta.ForkHistoryCopied || !meta.ForkBoundaryReached {
		t.Fatalf("fork metadata = %+v, want resolved legacy fork", meta)
	}
}

func TestScanFile_ForkedSessionPreservesBoundaryAcrossIncrementalScans(t *testing.T) {
	// Copied-history fork split across two scans. The trigger_turn marker lands in
	// the first chunk, so ForkHasTriggerTurn is resolved and persisted; the gate
	// and boundary must survive the incremental resume so inherited history stays
	// suppressed and only the child usage is emitted.
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-07-27T06:29:17.513Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-07-27T06:29:17.513Z","payload":{"id":"parent-session","thread_source":"user","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:17.513Z","payload":{"type":"task_started","turn_id":"parent-turn"}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:17.514Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
		`{"type":"inter_agent_communication_metadata","timestamp":"2026-07-27T06:29:19.131Z","payload":{"trigger_turn":true}}`,
	}
	path := writeCodexFile(t, lines)

	recs, offset, meta, err := ScanFileWithMetadata(path, 0, "child-session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("inherited records = %d, want 0", len(recs))
	}
	if !meta.IsSubagentFork || !meta.ForkHistoryCopied || !meta.ForkHasTriggerTurn || !meta.ForkBoundaryReached {
		t.Fatalf("fork metadata = %+v, want resolved copied-history fork at boundary", meta)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-07-27T06:29:26.820Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":175,"cached_input_tokens":70,"output_tokens":25},"last_token_usage":{"input_tokens":75,"cached_input_tokens":30,"output_tokens":5}}}}`)
	f.Close()

	recs, _, _, err = ScanFileWithMetadata(path, offset, "child-session", meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].RecordType != "usage" {
		t.Fatalf("incremental records = %+v, want one child usage", recs)
	}
	if recs[0].InputTokens == nil || *recs[0].InputTokens != 75 {
		t.Fatalf("child input tokens = %v, want 75", recs[0].InputTokens)
	}
}

func TestScanFile_NestedForkSkipsInheritedHistoryUntilGrandchildBoundary(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-07-27T07:50:25.000Z","payload":{"id":"grandchild-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-07-27T07:29:17.000Z","payload":{"id":"parent-session","forked_from_id":"grandparent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-07-27T07:00:00.000Z","payload":{"id":"grandparent-session","thread_source":"user","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-07-27T07:29:17.001Z","payload":{"cwd":"/myproject","model":"gpt-5.6-sol"}}`,
		`{"type":"response_item","timestamp":"2026-07-27T07:29:17.002Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"inherited parent task"}]}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T07:29:17.003Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":150,"cached_input_tokens":60,"output_tokens":23},"last_token_usage":{"input_tokens":50,"cached_input_tokens":20,"output_tokens":3}}}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T07:50:25.001Z","payload":{"type":"task_started","turn_id":"grandchild-turn"}}`,
		`{"type":"turn_context","timestamp":"2026-07-27T07:50:25.002Z","payload":{"cwd":"/myproject","model":"gpt-5.6-sol"}}`,
		`{"type":"inter_agent_communication_metadata","timestamp":"2026-07-27T07:50:25.003Z","payload":{"trigger_turn":true}}`,
		`{"type":"response_item","timestamp":"2026-07-27T07:50:25.004Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"grandchild task"}]}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T07:50:25.005Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":175,"cached_input_tokens":70,"output_tokens":25},"last_token_usage":{"input_tokens":25,"cached_input_tokens":10,"output_tokens":2}}}}`,
	}
	path := writeCodexFile(t, lines)

	recs, _, err := ScanFile(path, 0, "grandchild-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want grandchild message and usage", len(recs))
	}
	if recs[0].Content != "grandchild task" {
		t.Fatalf("first record = %+v, want grandchild task", recs[0])
	}
	if recs[1].InputTokens == nil || *recs[1].InputTokens != 25 {
		t.Fatalf("grandchild input tokens = %v, want 25", recs[1].InputTokens)
	}
	if recs[1].CacheReadTokens == nil || *recs[1].CacheReadTokens != 10 {
		t.Fatalf("grandchild cache tokens = %v, want 10", recs[1].CacheReadTokens)
	}
	if recs[1].OutputTokens == nil || *recs[1].OutputTokens != 2 {
		t.Fatalf("grandchild output tokens = %v, want 2", recs[1].OutputTokens)
	}
}

func TestScanFile_NoCopyForkWithEmbeddedParentMetaKeepsChildRecords(t *testing.T) {
	// A no-copy subagent fork embeds the parent session_meta (second session_meta)
	// but copies no parent transcript, so it never emits an inter_agent trigger_turn.
	// The gate must not swallow the child's own records when there is no trigger_turn
	// anywhere in the file.
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-07-19T01:17:06.000Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-07-19T01:17:06.000Z","payload":{"id":"parent-session","thread_source":"user","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-07-19T01:17:06.001Z","payload":{"type":"task_started","turn_id":"child-turn"}}`,
		`{"type":"turn_context","timestamp":"2026-07-19T01:17:06.002Z","payload":{"cwd":"/myproject","model":"gpt-5.6-sol"}}`,
		`{"type":"response_item","timestamp":"2026-07-19T01:17:06.003Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"no-copy child task"}]}}`,
		`{"type":"event_msg","timestamp":"2026-07-19T01:17:06.004Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}
	path := writeCodexFile(t, lines)

	recs, _, meta, err := ScanFileWithMetadata(path, 0, "child-session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want child message and usage", len(recs))
	}
	if recs[0].RecordType != "user" || recs[0].Content != "no-copy child task" {
		t.Fatalf("first record = %+v, want no-copy child task", recs[0])
	}
	if recs[1].RecordType != "usage" || recs[1].InputTokens == nil || *recs[1].InputTokens != 100 {
		t.Fatalf("second record = %+v, want child usage 100", recs[1])
	}
	if meta.ForkHasTriggerTurn {
		t.Fatalf("meta.ForkHasTriggerTurn = true, want false for no-copy fork")
	}
}

func TestScanFile_NoCopyForkStartingWithAgentMessageKeepsChildRecords(t *testing.T) {
	// Variant: the child's first event is an agent_message rather than task_started.
	// Still no trigger_turn anywhere, so nothing may be gated.
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-07-20T02:00:00.000Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-07-20T02:00:00.000Z","payload":{"id":"parent-session","thread_source":"user","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-07-20T02:00:00.001Z","payload":{"type":"agent_message","message":"starting"}}`,
		`{"type":"turn_context","timestamp":"2026-07-20T02:00:00.002Z","payload":{"cwd":"/myproject","model":"gpt-5.6-sol"}}`,
		`{"type":"response_item","timestamp":"2026-07-20T02:00:00.003Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"child answer"}]}}`,
		`{"type":"event_msg","timestamp":"2026-07-20T02:00:00.004Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":80,"cached_input_tokens":30,"output_tokens":10},"last_token_usage":{"input_tokens":80,"cached_input_tokens":30,"output_tokens":10}}}}`,
	}
	path := writeCodexFile(t, lines)

	recs, _, _, err := ScanFileWithMetadata(path, 0, "child-session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want child message and usage", len(recs))
	}
	if recs[0].RecordType != "assistant" || recs[0].Content != "child answer" {
		t.Fatalf("first record = %+v, want child answer", recs[0])
	}
	if recs[1].RecordType != "usage" || recs[1].InputTokens == nil || *recs[1].InputTokens != 80 {
		t.Fatalf("second record = %+v, want child usage 80", recs[1])
	}
}

func TestScanFile_NoCopyForkAcrossIncrementalScans(t *testing.T) {
	// A no-copy fork scanned in two passes must never lose the child records,
	// regardless of where the offset split falls.
	path := writeCodexFile(t, []string{
		`{"type":"session_meta","timestamp":"2026-07-19T01:17:06.000Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-07-19T01:17:06.000Z","payload":{"id":"parent-session","thread_source":"user","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-07-19T01:17:06.001Z","payload":{"type":"task_started","turn_id":"child-turn"}}`,
	})

	recs, offset, meta, err := ScanFileWithMetadata(path, 0, "child-session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("initial records = %d, want 0", len(recs))
	}
	if meta.ForkHasTriggerTurn {
		t.Fatalf("initial meta.ForkHasTriggerTurn = true, want false")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"type":"response_item","timestamp":"2026-07-19T01:17:06.003Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"incremental no-copy child task"}]}}`)
	fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-07-19T01:17:06.004Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`)
	f.Close()

	recs, _, _, err = ScanFileWithMetadata(path, offset, "child-session", meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("incremental records = %d, want child message and usage", len(recs))
	}
	if recs[0].Content != "incremental no-copy child task" {
		t.Fatalf("first record = %+v, want incremental no-copy child task", recs[0])
	}
	if recs[1].InputTokens == nil || *recs[1].InputTokens != 100 {
		t.Fatalf("child input tokens = %v, want 100", recs[1].InputTokens)
	}
}

func TestScanFile_SubagentWithoutForkKeepsUsage(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-07-27T06:29:17.513Z","payload":{"id":"review-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-07-27T06:29:17.513Z","payload":{"cwd":"/myproject","model":"gpt-5.6-sol"}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:17.514Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}
	path := writeCodexFile(t, lines)

	recs, _, err := ScanFile(path, 0, "review-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].RecordType != "usage" {
		t.Fatalf("records = %+v, want one usage record", recs)
	}
}

func TestScanFile_OldFormat(t *testing.T) {
	lines := []string{
		`{"id":"abc123","timestamp":"2025-06-20T13:30:47.000Z"}`,
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}`,
		`{"type":"function_call","call_id":"c1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}`,
		`{"type":"function_call_output","call_id":"c1","output":"file.txt"}`,
		`{"record_type":"state"}`,
	}
	path := writeCodexFile(t, lines)

	recs, _, err := ScanFile(path, 0, "old-session")
	if err != nil {
		t.Fatal(err)
	}
	// init + state skip; message user, message assistant, function_call, function_call_output = 4
	if len(recs) != 4 {
		t.Errorf("expected 4 records, got %d", len(recs))
	}
	if recs[0].RecordType != "user" {
		t.Errorf("recs[0].RecordType = %q, want user", recs[0].RecordType)
	}
	if recs[2].RecordType != "tool_call" {
		t.Errorf("recs[2].RecordType = %q, want tool_call", recs[2].RecordType)
	}
	if recs[2].ToolName != "bash" {
		t.Errorf("recs[2].ToolName = %q, want bash", recs[2].ToolName)
	}
}

func TestScanFile_Offset(t *testing.T) {
	lines := []string{
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second"}]}}`,
	}
	path := writeCodexFile(t, lines)

	// First scan: get offset
	_, offset, err := ScanFile(path, 0, "s1")
	if err != nil {
		t.Fatal(err)
	}

	// Append a new line
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	fmt.Fprintln(f, `{"type":"response_item","timestamp":"2026-04-23T11:30:14.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"third"}]}}`)
	f.Close()

	// Second scan from offset: should only get new line
	recs, _, err := ScanFile(path, offset, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Errorf("expected 1 new record, got %d", len(recs))
	}
	if recs[0].Content != "third" {
		t.Errorf("Content = %q, want third", recs[0].Content)
	}
}

func TestScanFile_PartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-test.jsonl")
	full := `{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`

	// Write a complete line + partial line (no trailing newline)
	f, _ := os.Create(path)
	fmt.Fprintln(f, full)             // complete
	fmt.Fprint(f, full[:len(full)/2]) // partial
	f.Close()

	fi, _ := os.Stat(path)
	recs, offset, err := ScanFile(path, 0, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Errorf("expected 1 record (partial line skipped), got %d", len(recs))
	}
	// Offset should not include the partial line
	if offset >= fi.Size() {
		t.Errorf("offset %d should be less than file size %d (partial not consumed)", offset, fi.Size())
	}
}

// Codex records no prompt_source, so originator is the only signal separating a human
// TUI session from an automated one (codex exec, the TS SDK, or a delegating harness
// such as Claude Code). It must survive the scan so the syncer can map it to entrypoint.
func TestScanMetadata_Originator(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"tui", `{"type":"session_meta","timestamp":"2026-08-06T06:08:45.000Z","payload":{"id":"a","cwd":"/p","originator":"codex-tui"}}`, "codex-tui"},
		{"exec", `{"type":"session_meta","timestamp":"2026-08-06T06:08:45.000Z","payload":{"id":"a","cwd":"/p","originator":"codex_exec"}}`, "codex_exec"},
		{"delegated", `{"type":"session_meta","timestamp":"2026-08-06T06:08:45.000Z","payload":{"id":"a","cwd":"/p","originator":"Claude Code"}}`, "Claude Code"},
		{"absent", `{"type":"session_meta","timestamp":"2026-08-06T06:08:45.000Z","payload":{"id":"a","cwd":"/p"}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := ScanMetadata(writeCodexFile(t, []string{tc.line}), 0)
			if err != nil {
				t.Fatal(err)
			}
			if meta.Originator != tc.want {
				t.Errorf("Originator = %q, want %q", meta.Originator, tc.want)
			}
		})
	}
}

// A fork file embeds the parent's session_meta right after the child's. The child's
// originator must win, exactly as cwd and the fork flags already do.
func TestScanMetadata_OriginatorStickyOnFork(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-08-06T06:08:45.000Z","payload":{"id":"child","forked_from_id":"parent","thread_source":"subagent","cwd":"/p","originator":"codex_exec"}}`,
		`{"type":"session_meta","timestamp":"2026-08-06T06:08:46.000Z","payload":{"id":"parent","thread_source":"user","cwd":"/p","originator":"codex-tui"}}`,
	}
	meta, err := ScanMetadata(writeCodexFile(t, lines), 0)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Originator != "codex_exec" {
		t.Errorf("Originator = %q, want codex_exec (child wins)", meta.Originator)
	}
}
