package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

func integrationProfile(t *testing.T) (*profile.Profile, []string, string) {
	t.Helper()
	p := profile.NewDefault()
	p.ID = "default"
	p.HomesVersion = 1
	p.HomeCollection = &profile.HomeCollection{Claude: true, Codex: true}
	p.User.ID = "fixture"
	p.Server.Endpoint = "https://fixture.invalid"
	p.Server.AuthToken = "synthetic"
	homes := []string{}
	for _, provider := range []string{"claude", "codex"} {
		for _, permission := range []string{"allow", "exclude"} {
			home, _ := profile.NormalizeCodexHome(t.TempDir())
			homes = append(homes, home)
			p.Homes = append(p.Homes, profile.HomeMembership{Provider: provider, Home: home, Transmission: permission})
		}
	}
	path := filepath.Join(t.TempDir(), "registry", "profile.json")
	if err := profile.SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	return p, homes, path
}
func TestConfirmedInitIntegrationsOnlyConfigureAllowedCollectingHomes(t *testing.T) {
	t.Parallel()
	p, homes, _ := integrationProfile(t)
	if err := applyConfirmedHomeIntegrations(p); err != nil {
		t.Fatal(err)
	}
	claude, err := os.ReadFile(filepath.Join(homes[0], "settings.json"))
	if err != nil || !strings.Contains(string(claude), "SessionStart") {
		t.Fatal("allowed collecting Claude home lost hooks/exporter", err)
	}
	codex, err := os.ReadFile(filepath.Join(homes[2], "config.toml"))
	if err != nil || !strings.Contains(string(codex), "metrics_exporter") {
		t.Fatal("allowed collecting Codex home lost exporter", err)
	}
	for _, path := range []string{filepath.Join(homes[1], "settings.json"), filepath.Join(homes[3], "config.toml")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("excluded home received integration")
		}
	}
}
func TestConfirmedInitIntegrationsRejectStaleDefinitionAndDoNotConfigureStoppedHomes(t *testing.T) {
	t.Parallel()
	p, homes, path := integrationProfile(t)
	p.SetCollectionEnabled("claude", false)
	p.SetCollectionEnabled("codex", false)

	if err := profile.SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	if err := applyConfirmedHomeIntegrations(p); err != nil {
		t.Fatal(err)
	}
	for i, home := range homes {
		name := "settings.json"
		if i >= 2 {
			name = "config.toml"
		}
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatal("stopped personal home configured")
		}
	}
	stale, err := profile.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := profile.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	current.SetCollectionEnabled("claude", true)
	if err = profile.SaveTo(current, path); err != nil {
		t.Fatal(err)
	}
	if err = applyConfirmedHomeIntegrations(stale); !errors.Is(err, profile.ErrConflict) {
		t.Fatalf("stale setup definition was applied: %v", err)
	}
}
