package codexlog

import (
	"strings"
	"testing"
	"time"
)

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		typ  string
		want Format
	}{
		{"session_meta", FormatNew},
		{"turn_context", FormatNew},
		{"response_item", FormatNew},
		{"event_msg", FormatNew},
		{"compacted", FormatNew},
		{"message", FormatOld},
		{"function_call", FormatOld},
		{"function_call_output", FormatOld},
		{"reasoning", FormatOld},
		{"", FormatUnknown},
		{"unknown_type", FormatUnknown},
	}
	for _, c := range cases {
		got := DetectFormat(c.typ)
		if got != c.want {
			t.Errorf("DetectFormat(%q) = %v, want %v", c.typ, got, c.want)
		}
	}
}

func TestParseNewFormat_SessionMeta(t *testing.T) {
	line := []byte(`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"id":"abc123","cwd":"/Users/test/myproject","model_provider":"openai","cli_version":"0.95.0"}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec != nil {
		t.Errorf("session_meta should not produce a record (it updates state), got %+v", rec)
	}
	if state.cwd != "/Users/test/myproject" {
		t.Errorf("cwd = %q, want /Users/test/myproject", state.cwd)
	}
}

func TestParseNewFormat_TurnContext(t *testing.T) {
	line := []byte(`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/Users/test/myproject","model":"gpt-5","effort":"medium"}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec != nil {
		t.Errorf("turn_context should not produce a record, got %+v", rec)
	}
	if state.model != "gpt-5" {
		t.Errorf("model = %q, want gpt-5", state.model)
	}
	if state.cwd != "/Users/test/myproject" {
		t.Errorf("cwd = %q, want /Users/test/myproject", state.cwd)
	}
}

func TestParseNewFormat_ResponseItem_UserMessage(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello world"}]}}`)
	state := &scanState{sessionID: "test-uuid", cwd: "/myproject", model: "gpt-5"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "user" {
		t.Errorf("RecordType = %q, want user", rec.RecordType)
	}
	if rec.Content != "hello world" {
		t.Errorf("Content = %q, want hello world", rec.Content)
	}
	if rec.CWD != "/myproject" {
		t.Errorf("CWD = %q, want /myproject", rec.CWD)
	}
	if rec.Model != "gpt-5" {
		t.Errorf("Model = %q, want gpt-5", rec.Model)
	}
	if rec.SessionID != "test-uuid" {
		t.Errorf("SessionID = %q, want test-uuid", rec.SessionID)
	}
	want := time.Date(2026, 4, 23, 11, 30, 12, 0, time.UTC)
	if !rec.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", rec.Timestamp, want)
	}
}

func TestParseNewFormat_ResponseItem_AssistantMessage(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}}`)
	state := &scanState{sessionID: "test-uuid", cwd: "/myproject", model: "gpt-5"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "assistant" {
		t.Errorf("RecordType = %q, want assistant", rec.RecordType)
	}
	if rec.Content != "world" {
		t.Errorf("Content = %q, want world", rec.Content)
	}
}

func TestParseNewFormat_ResponseItem_FunctionCall(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:14.000Z","payload":{"type":"function_call","call_id":"c1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "tool_call" {
		t.Errorf("RecordType = %q, want tool_call", rec.RecordType)
	}
	if rec.ToolName != "bash" {
		t.Errorf("ToolName = %q, want bash", rec.ToolName)
	}
}

func TestParseNewFormat_ResponseItem_CustomToolCall(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:15.000Z","payload":{"type":"custom_tool_call","id":"rs_1","status":"completed","call_id":"call_8SV","name":"apply_patch","input":"*** Begin Patch\n"}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "tool_call" {
		t.Errorf("RecordType = %q, want tool_call", rec.RecordType)
	}
	if rec.ToolName != "apply_patch" {
		t.Errorf("ToolName = %q, want apply_patch", rec.ToolName)
	}
	if rec.ToolArgs != "*** Begin Patch\n" {
		t.Errorf("ToolArgs = %q, want *** Begin Patch\\n", rec.ToolArgs)
	}
}

func TestParseNewFormat_ResponseItem_CustomToolCallOutput(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:16.000Z","payload":{"type":"custom_tool_call_output","call_id":"call_8SV","output":"apply_patch verification failed: ..."}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "tool_output" {
		t.Errorf("RecordType = %q, want tool_output", rec.RecordType)
	}
	if rec.ToolOutput != "apply_patch verification failed: ..." {
		t.Errorf("ToolOutput = %q, want apply_patch verification failed: ...", rec.ToolOutput)
	}
}

func TestParseNewFormat_ResponseItem_CustomToolCallOutput_ArrayOutput(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:16.000Z","payload":{"type":"custom_tool_call_output","call_id":"call_8SV","output":[{"type":"input_text","text":"Script completed\nWall time 0.2 seconds\nOutput:\n"},{"type":"input_text","text":"done"}]}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "tool_output" {
		t.Errorf("RecordType = %q, want tool_output", rec.RecordType)
	}
	want := "Script completed\nWall time 0.2 seconds\nOutput:\n\ndone"
	if rec.ToolOutput != want {
		t.Errorf("ToolOutput = %q, want %q", rec.ToolOutput, want)
	}
}

func TestParseNewFormat_ResponseItem_FunctionCallOutput_StringOutput(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:16.000Z","payload":{"type":"function_call_output","call_id":"call_8SV","output":"file.txt"}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "tool_output" {
		t.Errorf("RecordType = %q, want tool_output", rec.RecordType)
	}
	if rec.ToolOutput != "file.txt" {
		t.Errorf("ToolOutput = %q, want file.txt", rec.ToolOutput)
	}
}

func TestParseNewFormat_ResponseItem_AgentMessage(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:17.000Z","payload":{"type":"agent_message","id":"msg_1","author":"/root","recipient":"/root/security_review","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: /root/security_review\nSender: /root\nPayload:\n"},{"type":"encrypted_content","encrypted_content":"gAAAA..."}]}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "agent_task" {
		t.Errorf("RecordType = %q, want agent_task", rec.RecordType)
	}
	if !strings.Contains(rec.Content, "Message Type: NEW_TASK") {
		t.Errorf("Content = %q, want to contain Message Type: NEW_TASK", rec.Content)
	}
}

func TestParseNewFormat_ResponseItem_UnknownType_Nil(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:18.000Z","payload":{"type":"web_search_call","id":"ws_1","status":"completed"}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec != nil {
		t.Errorf("unknown response_item type should return nil, got %+v", rec)
	}
}

func TestParseNewFormat_EventMsg_Skip(t *testing.T) {
	line := []byte(`{"type":"event_msg","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/myproject","command":"ls","action":"run"}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec != nil {
		t.Errorf("event_msg should be skipped, got %+v", rec)
	}
}

func TestParseNewFormat_EventMsgTokenCount(t *testing.T) {
	line := []byte(`{"timestamp":"2026-05-15T09:26:02.304Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":16326,"cached_input_tokens":6528,"output_tokens":256,"reasoning_output_tokens":169,"total_tokens":16582}}}}`)
	state := &scanState{sessionID: "test-uuid", cwd: "/myproject", model: "gpt-5.5"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected token usage record, got nil")
	}
	if rec.RecordType != "usage" {
		t.Fatalf("RecordType = %q, want usage", rec.RecordType)
	}
	if rec.InputTokens == nil || *rec.InputTokens != 16326 {
		t.Fatalf("InputTokens = %v, want 16326", rec.InputTokens)
	}
	if rec.CacheReadTokens == nil || *rec.CacheReadTokens != 6528 {
		t.Fatalf("CacheReadTokens = %v, want 6528", rec.CacheReadTokens)
	}
	if rec.OutputTokens == nil || *rec.OutputTokens != 256 {
		t.Fatalf("OutputTokens = %v, want 256", rec.OutputTokens)
	}
	if rec.Model != "gpt-5.5" || rec.CWD != "/myproject" {
		t.Fatalf("context = %q/%q, want gpt-5.5//myproject", rec.Model, rec.CWD)
	}
}

func TestParseNewFormat_EventMsgTokenCountSkipsDuplicateTotal(t *testing.T) {
	first := []byte(`{"timestamp":"2026-05-15T09:26:02.304Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`)
	duplicate := []byte(`{"timestamp":"2026-05-15T09:26:46.078Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`)
	next := []byte(`{"timestamp":"2026-05-15T09:29:59.139Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":175,"cached_input_tokens":70,"output_tokens":25},"last_token_usage":{"input_tokens":75,"cached_input_tokens":30,"output_tokens":5}}}}`)
	state := &scanState{sessionID: "test-uuid", cwd: "/myproject", model: "gpt-5.5"}

	if rec := parseNewFormatLine(first, state); rec == nil {
		t.Fatal("expected first token usage record")
	}
	if rec := parseNewFormatLine(duplicate, state); rec != nil {
		t.Fatalf("duplicate total should be skipped, got %+v", rec)
	}
	rec := parseNewFormatLine(next, state)
	if rec == nil {
		t.Fatal("expected next token usage record")
	}
	if rec.InputTokens == nil || *rec.InputTokens != 75 {
		t.Fatalf("InputTokens = %v, want 75", rec.InputTokens)
	}
	if rec.CacheReadTokens == nil || *rec.CacheReadTokens != 30 {
		t.Fatalf("CacheReadTokens = %v, want 30", rec.CacheReadTokens)
	}
	if rec.OutputTokens == nil || *rec.OutputTokens != 5 {
		t.Fatalf("OutputTokens = %v, want 5", rec.OutputTokens)
	}
}

func TestParseOldFormat_Message_User(t *testing.T) {
	line := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}`)
	state := &scanState{sessionID: "old-uuid"}

	rec := parseOldFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "user" {
		t.Errorf("RecordType = %q, want user", rec.RecordType)
	}
	if rec.Content != "hello" {
		t.Errorf("Content = %q, want hello", rec.Content)
	}
}

func TestParseOldFormat_Message_Assistant(t *testing.T) {
	line := []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}]}`)
	state := &scanState{sessionID: "old-uuid"}

	rec := parseOldFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "assistant" {
		t.Errorf("RecordType = %q, want assistant", rec.RecordType)
	}
}

// Codex injects skill, permission and environment instructions as role=developer
// messages. Storing them as user turns counted them as typed requests.
func TestParseNewFormat_ResponseItem_DeveloperMessage(t *testing.T) {
	line := []byte(`{"type":"response_item","timestamp":"2026-04-23T11:30:11.000Z","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<skills_instructions>x</skills_instructions>"}]}}`)
	state := &scanState{sessionID: "test-uuid"}

	rec := parseNewFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "developer" {
		t.Errorf("RecordType = %q, want developer", rec.RecordType)
	}
}

func TestParseOldFormat_Message_Developer(t *testing.T) {
	line := []byte(`{"type":"message","role":"developer","content":[{"type":"input_text","text":"instructions"}]}`)
	state := &scanState{sessionID: "old-uuid"}

	rec := parseOldFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "developer" {
		t.Errorf("RecordType = %q, want developer", rec.RecordType)
	}
}

func TestParseOldFormat_FunctionCall(t *testing.T) {
	line := []byte(`{"type":"function_call","call_id":"c1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}`)
	state := &scanState{sessionID: "old-uuid"}

	rec := parseOldFormatLine(line, state)

	if rec == nil {
		t.Fatal("expected record, got nil")
	}
	if rec.RecordType != "tool_call" {
		t.Errorf("RecordType = %q, want tool_call", rec.RecordType)
	}
	if rec.ToolName != "bash" {
		t.Errorf("ToolName = %q, want bash", rec.ToolName)
	}
}

func TestParseOldFormat_State_Skip(t *testing.T) {
	line := []byte(`{"record_type":"state"}`)
	state := &scanState{sessionID: "old-uuid"}

	rec := parseOldFormatLine(line, state)

	if rec != nil {
		t.Errorf("state record should be skipped, got %+v", rec)
	}
}

func TestParseOldFormat_Init_Skip(t *testing.T) {
	line := []byte(`{"id":"abc123","timestamp":"2025-06-20T13:30:47.000Z"}`)
	state := &scanState{sessionID: "old-uuid"}

	rec := parseOldFormatLine(line, state)

	if rec != nil {
		t.Errorf("init record should be skipped, got %+v", rec)
	}
}

func TestExtractTextContent_Array(t *testing.T) {
	raw := []byte(`[{"type":"input_text","text":"hello world"}]`)
	got := extractTextContent(raw)
	if got != "hello world" {
		t.Errorf("extractTextContent = %q, want hello world", got)
	}
}

func TestExtractTextContent_MultiItem(t *testing.T) {
	raw := []byte(`[{"type":"input_text","text":"first"},{"type":"input_text","text":"second"}]`)
	got := extractTextContent(raw)
	if got != "first\nsecond" {
		t.Errorf("extractTextContent = %q, want first\\nsecond", got)
	}
}

func TestExtractTextContent_String(t *testing.T) {
	raw := []byte(`"plain string content"`)
	got := extractTextContent(raw)
	if got != "plain string content" {
		t.Errorf("extractTextContent = %q, want plain string content", got)
	}
}

func TestExtractTextContent_Nil(t *testing.T) {
	got := extractTextContent(nil)
	if got != "" {
		t.Errorf("extractTextContent(nil) = %q, want empty", got)
	}
}

func TestParseRecords_ToolCallID(t *testing.T) {
	cases := []struct {
		name, line string
		parse      func([]byte, *scanState) *Record
		wantName   string
	}{
		{"new function_call", `{"type":"response_item","timestamp":"2026-04-23T11:30:14.000Z","payload":{"type":"function_call","call_id":"c1","name":"bash","arguments":"{}"}}`, parseNewFormatLine, "bash"},
		{"new function_call_output", `{"type":"response_item","timestamp":"2026-04-23T11:30:14.000Z","payload":{"type":"function_call_output","call_id":"c1","output":"ok"}}`, parseNewFormatLine, ""},
		{"new custom_tool_call", `{"type":"response_item","timestamp":"2026-04-23T11:30:14.000Z","payload":{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"x"}}`, parseNewFormatLine, "apply_patch"},
		{"new custom_tool_call_output", `{"type":"response_item","timestamp":"2026-04-23T11:30:14.000Z","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}}`, parseNewFormatLine, ""},
		{"old function_call", `{"type":"function_call","call_id":"c1","name":"bash","arguments":"{}"}`, parseOldFormatLine, "bash"},
		{"old function_call_output", `{"type":"function_call_output","call_id":"c1","output":"ok"}`, parseOldFormatLine, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.parse([]byte(tc.line), &scanState{sessionID: "s"})
			if rec == nil {
				t.Fatal("expected record, got nil")
			}
			if rec.CallID != "c1" || rec.ToolName != tc.wantName {
				t.Fatalf("CallID = %q, ToolName = %q; want c1, %q", rec.CallID, rec.ToolName, tc.wantName)
			}
		})
	}
}
