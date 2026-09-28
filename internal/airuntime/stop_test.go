package airuntime

import (
	"errors"
	"testing"
)

// The three HTTP runtimes read three different fields for the same thing --
// response.status, stop_reason, finish_reason -- and then paired the same
// outcomes with the same codes and sentinels by hand. The mapping from the
// wire field is each provider's own business; the pairing is not, and doing it
// by hand is how chatruntime ended up reading finish_reason not at all.
func TestStopErrorPairsEachOutcomeWithItsCodeAndSentinel(t *testing.T) {
	cases := map[Stop]struct {
		code     string
		sentinel error
	}{
		StopOutputLimit:   {"output_limit", ErrBudget},
		StopContextWindow: {"context_window_exceeded", ErrBudget},
		StopRefused:       {"refused", ErrProtocol},
		StopFailed:        {"turn_failed", ErrUnavailable},
		StopUnknown:       {"protocol_error", ErrProtocol},
	}
	for stop, want := range cases {
		err := StopError(stop, "message stays the caller's")
		if err == nil {
			t.Errorf("%s: no error", stop)
			continue
		}
		if err.Code != want.code {
			t.Errorf("%s: code = %q, want %q", stop, err.Code, want.code)
		}
		if !errors.Is(err, want.sentinel) {
			t.Errorf("%s: sentinel = %v, want %v", stop, err.Err, want.sentinel)
		}
		if err.Message != "message stays the caller's" {
			t.Errorf("%s: message = %q, want the caller's", stop, err.Message)
		}
	}
}

// A turn that is still going is not an error: tool calls continue the loop and
// a final answer ends it normally.
func TestStopErrorIsNilWhileTheTurnContinues(t *testing.T) {
	for _, stop := range []Stop{StopFinal, StopToolUse} {
		if err := StopError(stop, "unused"); err != nil {
			t.Errorf("%s: err = %v, want nil", stop, err)
		}
	}
}
