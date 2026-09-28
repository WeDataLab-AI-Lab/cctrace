package api

import (
	"net/http"
	"testing"

	"cctrace/internal/aireport"
)

func TestGetAIConsentServesServerText(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	rec := a.do(http.MethodGet, "/api/ai/consent", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["granted"] != false || body["granted_at"] != nil || body["runtime_key"] != "codex-app-server:chatgpt" ||
		body["disclosure_version"] != aireport.DisclosureVersion || body["provider_retention"] != "확인되지 않음" {
		t.Fatalf("body = %v", body)
	}
	if len(body["sends"].([]any)) != len(aireport.ConsentSends) || len(body["not_sends"].([]any)) != len(aireport.ConsentNotSends) {
		t.Fatalf("text lists = %v", body)
	}
}

func TestPostAIConsent(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	good := `{"runtime_key":"codex-app-server:chatgpt","disclosure_version":"` + aireport.DisclosureVersion + `"}`

	if rec := a.do(http.MethodPost, "/api/ai/consent", good, false); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF status %d", rec.Code)
	}
	stale := `{"runtime_key":"codex-app-server:chatgpt","disclosure_version":"2020-01-01"}`
	if rec := a.do(http.MethodPost, "/api/ai/consent", stale, true); rec.Code != http.StatusConflict {
		t.Fatalf("stale version status %d", rec.Code)
	}
	if rec := a.do(http.MethodPost, "/api/ai/consent", `{`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json status %d", rec.Code)
	}
	if rec := a.do(http.MethodPost, "/api/ai/consent", good, true); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, a.do(http.MethodGet, "/api/ai/consent", "", false))
	if body["granted"] != true || body["granted_at"] == nil {
		t.Fatalf("after grant = %v", body)
	}

	unconfigured := newAITest(t, nil, aiCaller)
	rec := unconfigured.do(http.MethodPost, "/api/ai/consent", `{"runtime_key":"codex-app-server:none","disclosure_version":"`+aireport.DisclosureVersion+`"}`, true)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status %d", rec.Code)
	}
}

// The disclosure names where the data goes for the active runtime, and states
// only what is known about its retention.
func TestGetAIConsentNamesActiveProvider(t *testing.T) {
	st := aireport.NewMemStore()
	a := newAIRegistryTest(t, aiCaller, st, aireport.Config{EnvRuntime: aireport.RuntimeOpenAI},
		aiRuntime(), apiKeyRuntime(aireport.RuntimeOpenAI), apiKeyRuntime(aireport.RuntimeClaude))
	for _, c := range []struct{ choice, runtime, provider, retention string }{
		{"", "openai-api", "OpenAI", aireport.ProviderRetentions[aireport.RuntimeOpenAI]},
		{"claude-api", "claude-api", "Anthropic", "확인되지 않음"},
		{"codex-app-server", "codex-app-server", "OpenAI (Codex)", "확인되지 않음"},
	} {
		if c.choice != "" {
			_ = st.SetAIRuntimeChoice(t.Context(), c.choice, "admin@example.com")
		}
		body := decodeBody(t, a.do(http.MethodGet, "/api/ai/consent", "", false))
		if body["runtime"] != c.runtime || body["provider"] != c.provider || body["provider_retention"] != c.retention {
			t.Errorf("%s: %v", c.runtime, body)
		}
	}
	if r := aireport.ProviderRetentions[aireport.RuntimeOpenAI]; r == "" || r == "확인되지 않음" {
		t.Fatalf("openai retention = %q", r)
	}
}
