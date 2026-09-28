package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
)

func TestResolveUserAccessParamsUsesCctraceUserIDWhenEnabled(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	user := &auth.DashboardUser{
		Email:         "login@example.com",
		CctraceUserID: "otel-user-1",
	}

	profileEmail, loginEmail, userID := srv.resolveUserAccessParams(user)

	if profileEmail != "" || loginEmail != "" || userID != "otel-user-1" {
		t.Fatalf("access params = (%q, %q, %q), want user_id only", profileEmail, loginEmail, userID)
	}
}

func TestResolveUserAccessParamsUsesNoAccessSentinelWhenUserIDMissing(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	user := &auth.DashboardUser{
		Email: "login@example.com",
	}

	profileEmail, loginEmail, userID := srv.resolveUserAccessParams(user)

	if profileEmail != "" || loginEmail != "" || userID != noAccessSentinel {
		t.Fatalf("access params = (%q, %q, %q), want no-access sentinel", profileEmail, loginEmail, userID)
	}
}

func TestProjectRuleAccessReturnsNotFoundWhenRestrictedUserCannotSeeRule(t *testing.T) {
	var capturedUserID string
	m := &mockStore{
		projectRuleVisibleToFn: func(ctx context.Context, ruleID int64, userID, profileEmail string) (bool, error) {
			capturedUserID = userID
			if ruleID != 42 {
				t.Fatalf("ruleID = %d, want 42", ruleID)
			}
			return false, nil
		},
	}
	srv := newTestServer(m, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/project-rules/42", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{
		Role:          "user",
		Email:         "login@example.com",
		CctraceUserID: "otel-user-1",
	}))
	rec := httptest.NewRecorder()

	allowed := srv.enforceProjectRuleAccess(rec, req, 42)

	if allowed {
		t.Fatal("restricted user should not be allowed to access an invisible rule")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if capturedUserID != "otel-user-1" {
		t.Fatalf("visible check userID = %q, want otel-user-1", capturedUserID)
	}
}
