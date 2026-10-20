package desktop

import "testing"

func TestQuotaAccountOrderUsesProfileRoleInsteadOfRandomID(t *testing.T) {
	t.Parallel()
	included, excluded := false, true
	s := accountTestService(t, t.TempDir())
	home := t.TempDir()
	s.accounts = &quotaAccountStore{Accounts: []quotaAccountRecord{
		{ID: "a", Provider: "codex", Home: home, Label: "personal", Personal: true, Enabled: true},
		{ID: "b", Provider: "codex", Home: home, Label: "excluded", Profiles: []quotaAccountProfile{{ID: "named:excluded", Excluded: &excluded}}},
		{ID: "z", Provider: "codex", Home: home, Label: "legacy-label", Profiles: []quotaAccountProfile{{ID: "default", Excluded: &included}}},
		{ID: "d", Provider: "codex", Home: home, Label: "work", Profiles: []quotaAccountProfile{{ID: "named:work", Excluded: &included}}},
		{ID: "c", Provider: "codex", Home: home, Label: "alpha", Profiles: []quotaAccountProfile{{ID: "named:alpha", Excluded: &included}, {ID: "named:other", Excluded: &excluded}}},
	}}
	want := []string{"z", "c", "d", "a", "b"}
	for _, reverse := range []bool{false, true} {
		if reverse {
			for i, j := 0, len(s.accounts.Accounts)-1; i < j; i, j = i+1, j-1 {
				s.accounts.Accounts[i], s.accounts.Accounts[j] = s.accounts.Accounts[j], s.accounts.Accounts[i]
			}
		}
		got := s.accountsViewLocked().Accounts
		for i, id := range want {
			if got[i].ID != id {
				t.Fatalf("position %d = %s, want %s (reverse=%t)", i, got[i].ID, id, reverse)
			}
		}
		if got[0].Enabled || !got[3].Enabled {
			t.Fatal("presentation ordering changed monitoring preferences")
		}
	}
}
