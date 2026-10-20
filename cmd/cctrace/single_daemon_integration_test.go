package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/profile"
)

// The test executable runs real CLI watcher logic in isolated subprocesses.
// Only the fixture HOME and local ephemeral server are visible to those children.
func TestSingleDaemonProcessHelper(t *testing.T) {
	if os.Getenv("CCTRACE_SINGLE_DAEMON_HELPER") != "1" {
		return
	}
	version = "dev"
	name := os.Getenv("CCTRACE_SINGLE_DAEMON_PROFILE")
	if err := executeSyncCmdForTest(t, "--watch", "--profile", name, "--interval", "50ms"); err != nil {
		t.Fatal(err)
	}
}

func TestSingleDaemonMultipleProfilesSubprocess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CCTRACE_PROFILE", "")
	t.Setenv("CCTRACE_CODEX_SYNC", "false")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer server.Close()
	for _, name := range []string{"one", "two"} {
		p := profile.NewDefault()
		p.ClaudeConfigDir = filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Join(p.ClaudeConfigDir, "projects"), 0700); err != nil {
			t.Fatal(err)
		}
		p.Server.Endpoint = server.URL
		p.Server.SyncEndpoint = server.URL
		p.User = profile.UserInfo{ID: name, Name: name, Team: "fixture", Email: name + "@fixture.invalid"}
		if err := profile.SaveNamed(p, name); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, name := range []string{"one", "two"} {
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSingleDaemonProcessHelper$")
		child.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CCTRACE_SINGLE_DAEMON_HELPER=1", "CCTRACE_SINGLE_DAEMON_PROFILE=" + name, "CCTRACE_CODEX_SYNC=false"}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cancel(); _ = child.Wait() })
	}
	var pids []int
	for _, name := range []string{"one", "two"} {
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			rt, ok, err := readSyncRuntime(name)
			if err == nil && ok {
				pids = append(pids, rt.PID)
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("profile %s never became active", name)
			}
		}
	}
	if pids[0] != pids[1] {
		t.Fatalf("multiple long-lived local daemons: profile one pid=%d, two pid=%d; want one owner", pids[0], pids[1])
	}
}
