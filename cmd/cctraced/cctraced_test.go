package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"cctrace/internal/auth"
)

func TestEnvOr_ReturnsEnvValue(t *testing.T) {
	os.Setenv("TEST_KEY_XYZ", "hello")
	defer os.Unsetenv("TEST_KEY_XYZ")

	result := envOr("TEST_KEY_XYZ", "fallback")
	if result != "hello" {
		t.Errorf("expected %q, got %q", "hello", result)
	}
}

func TestEnvOr_ReturnsFallback(t *testing.T) {
	result := envOr("NONEXISTENT_KEY_XYZ_123", "fallback")
	if result != "fallback" {
		t.Errorf("expected %q, got %q", "fallback", result)
	}
}

func TestEnvOr_EmptyEnvUsesFallback(t *testing.T) {
	os.Setenv("TEST_EMPTY_KEY_XYZ", "")
	defer os.Unsetenv("TEST_EMPTY_KEY_XYZ")

	result := envOr("TEST_EMPTY_KEY_XYZ", "fallback")
	if result != "fallback" {
		t.Errorf("expected %q, got %q", "fallback", result)
	}
}

func TestValidateAuthConfigurationRequiresProductionSecrets(t *testing.T) {
	if err := validateAuthConfiguration(""); err == nil {
		t.Fatal("expected missing JWT secret to fail validation")
	}
	if err := validateAuthConfiguration("jwt"); err != nil {
		t.Fatalf("configured dashboard auth: %v", err)
	}
}

func TestAuthenticatedOTLPHandlerRequiresConfiguredToken(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := authenticatedOTLPHandler(next, auth.New("otel-secret"))

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/v1/metrics", nil))
	if missing.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", missing.Code)
	}

	validReq := httptest.NewRequest(http.MethodPost, "/v1/metrics", nil)
	validReq.Header.Set("Authorization", "Bearer otel-secret")
	valid := httptest.NewRecorder()
	handler.ServeHTTP(valid, validReq)
	if valid.Code != http.StatusNoContent {
		t.Fatalf("valid token status = %d, want 204", valid.Code)
	}
}

func TestNewHTTPServerSetsDefensiveTimeouts(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler())
	for name, got := range map[string]time.Duration{
		"read header": srv.ReadHeaderTimeout,
		"read":        srv.ReadTimeout,
		"write":       srv.WriteTimeout,
		"idle":        srv.IdleTimeout,
	} {
		if got <= 0 {
			t.Errorf("%s timeout = %s, want positive", name, got)
		}
	}
}

func TestDownloadHandlerServesFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/cctrace-linux-amd64", []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	downloadHandler(dir).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/downloads/cctrace-linux-amd64", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.String() != "binary" {
		t.Fatalf("body = %q, want binary", response.Body.String())
	}
}
