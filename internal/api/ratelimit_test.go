package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyncRateLimiter_AllowsWithinLimit(t *testing.T) {
	handler := SyncRateLimiter(10, 20)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Should allow burst of requests
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/sync", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
	}
}

func TestSyncRateLimiter_RejectsOverLimit(t *testing.T) {
	handler := SyncRateLimiter(1, 1)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First request uses the burst
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/sync", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rec.Code)
	}

	// Second request should be rate limited
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/sync", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429, got %d", rec.Code)
	}

	// Check Retry-After header
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After header")
	}

	// Check JSON body
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "rate limit exceeded" {
		t.Fatalf("expected 'rate limit exceeded', got %q", body["error"])
	}
}
