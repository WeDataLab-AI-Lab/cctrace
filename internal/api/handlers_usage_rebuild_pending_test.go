package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An exclusion change answers before the usage charts are rebuilt, so the two
// screens that make those changes need to know the charts are still catching
// up. The flag is on the list response rather than on an item: after the last
// exclusion is removed the list is empty and the rebuild is still owed.
func TestExclusionListsReportAPendingUsageRebuild(t *testing.T) {
	for _, pending := range []bool{true, false} {
		m := &mockStore{usageRebuildPending: pending}
		srv := newTestServer(m, nil)

		rec := httptest.NewRecorder()
		srv.handleListExcludedAccounts(rec, adminJSON(http.MethodGet, "/api/admin/excluded-accounts", ""))
		assertUsageRebuildPending(t, "admin excluded accounts", rec, &pending)

		rec = serveSelfExclusion(srv, http.MethodGet, "/api/self-exclusions/billing-accounts", "")
		assertUsageRebuildPending(t, "self exclusions", rec, &pending)
	}
}

// want nil means the field must be absent: not known.
func assertUsageRebuildPending(t *testing.T, name string, rec *httptest.ResponseRecorder, want *bool) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d (%s)", name, rec.Code, rec.Body.String())
	}
	var body struct {
		Accounts            []json.RawMessage `json:"accounts"`
		UsageRebuildPending *bool             `json:"usage_rebuild_pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: body = %s: %v", name, rec.Body.String(), err)
	}
	if body.Accounts == nil {
		t.Errorf("%s: body = %s, want an accounts array even when empty", name, rec.Body.String())
	}
	switch {
	case want == nil && body.UsageRebuildPending != nil:
		t.Errorf("%s: usage_rebuild_pending in %s, want it left out", name, rec.Body.String())
	case want != nil && (body.UsageRebuildPending == nil || *body.UsageRebuildPending != *want):
		t.Errorf("%s: usage_rebuild_pending in %s, want %v", name, rec.Body.String(), *want)
	}
}

// Whether the charts are catching up is a note beside the list. Failing to read
// it must not take the list away, and must not answer false either: the screen
// reads false as "the rebuild finished", refetches the charts, stops polling and
// hides the note. The field is left out -- not known.
func TestExclusionListsSurviveAFailedPendingRead(t *testing.T) {
	m := &mockStore{usageRebuildPendingErr: errors.New("read usage rebuild request: connection reset")}
	srv := newTestServer(m, nil)

	rec := httptest.NewRecorder()
	srv.handleListExcludedAccounts(rec, adminJSON(http.MethodGet, "/api/admin/excluded-accounts", ""))
	assertUsageRebuildPending(t, "admin excluded accounts", rec, nil)

	rec = serveSelfExclusion(srv, http.MethodGet, "/api/self-exclusions/billing-accounts", "")
	assertUsageRebuildPending(t, "self exclusions", rec, nil)
}
