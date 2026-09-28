package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/store"
)

// The migration backfills every existing dashboard_users.api_token into
// dashboard_api_tokens as the primary row, so on the day this ships every CLI token
// in the fleet would also open the documented read API. Nobody issued those tokens
// for that: they sit in plaintext in ~/.cctrace/profile.json on laptops, handed out
// by `cctrace init` so a machine could upload its sessions.
//
// created_via already records which is which -- 'api' for the CLI's primary token,
// 'web' for one a person created in Settings. The read API honours that.
func TestOpenAPIRejectsIngestionToken(t *testing.T) {
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(ctx context.Context, token string) (*store.DashboardUser, error) {
			return nil, store.ErrTokenNotOpenAPI
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer cli-token")
	rec := httptest.NewRecorder()
	newTestServer(m, nil).mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an ingestion-only token", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// The refusal has to say what to do about it. "unauthorized" alone sends someone
	// hunting for a typo in a token that is working perfectly well for its own job.
	if body["code"] != "ingestion_token" {
		t.Fatalf("code = %q, want ingestion_token", body["code"])
	}
	if body["error"] == "" || body["error"] == "unauthorized" {
		t.Fatalf("error = %q, want a message naming how to get a usable token", body["error"])
	}
}

// A token created in Settings works, and lands on the same read scope as before.
func TestOpenAPIAcceptsWebToken(t *testing.T) {
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(ctx context.Context, token string) (*store.DashboardUser, error) {
			return &store.DashboardUser{ID: 7, Email: "caller", Role: "user", IsActive: true}, nil
		},
		listSessionRecordsFn: func(ctx context.Context, f store.SessionRecordFilter) ([]*store.SessionRecord, error) {
			return nil, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/open/v1/events", nil)
	req.Header.Set("Authorization", "Bearer web-token")
	rec := httptest.NewRecorder()
	newTestServer(m, nil).mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a Settings-issued token: %s", rec.Code, rec.Body.String())
	}
}
