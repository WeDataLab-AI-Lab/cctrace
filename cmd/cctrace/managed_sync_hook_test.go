package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

func TestManagedStartPreservesHookTranscript(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	withHookStdin(t, map[string]any{"hook_event_name": "SessionStart", "transcript_path": transcript})
	var spawned []string
	spawnSyncProcessFn = func(args []string, name string) (int, error) {
		spawned = append([]string(nil), args...)
		return 123, nil
	}
	waitForSyncStartAckFn = func(string, time.Duration) (*syncStartAck, error) {
		return &syncStartAck{Status: syncStartAckStarted}, nil
	}
	if err := runManagedStart("", time.Second, "", "", "", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(spawned); i++ {
		if spawned[i] == "--collect-file" && spawned[i+1] == transcript {
			return
		}
	}
	t.Fatalf("hook transcript missing from supervisor args: %v", spawned)
}

// hookStartArgs is the SessionStart hook command envgen installs
// (internal/envgen/hooks.go, syncCommands): it always names the Claude home.
// --log-to-file binds a package variable; the caller's test restores it.
func hookStartArgs(t *testing.T, claudeDir string) []string {
	t.Helper()
	prev := syncLogToFile
	t.Cleanup(func() { syncLogToFile = prev })
	return []string{"--daemon", "--log-to-file", "--claude-dir", claudeDir, "--auto-profile", "--interval", "1s"}
}

func saveHookFixtureProfile(t *testing.T, name, claudeDir string) {
	t.Helper()
	p := profile.NewDefault()
	p.ClaudeConfigDir = claudeDir
	p.User = profile.UserInfo{ID: name, Name: name, Email: name + "@fixture.invalid", Team: "fixture"}
	p.Server.Endpoint = "http://127.0.0.1:1"
	save := func() error { return profile.SaveNamed(p, name) }
	if name == "default" {
		save = func() error { return profile.Save(p) }
	}
	if err := save(); err != nil {
		t.Fatal(err)
	}
}

// Every SessionStart after the first ran while the daemon the first one started
// was running, and the --claude-dir the hook always passes was refused as a
// runtime override although it names the home the profile is configured with.
func TestManagedStartHookSameClaudeHomeWhileDaemonRuns(t *testing.T) {
	home := useTempSyncHome(t)
	resetSyncControlHooks(t)
	t.Setenv("CCTRACE_PROFILE", "")
	saveHookFixtureProfile(t, "default", "")
	runningSupervisor(t)
	spawnSyncProcessFn = func([]string, string) (int, error) { t.Fatal("hook spawned a second daemon"); return 0, nil }
	withHookStdin(t, map[string]any{"hook_event_name": "SessionStart", "transcript_path": filepath.Join(home, "session.jsonl")})
	if err := executeSyncCmdForTest(t, hookStartArgs(t, filepath.Join(home, ".claude"))...); err != nil {
		t.Fatalf("SessionStart hook refused by the running daemon: %v", err)
	}
}

// A second Claude home's hook names its own profile's home. Its start is what
// resumes that profile's job in the daemon the first home started.
func TestManagedStartHookSecondClaudeHomeStartsItsProfile(t *testing.T) {
	home := useTempSyncHome(t)
	resetSyncControlHooks(t)
	t.Setenv("CCTRACE_PROFILE", "")
	second := filepath.Join(home, ".claude-3")
	t.Setenv("CLAUDE_CONFIG_DIR", second)
	saveHookFixtureProfile(t, "default", "")
	saveHookFixtureProfile(t, "three", second)
	root := profile.DefaultDir()
	startSupervisor(t, localdaemon.New(localdaemon.Config{Root: root, Refresh: 5 * time.Millisecond, Load: func() ([]localdaemon.Spec, error) {
		var specs []localdaemon.Spec
		for _, id := range []string{"default", "named:three"} {
			specs = append(specs, localdaemon.Spec{ID: id, Name: id, RuntimeDir: root, Enabled: true, Revision: "one", Run: func(_ context.Context, report func(localdaemon.Progress)) error {
				report(localdaemon.Progress{State: "healthy"})
				return nil
			}})
		}
		return specs, nil
	}}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := localdaemon.Command(ctx, root, "stop", "named:three"); err != nil {
		t.Fatal(err)
	}
	spawnSyncProcessFn = func([]string, string) (int, error) { t.Fatal("hook spawned a second daemon"); return 0, nil }
	withHookStdin(t, map[string]any{"hook_event_name": "SessionStart"})
	if err := executeSyncCmdForTest(t, hookStartArgs(t, second)...); err != nil {
		t.Fatalf("second home's SessionStart hook refused by the running daemon: %v", err)
	}
	managedEventually(t, func() bool {
		snapshot, err := localdaemon.ReadSnapshot(root)
		if err != nil {
			return false
		}
		for _, job := range snapshot.Jobs {
			if job.ID == "named:three" {
				return job.State != "stopped"
			}
		}
		return false
	})
}

// What the running daemon cannot take without a restart is still refused: a
// home the profile is not configured with -- including the one CLAUDE_CONFIG_DIR
// names when no profile claims it -- and the email, endpoint and local overrides.
func TestManagedStartRefusesOverridesTheRunningDaemonCannotApply(t *testing.T) {
	home := useTempSyncHome(t)
	resetSyncControlHooks(t)
	t.Setenv("CCTRACE_PROFILE", "")
	saveHookFixtureProfile(t, "default", "")
	runningSupervisor(t)
	spawnSyncProcessFn = func([]string, string) (int, error) { t.Fatal("override spawned a second daemon"); return 0, nil }
	unclaimed := filepath.Join(home, ".claude-3")
	for _, tc := range []struct {
		name, configDir string
		args            []string
	}{
		{"other claude home", "", hookStartArgs(t, filepath.Join(home, "elsewhere"))},
		{"unclaimed CLAUDE_CONFIG_DIR home", unclaimed, hookStartArgs(t, unclaimed)},
		{"email", "", []string{"--daemon", "--profile-email", "other@fixture.invalid"}},
		{"endpoint", "", []string{"--daemon", "--endpoint", "http://127.0.0.1:8080"}},
		{"local", "", []string{"--daemon", "--local"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", tc.configDir)
			withHookStdin(t, map[string]any{"hook_event_name": "SessionStart"})
			if err := executeSyncCmdForTest(t, tc.args...); err == nil || !strings.Contains(err.Error(), "runtime overrides") {
				t.Fatalf("override the running daemon cannot apply was accepted: %v", err)
			}
		})
	}
}
