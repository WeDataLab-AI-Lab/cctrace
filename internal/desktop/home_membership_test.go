package desktop

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"cctrace/internal/profile"
)

func TestMembershipMoveBindsOperationAndPreservesProfileExecutionFlags(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	homes := []string{}
	r := profile.Registry{Root: registry}
	for _, name := range []string{"default", "personal"} {
		home, _ := profile.NormalizeCodexHome(t.TempDir())
		homes = append(homes, home)
		p := profile.NewDefault()
		p.ID = profile.ProfileID(name)
		p.HomesVersion = 1
		p.Homes = []profile.HomeMembership{{Provider: "claude", Home: home, Transmission: "allow", QuotaEnabled: true}}
		p.Options.SyncEnabled = name == "default"
		p.User.ID = "fixture"
		p.Server.Endpoint = "https://fixture.invalid"
		path, _ := r.Path(name)
		if err := profile.SaveTo(p, path); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry})
	if err != nil {
		t.Fatal(err)
	}
	_ = call(t, s, "h", "hello", map[string]any{"multi_home_profiles": true})
	from, _ := r.Load("personal")
	to, _ := r.Load("default")
	payload := map[string]any{"provider": "claude", "home": homes[1], "source": "personal", "source_revision": from.Revision, "target": "default", "target_revision": to.Revision}
	preview := func() map[string]any {
		t.Helper()
		result := call(t, s, "p", "profiles.membership.preview", payload)
		if result.Error != nil {
			t.Fatal(result.Error)
		}
		raw, _ := json.Marshal(result.Result)
		var plan map[string]any
		_ = json.Unmarshal(raw, &plan)
		if !bytes.Contains(raw, []byte("destination collection=true")) {
			t.Fatal("eligibility effect absent")
		}
		return plan
	}
	first := preview()
	if wrong := call(t, s, "wrong", "profiles.transmission.apply", map[string]any{"preview_ref": first["preview_ref"], "confirm": true}); wrong.Error == nil {
		t.Fatal("wrong operation applied move")
	}
	unchanged, _ := r.Load("personal")
	if unchanged.Revision != from.Revision {
		t.Fatal("wrong-op token wrote profile")
	}
	plan := preview()
	if result := call(t, s, "apply", "profiles.membership.apply", map[string]any{"preview_ref": plan["preview_ref"], "confirm": true}); result.Error != nil {
		t.Fatal(result.Error)
	}
	from, _ = r.Load("personal")
	to, _ = r.Load("default")
	if len(from.Profile.Homes) != 0 || len(to.Profile.Homes) != 2 || from.Profile.CollectionEnabled("claude") || !to.Profile.CollectionEnabled("claude") {
		t.Fatal("membership move changed execution flags")
	}
	if !to.Profile.Homes[1].QuotaEnabled || to.Profile.Homes[1].Transmission != "allow" {
		t.Fatal("move changed consent")
	}
	if again := call(t, s, "replay", "profiles.membership.apply", map[string]any{"preview_ref": plan["preview_ref"], "confirm": true}); again.Error == nil {
		t.Fatal("move preview replay accepted")
	}
	tampered := []map[string]any{{"provider": "claude", "home": homes[0], "transmission": "exclude", "quota_enabled": true}, {"provider": "claude", "home": homes[1], "transmission": "allow", "quota_enabled": true}}
	if result := call(t, s, "tamper", "profiles.homes.preview", map[string]any{"name": "default", "revision": to.Revision, "homes": tampered}); result.Error == nil {
		t.Fatal("generic writer changed account-wide consent")
	}
}
