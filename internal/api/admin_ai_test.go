package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestAdminAIRequiresAdmin(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	if rec := a.do(http.MethodGet, "/api/admin/ai", "", false); rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestAdminAIReportsRuntimeAndUsage(t *testing.T) {
	admin := &auth.DashboardUser{ID: 9, Role: "admin", Email: "admin@example.com"}
	rt := aiRuntime()
	used := 32.0
	rt.StatusValue.UsedPercent = &used
	a := newAITest(t, rt, admin)
	_, _ = a.st.CreateAIReportRun(context.Background(), &store.AIReportRun{DashboardUserID: 1, Status: store.AIRunFailed, StartedAt: time.Now(),
		Usage: store.AIUsage{Reported: true, InputTokens: 1000, OutputTokens: 50}})

	rec := a.do(http.MethodGet, "/api/admin/ai", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	runtimes := body["runtimes"].([]any)
	if len(runtimes) != 5 {
		t.Fatalf("runtimes = %v", runtimes)
	}
	codex := runtimes[0].(map[string]any)
	if codex["key"] != "codex-app-server" || codex["implemented"] != true || codex["configured"] != true || codex["selected"] != true ||
		codex["auth_mode"] != "chatgpt" || codex["personal_account_warning"] != true || codex["used_percent"] != 32.0 ||
		codex["account_email"] != "ops@example.com" || codex["credential"] != nil {
		t.Fatalf("codex = %v", codex)
	}
	// Runtimes this server did not build are listed, not configured, and not selectable.
	if other := runtimes[1].(map[string]any); other["key"] != "openai-api" || other["implemented"] != true || other["configured"] != false || other["selected"] != false {
		t.Fatalf("openai entry = %v", other)
	}
	// nvidia-api is listed but not implemented: this build cannot drive it end to
	// end, so it is refused wherever a runtime is chosen and the screen leaves it
	// out of the options. (The comment here used to say both NVIDIA and LiteLLM
	// were unimplemented while asserting the opposite for both.)
	if nvidia := runtimes[3].(map[string]any); nvidia["key"] != "nvidia-api" || nvidia["implemented"] != false || nvidia["configured"] != false || nvidia["provider"] != "nvidia" {
		t.Fatalf("nvidia entry = %v", nvidia)
	}
	if litellm := runtimes[4].(map[string]any); litellm["key"] != "litellm-api" || litellm["implemented"] != true || litellm["configured"] != false || litellm["provider"] != "litellm" {
		t.Fatalf("litellm entry = %v", litellm)
	}
	// The model is the effective setting, no longer env-only.
	if _, pinned := body["model_env_managed"]; body["model"] != "gpt-5.6-terra" || pinned {
		t.Fatalf("body = %v", body)
	}
	usage := body["usage_this_week"].(map[string]any)
	if usage["runs"] != 1.0 || usage["failed"] != 1.0 || usage["input_tokens"] != 1000.0 {
		t.Fatalf("usage = %v", usage)
	}
}
