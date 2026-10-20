package desktop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/profile"
)

func TestQuotaAccountsExclusionUpdatesAllLinksWithoutChangingQuota(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	os.MkdirAll(registry, 0700)
	home := t.TempDir()
	for _, name := range []string{"one", "two"} {
		dir := filepath.Join(registry, "profiles", name)
		os.MkdirAll(dir, 0700)
		raw := `{"version":1,"user":{"id":"fixture"},"server":{"endpoint":"https://fixture.invalid","future":"preserved"},"options":{"sync_enabled":true,"codex_sync_enabled":false,"future_option":{"secret":"synthetic"}},"claude_config_dir":` + quoteAccountHome(home) + `,"future_root":[1,2]}`
		if e := os.WriteFile(filepath.Join(dir, "profile.json"), []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
	}
	s, e := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry, accountIdentityReader: func(context.Context, string, string) (string, error) { return "acct-one", nil }})
	if e != nil {
		t.Fatal(e)
	}
	a := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	for _, v := range a["profiles"].([]any) {
		if v.(map[string]any)["excluded"] != nil {
			t.Fatal("missing local metadata guessed an exclusion state")
		}
	}
	r := call(t, s, "exclude", "quota.account.exclusion", map[string]any{"account_ref": a["account_ref"], "generation": a["generation"], "excluded": true})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	for _, name := range []string{"one", "two"} {
		b, e := os.ReadFile(filepath.Join(registry, "profiles", name, "profile.json"))
		if e != nil {
			t.Fatal(e)
		}
		var v map[string]any
		json.Unmarshal(b, &v)
		options := v["options"].(map[string]any)
		if options["sync_enabled"] != true || options["future_option"] == nil || v["future_root"] == nil {
			t.Fatalf("unrelated field changed: %s", b)
		}
		ex := options["exclude_accounts"].([]any)
		if len(ex) != 1 || ex[0] != "anthropic:acct-one" {
			t.Fatalf("billing namespace: %v", ex)
		}
	}
	a = accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	if a["enabled"] != false {
		t.Fatal("quota implicitly enabled")
	}
}
func quoteAccountHome(home string) string { b, _ := json.Marshal(home); return string(b) }

func TestQuotaAccountsDisabledLocalExclusionShowsMetadataWithoutProviderRequest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	os.MkdirAll(registry, 0700)
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"acct-local"}}`), 0600)
	p := profile.NewDefault()
	p.ClaudeConfigDir = home
	p.Options.LocalAnalysisEnabled = true
	p.Options.SyncEnabled = true
	p.Options.ExcludeAccounts = []string{"anthropic:acct-local"}
	if e := profile.SaveTo(p, filepath.Join(registry, "profile.json")); e != nil {
		t.Fatal(e)
	}
	s, e := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry, accountReader: func(context.Context, string, string) (accountSample, error) {
		t.Error("OFF account queried provider")
		return accountSample{}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	snapshot := accountListFrom(t, s)
	rows := snapshot["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected one configured account, got %v", snapshot)
	}
	a := rows[0].(map[string]any)
	links := a["profiles"].([]any)
	if a["enabled"] != false || links[0].(map[string]any)["excluded"] != true || a["identity_state"] != "unverified" {
		t.Fatalf("local exclusion guessed or hidden: %v", a)
	}
}

func TestQuotaAccountsDefaultProfileExclusionUsesSameExplicitServiceHome(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	os.MkdirAll(registry, 0700)
	home := t.TempDir()
	p := profile.NewDefault()
	p.Options.SyncEnabled = true
	p.Options.LocalAnalysisEnabled = true
	p.ClaudeConfigDir = ""
	if e := profile.SaveTo(p, filepath.Join(registry, "profile.json")); e != nil {
		t.Fatal(e)
	}
	s, e := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry, DaemonRoot: registry, ProviderMode: true, ClaudeHome: home, accountIdentityReader: func(_ context.Context, provider, got string) (string, error) {
		want, _ := filepath.EvalSymlinks(home)
		if provider != "claude" || got != want {
			t.Error("wrong default source")
		}
		return "acct-default", nil
	}, daemonControlDeps: &daemonControlDependencies{profileDefaultDir: func() string { return registry }}})
	if e != nil {
		t.Fatal(e)
	}
	snapshot := accountListFrom(t, s)
	rows := snapshot["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("default discovery: %v", snapshot)
	}
	row := rows[0].(map[string]any)
	r := call(t, s, "exclude", "quota.account.exclusion", map[string]any{"account_ref": row["account_ref"], "generation": row["generation"], "excluded": true})
	if r.Error != nil {
		t.Fatalf("same default source rejected: %+v", r.Error)
	}
	entry, e := s.registry.Load("default")
	if e != nil || len(entry.Profile.Options.ExcludeAccounts) != 1 || entry.Profile.Options.ExcludeAccounts[0] != "anthropic:acct-default" {
		t.Fatal("default exclusion not persisted")
	}
}

func TestQuotaAccountsPartialExclusionFailureReportsMixedAndRetainsCard(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	os.MkdirAll(registry, 0700)
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"acct-one"}}`), 0600)
	second := ""
	for _, name := range []string{"one", "two"} {
		dir := filepath.Join(registry, "profiles", name)
		os.MkdirAll(dir, 0700)
		p := profile.NewDefault()
		p.ClaudeConfigDir = home
		p.Options.SyncEnabled = true
		p.Options.LocalAnalysisEnabled = true
		path := filepath.Join(dir, "profile.json")
		if e := profile.SaveTo(p, path); e != nil {
			t.Fatal(e)
		}
		if name == "two" {
			second = path
		}
	}
	proofs := 0
	s, e := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry, accountIdentityReader: func(context.Context, string, string) (string, error) {
		proofs++
		if proofs == 2 {
			if e := os.Chmod(second, 0400); e != nil {
				t.Error(e)
			}
		}
		return "acct-one", nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	before := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	r := call(t, s, "exclude", "quota.account.exclusion", map[string]any{"account_ref": before["account_ref"], "generation": before["generation"], "excluded": true})
	if r.Error == nil {
		t.Fatal("partial mutation claimed all success")
	}
	os.Chmod(second, 0600)
	after := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	if after["id"] != before["id"] || after["enabled"] != false {
		t.Fatal("card disappeared or quota enabled")
	}
	included, excluded := 0, 0
	for _, v := range after["profiles"].([]any) {
		state := v.(map[string]any)["excluded"]
		switch state {
		case true:
			excluded++
		case false:
			included++
		}
	}
	if included != 1 || excluded != 1 {
		t.Fatalf("actual mixed state hidden: %v", after)
	}
}

func TestQuotaAccountExclusionFinalStateRejectsContradictionAndHomeChange(t *testing.T) {
	t.Parallel()
	s := accountTestService(t, t.TempDir())
	home, _ := s.accountHome(t.TempDir())
	other := t.TempDir()
	a := quotaAccountRecord{Provider: "claude", Home: home}
	p := profile.NewDefault()
	p.ClaudeConfigDir = home
	p.Options.SyncEnabled = true
	entry := profile.Entry{Name: "one", Profile: p}
	if _, ok := s.checkedAccountExclusion(a, entry, "acct", true); ok {
		t.Fatal("requested exclusion substituted for actual state")
	}
	p.Options.ExcludeAccounts = []string{"anthropic:acct"}
	if state, ok := s.checkedAccountExclusion(a, entry, "acct", true); !ok || !state {
		t.Fatal("actual matching state rejected")
	}
	p.ClaudeConfigDir = other
	if _, ok := s.checkedAccountExclusion(a, entry, "acct", true); ok {
		t.Fatal("changed home accepted after mutation")
	}
}
