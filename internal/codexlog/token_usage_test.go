package codexlog

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestScanFile_TokenUsageRecordAndEventCountOnce(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"session","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:01.000Z","payload":{"usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120},"turn_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120},"thread_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120}}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:01.001Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}

	records, _, err := ScanFile(writeCodexFile(t, lines), 0, "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want one usage record", len(records))
	}
	if records[0].RecordType != "usage" {
		t.Fatalf("record type = %q, want usage", records[0].RecordType)
	}
	if records[0].InputTokens == nil || *records[0].InputTokens != 100 {
		t.Fatalf("input tokens = %v, want 100", records[0].InputTokens)
	}
	if records[0].CacheReadTokens == nil || *records[0].CacheReadTokens != 40 {
		t.Fatalf("cached input tokens = %v, want 40", records[0].CacheReadTokens)
	}
	if records[0].OutputTokens == nil || *records[0].OutputTokens != 20 {
		t.Fatalf("output tokens = %v, want 20", records[0].OutputTokens)
	}
	if !records[0].Timestamp.Equal(time.Date(2026, 9, 7, 4, 20, 1, 0, time.UTC)) {
		t.Fatalf("timestamp = %s, want token_usage_record timestamp", records[0].Timestamp)
	}
	if !strings.Contains(string(records[0].Raw), `"type":"token_usage_record"`) {
		t.Fatalf("raw record = %s, want token_usage_record source", records[0].Raw)
	}
}

func TestScanFile_TokenUsageRecordAndMatchingEventAcrossIncrementalScans(t *testing.T) {
	path := writeCodexFile(t, []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"session","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:01.000Z","payload":{"usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120},"turn_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120},"thread_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120}}}`,
	})

	first, offset, metadata, err := ScanFileWithMetadata(path, 0, "session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("first pass records = %d, want one token usage record", len(first))
	}
	if !metadata.HasTokenUsageRecord {
		t.Fatalf("first pass metadata = %+v, want token usage source marker", metadata)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-09-07T04:20:01.001Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	second, _, _, err := ScanFileWithMetadata(path, offset, "session", metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second pass records = %d, want matching event suppressed", len(second))
	}
}

func TestScanFile_TokenUsageRecordAndLastOnlyEventCountOnce(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"session","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:01.000Z","payload":{"usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"thread_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:01.001Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}

	records, _, err := ScanFile(writeCodexFile(t, lines), 0, "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want one usage record", len(records))
	}
	if !strings.Contains(string(records[0].Raw), `"type":"token_usage_record"`) {
		t.Fatalf("raw record = %s, want token_usage_record source", records[0].Raw)
	}
}

func TestScanFile_TokenUsageRecordAndLastOnlyEventAcrossIncrementalScans(t *testing.T) {
	path := writeCodexFile(t, []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"session","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:01.000Z","payload":{"usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"thread_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}`,
	})

	first, offset, metadata, err := ScanFileWithMetadata(path, 0, "session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || !metadata.HasTokenUsageRecord {
		t.Fatalf("first pass records/metadata = %d/%+v, want raw usage and source mode", len(first), metadata)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-09-07T04:20:01.001Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	second, _, _, err := ScanFileWithMetadata(path, offset, "session", metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second pass records = %d, want matching last-only event suppressed", len(second))
	}
}

func TestScanFile_StaleEventDoesNotRewindTokenUsageRecordBaseline(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"session","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:01.000Z","payload":{"usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":0},"thread_token_usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":0}}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:01.001Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":9,"cached_input_tokens":0,"output_tokens":0},"last_token_usage":{"input_tokens":0,"cached_input_tokens":0,"output_tokens":0}}}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:02.000Z","payload":{"usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":0}}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:02.001Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":15,"cached_input_tokens":0,"output_tokens":0},"last_token_usage":{"input_tokens":5,"cached_input_tokens":0,"output_tokens":0}}}}`,
	}

	records, _, metadata, err := ScanFileWithMetadata(writeCodexFile(t, lines), 0, "session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want two raw usage records only", len(records))
	}
	for i, record := range records {
		if !strings.Contains(string(record.Raw), `"type":"token_usage_record"`) {
			t.Fatalf("record %d raw = %s, want token_usage_record source", i, record.Raw)
		}
	}
	if metadata.TotalInputTokens != 15 || metadata.TotalCachedInputTokens != 0 || metadata.TotalOutputTokens != 0 {
		t.Fatalf("metadata totals = %d/%d/%d, want 15/0/0", metadata.TotalInputTokens, metadata.TotalCachedInputTokens, metadata.TotalOutputTokens)
	}
}

// A resumed session's thread_token_usage restarts at the resume, while the
// event_msg total_token_usage keeps counting the whole file. Production row pair
// from 2026-09-13: thread 125,542,703 vs session 1,390,186,040. Diffing the event
// against the thread baseline emitted a phantom 1.26B-token usage on every turn.
func TestScanFile_ResumedSessionEventTotalAboveThreadTotalCountsOnce(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"session","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:01.000Z","payload":{"usage":{"input_tokens":521098,"cached_input_tokens":5888,"output_tokens":278},"thread_token_usage":{"input_tokens":125542703,"cached_input_tokens":120783872,"output_tokens":207499}}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:01.001Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1390186040,"cached_input_tokens":1342912000,"output_tokens":2348121},"last_token_usage":{"input_tokens":521098,"cached_input_tokens":5888,"output_tokens":278}}}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:02.000Z","payload":{"usage":{"input_tokens":521555,"cached_input_tokens":520960,"output_tokens":151},"thread_token_usage":{"input_tokens":126064258,"cached_input_tokens":121304832,"output_tokens":207650}}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:02.001Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1390707595,"cached_input_tokens":1343432960,"output_tokens":2348272},"last_token_usage":{"input_tokens":521555,"cached_input_tokens":520960,"output_tokens":151}}}}`,
	}

	records, _, err := ScanFile(writeCodexFile(t, lines), 0, "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want the two token_usage_record usages only", len(records))
	}
	for i, record := range records {
		if !strings.Contains(string(record.Raw), `"type":"token_usage_record"`) {
			t.Fatalf("record %d raw = %s, want token_usage_record source", i, record.Raw)
		}
	}
}

func TestScanFile_PreExistingMetadataWithoutTokenUsageRecordCountsLastOnlyEvent(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"session","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:01.000Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}
	path := writeCodexFile(t, lines)
	offset := int64(len(lines[0]) + 1)
	initial := Metadata{
		CWD:                 "/myproject",
		Model:               "gpt-5",
		TokenUsageScanned:   true,
		HasTokenUsageRecord: false, // pre-existing state predates source-mode metadata
		ForkMetadataScanned: true,
	}

	records, _, metadata, err := ScanFileWithMetadata(path, offset, "session", initial)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want last-only legacy usage", len(records))
	}
	if records[0].InputTokens == nil || *records[0].InputTokens != 100 {
		t.Fatalf("input tokens = %v, want 100", records[0].InputTokens)
	}
	if metadata.HasTokenUsageRecord {
		t.Fatalf("metadata = %+v, want source mode to remain unset", metadata)
	}
}

func TestScanFile_CompactionTokenUsageRecordCountedOnceWithStaleEvent(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"session","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:01.000Z","payload":{"usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120},"turn_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120},"thread_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120}}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:01.001Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:02.000Z","payload":{"usage":{"input_tokens":200,"cached_input_tokens":180,"output_tokens":5,"total_tokens":205},"turn_token_usage":{"input_tokens":300,"cached_input_tokens":220,"output_tokens":25,"total_tokens":325},"thread_token_usage":{"input_tokens":300,"cached_input_tokens":220,"output_tokens":25,"total_tokens":325}}}`,
		`{"type":"compacted","timestamp":"2026-09-07T04:20:02.001Z","payload":{"message":"compacted"}}`,
		`{"type":"world_state","timestamp":"2026-09-07T04:20:02.002Z","payload":{}}`,
		`{"type":"turn_context","timestamp":"2026-09-07T04:20:02.003Z","payload":{"cwd":"/myproject","model":"gpt-5"}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:02.004Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}

	records, _, err := ScanFile(writeCodexFile(t, lines), 0, "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want normal and compaction usage", len(records))
	}
	if records[1].InputTokens == nil || *records[1].InputTokens != 200 {
		t.Fatalf("compaction input tokens = %v, want 200", records[1].InputTokens)
	}
	if records[1].CacheReadTokens == nil || *records[1].CacheReadTokens != 180 {
		t.Fatalf("compaction cached input tokens = %v, want 180", records[1].CacheReadTokens)
	}
	if records[1].OutputTokens == nil || *records[1].OutputTokens != 5 {
		t.Fatalf("compaction output tokens = %v, want 5", records[1].OutputTokens)
	}
	if !records[1].Timestamp.Equal(time.Date(2026, 9, 7, 4, 20, 2, 0, time.UTC)) {
		t.Fatalf("compaction timestamp = %s, want token_usage_record timestamp", records[1].Timestamp)
	}
	if !strings.Contains(string(records[1].Raw), `"type":"token_usage_record"`) {
		t.Fatalf("compaction raw record = %s, want token_usage_record source", records[1].Raw)
	}
}

func TestScanFile_LegacyEventMsgUsageOnlyRemainsUnchanged(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"session","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:01.000Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}

	records, _, err := ScanFile(writeCodexFile(t, lines), 0, "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want one usage record", len(records))
	}
	if strings.Contains(string(records[0].Raw), `"type":"token_usage_record"`) {
		t.Fatalf("raw record = %s, want legacy event_msg source", records[0].Raw)
	}
	if records[0].InputTokens == nil || *records[0].InputTokens != 100 {
		t.Fatalf("input tokens = %v, want 100", records[0].InputTokens)
	}
}

func TestScanFile_TokenUsageRecordForkGateSkipsInheritedHistory(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.000Z","payload":{"id":"child","forked_from_id":"parent","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-09-07T04:20:00.001Z","payload":{"id":"parent","thread_source":"user","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:01.000Z","payload":{"usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120},"turn_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120},"thread_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20,"total_tokens":120}}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:01.001Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
		`{"type":"inter_agent_communication_metadata","timestamp":"2026-09-07T04:20:02.000Z","payload":{"trigger_turn":true}}`,
		`{"type":"token_usage_record","timestamp":"2026-09-07T04:20:03.000Z","payload":{"usage":{"input_tokens":25,"cached_input_tokens":10,"output_tokens":2,"total_tokens":27},"turn_token_usage":{"input_tokens":25,"cached_input_tokens":10,"output_tokens":2,"total_tokens":27},"thread_token_usage":{"input_tokens":125,"cached_input_tokens":50,"output_tokens":22,"total_tokens":147}}}`,
		`{"type":"event_msg","timestamp":"2026-09-07T04:20:03.001Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":125,"cached_input_tokens":50,"output_tokens":22},"last_token_usage":{"input_tokens":25,"cached_input_tokens":10,"output_tokens":2}}}}`,
	}

	records, _, err := ScanFile(writeCodexFile(t, lines), 0, "child")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want child usage only", len(records))
	}
	if records[0].InputTokens == nil || *records[0].InputTokens != 25 {
		t.Fatalf("child input tokens = %v, want 25", records[0].InputTokens)
	}
	if !strings.Contains(string(records[0].Raw), `"type":"token_usage_record"`) {
		t.Fatalf("child raw record = %s, want token_usage_record source", records[0].Raw)
	}
}
