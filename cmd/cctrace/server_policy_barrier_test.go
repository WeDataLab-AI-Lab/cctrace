package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/configtxn"
	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

// HOME and the policy lock waits are process-wide, so none of these tests run in
// parallel.

// narrowBarrierWait gives a sync startup a short wait for the policy lock and a
// long wait for everything else, so a test tells which one it hit.
func narrowBarrierWait(t *testing.T) {
	t.Helper()
	originalBarrier, originalSettings := policyBarrierWait, policySettingsLockWait
	policyBarrierWait, policySettingsLockWait = testPolicyLockWait, 10*time.Second
	t.Cleanup(func() { policyBarrierWait, policySettingsLockWait = originalBarrier, originalSettings })
}

// A startup that waited out the policy lock says what it waited for. It used to
// report "another sync is already running", which it was not: a policy apply or
// another profile's startup held the lock.
func TestPolicySyncStartupBarrierReportsPolicyLockWait(t *testing.T) {
	usePolicyRuntimeHome(t)
	narrowBarrierWait(t)
	release := holdPolicyLock(t, globalPolicyLockPath(t))

	start := time.Now()
	_, err := policySyncStartupBarrier(context.Background(), "", "", false)
	elapsed := time.Since(start)

	if !errors.Is(err, localdaemon.ErrPolicyLockBusy) || errors.Is(err, errSyncAlreadyRunning) {
		t.Fatalf("barrier ended with %v, want the policy lock wait and not another sync", err)
	}
	if strings.Contains(err.Error(), "sync is already running") || !strings.Contains(err.Error(), "policy lock") {
		t.Fatalf("message %q should name the policy lock, not a running sync", err)
	}
	if elapsed < testPolicyLockWait || elapsed >= 5*time.Second {
		t.Fatalf("waited %v, want about the barrier's %v", elapsed, testPolicyLockWait)
	}
	release()
	free, err := policySyncStartupBarrier(context.Background(), "", "", false)
	if err != nil {
		t.Fatalf("barrier with the lock free: %v", err)
	}
	free()
}

// The settings-lock conversion and the other callers of acquireFileLock keep their
// contract: only the barrier names the policy lock.
func TestPolicyLockBusyErrorsOutsideTheBarrierKeepTheirNames(t *testing.T) {
	usePolicyRuntimeHome(t)
	hold := holdPolicyLock(t, globalPolicyLockPath(t))
	defer hold()
	if _, err := acquireFileLock(globalPolicyLockPath(t), 0); !errors.Is(err, errSyncAlreadyRunning) || errors.Is(err, localdaemon.ErrPolicyLockBusy) {
		t.Fatalf("acquireFileLock ended with %v, want errSyncAlreadyRunning", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testPolicyLockWait)
	defer cancel()
	var busy *policyLockBusyError
	if _, err := lockPolicyFile(ctx, globalPolicyLockPath(t)); !errors.As(err, &busy) || errors.Is(err, localdaemon.ErrPolicyLockBusy) {
		t.Fatalf("lockPolicyFile ended with %v, want *policyLockBusyError", err)
	}
}

// A startup that is asked to stop stops waiting for the lock, and says it was
// cancelled rather than that the lock was busy.
func TestPolicySyncStartupBarrierFollowsContext(t *testing.T) {
	usePolicyRuntimeHome(t)
	original := policyBarrierWait
	policyBarrierWait = time.Minute
	t.Cleanup(func() { policyBarrierWait = original })
	holdPolicyLock(t, globalPolicyLockPath(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := policySyncStartupBarrier(ctx, "", "", false)

	if !errors.Is(err, context.Canceled) || errors.Is(err, localdaemon.ErrPolicyLockBusy) {
		t.Fatalf("barrier ended with %v, want the cancellation", err)
	}
	if time.Since(start) >= 10*time.Second {
		t.Fatalf("a cancelled startup waited %v", time.Since(start))
	}
}

// interruptPolicyTransaction leaves a policy transaction half done, as a crash
// would: the file holds the new content and the journal can roll it back.
func interruptPolicyTransaction(t *testing.T) (file, journal string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(dir, "config")
	if err = os.WriteFile(file, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	journal, err = policyPath("", "server-policy-txn")
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected simulated interruption")
			}
		}()
		_ = configtxn.Apply(journal, []configtxn.Change{{Path: file, Before: []byte("old"), BeforeExists: true, After: []byte("new"), Mode: 0600}}, func() error { panic("interrupted") })
	}()
	return file, journal
}

// A recovery the startup needs is bounded like the lock: when the settings lock
// stays busy the journal stays as it is, the startup does not go on, and the
// error names the policy lock. A startup that cannot recover must not collect on
// settings that are half changed.
func TestPolicySyncStartupBarrierBoundsItsRecovery(t *testing.T) {
	usePolicyRuntimeHome(t)
	original, originalSettings := policyBarrierWait, policySettingsLockWait
	policyBarrierWait, policySettingsLockWait = 10*time.Second, testPolicyLockWait
	t.Cleanup(func() { policyBarrierWait, policySettingsLockWait = original, originalSettings })
	file, journal := interruptPolicyTransaction(t)
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	holdPolicyLock(t, claudeSettingsLockPath(t, p))

	start := time.Now()
	_, err = policySyncStartupBarrier(context.Background(), "", "", false)
	elapsed := time.Since(start)

	if !errors.Is(err, localdaemon.ErrPolicyLockBusy) {
		t.Fatalf("barrier ended with %v, want the policy lock wait", err)
	}
	if elapsed < testPolicyLockWait || elapsed >= 5*time.Second {
		t.Fatalf("waited %v, want about the settings lock's %v", elapsed, testPolicyLockWait)
	}
	if _, statErr := os.Stat(filepath.Join(journal, "journal.json")); statErr != nil {
		t.Fatalf("the journal is gone though the recovery did not run: %v", statErr)
	}
	if got, readErr := os.ReadFile(file); readErr != nil || string(got) != "new" {
		t.Fatalf("config %q, %v; want it untouched until the recovery runs", got, readErr)
	}
	wantFree(t, globalPolicyLockPath(t))
}

// The error reaches runSync's caller and nothing starts: no runtime is
// published, so no collection began on the half-changed settings.
func TestRunSyncStopsWhenTheStartupBarrierTimesOut(t *testing.T) {
	usePolicyRuntimeHome(t)
	resetSyncControlHooks(t)
	narrowBarrierWait(t)
	holdPolicyLock(t, globalPolicyLockPath(t))

	err := runSync(false, "", false, false, true, false, time.Second, "", "", "", false, "", false)

	if !errors.Is(err, localdaemon.ErrPolicyLockBusy) {
		t.Fatalf("runSync ended with %v, want the policy lock wait", err)
	}
	if _, ok, rtErr := readSyncRuntime(""); rtErr != nil || ok {
		t.Fatalf("a runtime was published (%v, %v) though the startup never passed the barrier", ok, rtErr)
	}
}

// A managed job waits for the barrier on the supervisor's behalf, so stopping or
// restarting it must end the wait; it used to sit out the whole barrier first.
// When the barrier does run out of time the supervisor gets the error it retries on.
func TestManagedProfilePrepareFollowsContextAndReportsPolicyLockWait(t *testing.T) {
	localTestProfile(t)
	original := policyBarrierWait
	policyBarrierWait = time.Minute
	t.Cleanup(func() { policyBarrierWait = original })
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load("default")
	if err != nil {
		t.Fatal(err)
	}
	holdPolicyLock(t, globalPolicyLockPath(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err = prepareManagedProfile(ctx, entry)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare ended with %v, want the cancellation", err)
	}
	if time.Since(start) >= 10*time.Second {
		t.Fatalf("a cancelled job waited %v for the barrier", time.Since(start))
	}
	policyBarrierWait = testPolicyLockWait
	if _, err = prepareManagedProfile(context.Background(), entry); !errors.Is(err, localdaemon.ErrPolicyLockBusy) {
		t.Fatalf("prepare ended with %v, want the policy lock wait the supervisor retries on", err)
	}
}

// A policy that commits while a managed job waits for the barrier has rewritten
// the profile the job's spec was built from. The job must not start on that
// spec: its client, endpoint and cursors are the old ones. It gives up with the
// error the supervisor retries on, and the retry builds the spec from the moved
// profile.
func TestManagedJobDoesNotStartOnASpecThePolicyOutdatedWhileItWaited(t *testing.T) {
	home := useTempSyncHome(t)
	p := profile.NewDefault()
	p.ClaudeConfigDir = filepath.Join(home, "source")
	p.Options.SyncEnabled = false
	p.User = profile.UserInfo{ID: "one", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
	if err := profile.SaveNamed(p, "one"); err != nil {
		t.Fatal(err)
	}
	registry := profile.Registry{Root: profile.DefaultDir()}
	entry, err := registry.Load("one")
	if err != nil {
		t.Fatal(err)
	}
	spec := profileJobSpec(entry, daemonOverrides{})
	release := holdPolicyLock(t, globalPolicyLockPath(t))
	acquired := make(chan error, 1)
	go func() {
		closeJob, err := spec.Acquire(context.Background(), localdaemon.Allocation{Allowed: spec.Sources})
		if err == nil {
			closeJob()
		}
		acquired <- err
	}()

	// The policy commits while the job waits: the profile moves, then the lock goes.
	moved, err := profile.LoadNamed("one")
	if err != nil {
		t.Fatal(err)
	}
	moved.Server.SyncEndpoint = "https://policy.example.invalid"
	if err := profile.SaveNamed(moved, "one"); err != nil {
		t.Fatal(err)
	}
	release()

	if err := <-acquired; !errors.Is(err, localdaemon.ErrPolicyLockBusy) {
		t.Fatalf("acquire ended with %v, want it to give up for the supervisor to retry", err)
	}
	if _, err := os.Stat(filepath.Join(entry.RuntimeDir, "sync-runtime.json")); !os.IsNotExist(err) {
		t.Fatalf("the job published a runtime from an outdated spec: %v", err)
	}
	reloaded, err := registry.Load("one")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Revision == entry.Revision {
		t.Fatal("the policy's rewrite did not change the profile's revision")
	}
	reloadedSpec := profileJobSpec(reloaded, daemonOverrides{})
	closeJob, err := reloadedSpec.Acquire(context.Background(), localdaemon.Allocation{Allowed: reloadedSpec.Sources})
	if err != nil {
		t.Fatalf("the retry on the moved profile: %v", err)
	}
	defer closeJob()
	if _, err := os.Stat(filepath.Join(reloaded.RuntimeDir, "sync-runtime.json")); err != nil {
		t.Fatalf("the retry did not start: %v", err)
	}
}

// The waits a recovery makes on its way through the barrier -- for the startup lock
// of a handoff, for the old watcher to exit, for the daemon to answer a command --
// are part of the barrier's wait, not extra to it. A startup that has 2 seconds
// left does not give the watcher the 35 it would have given a policy run.
func TestPolicySyncStartupBarrierGivesTheRecoveryOnlyTheTimeItHasLeft(t *testing.T) {
	t.Run("old watcher exit", func(t *testing.T) {
		usePolicyRuntimeHome(t)
		resetSyncControlHooks(t)
		original := policyBarrierWait
		policyBarrierWait = 2 * time.Second
		t.Cleanup(func() { policyBarrierWait = original })
		watcher := &syncRuntime{PID: os.Getpid(), InstanceID: "stand-alone-watcher", StartedAt: time.Now().UTC(), Interval: time.Second}
		if err := startWatchRuntime("", watcher); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { finishWatchRuntime("", watcher) })
		held, err := acquireSyncLock("", 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = held.Unlock() })
		if err := writePolicyHandoff("", &policyHandoff{Runtime: watcher}); err != nil {
			t.Fatal(err)
		}
		var waited atomic.Int64
		waitForWatcherExitFn = func(_, _ string, timeout time.Duration) (bool, error) {
			waited.Store(int64(timeout))
			return false, nil
		}

		_, err = policySyncStartupBarrier(context.Background(), "", "", false)

		if err == nil {
			t.Fatal("the barrier passed with the old watcher still running")
		}
		if got := time.Duration(waited.Load()); got <= 0 || got > policyBarrierWait {
			t.Fatalf("the recovery waited %v for the watcher to exit, want within the barrier's %v", got, policyBarrierWait)
		}
	})

	t.Run("handoff startup lock", func(t *testing.T) {
		usePolicyRuntimeHome(t)
		original := policyBarrierWait
		policyBarrierWait = testPolicyLockWait
		t.Cleanup(func() { policyBarrierWait = original })
		if err := writePolicyHandoff("", &policyHandoff{Runtime: &syncRuntime{Interval: time.Second}}); err != nil {
			t.Fatal(err)
		}
		startup, err := policyPath("", "server-policy-startup.lock")
		if err != nil {
			t.Fatal(err)
		}
		holdPolicyLock(t, startup)

		start := time.Now()
		_, err = policySyncStartupBarrier(context.Background(), "", "", false)

		if !errors.Is(err, localdaemon.ErrPolicyLockBusy) {
			t.Fatalf("barrier ended with %v, want the policy lock wait", err)
		}
		if time.Since(start) >= 10*time.Second {
			t.Fatalf("the barrier waited %v on the handoff's startup lock", time.Since(start))
		}
	})
}

// The error a startup ends in when the explicit Codex home's lock stays busy is the
// barrier's own, which names nothing: the managed status and the sync log carry it
// where the home's path must not go.
func TestPolicySyncStartupBarrierErrorNamesNoExplicitHome(t *testing.T) {
	usePolicyRuntimeHome(t)
	original, originalSettings := policyBarrierWait, policySettingsLockWait
	policyBarrierWait, policySettingsLockWait = 10*time.Second, testPolicyLockWait
	t.Cleanup(func() { policyBarrierWait, policySettingsLockWait = original, originalSettings })
	home, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	editSibling(t, "", func(p *profile.Profile) { p.CodexHome, p.Options.CodexSyncEnabled = home, true })
	interruptPolicyTransaction(t)
	lockPath, err := canonicalPolicyPath(filepath.Join(home, "config.toml.lock"))
	if err != nil {
		t.Fatal(err)
	}
	holdPolicyLock(t, lockPath)

	_, err = policySyncStartupBarrier(context.Background(), "", "", false)

	if !errors.Is(err, localdaemon.ErrPolicyLockBusy) {
		t.Fatalf("barrier ended with %v, want the policy lock wait", err)
	}
	if strings.Contains(err.Error(), home) {
		t.Fatalf("the error %q names the explicit home", err)
	}
}
