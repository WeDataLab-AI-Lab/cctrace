package aireport

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

func toolByName(t *testing.T, tools []airuntime.Tool, name string) airuntime.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q missing", name)
	return airuntime.Tool{}
}

func callTool(t *testing.T, tool airuntime.Tool, args string) airuntime.Invocation {
	t.Helper()
	return airuntime.InvokeTool(context.Background(), tool, json.RawMessage(args), time.Second)
}

func TestBuildToolsNamesAndSchemas(t *testing.T) {
	tools := BuildTools(NewMemStore(), Scope{}, testWeek(t))
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s schema: %v", tool.Name, err)
		}
	}
	if strings.Join(names, ",") != "query_segments,read_segment,compare_week" {
		t.Fatalf("tools = %v", names)
	}
}

func TestQuerySegmentsUsesFixedScopeAndParsedFilter(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", store.AISegment{ID: 1, StartTs: wk.Since.Add(time.Hour), ToolFailCount: 3, Agent: "claude"})
	st.AddSegment("me", store.AISegment{ID: 2, StartTs: wk.Since.Add(-time.Hour), ToolFailCount: 9, Agent: "claude"})
	st.AddSegment("other", store.AISegment{ID: 3, StartTs: wk.Since.Add(time.Hour), ToolFailCount: 9, Agent: "claude"})
	tool := toolByName(t, BuildTools(st, Scope{DashboardUserID: 1, UserID: "me"}, wk), "query_segments")

	inv := callTool(t, tool, `{"min_tool_fail":1,"order_by":"tool_fail_count","limit":20}`)
	if !inv.Success {
		t.Fatalf("failed: %s", inv.Output.Text)
	}
	var out struct {
		Segments []map[string]any `json:"segments"`
	}
	if err := json.Unmarshal([]byte(inv.Output.Text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Segments) != 1 || out.Segments[0]["segment_id"] != "1" {
		t.Fatalf("segments = %v", out.Segments)
	}
	if _, ok := out.Segments[0]["input_tokens"]; ok {
		t.Fatal("tokens must not be returned")
	}
	if inv.Output.Rows != 1 {
		t.Fatalf("rows = %d", inv.Output.Rows)
	}
	if inv.Output.ArgsSummary["order_by"] != "tool_fail_count" || inv.Output.ArgsSummary["limit"] != 20 {
		t.Fatalf("args summary = %v", inv.Output.ArgsSummary)
	}
	f := st.LastFilter()
	if f.MinToolFail != 1 || f.OrderBy != store.AISegmentOrderToolFailCount || f.Limit != 20 {
		t.Fatalf("filter = %+v", f)
	}
}

// A Codex segment's calls are counted but its outcomes are not recorded, so its
// tool_fail_count of 0 must travel with the flag that says it is unobserved.
func TestQuerySegmentsCarriesToolOutcomeEvidence(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", store.AISegment{ID: 1, StartTs: wk.Since.Add(time.Hour), Agent: "codex",
		ToolCallCount: 4, ToolEvidence: true})
	tool := toolByName(t, BuildTools(st, Scope{DashboardUserID: 1, UserID: "me"}, wk), "query_segments")

	inv := callTool(t, tool, `{}`)
	if !inv.Success {
		t.Fatalf("failed: %s", inv.Output.Text)
	}
	var out struct {
		Segments []map[string]any `json:"segments"`
	}
	if err := json.Unmarshal([]byte(inv.Output.Text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Segments) != 1 {
		t.Fatalf("segments = %v", out.Segments)
	}
	if got, ok := out.Segments[0]["tool_outcome_evidence"]; !ok || got != false {
		t.Fatalf("tool_outcome_evidence = %v (present %v), want false", got, ok)
	}
}

func TestQuerySegmentsRejectsBadArgs(t *testing.T) {
	tool := toolByName(t, BuildTools(NewMemStore(), Scope{UserID: "me"}, testWeek(t)), "query_segments")
	for _, args := range []string{
		`{"since":"2020-01-01"}`,
		`{"limit":51}`,
		`{"order_by":"input_tokens"}`,
		`[1]`,
	} {
		if inv := callTool(t, tool, args); inv.Success {
			t.Errorf("args %s succeeded", args)
		}
	}
	if inv := callTool(t, tool, ``); !inv.Success {
		t.Errorf("empty args should mean defaults: %s", inv.Output.Text)
	}
}

func TestReadSegmentOwnershipAndTruncation(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", segAt(7, wk))
	st.AddSegment("other", segAt(8, wk))
	long := strings.Repeat("가", 1000) // 3000 bytes
	st.Conversations[7] = []*store.SessionRecord{
		{RecordType: "user", Agent: "claude", Raw: json.RawMessage(`{"message":{"role":"user","content":"테스트 고쳐줘"}}`)},
		{RecordType: "assistant", Agent: "claude", Raw: json.RawMessage(`{"message":{"role":"assistant","content":[{"type":"text","text":"` + long + `"},{"type":"tool_use","name":"Bash","input":{"command":"secret"}}]}}`)},
		{RecordType: "user", Agent: "claude", Raw: json.RawMessage(`{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"stack trace"}]}}`)},
		{RecordType: "tool_call", Agent: "codex", Raw: json.RawMessage(`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}`)},
		{RecordType: "assistant", Agent: "codex", Raw: json.RawMessage(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`)},
	}
	tools := BuildTools(st, Scope{DashboardUserID: 1, UserID: "me"}, wk)
	tool := toolByName(t, tools, "read_segment")

	if inv := callTool(t, tool, `{"segment_id":"8"}`); inv.Success {
		t.Fatal("other user's segment must fail")
	}
	if inv := callTool(t, tool, `{"segment_id":"abc"}`); inv.Success {
		t.Fatal("non-numeric id must fail")
	}

	inv := callTool(t, tool, `{"segment_id":"7"}`)
	if !inv.Success {
		t.Fatal(inv.Output.Text)
	}
	text := inv.Output.Text
	if strings.Contains(text, "secret") || strings.Contains(text, "stack trace") || strings.Contains(text, "ls") {
		t.Fatalf("tool input/output leaked: %s", text)
	}
	var out struct {
		Messages []struct {
			Role    string `json:"role"`
			Text    string `json:"text"`
			Tool    string `json:"tool"`
			IsError *bool  `json:"is_error"`
		} `json:"messages"`
		NextOffset *int `json:"next_offset"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	if out.NextOffset != nil {
		t.Fatalf("next_offset = %d, want none", *out.NextOffset)
	}
	var tools2, errs int
	for _, m := range out.Messages {
		if len(m.Text) > maxMessageBytes+len("…") {
			t.Fatalf("message not truncated: %d bytes", len(m.Text))
		}
		if m.Tool != "" {
			tools2++
		}
		if m.IsError != nil && *m.IsError {
			errs++
		}
	}
	if tools2 != 2 || errs != 1 {
		t.Fatalf("tool entries=%d errors=%d in %s", tools2, errs, text)
	}
	if inv.Output.ArgsSummary["segment_id"] != "7" {
		t.Fatalf("args summary = %v", inv.Output.ArgsSummary)
	}
}

// User-role text also carries command and hook output, compact summaries and
// Codex environment/instruction preambles. The consent text promises no tool
// output, so these never reach the model.
func TestReadSegmentDropsOutputBearingUserText(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", segAt(7, wk))
	user := func(text string) *store.SessionRecord {
		raw, _ := json.Marshal(map[string]any{"message": map[string]any{"role": "user", "content": text}})
		return &store.SessionRecord{RecordType: "user", Agent: "claude", Raw: raw}
	}
	codexUser := func(text string) *store.SessionRecord {
		raw, _ := json.Marshal(map[string]any{"payload": map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}}})
		return &store.SessionRecord{RecordType: "user", Agent: "codex", Raw: raw}
	}
	compact := user("summary of LEAK1")
	compact.IsCompactSummary = true
	st.Conversations[7] = []*store.SessionRecord{
		compact,
		user("<bash-stdout>LEAK2</bash-stdout>"),
		user("  <local-command-stdout>LEAK3</local-command-stdout>"),
		user("<bash-stderr>LEAK4</bash-stderr>"),
		codexUser("<environment_context>LEAK5</environment_context>"),
		codexUser("<user_instructions>LEAK6</user_instructions>"),
		user("진짜 요청"),
	}
	inv := callTool(t, toolByName(t, BuildTools(st, Scope{UserID: "me"}, wk), "read_segment"), `{"segment_id":"7"}`)
	if !inv.Success || strings.Contains(inv.Output.Text, "LEAK") || !strings.Contains(inv.Output.Text, "진짜 요청") || inv.Output.Rows != 1 {
		t.Fatalf("rows=%d text=%s", inv.Output.Rows, inv.Output.Text)
	}
}

// Harness-injected blocks ride along in the same user message as typed text,
// in the same content array or even the same text block. Each is dropped on
// its own; checking only the joined text's prefix let them through.
func TestReadSegmentDropsInjectedBlocksWithinMessage(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", segAt(7, wk))
	blocks := func(texts ...string) *store.SessionRecord {
		content := []map[string]string{}
		for _, text := range texts {
			content = append(content, map[string]string{"type": "text", "text": text})
		}
		raw, _ := json.Marshal(map[string]any{"message": map[string]any{"role": "user", "content": content}})
		return &store.SessionRecord{RecordType: "user", Agent: "claude", Raw: raw}
	}
	codexBlocks := func(texts ...string) *store.SessionRecord {
		content := []map[string]string{}
		for _, text := range texts {
			content = append(content, map[string]string{"type": "input_text", "text": text})
		}
		raw, _ := json.Marshal(map[string]any{"payload": map[string]any{"type": "message", "role": "user", "content": content}})
		return &store.SessionRecord{RecordType: "user", Agent: "codex", Raw: raw}
	}
	tags := []string{
		"system-reminder", "bash-input", "bash-stdout", "bash-stderr", "command-name", "command-message",
		"command-args", "local-command-stdout", "local-command-stderr", "local-command-caveat",
		"environment_context", "user_instructions",
	}
	var recs []*store.SessionRecord
	for i, tag := range tags {
		leak := "<" + tag + ">LEAK" + itoa(i) + "</" + tag + ">"
		recs = append(recs,
			blocks("요청"+itoa(i), leak),
			blocks(leak, "뒤 요청"+itoa(i)),
			codexBlocks("코덱스"+itoa(i), leak),
			// Typed text and an injected block in one text block.
			blocks("같은 블록"+itoa(i)+"\n"+leak),
		)
	}
	st.Conversations[7] = recs
	inv := callTool(t, toolByName(t, BuildTools(st, Scope{UserID: "me"}, wk), "read_segment"), `{"segment_id":"7"}`)
	if !inv.Success {
		t.Fatal(inv.Output.Text)
	}
	for i, tag := range tags {
		if strings.Contains(inv.Output.Text, "LEAK"+itoa(i)+"<") || strings.Contains(inv.Output.Text, "<"+tag+">") {
			t.Errorf("%s block reached the model", tag)
		}
		wants := []string{"요청" + itoa(i), "뒤 요청" + itoa(i), "코덱스" + itoa(i), "같은 블록" + itoa(i)}
		switch tag {
		case "command-name":
			// A Claude record naming a slash command is a command, not typed text, as in the session viewer.
			wants = []string{"코덱스" + itoa(i)}
		case "command-message", "command-args", "local-command-stdout", "local-command-stderr", "local-command-caveat":
			// A Claude record opening with command scaffolding is skipped whole, as in the session viewer.
			wants = []string{"요청" + itoa(i), "코덱스" + itoa(i), "같은 블록" + itoa(i)}
		}
		for _, want := range wants {
			if !strings.Contains(inv.Output.Text, want) {
				t.Errorf("typed text %q dropped with the %s block", want, tag)
			}
		}
	}
}

func TestReadSegmentPagesPastCallLimit(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", segAt(7, wk))
	msg := `{"message":{"role":"user","content":"` + strings.Repeat("a", 1500) + `"}}`
	for i := 0; i < 40; i++ {
		st.Conversations[7] = append(st.Conversations[7], &store.SessionRecord{RecordType: "user", Agent: "claude", Raw: json.RawMessage(msg)})
	}
	tool := toolByName(t, BuildTools(st, Scope{UserID: "me"}, wk), "read_segment")
	inv := callTool(t, tool, `{"segment_id":"7"}`)
	if !inv.Success {
		t.Fatal(inv.Output.Text)
	}
	if len(inv.Output.Text) > maxCallBytes+1024 {
		t.Fatalf("call output %d bytes", len(inv.Output.Text))
	}
	var out struct {
		Messages   []json.RawMessage `json:"messages"`
		NextOffset *int              `json:"next_offset"`
	}
	_ = json.Unmarshal([]byte(inv.Output.Text), &out)
	if out.NextOffset == nil || *out.NextOffset != len(out.Messages) {
		t.Fatalf("next_offset = %v with %d messages", out.NextOffset, len(out.Messages))
	}
	inv2 := callTool(t, tool, `{"segment_id":"7","offset":`+itoa(*out.NextOffset)+`}`)
	if !inv2.Success {
		t.Fatal(inv2.Output.Text)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestCompareWeek(t *testing.T) {
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", segAt(1, wk))
	tool := toolByName(t, BuildTools(st, Scope{UserID: "me"}, wk), "compare_week")

	inv := callTool(t, tool, `{}`)
	if !inv.Success || !strings.Contains(inv.Output.Text, `"comparable":false`) || !strings.Contains(inv.Output.Text, "비교 불가") {
		t.Fatalf("no previous data: %s", inv.Output.Text)
	}

	prev := wk.Previous()
	st.AddSegment("me", store.AISegment{ID: 2, StartTs: prev.Since.Add(time.Hour)})
	inv = callTool(t, tool, `{}`)
	var out struct {
		Comparable bool                   `json:"comparable"`
		Previous   *store.AIWeekAggregate `json:"previous"`
		Current    *store.AIWeekAggregate `json:"current"`
	}
	if err := json.Unmarshal([]byte(inv.Output.Text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Comparable || out.Previous.SegmentCount != 1 || out.Current.SegmentCount != 1 {
		t.Fatalf("compare = %s", inv.Output.Text)
	}
	if inv := callTool(t, tool, `{"week":"2020-W01"}`); inv.Success {
		t.Fatal("unknown field must fail")
	}
}

// tool_call_count counts Codex calls, whose outcomes Codex does not record, so a
// failure rate over it reads low. compare_week carries the denominator the rate
// must use instead: the calls whose outcome was observed.
func TestCompareWeekCarriesOutcomeObservedCalls(t *testing.T) {
	wk := testWeek(t)
	prev := wk.Previous()
	st := NewMemStore()
	st.AddSegment("me", store.AISegment{ID: 1, StartTs: wk.Since.Add(time.Hour), Agent: "codex",
		ToolCallCount: 4, ToolEvidence: true})
	st.AddSegment("me", store.AISegment{ID: 2, StartTs: wk.Since.Add(2 * time.Hour), Agent: "claude",
		ToolCallCount: 6, ToolFailCount: 3, ToolEvidence: true, ToolOutcomeEvidence: true})
	st.AddSegment("me", store.AISegment{ID: 3, StartTs: prev.Since.Add(time.Hour), Agent: "claude",
		ToolCallCount: 2, ToolFailCount: 1, ToolEvidence: true, ToolOutcomeEvidence: true})

	tool := toolByName(t, BuildTools(st, Scope{UserID: "me"}, wk), "compare_week")
	inv := callTool(t, tool, `{}`)
	if !inv.Success {
		t.Fatalf("failed: %s", inv.Output.Text)
	}
	var out struct {
		Current  *store.AIWeekAggregate `json:"current"`
		Previous *store.AIWeekAggregate `json:"previous"`
	}
	if err := json.Unmarshal([]byte(inv.Output.Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.Current.ToolCallCount != 10 || out.Current.ToolOutcomeObservedCount != 6 {
		t.Errorf("current = %d calls / %d observed, want 10/6", out.Current.ToolCallCount, out.Current.ToolOutcomeObservedCount)
	}
	if out.Previous.ToolOutcomeObservedCount != 2 {
		t.Errorf("previous observed = %d, want 2", out.Previous.ToolOutcomeObservedCount)
	}
	if !strings.Contains(tool.Description, "tool_outcome_observed_count") {
		t.Errorf("description does not say which denominator to use: %s", tool.Description)
	}
}

func TestSummarizeArgsKeepsOnlyKnownScalars(t *testing.T) {
	got := SummarizeArgs("query_segments", map[string]any{"limit": 5.0, "order_by": "start_ts", "prompt": "leak", "agent": map[string]any{"x": 1}})
	if len(got) != 2 || got["limit"] != 5.0 || got["order_by"] != "start_ts" {
		t.Fatalf("got %v", got)
	}
	if got := SummarizeArgs("unknown_tool", map[string]any{"a": 1}); len(got) != 0 {
		t.Fatalf("unknown tool args kept: %v", got)
	}
}

// conversationEntries is what read_segment sends per record. These cases pin
// its output for Claude and Codex records as the collectors store them.
func TestConversationEntriesByRuntime(t *testing.T) {
	rec := func(agent, recordType, raw string) *store.SessionRecord {
		return &store.SessionRecord{Agent: agent, RecordType: recordType, Raw: json.RawMessage(raw)}
	}
	meta := rec("claude", "user", `{"message":{"role":"user","content":"meta"}}`)
	meta.IsMeta = true
	compact := rec("claude", "user", `{"message":{"role":"user","content":"summary"}}`)
	compact.IsCompactSummary = true
	cases := []struct {
		name string
		rec  *store.SessionRecord
		want string
	}{
		{"claude user text", rec("claude", "user", `{"message":{"role":"user","content":"  고쳐줘  "}}`),
			`[{"role":"user","text":"고쳐줘"}]`},
		{"claude assistant text and tool_use", rec("claude", "assistant", `{"message":{"role":"assistant","content":[{"type":"text","text":"본다"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`),
			`[{"role":"assistant","text":"본다"},{"role":"assistant","tool":"Bash"}]`},
		{"claude failed tool_result", rec("claude", "user", `{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":"boom"}]}}`),
			`[{"role":"tool_result","is_error":true}]`},
		{"claude ok tool_result", rec("claude", "user", `{"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"ok"}]}]}}`),
			`[{"role":"tool_result","is_error":false}]`},
		{"claude meta dropped", meta, `null`},
		{"claude compact summary dropped", compact, `null`},
		{"codex user message", rec("codex", "user", `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"해줘"}]}}`),
			`[{"role":"user","text":"해줘"}]`},
		{"codex assistant message", rec("codex", "assistant", `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"끝"}]}}`),
			`[{"role":"assistant","text":"끝"}]`},
		{"codex function_call", rec("codex", "tool_call", `{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"}}`),
			`[{"role":"assistant","tool":"shell"}]`},
		{"codex custom_tool_call", rec("codex", "tool_call", `{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin","call_id":"c2"}}`),
			`[{"role":"assistant","tool":"apply_patch"}]`},
		{"codex reasoning dropped", rec("codex", "reasoning", `{"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"생각"}]}}`),
			`null`},
		{"codex developer dropped", rec("codex", "developer", `{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"규칙"}]}}`),
			`null`},
		{"codex agent_task dropped", rec("codex", "agent_task", `{"type":"response_item","payload":{"type":"agent_message","content":[{"type":"input_text","text":"하위 작업"}]}}`),
			`null`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := json.Marshal(conversationEntries(c.rec))
			if string(got) != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}

// omo and gjc store pi-style message lines: toolCall blocks inside the assistant
// message and toolResult messages with isError. gjc also writes each toolCall as
// a sibling tool_call row sharing the assistant line, which must not repeat it.
// Codex tool outputs record no outcome, so is_error is left out: not observed
// is not success, as with tool_outcome_evidence on segments.
func TestConversationEntriesOtherRuntimes(t *testing.T) {
	rec := func(agent, recordType, raw string) *store.SessionRecord {
		return &store.SessionRecord{Agent: agent, RecordType: recordType, Raw: json.RawMessage(raw)}
	}
	for _, agent := range []string{"omo", "gjc"} {
		cases := []struct {
			name string
			rec  *store.SessionRecord
			want string
		}{
			{"user", rec(agent, "user", `{"type":"message","message":{"role":"user","content":[{"type":"text","text":"고쳐줘"}]}}`),
				`[{"role":"user","text":"고쳐줘"}]`},
			{"assistant text and toolCall", rec(agent, "assistant", `{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"본다"},{"type":"toolCall","id":"tc_1","name":"bash","arguments":{"command":"secret"}}]}}`),
				`[{"role":"assistant","text":"본다"},{"role":"assistant","tool":"bash"}]`},
			{"failed toolResult", rec(agent, "tool_result", `{"type":"message","message":{"role":"toolResult","toolCallId":"tc_1","toolName":"bash","isError":true,"content":[{"type":"text","text":"boom"}]}}`),
				`[{"role":"tool_result","is_error":true}]`},
			{"ok toolResult", rec(agent, "tool_result", `{"type":"message","message":{"role":"toolResult","toolCallId":"tc_1","content":[{"type":"text","text":"ok"}]}}`),
				`[{"role":"tool_result","is_error":false}]`},
			{"tool_call sibling row", rec(agent, "tool_call", `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"본다"},{"type":"toolCall","id":"tc_1","name":"bash","arguments":{}}]}}`),
				`null`},
		}
		for _, c := range cases {
			t.Run(agent+" "+c.name, func(t *testing.T) {
				got, _ := json.Marshal(conversationEntries(c.rec))
				if string(got) != c.want {
					t.Fatalf("got %s, want %s", got, c.want)
				}
			})
		}
	}
	t.Run("codex tool_output", func(t *testing.T) {
		got, _ := json.Marshal(conversationEntries(rec("codex", "tool_output", `{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"secret"}}`)))
		if string(got) != `[{"role":"tool_result"}]` {
			t.Fatalf("got %s, want the result kept without is_error", got)
		}
	})
}

// The consent text promises tool input and output are not sent. read_segment
// reads records through sessionview, whose ToolCall.Input and ToolResult.Output
// carry them in full, so the only guard is what messageView can hold. Adding a
// field here must be a deliberate change to this list, not a side effect.
func TestMessageViewFieldWhitelist(t *testing.T) {
	want := []string{"role", "text", "tool", "is_error"}
	typ := reflect.TypeOf(messageView{})
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		got = append(got, name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("messageView JSON fields = %v, want %v; review the consent text before sending more", got, want)
	}
}
