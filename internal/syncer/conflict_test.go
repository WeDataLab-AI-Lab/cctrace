package syncer

import (
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/store"
)

func conflictRecord(ts time.Time) *store.SessionRecord {
	return &store.SessionRecord{
		SessionID:    "ses-001",
		RecordType:   "assistant",
		ProfileEmail: "user@example.com",
		Ts:           ts,
	}
}

func TestConflictNudger_CountsWithinOneScan(t *testing.T) {
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	n := NewConflictNudger(nil)

	first, second := conflictRecord(ts), conflictRecord(ts)
	n.Apply(first)
	n.Apply(second)

	if !first.Ts.Equal(ts) {
		t.Fatalf("first ts = %s, want %s untouched", first.Ts, ts)
	}
	if want := ts.Add(time.Microsecond); !second.Ts.Equal(want) {
		t.Fatalf("second ts = %s, want %s", second.Ts, want)
	}
}

// Codex developer messages were stored as record_type='user' until codexlog split
// them out, and they routinely share a timestamp with the user message beside them.
// The rows already on the server carry the nudge that shared key produced, so a
// re-sync has to reproduce it or the upsert key misses and the row is duplicated.
func TestConflictNudger_DeveloperSharesTheUserKey(t *testing.T) {
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	n := NewConflictNudger(nil)

	developer, user := conflictRecord(ts), conflictRecord(ts)
	developer.RecordType, user.RecordType = "developer", "user"
	n.Apply(developer)
	n.Apply(user)

	if want := ts.Add(time.Microsecond); !user.Ts.Equal(want) {
		t.Fatalf("user ts = %s, want %s as when developer was stored as user", user.Ts, want)
	}
}

// The ledger is what turns a per-scan counter into a per-file one: a run of
// same-timestamp records split across two scans must keep counting where the
// first scan stopped, or the second half re-claims the first half's keys.
func TestConflictNudger_ResumesFromTheTail(t *testing.T) {
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	tail := &ConflictTail{Ts: ts, Counts: map[string]int{"ses-001|assistant|user@example.com": 2}}
	n := NewConflictNudger(tail)

	third, fourth := conflictRecord(ts), conflictRecord(ts)
	n.Apply(third)
	n.Apply(fourth)

	if want := ts.Add(2 * time.Microsecond); !third.Ts.Equal(want) {
		t.Fatalf("third ts = %s, want %s", third.Ts, want)
	}
	if want := ts.Add(3 * time.Microsecond); !fourth.Ts.Equal(want) {
		t.Fatalf("fourth ts = %s, want %s", fourth.Ts, want)
	}
	if got := n.Tail().Counts["ses-001|assistant|user@example.com"]; got != 4 {
		t.Fatalf("tail count = %d, want 4", got)
	}
}

// Only the newest timestamp is carried, so an older one has to be forgotten or
// the ledger grows with the file.
func TestConflictNudger_NewerTimestampReplacesTheTail(t *testing.T) {
	first := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	later := first.Add(time.Second)
	n := NewConflictNudger(nil)

	n.Apply(conflictRecord(first))
	n.Apply(conflictRecord(later))

	tail := n.Tail()
	if !tail.Ts.Equal(later) {
		t.Fatalf("tail ts = %s, want %s", tail.Ts, later)
	}
	if len(tail.Counts) != 1 || tail.Counts["ses-001|assistant|user@example.com"] != 1 {
		t.Fatalf("tail counts = %v, want a single count of 1 at the newer timestamp", tail.Counts)
	}
}

// A scan that emitted nothing must hand back the ledger it was given, or the
// next scan starts counting from zero against records that are already stored.
func TestConflictNudger_EmptyScanKeepsTheSeededTail(t *testing.T) {
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	tail := &ConflictTail{Ts: ts, Counts: map[string]int{"ses-001|assistant|user@example.com": 3}}

	got := NewConflictNudger(tail).Tail()
	if got == nil || !got.Ts.Equal(ts) || got.Counts["ses-001|assistant|user@example.com"] != 3 {
		t.Fatalf("tail = %+v, want the seed back unchanged", got)
	}
}

// The daemon restarts -- on upgrade, on reboot, on a crash -- and a run of
// same-timestamp records split across that restart has to keep counting, so the
// ledger has to survive the state file rather than living only in memory.
func TestConflictTail_SurvivesTheStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	file := "/home/user/.codex/sessions/rollout.jsonl"

	n := NewConflictNudger(nil)
	n.Apply(conflictRecord(ts))
	n.Apply(conflictRecord(ts))
	state.SetOffset(file, 100)
	state.AdvanceConflictTail(file, 0, 100, true, n)
	if err := state.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	resumed := NewConflictNudger(reloaded.ConflictTail(file))
	third := conflictRecord(ts)
	resumed.Apply(third)
	if want := ts.Add(2 * time.Microsecond); !third.Ts.Equal(want) {
		t.Fatalf("ts after restart = %s, want %s", third.Ts, want)
	}
}

// A pass whose offset was rewound re-reads the same records next time. If its
// ledger were written anyway, the re-read would nudge them a second time and
// land on keys the server does not hold -- duplicating instead of deduplicating.
func TestState_AdvanceConflictTailIgnoresARewoundOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	file := "/home/user/.codex/sessions/rollout.jsonl"
	state.SetOffset(file, 100)
	state.SetConflictTail(file, &ConflictTail{Ts: ts, Counts: map[string]int{"ses-001|assistant|user@example.com": 1}})

	n := NewConflictNudger(state.ConflictTail(file))
	n.Apply(conflictRecord(ts))
	state.AdvanceConflictTail(file, 100, 100, true, n)

	if got := state.ConflictTail(file).Counts["ses-001|assistant|user@example.com"]; got != 1 {
		t.Fatalf("tail count = %d, want 1 (a rewound pass must not advance the ledger)", got)
	}
}
