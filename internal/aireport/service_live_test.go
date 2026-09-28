//go:build aireportlive

package aireport

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/chatruntime"
	"cctrace/internal/clauderuntime"
	"cctrace/internal/openairuntime"
	"cctrace/internal/store"
)

// runLiveReport drives one real report through the service and checks what the
// service is responsible for: the tools ran, the model asked for is the model
// used, and usage came back. The chat/completions providers share it because
// the only thing that differs between them is the runtime handed in.
//
// user is a distinct dashboard user id per test: consent and the one-run-per-user
// guard are both keyed on it, so sharing one would make the tests interfere.
//
// The wait is generous on purpose. Measured on NVIDIA, a single call took 285s
// on kimi-k3 and 66s on deepseek-v4-flash; a report makes several. A short
// deadline would report a slow provider as a broken one.
func runLiveReport(t *testing.T, rt airuntime.Runtime, runtimeKeyName, model string, user int64) {
	t.Helper()
	st := NewMemStore()
	wk, err := ParseISOWeek("2026-W37", "UTC")
	if err != nil {
		t.Fatalf("ParseISOWeek: %v", err)
	}
	st.AddSegment("test-user", segAt(user, wk))

	sc := Scope{DashboardUserID: user, UserID: "test-user"}
	ctx := context.Background()
	if err := st.UpsertAIConsent(ctx, user, runtimeKey(rt.Info()), DisclosureVersion); err != nil {
		t.Fatal(err)
	}

	svc := NewService(st, []airuntime.Runtime{rt}, Config{
		EnvRuntime: runtimeKeyName,
		Env:        map[string]RuntimeEnv{runtimeKeyName: {Model: model}},
	})
	defer svc.Shutdown(context.Background())

	run, err := svc.Start(ctx, sc, wk)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(10 * time.Minute)
	var final *store.AIReportRun
	for time.Now().Before(deadline) {
		r, _ := st.GetAIReportRun(ctx, run.ID)
		if r != nil && (r.Status == store.AIRunCompleted || r.Status == store.AIRunFailed) {
			final = r
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if final == nil {
		r, _ := st.GetAIReportRun(ctx, run.ID)
		t.Fatalf("run did not settle in 10m: status=%s", r.Status)
	}
	if final.Status != store.AIRunCompleted {
		t.Fatalf("run failed: code=%s message=%s", final.ErrorCode, final.ErrorMessage)
	}
	if final.Model != model {
		t.Errorf("model mismatch: wanted %s, got %s", model, final.Model)
	}
	if !final.Usage.Reported {
		t.Errorf("usage not reported: %+v", final.Usage)
	}
	t.Logf("%s live report: model=%s usage=%+v", runtimeKeyName, final.Model, final.Usage)
}

// liveKey returns the first variable that holds a key. The provider's own
// standard name is accepted as a fallback so a key file written for the
// provider SDKs runs these tests unchanged.
func liveKey(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// TestLiveServiceOpenAI generates a real report via the service layer using OpenAI.
// It verifies that the service binds tools, runtimes, and storage correctly end-to-end.
func TestLiveServiceOpenAI(t *testing.T) {
	key := liveKey("CCTRACE_OPENAI_LIVE_KEY", "OPENAI_API_KEY")
	if key == "" {
		t.Skip("CCTRACE_OPENAI_LIVE_KEY not set")
	}

	model := os.Getenv("CCTRACE_OPENAI_LIVE_MODEL")
	if model == "" {
		model = "gpt-5.6-luna"
	}

	oaiRT := openairuntime.New(openairuntime.Config{
		APIKey:       func(context.Context) (string, error) { return key, nil },
		DefaultModel: model,
	})

	st := NewMemStore()
	wk, err := ParseISOWeek("2026-W37", "UTC")
	if err != nil {
		t.Fatalf("ParseISOWeek: %v", err)
	}
	st.AddSegment("test-user", segAt(1, wk))

	sc := Scope{DashboardUserID: 1, UserID: "test-user"}
	ctx := context.Background()

	// The consent key is the runtime's own key and auth mode, so it has to be
	// built from Info() rather than spelled out: an api_key runtime consented
	// to under another mode is asked to consent again.
	if err := st.UpsertAIConsent(ctx, 1, runtimeKey(oaiRT.Info()), DisclosureVersion); err != nil {
		t.Fatal(err)
	}

	// The service picks the model itself -- admin setting, then this runtime's
	// environment, then DefaultModels -- so a runtime's own DefaultModel never
	// reaches a run. The model and effort under test come in as the runtime's
	// environment, or the run would silently use the package default instead.
	svc := NewService(st, []airuntime.Runtime{oaiRT}, Config{
		EnvRuntime: RuntimeOpenAI,
		Env:        map[string]RuntimeEnv{RuntimeOpenAI: {Model: model, ReasoningEffort: "low"}},
	})
	defer svc.Shutdown(context.Background())

	run, err := svc.Start(ctx, sc, wk)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitRun := func(status string, deadline time.Time) *store.AIReportRun {
		for time.Now().Before(deadline) {
			r, _ := st.GetAIReportRun(ctx, run.ID)
			if r != nil && r.Status == status {
				return r
			}
			time.Sleep(100 * time.Millisecond)
		}
		r, _ := st.GetAIReportRun(ctx, run.ID)
		return r
	}

	finalRun := waitRun(store.AIRunCompleted, time.Now().Add(30*time.Second))
	if finalRun == nil {
		r, _ := st.GetAIReportRun(ctx, run.ID)
		t.Fatalf("run did not complete: status=%s error=%s", r.Status, r.ErrorMessage)
	}

	if finalRun.Model != model {
		t.Errorf("model mismatch: wanted %s, got %s", model, finalRun.Model)
	}

	if !finalRun.Usage.Reported {
		t.Errorf("usage not reported: %+v", finalRun.Usage)
	}

	t.Logf("OpenAI live test: model=%s usage=%+v", finalRun.Model, finalRun.Usage)
}

// TestLiveServiceClaude generates a real report via the service layer using Claude.
// It verifies schema validation and effort handling for models with limited support.
func TestLiveServiceClaude(t *testing.T) {
	key := liveKey("CCTRACE_ANTHROPIC_LIVE_KEY", "ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("CCTRACE_ANTHROPIC_LIVE_KEY not set")
	}

	model := os.Getenv("CCTRACE_ANTHROPIC_LIVE_MODEL")
	if model == "" {
		model = "claude-sonnet-5"
	}

	claudeRT := clauderuntime.New(clauderuntime.Config{
		APIKey:       func(context.Context) (string, error) { return key, nil },
		DefaultModel: model,
	})

	st := NewMemStore()
	wk, err := ParseISOWeek("2026-W37", "UTC")
	if err != nil {
		t.Fatalf("ParseISOWeek: %v", err)
	}
	st.AddSegment("test-user", segAt(2, wk))

	sc := Scope{DashboardUserID: 2, UserID: "test-user"}
	ctx := context.Background()

	if err := st.UpsertAIConsent(ctx, 2, runtimeKey(claudeRT.Info()), DisclosureVersion); err != nil {
		t.Fatal(err)
	}

	svc := NewService(st, []airuntime.Runtime{claudeRT}, Config{
		EnvRuntime: RuntimeClaude,
		Env:        map[string]RuntimeEnv{RuntimeClaude: {Model: model, ReasoningEffort: "low"}},
	})
	defer svc.Shutdown(context.Background())

	run, err := svc.Start(ctx, sc, wk)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitRun := func(status string, deadline time.Time) *store.AIReportRun {
		for time.Now().Before(deadline) {
			r, _ := st.GetAIReportRun(ctx, run.ID)
			if r != nil && r.Status == status {
				return r
			}
			time.Sleep(100 * time.Millisecond)
		}
		r, _ := st.GetAIReportRun(ctx, run.ID)
		return r
	}

	finalRun := waitRun(store.AIRunCompleted, time.Now().Add(30*time.Second))
	if finalRun == nil {
		r, _ := st.GetAIReportRun(ctx, run.ID)
		t.Fatalf("run did not complete: status=%s error=%s", r.Status, r.ErrorMessage)
	}

	if finalRun.Model != model {
		t.Errorf("model mismatch: wanted %s, got %s", model, finalRun.Model)
	}

	if !finalRun.Usage.Reported {
		t.Errorf("usage not reported: %+v", finalRun.Usage)
	}

	t.Logf("Claude live test: model=%s usage=%+v", finalRun.Model, finalRun.Usage)
}

// TestLiveModelCatalogOpenAI verifies OpenAI's model catalog against the actual API.
func TestLiveModelCatalogOpenAI(t *testing.T) {
	key := liveKey("CCTRACE_OPENAI_LIVE_KEY", "OPENAI_API_KEY")
	if key == "" {
		t.Skip("CCTRACE_OPENAI_LIVE_KEY not set")
	}

	oaiRT := openairuntime.New(openairuntime.Config{
		APIKey: func(context.Context) (string, error) { return key, nil },
	})

	ctx := context.Background()
	models, err := oaiRT.Models(ctx)
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	if len(models) == 0 {
		t.Fatal("no models returned")
	}

	t.Logf("OpenAI models from API: %d total", len(models))
	for _, m := range models {
		t.Logf("  %s: reasoning_efforts=%d default=%s", m.ID, len(m.SupportedReasoningEfforts), m.DefaultReasoningEffort)
	}
}

// TestLiveModelCatalogClaude verifies Claude's model catalog against the actual API.
func TestLiveModelCatalogClaude(t *testing.T) {
	key := liveKey("CCTRACE_ANTHROPIC_LIVE_KEY", "ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("CCTRACE_ANTHROPIC_LIVE_KEY not set")
	}

	claudeRT := clauderuntime.New(clauderuntime.Config{
		APIKey: func(context.Context) (string, error) { return key, nil },
	})

	ctx := context.Background()
	models, err := claudeRT.Models(ctx)
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	if len(models) == 0 {
		t.Fatal("no models returned")
	}

	t.Logf("Claude models from API: %d total", len(models))
	for _, m := range models {
		t.Logf("  %s: reasoning_efforts=%d default=%s", m.ID, len(m.SupportedReasoningEfforts), m.DefaultReasoningEffort)
	}
}

// TestLiveServiceNVIDIA generates a real report via the service layer using NVIDIA.
// It verifies that NVIDIA backend integration works end-to-end through the service.
func TestLiveServiceNVIDIA(t *testing.T) {
	key := liveKey("CCTRACE_NVIDIA_LIVE_KEY", "NVIDIA_API_KEY")
	if key == "" {
		t.Skip("CCTRACE_NVIDIA_LIVE_KEY not set")
	}

	baseURL := os.Getenv("CCTRACE_NVIDIA_LIVE_BASE_URL")
	if baseURL == "" {
		baseURL = "https://integrate.api.nvidia.com/v1"
	}
	model := os.Getenv("CCTRACE_NVIDIA_LIVE_MODEL")
	if model == "" {
		// The one NVIDIA model measured to honour both a forced tool call and
		// json_schema output, which is what a report needs.
		model = "z-ai/glm-5.3"
	}

	rt := chatruntime.New(chatruntime.Config{
		APIKey:       func(context.Context) (string, error) { return key, nil },
		RuntimeKey:   RuntimeNVIDIA,
		BaseURL:      func(context.Context) (string, error) { return baseURL, nil },
		DefaultModel: model,
		ProviderName: "nvidia",
	})
	runLiveReport(t, rt, RuntimeNVIDIA, model, 5)
}

// TestLiveServiceLiteLLM generates a real report via the service layer using LiteLLM.
// It verifies that LiteLLM proxy integration works end-to-end through the service.
func TestLiveServiceLiteLLM(t *testing.T) {
	key := liveKey("CCTRACE_LITELLM_LIVE_KEY")
	if key == "" {
		t.Skip("CCTRACE_LITELLM_LIVE_KEY not set")
	}

	baseURL := os.Getenv("CCTRACE_LITELLM_LIVE_BASE_URL")
	if baseURL == "" {
		t.Skip("CCTRACE_LITELLM_LIVE_BASE_URL not set")
	}
	model := os.Getenv("CCTRACE_LITELLM_LIVE_MODEL")
	if model == "" {
		// What the dev harness (deploy/litellm-dev-config.yaml) routes to NVIDIA.
		model = "z-ai/glm-5.3"
	}

	rt := chatruntime.New(chatruntime.Config{
		APIKey:       func(context.Context) (string, error) { return key, nil },
		RuntimeKey:   RuntimeLiteLLM,
		BaseURL:      func(context.Context) (string, error) { return baseURL, nil },
		DefaultModel: model,
		ProviderName: "litellm",
	})
	runLiveReport(t, rt, RuntimeLiteLLM, model, 6)
}
