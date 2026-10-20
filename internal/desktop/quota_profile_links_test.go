package desktop

import (
	"path/filepath"
	"testing"

	"cctrace/internal/profile"
)

func TestQuotaProfileLinkAggregationKeepsMixedAndRunningEvidence(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	cases := []struct {
		a, b       quotaAccountProfile
		mixed      bool
		collecting *bool
	}{
		{quotaAccountProfile{Excluded: &yes, Collecting: &no}, quotaAccountProfile{Excluded: &no, Collecting: &yes}, true, &yes},
		{quotaAccountProfile{Excluded: &yes, Collecting: &no}, quotaAccountProfile{Excluded: &yes, Collecting: &no}, false, &no},
		{quotaAccountProfile{Excluded: &no}, quotaAccountProfile{Excluded: &no, Collecting: &no}, false, nil},
	}
	for _, tc := range cases {
		got := mergeQuotaProfileLink(tc.a, tc.b)
		if (got.ExclusionState == "mixed") != tc.mixed {
			t.Fatal("mixed state lost")
		}
		if tc.mixed && got.Excluded != nil {
			t.Fatal("mixed presented as bool")
		}
		if (got.Collecting == nil) != (tc.collecting == nil) || got.Collecting != nil && *got.Collecting != *tc.collecting {
			t.Fatal("collection aggregation lost evidence")
		}
	}
}
func TestSameProfileProvenAliasesStayMixedAfterReconcile(t *testing.T) {
	t.Parallel()
	registry := filepath.Join(t.TempDir(), "registry")
	p := profile.NewDefault()
	p.HomesVersion = 1
	p.User.ID = "fixture"
	p.Server.Endpoint = "https://fixture.invalid"
	for _, policy := range []string{"allow", "exclude"} {
		home, _ := profile.NormalizeCodexHome(t.TempDir())
		p.Homes = append(p.Homes, profile.HomeMembership{Provider: "claude", Home: home, Transmission: policy})
	}
	if err := profile.SaveTo(p, filepath.Join(registry, "profile.json")); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{AppSupportRoot: filepath.Join(t.TempDir(), "support"), ProfileRoot: registry})
	if err != nil {
		t.Fatal(err)
	}
	_ = call(t, s, "h", "hello", map[string]any{"multi_home_profiles": true})
	_ = call(t, s, "homes", "hello", map[string]any{"quota_account_homes": true})
	_ = accountListFrom(t, s)
	s.accountsMu.Lock()
	if len(s.accounts.Accounts) != 2 {
		t.Fatal("home accounts not independently discovered")
	}
	if e := s.applyAccountProofLocked(0, "synthetic-proof", "acct-synthetic"); e != nil {
		t.Fatal(e)
	}
	if e := s.applyAccountProofLocked(1, "synthetic-proof", "acct-synthetic"); e == nil || e.Code != "account_mismatch" {
		t.Fatal("proof merge did not fence old refs")
	}
	s.accountsMu.Unlock()
	for i := 0; i < 2; i++ {
		rows := accountListFrom(t, s)["accounts"].([]any)
		if len(rows) != 1 {
			t.Fatal("proven aliases split")
		}
		row := rows[0].(map[string]any)
		links := row["profiles"].([]any)
		if len(links) != 1 || links[0].(map[string]any)["excluded"] != nil || links[0].(map[string]any)["exclusion_state"] != "mixed" || len(row["config_homes"].([]any)) != 2 {
			t.Fatalf("same-profile mixed aliases hidden: %v", row)
		}
	}
}
