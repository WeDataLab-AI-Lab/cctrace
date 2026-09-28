package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
)

// setupTestHome redirects HOME to a temp dir and seeds a default profile.
// Returns the temp home dir. The caller must call t.Cleanup via t.TempDir().
func setupTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows uses USERPROFILE instead of HOME

	// Create a minimal default profile.
	p := &profile.Profile{
		Version: 1,
		User: profile.UserInfo{
			ID:    "testuser",
			Name:  "Test User",
			Email: "testuser@example.com",
			Team:  "Engineering",
		},
		Server: profile.ServerInfo{
			Endpoint:     "http://localhost:14317",
			SyncEndpoint: "http://localhost:18080",
			Protocol:     "grpc",
			AuthToken:    "cct_test",
		},
		Options: profile.ProfileOptions{
			SyncEnabled:           true,
			MetricsExportInterval: 60000,
			LogsExportInterval:    5000,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := profile.Save(p); err != nil {
		t.Fatalf("seed default profile: %v", err)
	}
	return home
}

// runProfileCmd executes the profile cobra command with given args and returns stdout output.
func runProfileCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := profileCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// TestProfileAdd_CreatesProfile verifies that add creates profile.json with correct claude_config_dir.
func TestProfileAdd_CreatesProfile(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, ".claude-test")
	if err := os.MkdirAll(claudeHome, 0700); err != nil {
		t.Fatalf("mkdir claude home: %v", err)
	}

	_, err := runProfileCmd(t, "add", "myprofile", "--home", claudeHome)
	if err != nil {
		t.Fatalf("profile add: %v", err)
	}

	p, err := profile.LoadNamed("myprofile")
	if err != nil {
		t.Fatalf("load named profile: %v", err)
	}
	if p.ClaudeConfigDir != claudeHome {
		t.Errorf("ClaudeConfigDir = %q, want %q", p.ClaudeConfigDir, claudeHome)
	}
	if p.User.Email != "testuser@example.com" {
		t.Errorf("user.email = %q, want %q", p.User.Email, "testuser@example.com")
	}
}

// TestProfileAdd_InheritsDefaultServer verifies server settings are copied from default profile.
func TestProfileAdd_InheritsDefaultServer(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, ".claude-test")
	_ = os.MkdirAll(claudeHome, 0700)

	_, err := runProfileCmd(t, "add", "myprofile", "--home", claudeHome)
	if err != nil {
		t.Fatalf("profile add: %v", err)
	}

	p, _ := profile.LoadNamed("myprofile")
	if p.Server.AuthToken != "cct_test" {
		t.Errorf("AuthToken = %q, want %q", p.Server.AuthToken, "cct_test")
	}
	if p.Server.SyncEndpoint != "http://localhost:18080" {
		t.Errorf("SyncEndpoint = %q", p.Server.SyncEndpoint)
	}
}

// TestProfileAdd_RejectsInvalidName verifies validation rejects bad names.
func TestProfileAdd_RejectsInvalidName(t *testing.T) {
	setupTestHome(t)

	_, err := runProfileCmd(t, "add", "invalid name!", "--home", "/tmp/x")
	if err == nil {
		t.Fatal("expected error for invalid profile name, got nil")
	}
}

// TestProfileAdd_RejectsReservedName verifies "default" is rejected.
func TestProfileAdd_RejectsReservedName(t *testing.T) {
	setupTestHome(t)

	_, err := runProfileCmd(t, "add", "default", "--home", "/tmp/x")
	if err == nil {
		t.Fatal("expected error for reserved name 'default'")
	}
}

// TestProfileAdd_RejectsDuplicate verifies adding the same name twice fails.
func TestProfileAdd_RejectsDuplicate(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, ".claude-a")
	_ = os.MkdirAll(claudeHome, 0700)

	if _, err := runProfileCmd(t, "add", "dup", "--home", claudeHome); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, err := runProfileCmd(t, "add", "dup", "--home", claudeHome)
	if err == nil {
		t.Fatal("expected error adding duplicate profile name")
	}
}

// TestProfileAdd_RejectsConflictingHome verifies two profiles cannot share the same Claude home.
func TestProfileAdd_RejectsConflictingHome(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, ".claude-shared")
	_ = os.MkdirAll(claudeHome, 0700)

	if _, err := runProfileCmd(t, "add", "first", "--home", claudeHome); err != nil {
		t.Fatalf("first add: %v", err)
	}
	_, err := runProfileCmd(t, "add", "second", "--home", claudeHome)
	if err == nil {
		t.Fatal("expected error: two profiles with same Claude home")
	}
}

// TestProfileAdd_FailsWithoutDefaultProfile verifies error when no default profile exists.
func TestProfileAdd_FailsWithoutDefaultProfile(t *testing.T) {
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)        // empty home — no default profile
	t.Setenv("USERPROFILE", emptyHome) // Windows

	_, err := runProfileCmd(t, "add", "myprofile", "--home", "/tmp/any")
	if err == nil {
		t.Fatal("expected error when default profile missing")
	}
}

// TestProfileAdd_RequiresHomeFlag verifies --home is required.
func TestProfileAdd_RequiresHomeFlag(t *testing.T) {
	setupTestHome(t)

	_, err := runProfileCmd(t, "add", "myprofile")
	if err == nil {
		t.Fatal("expected error when --home is missing")
	}
}

// TestProfileList_ShowsAddedProfile verifies profile list includes newly added profile.
func TestProfileList_ShowsAddedProfile(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, ".claude-list")
	_ = os.MkdirAll(claudeHome, 0700)

	if _, err := runProfileCmd(t, "add", "listtest", "--home", claudeHome); err != nil {
		t.Fatalf("profile add: %v", err)
	}

	out, err := runProfileCmd(t, "list")
	if err != nil {
		t.Fatalf("profile list: %v", err)
	}
	if !strings.Contains(out, "listtest") {
		t.Errorf("list output missing 'listtest':\n%s", out)
	}
	if !strings.Contains(out, claudeHome) {
		t.Errorf("list output missing claude home path:\n%s", out)
	}
}

// TestProfileList_EmptyWhenNoProfiles verifies list message when no named profiles exist.
func TestProfileList_EmptyWhenNoProfiles(t *testing.T) {
	setupTestHome(t)

	out, err := runProfileCmd(t, "list")
	if err != nil {
		t.Fatalf("profile list: %v", err)
	}
	if !strings.Contains(out, "No named profiles") {
		t.Errorf("expected 'No named profiles', got: %s", out)
	}
}

// TestProfileRemove_DeletesProfile verifies remove eliminates the named profile.
func TestProfileRemove_DeletesProfile(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, ".claude-rm")
	_ = os.MkdirAll(claudeHome, 0700)

	if _, err := runProfileCmd(t, "add", "rmtest", "--home", claudeHome); err != nil {
		t.Fatalf("profile add: %v", err)
	}
	if _, err := runProfileCmd(t, "remove", "--force", "rmtest"); err != nil {
		t.Fatalf("profile remove: %v", err)
	}

	names, _ := profile.ListNamed()
	for _, n := range names {
		if n == "rmtest" {
			t.Error("profile 'rmtest' still present after remove")
		}
	}
}

// TestProfileAdd_WritesValidJSON verifies the written profile.json is valid JSON with expected fields.
func TestProfileAdd_WritesValidJSON(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, ".claude-json")
	_ = os.MkdirAll(claudeHome, 0700)

	if _, err := runProfileCmd(t, "add", "jsontest", "--home", claudeHome); err != nil {
		t.Fatalf("profile add: %v", err)
	}

	path, _ := profile.NamedPath("jsontest")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read profile.json: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if raw["claude_config_dir"] != claudeHome {
		t.Errorf("claude_config_dir = %v, want %q", raw["claude_config_dir"], claudeHome)
	}
	if raw["version"] != float64(1) {
		t.Errorf("version = %v, want 1", raw["version"])
	}
}
