package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

// HOME, the runtime seams and the local collection daemon's root are process-wide,
// so none of these tests run in parallel.

// runningSupervisor starts a local collection daemon that runs the default profile,
// under HOME as the test has it, and stops it with the test.
func runningSupervisor(t *testing.T) string {
	t.Helper()
	root := profile.DefaultDir()
	startSupervisor(t, localdaemon.New(localdaemon.Config{Root: root, Refresh: 5 * time.Millisecond, Load: func() ([]localdaemon.Spec, error) {
		return []localdaemon.Spec{{ID: "default", Name: "default", RuntimeDir: root, Enabled: true, Revision: "one", Run: func(_ context.Context, report func(localdaemon.Progress)) error {
			report(localdaemon.Progress{State: "healthy"})
			return nil
		}}}, nil
	}}))
	return root
}

// startSupervisor runs the daemon until the test ends and returns once it is
// ready. A Run that fails before it is ready fails the test with its error: the
// ready callback never comes then, and waiting on it alone hangs the test.
func startSupervisor(t *testing.T, supervisor *localdaemon.Supervisor) localdaemon.Runtime {
	t.Helper()
	ready := make(chan localdaemon.Runtime, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- supervisor.Run(ctx, func(rt localdaemon.Runtime) { ready <- rt }) }()
	select {
	case rt := <-ready:
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
		return rt
	case err := <-done:
		cancel()
		t.Fatalf("the daemon stopped before it was ready: %v", err)
		return localdaemon.Runtime{}
	}
}

// standAloneWatcherFixture is the case the local collection daemon makes of an
// upgrade: the profile's sync is still an old stand-alone watcher, the daemon is
// running, and the watcher's stop is a seam that counts. Replacement processes are
// counted too; none should be needed.
type standAloneWatcherFixture struct {
	stops, spawns, acks atomic.Int32
}

func newStandAloneWatcherFixture(t *testing.T) *standAloneWatcherFixture {
	t.Helper()
	resetSyncControlHooks(t)
	f := &standAloneWatcherFixture{}
	oldRead, oldFree, oldStop := policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn
	t.Cleanup(func() { policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn = oldRead, oldFree, oldStop })
	watcher := &syncRuntime{PID: 1, InstanceID: "stand-alone-watcher", Interval: time.Second}
	policyReadRuntimeFn = func(string) (*syncRuntime, bool, error) { return watcher, true, nil }
	policySyncFreeFn = func(string) (bool, error) { return false, nil }
	policyStopRuntimeFn = func(context.Context, string) error { f.stops.Add(1); return nil }
	spawnSyncProcessFn = func([]string, string) (int, error) { f.spawns.Add(1); return 42, nil }
	waitForSyncStartAckFn = func(string, time.Duration) (*syncStartAck, error) {
		f.acks.Add(1)
		return &syncStartAck{Status: syncStartAckAlreadyRunning}, nil
	}
	return f
}

// wantStartedOnSupervisor checks the daemon was asked to start the default profile.
func wantStartedOnSupervisor(t *testing.T, root string) {
	t.Helper()
	var stops map[string]bool
	if err := localdaemon.ReadJSON(filepath.Join(root, "daemon-stops.json"), &stops); err != nil {
		t.Fatalf("the daemon was never asked to start the profile: %v", err)
	}
	if stopped, asked := stops["default"]; !asked || stopped {
		t.Fatalf("daemon stop flags %v; want the default profile started", stops)
	}
}

// wantSettledHandoff checks no recovery is left pending.
func wantSettledHandoff(t *testing.T) {
	t.Helper()
	handoff, err := readPolicyHandoff("")
	if err != nil || handoff == nil || !handoff.Done {
		t.Fatalf("handoff %+v %v; want it finished", handoff, err)
	}
	if serverPolicyRecoveryPending("") {
		t.Fatal("a recovery is still pending")
	}
}

// A policy moves a profile whose sync is an old stand-alone watcher while the
// local collection daemon runs. The daemon is the one that runs the profile from
// now on, so the run asks it to start the profile instead of spawning a
// replacement that could only answer "already running": the move stays committed,
// the handoff is finished and the policy is recorded as applied. Run again, it
// finds nothing to do.
func TestServerPolicyHandsAStandAloneWatcherToARunningSupervisor(t *testing.T) {
	pol, _ := policyStateFixture(t, false)
	f := newStandAloneWatcherFixture(t)
	root := runningSupervisor(t)

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	state, err := readServerPolicyState("")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "applied" || state.Sequence != pol.Sequence {
		t.Fatalf("state %+v; want the policy applied", state)
	}
	moved, err := loadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Server.Endpoint != pol.To.OTEL || moved.Server.SyncEndpoint != pol.To.Sync {
		t.Fatalf("profile at %q %q, want the policy's %q %q", moved.Server.Endpoint, moved.Server.SyncEndpoint, pol.To.OTEL, pol.To.Sync)
	}
	wantSettledHandoff(t)
	wantStartedOnSupervisor(t, root)
	if f.stops.Load() != 1 || f.spawns.Load() != 0 || f.acks.Load() != 0 {
		t.Fatalf("%d stops, %d spawns, %d acks; want the watcher stopped once and no replacement process", f.stops.Load(), f.spawns.Load(), f.acks.Load())
	}

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if f.stops.Load() != 1 || f.spawns.Load() != 0 {
		t.Fatalf("the second run stopped %d and spawned %d more", f.stops.Load()-1, f.spawns.Load())
	}
}

// A run that was interrupted between the watcher's stop and its restart leaves the
// handoff pending. The recovery restarts what was stopped by what runs now: with
// the daemon running that is a start of the profile on it, however the watcher was
// recorded, and the recovery ends instead of meeting "already running" every run.
func TestServerPolicyRecoveryStartsTheStoppedProfileOnARunningSupervisor(t *testing.T) {
	pol, _ := policyStateFixture(t, false)
	f := newStandAloneWatcherFixture(t)
	root := runningSupervisor(t)
	if err := writePolicyHandoff("", &policyHandoff{Runtime: &syncRuntime{PID: 1, InstanceID: "stand-alone-watcher", Interval: time.Second}}); err != nil {
		t.Fatal(err)
	}
	if !serverPolicyRecoveryPending("") {
		t.Fatal("fixture: no recovery pending")
	}

	if err := recoverServerPolicyChanges(context.Background(), ""); err != nil {
		t.Fatalf("recovery: %v", err)
	}

	wantSettledHandoff(t)
	wantStartedOnSupervisor(t, root)
	if f.spawns.Load() != 0 || f.acks.Load() != 0 {
		t.Fatalf("%d spawns, %d acks; want no replacement process", f.spawns.Load(), f.acks.Load())
	}
	// Nothing is left in the way of the policy itself.
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run after the recovery: %v", err)
	}
	if got := readStateSequence(t); got != pol.Sequence {
		t.Fatalf("applied sequence %d, want %d", got, pol.Sequence)
	}
	wantSettledHandoff(t)
}

// The daemon can start between the run's look and the replacement's, which then
// answers "already running". The run takes that as a start and asks the daemon for
// the profile itself: the replacement's own request may not have happened (for the
// default profile it makes none), and a start the daemon has already seen is a no-op
// for it.
func TestServerPolicyTakesAlreadyRunningFromASupervisorThatJustStarted(t *testing.T) {
	pol, _ := policyStateFixture(t, false)
	f := newStandAloneWatcherFixture(t)
	root := runningSupervisor(t)
	oldSupervisor := policySupervisorRunningFn
	t.Cleanup(func() { policySupervisorRunningFn = oldSupervisor })
	var looks atomic.Int32
	policySupervisorRunningFn = func() bool { return looks.Add(1) > 1 }

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := readStateSequence(t); got != pol.Sequence {
		t.Fatalf("applied sequence %d, want %d", got, pol.Sequence)
	}
	wantSettledHandoff(t)
	wantStartedOnSupervisor(t, root)
	if f.spawns.Load() != 1 || f.acks.Load() != 1 {
		t.Fatalf("%d spawns, %d acks; want the one replacement and its answer", f.spawns.Load(), f.acks.Load())
	}
}

// A managed job that finds a handoff pending while it passes the start barrier is
// the restart: its supervisor is the process the recovery runs in, and a start
// command to it would be queued behind the job that waits for the answer -- the
// job ran out the whole barrier wait on it, holding the global policy lock, then
// started over. The recovery finishes the handoff and leaves the start to the job.
func TestManagedJobRecoveringItsOwnHandoffDoesNotCommandItsSupervisor(t *testing.T) {
	policyStateFixture(t, false)
	f := newStandAloneWatcherFixture(t)
	if err := writePolicyHandoff("", &policyHandoff{Runtime: &syncRuntime{PID: 1, InstanceID: "stand-alone-watcher", Interval: time.Second}}); err != nil {
		t.Fatal(err)
	}
	root := profile.DefaultDir()
	entry, err := (profile.Registry{Root: root}).Load("default")
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		took time.Duration
		err  error
	}
	acquired := make(chan outcome, 8)
	supervisor := localdaemon.New(localdaemon.Config{Root: root, Refresh: 5 * time.Millisecond, Load: func() ([]localdaemon.Spec, error) {
		return []localdaemon.Spec{{ID: "default", Name: "default", RuntimeDir: root, Enabled: true, Revision: entry.Revision,
			Acquire: func(ctx context.Context, _ localdaemon.Allocation) (func(), error) {
				start := time.Now()
				release, err := prepareManagedProfile(ctx, entry)
				if err == nil {
					release()
				}
				acquired <- outcome{time.Since(start), err}
				return func() {}, err
			},
			Run: func(_ context.Context, report func(localdaemon.Progress)) error {
				report(localdaemon.Progress{State: "healthy"})
				return nil
			}}}, nil
	}})
	startSupervisor(t, supervisor)

	select {
	case got := <-acquired:
		if got.err != nil {
			t.Fatalf("the job's start barrier ended with %v", got.err)
		}
		t.Logf("start barrier passed after %v", got.took)
	case <-time.After(10 * time.Second):
		t.Fatalf("the job is still inside its start barrier; global policy lock held: %v", policyLockHeldNow(globalPolicyLockPath(t)))
	}

	wantSettledHandoff(t)
	if f.spawns.Load() != 0 {
		t.Fatalf("%d replacement processes for a job that is itself the restart", f.spawns.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "daemon-stops.json")); !os.IsNotExist(err) {
		t.Fatalf("the job commanded its own supervisor: %v", err)
	}
}

// A command to the daemon waits for its answer only as long as the caller does: a
// recovery in a startup's barrier is cut off with the barrier, and a cancelled
// caller gets the cancellation, not a daemon that was slow to answer.
func TestStartProfileOnSupervisorFollowsContext(t *testing.T) {
	policyStateFixture(t, false)
	runningSupervisor(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := startProfileOnSupervisor(ctx, "")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start ended with %v, want the cancellation", err)
	}
	if time.Since(start) >= 10*time.Second {
		t.Fatalf("a cancelled start waited %v", time.Since(start))
	}
}
