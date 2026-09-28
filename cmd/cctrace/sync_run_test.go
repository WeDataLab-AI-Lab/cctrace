package main

import (
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

	"cctrace/internal/codexsyncer"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

func TestRunSyncOneShotSendsNewSessionAndRefreshesSettings(t *testing.T) {
	useTempSyncHome(t)

	var got syncRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode sync request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": len(got.Records)})
		case "/api/project-rules":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0, "inserted_versions": 0})
		default:
			t.Fatalf("path = %q, want /api/sync or /api/project-rules", r.URL.Path)
		}
	}))
	defer srv.Close()

	claudeDir := filepath.Join(t.TempDir(), ".claude")
	repoDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"s1","cwd":` + quoteJSONString(repoDir) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.User.ID = "u1"
	p.User.Name = "User"
	p.User.Team = "Platform"
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.SyncEndpoint = srv.URL
	p.ClaudeConfigDir = claudeDir
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	state, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync: %v", err)
	}

	if got.ProfileEmail != "user@example.com" {
		t.Fatalf("profile_email = %q, want user@example.com", got.ProfileEmail)
	}
	if got.UserID != "u1" {
		t.Fatalf("user_id = %q, want u1", got.UserID)
	}
	if len(got.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(got.Records))
	}
	refreshed, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	if !json.Valid(refreshed) {
		t.Fatalf("settings.json is not valid JSON: %s", refreshed)
	}
	state, err = syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if gotOffset := state.GetOffset(sessionPath); gotOffset == 0 {
		t.Fatal("sync state offset was not advanced")
	}
}

type syncRequestBody struct {
	ProfileEmail string            `json:"profile_email"`
	UserID       string            `json:"user_id"`
	Records      []json.RawMessage `json:"records"`
}

func quoteJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestRunSyncStopReturnsNoDaemonWhenLockFree(t *testing.T) {
	useTempSyncHome(t)

	err := runSync(false, "", false, false, false, true, time.Second, "", "", "", false, "", false)
	if err == nil {
		t.Fatal("expected no running daemon error")
	}
}

func TestRunSyncStopPropagatesRuntimeReadAndStopWriteErrors(t *testing.T) {
	t.Run("runtime read", func(t *testing.T) {
		useTempSyncHome(t)
		if err := os.MkdirAll(runtimeFilePath(""), 0700); err != nil {
			t.Fatalf("mkdir runtime path: %v", err)
		}
		err := runSync(false, "", false, false, false, true, time.Second, "", "", "", false, "", false)
		if err == nil || !strings.Contains(err.Error(), "read watcher runtime") {
			t.Fatalf("error = %v, want read watcher runtime", err)
		}
	})

	t.Run("stop write", func(t *testing.T) {
		useTempSyncHome(t)
		rt := &syncRuntime{PID: 1234, InstanceID: "inst-stop", StartedAt: time.Now().UTC()}
		if err := writeJSONFile(runtimeFilePath(""), rt); err != nil {
			t.Fatalf("write runtime: %v", err)
		}
		if err := os.MkdirAll(stopRequestFilePath(""), 0700); err != nil {
			t.Fatalf("mkdir stop request path: %v", err)
		}
		err := runSync(false, "", false, false, false, true, time.Second, "", "", "", false, "", false)
		if err == nil || !strings.Contains(err.Error(), "write stop request") {
			t.Fatalf("error = %v, want write stop request", err)
		}
	})
}

func TestRunSyncDaemonSpawnsBackgroundArgs(t *testing.T) {
	resetSyncControlHooks(t)
	tests := []struct {
		name       string
		daemonOnce bool
		want       []string
	}{
		{
			name:       "watch",
			daemonOnce: false,
			want: []string{
				"sync", "--watch", "--interval", "4s",
				"--claude-dir", "/tmp/claude",
				"--profile", "work",
				"--profile-email", "user@example.com",
				"--endpoint", "http://sync.example.com",
				"--local",
			},
		},
		{
			name:       "once",
			daemonOnce: true,
			want: []string{
				"sync", "--once",
				"--claude-dir", "/tmp/claude",
				"--profile", "work",
				"--profile-email", "user@example.com",
				"--endpoint", "http://sync.example.com",
				"--local",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetSyncControlHooks(t)
			var gotArgs []string
			var gotProfile string
			spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
				gotArgs = append([]string(nil), args...)
				gotProfile = profileName
				return 123, nil
			}

			err := runSync(false, "/tmp/claude", false, true, tt.daemonOnce, false, 4*time.Second, "work", "user@example.com", "http://sync.example.com", true, "", false)
			if err != nil {
				t.Fatalf("runSync daemon: %v", err)
			}
			assertArgsEqual(t, gotArgs, tt.want)
			if gotProfile != "work" {
				t.Fatalf("spawn profile = %q, want work", gotProfile)
			}
		})
	}
}

func TestRunSyncDaemonChecksVersionBeforeSpawning(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)

	oldVersion := version
	version = "v0.5.5"
	t.Cleanup(func() {
		version = oldVersion
	})

	var checked atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			http.NotFound(w, r)
			return
		}
		checked.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "v0.5.5"})
	}))
	defer srv.Close()

	p := profile.NewDefault()
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.SyncEndpoint = srv.URL
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}

	spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
		if !checked.Load() {
			t.Fatal("daemon parent spawned child before checking server version")
		}
		return 123, nil
	}

	err := runSync(false, "", false, true, false, false, time.Second, "", "", "", false, "", false)
	if err != nil {
		t.Fatalf("runSync daemon: %v", err)
	}
	if !checked.Load() {
		t.Fatal("daemon parent did not check server version")
	}
}

func TestRunSyncDaemonPropagatesSpawnError(t *testing.T) {
	resetSyncControlHooks(t)
	spawnSyncProcessFn = func(args []string, profileName string) (int, error) {
		return 0, errors.New("spawn failed")
	}

	err := runSync(false, "", false, true, false, false, time.Second, "", "", "", false, "", false)
	if err == nil || !strings.Contains(err.Error(), "spawn failed") {
		t.Fatalf("error = %v, want spawn failed", err)
	}
}

func TestRunSyncStopReportsLegacyDaemonWhenLockHeldWithoutRuntime(t *testing.T) {
	useTempSyncHome(t)
	fl, err := acquireSyncLock("", 0)
	if err != nil {
		t.Fatalf("acquireSyncLock: %v", err)
	}
	defer fl.Unlock()

	err = runSync(false, "", false, false, false, true, time.Second, "", "", "", false, "", false)
	if err == nil || !strings.Contains(err.Error(), "does not support graceful stop") {
		t.Fatalf("error = %v, want legacy daemon stop error", err)
	}
}

func TestRunSyncOneShotReturnsAlreadyRunningWhenLockHeld(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	fl, err := acquireSyncLock("", 0)
	if err != nil {
		t.Fatalf("acquireSyncLock: %v", err)
	}
	defer fl.Unlock()
	syncLockWaitDuration = 20 * time.Millisecond

	err = runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false)
	if !errors.Is(err, errSyncAlreadyRunning) {
		t.Fatalf("error = %v, want errSyncAlreadyRunning", err)
	}
}

func TestRunSyncDaemonOnceSkipsWhenWatcherLockHeld(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	fl, err := acquireSyncLock("", 0)
	if err != nil {
		t.Fatalf("acquireSyncLock: %v", err)
	}
	defer fl.Unlock()
	syncLockWaitDuration = 20 * time.Millisecond

	err = runSync(false, claudeDir, false, false, true, false, time.Second, "", "", "", false, "", false)
	if err != nil {
		t.Fatalf("daemon-once child with active watcher should exit cleanly, got %v", err)
	}
}

func TestRunSyncStopPropagatesWaitErrorAndTimeout(t *testing.T) {
	tests := []struct {
		name    string
		stopped bool
		waitErr error
		wantErr string
	}{
		{name: "wait error", waitErr: errors.New("wait failed"), wantErr: "wait for daemon shutdown"},
		{name: "timeout", stopped: false, wantErr: "timed out waiting for daemon shutdown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			rt := &syncRuntime{PID: 1234, InstanceID: "inst-stop", StartedAt: time.Now().UTC()}
			if err := startWatchRuntime("", rt); err != nil {
				t.Fatalf("startWatchRuntime: %v", err)
			}
			waitForWatcherExitFn = func(profileName string, instanceID string, timeout time.Duration) (bool, error) {
				if instanceID != rt.InstanceID {
					t.Fatalf("instanceID = %q, want %q", instanceID, rt.InstanceID)
				}
				return tt.stopped, tt.waitErr
			}

			err := runSync(false, "", false, false, false, true, time.Second, "", "", "", false, "", false)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunSyncStopWritesStopRequestForRuntimeInstance(t *testing.T) {
	useTempSyncHome(t)
	rt := &syncRuntime{PID: 1234, InstanceID: "inst-stop", StartedAt: time.Now().UTC()}
	if err := startWatchRuntime("", rt); err != nil {
		t.Fatalf("startWatchRuntime: %v", err)
	}

	if err := runSync(false, "", false, false, false, true, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync stop: %v", err)
	}
	req, ok, err := readSyncStopRequest("")
	if err != nil {
		t.Fatalf("read stop request: %v", err)
	}
	if !ok || req.InstanceID != "inst-stop" {
		t.Fatalf("stop request = %+v ok=%v, want instance inst-stop", req, ok)
	}
}

func TestRunSyncWatchWritesStartAckErrorWhenNamedStateUnreadable(t *testing.T) {
	useTempSyncHome(t)
	profileName := "work"
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	if err := profile.SaveNamed(p, profileName); err != nil {
		t.Fatalf("profile.SaveNamed: %v", err)
	}
	dir, err := profile.NamedDir(profileName)
	if err != nil {
		t.Fatalf("profile.NamedDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sync-state.json"), 0700); err != nil {
		t.Fatalf("mkdir state path as dir: %v", err)
	}
	ackPath := filepath.Join(t.TempDir(), "ack.json")

	err = runSync(false, claudeDir, true, false, false, false, time.Second, profileName, "", "", false, ackPath, false)
	if err == nil || !strings.Contains(err.Error(), "load sync state") {
		t.Fatalf("error = %v, want load sync state", err)
	}
	ack, ok, err := readSyncStartAck(ackPath)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if !ok || ack.Status != syncStartAckError || !strings.Contains(ack.Error, "load sync state") {
		t.Fatalf("ack = %+v ok=%v, want load-state error ack", ack, ok)
	}
}

func TestRunSyncWatchWritesStartAckErrorWhenRuntimeStartFails(t *testing.T) {
	useTempSyncHome(t)
	profileName := "work"
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	if err := profile.SaveNamed(p, profileName); err != nil {
		t.Fatalf("profile.SaveNamed: %v", err)
	}
	if err := os.MkdirAll(pidFilePath(profileName), 0700); err != nil {
		t.Fatalf("mkdir pid path: %v", err)
	}
	ackPath := filepath.Join(t.TempDir(), "ack.json")

	err := runSync(false, claudeDir, true, false, false, false, time.Second, profileName, "", "", false, ackPath, false)
	if err == nil || !strings.Contains(err.Error(), "write pid file") {
		t.Fatalf("error = %v, want write pid file", err)
	}
	ack, ok, err := readSyncStartAck(ackPath)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if !ok || ack.Status != syncStartAckError || ack.Error == "" {
		t.Fatalf("ack = %+v ok=%v, want runtime start error ack", ack, ok)
	}
}

func TestRunSyncAllRunsDefaultAndNamedProfiles(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	if err := profile.Save(profile.NewDefault()); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	if err := profile.SaveNamed(profile.NewDefault(), "work"); err != nil {
		t.Fatalf("profile.SaveNamed: %v", err)
	}
	var got []string
	runSyncFn = func(dryRun bool, claudeDir string, watch bool, daemon bool, daemonOnce bool, stop bool, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool, startAckFile string, finalizeWorker bool) error {
		got = append(got, profileName)
		if !dryRun || interval != 9*time.Second || profileEmail != "user@example.com" || endpointOverride != "http://sync.example.com" || !local {
			t.Fatalf("runSyncAll did not propagate context")
		}
		return nil
	}

	if err := runSyncAll(true, 9*time.Second, "user@example.com", "http://sync.example.com", true); err != nil {
		t.Fatalf("runSyncAll: %v", err)
	}
	assertArgsEqual(t, got, []string{"", "work"})
}

func TestRunSyncLocalUsesLocalSyncEndpoint(t *testing.T) {
	useTempSyncHome(t)
	var got syncRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode sync request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": len(got.Records)})
		case "/api/project-rules":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0, "inserted_versions": 0})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("CCTRACE_LOCAL_SYNC_ENDPOINT", strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("CCTRACE_LOCAL_OTEL_ENDPOINT", "")
	t.Setenv("HTTP_PORT", "")
	t.Setenv("GRPC_PORT", "")

	claudeDir := filepath.Join(t.TempDir(), ".claude")
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"local-1","cwd":` + quoteJSONString(t.TempDir()) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "https://trace.example.com:4317"
	p.Server.SyncEndpoint = "https://trace.example.com"
	p.ClaudeConfigDir = claudeDir
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	state, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", true, "", false); err != nil {
		t.Fatalf("runSync local: %v", err)
	}
	if len(got.Records) != 1 {
		t.Fatalf("records sent to local endpoint = %d, want 1", len(got.Records))
	}
}

func TestRunSyncCodexEnabledHandlesStateLoadError(t *testing.T) {
	useTempSyncHome(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	codexDir := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", codexDir)
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	p.Options.CodexSyncEnabled = true
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	if err := os.MkdirAll(codexsyncer.StatePathForProfile(""), 0700); err != nil {
		t.Fatalf("mkdir codex state path as dir: %v", err)
	}

	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync with codex state load error should not abort: %v", err)
	}
}

func TestRunSyncCodexEnabledRunsCodexSyncer(t *testing.T) {
	useTempSyncHome(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	codexDir := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", codexDir)
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	p.Options.CodexSyncEnabled = true
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}

	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync with codex enabled: %v", err)
	}
}

func TestRunSyncReturnsWhenSyncDisabled(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Options.SyncEnabled = false
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}

	if err := runSync(false, "", false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync disabled: %v", err)
	}
}

func TestRunSyncRequiresEndpoint(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}

	if err := runSync(false, "", false, false, false, false, time.Second, "", "", "", false, "", false); err == nil {
		t.Fatal("expected missing endpoint error")
	}
}

func TestRunSyncWatchWritesAlreadyRunningAckWhenLockHeld(t *testing.T) {
	useTempSyncHome(t)

	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), ".claude")
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	fl, err := acquireSyncLock("", 0)
	if err != nil {
		t.Fatalf("acquireSyncLock: %v", err)
	}
	defer fl.Unlock()
	ackPath := filepath.Join(t.TempDir(), "ack.json")

	if err := runSync(false, p.ClaudeConfigDir, true, false, false, false, time.Second, "", "", "", false, ackPath, false); err != nil {
		t.Fatalf("runSync watch: %v", err)
	}
	ack, ok, err := readSyncStartAck(ackPath)
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if !ok || ack.Status != syncStartAckAlreadyRunning {
		t.Fatalf("ack = %+v ok=%v, want already-running", ack, ok)
	}
}

func TestRunSyncWatchStartsAndStopsFromStopRequest(t *testing.T) {
	useTempSyncHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync":
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 0})
		case "/api/project-rules":
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0, "inserted_versions": 0})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.SyncEndpoint = srv.URL
	p.ClaudeConfigDir = claudeDir
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	state, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	ackPath := filepath.Join(t.TempDir(), "ack.json")
	stopped := stopWatchWhenReady(t, ackPath)

	runWatchUntilStopped(t, "runSync watch", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, ackPath, false)
	})
	<-stopped
	if _, err := os.Stat(runtimeFilePath("")); !os.IsNotExist(err) {
		t.Fatalf("runtime file should be removed after watcher exits, stat err=%v", err)
	}
}

func TestRunSyncWatchStopsAfterInitialRetryableFailure(t *testing.T) {
	useTempSyncHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/api/project-rules":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0, "inserted_versions": 0})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	claudeDir := filepath.Join(t.TempDir(), ".claude")
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"retry-1","cwd":` + quoteJSONString(t.TempDir()) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
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
	ackPath := filepath.Join(t.TempDir(), "ack.json")
	stopped := stopWatchWhenReady(t, ackPath)

	runWatchUntilStopped(t, "runSync watch retry failure", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Hour, "", "", "", false, ackPath, false)
	})
	<-stopped
}

func TestRunSyncDryUsesResolvedClaudeDir(t *testing.T) {
	useTempSyncHome(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}

	if err := runSync(true, "", false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync dry: %v", err)
	}
}
