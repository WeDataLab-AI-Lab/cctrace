package sessiontrace

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func row(id int64, agent string, second int, raw string) Record {
	return Record{ID: id, SessionID: "session", Agent: agent, TS: time.Date(2026, 10, 13, 0, 0, second, 0, time.UTC), UUID: "u" + time.Duration(id).String(), SourceFile: "source.jsonl", Raw: json.RawMessage(raw)}
}

func call(id int64, second int, callID string) Record {
	r := row(id, "claude", second, "")
	r.RecordType = "assistant"
	r.Blocks = []Block{{Type: "tool_use", ID: callID, Name: "Bash"}}
	return r
}

func result(id int64, second int, callID string) Record {
	r := row(id, "claude", second, "")
	r.RecordType = "user"
	r.Blocks = []Block{{Type: "tool_result", ToolUseID: callID}}
	return r
}

func only(t *testing.T, events []Event) Event {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("want one event, got %d: %+v", len(events), events)
	}
	return events[0]
}

func has(e Event, reason string) bool { return slices.Contains(e.Evidence, reason) }

func TestNormalizeClaudeRawPointsAndParallelCalls(t *testing.T) {
	t.Parallel()
	u := row(1, "claude", 1, `{"type":"user","message":{"role":"user","content":"fixture input"}}`)
	u.RecordType = "user"
	a := row(2, "claude", 2, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"fixture response"},{"type":"tool_use","id":"a","name":"Read","input":{"secret":"private"}},{"type":"tool_use","id":"b","name":"Bash"}]}}`)
	a.RecordType = "assistant"
	r := row(3, "claude", 5, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","is_error":false,"content":"private result"},{"type":"tool_result","tool_use_id":"b","is_error":true}]}}`)
	r.RecordType = "user"
	events := Normalize([]Record{r, a, u})
	if len(events) != 4 {
		t.Fatalf("want input,response,2 tools; got %+v", events)
	}
	for _, e := range events {
		if e.Kind == "input" || e.Kind == "response" {
			if e.StartedAt != nil || e.EndedAt != nil {
				t.Fatalf("message point became span: %+v", e)
			}
		} else if e.StartedAt == nil || e.EndedAt == nil || !e.StartedAt.Equal(a.TS) || !e.EndedAt.Equal(r.TS) || !has(e, "observed_interval") {
			t.Fatalf("tool interval absent: %+v", e)
		}
		if e.TurnKey != nil || e.ParentEventID != nil {
			t.Fatalf("fabricated relationship: %+v", e)
		}
	}
	if events[2].Status != "completed" || events[3].Status != "error" {
		t.Fatalf("wrong observed outcomes: %+v", events)
	}
	wire, _ := json.Marshal(events)
	if strings.Contains(string(wire), "private") || strings.Contains(string(wire), "fixture") {
		t.Fatalf("raw body leaked: %s", wire)
	}
}

func TestNormalizeCodexRawAndExactTurn(t *testing.T) {
	t.Parallel()
	c := row(1, "codex", 2, `{"type":"response_item","payload":{"type":"function_call","call_id":"x","name":"exec_command","arguments":"private"}}`)
	r := row(2, "codex", 6, `{"type":"response_item","payload":{"type":"function_call_output","call_id":"x","output":"private"}}`)
	c.TurnKey, r.TurnKey = "exact-turn", "exact-turn"
	c.CodexTurnID, r.CodexTurnID = "turn", "turn"
	e := only(t, Normalize([]Record{r, c}))
	if e.Kind != "tool" || e.TurnKey == nil || *e.TurnKey != "exact-turn" || e.EndedAt == nil || e.Status != "unknown" {
		t.Fatalf("wrong Codex observation: %+v", e)
	}
	if !has(e, "outcome_unknown") {
		t.Fatal("missing unknown outcome evidence")
	}
}

func TestNormalizeScopeDoesNotCrossPair(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"session", "provider", "agent", "file", "branch", "account", "turn"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			c, r := call(1, 1, "same"), result(2, 2, "same")
			switch scope {
			case "session":
				r.SessionID = "other"
			case "provider":
				r.Agent = "codex"
			case "agent":
				r.AgentID = "child"
			case "file":
				r.SourceFile = "copy.jsonl"
			case "branch":
				r.Branch = "fork"
			case "account":
				r.SourceScope = "other-owner"
			case "turn":
				c.CodexTurnID, r.CodexTurnID = "turn-a", "turn-b"
			}
			events := Normalize([]Record{c, r})
			if len(events) != 2 {
				t.Fatalf("scope incorrectly paired: %+v", events)
			}
			for _, e := range events {
				if e.StartedAt != nil && e.EndedAt != nil {
					t.Fatalf("fabricated interval: %+v", e)
				}
			}
		})
	}
}

func TestNormalizeMissingAndClockReversal(t *testing.T) {
	t.Parallel()
	c, r := call(1, 5, "x"), result(2, 3, "x")
	open := only(t, Normalize([]Record{c}))
	if open.EndedAt != nil || open.Status != "unknown" || !has(open, "end_missing") {
		t.Fatalf("missing end fabricated: %+v", open)
	}
	orphan := only(t, Normalize([]Record{r}))
	if orphan.StartedAt != nil || orphan.EndedAt == nil || !has(orphan, "start_missing") {
		t.Fatalf("missing start fabricated: %+v", orphan)
	}
	reversed := only(t, Normalize([]Record{c, r}))
	if reversed.Status != "unknown" || reversed.EndedAt != nil || !has(reversed, "clock_reversal") {
		t.Fatalf("reversed duration accepted: %+v", reversed)
	}
	if len(Sources(reversed)) != 2 {
		t.Fatal("lost observed result provenance")
	}
}

func TestNormalizeDistinctCopiesAmbiguousIDsAndStableOrder(t *testing.T) {
	t.Parallel()
	c, r := call(1, 1, "x"), result(3, 4, "x")
	copy := c
	copy.ID = 2
	events := Normalize([]Record{c, copy, r})
	if len(events) != 3 {
		t.Fatalf("copies collapsed or arbitrarily paired: %+v", events)
	}
	if events[0].EventID == events[1].EventID || events[0].SourceRef == events[1].SourceRef {
		t.Fatal("source tuple ignored")
	}
	for _, e := range events {
		if e.Status != "unknown" || !has(e, "ambiguous_call_id") {
			t.Fatalf("ambiguous pairing accepted: %+v", e)
		}
	}
	if !reflect.DeepEqual(events, Normalize([]Record{r, copy, c})) {
		t.Fatal("record order changed output")
	}
	if len(Normalize([]Record{c, c})) != 1 {
		t.Fatal("exact source replay not idempotent")
	}
}

func TestNormalizeMetadataRedactedMissingAndOrdinality(t *testing.T) {
	t.Parallel()
	idx := 7
	r := call(1, 1, "x")
	r.Redacted = true
	r.Raw = json.RawMessage(`{"message":{"content":"private fallback"}}`)
	r.Blocks[0].Index = &idx
	e := only(t, Normalize([]Record{r}))
	if !has(e, "redacted_metadata") {
		t.Fatal("redaction evidence absent")
	}
	r.Blocks = []Block{{Type: "text", HasText: false}, {Type: "tool_use", ID: "x", Name: "Bash", Index: &idx}}
	e2 := only(t, Normalize([]Record{r}))
	if e.EventID != e2.EventID {
		t.Fatal("filtered SQL projection changed original block ID")
	}
	r.Blocks = []Block{}
	if len(Normalize([]Record{r})) != 0 {
		t.Fatal("explicit empty projection fell back to raw")
	}
	r.Blocks = []Block{{Type: "tool_use", Name: "Bash"}}
	missing := only(t, Normalize([]Record{r}))
	if missing.Status != "unknown" || !has(missing, "call_id_missing") {
		t.Fatalf("missing metadata guessed: %+v", missing)
	}
	r.TS = time.Time{}
	if len(Normalize([]Record{r})) != 0 {
		t.Fatal("unknown timestamp fabricated")
	}
	r = row(5, "claude", 1, `{"broken":`)
	if len(Normalize([]Record{r})) != 0 {
		t.Fatal("malformed raw invented event")
	}
}

func TestNormalizeExplicitAgentParentAndNoProximity(t *testing.T) {
	t.Parallel()
	spawn, end := call(1, 1, "spawn"), result(2, 8, "spawn")
	spawn.Blocks[0].Name = "Agent"
	spawn.ChildAgentID = "child"
	child := row(3, "claude", 2, "")
	child.RecordType, child.AgentID, child.SourceFile = "assistant", "child", "child.jsonl"
	child.Blocks = []Block{{Type: "text", HasText: true}}
	child.ParentToolUseID, child.ParentSourceFile = "spawn", "source.jsonl"
	unknown := child
	unknown.ID, unknown.ParentToolUseID = 4, ""
	events := Normalize([]Record{spawn, end, child, unknown})
	if len(events) != 4 || events[0].Kind != "tool" || events[0].EndedAt == nil || events[1].Kind != "agent" || events[1].StartedAt != nil || events[1].EndedAt != nil || events[1].Status != "unknown" {
		t.Fatalf("dispatch is not subagent runtime: %+v", events)
	}
	if events[2].ParentEventID == nil || *events[2].ParentEventID != events[0].EventID {
		t.Fatalf("exact agent relation missing: %+v", events[2])
	}
	if events[3].ParentEventID != nil {
		t.Fatalf("nearby subagent parent fabricated: %+v", events[3])
	}
}

func TestNormalizeWireNullsOpaqueSourceAndNoInternalFields(t *testing.T) {
	t.Parallel()
	e := only(t, Normalize([]Record{call(1, 1, "secret-call")}))
	wire, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(wire, &obj); err != nil {
		t.Fatal(err)
	}
	if len(obj) != 12 {
		t.Fatalf("wire contract fields changed: %s", wire)
	}
	for _, field := range []string{"ended_at", "turn_key", "parent_event_id", "agent_id"} {
		if value, exists := obj[field]; !exists || value != nil {
			t.Fatalf("%s is not explicit null: %s", field, wire)
		}
	}
	if strings.Contains(string(wire), "secret-call") || strings.Contains(e.SourceRef, "source.jsonl") {
		t.Fatalf("correlation leaked: %s", wire)
	}
	if len(Sources(e)) != 1 || Sources(e)[0].ID != 1 {
		t.Fatal("source lookup missing")
	}
}

func TestMergeCrossChunkLateResultReplayAndConflict(t *testing.T) {
	t.Parallel()
	c, r := call(1, 1, "x"), result(2, 8, "x")
	a, b := Normalize([]Record{c}), Normalize([]Record{r})
	combined := Merge(b, a)
	if !reflect.DeepEqual(combined, Normalize([]Record{c, r})) {
		t.Fatalf("cross-chunk differs from one shot: %+v", combined)
	}
	if !reflect.DeepEqual(combined, Merge(a, b, a, b)) {
		t.Fatal("chunk replay duplicated source")
	}
	if !reflect.DeepEqual(combined, Merge(combined, a)) {
		t.Fatal("merged chunk replay not idempotent")
	}
	conflict := c
	conflict.ID = 3
	if got := Merge(combined, Normalize([]Record{conflict})); len(got) != 3 {
		t.Fatalf("late ambiguity discarded: %+v", got)
	}
	if a[0].EndedAt != nil {
		t.Fatal("merge mutated previous snapshot")
	}
}
