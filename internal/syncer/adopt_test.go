package syncer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// under maps a previous state's keys below from to the same names below to.
func under(from, to string) func(string) (string, bool) {
	return func(prev string) (string, bool) {
		rel, ok := strings.CutPrefix(prev, from+string(filepath.Separator))
		if !ok {
			return "", false
		}
		return filepath.Join(to, rel), true
	}
}

func TestAdoptSourceContinuesFromThePreviousHoldersCursor(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	from, to, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	prev := &State{Files: map[string]*FileState{
		filepath.Join(from, "a.jsonl"): {Offset: 120, LastSynced: t0, CWD: "/work/p", Model: "m", SkippedAtFirstSync: 40, ExcludedSeen: true,
			Held:         map[string]HeldRun{"/work/q": {Since: t0, Reason: "no repo"}},
			ConflictTail: &ConflictTail{Offset: 120, Anchor: "x"}, SessionSnapshot: &SessionSnapshotState{}, CollectedFrom: 7},
		filepath.Join(from, "sub", "b.jsonl"): {Offset: 5},
		filepath.Join(elsewhere, "c.jsonl"):   {Offset: 9},
	}, Taints: map[string][]Taint{filepath.Join(from, "a.jsonl"): {{}}, filepath.Join(elsewhere, "c.jsonl"): {{}}},
		RulesDenied: map[string]time.Time{"repo": t0}, TransportFailure: &TransportFailure{Class: "transport"}, ServerExcludedAccounts: map[string]time.Time{"codex:a": t0},
		CWDIdentity: map[string]*CWDIdentity{"/work/p": {}}}
	next := &State{Files: map[string]*FileState{
		filepath.Join(to, "a.jsonl"):  {Offset: 3},
		filepath.Join(to, "own.json"): {Offset: 1},
	}, RulesDenied: map[string]time.Time{"mine": t0}}

	n := next.AdoptSource(prev, Adoption{Path: under(from, to)})
	if n != 2 {
		t.Fatalf("adopted %d files, want the two under the source", n)
	}
	got := next.Files[filepath.Join(to, "a.jsonl")]
	want := *prev.Files[filepath.Join(from, "a.jsonl")]
	if got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("a.jsonl = %+v, want the previous holder's %+v (it is the newer cursor)", got, want)
	}
	if next.Files[filepath.Join(to, "sub", "b.jsonl")].Offset != 5 {
		t.Fatal("a nested file was not carried over")
	}
	if next.HasFile(filepath.Join(elsewhere, "c.jsonl")) || next.HasFile(filepath.Join(to, "c.jsonl")) {
		t.Fatal("a file of another source was taken")
	}
	if next.Files[filepath.Join(to, "own.json")].Offset != 1 {
		t.Fatal("the new owner's own cursors were disturbed")
	}
	if len(next.Taints[filepath.Join(to, "a.jsonl")]) != 1 || len(next.Taints) != 1 {
		t.Fatalf("taints = %+v", next.Taints)
	}
	if next.TransportFailure != nil || next.CWDIdentity != nil || len(next.ServerExcludedAccounts) != 0 || len(next.RulesDenied) != 1 {
		t.Fatalf("what is about the previous holder's own transport, server and caches was taken: %+v", next)
	}
	// A copy, not a share: the previous state is not the new one's to change.
	next.Files[filepath.Join(to, "a.jsonl")].Offset = 999
	if prev.Files[filepath.Join(from, "a.jsonl")].Offset != 120 {
		t.Fatal("the adopted entry is the previous state's own")
	}
}

func TestAdoptSourceMergesTheAccountsSeenForThatHomeOnly(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	prevHome, newHome := "/prev/home", "/new/home"
	prev := &State{Files: map[string]*FileState{},
		ClaudeAccountObservations: []ClaudeAccountObservation{
			{ObservedAt: t0, Home: prevHome, AccountUUID: "u1"},
			{ObservedAt: t0.Add(2 * time.Hour), Home: prevHome, AccountUUID: "u2"},
			{ObservedAt: t0, Home: "/other/home", AccountUUID: "x"},
		},
		CodexAccountObservations: []CodexAccountObservation{
			{ObservedAt: t0.Add(time.Hour), Home: prevHome, AccountID: "c1"},
			{ObservedAt: t0, Home: "/other/home", AccountID: "y"},
		}}
	next := &State{Files: map[string]*FileState{},
		ClaudeAccountObservations: []ClaudeAccountObservation{
			{ObservedAt: t0.Add(time.Hour), Home: newHome, AccountUUID: "u-mid"},
			{ObservedAt: t0, Home: newHome, AccountUUID: "u1"}, // already known: not duplicated
		}}
	rehome := func(h string) (string, bool) { return newHome, h == prevHome }
	next.AdoptSource(prev, Adoption{Path: func(string) (string, bool) { return "", false }, Home: rehome})

	if got := next.ClaudeAccountAt(newHome, t0.Add(30*time.Minute)); got != "u1" {
		t.Fatalf("account at +30m = %q, want u1", got)
	}
	if got := next.ClaudeAccountAt(newHome, t0.Add(3*time.Hour)); got != "u2" {
		t.Fatalf("account at +3h = %q, want the previous holder's later observation", got)
	}
	count := 0
	for _, o := range next.ClaudeAccountObservations {
		if o.AccountUUID == "u1" {
			count++
		}
		if o.Home == "/other/home" {
			t.Fatal("another home's account was taken")
		}
	}
	if count != 1 {
		t.Fatalf("u1 appears %d times", count)
	}
	for i := 1; i < len(next.ClaudeAccountObservations); i++ {
		if next.ClaudeAccountObservations[i].ObservedAt.Before(next.ClaudeAccountObservations[i-1].ObservedAt) {
			t.Fatalf("observations are not in time order: %+v", next.ClaudeAccountObservations)
		}
	}
	if got := next.CodexAccountAt(newHome, t0.Add(2*time.Hour)); got != "c1" || len(next.CodexAccountObservations) != 1 {
		t.Fatalf("codex account = %q (%+v)", got, next.CodexAccountObservations)
	}
}

func TestAdoptSourceDeclinesSnapshotRebuildForAnAllowlistHolder(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	key := filepath.Join(from, "a.jsonl")
	for _, decline := range []bool{true, false} {
		prev := &State{Files: map[string]*FileState{key: {Offset: 4}}}
		next := &State{Files: map[string]*FileState{}}
		next.AdoptSource(prev, Adoption{Path: under(from, to), DeclineRebuild: decline})
		if got := next.Files[filepath.Join(to, "a.jsonl")].RebuildDeclined; got != decline {
			t.Fatalf("decline=%v gave RebuildDeclined=%v", decline, got)
		}
	}
	// A decline already recorded is kept when the new adoption does not ask.
	prev := &State{Files: map[string]*FileState{key: {Offset: 4, RebuildDeclined: true}}}
	next := &State{Files: map[string]*FileState{}}
	next.AdoptSource(prev, Adoption{Path: under(from, to)})
	if !next.Files[filepath.Join(to, "a.jsonl")].RebuildDeclined {
		t.Fatal("a recorded decline was lost")
	}
}

// The conflict ledger stays valid across the hand-over because it is proved by
// the bytes in front of the offset, not by the name of the file.
func TestAdoptSourceKeepsTheConflictLedgerTrustedUnderAnotherSpelling(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	file := filepath.Join(real, "s.jsonl")
	if err := os.WriteFile(file, []byte(strings.Repeat("{\"x\":1}\n", 40)), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := int64(8 * 30)
	prev := &State{Files: map[string]*FileState{file: {Offset: offset, ConflictTail: &ConflictTail{Offset: offset, Anchor: ConflictAnchor(file, offset)}}}}
	next := &State{Files: map[string]*FileState{}}
	next.AdoptSource(prev, Adoption{Path: under(real, alias)})
	adopted := filepath.Join(alias, "s.jsonl")
	if tail, trusted := next.ConflictSeed(adopted, offset); !trusted || tail == nil {
		t.Fatalf("the ledger of the file under its new name is not trusted: %v %v", tail, trusted)
	}
}
