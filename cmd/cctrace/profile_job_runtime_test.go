package main

import (
	"context"
	"path/filepath"
	"testing"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

func TestManagedRuntimeExtensionUsesProfileLeaseLifetime(t *testing.T) {
	home := useTempSyncHome(t)
	p := profile.NewDefault()
	p.ClaudeConfigDir = filepath.Join(home, "source")
	p.Options.SyncEnabled = false
	p.Options.LocalAnalysisEnabled = true
	p.User = profile.UserInfo{ID: "one", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
	if err := profile.SaveNamed(p, "one"); err != nil {
		t.Fatal(err)
	}
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load("one")
	if err != nil {
		t.Fatal(err)
	}
	old := startManagedProfileRuntime
	t.Cleanup(func() { startManagedProfileRuntime = old })
	var runtimeCtx context.Context
	closed := false
	startManagedProfileRuntime = func(ctx context.Context, entry profile.Entry, dir string) (func(), error) {
		if entry.ID != "named:one" || dir != p.ClaudeConfigDir {
			t.Fatal("runtime attribution mismatch")
		}
		runtimeCtx = ctx
		return func() { closed = true }, nil
	}
	passCtx, passCancel := context.WithCancel(context.Background())
	spec := profileJobSpec(entry, daemonOverrides{})
	release, err := spec.Acquire(passCtx, localdaemon.Allocation{Allowed: spec.Sources})
	if err != nil {
		t.Fatal(err)
	}
	passCancel()
	if runtimeCtx.Err() != nil || closed {
		t.Fatal("one pass cancellation stopped the profile runtime lease")
	}
	release()
	if runtimeCtx.Err() == nil || !closed {
		t.Fatal("profile runtime survived its lease")
	}
}
