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

func TestOrganizationInsightsIsAdminOnlyAndSanitized(t *testing.T) {
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(_ context.Context, token string) (*store.DashboardUser, error) {
			switch token {
			case "admin":
				return &store.DashboardUser{Role: "admin", IsActive: true}, nil
			case "user":
				return &store.DashboardUser{Role: "user", IsActive: true, CctraceUserID: "alice"}, nil
			default:
				return nil, context.Canceled
			}
		},
		organizationInsightsFn: func(_ context.Context, since, until time.Time, minUsers int64) (*store.OrganizationInsights, error) {
			if minUsers != 5 || !since.Before(until) {
				t.Fatalf("query = %v..%v, minimum=%d", since, until, minUsers)
			}
			return &store.OrganizationInsights{
				Available:    true,
				ActiveUsers:  8,
				MinimumUsers: 5,
				Models: []store.OrganizationModelInsight{{
					Model: "sonnet", ContributorCount: 7, InputTokens: 100, OutputTokens: 20, CostUSD: 1.5,
				}},
				Tools: []store.OrganizationToolInsight{{
					ToolName: "Bash", ContributorCount: 6, UseCount: 30, SuccessCount: 28, FailCount: 2,
				}},
			}, nil
		},
	}
	srv := newTestServer(m, nil)

	user := openAPIRequest(t, srv, http.MethodGet, "/api/open/v1/organization-insights", "Bearer user")
	if user.Code != http.StatusForbidden {
		t.Fatalf("user status = %d, want 403; body=%s", user.Code, user.Body.String())
	}

	admin := openAPIRequest(t, srv, http.MethodGet,
		"/api/open/v1/organization-insights?since=2026-08-01T00:00:00Z&until=2026-08-08T00:00:00Z", "Bearer admin")
	if admin.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200; body=%s", admin.Code, admin.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(admin.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, body, "available", "active_users", "minimum_users", "models", "tools")
	for _, forbidden := range []string{"user_id", "email", "session_id", "prompt", "path"} {
		if strings.Contains(admin.Body.String(), forbidden) {
			t.Errorf("response contains %q: %s", forbidden, admin.Body.String())
		}
	}
}

func TestOrganizationInsightsSuppressesSmallCohorts(t *testing.T) {
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: apiUserTokenLookup("admin", &store.DashboardUser{Role: "admin", IsActive: true}),
		organizationInsightsFn: func(context.Context, time.Time, time.Time, int64) (*store.OrganizationInsights, error) {
			return &store.OrganizationInsights{Available: false, MinimumUsers: 5}, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/open/v1/organization-insights", "Bearer admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	assertJSONKeysExactly(t, body, "available", "minimum_users")
}

func TestDashboardOrganizationInsightsRequiresAdmin(t *testing.T) {
	called := false
	srv := newTestServer(&mockStore{organizationInsightsFn: func(context.Context, time.Time, time.Time, int64) (*store.OrganizationInsights, error) {
		called = true
		return &store.OrganizationInsights{Available: true, ActiveUsers: 5, MinimumUsers: 5}, nil
	}}, nil)

	request := httptest.NewRequest(http.MethodGet, "/api/admin/organization-insights", nil)
	request = request.WithContext(auth.WithUser(request.Context(), &auth.DashboardUser{Role: "user"}))
	forbidden := httptest.NewRecorder()
	srv.handleDashboardOrganizationInsights(forbidden, request)
	if forbidden.Code != http.StatusForbidden || called {
		t.Fatalf("non-admin status=%d called=%v", forbidden.Code, called)
	}

	request = request.WithContext(auth.WithUser(request.Context(), &auth.DashboardUser{Role: "admin"}))
	allowed := httptest.NewRecorder()
	srv.handleDashboardOrganizationInsights(allowed, request)
	if allowed.Code != http.StatusOK || !called {
		t.Fatalf("admin status=%d called=%v body=%s", allowed.Code, called, allowed.Body.String())
	}
}

// The spec and the dashboard's OrganizationInsights type both carry
// typed_turn_count, the denominator for task coverage, but the handler built its
// response map without it.
func TestOrganizationInsightsIncludesTypedTurnCount(t *testing.T) {
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: apiUserTokenLookup("admin", &store.DashboardUser{Role: "admin", IsActive: true}),
		organizationInsightsFn: func(context.Context, time.Time, time.Time, int64) (*store.OrganizationInsights, error) {
			return &store.OrganizationInsights{
				Available: true, ActiveUsers: 6, MinimumUsers: 5, TypedTurnCount: 42,
				Tasks: []store.OrganizationTaskInsight{{TaskType: "debugging", ContributorCount: 5, PromptCount: 10}},
			}, nil
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/open/v1/organization-insights", "Bearer admin")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["typed_turn_count"] != float64(42) {
		t.Fatalf("typed_turn_count = %v; body=%s", body["typed_turn_count"], rec.Body.String())
	}
}
