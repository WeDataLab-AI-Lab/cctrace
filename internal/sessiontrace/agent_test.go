package sessiontrace

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestAgentDispatchResultDoesNotCompleteBackgroundRuntime(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Agent", "Task", "spawn_agent"} {
		c, r := call(1, 1, "spawn"), result(2, 2, "spawn")
		c.Blocks[0].Name, c.ChildAgentID = name, "child"
		success := false
		r.Blocks[0].IsError = &success
		events := Normalize([]Record{c, r})
		if len(events) != 2 {
			t.Fatalf("want dispatch and explicit launch point: %+v", events)
		}
		for _, e := range events {
			if e.Kind == "agent" && (e.StartedAt != nil || e.EndedAt != nil || e.Status != "unknown" || !has(e, "dispatch_only")) {
				t.Fatalf("background runtime falsely completed by dispatch result: %+v", e)
			}
			if e.Kind == "tool" && (e.EndedAt == nil || e.Status != "completed") {
				t.Fatalf("observed dispatch interval lost: %+v", e)
			}
		}
	}
}

func TestAgentActualClaudeResultMetadataHasNoInventedRuntime(t *testing.T) {
	t.Parallel()
	c := call(1, 1, "spawn")
	c.Blocks[0].Name = "Agent"
	r := result(2, 2, "spawn")
	r.Blocks = nil
	r.Raw = json.RawMessage(`{"type":"user","toolUseResult":{"agentId":"child","isAsync":true,"status":"async_launched","stdout":"private"},"message":{"content":[{"type":"tool_result","tool_use_id":"spawn","content":"private"}]}}`)
	events := Normalize([]Record{c, r})
	if len(events) != 2 {
		t.Fatalf("actual child metadata lost: %+v", events)
	}
	for _, e := range events {
		if e.Kind == "agent" && (e.AgentID == nil || *e.AgentID != "child" || !e.Timestamp.Equal(r.TS) || e.StartedAt != nil || e.EndedAt != nil || e.Status != "unknown" || e.ParentEventID == nil) {
			t.Fatalf("actual result became fabricated lifecycle: %+v", e)
		}
	}
}

func TestAgentResultRowMetadataDoesNotSpreadToOtherCalls(t *testing.T) {
	t.Parallel()
	r := result(3, 3, "spawn")
	r.ChildAgentID, r.ToolUseID = "child", "spawn"
	r.Blocks = append(r.Blocks, Block{Type: "tool_result", ToolUseID: "other"})
	events := Normalize([]Record{r})
	var agents int
	for _, e := range events {
		if e.Kind == "agent" {
			agents++
		}
	}
	if agents != 0 {
		t.Fatalf("row child metadata spread across unrelated calls: %+v", events)
	}
	r.Blocks[0].ChildAgentID = "child"
	events = Normalize([]Record{r})
	agents = 0
	for _, e := range events {
		if e.Kind == "agent" {
			agents++
		}
	}
	if agents != 1 {
		t.Fatalf("explicit block child metadata lost: %+v", events)
	}
}

func TestAgentSingleResultMetadataUsesResultBlockNotSpawningAnchor(t *testing.T) {
	t.Parallel()
	r := result(3, 3, "spawn")
	r.ChildAgentID, r.ToolUseID = "child", "outer-spawning-call"
	r.Blocks = append(r.Blocks, Block{Type: "text", HasText: true})
	events := Normalize([]Record{r})
	var agents int
	for _, e := range events {
		if e.Kind == "agent" {
			agents++
			if e.AgentID == nil || *e.AgentID != "child" || e.StartedAt != nil || e.EndedAt != nil {
				t.Fatalf("single result child metadata misattributed: %+v", e)
			}
		}
	}
	if agents != 1 {
		t.Fatalf("single result child metadata missing: %+v", events)
	}
}

func TestAgentExplicitLifecycleIsIndependentOfDispatch(t *testing.T) {
	t.Parallel()
	dispatch := call(1, 1, "x")
	dispatch.Blocks[0].Name = "Agent"
	start, end := call(2, 2, "x"), result(3, 9, "x")
	start.Blocks[0].Type = "agent_start"
	end.Blocks[0].Type, end.Blocks[0].Status = "agent_end", "interrupted"
	events := Normalize([]Record{dispatch, start, end})
	if len(events) != 2 {
		t.Fatalf("agent lifecycle collided with tool call ID: %+v", events)
	}
	for _, e := range events {
		if e.Kind == "agent" && (e.EndedAt == nil || e.Status != "interrupted") {
			t.Fatalf("explicit agent lifecycle missing: %+v", e)
		}
		if e.Kind == "tool" && e.EndedAt != nil {
			t.Fatalf("lifecycle fabricated dispatch result: %+v", e)
		}
	}
}

func TestAgentExplicitParentAcrossChunksExposesLookupBucket(t *testing.T) {
	t.Parallel()
	parent := call(1, 1, "spawn")
	child := call(2, 2, "read")
	child.AgentID, child.SourceFile = "child", "child.jsonl"
	child.ParentToolUseID, child.ParentSourceFile = "spawn", "source.jsonl"
	p, c := only(t, Normalize([]Record{parent})), only(t, Normalize([]Record{child}))
	keys := EventRelatedCorrelationKeys(c)
	if len(keys) != 2 || !slices.Contains(keys, EventCorrelationKey(p)) || !slices.Contains(keys, EventCorrelationKey(c)) {
		t.Fatalf("parent cache bucket missing: %v", keys)
	}
	data, err := MarshalStorage([]Event{c})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := UnmarshalStorage(data)
	if err != nil {
		t.Fatal(err)
	}
	events := Merge(restored, []Event{p})
	if events[1].ParentEventID == nil || *events[1].ParentEventID != p.EventID {
		t.Fatalf("cross-chunk parent not resolved: %+v", events)
	}
}

func TestAgentConflictingExplicitParentsRemainUnknown(t *testing.T) {
	t.Parallel()
	a, b := call(1, 1, "a"), call(2, 2, "b")
	c, r := call(3, 3, "child-call"), result(4, 5, "child-call")
	c.AgentID, r.AgentID, c.SourceFile, r.SourceFile = "child", "child", "child.jsonl", "child.jsonl"
	c.ParentToolUseID, r.ParentToolUseID = "a", "b"
	c.ParentSourceFile, r.ParentSourceFile = "source.jsonl", "source.jsonl"
	events := Normalize([]Record{a, b, c, r})
	e := events[2]
	if e.ParentEventID != nil || e.Status != "unknown" || !has(e, "parent_conflict") {
		t.Fatalf("conflicting exact parents arbitrarily selected: %+v", e)
	}
}
