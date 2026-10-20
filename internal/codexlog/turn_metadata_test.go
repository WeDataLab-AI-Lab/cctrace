package codexlog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTurnMetadataUsesOrderedLifecycleAndExactTopLevelTimes(t *testing.T) {
	lines := []string{
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:01.339Z","payload":{"type":"task_started","turn_id":"root-a","root_turn_id":"root-a","started_at":1791526801}}`,
		`{"type":"response_item","timestamp":"2026-10-09T06:20:01.345Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}}`,
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:01.352Z","payload":{"type":"task_complete","turn_id":"root-a","root_turn_id":"root-a","completed_at":1791526801}}`,
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:01.360Z","payload":{"type":"task_started","turn_id":"root-b","root_turn_id":"root-b","started_at":1791526801}}`,
		`{"type":"response_item","timestamp":"2026-10-09T06:20:01.370Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}}`,
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:01.380Z","payload":{"type":"task_complete","turn_id":"root-b","root_turn_id":"root-b","completed_at":1791526801}}`,
	}
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	records, _, turns, _, metadata, err := ScanFileWithTurnsContext(context.Background(), path, 0, "session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || !records[0].TurnMetadataKnown || records[0].TurnID != "root-a" || records[0].RootTurnID != "root-a" ||
		!records[1].TurnMetadataKnown || records[1].TurnID != "root-b" || records[1].RootTurnID != "root-b" {
		t.Fatalf("record lifecycle metadata: %+v", records)
	}
	if len(turns) != 4 {
		t.Fatalf("turn events=%d, want 4", len(turns))
	}
	wantStart := time.Date(2026, 10, 9, 6, 20, 1, 339_000_000, time.UTC)
	wantEnd := time.Date(2026, 10, 9, 6, 20, 1, 352_000_000, time.UTC)
	if !turns[0].At.Equal(wantStart) || !turns[1].At.Equal(wantEnd) {
		t.Fatalf("top-level lifecycle times start=%s end=%s; want %s and %s", turns[0].At, turns[1].At, wantStart, wantEnd)
	}
	if !metadata.TurnMetadataScanned || metadata.ActiveTurnID != "" || metadata.ActiveRootTurnID != "" {
		t.Fatalf("final metadata=%+v, want a scanned, inactive turn context", metadata)
	}

	prefixOffset := int64(len(lines[0]) + 1)
	prefix, err := ScanMetadataContext(context.Background(), path, prefixOffset)
	if err != nil {
		t.Fatal(err)
	}
	if !prefix.TurnMetadataScanned || prefix.ActiveTurnID != "root-a" || prefix.ActiveRootTurnID != "root-a" {
		t.Fatalf("prefix metadata=%+v, want root-a active across a restart", prefix)
	}
	tail, _, _, _, _, err := ScanFileWithTurnsContext(context.Background(), path, prefixOffset, "session", prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 2 || tail[0].RootTurnID != "root-a" || tail[1].RootTurnID != "root-b" {
		t.Fatalf("resumed records=%+v", tail)
	}
}

func TestTurnMetadataRetainsRootAcrossNestedTurnAndClearsAfterCompletion(t *testing.T) {
	lines := []string{
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:01.100Z","payload":{"type":"task_started","turn_id":"root-a"}}`,
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:01.110Z","payload":{"type":"task_started","turn_id":"child-a","root_turn_id":"root-a"}}`,
		`{"type":"response_item","timestamp":"2026-10-09T06:20:01.120Z","payload":{"type":"function_call","call_id":"call-a","name":"exec_command","arguments":"{}"}}`,
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:01.130Z","payload":{"type":"task_complete","turn_id":"child-a","root_turn_id":"root-a"}}`,
		`{"type":"response_item","timestamp":"2026-10-09T06:20:01.140Z","payload":{"type":"function_call_output","call_id":"call-a","output":"ok"}}`,
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:01.150Z","payload":{"type":"task_complete","turn_id":"root-a"}}`,
		`{"type":"response_item","timestamp":"2026-10-09T06:20:01.160Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"outside"}]}}`,
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:01.200Z","payload":{"type":"task_started","turn_id":"root-b"}}`,
		`{"type":"response_item","timestamp":"2026-10-09T06:20:01.210Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}}`,
	}
	path := filepath.Join(t.TempDir(), "nested.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	records, _, _, _, _, err := ScanFileWithTurnsContext(context.Background(), path, 0, "session", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("records=%d, want function call/output and two user rows", len(records))
	}
	if records[0].TurnID != "child-a" || records[0].RootTurnID != "root-a" || records[1].TurnID != "root-a" || records[1].RootTurnID != "root-a" {
		t.Fatalf("nested root context lost: call=%+v output=%+v", records[0], records[1])
	}
	if !records[2].TurnMetadataKnown || records[2].RootTurnID != "" || records[2].TurnID != "" {
		t.Fatalf("row after completion has stale root metadata: %+v", records[2])
	}
	if records[3].RootTurnID != "root-b" || records[3].TurnID != "root-b" {
		t.Fatalf("second root metadata=%+v", records[3])
	}
}

func TestTurnMetadataDoesNotCarryCopiedForkRootPastTriggerBoundary(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-10-09T06:20:00.000Z","payload":{"id":"child","forked_from_id":"parent","thread_source":"subagent"}}`,
		`{"type":"session_meta","timestamp":"2026-10-09T06:20:00.001Z","payload":{"id":"parent","thread_source":"user"}}`,
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:00.010Z","payload":{"type":"task_started","turn_id":"parent-root"}}`,
		`{"type":"response_item","timestamp":"2026-10-09T06:20:00.020Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"copied"}]}}`,
		`{"type":"inter_agent_communication_metadata","timestamp":"2026-10-09T06:20:00.030Z","payload":{"trigger_turn":true}}`,
		`{"type":"event_msg","timestamp":"2026-10-09T06:20:00.040Z","payload":{"type":"task_started","turn_id":"child-root"}}`,
		`{"type":"response_item","timestamp":"2026-10-09T06:20:00.050Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"child"}]}}`,
	}
	path := filepath.Join(t.TempDir(), "fork.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	records, _, _, _, _, err := ScanFileWithTurnsContext(context.Background(), path, 0, "child", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	records = transcriptAndUsage(records)
	if len(records) != 1 || records[0].Content != "child" || records[0].RootTurnID != "child-root" {
		t.Fatalf("fork records carried copied or stale root metadata: %+v", records)
	}
}
