package otelrecv

import "testing"

// The mismatch detector exists to surface account switches (#140, #220). It kept
// the first login_email it ever saw for a user_id and never replaced it, so after
// one switch every later export compared against a value that was no longer the
// previous account: the log line said "prev_email" but meant "first email", and
// it repeated for every event from then on instead of once per switch.
func TestNoteLoginEmailReportsEachSwitchOnce(t *testing.T) {
	r := &LogsReceiver{uidEmailMap: make(map[string]string)}

	if prev, switched := r.noteLoginEmail("u1", "one@example.com"); switched {
		t.Fatalf("first sighting reported a switch (prev=%q)", prev)
	}
	// Repeats of the same account are not switches.
	if _, switched := r.noteLoginEmail("u1", "one@example.com"); switched {
		t.Fatal("same account reported as a switch")
	}

	prev, switched := r.noteLoginEmail("u1", "two@example.com")
	if !switched {
		t.Fatal("account change not reported as a switch")
	}
	if prev != "one@example.com" {
		t.Fatalf("prev = %q, want one@example.com", prev)
	}

	// The switch is reported once. Subsequent events on the new account are the
	// steady state, not a repeat of the same switch.
	if _, switched := r.noteLoginEmail("u1", "two@example.com"); switched {
		t.Fatal("steady state after a switch reported as another switch")
	}

	// Switching back is a new switch, and prev is now the account left behind.
	prev, switched = r.noteLoginEmail("u1", "one@example.com")
	if !switched || prev != "two@example.com" {
		t.Fatalf("switch back: prev=%q switched=%v, want two@example.com true", prev, switched)
	}
}

// Users are tracked independently: one user's switch must not be attributed to
// another, which is the whole point of keying on user_id.
func TestNoteLoginEmailIsPerUser(t *testing.T) {
	r := &LogsReceiver{uidEmailMap: make(map[string]string)}

	r.noteLoginEmail("u1", "one@example.com")
	if _, switched := r.noteLoginEmail("u2", "two@example.com"); switched {
		t.Fatal("second user's first sighting reported as a switch")
	}
	if _, switched := r.noteLoginEmail("u1", "one@example.com"); switched {
		t.Fatal("first user's steady state disturbed by another user")
	}
}

// Blank identifiers carry no information; treating them as a value would make
// every unauthenticated export look like a switch.
func TestNoteLoginEmailIgnoresBlanks(t *testing.T) {
	r := &LogsReceiver{uidEmailMap: make(map[string]string)}

	if _, switched := r.noteLoginEmail("", "one@example.com"); switched {
		t.Fatal("blank user_id reported a switch")
	}
	if _, switched := r.noteLoginEmail("u1", ""); switched {
		t.Fatal("blank login_email reported a switch")
	}
	if len(r.uidEmailMap) != 0 {
		t.Fatalf("blank inputs recorded %d entries, want 0", len(r.uidEmailMap))
	}
}
