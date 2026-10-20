package codexlog

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// turnFixtureLines is a rollout of three turns in the shapes measured on local
// files: one that completes, one started under a root turn and interrupted, and
// one still open whose task_started carries neither started_at nor a context
// window. The conversation text in it -- the final reply, the developer
// instructions -- must not reach the lifecycle metadata outputs. Instruction
// context records are collected separately for inspection.
var turnFixtureLines = []string{
	`{"type":"session_meta","timestamp":"2026-10-01T09:00:00.000Z","payload":{"id":"019d0000-0000-7000-8000-000000000001","cwd":"/work/repo","originator":"codex-tui","model_provider":"openai"}}`,
	`{"type":"event_msg","timestamp":"2026-10-01T09:00:01.000Z","payload":{"type":"task_started","turn_id":"turn-1","started_at":1790845201,"model_context_window":258400,"collaboration_mode_kind":"default"}}`,
	`{"type":"turn_context","timestamp":"2026-10-01T09:00:01.100Z","payload":{"turn_id":"turn-1","cwd":"/work/repo","model":"test-model","effort":"high","collaboration_mode":{"mode":"default","settings":{"model":"test-model","reasoning_effort":"high","developer_instructions":"DEVELOPER-INSTRUCTIONS-TEXT"}},"approval_policy":"on-request","sandbox_policy":{"type":"workspace-write"},"summary":"auto"}}`,
	`{"type":"response_item","timestamp":"2026-10-01T09:00:02.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first request"}]}}`,
	`{"type":"response_item","timestamp":"2026-10-01T09:00:05.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}`,
	`{"type":"event_msg","timestamp":"2026-10-01T09:00:05.500Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30}},"rate_limits":null}}`,
	`{"type":"event_msg","timestamp":"2026-10-01T09:00:06.000Z","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":1790845206,"duration_ms":5000,"time_to_first_token_ms":800,"last_agent_message":"LAST-AGENT-MESSAGE-TEXT"}}`,
	`{"type":"event_msg","timestamp":"2026-10-01T09:01:00.000Z","payload":{"type":"task_started","turn_id":"turn-2","root_turn_id":"turn-root-2","started_at":1790845260.25,"model_context_window":258400,"collaboration_mode_kind":"plan"}}`,
	`{"type":"turn_context","timestamp":"2026-10-01T09:01:00.100Z","payload":{"turn_id":"turn-2","root_turn_id":"turn-root-2","cwd":"/work/repo","model":"test-model","effort":"medium","collaboration_mode":{"mode":"plan","settings":{"developer_instructions":"DEVELOPER-INSTRUCTIONS-TEXT"}}}}`,
	`{"type":"response_item","timestamp":"2026-10-01T09:01:01.000Z","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":"{\"command\":[\"ls\"]}"}}`,
	`{"type":"event_msg","timestamp":"2026-10-01T09:01:10.000Z","payload":{"type":"turn_aborted","turn_id":"turn-2","reason":"interrupted","completed_at":1790845270,"duration_ms":9750}}`,
	`{"type":"event_msg","timestamp":"2026-10-01T09:02:00.000Z","payload":{"type":"task_started","turn_id":"turn-3","model_context_window":null,"collaboration_mode_kind":"default"}}`,
}

func i64(v int64) *int64 { return &v }

// Each lifecycle line becomes one TurnEvent carrying what it says, the offset it
// starts at and its own timestamp; epoch seconds become UTC times, fraction
// kept. None of the conversation text the lines hold reaches any output.
func TestScanFindsTurnLifecycleLines(t *testing.T) {
	path := writeCodexFile(t, turnFixtureLines)
	records, samples, turns, _, meta, err := ScanFileWithTurnsContext(t.Context(), path, 0, "sess-turns", Metadata{})
	if err != nil {
		t.Fatal(err)
	}

	var starts []int64
	at := int64(0)
	for _, l := range turnFixtureLines {
		starts = append(starts, at)
		at += int64(len(l)) + 1
	}
	ts := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	want := []TurnEvent{
		{Kind: "task_started", Offset: starts[1], At: ts("2026-10-01T09:00:01Z"), TurnID: "turn-1",
			StartedAt: ts("2026-10-01T09:00:01Z"), ModelContextWindow: i64(258400), CollaborationModeKind: "default"},
		{Kind: "turn_context", Offset: starts[2], At: ts("2026-10-01T09:00:01.1Z"), TurnID: "turn-1", Model: "test-model", Effort: "high"},
		{Kind: "task_complete", Offset: starts[6], At: ts("2026-10-01T09:00:06Z"), TurnID: "turn-1",
			CompletedAt: ts("2026-10-01T09:00:06Z"), DurationMs: i64(5000), TimeToFirstTokenMs: i64(800)},
		{Kind: "task_started", Offset: starts[7], At: ts("2026-10-01T09:01:00Z"), TurnID: "turn-2", RootTurnID: "turn-root-2",
			StartedAt: ts("2026-10-01T09:01:00.25Z"), ModelContextWindow: i64(258400), CollaborationModeKind: "plan"},
		{Kind: "turn_context", Offset: starts[8], At: ts("2026-10-01T09:01:00.1Z"), TurnID: "turn-2", RootTurnID: "turn-root-2", Model: "test-model", Effort: "medium"},
		{Kind: "turn_aborted", Offset: starts[10], At: ts("2026-10-01T09:01:10Z"), TurnID: "turn-2",
			CompletedAt: ts("2026-10-01T09:01:10Z"), DurationMs: i64(9750), Reason: "interrupted"},
		{Kind: "task_started", Offset: starts[11], At: ts("2026-10-01T09:02:00Z"), TurnID: "turn-3", CollaborationModeKind: "default"},
	}
	gotJSON, _ := json.Marshal(turns)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("turns\n got %s\nwant %s", gotJSON, wantJSON)
	}
	for _, e := range turns {
		if e.StartedAt.Location() != time.UTC || e.CompletedAt.Location() != time.UTC {
			t.Errorf("%s %s: times not in UTC", e.Kind, e.TurnID)
		}
	}

	everything, err := json.Marshal([]any{samples, turns, meta})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"LAST-AGENT-MESSAGE-TEXT", "DEVELOPER-INSTRUCTIONS-TEXT"} {
		if strings.Contains(string(everything), text) {
			t.Errorf("%s reached the scan's output", text)
		}
	}
	for _, record := range records {
		if strings.Contains(string(record.Raw), "LAST-AGENT-MESSAGE-TEXT") {
			t.Error("lifecycle completion reply was duplicated into a record")
		}
	}

	// ScanTurns finds the same entries without holding a Record.
	only, err := ScanTurns(t.Context(), path, "sess-turns")
	if err != nil {
		t.Fatal(err)
	}
	if onlyJSON, _ := json.Marshal(only); string(onlyJSON) != string(gotJSON) {
		t.Errorf("ScanTurns\n got %s\nwant %s", onlyJSON, gotJSON)
	}
}

// goldenTurnFixtureRecords is what the scanner produced for turnFixtureLines at
// f3810fda, before it read turn lifecycle lines.
const goldenTurnFixtureRecords = `[{"SessionID":"sess-turns","Timestamp":"2026-10-01T09:00:02Z","RecordType":"user","CWD":"/work/repo","Model":"test-model","Content":"first request","ToolName":"","CallID":"","ToolArgs":"","ToolOutput":"","InputTokens":null,"OutputTokens":null,"CacheReadTokens":null,"Raw":{"type":"response_item","timestamp":"2026-10-01T09:00:02.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first request"}]}}},` +
	`{"SessionID":"sess-turns","Timestamp":"2026-10-01T09:00:05Z","RecordType":"assistant","CWD":"/work/repo","Model":"test-model","Content":"first answer","ToolName":"","CallID":"","ToolArgs":"","ToolOutput":"","InputTokens":null,"OutputTokens":null,"CacheReadTokens":null,"Raw":{"type":"response_item","timestamp":"2026-10-01T09:00:05.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]}}},` +
	`{"SessionID":"sess-turns","Timestamp":"2026-10-01T09:00:05.5Z","RecordType":"usage","CWD":"/work/repo","Model":"test-model","Content":"","ToolName":"","CallID":"","ToolArgs":"","ToolOutput":"","InputTokens":100,"OutputTokens":30,"CacheReadTokens":20,"Raw":{"type":"event_msg","timestamp":"2026-10-01T09:00:05.500Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30}},"rate_limits":null}}},` +
	`{"SessionID":"sess-turns","Timestamp":"2026-10-01T09:01:01Z","RecordType":"tool_call","CWD":"/work/repo","Model":"test-model","Content":"","ToolName":"shell","CallID":"call-1","ToolArgs":"\"{\\\"command\\\":[\\\"ls\\\"]}\"","ToolOutput":"","InputTokens":null,"OutputTokens":null,"CacheReadTokens":null,"Raw":{"type":"response_item","timestamp":"2026-10-01T09:01:01.000Z","payload":{"type":"function_call","name":"shell","call_id":"call-1","arguments":"{\"command\":[\"ls\"]}"}}}]`

// Reading turn lines preserves every existing Record. Instruction-bearing
// contexts now add their own records without changing the original rows.
func TestTurnReadingLeavesRecordsAsTheyWere(t *testing.T) {
	path := writeCodexFile(t, turnFixtureLines)
	viaTurns, _, _, _, _, err := ScanFileWithTurnsContext(t.Context(), path, 0, "sess-turns", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	viaSamples, _, _, _, err := ScanFileWithSamplesContext(t.Context(), path, 0, "sess-turns", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	viaScanFile, _, err := ScanFile(path, 0, "sess-turns")
	if err != nil {
		t.Fatal(err)
	}
	for name, records := range map[string][]*Record{"ScanFileWithTurnsContext": viaTurns, "ScanFileWithSamplesContext": viaSamples, "ScanFile": viaScanFile} {
		var existing []*Record
		contexts := 0
		for _, record := range records {
			if record.RecordType == "context" {
				contexts++
				if !strings.Contains(string(record.Raw), "DEVELOPER-INSTRUCTIONS-TEXT") {
					t.Errorf("%s: context lost its instructions", name)
				}
				continue
			}
			existing = append(existing, record)
		}
		if contexts != 2 {
			t.Errorf("%s: got %d instruction contexts, want 2", name, contexts)
		}
		got, err := json.Marshal(existing)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != goldenTurnFixtureRecords {
			t.Errorf("%s records\n got %s\nwant %s", name, got, goldenTurnFixtureRecords)
		}
	}
}

// A subagent fork copies its parent's history, turn lines included, ahead of the
// trigger_turn marker. Those turns are the parent's: they are gated as the
// parent's records are, and only the fork's own turns are read.
func TestTurnsOfCopiedForkHistoryAreNotRead(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-07-27T06:29:17.513Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-07-27T06:29:17.513Z","payload":{"id":"parent-session","thread_source":"user","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:17.513Z","payload":{"type":"task_started","turn_id":"parent-turn","started_at":1785133757}}`,
		`{"type":"turn_context","timestamp":"2026-07-27T06:29:17.514Z","payload":{"turn_id":"parent-turn","cwd":"/myproject","model":"test-model"}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:18.000Z","payload":{"type":"task_complete","turn_id":"parent-turn","completed_at":1785133758}}`,
		`{"type":"inter_agent_communication_metadata","timestamp":"2026-07-27T06:29:19.131Z","payload":{"trigger_turn":true}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:20.000Z","payload":{"type":"task_started","turn_id":"child-turn","started_at":1785133760}}`,
	}
	turns, err := ScanTurns(t.Context(), writeCodexFile(t, lines), "child-session")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range turns {
		ids = append(ids, fmt.Sprintf("%s/%s", e.Kind, e.TurnID))
	}
	if len(ids) != 1 || ids[0] != "task_started/child-turn" {
		t.Errorf("turns %v, want the child's own task_started alone", ids)
	}
}

// An epoch value a JSON timestamp cannot carry -- milliseconds written where
// seconds belong, a negative one -- is no time at all rather than a value that
// would fail every request it went out in.
func TestTurnTimesOutOfRangeAreDropped(t *testing.T) {
	for _, v := range []float64{-1, 0, 1790845201000, maxEpochSeconds} {
		if got := epochSeconds(&v); !got.IsZero() {
			t.Errorf("epochSeconds(%v) = %v, want zero", v, got)
		}
	}
	v := float64(maxEpochSeconds - 1)
	if got, err := json.Marshal(epochSeconds(&v)); err != nil {
		t.Errorf("the last second before the cut does not encode: %v (%s)", err, got)
	}
}

// A task_complete that carries an error ended in it. Its codex_error_info is
// read when it is a string; an object is not, and the error's message -- text --
// never is.
func TestTurnErrorIsReadWithoutItsMessage(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-10-01T09:00:00.000Z","payload":{"id":"s","cwd":"/work/repo","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:00:06.000Z","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":1790845206,"error":{"message":"ERROR-MESSAGE-TEXT","codex_error_info":"context_window_exceeded"}}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:01:06.000Z","payload":{"type":"task_complete","turn_id":"turn-2","completed_at":1790845266,"error":{"message":"ERROR-MESSAGE-TEXT","codex_error_info":{"http_connection_failed":{"http_status_code":502}}}}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:02:06.000Z","payload":{"type":"task_complete","turn_id":"turn-3","completed_at":1790845326,"error":null}}`,
	}
	records, samples, turns, _, meta, err := ScanFileWithTurnsContext(t.Context(), writeCodexFile(t, lines), 0, "s", Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 3 {
		t.Fatalf("%d turns, want 3", len(turns))
	}
	for i, want := range []struct {
		err  bool
		info string
	}{{true, "context_window_exceeded"}, {true, ""}, {false, ""}} {
		if turns[i].Error != want.err || turns[i].ErrorInfo != want.info {
			t.Errorf("%s: error %v info %q, want %v %q", turns[i].TurnID, turns[i].Error, turns[i].ErrorInfo, want.err, want.info)
		}
	}
	everything, _ := json.Marshal([]any{records, samples, turns, meta})
	if strings.Contains(string(everything), "ERROR-MESSAGE-TEXT") {
		t.Error("the error's message reached the scan's output")
	}
}

// A metadata scan wants the file's context alone: it collects no turn.
func TestMetadataScanReadsNoTurn(t *testing.T) {
	state, err := scanMetadata(t.Context(), writeCodexFile(t, turnFixtureLines), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.turns) != 0 {
		t.Errorf("the metadata scan collected %d turns", len(state.turns))
	}
	if state.cwd != "/work/repo" || state.model != "test-model" {
		t.Errorf("metadata cwd %q model %q", state.cwd, state.model)
	}
}
