package main

import (
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

func TestManagedLoaderBlocksUnresolvedSharedMigration(t *testing.T) {
	home := useTempSyncHome(t)
	p := profile.NewDefault()
	p.User = profile.UserInfo{ID: "one", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
	p.Server.Endpoint = "http://127.0.0.1:1"
	p.ClaudeConfigDir = filepath.Join(home, "cli-source")
	if err := profile.SaveNamed(p, "one"); err != nil {
		t.Fatal(err)
	}
	legacy := profile.Registry{Root: filepath.Join(home, "legacy")}
	if _, err := legacy.RegisterMapping("one", filepath.Join(home, "gui-source"), ""); err != nil {
		t.Fatal(err)
	}
	specs, err := loadManagedProfiles(profile.Registry{Root: profile.DefaultDir()}, legacy.Root, daemonOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].ID != "named:one" || specs[0].BlockedReason != "migration_conflict" {
		t.Fatalf("ambiguous migration collected without a decision: %+v", specs)
	}
}

func TestStableProfileIDIsControlOnly(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	spawnSyncProcessFn = func([]string, string) (int, error) { t.Fatal("one-shot stable ID launched daemon"); return 0, nil }
	if err := executeSyncCmdForTest(t, "--profile-id", "default"); err == nil || !strings.Contains(err.Error(), "collection control") {
		t.Fatalf("one-shot silently maps default to named profile: %v", err)
	}
}
