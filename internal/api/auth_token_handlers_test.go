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

func authenticatedTokenRequest(method, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/auth/api-tokens", strings.NewReader(body))
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	ctx := auth.WithUser(req.Context(), &auth.DashboardUser{ID: 42, Email: "user@example.com", Role: "user"})
	return req.WithContext(ctx)
}

func TestHandleListOwnAPITokensReturnsMetadataOnly(t *testing.T) {
	createdAt := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	srv := &Server{store: &mockStore{
		listDashboardUserAPITokensFn: func(_ context.Context, userID int64) ([]*store.DashboardAPIToken, error) {
			if userID != 42 {
				t.Fatalf("expected user id 42, got %d", userID)
			}
			return []*store.DashboardAPIToken{{
				ID: 7, Name: "CI", TokenHint: "cct_…123456", CreatedVia: "web", CreatedAt: createdAt,
			}}, nil
		},
	}}
	rec := httptest.NewRecorder()

	srv.handleListOwnAPITokens(rec, authenticatedTokenRequest(http.MethodGet, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "cct_full-secret") {
		t.Fatal("list response exposed a token secret")
	}
	var body []*store.DashboardAPIToken
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body[0].Name != "CI" || body[0].CreatedVia != "web" {
		t.Fatalf("unexpected token metadata: %+v", body)
	}
}

func TestHandleCreateOwnAPITokenCreatesNamedWebToken(t *testing.T) {
	var storedSecret string
	srv := &Server{store: &mockStore{
		createDashboardUserAPITokenFn: func(_ context.Context, userID int64, name, secret, createdVia string, expiresAt *time.Time) (*store.DashboardAPIToken, error) {
			if userID != 42 || name != "CI deployment" || createdVia != "web" {
				t.Fatalf("unexpected create arguments: user=%d name=%q via=%q", userID, name, createdVia)
			}
			if expiresAt != nil {
				t.Fatalf("expected unlimited token, got expiration %v", expiresAt)
			}
			storedSecret = secret
			return &store.DashboardAPIToken{ID: 9, Name: name, CreatedVia: createdVia}, nil
		},
	}}
	rec := httptest.NewRecorder()

	srv.handleCreateOwnAPIToken(rec, authenticatedTokenRequest(http.MethodPost, `{"name":" CI deployment "}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(storedSecret, "cct_") {
		t.Fatalf("expected cct_ token, got %q", storedSecret)
	}
	var body struct {
		APIToken string `json:"api_token"`
		ID       int64  `json:"id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ID != 9 || body.APIToken != storedSecret {
		t.Fatalf("unexpected create response: %+v", body)
	}
}

func TestHandleCreateOwnAPITokenAcceptsCustomExpiration(t *testing.T) {
	expiresAt := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	srv := &Server{store: &mockStore{
		createDashboardUserAPITokenFn: func(_ context.Context, _ int64, _ string, _ string, _ string, received *time.Time) (*store.DashboardAPIToken, error) {
			if received == nil || !received.Equal(expiresAt) {
				t.Fatalf("expected expiration %v, got %v", expiresAt, received)
			}
			return &store.DashboardAPIToken{ID: 10, Name: "Export", IsActive: true, ExpiresAt: received}, nil
		},
	}}
	body := `{"name":"Export","expiration_mode":"custom","expires_at":"` + expiresAt.Format(time.RFC3339) + `"}`
	rec := httptest.NewRecorder()

	srv.handleCreateOwnAPIToken(rec, authenticatedTokenRequest(http.MethodPost, body))

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleRotateOwnAPITokenPreservesRecord(t *testing.T) {
	var storedSecret string
	srv := &Server{store: &mockStore{
		rotateDashboardUserAPITokenFn: func(_ context.Context, userID, tokenID int64, secret string) (*store.DashboardAPIToken, error) {
			if userID != 42 || tokenID != 7 {
				t.Fatalf("unexpected rotate target: user=%d token=%d", userID, tokenID)
			}
			storedSecret = secret
			now := time.Now()
			return &store.DashboardAPIToken{ID: tokenID, Name: "CI", RotatedAt: &now}, nil
		},
	}}
	req := authenticatedTokenRequest(http.MethodPost, "")
	req.SetPathValue("id", "7")
	rec := httptest.NewRecorder()

	srv.handleRotateOwnAPIToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(storedSecret, "cct_") || !strings.Contains(rec.Body.String(), storedSecret) {
		t.Fatal("rotation did not return the newly stored secret")
	}
}

func TestHandleDeleteOwnAPITokenScopesByUserAndID(t *testing.T) {
	called := false
	srv := &Server{store: &mockStore{
		deleteDashboardUserAPITokenFn: func(_ context.Context, userID, tokenID int64) (bool, error) {
			called = userID == 42 && tokenID == 7
			return true, nil
		},
	}}
	req := authenticatedTokenRequest(http.MethodDelete, "")
	req.SetPathValue("id", "7")
	rec := httptest.NewRecorder()

	srv.handleDeleteOwnAPIToken(rec, req)

	if rec.Code != http.StatusOK || !called {
		t.Fatalf("expected scoped deletion, status=%d called=%v", rec.Code, called)
	}
}

func TestHandleUpdateOwnAPITokenChangesActiveMode(t *testing.T) {
	called := false
	srv := &Server{store: &mockStore{
		setDashboardUserTokenActiveFn: func(_ context.Context, userID, tokenID int64, active bool) (*store.DashboardAPIToken, error) {
			called = userID == 42 && tokenID == 7 && !active
			return &store.DashboardAPIToken{ID: tokenID, IsActive: active}, nil
		},
	}}
	req := authenticatedTokenRequest(http.MethodPatch, `{"is_active":false}`)
	req.SetPathValue("id", "7")
	rec := httptest.NewRecorder()

	srv.handleUpdateOwnAPIToken(rec, req)

	if rec.Code != http.StatusOK || !called {
		t.Fatalf("expected active-mode update, status=%d called=%v", rec.Code, called)
	}
}

func TestHandleUpdateOwnAPITokenSetsUnlimitedExpiration(t *testing.T) {
	called := false
	srv := &Server{store: &mockStore{
		setDashboardUserExpirationFn: func(_ context.Context, userID, tokenID int64, expiresAt *time.Time) (*store.DashboardAPIToken, error) {
			called = userID == 42 && tokenID == 7 && expiresAt == nil
			return &store.DashboardAPIToken{ID: tokenID, IsActive: true}, nil
		},
	}}
	req := authenticatedTokenRequest(http.MethodPatch, `{"expiration_mode":"unlimited"}`)
	req.SetPathValue("id", "7")
	rec := httptest.NewRecorder()

	srv.handleUpdateOwnAPIToken(rec, req)

	if rec.Code != http.StatusOK || !called {
		t.Fatalf("expected unlimited expiration update, status=%d called=%v", rec.Code, called)
	}
}

func TestHandleCreateOwnAPITokenValidatesNameAndCSRF(t *testing.T) {
	srv := &Server{store: &mockStore{}}

	badName := httptest.NewRecorder()
	srv.handleCreateOwnAPIToken(badName, authenticatedTokenRequest(http.MethodPost, `{"name":" "}`))
	if badName.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for blank name, got %d", badName.Code)
	}

	missingCSRF := authenticatedTokenRequest(http.MethodPost, `{"name":"CI"}`)
	missingCSRF.Header.Del("X-Requested-With")
	rec := httptest.NewRecorder()
	srv.handleCreateOwnAPIToken(rec, missingCSRF)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF header, got %d", rec.Code)
	}
}

func TestHandleListOwnAPITokensRequiresAuthentication(t *testing.T) {
	srv := &Server{store: &mockStore{}}
	rec := httptest.NewRecorder()

	srv.handleListOwnAPITokens(rec, httptest.NewRequest(http.MethodGet, "/api/auth/api-tokens", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
