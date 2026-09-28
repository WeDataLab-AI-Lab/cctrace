package main

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

func executeSyncCmdForTest(t *testing.T, args ...string) error {
	t.Helper()
	cmd := syncCmd()
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.Execute()
}

func TestSyncCmdRoutesStartAction(t *testing.T) {
	resetSyncControlHooks(t)
	called := false
	runSyncStartFn = func(claudeDir string, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool) error {
		called = true
		if claudeDir != "/tmp/claude" || interval != 5*time.Second || profileName != "work" || profileEmail != "user@example.com" || endpointOverride != "http://sync.example.com" || !local {
			t.Fatalf("start context mismatch: dir=%q interval=%s profile=%q email=%q endpoint=%q local=%v", claudeDir, interval, profileName, profileEmail, endpointOverride, local)
		}
		return nil
	}

	err := executeSyncCmdForTest(t,
		"start",
		"--claude-dir", "/tmp/claude",
		"--interval", "5s",
		"--profile", "work",
		"--profile-email", "user@example.com",
		"--endpoint", "http://sync.example.com",
		"--local",
	)
	if err != nil {
		t.Fatalf("sync start command: %v", err)
	}
	if !called {
		t.Fatal("sync start did not call runSyncStart")
	}
}

func TestSyncCmdRoutesFinalizeAction(t *testing.T) {
	resetSyncControlHooks(t)
	called := false
	runSyncFinalizeFn = func(claudeDir string, profileName string, profileEmail string, endpointOverride string, local bool) error {
		called = true
		if claudeDir != "/tmp/claude" || profileName != "work" || profileEmail != "user@example.com" || endpointOverride != "http://sync.example.com" || !local {
			t.Fatalf("finalize context mismatch: dir=%q profile=%q email=%q endpoint=%q local=%v", claudeDir, profileName, profileEmail, endpointOverride, local)
		}
		return nil
	}

	err := executeSyncCmdForTest(t,
		"finalize",
		"--claude-dir", "/tmp/claude",
		"--profile", "work",
		"--profile-email", "user@example.com",
		"--endpoint", "http://sync.example.com",
		"--local",
	)
	if err != nil {
		t.Fatalf("sync finalize command: %v", err)
	}
	if !called {
		t.Fatal("sync finalize did not call runSyncFinalize")
	}
}

func TestSyncCmdRoutesReenrichAction(t *testing.T) {
	resetSyncControlHooks(t)
	called := false
	runSyncReenrichFn = func(claudeDir string, profileName string, profileEmail string, endpointOverride string, local bool) error {
		called = true
		if claudeDir != "/tmp/claude" || profileName != "work" || profileEmail != "user@example.com" || endpointOverride != "http://sync.example.com" || !local {
			t.Fatalf("reenrich context mismatch: dir=%q profile=%q email=%q endpoint=%q local=%v", claudeDir, profileName, profileEmail, endpointOverride, local)
		}
		return nil
	}

	err := executeSyncCmdForTest(t,
		"reenrich",
		"--claude-dir", "/tmp/claude",
		"--profile", "work",
		"--profile-email", "user@example.com",
		"--endpoint", "http://sync.example.com",
		"--local",
	)
	if err != nil {
		t.Fatalf("sync reenrich command: %v", err)
	}
	if !called {
		t.Fatal("sync reenrich did not call runSyncReenrich")
	}
}

func TestSyncCmdRoutesFinalizeWorkerBeforePublicActions(t *testing.T) {
	resetSyncControlHooks(t)
	called := false
	runSyncFinalizeWorkerFn = func(claudeDir string, profileName string, profileEmail string, endpointOverride string, local bool) error {
		called = true
		if claudeDir != "/tmp/claude" || profileName != "work" || profileEmail != "user@example.com" || endpointOverride != "http://sync.example.com" || !local {
			t.Fatalf("finalize worker context mismatch: dir=%q profile=%q email=%q endpoint=%q local=%v", claudeDir, profileName, profileEmail, endpointOverride, local)
		}
		return nil
	}
	runSyncStartFn = func(claudeDir string, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool) error {
		t.Fatal("finalize worker must not route through public start action")
		return nil
	}

	err := executeSyncCmdForTest(t,
		"start",
		"--finalize-worker",
		"--claude-dir", "/tmp/claude",
		"--profile", "work",
		"--profile-email", "user@example.com",
		"--endpoint", "http://sync.example.com",
		"--local",
	)
	if err != nil {
		t.Fatalf("sync finalize-worker command: %v", err)
	}
	if !called {
		t.Fatal("sync --finalize-worker did not call runSyncFinalizeWorker")
	}
}

func TestSyncCmdRoutesDefaultOneShotToAllProfiles(t *testing.T) {
	resetSyncControlHooks(t)
	called := false
	runSyncAllFn = func(dryRun bool, interval time.Duration, profileEmail string, endpointOverride string, local bool) error {
		called = true
		if !dryRun || interval != 7*time.Second || profileEmail != "user@example.com" || endpointOverride != "http://sync.example.com" || !local {
			t.Fatalf("run all context mismatch: dry=%v interval=%s email=%q endpoint=%q local=%v", dryRun, interval, profileEmail, endpointOverride, local)
		}
		return nil
	}
	runSyncFn = func(dryRun bool, claudeDir string, watch bool, daemon bool, daemonOnce bool, stop bool, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool, startAckFile string, finalizeWorker bool) error {
		t.Fatal("default one-shot without profile must route through runSyncAll")
		return nil
	}

	err := executeSyncCmdForTest(t, "--dry-run", "--interval", "7s", "--profile-email", "user@example.com", "--endpoint", "http://sync.example.com", "--local")
	if err != nil {
		t.Fatalf("sync default all command: %v", err)
	}
	if !called {
		t.Fatal("sync default all did not call runSyncAll")
	}
}

func TestSyncCmdRoutesOnceMarkerToSingleRun(t *testing.T) {
	resetSyncControlHooks(t)
	called := false
	runSyncAllFn = func(dryRun bool, interval time.Duration, profileEmail string, endpointOverride string, local bool) error {
		t.Fatal("--once marker must not route through runSyncAll")
		return nil
	}
	runSyncFn = func(dryRun bool, claudeDir string, watch bool, daemon bool, daemonOnce bool, stop bool, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool, startAckFile string, finalizeWorker bool) error {
		called = true
		if !daemonOnce || daemon || watch || stop {
			t.Fatalf("once marker context mismatch: daemonOnce=%v daemon=%v watch=%v stop=%v", daemonOnce, daemon, watch, stop)
		}
		return nil
	}

	err := executeSyncCmdForTest(t, "--once")
	if err != nil {
		t.Fatalf("sync --once command: %v", err)
	}
	if !called {
		t.Fatal("sync --once did not call runSync")
	}
}

func TestSyncCmdAutoProfileSelectsProfileByClaudeDir(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := profile.NewDefault()
	p.ClaudeConfigDir = claudeDir
	if err := profile.SaveNamed(p, "work"); err != nil {
		t.Fatalf("profile.SaveNamed: %v", err)
	}

	called := false
	runSyncFn = func(dryRun bool, claudeDirArg string, watch bool, daemon bool, daemonOnce bool, stop bool, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool, startAckFile string, finalizeWorker bool) error {
		called = true
		if claudeDirArg != claudeDir || profileName != "work" {
			t.Fatalf("auto profile context = dir %q profile %q, want %q/work", claudeDirArg, profileName, claudeDir)
		}
		return nil
	}

	err := executeSyncCmdForTest(t, "--auto-profile", "--claude-dir", claudeDir)
	if err != nil {
		t.Fatalf("sync auto-profile command: %v", err)
	}
	if !called {
		t.Fatal("sync auto-profile did not call runSync")
	}
}

func TestSyncCmdAutoProfileUsesClaudeConfigDirEnv(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude-env")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	p := profile.NewDefault()
	p.ClaudeConfigDir = claudeDir
	if err := profile.SaveNamed(p, "envwork"); err != nil {
		t.Fatalf("profile.SaveNamed: %v", err)
	}

	called := false
	runSyncFn = func(dryRun bool, claudeDirArg string, watch bool, daemon bool, daemonOnce bool, stop bool, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool, startAckFile string, finalizeWorker bool) error {
		called = true
		if claudeDirArg != "" || profileName != "envwork" {
			t.Fatalf("auto profile env context = dir %q profile %q, want empty/envwork", claudeDirArg, profileName)
		}
		return nil
	}

	err := executeSyncCmdForTest(t, "--auto-profile")
	if err != nil {
		t.Fatalf("sync auto-profile env command: %v", err)
	}
	if !called {
		t.Fatal("sync auto-profile env did not call runSync")
	}
}

func TestSyncCmdRoutesExplicitProfileToSingleRun(t *testing.T) {
	resetSyncControlHooks(t)
	called := false
	runSyncFn = func(dryRun bool, claudeDir string, watch bool, daemon bool, daemonOnce bool, stop bool, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool, startAckFile string, finalizeWorker bool) error {
		called = true
		if !watch || interval != 3*time.Second || profileName != "work" || startAckFile != "/tmp/ack.json" || !local {
			t.Fatalf("single run context mismatch: watch=%v interval=%s profile=%q ack=%q local=%v", watch, interval, profileName, startAckFile, local)
		}
		return nil
	}

	err := executeSyncCmdForTest(t, "--watch", "--interval", "3s", "--profile", "work", "--start-ack-file", "/tmp/ack.json", "--local")
	if err != nil {
		t.Fatalf("sync explicit profile command: %v", err)
	}
	if !called {
		t.Fatal("sync explicit profile did not call runSync")
	}
}

func TestSyncCmdRejectsUnknownAction(t *testing.T) {
	err := executeSyncCmdForTest(t, "restart")
	if err == nil || !strings.Contains(err.Error(), `unknown sync action "restart"`) {
		t.Fatalf("error = %v, want unknown action", err)
	}
}
