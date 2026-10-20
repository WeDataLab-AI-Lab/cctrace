package localdaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// blockFixture runs a supervisor over profiles that share one source. The first
// ID (alphabetically) becomes the route owner; the others are blocked.
type blockFixture struct {
	t            *testing.T
	root         string
	source       string
	mu           sync.Mutex
	ids          []string
	loads        int
	logs         []string
	spec         func(Spec) Spec
	snapshotRead func(string) (Snapshot, error)
	ready        chan struct{}
}

func newBlockFixture(t *testing.T, ids ...string) *blockFixture {
	t.Helper()
	f := &blockFixture{t: t, root: t.TempDir(), ids: ids, snapshotRead: ReadSnapshot}
	f.source = filepath.Join(f.root, "shared")
	if err := os.MkdirAll(f.source, 0700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *blockFixture) load() ([]Spec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	var specs []Spec
	for _, id := range f.ids {
		spec := Spec{ID: id, Name: id, Revision: "1", Enabled: true, RuntimeDir: filepath.Join(f.root, id), Destination: "dest", Sources: []string{"codex:" + f.source}, Run: func(ctx context.Context, report func(Progress)) error {
			report(Progress{State: "healthy"})
			return nil
		}}
		if f.spec != nil {
			spec = f.spec(spec)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func (f *blockFixture) logf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

func (f *blockFixture) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logs...)
}

func (f *blockFixture) loadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loads
}

// start returns a stop function that waits for the supervisor to exit.
func (f *blockFixture) start() func() {
	f.t.Helper()
	s := New(Config{Root: f.root, Version: "dev", Interval: 10 * time.Millisecond, Refresh: 10 * time.Millisecond, Load: f.load, Logf: f.logf})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan struct{})
	f.ready = ready
	go func() { done <- s.Run(ctx, func(Runtime) { close(ready) }) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				f.t.Error(err)
			}
		})
	}
	f.t.Cleanup(stop)
	return stop
}

// Live probes briefly acquire daemon.lock. Gate them inside their caller's
// existing observation deadline, rather than adding a separate startup wait.
func (f *blockFixture) probeWhenReady(probe func() bool) bool {
	select {
	case <-f.ready:
		return probe()
	default:
		return false
	}
}

// command sends one control request, bounded so a hung daemon fails the test
// instead of hanging it.
func (f *blockFixture) command(action, id string) error {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Command(ctx, f.root, action, id)
}

func (f *blockFixture) blocked(id string) bool {
	snap, err := readBlockFixtureSnapshot(f.t, f.root, f.snapshotRead)
	if err != nil {
		return false
	}
	for _, job := range snap.Jobs {
		if job.ID == id {
			return job.ErrorCode == "duplicate_route"
		}
	}
	return false
}

// Read the publisher's atomic replacement with the production reader's exact
// bound. A final refusal stays observable; callers retain their absent contract.
func readBlockFixtureSnapshot(t *testing.T, root string, read func(string) (Snapshot, error)) (Snapshot, error) {
	t.Helper()
	_, snap, err := readStable(func() (Runtime, Snapshot, error) {
		snap, err := read(root)
		return Runtime{}, snap, err
	})
	if err != nil {
		t.Logf("block fixture snapshot unavailable after bounded metadata check: %v", err)
	}
	return snap, err
}

func TestBlockFixtureBlockedSurvivesMetadataReplacement(t *testing.T) {
	t.Parallel()
	f := newBlockFixture(t, "one", "two")
	calls := 0
	f.snapshotRead = func(string) (Snapshot, error) {
		calls++
		if calls == 1 {
			return Snapshot{}, ErrMetadataChanged
		}
		return Snapshot{Jobs: []JobStatus{{ID: "two", ErrorCode: "duplicate_route"}}}, nil
	}
	if !f.blocked("two") || calls != 2 {
		t.Fatalf("metadata replacement falsely lifted the fixture block: calls=%d", calls)
	}
}

func TestBlockFixtureSnapshotRefusalsStayBoundedAndVisible(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		err   error
		calls int
	}{
		{"second replacement", ErrMetadataChanged, 2},
		{"unsafe metadata", errors.New("unsafe synthetic metadata"), 1},
		{"not yet published", os.ErrNotExist, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBlockFixture(t, "one", "two")
			calls := 0
			f.snapshotRead = func(string) (Snapshot, error) {
				calls++
				return Snapshot{Jobs: []JobStatus{{ID: "two", ErrorCode: "duplicate_route"}}}, tc.err
			}
			if _, err := readBlockFixtureSnapshot(t, f.root, f.snapshotRead); err != tc.err || calls != tc.calls {
				t.Fatalf("refusal hidden or retried beyond its bound: calls=%d err=%v want=%v", calls, err, tc.err)
			}
			calls = 0
			if f.blocked("two") || calls != tc.calls {
				t.Fatalf("refused read became accepted state or exceeded its bound: calls=%d", calls)
			}
		})
	}
}

// waitReconciles lets the supervisor run n more reconcile passes, counted by
// its own Load calls instead of by sleeping.
func (f *blockFixture) waitReconciles(n int) {
	f.t.Helper()
	target := f.loadCount() + n
	eventually(f.t, func() bool { return f.loadCount() >= target })
}

// routeLines returns the first route of the persisted file, whose id the log lines
// quote, and the shortened destination they print.
func (f *blockFixture) routeLines() (id, destination string) {
	f.t.Helper()
	file, err := ReadRoutes(f.root)
	if err != nil || len(file.Routes) != 1 {
		f.t.Fatalf("routes file: %+v, %v", file, err)
	}
	return file.Routes[0].ID, file.Routes[0].Destination[:8]
}

func TestBlockedJobIsLoggedOncePerChange(t *testing.T) {
	t.Parallel()
	f := newBlockFixture(t, "one", "two")
	stop := f.start()
	eventually(t, func() bool { return f.blocked("two") })
	f.waitReconciles(10)

	id, destination := f.routeLines()
	want := []string{
		"[routes] reserved profile=one kind=codex source=" + id + " label=- destination=" + destination,
		"[routes] excluded profile=two kind=codex source=" + id + " label=- reason=same holder=one holder_state=active",
		"[routes] fully-shared profile=two sources=1 holders=one",
	}
	if got := f.lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("log = %q, want exactly %q", got, want)
	}
	for _, line := range f.lines() {
		if strings.Contains(line, f.root) {
			t.Fatalf("the log line names a path: %s", line)
		}
	}

	// A new process says what holds again, but not the reservation it finds.
	stop()
	f.mu.Lock()
	f.logs = nil
	f.mu.Unlock()
	f.start()
	eventually(t, func() bool { return f.blocked("two") })
	f.waitReconciles(10)
	if got := f.lines(); !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("restarted log = %q, want exactly %q", got, want[1:])
	}
}

func TestBlockedJobWhoseHolderWasDeletedSaysSo(t *testing.T) {
	t.Parallel()
	f := newBlockFixture(t, "one", "two")
	stop := f.start()
	eventually(t, func() bool { return f.blocked("two") })
	id, _ := f.routeLines()

	f.mu.Lock()
	f.ids = []string{"two"}
	f.logs = nil
	f.mu.Unlock()
	eventually(t, func() bool { return len(f.lines()) == 3 })
	f.waitReconciles(10)
	want := []string{
		"[routes] excluded profile=two kind=codex source=" + id + " label=- reason=same holder=- holder_state=removed",
		"[routes] fully-shared profile=two sources=1 holders=(removed profile)",
		"[routes] orphan source=" + id + " former_holder=one",
	}
	if got := f.lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("log = %q, want exactly %q", got, want)
	}

	err := f.command("restart", "two")
	var control *ControlError
	if !errors.As(err, &control) || control.Reason != "source_overlap" || !reflect.DeepEqual(control.Holders, []string{"(removed profile)"}) {
		t.Fatalf("control error = %#v", err)
	}
	if !strings.Contains(err.Error(), "(held by: (removed profile))") {
		t.Fatalf("message %q does not say the holder is gone", err)
	}
	stop()
}

func TestProfileUnavailableNamesTheConflict(t *testing.T) {
	t.Parallel()
	f := newBlockFixture(t, "one", "two")
	f.start()
	eventually(t, func() bool { return f.blocked("two") })

	err := f.command("restart", "two")
	var control *ControlError
	if !errors.As(err, &control) {
		t.Fatalf("want *ControlError, got %#v", err)
	}
	if control.Code != "profile_unavailable" || control.Reason != "source_overlap" || strings.Join(control.Holders, ",") != "one" {
		t.Fatalf("control error = %+v", control)
	}
	const want = "daemon control: profile_unavailable: every collection source of this profile is already collected by another profile (held by: one). Free a source in that profile, or run: cctrace profile claim <profile> --all"
	if err.Error() != want {
		t.Fatalf("message = %q\nwant      %q", err.Error(), want)
	}
	// Stopping a blocked profile stays allowed, as before.
	if err := f.command("stop", "two"); err != nil {
		t.Fatalf("stop of a blocked profile: %v", err)
	}
}

func TestProfileUnavailableNamesDisabledAndMigrationConflict(t *testing.T) {
	t.Parallel()
	f := newBlockFixture(t, "off", "stuck", "ok")
	f.spec = func(s Spec) Spec {
		switch s.ID {
		case "off":
			s.Enabled = false
			s.Sources = nil
		case "stuck":
			s.BlockedReason = "migration_conflict"
			s.Sources = nil
		default:
			s.Sources = nil
		}
		return s
	}
	f.start()
	eventually(t, func() bool {
		snap, err := readBlockFixtureSnapshot(t, f.root, f.snapshotRead)
		return err == nil && len(snap.Jobs) == 3
	})

	cases := map[string]string{
		"off":   "daemon control: profile_unavailable: collection is disabled for this profile",
		"stuck": "daemon control: profile_unavailable: profile migration conflict must be resolved first",
	}
	for id, want := range cases {
		err := f.command("start", id)
		var control *ControlError
		if !errors.As(err, &control) || control.Code != "profile_unavailable" || err.Error() != want {
			t.Fatalf("%s: %v (%+v), want %q", id, err, control, want)
		}
	}
	// A profile that is not in the registry keeps the bare code: there is no
	// conflict to name.
	err := f.command("start", "missing")
	if err == nil || err.Error() != "daemon control: profile_unavailable" {
		t.Fatalf("unknown profile: %v", err)
	}
}

func TestProfileUnavailableCapsTheHolderList(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var sources []string
	var ids []string
	for i := 1; i <= 10; i++ {
		dir := filepath.Join(root, fmt.Sprintf("src%02d", i))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, "claude:"+dir)
		ids = append(ids, fmt.Sprintf("o%02d", i))
	}
	load := func() ([]Spec, error) {
		var specs []Spec
		for i, id := range ids {
			specs = append(specs, Spec{ID: id, Name: id, Revision: "1", Enabled: true, RuntimeDir: filepath.Join(root, id), Destination: "dest", Sources: sources[i : i+1], Run: func(context.Context, func(Progress)) error { return nil }})
		}
		specs = append(specs, Spec{ID: "z", Name: "z", Revision: "1", Enabled: true, RuntimeDir: filepath.Join(root, "z"), Destination: "dest", Sources: sources, Run: func(context.Context, func(Progress)) error { return nil }})
		return specs, nil
	}
	s := New(Config{Root: root, Interval: 10 * time.Millisecond, Refresh: 10 * time.Millisecond, Load: load})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, func() bool {
		snap, err := readBlockFixtureSnapshot(t, root, ReadSnapshot)
		if err != nil {
			return false
		}
		for _, job := range snap.Jobs {
			if job.ID == "z" && job.ErrorCode == "duplicate_route" {
				return true
			}
		}
		return false
	})

	commandCtx, cancelCommand := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCommand()
	err := Command(commandCtx, root, "restart", "z")
	var control *ControlError
	if !errors.As(err, &control) || len(control.Holders) != 8 || control.More != 2 {
		t.Fatalf("holders = %+v, want 8 names and 2 more", control)
	}
	if want := "(held by: o01, o02, o03, o04, o05, o06, o07, o08, and 2 more)"; !strings.Contains(err.Error(), want) {
		t.Fatalf("message %q lacks %q", err, want)
	}
}

// The block is the holder's reservation, not a memory of one pass: stopping the
// holder or deleting it does not hand its sources to anyone.
func TestBlockOutlivesTheHolderStoppingOrBeingRemoved(t *testing.T) {
	t.Parallel()
	f := newBlockFixture(t, "one", "two")
	f.start()
	eventually(t, func() bool { return f.blocked("two") })

	if err := f.command("stop", "one"); err != nil {
		t.Fatal(err)
	}
	f.waitReconciles(10)
	if !f.blocked("two") {
		t.Fatal("stopping the holder lifted the block; the control error's advice would be wrong")
	}

	f.mu.Lock()
	f.ids = []string{"two"}
	f.mu.Unlock()
	f.waitReconciles(10)
	if !f.blocked("two") {
		t.Fatal("removing the holder lifted the block; the control error's advice would be wrong")
	}
}
