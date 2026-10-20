package localdaemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// readers counts the leases that read one folder for one destination at a time,
// by the folder on disk, and can hold a profile's job past its cancellation, as
// a pass that takes a while to end does.
type readers struct {
	mu     sync.Mutex
	now    map[string]int
	most   map[string]int
	read   map[string][]string      // what each profile's leases were for
	linger map[string]chan struct{} // by profile ID
}

func (rd *readers) keys(destination string, lines []string) []string {
	var keys []string
	for _, line := range lines {
		kind, folder, _ := strings.Cut(line, ":")
		if resolved, err := filepath.EvalSymlinks(folder); err == nil {
			folder = resolved
		}
		keys = append(keys, destination+"|"+kind+"|"+folder)
	}
	return keys
}

// startReading runs a supervisor over r whose jobs are counted by rd, and returns
// a stop function that waits for it.
func (r *routed) startReading(rd *readers) func() {
	r.t.Helper()
	rd.now, rd.most, rd.read = map[string]int{}, map[string]int{}, map[string][]string{}
	s := New(r.config())
	load := s.config.Load
	s.config.Load = func() ([]Spec, error) {
		specs, err := load()
		for i := range specs {
			sp := specs[i]
			acquire, run := sp.Acquire, sp.Run
			specs[i].Acquire = func(ctx context.Context, alloc Allocation) (func(), error) {
				release, err := acquire(ctx, alloc)
				if err != nil {
					return release, err
				}
				keys := rd.keys(sp.Destination, alloc.Allowed)
				rd.mu.Lock()
				for _, k := range keys {
					rd.now[k]++
					rd.most[k] = max(rd.most[k], rd.now[k])
				}
				rd.read[sp.ID] = append(rd.read[sp.ID], keys...)
				rd.mu.Unlock()
				var once sync.Once
				return func() {
					once.Do(func() {
						rd.mu.Lock()
						for _, k := range keys {
							rd.now[k]--
						}
						rd.mu.Unlock()
						release()
					})
				}, nil
			}
			specs[i].Run = func(ctx context.Context, report func(Progress)) error {
				err := run(ctx, report)
				rd.mu.Lock()
				linger := rd.linger[sp.ID]
				rd.mu.Unlock()
				if linger != nil && ctx.Err() != nil {
					<-linger
				}
				return err
			}
		}
		return specs, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			rd.let()
			cancel()
			if err := <-done; err != nil {
				r.t.Error(err)
			}
		})
	}
	r.t.Cleanup(stop)
	return stop
}

// let lets every held job end.
func (rd *readers) let() {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	for _, ch := range rd.linger {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func (rd *readers) mostAt(destination, line string) int {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	return rd.most[rd.keys(destination, []string{line})[0]]
}

// hasRead: a lease of the profile read the line for the destination.
func (rd *readers) hasRead(id, destination, line string) bool {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	return slices.Contains(rd.read[id], rd.keys(destination, []string{line})[0])
}

// When the owner of a source follows a policy to the destination another profile
// moved to first, the other profile's route there goes. The owner may read the
// source there only once that profile's job has stopped reading it.
func TestRoutedRoutesMergedByAMoveAreNeverReadTwiceAtOnce(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:work", "two", shared).blockRun = true
	rd := &readers{linger: map[string]chan struct{}{"named:work": make(chan struct{})}}
	r.startReading(rd)
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })

	r.edit("default", func(p *rprofile) { p.dest = "two" })
	r.waitJob("named:work", func(j JobStatus) bool { return j.State == "stopping" })
	r.waitLoads(10)
	rd.let()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	eventually(t, func() bool { return rd.hasRead("default", testDest("two"), shared) })
	if n := rd.mostAt(testDest("two"), shared); n != 1 {
		t.Fatalf("%d leases read the source for one destination at once", n)
	}
	f, err := ReadRoutes(r.root)
	if err != nil || len(f.Routes) != 1 || f.Routes[0].Owner != "default" || f.Routes[0].Destination != testDest("two") {
		t.Fatalf("routes %+v, %v", f, err)
	}
}

// A folder replaced by a link to another makes the routes of two profiles one
// source. The daemon does not leave both profiles out of it: the one whose route
// loses stops before the other reads the folder again, and the file it writes is
// one the next start reads.
func TestRoutedRoutesTheDiskMadeOneAreResolved(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	a, b := r.env.line("codex", "codex-a"), r.env.line("codex", "codex-b")
	for _, line := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(strings.TrimPrefix(line, "codex:"), "sessions"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	r.add("default", "one", a)
	r.add("named:work", "one", b).blockRun = true
	rd := &readers{linger: map[string]chan struct{}{"named:work": make(chan struct{})}}
	stop := r.startReading(rd)
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })

	if err := os.RemoveAll(strings.TrimPrefix(b, "codex:")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(strings.TrimPrefix(a, "codex:"), strings.TrimPrefix(b, "codex:")); err != nil {
		t.Skip("symlinks unavailable")
	}
	r.mu.Lock()
	since := len(r.events)
	r.mu.Unlock()
	// Nothing the daemon is told of has changed; it would find out at its next full
	// plan. A profile added elsewhere brings that plan forward.
	r.add("named:other", "two", r.env.line("claude", "other"))
	r.waitJob("named:work", func(j JobStatus) bool { return j.State == "stopping" })
	r.waitLoads(10)
	rd.let()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	var after []string
	eventually(t, func() bool {
		r.mu.Lock()
		after = slices.Clone(r.events[since:])
		r.mu.Unlock()
		return slices.Contains(after, "acquire default")
	})
	if i := slices.Index(after, "release named:work"); i < 0 || i > slices.Index(after, "acquire default") {
		t.Fatalf("default read the folder again before work stopped: %v", after)
	}
	f, err := ReadRoutes(r.root)
	if err != nil {
		t.Fatalf("the file left behind is not one a start reads: %v", err)
	}
	for _, route := range f.Routes {
		if route.Kind == "codex" && route.Owner != "default" {
			t.Fatalf("routes %+v", f.Routes)
		}
	}

	stop()
	first := r.snapshot().InstanceID
	r.start()
	eventually(t, func() bool { snap := r.snapshot(); return snap.InstanceID != "" && snap.InstanceID != first })
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	for _, l := range r.logLines() {
		if strings.HasPrefix(l, "[routes] invalid") {
			t.Fatalf("the next start found the file unusable: %q", r.logLines())
		}
	}
}
