package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cctrace/internal/profile"
	"cctrace/internal/usage"
)

// HOME and the fetch seam are process-wide; these cases intentionally run serially.
func statusMultiHomeFixture(t *testing.T) (string, *[]string) {
	t.Helper()
	home := setupTestHome(t)
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Server = profile.ServerInfo{}
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	old := statusUsageFetch
	var calls []string
	statusUsageFetch = func(home string) (*usage.Response, error) {
		calls = append(calls, home)
		return &usage.Response{FiveHour: &usage.UsageWindow{Utilization: 37}}, nil
	}
	t.Cleanup(func() { statusUsageFetch = old })
	return home, &calls
}

func explicitStatusProfile(t *testing.T, home string, count int) *profile.Profile {
	t.Helper()
	p := profile.NewDefault()
	p.HomesVersion, p.Homes = 1, []profile.HomeMembership{}
	p.HomeCollection = &profile.HomeCollection{}
	base, err := os.MkdirTemp(home, "enrolled-")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		provider := "claude"
		if i >= count/2 {
			provider = "codex"
		}
		dir := filepath.Join(base, "registered-"+provider+string(rune('a'+i)))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		p.Homes = append(p.Homes, profile.HomeMembership{Provider: provider, Home: dir, Transmission: "exclude", QuotaEnabled: i%2 == 0})
	}
	return p
}

func TestStatusExplicitPersonalListsFourHomesWithoutProviderFetch(t *testing.T) {
	home, calls := statusMultiHomeFixture(t)
	p := explicitStatusProfile(t, home, 4)
	if err := profile.SaveNamed(p, "personal"); err != nil {
		t.Fatal(err)
	}
	out := captureStatus(t, "personal")
	for _, h := range p.Homes {
		want := h.Provider + " home: " + h.Home + " (transmission=exclude, collection=disabled, quota enrollment=" + boolStatus(h.QuotaEnabled) + ")"
		if !strings.Contains(out, want) {
			t.Errorf("missing explicit consent row %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, filepath.Join(home, ".claude")) || strings.Contains(out, "Settings:") {
		t.Errorf("explicit status fell back to an unregistered home:\n%s", out)
	}
	if len(*calls) != 0 {
		t.Errorf("explicit status requested provider usage (including a default/unregistered home): count=%d homes=%q", len(*calls), *calls)
	}
	if !strings.Contains(out, "cctrace profile accounts") || !strings.Contains(out, "not the current account opt-in") {
		t.Errorf("status must defer current per-account preferences to supported authority:\n%s", out)
	}
}

func TestStatusExplicitEmptyHomesNeverFallsBackOrFetches(t *testing.T) {
	home, calls := statusMultiHomeFixture(t)
	p := explicitStatusProfile(t, home, 0)
	if err := profile.SaveNamed(p, "empty"); err != nil {
		t.Fatal(err)
	}
	out := captureStatus(t, "empty")
	if !strings.Contains(out, "Registered homes: (none; no implicit defaults)") || strings.Contains(out, filepath.Join(home, ".claude")) || strings.Contains(out, "Settings:") || len(*calls) != 0 {
		t.Fatalf("explicit empty profile implied a home or requested usage: calls=%v\n%s", *calls, out)
	}
}

func TestStatusExplicitDefaultOverviewListsNamedHomesAndEffectiveCollection(t *testing.T) {
	home, calls := statusMultiHomeFixture(t)
	definition := explicitStatusProfile(t, home, 2)
	def, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	def.HomesVersion, def.Homes, def.HomeCollection = definition.HomesVersion, definition.Homes, definition.HomeCollection
	def.Homes[0].Transmission = "allow"
	def.Homes[1].Transmission = "allow"
	def.HomeCollection.Claude = true // Legacy sync flags are fenced false.
	if err := profile.Save(def); err != nil {
		t.Fatal(err)
	}
	named := explicitStatusProfile(t, home, 4)
	if err := profile.SaveNamed(named, "personal"); err != nil {
		t.Fatal(err)
	}
	out := captureStatus(t, "") // Default uses no reserved --profile default flag.
	for _, h := range append(def.Homes, named.Homes...) {
		if !strings.Contains(out, h.Provider+" home: "+h.Home) {
			t.Errorf("overview omitted registered home %s:\n%s", h.Home, out)
		}
	}
	if !strings.Contains(out, "transmission=allow, collection=enabled") || strings.Contains(out, filepath.Join(home, ".claude")) || len(*calls) != 0 {
		t.Fatalf("overview ignored effective collection or performed implicit usage: calls=%v\n%s", *calls, out)
	}
}

func TestStatusLegacyHomeAndQuotaFetchAreUnchanged(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(boolStatus(configured), func(t *testing.T) {
			home, calls := statusMultiHomeFixture(t)
			p, err := profile.Load()
			if err != nil {
				t.Fatal(err)
			}
			wantDir := filepath.Join(home, ".claude")
			if configured {
				wantDir = filepath.Join(home, "legacy-provider")
				p.ClaudeConfigDir = wantDir
			}
			if err := profile.Save(p); err != nil {
				t.Fatal(err)
			}
			out := captureStatus(t, "")
			if !reflect.DeepEqual(*calls, []string{p.ClaudeConfigDir}) || !strings.Contains(out, "Claude home:   "+wantDir) || !strings.Contains(out, "5h:") || !strings.Contains(out, "37.0%") {
				t.Fatalf("legacy display/fetch changed: calls=%v\n%s", *calls, out)
			}
		})
	}
}
