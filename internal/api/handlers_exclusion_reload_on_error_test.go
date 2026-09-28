package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/ingestblock"
)

// A failed commit does not say whether the exclusion change landed. The
// request then answers 500 although the table may already have changed; ingest
// has to follow the table anyway, or an exclusion goes on accepting data -- or
// an un-exclusion goes on refusing it -- until the next refresh tick.
func TestExclusionChangesReloadTheIngestBlocklistEvenOnError(t *testing.T) {
	commitFailed := errors.New("commit exclusion change: connection lost")

	for _, tc := range []struct {
		name      string
		excluding bool // whether the committed change leaves the account excluded
		serve     func(srv *Server) *httptest.ResponseRecorder
	}{
		{"admin excludes an address", true, func(srv *Server) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			srv.handleExcludeAccount(rec, adminJSON(http.MethodPost, "/api/admin/excluded-accounts",
				`{"login_email":"personal@example.test","reason":"personal"}`))
			return rec
		}},
		{"admin un-excludes an address", false, func(srv *Server) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			srv.handleRemoveExcludedAccount(rec, adminJSON(http.MethodDelete,
				"/api/admin/excluded-accounts?login_email=personal@example.test", ""))
			return rec
		}},
		{"admin excludes a billing account", true, func(srv *Server) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			srv.handleExcludeBillingAccount(rec, adminJSON(http.MethodPost, "/api/admin/excluded-billing-accounts",
				`{"billing_provider":"openai","account_id":"acct-mine","reason":"personal"}`))
			return rec
		}},
		{"admin un-excludes a billing account", false, func(srv *Server) *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			srv.handleRemoveExcludedBillingAccount(rec, adminJSON(http.MethodDelete,
				"/api/admin/excluded-billing-accounts?billing_provider=openai&account_id=acct-mine", ""))
			return rec
		}},
		{"user self-excludes", true, func(srv *Server) *httptest.ResponseRecorder {
			return serveSelfExclusion(srv, http.MethodPost, "/api/self-exclusions/billing-accounts",
				`{"billing_provider":"openai","account_id":"acct-mine"}`)
		}},
		{"user includes again", false, func(srv *Server) *httptest.ResponseRecorder {
			return serveSelfExclusion(srv, http.MethodDelete,
				"/api/self-exclusions/billing-accounts?billing_provider=openai&account_id=acct-mine", "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loader := func(context.Context) (*ingestblock.Sets, error) {
				sets := &ingestblock.Sets{ExcludedAccounts: map[string]bool{}, ExcludedEmails: map[string]bool{}}
				if tc.excluding {
					sets.ExcludedAccounts[ingestblock.AccountKey("openai", "acct-mine")] = true
				}
				return sets, nil
			}
			m := &selfExclusionStore{mockStore: &mockStore{exclusionChangeErr: commitFailed}, changeErr: commitFailed}
			bl := ingestblock.New()
			if !tc.excluding {
				bl.AddExcludedAccount("openai", "acct-mine")
			}
			srv := newTestServer(m, nil).WithIngestBlocklist(bl).WithIngestBlocklistLoader(loader)

			rec := tc.serve(srv)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d (%s), want the store's error reported", rec.Code, rec.Body.String())
			}
			if got := bl.AccountExcluded("openai", "acct-mine"); got != tc.excluding {
				t.Fatalf("refused at ingest = %v, want %v: the blocklist was not reloaded after the error", got, tc.excluding)
			}
		})
	}
}
