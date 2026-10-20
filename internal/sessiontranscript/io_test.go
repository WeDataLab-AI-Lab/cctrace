package sessiontranscript

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCounterCountsSavedHumanAndModelTextWithoutToolContent(t *testing.T) {
	base := time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)
	counter := NewCounter()
	for _, record := range []Record{
		{
			Agent: "claude", RecordType: "user", PromptSource: "typed", ObservedAt: base,
			Raw: json.RawMessage(`{"type":"user","message":{"role":"user","content":"안녕하세요\r\nok\rend"}}`),
		},
		{
			Agent: "claude", RecordType: "assistant", ObservedAt: base.Add(time.Second),
			Raw: json.RawMessage(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"hi"},{"type":"tool_use","name":"shell"},{"type":"text","text":" there\n"}]}}`),
		},
		{
			Agent: "claude", RecordType: "user", PromptSource: "typed", IsSidechain: true,
			Raw: json.RawMessage(`{"type":"user","message":{"role":"user","content":"SYNTHETIC-SUBAGENT-PROMPT"}}`),
		},
		{
			Agent: "claude", RecordType: "assistant", SyntheticOutput: true,
			Raw: json.RawMessage(`{"type":"assistant","message":{"role":"assistant","content":"SYNTHETIC-API-ERROR"}}`),
		},
		{
			Agent: "claude", RecordType: "user", PromptSource: "sdk", ObservedAt: base.Add(20 * time.Second),
			Raw: json.RawMessage(`{"type":"user","message":{"role":"user","content":"automated prompt"}}`),
		},
		{
			Agent: "codex", RecordType: "user", Entrypoint: "cli", ObservedAt: base.Add(3 * time.Second),
			Raw: json.RawMessage(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"차"},{"type":"image"}]}}`),
		},
		{
			Agent: "codex", RecordType: "assistant", ObservedAt: base.Add(4 * time.Second),
			Raw: json.RawMessage(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"완료"}]}}`),
		},
		{
			Agent: "codex", RecordType: "tool_output", Entrypoint: "cli", ObservedAt: base.Add(5 * time.Second),
			Raw: json.RawMessage(`{"type":"response_item","payload":{"type":"function_call_output","output":"tool output must not count"}}`),
		},
	} {
		counter.Add(record)
	}

	got := counter.Snapshot()
	inputBytes := int64(len("안녕하세요\r\nok\rend") + len("차"))
	outputBytes := int64(len("hi there\n") + len("완료"))
	if got.InputBytes == nil || *got.InputBytes != inputBytes || got.InputLines == nil || *got.InputLines != 4 {
		t.Errorf("input counts = (%v,%v), want (%d,4)", got.InputBytes, got.InputLines, inputBytes)
	}
	if got.OutputBytes == nil || *got.OutputBytes != outputBytes || got.OutputLines == nil || *got.OutputLines != 2 {
		t.Errorf("output counts = (%v,%v), want (%d,2)", got.OutputBytes, got.OutputLines, outputBytes)
	}
	if got.Partial || got.AsOf == nil || !got.AsOf.Equal(base.Add(4*time.Second)) {
		t.Errorf("metadata = partial:%v as_of:%v, want complete through last counted message", got.Partial, got.AsOf)
	}
}

func TestCounterMakesRedactedAndUnprovenInputUnknown(t *testing.T) {
	cases := []struct {
		name   string
		record Record
	}{
		{
			name: "redacted human body",
			record: Record{Agent: "claude", RecordType: "user", PromptSource: "typed",
				Raw: json.RawMessage(`{"type":"user","message":{"role":"user","content":"[redacted by cctrace client]"}}`)},
		},
		{
			name: "whole raw withheld",
			record: Record{Agent: "claude", RecordType: "user",
				Raw: json.RawMessage(`{"redacted":"[redacted by cctrace client]"}`)},
		},
		{
			name: "legacy Codex originator unknown",
			record: Record{Agent: "codex", RecordType: "user",
				Raw: json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"possibly automated"}]}`)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter := NewCounter()
			counter.Add(tc.record)
			got := counter.Snapshot()
			if got.InputBytes != nil || got.InputLines != nil || !got.Partial {
				t.Fatalf("redacted or unproven input = (%v,%v), partial=%v; want unknown and partial", got.InputBytes, got.InputLines, got.Partial)
			}
		})
	}
}

func TestCounterLeavesUnobservedDirectionsUnknown(t *testing.T) {
	counter := NewCounter()
	counter.Add(Record{Agent: "claude", RecordType: "assistant", Raw: json.RawMessage(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"shell"}]}}`)})
	got := counter.Snapshot()
	if got.InputBytes != nil || got.InputLines != nil || got.OutputBytes != nil || got.OutputLines != nil {
		t.Fatalf("tool-only session counts = %+v; unobserved directions must stay unknown", got)
	}
}
