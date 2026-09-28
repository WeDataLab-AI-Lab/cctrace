package main

import (
	"strings"
	"testing"

	"cctrace/internal/syncer"
)

// A held sync is the one state a person has to act on, and the fix is on the
// server. The syncer logs it once and sync.log rotates, so status is where it
// has to survive -- the same reason firstRunSkipNotice exists.
func TestBodyLimitBlockNotice(t *testing.T) {
	if got := bodyLimitBlockNotice(map[string]*syncer.FileState{
		"a.jsonl": {},
		"b.jsonl": nil,
	}); got != "" {
		t.Errorf("notice for an unblocked set: %q", got)
	}

	one := bodyLimitBlockNotice(map[string]*syncer.FileState{
		"a.jsonl": {BlockedByBodyLimit: true},
		"b.jsonl": {},
	})
	if !strings.Contains(one, "1 session holding") {
		t.Errorf("one blocked file: %q", one)
	}
	// The reader is at a client and the lever is on the server, so the notice has
	// to name it.
	if !strings.Contains(one, "CCTRACE_MAX_SYNC_BODY_BYTES") {
		t.Errorf("notice does not say what to change: %q", one)
	}
	if !strings.Contains(one, "nothing is lost") {
		t.Errorf("notice does not say the data survives: %q", one)
	}

	many := bodyLimitBlockNotice(map[string]*syncer.FileState{
		"a.jsonl": {BlockedByBodyLimit: true},
		"b.jsonl": {BlockedByBodyLimit: true},
	})
	if !strings.Contains(many, "2 sessions holding") {
		t.Errorf("two blocked files: %q", many)
	}
}
