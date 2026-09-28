package syncer

import (
	"testing"
	"time"
)

// The Claude observation log mirrors the Codex one: it exists so records
// collected before the first observation stay unattributed instead of being
// stamped with today's account.
func TestObserveClaudeAccountAppendsOnlyOnChange(t *testing.T) {
	s := &State{Files: map[string]*FileState{}}
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if !s.ObserveClaudeAccount(t0, "/home/.claude", "acct-one") {
		t.Fatal("first observation was not recorded")
	}
	// Claude Code rewrites .claude.json constantly (caches, project paths); only a
	// changed account is a switch, which is why the file's mtime is never used.
	if s.ObserveClaudeAccount(t0.Add(time.Minute), "/home/.claude", "acct-one") {
		t.Fatal("unchanged account appended a second observation")
	}
	if !s.ObserveClaudeAccount(t0.Add(time.Hour), "/home/.claude", "acct-two") {
		t.Fatal("account change was not recorded")
	}
	if len(s.ClaudeAccountObservations) != 2 {
		t.Fatalf("observations = %d, want 2", len(s.ClaudeAccountObservations))
	}
}

// Homes are separate logins. Comparing against the log's last entry regardless
// of home would make alternating passes over two homes append forever, and would
// let one home's account answer for another's.
func TestObserveClaudeAccountIsPerHome(t *testing.T) {
	s := &State{Files: map[string]*FileState{}}
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	s.ObserveClaudeAccount(t0, "/home/.claude", "acct-one")
	s.ObserveClaudeAccount(t0, "/home/.claude-2", "acct-two")
	if s.ObserveClaudeAccount(t0.Add(time.Minute), "/home/.claude", "acct-one") {
		t.Fatal("unchanged account appended after another home was observed")
	}
	if got := s.ClaudeAccountAt("/home/.claude", t0.Add(time.Hour)); got != "acct-one" {
		t.Fatalf("account = %q, want acct-one", got)
	}
	if got := s.ClaudeAccountAt("/home/.claude-2", t0.Add(time.Hour)); got != "acct-two" {
		t.Fatalf("account = %q, want acct-two", got)
	}
}

func TestObserveClaudeAccountIgnoresBlanks(t *testing.T) {
	s := &State{Files: map[string]*FileState{}}
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if s.ObserveClaudeAccount(t0, "", "acct-one") {
		t.Error("blank home was recorded")
	}
	if s.ObserveClaudeAccount(t0, "/home/.claude", "") {
		t.Error("blank account was recorded")
	}
	if len(s.ClaudeAccountObservations) != 0 {
		t.Fatalf("observations = %d, want 0", len(s.ClaudeAccountObservations))
	}
}

// A record older than its home's first observation is unattributed on purpose.
// Stamping it with the account observed later is the retroactive mislabelling
// this log exists to prevent, and another home is never a fallback.
func TestClaudeAccountAtRefusesToGuess(t *testing.T) {
	s := &State{Files: map[string]*FileState{}}
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	s.ObserveClaudeAccount(t0, "/home/.claude", "acct-one")

	if got := s.ClaudeAccountAt("/home/.claude", t0.Add(-time.Hour)); got != "" {
		t.Errorf("pre-observation record got %q, want empty", got)
	}
	if got := s.ClaudeAccountAt("/home/.claude-never-seen", t0.Add(time.Hour)); got != "" {
		t.Errorf("unobserved home got %q, want empty", got)
	}
	if got := s.ClaudeAccountAt("", t0.Add(time.Hour)); got != "" {
		t.Errorf("blank home got %q, want empty", got)
	}
}

// A record between two observations belongs to the earlier one: that is the
// account that was active when it was written.
func TestClaudeAccountAtPicksTheIntervalTheRecordFallsIn(t *testing.T) {
	s := &State{Files: map[string]*FileState{}}
	t0 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	switched := t0.Add(2 * time.Hour)

	s.ObserveClaudeAccount(t0, "/home/.claude", "acct-one")
	s.ObserveClaudeAccount(switched, "/home/.claude", "acct-two")

	for _, tc := range []struct {
		ts   time.Time
		want string
	}{
		{t0, "acct-one"},
		{t0.Add(time.Hour), "acct-one"},
		{switched.Add(-time.Second), "acct-one"},
		{switched, "acct-two"},
		{switched.Add(time.Hour), "acct-two"},
	} {
		if got := s.ClaudeAccountAt("/home/.claude", tc.ts); got != tc.want {
			t.Errorf("at %v got %q, want %q", tc.ts.Sub(t0), got, tc.want)
		}
	}
}
