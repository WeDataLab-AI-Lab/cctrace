package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

func TestLocalProfileCatalogUsesOnlySyntheticExactAccountIDs(t *testing.T) {
	home := setupTestHome(t)
	claudeHome := filepath.Join(home, ".claude")
	if err := os.Mkdir(claudeHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"claude-acct-fixture","emailAddress":"private@example.test"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(home, "codex-work")
	if err := os.Mkdir(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"tokens":{"account_id":"openai-acct-fixture","access_token":"fake-secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.CodexHome, err = profile.NormalizeCodexHome(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	catalog := localProfileCatalog()
	if len(catalog) != 2 {
		t.Fatalf("catalog = %+v, want Claude and Codex profile entries", catalog)
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{home, "private@example.test", "fake-secret", "auth.json"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("local profile catalog leaked %q: %s", forbidden, data)
		}
	}
	if !strings.Contains(string(data), "claude-acct-fixture") || !strings.Contains(string(data), "openai-acct-fixture") {
		t.Fatalf("catalog misses exact local account ids: %s", data)
	}
}

func TestProfileAddDoesNotInheritDefaultCodexHome(t *testing.T) {
	home := setupTestHome(t)
	base, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	base.CodexHome, err = profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.Save(base); err != nil {
		t.Fatal(err)
	}
	claudeHome := filepath.Join(home, "claude-team")
	codexHome := filepath.Join(home, "codex-team")
	for _, dir := range []string{claudeHome, codexHome} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "claude-only", "--home", claudeHome}, {"add", "with-codex", "--home", claudeHome + "-2", "--codex-home", codexHome}} {
		if _, err := runProfileCmd(t, args...); err == nil || !strings.Contains(err.Error(), "onboard") {
			t.Fatal("new home bypassed consent", err)
		}
	}
	if names, _ := profile.ListNamed(); len(names) != 0 {
		t.Fatal("a default execution home was inherited without enrollment")
	}
	preserved, err := profile.Load()
	if err != nil || preserved.CodexHome != base.CodexHome {
		t.Fatal("default execution home changed")
	}

}

// local serve, which the dashboard's local-session view reads, keeps the home
// rules it had before multi-home profiles: a profile's legacy single-home
// fields only. The dependency audit reads every home a profile registers,
// whatever its transmission choice, and says which choice that was; a home two
// profiles register differently is "mixed".
func TestLocalServeKeepsLegacyHomesWhileTheAuditReadsEveryMembership(t *testing.T) {
	// Not parallel: the profiles live under HOME, set with t.Setenv.
	setupTestHome(t)
	root := t.TempDir()
	dir := func(name string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		canonical, err := profile.NormalizeCodexHome(path)
		if err != nil {
			t.Fatal(err)
		}
		return canonical
	}
	claudeAllow, claudeExclude, claudeLegacy := dir("claude-work"), dir("claude-team"), dir("claude-old")
	codexAllow, codexUndecided := dir("codex-work"), dir("codex-team")
	multi := profile.NewDefault()
	multi.HomesVersion = 1
	multi.Homes = []profile.HomeMembership{
		{Provider: "claude", Home: claudeAllow, Transmission: "allow"},
		{Provider: "claude", Home: claudeExclude, Transmission: "exclude"},
		{Provider: "codex", Home: codexAllow, Transmission: "allow"},
		{Provider: "codex", Home: codexUndecided, Transmission: "undecided"},
	}
	multi.ClaudeConfigDir = claudeLegacy
	if err := profile.SaveNamed(multi, "multi"); err != nil {
		t.Fatal(err)
	}
	other := profile.NewDefault()
	other.HomesVersion = 1
	other.Homes = []profile.HomeMembership{{Provider: "codex", Home: codexAllow, Transmission: "exclude"}}
	if err := profile.SaveNamed(other, "other"); err != nil {
		t.Fatal(err)
	}
	if got := localProfileHomes("claude"); !slices.Equal(got, []string{claudeLegacy}) {
		t.Errorf("local serve claude homes = %v, want only the legacy field %v", got, claudeLegacy)
	}
	if got := localProfileHomes("codex"); len(got) != 0 {
		t.Errorf("local serve codex homes = %v, want none", got)
	}
	if got, want := dependencyProfileHomes("claude"), []dependencyHome{{claudeAllow, "allow"}, {claudeExclude, "exclude"}}; !slices.Equal(got, want) {
		t.Errorf("audit claude homes = %v, want %v", got, want)
	}
	if got, want := dependencyProfileHomes("codex"), []dependencyHome{{codexAllow, "mixed"}, {codexUndecided, "undecided"}}; !slices.Equal(got, want) {
		t.Errorf("audit codex homes = %v, want %v", got, want)
	}
}

func TestLocalProfileHomesDoNotPromoteCodexScanDirs(t *testing.T) {
	setupTestHome(t)
	explicit := filepath.Join(t.TempDir(), "codex-home")
	scanOnly := t.TempDir()
	if err := os.Mkdir(explicit, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, err := profile.NormalizeCodexHome(explicit)
	if err != nil {
		t.Fatal(err)
	}
	work := profile.NewDefault()
	work.CodexHome = canonical
	work.Options.CodexDirs = []string{scanOnly}
	if err := profile.SaveNamed(work, "work"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	got := localProfileHomes("codex")
	if len(got) != 1 || got[0] != canonical {
		t.Fatalf("localProfileHomes(codex) = %v, want only explicit CodexHome %q", got, canonical)
	}
}
