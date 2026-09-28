package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// accountRuntime scripts AccountManager answers and records what it was asked.
type accountRuntime struct {
	*airuntime.FakeRuntime
	account   airuntime.Account
	login     airuntime.DeviceLogin
	startErr  error
	keyErr    error
	logoutErr error

	keys      []string
	canceled  string
	loggedOut bool
}

func newAccountRuntime() *accountRuntime {
	return &accountRuntime{
		FakeRuntime: aiRuntime(),
		account:     airuntime.Account{AuthMode: airuntime.AuthModeChatGPT, Email: "ops@example.com", PlanType: "pro"},
		login: airuntime.DeviceLogin{ID: "login-1", VerificationURL: "https://auth.openai.com/codex/device", UserCode: "ABCD-1234",
			ExpiresAt: time.Date(2026, 9, 15, 3, 15, 0, 0, time.UTC), State: airuntime.LoginPending},
	}
}

func (a *accountRuntime) Account(context.Context) (airuntime.Account, error) { return a.account, nil }

func (a *accountRuntime) StartDeviceLogin(context.Context) (airuntime.DeviceLogin, error) {
	if a.startErr != nil {
		return airuntime.DeviceLogin{}, a.startErr
	}
	return a.login, nil
}

func (a *accountRuntime) DeviceLogin(id string) (airuntime.DeviceLogin, error) {
	if id != a.login.ID {
		return airuntime.DeviceLogin{}, airuntime.ErrLoginNotFound
	}
	return a.login, nil
}

func (a *accountRuntime) CancelDeviceLogin(_ context.Context, id string) (airuntime.DeviceLogin, error) {
	if id != a.login.ID {
		return airuntime.DeviceLogin{}, airuntime.ErrLoginNotFound
	}
	a.canceled = id
	l := a.login
	l.State = airuntime.LoginCanceled
	return l, nil
}

func (a *accountRuntime) LoginAPIKey(_ context.Context, key string) error {
	a.keys = append(a.keys, key)
	return a.keyErr
}

func (a *accountRuntime) Logout(context.Context) error {
	a.loggedOut = a.logoutErr == nil
	return a.logoutErr
}

func (a *accountRuntime) CloseLogins() {}

func TestAdminAIAccountRequiresAdminAndCSRF(t *testing.T) {
	user := newAITest(t, newAccountRuntime(), aiCaller)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/ai/account", ""},
		{http.MethodPost, "/api/admin/ai/account/login", `{"type":"chatgpt_device_code"}`},
		{http.MethodGet, "/api/admin/ai/account/login/login-1", ""},
		{http.MethodDelete, "/api/admin/ai/account/login/login-1", ""},
		{http.MethodPost, "/api/admin/ai/account/logout", ""},
	} {
		if rec := user.do(c.method, c.path, c.body, true); rec.Code != http.StatusForbidden {
			t.Errorf("non-admin %s %s: %d", c.method, c.path, rec.Code)
		}
	}
	rt := newAccountRuntime()
	admin := newAITest(t, rt, aiAdmin)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/admin/ai/account/login", `{"type":"api_key","api_key":"sk-x"}`},
		{http.MethodDelete, "/api/admin/ai/account/login/login-1", ""},
		{http.MethodPost, "/api/admin/ai/account/logout", ""},
	} {
		if rec := admin.do(c.method, c.path, c.body, false); rec.Code != http.StatusForbidden {
			t.Errorf("without CSRF %s %s: %d", c.method, c.path, rec.Code)
		}
	}
	if len(rt.keys) != 0 || rt.canceled != "" || rt.loggedOut {
		t.Fatalf("refused requests reached the runtime: %+v", rt)
	}
}

// An admin's CLI token is a file on a client PC. Reading a pending device code
// through it would let whoever holds the file enter the code into their own
// ChatGPT account first, so account management takes the browser session only.
func TestAdminAIAccountRefusesTokenAuth(t *testing.T) {
	rt := newAccountRuntime()
	pending := rt.login
	rt.account.Login = &pending
	a := newAITest(t, rt, aiAdmin)
	signIn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithTokenAuth(auth.WithUser(r.Context(), aiAdmin))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	a.srv = newServer(&mockStore{}, nil, signIn).WithAIReports(a.svc)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/ai/account", ""},
		{http.MethodPost, "/api/admin/ai/account/login", `{"type":"chatgpt_device_code"}`},
		{http.MethodGet, "/api/admin/ai/account/login/login-1", ""},
		{http.MethodDelete, "/api/admin/ai/account/login/login-1", ""},
		{http.MethodPost, "/api/admin/ai/account/logout", ""},
	} {
		rec := a.do(c.method, c.path, c.body, true)
		if rec.Code != http.StatusForbidden || decodeBody(t, rec)["error"] != "session_required" {
			t.Errorf("token %s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "ABCD-1234") {
			t.Errorf("token %s %s leaks the user code", c.method, c.path)
		}
	}
	if rt.canceled != "" || rt.loggedOut {
		t.Fatalf("refused requests reached the runtime: %+v", rt)
	}
}

func TestAdminAIAccount(t *testing.T) {
	rt := newAccountRuntime()
	rt.account.AuthFileIsSymlink = true
	pending := rt.login
	rt.account.Login = &pending
	rec := newAITest(t, rt, aiAdmin).do(http.MethodGet, "/api/admin/ai/account", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	login, _ := body["login"].(map[string]any)
	if body["auth_mode"] != "chatgpt" || body["email"] != "ops@example.com" || body["plan_type"] != "pro" ||
		body["env_managed"] != false || body["auth_file_is_symlink"] != true ||
		login["login_id"] != "login-1" || login["status"] != "pending" || login["expires_at"] != "2026-09-15T03:15:00Z" {
		t.Fatalf("body = %v", body)
	}

	rt.account.Login = nil
	if body := decodeBody(t, newAITest(t, rt, aiAdmin).do(http.MethodGet, "/api/admin/ai/account", "", false)); body["login"] != nil {
		t.Fatalf("login = %v, want null", body["login"])
	}
}

// A runtime that cannot manage accounts answers like a missing one.
func TestAdminAIAccountUnconfigured(t *testing.T) {
	for name, rt := range map[string]*airuntime.FakeRuntime{"nil": nil, "no manager": aiRuntime()} {
		var a *aiTest
		if rt == nil {
			a = newAITest(t, nil, aiAdmin)
		} else {
			a = newAITest(t, rt, aiAdmin)
		}
		for _, c := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/admin/ai/account", ""},
			{http.MethodPost, "/api/admin/ai/account/login", `{"type":"chatgpt_device_code"}`},
			{http.MethodPost, "/api/admin/ai/account/logout", ""},
		} {
			rec := a.do(c.method, c.path, c.body, true)
			if rec.Code != http.StatusServiceUnavailable || decodeBody(t, rec)["error"] != "runtime_unconfigured" {
				t.Errorf("%s %s %s: %d %s", name, c.method, c.path, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestAdminAIDeviceLogin(t *testing.T) {
	rt := newAccountRuntime()
	a := newAITest(t, rt, aiAdmin)
	rec := a.do(http.MethodPost, "/api/admin/ai/account/login", `{"type":"chatgpt_device_code"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["login_id"] != "login-1" || body["user_code"] != "ABCD-1234" || body["verification_url"] != "https://auth.openai.com/codex/device" ||
		body["expires_at"] != "2026-09-15T03:15:00Z" || body["status"] != "pending" || body["error"] != nil {
		t.Fatalf("body = %v", body)
	}

	if body := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai/account/login/login-1", "", false)); body["status"] != "pending" {
		t.Fatalf("status body = %v", body)
	}
	// error is a code: runtime text reaches the log, not the screen.
	rt.login.State, rt.login.Error = airuntime.LoginFailed, airuntime.LoginErrorRuntimeExited
	if body := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai/account/login/login-1", "", false)); body["status"] != "failed" || body["error"] != "runtime_exited" {
		t.Fatalf("failed body = %v", body)
	}
	rt.login.Error = "authorization denied for ops@example.com"
	if body := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai/account/login/login-1", "", false)); body["error"] != "login_failed" {
		t.Fatalf("unknown error body = %v", body)
	}
	if rec := a.do(http.MethodGet, "/api/admin/ai/account/login/nope", "", false); rec.Code != http.StatusNotFound || decodeBody(t, rec)["error"] != "login_not_found" {
		t.Fatalf("unknown id: %d %s", rec.Code, rec.Body.String())
	}

	rec = a.do(http.MethodDelete, "/api/admin/ai/account/login/login-1", "", true)
	if rec.Code != http.StatusOK || decodeBody(t, rec)["status"] != "canceled" || rt.canceled != "login-1" {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if rec := a.do(http.MethodDelete, "/api/admin/ai/account/login/nope", "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown id: %d", rec.Code)
	}
}

func TestAdminAILoginErrors(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{airuntime.ErrEnvManaged, http.StatusConflict, "env_managed"},
		{airuntime.ErrAuthFileIsSymlink, http.StatusConflict, "auth_file_is_symlink"},
		{airuntime.ErrLoginInProgress, http.StatusConflict, "login_in_progress"},
		{fmt.Errorf("%w: codex app-server error -32000: /srv/codex exit 1", airuntime.ErrLoginFailed), http.StatusBadGateway, "login_failed"},
		{fmt.Errorf("%w: account/logout: /srv/codex exit 1", airuntime.ErrUnavailable), http.StatusBadGateway, "login_failed"},
	} {
		rt := newAccountRuntime()
		rt.startErr, rt.keyErr, rt.logoutErr = c.err, c.err, c.err
		a := newAITest(t, rt, aiAdmin)
		for _, req := range []struct{ path, body string }{
			{"/api/admin/ai/account/login", `{"type":"chatgpt_device_code"}`},
			{"/api/admin/ai/account/login", `{"type":"api_key","api_key":"sk-x"}`},
			{"/api/admin/ai/account/logout", ""},
		} {
			rec := a.do(http.MethodPost, req.path, req.body, true)
			if rec.Code != c.status || decodeBody(t, rec)["error"] != c.code {
				t.Errorf("%v %s %s: %d %s", c.err, req.path, req.body, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "/srv/codex") {
				t.Errorf("%s %s echoes runtime output: %s", req.path, req.body, rec.Body.String())
			}
		}
	}

	a := newAITest(t, newAccountRuntime(), aiAdmin)
	for _, body := range []string{``, `{}`, `{"type":"chatgpt"}`, `{"type":"api_key"}`, `{"type":"api_key","api_key":"   "}`} {
		rec := a.do(http.MethodPost, "/api/admin/ai/account/login", body, true)
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["error"] != "invalid_request" {
			t.Errorf("%q: %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

// The key arrives once and goes nowhere else: not the response, not an error.
func TestAdminAIAPIKeyLogin(t *testing.T) {
	const key = "sk-secret-123"
	rt := newAccountRuntime()
	a := newAITest(t, rt, aiAdmin)
	rec := a.do(http.MethodPost, "/api/admin/ai/account/login", `{"type":"api_key","api_key":"`+key+`"}`, true)
	if rec.Code != http.StatusOK || decodeBody(t, rec)["status"] != "succeeded" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(rt.keys) != 1 || rt.keys[0] != key {
		t.Fatalf("keys = %v", rt.keys)
	}
	if strings.Contains(rec.Body.String(), key) {
		t.Fatalf("response echoes the key: %s", rec.Body.String())
	}

	// Redaction replaces the whole key only; a server repeating part of it
	// must still not reach the screen.
	rt.keyErr = fmt.Errorf("%w: invalid api key sk-secret-12…", airuntime.ErrLoginFailed)
	rec = a.do(http.MethodPost, "/api/admin/ai/account/login", `{"type":"api_key","api_key":"`+key+`"}`, true)
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("failure: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminAILogout(t *testing.T) {
	rt := newAccountRuntime()
	rec := newAITest(t, rt, aiAdmin).do(http.MethodPost, "/api/admin/ai/account/logout", "", true)
	if rec.Code != http.StatusOK || !rt.loggedOut {
		t.Fatalf("status %d loggedOut %v: %s", rec.Code, rt.loggedOut, rec.Body.String())
	}
}

// as serves the same service to another signed-in user.
func (a *aiTest) as(user *auth.DashboardUser) *aiTest {
	signIn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})
	}
	return &aiTest{srv: newServer(&mockStore{}, nil, signIn).WithAIReports(a.svc), st: a.st, svc: a.svc, wk: a.wk}
}

func TestAdminAIAccountChangeRefusedDuringRun(t *testing.T) {
	rt := newAccountRuntime()
	rt.Steps = []airuntime.FakeStep{{WaitForCancel: true}}
	admin := newAITest(t, rt, aiAdmin)
	user := admin.as(aiCaller)
	user.consent(t, 1)
	user.st.AddSegment("caller", store.AISegment{ID: 10, StartTs: user.wk.Since.Add(time.Hour)})
	if rec := user.do(http.MethodPost, "/api/ai-reports", `{"week":"2026-W37","tz":"Asia/Seoul"}`, true); rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	for _, req := range []struct{ path, body string }{
		{"/api/admin/ai/account/login", `{"type":"chatgpt_device_code"}`},
		{"/api/admin/ai/account/login", `{"type":"api_key","api_key":"sk-x"}`},
		{"/api/admin/ai/account/logout", ""},
	} {
		rec := admin.do(http.MethodPost, req.path, req.body, true)
		if rec.Code != http.StatusConflict || decodeBody(t, rec)["error"] != "run_in_progress" {
			t.Errorf("%s %s: %d %s", req.path, req.body, rec.Code, rec.Body.String())
		}
	}
	if rt.loggedOut || len(rt.keys) != 0 {
		t.Fatalf("refused changes reached the runtime: %+v", rt)
	}
}

func TestAIReportStartRefusedDuringDeviceLogin(t *testing.T) {
	rt := newAccountRuntime()
	admin := newAITest(t, rt, aiAdmin)
	user := admin.as(aiCaller)
	user.consent(t, 1)
	user.st.AddSegment("caller", store.AISegment{ID: 10, StartTs: user.wk.Since.Add(time.Hour)})
	if rec := admin.do(http.MethodPost, "/api/admin/ai/account/login", `{"type":"chatgpt_device_code"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	rec := user.do(http.MethodPost, "/api/ai-reports", `{"week":"2026-W37","tz":"Asia/Seoul"}`, true)
	if rec.Code != http.StatusConflict || decodeBody(t, rec)["error"] != "runtime_account_changing" {
		t.Fatalf("start during login: %d %s", rec.Code, rec.Body.String())
	}
}
