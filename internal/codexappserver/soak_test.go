package codexappserver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The unit tests prove the child is reaped over five calls. Five is not the
// number that matters: a daemon runs for weeks, and the failure being guarded
// against is one that only shows as accumulation. This runs the same paths
// thousands of times and measures what accumulates — live children, zombies,
// resident memory, open descriptors.
//
// It is opt-in because it takes minutes and starts thousands of processes,
// which is not something an ordinary `go test ./...` should do:
//
//	CCTRACE_SOAK=2000 go test ./internal/codexappserver -run Soak -v -timeout 30m
func TestSoakLeavesNothingBehind(t *testing.T) {
	iterations := soakIterations(t)

	// Each scenario is a failure mode with its own teardown path. Interleaving
	// them is deliberate: a leak that only appears when a timeout follows a
	// success would be invisible to any of them run alone.
	scenarios := []struct {
		name     string
		behavior string
	}{
		{"success", fakeReply},
		{"hang", fakeHang},
		{"early-exit", fakeExit3},
		{"garbage", fakeGarbage},
		{"jsonrpc-error", fakeRPCError},
		{"dies-midway", fakeDiesMidway},
	}

	// Everything the harness allocates is allocated once, before the baseline
	// is taken. Creating a temp dir or registering a cleanup per iteration
	// would grow this process's own memory in step with the loop and hand back
	// the harness's footprint as if it were the code's.
	prevLookPath, prevTimeout := lookPathFn, fetchTimeout
	fetchTimeout = 400 * time.Millisecond
	t.Cleanup(func() { lookPathFn, fetchTimeout = prevLookPath, prevTimeout })

	// One binary for every scenario: the child reads which one to play from the
	// environment, so nothing is written or allocated per iteration. That matters
	// here -- this test measures memory, and a temp file per scenario would hand
	// back the harness's footprint as if it were the code's.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	prevFake := os.Getenv(fakeServerEnv)
	t.Cleanup(func() { _ = os.Setenv(fakeServerEnv, prevFake) })
	lookPathFn = func(string) (string, error) { return self, nil }
	t.Setenv("CCTRACE_TEST_REPLY", multiBucketReply)
	home := t.TempDir()

	base := measure(t)
	t.Logf("before: %s", base)

	for i := 0; i < iterations; i++ {
		n := i % len(scenarios)
		_ = os.Setenv(fakeServerEnv, scenarios[n].behavior)
		// The cache is cleared rather than dodged with a fresh home, so every
		// iteration really does start a process. The cache is what a running
		// daemon leans on; this measures the path underneath it.
		clearCache()
		_, _ = Fetch(context.Background(), home)

		if i > 0 && i%500 == 0 {
			t.Logf("at %d: %s", i, measure(t))
		}
	}

	// One settle. Reaping is synchronous inside Fetch, so anything still here
	// after this is not a scheduling artefact.
	time.Sleep(2 * time.Second)
	after := measure(t)
	t.Logf("after %d iterations: %s", iterations, after)

	if after.children > 0 {
		t.Errorf("%d child process(es) still alive after %d iterations", after.children, iterations)
	}
	if after.zombies > 0 {
		t.Errorf("%d zombie(s) after %d iterations", after.zombies, iterations)
	}
	// Descriptors are the tightest of the three: two pipes per call, so a leak
	// of even one per iteration would be thousands by now. The allowance covers
	// the temp directories the harness itself opens.
	if grown := after.fds - base.fds; grown > 64 {
		t.Errorf("open descriptors grew by %d over %d iterations", grown, iterations)
	}
	// The live heap is asserted and RSS is only logged. RSS includes arenas and
	// goroutine stacks the runtime has grown and not yet given back, so it
	// climbs and then flattens on a process that leaks nothing; treating that
	// as the signal would make this test fail on healthy code.
	if grown := after.heapKB - base.heapKB; grown > 4096 {
		t.Errorf("live heap grew by %dKB over %d iterations", grown, iterations)
	}
	// One goroutine per call is started to read the reply. A timeout returns
	// without it, so this is where a goroutine parked forever on a pipe read
	// would show.
	if grown := after.goros - base.goros; grown > 4 {
		t.Errorf("goroutine count grew by %d over %d iterations", grown, iterations)
	}
}

func soakIterations(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("CCTRACE_SOAK")
	if raw == "" {
		t.Skip("set CCTRACE_SOAK=<iterations> to run the soak test")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		t.Fatalf("CCTRACE_SOAK=%q is not a positive count", raw)
	}
	return n
}

// reading is one measurement of what this process is holding.
type reading struct {
	children int
	zombies  int
	rssKB    int
	heapKB   int
	fds      int
	goros    int
}

func (r reading) String() string {
	return fmt.Sprintf("children=%d zombies=%d goroutines=%d rss=%dKB heap=%dKB fds=%d",
		r.children, r.zombies, r.goros, r.rssKB, r.heapKB, r.fds)
}

// measure counts only this process's own children. The machine this runs on has
// other agent sessions with their own codex processes; counting by name would
// charge theirs here and turn somebody else's work into a failed assertion.
func measure(t *testing.T) reading {
	t.Helper()
	self := strconv.Itoa(os.Getpid())

	// RSS alone cannot tell a leak from a heap the collector has not yet handed
	// back, and the runtime returns memory to the OS lazily. Collecting first
	// and reporting the live heap beside RSS is what makes the two separable.
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	out, err := exec.Command("ps", "-o", "pid=,ppid=,stat=,rss=,comm=", "-ax").Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	r := reading{heapKB: int(ms.HeapAlloc / 1024), goros: runtime.NumGoroutine()}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		if f[0] == self {
			r.rssKB, _ = strconv.Atoi(f[3])
			continue
		}
		if f[1] != f[0] && f[1] == self {
			if cmd := f[4]; cmd == "ps" || strings.HasSuffix(cmd, "/ps") {
				continue
			}
			r.children++
			if strings.HasPrefix(f[2], "Z") {
				r.zombies++
			}
		}
	}

	// /dev/fd lists this process's descriptors on both macOS and Linux, and
	// costs no subprocess of its own to read. Readdirnames rather than ReadDir:
	// the entries are live descriptors, and stat'ing them — which ReadDir does
	// on this platform — fails on the ones that are pipes mid-teardown, taking
	// the whole listing with it.
	if d, err := os.Open("/dev/fd"); err == nil {
		names, _ := d.Readdirnames(-1)
		r.fds = len(names)
		d.Close()
	}
	return r
}
