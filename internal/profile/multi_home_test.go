package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMultiHomeRoundTripAndLegacyWriterFence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "profile.json")
	_ = os.Chmod(filepath.Dir(path), 0700)
	home := t.TempDir()
	p := NewDefault()
	raw, _ := json.Marshal(p)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["homes_version"] = 1
	fields["homes"] = []any{map[string]any{"provider": "claude", "home": home, "transmission": "exclude", "quota_enabled": true}}
	raw, _ = json.Marshal(fields)
	_ = json.Unmarshal(raw, p)
	if err := SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(loaded)
	var saved map[string]any
	_ = json.Unmarshal(got, &saved)
	if saved["homes_version"] != float64(1) {
		t.Fatalf("explicit membership lost: %s", got)
	}
	// A legacy mapping API must not overwrite an explicit home list.
	r := Registry{Root: filepath.Dir(path)}
	entry, err := r.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.UpdateMapping("default", entry.Revision, t.TempDir(), ""); err == nil {
		t.Fatal("lossy legacy mapping accepted")
	}
}

func TestMultiHomeRejectsUnknownPolicyAndVersion(t *testing.T) {
	t.Parallel()
	for _, fragment := range []string{`"homes_version":2,"homes":[]`, `"homes_version":1,"homes":[{"provider":"claude","home":"/tmp","transmission":"maybe","quota_enabled":false}]`} {
		var p Profile
		if err := json.Unmarshal([]byte(`{`+fragment+`}`), &p); err != nil {
			continue
		}
		if err := SaveTo(&p, filepath.Join(t.TempDir(), "profile.json")); err == nil {
			t.Fatalf("unsafe schema accepted: %s", fragment)
		}
	}
}

func TestMultiHomeUnknownNestedPolicyCannotBeRewritten(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"homes_version":1,"homes":[],"home_collection":{"claude":null,"codex":false}}`, `{"homes_version":1,"homes":[{"provider":"claude","home":"/synthetic","transmission":"exclude","quota_enabled":false,"future_consent":true}]}`, `{"homes_version":1,"homes":[],"home_collection":{"claude":false,"codex":false,"future_provider":true}}`} {
		path := filepath.Join(t.TempDir(), "profile.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFrom(path); err == nil {
			t.Fatal("unknown nested policy accepted by a lossy writer")
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatal("unsupported policy was rewritten")
		}
	}
}

func TestExplicitCollectionOptInActivatesConfiguredRegistrationWithoutChangingConsent(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "registry")
	r := Registry{Root: root}
	home, _ := NormalizeCodexHome(t.TempDir())
	p := NewDefault()
	p.ID = "named:personal"
	p.RegistrationOnly = true
	p.HomesVersion = 1
	p.Homes = []HomeMembership{{Provider: "claude", Home: home, Transmission: "allow", QuotaEnabled: true}}
	p.HomeCollection = &HomeCollection{}
	p.User.ID = "fixture"
	p.Server.Endpoint = "https://fixture.invalid"
	path, _ := r.Path("personal")
	if err := SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	entry, _ := r.Load("personal")
	if entry.CollectionState != "registered" {
		t.Fatal("registration activated without execution opt-in")
	}
	entry.Profile.SetCollectionEnabled("claude", true)
	if err := SaveTo(entry.Profile, path); err != nil {
		t.Fatal(err)
	}
	active, err := r.Load("personal")
	if err != nil {
		t.Fatal(err)
	}
	if active.CollectionState != "enabled" || active.Profile.Options.SyncEnabled || active.Profile.Options.CodexSyncEnabled || active.Profile.Homes[0].Transmission != "allow" || !active.Profile.Homes[0].QuotaEnabled {
		t.Fatal("execution opt-in was ineffective or changed independent axes")
	}
	active.Profile.SetCollectionEnabled("claude", false)
	if err := SaveTo(active.Profile, path); err != nil {
		t.Fatal(err)
	}
	stopped, _ := r.Load("personal")
	if stopped.CollectionState != "disabled" || stopped.Profile.Homes[0].Transmission != "allow" || !stopped.Profile.Homes[0].QuotaEnabled {
		t.Fatal("execution stop changed consent or membership")
	}
}

func TestExplicitStoredHomesDistinguishMissingNullAndEmpty(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"homes_version":1}`, `{"homes_version":1,"homes":null}`} {
		path := filepath.Join(t.TempDir(), "profile.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFrom(path); err == nil {
			t.Fatalf("ambiguous explicit membership accepted: %s", raw)
		}
	}
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(path, []byte(`{"homes_version":1,"homes":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFrom(path); err != nil {
		t.Fatal("intentional empty membership rejected", err)
	}
}
