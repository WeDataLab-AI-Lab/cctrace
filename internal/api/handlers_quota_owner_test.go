package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func quotaOwnerRequest(method, target, body string, user *auth.DashboardUser) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != nil {
		req = req.WithContext(auth.WithUser(req.Context(), user))
	}
	return req
}

func TestQuotaBindingRequestUsesAuthenticatedOwner(t *testing.T) {
	var ownerID int64
	m := &mockStore{quotaBindingApplyFn: func(_ context.Context, id int64, provider, accountID string) (*store.QuotaAccountBinding, error) {
		ownerID = id
		return &store.QuotaAccountBinding{ID: 19, DashboardUserID: id, BillingProvider: provider, AccountID: accountID, Status: "pending"}, nil
	}}
	s := newTestServer(m, nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, quotaOwnerRequest(http.MethodPost, "/api/me/quota-bindings",
		`{"billing_provider":"anthropic","account_id":"acct-owner"}`,
		&auth.DashboardUser{ID: 7, Role: "user", Email: "owner@example.test", CctraceUserID: "other-user"}))
	if rec.Code != http.StatusOK || ownerID != 7 {
		t.Fatalf("status=%d ownerID=%d body=%s, want 200 and authenticated user 7", rec.Code, ownerID, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "pending" {
		t.Fatalf("binding response = %v, want pending", body)
	}
}

func TestQuotaBindingRequestRejectsClientSuppliedIdentity(t *testing.T) {
	calls := 0
	m := &mockStore{quotaBindingApplyFn: func(context.Context, int64, string, string) (*store.QuotaAccountBinding, error) {
		calls++
		return nil, nil
	}}
	s := newTestServer(m, nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, quotaOwnerRequest(http.MethodPost, "/api/me/quota-bindings",
		`{"billing_provider":"anthropic","account_id":"acct-owner","dashboard_user_id":8,"email":"other@example.test"}`,
		&auth.DashboardUser{ID: 7, Role: "user"}))
	if rec.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("status=%d store calls=%d body=%s, want 400 before store", rec.Code, calls, rec.Body.String())
	}
}

func TestAdminQuotaBindingApprovalRequiresAdminAndIndependentEvidence(t *testing.T) {
	adminCalls := 0
	m := &mockStore{quotaBindingApproveFn: func(_ context.Context, bindingID, adminID int64, method, reference string) (*store.QuotaAccountBinding, error) {
		adminCalls++
		if bindingID != 19 || adminID != 2 || method != "provider_console" || reference != "VERIFY-42" {
			t.Fatalf("approve args = %d/%d/%q/%q", bindingID, adminID, method, reference)
		}
		return &store.QuotaAccountBinding{ID: bindingID, DashboardUserID: 7, BillingProvider: "anthropic", AccountID: "acct-owner", Status: "approved"}, nil
	}}
	s := newTestServer(m, nil)
	userRec := httptest.NewRecorder()
	s.mux.ServeHTTP(userRec, quotaOwnerRequest(http.MethodPost, "/api/admin/quota-bindings/19/approve",
		`{"verification_method":"provider_console","verification_reference":"VERIFY-42","verified_externally":true}`,
		&auth.DashboardUser{ID: 7, Role: "user"}))
	if userRec.Code != http.StatusForbidden || adminCalls != 0 {
		t.Fatalf("non-admin status=%d calls=%d", userRec.Code, adminCalls)
	}

	adminRec := httptest.NewRecorder()
	s.mux.ServeHTTP(adminRec, quotaOwnerRequest(http.MethodPost, "/api/admin/quota-bindings/19/approve",
		`{"verification_method":"provider_console","verification_reference":"VERIFY-42"}`,
		&auth.DashboardUser{ID: 2, Role: "admin"}))
	if adminRec.Code != http.StatusBadRequest || adminCalls != 0 {
		t.Fatalf("approval without explicit confirmation status=%d calls=%d", adminRec.Code, adminCalls)
	}

	adminRec = httptest.NewRecorder()
	s.mux.ServeHTTP(adminRec, quotaOwnerRequest(http.MethodPost, "/api/admin/quota-bindings/19/approve",
		`{"verification_method":"provider_console","verification_reference":"VERIFY-42","verified_externally":true}`,
		&auth.DashboardUser{ID: 2, Role: "admin"}))
	if adminRec.Code != http.StatusOK || adminCalls != 1 {
		t.Fatalf("approved status=%d calls=%d body=%s", adminRec.Code, adminCalls, adminRec.Body.String())
	}
}

func TestQuotaOwnerSamplesRequirePerUserIngestTokenAndStripIdentity(t *testing.T) {
	var ownerID int64
	var captured []*store.QuotaOwnerSample
	m := &mockStore{
		getDashboardUserByAPITokenFn: func(_ context.Context, token string) (*store.DashboardUser, error) {
			if token != "owner-upload-token" {
				t.Fatalf("resolved token = %q", token)
			}
			return &store.DashboardUser{ID: 7, Role: "user", IsActive: true}, nil
		},
		quotaOwnerSamplesFn: func(_ context.Context, id int64, samples []*store.QuotaOwnerSample) (int, error) {
			ownerID = id
			captured = samples
			return len(samples), nil
		},
	}
	pass := func(next http.Handler) http.Handler { return next }
	s := newTestServer(m, nil, pass)
	body := `{"samples":[{"billing_provider":"anthropic","account_id":"acct-owner","window_key":"session","sampled_at":"2026-10-09T00:00:00Z","used_pct":42,"window_minutes":300,"attribution":"observed"}]}`
	globalRec := httptest.NewRecorder()
	globalReq := quotaOwnerRequest(http.MethodPost, "/api/me/quota-usage/samples", body, nil)
	s.mux.ServeHTTP(globalRec, globalReq)
	if globalRec.Code != http.StatusForbidden {
		t.Fatalf("global key/anonymous sample status = %d, want 403", globalRec.Code)
	}

	userReq := quotaOwnerRequest(http.MethodPost, "/api/me/quota-usage/samples", body, nil)
	userReq = userReq.WithContext(auth.ContextWithIngestToken(userReq.Context(), "owner-upload-token"))
	userRec := httptest.NewRecorder()
	s.mux.ServeHTTP(userRec, userReq)
	if userRec.Code != http.StatusOK || ownerID != 7 || len(captured) != 1 {
		t.Fatalf("user sample status=%d owner=%d captured=%d body=%s", userRec.Code, ownerID, len(captured), userRec.Body.String())
	}
	if captured[0].BillingProvider != "anthropic" || captured[0].AccountID != "acct-owner" || captured[0].Attribution != "observed" {
		t.Fatalf("sample identity = %+v", captured[0])
	}

	bad := `{"samples":[{"billing_provider":"anthropic","account_id":"acct-owner","profile_email":"victim@example.test","window_key":"session","sampled_at":"2026-10-09T00:00:00Z","used_pct":42,"window_minutes":300,"attribution":"observed"}]}`
	badReq := quotaOwnerRequest(http.MethodPost, "/api/me/quota-usage/samples", bad, nil)
	badReq = badReq.WithContext(auth.ContextWithIngestToken(badReq.Context(), "owner-upload-token"))
	badRec := httptest.NewRecorder()
	s.mux.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("identity-bearing owner sample status=%d body=%s", badRec.Code, badRec.Body.String())
	}
}

func TestQuotaUsageReadScopesToDashboardUser(t *testing.T) {
	var ownerID int64
	m := &mockStore{quotaUsageFn: func(_ context.Context, id int64, from, to, readAt time.Time) ([]*store.QuotaUsageAccount, error) {
		ownerID = id
		if from.IsZero() || to.IsZero() || readAt.IsZero() {
			t.Fatal("quota usage did not use a bounded, server-dated range")
		}
		return []*store.QuotaUsageAccount{}, nil
	}}
	s := newTestServer(m, nil)
	req := quotaOwnerRequest(http.MethodGet, "/api/me/quota-usage?from=2026-10-08T00:00:00Z&to=2026-10-09T00:00:00Z", "", &auth.DashboardUser{ID: 7, Role: "user"})
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || ownerID != 7 {
		t.Fatalf("usage status=%d owner=%d body=%s", rec.Code, ownerID, rec.Body.String())
	}
}

func TestQuotaUsageComparisonScopesToDashboardUserAndValidatesWindow(t *testing.T) {
	var ownerID int64
	calls := 0
	m := &mockStore{quotaComparisonFn: func(_ context.Context, id int64, provider, accountID string, minutes, limit int, now time.Time) (*store.QuotaWindowComparison, error) {
		calls++
		ownerID = id
		if provider != "openai" || accountID != "acct-owner" || minutes != 10080 || limit != 0 || now.IsZero() {
			t.Fatalf("comparison args = %d/%q/%q/%d/%d/%v", id, provider, accountID, minutes, limit, now)
		}
		return &store.QuotaWindowComparison{BillingProvider: provider, AccountID: accountID, WindowMinutes: minutes, History: []store.QuotaComparisonWindow{}, Quantiles: []store.QuotaQuantilePoint{}, Source: "client_reported"}, nil
	}}
	s := newTestServer(m, nil)
	user := &auth.DashboardUser{ID: 7, Role: "user"}
	bad := httptest.NewRecorder()
	s.mux.ServeHTTP(bad, quotaOwnerRequest(http.MethodGet, "/api/me/quota-usage/window-comparison?billing_provider=openai&account_id=acct-owner&window_minutes=60", "", user))
	if bad.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("invalid window status=%d store calls=%d", bad.Code, calls)
	}
	good := httptest.NewRecorder()
	s.mux.ServeHTTP(good, quotaOwnerRequest(http.MethodGet, "/api/me/quota-usage/window-comparison?billing_provider=openai&account_id=acct-owner&window_minutes=10080", "", user))
	if good.Code != http.StatusOK || calls != 1 || ownerID != 7 {
		t.Fatalf("comparison status=%d calls=%d owner=%d body=%s", good.Code, calls, ownerID, good.Body.String())
	}
}
