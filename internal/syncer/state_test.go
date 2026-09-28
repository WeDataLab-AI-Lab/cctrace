package syncer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A token refresh rewrites auth.json without changing the account id, so the
// same id observed again is not a new observation. Only a real account switch
// appends, which is what makes the two distinguishable later.
func TestObserveCodexAccountAppendsOnlyOnChange(t *testing.T) {
	s := &State{}
	t0 := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

	if !s.ObserveCodexAccount(t0, "codex-home-a", "acct-a") {
		t.Fatal("first observation should append")
	}
	if s.ObserveCodexAccount(t0.Add(time.Hour), "codex-home-a", "acct-a") {
		t.Fatal("same account id should not append")
	}
	if len(s.CodexAccountObservations) != 1 {
		t.Fatalf("observations = %d, want 1", len(s.CodexAccountObservations))
	}

	if !s.ObserveCodexAccount(t0.Add(2*time.Hour), "codex-home-a", "acct-b") {
		t.Fatal("switched account id should append")
	}
	if len(s.CodexAccountObservations) != 2 {
		t.Fatalf("observations = %d, want 2", len(s.CodexAccountObservations))
	}
	if s.CodexAccountObservations[1].AccountID != "acct-b" {
		t.Fatalf("second observation = %q", s.CodexAccountObservations[1].AccountID)
	}
}

func TestObserveCodexAccountIgnoresEmpty(t *testing.T) {
	s := &State{}
	if s.ObserveCodexAccount(time.Now(), "codex-home-a", "") {
		t.Fatal("empty account id should not append")
	}
	if s.ObserveCodexAccount(time.Now(), "", "acct-a") {
		t.Fatal("empty home should not append")
	}
	if len(s.CodexAccountObservations) != 0 {
		t.Fatalf("observations = %d, want 0", len(s.CodexAccountObservations))
	}
}

// Each Codex home has its own account. An observation from one home must
// neither suppress an observation from another nor answer a lookup for it.
func TestObserveCodexAccountIsPerHome(t *testing.T) {
	s := &State{}
	t0 := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

	if !s.ObserveCodexAccount(t0, "codex-home-a", "acct-a") {
		t.Fatal("home a observation should append")
	}
	if !s.ObserveCodexAccount(t0, "codex-home-b", "acct-b") {
		t.Fatal("home b observation should append despite a different home holding another id")
	}
	if s.ObserveCodexAccount(t0.Add(time.Hour), "codex-home-b", "acct-b") {
		t.Fatal("repeating home b's own id should not append")
	}
	if len(s.CodexAccountObservations) != 2 {
		t.Fatalf("observations = %d, want 2", len(s.CodexAccountObservations))
	}
	if got := s.CodexAccountAt("codex-home-a", t0); got != "acct-a" {
		t.Fatalf("home a account = %q, want acct-a", got)
	}
	if got := s.CodexAccountAt("codex-home-b", t0); got != "acct-b" {
		t.Fatalf("home b account = %q, want acct-b", got)
	}
	// A home that was never observed is unattributable, never another home's id.
	if got := s.CodexAccountAt("codex-home-c", t0); got != "" {
		t.Fatalf("unobserved home account = %q, want empty", got)
	}
}

// Observations written before the home field existed cannot be attributed to a
// home, and guessing one is the defect this field exists to prevent.
func TestCodexAccountAtIgnoresHomelessObservations(t *testing.T) {
	t0 := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	s := &State{CodexAccountObservations: []CodexAccountObservation{{ObservedAt: t0, AccountID: "acct-legacy"}}}

	if got := s.CodexAccountAt("codex-home-a", t0.Add(time.Hour)); got != "" {
		t.Fatalf("CodexAccountAt = %q, want empty", got)
	}
	if got := s.CodexAccountAt("", t0.Add(time.Hour)); got != "" {
		t.Fatalf("CodexAccountAt with empty home = %q, want empty", got)
	}
}

func TestCodexAccountAt(t *testing.T) {
	t0 := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	s := &State{}
	s.ObserveCodexAccount(t0, "codex-home-a", "acct-a")
	s.ObserveCodexAccount(t0.Add(2*time.Hour), "codex-home-a", "acct-b")

	cases := []struct {
		name string
		ts   time.Time
		want string
	}{
		// Records that predate the first observation are unattributable: the
		// account in use back then was never seen, and the current one is a guess.
		{"before first observation", t0.Add(-time.Minute), ""},
		{"at first observation", t0, "acct-a"},
		{"between observations", t0.Add(time.Hour), "acct-a"},
		{"at second observation", t0.Add(2 * time.Hour), "acct-b"},
		{"after last observation", t0.Add(72 * time.Hour), "acct-b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.CodexAccountAt("codex-home-a", tc.ts); got != tc.want {
				t.Fatalf("CodexAccountAt = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodexAccountAtWithoutObservations(t *testing.T) {
	s := &State{}
	if got := s.CodexAccountAt("codex-home-a", time.Now()); got != "" {
		t.Fatalf("CodexAccountAt = %q, want empty", got)
	}
}

func TestStateRoundTripsCodexAccountObservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-state.json")
	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	t0 := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	s.ObserveCodexAccount(t0, "codex-home-a", "acct-a")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(loaded.CodexAccountObservations) != 1 {
		t.Fatalf("observations = %d, want 1", len(loaded.CodexAccountObservations))
	}
	if got := loaded.CodexAccountAt("codex-home-a", t0); got != "acct-a" {
		t.Fatalf("CodexAccountAt = %q", got)
	}
	// The reloaded state must also recognise the same id as already observed.
	if loaded.ObserveCodexAccount(t0.Add(time.Hour), "codex-home-a", "acct-a") {
		t.Fatal("reloaded state should not re-append the same account id")
	}
}

// Existing sync-state files have no observation list; loading one must keep
// working and simply report no observations.
func TestLoadStateWithoutCodexAccountObservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-state.json")
	if err := os.WriteFile(path, []byte(`{"files":{"/a.jsonl":{"offset":42}}}`), 0600); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if s.GetOffset("/a.jsonl") != 42 {
		t.Fatalf("offset = %d, want 42", s.GetOffset("/a.jsonl"))
	}
	if len(s.CodexAccountObservations) != 0 {
		t.Fatalf("observations = %d, want 0", len(s.CodexAccountObservations))
	}
}

// A sync-state file written before observations carried a home must load
// without error. Its entries are kept but attribute nothing: which home they
// came from is unknown, and the next pass re-observes every home anyway.
func TestLoadStateWithHomelessCodexAccountObservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-state.json")
	body := `{"files":{},"codex_account_observations":[{"observed_at":"2026-08-13T10:00:00Z","account_id":"acct-legacy"}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(s.CodexAccountObservations) != 1 {
		t.Fatalf("observations = %d, want 1", len(s.CodexAccountObservations))
	}
	if s.CodexAccountObservations[0].Home != "" {
		t.Fatalf("legacy home = %q, want empty", s.CodexAccountObservations[0].Home)
	}
	ts := time.Date(2026, 8, 13, 11, 0, 0, 0, time.UTC)
	if got := s.CodexAccountAt("codex-home-a", ts); got != "" {
		t.Fatalf("CodexAccountAt = %q, want empty", got)
	}
	// The legacy entry must not suppress the first real observation for a home.
	if !s.ObserveCodexAccount(ts, "codex-home-a", "acct-legacy") {
		t.Fatal("first per-home observation should append")
	}
}
