package api

import (
	"strings"
	"testing"

	"cctrace/internal/store"
)

// Every code a runtime or the report service can store must have something a
// reader can understand. Fifteen did not: auth_failed, rate_limited, refused
// and context_window_exceeded among them, so a run that hit a rate limit told
// the user "오류 코드 rate_limited". The list is written out rather than
// derived so that adding a code without its message fails here.
func TestEveryStoredErrorCodeHasAMessage(t *testing.T) {
	codes := []string{
		// internal/airuntime and the three HTTP runtimes
		"auth_failed", "budget_exceeded", "canceled", "context_window_exceeded",
		"endpoint_not_found", "invalid_request", "model_unavailable", "not_configured",
		"output_limit", "permission_denied", "protocol_error", "provider_unavailable",
		"rate_limited", "refused", "runtime_unavailable", "schema_violation",
		"time_limit", "tool_call_limit", "turn_failed", "unexpected_tool",
		// internal/aireport
		"empty_report", "invalid_output", "runtime_account_changing", "runtime_changing",
		"runtime_error", "runtime_unconfigured", "server_restarted", "server_shutdown",
		"store_error",
	}
	for _, code := range codes {
		if _, ok := aiRunErrorMessages[code]; !ok {
			t.Errorf("%s has no message; the reader sees the raw code", code)
		}
	}
}

// The stored error_message holds runtime output -- process errors, paths,
// protocol text -- and must not reach the screen. The report panel used to
// guard this from the browser side by asserting a path did not appear; that
// guard belonged here, since the decision is the server's.
func TestRunJSONNeverCarriesTheStoredErrorMessage(t *testing.T) {
	run := &store.AIReportRun{
		ID:           7,
		Status:       store.AIRunFailed,
		ErrorCode:    "time_limit",
		ErrorMessage: "dial tcp 10.0.0.3:443: i/o timeout at /srv/secret/path",
	}
	out := runJSON(run, nil)
	if out.Error == nil {
		t.Fatal("a failed run has no error in the response")
	}
	if strings.Contains(out.Error.Message, "/srv/secret/path") || out.Error.Message == run.ErrorMessage {
		t.Errorf("the stored message reached the response: %q", out.Error.Message)
	}
	if out.Error.Message != aiRunErrorMessages["time_limit"] {
		t.Errorf("message = %q, want the reader's message for the code", out.Error.Message)
	}
}
