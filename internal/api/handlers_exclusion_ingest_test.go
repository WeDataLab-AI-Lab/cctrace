package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/ingestblock"
)

func adminJSON(method, url, body string) *http.Request {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	return r.WithContext(auth.WithUser(r.Context(), admin()))
}

// Changing an exclusion has to reach ingest at once, and through the database:
// excluding an address also excludes the billing accounts it is linked to, which
// only the store knows. The cache is reloaded after the change commits rather
// than patched by hand.
//
// Un-excluding is the case that must not wait for the 30-second tick: until the
// cache forgets the account, live data from it is refused and nothing sweeps it
// back.
func TestExclusionChangesReloadTheIngestBlocklist(t *testing.T) {
	excluded := false
	loader := func(context.Context) (*ingestblock.Sets, error) {
		sets := &ingestblock.Sets{ExcludedAccounts: map[string]bool{}, ExcludedEmails: map[string]bool{}}
		if excluded {
			sets.ExcludedEmails["personal@example.test"] = true
			sets.ExcludedAccounts[ingestblock.AccountKey("openai", "acct-linked")] = true
		}
		return sets, nil
	}
	m := &mockStore{}
	bl := ingestblock.New()
	srv := newTestServer(m, nil).WithIngestBlocklist(bl).WithIngestBlocklistLoader(loader)

	excluded = true
	rec := httptest.NewRecorder()
	srv.handleExcludeAccount(rec, adminJSON(http.MethodPost, "/api/admin/excluded-accounts",
		`{"login_email":"personal@example.test","reason":"personal"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("exclude status = %d (%s)", rec.Code, rec.Body.String())
	}
	if !bl.EmailExcluded("personal@example.test") {
		t.Error("the excluded address is still accepted at ingest")
	}
	if !bl.AccountExcluded("openai", "acct-linked") {
		t.Error("the billing account linked to the address is still accepted at ingest")
	}

	excluded = false
	rec = httptest.NewRecorder()
	srv.handleRemoveExcludedAccount(rec, adminJSON(http.MethodDelete,
		"/api/admin/excluded-accounts?login_email=personal@example.test", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d (%s)", rec.Code, rec.Body.String())
	}
	if bl.EmailExcluded("personal@example.test") || bl.AccountExcluded("openai", "acct-linked") {
		t.Error("ingest still refuses an account that is no longer excluded")
	}
}

// The billing-id path does the same.
func TestBillingExclusionChangesReloadTheIngestBlocklist(t *testing.T) {
	excluded := false
	loader := func(context.Context) (*ingestblock.Sets, error) {
		sets := &ingestblock.Sets{ExcludedAccounts: map[string]bool{}}
		if excluded {
			sets.ExcludedAccounts[ingestblock.AccountKey("openai", "acct-direct")] = true
		}
		return sets, nil
	}
	bl := ingestblock.New()
	srv := newTestServer(&mockStore{}, nil).WithIngestBlocklist(bl).WithIngestBlocklistLoader(loader)

	excluded = true
	rec := httptest.NewRecorder()
	srv.handleExcludeBillingAccount(rec, adminJSON(http.MethodPost, "/api/admin/excluded-billing-accounts",
		`{"billing_provider":"openai","account_id":"acct-direct","reason":"personal"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("exclude status = %d (%s)", rec.Code, rec.Body.String())
	}
	if !bl.AccountExcluded("openai", "acct-direct") {
		t.Error("the excluded billing account is still accepted at ingest")
	}

	excluded = false
	rec = httptest.NewRecorder()
	srv.handleRemoveExcludedBillingAccount(rec, adminJSON(http.MethodDelete,
		"/api/admin/excluded-billing-accounts?billing_provider=openai&account_id=acct-direct", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove status = %d (%s)", rec.Code, rec.Body.String())
	}
	if bl.AccountExcluded("openai", "acct-direct") {
		t.Error("ingest still refuses a billing account that is no longer excluded")
	}
}

// A reload that fails leaves the cache as it was, so each handler first applies
// what it knows directly. Excluding must not wait for the next tick to start
// refusing, and un-excluding must not keep refusing live data until then.
func TestExclusionChangesApplyEvenWhenTheReloadFails(t *testing.T) {
	failing := func(context.Context) (*ingestblock.Sets, error) { return nil, errors.New("db down") }
	bl := ingestblock.New()
	srv := newTestServer(&mockStore{}, nil).WithIngestBlocklist(bl).WithIngestBlocklistLoader(failing)

	cases := []struct {
		name    string
		req     *http.Request
		handler func(http.ResponseWriter, *http.Request)
		refused func() bool
		want    bool
	}{
		{"exclude address", adminJSON(http.MethodPost, "/api/admin/excluded-accounts", `{"login_email":"personal@example.test"}`),
			srv.handleExcludeAccount, func() bool { return bl.EmailExcluded("personal@example.test") }, true},
		{"un-exclude address", adminJSON(http.MethodDelete, "/api/admin/excluded-accounts?login_email=personal@example.test", ""),
			srv.handleRemoveExcludedAccount, func() bool { return bl.EmailExcluded("personal@example.test") }, false},
		{"exclude billing account", adminJSON(http.MethodPost, "/api/admin/excluded-billing-accounts", `{"billing_provider":"openai","account_id":"acct-direct"}`),
			srv.handleExcludeBillingAccount, func() bool { return bl.AccountExcluded("openai", "acct-direct") }, true},
		{"un-exclude billing account", adminJSON(http.MethodDelete, "/api/admin/excluded-billing-accounts?billing_provider=openai&account_id=acct-direct", ""),
			srv.handleRemoveExcludedBillingAccount, func() bool { return bl.AccountExcluded("openai", "acct-direct") }, false},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		c.handler(rec, c.req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d (%s)", c.name, rec.Code, rec.Body.String())
		}
		if got := c.refused(); got != c.want {
			t.Errorf("%s with a failing reload: refused = %v, want %v", c.name, got, c.want)
		}
	}
}
