package api

import (
	"fmt"
	"net/http"
	"testing"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
)

var aiAdmin = &auth.DashboardUser{ID: 9, Role: "admin", Email: "admin@example.com"}

func catalogAIRuntime() *airuntime.FakeRuntime {
	rt := aiRuntime()
	efforts := []airuntime.ReasoningEffortOption{{ReasoningEffort: "medium", Description: "balanced"}, {ReasoningEffort: "high", Description: "deeper"}}
	rt.ModelsValue = []airuntime.Model{
		{ID: aireport.DefaultModel, DisplayName: "GPT-5.6 Terra", Description: "d", IsDefault: true, DefaultReasoningEffort: "medium", SupportedReasoningEfforts: efforts},
		{ID: "gpt-small", DisplayName: "Small", DefaultReasoningEffort: "medium", SupportedReasoningEfforts: efforts[:1]},
	}
	return rt
}

func TestAdminAIReportsEffectiveSettings(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiAdmin)
	body := decodeBody(t, a.do(http.MethodGet, "/api/admin/ai", "", false))
	settings, _ := body["settings"].(map[string]any)
	source, _ := settings["source"].(map[string]any)
	if settings["model"] != aireport.DefaultModel || settings["reasoning_effort"] != "" ||
		source["model"] != "default" || source["reasoning_effort"] != "default" ||
		settings["env_model"] != "" || settings["env_reasoning_effort"] != "" {
		t.Fatalf("settings = %v", body["settings"])
	}
	if body["model"] != aireport.DefaultModel {
		t.Fatalf("model = %v, want the effective model", body["model"])
	}
}

func TestAdminAIModels(t *testing.T) {
	if rec := newAITest(t, catalogAIRuntime(), aiCaller).do(http.MethodGet, "/api/admin/ai/models", "", false); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status %d", rec.Code)
	}

	rec := newAITest(t, catalogAIRuntime(), aiAdmin).do(http.MethodGet, "/api/admin/ai/models", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	models, _ := decodeBody(t, rec)["models"].([]any)
	if len(models) != 2 {
		t.Fatalf("models = %v", models)
	}
	m := models[0].(map[string]any)
	efforts, _ := m["supported_reasoning_efforts"].([]any)
	if m["id"] != aireport.DefaultModel || m["display_name"] != "GPT-5.6 Terra" || m["is_default"] != true ||
		m["default_reasoning_effort"] != "medium" || len(efforts) != 2 ||
		efforts[1].(map[string]any)["reasoning_effort"] != "high" {
		t.Fatalf("model = %v", m)
	}
}

// Unconfigured, logged out and unreachable are different fixes, so the
// screen gets different codes.
func TestAdminAIModelsErrors(t *testing.T) {
	if rec := newAITest(t, nil, aiAdmin).do(http.MethodGet, "/api/admin/ai/models", "", false); rec.Code != http.StatusServiceUnavailable || decodeBody(t, rec)["error"] != "runtime_unconfigured" {
		t.Fatalf("unconfigured: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("%w: no auth.json", airuntime.ErrNotLoggedIn), http.StatusServiceUnavailable, "runtime_not_logged_in"},
		{fmt.Errorf("%w: exit 1", airuntime.ErrUnavailable), http.StatusBadGateway, "catalog_unavailable"},
	} {
		rt := catalogAIRuntime()
		rt.ModelsErr = c.err
		rec := newAITest(t, rt, aiAdmin).do(http.MethodGet, "/api/admin/ai/models", "", false)
		if rec.Code != c.status || decodeBody(t, rec)["error"] != c.code {
			t.Fatalf("%v: %d %s", c.err, rec.Code, rec.Body.String())
		}
	}
}

func TestAdminAISetSettings(t *testing.T) {
	const path = "/api/admin/ai/settings"
	a := newAITest(t, catalogAIRuntime(), aiAdmin)

	if rec := a.do(http.MethodPut, path, `{"model":"gpt-small","reasoning_effort":""}`, false); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF status %d", rec.Code)
	}
	if rec := newAITest(t, catalogAIRuntime(), aiCaller).do(http.MethodPut, path, `{"model":"gpt-small","reasoning_effort":""}`, true); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status %d", rec.Code)
	}
	for body, code := range map[string]string{
		`{"model":"gpt-small"}`:                                                  "invalid_request",
		`{"model":"gpt-nope","reasoning_effort":""}`:                             "invalid_model",
		`{"model":"gpt-small","reasoning_effort":"high"}`:                        "invalid_reasoning_effort",
		`{"model":"gpt-small","reasoning_effort":"","base_url":"invalid://url"}`: "invalid_base_url",
	} {
		rec := a.do(http.MethodPut, path, body, true)
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["error"] != code {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(a.st.Settings) != 0 {
		t.Fatalf("rejected requests stored %+v", a.st.Settings)
	}

	rec := a.do(http.MethodPut, path, `{"model":"gpt-small","reasoning_effort":"medium"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	settings, _ := decodeBody(t, rec)["settings"].(map[string]any)
	if source, _ := settings["source"].(map[string]any); settings["model"] != "gpt-small" || source["model"] != "admin" || source["reasoning_effort"] != "admin" {
		t.Fatalf("settings = %v", settings)
	}
	if a.st.Settings["codex-app-server"] == nil || a.st.Settings["codex-app-server"].UpdatedBy != "admin@example.com" {
		t.Fatalf("stored = %+v", a.st.Settings)
	}

	// Empty values hand both items back to the environment layer.
	rec = a.do(http.MethodPut, path, `{"model":"","reasoning_effort":""}`, true)
	settings, _ = decodeBody(t, rec)["settings"].(map[string]any)
	if source, _ := settings["source"].(map[string]any); rec.Code != http.StatusOK || settings["model"] != aireport.DefaultModel || source["model"] != "default" {
		t.Fatalf("clear: %d %v", rec.Code, settings)
	}
}
