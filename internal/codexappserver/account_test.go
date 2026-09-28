package codexappserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

// shortFetch keeps Account's status read from waiting out the full timeout:
// the account fakes never answer a rate-limit read.
func shortFetch(t *testing.T) {
	t.Helper()
	prev := fetchTimeout
	fetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { fetchTimeout = prev })
}

func accountRuntime(t *testing.T, behavior string) (*CodexRuntime, string) {
	t.Helper()
	fakeServer(t, behavior)
	shortFetch(t)
	home := t.TempDir()
	rt := NewRuntime(RuntimeConfig{Home: home}).(*CodexRuntime)
	t.Cleanup(rt.CloseLogins)
	return rt, home
}

// waitLogin polls until the login leaves pending.
func waitLogin(t *testing.T, rt *CodexRuntime, id string) airuntime.DeviceLogin {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l, err := rt.DeviceLogin(id)
		if err != nil {
			t.Fatalf("DeviceLogin: %v", err)
		}
		if l.State != airuntime.LoginPending {
			return l
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("login still pending")
	return airuntime.DeviceLogin{}
}

func TestDeviceLoginSucceeds(t *testing.T) {
	rt, home := accountRuntime(t, fakeDeviceLoginOK)
	before := descendantPIDs(t)
	// A cached failure from before the login must not outlive it.
	cache[home] = &cached{err: errors.New("not logged in"), fetchedAt: nowFn(), ttl: time.Hour}

	l, err := rt.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceLogin: %v", err)
	}
	if l.ID != "login-1" || l.UserCode != "ABCD-1234" || l.VerificationURL != "https://auth.openai.com/codex/device" ||
		l.State != airuntime.LoginPending || time.Until(l.ExpiresAt) < 10*time.Minute {
		t.Fatalf("login = %+v", l)
	}
	if acct, err := rt.Account(context.Background()); err != nil || acct.Login == nil || acct.Login.ID != "login-1" {
		t.Fatalf("account while pending = %+v, %v", acct, err)
	}

	if got := waitLogin(t, rt, l.ID); got.State != airuntime.LoginSucceeded {
		t.Fatalf("login = %+v", got)
	}
	assertNoNewDescendants(t, before)
	if AuthMode(home, "") != airuntime.AuthModeChatGPT {
		t.Fatal("no ChatGPT auth.json after the login")
	}
	cacheMu.Lock()
	_, stale := cache[home]
	cacheMu.Unlock()
	if stale {
		t.Fatal("status cache not invalidated after login")
	}
	if acct, _ := rt.Account(context.Background()); acct.Login != nil {
		t.Fatalf("finished login still reported as pending: %+v", acct.Login)
	}
}

func TestDeviceLoginFails(t *testing.T) {
	rt, _ := accountRuntime(t, fakeDeviceLoginFail)
	before := descendantPIDs(t)
	l, err := rt.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The server's own text goes to the log; the login carries a code.
	if got := waitLogin(t, rt, l.ID); got.State != airuntime.LoginFailed || got.Error != airuntime.LoginErrorFailed {
		t.Fatalf("login = %+v", got)
	}
	// Checked at once: whoever sees the login end may start the next change,
	// and the process must already be gone by then.
	assertNoNewDescendants(t, before)
}

// The server exiting mid-login is a failure, not a login pending forever.
func TestDeviceLoginServerExit(t *testing.T) {
	rt, _ := accountRuntime(t, fakeDeviceLoginExit)
	l, err := rt.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := waitLogin(t, rt, l.ID); got.State != airuntime.LoginFailed || got.Error != airuntime.LoginErrorRuntimeExited {
		t.Fatalf("login = %+v", got)
	}
}

func TestDeviceLoginCancel(t *testing.T) {
	rt, _ := accountRuntime(t, fakeDeviceLoginWait)
	capture := filepath.Join(t.TempDir(), "capture")
	t.Setenv(fakeCaptureEnv, capture)
	before := descendantPIDs(t)

	l, err := rt.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// One at a time: a second start and the other account changes wait.
	if _, err := rt.StartDeviceLogin(context.Background()); !errors.Is(err, airuntime.ErrLoginInProgress) {
		t.Fatalf("second start err = %v", err)
	}
	if err := rt.Logout(context.Background()); !errors.Is(err, airuntime.ErrLoginInProgress) {
		t.Fatalf("logout during login err = %v", err)
	}
	if _, err := rt.CancelDeviceLogin(context.Background(), "other"); !errors.Is(err, airuntime.ErrLoginNotFound) {
		t.Fatalf("cancel of unknown id err = %v", err)
	}

	got, err := rt.CancelDeviceLogin(context.Background(), l.ID)
	if err != nil || got.State != airuntime.LoginCanceled {
		t.Fatalf("cancel = %+v, %v", got, err)
	}
	assertNoNewDescendants(t, before)
	if b, _ := os.ReadFile(capture); !strings.Contains(string(b), `"account/login/cancel"`) {
		t.Fatalf("server was not asked to cancel: %s", b)
	}
	// A finished login is still readable, and a new one may start.
	if again, err := rt.DeviceLogin(l.ID); err != nil || again.State != airuntime.LoginCanceled {
		t.Fatalf("after cancel = %+v, %v", again, err)
	}
	if _, err := rt.StartDeviceLogin(context.Background()); err != nil {
		t.Fatalf("start after cancel: %v", err)
	}
}

func TestDeviceLoginExpires(t *testing.T) {
	prev := deviceLoginTimeout
	deviceLoginTimeout = 200 * time.Millisecond
	t.Cleanup(func() { deviceLoginTimeout = prev })
	rt, _ := accountRuntime(t, fakeDeviceLoginWait)
	before := descendantPIDs(t)

	l, err := rt.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := waitLogin(t, rt, l.ID); got.State != airuntime.LoginExpired {
		t.Fatalf("login = %+v", got)
	}
	assertNoNewDescendants(t, before)
}

// Server shutdown ends a pending login and its process.
func TestCloseLoginsEndsPendingLogin(t *testing.T) {
	rt, _ := accountRuntime(t, fakeDeviceLoginWait)
	before := descendantPIDs(t)
	l, err := rt.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rt.CloseLogins()
	if got, _ := rt.DeviceLogin(l.ID); got.State != airuntime.LoginCanceled {
		t.Fatalf("login = %+v", got)
	}
	assertNoNewDescendants(t, before)
}

func TestLoginAPIKey(t *testing.T) {
	rt, home := accountRuntime(t, fakeAPIKeyLogin)
	if err := rt.LoginAPIKey(context.Background(), "  "); err == nil {
		t.Fatal("blank key accepted")
	}
	if err := rt.LoginAPIKey(context.Background(), "sk-screen"); err != nil {
		t.Fatalf("LoginAPIKey: %v", err)
	}
	if got := readAuth(t, home); got["OPENAI_API_KEY"] != "sk-screen" {
		t.Fatalf("auth.json = %v", got)
	}
	if info := rt.Info(); info.AuthMode != airuntime.AuthModeAPIKey {
		t.Fatalf("auth mode = %q", info.AuthMode)
	}
}

// The key must not come back in an error, where it would reach the API
// response and the log.
func TestLoginAPIKeyErrorOmitsKey(t *testing.T) {
	rt, _ := accountRuntime(t, fakeAPIKeyReject)
	err := rt.LoginAPIKey(context.Background(), "sk-secret-123")
	if !errors.Is(err, airuntime.ErrLoginFailed) {
		t.Fatalf("err = %v, want ErrLoginFailed", err)
	}
	if strings.Contains(err.Error(), "sk-secret-123") {
		t.Fatalf("error leaks the key: %v", err)
	}
}

func TestLogout(t *testing.T) {
	rt, home := accountRuntime(t, fakeLogout)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cache[home] = &cached{snap: &Snapshot{LoginEmail: "old@example.test"}, fetchedAt: nowFn(), ttl: time.Hour}
	if err := rt.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if AuthMode(home, "") != airuntime.AuthModeNone {
		t.Fatal("auth.json still present after logout")
	}
	cacheMu.Lock()
	_, stale := cache[home]
	cacheMu.Unlock()
	if stale {
		t.Fatal("status cache not invalidated after logout")
	}
}

// With CODEX_API_KEY the environment owns the login; the screen may read it
// but not change it.
func TestAccountChangesRefusedWhenEnvManaged(t *testing.T) {
	fakeServer(t, fakeLogout)
	shortFetch(t)
	rt := NewRuntime(RuntimeConfig{Home: t.TempDir(), APIKey: "sk-env"}).(*CodexRuntime)
	if _, err := rt.StartDeviceLogin(context.Background()); !errors.Is(err, airuntime.ErrEnvManaged) {
		t.Fatalf("device login err = %v", err)
	}
	if err := rt.LoginAPIKey(context.Background(), "sk-other"); !errors.Is(err, airuntime.ErrEnvManaged) {
		t.Fatalf("api key err = %v", err)
	}
	if err := rt.Logout(context.Background()); !errors.Is(err, airuntime.ErrEnvManaged) {
		t.Fatalf("logout err = %v", err)
	}
	if acct, err := rt.Account(context.Background()); err != nil || !acct.EnvManaged || acct.AuthMode != airuntime.AuthModeAPIKey {
		t.Fatalf("account = %+v, %v", acct, err)
	}
}

// A linked auth.json is someone's own login (a local preview links
// ~/.codex/auth.json); logging out through it would sign them out.
func TestAccountChangesRefusedForSymlinkedAuthFile(t *testing.T) {
	rt, home := accountRuntime(t, fakeLogout)
	target := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(target, []byte(`{"auth_mode":"chatgpt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "auth.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := rt.Logout(context.Background()); !errors.Is(err, airuntime.ErrAuthFileIsSymlink) {
		t.Fatalf("logout err = %v", err)
	}
	if err := rt.LoginAPIKey(context.Background(), "sk-x"); !errors.Is(err, airuntime.ErrAuthFileIsSymlink) {
		t.Fatalf("api key err = %v", err)
	}
	if _, err := rt.StartDeviceLogin(context.Background()); !errors.Is(err, airuntime.ErrAuthFileIsSymlink) {
		t.Fatalf("device login err = %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("linked login touched: %v", err)
	}
	if acct, err := rt.Account(context.Background()); err != nil || !acct.AuthFileIsSymlink {
		t.Fatalf("account = %+v, %v", acct, err)
	}
}

func TestAccountManagerUnconfigured(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{}).(*CodexRuntime)
	if _, err := rt.Account(context.Background()); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if _, err := rt.DeviceLogin("x"); !errors.Is(err, airuntime.ErrLoginNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// A hard link is the same file under another name; writing through it changes
// the other login just as a symlink would.
func TestAccountChangesRefusedForHardLinkedAuthFile(t *testing.T) {
	rt, home := accountRuntime(t, fakeLogout)
	target := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(target, []byte(`{"auth_mode":"chatgpt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(home, "auth.json")); err != nil {
		t.Skipf("hard link unavailable: %v", err)
	}
	if err := rt.Logout(context.Background()); !errors.Is(err, airuntime.ErrAuthFileIsSymlink) {
		t.Fatalf("logout err = %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("linked login touched: %v", err)
	}
	if acct, err := rt.Account(context.Background()); err != nil || !acct.AuthFileIsSymlink {
		t.Fatalf("account = %+v, %v", acct, err)
	}
}

// A linked home makes every file in it someone else's.
func TestAccountChangesRefusedForLinkedHome(t *testing.T) {
	fakeServer(t, fakeLogout)
	shortFetch(t)
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "auth.json"), []byte(`{"auth_mode":"chatgpt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, home); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	rt := NewRuntime(RuntimeConfig{Home: home}).(*CodexRuntime)
	t.Cleanup(rt.CloseLogins)
	if err := rt.Logout(context.Background()); !errors.Is(err, airuntime.ErrAuthFileIsSymlink) {
		t.Fatalf("logout err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(real, "auth.json")); err != nil {
		t.Fatalf("linked login touched: %v", err)
	}
}

// The guard runs when the login starts; the file may become a link before the
// code is entered. A login that ends on a linked file is not a success.
func TestDeviceLoginFailsWhenAuthFileBecomesLink(t *testing.T) {
	rt, _ := accountRuntime(t, fakeDeviceLoginLink)
	l, err := rt.StartDeviceLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := waitLogin(t, rt, l.ID); got.State != airuntime.LoginFailed || got.Error != airuntime.LoginErrorAuthFileLinked {
		t.Fatalf("login = %+v", got)
	}
}

// An account change starts its own process. The status read's bound, which
// shortFetch shortens, must not also be the time that process gets to start and
// answer initialize: under load the start alone outlasts a short read.
func TestAccountChangesNotBoundByStatusReadTimeout(t *testing.T) {
	for _, tc := range []struct {
		behavior string
		change   func(*CodexRuntime) error
	}{
		{fakeDeviceLoginOK, func(rt *CodexRuntime) error { _, err := rt.StartDeviceLogin(context.Background()); return err }},
		{fakeAPIKeyLogin, func(rt *CodexRuntime) error { return rt.LoginAPIKey(context.Background(), "sk-x") }},
		{fakeLogout, func(rt *CodexRuntime) error { return rt.Logout(context.Background()) }},
	} {
		t.Run(tc.behavior, func(t *testing.T) {
			rt, _ := accountRuntime(t, tc.behavior)
			prev := fetchTimeout
			fetchTimeout = time.Nanosecond
			t.Cleanup(func() { fetchTimeout = prev })
			if err := tc.change(rt); err != nil {
				t.Fatalf("account change with a 1ns status-read timeout: %v", err)
			}
		})
	}
}
