package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/openairuntime"
	"cctrace/internal/store"
)

// GET /api/admin/ai says whether reports run and who decided; PUT
// /api/admin/ai/enabled sets or clears the admin's switch.
func TestAdminAIEnabledSwitch(t *testing.T) {
	const path = "/api/admin/ai/enabled"
	st := aireport.NewMemStore()
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{}, nil, apiKeyRuntime(aireport.RuntimeOpenAI))

	body := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai", "", false))
	if body["enabled"] != false || body["enabled_source"] != "default" || body["env_enabled"] != nil {
		t.Fatalf("default = %v", body)
	}
	for req, want := range map[string]struct {
		status int
		code   string
	}{
		`{}`:                {http.StatusBadRequest, "invalid_request"},
		`{"enabled":"yes"}`: {http.StatusBadRequest, "invalid_request"},
		`{"enabled":true}`:  {http.StatusConflict, "runtime_not_selected"},
	} {
		rec := a.do(http.MethodPut, path, req, true)
		if rec.Code != want.status || decodeBody(t, rec)["error"] != want.code {
			t.Errorf("%s: %d %s", req, rec.Code, rec.Body.String())
		}
	}
	if st.Enabled != nil {
		t.Fatalf("refused switch stored: %+v", st.Enabled)
	}

	if rec := a.do(http.MethodPut, "/api/admin/ai/runtime", `{"runtime":"openai-api"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("select: %d %s", rec.Code, rec.Body.String())
	}
	rec := a.do(http.MethodPut, path, `{"enabled":true}`, true)
	if body = decodeBody(t, rec); rec.Code != http.StatusOK || body["enabled"] != true || body["enabled_source"] != "admin" {
		t.Fatalf("on: %d %v", rec.Code, body)
	}
	if st.Enabled == nil || !st.Enabled.Enabled || st.Enabled.UpdatedBy != "admin@example.com" {
		t.Fatalf("stored = %+v", st.Enabled)
	}
	rec = a.do(http.MethodPut, path, `{"enabled":null}`, true)
	if body = decodeBody(t, rec); rec.Code != http.StatusOK || body["enabled"] != false || body["enabled_source"] != "default" || st.Enabled != nil {
		t.Fatalf("clear: %d %v", rec.Code, body)
	}
}

// The environment's word is shown beside the admin's, a set CCTRACE_AI_RUNTIME
// counting as on.
func TestAdminAIEnabledShowsEnvironment(t *testing.T) {
	st := aireport.NewMemStore()
	off := false
	_ = st.SetAIEnabledChoice(context.Background(), &off, "a")
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{EnvRuntime: aireport.RuntimeOpenAI}, apiKeyRuntime(aireport.RuntimeOpenAI))
	body := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai", "", false))
	if body["enabled"] != false || body["enabled_source"] != "admin" || body["env_enabled"] != true {
		t.Fatalf("admin off over env = %v", body)
	}
}

// While reports are off, starting a report and consenting answer
// runtime_disabled, and the report screen is told why.
func TestAIReportsRefusedWhileDisabled(t *testing.T) {
	st := aireport.NewMemStore()
	off := false
	_ = st.SetAIEnabledChoice(context.Background(), &off, "a")
	rt := aiRuntime()
	a := newAIRegistryTest(t, aiCaller, st, aireport.Config{EnvRuntime: aireport.DefaultRuntimeKey}, rt)
	wk, _ := aireport.ParseISOWeek("2026-W37", "Asia/Seoul")
	st.AddSegment("caller", store.AISegment{ID: 2, StartTs: wk.Since})

	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/ai-reports", `{"week":"2026-W37","tz":"Asia/Seoul"}`},
		{http.MethodPost, "/api/ai/consent", `{"runtime_key":"codex-app-server:chatgpt","disclosure_version":"` + aireport.DisclosureVersion + `"}`},
	} {
		rec := a.do(c.method, c.path, c.body, true)
		if rec.Code != http.StatusServiceUnavailable || decodeBody(t, rec)["error"] != "runtime_disabled" {
			t.Errorf("%s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if len(rt.Requests()) != 0 || len(st.Runs) != 0 {
		t.Fatal("a disabled report reached the runtime")
	}
	body := decodeBody(t, a.do(http.MethodGet, "/api/ai-reports?week=2026-W37&tz=Asia/Seoul", "", false))
	if runtime, _ := body["runtime"].(map[string]any); runtime["enabled"] != false {
		t.Fatalf("runtime = %v", body["runtime"])
	}
}

// While reports are off, reading the report screen asks the provider nothing:
// an OpenAI runtime's status would otherwise list models with the key.
func TestGetAIReportsWhileDisabledCallsNoProvider(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(provider.Close)
	openai := openairuntime.New(openairuntime.Config{
		BaseURL: provider.URL,
		APIKey:  func(context.Context) (string, error) { return "sk-test", nil },
	})
	off := false
	a := newAIRegistryTest(t, aiCaller, aireport.NewMemStore(), aireport.Config{EnvRuntime: aireport.RuntimeOpenAI, EnvEnabled: &off}, openai)

	rec := a.do(http.MethodGet, "/api/ai-reports?week=2026-W37&tz=Asia/Seoul", "", false)
	runtime, _ := decodeBody(t, rec)["runtime"].(map[string]any)
	if rec.Code != http.StatusOK || runtime["enabled"] != false || runtime["configured"] != false || runtime["available"] != false {
		t.Fatalf("disabled: %d %s", rec.Code, rec.Body.String())
	}
	if n := requests.Load(); n != 0 {
		t.Fatalf("provider requests while disabled = %d", n)
	}
}

// Turning reports on with a chosen runtime that is built but not configured is
// refused the way the admin screen blocks it, and nothing is stored.
func TestAdminAIEnabledRefusesUnconfiguredRuntime(t *testing.T) {
	st := aireport.NewMemStore()
	openai := apiKeyRuntime(aireport.RuntimeOpenAI)
	openai.StatusValue = airuntime.Status{Reason: "OpenAI API key is not set"}
	a := newAIRegistryTest(t, aiAdmin, st, aireport.Config{EnvRuntime: aireport.RuntimeOpenAI}, openai)

	rec := a.do(http.MethodPut, "/api/admin/ai/enabled", `{"enabled":true}`, true)
	if rec.Code != http.StatusConflict || decodeBody(t, rec)["error"] != "runtime_not_configured" {
		t.Fatalf("on with an unconfigured runtime: %d %s", rec.Code, rec.Body.String())
	}
	if st.Enabled != nil {
		t.Fatalf("refused switch stored: %+v", st.Enabled)
	}
}
