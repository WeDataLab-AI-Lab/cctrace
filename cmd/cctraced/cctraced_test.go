package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	if err := validateAuthConfiguration("", ""); err == nil {
		t.Fatal("expected missing JWT secret to fail validation")
	}
	if err := validateAuthConfiguration("jwt", ""); err != nil {
		t.Fatalf("configured dashboard auth: %v", err)
	}
}

// deploy/.env.example shipped this JWT_SECRET; anyone holding it can sign an
// admin token, and with CCTRACE_SECRETS_KEY empty it is also the sealing key.
const publishedJWTExample = "change-me-at-least-32-bytes-long-secret-key"

func TestValidateAuthConfigurationRefusesPublishedExampleSecrets(t *testing.T) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	randomSecret := hex.EncodeToString(random) // what `openssl rand -hex 32` prints

	for _, tc := range []struct {
		name, jwtSecret, secretsKey string
		mention                     []string
		omit                        []string
	}{
		{
			name:      "JWT_SECRET, sealing key falls back to it",
			jwtSecret: publishedJWTExample, secretsKey: "",
			mention: []string{"JWT_SECRET", "openssl rand -hex 32", "session", "Admin > AI"},
		},
		{
			name:      "JWT_SECRET, sealing key set separately",
			jwtSecret: publishedJWTExample, secretsKey: randomSecret,
			mention: []string{"JWT_SECRET", "openssl rand -hex 32", "session"},
			omit:    []string{"Admin > AI"},
		},
		{
			name:      "CCTRACE_SECRETS_KEY set to it",
			jwtSecret: randomSecret, secretsKey: publishedJWTExample,
			mention: []string{"CCTRACE_SECRETS_KEY", "openssl rand -hex 32", "Admin > AI"},
			omit:    []string{"JWT_SECRET", "session"},
		},
		{
			name:      "both set to it",
			jwtSecret: publishedJWTExample, secretsKey: publishedJWTExample,
			mention: []string{"JWT_SECRET", "session", "CCTRACE_SECRETS_KEY", "Admin > AI"},
			omit:    []string{"CCTRACE_SECRETS_KEY is empty"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAuthConfiguration(tc.jwtSecret, tc.secretsKey)
			if err == nil {
				t.Fatal("published example secret passed validation")
			}
			msg := err.Error()
			if strings.Contains(msg, publishedJWTExample) {
				t.Errorf("error repeats the secret: %q", msg)
			}
			for _, want := range tc.mention {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not mention %q: %q", want, msg)
				}
			}
			for _, unwanted := range tc.omit {
				if strings.Contains(msg, unwanted) {
					t.Errorf("error mentions %q, which does not apply: %q", unwanted, msg)
				}
			}
		})
	}

	// A quoted .env value or `set -a; . ./.env` keeps the padding, and the key
	// is the raw bytes, so a padded copy signs tokens just as well.
	for _, padded := range []string{
		publishedJWTExample + "\r", publishedJWTExample + "\n", publishedJWTExample + " ",
		publishedJWTExample + "\t", " " + publishedJWTExample,
	} {
		if validateAuthConfiguration(padded, "") == nil {
			t.Errorf("JWT_SECRET %q passed validation", padded)
		}
		if validateAuthConfiguration(randomSecret, padded) == nil {
			t.Errorf("CCTRACE_SECRETS_KEY %q passed validation", padded)
		}
	}

	for _, ok := range [][2]string{
		{randomSecret, ""},
		{randomSecret, randomSecret},
		{"test-jwt-secret-that-is-at-least-32-bytes-long", ""},
	} {
		if err := validateAuthConfiguration(ok[0], ok[1]); err != nil {
			t.Errorf("unpublished secret refused: %v", err)
		}
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
