package codexappserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const multiBucketReply = `{"jsonrpc":"2.0","id":2,"result":{` +
	`"rateLimits":{"limitId":"codex","limitName":null,"planType":"pro",` +
	`"primary":{"usedPercent":27,"windowDurationMins":10080,"resetsAt":1788137190},"secondary":null},` +
	`"rateLimitsByLimitId":{` +
	`"codex":{"limitId":"codex","limitName":null,"planType":"pro",` +
	`"primary":{"usedPercent":27,"windowDurationMins":10080,"resetsAt":1788137190},"secondary":null},` +
	`"codex_bengalfox":{"limitId":"codex_bengalfox","limitName":"GPT-5.3-Codex-Spark","planType":"pro",` +
	`"primary":{"usedPercent":4,"windowDurationMins":300,"resetsAt":1787663532},` +
	`"secondary":{"usedPercent":1,"windowDurationMins":10080,"resetsAt":1788250332}}}}}`

func withReply(t *testing.T, reply string) {
	t.Helper()
	t.Setenv("CCTRACE_TEST_REPLY", reply)
}

func TestFetchReturnsEveryBucketsWindows(t *testing.T) {
	fakeServer(t, fakeReply)
	withReply(t, multiBucketReply)

	snap, err := Fetch(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got := map[string]Reading{}
	for _, r := range snap.Readings {
		got[fmt.Sprintf("%s/%d", r.LimitID, r.WindowMinutes)] = r
	}
	if len(got) != 3 {
		t.Fatalf("readings = %d, want 3: %+v", len(got), snap.Readings)
	}
	five, ok := got["codex_bengalfox/300"]
	if !ok {
		t.Fatalf("five-hour window missing: %+v", snap.Readings)
	}
	if five.UsedPercent != 4 {
		t.Errorf("five-hour used = %v, want 4", five.UsedPercent)
	}
	if five.LimitName != "GPT-5.3-Codex-Spark" {
		t.Errorf("limit name = %q", five.LimitName)
	}
	if five.PlanType != "pro" {
		t.Errorf("plan = %q", five.PlanType)
	}
	if five.ResetsAt == nil || five.ResetsAt.Unix() != 1787663532 {
		t.Errorf("resets_at = %v", five.ResetsAt)
	}
	if snap.FetchedAt.IsZero() {
		t.Error("FetchedAt is zero")
	}
	if snap.LoginEmail != "login@example.test" {
		t.Errorf("login email = %q, want account/read metadata", snap.LoginEmail)
	}
}

// The map key is the bucket identity. Some app-server versions repeat the
// generic "codex" id inside a model-scoped map entry; trusting that duplicate
// turns its five-hour meter into a generic Codex five-hour meter.
func TestFetchUsesMapKeyAsBucketIdentity(t *testing.T) {
	fakeServer(t, fakeReply)
	withReply(t, `{"jsonrpc":"2.0","id":2,"result":{"rateLimitsByLimitId":{`+
		`"codex_bengalfox":{"limitId":"codex","limitName":"GPT-5.3-Codex-Spark","planType":"pro",`+
		`"primary":{"usedPercent":4,"windowDurationMins":300,"resetsAt":1787663532}}}}}`)

	snap, err := Fetch(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Readings) != 1 || snap.Readings[0].LimitID != "codex_bengalfox" {
		t.Fatalf("readings = %+v, want model-scoped bucket identity", snap.Readings)
	}
}

// An older codex knows the single-bucket field and not the map. Reporting
// nothing there would be a regression against what the JSONL path already sees.
func TestFetchFallsBackToSingleBucketView(t *testing.T) {
	fakeServer(t, fakeReply)
	withReply(t, `{"jsonrpc":"2.0","id":2,"result":{"rateLimits":`+
		`{"limitId":"codex","planType":"pro",`+
		`"primary":{"usedPercent":9,"windowDurationMins":10080,"resetsAt":1788137190},"secondary":null}}}`)

	snap, err := Fetch(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Readings) != 1 || snap.Readings[0].WindowMinutes != 10080 {
		t.Fatalf("readings = %+v", snap.Readings)
	}
}

// A window with no declared length cannot be filed against any series, and the
// schema marks the field nullable. Dropping it is right; erroring is not.
func TestFetchSkipsWindowWithoutLength(t *testing.T) {
	fakeServer(t, fakeReply)
	withReply(t, `{"jsonrpc":"2.0","id":2,"result":{"rateLimits":`+
		`{"limitId":"codex","primary":{"usedPercent":9,"windowDurationMins":null},"secondary":null}}}`)

	snap, err := Fetch(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Readings) != 0 {
		t.Fatalf("readings = %+v, want none", snap.Readings)
	}
}

// Notifications arrive interleaved with responses; matching on id rather than
// on arrival order is what keeps one from being read as the other.
func TestFetchIgnoresInterleavedNotifications(t *testing.T) {
	fakeServer(t, fakeReplyNotify)
	withReply(t, multiBucketReply)

	snap, err := Fetch(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Readings) != 3 {
		t.Fatalf("readings = %+v", snap.Readings)
	}
}

func TestFetchReportsJSONRPCError(t *testing.T) {
	fakeServer(t, fakeRPCError)
	if _, err := Fetch(context.Background(), t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v, want the server's message", err)
	}
}

func TestFetchReportsGarbageOutput(t *testing.T) {
	fakeServer(t, fakeGarbage)
	if _, err := Fetch(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected an error on unparseable output")
	}
}

// A server that exits without answering must surface as an error rather than a
// silent empty reading, which would chart as "the account used nothing".
func TestFetchReportsEarlyExit(t *testing.T) {
	fakeServer(t, fakeExit3)
	if _, err := Fetch(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected an error when the server exits without answering")
	}
}

func TestFetchReportsMissingBinary(t *testing.T) {
	prev := lookPathFn
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { lookPathFn = prev })
	clearCache()
	t.Cleanup(clearCache)

	if _, err := Fetch(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected an error when codex is not installed")
	}
}

// A server that accepts the request and never answers must not hold the poll
// open. The bound is the package's own timeout, not the caller's patience.
func TestFetchTimesOutOnSilentServer(t *testing.T) {
	fakeServer(t, fakeHang)
	prev := fetchTimeout
	fetchTimeout = 500 * time.Millisecond
	t.Cleanup(func() { fetchTimeout = prev })

	start := time.Now()
	_, err := Fetch(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Fetch took %s, well past the timeout", elapsed)
	}
}

// Every exit path must leave the child reaped. A timeout is the path where a
// process is most likely to be left behind, so it is the one measured.
func TestFetchLeavesNoProcessBehindOnTimeout(t *testing.T) {
	fakeServer(t, fakeHang)
	prev := fetchTimeout
	fetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { fetchTimeout = prev })

	before := descendantPIDs(t)
	for i := 0; i < 5; i++ {
		clearCache()
		_, _ = Fetch(context.Background(), t.TempDir())
	}
	assertNoNewDescendants(t, before)
}

// The success path is measured too: a child that answered and was left running
// would accumulate one process per poll for the life of the daemon.
func TestFetchLeavesNoProcessBehindOnSuccess(t *testing.T) {
	// The server does not exit on its own here — it waits for more input, so
	// only the caller's teardown can end it.
	fakeServer(t, fakeReplyNoExit)
	withReply(t, multiBucketReply)

	before := descendantPIDs(t)
	for i := 0; i < 5; i++ {
		clearCache()
		if _, err := Fetch(context.Background(), t.TempDir()); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	}
	assertNoNewDescendants(t, before)
}

// A repeated call must not spawn a second process. Without this the cache is
// decoration, and the poll rate is whatever the caller happens to do.
func TestFetchServesRepeatCallsFromCache(t *testing.T) {
	_ = fakeServer(t, fakeReply)
	counter := filepath.Join(t.TempDir(), "runs")
	t.Setenv(fakeRunCounterEnv, counter)
	withReply(t, multiBucketReply)

	home := t.TempDir()
	for i := 0; i < 3; i++ {
		if _, err := Fetch(context.Background(), home); err != nil {
			t.Fatalf("Fetch %d: %v", i, err)
		}
	}
	if n := countLines(t, counter); n != 1 {
		t.Fatalf("spawned %d processes for 3 calls, want 1", n)
	}
}

// Failures are cached for the same reason successes are. Caching only successes
// is what let one failing endpoint turn a five-minute interval into a request
// per second — 1,293,935 of them on one client. Here the cost of that mistake
// would be a process, not a request.
func TestFetchCachesFailuresToo(t *testing.T) {
	_ = fakeServer(t, fakeExit3)
	counter := filepath.Join(t.TempDir(), "runs")
	t.Setenv(fakeRunCounterEnv, counter)

	home := t.TempDir()
	for i := 0; i < 20; i++ {
		if _, err := Fetch(context.Background(), home); err == nil {
			t.Fatal("expected failure")
		}
	}
	if n := countLines(t, counter); n != 1 {
		t.Fatalf("spawned %d processes for 20 failing calls, want 1", n)
	}
}

// Two homes are two accounts. Sharing one cache entry would report one home's
// usage under the other's name — a wrong value rather than a missing one.
func TestFetchCachesPerHome(t *testing.T) {
	_ = fakeServer(t, fakeReply)
	counter := filepath.Join(t.TempDir(), "runs")
	t.Setenv(fakeRunCounterEnv, counter)
	withReply(t, multiBucketReply)

	for _, home := range []string{t.TempDir(), t.TempDir()} {
		if _, err := Fetch(context.Background(), home); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	}
	if n := countLines(t, counter); n != 2 {
		t.Fatalf("spawned %d processes for 2 homes, want 2", n)
	}
}

// The home is passed to the child the only way codex reads it. Fetching one
// home's meter while labelling it with another's does not leave a field blank,
// it fills it with the wrong account.
func TestFetchPassesCodexHomeToChild(t *testing.T) {
	_ = fakeServer(t, fakeExit3)
	seen := filepath.Join(t.TempDir(), "home")
	t.Setenv(fakeHomeSinkEnv, seen)

	home := t.TempDir()
	_, _ = Fetch(context.Background(), home)

	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != home {
		t.Fatalf("CODEX_HOME = %q, want %q", got, home)
	}
}

// Output is bounded. A server stuck printing would otherwise be read into
// memory without limit, which is the memory-leak shape of the same failure the
// process handling guards against.
func TestFetchBoundsOutput(t *testing.T) {
	fakeServer(t, fakeNoise)
	prev := maxResponseBytes
	maxResponseBytes = 64 * 1024
	t.Cleanup(func() { maxResponseBytes = prev })

	if _, err := Fetch(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected an error once the output budget is spent")
	}
}

// A server request carries an id from the server's own counter, so it can
// share a number with a pending client request. Only a message without a
// method is a reply; taking the request for it returns the wrong payload.
func TestAwaitResultSkipsServerRequestWithSameID(t *testing.T) {
	br := bufio.NewReader(strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"item/tool/call","params":{"tool":"x"}}` + "\n" +
			`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"))
	got, err := awaitResult(br, 1)
	if err != nil {
		t.Fatalf("awaitResult: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("result = %s, want the reply rather than the server request", got)
	}
}

// --- helpers -------------------------------------------------------------

// descendantPIDs lists processes whose parent is this test binary. Other
// sessions on the machine run their own codex processes; counting by name
// would charge theirs to this test.
//
// The ps invocation is itself a child of this process and appears in its own
// output, so it is excluded — otherwise every measurement reports one leak.
func descendantPIDs(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command("ps", "-o", "pid=,ppid=,comm=", "-ax").Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	self := fmt.Sprintf("%d", os.Getpid())
	pids := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != self {
			continue
		}
		if cmd := f[2]; cmd == "ps" || strings.HasSuffix(cmd, "/ps") {
			continue
		}
		pids[f[0]] = true
	}
	return pids
}

func assertNoNewDescendants(t *testing.T, before map[string]bool) {
	t.Helper()
	// One short settle: reaping is synchronous in Fetch, so this only covers
	// the scheduler, not a retry loop.
	deadline := time.Now().Add(2 * time.Second)
	var leftover []string
	for time.Now().Before(deadline) {
		leftover = nil
		for pid := range descendantPIDs(t) {
			if !before[pid] {
				leftover = append(leftover, pid)
			}
		}
		if len(leftover) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%d child process(es) left behind: %v", len(leftover), leftover)
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return len(strings.Fields(string(b)))
}
