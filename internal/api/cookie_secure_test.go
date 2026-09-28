package api

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestAuthCookieSecureTransport(t *testing.T) {
	for _, tc := range []struct {
		name, env, forwarded string
		tls, want            bool
	}{
		{name: "plain HTTP"},
		{name: "TLS", tls: true, want: true},
		{name: "forwarded HTTPS", forwarded: "https", want: true},
		{name: "forced HTTPS", env: "1", want: true},
		{name: "force only accepts 1", env: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COOKIE_SECURE", tc.env)
			mgr, err := auth.NewJWTManager(strings.Repeat("s", 32))
			if err != nil {
				t.Fatal(err)
			}
			user := &store.DashboardUser{ID: 7, Email: "user@example.com", IsActive: true}
			srv := NewServer(&mockStore{getDashboardUserByIDFn: func(context.Context, int64) (*store.DashboardUser, error) { return user, nil }}, mgr).WithCookieSecure(os.Getenv("COOKIE_SECURE") == "1")
			req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
			// CSRF runs before these handlers; a same-origin request is required
			// or the check rejects it with 403 before the behaviour under test.
			req.Header.Set("Origin", "http://"+req.Host)
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			req.Header.Set("X-Forwarded-Proto", tc.forwarded)
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			rec := httptest.NewRecorder()
			srv.setAuthCookies(rec, req, user)
			if len(rec.Result().Cookies()) != 2 {
				t.Fatal("missing login cookies")
			}
			for _, cookie := range rec.Result().Cookies() {
				if cookie.Secure != tc.want {
					t.Errorf("login %s Secure=%v, want %v", cookie.Name, cookie.Secure, tc.want)
				}
				if cookie.Name == "cctrace_refresh" {
					req.AddCookie(cookie)
				}
			}
			rec = httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
				t.Fatalf("refresh failed: %d %s", rec.Code, rec.Body.String())
			}
			if cookie := rec.Result().Cookies()[0]; cookie.Secure != tc.want {
				t.Errorf("refresh Secure=%v, want %v", cookie.Secure, tc.want)
			}
		})
	}
}
