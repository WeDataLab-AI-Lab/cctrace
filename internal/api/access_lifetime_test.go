package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestAccessCookieLifetimeAndRefresh(t *testing.T) {
	mgr, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	user := &store.DashboardUser{ID: 7, Email: "user@example.com", IsActive: true}
	srv := NewServer(&mockStore{getDashboardUserByIDFn: func(context.Context, int64) (*store.DashboardUser, error) { return user, nil }}, mgr)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	// CSRF runs before these handlers; a same-origin request is required
	// or the check rejects it with 403 before the behaviour under test.
	req.Header.Set("Origin", "http://"+req.Host)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	srv.setAuthCookies(rec, req, user)
	checkAccess := func(c *http.Cookie) {
		t.Helper()
		if c.MaxAge != 900 {
			t.Errorf("access cookie MaxAge=%d, want 900", c.MaxAge)
		}
		claims, err := mgr.ValidateToken(c.Value)
		if err != nil {
			t.Fatal(err)
		}
		if got := claims.ExpiresAt.Sub(claims.IssuedAt.Time); got != 15*time.Minute {
			t.Errorf("access JWT lifetime=%v, want 15m", got)
		}
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "cctrace_token" {
			checkAccess(c)
		}
		if c.Name == "cctrace_refresh" {
			if c.MaxAge != 604800 {
				t.Errorf("refresh MaxAge=%d", c.MaxAge)
			}
			req.AddCookie(c)
		}
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("refresh failed: %d %s", rec.Code, rec.Body.String())
	}
	checkAccess(rec.Result().Cookies()[0])
	user.IsActive = false
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("disabled user refreshed: %d %s", rec.Code, rec.Body.String())
	}
}
