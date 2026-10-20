package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Global credential/HTTP seams match the existing context tests; do not parallelize.
func TestAccountContextSameCredentialProof(t *testing.T) {
	contextDependencies(t)
	home := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-context-token" {
			t.Error("credential mismatch")
		}
		if r.URL.Path == "/api/oauth/profile" {
			w.Write([]byte(`{"account":{"uuid":"account-one","email":"private@example.invalid"},"organization":{"uuid":"org-one"}}`))
			return
		}
		w.Write([]byte(`{"five_hour":{"utilization":0,"resets_at":"2026-10-13T12:00:00Z"}}`))
	}))
	defer srv.Close()
	apiURL = srv.URL + "/api/oauth/usage"
	got, err := FetchAccountContext(context.Background(), home)
	if err != nil || got == nil || got.Usage == nil || got.AccountID != "account-one" || got.OrganizationID != "org-one" || got.Usage.TokenFingerprint != fingerprintToken("synthetic-context-token") {
		t.Fatalf("missing paired identity: %+v %v", got, err)
	}
}
func TestAccountContextRejectsInvalidAndChangingIdentity(t *testing.T) {
	for _, kind := range []string{"missing", "private-id", "rotation", "profile-401", "profile-429"} {
		t.Run(kind, func(t *testing.T) {
			contextDependencies(t)
			calls := 0
			readContextCredentials = func(context.Context, string) (Credentials, error) {
				calls++
				token := "synthetic-context-token"
				if kind == "rotation" && calls > 1 {
					token = "changed"
				}
				return Credentials{AccessToken: token}, nil
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/oauth/profile" {
					if kind == "profile-401" {
						w.WriteHeader(401)
						return
					}
					if kind == "profile-429" {
						w.Header().Set("Retry-After", "30")
						w.WriteHeader(429)
						return
					}
					if kind == "missing" {
						w.Write([]byte(`{"account":{},"organization":{}}`))
						return
					}
					if kind == "private-id" {
						w.Write([]byte(`{"account":{"uuid":"/private/SECRET"},"organization":{"uuid":"org-one"}}`))
						return
					}
					w.Write([]byte(`{"account":{"uuid":"account-one"},"organization":{"uuid":"org-one"}}`))
					return
				}
				w.Write([]byte(`{"five_hour":{"utilization":0,"resets_at":"2026-10-13T12:00:00Z"}}`))
			}))
			defer srv.Close()
			apiURL = srv.URL + "/api/oauth/usage"
			got, err := FetchAccountContext(context.Background(), t.TempDir())
			if err == nil || got != nil {
				t.Fatalf("unsafe %s accepted: %+v %v", kind, got, err)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "synthetic-context-token") {
				t.Fatal("private error leaked")
			}
		})
	}
}

func TestAccountContextPreservesErrorCategories(t *testing.T) {
	for _, which := range []string{"profile401", "usage401", "usage429", "cancelled"} {
		t.Run(which, func(t *testing.T) {
			contextDependencies(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/oauth/profile" {
					if which == "profile401" {
						w.WriteHeader(401)
						return
					}
					w.Write([]byte(`{"account":{"uuid":"acct"},"organization":{"uuid":"org"}}`))
					return
				}
				if which == "usage429" {
					w.Header().Set("Retry-After", "30")
					w.WriteHeader(429)
				} else {
					w.WriteHeader(401)
				}
			}))
			defer srv.Close()
			apiURL = srv.URL + "/api/oauth/usage"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if which == "cancelled" {
				cancel()
			}
			_, err := FetchAccountContext(ctx, t.TempDir())
			switch which {
			case "profile401", "usage401":
				if !errors.Is(err, ErrAccountAuthentication) {
					t.Fatalf("auth lost: %v", err)
				}
			case "usage429":
				var rate *RateLimitError
				if !errors.As(err, &rate) || rate.RetryAfter < 30*time.Second {
					t.Fatalf("retry lost: %v", err)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel lost: %v", err)
				}
			}
		})
	}
}

func TestAccountIdentityActionDoesNotFetchQuota(t *testing.T) {
	contextDependencies(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/oauth/profile" {
			t.Error("identity action fetched usage")
		}
		w.Write([]byte(`{"account":{"uuid":"acct"},"organization":{"uuid":"org"}}`))
	}))
	defer srv.Close()
	apiURL = srv.URL + "/api/oauth/usage"
	result, err := FetchAccountIdentityContext(context.Background(), t.TempDir())
	if err != nil || result == nil || result.Usage != nil || calls != 1 {
		t.Fatalf("implicit quota request: %+v %v calls%d", result, err, calls)
	}
}
