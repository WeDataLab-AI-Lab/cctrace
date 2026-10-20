package localdaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/atomicfile"
)

// routed runs a supervisor over profiles whose sources are folders of a temp
// root, and records what each job was given and what it held.
type routed struct {
	t            *testing.T
	root         string
	env          *planEnv
	snapshotRead func(string) (Snapshot, error)

	mu      sync.Mutex
	profs   map[string]*rprofile
	logs    []string
	allocs  map[string][]Allocation
	passes  map[string]int
	live    map[string]int // line -> leases holding it now
	overlap []string       // lines a second lease took while another still held them
	loads   int
	events  []string // acquire/release/pass in order, for ordering assertions
}

type rprofile struct {
	present, disabled bool
	dest              string
	lines             []string
	revision          int
	blockRun          bool // Run waits for cancellation, as a long pass does
	filters           Filters
	localAnalysis     bool
	failAcquire       int  // Acquire fails this many times first
	busyAcquire       int  // ... and this many times first, because a line is in use
	declineHandoffs   bool // Acquire takes none of the hand-overs it is given
}

func newRouted(t *testing.T) *routed {
	t.Helper()
	root := t.TempDir()
	return &routed{t: t, root: root, env: &planEnv{t: t, root: canonicalSource(t.TempDir())}, snapshotRead: ReadSnapshot, profs: map[string]*rprofile{}, allocs: map[string][]Allocation{}, passes: map[string]int{}, live: map[string]int{}}
}

// add registers a profile at destination dest with the given lines.
func (r *routed) add(id, dest string, lines ...string) *rprofile {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := &rprofile{present: true, dest: dest, lines: lines, revision: 1}
	r.profs[id] = p
	return p
}

func (r *routed) load() ([]Spec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads++
	var specs []Spec
	for id, p := range r.profs {
		if !p.present {
			continue
		}
		id, p := id, p
		dir := filepath.Join(r.root, "rt", strings.ReplaceAll(id, ":", "_"))
		_ = os.MkdirAll(dir, 0700)
		spec := Spec{ID: id, Name: strings.TrimPrefix(id, "named:"), RuntimeDir: dir, Revision: fmt.Sprint(p.revision), Enabled: !p.disabled, Destination: testDest(p.dest), Sources: slices.Clone(p.lines), Filters: p.filters, LocalAnalysis: p.localAnalysis}
		spec.Acquire = func(ctx context.Context, alloc Allocation) (func(), error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if p.busyAcquire > 0 {
				p.busyAcquire--
				return nil, fmt.Errorf("take the lines: %w", ErrLineBusy)
			}
			if p.failAcquire > 0 {
				p.failAcquire--
				return nil, errors.New("lease refused")
			}
			cp := Allocation{Allowed: slices.Clone(alloc.Allowed), Routes: slices.Clone(alloc.Routes), Handoffs: slices.Clone(alloc.Handoffs), PeerPending: slices.Clone(alloc.PeerPending), AfterBlock: alloc.AfterBlock}
			r.allocs[id] = append(r.allocs[id], cp)
			r.events = append(r.events, "acquire "+id)
			for _, line := range alloc.Allowed {
				r.live[line]++
				if r.live[line] > 1 {
					r.overlap = append(r.overlap, line)
				}
			}
			var once sync.Once
			return func() {
				once.Do(func() {
					r.mu.Lock()
					defer r.mu.Unlock()
					r.events = append(r.events, "release "+id)
					for _, line := range alloc.Allowed {
						r.live[line]--
					}
				})
			}, nil
		}
		if p.declineHandoffs {
			spec.TookOver = func() []Handoff { return nil }
		}
		spec.Run = func(ctx context.Context, report func(Progress)) error {
			r.mu.Lock()
			r.passes[id]++
			block := p.blockRun
			r.mu.Unlock()
			report(Progress{State: "healthy", Sent: 1})
			if block {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func (r *routed) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *routed) config() Config {
	return Config{Root: r.root, Version: "dev", Interval: 10 * time.Millisecond, Refresh: 10 * time.Millisecond, Load: r.load, Logf: r.logf}
}

// start runs a supervisor and returns a stop function that waits for it.
func (r *routed) start() func() {
	r.t.Helper()
	s := New(r.config())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				r.t.Error(err)
			}
		})
	}
	r.t.Cleanup(stop)
	return stop
}

func (r *routed) snapshot() Snapshot {
	r.t.Helper()
	snap, err := r.stableSnapshot()
	if err != nil {
		// Before first publication, or while a test intentionally leaves damaged
		// old metadata, polling still reports absent. Keep the cause observable.
		r.t.Logf("routed snapshot unavailable after bounded metadata check: %v; logs=%v", err, r.logLines())
		return Snapshot{}
	}
	return snap
}

func (r *routed) stableSnapshot() (Snapshot, error) {
	// Use the production reader's one bounded retry for atomic replacement;
	// all other errors, and a second replacement, remain explicit failures.
	_, snap, err := readStable(func() (Runtime, Snapshot, error) {
		snap, err := r.snapshotRead(r.root)
		return Runtime{}, snap, err
	})
	return snap, err
}

func TestRoutedSnapshotRetainsStateAfterMetadataReplacement(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	want := Snapshot{Runtime: Runtime{InstanceID: "replacement"}, Complete: true, Jobs: []JobStatus{{ID: "default", State: "healthy"}}}
	calls := 0
	r.snapshotRead = func(string) (Snapshot, error) {
		calls++
		if calls == 1 {
			return Snapshot{}, ErrMetadataChanged
		}
		return want, nil
	}
	if got := r.snapshot(); !reflect.DeepEqual(got, want) || calls != 2 {
		t.Fatalf("metadata replacement became empty fixture state: calls=%d got=%+v want=%+v", calls, got, want)
	}
}

func TestRoutedSnapshotRefusalsStayBoundedAndVisible(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		err   error
		calls int
	}{
		{"another replacement", ErrMetadataChanged, 2},
		{"unsafe metadata", errors.New("unsafe metadata"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouted(t)
			calls := 0
			r.snapshotRead = func(string) (Snapshot, error) { calls++; return Snapshot{}, tc.err }
			if _, err := r.stableSnapshot(); err != tc.err || calls != tc.calls {
				t.Fatalf("refusal hidden or retried too often: calls=%d err=%v want=%v", calls, err, tc.err)
			}
		})
	}
}

func (r *routed) job(id string) (JobStatus, bool) {
	for _, j := range r.snapshot().Jobs {
		if j.ID == id {
			return j, true
		}
	}
	return JobStatus{}, false
}

func TestRoutedJobPollDistinguishesUnpublishedFromPublishedState(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	want := JobStatus{ID: "default", State: "healthy"}
	calls := 0
	r.snapshotRead = func(string) (Snapshot, error) {
		calls++
		if calls == 1 {
			return Snapshot{}, os.ErrNotExist
		}
		return Snapshot{Jobs: []JobStatus{want}}, nil
	}
	if got, found := r.job("default"); found || got.ID != "" || calls != 1 {
		t.Fatalf("unpublished state was fabricated or retried: calls=%d found=%v job=%+v", calls, found, got)
	}
	if got, found := r.job("default"); !found || !reflect.DeepEqual(got, want) || calls != 2 {
		t.Fatalf("next poll lost the published job: calls=%d found=%v job=%+v", calls, found, got)
	}
}

// waitJob waits for a snapshot of the job that satisfies ok.
func (r *routed) waitJob(id string, ok func(JobStatus) bool) JobStatus {
	r.t.Helper()
	var got JobStatus
	eventually(r.t, func() bool {
		j, found := r.job(id)
		got = j
		return found && ok(j)
	})
	return got
}

func (r *routed) allocations(id string) []Allocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.allocs[id])
}

func (r *routed) logLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.logs)
}

func (r *routed) command(action, id string) error {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Command(ctx, r.root, action, id)
}

// waitLoads lets the supervisor go through n more refreshes, counted by its Load
// calls instead of by the clock.
func (r *routed) waitLoads(n int) {
	r.t.Helper()
	r.mu.Lock()
	target := r.loads + n
	r.mu.Unlock()
	eventually(r.t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.loads >= target })
}

func TestRoutedPartialExclusionKeepsTheRestCollecting(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared, mine, theirs := r.env.line("codex", "shared"), r.env.line("claude", "mine"), r.env.line("claude", "theirs")
	r.add("default", "one", mine, shared)
	r.add("named:work", "one", theirs, shared)
	r.start()

	work := r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	def := r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	if work.ErrorCode != "" || work.State == "blocked" {
		t.Fatalf("a profile with a line left out keeps collecting the others: %+v", work)
	}
	if got := r.allocations("named:work"); len(got) == 0 || !slices.Equal(got[0].Allowed, []string{theirs}) {
		t.Fatalf("work was allowed %+v, want only its own line", got)
	}
	if got := r.allocations("default"); len(got) == 0 || !slices.Equal(lineSet(got[0].Allowed), lineSet([]string{mine, shared})) {
		t.Fatalf("default was allowed %+v", got)
	}
	if len(work.SharedSources) != 1 || work.SharedSources[0].Kind != "codex" || work.SharedSources[0].Holder != "default" || work.SharedSources[0].HolderState != HolderActive || work.SharedSources[0].Reason != ShareSame {
		t.Fatalf("shared sources = %+v", work.SharedSources)
	}
	if work.RouteOwner || !def.RouteOwner {
		t.Fatalf("route_owner: work %v, default %v", work.RouteOwner, def.RouteOwner)
	}
	if len(def.SharedSources) != 0 {
		t.Fatalf("the holder is told nothing: %+v", def.SharedSources)
	}
}

func TestRoutedWarningLeavesErrorCodeAlone(t *testing.T) {
	t.Parallel()
	// A job that is told to wait by the server shows both facts.
	r := newRouted(t)
	shared, theirs := r.env.line("codex", "shared"), r.env.line("claude", "theirs")
	r.add("default", "one", shared)
	r.add("named:work", "one", theirs, shared)
	s := New(r.config())
	wrapped := s.config.Load
	s.config.Load = func() ([]Spec, error) {
		specs, err := wrapped()
		for i := range specs {
			if specs[i].ID == "named:work" {
				specs[i].Run = func(ctx context.Context, report func(Progress)) error {
					report(Progress{State: "retrying", ErrorCode: "rate_limited", RetryAfter: time.Hour})
					return nil
				}
			}
		}
		return specs, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	t.Cleanup(func() { cancel(); <-done })
	j := r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "rate_limited" && len(j.SharedSources) == 1 })
	if j.State != "retrying" || j.NextRetryAt.IsZero() {
		t.Fatalf("a rate-limited job keeps its retry clock while it is warned: %+v", j)
	}
}

func TestRoutedAllExcludedBlocksAndSaysWhoHoldsIt(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:work", "one", shared)
	r.start()

	work := r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	if work.State != "blocked" || len(work.SharedSources) != 1 {
		t.Fatalf("work: %+v", work)
	}
	if n := len(r.allocations("named:work")); n != 0 {
		t.Fatalf("a profile with nothing to read took a lease %d times", n)
	}
	for _, action := range []string{"start", "restart"} {
		err := r.command(action, "named:work")
		var control *ControlError
		if !errors.As(err, &control) || control.Code != "profile_unavailable" || control.Reason != "source_overlap" || !slices.Equal(control.Holders, []string{"default"}) {
			t.Fatalf("%s: %#v", action, err)
		}
	}
	const want = "daemon control: profile_unavailable: every collection source of this profile is already collected by another profile (held by: default). Free a source in that profile, or run: cctrace profile claim <profile> --all"
	if err := r.command("restart", "named:work"); err == nil || err.Error() != want {
		t.Fatalf("message = %v\nwant      %s", err, want)
	}
	if err := r.command("stop", "named:work"); err != nil {
		t.Fatalf("stopping a blocked profile stays allowed: %v", err)
	}
}

func TestRoutedOwnerOutlivesTheSnapshotAndRestarts(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	// The first by ID would win a fresh decision; the profile that was running
	// first is the owner.
	r.add("named:zeta", "one", shared)
	stop := r.start()
	r.waitJob("named:zeta", func(j JobStatus) bool { return j.Sent > 0 })
	stop()

	r.add("default", "one", shared)
	if err := os.Remove(filepath.Join(r.root, "daemon-status.json")); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	z := r.waitJob("named:zeta", func(j JobStatus) bool { return j.Sent > 0 })
	if z.ErrorCode != "" || !z.RouteOwner {
		t.Fatalf("the owner changed across a restart without a snapshot: %+v", z)
	}
	if n := len(r.allocations("default")); n != 0 {
		t.Fatalf("default took a lease on a source it does not own")
	}
}

func TestRoutedFirstStartReservesInIDOrder(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("named:a", "one", shared)
	r.add("default", "one", shared)
	r.start()
	r.waitJob("named:a", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	def := r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	if !def.RouteOwner {
		t.Fatalf("default is the first by ID: %+v", def)
	}
	f, err := ReadRoutes(r.root)
	if err != nil || len(f.Routes) != 1 || f.Routes[0].Owner != "default" || f.Rev != 1 {
		t.Fatalf("persisted routes %+v, %v", f, err)
	}
	requirePrivate(t, filepath.Join(r.root, "daemon-routes.json"), 0600)
}

func TestRoutedPlansOnlyWhenSomethingChanged(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:work", "one", shared)
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	r.waitLoads(3)
	before, err := os.Stat(filepath.Join(r.root, "daemon-routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	r.waitLoads(20)
	after, err := os.Stat(filepath.Join(r.root, "daemon-routes.json"))
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("an idle daemon rewrote the routes file: %v %v", err, after)
	}
}

// A change of settings that leaves the sources alone is still a change: the
// status a plan sets is set again, and the allocations name the new revision,
// before the job can start on the old decision.
func TestRoutedPlansAgainWhenOnlyTheRevisionChanged(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:work", "one", shared)
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	r.edit("named:work", func(p *rprofile) {})
	eventually(t, func() bool {
		f, err := ReadAllocations(r.root)
		return err == nil && f.Jobs["named:work"].Revision == "2"
	})
	r.waitLoads(5)
	if n := len(r.allocations("named:work")); n != 0 {
		t.Fatalf("a profile with nothing to read took %d leases after an edit", n)
	}
	if j, _ := r.job("named:work"); j.ErrorCode != "duplicate_route" || j.State != "blocked" {
		t.Fatalf("an edit that changes no source cleared the block: %+v", j)
	}
}

func TestRoutedUnreadableFileClosesOnlyOverlappingLines(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared, mine, theirs := r.env.line("codex", "shared"), r.env.line("claude", "mine"), r.env.line("claude", "theirs")
	r.add("default", "one", mine, shared)
	r.add("named:work", "one", theirs, shared)
	path := filepath.Join(r.root, "daemon-routes.json")
	damaged := []byte(`{"version":1,"rev":4,"routes":[{"id":"zz`)
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	r.start()
	work := r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	def := r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	for _, id := range []string{"default", "named:work"} {
		got := r.allocations(id)
		if len(got) == 0 || len(got[0].Allowed) != 1 || strings.Contains(got[0].Allowed[0], "shared") {
			t.Fatalf("%s may read only what nobody shares: %+v", id, got)
		}
	}
	// Runs outside the daemon and `profile claim` read what is closed from the
	// allocations file; a line closed for want of a file names no route.
	published, err := ReadAllocations(r.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"default", "named:work"} {
		if got := published.Jobs[id].Excluded; !slices.Equal(got, []ExcludedLine{{Line: shared}}) {
			t.Fatalf("%s: the allocations file says it is held back from %+v", id, got)
		}
	}
	for _, j := range []JobStatus{work, def} {
		if len(j.SharedSources) != 1 || j.SharedSources[0].HolderState != HolderUnknown || j.SharedSources[0].Holder != "" || j.SharedSources[0].Source != "" {
			t.Fatalf("%s: an unknown holder names nobody: %+v", j.ID, j.SharedSources)
		}
		if j.RouteOwner || j.ErrorCode != "" {
			t.Fatalf("%s: %+v", j.ID, j)
		}
	}
	r.waitLoads(5)
	if got, _ := os.ReadFile(path); string(got) != string(damaged) {
		t.Fatalf("evidence was rewritten: %q", got)
	}
	var said []string
	for _, l := range r.logLines() {
		if strings.HasPrefix(l, "[routes] invalid") {
			said = append(said, l)
		}
	}
	if !slices.Equal(said, []string{"[routes] invalid reason=format"}) {
		t.Fatalf("log = %q", said)
	}
}

func TestRoutedFirstStartPersistFailureStartsNothingOnUnsavedDecisions(t *testing.T) {
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:work", "one", shared)
	var mu sync.Mutex
	failures := 2
	writeSeam(t, func(path string, data []byte, perm os.FileMode) error {
		mu.Lock()
		defer mu.Unlock()
		if failures > 0 {
			failures--
			return errors.New("disk full")
		}
		return atomicfile.Write(path, data, perm)
	})
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	mu.Lock()
	left := failures
	mu.Unlock()
	if left != 0 {
		t.Fatalf("started with %d failures still pending: it acted on a decision it had not saved", left)
	}
}

// A write that fails holds every decision back; the log says so, once for as long
// as it keeps failing.
func TestRoutedFailingWriteIsSaidOnce(t *testing.T) {
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	var mu sync.Mutex
	failures := 3
	writeSeam(t, func(path string, data []byte, perm os.FileMode) error {
		mu.Lock()
		defer mu.Unlock()
		if failures > 0 {
			failures--
			return errors.New("disk full")
		}
		return atomicfile.Write(path, data, perm)
	})
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	var said []string
	for _, l := range r.logLines() {
		if strings.HasPrefix(l, "[routes] write-failed") {
			said = append(said, l)
		}
	}
	if !slices.Equal(said, []string{"[routes] write-failed rev=1 reason=io"}) {
		t.Fatalf("log = %q", r.logLines())
	}
}

func TestRoutedUncertainWriteIsReadBackBeforeAnyDecision(t *testing.T) {
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:work", "one", shared)
	// The first write leaves a different, valid file behind and reports failure.
	var once sync.Once
	other := RouteFile{Version: 1, Rev: 9, Routes: []Route{{ID: "0000aa01", Rev: 1, Kind: "codex", Path: strings.TrimPrefix(shared, "codex:"), Destination: testDest("one"), Owner: "named:work", State: RouteActive, Since: time.Now().UTC().Add(-time.Hour)}}}
	writeSeam(t, func(path string, data []byte, perm os.FileMode) error {
		var failed error
		once.Do(func() {
			b, _ := marshalForTest(other)
			_ = atomicfile.Write(path, b, perm)
			failed = errors.New("sync directory failed")
		})
		if failed != nil {
			return failed
		}
		return atomicfile.Write(path, data, perm)
	})
	r.start()
	// The daemon trusts the file it finds, not the one it meant to write.
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 && j.RouteOwner })
	r.waitJob("default", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	said := false
	for _, l := range r.logLines() {
		if strings.HasPrefix(l, "[routes] write-uncertain rev=") {
			said = true
		}
	}
	if !said {
		t.Fatalf("log = %q", r.logLines())
	}
}

func TestRoutedLocalAnalysisProfilesAreNeverBlocked(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:only", "one") // local analysis only: no upload lines
	r.add("named:work", "one", shared).localAnalysis = true
	r.start()
	only := r.waitJob("named:only", func(j JobStatus) bool { return j.Sent > 0 })
	work := r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	if only.ErrorCode != "" || work.ErrorCode != "" || work.State == "blocked" {
		t.Fatalf("only %+v work %+v", only, work)
	}
	if got := r.allocations("named:work"); len(got) == 0 || len(got[0].Allowed) != 0 {
		t.Fatalf("a fully excluded profile that analyses locally runs with no upload line: %+v", got)
	}
	if len(work.SharedSources) != 1 {
		t.Fatalf("it is still told what it does not collect: %+v", work.SharedSources)
	}
	if n := len(r.snapshot().Jobs); n != 3 {
		t.Fatalf("job count %d != profile count 3", n)
	}
}

func marshalForTest(f RouteFile) ([]byte, error) { return json.Marshal(f) }

func TestApplyJobEndsAGenerationWhoseLinesChanged(t *testing.T) {
	s := New(Config{Root: t.TempDir()})
	cancelled := 0
	floor := time.Now().Add(time.Hour)
	j := &job{spec: Spec{ID: "one", Name: "one", Enabled: true}, status: JobStatus{ID: "one", Name: "one", State: "collecting"}, active: true, token: "gen1", cancel: func() { cancelled++ }, held: &Allocation{Allowed: []string{"a", "b"}}, next: floor}
	s.jobs["one"] = j

	s.applyJob(j, JobPlan{Allowed: []string{"b", "a"}})
	if j.reset || cancelled != 0 {
		t.Fatal("the same lines in another order are the same generation")
	}
	s.applyJob(j, JobPlan{Allowed: []string{"b"}})
	if !j.reset || cancelled != 1 || j.status.State != "stopping" {
		t.Fatalf("a generation that holds a line it may no longer read must end: reset=%v cancelled=%d state=%s", j.reset, cancelled, j.status.State)
	}
	// What the old generation still reports is not the new one's.
	s.handleEvent(event{id: "one", token: "gen1", progress: &Progress{State: "healthy", Sent: 9}}, false)
	if j.status.Sent != 0 || j.status.State != "stopping" {
		t.Fatalf("a late report from the old generation reached the status: %+v", j.status)
	}
	// The server's retry-after outlives the change of lines.
	j.status.ErrorCode, j.status.NextRetryAt = "rate_limited", floor
	s.handleEvent(event{id: "one", token: "gen1", exit: true, err: context.Canceled}, false)
	if j.active || j.reset || !j.next.Equal(floor) || j.status.State != "retrying" || j.held != nil {
		t.Fatalf("after the old generation ended: active=%v reset=%v next=%v held=%v state=%s", j.active, j.reset, j.next, j.held, j.status.State)
	}
	if got := *j.alloc; !sameLines(got.Allowed, []string{"b"}) {
		t.Fatalf("the next generation is allowed %v", got.Allowed)
	}
}

func TestApplyJobReleasesALeaseMadeForOtherLines(t *testing.T) {
	s := New(Config{Root: t.TempDir()})
	released := 0
	j := &job{spec: Spec{ID: "one", Name: "one", Enabled: true}, status: JobStatus{ID: "one", Name: "one", State: "healthy"}, release: func() { released++ }, held: &Allocation{Allowed: []string{"a"}}}
	s.jobs["one"] = j
	s.applyJob(j, JobPlan{Allowed: []string{"a"}})
	if released != 0 || j.release == nil {
		t.Fatal("the same lines keep the lease")
	}
	s.applyJob(j, JobPlan{Allowed: []string{"a", "c"}})
	if released != 1 || j.release != nil || j.held != nil {
		t.Fatalf("a job between passes gives up a lease made for other lines: released=%d", released)
	}
}

func TestApplyJobBlocksOnlyWhatIsFullyExcluded(t *testing.T) {
	s := New(Config{Root: t.TempDir()})
	cancelled := 0
	j := &job{spec: Spec{ID: "one", Name: "one", Enabled: true}, status: JobStatus{ID: "one", Name: "one", State: "collecting"}, active: true, token: "t", cancel: func() { cancelled++ }, held: &Allocation{Allowed: []string{"a"}}}
	s.jobs["one"] = j
	s.applyJob(j, JobPlan{AllExcluded: true})
	if j.status.ErrorCode != "duplicate_route" || cancelled != 1 {
		t.Fatalf("a running profile whose every line was taken stops: %+v cancelled=%d", j.status, cancelled)
	}
	s.handleEvent(event{id: "one", token: "t", exit: true, err: context.Canceled}, false)
	if j.status.State != "blocked" || j.active {
		t.Fatalf("after exit: %+v", j.status)
	}
	// The block is the plan's, so it lifts with it.
	j.next = time.Time{}
	s.applyJob(j, JobPlan{Allowed: []string{"a"}})
	if j.status.ErrorCode != "" || j.status.State != "unknown" || !s.mayStart(j) {
		t.Fatalf("a profile that may read again starts again: %+v", j.status)
	}
	// A rate limit is not the plan's to clear.
	j.status.ErrorCode = "rate_limited"
	s.applyJob(j, JobPlan{Allowed: []string{"a"}})
	if j.status.ErrorCode != "rate_limited" {
		t.Fatal("a plan cleared a rate limit")
	}
}

// edit changes a profile as the registry would: its revision advances.
func (r *routed) edit(id string, change func(p *rprofile)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(r.profs[id])
	r.profs[id].revision++
}

func (r *routed) noOverlap() {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.overlap) != 0 {
		r.t.Fatalf("two leases read a line at once: %v", r.overlap)
	}
}

// routeOf returns the route whose path is the folder of a "kind:folder" line.
func (r *routed) routeOf(line string) (Route, bool) {
	kind, folder, _ := strings.Cut(line, ":")
	f, err := ReadRoutes(r.root)
	if err != nil {
		return Route{}, false
	}
	for _, route := range f.Routes {
		if route.Kind == kind && route.Path == folder {
			return route, true
		}
	}
	return Route{}, false
}

func (r *routed) waitRoute(line string, ok func(Route) bool) Route {
	r.t.Helper()
	var got Route
	eventually(r.t, func() bool {
		route, found := r.routeOf(line)
		got = route
		return found && ok(route)
	})
	return got
}

// everAllowed reports whether any acquire of the job was allowed the line.
func (r *routed) everAllowed(id, line string) bool {
	for _, a := range r.allocations(id) {
		if slices.Contains(a.Allowed, line) {
			return true
		}
	}
	return false
}

// atomicfile.Write can fail after the file has been replaced. The daemon reads
// the file back and, finding what it meant to write, carries on without a pause
// and without calling the write uncertain.
func TestRoutedWriteThatLandedBeforeItFailedIsASuccess(t *testing.T) {
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:work", "one", shared)
	var once sync.Once
	writeSeam(t, func(path string, data []byte, perm os.FileMode) error {
		err := atomicfile.Write(path, data, perm)
		once.Do(func() { err = errors.New("sync directory failed") })
		return err
	})
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 && j.RouteOwner })
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	for _, l := range r.logLines() {
		if strings.HasPrefix(l, "[routes] write-uncertain") {
			t.Fatalf("a write that took effect was reported uncertain: %q", r.logLines())
		}
	}
}

func TestRoutedAllocationNamesTheRoutesToLock(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared, mine := r.env.line("codex", "shared"), r.env.line("claude", "mine")
	r.add("default", "one", mine, shared)
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	f, err := ReadRoutes(r.root)
	if err != nil || len(f.Routes) != 2 {
		t.Fatalf("%+v %v", f, err)
	}
	want := []string{f.Routes[0].ID, f.Routes[1].ID}
	slices.Sort(want)
	if got := r.allocations("default")[0].Routes; !slices.Equal(got, want) {
		t.Fatalf("the job takes the locks of %v, was told %v", want, got)
	}
}

// A line another run still reads is not an error: the job tries again at the
// next refresh, and nobody is told it failed.
func TestRoutedBusyLineIsRetried(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	p := r.add("default", "one", r.env.line("claude", "mine"))
	p.busyAcquire = 2
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	r.mu.Lock()
	left := p.busyAcquire
	r.mu.Unlock()
	if left != 0 || len(r.allocations("default")) != 1 {
		t.Fatalf("two busy refusals, then a lease: refusals left %d, leases %d", left, len(r.allocations("default")))
	}
	if j, _ := r.job("default"); j.ErrorCode != "" {
		t.Fatalf("a busy line showed up as an error: %+v", j)
	}
}

func TestBusyLineEndsAGenerationWithoutAFailure(t *testing.T) {
	s := New(Config{Root: t.TempDir()})
	j := &job{spec: Spec{ID: "one", Name: "one", Enabled: true}, status: JobStatus{ID: "one", Name: "one", State: "collecting"}, active: true, token: "t", cancel: func() {}, failures: 2}
	s.jobs["one"] = j
	s.handleEvent(event{id: "one", token: "t", exit: true, busy: true, err: fmt.Errorf("take the lines: %w", ErrLineBusy)}, false)
	if j.active || j.status.ErrorCode != "" || j.status.State != "unknown" || !j.next.IsZero() || !j.status.NextRetryAt.IsZero() {
		t.Fatalf("a busy line must cost nothing but a retry at the next refresh: %+v next=%v", j.status, j.next)
	}
	if !s.canStart(j) {
		t.Fatal("the job is not retried")
	}
}

// A route the daemon removes leaves its lock file behind; the daemon clears it
// once nobody holds it, and never touches the lock of a route that stands. A
// lock file it never knew as a route's is what a run outside it took for a line
// no route covers: removing it under a run that has just opened it would let a
// second run take the same lock on a new file, so it stays.
func TestRoutedLockFilesOfDeletedRoutesAreRemoved(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	mine, theirs := r.env.line("claude", "mine"), r.env.line("claude", "theirs")
	r.add("default", "one", mine)
	r.add("named:idle", "one", theirs).disabled = true
	lock := func(id string) string {
		release, err := LockLines(context.Background(), r.root, []string{id}, 0)
		if err != nil {
			t.Fatal(err)
		}
		release()
		return filepath.Join(r.root, "route-locks", id+".lock")
	}
	unreserved := lock(SourceLockID(theirs, testDest("one")))
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })

	live, _ := r.routeOf(mine)
	standing := lock(live.ID)
	r.waitLoads(10)
	if _, err := os.Stat(standing); err != nil {
		t.Fatalf("the lock of a route that stands was removed: %v", err)
	}
	r.edit("default", dropLine(mine))
	eventually(t, func() bool { _, err := os.Stat(standing); return os.IsNotExist(err) })
	if _, err := os.Stat(unreserved); err != nil {
		t.Fatalf("the lock a run takes for a line no route covers was removed: %v", err)
	}
}

// An unreadable routes file says nothing about which routes exist, so no lock file
// is removed on its word.
func TestRoutedUnknownRoutesKeepEveryLockFile(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	r.add("default", "one", r.env.line("claude", "mine"))
	if err := os.WriteFile(filepath.Join(r.root, "daemon-routes.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := LockLines(context.Background(), r.root, []string{"0000ffff"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	release()
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	r.waitLoads(10)
	if _, err := os.Stat(filepath.Join(r.root, "route-locks", "0000ffff.lock")); err != nil {
		t.Fatalf("a lock was removed while the routes are unknown: %v", err)
	}
}

// The owner of a Claude folder is told which profiles were left out of it, so its
// Syncer can send their session ends; a profile that appears later changes the
// list, and the job's next generation has it.
func TestRoutedOwnerIsToldWhichProfilesWereLeftOutOfItsClaudeFolder(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	home := r.env.line("claude", "home")
	r.add("default", "one", home)
	r.add("named:work", "one", home)
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	work := filepath.Join(r.root, "rt", "named_work")
	if got := r.allocations("default"); len(got) == 0 || !slices.Equal(got[0].PeerPending, []string{work}) {
		t.Fatalf("default's allocations = %+v, want its sibling's runtime directory", got)
	}
	if got := r.allocations("named:work"); len(got) != 0 {
		t.Fatalf("a fully excluded profile took a lease: %+v", got)
	}
	r.add("named:late", "one", home)
	third := filepath.Join(r.root, "rt", "named_late")
	eventually(t, func() bool {
		got := r.allocations("default")
		return len(got) >= 2 && slices.Equal(got[len(got)-1].PeerPending, []string{third, work})
	})
	r.noOverlap()
}
