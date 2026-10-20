package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

func managedEventually(t *testing.T, predicate func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if predicate() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("managed CLI fixture condition not satisfied")
		}
	}
}

func TestManagedDaemonCLIJobAndWholeRecoverySubprocess(t *testing.T) {
	fixture := t.TempDir()
	binary := filepath.Join(fixture, "cctrace")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture CLI: %v %s", err, output)
	}
	home := filepath.Join(fixture, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows reads the home from USERPROFILE
	t.Setenv("CCTRACE_PROFILE", "")
	root := profile.DefaultDir()
	cli := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"sync"}, args...)...)
		cmd.Env = []string{"HOME=" + home, "USERPROFILE=" + home, "PATH=" + os.Getenv("PATH"), "CCTRACE_PROFILE_EMAIL=wrong@fixture.invalid", "CLAUDE_CONFIG_DIR=" + filepath.Join(home, "wrong-claude"), "CODEX_HOME=" + filepath.Join(home, "wrong-codex")}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture sync %v: %v %s", args, err, output)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if running, err := localdaemon.Running(root); err == nil && running {
			if err := localdaemon.Command(ctx, root, "shutdown", ""); err != nil {
				t.Errorf("fixture daemon shutdown: %v", err)
			}
		}
	})
	t.Cleanup(func() {
		if t.Failed() {
			if snap, err := localdaemon.ReadSnapshot(root); err == nil {
				t.Logf("fixture supervisor snapshot: %+v", snap)
			}
			if b, err := os.ReadFile(filepath.Join(root, "sync.crash.log")); err == nil {
				t.Logf("fixture daemon diagnostic: %s", b)
			}
		}
	})
	var mu sync.Mutex
	seen := map[string]int{}
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync" {
			w.WriteHeader(413)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}))
	defer blocked.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync" {
			fmt.Fprint(w, `{}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-two" {
			t.Error("wrong upload token")
		}
		var p syncer.SyncPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p.UserID != "two" || p.ProfileEmail != "two@fixture.invalid" {
			t.Errorf("cross-profile attribution: %s %s", p.UserID, p.ProfileEmail)
		}
		mu.Lock()
		for _, record := range p.Records {
			seen[record.UUID]++
		}
		mu.Unlock()
		fmt.Fprintf(w, `{"inserted":%d}`, len(p.Records))
	}))
	defer healthy.Close()
	source := filepath.Join(home, "source")
	file := filepath.Join(source, "projects", "fixture", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	line := func(id string) string {
		return `{"type":"user","timestamp":"2026-10-12T09:00:00Z","sessionId":"fixture","uuid":"` + id + `","message":{"role":"user","content":"fixture"}}` + "\n"
	}
	first, second := line("one"), line("two")
	if err := os.WriteFile(file, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		p := profile.NewDefault()
		p.ClaudeConfigDir = source
		p.User = profile.UserInfo{ID: name, Name: name, Email: name + "@fixture.invalid", Team: "fixture"}
		p.Server.Endpoint = blocked.URL
		if name == "two" {
			p.Server.Endpoint = healthy.URL
		}
		p.Server.SyncEndpoint = p.Server.Endpoint
		p.Server.AuthToken = "fixture-" + name
		if err := profile.SaveNamed(p, name); err != nil {
			t.Fatal(err)
		}
		dir, _ := profile.NamedDir(name)
		state, err := syncer.LoadState(filepath.Join(dir, "sync-state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := state.Save(); err != nil {
			t.Fatal(err)
		}
	}
	cli("--daemon", "--profile", "one", "--interval", "50ms")
	rt, err := localdaemon.ReadRuntime(root)
	if err != nil {
		t.Fatal(err)
	}
	cli("--daemon", "--profile", "two", "--interval", "50ms")
	managedEventually(t, func() bool {
		snap, err := localdaemon.ReadSnapshot(root)
		if err != nil {
			return false
		}
		one, two := "", ""
		for _, j := range snap.Jobs {
			if j.ID == "named:one" {
				one = j.State
			}
			if j.ID == "named:two" {
				two = j.State
			}
		}
		return one == "blocked" && two == "healthy"
	})
	for _, name := range []string{"one", "two"} {
		profileRT, ok, err := readSyncRuntime(name)
		if err != nil || !ok || profileRT.PID != rt.PID {
			t.Fatalf("profile %s has separate daemon: %+v %v", name, profileRT, err)
		}
	}
	cli("--stop", "--profile", "two")
	if got, err := localdaemon.ReadRuntime(root); err != nil || got.InstanceID != rt.InstanceID {
		t.Fatal("job stop stopped whole daemon")
	}
	if f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600); err != nil {
		t.Fatal(err)
	} else {
		if _, err := f.WriteString(second); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	cli("restart", "--profile", "two")
	managedEventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return seen["one"] == 1 && seen["two"] == 1 })
	cli("restart")
	managedEventually(t, func() bool {
		next, err := localdaemon.ReadRuntime(root)
		return err == nil && next.InstanceID != rt.InstanceID
	})
	dir, _ := profile.NamedDir("two")
	state, err := syncer.LoadState(filepath.Join(dir, "sync-state.json"))
	if err != nil || state.GetOffset(file) != int64(len(first)+len(second)) {
		t.Fatalf("restart lost cursor: %v offset=%d", err, state.GetOffset(file))
	}
	cli("--stop")
	mu.Lock()
	defer mu.Unlock()
	if seen["one"] != 1 || seen["two"] != 1 {
		t.Fatalf("unexpected duplicate forwarding: %v", seen)
	}
	if running, err := localdaemon.Running(root); err != nil || running {
		t.Fatalf("whole stop unconfirmed: %v %v", running, err)
	}
}
