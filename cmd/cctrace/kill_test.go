package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// useKillTestHome points the profile directory at a temp dir so a test never
// touches a real profile, and restores the seams it swaps.
func useKillTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	prevLockFree, prevKill := syncLockFreeFn, killProcessFn
	t.Cleanup(func() { syncLockFreeFn, killProcessFn = prevLockFree, prevKill })
}

// A free sync lock means the process that held it is already gone, so there is
// nothing to kill and no pid worth trusting.
//
// This is the whole safety argument for the command. The pid comes from a file
// the daemon wrote, and a file outlives the process it describes -- after a
// crash the recorded pid can belong to something else entirely by the time
// anyone runs this. Killing on a stale pid would terminate an unrelated
// process, so the lock, not the file, decides whether a kill happens at all.
func TestRunKillRefusesWhenTheSyncLockIsFree(t *testing.T) {
	useKillTestHome(t)
	syncLockFreeFn = func(string) (bool, error) { return true, nil }
	killed := 0
	killProcessFn = func(int) error { killed++; return nil }

	if err := runKill(""); err != nil {
		t.Fatalf("runKill: %v", err)
	}
	if killed != 0 {
		t.Fatalf("killed %d processes with a free lock, want 0", killed)
	}
}

// A held lock with a recorded pid is the case the command exists for.
func TestRunKillTerminatesTheRecordedPIDWhenTheLockIsHeld(t *testing.T) {
	useKillTestHome(t)
	writeKillTestRuntime(t, "", 4242)

	freeCalls := 0
	syncLockFreeFn = func(string) (bool, error) {
		freeCalls++
		// Held on the first look, free once the kill has landed.
		return freeCalls > 1, nil
	}
	var killedPIDs []int
	killProcessFn = func(pid int) error { killedPIDs = append(killedPIDs, pid); return nil }

	if err := runKill(""); err != nil {
		t.Fatalf("runKill: %v", err)
	}
	if len(killedPIDs) != 1 || killedPIDs[0] != 4242 {
		t.Fatalf("killed %v, want [4242]", killedPIDs)
	}
}

// Bookkeeping files describe a process that no longer exists, so leaving them
// behind would report a running daemon to the next command that looks.
func TestRunKillClearsBookkeepingAfterTerminating(t *testing.T) {
	useKillTestHome(t)
	writeKillTestRuntime(t, "", 4242)
	if err := writeSyncStopRequest("", syncStopRequest{InstanceID: "i-1"}); err != nil {
		t.Fatalf("writeSyncStopRequest: %v", err)
	}

	freeCalls := 0
	syncLockFreeFn = func(string) (bool, error) { freeCalls++; return freeCalls > 1, nil }
	killProcessFn = func(int) error { return nil }

	if err := runKill(""); err != nil {
		t.Fatalf("runKill: %v", err)
	}
	for _, path := range []string{runtimeFilePath(""), pidFilePath(""), stopRequestFilePath("")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still present after kill (err=%v)%s", filepath.Base(path), err, diagnoseSurvivingFile(path))
		}
	}
}

// diagnoseSurvivingFile runs only when the assertion above has already failed,
// and answers the question #449 has been stuck on: is the leftover transient?
//
// #467 made clearSyncBookkeeping report a non-ErrNotExist os.Remove error, which
// covers a file that is locked. It does not cover the likelier Windows shape:
// DeleteFile on a file that still has an open handle marks it delete-pending and
// returns success, so the name survives with no error to report. The one observed
// failure recorded exactly that -- `err=<nil>` from os.Stat, and nothing logged.
//
// So ask the file directly. A retry that succeeds means the leftover was
// transient and a retry inside clearSyncBookkeeping would have cleared it; a
// retry that keeps failing means it is held, and the error names the holder.
// Either answer closes the open completion condition; neither can be read from
// "the file is still there".
func diagnoseSurvivingFile(path string) string {
	var b strings.Builder
	// Open first. A delete-pending file on Windows is still listed but refuses to
	// open, and that only holds while the name is there -- asking after a
	// successful retry reports "no such file" and tells us nothing.
	if f, openErr := os.Open(path); openErr == nil {
		_ = f.Close()
		b.WriteString("\n  diagnosis: opens fine")
	} else {
		fmt.Fprintf(&b, "\n  diagnosis: open -> err=%v", openErr)
	}
	removeErr := os.Remove(path)
	fmt.Fprintf(&b, "; retry remove -> err=%v", removeErr)
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		b.WriteString("; gone after retry (transient)")
	} else {
		fmt.Fprintf(&b, "; still present after retry (stat err=%v)", statErr)
	}
	return b.String()
}

// A process that outlives the kill is reported, not silently accepted: the
// caller's next step (replacing the binary) fails confusingly if it believes a
// stop happened that did not.
func TestRunKillReportsAProcessThatSurvives(t *testing.T) {
	useKillTestHome(t)
	writeKillTestRuntime(t, "", 4242)
	syncLockFreeFn = func(string) (bool, error) { return false, nil }
	killProcessFn = func(int) error { return nil }

	err := runKill("")
	if err == nil {
		t.Fatal("runKill succeeded while the lock stayed held, want an error")
	}
}

func writeKillTestRuntime(t *testing.T, profileName string, pid int) {
	t.Helper()
	if err := startWatchRuntime(profileName, &syncRuntime{PID: pid, InstanceID: "i-1"}); err != nil {
		t.Fatalf("startWatchRuntime: %v", err)
	}
}

// A removal that fails for a reason other than "already gone" is reported.
//
// Issue #449: the Windows runner saw sync-stop.json survive a kill, and the CI
// log carried nothing about why -- the three removals discarded their errors on
// the premise that each one could only ever be os.ErrNotExist. When that
// premise breaks the operator is left with "the file is there" and no cause.
func TestClearSyncBookkeepingReportsARemovalThatFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not gate deletion on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this test relies on")
	}
	useKillTestHome(t)
	prevInstalled := daemonLogInstalled
	daemonLogInstalled = false
	t.Cleanup(func() { daemonLogInstalled = prevInstalled })

	writeKillTestRuntime(t, "", 4242)
	if err := writeSyncStopRequest("", syncStopRequest{InstanceID: "i-1"}); err != nil {
		t.Fatalf("writeSyncStopRequest: %v", err)
	}

	dir := syncProfileDir("")
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	_, stderr := captureOutput(t, func() { clearSyncBookkeeping("") })

	if !strings.Contains(stderr, "sync-stop.json") {
		t.Fatalf("stderr %q does not name the file that could not be removed", stderr)
	}
	if !strings.Contains(stderr, "permission denied") {
		t.Fatalf("stderr %q does not carry the removal error", stderr)
	}
}

// "Already gone" stays silent: that is the case the command is normally in,
// and reporting it would bury the removals that actually failed.
func TestClearSyncBookkeepingStaysSilentWhenFilesAreAlreadyGone(t *testing.T) {
	useKillTestHome(t)
	prevInstalled := daemonLogInstalled
	daemonLogInstalled = false
	t.Cleanup(func() { daemonLogInstalled = prevInstalled })

	_, stderr := captureOutput(t, func() { clearSyncBookkeeping("") })

	if stderr != "" {
		t.Fatalf("stderr %q, want nothing reported for files that were never there", stderr)
	}
}
