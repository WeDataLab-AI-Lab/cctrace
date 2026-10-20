package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/envgen"
	"cctrace/internal/profile"
)

func TestHomeOnboardingSelectedMigrationKeepsAccountIDsAndRetiresSafely(t *testing.T) {
	t.Parallel()
	base, _ := profile.NormalizeCodexHome(t.TempDir())
	registry := filepath.Join(t.TempDir(), "registry")
	homes := []string{filepath.Join(base, ".claude"), filepath.Join(base, ".claude-2"), filepath.Join(base, ".claude-backups")}
	for _, home := range homes {
		if err := os.Mkdir(home, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"synthetic":"auth-untouched"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := profile.Registry{Root: registry}
	for i, name := range []string{"default", "legacy"} {
		p := profile.NewDefault()
		p.ID = profile.ProfileID(name)
		p.ClaudeConfigDir = homes[i]
		p.User.ID = "fixture"
		p.Server.Endpoint = "https://fixture.invalid"
		p.Server.AuthToken = "synthetic"
		path, _ := r.Path(name)
		if err := profile.SaveTo(p, path); err != nil {
			t.Fatal(err)
		}
		if err := envgen.ApplyToClaudeSettings(p); err != nil {
			t.Fatal(err)
		}
	}
	legacyPath, _ := r.Path("legacy")
	if err := os.WriteFile(filepath.Join(filepath.Dir(legacyPath), "sync-state.json"), []byte("cursor-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{AppSupportRoot: filepath.Join(t.TempDir(), "support"), ProfileRoot: registry, homeDiscoveryRoot: base})
	if err != nil {
		t.Fatal(err)
	}
	before := accountListFrom(t, s)["accounts"].([]any)
	id := ""
	for _, row := range before {
		v := row.(map[string]any)
		if v["label"] == "legacy" {
			id = v["id"].(string)
		}
	}
	if id == "" {
		t.Fatal("legacy account absent")
	}
	discovery := call(t, s, "d", "profiles.onboarding.discover", map[string]any{})
	if discovery.Error != nil {
		t.Fatal(discovery.Error)
	}
	raw, _ := json.Marshal(discovery.Result)
	var found map[string]any
	_ = json.Unmarshal(raw, &found)
	decisions := []map[string]any{{"provider": "claude", "home": homes[0], "transmission": "allow", "quota_enabled": true}, {"provider": "claude", "home": homes[1], "transmission": "exclude", "quota_enabled": true}}
	preview := call(t, s, "p", "profiles.onboarding.preview", map[string]any{"discovery_ref": found["discovery_ref"], "decisions": decisions})
	if preview.Error != nil {
		t.Fatal(preview.Error)
	}
	raw, _ = json.Marshal(preview.Result)
	var plan map[string]any
	_ = json.Unmarshal(raw, &plan)
	if result := call(t, s, "a", "profiles.onboarding.apply", map[string]any{"preview_ref": plan["preview_ref"], "confirm": true}); result.Error != nil {
		t.Fatal(result.Error)
	}
	entries, err := r.List()
	if err != nil || len(entries) != 2 || entries[0].Name != "default" || entries[1].Name != "personal" {
		t.Fatalf("final groups=%+v %v", entries, err)
	}
	if entries[1].Profile.CollectionEnabled("claude") {
		t.Fatal("personal collector enabled")
	}
	rows := accountListFrom(t, s)["accounts"].([]any)
	retained := false
	for _, row := range rows {
		v := row.(map[string]any)
		if v["id"] == id {
			retained = true
			if v["enabled"] != true {
				t.Fatal("confirmed opt-in ignored for existing row")
			}
			links := v["profiles"].([]any)
			if len(links) != 1 || links[0].(map[string]any)["collecting"] != false {
				t.Fatalf("personal collection not stopped: %v", links)
			}
		}
	}
	if !retained {
		t.Fatal("migration replaced account ID")
	}
	for _, home := range homes {
		auth, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
		if string(auth) != `{"synthetic":"auth-untouched"}` {
			t.Fatal("provider auth changed")
		}
	}
	settings, _ := os.ReadFile(filepath.Join(homes[1], "settings.json"))
	var personal map[string]any
	_ = json.Unmarshal(settings, &personal)
	if env, ok := personal["env"].(map[string]any); ok && env["CLAUDE_CODE_ENABLE_TELEMETRY"] != nil {
		t.Fatal("personal direct telemetry remains managed")
	}
	discovered, err := r.DiscoverHomes(base)
	if err != nil || len(discovered.Candidates) != 1 || discovered.Candidates[0].Home != homes[2] {
		t.Fatalf("unselected home enrolled: %+v %v", discovered, err)
	}
	// A later scoped enrollment must not replay old enrollment consent over a
	// current account-level opt-out on an unrelated existing home.
	current := accountListFrom(t, s)["accounts"].([]any)
	defaultID := ""
	for _, row := range current {
		v := row.(map[string]any)
		for _, link := range v["profiles"].([]any) {
			if link.(map[string]any)["id"] == "default" {
				defaultID = v["id"].(string)
				if result := call(t, s, "off", "quota.account.set", map[string]any{"account_ref": v["account_ref"], "generation": v["generation"], "enabled": false}); result.Error != nil {
					t.Fatal(result.Error)
				}
			}
		}
	}
	next := call(t, s, "next", "profiles.onboarding.discover", map[string]any{})
	raw, _ = json.Marshal(next.Result)
	_ = json.Unmarshal(raw, &found)
	nextPlan := call(t, s, "nextp", "profiles.onboarding.preview", map[string]any{"discovery_ref": found["discovery_ref"], "decisions": []map[string]any{{"provider": "claude", "home": homes[2], "transmission": "exclude", "quota_enabled": true}}})
	if nextPlan.Error != nil {
		t.Fatal(nextPlan.Error)
	}
	raw, _ = json.Marshal(nextPlan.Result)
	_ = json.Unmarshal(raw, &plan)
	if result := call(t, s, "nexta", "profiles.onboarding.apply", map[string]any{"preview_ref": plan["preview_ref"], "confirm": true}); result.Error != nil {
		t.Fatal(result.Error)
	}
	for _, row := range accountListFrom(t, s)["accounts"].([]any) {
		v := row.(map[string]any)
		if v["id"] == defaultID && v["enabled"] != false {
			t.Fatal("scoped onboarding re-enabled unrelated opt-out")
		}
	}

}

func TestMalformedExplicitHomeCensusKeepsKnownAccountLinks(t *testing.T) {
	t.Parallel()
	registry := filepath.Join(t.TempDir(), "registry")
	home, _ := profile.NormalizeCodexHome(t.TempDir())
	p := profile.NewDefault()
	p.HomesVersion = 1
	p.Homes = []profile.HomeMembership{{Provider: "claude", Home: home, Transmission: "exclude"}}
	p.Options.SyncEnabled = false
	path := filepath.Join(registry, "profile.json")
	if err := profile.SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{AppSupportRoot: filepath.Join(t.TempDir(), "support"), ProfileRoot: registry})
	if err != nil {
		t.Fatal(err)
	}
	before := accountListFrom(t, s)["accounts"].([]any)[0].(map[string]any)
	data, _ := os.ReadFile(path)
	var invalid map[string]any
	_ = json.Unmarshal(data, &invalid)
	invalid["homes_version"] = 2
	data, _ = json.Marshal(invalid)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	result := accountListFrom(t, s)
	if result["complete"] != false || len(result["errors"].([]any)) != 1 {
		t.Fatalf("invalid explicit profile treated as a complete census: %v", result)
	}
	rows := result["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatal("invalid census dropped known account")
	}
	current := rows[0].(map[string]any)
	if current["id"] != before["id"] || current["retention_pending"] != false || len(current["profiles"].([]any)) != 1 {
		t.Fatal("invalid census lost known membership or started retention")
	}
	if result["errors"].([]any)[0].(map[string]any)["code"] != "invalid_profile" {
		t.Fatal("invalid explicit profile not diagnosed")
	}
}
