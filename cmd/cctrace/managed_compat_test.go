package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/configtxn"
	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

func TestManagedPolicyRestartsOnlyItsProfileJob(t *testing.T) {
	for _, target := range []string{"one", "default"} {
		t.Run(target, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			root := profile.DefaultDir()
			supervisor := localdaemon.New(localdaemon.Config{Root: root, Refresh: 5 * time.Millisecond, Load: func() ([]localdaemon.Spec, error) {
				var specs []localdaemon.Spec
				for _, name := range []string{target, "two"} {
					specs = append(specs, localdaemon.Spec{ID: profile.ProfileID(name), Name: name, RuntimeDir: root, Enabled: true, Revision: "one", Run: func(_ context.Context, report func(localdaemon.Progress)) error {
						report(localdaemon.Progress{State: "healthy"})
						return nil
					}})
				}
				return specs, nil
			}})
			runtime := startSupervisor(t, supervisor)
			rt := &syncRuntime{PID: runtime.PID, InstanceID: "fixture-instance", StartedAt: time.Now().UTC(), Interval: time.Second, SupervisorRoot: root}
			if err := writeJSONFile(filepath.Join(syncProfileDir(managedLegacyName(target)), "sync-runtime.json"), rt); err != nil {
				t.Fatal(err)
			}
			waitForWatcherExitFn = func(string, string, time.Duration) (bool, error) { return false, nil }
			if err := stopServerPolicyRuntime(context.Background(), managedLegacyName(target)); err != nil {
				t.Fatalf("managed policy stop failed: %v", err)
			}
			for deadline := time.Now().Add(time.Second); ; {
				snapshot, err := localdaemon.ReadSnapshot(root)
				stopped := false
				if err == nil {
					for _, job := range snapshot.Jobs {
						if job.ID == profile.ProfileID(target) && job.State == "stopped" {
							stopped = true
						}
					}
				}
				if stopped {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("policy stop did not stop its target job")
				}
				time.Sleep(time.Millisecond)
			}
			// The supervisor republishes its status every 5ms, and a read that
			// races a rewrite is refused with ErrMetadataChanged; read again.
			snapshot, err := localdaemon.ReadSnapshot(root)
			for i := 0; errors.Is(err, localdaemon.ErrMetadataChanged) && i < 100; i++ {
				snapshot, err = localdaemon.ReadSnapshot(root)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, job := range snapshot.Jobs {
				if job.ID == "named:two" && job.State == "stopped" {
					t.Fatal("policy stopped another profile")
				}
			}
			if err := startServerPolicyRuntime(context.Background(), managedLegacyName(target), rt); err != nil {
				t.Fatalf("managed policy restart failed: %v", err)
			}
			if running, err := localdaemon.Running(root); err != nil || !running {
				t.Fatalf("policy replaced singleton owner: %v", err)
			}
		})
	}
}

func TestManagedProfileRecoversPolicyBeforeCollection(t *testing.T) {
	localTestProfile(t)
	home, err := filepath.EvalSymlinks(os.Getenv("HOME"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	file := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(file, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err = filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := policyPath("", "server-policy-txn")
	if err != nil {
		t.Fatal(err)
	}
	interrupted := false
	func() {
		defer func() { _ = recover() }()
		_ = configtxn.Apply(journal, []configtxn.Change{{Path: file, Before: []byte("old"), BeforeExists: true, After: []byte("new"), Mode: 0600}}, func() error { interrupted = true; panic("interrupted") })
	}()
	data, err := os.ReadFile(file)
	if !interrupted || err != nil || string(data) != "new" {
		t.Fatalf("interrupted transaction witness missing: %q, %v", data, err)
	}
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load("default")
	if err != nil {
		t.Fatal(err)
	}
	release, err := prepareManagedProfile(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	data, err = os.ReadFile(file)
	if err != nil || string(data) != "old" {
		t.Fatalf("collection started before policy recovery: %q, %v", data, err)
	}
}

func TestManagedProfileRunsAndClosesLocalAnalysis(t *testing.T) {
	localTestProfile(t)
	var out bytes.Buffer
	if err := runLocalEnable(&out, "", "http://localhost:3000"); err != nil {
		t.Fatal(err)
	}
	var bootstrap struct{ Endpoint string }
	if err := json.Unmarshal(out.Bytes(), &bootstrap); err != nil {
		t.Fatal(err)
	}
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load("default")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeRuntime, err := startManagedProfileRuntime(ctx, entry, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer closeRuntime()
	address := strings.TrimPrefix(bootstrap.Endpoint, "http://")
	probe, err := net.Listen("tcp", address)
	if err == nil {
		probe.Close()
		t.Fatal("enabled local analysis listener was not started")
	}
	closeRuntime()
	probe, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("profile runtime listener survived release: %v", err)
	}
	probe.Close()
}
