package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// useDaemonLockTestHome points cctraceDir() at a temp dir so a test never
// touches the real ~/.cctrace. t.Setenv rules out t.Parallel.
func useDaemonLockTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// The lock, not the pid file, is what makes a second daemon impossible.
func TestAcquireDaemonLockRefusesASecondDaemon(t *testing.T) {
	useDaemonLockTestHome(t)

	first, err := acquireDaemonLock()
	if err != nil {
		t.Fatalf("acquireDaemonLock: %v", err)
	}
	defer func() { _ = first.Unlock() }()

	if _, err := acquireDaemonLock(); !errors.Is(err, errDaemonAlreadyRunning) {
		t.Fatalf("second acquireDaemonLock = %v, want errDaemonAlreadyRunning", err)
	}
}

// The wait is a retry loop, not a sleep followed by a verdict: a holder that
// lets go inside the window has to be noticed.
func TestAcquireDaemonLockSucceedsOnceTheHolderReleases(t *testing.T) {
	useDaemonLockTestHome(t)

	first, err := acquireDaemonLock()
	if err != nil {
		t.Fatalf("acquireDaemonLock: %v", err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = first.Unlock()
	}()

	second, err := acquireDaemonLock()
	if err != nil {
		t.Fatalf("second acquireDaemonLock: %v", err)
	}
	if err := second.Unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

// --stop must not bring ~/.cctrace into being on a machine that has never run
// a daemon, which is why the probe drops O_CREATE.
func TestProbeDaemonLockCreatesNothingWhenTheLockFileIsMissing(t *testing.T) {
	home := useDaemonLockTestHome(t)

	fl, exists, err := probeDaemonLock()
	if err != nil || exists || fl != nil {
		t.Fatalf("probeDaemonLock = (%v, %v, %v), want (nil, false, nil)", fl, exists, err)
	}
	for _, path := range []string{filepath.Join(home, ".cctrace"), lockFilePath()} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %s = %v, want ErrNotExist", path, err)
		}
	}
}

// A free lock hands the caller the handle so it can clean up underneath it,
// and releasing that handle has to leave the lock takeable again.
func TestProbeDaemonLockTakesAFreeLockAndReleasesIt(t *testing.T) {
	useDaemonLockTestHome(t)
	if err := os.MkdirAll(cctraceDir(), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(lockFilePath(), nil, 0644); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	fl, exists, err := probeDaemonLock()
	if err != nil {
		t.Fatalf("probeDaemonLock: %v", err)
	}
	if !exists || fl == nil {
		t.Fatalf("probeDaemonLock = (%v, %v), want a handle on an existing file", fl, exists)
	}
	if err := fl.Unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	again, err := acquireDaemonLock()
	if err != nil {
		t.Fatalf("acquireDaemonLock after probe: %v", err)
	}
	_ = again.Unlock()
}

func TestProbeDaemonLockReportsAHeldLock(t *testing.T) {
	useDaemonLockTestHome(t)

	held, err := acquireDaemonLock()
	if err != nil {
		t.Fatalf("acquireDaemonLock: %v", err)
	}
	defer func() { _ = held.Unlock() }()

	fl, exists, err := probeDaemonLock()
	if err != nil || !exists || fl != nil {
		t.Fatalf("probeDaemonLock = (%v, %v, %v), want (nil, true, nil)", fl, exists, err)
	}
}

func TestWaitForDaemonLockFreeReturnsWhenTheHolderReleases(t *testing.T) {
	useDaemonLockTestHome(t)

	held, err := acquireDaemonLock()
	if err != nil {
		t.Fatalf("acquireDaemonLock: %v", err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = held.Unlock()
	}()

	fl, err := waitForDaemonLockFree(2 * time.Second)
	if err != nil {
		t.Fatalf("waitForDaemonLockFree: %v", err)
	}
	if fl == nil {
		t.Fatal("waitForDaemonLockFree returned no handle for a lock that came free")
	}
	_ = fl.Unlock()
}

func TestWaitForDaemonLockFreeTimesOutWhileHeld(t *testing.T) {
	useDaemonLockTestHome(t)

	held, err := acquireDaemonLock()
	if err != nil {
		t.Fatalf("acquireDaemonLock: %v", err)
	}
	defer func() { _ = held.Unlock() }()

	if _, err := waitForDaemonLockFree(200 * time.Millisecond); !errors.Is(err, errDaemonLockStillHeld) {
		t.Fatalf("waitForDaemonLockFree = %v, want errDaemonLockStillHeld", err)
	}
}

// The seams above prove the branches; this proves the mechanism. A real child
// process takes the lock, gets the real signal, and the parent watches the
// kernel hand the lock back. On Windows it is the one place the
// TerminateProcess -> handle close -> waiter chain is exercised, which is
// asynchronous there and is what the polling absorbs.
func TestSignalDaemonByPIDReleasesTheLockOfARealProcess(t *testing.T) {
	home := useDaemonLockTestHome(t)

	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcessHoldsTheDaemonLock")
	cmd.Env = append(os.Environ(),
		"CCTRACED_LOCK_HELPER=1",
		"HOME="+home,
		"USERPROFILE="+home,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() { _ = cmd.Wait() }()

	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatalf("helper never reported the lock: %v", err)
	}
	if fl, exists, err := probeDaemonLock(); err != nil || !exists || fl != nil {
		t.Fatalf("probeDaemonLock = (%v, %v, %v) while the helper holds it", fl, exists, err)
	}

	if err := signalDaemonByPID(cmd.Process.Pid); err != nil {
		t.Fatalf("signalDaemonByPID: %v", err)
	}

	fl, err := waitForDaemonLockFree(10 * time.Second)
	if err != nil {
		t.Fatalf("waitForDaemonLockFree after signalling the helper: %v", err)
	}
	if fl == nil {
		t.Fatal("the lock file disappeared instead of coming free")
	}
	_ = fl.Unlock()
}

// TestHelperProcessHoldsTheDaemonLock is not a test; it is the child body of
// TestSignalDaemonByPIDReleasesTheLockOfARealProcess.
func TestHelperProcessHoldsTheDaemonLock(t *testing.T) {
	if os.Getenv("CCTRACED_LOCK_HELPER") != "1" {
		t.Skip("helper process body")
	}
	if _, err := acquireDaemonLock(); err != nil {
		t.Fatalf("helper acquireDaemonLock: %v", err)
	}
	fmt.Println("locked")
	// Held until the signal lands. The parent bounds the wait.
	time.Sleep(60 * time.Second)
}

// Contention on the control lock is evidence that another cctraced exists:
// only a startup and a --stop take it, and both hold it for a moment. Startup
// refuses on this error rather than continuing without the daemon lock, which
// would put two daemons on one home.
func TestAcquireControlLockRefusesWhileAnotherHandleHoldsIt(t *testing.T) {
	useDaemonLockTestHome(t)

	held, err := acquireControlLock(daemonLockAcquireWait)
	if err != nil {
		t.Fatalf("acquireControlLock: %v", err)
	}
	defer func() { _ = held.Unlock() }()

	if _, err := acquireControlLock(200 * time.Millisecond); !errors.Is(err, errDaemonAlreadyRunning) {
		t.Fatalf("second acquireControlLock = %v, want errDaemonAlreadyRunning", err)
	}
}
