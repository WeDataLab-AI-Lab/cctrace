package main

import (
	"strings"
	"testing"
	"time"

	"cctrace/internal/syncer"
)

// A held run is silent by design -- nothing is sent and nothing fails -- so
// status is the one place a person can learn that a session is waiting. The
// reason is printed as stored: a reason this binary does not know (written by
// a newer one) is still a hold, and folding it into another would misstate
// why.
func TestHeldNotice(t *testing.T) {
	if got := heldNotice(map[string]*syncer.FileState{"a.jsonl": {}, "b.jsonl": nil}); got != "" {
		t.Errorf("notice without holds: %q", got)
	}

	uncertain := syncer.HeldRun{Reason: syncer.HeldReasonGitUncertain}
	one := heldNotice(map[string]*syncer.FileState{
		"a.jsonl": {Held: map[string]syncer.HeldRun{"/work/a": uncertain}},
		"b.jsonl": {},
	})
	if !strings.Contains(one, "1 session holding") || !strings.Contains(one, "git-uncertain x1") {
		t.Errorf("one held file: %q", one)
	}
	// A hold is not free: past 24 hours of failing its records are dropped,
	// and the person reading this has to know that before it happens.
	if !strings.Contains(one, "dropped after 24h") {
		t.Errorf("notice does not say a hold ends in loss: %q", one)
	}

	// Counted by file: two cwds of one session held for the same reason are
	// one session waiting, and a session held for two reasons is under both.
	many := heldNotice(map[string]*syncer.FileState{
		"a.jsonl": {Held: map[string]syncer.HeldRun{"/work/a": uncertain, "/work/b": uncertain}},
		"b.jsonl": {Held: map[string]syncer.HeldRun{"": {Reason: syncer.HeldReasonCWDUnknown}, "/work/c": {Reason: "future-reason"}}},
		"c.jsonl": {Held: map[string]syncer.HeldRun{"/work/a": uncertain}},
	})
	for _, want := range []string{"3 sessions holding", "git-uncertain x2", "cwd-unknown x1", "future-reason x1"} {
		if !strings.Contains(many, want) {
			t.Errorf("notice %q lacks %q", many, want)
		}
	}
}

// A hold that was seen failing for 24 hours dropped records for good. That is
// a loss, and it is said as one, with the reason as stored.
func TestHoldExpiredNotice(t *testing.T) {
	at := time.Date(2026, 8, 13, 14, 0, 0, 0, time.UTC)
	if got := holdExpiredNotice(map[string]*syncer.FileState{"a.jsonl": {}, "b.jsonl": nil}); got != "" {
		t.Errorf("notice without expiries: %q", got)
	}
	got := holdExpiredNotice(map[string]*syncer.FileState{
		"a.jsonl": {HoldExpired: &syncer.HoldExpired{At: at, Reason: syncer.HeldReasonGitUncertain}},
		"b.jsonl": {HoldExpired: &syncer.HoldExpired{At: at, Reason: syncer.HeldReasonCWDUnknown}},
		"c.jsonl": {Held: map[string]syncer.HeldRun{"/work/a": {Reason: syncer.HeldReasonGitUncertain}}},
	})
	for _, want := range []string{"2 sessions", "not collected", "git-uncertain x1", "cwd-unknown x1"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice %q lacks %q", got, want)
		}
	}
}
