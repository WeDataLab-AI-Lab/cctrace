package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeLedgerFile writes session-log-shaped filler and returns its path, so
// ConflictAnchor has real bytes to fingerprint.
func writeLedgerFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newLedgerState(t *testing.T) *State {
	t.Helper()
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// The ledger is a claim about which bytes were counted, and a scan may act on it
// only where the claim is checkable. A state file written before the ledger
// existed makes no claim at all -- every client already in the field has one --
// and reading its silence as "the prefix placed no records" seeds the next scan
// with a suffix's totals, which then get recorded as if they were the file's.
func TestState_ConflictSeedRefusesAStateThatPredatesTheLedger(t *testing.T) {
	path := writeLedgerFile(t, strings.Repeat("a", 400))
	state := newLedgerState(t)
	state.SetOffset(path, 200)

	if seed, trusted := state.ConflictSeed(path, 200); trusted || seed != nil {
		t.Fatalf("ConflictSeed = (%v, %v), want (nil, false) for a state with no ledger", seed, trusted)
	}
}

// A scan that starts at byte zero skips nothing, so counting from zero describes
// the prefix by definition. This is the only place a ledger can be born.
func TestState_ConflictSeedTrustsAScanFromByteZero(t *testing.T) {
	path := writeLedgerFile(t, strings.Repeat("a", 400))
	state := newLedgerState(t)

	if seed, trusted := state.ConflictSeed(path, 0); !trusted || seed != nil {
		t.Fatalf("ConflictSeed = (%v, %v), want (nil, true) at offset zero", seed, trusted)
	}
}

// A byte offset is not an identity. Codex rewrites rollouts in place when it
// migrates them to paginated history, and afterwards the same offset lands on a
// different record while the number itself is unchanged -- so the offset alone
// cannot say whether the ledger still describes what is in front of it.
func TestState_ConflictSeedRefusesALedgerWhoseFileWasRewritten(t *testing.T) {
	path := writeLedgerFile(t, strings.Repeat("a", 400))
	state := newLedgerState(t)
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	n := NewConflictNudger(nil)
	n.Apply(conflictRecord(ts))
	state.SetOffset(path, 200)
	state.AdvanceConflictTail(path, 0, 200, true, n)

	if _, trusted := state.ConflictSeed(path, 200); !trusted {
		t.Fatal("ConflictSeed refused a ledger it had just recorded for these bytes")
	}

	// Same length, different content: the shrink check never sees this.
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 400)), 0o644); err != nil {
		t.Fatal(err)
	}
	if seed, trusted := state.ConflictSeed(path, 200); trusted || seed != nil {
		t.Fatalf("ConflictSeed = (%v, %v), want (nil, false) after the file was rewritten under the ledger", seed, trusted)
	}
}

// The offset moving without the ledger moving means something wrote one and not
// the other. Whatever the cause, the counts no longer describe the prefix.
func TestState_ConflictSeedRefusesALedgerLeftBehindByTheOffset(t *testing.T) {
	path := writeLedgerFile(t, strings.Repeat("a", 400))
	state := newLedgerState(t)
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	n := NewConflictNudger(nil)
	n.Apply(conflictRecord(ts))
	state.AdvanceConflictTail(path, 0, 200, true, n)

	if seed, trusted := state.ConflictSeed(path, 300); trusted || seed != nil {
		t.Fatalf("ConflictSeed = (%v, %v), want (nil, false) for a ledger describing another offset", seed, trusted)
	}
}

// An untrusted scan counted from zero over a prefix nobody counted. Recording
// its totals would make the next scan trust a ledger built from a suffix, which
// is how one unproven start spreads forward into every later pass.
func TestState_AdvanceConflictTailDropsTheLedgerOfAnUntrustedScan(t *testing.T) {
	path := writeLedgerFile(t, strings.Repeat("a", 400))
	state := newLedgerState(t)
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	state.SetOffset(path, 200)
	state.SetConflictTail(path, &ConflictTail{
		Ts:     ts,
		Counts: map[string]int{"ses-001|assistant|user@example.com": 7},
		Offset: 200,
	})

	n := NewConflictNudger(nil)
	n.Apply(conflictRecord(ts))
	state.AdvanceConflictTail(path, 200, 300, false, n)

	if got := state.ConflictTail(path); got != nil {
		t.Fatalf("tail = %+v, want nil: an unproven scan must not leave a ledger behind", got)
	}
}

// A prefix that placed no records is not the same as a prefix nobody counted.
// Collapsing the two drops a file back to the pre-ledger path the first time a
// scan consumes bytes that hold nothing to count.
func TestState_AnEmptyLedgerIsStillALedger(t *testing.T) {
	path := writeLedgerFile(t, strings.Repeat("a", 400))
	state := newLedgerState(t)
	state.SetOffset(path, 200)
	state.AdvanceConflictTail(path, 0, 200, true, NewConflictNudger(nil))

	if _, trusted := state.ConflictSeed(path, 200); !trusted {
		t.Fatal("a from-zero scan that applied no records left no ledger, so the next scan gives up the nudge")
	}
}

// The ledger goes through JSON on every pass, so the fields the trust check
// reads have to come back from the state file as they went in.
func TestConflictTail_OffsetAndAnchorSurviveTheStateFile(t *testing.T) {
	path := writeLedgerFile(t, strings.Repeat("a", 400))
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 8, 14, 10, 0, 2, 0, time.UTC)
	n := NewConflictNudger(nil)
	n.Apply(conflictRecord(ts))
	state.SetOffset(path, 200)
	state.AdvanceConflictTail(path, 0, 200, true, n)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, trusted := reloaded.ConflictSeed(path, 200); !trusted {
		t.Fatal("a ledger recorded before a restart is refused after it, so the nudge stops at every daemon restart")
	}
}
