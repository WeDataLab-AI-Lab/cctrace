package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexlog"
)

func useTempSyncHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// Redirecting HOME is not enough: codexlog.DefaultCodexDir prefers
	// CODEX_CONFIG_DIR and ResolveScanDirs additionally scans CODEX_HOME, so a
	// developer with either set has their real Codex tree read by the sync tests
	// — and autoMigrateCodex writes an [otel] block, complete with auth token,
	// into their real config.toml. Point both inside the temp home.
	t.Setenv("CODEX_CONFIG_DIR", filepath.Join(home, ".codex"))
	t.Setenv("CODEX_HOME", "")
	return home
}

// Regression lock: every Codex path this package can reach must resolve inside
// the temp home. A test that escapes it writes into the developer's real
// ~/.codex — an earlier version of this suite created an [otel] block with a
// bearer token in the developer's config.toml.
func TestUseTempSyncHomeContainsCodexPaths(t *testing.T) {
	home := useTempSyncHome(t)

	configDir := codexlog.DefaultCodexDir()
	if !strings.HasPrefix(configDir, home) {
		t.Errorf("DefaultCodexDir() = %q, want a path under %q", configDir, home)
	}
	for _, dir := range codexlog.ResolveScanDirs(nil) {
		if !strings.HasPrefix(dir, home) {
			t.Errorf("ResolveScanDirs returned %q, outside temp home %q", dir, home)
		}
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func resetSyncControlHooks(t *testing.T) {
	t.Helper()
	oldSpawn := spawnSyncProcessFn
	oldWaitAck := waitForSyncStartAckFn
	oldWaitExit := waitForWatcherExitFn
	oldRunSync := runSyncFn
	oldRunStart := runSyncStartFn
	oldRunFinalize := runSyncFinalizeFn
	oldRunFinalizeWorker := runSyncFinalizeWorkerFn
	oldRunReenrich := runSyncReenrichFn
	oldRunAll := runSyncAllFn
	oldSyncLockWait := syncLockWaitDuration
	oldExecutablePath := syncExecutablePathFn
	oldStartProcess := startProcessFn
	oldReleaseProcess := releaseProcessFn
	oldApplyDaemonParentUpdate := applyDaemonParentUpdateFn
	oldReexecDaemonParent := reexecDaemonParentFn
	t.Cleanup(func() {
		spawnSyncProcessFn = oldSpawn
		waitForSyncStartAckFn = oldWaitAck
		waitForWatcherExitFn = oldWaitExit
		runSyncFn = oldRunSync
		runSyncStartFn = oldRunStart
		runSyncFinalizeFn = oldRunFinalize
		runSyncFinalizeWorkerFn = oldRunFinalizeWorker
		runSyncReenrichFn = oldRunReenrich
		runSyncAllFn = oldRunAll
		syncLockWaitDuration = oldSyncLockWait
		syncExecutablePathFn = oldExecutablePath
		startProcessFn = oldStartProcess
		releaseProcessFn = oldReleaseProcess
		applyDaemonParentUpdateFn = oldApplyDaemonParentUpdate
		reexecDaemonParentFn = oldReexecDaemonParent
	})
}

func assertArgsEqual(t *testing.T, got []string, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestBuildFinalizeWorkerArgs_LocalFlag(t *testing.T) {
	if !hasArg(buildFinalizeWorkerArgs("", "", "", "", true), "--local") {
		t.Fatal("local=true must include --local in finalize worker args")
	}
	if hasArg(buildFinalizeWorkerArgs("", "", "", "", false), "--local") {
		t.Fatal("local=false must not include --local")
	}
}

// syncAckTestWait bounds the tests that wait for a background goroutine to
// write a control file. These are correctness ceilings, not timing assertions:
// each wait returns the moment the file appears, so a generous value keeps the
// happy path fast while surviving a busy CI runner.
const syncAckTestWait = 5 * time.Second

func TestWaitForSyncStartAckReadsWrittenAck(t *testing.T) {
	useTempSyncHome(t)

	ackPath, err := createSyncTempPath("", "sync-start-ack-*.json")
	if err != nil {
		t.Fatalf("createSyncTempPath: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = writeSyncStartAck(ackPath, syncStartAck{
			Status:     syncStartAckStarted,
			InstanceID: "inst-1",
		})
	}()

	// The writer sleeps 100ms, so a 1s limit left only 900ms of slack and this
	// test flaked on loaded CI runners while the other sync-daemon tests were
	// competing for CPU and disk. The wait returns as soon as the ack lands, so
	// a longer ceiling costs nothing on the happy path.
	ack, err := waitForSyncStartAck(ackPath, syncAckTestWait)
	if err != nil {
		t.Fatalf("waitForSyncStartAck: %v", err)
	}
	if ack.Status != syncStartAckStarted {
		t.Fatalf("ack.Status = %q, want %q", ack.Status, syncStartAckStarted)
	}
	if ack.InstanceID != "inst-1" {
		t.Fatalf("ack.InstanceID = %q, want %q", ack.InstanceID, "inst-1")
	}
}

func TestWatchRuntimeLifecycleWritesExitAck(t *testing.T) {
	useTempSyncHome(t)
	if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
		t.Fatalf("mkdir sync profile dir: %v", err)
	}

	rt := &syncRuntime{
		PID:        12345,
		InstanceID: "inst-2",
		StartedAt:  time.Now().UTC(),
	}
	if err := startWatchRuntime("", rt); err != nil {
		t.Fatalf("startWatchRuntime: %v", err)
	}

	if _, err := os.Stat(pidFilePath("")); err != nil {
		t.Fatalf("pid file missing: %v", err)
	}
	if _, ok, err := readSyncRuntime(""); err != nil || !ok {
		t.Fatalf("runtime file missing: ok=%v err=%v", ok, err)
	}

	finishWatchRuntime("", rt)

	if _, err := os.Stat(pidFilePath("")); !os.IsNotExist(err) {
		t.Fatalf("pid file should be removed, got err=%v", err)
	}
	if _, err := os.Stat(runtimeFilePath("")); !os.IsNotExist(err) {
		t.Fatalf("runtime file should be removed, got err=%v", err)
	}

	exit, ok, err := readSyncExit("")
	if err != nil {
		t.Fatalf("readSyncExit: %v", err)
	}
	if !ok {
		t.Fatal("sync exit ack missing")
	}
	if exit.InstanceID != rt.InstanceID {
		t.Fatalf("exit.InstanceID = %q, want %q", exit.InstanceID, rt.InstanceID)
	}
}

func TestWaitForWatcherExitTreatsStaleRuntimeAsStoppedWhenLockFree(t *testing.T) {
	useTempSyncHome(t)
	if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
		t.Fatalf("mkdir sync profile dir: %v", err)
	}

	rt := &syncRuntime{
		PID:        23456,
		InstanceID: "inst-3",
		StartedAt:  time.Now().UTC(),
	}
	if err := writeJSONFile(runtimeFilePath(""), rt); err != nil {
		t.Fatalf("write runtime: %v", err)
	}

	stopped, err := waitForWatcherExit("", rt.InstanceID, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForWatcherExit: %v", err)
	}
	if !stopped {
		t.Fatal("expected watcher to be observed as stopped")
	}
}

func TestWatcherStoppedIgnoresStalePIDWhenLockFree(t *testing.T) {
	useTempSyncHome(t)
	if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
		t.Fatalf("mkdir sync profile dir: %v", err)
	}

	if err := writePID(pidFilePath(""), 99999); err != nil {
		t.Fatalf("writePID: %v", err)
	}

	stopped, err := watcherStopped("", "")
	if err != nil {
		t.Fatalf("watcherStopped: %v", err)
	}
	if !stopped {
		t.Fatal("expected stale pid without runtime or lock to be treated as stopped")
	}
}

func TestWaitForWatcherExitReturnsFalseWhileLockHeld(t *testing.T) {
	useTempSyncHome(t)
	if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
		t.Fatalf("mkdir sync profile dir: %v", err)
	}

	fl, err := acquireSyncLock("", 0)
	if err != nil {
		t.Fatalf("acquireSyncLock: %v", err)
	}
	defer fl.Unlock()

	stopped, err := waitForWatcherExit("", "", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForWatcherExit: %v", err)
	}
	if stopped {
		t.Fatal("expected watcher to still be running while lock is held")
	}
}

func TestWatcherStoppedRequiresLockToBeFreeEvenWithMatchingExitAck(t *testing.T) {
	useTempSyncHome(t)
	if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
		t.Fatalf("mkdir sync profile dir: %v", err)
	}

	fl, err := acquireSyncLock("", 0)
	if err != nil {
		t.Fatalf("acquireSyncLock: %v", err)
	}
	defer fl.Unlock()

	if err := writeJSONFile(exitFilePath(""), syncExit{
		InstanceID: "inst-held",
		ExitedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("write exit ack: %v", err)
	}

	stopped, err := watcherStopped("", "inst-held")
	if err != nil {
		t.Fatalf("watcherStopped: %v", err)
	}
	if stopped {
		t.Fatal("expected watcherStopped to report running while lock is still held")
	}
}

func TestWatchSyncContextStopsOnMatchingStopRequest(t *testing.T) {
	useTempSyncHome(t)
	if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
		t.Fatalf("mkdir sync profile dir: %v", err)
	}

	ctx, stopped := newWatchSyncContext(context.Background(), "", "inst-stop")

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = writeSyncStopRequest("", syncStopRequest{
			InstanceID:  "inst-stop",
			RequestedAt: time.Now().UTC(),
		})
	}()

	select {
	case <-stopped:
	case <-time.After(syncAckTestWait):
		t.Fatal("timed out waiting for stop request to cancel watcher")
	}

	if err := ctx.Err(); err == nil {
		t.Fatal("expected watch context to be canceled after stop request")
	}
}
