package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/envgen"
	"cctrace/internal/profile"
)

func TestProviderConsentRecoversCrashImmediatelyAfterCollectionStopConfigWrite(t *testing.T) {
	t.Parallel()
	r := profile.Registry{Root: filepath.Join(t.TempDir(), "registry")}
	homes := []string{}
	originals := [][]byte{}
	for i := 0; i < 2; i++ {
		home, _ := profile.NormalizeCodexHome(t.TempDir())
		homes = append(homes, home)
		p := profile.NewDefault()
		p.ClaudeConfigDir = home
		p.Server.Endpoint = "https://fixture.invalid"
		p.Server.AuthToken = "synthetic"
		if err := envgen.ApplyToClaudeSettings(p); err != nil {
			t.Fatal(err)
		}
		original, _ := os.ReadFile(filepath.Join(home, "settings.json"))
		originals = append(originals, original)
	}
	p := profile.NewDefault()
	p.ID = "default"
	p.ClaudeConfigDir = homes[0]
	p.HomesVersion = 1
	p.Homes = []profile.HomeMembership{{Provider: "claude", Home: homes[0], Transmission: "allow"}, {Provider: "claude", Home: homes[1], Transmission: "allow"}}
	p.Server.Endpoint = "https://fixture.invalid"
	p.Server.AuthToken = "synthetic"
	p.User.ID = "fixture"
	path, _ := r.Path("default")
	if err := profile.SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	entry, _ := r.Load("default")
	before, _ := os.ReadFile(path)
	s, err := New(Config{AppSupportRoot: filepath.Join(t.TempDir(), "support"), ProfileRoot: r.Root})
	if err != nil {
		t.Fatal(err)
	}
	denied := append([]profile.HomeMembership{}, entry.Profile.Homes...)
	for i := range denied {
		denied[i].Transmission = "exclude"
	}
	stopped := *entry.Profile
	stopped.HomeCollection = &profile.HomeCollection{}
	result, e := s.storeConsentPlan(consentPlan{kind: "onboarding", migration: true, Profiles: []profile.HomeUpdate{{Name: "default", Revision: entry.Revision, Homes: denied, Definition: &stopped}}, homes: homes})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(result)
	var preview map[string]any
	_ = json.Unmarshal(raw, &preview)
	if len(preview["provider_effects"].([]any)) != 2 {
		t.Fatal("provider effects not shown before confirmation")
	}
	s.providerConsentAfterWrite = func(index int) error {
		if index == 0 {
			return errors.New("synthetic crash after first provider write")
		}
		return nil
	}
	request, _ := json.Marshal(map[string]any{"preview_ref": preview["preview_ref"], "confirm": true})
	if _, e = s.applyConsent(context.Background(), request, "onboarding"); e == nil {
		t.Fatal("interrupted apply reported success")
	}
	if !r.HomeTransactionPending() {
		t.Fatal("interruption did not retain fail-closed barrier")
	}
	changed, _ := os.ReadFile(filepath.Join(homes[0], "settings.json"))
	if bytes.Equal(changed, originals[0]) {
		t.Fatal("fault did not occur after actual config write")
	}
	recovery, _ := json.Marshal(map[string]any{"confirm": true})
	if _, e = s.recoverHomeConsent(recovery); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("profile not restored")
	}
	for i, home := range homes {
		actual, _ := os.ReadFile(filepath.Join(home, "settings.json"))
		if !bytes.Equal(actual, originals[i]) {
			t.Fatal("provider config did not restore exact original")
		}
	}
}
