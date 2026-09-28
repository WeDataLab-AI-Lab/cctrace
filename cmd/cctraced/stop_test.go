package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// useStopTestHome points cctraceDir() at a temp dir and restores the seams
// runStop is driven through. t.Setenv rules out t.Parallel.
func useStopTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	prevProbe, prevWait := probeDaemonLockFn, waitLockFreeFn
	prevCtl, prevSignal, prevRemove := acquireControlLockFn, signalDaemonFn, removeFileFn
	t.Cleanup(func() {
		probeDaemonLockFn, waitLockFreeFn = prevProbe, prevWait
		acquireControlLockFn, signalDaemonFn, removeFileFn = prevCtl, prevSignal, prevRemove
	})
	return home
}

// countSignals swaps the signal seam for a counter and returns the pids it saw.
func countSignals(t *testing.T, err error) *[]int {
	t.Helper()
	sent := []int{}
	signalDaemonFn = func(pid int) error {
		sent = append(sent, pid)
		return err
	}
	return &sent
}

func writeLockFile(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(cctraceDir(), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(lockFilePath(), nil, 0644); err != nil {
		t.Fatalf("write lock file: %v", err)
	}
}

func writePIDFileWith(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(cctraceDir(), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(pidFilePath(), []byte(content), 0644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
}

// captureStderr redirects os.Stderr for the length of a test and returns the
// text runStop wrote there.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = prev })
	return func() string {
		os.Stderr = prev
		_ = w.Close()
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("read stderr: %v", err)
		}
		return string(data)
	}
}

func heldProbe() (*flock.Flock, bool, error) { return nil, true, nil }

// "Stop what is running" is satisfied by nothing running, and asking that of a
// machine that never ran a daemon must not leave ~/.cctrace behind.
func TestRunStopWithoutALockFileOrPIDFileTouchesNothing(t *testing.T) {
	home := useStopTestHome(t)
	sent := countSignals(t, nil)

	if err := runStop(time.Second); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if len(*sent) != 0 {
		t.Fatalf("signalled %v with nothing running, want none", *sent)
	}
	if _, err := os.Stat(filepath.Join(home, ".cctrace")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat ~/.cctrace = %v, want ErrNotExist", err)
	}
}

// A pid file with no lock file beside it is a state only a human deleting the
// lock produces. Guessing from it is how an unrelated process gets signalled.
func TestRunStopRefusesWhenOnlyThePIDFileIsPresent(t *testing.T) {
	useStopTestHome(t)
	writePIDFileWith(t, "4242")
	sent := countSignals(t, nil)

	if err := runStop(time.Second); err == nil {
		t.Fatal("runStop succeeded with a pid file and no lock file, want an error")
	}
	if len(*sent) != 0 {
		t.Fatalf("signalled %v, want none", *sent)
	}
	if _, err := os.Stat(pidFilePath()); err != nil {
		t.Fatalf("pid file was removed on an unknown state: %v", err)
	}
}

// The three things issue #525 asks for, together: a free lock means the daemon
// is gone, so nothing is signalled, the stale pid file is cleared, and the
// command succeeds.
func TestRunStopClearsAStalePIDFileWithoutSignallingAnything(t *testing.T) {
	useStopTestHome(t)
	writeLockFile(t)
	writePIDFileWith(t, "4242")
	sent := countSignals(t, nil)

	if err := runStop(time.Second); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if len(*sent) != 0 {
		t.Fatalf("signalled %v with a free lock, want none", *sent)
	}
	if _, err := os.Stat(pidFilePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat pid file = %v, want ErrNotExist", err)
	}
}

// #449: a removal that fails does not change the outcome, but the cause has to
// be left where the next failure can be read from it.
func TestRunStopSucceedsWhenTheStalePIDFileCannotBeRemoved(t *testing.T) {
	useStopTestHome(t)
	writeLockFile(t)
	writePIDFileWith(t, "4242")
	sent := countSignals(t, nil)
	removeFileFn = func(string) error { return errors.New("remove refused") }
	stderr := captureStderr(t)

	if err := runStop(time.Second); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if len(*sent) != 0 {
		t.Fatalf("signalled %v, want none", *sent)
	}
	if reported := stderr(); !strings.Contains(reported, "remove refused") {
		t.Fatalf("stderr said %q, want the removal failure reported", reported)
	}
}

// A lock file that vanishes while --stop waits leaves nothing to hold, and
// without the lock there is no way to know the pid file still belongs to the
// daemon that was signalled rather than to one starting right now.
func TestRunStopKeepsThePIDFileWhenTheLockFileDisappears(t *testing.T) {
	useStopTestHome(t)
	writeLockFile(t)
	writePIDFileWith(t, "4242")
	sent := countSignals(t, nil)
	probeDaemonLockFn = heldProbe
	waitLockFreeFn = func(time.Duration) (*flock.Flock, error) { return nil, nil }
	removed := 0
	removeFileFn = func(string) error { removed++; return nil }
	stderr := captureStderr(t)

	if err := runStop(time.Second); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("signalled %v, want one signal", *sent)
	}
	if removed != 0 {
		t.Fatalf("removed the pid file %d times without holding the lock, want 0", removed)
	}
	if _, err := os.Stat(pidFilePath()); err != nil {
		t.Fatalf("pid file removed without holding the lock: %v", err)
	}
	if reported := stderr(); !strings.Contains(reported, "disappeared") {
		t.Fatalf("stderr said %q, want the missing lock file reported", reported)
	}
}

func TestRunStopSignalsTheLockHolderAndConfirmsItLetGo(t *testing.T) {
	useStopTestHome(t)
	writeLockFile(t)
	writePIDFileWith(t, "4242")
	sent := countSignals(t, nil)
	probeDaemonLockFn = heldProbe
	waited := 0
	// The real handle, so the "clean up while holding the lock" branch runs.
	waitLockFreeFn = func(time.Duration) (*flock.Flock, error) {
		waited++
		fl, _, err := probeDaemonLock()
		return fl, err
	}

	if err := runStop(time.Second); err != nil {
		t.Fatalf("runStop: %v", err)
	}
	if len(*sent) != 1 || (*sent)[0] != 4242 {
		t.Fatalf("signalled %v, want [4242]", *sent)
	}
	if waited != 1 {
		t.Fatalf("waited for the lock %d times, want 1", waited)
	}
	if _, err := os.Stat(pidFilePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat pid file = %v, want ErrNotExist", err)
	}
}

// The lock says something is running, but the pid it should be reachable at is
// not readable. Signalling a guess is worse than saying so.
func TestRunStopRefusesWhenTheLockHolderHasNoUsablePID(t *testing.T) {
	for _, tc := range []struct{ name, pid string }{
		{"missing", ""},
		{"empty", " \n"},
		{"garbage", "abc"},
		{"trailing junk", "123junk"},
		{"zero", "0"},
		{"negative", "-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useStopTestHome(t)
			writeLockFile(t)
			if tc.name != "missing" {
				writePIDFileWith(t, tc.pid)
			}
			sent := countSignals(t, nil)
			probeDaemonLockFn = heldProbe

			if err := runStop(time.Second); err == nil {
				t.Fatal("runStop succeeded with an unusable pid, want an error")
			}
			if len(*sent) != 0 {
				t.Fatalf("signalled %v, want none", *sent)
			}
		})
	}
}

func TestRunStopKeepsThePIDFileWhenSignallingFails(t *testing.T) {
	useStopTestHome(t)
	writeLockFile(t)
	writePIDFileWith(t, "4242")
	countSignals(t, errors.New("no permission"))
	probeDaemonLockFn = heldProbe

	if err := runStop(time.Second); err == nil {
		t.Fatal("runStop succeeded after a failed signal, want an error")
	}
	if _, err := os.Stat(pidFilePath()); err != nil {
		t.Fatalf("pid file removed after a failed signal: %v", err)
	}
}

// Reported rather than assumed: the pid file describes a process that is still
// there, so removing it would erase the only record of what to look at.
func TestRunStopKeepsThePIDFileWhenTheLockStaysHeld(t *testing.T) {
	useStopTestHome(t)
	writeLockFile(t)
	writePIDFileWith(t, "4242")
	countSignals(t, nil)
	probeDaemonLockFn = heldProbe
	waitLockFreeFn = func(time.Duration) (*flock.Flock, error) { return nil, errDaemonLockStillHeld }

	if err := runStop(time.Second); err == nil {
		t.Fatal("runStop succeeded while the lock was still held, want an error")
	}
	if _, err := os.Stat(pidFilePath()); err != nil {
		t.Fatalf("pid file removed while the lock was still held: %v", err)
	}
}

func TestRunStopFailsWhenTheLockCannotBeProbed(t *testing.T) {
	useStopTestHome(t)
	writeLockFile(t)
	writePIDFileWith(t, "4242")
	sent := countSignals(t, nil)
	probeDaemonLockFn = func() (*flock.Flock, bool, error) {
		return nil, false, errors.New("permission denied")
	}

	if err := runStop(time.Second); err == nil {
		t.Fatal("runStop succeeded on a probe error, want an error")
	}
	if len(*sent) != 0 {
		t.Fatalf("signalled %v on a probe error, want none", *sent)
	}
}

// Without the control lock the decision is not serialized against a daemon that
// is starting, so there is no basis to act at all.
func TestRunStopFailsWhenTheControlLockCannotBeTaken(t *testing.T) {
	useStopTestHome(t)
	writeLockFile(t)
	writePIDFileWith(t, "4242")
	sent := countSignals(t, nil)
	acquireControlLockFn = func(time.Duration) (*flock.Flock, error) {
		return nil, errDaemonAlreadyRunning
	}
	probed := 0
	probeDaemonLockFn = func() (*flock.Flock, bool, error) { probed++; return heldProbe() }

	if err := runStop(time.Second); err == nil {
		t.Fatal("runStop succeeded without the control lock, want an error")
	}
	if probed != 0 || len(*sent) != 0 {
		t.Fatalf("probed %d times and signalled %v without the control lock, want 0 and none", probed, *sent)
	}
}
