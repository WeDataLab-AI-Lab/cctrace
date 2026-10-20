package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/configtxn"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
)

// These tests replace process-control seams and environment, so cannot run parallel.
func TestServerPolicyRuntimeRollback(t *testing.T) {
	usePolicyRuntimeHome(t)
	oldRead, oldFree, oldStop, oldStart := policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn, policyStartRuntimeFn
	t.Cleanup(func() {
		policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn, policyStartRuntimeFn = oldRead, oldFree, oldStop, oldStart
	})
	policyReadRuntimeFn = func(string) (*syncRuntime, bool, error) {
		return &syncRuntime{PID: 42, InstanceID: "old", Interval: time.Second}, true, nil
	}
	policySyncFreeFn = func(string) (bool, error) { return false, nil }
	stops, starts := 0, 0
	policyStopRuntimeFn = func(context.Context, string) error { stops++; return nil }
	policyStartRuntimeFn = func(context.Context, string, *syncRuntime) error {
		starts++
		if starts == 1 {
			return errors.New("ack failed")
		}
		return nil
	}
	p := filepath.Join(t.TempDir(), "config")
	pdir, e := filepath.EvalSymlinks(filepath.Dir(p))
	if e != nil {
		t.Fatal(e)
	}
	p = filepath.Join(pdir, "config")
	if e := os.WriteFile(p, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	err := applyServerPolicyChanges("", []configtxn.Change{{Path: p, Before: []byte("old"), BeforeExists: true, After: []byte("new"), Mode: 0600}})
	if err == nil {
		t.Fatal("expected restart failure")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "old" || starts != 2 || stops < 2 {
		t.Fatalf("bytes=%s starts=%d stops=%d", b, starts, stops)
	}
}

func TestServerPolicyRuntimeDefersUnsafeRollback(t *testing.T) {
	usePolicyRuntimeHome(t)
	oldRead, oldFree, oldStop, oldStart := policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn, policyStartRuntimeFn
	t.Cleanup(func() {
		policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn, policyStartRuntimeFn = oldRead, oldFree, oldStop, oldStart
	})
	policyReadRuntimeFn = func(string) (*syncRuntime, bool, error) {
		return &syncRuntime{PID: 42, InstanceID: "old", Interval: time.Second}, true, nil
	}
	policySyncFreeFn = func(string) (bool, error) { return false, nil }
	stops := 0
	policyStopRuntimeFn = func(context.Context, string) error {
		stops++
		if stops > 1 {
			return errors.New("still alive")
		}
		return nil
	}
	policyStartRuntimeFn = func(context.Context, string, *syncRuntime) error { return errors.New("ack timeout") }
	d, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "config")
	if e = os.WriteFile(p, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	err := applyServerPolicyChanges("", []configtxn.Change{{Path: p, Before: []byte("old"), BeforeExists: true, After: []byte("new"), Mode: 0600}})
	if !errors.Is(err, configtxn.ErrDeferRecovery) {
		t.Fatalf("expected deferred recovery: %v", err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "new" {
		t.Fatalf("unsafe rollback: %s", b)
	}
}

func TestServerPolicyCoordinatorEligibilityAndCadence(t *testing.T) {
	oldKey, oldSpawn := updatePublicKeyBase64, spawnSyncProcessFn
	t.Cleanup(func() { updatePublicKeyBase64, spawnSyncProcessFn = oldKey, oldSpawn })
	updatePublicKeyBase64 = "trusted-build"
	calls := 0
	spawnSyncProcessFn = func(args []string, name string) (int, error) {
		calls++
		if name != "test" || !hasArg(args, "--automatic") {
			t.Fatalf("bad coordinator args: %v %s", args, name)
		}
		return 1, nil
	}
	for _, c := range []struct {
		watch, dry, local, final bool
		endpoint                 string
	}{{false, false, false, false, ""}, {true, true, false, false, ""}, {true, false, true, false, ""}, {true, false, false, true, ""}, {true, false, false, false, "http://localhost:1"}} {
		var last time.Time
		maybeStartPolicyCoordinator("test", c.watch, c.dry, c.local, c.final, c.endpoint, &last)
	}
	if calls != 0 {
		t.Fatalf("ineligible calls=%d", calls)
	}
	var last time.Time
	maybeStartPolicyCoordinator("test", true, false, false, false, "", &last)
	maybeStartPolicyCoordinator("test", true, false, false, false, "", &last)
	if calls != 1 {
		t.Fatalf("cadence calls=%d", calls)
	}
	last = time.Now().Add(-6 * time.Minute)
	maybeStartPolicyCoordinator("test", true, false, false, false, "", &last)
	updatePublicKeyBase64 = ""
	last = time.Time{}
	maybeStartPolicyCoordinator("test", true, false, false, false, "", &last)
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestServerPolicyStartPreservesRuntimeArguments(t *testing.T) {
	usePolicyRuntimeHome(t)
	oldSpawn, oldAck := spawnSyncProcessFn, waitForSyncStartAckFn
	t.Cleanup(func() { spawnSyncProcessFn, waitForSyncStartAckFn = oldSpawn, oldAck })
	rt := &syncRuntime{Interval: 17 * time.Second, ClaudeDir: "/isolated/claude", ProfileEmail: "tester@example.test"}
	if e := writePolicyHandoff("", &policyHandoff{Runtime: rt}); e != nil {
		t.Fatal(e)
	}
	spawnSyncProcessFn = func(args []string, name string) (int, error) {
		for _, want := range []string{"--policy-handoff", "17s", "/isolated/claude", "tester@example.test"} {
			if !hasArg(args, want) {
				t.Fatalf("missing %s: %v", want, args)
			}
		}
		return 42, nil
	}
	waitForSyncStartAckFn = func(string, time.Duration) (*syncStartAck, error) {
		return &syncStartAck{Status: syncStartAckStarted, InstanceID: "new"}, nil
	}
	if e := startServerPolicyRuntime(context.Background(), "", rt); e != nil {
		t.Fatal(e)
	}
	h, e := readPolicyHandoff("")
	if e != nil || h.AckPath == "" {
		t.Fatalf("intent %#v %v", h, e)
	}
	release, e := policySyncStartupBarrier(context.Background(), "", h.AckPath, true)
	if e != nil {
		t.Fatal(e)
	}
	release()
	h.AckPath = ""
	if e = writePolicyHandoff("", h); e != nil {
		t.Fatal(e)
	}
	if release, e = policySyncStartupBarrier(context.Background(), "", "old-ack", true); e == nil {
		release()
		t.Fatal("accepted cancelled handoff")
	}
}

func TestServerPolicyRuntimeCrashRecovery(t *testing.T) {
	usePolicyRuntimeHome(t)
	oldStop, oldStart := policyStopRuntimeFn, policyStartRuntimeFn
	t.Cleanup(func() { policyStopRuntimeFn, policyStartRuntimeFn = oldStop, oldStart })
	d, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "config")
	if e = os.WriteFile(p, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	rt := &syncRuntime{Interval: time.Second, InstanceID: "old"}
	if e = writePolicyHandoff("", &policyHandoff{Runtime: rt, AckPath: "pending"}); e != nil {
		t.Fatal(e)
	}
	j, e := policyPath("", "server-policy-txn")
	if e != nil {
		t.Fatal(e)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected simulated interruption")
			}
		}()
		_ = configtxn.Apply(j, []configtxn.Change{{Path: p, Before: []byte("old"), BeforeExists: true, After: []byte("new"), Mode: 0600}}, func() error { panic("coordinator interrupted") })
	}()
	stopped, started := false, false
	policyStopRuntimeFn = func(context.Context, string) error { stopped = true; return nil }
	policyStartRuntimeFn = func(context.Context, string, *syncRuntime) error {
		if !stopped {
			t.Fatal("restart before stop")
		}
		b, _ := os.ReadFile(p)
		if string(b) != "old" {
			t.Fatalf("restart before rollback: %s", b)
		}
		started = true
		return nil
	}
	if e = recoverServerPolicyChanges(context.Background(), ""); e != nil {
		t.Fatal(e)
	}
	if !started {
		t.Fatal("old sync not restarted")
	}
	h, e := readPolicyHandoff("")
	if e != nil || !h.Done {
		t.Fatalf("handoff incomplete: %#v %v", h, e)
	}
}

func TestServerPolicyRuntimeStopFailureLeavesOriginal(t *testing.T) {
	usePolicyRuntimeHome(t)
	oldRead, oldFree, oldStop := policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn
	t.Cleanup(func() { policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn = oldRead, oldFree, oldStop })
	policyReadRuntimeFn = func(string) (*syncRuntime, bool, error) { return &syncRuntime{Interval: time.Second}, true, nil }
	policySyncFreeFn = func(string) (bool, error) { return false, nil }
	policyStopRuntimeFn = func(context.Context, string) error { return errors.New("shutdown failed") }
	d, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "config")
	if e = os.WriteFile(p, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = applyServerPolicyChanges("", []configtxn.Change{{Path: p, Before: []byte("old"), BeforeExists: true, After: []byte("new"), Mode: 0600}}); e == nil {
		t.Fatal("expected stop failure")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "old" {
		t.Fatalf("changed before stop: %s", b)
	}
}

func TestServerPolicyNormalStartupRecoversUnwatchedTransaction(t *testing.T) {
	usePolicyRuntimeHome(t)
	d, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(d, "config")
	if e = os.WriteFile(p, []byte("old"), 0600); e != nil {
		t.Fatal(e)
	}
	j, e := policyPath("", "server-policy-txn")
	if e != nil {
		t.Fatal(e)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected interruption")
			}
		}()
		_ = configtxn.Apply(j, []configtxn.Change{{Path: p, Before: []byte("old"), BeforeExists: true, After: []byte("new"), Mode: 0600}}, func() error { panic("interrupted") })
	}()
	release, e := policySyncStartupBarrier(context.Background(), "", "", false)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	b, _ := os.ReadFile(p)
	if string(b) != "old" {
		t.Fatalf("startup retained partial config: %s", b)
	}
}

func usePolicyRuntimeHome(t *testing.T) {
	t.Helper()
	useTempSyncHome(t)
	p := profile.NewDefault()
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	path, err := envgen.ClaudeSettingsPath(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestServerPolicyRuntimeRejectsClaudeDirectoryOverride(t *testing.T) {
	usePolicyRuntimeHome(t)
	oldRead, oldFree := policyReadRuntimeFn, policySyncFreeFn
	t.Cleanup(func() { policyReadRuntimeFn, policySyncFreeFn = oldRead, oldFree })
	policyReadRuntimeFn = func(string) (*syncRuntime, bool, error) {
		return &syncRuntime{Interval: time.Second, ClaudeDir: filepath.Join(t.TempDir(), "override")}, true, nil
	}
	policySyncFreeFn = func(string) (bool, error) { return false, nil }
	if e := applyServerPolicyChanges("", nil); e == nil || !strings.Contains(e.Error(), "different Claude directory") {
		t.Fatalf("expected override refusal, got %v", e)
	}
}

// A handover restarts the daemon with the Claude directory it was started on, so
// the directory must be the one this profile's policy rewrites. A case-insensitive
// disk names one directory twice (~/.claude, ~/.CLAUDE); comparing the names
// refused a handover that would have worked. policyStatFn stands in for that disk.
func TestRuntimeClaudeDirMatches(t *testing.T) {
	dir := t.TempDir()
	claude, other := filepath.Join(dir, "claude"), filepath.Join(dir, "other")
	for _, d := range []string{claude, other} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "claude-link")
	if err := os.Symlink(claude, link); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "CLAUDE")
	original := policyStatFn
	t.Cleanup(func() { policyStatFn = original })
	policyStatFn = func(path string) (os.FileInfo, error) {
		if path == alias {
			path = claude
		}
		return os.Stat(path)
	}
	p := profile.NewDefault()
	p.ClaudeConfigDir = claude
	for _, tc := range []struct {
		name      string
		claudeDir string
		want      bool
	}{
		{"unrecorded", "", true},
		{"same directory", claude, true},
		{"case alias of the directory", alias, true},
		{"link to the directory", link, true},
		{"other directory", other, false},
		{"directory that does not exist", filepath.Join(dir, "gone"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runtimeClaudeDirMatches(p, &syncRuntime{ClaudeDir: tc.claudeDir})
			if err != nil || got != tc.want {
				t.Fatalf("runtimeClaudeDirMatches = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	t.Run("both directories do not exist", func(t *testing.T) {
		gone := filepath.Join(dir, "gone")
		p := profile.NewDefault()
		p.ClaudeConfigDir = gone
		for claudeDir, want := range map[string]bool{gone: true, filepath.Join(dir, "other-gone"): false} {
			got, err := runtimeClaudeDirMatches(p, &syncRuntime{ClaudeDir: claudeDir})
			if err != nil || got != want {
				t.Errorf("%s: runtimeClaudeDirMatches = %v, %v; want %v", claudeDir, got, err, want)
			}
		}
	})
}

func TestPolicyHandoffArgumentsAreRemovedAfterStartup(t *testing.T) {
	args := []string{"cctrace", "sync", "--watch", "--policy-handoff", "--interval", "17s", "--start-ack-file", "/tmp/old-ack", "--profile", "work", "--log-to-file"}
	want := []string{"cctrace", "sync", "--watch", "--interval", "17s", "--profile", "work", "--log-to-file"}
	if got := withoutPolicyHandoffArgs(args); !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%v want %v", got, want)
	}
}

func TestServerPolicyDiscoveryWithoutWatch(t *testing.T) {
	// Process seams and profile environment are global to this package.
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("session_sync_%t", enabled), func(t *testing.T) {
			usePolicyRuntimeHome(t)
			oldKey, oldSpawn := updatePublicKeyBase64, spawnSyncProcessFn
			t.Cleanup(func() { updatePublicKeyBase64, spawnSyncProcessFn = oldKey, oldSpawn })
			updatePublicKeyBase64 = "trusted-build"
			calls := 0
			spawnSyncProcessFn = func(args []string, name string) (int, error) {
				if !hasArg(args, "--automatic") {
					t.Fatalf("unexpected process: %v", args)
				}
				free, err := isSyncLockFree(name)
				if err != nil || !free {
					t.Fatalf("coordinator launched before one-shot released sync lock: %v", err)
				}
				calls++
				return 42, nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()
			p, err := profile.Load()
			if err != nil {
				t.Fatal(err)
			}
			p.Server.Endpoint = server.URL
			p.Server.SyncEndpoint = server.URL
			p.Server.AuthToken = "fixture"
			p.Options.SyncEnabled = enabled
			p.Options.CodexSyncEnabled = true
			p.Options.CodexSyncDeclined = true
			if e := profile.Save(p); e != nil {
				t.Fatal(e)
			}
			if e := runSync(false, "", false, false, false, false, time.Second, "", "", "", false, "", false); e != nil {
				t.Fatal(e)
			}
			if calls != 1 {
				t.Fatalf("policy coordinator calls=%d, want1", calls)
			}
			stored, e := profile.Load()
			if e != nil {
				t.Fatal(e)
			}
			if stored.Options.SyncEnabled != enabled {
				t.Fatal("policy discovery changed session-sync preference")
			}
		})
	}
}

func TestServerPolicyRecoveryHoldsOrdinaryClaudeSettingsLock(t *testing.T) {
	usePolicyRuntimeHome(t)
	p, e := profile.Load()
	if e != nil {
		t.Fatal(e)
	}
	path, e := envgen.ClaudeSettingsPath(p)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte(`{"userPreference":"preserved"}`), 0600); e != nil {
		t.Fatal(e)
	}
	writerDone := make(chan error, 1)
	e = withPolicyRecoverySettingsLock(context.Background(), "", func() error {
		contender := flock.New(path + ".lock")
		acquired, err := contender.TryLock()
		if err != nil {
			return err
		}
		if acquired {
			_ = contender.Unlock()
			return errors.New("recovery did not hold settings lock")
		}
		go func() { writerDone <- envgen.ApplyToClaudeSettings(p) }()
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	select {
	case e = <-writerDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary writer did not resume after recovery unlocked")
	}
	b, e := os.ReadFile(path)
	if e != nil || !strings.Contains(string(b), "preserved") {
		t.Fatalf("settings=%s err=%v", b, e)
	}
}

// A policy moves a profile whose sync is an old stand-alone watcher by stopping it
// and starting a replacement. When the replacement answers "already running" and
// no local collection daemon runs, some other watcher holds the profile and still
// has the old settings in memory, so the start is a failure: the move is rolled
// back, the settings stay as they were, and nothing is recorded as applied.
func TestServerPolicyReplacementRestartRollsBackWhenAnotherWatcherHoldsTheProfile(t *testing.T) {
	pol, p := policyStateFixture(t, false)
	resetSyncControlHooks(t)
	oldRead, oldFree, oldStop := policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn
	t.Cleanup(func() { policyReadRuntimeFn, policySyncFreeFn, policyStopRuntimeFn = oldRead, oldFree, oldStop })
	policySyncFreeFn = func(string) (bool, error) { return false, nil }
	oldSupervisor := policySupervisorRunningFn
	t.Cleanup(func() { policySupervisorRunningFn = oldSupervisor })
	policySupervisorRunningFn = func() bool { return false }
	watcher := &syncRuntime{PID: 1, InstanceID: "stand-alone-watcher", Interval: time.Second}
	policyReadRuntimeFn = func(string) (*syncRuntime, bool, error) { return watcher, true, nil }
	stops := 0
	policyStopRuntimeFn = func(context.Context, string) error { stops++; return nil }
	spawnSyncProcessFn = func([]string, string) (int, error) { return 42, nil }
	waitForSyncStartAckFn = func(string, time.Duration) (*syncStartAck, error) {
		return &syncStartAck{Status: syncStartAckAlreadyRunning}, nil
	}
	settings, err := envgen.ClaudeSettingsPath(p)
	if err != nil {
		t.Fatal(err)
	}
	settingsBefore, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}

	_, runErr := runServerPolicy(context.Background(), "", false)

	if runErr == nil || !strings.Contains(runErr.Error(), syncStartAckAlreadyRunning) {
		t.Fatalf("run ended with %v, want the replacement's already-running answer", runErr)
	}
	if stops == 0 {
		t.Fatal("the old watcher was never stopped")
	}
	if after, err := os.ReadFile(settings); err != nil || string(after) != string(settingsBefore) {
		t.Fatalf("the Claude settings changed though the move was rolled back: %v", err)
	}
	moved, err := loadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Server.Endpoint != p.Server.Endpoint || moved.Server.SyncEndpoint != p.Server.SyncEndpoint {
		t.Fatalf("profile now at %q %q, want it back at %q %q", moved.Server.Endpoint, moved.Server.SyncEndpoint, p.Server.Endpoint, p.Server.SyncEndpoint)
	}
	if got := readStateSequence(t); got == pol.Sequence {
		t.Fatalf("applied sequence %d for a move that was rolled back", got)
	}
}
