package usage

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type sessionTransport func(*http.Request) (*http.Response, error)

func (f sessionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSessionExpiryStopsBeforeNetwork(t *testing.T) {
	contextDependencies(t)
	old := httpClient
	t.Cleanup(func() { httpClient = old })
	httpClient = &http.Client{Transport: sessionTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("expired token reached network")
		return nil, errors.New("unexpected")
	})}
	creds, err := parseCredentials([]byte(`{"claudeAiOauth":{"accessToken":"synthetic","refreshToken":"private-refresh","expiresAt":1}}`), "fixture")
	if err != nil || !creds.RefreshAvailable || creds.ExpiresAt.UnixMilli() != 1 {
		t.Fatalf("lost expiry metadata: %v", err)
	}
	readContextCredentials = func(context.Context, string) (Credentials, error) { return creds, nil }
	for _, fetch := range []func(context.Context, string) error{
		func(c context.Context, h string) error { _, e := FetchContext(c, h); return e },
		func(c context.Context, h string) error { _, e := FetchAccountContext(c, h); return e },
	} {
		if err := fetch(context.Background(), t.TempDir()); !errors.Is(err, ErrTokenRefreshRequired) {
			t.Fatalf("expiry category: %v", err)
		}
	}
}

func TestSessionHTTPErrorCategories(t *testing.T) {
	for _, tc := range []struct {
		status  int
		refresh bool
		want    error
	}{
		{401, true, ErrTokenRefreshRequired}, {401, false, ErrAccountAuthentication}, {403, true, ErrAuthorizationDenied},
	} {
		t.Run(tc.want.Error(), func(t *testing.T) {
			contextDependencies(t)
			old := httpClient
			t.Cleanup(func() { httpClient = old })
			httpClient = &http.Client{Transport: sessionTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: http.NoBody, Header: http.Header{}}, nil
			})}
			readContextCredentials = func(context.Context, string) (Credentials, error) {
				return Credentials{AccessToken: "synthetic", RefreshAvailable: tc.refresh, ExpiresAt: time.Now().Add(time.Hour)}, nil
			}
			_, err := FetchAccountContext(context.Background(), t.TempDir())
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func TestSessionRecoveryRejectsDecoratedPrompts(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"Log\x1b[31m in", "Please\r\n sign in", "Press Enter to continue", "Do you trust this folder?", "Unknown option --safe-mode", "Open your browser", "Welcome to Claude Code", "https://local.test/oauth/start"} {
		if !recoveryPrompt(text) {
			t.Errorf("prompt accepted: %q", text)
		}
	}
	if recoveryPrompt("Claude Code v2.1.301") {
		t.Fatal("ordinary banner rejected")
	}
}
