package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexlog"
)

func startedEvent(turn string, offset int64, start time.Time) codexlog.TurnEvent {
	return codexlog.TurnEvent{Kind: "task_started", Offset: offset, At: start, TurnID: turn, StartedAt: start, CollaborationModeKind: "default"}
}

func turnIDs(turns []SnapshotCodexTurn) []string {
	var ids []string
	for _, t := range turns {
		ids = append(ids, t.TurnID)
	}
	return ids
}

// One turn is one entry however many lines and passes name it: a turn started
// in one pass and ended in the next merges, and a pass folds only the lines it
// commits -- a line past a newOffset held back waits for the pass that commits
// it.
func TestCodexTurnMergesAcrossPasses(t *testing.T) {
	fakeClock(t)
	fs := &FileState{}
	start := t0
	ten, eight := int64(5000), int64(800)
	AdvanceCodexSnapshot(context.Background(), fs, "rollout.jsonl", 0, 200, "ph", []codexlog.TurnEvent{
		startedEvent("t-1", 10, start),
		{Kind: "turn_context", Offset: 120, At: start, TurnID: "t-1", Model: "test-model", Effort: "high"},
	})
	if fs.SessionSnapshot == nil || fs.SessionSnapshot.Revision != 1 {
		t.Fatalf("after the first pass: state %+v, want a snapshot at revision 1", fs.SessionSnapshot)
	}
	held := []codexlog.TurnEvent{{Kind: "task_complete", Offset: 200, At: start.Add(time.Minute), TurnID: "t-1",
		CompletedAt: start.Add(time.Minute), DurationMs: &ten, TimeToFirstTokenMs: &eight}}
	AdvanceCodexSnapshot(context.Background(), fs, "rollout.jsonl", 200, 200, "ph", held)
	if got := fs.SessionSnapshot.Snapshot.CodexTurns[0].End; got != "" || fs.SessionSnapshot.Revision != 1 {
		t.Fatalf("a line past the committed offset was folded: end %q revision %d", got, fs.SessionSnapshot.Revision)
	}
	AdvanceCodexSnapshot(context.Background(), fs, "rollout.jsonl", 200, 300, "ph", held)

	st := fs.SessionSnapshot
	want := `[{"turn_id":"t-1","started_at":"2026-10-01T09:00:00Z","ended_at":"2026-10-01T09:01:00Z","timestamps_exact":true,"end":"complete",` +
		`"duration_ms":5000,"time_to_first_token_ms":800,"collaboration_mode_kind":"default","model":"test-model","effort":"high"}]`
	if got, _ := json.Marshal(st.Snapshot.CodexTurns); string(got) != want || st.Revision != 2 {
		t.Errorf("turns %s revision %d\nwant %s revision 2", got, st.Revision, want)
	}
	if got := encodeSnapshot(t, st.Snapshot); !strings.Contains(got, `"ai_title":"","custom_title":"","agent_name":""`) || strings.Contains(got, "truncated") || strings.Contains(got, `"git"`) {
		t.Errorf("snapshot %s: want empty titles, nothing truncated, no git state", got)
	}
	if !st.NewestRecordAt.Equal(start.Add(time.Minute)) {
		t.Errorf("newest record at %v, want the newest turn line's time", st.NewestRecordAt)
	}
}

// Past maxSnapshotCodexTurns the newest by start are kept, whatever order the
// lines came in, and codex_turns is named in truncated. A turn whose lines gave
// no start keeps its place behind the turn it was first seen after.
func TestCodexTurnsKeepTheNewestByStart(t *testing.T) {
	var events []codexlog.TurnEvent
	for i := maxSnapshotCodexTurns + 9; i >= 0; i-- { // newest first
		events = append(events, startedEvent(fmt.Sprintf("t-%03d", i), int64(i), t0.Add(time.Duration(i)*time.Minute)))
	}
	snap := newSessionSnapshot()
	snap.foldCodexTurns(events, 0, 1<<20)
	ids := turnIDs(snap.CodexTurns)
	if len(ids) != maxSnapshotCodexTurns || ids[0] != "t-010" || ids[len(ids)-1] != fmt.Sprintf("t-%03d", maxSnapshotCodexTurns+9) {
		t.Errorf("kept %d turns, %s..%s; want the newest %d by start", len(ids), ids[0], ids[len(ids)-1], maxSnapshotCodexTurns)
	}
	if !slices.IsSortedFunc(snap.CodexTurns, func(a, b SnapshotCodexTurn) int { return a.StartedAt.Compare(b.StartedAt) }) {
		t.Error("turns not ordered by start")
	}
	if !slices.Contains(snap.Truncated, "codex_turns") {
		t.Errorf("truncated %v, want codex_turns named", snap.Truncated)
	}

	unstarted := newSessionSnapshot()
	unstarted.foldCodexTurns([]codexlog.TurnEvent{
		{Kind: "task_complete", Offset: 1, TurnID: "no-start-first"},
		startedEvent("late", 2, t0.Add(time.Hour)),
		{Kind: "task_complete", Offset: 3, TurnID: "no-start-after-late"},
		startedEvent("early", 4, t0),
	}, 0, 10)
	if got, want := turnIDs(unstarted.CodexTurns), []string{"no-start-first", "early", "late", "no-start-after-late"}; !slices.Equal(got, want) {
		t.Errorf("order %v, want %v", got, want)
	}
	if len(unstarted.Truncated) != 0 {
		t.Errorf("truncated %v with nothing cut", unstarted.Truncated)
	}
}

// A full list of turns whose ids run to the identifier cap, and whose names to
// the short-name cap, encodes past the server's 64 KiB. The oldest turns go
// until the snapshot fits, and codex_turns is named in truncated.
func TestCodexSnapshotStaysUnderTheServersCap(t *testing.T) {
	long := strings.Repeat("r", 700)
	name := strings.Repeat("m", maxShortNameBytes)
	var events []codexlog.TurnEvent
	for i := 0; i < maxSnapshotCodexTurns; i++ {
		at := t0.Add(time.Duration(i) * time.Minute)
		id := fmt.Sprintf("t-%03d-%s", i, long)
		events = append(events,
			codexlog.TurnEvent{Kind: "task_started", Offset: int64(2 * i), At: at, TurnID: id, RootTurnID: long, StartedAt: at, CollaborationModeKind: name},
			codexlog.TurnEvent{Kind: "turn_context", Offset: int64(2*i + 1), At: at, TurnID: id, Model: name, Effort: name})
	}
	fs := &FileState{}
	AdvanceCodexSnapshot(context.Background(), fs, filepath.Join(t.TempDir(), "rollout.jsonl"), 0, 1<<20, "ph", events)
	snap := fs.SessionSnapshot.Snapshot
	if b, _ := json.Marshal(snap); len(b) > maxSnapshotBytes {
		t.Fatalf("snapshot encodes to %d bytes, over %d", len(b), maxSnapshotBytes)
	}
	ids := turnIDs(snap.CodexTurns)
	if len(ids) == 0 || len(ids) == maxSnapshotCodexTurns || !strings.HasPrefix(ids[len(ids)-1], fmt.Sprintf("t-%03d-", maxSnapshotCodexTurns-1)) {
		t.Errorf("kept %d turns; want the newest few", len(ids))
	}
	if !slices.Contains(snap.Truncated, "codex_turns") {
		t.Errorf("truncated %v, want codex_turns named", snap.Truncated)
	}
	if got := snap.CodexTurns[0]; len(got.TurnID) != maxSnapshotFieldBytes || len(got.RootTurnID) != maxSnapshotFieldBytes || got.Model != name {
		t.Errorf("turn id %d bytes, root %d bytes, model %q; want ids cut at %d and the name kept", len(got.TurnID), len(got.RootTurnID), got.Model, maxSnapshotFieldBytes)
	}
}

// A Codex project names its rollout's working directory, which no cap bounds.
// One past the server's key cap would be refused whole, and a few refusals park
// every flush, so such a snapshot is not sent: the heartbeat goes alone and the
// snapshot stays as it was.
func TestCodexSnapshotPastTheKeyCapIsNotSent(t *testing.T) {
	srv := newObservationServer(t)
	home := t.TempDir()
	codexState, err := LoadState(filepath.Join(t.TempDir(), "codex-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(normalizeHome(home), "sessions", "rollout-2026-10-01T09-00-00-00000000-0000-0000-0000-00000000cafe.jsonl")
	snap := newSessionSnapshot()
	snap.addCodexTurns([]SnapshotCodexTurn{{TurnID: "t-1", StartedAt: t0}})
	st := &SessionSnapshotState{Snapshot: snap, Revision: 1, ProjectHash: "-" + strings.Repeat("p", maxObservationKeyBytes), ObservedAt: t0}
	codexState.Files[path] = &FileState{SessionSnapshot: st}
	s, _ := newSnapshotSyncer(t, t.TempDir(), srv.URL())
	s.ServeCodexSnapshots(codexState, []string{home})

	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reqs := srv.requests(); len(reqs) != 1 || len(reqs[0].Snapshots) != 0 {
		t.Errorf("requests %v, want the heartbeat alone", srv.bodies())
	}
	if !st.dirty() {
		t.Error("the snapshot was marked sent")
	}
}

// The exact top-level task_started timestamp wins over payload seconds. When
// the line timestamp is absent, task_started's payload time outranks another
// lifecycle line's fallback time, including after snapshot reload.
func TestCodexTurnStartPrefersTaskStarted(t *testing.T) {
	line := t0.Add(5 * time.Second)
	for _, readBack := range []bool{false, true} {
		snap := newSessionSnapshot()
		snap.foldCodexTurns([]codexlog.TurnEvent{{Kind: "task_started", Offset: 1, At: line, TurnID: "a"}}, 0, 10)
		if got := snap.CodexTurns[0]; !got.StartedAt.Equal(line) || !got.TimestampsExact || got.StartedAtSource != "" {
			t.Errorf("without started_at: start %v exact=%v source=%q, want exact line time %v", got.StartedAt, got.TimestampsExact, got.StartedAtSource, line)
		}
		if readBack {
			encoded := encodeSnapshot(t, snap)
			snap = SessionSnapshot{}
			if err := json.Unmarshal([]byte(encoded), &snap); err != nil {
				t.Fatal(err)
			}
		}
		snap.foldCodexTurns([]codexlog.TurnEvent{{Kind: "task_complete", Offset: 2, At: line, TurnID: "a", StartedAt: t0, CompletedAt: t0.Add(time.Minute)}}, 0, 10)
		if got := snap.CodexTurns[0]; !got.StartedAt.Equal(line) || !got.TimestampsExact {
			t.Errorf("read back %v, after coarse lifecycle start: start %v exact=%v, want exact %v", readBack, got.StartedAt, got.TimestampsExact, line)
		}
	}

	for name, events := range map[string][]codexlog.TurnEvent{
		"task_started first": {
			{Kind: "task_started", Offset: 1, TurnID: "b", StartedAt: t0},
			{Kind: "task_complete", Offset: 2, TurnID: "b", StartedAt: t0.Add(time.Second)},
		},
		"task_started last": {
			{Kind: "task_complete", Offset: 1, TurnID: "b", StartedAt: t0.Add(time.Second)},
			{Kind: "task_started", Offset: 2, TurnID: "b", StartedAt: t0},
		},
	} {
		s := newSessionSnapshot()
		s.foldCodexTurns(events, 0, 10)
		if got := s.CodexTurns[0].StartedAt; !got.Equal(t0) {
			t.Errorf("%s: start %v, want task_started's %v", name, got, t0)
		}
		var reloaded SessionSnapshot
		if err := json.Unmarshal([]byte(encodeSnapshot(t, s)), &reloaded); err != nil {
			t.Fatal(err)
		}
		reloaded.foldCodexTurns([]codexlog.TurnEvent{{Kind: "turn_aborted", Offset: 3, At: line, TurnID: "b", StartedAt: t0.Add(2 * time.Second)}}, 0, 10)
		if got := reloaded.CodexTurns[0].StartedAt; !got.Equal(t0) {
			t.Errorf("%s, read back: start %v, want %v kept", name, got, t0)
		}
	}
}

func TestCodexTurnSnapshotPrefersTopLevelMillisecondTimestamps(t *testing.T) {
	startLine := time.Date(2026, 10, 9, 6, 20, 1, 339_000_000, time.UTC)
	endLine := time.Date(2026, 10, 9, 6, 20, 1, 352_000_000, time.UTC)
	payloadSecond := time.Date(2026, 10, 9, 6, 20, 1, 0, time.UTC)
	started := codexTurnOf(codexlog.TurnEvent{Kind: "task_started", TurnID: "root", At: startLine, StartedAt: payloadSecond})
	completed := codexTurnOf(codexlog.TurnEvent{Kind: "task_complete", TurnID: "root", At: endLine, CompletedAt: payloadSecond})
	if !started.StartedAt.Equal(startLine) || !started.TimestampsExact {
		t.Fatalf("start=%s exact=%v, want top-level %s", started.StartedAt, started.TimestampsExact, startLine)
	}
	started.update(completed)
	if !started.EndedAt.Equal(endLine) || !started.TimestampsExact {
		t.Fatalf("end=%s exact=%v, want top-level %s", started.EndedAt, started.TimestampsExact, endLine)
	}
}

// Once the list has been cut, the ending of a turn the cut took, read late, is
// dropped rather than taken in as the newest turn; the ending of a turn the list
// still holds is merged.
func TestCodexTurnsDropTheLateEndingOfACutTurn(t *testing.T) {
	var events []codexlog.TurnEvent
	for i := 0; i <= maxSnapshotCodexTurns; i++ {
		events = append(events, startedEvent(fmt.Sprintf("t-%03d", i), int64(i), t0.Add(time.Duration(i)*time.Minute)))
	}
	snap := newSessionSnapshot()
	snap.foldCodexTurns(events, 0, 1<<20)
	before := turnIDs(snap.CodexTurns)
	if before[0] != "t-001" {
		t.Fatalf("fixture: first kept turn %s", before[0])
	}
	newest := fmt.Sprintf("t-%03d", maxSnapshotCodexTurns)
	snap.foldCodexTurns([]codexlog.TurnEvent{
		{Kind: "task_complete", Offset: 1000, TurnID: "t-000", CompletedAt: t0.Add(time.Hour)},
		{Kind: "task_complete", Offset: 1001, TurnID: newest, CompletedAt: t0.Add(time.Hour)},
	}, 0, 1<<20)
	if got := turnIDs(snap.CodexTurns); !slices.Equal(got, before) {
		t.Errorf("turns after the late endings: %d, first %s; want the same %d", len(got), got[0], len(before))
	}
	if last := snap.CodexTurns[len(snap.CodexTurns)-1]; last.TurnID != newest || last.End != "complete" {
		t.Errorf("newest turn %s ended %q, want the kept turn's ending merged", last.TurnID, last.End)
	}
}

// A snapshot carries Codex's names -- model, effort, collaboration mode, abort
// reason, error kind -- only when they are short names: letters, digits and
// -_.:/, at most 64 bytes. Anything else is left out.
func TestCodexTurnKeepsOnlyShortNames(t *testing.T) {
	good := codexTurnOf(codexlog.TurnEvent{Kind: "turn_aborted", TurnID: "a", Model: "gpt-5.5-codex", Effort: "xhigh",
		CollaborationModeKind: "plan", Reason: "interrupted"})
	if good.Model != "gpt-5.5-codex" || good.Effort != "xhigh" || good.CollaborationModeKind != "plan" || good.AbortReason != "interrupted" {
		t.Errorf("short names dropped: %+v", good)
	}
	bad := codexTurnOf(codexlog.TurnEvent{Kind: "turn_aborted", TurnID: "b", Model: "gpt 5 (beta)", Effort: strings.Repeat("x", maxShortNameBytes+1),
		CollaborationModeKind: "défaut", Reason: "interrupted by the user"})
	if bad.Model != "" || bad.Effort != "" || bad.CollaborationModeKind != "" || bad.AbortReason != "" {
		t.Errorf("names that are not short names were kept: %+v", bad)
	}
	errored := codexTurnOf(codexlog.TurnEvent{Kind: "task_complete", TurnID: "c", Error: true, ErrorInfo: "message: the model said no"})
	if errored.End != "error" || errored.CodexErrorInfo != "" {
		t.Errorf("error turn %+v, want end error and the free text left out", errored)
	}
}

// A task_complete that carried an error ends its turn in "error", with its
// error kind when that is a short name.
func TestCodexTurnEndsInError(t *testing.T) {
	snap := newSessionSnapshot()
	snap.foldCodexTurns([]codexlog.TurnEvent{
		startedEvent("e-1", 1, t0),
		{Kind: "task_complete", Offset: 2, At: t0.Add(time.Minute), TurnID: "e-1", CompletedAt: t0.Add(time.Minute), Error: true, ErrorInfo: "context_window_exceeded"},
	}, 0, 10)
	got, _ := json.Marshal(snap.CodexTurns)
	want := `[{"turn_id":"e-1","started_at":"2026-10-01T09:00:00Z","ended_at":"2026-10-01T09:01:00Z","timestamps_exact":true,"end":"error","codex_error_info":"context_window_exceeded","collaboration_mode_kind":"default"}]`
	if string(got) != want {
		t.Errorf("turns\n got %s\nwant %s", got, want)
	}
}

// The snapshot of a rollout already sent and not grown for a week leaves the
// Codex state. When the rollout grows again, the snapshot is rebuilt from the
// part sync collected, older turns included, and sent again.
func TestCodexSnapshotOfAnIdleRolloutIsDroppedAndRebuilt(t *testing.T) {
	fakeClock(t)
	srv := newObservationServer(t)
	home := t.TempDir()
	path := filepath.Join(normalizeHome(home), "sessions", "rollout-2026-10-01T09-00-00-00000000-0000-0000-0000-0000000000d1.jsonl")
	turn := func(id string, at time.Time) []string {
		return []string{
			fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"task_started","turn_id":%q,"started_at":%d}}`, at.Format(time.RFC3339), id, at.Unix()),
			fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"task_complete","turn_id":%q,"completed_at":%d}}`, at.Add(time.Minute).Format(time.RFC3339), id, at.Add(time.Minute).Unix()),
		}
	}
	writeFile(t, path, strings.Join(append([]string{`{"type":"session_meta","timestamp":"2026-10-01T09:00:00Z","payload":{"id":"x","cwd":"/work/repo"}}`}, turn("turn-1", t0)...), "\n")+"\n")
	statePath := filepath.Join(t.TempDir(), "codex-state.json")
	codexState, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	scan := func(from int64) ([]codexlog.TurnEvent, int64) {
		t.Helper()
		_, _, turns, to, _, err := codexlog.ScanFileWithTurnsContext(context.Background(), path, from, "x", codexlog.Metadata{})
		if err != nil {
			t.Fatal(err)
		}
		return turns, to
	}
	turns, size := scan(0)
	codexState.SetOffset(path, size)
	fs := codexState.Files[path]
	AdvanceCodexSnapshot(context.Background(), fs, path, 0, size, "-work-repo", turns)
	fs.SessionSnapshot.SentRevision = fs.SessionSnapshot.Revision
	fs.LastSynced = nowFn().Add(-codexSnapshotIdle - time.Hour)
	// One not sent yet stays, however idle.
	unsentPath := filepath.Join(normalizeHome(home), "sessions", "rollout-2026-10-01T09-00-00-00000000-0000-0000-0000-0000000000d2.jsonl")
	unsent := newSessionSnapshot()
	unsent.addCodexTurns([]SnapshotCodexTurn{{TurnID: "u-1", StartedAt: t0}})
	codexState.Files[unsentPath] = &FileState{LastSynced: fs.LastSynced, SessionSnapshot: &SessionSnapshotState{Snapshot: unsent, Revision: 1, ProjectHash: "-work-repo", ObservedAt: t0}}
	// One sent whose rollout grew within the week stays too.
	recentPath := filepath.Join(normalizeHome(home), "sessions", "rollout-2026-10-01T09-00-00-00000000-0000-0000-0000-0000000000d3.jsonl")
	codexState.Files[recentPath] = &FileState{LastSynced: nowFn().Add(-codexSnapshotIdle + time.Hour), SessionSnapshot: &SessionSnapshotState{Snapshot: unsent.clone(), Revision: 1, SentRevision: 1, ProjectHash: "-work-repo", ObservedAt: t0}}
	if err := codexState.Save(); err != nil {
		t.Fatal(err)
	}
	s, _ := newSnapshotSyncer(t, t.TempDir(), srv.URL())
	s.ServeCodexSnapshots(codexState, []string{home})
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Files[path].SessionSnapshot != nil || fs.SessionSnapshot != nil {
		t.Fatal("the snapshot of an idle rollout already sent stayed in the state")
	}
	if codexState.Files[unsentPath].SessionSnapshot == nil {
		t.Fatal("the snapshot of an idle rollout not yet sent was dropped")
	}
	if codexState.Files[recentPath].SessionSnapshot == nil {
		t.Fatal("the snapshot of a rollout that grew within the week was dropped")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(turn("turn-2", t0.Add(time.Hour)), "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	more, newSize := scan(size)
	AdvanceCodexSnapshot(context.Background(), fs, path, size, newSize, "-work-repo", more)
	if got := turnIDs(fs.SessionSnapshot.Snapshot.CodexTurns); !slices.Equal(got, []string{"turn-1", "turn-2"}) {
		t.Fatalf("rebuilt turns %v, want the old turn and the new one", got)
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	last := reqs[len(reqs)-1]
	if len(last.Snapshots) != 1 || len(last.Snapshots[0].Snapshot.CodexTurns) != 2 || last.Snapshots[0].SessionID != "00000000-0000-0000-0000-0000000000d1" {
		t.Errorf("sent %+v, want the rebuilt snapshot with both turns", last.Snapshots)
	}
}
