package sessionfacts

import (
	"testing"
	"time"
)

// Codex writes the user message before the turn's first lifecycle event, often
// by seconds, and a message typed while the previous turn ran waits even longer.
// The message's own turn id places it; its timestamp alone shifted every input
// one turn earlier.
func codexTurnIDSession() Input {
	base := time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)
	return Input{
		Agent: "codex", ReadAt: base.Add(time.Hour),
		CodexRows: []CodexRow{
			{ID: 1, TS: base.Add(-2 * time.Second), UUID: "u1", RecordType: "user", Role: "user", TextHead: "first request", Entrypoint: "cli", TurnID: "root-a"},
			{ID: 2, TS: base.Add(5 * time.Second), UUID: "usage-a", RecordType: "usage", InputTokens: 100, TurnID: "root-a", RootTurnID: "root-a"},
			{ID: 3, TS: base.Add(100 * time.Second), UUID: "u2", RecordType: "user", Role: "user", TextHead: "second request", Entrypoint: "cli", TurnID: "root-b"},
			{ID: 4, TS: base.Add(190 * time.Second), UUID: "usage-b", RecordType: "usage", InputTokens: 100, TurnID: "root-b", RootTurnID: "root-b"},
			{ID: 5, TS: base.Add(300 * time.Second), UUID: "u3", RecordType: "user", Role: "user", TextHead: "third request", Entrypoint: "cli", TurnID: "root-c"},
			{ID: 6, TS: base.Add(305 * time.Second), UUID: "usage-c", RecordType: "usage", InputTokens: 100, TurnID: "root-c", RootTurnID: "root-c"},
		},
		Snapshots: []Snapshot{{ObservedAt: base.Add(time.Hour), CodexTurns: []SnapshotCodexTurn{
			{TurnID: "root-a", StartedAt: base, EndedAt: base.Add(60 * time.Second), End: "complete", StartedAtSource: "line", EndedAtSource: "line"},
			{TurnID: "root-b", StartedAt: base.Add(180 * time.Second), EndedAt: base.Add(200 * time.Second), End: "complete", StartedAtSource: "line", EndedAtSource: "line"},
			{TurnID: "root-c", StartedAt: base.Add(302 * time.Second), EndedAt: base.Add(310 * time.Second), End: "complete", StartedAtSource: "line", EndedAtSource: "line"},
		}}},
	}
}

func checkCodexTurnIDAttribution(t *testing.T, out Output) {
	t.Helper()
	want := []struct{ key, preview string }{{"root-a", "first request"}, {"root-b", "second request"}, {"root-c", "third request"}}
	if len(out.Turns) != len(want) {
		t.Fatalf("turns=%d: %+v", len(out.Turns), out.Turns)
	}
	for i, w := range want {
		turn := out.Turns[i]
		if turn.TurnKey != w.key || turn.Preview == nil || *turn.Preview != w.preview || !turn.IsRequest {
			t.Fatalf("turn %d=%+v preview=%v, want %s opened by %q", i, turn, turn.Preview, w.key, w.preview)
		}
		// Each turn keeps its own number; they used to share one counter.
		if turn.RequestNo == nil || *turn.RequestNo != i+1 {
			t.Fatalf("turn %d request_no=%v, want %d", i, turn.RequestNo, i+1)
		}
	}
}

func TestCodexInputsFollowTheirTurnIDOnTheWorkerPath(t *testing.T) {
	checkCodexTurnIDAttribution(t, ComputeWithTimelineAndCommits(codexTurnIDSession()).Output)
}
