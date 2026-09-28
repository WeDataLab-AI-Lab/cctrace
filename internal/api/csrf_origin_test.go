package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestCSRFPreviouslyUnprotectedRoutes(t *testing.T) {
	jwt, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	access, err := jwt.GenerateAccessToken(1, "admin@example.com", "admin", "Admin", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := jwt.GenerateRefreshToken(1, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	routes := []struct{ method, path string }{
		{"POST", "/api/users/merge"}, {"DELETE", "/api/users/data"},
		{"POST", "/api/admin/task-types/backfill"},
		{"POST", "/api/project-rules/1/comments"},
		{"PATCH", "/api/project-rules/1/versions/1/change-reason"},
		{"POST", "/api/auth/refresh"},
	}
	cases := []struct {
		name, origin, referer, xhr string
		want                       int
	}{
		{"same host", "http://app.example.com", "", "XMLHttpRequest", 200},
		{"TLS proxy", "https://app.example.com", "", "XMLHttpRequest", 200},
		{"allowed", "https://dashboard.example.com", "", "XMLHttpRequest", 200},
		{"other port", "https://app.example.com:444", "", "XMLHttpRequest", 403},
		{"sibling", "https://evil.example.com", "", "XMLHttpRequest", 403},
		{"simple", "https://evil.example.com", "", "", 403},
		{"missing", "", "", "XMLHttpRequest", 403},
		{"referer", "", "https://app.example.com/dashboard", "XMLHttpRequest", 200},
		{"untrusted referer", "", "https://evil.example.com/dashboard", "XMLHttpRequest", 403},
		{"origin precedence", "null", "https://app.example.com/", "XMLHttpRequest", 403},
		{"header required", "https://app.example.com", "", "", 403},
	}
	for _, route := range routes {
		for _, tc := range cases {
			t.Run(route.path+"/"+tc.name, func(t *testing.T) {
				srv := NewServer(&mockStore{getDashboardUserByIDFn: func(context.Context, int64) (*store.DashboardUser, error) {
					return &store.DashboardUser{ID: 1, Email: "admin@example.com", Role: "admin", IsActive: true}, nil
				}}, jwt).WithAllowedOrigins([]string{"https://dashboard.example.com"})
				req := httptest.NewRequest(route.method, "http://app.example.com"+route.path, strings.NewReader(`{}`))
				req.Header.Set("Origin", tc.origin)
				req.Header.Set("Referer", tc.referer)
				req.Header.Set("X-Requested-With", tc.xhr)
				req.Header.Set("Content-Type", "text/plain")
				req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: access})
				req.AddCookie(&http.Cookie{Name: "cctrace_refresh", Value: refresh})
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
				}
				if tc.name == "missing" && !strings.Contains(rec.Body.String(), "Origin/Referer") {
					t.Error("missing diagnostic")
				}
			})
		}
	}
}

func TestCSRFOriginParsing(t *testing.T) {
	for _, tc := range []struct {
		origin, referer, host string
		want                  int
	}{
		{"https://app.example.com", "", "app.example.com", 204},
		{"http://app.example.com:8080", "", "app.example.com:8080", 204},
		{"https://app.example.com:443", "", "app.example.com", 403},
		{"https://app.example.com/path", "", "app.example.com", 403},
		{"https://app.example.com?", "", "app.example.com", 403},
		{"https://user@app.example.com", "", "app.example.com", 403},
		{"https://app.example.com.evil.test", "", "app.example.com", 403},
		{"https://app.example.com https://evil.test", "", "app.example.com", 403},
		{"file://app.example.com", "", "app.example.com", 403},
		{"null", "https://app.example.com", "app.example.com", 403},
		{"", "https://dashboard.example.com/path?query=x", "app.example.com", 204},
	} {
		t.Run(tc.origin+tc.referer+tc.host, func(t *testing.T) {
			srv := (&Server{}).WithAllowedOrigins([]string{"https://dashboard.example.com"})
			handler := srv.withCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
			req := httptest.NewRequest("PUT", "http://"+tc.host+"/", nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Referer", tc.referer)
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			req.Header.Set("X-Forwarded-Host", "evil.test")
			req.Header.Set("X-Forwarded-Proto", "https")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestCSRFDoesNotAffectTokenRoutes(t *testing.T) {
	authenticator := auth.New("test-ingest-key")
	srv := NewServer(&mockStore{getDashboardUserByAPITokenFn: apiUserTokenLookup("web-token", &store.DashboardUser{ID: 1, Role: "admin", IsActive: true})}, nil, authenticator.HTTPMiddleware)
	for _, tc := range []struct {
		method, path, token, body string
		want                      int
	}{
		{"POST", "/api/sync", "test-ingest-key", `{"records":[]}`, 200},
		{"POST", "/api/quota", "test-ingest-key", `{"profile_email":"one@example.test","five_hour_pct":42}`, 200},
		{"POST", "/api/quota-samples", "test-ingest-key", `{"samples":[{"billing_provider":"anthropic","account_id":"acct-1","window_key":"session","sampled_at":"2026-08-24T09:00:00Z","used_pct":42}]}`, 200},
		{"GET", "/api/open/v1/events", "web-token", "", 200},
	} {
		for _, origin := range []string{"", "https://evil.example.com"} {
			t.Run(tc.path+origin, func(t *testing.T) {
				req := httptest.NewRequest(tc.method, "http://app.example.com"+tc.path, strings.NewReader(tc.body))
				req.Header.Set("Authorization", "Bearer "+tc.token)
				req.Header.Set("Origin", origin)
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Fatalf("got %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
				}
			})
		}
	}
}
