package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginRateLimits(t *testing.T) {
	for _, path := range []string{"/api/auth/login", "/api/cli/auth", "/api/cli/read-token"} {
		t.Run(path, func(t *testing.T) {
			srv := newTestServer(&mockStore{}, nil)
			request := func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{"))
				// CSRF runs before these handlers; a same-origin request is required
				// or the check rejects it with 403 before the behaviour under test.
				req.Header.Set("Origin", "http://"+req.Host)
				req.Header.Set("X-Requested-With", "XMLHttpRequest")
				req.Header.Set("X-Requested-With", "XMLHttpRequest")
				srv.Handler().ServeHTTP(rec, req)
				return rec
			}
			for i := 0; i < 5; i++ {
				if rec := request(); rec.Code != http.StatusBadRequest {
					t.Fatalf("initial burst %d: got %d", i+1, rec.Code)
				}
			}
			if rec := request(); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("burst exhausted: got %d, want 429 with Retry-After", rec.Code)
			}
			time.Sleep(1100 * time.Millisecond)
			if rec := request(); rec.Code != http.StatusBadRequest {
				t.Fatalf("one token refilled: got %d", rec.Code)
			}
			if rec := request(); rec.Code != http.StatusTooManyRequests {
				t.Fatalf("refill must be 1 rps: got %d", rec.Code)
			}
		})
	}
}

// Both CLI password exchanges draw from one bucket, so adding the read-token
// endpoint did not give a password guesser a second budget.
func TestCLIPasswordEndpointsShareOneRateBudget(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	request := func(path string) int {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{")))
		return rec.Code
	}
	for i := 0; i < 5; i++ {
		if code := request("/api/cli/auth"); code != http.StatusBadRequest {
			t.Fatalf("burst %d: %d", i+1, code)
		}
	}
	if code := request("/api/cli/read-token"); code != http.StatusTooManyRequests {
		t.Fatalf("read-token after the shared burst: %d, want 429", code)
	}
}
