package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOnboardingRequiresEveryDecisionIncludingDiscoveryExclusion(t *testing.T) {
	t.Parallel()
	base, _ := NormalizeCodexHome(t.TempDir())
	home := filepath.Join(base, ".claude-2")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	found, err := r.DiscoverHomes(base)
	if err != nil || len(found.Candidates) != 1 {
		t.Fatalf("discovery %v %v", found, err)
	}
	if err = r.SetHomeExcluded(found.Candidates[0], true); err != nil {
		t.Fatal(err)
	}
	homes, err := r.OnboardingHomes(base)
	if err != nil || len(homes) != 1 {
		t.Fatalf("excluded home disappeared: %v %v", homes, err)
	}
	if _, err = r.PlanOnboarding(base, homes); err == nil {
		t.Fatal("undecided accepted")
	}
	homes[0].Transmission = "exclude"
	homes[0].QuotaEnabled = true
	plan, err := r.PlanOnboarding(base, homes)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 || len(plan[0].Homes) != 0 || len(plan[1].Homes) != 1 {
		t.Fatalf("assignment=%+v", plan)
	}
	if _, err = r.ApplyHomeUpdates(plan, true); err != nil {
		t.Fatal(err)
	}
	personal, err := r.Load("personal")
	if err != nil {
		t.Fatal(err)
	}
	if personal.Profile.Options.SyncEnabled || !personal.Profile.Homes[0].QuotaEnabled {
		t.Fatal("axes coupled")
	}
}

func TestSelectedDiscoveryExclusionMigrationPreservesUnselectedChoice(t *testing.T) {
	t.Parallel()
	base, _ := NormalizeCodexHome(t.TempDir())
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	for _, name := range []string{".claude-2", ".claude-3"} {
		home := filepath.Join(base, name)
		if err := os.Mkdir(home, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	found, err := r.DiscoverHomes(base)
	if err != nil || len(found.Candidates) != 2 {
		t.Fatalf("census %v %v", found, err)
	}
	for _, candidate := range found.Candidates {
		if err = r.SetHomeExcluded(candidate, true); err != nil {
			t.Fatal(err)
		}
	}
	selected := found.Candidates[0]
	updates, err := r.PlanOnboarding(base, []HomeMembership{{Provider: selected.Provider, Home: selected.Home, Transmission: "exclude", QuotaEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ApplyHomeUpdates(updates, true); err != nil {
		t.Fatal(err)
	}
	exclusions, err := r.homeExclusions()
	if err != nil || len(exclusions) != 1 || exclusions[homeExclusionKey(selected)] || !exclusions[homeExclusionKey(found.Candidates[1])] {
		t.Fatalf("unrelated discovery choice changed: %v %v", exclusions, err)
	}
}
