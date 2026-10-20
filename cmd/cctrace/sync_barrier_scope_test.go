package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/profile"
)

// HOME, the update seams and the version are process-wide, so none of these
// tests run in parallel.

// policyLockHeldNow reports whether something holds the lock at path right now.
func policyLockHeldNow(path string) bool {
	lock := flock.New(path)
	ok, err := lock.TryLock()
	if err == nil && ok {
		_ = lock.Unlock()
		return false
	}
	return true
}

// A startup holds the global policy lock only for what must not interleave with a
// policy application: recovery, reading the profile and repairing the settings from
// it. The self-update check and the download that may follow can take as long as
// the server and the network like, and every other profile's startup and every
// policy run waits on the lock meanwhile.
func TestSyncStartupChecksForUpdatesOutsideThePolicyBarrier(t *testing.T) {
	oldVersion := version
	version = "v0.7.18"
	t.Cleanup(func() { version = oldVersion })

	t.Run("daemon parent", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		claudeDir := observationsProfile(t, "http://localhost:4317")
		lockPath := globalPolicyLockPath(t)
		var held atomic.Bool
		applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
			held.Store(policyLockHeldNow(lockPath))
			return daemonParentUpdateResult{}
		}
		spawnSyncProcessFn = func([]string, string) (int, error) { return 123, nil }

		captureOutput(t, func() {
			if err := runSync(false, claudeDir, false, true, true, false, time.Second, "", "", "", false, "", false); err != nil {
				t.Errorf("daemon parent: %v", err)
			}
		})

		if held.Load() {
			t.Fatal("the daemon parent checked for an update while holding the policy lock")
		}
	})

	t.Run("foreground watch", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		claudeDir := observationsProfile(t, "http://localhost:4317")
		lockPath := globalPolicyLockPath(t)
		var held atomic.Bool
		applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
			held.Store(policyLockHeldNow(lockPath))
			return daemonParentUpdateResult{Applied: true}
		}
		syncExecutablePathFn = func() (string, error) { return "/cctrace/test-binary", nil }
		reexecDaemonParentFn = func(string) error { return nil }

		if err := runSync(false, claudeDir, true, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
			t.Fatalf("foreground watch: %v", err)
		}

		if held.Load() {
			t.Fatal("the foreground watch checked for an update while holding the policy lock")
		}
	})

	t.Run("one-shot", func(t *testing.T) {
		useTempSyncHome(t)
		resetSyncControlHooks(t)
		lockPath := globalPolicyLockPath(t)
		var held, asked atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/version" {
				asked.Store(true)
				held.Store(policyLockHeldNow(lockPath))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"version":"v0.7.18"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"inserted":0}`))
		}))
		defer srv.Close()
		claudeDir := observationsProfile(t, srv.URL)

		captureOutput(t, func() {
			if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
				t.Errorf("one-shot: %v", err)
			}
		})

		if !asked.Load() {
			t.Fatal("the one-shot never asked the server for its version")
		}
		if held.Load() {
			t.Fatal("the one-shot checked for an update while holding the policy lock")
		}
	})
}

// What the barrier protects stays under it: a startup reads its profile only after
// it has the barrier, so a policy that moved the profile while it waited is the
// one it starts on.
func TestSyncStartupReadsItsProfileUnderThePolicyBarrier(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	var oldHits, newHits atomic.Int32
	count := func(hits *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"inserted":0}`))
		}
	}
	oldServer := httptest.NewServer(count(&oldHits))
	defer oldServer.Close()
	newServer := httptest.NewServer(count(&newHits))
	defer newServer.Close()
	claudeDir := observationsProfile(t, oldServer.URL)
	release := holdPolicyLock(t, globalPolicyLockPath(t))
	var runErr error
	// The output is captured around the whole exchange, on the test's goroutine:
	// captureOutput swaps the process's stdout and restores it when it returns.
	captureOutput(t, func() {
		done := make(chan error, 1)
		go func() {
			done <- runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false)
		}()

		// The policy commits while the startup waits: the profile moves, then the lock goes.
		p, err := profile.Load()
		if err != nil {
			t.Error(err)
		} else {
			p.Server.SyncEndpoint = newServer.URL
			if err := profile.Save(p); err != nil {
				t.Error(err)
			}
		}
		release()
		runErr = <-done
	})

	if runErr != nil {
		t.Fatalf("startup: %v", runErr)
	}
	if oldHits.Load() != 0 || newHits.Load() == 0 {
		t.Fatalf("old server hit %d times, new %d: the startup used a profile read before the barrier", oldHits.Load(), newHits.Load())
	}
}
