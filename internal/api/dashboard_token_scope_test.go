package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// The dashboard read API takes a bearer token on the same rule as the Open API
// (#702): tokens a person created for reading (web, cli_read), and no cli_read
// token of an administrator. The upload token `cctrace init` stores in plaintext
// used to pass here, and for an administrator that meant /api/admin/* too.
func TestDashboardReadTokenFollowsOpenAPIRule(t *testing.T) {
	mgr, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	admin := &store.DashboardUser{ID: 1, Email: "admin@ex.com", Role: "admin", IsActive: true}
	m := &mockStore{
		// The unfiltered lookup accepts everything, as it did before the fix.
		getDashboardUserByAPITokenFn: func(context.Context, string) (*store.DashboardUser, error) {
			return admin, nil
		},
		getDashboardUserByOpenAPITokenFn: func(_ context.Context, token string) (*store.DashboardUser, error) {
			switch token {
			case "upload":
				return nil, store.ErrTokenNotOpenAPI
			case "cli-read-of-admin":
				return nil, store.ErrCLIReadTokenAdmin
			case "web":
				return &store.DashboardUser{ID: 7, Email: "alice@ex.com", Role: "user", IsActive: true}, nil
			case "web-inactive":
				return &store.DashboardUser{ID: 8, Email: "bob@ex.com", Role: "user", IsActive: false}, nil
			}
			return nil, errors.New("not found")
		},
	}
	srv := NewServer(m, mgr)
	get := func(target, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	t.Run("upload token is refused with a hint", func(t *testing.T) {
		rec := get("/api/admin/users", "upload")
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"code":"ingestion_token"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("cli_read token of an administrator is refused", func(t *testing.T) {
		rec := get("/api/admin/users", "cli-read-of-admin")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"admin_read_token_forbidden"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("web token is accepted", func(t *testing.T) {
		rec := get("/api/auth/me", "web")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "alice@ex.com") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("inactive owner is refused", func(t *testing.T) {
		if rec := get("/api/auth/me", "web-inactive"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("browser cookie is unaffected", func(t *testing.T) {
		access, err := mgr.GenerateAccessToken(1, "admin@ex.com", "admin", "", "", false)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: access})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "admin@ex.com") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}
