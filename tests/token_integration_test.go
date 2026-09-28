package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cctrace/internal/api"
	"cctrace/internal/auth"
	"cctrace/internal/store"

	tc "github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"

	"cctrace/internal/containertest"
)

// --- shared container (reused across token tests) ---

var (
	tokenPgOnce    sync.Once
	tokenPgStore   *store.PgStore
	tokenPgInitErr error
)

func acquireTokenStore(t *testing.T) *store.PgStore {
	t.Helper()
	ensureSyncDockerHost() // reuse docker host detection
	tokenPgOnce.Do(func() {
		ctx := context.Background()
		ctr, err := pgmod.Run(ctx,
			"timescale/timescaledb:latest-pg16",
			pgmod.WithDatabase("tokendb"),
			pgmod.WithUsername("test"),
			pgmod.WithPassword("test"),
			tc.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			tokenPgInitErr = err
			return
		}
		dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			tokenPgInitErr = err
			return
		}
		s, err := store.NewPgStore(ctx, dsn)
		if err != nil {
			tokenPgInitErr = err
			return
		}
		// Full migration — same as production
		if err := s.Migrate(ctx); err != nil {
			tokenPgInitErr = err
			return
		}
		tokenPgStore = s
	})
	if tokenPgInitErr != nil {
		containertest.SkipOrFail(t, "postgres container", tokenPgInitErr)
	}
	return tokenPgStore
}

func truncateTokenTables(t *testing.T, s *store.PgStore) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"dashboard_users", "session_records", "projects"} {
		if _, err := s.Pool().Exec(ctx, "TRUNCATE "+table+" CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
}

// newTokenServer creates a test server with API key auth + per-user token validation.
func newTokenServer(s *store.PgStore) (*httptest.Server, string) {
	apiKey := "test-global-api-key"
	tokenValidator := func(ctx context.Context, token string) (bool, error) {
		user, err := s.GetDashboardUserByApiToken(ctx, token)
		if err != nil {
			return false, nil
		}
		return user.IsActive, nil
	}
	authn := auth.New(apiKey, auth.WithTokenValidator(tokenValidator))
	jwtMgr, _ := auth.NewJWTManager("test-jwt-secret-that-is-at-least-32-bytes-long")
	srv := api.NewServer(s, jwtMgr, authn.HTTPMiddleware)
	return httptest.NewServer(srv.Handler()), apiKey
}

// --- tests ---

func TestTokenFullLifecycle(t *testing.T) {
	s := acquireTokenStore(t)
	truncateTokenTables(t, s)

	ts, apiKey := newTokenServer(s)
	defer ts.Close()

	ctx := context.Background()

	// Step 1: Create a dashboard user with cctrace_user_id
	hash, _ := bcrypt.GenerateFromPassword([]byte("testpass123"), 10)
	user := &store.DashboardUser{
		Email:              "alice@example.com",
		PasswordHash:       string(hash),
		Role:               "user",
		Name:               "Alice",
		CctraceUserID:      "alice",
		MustChangePassword: false,
	}
	user, err := s.CreateDashboardUser(ctx, user)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Step 2: CLI auth → should return api_token
	cliAuthBody, _ := json.Marshal(map[string]string{
		"user_id":  "alice",
		"password": "testpass123",
	})
	req, _ := http.NewRequest("POST", ts.URL+"/api/cli/auth", bytes.NewReader(cliAuthBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cli auth request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cli auth: expected 200, got %d", resp.StatusCode)
	}

	var authResp map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&authResp)

	apiToken, ok := authResp["api_token"].(string)
	if !ok || apiToken == "" {
		t.Fatalf("expected api_token in response, got %v", authResp)
	}
	if len(apiToken) < 20 || apiToken[:4] != "cct_" {
		t.Fatalf("invalid token format: %q", apiToken)
	}
	t.Logf("issued token: %s", apiToken[:12]+"...")

	// Step 3: Sync with per-user token succeeds
	syncBody, _ := json.Marshal(map[string]interface{}{"records": []interface{}{}})
	req, _ = http.NewRequest("POST", ts.URL+"/api/sync", bytes.NewReader(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sync request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sync with per-user token: expected 200, got %d", resp.StatusCode)
	}

	// Step 4: Re-auth returns same token (idempotent)
	req, _ = http.NewRequest("POST", ts.URL+"/api/cli/auth", bytes.NewReader(cliAuthBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("re-auth request: %v", err)
	}
	var authResp2 map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&authResp2)
	resp.Body.Close()

	if authResp2["api_token"] != apiToken {
		t.Fatalf("re-auth should return same token, got %q vs %q", authResp2["api_token"], apiToken)
	}

	// Step 5: Admin revokes the token
	err = s.SetDashboardUserApiToken(ctx, user.ID, "")
	if err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	// Step 6: Sync with revoked token fails (401)
	req, _ = http.NewRequest("POST", ts.URL+"/api/sync", bytes.NewReader(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sync after revoke: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sync after revoke: expected 401, got %d", resp.StatusCode)
	}

	// Step 7: Re-auth after revoke → new token issued
	req, _ = http.NewRequest("POST", ts.URL+"/api/cli/auth", bytes.NewReader(cliAuthBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("re-auth after revoke: %v", err)
	}
	var authResp3 map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&authResp3)
	resp.Body.Close()

	newToken, ok := authResp3["api_token"].(string)
	if !ok || newToken == "" {
		t.Fatalf("expected new api_token after revoke, got %v", authResp3)
	}
	if newToken == apiToken {
		t.Fatalf("new token should differ from revoked token")
	}

	// Step 8: Sync with new token succeeds
	req, _ = http.NewRequest("POST", ts.URL+"/api/sync", bytes.NewReader(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+newToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sync with new token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sync with new token: expected 200, got %d", resp.StatusCode)
	}
}

func TestTokenGlobalKeyStillWorks(t *testing.T) {
	s := acquireTokenStore(t)
	truncateTokenTables(t, s)

	ts, apiKey := newTokenServer(s)
	defer ts.Close()

	// Sync with global API key should still work (backward compat)
	syncBody, _ := json.Marshal(map[string]interface{}{"records": []interface{}{}})
	req, _ := http.NewRequest("POST", ts.URL+"/api/sync", bytes.NewReader(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sync with global key: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("global API key sync: expected 200, got %d", resp.StatusCode)
	}
}

func TestTokenInactiveUserBlocked(t *testing.T) {
	s := acquireTokenStore(t)
	truncateTokenTables(t, s)

	ts, apiKey := newTokenServer(s)
	defer ts.Close()

	ctx := context.Background()

	// Create user, auth to get token, then deactivate
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass123456"), 10)
	user := &store.DashboardUser{
		Email:         "bob@example.com",
		PasswordHash:  string(hash),
		Role:          "user",
		Name:          "Bob",
		CctraceUserID: "bob",
	}
	user, _ = s.CreateDashboardUser(ctx, user)

	// CLI auth to get token
	cliAuthBody, _ := json.Marshal(map[string]string{"user_id": "bob", "password": "pass123456"})
	req, _ := http.NewRequest("POST", ts.URL+"/api/cli/auth", bytes.NewReader(cliAuthBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, _ := http.DefaultClient.Do(req)
	var authResp map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&authResp)
	resp.Body.Close()
	token := authResp["api_token"].(string)

	// Deactivate user
	isActive := false
	s.UpdateDashboardUser(ctx, user.ID, store.UpdateDashboardUserParams{IsActive: &isActive})

	// Sync with token of deactivated user → 401
	syncBody, _ := json.Marshal(map[string]interface{}{"records": []interface{}{}})
	req, _ = http.NewRequest("POST", ts.URL+"/api/sync", bytes.NewReader(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deactivated user sync: expected 401, got %d", resp.StatusCode)
	}
}
