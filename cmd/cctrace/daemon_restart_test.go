package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// TestReplaceOutdatedWatchChild covers the #103 decision that the daemon parent
// makes before spawning: only a STRICTLY older, running watch child is stopped.
// Every no-op branch must avoid issuing a stop request (which would otherwise
// churn/loop against pre-stamp legacy daemons).
func TestReplaceOutdatedWatchChild(t *testing.T) {
	cases := []struct {
		name        string
		serverVer   string
		childVer    string
		writeChild  bool
		wantReplace bool
	}{
		{"older child is replaced", "v0.6.0", "v0.5.9", true, true},
		{"much older child is replaced", "v1.0.0", "v0.5.9", true, true},
		{"current child is left alone", "v0.6.0", "v0.6.0", true, false},
		{"newer child is left alone", "v0.6.0", "v0.7.0", true, false},
		{"empty child version left alone (no loop)", "v0.6.0", "", true, false},
		{"dev child version left alone", "v0.6.0", "dev", true, false},
		{"no server version is a no-op", "", "v0.5.9", true, false},
		{"no running child is a no-op", "v0.6.0", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)

			var stopWaited bool
			var waitedInstance string
			waitForWatcherExitFn = func(profileName string, instanceID string, timeout time.Duration) (bool, error) {
				stopWaited = true
				waitedInstance = instanceID
				if timeout != daemonRespawnStopWait {
					t.Fatalf("wait timeout = %s, want %s", timeout, daemonRespawnStopWait)
				}
				return true, nil
			}

			const profileName = ""
			const instanceID = "inst-123"
			if err := os.MkdirAll(syncProfileDir(profileName), 0700); err != nil {
				t.Fatalf("mkdir profile dir: %v", err)
			}
			if tc.writeChild {
				rt := syncRuntime{PID: 4242, InstanceID: instanceID, StartedAt: time.Now().UTC(), Version: tc.childVer}
				if err := writeJSONFile(runtimeFilePath(profileName), rt); err != nil {
					t.Fatalf("write runtime: %v", err)
				}
			}

			got := replaceOutdatedWatchChild(profileName, tc.serverVer)
			if got != tc.wantReplace {
				t.Fatalf("replaceOutdatedWatchChild = %v, want %v", got, tc.wantReplace)
			}

			// A stop request + a wait for exit must be issued IFF we replaced —
			// never for a current/newer/empty child (that would churn the daemon).
			if stopWaited != tc.wantReplace {
				t.Fatalf("waitForWatcherExit called = %v, want %v", stopWaited, tc.wantReplace)
			}
			if _, stopWritten, _ := readSyncStopRequest(profileName); stopWritten != tc.wantReplace {
				t.Fatalf("stop request written = %v, want %v", stopWritten, tc.wantReplace)
			}
			if tc.wantReplace && waitedInstance != instanceID {
				t.Fatalf("waited instance = %q, want %q", waitedInstance, instanceID)
			}
		})
	}
}

// TestDaemonParentReplacesOutdatedChildThenSpawns exercises the full daemon-parent
// path (runSync with daemon=true) with the network update mocked: it must stop an
// outdated running child (and only an outdated one) and then always spawn exactly
// one fresh watch child. The production lock guard (not exercised with a mocked
// spawn) is what prevents duplicates when the stop does not take.
func TestDaemonParentReplacesOutdatedChildThenSpawns(t *testing.T) {
	cases := []struct {
		name      string
		childVer  string
		serverVer string
		wantStop  bool
	}{
		{"outdated child stopped then respawned", "v0.5.9", "v0.6.0", true},
		{"current child not stopped, still respawns", "v0.6.0", "v0.6.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)

			origUpdate := applyDaemonParentUpdateFn
			t.Cleanup(func() { applyDaemonParentUpdateFn = origUpdate })
			applyDaemonParentUpdateFn = func(ctx context.Context, profileName, endpoint string, local bool) daemonParentUpdateResult {
				return daemonParentUpdateResult{ServerVersion: tc.serverVer}
			}

			var stopWaited bool
			waitForWatcherExitFn = func(profileName, instanceID string, timeout time.Duration) (bool, error) {
				stopWaited = true
				return true, nil
			}
			var spawned [][]string
			spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
				spawned = append(spawned, append([]string(nil), args...))
				return 777, nil
			}

			if err := os.MkdirAll(syncProfileDir(""), 0700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := writeJSONFile(runtimeFilePath(""), syncRuntime{PID: 4242, InstanceID: "old", StartedAt: time.Now().UTC(), Version: tc.childVer}); err != nil {
				t.Fatalf("write runtime: %v", err)
			}

			// daemon parent path: watch=false, daemon=true, daemonOnce=false
			if err := runSync(false, "", false, true, false, false, time.Second, "", "", "", false, "", false); err != nil {
				t.Fatalf("runSync daemon: %v", err)
			}

			if stopWaited != tc.wantStop {
				t.Fatalf("stop issued = %v, want %v", stopWaited, tc.wantStop)
			}
			if len(spawned) != 1 {
				t.Fatalf("spawn count = %d, want exactly 1 fresh child", len(spawned))
			}
			if len(spawned[0]) < 2 || spawned[0][0] != "sync" || spawned[0][1] != "--watch" {
				t.Fatalf("spawn args = %v, want a --watch child", spawned[0])
			}
		})
	}
}

func TestDirectWatchReexecsAfterApplyingUpdateBeforeCollection(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)

	oldVersion := version
	version = "v0.7.18"
	t.Cleanup(func() { version = oldVersion })

	p := profile.NewDefault()
	p.Server.Endpoint = "https://trace.example.com"
	p.ClaudeConfigDir = t.TempDir()
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	// If the watch path reaches collection instead of handing off first, state
	// loading fails rather than leaving the regression test in a live watch loop.
	if err := os.MkdirAll(syncer.DefaultStatePath(), 0700); err != nil {
		t.Fatalf("mkdir state path: %v", err)
	}

	applyCalls := 0
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		applyCalls++
		return daemonParentUpdateResult{ServerVersion: "v0.7.29", Applied: true}
	}
	var reexecCalls int
	reexecDaemonParentFn = func(string) error {
		reexecCalls++
		return nil
	}

	if err := runSync(false, p.ClaudeConfigDir, true, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync direct watch: %v", err)
	}
	if applyCalls != 1 {
		t.Fatalf("watch update calls = %d, want 1", applyCalls)
	}
	if reexecCalls != 1 {
		t.Fatalf("watch reexec calls = %d, want 1", reexecCalls)
	}
}

func TestDirectWatchPeriodicUpdateHandoffRunsBetweenSyncPasses(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)

	oldVersion := version
	version = "v0.7.18"
	t.Cleanup(func() { version = oldVersion })
	oldCheckInterval := watchUpdateCheckInterval
	watchUpdateCheckInterval = 0
	t.Cleanup(func() { watchUpdateCheckInterval = oldCheckInterval })

	var synced atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			synced.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
		case "/api/project-rules":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0, "inserted_versions": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	claudeDir := filepath.Join(t.TempDir(), ".claude")
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-08-26T00:00:00Z","sessionId":"update-handoff","cwd":"/tmp/repo","message":{"role":"user","content":"before update"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0600); err != nil {
		t.Fatalf("write session: %v", err)
	}

	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = srv.URL
	p.Server.SyncEndpoint = srv.URL
	p.ClaudeConfigDir = claudeDir
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	state, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(sessionPath, 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	var updateCalls int
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		updateCalls++
		if updateCalls == 1 {
			return daemonParentUpdateResult{ServerVersion: "v0.7.18"}
		}
		if !synced.Load() {
			t.Fatal("periodic update ran before the initial sync pass completed")
		}
		return daemonParentUpdateResult{ServerVersion: "v0.7.29", Applied: true}
	}
	var reexecCalls int
	reexecDaemonParentFn = func(string) error {
		reexecCalls++
		if reexecCalls == 1 {
			return errors.New("transient reexec failure")
		}
		return nil
	}

	runWatchUntilStopped(t, "direct watch update handoff", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, "", false)
	})
	if updateCalls != 3 {
		t.Fatalf("watch update calls = %d, want startup plus two periodic checks", updateCalls)
	}
	if reexecCalls != 2 {
		t.Fatalf("watch reexec calls = %d, want failed handoff followed by success", reexecCalls)
	}
	reloaded, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if offset := reloaded.GetOffset(sessionPath); offset == 0 {
		t.Fatal("sync state was not saved before update handoff")
	}
}

func TestDirectWatchReexecMarkerPreventsLoop(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)

	oldVersion := version
	version = "v0.7.18"
	t.Cleanup(func() { version = oldVersion })
	t.Setenv(daemonParentReexecEnv, "1")

	p := profile.NewDefault()
	p.Server.Endpoint = "https://trace.example.com"
	p.ClaudeConfigDir = t.TempDir()
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	if err := os.MkdirAll(syncer.DefaultStatePath(), 0700); err != nil {
		t.Fatalf("mkdir state path: %v", err)
	}

	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		return daemonParentUpdateResult{ServerVersion: "v0.7.29", Applied: true}
	}
	reexecDaemonParentFn = func(string) error {
		t.Fatal("replacement watch must not re-exec again")
		return nil
	}

	err := runSync(false, p.ClaudeConfigDir, true, false, false, false, time.Second, "", "", "", false, "", false)
	if err == nil || !strings.Contains(err.Error(), "load sync state") {
		t.Fatalf("runSync error = %v, want sentinel state-load failure after loop guard", err)
	}
	if got := os.Getenv(daemonParentReexecEnv); got != "" {
		t.Fatalf("%s = %q, want consumed marker", daemonParentReexecEnv, got)
	}
}

// These three drive runSync(daemon=true) far enough to reach
// replaceOutdatedWatchChild, which reads sync-runtime.json and can write a stop
// request. Without a temporary home that is the developer's OWN daemon: the
// stubbed update reports v0.7.22, so any real daemon stamped below it would
// have been asked to stop by running the test suite. It did not fire only
// because the machine happened to be on a newer build.
func TestDaemonParentReexecsAfterApplyingUpdate(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	executable := "/install/cctrace"
	syncExecutablePathFn = func() (string, error) { return executable, nil }
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		executable = "/install/.cctrace.old"
		return daemonParentUpdateResult{ServerVersion: "v0.7.22", Applied: true}
	}

	var reexecCalls, spawnCalls int
	reexecDaemonParentFn = func(path string) error {
		reexecCalls++
		if path != "/install/cctrace" {
			t.Fatalf("replacement path = %q, want pre-update executable path", path)
		}
		return nil
	}
	spawnSyncProcessFn = func([]string, string) (int, error) {
		spawnCalls++
		return 123, nil
	}

	if err := runSync(false, "", false, true, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync daemon: %v", err)
	}
	if reexecCalls != 1 {
		t.Fatalf("reexec calls = %d, want 1", reexecCalls)
	}
	if spawnCalls != 0 {
		t.Fatalf("old parent spawn calls = %d, want 0", spawnCalls)
	}
}

func TestDaemonParentReexecMarkerPreventsLoopAndIsNotInherited(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	t.Setenv(daemonParentReexecEnv, "1")
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		return daemonParentUpdateResult{ServerVersion: "v0.7.22", Applied: true}
	}
	reexecDaemonParentFn = func(string) error {
		t.Fatal("marked replacement parent must not reexec again")
		return nil
	}

	var spawnCalls int
	spawnSyncProcessFn = func([]string, string) (int, error) {
		spawnCalls++
		if got := os.Getenv(daemonParentReexecEnv); got != "" {
			t.Fatalf("spawn inherited %s=%q, want unset", daemonParentReexecEnv, got)
		}
		return 123, nil
	}

	if err := runSync(false, "", false, true, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync daemon: %v", err)
	}
	if spawnCalls != 1 {
		t.Fatalf("spawn calls = %d, want 1", spawnCalls)
	}
}

func TestDaemonParentFallsBackToSpawnWhenReexecFails(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		return daemonParentUpdateResult{ServerVersion: "v0.7.22", Applied: true}
	}
	reexecDaemonParentFn = func(string) error { return errors.New("reexec unavailable") }

	var spawnCalls int
	spawnSyncProcessFn = func([]string, string) (int, error) {
		spawnCalls++
		return 123, nil
	}

	if err := runSync(false, "", false, true, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync daemon fallback: %v", err)
	}
	if spawnCalls != 1 {
		t.Fatalf("fallback spawn calls = %d, want 1", spawnCalls)
	}
}

// TestDaemonParentReexecFailureReachesSyncLog covers the only branch that reports
// "the disk was replaced but the running process is still the old binary" — the
// exact split #458 could not resolve. The hook command carries --log-to-file, so
// this parent's stderr is discarded by the caller; the report has to reach
// sync.log or it is lost.
func TestDaemonParentReexecFailureReachesSyncLog(t *testing.T) {
	cases := []struct {
		name       string
		executable func() (string, error)
		reexec     func(string) error
		want       string
	}{
		{
			name:       "restart failure",
			executable: func() (string, error) { return "/install/cctrace", nil },
			reexec:     func(string) error { return errors.New("reexec unavailable") },
			want:       "restart daemon parent",
		},
		{
			name:       "executable resolve failure",
			executable: func() (string, error) { return "", errors.New("no executable path") },
			reexec:     func(string) error { t.Fatal("must not reexec without an executable path"); return nil },
			want:       "resolve daemon parent executable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			useProgressTTY(t, false)

			// The hook command spawns the daemon parent with --log-to-file, which
			// is what routes this branch's output at sync.log.
			prevLogToFile := syncLogToFile
			syncLogToFile = true
			t.Cleanup(func() { syncLogToFile = prevLogToFile })

			syncExecutablePathFn = tc.executable
			applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
				return daemonParentUpdateResult{ServerVersion: "v0.7.22", Applied: true}
			}
			reexecDaemonParentFn = tc.reexec
			spawnSyncProcessFn = func([]string, string) (int, error) { return 123, nil }

			if err := runSync(false, "", false, true, false, false, time.Second, "", "", "", false, "", false); err != nil {
				t.Fatalf("runSync daemon: %v", err)
			}

			data, err := os.ReadFile(syncLogPath(""))
			if err != nil {
				t.Fatalf("read sync log: %v", err)
			}
			if !strings.Contains(string(data), tc.want) {
				t.Fatalf("sync.log missing %q:\n%s", tc.want, data)
			}
		})
	}
}
