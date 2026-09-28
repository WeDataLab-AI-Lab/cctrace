package syncer

import (
	"testing"
	"time"
)

// The client held one 30 s deadline for every request regardless of how much it
// was sending, so a body the server is configured to accept could still be
// undeliverable: the transfer needed longer than the client would wait, and the
// failure arrived as a transport error with no HTTP status -- indistinguishable
// from an unreachable server, and invisible to the 413 split that #565 added
// (there is no 413 to react to). The wait has to be a function of the bytes.
func TestTransferDeadline(t *testing.T) {
	small := transferDeadline(400 << 10) // a typical batch
	if small < 30*time.Second {
		t.Errorf("a small body got %s, less than the old fixed wait", small)
	}
	if small > 35*time.Second {
		t.Errorf("a small body got %s; the common case should not wait longer than before", small)
	}

	// A body the server may be configured to accept has to be deliverable on a
	// link that is not fast. The floor the formula assumes is stated in the
	// implementation; here we only check that it scales.
	big := transferDeadline(64 << 20)
	if big <= small {
		t.Errorf("64 MiB got %s, no more than the %s a 400 KiB body gets", big, small)
	}
	if big < 90*time.Second {
		t.Errorf("64 MiB got %s; too little to cross a slow link", big)
	}

	// Bounded, so one sender cannot hold a connection indefinitely by being slow.
	huge := transferDeadline(1 << 40)
	if huge > 10*time.Minute {
		t.Errorf("an absurd size got %s; the deadline has to stay bounded", huge)
	}
}
