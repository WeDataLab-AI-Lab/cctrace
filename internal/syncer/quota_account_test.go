package syncer

import (
	"testing"
	"time"
)

func at(min int) time.Time {
	return time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC).Add(time.Duration(min) * time.Minute)
}

// observe builds a state whose observation log for one home reads as the given
// sequence, one observation every ten minutes.
func observe(accounts ...string) *State {
	s := &State{}
	for i, a := range accounts {
		s.CodexAccountObservations = append(s.CodexAccountObservations, CodexAccountObservation{
			Home:       "/home/user/.codex",
			AccountID:  a,
			ObservedAt: at(i * 10),
		})
	}
	return s
}

// A reading inside a gap whose ends name the same account belongs to it, and
// nothing about that is uncertain.
func TestQuotaAccountAt_gapBetweenSameAccountIsObserved(t *testing.T) {
	// a a b b a b * b c — the reading sits between the b at minute 50 and the b
	// at minute 60. (A real log never repeats an account back to back, since
	// ObserveCodexAccount drops a repeat of the standing one; the sequence is
	// written out in full here because the rule must hold for any log.)
	s := observe("a", "a", "b", "b", "a", "b", "b", "c")

	got, inferred := s.QuotaAccountAt("/home/user/.codex", at(55))
	if got != "b" {
		t.Errorf("account = %q, want b", got)
	}
	if inferred {
		t.Error("marked inferred, but both ends of the gap name the same account")
	}
}

// An observation lags the switch it records: the daemon stamps the new account
// when it next looks, not when the user actually ran /login. So a reading in a
// gap whose ends differ is credited to the later account — the switch is
// assumed at the start of the gap.
//
// Assuming the other end leaks. If b is an excluded account and the gap is
// credited to a, b's usage appears on screen wearing a's label. Erring toward
// the later account errs toward hiding rather than exposing.
func TestQuotaAccountAt_gapBetweenDifferentAccountsTakesTheLater(t *testing.T) {
	// a a * b b — the reading sits between the last a and the first b.
	s := observe("a", "a", "b", "b")

	got, inferred := s.QuotaAccountAt("/home/user/.codex", at(15))
	if got != "b" {
		t.Errorf("account = %q, want b (the switch is assumed at the start of the gap)", got)
	}
	if !inferred {
		t.Error("not marked inferred, but the account was chosen across a gap")
	}
}

// Everything before the first observation is one such gap with no earlier end.
// This is the whole of the Codex backfill: the observation log starts when the
// feature ships, and the files being walked predate it.
//
// The existing CodexAccountAt answers "" here on purpose, which is right for
// session cost attribution and would make the backfill insert nothing.
func TestQuotaAccountAt_beforeTheFirstObservation(t *testing.T) {
	s := observe("a", "b")

	got, inferred := s.QuotaAccountAt("/home/user/.codex", at(-500))
	if got != "a" {
		t.Errorf("account = %q, want the first observed account", got)
	}
	if !inferred {
		t.Error("not marked inferred, but nothing was observed at or before this instant")
	}

	// The rule the session path keeps is deliberately different, and this change
	// must not have moved it.
	if legacy := s.CodexAccountAt("/home/user/.codex", at(-500)); legacy != "" {
		t.Errorf("CodexAccountAt = %q, want \"\" — session attribution must stay unguessed", legacy)
	}
}

// After the last observation there is no gap to reason about: this is the live
// case, where the reading and the observation are effectively simultaneous.
func TestQuotaAccountAt_afterTheLastObservationIsObserved(t *testing.T) {
	s := observe("a", "b")

	got, inferred := s.QuotaAccountAt("/home/user/.codex", at(500))
	if got != "b" {
		t.Errorf("account = %q, want b", got)
	}
	if inferred {
		t.Error("marked inferred, but b is the standing observation")
	}
}

// An instant that lands exactly on an observation is observed, not inferred.
func TestQuotaAccountAt_exactObservationInstant(t *testing.T) {
	s := observe("a", "b")

	got, inferred := s.QuotaAccountAt("/home/user/.codex", at(10))
	if got != "b" || inferred {
		t.Errorf("account = %q inferred = %v, want b/observed", got, inferred)
	}
}

// With nothing observed there is no account to credit, and inventing one is the
// mis-attribution the account key exists to prevent.
func TestQuotaAccountAt_noObservationsYieldsNothing(t *testing.T) {
	s := &State{}
	if got, _ := s.QuotaAccountAt("/home/user/.codex", at(0)); got != "" {
		t.Errorf("account = %q, want empty", got)
	}
}

// Another home's account is never a fallback, exactly as for the session path.
func TestQuotaAccountAt_neverBorrowsAnotherHome(t *testing.T) {
	s := observe("a", "b")
	if got, _ := s.QuotaAccountAt("/opt/tools/my-cctrace", at(0)); got != "" {
		t.Errorf("account = %q, want empty for an unobserved home", got)
	}
}

// The Claude observation log is read by the same rule, so both providers agree
// on what a gap means.
func TestQuotaAccountAt_readsClaudeObservationsToo(t *testing.T) {
	s := &State{}
	s.ClaudeAccountObservations = []ClaudeAccountObservation{
		{Home: "/home/user/.claude", AccountUUID: "uuid-a", ObservedAt: at(0)},
		{Home: "/home/user/.claude", AccountUUID: "uuid-b", ObservedAt: at(10)},
	}

	got, inferred := s.QuotaAccountAtClaude("/home/user/.claude", at(5))
	if got != "uuid-b" || !inferred {
		t.Errorf("account = %q inferred = %v, want uuid-b/inferred", got, inferred)
	}
}
