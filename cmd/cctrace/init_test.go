package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

func TestRunCodexPatchUpdatesExistingProfileAndLegacyConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	legacy := `model = "gpt-5"

[otel]
endpoint = "http://old:4317"
headers = { Authorization = "Bearer old-token" }
`
	if err := os.WriteFile(filepath.Join(codexDir, "config.toml"), []byte(legacy), 0600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	p := &profile.Profile{
		Version: 1,
		User: profile.UserInfo{
			ID:    "testuser",
			Name:  "Test User",
			Email: "testuser@example.com",
			Team:  "Engineering",
		},
		Server: profile.ServerInfo{
			Endpoint:     "http://trace.example.com:14317",
			SyncEndpoint: "http://trace.example.com:18080",
			Protocol:     "grpc",
			AuthToken:    "cct_test_token",
		},
		Options: profile.ProfileOptions{
			SyncEnabled:           true,
			MetricsExportInterval: 60000,
			LogsExportInterval:    5000,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := runCodexPatch(p, false, ""); err != nil {
		t.Fatalf("runCodexPatch: %v", err)
	}

	saved, err := profile.Load()
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	if !saved.Options.CodexSyncEnabled {
		t.Fatal("CodexSyncEnabled = false, want true")
	}

	data, err := os.ReadFile(filepath.Join(codexDir, "config.toml"))
	if err != nil {
		t.Fatalf("read codex config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "metrics_exporter") {
		t.Fatal("expected metrics_exporter in Codex config")
	}
	if !strings.Contains(content, "http://trace.example.com:14318/v1/metrics") {
		t.Fatalf("expected converted metrics endpoint, got:\n%s", content)
	}
	if strings.Contains(content, "old-token") || strings.Contains(content, "http://old:4317") {
		t.Fatalf("legacy OTEL values were not replaced:\n%s", content)
	}
}

func TestExpandTildeOnlyExpandsHomeSlashPrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got := expandTilde("~/work/.claude")
	want := filepath.Join(home, "work", ".claude")
	if got != want {
		t.Fatalf("expandTilde = %q, want %q", got, want)
	}

	for _, path := range []string{"~", "~other/.claude", "/tmp/~/.claude"} {
		if got := expandTilde(path); got != path {
			t.Fatalf("expandTilde(%q) = %q, want unchanged", path, got)
		}
	}
}

func TestBackupProfileRestoreFromBackupRestoresDefaultProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	original := &profile.Profile{
		Version: 1,
		User: profile.UserInfo{
			ID:    "original",
			Name:  "Original User",
			Email: "original@example.com",
			Team:  "Engineering",
		},
		Server: profile.ServerInfo{
			Endpoint: "http://trace.example.com:4317",
			Protocol: "grpc",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := profile.Save(original); err != nil {
		t.Fatalf("save original profile: %v", err)
	}

	backupPath, err := backupProfile(false, "")
	if err != nil {
		t.Fatalf("backupProfile: %v", err)
	}

	mutated := *original
	mutated.User.ID = "mutated"
	mutated.User.Email = "mutated@example.com"
	if err := profile.Save(&mutated); err != nil {
		t.Fatalf("save mutated profile: %v", err)
	}

	if err := restoreFromBackup(backupPath, false, ""); err != nil {
		t.Fatalf("restoreFromBackup: %v", err)
	}

	restored, err := profile.Load()
	if err != nil {
		t.Fatalf("load restored profile: %v", err)
	}
	if restored.User.ID != "original" || restored.User.Email != "original@example.com" {
		t.Fatalf("restored profile = %+v, want original identity", restored.User)
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("backup file should be removed after successful restore, stat err=%v", err)
	}
}
