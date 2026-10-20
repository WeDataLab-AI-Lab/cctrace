package usage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func contextDependencies(t *testing.T) {
	t.Helper()
	oldToken, oldURL, oldClock := readContextCredentials, apiURL, nowFn
	readContextCredentials = func(ctx context.Context, home string) (Credentials, error) {
		return Credentials{AccessToken: "synthetic-context-token"}, ctx.Err()
	}
	t.Cleanup(func() { readContextCredentials, apiURL, nowFn = oldToken, oldURL, oldClock })
}

func TestFetchContextExplicitHomeAndZero(t *testing.T) {
	contextDependencies(t)
	home := t.TempDir()
	readContextCredentials = func(ctx context.Context, got string) (Credentials, error) {
		if got != home {
			t.Errorf("wrong selected home")
		}
		return Credentials{AccessToken: "synthetic-context-token"}, ctx.Err()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-context-token" {
			t.Error("missing synthetic authorization")
		}
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":0,"resets_at":"2026-10-06T12:00:00Z"}}`))
	}))
	t.Cleanup(srv.Close)
	apiURL = srv.URL
	now := time.Date(2026, 10, 6, 6, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return now }
	r, err := FetchContext(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if r.Window(KindSession) == nil || r.Window(KindSession).Utilization != 0 || !r.FetchedAt.Equal(now) {
		t.Fatal("zero or fetch time was lost")
	}
	if r.TokenFingerprint != fingerprintToken("synthetic-context-token") {
		t.Fatal("missing account binding")
	}
}

func TestFetchContextRejectsImplicitHomeBeforeCredentials(t *testing.T) {
	contextDependencies(t)
	calls := 0
	readContextCredentials = func(context.Context, string) (Credentials, error) { calls++; return Credentials{}, nil }
	for _, home := range []string{"", "relative/home"} {
		if _, err := FetchContext(context.Background(), home); err == nil {
			t.Fatal("implicit home accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchContext(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if calls != 0 {
		t.Fatal("credential lookup crossed rejected boundary")
	}
}

func TestFetchContextCancelsHTTPAndCredentialStage(t *testing.T) {
	contextDependencies(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(srv.Close)
	apiURL = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	start := time.Now()
	_, err := FetchContext(ctx, t.TempDir())
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("HTTP cancellation: %v", err)
	}
	readContextCredentials = func(ctx context.Context, _ string) (Credentials, error) {
		<-ctx.Done()
		return Credentials{}, ctx.Err()
	}
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	start = time.Now()
	_, err = FetchContext(ctx, t.TempDir())
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("credential cancellation: %v", err)
	}
}

func TestFetchContextBoundsAndValidatesResponse(t *testing.T) {
	contextDependencies(t)
	for _, body := range []string{"null", "{}", `{"five_hour":{"resets_at":"2026-10-06T12:00:00Z"}}`, `{"five_hour":{"utilization":null}}`, `{"limits":[{"kind":"session"}]}`, `{"five_hour":{"utilization":101}}`, `{"five_hour":{"utilization":0,"resets_at":"2026-10-06T12:00:00Z"}} {}`, strings.Repeat(" ", 1_048_577)} {
		t.Run(body[:min(len(body), 20)], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			apiURL = srv.URL
			if _, err := FetchContext(context.Background(), t.TempDir()); err == nil {
				t.Fatal("invalid/oversize usage accepted")
			}
		})
	}
}

func TestFetchContextRateLimitAndAccountChange(t *testing.T) {
	contextDependencies(t)
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") == "Bearer old-synthetic" {
			_, _ = w.Write([]byte(`{"five_hour":{"utilization":37,"resets_at":"2026-10-06T12:00:00Z"}}`))
			return
		}
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	apiURL = srv.URL
	token := "old-synthetic"
	readContextCredentials = func(context.Context, string) (Credentials, error) { return Credentials{AccessToken: token}, nil }
	home := t.TempDir()
	if _, err := FetchContext(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	token = "new-synthetic"
	r, err := FetchContext(context.Background(), home)
	var limited *RateLimitError
	if r != nil || !errors.As(err, &limited) || limited.RetryAfter != time.Minute || hits.Load() != 2 {
		t.Fatal("old-account cache or rate-limit error lost")
	}
}

func TestContextCredentialsReapsCanceledOwnedCommand(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin keychain command adapter")
	}
	old := contextSecurityCommand
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	var cmd *exec.Cmd
	contextSecurityCommand = func(ctx context.Context, service string) *exec.Cmd {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", "printf x >&3; exec /bin/sleep 30")
		cmd.ExtraFiles = []*os.File{readyWrite}
		return cmd
	}
	t.Cleanup(func() { contextSecurityCommand = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchdog := time.AfterFunc(2*time.Second, cancel)
	defer watchdog.Stop()
	type readiness struct {
		at  time.Time
		err error
	}
	ready := make(chan readiness, 1)
	go func() {
		_, err := io.ReadFull(readyRead, make([]byte, 1))
		ready <- readiness{time.Now(), err}
		cancel()
	}()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"must-not-fall-back"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = credentialsForContext(ctx, home)
	readyWrite.Close()
	started := <-ready
	if started.err != nil || !errors.Is(err, context.Canceled) || time.Since(started.at) > time.Second || cmd == nil || cmd.ProcessState == nil {
		t.Fatalf("owned command not reaped or file fallback used: %v", err)
	}
	if cmd.ProcessState == nil || cmd.ProcessState.Pid() <= 0 {
		t.Fatal("missing owned process exit receipt")
	}
}

func TestContextCredentialOutputCannotFallBackAfterOversize(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin command adapter")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"must-not-fall-back"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "owned-command-output")
	if err := os.WriteFile(output, []byte(strings.Repeat("x", 65_537)), 0600); err != nil {
		t.Fatal(err)
	}
	old := contextSecurityCommand
	contextSecurityCommand = func(ctx context.Context, _ string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/cat", output) }
	t.Cleanup(func() { contextSecurityCommand = old })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := credentialsForContext(ctx, home); err == nil {
		t.Fatal("oversize credential command fell back to the file")
	}
}

func TestContextCredentialFallbackStaysInSelectedRegularFile(t *testing.T) {
	old := contextSecurityCommand
	contextSecurityCommand = func(ctx context.Context, _ string) *exec.Cmd { return exec.CommandContext(ctx, "/usr/bin/false") }
	t.Cleanup(func() { contextSecurityCommand = old })
	home, other := t.TempDir(), t.TempDir()
	path := filepath.Join(home, ".credentials.json")
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"selected-synthetic"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"other-synthetic"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	creds, err := credentialsForContext(context.Background(), home)
	if err != nil || creds.AccessToken != "selected-synthetic" {
		t.Fatalf("selected file: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, ".credentials.json"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := credentialsForContext(context.Background(), home); err == nil {
		t.Fatal("credential symlink escape accepted")
	}
}
