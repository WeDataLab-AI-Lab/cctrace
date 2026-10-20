package sessiontrace

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestProjectionCodexSkinnyMessagesAndCustomCalls(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct{ raw, kind string }{
		{`{"type":"response_item","payload":{"type":"message","role":"user","has_text":true}}`, "input"},
		{`{"type":"response_item","payload":{"type":"message","role":"assistant","has_text":true}}`, "response"},
		{`{"type":"event_msg","payload":{"type":"user_message"}}`, "input"},
		{`{"type":"event_msg","payload":{"type":"agent_message"}}`, "response"},
		{`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture"}]}}`, "response"},
	} {
		e := only(t, Normalize([]Record{row(1, "codex", 1, fixture.raw)}))
		if e.Kind != fixture.kind || e.StartedAt != nil || e.EndedAt != nil {
			t.Fatalf("wrong message projection: %+v", e)
		}
	}
	c := row(1, "codex", 1, `{"type":"response_item","payload":{"type":"custom_tool_call","call_id":"x","name":"apply_patch"}}`)
	r := row(2, "codex", 3, `{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"x","status":"completed"}}`)
	if e := only(t, Normalize([]Record{r, c})); e.EndedAt == nil || e.Status != "completed" {
		t.Fatalf("custom tool not paired: %+v", e)
	}
}

func TestProjectionCodexExactPayloadTurnsDoNotCrossPair(t *testing.T) {
	t.Parallel()
	c := row(1, "codex", 1, `{"type":"response_item","payload":{"type":"function_call","call_id":"x","name":"exec_command","turn_id":"one","root_turn_id":"root"}}`)
	r := row(2, "codex", 3, `{"type":"response_item","payload":{"type":"function_call_output","call_id":"x","turn_id":"two","root_turn_id":"root"}}`)
	if got := Normalize([]Record{c, r}); len(got) != 2 {
		t.Fatalf("exact raw turns crossed: %+v", got)
	}
}

func TestProjectionBodyFreeMetadataMatchesRawSourceID(t *testing.T) {
	t.Parallel()
	raw := row(1, "claude", 1, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private"},{"type":"tool_use","id":"x","name":"Read","input":{"path":"private"}}]}}`)
	raw.RecordType = "assistant"
	idx := 1
	skinny := raw
	skinny.Raw = nil
	skinny.Blocks = []Block{{Index: &idx, Type: "tool_use", ID: "x", Name: "Read"}}
	a, b := Normalize([]Record{raw}), Normalize([]Record{skinny})
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("SQL projection changes observed identity: %+v %+v", a, b)
	}
	compact := raw
	compact.Raw = json.RawMessage(`{"message":{"content":[{"index":1,"t":"tool_use","id":"x","n":"Read"}]}}`)
	if !reflect.DeepEqual(a, Normalize([]Record{compact})) {
		t.Fatal("compact skinny vocabulary differs")
	}
}

func TestNormalizeMultiCallDuplicateIDAndMissingScope(t *testing.T) {
	t.Parallel()
	c, r := call(1, 1, "x"), result(2, 3, "x")
	c.Blocks = append(c.Blocks, Block{Type: "tool_use", ID: "x", Name: "Read"})
	if events := Normalize([]Record{c, r}); len(events) != 3 {
		t.Fatalf("same-record duplicate call IDs paired: %+v", events)
	}
	c.Blocks = c.Blocks[:1]
	c.SourceFile, r.SourceFile = "", ""
	if events := Normalize([]Record{c, r}); len(events) != 2 {
		t.Fatalf("unknown sourcefile fabricated exact scope: %+v", events)
	}
}

func TestNormalizeSameSourceConflictIsDeterministicUnknown(t *testing.T) {
	t.Parallel()
	a := call(1, 1, "x")
	b := a
	b.Blocks = []Block{{Type: "tool_use", ID: "y", Name: "Read"}}
	first, reversed := Normalize([]Record{a, b}), Normalize([]Record{b, a})
	if !reflect.DeepEqual(first, reversed) {
		t.Fatal("conflicting source tuple depends on order")
	}
	e := only(t, first)
	if e.Status != "unknown" || !has(e, "source_conflict") || EventCorrelationKey(e) != "" {
		t.Fatalf("conflicting source guessed: %+v", e)
	}
	if !reflect.DeepEqual(first, Merge(Normalize([]Record{a}), Normalize([]Record{b}))) {
		t.Fatal("chunk source conflict differs")
	}
}

func TestProjectionExplicitEmptyBlocksAndHasTextPoint(t *testing.T) {
	t.Parallel()
	r := row(1, "claude", 1, "")
	r.RecordType, r.HasText, r.Blocks = "user", true, []Block{}
	e := only(t, Normalize([]Record{r}))
	if e.Kind != "input" || e.StartedAt != nil || e.EndedAt != nil {
		t.Fatalf("metadata input point missing: %+v", e)
	}
	r.IsMeta = true
	if got := Normalize([]Record{r}); len(got) != 0 {
		t.Fatalf("bookkeeping rendered: %+v", got)
	}
}

func TestProjectionSQLScalarRecordsWithoutRaw(t *testing.T) {
	t.Parallel()
	user := row(1, "claude", 1, "")
	user.RecordType, user.Role, user.HasText = "user", "user", true
	if e := only(t, Normalize([]Record{user})); e.Kind != "input" {
		t.Fatalf("SQL Claude string lost: %+v", e)
	}
	start, end := row(2, "codex", 2, ""), row(3, "codex", 5, "")
	start.RecordType, end.RecordType = "response_item", "response_item"
	start.PayloadType, start.ToolCallID, start.ToolName = "function_call", "x", "exec_command"
	end.PayloadType, end.ToolCallID, end.ToolStatus = "function_call_output", "x", "completed"
	start.CodexTurnID, end.CodexTurnID = "turn", "turn"
	start.CodexRootTurnID, end.CodexRootTurnID = "root", "root"
	if e := only(t, Normalize([]Record{start, end})); e.EndedAt == nil || e.Status != "completed" {
		t.Fatalf("SQL Codex scalar call lost: %+v", e)
	}
	input := row(4, "codex", 7, "")
	input.RecordType, input.PayloadType = "event_msg", "user_message"
	if e := only(t, Normalize([]Record{input})); e.Kind != "input" || e.StartedAt != nil || e.EndedAt != nil {
		t.Fatalf("SQL Codex user message lost: %+v", e)
	}
}
