package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

func TestQuotaAccountHomesRequireExplicitNegotiation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s := accountTestService(t, root)
	home := t.TempDir()
	if r := call(t, s, "add", "quota.account.add", map[string]any{"provider": "claude", "label": "Personal", "home": home}); r.Error != nil {
		t.Fatal(r.Error)
	}
	before := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	if _, exists := before["config_home"]; exists {
		t.Fatal("legacy client received local home")
	}
	hello := call(t, s, "homes", "hello", map[string]any{"quota_account_homes": true})
	if hello.Error != nil {
		t.Fatal(hello.Error)
	}
	body, _ := json.Marshal(hello.Result)
	if !strings.Contains(string(body), `"quota_account_homes":true`) {
		t.Fatal("homes capability not advertised")
	}
	for _, payload := range []map[string]any{{"quota_accounts": true}, {"daemon_status": true}, {"daemon_control": true}, {"profile_registry": true}, {}} {
		if r := call(t, s, "peerhello", "hello", payload); r.Error != nil {
			t.Fatal(r.Error)
		}
		row := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
		valid, _ := s.accountHome(home)
		if row["config_home"] != valid {
			t.Fatalf("home lost after hello: %v", row)
		}
	}
	if r := call(t, s, "invalid", "hello", map[string]any{"quota_account_homes": false}); r.Error == nil {
		t.Fatal("false negotiation accepted")
	}
	fresh := accountTestService(t, root)
	legacy := accountListFrom(t, fresh)["accounts"].([]any)[0].(map[string]any)
	if _, exists := legacy["config_home"]; exists {
		t.Fatal("opt-in persisted across helper sessions")
	}
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	invalid := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	if _, exists := invalid["config_home"]; exists {
		t.Fatal("invalid home exported")
	}

}

func TestQuotaAccountHomesUseSharedMetadataHome(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	home := t.TempDir()
	for _, path := range []string{filepath.Join(registry, "profile.json"), filepath.Join(registry, "profiles", "peer", "profile.json")} {
		p := profile.NewDefault()
		p.ClaudeConfigDir = home
		p.Options.SyncEnabled = true
		p.Options.LocalAnalysisEnabled = true
		if err := profile.SaveTo(p, path); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry})
	if err != nil {
		t.Fatal(err)
	}
	if r := call(t, s, "homes", "hello", map[string]any{"quota_account_homes": true}); r.Error != nil {
		t.Fatal(r.Error)
	}
	rows := accountListFrom(t, s)["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows=%v", rows)
	}
	row := rows[0].(map[string]any)
	valid, _ := s.accountHome(home)
	if row["config_home"] != valid || len(row["profiles"].([]any)) != 2 {
		t.Fatalf("metadata=%v", row)
	}
}
