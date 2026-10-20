package sessionfacts

import (
	"testing"
	"time"
)

func TestCodexTurnForCallUsesCollectedRootAndRejectsCoarseSameSecondJoin(t *testing.T) {
	base := time.Date(2026, 10, 9, 6, 20, 1, 0, time.UTC)
	start := base.Add(339 * time.Millisecond)
	end := base.Add(352 * time.Millisecond)
	rowAt := base.Add(345 * time.Millisecond)
	coarse := &computation{turns: []*turn{
		{out: Turn{TurnKey: "root-a", StartedAt: base, EndedAt: base}, startedAtSource: "payload", endedAtSource: "payload"},
		{out: Turn{TurnKey: "root-b", StartedAt: base, EndedAt: base}, startedAtSource: "payload", endedAtSource: "payload"},
	}}
	if got := codexTurnForCall(coarse, &CodexRow{TS: rowAt, CodexTurnMetadataKnown: true, CodexTurnID: "root-a", CodexRootTurnID: "root-a"}); got != "root-a" {
		t.Fatalf("collected root attribution=%q, want root-a", got)
	}
	if got := codexTurnForCall(coarse, &CodexRow{TS: rowAt, CodexTurnMetadataKnown: true}); got != "" {
		t.Fatalf("known empty context guessed root %q", got)
	}
	if got := codexTurnForCall(coarse, &CodexRow{TS: rowAt}); got != "" {
		t.Fatalf("coarse same-second time join guessed root %q", got)
	}

	precise := &computation{turns: []*turn{
		{out: Turn{TurnKey: "root-a", StartedAt: start, EndedAt: end}, startedAtSource: "line", endedAtSource: "line"},
		{out: Turn{TurnKey: "root-b", StartedAt: end, EndedAt: end}, startedAtSource: "line", endedAtSource: "line"},
	}}
	if got := codexTurnForCall(precise, &CodexRow{TS: rowAt}); got != "root-a" {
		t.Fatalf("precise interval attribution=%q, want root-a", got)
	}
}

func TestCodexSameSecondRootMetadataPreventsHumanTurnSplit(t *testing.T) {
	base := time.Date(2026, 10, 9, 6, 20, 1, 0, time.UTC)
	in := Input{
		Agent:  "codex",
		ReadAt: base.Add(time.Minute),
		CodexRows: []CodexRow{
			{ID: 1, TS: base.Add(345 * time.Millisecond), UUID: "user-row", RecordType: "user", Role: "user", TextHead: "first input", Entrypoint: "cli",
				CodexTurnID: "root-a", CodexRootTurnID: "root-a", CodexTurnMetadataKnown: true},
			{ID: 2, TS: base.Add(350 * time.Millisecond), UUID: "usage-a", RecordType: "usage", InputTokens: 100,
				TurnID: "root-a", RootTurnID: "root-a"},
			{ID: 3, TS: base.Add(370 * time.Millisecond), UUID: "usage-b", RecordType: "usage", InputTokens: 100,
				TurnID: "root-b", RootTurnID: "root-b"},
		},
		Snapshots: []Snapshot{{ObservedAt: base.Add(time.Minute), CodexTurns: []SnapshotCodexTurn{
			{TurnID: "root-a", StartedAt: base, EndedAt: base, End: "complete", StartedAtSource: "payload", EndedAtSource: "payload"},
			{TurnID: "root-b", StartedAt: base, StartedAtSource: "payload"},
		}}},
	}
	extended := ComputeWithTimelineAndCommits(in)
	out := extended.Output
	if len(out.Turns) != 2 {
		t.Fatalf("turns=%d, want the two lifecycle roots without a synthetic third turn: %+v", len(out.Turns), out.Turns)
	}
	if out.Turns[0].TurnKey != "root-a" || out.Turns[0].Preview == nil || *out.Turns[0].Preview != "first input" ||
		out.Turns[0].RequestNo == nil || out.Session.Requests == nil || *out.Session.Requests != 1 {
		t.Fatalf("root-a input attribution: session=%+v turns=%+v", out.Session, out.Turns)
	}
	if out.Turns[1].TurnKey != "root-b" || out.Turns[1].Preview != nil {
		t.Fatalf("root-b took root-a's input: %+v", out.Turns[1])
	}
	for _, row := range extended.Timeline.Rows {
		if row.ID == 1 {
			if row.TurnKey != "root-a" || row.Unmapped != "" {
				t.Fatalf("timeline row lost exact root attribution: %+v", row)
			}
			return
		}
	}
	t.Fatal("timeline omitted the human input row")
}

func TestCodexLegacySameSecondBoundaryReportsUnknownAttribution(t *testing.T) {
	base := time.Date(2026, 10, 9, 6, 20, 1, 0, time.UTC)
	in := Input{
		Agent: "codex", ReadAt: base.Add(time.Minute),
		CodexRows: []CodexRow{
			{ID: 1, TS: base.Add(345 * time.Millisecond), UUID: "user-row", RecordType: "user", Role: "user", TextHead: "first input", Entrypoint: "cli"},
			{ID: 2, TS: base.Add(350 * time.Millisecond), UUID: "usage-row", RecordType: "usage", InputTokens: 100, TurnID: "root-a", RootTurnID: "root-a"},
		},
		Snapshots: []Snapshot{{ObservedAt: base.Add(time.Minute), CodexTurns: []SnapshotCodexTurn{
			{TurnID: "root-a", StartedAt: base, EndedAt: base, End: "complete"},
		}}},
	}
	out := ComputeWithTimelineAndCommits(in)
	if len(out.Turns) != 1 || out.Turns[0].Preview != nil || out.Session.Requests != nil || out.Session.Inputs != nil {
		t.Fatalf("coarse timestamp was assigned as observed: session=%+v turns=%+v", out.Session, out.Turns)
	}
	state := out.Session.Detail.CodexTurnAttribution
	if state == nil || state.State != "undeterminable" || state.Source != "session_records.timestamp+session_snapshots.codex_turns" || state.Reason != "same_second_coarse_lifecycle_boundary" {
		t.Fatalf("attribution status=%+v", state)
	}
	for _, row := range out.Timeline.Rows {
		if row.ID == 1 {
			if row.Unmapped != "codex_same_second_lifecycle_boundary" {
				t.Fatalf("timeline row reason=%q", row.Unmapped)
			}
			return
		}
	}
	t.Fatal("timeline omitted the ambiguous human row")
}
