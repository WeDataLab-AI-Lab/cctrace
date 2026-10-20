package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"cctrace/internal/envgen"
	"cctrace/internal/profile"
)

func TestTransmissionConfirmationRestoresAllProvenLinksAndLegacyLocalExclusions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	r := profile.Registry{Root: registry}
	for _, name := range []string{"default", "personal"} {
		home, _ := profile.NormalizeCodexHome(t.TempDir())
		p := profile.NewDefault()
		p.ID = profile.ProfileID(name)
		p.HomesVersion = 1
		p.Homes = []profile.HomeMembership{{Provider: "claude", Home: home, Transmission: "exclude", QuotaEnabled: true}}
		p.Options.SyncEnabled = name == "default"
		p.Options.ExcludeAccounts = []string{"anthropic:acct-fixture", "openai:unrelated"}
		p.User.ID = "fixture"
		p.Server.Endpoint = "https://fixture.invalid"
		path, _ := r.Path(name)
		if err := profile.SaveTo(p, path); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry, accountIdentityReader: func(context.Context, string, string) (string, error) { calls.Add(1); return "acct-fixture", nil }})
	if err != nil {
		t.Fatal(err)
	}
	_ = accountListFrom(t, s)
	s.accountsMu.Lock()
	first := s.accounts.Accounts[0]
	second := s.accounts.Accounts[1]
	first.Aliases = []string{second.Home}
	first.Profiles = append(first.Profiles, second.Profiles...)
	for key, value := range second.Definitions {
		first.Definitions[key] = value
	}
	first.Proof = "synthetic-proven-same-account"
	first.BillingID = "acct-fixture"
	first.Enabled = false
	s.accounts.Accounts = []quotaAccountRecord{first}
	s.accountsMu.Unlock()
	result := call(t, s, "p", "profiles.transmission.preview", map[string]any{"provider": "claude", "home": first.Home, "account_ref": first.Ref, "generation": first.Generation, "excluded": false})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	raw, _ := json.Marshal(result.Result)
	var plan map[string]any
	_ = json.Unmarshal(raw, &plan)
	if calls.Load() != 0 || len(plan["target_homes"].([]any)) != 2 {
		t.Fatal("preview requested provider or missed proven alias")
	}
	if result = call(t, s, "a", "profiles.transmission.apply", map[string]any{"preview_ref": plan["preview_ref"], "confirm": true}); result.Error != nil {
		t.Fatal(result.Error)
	}
	if calls.Load() != 2 {
		t.Fatal("confirmed proof did not recheck every home")
	}
	for _, name := range []string{"default", "personal"} {
		entry, err := r.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Profile.Homes[0].Transmission != "allow" || len(entry.Profile.Options.ExcludeAccounts) != 1 || entry.Profile.Options.ExcludeAccounts[0] != "openai:unrelated" || entry.Profile.CollectionEnabled("claude") != (name == "default") {
			t.Fatalf("independent state wrong for %s", name)
		}
	}
	if s.accounts.Accounts[0].Enabled {
		t.Fatal("transmission restore enabled quota")
	}
}

func TestTransmissionTogglePreservesProviderConfigurationBothDirections(t *testing.T) {
	t.Parallel()
	home, _ := profile.NormalizeCodexHome(t.TempDir())
	registry := filepath.Join(t.TempDir(), "registry")
	p := profile.NewDefault()
	p.ID = "default"
	p.HomesVersion = 1
	p.Homes = []profile.HomeMembership{{Provider: "claude", Home: home, Transmission: "allow", QuotaEnabled: true}}
	p.ClaudeConfigDir = home
	p.User.ID = "fixture"
	p.Server.Endpoint = "https://fixture.invalid"
	p.Server.AuthToken = "synthetic"
	r := profile.Registry{Root: registry}
	path, _ := r.Path("default")
	if err := profile.SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, "settings.json")
	original, _ := os.ReadFile(settings)
	s, err := New(Config{AppSupportRoot: filepath.Join(t.TempDir(), "support"), ProfileRoot: registry})
	if err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []bool{true, false} {
		result := call(t, s, "p", "profiles.transmission.preview", map[string]any{"provider": "claude", "home": home, "excluded": excluded})
		if result.Error != nil {
			t.Fatal(result.Error)
		}
		raw, _ := json.Marshal(result.Result)
		var plan map[string]any
		_ = json.Unmarshal(raw, &plan)
		if len(plan["provider_effects"].([]any)) != 0 {
			t.Fatal("session-only exclusion planned provider changes")
		}
		if result = call(t, s, "a", "profiles.transmission.apply", map[string]any{"preview_ref": plan["preview_ref"], "confirm": true}); result.Error != nil {
			t.Fatal(result.Error)
		}
		actual, _ := os.ReadFile(settings)
		if !bytes.Equal(original, actual) {
			t.Fatal("session-only exclusion changed provider config")
		}
		entry, _ := r.Load("default")
		if !entry.Profile.CollectionEnabled("claude") || entry.Profile.Homes[0].QuotaEnabled != true {
			t.Fatal("exclusion changed independent execution/usage consent")
		}
		specPolicy := entry.Profile.Homes[0].Transmission
		if (specPolicy == "exclude") != excluded {
			t.Fatal("session source policy not applied")
		}
	}
}
