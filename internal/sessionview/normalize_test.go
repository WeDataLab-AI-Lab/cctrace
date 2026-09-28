package sessionview

import (
	"encoding/json"
	"reflect"
	"regexp"
	"testing"
	"time"

	"cctrace/internal/store"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		rec  store.SessionRecord
		want View
	}{
		// --- claude ---
		{
			name: "claude assistant text and tool_use",
			rec: store.SessionRecord{RecordType: "assistant", Raw: raw(`{"type":"assistant","message":{"role":"assistant","content":[
				{"type":"thinking","thinking":"hmm"},
				{"type":"text","text":"Let me look."},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls <dir>","z":1,"a":2}},
				{"type":"tool_use","id":"toolu_2","name":"Raw","input":"as-is"}]}}`)},
			want: View{Kind: KindMessage, Role: "assistant", Text: "Let me look.", ToolCalls: []ToolCall{
				{ID: "toolu_1", Name: "Bash", Input: "{\n  \"command\": \"ls <dir>\",\n  \"z\": 1,\n  \"a\": 2\n}"},
				{ID: "toolu_2", Name: "Raw", Input: "as-is"},
			}},
		},
		{
			name: "claude assistant thinking only is hidden",
			rec:  store.SessionRecord{RecordType: "assistant", Raw: raw(`{"message":{"content":[{"type":"thinking","thinking":"x"}]}}`)},
			want: View{Kind: KindHidden},
		},
		{
			name: "claude user string content",
			rec:  store.SessionRecord{Agent: "claude", RecordType: "user", PromptSource: "typed", Raw: raw(`{"message":{"role":"user","content":"fix the bug"}}`)},
			want: View{Kind: KindMessage, Role: "user", Text: "fix the bug"},
		},
		{
			name: "claude user tool_result blocks only",
			rec: store.SessionRecord{RecordType: "user", Raw: raw(`{"message":{"content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"out","is_error":true},
				{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"a"},{"type":"image"},{"type":"text","text":"b"}]},
				{"type":"tool_result","tool_use_id":"toolu_3","content":{"k":"v"}},
				{"type":"tool_result","content":"no id is unpairable"}]}}`)},
			want: View{Kind: KindToolResult, ToolResults: []ToolResult{
				{ID: "toolu_1", Output: "out", IsError: outcome(true)},
				{ID: "toolu_2", Output: "ab", IsError: outcome(false)},
				{ID: "toolu_3", Output: "{\n  \"k\": \"v\"\n}", IsError: outcome(false)},
			}},
		},
		{
			name: "claude user text with tool_result keeps both",
			rec: store.SessionRecord{RecordType: "user", Raw: raw(`{"message":{"content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":"out"},{"type":"text","text":"and also"}]}}`)},
			want: View{Kind: KindMessage, Role: "user", Text: "and also", ToolResults: []ToolResult{{ID: "toolu_1", Output: "out", IsError: outcome(false)}}},
		},
		{
			name: "claude compact summary",
			rec:  store.SessionRecord{RecordType: "user", IsCompactSummary: true, Raw: raw(`{"message":{"content":"This session is being continued"}}`)},
			want: View{Kind: KindMessage, Role: "user", Text: "This session is being continued", CompactSummary: true},
		},
		{
			name: "claude meta scaffolding is hidden",
			rec:  store.SessionRecord{RecordType: "user", IsMeta: true, Raw: raw(`{"message":{"content":"<local-command-caveat>Caveat</local-command-caveat>"}}`)},
			want: View{Kind: KindHidden},
		},
		{
			name: "claude slash command becomes chip",
			rec:  store.SessionRecord{RecordType: "user", Raw: raw(`{"message":{"content":"<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args> opus </command-args>"}}`)},
			want: View{Kind: KindMessage, Role: "user", Command: "/model", CommandArgs: "opus"},
		},
		{
			name: "claude local command stdout is hidden",
			rec:  store.SessionRecord{RecordType: "user", Raw: raw(`{"message":{"content":"  <local-command-stdout>Set model</local-command-stdout>"}}`)},
			want: View{Kind: KindHidden},
		},
		{
			name: "claude command tags ignored for genuine prompt",
			rec:  store.SessionRecord{RecordType: "user", PromptSource: "typed", Raw: raw(`{"message":{"content":"explain <command-name>x</command-name>"}}`)},
			want: View{Kind: KindMessage, Role: "user", Text: "explain <command-name>x</command-name>"},
		},
		{
			name: "claude sdk harness keeps request and drops replay and prose results",
			rec:  store.SessionRecord{RecordType: "user", PromptSource: "sdk", Raw: raw(`{"message":{"content":"<conversation_history>old</conversation_history>\nThe above is the conversation history so far. Please continue the transcript.\nRun the tests\nTool result (Bash, id=toolu_01A):\n\nok"}}`)},
			want: View{Kind: KindMessage, Role: "user", Text: "Run the tests", ToolResults: []ToolResult{{ID: "toolu_01A", Output: "ok", Inferred: true}}},
		},
		{
			name: "claude sdk prose tool results only",
			rec:  store.SessionRecord{RecordType: "user", PromptSource: "sdk", Raw: raw(`{"message":{"content":[{"type":"text","text":"Tool result (Bash, id=toolu_01A):\n\nline1\n\nTool result (Read, id=toolu_02B): body\n"}]}}`)},
			want: View{Kind: KindToolResult, ToolResults: []ToolResult{{ID: "toolu_01A", Output: "line1", Inferred: true}, {ID: "toolu_02B", Output: "body", Inferred: true}}},
		},
		{
			name: "claude sdk genuine block and prose result for the same id are both kept",
			rec: store.SessionRecord{RecordType: "user", PromptSource: "sdk", Raw: raw(`{"message":{"content":[
				{"type":"tool_result","tool_use_id":"toolu_01A","content":"real"},
				{"type":"text","text":"Tool result (Bash, id=toolu_01A):\nprose"}]}}`)},
			want: View{Kind: KindToolResult, ToolResults: []ToolResult{{ID: "toolu_01A", Output: "real", IsError: outcome(false)}, {ID: "toolu_01A", Output: "prose", Inferred: true}}},
		},
		{
			name: "claude sdk skill tag becomes chip",
			rec:  store.SessionRecord{RecordType: "user", PromptSource: "sdk", Raw: raw(`{"message":{"content":"<skill name=\"deploy\">\nsteps\n</skill>"}}`)},
			want: View{Kind: KindMessage, Role: "user", Command: "deploy"},
		},
		{
			name: "claude sdk replay only is hidden",
			rec:  store.SessionRecord{RecordType: "user", PromptSource: "sdk", Raw: raw(`{"message":{"content":"<conversation_history>x</conversation_history>\nThe above is the conversation history so far. continue the transcript.\n"}}`)},
			want: View{Kind: KindHidden},
		},
		{
			name: "claude non-object content is hidden",
			rec:  store.SessionRecord{RecordType: "user", Raw: raw(`{"message":{"content":42}}`)},
			want: View{Kind: KindHidden},
		},
		{
			name: "claude system record is hidden",
			rec:  store.SessionRecord{RecordType: "system", Raw: raw(`{"type":"system","content":"hook ran"}`)},
			want: View{Kind: KindHidden},
		},
		{
			name: "malformed raw is hidden",
			rec:  store.SessionRecord{RecordType: "user", Raw: raw(`{not json`)},
			want: View{Kind: KindHidden},
		},

		// --- codex ---
		{
			name: "codex user message",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "user", Raw: raw(`{"timestamp":"t","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"a"},{"type":"input_image","image_url":"x"},{"type":"input_text","text":"b"}]}}`)},
			want: View{Kind: KindMessage, Role: "user", Text: "a\nb"},
		},
		{
			name: "codex assistant message",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "assistant", Raw: raw(`{"type":"response_item","payload":{"type":"message","role":"assistant","phase":"final","content":[{"type":"output_text","text":"done"}]}}`)},
			want: View{Kind: KindMessage, Role: "assistant", Text: "done"},
		},
		{
			name: "codex old flat format message",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "assistant", Raw: raw(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"old"}]}`)},
			want: View{Kind: KindMessage, Role: "assistant", Text: "old"},
		},
		{
			name: "codex agent task",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "agent_task", Raw: raw(`{"type":"response_item","payload":{"type":"agent_message","author":"/root","content":[{"type":"input_text","text":"do X"},{"type":"encrypted_content","encrypted_content":"zz"}]}}`)},
			want: View{Kind: KindMessage, Role: "user", Text: "do X", AgentTask: true},
		},
		{
			name: "codex function_call",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "tool_call", Raw: raw(`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","namespace":"x","arguments":"{\"cmd\":\"ls\"}","call_id":"call_1","id":"fc_1"}}`)},
			want: View{Kind: KindToolCall, Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "exec_command", Input: `{"cmd":"ls"}`}}},
		},
		{
			// reenrich can mis-assign same-millisecond parallel calls, so the raw line wins.
			name: "codex custom_tool_call prefers raw over columns",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "tool_call", ToolName: "column_name", ToolCallID: "call_B", Raw: raw(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch","call_id":"call_A","status":"completed"}}`)},
			want: View{Kind: KindToolCall, Role: "assistant", ToolCalls: []ToolCall{{ID: "call_A", Name: "apply_patch", Input: "*** Begin Patch"}}},
		},
		{
			name: "codex tool call falls back to columns when raw lacks them",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "tool_call", ToolName: "exec", ToolCallID: "call_B", Raw: raw(`{"type":"response_item","payload":{"type":"function_call","arguments":"{}"}}`)},
			want: View{Kind: KindToolCall, Role: "assistant", ToolCalls: []ToolCall{{ID: "call_B", Name: "exec", Input: "{}"}}},
		},
		{
			// Codex JSONL records no tool outcome, so IsError stays nil (not observed), never false.
			name: "codex tool output prefers raw call_id",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "tool_output", ToolCallID: "call_B", Raw: raw(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call_A","output":"x"}}`)},
			want: View{Kind: KindToolResult, ToolResults: []ToolResult{{ID: "call_A", Output: "x"}}},
		},
		{
			name: "codex function_call_output string stays as-is",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "tool_output", Raw: raw(`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"{\"output\":\"hi\",\"metadata\":{\"exit_code\":0}}"}}`)},
			want: View{Kind: KindToolResult, ToolResults: []ToolResult{{ID: "call_1", Output: `{"output":"hi","metadata":{"exit_code":0}}`}}},
		},
		{
			name: "codex custom_tool_call_output array",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "tool_output", Raw: raw(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call_2","output":[{"type":"input_text","text":"Exit code: 0"},{"type":"input_text","text":"Output:"}]}}`)},
			want: View{Kind: KindToolResult, ToolResults: []ToolResult{{ID: "call_2", Output: "Exit code: 0\nOutput:"}}},
		},
		{
			name: "codex reasoning with summary",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "reasoning", Raw: raw(`{"type":"response_item","payload":{"type":"reasoning","content":null,"summary":[{"type":"summary_text","text":"**Plan**"}],"encrypted_content":"gAAA"}}`)},
			want: View{Kind: KindReasoning, Role: "assistant", Text: "**Plan**"},
		},
		{
			name: "codex encrypted reasoning has empty text",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "reasoning", Raw: raw(`{"type":"response_item","payload":{"type":"reasoning","summary":[],"encrypted_content":"gAAA"}}`)},
			want: View{Kind: KindReasoning, Role: "assistant"},
		},
		{
			name: "codex usage is hidden",
			rec:  store.SessionRecord{Agent: "codex", RecordType: "usage", Raw: raw(`{"type":"event_msg","payload":{"type":"token_count","info":{}}}`)},
			want: View{Kind: KindHidden},
		},

		// --- omo / gjc share the pi message layout ---
		{
			name: "omo user",
			rec:  store.SessionRecord{Agent: "omo", RecordType: "user", Raw: raw(`{"type":"message","id":"a","parentId":"b","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`)},
			want: View{Kind: KindMessage, Role: "user", Text: "hello"},
		},
		{
			name: "omo assistant with toolCall",
			rec: store.SessionRecord{Agent: "omo", RecordType: "assistant", Raw: raw(`{"type":"message","message":{"role":"assistant","content":[
				{"type":"thinking","thinking":"x"},{"type":"text","text":"On it"},
				{"type":"toolCall","id":"tc_1","name":"bash","arguments":{"command":"ls"}}]}}`)},
			want: View{Kind: KindMessage, Role: "assistant", Text: "On it", ToolCalls: []ToolCall{{ID: "tc_1", Name: "bash", Input: "{\n  \"command\": \"ls\"\n}"}}},
		},
		{
			name: "omo assistant thinking only is hidden",
			rec:  store.SessionRecord{Agent: "omo", RecordType: "assistant", Raw: raw(`{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"x"}]}}`)},
			want: View{Kind: KindHidden},
		},
		{
			name: "omo tool result",
			rec:  store.SessionRecord{Agent: "omo", RecordType: "tool_result", Raw: raw(`{"type":"message","message":{"role":"toolResult","toolCallId":"tc_1","toolName":"bash","isError":true,"content":[{"type":"text","text":"boom"},{"type":"image","data":"xx"}]}}`)},
			want: View{Kind: KindToolResult, ToolResults: []ToolResult{{ID: "tc_1", Output: "boom", IsError: outcome(true)}}},
		},
		{
			name: "gjc assistant",
			rec:  store.SessionRecord{Agent: "gjc", RecordType: "assistant", Raw: raw(`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"toolCall","id":"tc_9","name":"read","arguments":{}}]}}`)},
			want: View{Kind: KindMessage, Role: "assistant", Text: "a\nb", ToolCalls: []ToolCall{{ID: "tc_9", Name: "read", Input: "{}"}}},
		},
		{
			name: "gjc tool_call sibling is hidden to avoid showing the call twice",
			rec:  store.SessionRecord{Agent: "gjc", RecordType: "tool_call", ToolName: "read", ToolCallID: "tc_9", Raw: raw(`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","id":"tc_9","name":"read","arguments":{}}]}}`)},
			want: View{Kind: KindHidden},
		},
		{
			name: "gjc tool result prefers raw id",
			rec:  store.SessionRecord{Agent: "gjc", RecordType: "tool_result", ToolCallID: "column", Raw: raw(`{"type":"message","message":{"role":"toolResult","toolCallId":"tc_9","content":[{"type":"text","text":"ok"}]}}`)},
			want: View{Kind: KindToolResult, ToolResults: []ToolResult{{ID: "tc_9", Output: "ok", IsError: outcome(false)}}},
		},
		{
			name: "gjc tool result falls back to column id",
			rec:  store.SessionRecord{Agent: "gjc", RecordType: "tool_result", ToolCallID: "column", Raw: raw(`{"type":"message","message":{"role":"toolResult","content":[{"type":"text","text":"ok"}]}}`)},
			want: View{Kind: KindToolResult, ToolResults: []ToolResult{{ID: "column", Output: "ok", IsError: outcome(false)}}},
		},

		// --- unknown agent ---
		{
			name: "unknown agent with claude shape",
			rec:  store.SessionRecord{Agent: "future", RecordType: "user", Raw: raw(`{"message":{"content":"hi"}}`)},
			want: View{Kind: KindMessage, Role: "user", Text: "hi"},
		},
		{
			name: "unknown agent with codex payload",
			rec:  store.SessionRecord{Agent: "future", RecordType: "assistant", Raw: raw(`{"payload":{"type":"message","content":[{"type":"output_text","text":"yo"}]}}`)},
			want: View{Kind: KindMessage, Role: "assistant", Text: "yo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(&tt.rec)
			if got.AnchorID == "" {
				t.Fatalf("AnchorID is empty")
			}
			if got.DedupeKey == "" {
				t.Fatalf("DedupeKey is empty")
			}
			if s := Summarize(&tt.rec); s.Kind != got.Kind || s.ToolCallCount != len(got.ToolCalls) {
				t.Errorf("Summarize = %+v, Normalize kind = %q with %d tool calls", s, got.Kind, len(got.ToolCalls))
			}
			got.AnchorID, got.DedupeKey = "", ""
			if !reflect.DeepEqual(got, tt.want) {
				gj, _ := json.MarshalIndent(got, "", "  ")
				wj, _ := json.MarshalIndent(tt.want, "", "  ")
				t.Errorf("got\n%s\nwant\n%s", gj, wj)
			}
		})
	}
}

func TestNormalizeAnchorID(t *testing.T) {
	ts := time.Date(2026, 9, 1, 8, 0, 0, 123456789, time.FixedZone("KST", 9*3600))
	base := store.SessionRecord{SessionID: "s", SourceFile: "f.jsonl", Ts: ts, RecordType: "tool_call", ToolCallID: "c", Raw: raw(`{"a":1}`)}

	if got := Normalize(&store.SessionRecord{UUID: "u-1", Raw: raw(`{}`)}).AnchorID; got != "u-1" {
		t.Errorf("uuid anchor = %q, want u-1", got)
	}

	a := Normalize(&base).AnchorID
	if !regexp.MustCompile(`^h:[0-9a-f]{16}$`).MatchString(a) {
		t.Fatalf("hash anchor = %q, want h:<16 hex>", a)
	}
	same := base
	same.Ts = ts.UTC()
	if got := Normalize(&same).AnchorID; got != a {
		t.Errorf("anchor depends on timezone: %q vs %q", got, a)
	}
	for name, mutate := range map[string]func(*store.SessionRecord){
		"session_id":  func(r *store.SessionRecord) { r.SessionID = "s2" },
		"ts":          func(r *store.SessionRecord) { r.Ts = ts.Add(time.Nanosecond) },
		"record_type": func(r *store.SessionRecord) { r.RecordType = "tool_output" },
		"raw":         func(r *store.SessionRecord) { r.Raw = raw(`{"a":2}`) },
	} {
		r := base
		mutate(&r)
		if Normalize(&r).AnchorID == a {
			t.Errorf("anchor ignores %s", name)
		}
	}
	// reenrich fills these later; the anchor must not move when it does.
	for name, mutate := range map[string]func(*store.SessionRecord){
		"source_file":  func(r *store.SessionRecord) { r.SourceFile = "" },
		"tool_call_id": func(r *store.SessionRecord) { r.ToolCallID = "" },
	} {
		r := base
		mutate(&r)
		if got := Normalize(&r).AnchorID; got != a {
			t.Errorf("anchor depends on %s: %q vs %q", name, got, a)
		}
	}
}

func TestNormalizeDedupeKey(t *testing.T) {
	ts := time.Date(2026, 9, 1, 8, 0, 0, 5, time.UTC)
	body := raw(`{"type":"user","message":{"role":"user","content":"hi"}}`)
	legacy := store.SessionRecord{Ts: ts, RecordType: "user", Raw: body}
	enriched := store.SessionRecord{
		Ts: ts.In(time.FixedZone("KST", 9*3600)), RecordType: "user", SessionID: "s", SourceFile: "f.jsonl",
		UUID: "u-1", PromptSource: "sdk", IsMeta: true, IsCompactSummary: true, ToolName: "n", ToolCallID: "c",
		// Enrichment may add top-level fields and the body may be re-spaced; neither is the line's identity.
		Raw: raw(`{"type":"user","uuid":"u-1","promptSource":"sdk","message": { "role": "user", "content": "hi" }}`),
	}
	d := Normalize(&legacy).DedupeKey
	if !regexp.MustCompile(`^d:[0-9a-f]{16}$`).MatchString(d) {
		t.Fatalf("dedupe key = %q, want d:<16 hex>", d)
	}
	if got := Normalize(&enriched).DedupeKey; got != d {
		t.Errorf("legacy and enriched copies differ: %q vs %q", d, got)
	}

	for name, r := range map[string]store.SessionRecord{
		"message body": {Ts: ts, RecordType: "user", Raw: raw(`{"message":{"role":"user","content":"bye"}}`)},
		"ts":           {Ts: ts.Add(time.Nanosecond), RecordType: "user", Raw: body},
		"record_type":  {Ts: ts, RecordType: "assistant", Raw: body},
		"payload":      {Ts: ts, RecordType: "user", Raw: raw(`{"payload":{"type":"message"}}`)},
	} {
		if Normalize(&r).DedupeKey == d {
			t.Errorf("dedupe key ignores %s", name)
		}
	}
	p1 := store.SessionRecord{Ts: ts, RecordType: "tool_call", Raw: raw(`{"timestamp":"a","payload":{"type":"function_call","call_id":"x"}}`)}
	p2 := store.SessionRecord{Ts: ts, RecordType: "tool_call", Raw: raw(`{"timestamp":"b","payload": {"type":"function_call", "call_id":"x"}}`)}
	if Normalize(&p1).DedupeKey != Normalize(&p2).DedupeKey {
		t.Errorf("payload records with the same payload differ")
	}
	w1 := store.SessionRecord{Ts: ts, RecordType: "x", Raw: raw(`{"a":1}`)}
	w2 := store.SessionRecord{Ts: ts, RecordType: "x", Raw: raw(`{"a":2}`)}
	if Normalize(&w1).DedupeKey == Normalize(&w2).DedupeKey {
		t.Errorf("whole-raw fallback ignores the body")
	}
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }
