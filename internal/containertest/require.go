// Package containertest holds the policy for tests that need a container
// runtime: when the runtime is unavailable, do they skip or fail?
//
// Skipping is right for a developer running `go test ./...` on a machine
// without Docker. It is wrong for `make test-integration`, whose entire purpose
// is to exercise the database — there, an unavailable runtime means the run
// verified nothing, and reporting ok hides that. A leaked-container regression
// went unnoticed for exactly this reason: the suite kept printing ok while
// every database test skipped.
package containertest

import (
	"os"
	"testing"
)

// RequireEnv, when set to "1", turns an unavailable container runtime from a
// skip into a failure. The integration make targets set it.
const RequireEnv = "CCTRACE_REQUIRE_CONTAINERS"

// Required reports whether the caller demanded a working container runtime.
func Required() bool {
	return os.Getenv(RequireEnv) == "1"
}

// SkipOrFail ends the test because the container runtime is unavailable:
// failing when the caller demanded containers, skipping otherwise. what names
// the runtime that could not be started, e.g. "postgres container".
func SkipOrFail(t *testing.T, what string, err error) {
	t.Helper()
	if Required() {
		t.Fatalf("%s unavailable and %s=1: %v", what, RequireEnv, err)
	}
	t.Skipf("%s unavailable: %v", what, err)
}
