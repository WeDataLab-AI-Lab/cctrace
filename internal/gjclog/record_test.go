package gjclog

import (
	"testing"
	"time"
)

func TestParseLine_HeaderSetsStateNoRecord(t *testing.T) {
	line := []byte(`{"type":"session","version":5,"id":"019f0000-0000-7000-8000-000000000001","timestamp":"2026-01-01T16:17:22.275Z","cwd":"/Users/alice/project","title":"my title","titleSource":"auto"}`)
	state := &scanState{}

	recs := parseLine(line, state)

	if recs != nil {
		t.Errorf("header line should not produce a record, got %+v", recs)
	}
	if state.sessionID != "019f0000-0000-7000-8000-000000000001" {
		t.Errorf("sessionID = %q, want the header id", state.sessionID)
	}
	if state.cwd != "/Users/alice/project" {
		t.Errorf("cwd = %q, want /Users/alice/project", state.cwd)
	}
	if state.title != "my title" {
		t.Errorf("title = %q, want my title", state.title)
	}
}

func TestParseLine_HeaderTitleAbsent(t *testing.T) {
	line := []byte(`{"type":"session","version":5,"id":"019f0000-0000-7000-8000-000000000001","timestamp":"2026-01-01T16:17:22.275Z","cwd":"/Users/alice/project"}`)
	state := &scanState{}

	parseLine(line, state)

	if state.title != "" {
		t.Errorf("title = %q, want empty", state.title)
	}
}

// A subsequent header line (e.g. re-scanned during a metadata pass) must not
// clobber previously inherited cwd/title with blanks when its own fields are
// empty. Mirrors omolog's guard.
func TestParseLine_HeaderDoesNotClobberInheritedMetadataWithEmptyFields(t *testing.T) {
	state := &scanState{sessionID: "s1", cwd: "/inherited", title: "inherited title"}
	line := []byte(`{"type":"session","version":5,"id":"","timestamp":"2026-01-01T16:17:22.275Z","cwd":"","title":""}`)

	parseLine(line, state)

	if state.cwd != "/inherited" {
		t.Errorf("cwd = %q, want inherited value preserved", state.cwd)
	}
	if state.title != "inherited title" {
		t.Errorf("title = %q, want inherited value preserved", state.title)
	}
	if state.sessionID != "s1" {
		t.Errorf("sessionID = %q, want inherited value preserved", state.sessionID)
	}
}

func TestParseLine_UserMessage(t *testing.T) {
	line := []byte(`{"id":"abc12345","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","attribution":"user","content":"hello there"}}`)
	state := &scanState{sessionID: "sess-1", cwd: "/proj", title: "t"}

	recs := parseLine(line, state)

	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	rec := recs[0]
	if rec.RecordType != "user" {
		t.Errorf("RecordType = %q, want user", rec.RecordType)
	}
	if rec.MessageID != "abc12345" {
		t.Errorf("MessageID = %q, want abc12345", rec.MessageID)
	}
	if rec.ParentID != "" {
		t.Errorf("ParentID = %q, want empty for null parentId", rec.ParentID)
	}
	if rec.Content != "hello there" {
		t.Errorf("Content = %q, want hello there", rec.Content)
	}
	if rec.SessionID != "sess-1" || rec.CWD != "/proj" || rec.Title != "t" {
		t.Errorf("context = %+v, want sess-1//proj/t", rec)
	}
	want := time.Date(2026, 1, 1, 16, 17, 23, 0, time.UTC)
	if !rec.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", rec.Timestamp, want)
	}
	if rec.InputTokens != nil {
		t.Errorf("InputTokens = %v, want nil for user record", rec.InputTokens)
	}
	if rec.Attribution != "user" {
		t.Errorf("Attribution = %q, want user", rec.Attribution)
	}
}

// A subagent-generated user turn carries attribution "agent" instead of
// "user" - it must be parsed through as-is, not normalized or dropped.
func TestParseLine_UserMessageAttributionAgent(t *testing.T) {
	line := []byte(`{"id":"abc22222","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","attribution":"agent","content":"synthetic turn"}}`)
	state := &scanState{sessionID: "sess-1"}

	recs := parseLine(line, state)

	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if recs[0].Attribution != "agent" {
		t.Errorf("Attribution = %q, want agent", recs[0].Attribution)
	}
}

// When the attribution field is absent altogether, Attribution must stay
// empty - not defaulted to "user" or any other value, since an absent field
// is not evidence of who authored the turn.
func TestParseLine_UserMessageAttributionAbsent(t *testing.T) {
	line := []byte(`{"id":"abc33333","parentId":null,"timestamp":"2026-01-01T16:17:23.000Z","type":"message","message":{"role":"user","content":"no attribution field"}}`)
	state := &scanState{sessionID: "sess-1"}

	recs := parseLine(line, state)

	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	if recs[0].Attribution != "" {
		t.Errorf("Attribution = %q, want empty when field absent", recs[0].Attribution)
	}
}

func TestParseLine_AssistantMessageWithUsageAndCost(t *testing.T) {
	line := []byte(`{"id":"def67890","parentId":"abc12345","timestamp":"2026-01-01T16:17:24.000Z","type":"message","message":{"role":"assistant","api":"messages","model":"claude-opus-5","provider":"anthropic","duration":1234.5,"ttft":210.1,"stopReason":"end_turn","responseId":"resp-1","content":[{"type":"text","text":"hi"}],"usage":{"input":2,"output":190,"cacheRead":0,"cacheWrite":18578,"totalTokens":18770,"cost":{"input":0.00001,"output":0.00475,"cacheRead":0,"cacheWrite":0.1161125,"total":0.1208725},"cttl":{"ephemeral1h":18578}}}}`)
	state := &scanState{sessionID: "sess-1", cwd: "/proj"}

	recs := parseLine(line, state)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	rec := recs[0]

	if rec.RecordType != "assistant" {
		t.Errorf("RecordType = %q, want assistant", rec.RecordType)
	}
	if rec.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want claude-opus-5", rec.Model)
	}
	if rec.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", rec.Provider)
	}
	if rec.Content != "hi" {
		t.Errorf("Content = %q, want hi", rec.Content)
	}
	if rec.InputTokens == nil || *rec.InputTokens != 2 {
		t.Fatalf("InputTokens = %v, want 2", rec.InputTokens)
	}
	if rec.OutputTokens == nil || *rec.OutputTokens != 190 {
		t.Fatalf("OutputTokens = %v, want 190", rec.OutputTokens)
	}
	if rec.CacheReadTokens == nil || *rec.CacheReadTokens != 0 {
		t.Fatalf("CacheReadTokens = %v, want 0", rec.CacheReadTokens)
	}
	if rec.CacheWriteTokens == nil || *rec.CacheWriteTokens != 18578 {
		t.Fatalf("CacheWriteTokens = %v, want 18578", rec.CacheWriteTokens)
	}
	if rec.TotalTokens == nil || *rec.TotalTokens != 18770 {
		t.Fatalf("TotalTokens = %v, want 18770", rec.TotalTokens)
	}
	if rec.CostUSD == nil || *rec.CostUSD != 0.1208725 {
		t.Fatalf("CostUSD = %v, want 0.1208725", rec.CostUSD)
	}
	if rec.DurationMS == nil || *rec.DurationMS != 1234.5 {
		t.Fatalf("DurationMS = %v, want 1234.5", rec.DurationMS)
	}
	if rec.TTFTMS == nil || *rec.TTFTMS != 210.1 {
		t.Fatalf("TTFTMS = %v, want 210.1", rec.TTFTMS)
	}
}

func TestParseLine_ToolResultMessage(t *testing.T) {
	line := []byte(`{"id":"ghi11111","parentId":"def67890","timestamp":"2026-01-01T16:17:25.000Z","type":"message","message":{"role":"toolResult","toolName":"bash","toolCallId":"call-1","isError":true,"content":"command failed","details":{}}}`)
	state := &scanState{sessionID: "sess-1"}

	recs := parseLine(line, state)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	rec := recs[0]

	if rec.RecordType != "tool_result" {
		t.Errorf("RecordType = %q, want tool_result", rec.RecordType)
	}
	if rec.ToolName != "bash" {
		t.Errorf("ToolName = %q, want bash", rec.ToolName)
	}
	if rec.ToolCallID != "call-1" {
		t.Errorf("ToolCallID = %q, want call-1", rec.ToolCallID)
	}
	if !rec.IsError {
		t.Errorf("IsError = false, want true")
	}
	if rec.Content != "command failed" {
		t.Errorf("Content = %q, want command failed", rec.Content)
	}
	if rec.Attribution != "" {
		t.Errorf("Attribution = %q, want empty for a toolResult record", rec.Attribution)
	}
}

// toolCall blocks only occur in assistant messages in real gjc data. A
// toolCall-typed block appearing in a user or toolResult message's content
// (which would have no assistant context: no model/provider) must not emit a
// sparse tool_call record.
func TestParseLine_ToolCallBlockOutsideAssistantMessageProducesNoToolCallRecord(t *testing.T) {
	line := []byte(`{"id":"m1","parentId":null,"timestamp":"2026-01-01T16:17:29.000Z","type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"},{"type":"toolCall","id":"call-x","name":"bash","arguments":{}}]}}`)
	state := &scanState{sessionID: "sess-1"}

	recs := parseLine(line, state)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record (user only, no tool_call), got %d: %+v", len(recs), recs)
	}
	if recs[0].RecordType != "user" {
		t.Errorf("RecordType = %q, want user", recs[0].RecordType)
	}
}

// Content must contain only text-block text; thinking-block text goes to the
// separate Thinking field. Real gjc data shows most assistant messages carry
// thinking with no text block at all, so conflating the two would make
// Content pure reasoning rather than what the assistant actually said.
func TestParseLine_AssistantMessageTextAndThinkingBlocks(t *testing.T) {
	line := []byte(`{"id":"jkl22222","parentId":null,"timestamp":"2026-01-01T16:17:26.000Z","type":"message","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"thinking","thinking":"let me think"},{"type":"text","text":"answer"}]}}`)
	state := &scanState{sessionID: "sess-1"}

	recs := parseLine(line, state)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	rec := recs[0]

	if rec.Content != "answer" {
		t.Errorf("Content = %q, want answer (text block only)", rec.Content)
	}
	if rec.Thinking != "let me think" {
		t.Errorf("Thinking = %q, want let me think", rec.Thinking)
	}
}

func TestParseLine_AssistantMessageOnlyThinking(t *testing.T) {
	line := []byte(`{"id":"jkl33333","parentId":null,"timestamp":"2026-01-01T16:17:27.000Z","type":"message","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"thinking","thinking":"pure reasoning, no reply yet"}]}}`)
	state := &scanState{sessionID: "sess-1"}

	recs := parseLine(line, state)
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
	rec := recs[0]

	if rec.Content != "" {
		t.Errorf("Content = %q, want empty when only a thinking block is present", rec.Content)
	}
	if rec.Thinking != "pure reasoning, no reply yet" {
		t.Errorf("Thinking = %q, want pure reasoning, no reply yet", rec.Thinking)
	}
}

// gjc puts tool invocations as toolCall content blocks inside assistant
// messages. Each must surface as its own tool_call record (mirroring
// codexlog's function_call handling) in addition to the assistant record.
func TestParseLine_AssistantMessageWithToolCallBlocks(t *testing.T) {
	line := []byte(`{"id":"m1","parentId":null,"timestamp":"2026-01-01T16:17:28.000Z","type":"message","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"running two tools"},{"type":"toolCall","id":"call-1","name":"bash","arguments":{"cmd":"ls"}},{"type":"toolCall","id":"call-2","name":"task","arguments":{"tasks":[{"id":"TokenLogProbe"}]}}]}}`)
	state := &scanState{sessionID: "sess-1", cwd: "/proj"}

	recs := parseLine(line, state)
	if len(recs) != 3 {
		t.Fatalf("expected 1 assistant + 2 tool_call records, got %d: %+v", len(recs), recs)
	}
	if recs[0].RecordType != "assistant" || recs[0].Content != "running two tools" {
		t.Fatalf("recs[0] = %+v, want assistant/running two tools", recs[0])
	}
	if recs[1].RecordType != "tool_call" || recs[1].ToolName != "bash" || recs[1].ToolCallID != "call-1" {
		t.Fatalf("recs[1] = %+v, want tool_call/bash/call-1", recs[1])
	}
	if recs[1].ToolArgs != `{"cmd":"ls"}` {
		t.Errorf("recs[1].ToolArgs = %q, want {\"cmd\":\"ls\"}", recs[1].ToolArgs)
	}
	if recs[2].RecordType != "tool_call" || recs[2].ToolName != "task" || recs[2].ToolCallID != "call-2" {
		t.Fatalf("recs[2] = %+v, want tool_call/task/call-2", recs[2])
	}
	// tool_call records carry the same session context as the assistant record.
	if recs[1].SessionID != "sess-1" || recs[1].CWD != "/proj" {
		t.Errorf("recs[1] context = %+v, want sess-1//proj", recs[1])
	}
}

func TestParseLine_SkipsNonMessageLines(t *testing.T) {
	lines := []string{
		`{"type":"model_change","timestamp":"2026-01-01T16:17:27.000Z","model":"claude-opus-5"}`,
		`{"type":"thinking_level_change","timestamp":"2026-01-01T16:17:27.000Z","level":"high"}`,
		`{"type":"configured_model_chain","timestamp":"2026-01-01T16:17:27.000Z"}`,
		`{"type":"custom","timestamp":"2026-01-01T16:17:27.000Z"}`,
		`{"type":"custom_message","timestamp":"2026-01-01T16:17:27.000Z"}`,
		`{"type":"session_init","timestamp":"2026-01-01T16:17:27.000Z"}`,
		`{"type":"compaction","timestamp":"2026-01-01T16:17:27.000Z"}`,
	}
	state := &scanState{sessionID: "sess-1"}
	for _, l := range lines {
		if recs := parseLine([]byte(l), state); recs != nil {
			t.Errorf("line %q should be skipped, got %+v", l, recs)
		}
	}
}

func TestParseLine_MalformedJSON(t *testing.T) {
	state := &scanState{sessionID: "sess-1"}
	recs := parseLine([]byte(`{not valid json`), state)
	if recs != nil {
		t.Errorf("malformed line should return nil, got %+v", recs)
	}
}

func TestExtractTextContent_String(t *testing.T) {
	text, thinking, toolCalls := extractContent([]byte(`"plain string"`))
	if text != "plain string" {
		t.Errorf("text = %q, want plain string", text)
	}
	if thinking != "" || toolCalls != nil {
		t.Errorf("thinking/toolCalls = %q/%v, want empty/nil for plain string", thinking, toolCalls)
	}
}

func TestExtractTextContent_Nil(t *testing.T) {
	text, thinking, toolCalls := extractContent(nil)
	if text != "" || thinking != "" || toolCalls != nil {
		t.Errorf("extractContent(nil) = %q/%q/%v, want all empty", text, thinking, toolCalls)
	}
}

func TestParseTokenLogLine(t *testing.T) {
	line := []byte(`{"subagentId":"root","agent":"main","turn":1,"at":"2026-01-01T02:50:34.738Z","input":2,"output":367,"cacheRead":0,"cacheWrite":18580,"totalTokens":18949,"model":"claude-opus-5"}`)

	rec := parseTokenLogLine(line)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.SubagentID != "root" || rec.Agent != "main" || rec.Turn != 1 {
		t.Errorf("identity = %+v, want root/main/1", rec)
	}
	if rec.Input != 2 || rec.Output != 367 || rec.CacheRead != 0 || rec.CacheWrite != 18580 || rec.TotalTokens != 18949 {
		t.Errorf("tokens = %+v, want 2/367/0/18580/18949", rec)
	}
	if rec.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want claude-opus-5", rec.Model)
	}
	want := time.Date(2026, 1, 1, 2, 50, 34, 738000000, time.UTC)
	if !rec.At.Equal(want) {
		t.Errorf("At = %v, want %v", rec.At, want)
	}
}

func TestParseTokenLogLine_Malformed(t *testing.T) {
	rec := parseTokenLogLine([]byte(`{not valid`))
	if rec != nil {
		t.Errorf("expected nil for malformed line, got %+v", rec)
	}
}

// A JSON `null` line (or a content-free object) must not produce a
// zero-value TokenLogRecord - it carries no subagent identity or timestamp,
// so it is not a real token-log entry.
func TestParseTokenLogLine_NullLine(t *testing.T) {
	rec := parseTokenLogLine([]byte(`null`))
	if rec != nil {
		t.Errorf("expected nil for a null line, got %+v", rec)
	}
}

func TestParseTokenLogLine_EmptyObject(t *testing.T) {
	rec := parseTokenLogLine([]byte(`{}`))
	if rec != nil {
		t.Errorf("expected nil for an empty object, got %+v", rec)
	}
}
