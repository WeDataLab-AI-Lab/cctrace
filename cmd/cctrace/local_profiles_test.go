package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
