package localdaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSupervisorErrorExitPreservesRetryFloor(t *testing.T) {
	root := t.TempDir()
	s := New(Config{Root: root, Refresh: 10 * time.Millisecond, Load: func() ([]Spec, error) {
		return []Spec{{ID: "one", Name: "one", RuntimeDir: root, Enabled: true, Revision: "1", Run: func(ctx context.Context, report func(Progress)) error {
			report(Progress{State: "retrying", ErrorCode: "rate_limited", RetryAfter: time.Hour})
			return errors.New("server returned 429")
		}}}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := runDaemon(t, ctx, s)
	eventuallyUp(t, d, func() bool {
		snap, err := ReadSnapshot(root)
		return err == nil && len(snap.Jobs) == 1 && snap.Jobs[0].State == "error"
	})
	cancel()
	if err := d.wait(); err != nil {
		t.Fatal(err)
	}
	j := s.jobs["one"]
	if j.next.Before(j.status.NextRetryAt) || j.next.Before(time.Now().Add(55*time.Minute)) {
		t.Fatalf("actual retry %s does not honour displayed server floor %s", j.next, j.status.NextRetryAt)
	}
}

func TestSupervisorIdlePublicationRate(t *testing.T) {
	root := t.TempDir()
	s := New(Config{Root: root, Load: func() ([]Spec, error) { return nil, nil }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	eventually(t, func() bool { _, err := ReadSnapshot(root); return err == nil })
	changes := 0
	var prior time.Time
	for end := time.Now().Add(1200 * time.Millisecond); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if info, err := os.Stat(filepath.Join(root, "daemon-status.json")); err == nil && !info.ModTime().Equal(prior) {
			changes++
			prior = info.ModTime()
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Logf("idle snapshot writes observed over 1.2s: %d", changes)
	if changes > 4 {
		t.Fatalf("idle status publication rate too high: %d in 1.2s", changes)
	}
}

func eventually(t *testing.T, predicate func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if predicate() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition not satisfied")
		}
	}
}

// runningDaemon is a supervisor under test. Run's result is kept, not only sent,
// so a wait can tell that the daemon is gone and say why, and the test can still
// join it afterwards.
type runningDaemon struct {
	done chan struct{}
	err  error
}

// runDaemon starts Run under ctx. The test cancels ctx; the cleanup waits for Run
// to return, so a failed test leaves no daemon holding files in its directory.
func runDaemon(t *testing.T, ctx context.Context, s *Supervisor) *runningDaemon {
	d := &runningDaemon{done: make(chan struct{})}
	go func() { d.err = s.Run(ctx, nil); close(d.done) }()
	t.Cleanup(func() { <-d.done })
	return d
}

// wait returns Run's result, once it has returned.
func (d *runningDaemon) wait() error { <-d.done; return d.err }

// eventuallyUp is eventually, except that a daemon that has stopped fails the test
// with the error it stopped on, rather than leaving the condition to time out.
func eventuallyUp(t *testing.T, d *runningDaemon, predicate func() bool) {
	t.Helper()
	eventually(t, func() bool {
		select {
		case <-d.done:
			t.Fatalf("the daemon stopped before the condition held: %v", d.err)
		default:
		}
		return predicate()
	})
}

func TestSupervisorLateOldRevisionProgressStaysStopping(t *testing.T) {
	root := t.TempDir()
	s := New(Config{Root: root})
	j := &job{spec: Spec{ID: "one", Name: "one", Revision: "new", Enabled: true}, status: JobStatus{ID: "one", Name: "one", State: "stopping"}, active: true, token: "old-pass", reset: true, cancel: func() {}}
	s.jobs["one"] = j
	late := Progress{State: "healthy", Sent: 9}
	s.handleEvent(event{id: "one", token: "old-pass", progress: &late}, false)
	if err := s.publish(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Jobs[0].State != "stopping" || snapshot.Jobs[0].Sent != 0 || !j.next.IsZero() {
		t.Fatalf("old revision success reached new configuration: %+v", snapshot.Jobs[0])
	}
	s.handleEvent(event{id: "one", token: "old-pass", exit: true, err: context.Canceled}, false)
	if j.active || !j.next.IsZero() || j.status.State != "unknown" {
		t.Fatal("old pass delayed the replacement revision")
	}
}

func TestSupervisorKeepsExistingRouteOwnerWhenProfilesChange(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	ids := []string{"two"}
	calls := map[string]int{}
	load := func() ([]Spec, error) {
		mu.Lock()
		defer mu.Unlock()
		var specs []Spec
		for _, id := range ids {
			id := id
			specs = append(specs, Spec{ID: id, Name: id, RuntimeDir: filepath.Join(root, id), Enabled: true, Revision: "1", Destination: "same", Sources: []string{"claude:/fixture/source"}, Run: func(ctx context.Context, report func(Progress)) error {
				mu.Lock()
				calls[id]++
				mu.Unlock()
				report(Progress{State: "healthy", Sent: 1})
				return nil
			}})
		}
		return specs, nil
	}
	s := New(Config{Root: root, Load: load, Interval: 10 * time.Millisecond, Refresh: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			<-done
		}
	})
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return calls["two"] > 0 })
	mu.Lock()
	ids = []string{"one", "two"}
	mu.Unlock()
	eventually(t, func() bool {
		snap, err := ReadSnapshot(root)
		return err == nil && len(snap.Jobs) == 2 && snap.Jobs[0].ErrorCode == "duplicate_route"
	})
	mu.Lock()
	ids = []string{"one"}
	mu.Unlock()
	eventually(t, func() bool { snap, err := ReadSnapshot(root); return err == nil && len(snap.Jobs) == 1 })
	mu.Lock()
	one := calls["one"]
	mu.Unlock()
	if one != 0 {
		t.Fatalf("new duplicate silently took over an existing route: %d passes", one)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	joined = true
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- New(Config{Root: root, Load: load, Refresh: 10 * time.Millisecond}).Run(ctx, nil) }()
	joined = false
	eventually(t, func() bool {
		snap, err := ReadSnapshot(root)
		return err == nil && len(snap.Jobs) == 1 && snap.Jobs[0].ErrorCode == "duplicate_route"
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	joined = true
}

func TestSupervisorQuiescesPausedLegacyProfiles(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	quiesced, runs := 0, 0
	load := func() ([]Spec, error) {
		return []Spec{{ID: "one", Name: "one", RuntimeDir: root, Revision: "1", Enabled: true, BlockedReason: "migration_conflict", Quiesce: func(ctx context.Context) error { mu.Lock(); quiesced++; mu.Unlock(); return nil }, Run: func(context.Context, func(Progress)) error { mu.Lock(); runs++; mu.Unlock(); return nil }}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(Config{Root: root, Load: load, Refresh: 10 * time.Millisecond}).Run(ctx, nil) }()
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return quiesced == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if runs != 0 {
		t.Fatal("paused legacy job forwarded data")
	}
}

func TestSupervisorIndependentJobsAndDynamicChanges(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	var calls = map[string]int{}
	specs := []Spec{}
	runner := func(id string, blocked bool) Runner {
		return func(ctx context.Context, report func(Progress)) error {
			mu.Lock()
			calls[id]++
			mu.Unlock()
			if blocked {
				report(Progress{State: "blocked", BlockedFiles: 1})
				<-ctx.Done()
				return ctx.Err()
			}
			report(Progress{State: "healthy", Sent: 1})
			return nil
		}
	}
	for _, id := range []string{"one", "two"} {
		specs = append(specs, Spec{ID: id, Name: id, Enabled: true, Revision: "1", RuntimeDir: filepath.Join(root, id), Destination: id, Sources: []string{"claude:/source"}, Run: runner(id, id == "one")})
	}
	load := func() ([]Spec, error) { mu.Lock(); defer mu.Unlock(); return append([]Spec(nil), specs...), nil }
	s := New(Config{Root: root, Version: "dev", Interval: 10 * time.Millisecond, Refresh: 10 * time.Millisecond, Load: load})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := runDaemon(t, ctx, s)
	eventuallyUp(t, d, func() bool {
		snap, err := ReadSnapshot(root)
		return err == nil && len(snap.Jobs) == 2 && snap.Jobs[0].State == "blocked"
	})
	eventuallyUp(t, d, func() bool { mu.Lock(); defer mu.Unlock(); return calls["two"] >= 3 })
	commandCtx, commandCancel := context.WithTimeout(ctx, 5*time.Second)
	defer commandCancel()
	if err := Command(commandCtx, root, "stop", "one"); err != nil {
		t.Fatal(err)
	}
	// Publication can replace metadata during a read; observe a coherent stopped snapshot.
	eventuallyUp(t, d, func() bool {
		snap, err := ReadSnapshot(root)
		if err != nil {
			if err.Error() == "daemon metadata changed" {
				return false
			}
			t.Fatalf("read stopped job snapshot: %v", err)
		}
		return len(snap.Jobs) == 2 && snap.Jobs[0].ID == "one" && snap.Jobs[0].State == "stopped"
	})
	mu.Lock()
	before := calls["one"]
	specs[0].Revision = "2"
	specs = append(specs, Spec{ID: "three", Name: "three", Enabled: true, Revision: "1", RuntimeDir: filepath.Join(root, "three"), Destination: "three", Sources: []string{"claude:/source"}, Run: runner("three", false)})
	mu.Unlock()
	eventuallyUp(t, d, func() bool { mu.Lock(); defer mu.Unlock(); return calls["three"] > 0 })
	mu.Lock()
	after := calls["one"]
	mu.Unlock()
	if before != after {
		t.Fatal("configuration refresh resumed a manually stopped job")
	}
	if err := Command(commandCtx, root, "restart", "one"); err != nil {
		t.Fatal(err)
	}
	eventuallyUp(t, d, func() bool { mu.Lock(); defer mu.Unlock(); return calls["one"] > before })
	mu.Lock()
	specs = specs[1:]
	mu.Unlock()
	eventuallyUp(t, d, func() bool { snap, err := ReadSnapshot(root); return err == nil && len(snap.Jobs) == 2 })
	cancel()
	if err := d.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorDuplicateRouteAndRetryIsolation(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	calls := map[string]int{}
	load := func() ([]Spec, error) {
		var specs []Spec
		for _, id := range []string{"one", "two", "three"} {
			id := id
			destination := "dest"
			if id == "three" {
				destination = "other"
			}
			specs = append(specs, Spec{ID: id, Name: id, Revision: "1", Enabled: true, RuntimeDir: filepath.Join(root, id), Destination: destination, Sources: []string{"claude:/source"}, Run: func(ctx context.Context, report func(Progress)) error {
				mu.Lock()
				calls[id]++
				mu.Unlock()
				if id == "one" {
					report(Progress{State: "retrying", RetryAfter: time.Hour, ErrorCode: "rate_limited"})
				} else {
					report(Progress{State: "healthy"})
				}
				return nil
			}})
		}
		return specs, nil
	}
	s := New(Config{Root: root, Version: "dev", Interval: 10 * time.Millisecond, Refresh: 10 * time.Millisecond, Load: load})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, nil) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			<-done
		}
	})
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return calls["three"] >= 3 })
	mu.Lock()
	one, two := calls["one"], calls["two"]
	mu.Unlock()
	if one != 1 || two != 0 {
		t.Fatalf("retry/route isolation failed: %v", calls)
	}
	eventually(t, func() bool {
		snap, err := ReadSnapshot(root)
		return err == nil && len(snap.Jobs) == 3 && snap.Jobs[2].ErrorCode == "duplicate_route"
	})
	if err := Command(ctx, root, "restart", "one"); err == nil {
		t.Fatal("restart bypassed server retry-after")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	joined = true
}

func TestSupervisorOwnsOneRootAndPersistsJobStops(t *testing.T) {
	root := t.TempDir()
	load := func() ([]Spec, error) {
		return []Spec{{ID: "one", Name: "one", Revision: "1", Enabled: true, RuntimeDir: filepath.Join(root, "one"), Run: func(ctx context.Context, report func(Progress)) error { report(Progress{State: "healthy"}); return nil }}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- New(Config{Root: root, Version: "dev", Load: load, Refresh: 10 * time.Millisecond}).Run(ctx, nil)
	}()
	eventually(t, func() bool { _, err := ReadSnapshot(root); return err == nil })
	if err := New(Config{Root: root, Version: "dev", Load: load}).Run(ctx, nil); err != ErrAlreadyRunning {
		t.Fatalf("second root owner: %v", err)
	}
	if err := Command(ctx, root, "stop", "one"); err != nil {
		t.Fatal(err)
	}
	if err := Command(ctx, root, "shutdown", ""); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancel()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go func() {
		done <- New(Config{Root: root, Version: "dev", Load: load, Refresh: 10 * time.Millisecond}).Run(ctx, nil)
	}()
	eventually(t, func() bool {
		snap, err := ReadSnapshot(root)
		return err == nil && len(snap.Jobs) == 1 && snap.Jobs[0].State == "stopped"
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "daemon-stops.json")); err != nil {
		t.Fatal(err)
	}
}

// A job that could not start because the policy lock stayed held is retried like
// any setup failure, and says so under its own code: it is neither a broken
// setup nor a legacy migration.
func TestSupervisorReportsPolicyLockBusyAsItsOwnCode(t *testing.T) {
	root := t.TempDir()
	s := New(Config{Root: root, Refresh: 10 * time.Millisecond, Load: func() ([]Spec, error) {
		return []Spec{{ID: "one", Name: "one", RuntimeDir: root, Enabled: true, Revision: "1",
			Acquire: func(context.Context, Allocation) (func(), error) {
				return nil, fmt.Errorf("start barrier: %w", ErrPolicyLockBusy)
			},
			Run: func(context.Context, func(Progress)) error { return nil }}}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := runDaemon(t, ctx, s)
	var job JobStatus
	eventuallyUp(t, d, func() bool {
		snap, err := ReadSnapshot(root)
		if err != nil || len(snap.Jobs) != 1 || snap.Jobs[0].ErrorCode == "" {
			return false
		}
		job = snap.Jobs[0]
		return true
	})
	cancel()
	if err := d.wait(); err != nil {
		t.Fatal(err)
	}
	if job.ErrorCode != "policy_lock_busy" || job.State != "error" || !job.NextRetryAt.After(time.Now().Add(-time.Second)) {
		t.Fatalf("job %+v; want error policy_lock_busy with a retry scheduled", job)
	}
}
