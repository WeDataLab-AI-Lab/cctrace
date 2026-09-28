package buffer

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// skipPOSIXOnlyOnWindows marks a test whose expectation depends on POSIX
// filesystem semantics that Windows does not share. why names the specific
// semantics so the skip stays reviewable -- a skip whose reason is "Windows" is
// indistinguishable from one that hides a defect.
//
// Making these pass on Windows is work nothing would use:
// TestClientDoesNotImportBuffer below pins that this package reaches only
// cctraced, which is built and run in Linux containers. t.Skip rather than a
// build tag so `go test -v` says so out loud instead of reporting a package with
// no tests.
func skipPOSIXOnlyOnWindows(t *testing.T, why string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip(why + " -- POSIX-specific, and this package is Linux-only (see TestClientDoesNotImportBuffer)")
	}
}

// The skip above is only defensible while the client cannot reach this package.
// That is a claim about the import graph, so check the import graph rather than
// trusting a comment: if cmd/cctrace ever imports buffer, the POSIX assumptions
// in the durability path start running on the machines our users are on, and the
// skip turns from "not needed" into "hiding it".
func TestClientDoesNotImportBuffer(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "../../cmd/cctrace").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "cctrace/internal/buffer" {
			t.Fatal("cmd/cctrace now imports internal/buffer: the Windows skips in this package " +
				"assume it cannot, because the durability path is POSIX-specific. Either keep the " +
				"client off this package, or make diskspiller Windows-correct and drop the skips.")
		}
	}
}
