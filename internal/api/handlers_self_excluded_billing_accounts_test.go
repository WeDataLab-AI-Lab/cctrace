package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/ingestblock"
	"cctrace/internal/store"
)

// selfExclusionStore observes openai/acct-mine (free), openai/acct-shared
// (shared) and anthropic/acct-admin (excluded by an admin) in the caller's data.
type selfExclusionStore struct {
	*mockStore
	scope        [3]string
	selfExcluded []string
	selfRemoved  []string
	removeOK     bool
	usedBy       [3]string
	usedByOthers bool
	changeErr    error
	// extra is appended to the observed accounts.
	extra []store.ObservedBillingAccount
}

func (s *selfExclusionStore) BillingAccountUsedByOthers(_ context.Context, provider, accountID, profileEmail, userID string) (bool, error) {
	s.usedBy = [3]string{provider + "/" + accountID, profileEmail, userID}
	return s.usedByOthers, nil
}

func (s *selfExclusionStore) ListObservedBillingAccounts(_ context.Context, profileEmail, userID, actor string) ([]store.ObservedBillingAccount, error) {
	s.scope = [3]string{profileEmail, userID, actor}
	return append([]store.ObservedBillingAccount{
		{BillingProvider: "openai", AccountID: "acct-mine"},
		{BillingProvider: "openai", AccountID: "acct-shared", Shared: true},
		{BillingProvider: "anthropic", AccountID: "acct-admin", Excluded: true},
	}, s.extra...), nil
}

func (s *selfExclusionStore) SelfExcludeBillingAccount(_ context.Context, provider, accountID, _, actor string) error {
	s.selfExcluded = append(s.selfExcluded, provider+"/"+accountID+" by "+actor)
	return s.changeErr
}

func (s *selfExclusionStore) RemoveSelfExcludedBillingAccount(_ context.Context, provider, accountID, actor string) (bool, error) {
	s.selfRemoved = append(s.selfRemoved, provider+"/"+accountID+" by "+actor)
	return s.removeOK || s.changeErr != nil, s.changeErr
}

func selfUser() *auth.DashboardUser {
	return &auth.DashboardUser{Role: "user", Email: "me@example.test", CctraceUserID: "u-me"}
}

func serveSelfExclusion(srv *Server, method, url, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r.Header.Set("Origin", "http://"+r.Host)
	r = r.WithContext(auth.WithUser(r.Context(), selfUser()))
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, r)
	return rec
}

// A plain user reads the accounts in their own data, scoped the way every other
// read of theirs is.
func TestSelfExclusion_listIsScopedToTheCaller(t *testing.T) {
	m := &selfExclusionStore{mockStore: &mockStore{}}
	srv := newTestServer(m, nil)
	srv.useUserIDAccessControl = true

	rec := serveSelfExclusion(srv, http.MethodGet, "/api/self-exclusions/billing-accounts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if m.scope != [3]string{"", "u-me", "me@example.test"} {
		t.Errorf("store scope = %q, want the caller's user id and email", m.scope)
	}
	var got struct {
		Accounts []store.ObservedBillingAccount `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Accounts) != 3 {
		t.Fatalf("body = %s (%v), want the three observed accounts", rec.Body.String(), err)
	}
}

// Only an account in the caller's own data, not shared and not already excluded,
// can be self-excluded. Anything else would let a user hide someone else's data
// or take over an admin's entry.
func TestSelfExclusion_excludeOnlyWhatIsTheirsToExclude(t *testing.T) {
	for _, tc := range []struct {
		name, account string
		want          int
	}{
		{"own account", `"openai","account_id":"acct-mine"`, http.StatusOK},
		{"not observed", `"openai","account_id":"acct-someone-else"`, http.StatusNotFound},
		{"shared", `"openai","account_id":"acct-shared"`, http.StatusForbidden},
		{"already excluded", `"anthropic","account_id":"acct-admin"`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &selfExclusionStore{mockStore: &mockStore{}}
			srv := newTestServer(m, nil)
			rec := serveSelfExclusion(srv, http.MethodPost, "/api/self-exclusions/billing-accounts",
				`{"billing_provider":`+tc.account+`}`)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.want, rec.Body.String())
			}
			wantCalls := 0
			if tc.want == http.StatusOK {
				wantCalls = 1
			}
			if len(m.selfExcluded) != wantCalls {
				t.Fatalf("store exclusions = %v, want %d", m.selfExcluded, wantCalls)
			}
		})
	}
}

// A POST that ran past the server's write timeout is resent by the browser, and
// the resend finds the exclusion it already made. That is success, not a
// conflict: showing "already excluded" for the caller's own exclusion reads as a
// failure. Someone else's exclusion stays a conflict.
func TestSelfExclusion_excludingAgainIsIdempotentForTheCallersOwn(t *testing.T) {
	m := &selfExclusionStore{mockStore: &mockStore{}, extra: []store.ObservedBillingAccount{
		{BillingProvider: "openai", AccountID: "acct-done", Excluded: true, SelfRegistered: true},
	}}
	srv := newTestServer(m, nil)
	rec := serveSelfExclusion(srv, http.MethodPost, "/api/self-exclusions/billing-accounts",
		`{"billing_provider":"openai","account_id":"acct-done"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the caller's own existing exclusion (%s)", rec.Code, rec.Body.String())
	}
	if len(m.selfExcluded) != 0 {
		t.Errorf("store exclusions = %v, want none: nothing changes", m.selfExcluded)
	}
}

// Removing is limited to the caller's own registrations; the store decides, and
// a refusal is a 403 rather than a silent success.
func TestSelfExclusion_removeOnlyTheirOwnRegistration(t *testing.T) {
	for _, removeOK := range []bool{true, false} {
		m := &selfExclusionStore{mockStore: &mockStore{}, removeOK: removeOK}
		srv := newTestServer(m, nil)
		rec := serveSelfExclusion(srv, http.MethodDelete,
			"/api/self-exclusions/billing-accounts?billing_provider=openai&account_id=acct-mine", "")
		want := map[bool]int{true: http.StatusOK, false: http.StatusForbidden}[removeOK]
		if rec.Code != want {
			t.Fatalf("removeOK=%v status = %d, want %d (%s)", removeOK, rec.Code, want, rec.Body.String())
		}
		if len(m.selfRemoved) != 1 || m.selfRemoved[0] != "openai/acct-mine by me@example.test" {
			t.Fatalf("store removals = %v, want the caller's own key", m.selfRemoved)
		}
	}
}

// A self exclusion reaches ingest at once, the same as an admin's.
func TestSelfExclusion_reloadsTheIngestBlocklist(t *testing.T) {
	excluded := false
	loader := func(context.Context) (*ingestblock.Sets, error) {
		sets := &ingestblock.Sets{ExcludedAccounts: map[string]bool{}}
		if excluded {
			sets.ExcludedAccounts[ingestblock.AccountKey("openai", "acct-mine")] = true
		}
		return sets, nil
	}
	m := &selfExclusionStore{mockStore: &mockStore{}, removeOK: true}
	bl := ingestblock.New()
	srv := newTestServer(m, nil).WithIngestBlocklist(bl).WithIngestBlocklistLoader(loader)

	excluded = true
	rec := serveSelfExclusion(srv, http.MethodPost, "/api/self-exclusions/billing-accounts",
		`{"billing_provider":"openai","account_id":"acct-mine"}`)
	if rec.Code != http.StatusOK || !bl.AccountExcluded("openai", "acct-mine") {
		t.Fatalf("status = %d, refused at ingest = %v; want 200 and refused", rec.Code, bl.AccountExcluded("openai", "acct-mine"))
	}

	excluded = false
	rec = serveSelfExclusion(srv, http.MethodDelete,
		"/api/self-exclusions/billing-accounts?billing_provider=openai&account_id=acct-mine", "")
	if rec.Code != http.StatusOK || bl.AccountExcluded("openai", "acct-mine") {
		t.Fatalf("status = %d, refused at ingest = %v; want 200 and accepted again", rec.Code, bl.AccountExcluded("openai", "acct-mine"))
	}
}

// The routes carry the dashboard's CSRF guard like every other write.
func TestSelfExclusion_requiresTheCSRFHeader(t *testing.T) {
	m := &selfExclusionStore{mockStore: &mockStore{}}
	srv := newTestServer(m, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/self-exclusions/billing-accounts",
		strings.NewReader(`{"billing_provider":"openai","account_id":"acct-mine"}`))
	r = r.WithContext(auth.WithUser(r.Context(), selfUser()))
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden || len(m.selfExcluded) != 0 {
		t.Fatalf("status = %d, exclusions = %v; want 403 and none", rec.Code, m.selfExcluded)
	}
}

// An account on someone else's session records bills them too -- a team account
// whose quota readings carry no address, or one a user planted a record for.
// Either way it is not the caller's to hide from everyone.
func TestSelfExclusion_refusesAnAccountOthersUse(t *testing.T) {
	m := &selfExclusionStore{mockStore: &mockStore{}, usedByOthers: true}
	srv := newTestServer(m, nil)
	srv.useUserIDAccessControl = true
	rec := serveSelfExclusion(srv, http.MethodPost, "/api/self-exclusions/billing-accounts",
		`{"billing_provider":"openai","account_id":"acct-mine"}`)
	if rec.Code != http.StatusForbidden || len(m.selfExcluded) != 0 {
		t.Fatalf("status = %d, exclusions = %v; want 403 and none", rec.Code, m.selfExcluded)
	}
	if m.usedBy != [3]string{"openai/acct-mine", "", "u-me"} {
		t.Errorf("checked %q, want the account against the caller's own scope", m.usedBy)
	}
}

// Every accepted change rebuilds derived tables for everyone, so one user may
// not ask for them in a loop. Exclude and include share one budget: alternating
// them is the loop.
func TestSelfExclusion_changesAreRateLimitedPerUser(t *testing.T) {
	m := &selfExclusionStore{mockStore: &mockStore{}, removeOK: true}
	srv := newTestServer(m, nil)

	codes := []int{}
	for i := 0; i < selfExclusionBurst+1; i++ {
		var rec *httptest.ResponseRecorder
		if i%2 == 0 {
			rec = serveSelfExclusion(srv, http.MethodPost, "/api/self-exclusions/billing-accounts",
				`{"billing_provider":"openai","account_id":"acct-mine"}`)
		} else {
			rec = serveSelfExclusion(srv, http.MethodDelete,
				"/api/self-exclusions/billing-accounts?billing_provider=openai&account_id=acct-mine", "")
		}
		codes = append(codes, rec.Code)
	}
	if last := codes[len(codes)-1]; last != http.StatusTooManyRequests {
		t.Fatalf("statuses = %v, want the last one refused with 429", codes)
	}
	if first := codes[0]; first != http.StatusOK {
		t.Fatalf("statuses = %v, want the first one accepted", codes)
	}

	// Reading the list is not a change and stays unlimited.
	if rec := serveSelfExclusion(srv, http.MethodGet, "/api/self-exclusions/billing-accounts", ""); rec.Code != http.StatusOK {
		t.Fatalf("list status = %d after the change budget ran out, want 200", rec.Code)
	}
}

// The reason is free text from a non-admin and lands in the audit log. It is
// bounded on input and quoted in the log, so it cannot forge a second log line.
func TestSelfExclusion_reasonIsBoundedAndQuotedInTheAuditLog(t *testing.T) {
	m := &selfExclusionStore{mockStore: &mockStore{}}
	srv := newTestServer(m, nil)
	rec := serveSelfExclusion(srv, http.MethodPost, "/api/self-exclusions/billing-accounts",
		`{"billing_provider":"openai","account_id":"acct-mine","reason":"`+strings.Repeat("x", maxSelfExclusionReason+1)+`"}`)
	if rec.Code != http.StatusBadRequest || len(m.selfExcluded) != 0 {
		t.Fatalf("over-long reason: status = %d, exclusions = %v; want 400 and none", rec.Code, m.selfExcluded)
	}

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	rec = serveSelfExclusion(srv, http.MethodPost, "/api/self-exclusions/billing-accounts",
		`{"billing_provider":"openai","account_id":"acct-mine","reason":"personal\n[audit] action=forged"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(buf.String(), "\n[audit] action=forged") {
		t.Fatalf("the reason broke the audit line:\n%s", buf.String())
	}
}
