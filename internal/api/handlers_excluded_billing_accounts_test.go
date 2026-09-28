package api

import "testing"

// The provider is folded and the account id is not. An address is
// case-insensitive; an account id is an opaque token from the provider, so two
// ids differing only in case are two accounts, and folding one into the other
// would exclude an account nobody asked to exclude.
func TestValidBillingKey_foldsTheProviderButNotTheAccountID(t *testing.T) {
	provider, accountID, msg := validBillingKey("  OpenAI  ", "  AcctCodex  ")
	if msg != "" {
		t.Fatalf("rejected a valid pair: %s", msg)
	}
	if provider != "openai" {
		t.Errorf("provider = %q, want it folded to openai", provider)
	}
	if accountID != "AcctCodex" {
		t.Errorf("account_id = %q, want its case kept", accountID)
	}
}

// Both halves are required: a blank one would make the key match rows it was
// never meant to.
func TestValidBillingKey_requiresBothHalves(t *testing.T) {
	for _, tc := range []struct{ provider, accountID, want string }{
		{"", "acct", "billing_provider required"},
		{"   ", "acct", "billing_provider required"},
		{"openai", "", "account_id required"},
		{"openai", "   ", "account_id required"},
	} {
		if _, _, msg := validBillingKey(tc.provider, tc.accountID); msg != tc.want {
			t.Errorf("validBillingKey(%q, %q) = %q, want %q", tc.provider, tc.accountID, msg, tc.want)
		}
	}
}
