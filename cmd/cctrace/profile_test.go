package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/desktop"
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

// Existing legacy definitions remain readable with their original server identity.
func seedLegacyNamedProfile(t *testing.T, name, home string) {
	t.Helper()
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.ClaudeConfigDir = home
	if err = profile.SaveNamed(p, name); err != nil {
		t.Fatal(err)
	}
}
func TestLegacyNamedProfileRemainsReadable(t *testing.T) {
	home := setupTestHome(t)
	source := filepath.Join(home, ".claude-existing")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	seedLegacyNamedProfile(t, "existing", source)
	p, err := profile.LoadNamed("existing")
	if err != nil {
		t.Fatal(err)
	}
	if p.ClaudeConfigDir != source || p.User.Email != "testuser@example.com" || p.Server.AuthToken != "cct_test" || p.Server.SyncEndpoint != "http://localhost:18080" {
		t.Fatal("legacy identity/server compatibility changed")
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

	seedLegacyNamedProfile(t, "dup", claudeHome)
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

	seedLegacyNamedProfile(t, "first", claudeHome)
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

	seedLegacyNamedProfile(t, "listtest", claudeHome)

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

	seedLegacyNamedProfile(t, "rmtest", claudeHome)
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

// Existing stored legacy JSON retains its readable fields and identity.
func TestLegacyNamedProfile_WritesValidJSON(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, ".claude-json")
	_ = os.MkdirAll(claudeHome, 0700)

	seedLegacyNamedProfile(t, "jsontest", claudeHome)

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

func TestProfileRegisterAndEditShareDisabledRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	claude := filepath.Join(home, "provider-claude")
	codex := filepath.Join(home, "provider-codex")
	for _, dir := range []string{claude, codex} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sentinel := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(sentinel, []byte(`{"fixture":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runProfileCmd(t, "register", "gui", "--home", claude, "--codex-home", codex); err != nil {
		t.Fatal(err)
	}
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load("gui")
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "named:gui" || entry.CollectionState != "registered" || entry.Profile.Options.SyncEnabled || entry.Profile.CodexHome != codex {
		t.Fatal("registration changed collection semantics")
	}
	next := filepath.Join(home, "next-claude")
	if err := os.Mkdir(next, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := runProfileCmd(t, "edit", "gui", "--home", next, "--revision", entry.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := runProfileCmd(t, "edit", "gui", "--home", claude, "--revision", entry.Revision); err == nil {
		t.Fatal("stale CLI edit accepted")
	}
	out, err := runProfileCmd(t, "list")
	if err != nil || !strings.Contains(out, "named:gui") || !strings.Contains(out, next) || !strings.Contains(out, "registered") {
		t.Fatalf("shared list=%q err=%v", out, err)
	}
	after, _ := os.ReadFile(sentinel)
	if string(after) != `{"fixture":true}` {
		t.Fatal("registration or edit modified provider settings")
	}
}

func TestProfileCommandsAndDesktopShareBothHomeEdits(t *testing.T) {
	home := setupTestHome(t)
	claude := filepath.Join(home, "claude-fixture")
	codex := filepath.Join(home, "codex-fixture")
	nextClaude := filepath.Join(home, "claude-next")
	nextCodex := filepath.Join(home, "codex-next")
	for _, dir := range []string{claude, codex, nextClaude, nextCodex} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(marker, []byte(`{"fixture":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runProfileCmd(t, "register", "cli", "--home", claude, "--codex-home", codex); err != nil {
		t.Fatal(err)
	}
	svc, err := desktop.New(desktop.Config{AppSupportRoot: filepath.Join(home, "app-support"), ProfileRoot: profile.DefaultDir()})
	if err != nil {
		t.Fatal(err)
	}
	request := func(op string, payload any) desktop.Response {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		response := svc.Handle(context.Background(), desktop.Request{Version: 1, Kind: "request", ID: "fixture", Op: op, Payload: raw})
		if response.Error != nil {
			t.Fatal(response.Error)
		}
		return response
	}
	list := request("profiles.list", map[string]any{})
	raw, _ := json.Marshal(list.Result)
	var mappings struct {
		Profiles []struct {
			Name     string `json:"name"`
			ID       string `json:"id"`
			Revision string `json:"revision"`
			Claude   string `json:"claude_home"`
			Codex    string `json:"codex_home"`
		}
	}
	if err := json.Unmarshal(raw, &mappings); err != nil {
		t.Fatal(err)
	}
	if len(mappings.Profiles) != 2 || mappings.Profiles[1].ID != "named:cli" || mappings.Profiles[1].Claude != claude || mappings.Profiles[1].Codex != codex {
		t.Fatal("actual CLI registration missing from desktop")
	}
	request("profiles.add", map[string]any{"name": "gui", "claude_home": claude, "codex_home": codex})
	output, err := runProfileCmd(t, "list")
	if err != nil || !strings.Contains(output, "named:gui collection=registered") || !strings.Contains(output, "id=default") {
		t.Fatalf("actual CLI list=%s err=%v", output, err)
	}
	if _, err := runProfileCmd(t, "edit", "cli", "--home", nextClaude, "--codex-home", nextCodex, "--revision", mappings.Profiles[1].Revision); err != nil {
		t.Fatal(err)
	}
	list = request("profiles.list", map[string]any{})
	raw, _ = json.Marshal(list.Result)
	if err := json.Unmarshal(raw, &mappings); err != nil {
		t.Fatal(err)
	}
	if mappings.Profiles[1].Claude != nextClaude || mappings.Profiles[1].Codex != nextCodex {
		t.Fatal("actual CLI both-home edit missing from desktop")
	}
	request("profiles.update", map[string]any{"name": "gui", "revision": mappings.Profiles[2].Revision, "claude_home": nextClaude, "codex_home": nextCodex})
	output, err = runProfileCmd(t, "list")
	if err != nil || strings.Count(output, nextCodex) != 2 || strings.Count(output, nextClaude) != 2 {
		t.Fatalf("desktop edit missing from actual CLI list: %s %v", output, err)
	}
	after, _ := os.ReadFile(marker)
	if string(after) != `{"fixture":true}` {
		t.Fatal("shared management touched provider config")
	}
}
