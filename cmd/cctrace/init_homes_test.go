package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

// HOME is process-wide: these CLI onboarding tests cannot run in parallel.
func TestAdditionalHomeSetupSelectedProvidersStayRegistrationOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, item := range []struct{ name, marker string }{{".claude-3", "settings.json"}, {".codex-2", "config.toml"}} {
		dir := filepath.Join(home, item.name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, item.marker), []byte("synthetic marker"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := profile.NewDefault()
	base.User.ID = "synthetic-owner"
	base.Options.CodexSyncEnabled = true
	ir := &inputReader{reader: bufio.NewReader(strings.NewReader("1,2\n"))}
	detectAndOfferAdditionalHomes(ir, base)
	entries, err := (profile.Registry{Root: profile.DefaultDir()}).List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("CLI/registry missing selected providers: %+v %v", entries, err)
	}
	for _, entry := range entries {
		p := entry.Profile
		if !p.RegistrationOnly || p.Options.SyncEnabled || p.Options.CodexSyncEnabled || p.Options.GjcSyncEnabled || p.Options.OmoSyncEnabled || p.Options.LocalAnalysisEnabled || p.User.ID != "" {
			t.Fatalf("registration started collection or copied enrollment: %+v", p)
		}
	}
	for _, item := range []struct{ name, marker string }{{".claude-3", "settings.json"}, {".codex-2", "config.toml"}} {
		b, _ := os.ReadFile(filepath.Join(home, item.name, item.marker))
		if string(b) != "synthetic marker" {
			t.Fatal("registration changed provider settings")
		}
	}
}

// HOME is process-wide: keep environment changes serial.
func TestAdditionalHomeSetupDefaultsToNoSelection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude-3")
	_ = os.Mkdir(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{}"), 0600)
	ir := &inputReader{reader: bufio.NewReader(strings.NewReader("\n"))}
	detectAndOfferAdditionalHomes(ir, profile.NewDefault())
	if entries, err := (profile.Registry{Root: profile.DefaultDir()}).List(); err != nil || len(entries) != 0 {
		t.Fatalf("default selection registered profiles: %+v %v", entries, err)
	}
}

// HOME is process-wide: keep environment changes serial.
func TestAdditionalHomeSetupExplicitExclusionAndRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex-2")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("# marker"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(answer string) {
		detectAndOfferAdditionalHomes(&inputReader{reader: bufio.NewReader(strings.NewReader(answer + "\n"))}, profile.NewDefault())
	}
	registry := profile.Registry{Root: profile.DefaultDir()}
	run("exclude 1")
	found, err := registry.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 1 || !found.Candidates[0].Excluded {
		t.Fatalf("explicit exclusion lost %+v %v", found, err)
	}
	for _, answer := range []string{"all", "1", "none", ""} {
		run(answer)
		entries, err := registry.List()
		if err != nil || len(entries) != 0 {
			t.Fatalf("excluded registered by %q: %+v %v", answer, entries, err)
		}
	}
	run("restore 1")
	found, err = registry.DiscoverHomes(home)
	if err != nil || len(found.Candidates) != 1 || found.Candidates[0].Excluded {
		t.Fatalf("restore failed %+v %v", found, err)
	}
	entries, err := registry.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("restore registered %+v %v", entries, err)
	}
	run("all")
	entries, err = registry.List()
	if err != nil || len(entries) != 1 || !entries[0].Profile.RegistrationOnly || entries[0].Profile.Options.CodexSyncEnabled {
		t.Fatalf("registration failed %+v %v", entries, err)
	}
}
