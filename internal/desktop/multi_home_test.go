package desktop

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/profile"
)

func TestMultiHomePreviewConfirmationAndPersonalDiscovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	home, _ := filepath.EvalSymlinks(t.TempDir())
	p := profile.NewDefault()
	p.HomesVersion = 1
	p.Homes = []profile.HomeMembership{{Provider: "claude", Home: home, Transmission: "exclude", QuotaEnabled: true}}
	p.Options.SyncEnabled = false
	if err := profile.SaveTo(p, filepath.Join(registry, "profile.json")); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry})
	if err != nil {
		t.Fatal(err)
	}
	if r := call(t, s, "h", "hello", map[string]any{"multi_home_profiles": true}); r.Error != nil {
		t.Fatal(r.Error)
	}
	entry, err := s.registry.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(home, 0700)
	homes := []map[string]any{{"provider": "claude", "home": home, "transmission": "exclude", "quota_enabled": true}}
	before, _ := os.ReadFile(filepath.Join(registry, "profile.json"))
	r := call(t, s, "p", "profiles.homes.preview", map[string]any{"name": "default", "revision": entry.Revision, "homes": homes})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	b, _ := json.Marshal(r.Result)
	var preview map[string]any
	_ = json.Unmarshal(b, &preview)
	after, _ := os.ReadFile(filepath.Join(registry, "profile.json"))
	if string(before) != string(after) {
		t.Fatal("preview wrote profile")
	}
	if r = call(t, s, "cancel", "profiles.homes.apply", map[string]any{"preview_ref": preview["preview_ref"], "confirm": false}); r.Error == nil {
		t.Fatal("unconfirmed applied")
	}
	if r = call(t, s, "a", "profiles.homes.apply", map[string]any{"preview_ref": preview["preview_ref"], "confirm": true}); r.Error != nil {
		t.Fatal(r.Error)
	}
	got, _ := s.registry.Load("default")
	if got.Profile.Options.SyncEnabled {
		t.Fatal("membership enabled collection")
	}
	rows := accountListFrom(t, s)["accounts"].([]any)
	if len(rows) != 1 {
		t.Fatalf("collection-off home absent: %v", rows)
	}
	account := rows[0].(map[string]any)
	if account["enabled"] != true {
		t.Fatal("quota consent lost")
	}
	links := account["profiles"].([]any)
	if links[0].(map[string]any)["excluded"] != true {
		t.Fatal("path exclusion lost without billing identity")
	}
	if r = call(t, s, "replay", "profiles.homes.apply", map[string]any{"preview_ref": preview["preview_ref"], "confirm": true}); r.Error == nil {
		t.Fatal("preview replay accepted")
	}
}

func TestTransmissionPreviewPathPolicyNeverMovesMembership(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	home, _ := filepath.EvalSymlinks(t.TempDir())
	p := profile.NewDefault()
	p.Options.SyncEnabled = false
	p.HomesVersion = 1
	p.Homes = []profile.HomeMembership{{Provider: "claude", Home: home, Transmission: "exclude", QuotaEnabled: true}}
	if err := profile.SaveTo(p, filepath.Join(registry, "profiles", "personal", "profile.json")); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry})
	if err != nil {
		t.Fatal(err)
	}
	r := call(t, s, "p", "profiles.transmission.preview", map[string]any{"provider": "claude", "home": home, "excluded": false})
	if r.Error != nil {
		t.Fatal(r.Error)
	}
	b, _ := json.Marshal(r.Result)
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	if r = call(t, s, "a", "profiles.transmission.apply", map[string]any{"preview_ref": v["preview_ref"], "confirm": true}); r.Error != nil {
		t.Fatal(r.Error)
	}
	entry, err := s.registry.Load("personal")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Profile.Options.SyncEnabled || entry.Profile.Homes[0].Transmission != "allow" || !entry.Profile.Homes[0].QuotaEnabled {
		t.Fatalf("axes changed: %+v", entry.Profile)
	}
}

func TestMultiHomeEmptyGroupWireIsExplicitOnlyAfterNegotiation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	registry := filepath.Join(root, "registry")
	p := profile.NewDefault()
	p.HomesVersion = 1
	p.Homes = []profile.HomeMembership{}
	p.Options.SyncEnabled = false
	if err := profile.SaveTo(p, filepath.Join(registry, "profile.json")); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{AppSupportRoot: filepath.Join(root, "support"), ProfileRoot: registry})
	if err != nil {
		t.Fatal(err)
	}
	before := call(t, s, "l", "profiles.list", map[string]any{})
	if before.Error != nil {
		t.Fatal(before.Error)
	}
	raw, _ := json.Marshal(before.Result)
	if bytes.Contains(raw, []byte(`"homes"`)) {
		t.Fatal("unnegotiated explicit homes leaked")
	}
	if r := call(t, s, "h", "hello", map[string]any{"multi_home_profiles": true}); r.Error != nil {
		t.Fatal(r.Error)
	}
	after := call(t, s, "l2", "profiles.list", map[string]any{})
	if after.Error != nil {
		t.Fatal(after.Error)
	}
	raw, _ = json.Marshal(after.Result)
	if !bytes.Contains(raw, []byte(`"homes":[]`)) || !bytes.Contains(raw, []byte(`"home_collection":{"claude":false,"codex":false}`)) {
		t.Fatalf("empty group ambiguous: %s", raw)
	}
}
