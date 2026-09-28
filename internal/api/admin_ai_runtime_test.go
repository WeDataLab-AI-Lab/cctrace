package api

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
	"cctrace/internal/store"
)

const apiTestKey = "sk-ant-api03-secret-value-7890"

// testSecret is long enough to seal keys with.
const testSecret = "test-secret-at-least-32-bytes-long!!"

func apiKeyRuntime(key string) *airuntime.FakeRuntime {
	rt := aiRuntime()
	rt.InfoValue = airuntime.Info{Key: key, AuthMode: airuntime.AuthModeAPIKey}
	rt.StatusValue = airuntime.Status{Configured: true, Available: true}
	return rt
}

// newAIRegistryTest serves a service over several runtimes.
func newAIRegistryTest(t *testing.T, user *auth.DashboardUser, st *aireport.MemStore, cfg aireport.Config, rts ...airuntime.Runtime) *aiTest {
	t.Helper()
	svc := aireport.NewService(st, rts, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		svc.Shutdown(ctx)
	})
	signIn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})
	}
	return &aiTest{srv: newServer(&mockStore{}, nil, signIn).WithAIReports(svc), st: st, svc: svc}
}

func (a *aiTest) withTokenAuth(user *auth.DashboardUser) *aiTest {
	signIn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithTokenAuth(auth.WithUser(r.Context(), user))))
		})
	}
	return &aiTest{srv: newServer(&mockStore{}, nil, signIn).WithAIReports(a.svc), st: a.st, svc: a.svc}
}

func TestAdminAIListsRuntimesAndCredentials(t *testing.T) {
	st := aireport.NewMemStore()
	kr := aireport.NewKeyring(st, testSecret, map[string]string{aireport.ProviderOpenAI: "sk-env-openai-key-4321"})
	if _, err := kr.SetKey(context.Background(), aireport.ProviderAnthropic, apiTestKey, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	claude := apiKeyRuntime(aireport.RuntimeClaude)
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{Keyring: kr, EnvRuntime: aireport.RuntimeClaude, Missing: map[string]string{aireport.DefaultRuntimeKey: "codex CLI 가 없습니다"}},
		nil, apiKeyRuntime(aireport.RuntimeOpenAI), claude)

	rec := a.do(http.MethodGet, "/api/admin/ai", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), apiTestKey) || strings.Contains(rec.Body.String(), "sk-env-openai") {
		t.Fatal("response carries a key")
	}
	body := decodeBody(t, rec)
	if body["selected_runtime"] != aireport.RuntimeClaude || body["runtime_source"] != "env" || body["env_runtime"] != aireport.RuntimeClaude || body["selection_reason"] != nil {
		t.Fatalf("selection = %v", body)
	}
	runtimes := body["runtimes"].([]any)
	if len(runtimes) != 5 {
		t.Fatalf("runtimes = %v", runtimes)
	}
	codex, openai, cl, nvidia, litellm := runtimes[0].(map[string]any), runtimes[1].(map[string]any), runtimes[2].(map[string]any), runtimes[3].(map[string]any), runtimes[4].(map[string]any)
	if codex["key"] != "codex-app-server" || codex["implemented"] != true || codex["configured"] != false || codex["selected"] != false ||
		codex["reason"] != "codex CLI 가 없습니다" || codex["credential"] != nil || codex["provider"] != "codex" {
		t.Errorf("codex = %v", codex)
	}
	oc, _ := openai["credential"].(map[string]any)
	if openai["key"] != "openai-api" || openai["configured"] != true || openai["selected"] != false || openai["provider"] != "openai" ||
		oc["source"] != "env" || oc["key_hint"] != "4321" || oc["env_var"] != "CCTRACE_AI_OPENAI_API_KEY" || oc["reason"] != nil {
		t.Errorf("openai = %v", openai)
	}
	cc, _ := cl["credential"].(map[string]any)
	if cl["selected"] != true || cl["auth_mode"] != "api_key" || cc["source"] != "admin" || cc["key_hint"] != "7890" || cc["updated_at"] == nil {
		t.Errorf("claude = %v", cl)
	}
	// nvidia-api is carried in the list so the screen can still account for it,
	// but implemented is false: this build cannot drive it end to end, so the row
	// must not be offered as a choice. implemented has existed on this payload
	// from the start and was hardcoded true everywhere; this is the first key to
	// make it mean something.
	if nvidia["key"] != "nvidia-api" || nvidia["provider"] != "nvidia" || nvidia["implemented"] != false || nvidia["configured"] != false {
		t.Errorf("nvidia = %v", nvidia)
	}
	if litellm["key"] != "litellm-api" || litellm["provider"] != "litellm" || litellm["implemented"] != true || litellm["configured"] != false {
		t.Errorf("litellm = %v", litellm)
	}
	if settings, _ := body["settings"].(map[string]any); settings["runtime"] != aireport.RuntimeClaude || settings["model"] != "claude-sonnet-5" {
		t.Errorf("settings = %v", body["settings"])
	}
}

func TestAdminAISelectRuntime(t *testing.T) {
	const path = "/api/admin/ai/runtime"
	unconfigured := apiKeyRuntime(aireport.RuntimeClaude)
	unconfigured.StatusValue = airuntime.Status{Reason: "Anthropic API key is not set"}
	st := aireport.NewMemStore()
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{}, aiRuntime(), apiKeyRuntime(aireport.RuntimeOpenAI), unconfigured)

	if rec := a.do(http.MethodPut, path, `{"runtime":"openai-api"}`, false); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF %d", rec.Code)
	}
	for body, want := range map[string]struct {
		status int
		code   string
	}{
		`{}`:                       {http.StatusBadRequest, "invalid_request"},
		`{"runtime":"gemini"}`:     {http.StatusBadRequest, "unknown_runtime"},
		`{"runtime":"claude-api"}`: {http.StatusConflict, "runtime_unconfigured"},
		// A runtime this build does not implement is refused before anything is
		// asked about keys or availability: "구성되지 않았다" would invite an admin
		// to register a key and try again, which cannot help. The order matters as
		// much as the code -- nvidia-api is not built in this test either, so a
		// check placed after the built/configured one would still answer
		// runtime_unconfigured and hide the real reason.
		`{"runtime":"nvidia-api"}`: {http.StatusConflict, "runtime_unimplemented"},
	} {
		rec := a.do(http.MethodPut, path, body, true)
		if rec.Code != want.status || decodeBody(t, rec)["error"] != want.code {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if st.Choice != nil {
		t.Fatalf("refused choice stored %+v", st.Choice)
	}

	rec := a.do(http.MethodPut, path, `{"runtime":"openai-api"}`, true)
	body := decodeBody(t, rec)
	if rec.Code != http.StatusOK || body["selected_runtime"] != "openai-api" || body["runtime_source"] != "admin" {
		t.Fatalf("select: %d %v", rec.Code, body)
	}
	if settings, _ := body["settings"].(map[string]any); settings["runtime"] != "openai-api" {
		t.Fatalf("settings = %v", body["settings"])
	}
	if st.Choice == nil || st.Choice.UpdatedBy != "admin@example.com" {
		t.Fatalf("choice = %+v", st.Choice)
	}
	rec = a.do(http.MethodPut, path, `{"runtime":""}`, true)
	if body = decodeBody(t, rec); rec.Code != http.StatusOK || body["selected_runtime"] != "" || body["runtime_source"] != "default" || body["settings"] != nil {
		t.Fatalf("clear: %d %v", rec.Code, body)
	}
	// Cleared with no CCTRACE_AI_RUNTIME: no runtime is active, not the first built,
	// and the server says so.
	rec = a.do(http.MethodGet, "/api/admin/ai", "", false)
	if body = decodeBody(t, rec); rec.Code != http.StatusOK || body["selected_runtime"] != "" || body["settings"] != nil ||
		body["selection_reason_code"] != "runtime_not_selected" || body["selection_reason"] == nil {
		t.Fatalf("after clear: %d %v", rec.Code, body)
	}
	for _, rt := range body["runtimes"].([]any) {
		if rt.(map[string]any)["selected"] == true {
			t.Fatalf("a runtime is selected after clear: %v", rt)
		}
	}
}

// A chosen runtime this server cannot run is named with why, and nothing else
// is marked selected in its place.
func TestAdminAIShowsUnusableSelection(t *testing.T) {
	a := newAIRegistryTest(t, aiAdmin, aireport.NewMemStore(), aireport.Config{EnvRuntime: aireport.DefaultRuntimeKey, Missing: map[string]string{aireport.DefaultRuntimeKey: "codex CLI 가 없습니다"}},
		nil, apiKeyRuntime(aireport.RuntimeOpenAI))
	rec := a.do(http.MethodGet, "/api/admin/ai", "", false)
	body := decodeBody(t, rec)
	reason, _ := body["selection_reason"].(string)
	if rec.Code != http.StatusOK || body["selected_runtime"] != aireport.DefaultRuntimeKey || !strings.Contains(reason, "codex CLI 가 없습니다") || body["selection_reason_code"] != "runtime_unavailable" {
		t.Fatalf("unusable selection: %d %v", rec.Code, body)
	}
	for _, rt := range body["runtimes"].([]any) {
		if rt.(map[string]any)["selected"] == true {
			t.Fatalf("selected in place of the unbuilt choice: %v", rt)
		}
	}
}

func TestAdminAISelectRuntimeRefusedWhileRunning(t *testing.T) {
	rt := aiRuntime(airuntime.FakeStep{WaitForCancel: true})
	st := aireport.NewMemStore()
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{EnvRuntime: aireport.DefaultRuntimeKey}, rt, apiKeyRuntime(aireport.RuntimeOpenAI))
	sc := aireport.Scope{DashboardUserID: aiAdmin.ID, UserID: "caller"}
	if err := st.UpsertAIConsent(context.Background(), aiAdmin.ID, "codex-app-server:chatgpt", aireport.DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	wk, _ := aireport.ParseISOWeek("2026-W37", "Asia/Seoul")
	st.AddSegment("caller", store.AISegment{ID: 2, StartTs: wk.Since.Add(time.Hour)})
	run, err := a.svc.Start(context.Background(), sc, wk)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.svc.Cancel(context.Background(), sc, run.ID) }()
	rec := a.do(http.MethodPut, "/api/admin/ai/runtime", `{"runtime":"openai-api"}`, true)
	if rec.Code != http.StatusConflict || decodeBody(t, rec)["error"] != "run_in_progress" {
		t.Fatalf("switch during run: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminAIProviderKey(t *testing.T) {
	const path = "/api/admin/ai/providers/anthropic/key"
	st := aireport.NewMemStore()
	kr := aireport.NewKeyring(st, testSecret, map[string]string{aireport.ProviderOpenAI: "sk-env-openai-key-4321"})
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{Keyring: kr}, apiKeyRuntime(aireport.RuntimeClaude))
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	if rec := a.do(http.MethodPut, path, `{"api_key":"`+apiTestKey+`"}`, false); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF %d", rec.Code)
	}
	for _, c := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{http.MethodPut, "/api/admin/ai/providers/gemini/key", `{"api_key":"` + apiTestKey + `"}`, http.StatusNotFound, "unknown_provider"},
		{http.MethodPut, path, `{"api_key":"  "}`, http.StatusBadRequest, "invalid_request"},
		{http.MethodPut, path, `not json`, http.StatusBadRequest, "invalid_request"},
		{http.MethodPut, "/api/admin/ai/providers/openai/key", `{"api_key":"` + apiTestKey + `"}`, http.StatusConflict, "env_managed"},
		{http.MethodDelete, "/api/admin/ai/providers/openai/key", ``, http.StatusConflict, "env_managed"},
	} {
		rec := a.do(c.method, c.path, c.body, true)
		if rec.Code != c.status || decodeBody(t, rec)["error"] != c.code {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), apiTestKey) {
			t.Errorf("%s %s echoes the key", c.method, c.path)
		}
	}

	rec := a.do(http.MethodPut, path, `{"api_key":"`+apiTestKey+`"}`, true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), apiTestKey) {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	cred, _ := decodeBody(t, rec)["credential"].(map[string]any)
	if cred["provider"] != "anthropic" || cred["source"] != "admin" || cred["key_hint"] != "7890" {
		t.Fatalf("credential = %v", cred)
	}
	if key, _ := kr.APIKey(aireport.ProviderAnthropic)(context.Background()); key != apiTestKey {
		t.Fatalf("stored key = %q", key)
	}

	rec = a.do(http.MethodDelete, path, "", true)
	if cred, _ = decodeBody(t, rec)["credential"].(map[string]any); rec.Code != http.StatusOK || cred["source"] != "none" || cred["key_hint"] != nil {
		t.Fatalf("delete: %d %v", rec.Code, cred)
	}

	// A store failure answers a fixed message and logs nothing of the key.
	st.CredentialErr = errFake("db down")
	rec = a.do(http.MethodPut, path, `{"api_key":"`+apiTestKey+`"}`, true)
	if rec.Code != http.StatusInternalServerError || decodeBody(t, rec)["error"] != "credential_store_failed" || strings.Contains(rec.Body.String(), apiTestKey) {
		t.Fatalf("store failure: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(logs.String(), apiTestKey) {
		t.Fatal("log carries the key")
	}
}

func TestAdminAIProviderKeyNeedsSecret(t *testing.T) {
	a := newAIRegistryTest(t, aiAdmin, aireport.NewMemStore(), aireport.Config{}, apiKeyRuntime(aireport.RuntimeOpenAI))
	rec := a.do(http.MethodPut, "/api/admin/ai/providers/openai/key", `{"api_key":"`+apiTestKey+`"}`, true)
	if rec.Code != http.StatusServiceUnavailable || decodeBody(t, rec)["error"] != "secrets_unavailable" {
		t.Fatalf("no secret: %d %s", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai", "", false)); body["secrets_reason"] == nil {
		t.Fatalf("no secret reason on the admin screen: %v", body)
	}
}

// A secret under 32 bytes refuses registration with the reason, on the screen
// and in the refusal.
func TestAdminAIProviderKeyShortSecret(t *testing.T) {
	st := aireport.NewMemStore()
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{Keyring: aireport.NewKeyring(st, "short-secret", nil)}, apiKeyRuntime(aireport.RuntimeOpenAI))
	rec := a.do(http.MethodPut, "/api/admin/ai/providers/openai/key", `{"api_key":"`+apiTestKey+`"}`, true)
	body := decodeBody(t, rec)
	if msg, _ := body["message"].(string); rec.Code != http.StatusServiceUnavailable || body["error"] != "secrets_unavailable" || !strings.Contains(msg, "32") {
		t.Fatalf("short secret: %d %v", rec.Code, body)
	}
	if reason, _ := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai", "", false))["secrets_reason"].(string); !strings.Contains(reason, "32") {
		t.Fatalf("secrets_reason = %q", reason)
	}
	if len(st.Credentials) != 0 {
		t.Fatal("a key was stored under a short secret")
	}
	ok := newAIRegistryTest(t, aiAdmin, st, aireport.Config{Keyring: aireport.NewKeyring(st, testSecret, nil)}, apiKeyRuntime(aireport.RuntimeOpenAI))
	if body := decodeBody(t, ok.do(http.MethodGet, "/api/admin/ai", "", false)); body["secrets_reason"] != nil {
		t.Fatalf("usable secret reason = %v", body["secrets_reason"])
	}
}

// Settings and key management take the browser session only, as account
// management does.
func TestAdminAISettingsRefuseTokenAuth(t *testing.T) {
	st := aireport.NewMemStore()
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{Keyring: aireport.NewKeyring(st, testSecret, nil)}, catalogAIRuntime(), apiKeyRuntime(aireport.RuntimeOpenAI)).withTokenAuth(aiAdmin)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/ai", ""},
		{http.MethodGet, "/api/admin/ai/models?runtime=openai-api", ""},
		{http.MethodPut, "/api/admin/ai/enabled", `{"enabled":false}`},
		{http.MethodPut, "/api/admin/ai/settings", `{"model":"","reasoning_effort":""}`},
		{http.MethodPut, "/api/admin/ai/runtime", `{"runtime":"openai-api"}`},
		{http.MethodPut, "/api/admin/ai/providers/openai/key", `{"api_key":"` + apiTestKey + `"}`},
		{http.MethodDelete, "/api/admin/ai/providers/openai/key", ""},
	} {
		rec := a.do(c.method, c.path, c.body, true)
		if rec.Code != http.StatusForbidden || decodeBody(t, rec)["error"] != "session_required" {
			t.Errorf("token %s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if st.Choice != nil || st.Enabled != nil || len(st.Credentials) != 0 || len(st.Settings) != 0 {
		t.Fatal("refused requests changed state")
	}
}

// Every AI settings write checks CSRF before anything else.
func TestAdminAIWritesRequireCSRF(t *testing.T) {
	st := aireport.NewMemStore()
	kr := aireport.NewKeyring(st, testSecret, nil)
	if _, err := kr.SetKey(context.Background(), aireport.ProviderAnthropic, apiTestKey, "a"); err != nil {
		t.Fatal(err)
	}
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{Keyring: kr}, catalogAIRuntime(), apiKeyRuntime(aireport.RuntimeOpenAI))
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/admin/ai/runtime", `{"runtime":"openai-api"}`},
		{http.MethodPut, "/api/admin/ai/enabled", `{"enabled":false}`},
		{http.MethodPut, "/api/admin/ai/settings", `{"model":"","reasoning_effort":""}`},
		{http.MethodPut, "/api/admin/ai/providers/openai/key", `{"api_key":"` + apiTestKey + `"}`},
		{http.MethodDelete, "/api/admin/ai/providers/anthropic/key", ""},
	} {
		if rec := a.do(c.method, c.path, c.body, false); rec.Code != http.StatusForbidden {
			t.Errorf("without CSRF %s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if st.Choice != nil || st.Enabled != nil || len(st.Settings) != 0 || st.Credentials[aireport.ProviderAnthropic] == nil || st.Credentials[aireport.ProviderOpenAI] != nil {
		t.Fatal("a request without CSRF changed state")
	}
}

// ?runtime= reads and writes another runtime's catalog and settings.
func TestAdminAISettingsForRuntime(t *testing.T) {
	claude := apiKeyRuntime(aireport.RuntimeClaude)
	claude.ModelsValue = []airuntime.Model{{ID: "claude-opus-5", DisplayName: "Opus 5"}}
	st := aireport.NewMemStore()
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{}, catalogAIRuntime(), claude)

	rec := a.do(http.MethodGet, "/api/admin/ai/models?runtime=claude-api", "", false)
	if models, _ := decodeBody(t, rec)["models"].([]any); rec.Code != http.StatusOK || len(models) != 1 {
		t.Fatalf("claude models: %d %s", rec.Code, rec.Body.String())
	}
	rec = a.do(http.MethodPut, "/api/admin/ai/settings?runtime=claude-api", `{"model":"claude-opus-5","reasoning_effort":""}`, true)
	if settings, _ := decodeBody(t, rec)["settings"].(map[string]any); rec.Code != http.StatusOK || settings["runtime"] != "claude-api" || settings["model"] != "claude-opus-5" {
		t.Fatalf("claude settings: %d %s", rec.Code, rec.Body.String())
	}
	if st.Settings["claude-api"] == nil || st.Settings["codex-app-server"] != nil {
		t.Fatalf("stored = %+v", st.Settings)
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/admin/ai/models?runtime=gemini", ""},
		{http.MethodPut, "/api/admin/ai/settings?runtime=gemini", `{"model":"","reasoning_effort":""}`},
	} {
		rec := a.do(c.method, c.path, c.body, true)
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["error"] != "unknown_runtime" {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
