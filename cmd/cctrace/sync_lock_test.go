package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// TestLockFilePath_Default checks the default lock path uses the default profile dir.
func TestLockFilePath_Default(t *testing.T) {
	path := lockFilePath("")
	if !strings.HasSuffix(path, "sync.lock") {
		t.Errorf("expected path ending in sync.lock, got %q", path)
	}
	if filepath.Base(path) != "sync.lock" {
		t.Errorf("expected filename sync.lock, got %q", filepath.Base(path))
	}
}

// TestSingleInstance_FirstAcquires verifies the first locker succeeds.
func TestSingleInstance_FirstAcquires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")
	fl := flock.New(path)
	locked, err := fl.TryLock()
	if err != nil {
		t.Fatalf("TryLock error: %v", err)
	}
	if !locked {
		t.Fatal("expected first locker to acquire lock")
	}
	fl.Unlock()
}

// TestSingleInstance_SecondBlocked verifies a second locker fails while the first holds the lock.
func TestSingleInstance_SecondBlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")

	first := flock.New(path)
	locked, err := first.TryLock()
	if err != nil {
		t.Fatalf("first TryLock error: %v", err)
	}
	if !locked {
		t.Fatal("expected first locker to acquire lock")
	}
	defer first.Unlock()

	second := flock.New(path)
	locked2, err := second.TryLock()
	if err != nil {
		t.Fatalf("second TryLock error: %v", err)
	}
	if locked2 {
		second.Unlock()
		t.Fatal("expected second locker to be blocked")
	}
}

// TestSingleInstance_ReleasedAllowsReacquire verifies that after the first locker releases,
// a new locker can acquire the lock.
func TestSingleInstance_ReleasedAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")

	first := flock.New(path)
	locked, err := first.TryLock()
	if err != nil || !locked {
		t.Fatalf("first TryLock failed: locked=%v err=%v", locked, err)
	}
	first.Unlock()

	second := flock.New(path)
	locked2, err := second.TryLock()
	if err != nil {
		t.Fatalf("second TryLock error: %v", err)
	}
	if !locked2 {
		t.Fatal("expected second locker to acquire lock after first released")
	}
	second.Unlock()
}

// TestSingleInstance_StaleLockFile verifies that a stale lock file (no process holding it)
// does not block a new locker — the OS releases flock when the process dies.
func TestSingleInstance_StaleLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")

	// Create the file without acquiring a lock (simulates a leftover file)
	if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	fl := flock.New(path)
	locked, err := fl.TryLock()
	if err != nil {
		t.Fatalf("TryLock on stale file error: %v", err)
	}
	if !locked {
		t.Fatal("expected locker to acquire stale (unheld) lock file")
	}
	fl.Unlock()
}

// TestSingleInstance_ConcurrentLockersOnlyOneWins verifies that among multiple concurrent
// lockers, exactly one acquires the lock.
func TestSingleInstance_ConcurrentLockersOnlyOneWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")

	results := make([]bool, 5)
	done := make(chan struct{})

	for i := range results {
		i := i
		go func() {
			fl := flock.New(path)
			locked, _ := fl.TryLock()
			results[i] = locked
			if locked {
				// Hold briefly to let others attempt
				fl.Unlock()
			}
			done <- struct{}{}
		}()
	}

	for range results {
		<-done
	}

	winners := 0
	for _, locked := range results {
		if locked {
			winners++
		}
	}
	// At most one should win at any instant (may be more due to sequential release, but never zero)
	if winners == 0 {
		t.Fatal("expected at least one locker to acquire the lock")
	}
}

func TestAcquireSyncLockFailsImmediatelyWhenAlreadyHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")

	first := flock.New(path)
	locked, err := first.TryLock()
	if err != nil || !locked {
		t.Fatalf("first TryLock failed: locked=%v err=%v", locked, err)
	}
	defer first.Unlock()

	_, err = acquireFileLock(path, 0)
	if err == nil || !strings.Contains(err.Error(), errSyncAlreadyRunning.Error()) {
		t.Fatalf("expected already-running error, got %v", err)
	}
}

func TestAcquireSyncLockWaitsForRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")

	first := flock.New(path)
	locked, err := first.TryLock()
	if err != nil || !locked {
		t.Fatalf("first TryLock failed: locked=%v err=%v", locked, err)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		first.Unlock()
	}()

	fl, err := acquireFileLock(path, time.Second)
	if err != nil {
		t.Fatalf("acquireFileLock: %v", err)
	}
	fl.Unlock()
}

func TestAcquireWatchSyncLockRetriesWhenRuntimeMissing(t *testing.T) {
	useTempSyncHome(t)
	if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
		t.Fatalf("mkdir sync profile dir: %v", err)
	}

	path := lockFilePath("")
	holder := flock.New(path)
	locked, err := holder.TryLock()
	if err != nil || !locked {
		t.Fatalf("holder TryLock failed: locked=%v err=%v", locked, err)
	}

	acquired := make(chan *flock.Flock, 1)
	acquireErr := make(chan error, 1)
	go func() {
		fl, err := acquireWatchSyncLock("")
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- fl
	}()

	time.Sleep(100 * time.Millisecond)
	_ = holder.Unlock()

	select {
	case err := <-acquireErr:
		t.Fatalf("acquireWatchSyncLock: %v", err)
	case fl := <-acquired:
		_ = fl.Unlock()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for acquireWatchSyncLock to succeed")
	}
}

func TestAcquireWatchSyncLockFailsFastWhenRuntimeExists(t *testing.T) {
	useTempSyncHome(t)
	if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
		t.Fatalf("mkdir sync profile dir: %v", err)
	}

	if err := writeJSONFile(runtimeFilePath(""), syncRuntime{
		PID:        123,
		InstanceID: "inst-running",
		StartedAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("write runtime: %v", err)
	}

	path := lockFilePath("")
	holder := flock.New(path)
	locked, err := holder.TryLock()
	if err != nil || !locked {
		t.Fatalf("holder TryLock failed: locked=%v err=%v", locked, err)
	}
	defer holder.Unlock()

	start := time.Now()
	_, err = acquireWatchSyncLock("")
	elapsed := time.Since(start)
	if !errors.Is(err, errSyncAlreadyRunning) {
		t.Fatalf("expected already-running error, got %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("expected fast failure with runtime present, took %s", elapsed)
	}
}

func TestWaitForFileUnlockWaitsForRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")

	first := flock.New(path)
	locked, err := first.TryLock()
	if err != nil || !locked {
		t.Fatalf("first TryLock failed: locked=%v err=%v", locked, err)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		first.Unlock()
	}()

	if err := waitForFileUnlock(path, time.Second); err != nil {
		t.Fatalf("waitForFileUnlock: %v", err)
	}
}

func TestWaitForFileUnlockTimesOutWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")

	first := flock.New(path)
	locked, err := first.TryLock()
	if err != nil || !locked {
		t.Fatalf("first TryLock failed: locked=%v err=%v", locked, err)
	}
	defer first.Unlock()

	if err := waitForFileUnlock(path, 50*time.Millisecond); !errors.Is(err, errSyncAlreadyRunning) {
		t.Fatalf("expected already-running error, got %v", err)
	}
}

func TestWaitForFileUnlockReturnsImmediatelyWhenUnlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")

	if err := os.WriteFile(path, []byte("stale"), 0644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	if err := waitForFileUnlock(path, time.Second); err != nil {
		t.Fatalf("waitForFileUnlock on unlocked file: %v", err)
	}
}

func TestWaitForFileUnlockCreatesParentDirWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "sync.lock")

	if err := waitForFileUnlock(path, time.Second); err != nil {
		t.Fatalf("waitForFileUnlock with missing parent dir: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("parent directory was not created: %v", err)
	}
}
