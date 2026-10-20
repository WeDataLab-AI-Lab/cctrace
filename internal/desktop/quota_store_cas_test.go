package desktop

import "testing"

func TestQuotaStoreIndependentHelpersRejectLostUpdate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := accountTestService(t, root)
	second := accountTestService(t, root)
	_ = accountListFrom(t, first)
	_ = accountListFrom(t, second)
	second.accountsMu.Lock()
	stale := second.accountsCloneLocked()
	second.accountsMu.Unlock()
	if r := call(t, first, "a", "quota.account.add", map[string]any{"provider": "claude", "home": t.TempDir(), "label": "first"}); r.Error != nil {
		t.Fatal(r.Error)
	}
	second.accountsMu.Lock()
	staleError := second.commitAccountsLocked(stale)
	second.accountsMu.Unlock()
	if staleError == nil || staleError.Code != "account_mismatch" {
		t.Fatalf("stale helper overwrote preferences: %v", staleError)
	}
	rows := accountListFrom(t, second)["accounts"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["label"] != "first" {
		t.Fatal("concurrent preference disappeared")
	}
	if r := call(t, second, "retry", "quota.account.add", map[string]any{"provider": "claude", "home": t.TempDir(), "label": "second"}); r.Error != nil {
		t.Fatal(r.Error)
	}
	refreshed := accountListFrom(t, first)["accounts"].([]any)
	if len(refreshed) != 2 {
		t.Fatal("independent helper read did not refresh shared preferences")
	}
}
