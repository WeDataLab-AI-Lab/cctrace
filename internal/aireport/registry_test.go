package aireport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

func apiRuntime(key string) *airuntime.FakeRuntime {
	rt := readyRuntime(nil, goodOutput)
	rt.InfoValue = airuntime.Info{Key: key, AuthMode: airuntime.AuthModeAPIKey}
	return rt
}

func newRegistryService(t *testing.T, st *MemStore, cfg Config, rts ...airuntime.Runtime) *Service {
	t.Helper()
	svc := NewService(st, rts, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		svc.Shutdown(ctx)
	})
	return svc
}

// Admin choice > CCTRACE_AI_RUNTIME, and nothing else: no choice leaves no
// runtime active, and an explicit choice this server cannot run never falls
// through to another provider.
func TestActiveRuntimeExplicitOnly(t *testing.T) {
	ctx := context.Background()
	codex, openai, claude := readyRuntime(nil, goodOutput), apiRuntime(RuntimeOpenAI), apiRuntime(RuntimeClaude)
	st := NewMemStore()
	svc := newRegistryService(t, st, Config{}, codex, openai, claude)

	if sel, _ := svc.Selection(ctx); sel.Key != "" || sel.Source != SourceDefault {
		t.Fatalf("no choice = %+v", sel)
	}
	if info, status := svc.RuntimeStatus(ctx); info.Key != DefaultRuntimeKey || status.Configured {
		t.Fatalf("no choice status = %+v %+v", info, status)
	}
	if _, err := svc.Settings(ctx); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("no choice settings: %v", err)
	}
	svc.cfg.EnvRuntime = RuntimeClaude
	if sel, _ := svc.Selection(ctx); sel.Key != RuntimeClaude || sel.Source != SourceEnv {
		t.Fatalf("env = %+v", sel)
	}
	_ = st.SetAIRuntimeChoice(ctx, RuntimeOpenAI, "a")
	if sel, _ := svc.Selection(ctx); sel.Key != RuntimeOpenAI || sel.Source != SourceAdmin || sel.EnvRuntime != RuntimeClaude {
		t.Fatalf("admin = %+v", sel)
	}
	if info, _ := svc.RuntimeStatus(ctx); info.Key != RuntimeOpenAI {
		t.Fatalf("status key = %q", info.Key)
	}

	// Only openai-api built: the codex choice stays codex, with nothing to run.
	f := newFixture(t, nil)
	only := newRegistryService(t, f.st, Config{EnvRuntime: RuntimeOpenAI}, nil, openai)
	_ = f.st.UpsertAIConsent(ctx, 1, RuntimeOpenAI+":api_key", DisclosureVersion)
	_ = f.st.SetAIRuntimeChoice(ctx, DefaultRuntimeKey, "a")
	if sel, _ := only.Selection(ctx); sel.Key != DefaultRuntimeKey || sel.Source != SourceAdmin {
		t.Fatalf("unbuilt admin choice = %+v", sel)
	}
	if _, err := only.Start(ctx, f.sc, f.wk); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("start on unbuilt choice: %v", err)
	}
	if len(openai.Requests()) != 0 {
		t.Fatal("another provider ran in place of the chosen runtime")
	}
	// The same for CCTRACE_AI_RUNTIME, and for a name no runtime has.
	for _, env := range []string{DefaultRuntimeKey, "gemini"} {
		envOnly := newRegistryService(t, NewMemStore(), Config{EnvRuntime: env}, nil, openai)
		if sel, _ := envOnly.Selection(ctx); sel.Key != env || sel.Source != SourceEnv {
			t.Fatalf("env %s = %+v", env, sel)
		}
		if info, status := envOnly.RuntimeStatus(ctx); info.Key == RuntimeOpenAI || status.Configured {
			t.Fatalf("env %s status = %+v %+v", env, info, status)
		}
	}

	none := newRegistryService(t, NewMemStore(), Config{EnvEnabled: boolPtr(true)})
	if _, err := none.Start(ctx, Scope{DashboardUserID: 1}, testWeek(t)); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("no runtime: %v", err)
	}
}

// With no explicit choice nothing is active, even when a runtime has what it
// needs: an API key in the environment does not start sending data.
func TestNoChoiceIgnoresConfiguredRuntime(t *testing.T) {
	ctx := context.Background()
	codex := readyRuntime(nil, goodOutput)
	codex.StatusValue = airuntime.Status{Reason: "codex home is not set"}
	svc := newRegistryService(t, NewMemStore(), Config{}, codex, apiRuntime(RuntimeOpenAI))
	if sel, _ := svc.Selection(ctx); sel.Key != "" {
		t.Fatalf("selection = %+v", sel)
	}
}

// The admin screen says why an explicit choice cannot run.
func TestRuntimeViewsExplainUnusableSelection(t *testing.T) {
	ctx := context.Background()
	unconfigured := apiRuntime(RuntimeClaude)
	unconfigured.StatusValue = airuntime.Status{Reason: "Anthropic API key is not set"}
	st := NewMemStore()
	svc := newRegistryService(t, st, Config{EnvRuntime: DefaultRuntimeKey, Missing: map[string]string{DefaultRuntimeKey: "codex CLI not found"}}, nil, apiRuntime(RuntimeOpenAI), unconfigured)

	_, sel, err := svc.RuntimeViews(ctx)
	if err != nil || sel.Key != DefaultRuntimeKey || !strings.Contains(sel.Reason, "codex CLI not found") {
		t.Fatalf("unbuilt = %+v, %v", sel, err)
	}
	_ = st.SetAIRuntimeChoice(ctx, RuntimeClaude, "a")
	if _, sel, _ = svc.RuntimeViews(ctx); !strings.Contains(sel.Reason, "Anthropic API key is not set") {
		t.Fatalf("unconfigured = %+v", sel)
	}
	_ = st.SetAIRuntimeChoice(ctx, RuntimeOpenAI, "a")
	if _, sel, _ = svc.RuntimeViews(ctx); sel.Reason != "" {
		t.Fatalf("usable = %+v", sel)
	}
	svc.cfg.EnvRuntime = "gemini"
	_ = st.SetAIRuntimeChoice(ctx, "", "a")
	if _, sel, _ = svc.RuntimeViews(ctx); sel.Key != "gemini" || sel.Reason == "" {
		t.Fatalf("unknown env runtime = %+v", sel)
	}
}

// Each run reads the active runtime when it starts, runs on it with that
// runtime's settings, and records it.
func TestStartUsesActiveRuntimeAndItsSettings(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	codex, claude := readyRuntime(nil, goodOutput), apiRuntime(RuntimeClaude)
	svc := newRegistryService(t, f.st, Config{EnvRuntime: DefaultRuntimeKey, Env: map[string]RuntimeEnv{
		DefaultRuntimeKey: {Model: "gpt-env", ReasoningEffort: "low"},
	}}, codex, claude)
	_ = f.st.UpsertAIConsent(ctx, 1, RuntimeClaude+":api_key", DisclosureVersion)

	run, err := svc.Start(ctx, f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitRun(t, f.st, run.ID, store.AIRunCompleted); got.Runtime != DefaultRuntimeKey || got.Model != "gpt-env" {
		t.Fatalf("codex run = %+v", got)
	}

	if _, err := svc.SelectRuntime(ctx, RuntimeClaude, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	run, err = svc.Start(ctx, f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	got := waitRun(t, f.st, run.ID, store.AIRunCompleted)
	// The codex env effort must not leak into another runtime's run.
	if got.Runtime != RuntimeClaude || got.Model != "claude-sonnet-5" || got.AuthMode != airuntime.AuthModeAPIKey {
		t.Fatalf("claude run = %+v", got)
	}
	if reqs := claude.Requests(); len(reqs) != 1 || reqs[0].Model != "claude-sonnet-5" || reqs[0].ReasoningEffort != "" {
		t.Fatalf("claude requests = %+v", reqs)
	}
	if len(codex.Requests()) != 1 {
		t.Fatalf("codex ran %d times", len(codex.Requests()))
	}
}

// Consent is per runtime: switching asks for consent again.
func TestConsentFollowsActiveRuntime(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	svc := newRegistryService(t, f.st, Config{EnvRuntime: DefaultRuntimeKey}, readyRuntime(nil, goodOutput), apiRuntime(RuntimeOpenAI))
	if cs, _ := svc.Consent(ctx, f.sc); !cs.Granted || cs.RuntimeKey != testRuntimeKey {
		t.Fatalf("codex consent = %+v", cs)
	}
	if _, err := svc.SelectRuntime(ctx, RuntimeOpenAI, "a"); err != nil {
		t.Fatal(err)
	}
	cs, _ := svc.Consent(ctx, f.sc)
	if cs.Granted || cs.RuntimeKey != RuntimeOpenAI+":api_key" {
		t.Fatalf("openai consent = %+v", cs)
	}
	if _, err := svc.Start(ctx, f.sc, f.wk); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("start without consent: %v", err)
	}
	if err := svc.GrantConsent(ctx, f.sc, testRuntimeKey, DisclosureVersion); !errors.Is(err, ErrConsentMismatch) {
		t.Fatalf("stale runtime consent: %v", err)
	}
}

func TestSettingsArePerRuntime(t *testing.T) {
	ctx := context.Background()
	codex, openai, claude := catalogRuntime(), apiRuntime(RuntimeOpenAI), apiRuntime(RuntimeClaude)
	claude.ModelsValue = []airuntime.Model{{ID: "claude-sonnet-5", SupportedReasoningEfforts: []airuntime.ReasoningEffortOption{{ReasoningEffort: "high"}}}, {ID: "claude-opus-5"}}
	svc := newRegistryService(t, NewMemStore(), Config{EnvRuntime: DefaultRuntimeKey, Env: map[string]RuntimeEnv{
		DefaultRuntimeKey: {Model: "gpt-env", ReasoningEffort: "medium"},
		RuntimeOpenAI:     {Model: "gpt-openai-env"},
	}}, codex, openai, claude)

	cases := map[string]Settings{
		DefaultRuntimeKey: {Runtime: DefaultRuntimeKey, Model: "gpt-env", ModelSource: SourceEnv, ReasoningEffort: "medium", ReasoningEffortSource: SourceEnv, BaseURL: "", BaseURLSource: SourceDefault, EnvModel: "gpt-env", EnvReasoningEffort: "medium", EnvBaseURL: ""},
		RuntimeOpenAI:     {Runtime: RuntimeOpenAI, Model: "gpt-openai-env", ModelSource: SourceEnv, ReasoningEffortSource: SourceDefault, BaseURL: "", BaseURLSource: SourceDefault, EnvModel: "gpt-openai-env", EnvBaseURL: ""},
		RuntimeClaude:     {Runtime: RuntimeClaude, Model: "claude-sonnet-5", ModelSource: SourceDefault, ReasoningEffortSource: SourceDefault, BaseURL: "", BaseURLSource: SourceDefault, EnvBaseURL: ""},
	}
	for key, want := range cases {
		if got, err := svc.SettingsFor(ctx, key); err != nil || got != want {
			t.Errorf("%s = %+v, %v", key, got, err)
		}
	}

	// Validated against that runtime's catalog: a codex model is not a Claude model.
	if _, err := svc.SetSettings(ctx, RuntimeClaude, DefaultModel, "", "", "a"); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("codex model on claude: %v", err)
	}
	got, err := svc.SetSettings(ctx, RuntimeClaude, "claude-sonnet-5", "high", "", "a")
	if err != nil || got.Runtime != RuntimeClaude || got.Model != "claude-sonnet-5" || got.ModelSource != SourceAdmin {
		t.Fatalf("claude set = %+v, %v", got, err)
	}
	if codexNow, _ := svc.SettingsFor(ctx, DefaultRuntimeKey); codexNow != cases[DefaultRuntimeKey] {
		t.Fatalf("codex changed: %+v", codexNow)
	}
	if models, err := svc.Models(ctx, RuntimeClaude); err != nil || len(models) != 2 {
		t.Fatalf("claude models = %v, %v", models, err)
	}
	// "" is the active runtime.
	if active, _ := svc.Settings(ctx); active.Runtime != DefaultRuntimeKey {
		t.Fatalf("active settings = %+v", active)
	}
	if _, err := svc.SettingsFor(ctx, "gemini"); !errors.Is(err, ErrUnknownRuntime) {
		t.Fatalf("unknown runtime: %v", err)
	}
	if _, err := newRegistryService(t, NewMemStore(), Config{}, codex).Models(ctx, RuntimeClaude); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("unbuilt runtime models: %v", err)
	}
}

func TestSelectRuntimeRefusals(t *testing.T) {
	ctx := context.Background()
	unconfigured := apiRuntime(RuntimeClaude)
	unconfigured.StatusValue = airuntime.Status{Reason: "Anthropic API key is not set"}
	st := NewMemStore()
	svc := newRegistryService(t, st, Config{}, nil, apiRuntime(RuntimeOpenAI), unconfigured)

	if _, err := svc.SelectRuntime(ctx, "gemini", "a"); !errors.Is(err, ErrUnknownRuntime) {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := svc.SelectRuntime(ctx, DefaultRuntimeKey, "a"); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("not built: %v", err)
	}
	if _, err := svc.SelectRuntime(ctx, RuntimeClaude, "a"); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("unconfigured: %v", err)
	}
	if st.Choice != nil {
		t.Fatalf("refused choice stored: %+v", st.Choice)
	}
	sel, err := svc.SelectRuntime(ctx, RuntimeOpenAI, "admin@example.com")
	if err != nil || sel.Key != RuntimeOpenAI || sel.Source != SourceAdmin || st.Choice.UpdatedBy != "admin@example.com" {
		t.Fatalf("select = %+v, %v", sel, err)
	}
	if sel, err = svc.SelectRuntime(ctx, "", "a"); err != nil || sel.Source != SourceDefault || sel.Key != "" {
		t.Fatalf("clear = %+v, %v", sel, err)
	}
}

// A runtime switch is refused while a report runs, as an account change is.
func TestSelectRuntimeRefusedWhileRunRunning(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	codex := readyRuntime([]airuntime.FakeStep{{WaitForCancel: true}}, goodOutput)
	svc := newRegistryService(t, f.st, Config{EnvRuntime: DefaultRuntimeKey}, codex, apiRuntime(RuntimeOpenAI))
	run, err := svc.Start(ctx, f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SelectRuntime(ctx, RuntimeOpenAI, "a"); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("switch during run: %v", err)
	}
	if err := svc.Cancel(ctx, f.sc, run.ID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, f.st, run.ID, store.AIRunCanceled)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err = svc.SelectRuntime(ctx, RuntimeOpenAI, "a"); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("switch after run: %v", err)
	}
}

// A key that cannot be decrypted leaves its runtime unconfigured with the
// re-register reason, whatever the runtime itself says.
func TestRuntimeViewsReportCredentials(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	if _, err := NewKeyring(st, testSecret, nil).SetKey(ctx, ProviderAnthropic, testKey, "a"); err != nil {
		t.Fatal(err)
	}
	claude := apiRuntime(RuntimeClaude)
	kr := NewKeyring(st, otherSecret, map[string]string{ProviderOpenAI: "env-openai-key-9876"})
	svc := newRegistryService(t, st, Config{Keyring: kr, EnvRuntime: RuntimeOpenAI, Missing: map[string]string{DefaultRuntimeKey: "codex CLI not found"}}, nil, apiRuntime(RuntimeOpenAI), claude)

	views, _, err := svc.RuntimeViews(ctx)
	if err != nil || len(views) != 5 {
		t.Fatalf("views = %+v, %v", views, err)
	}
	codex, openai, cl, nvidia, litellm := views[0], views[1], views[2], views[3], views[4]
	if codex.Key != DefaultRuntimeKey || codex.Built || codex.Status.Configured || codex.Status.Reason != "codex CLI not found" || codex.Credential != nil || codex.Provider != "codex" {
		t.Errorf("codex = %+v", codex)
	}
	if openai.Key != RuntimeOpenAI || !openai.Built || !openai.Selected || !openai.Status.Configured ||
		openai.Credential == nil || openai.Credential.Source != CredentialEnv || openai.Credential.KeyHint != "9876" {
		t.Errorf("openai = %+v", openai)
	}
	if cl.Status.Configured || cl.Status.Available || cl.Status.Reason != ReasonReRegister || cl.Credential.Source != CredentialAdmin {
		t.Errorf("claude = %+v cred %+v", cl, cl.Credential)
	}
	if nvidia.Key != RuntimeNVIDIA || nvidia.Built || nvidia.Provider != ProviderNVIDIA || nvidia.Credential == nil {
		t.Errorf("nvidia = %+v", nvidia)
	}
	if litellm.Key != RuntimeLiteLLM || litellm.Built || litellm.Provider != ProviderLiteLLM || litellm.Credential == nil {
		t.Errorf("litellm = %+v", litellm)
	}
	if _, err := svc.SelectRuntime(ctx, RuntimeClaude, "a"); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("select undecryptable: %v", err)
	}
	if svc.Keyring() != kr {
		t.Fatal("Keyring must be the configured one")
	}
}

// Codex account changes are refused only while a run uses Codex: a run on an
// API runtime shares nothing with the Codex home.
func TestAccountGuardScopedToAccountRuntime(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	codex := newAccountRuntime()
	openai := apiRuntime(RuntimeOpenAI)
	openai.Steps = []airuntime.FakeStep{{WaitForCancel: true}}
	svc := newRegistryService(t, f.st, Config{EnvEnabled: boolPtr(true)}, codex, openai)
	_ = f.st.UpsertAIConsent(ctx, 1, RuntimeOpenAI+":api_key", DisclosureVersion)
	if _, err := svc.SelectRuntime(ctx, RuntimeOpenAI, "a"); err != nil {
		t.Fatal(err)
	}
	run, err := svc.Start(ctx, f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.Accounts()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.LoginAPIKey(ctx, "sk-x"); err != nil {
		t.Fatalf("codex change during an openai run: %v", err)
	}
	_ = svc.Cancel(ctx, f.sc, run.ID)
	waitRun(t, f.st, run.ID, store.AIRunCanceled)
}

// gatedStore holds Start after it has read the active runtime.
type gatedStore struct {
	*MemStore
	entered, release chan struct{}
}

func (g *gatedStore) AIWeekAggregate(ctx context.Context, sc store.AISegmentScope) (*store.AIWeekAggregate, error) {
	close(g.entered)
	<-g.release
	return g.MemStore.AIWeekAggregate(ctx, sc)
}

// A run that read the runtime before a switch completed must not start on the
// old runtime after the switch has answered.
func TestStartAcrossRuntimeSwitchFails(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	gs := &gatedStore{MemStore: f.st, entered: make(chan struct{}), release: make(chan struct{})}
	codex := readyRuntime(nil, goodOutput)
	svc := NewService(gs, []airuntime.Runtime{codex, apiRuntime(RuntimeOpenAI)}, Config{EnvRuntime: DefaultRuntimeKey})
	t.Cleanup(func() { svc.Shutdown(context.Background()) })

	errc := make(chan error, 1)
	go func() {
		_, err := svc.Start(ctx, f.sc, f.wk)
		errc <- err
	}()
	<-gs.entered
	if _, err := svc.SelectRuntime(ctx, RuntimeOpenAI, "a"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	close(gs.release)
	if err := <-errc; !errors.Is(err, ErrRuntimeChanging) {
		t.Fatalf("start across switch: %v", err)
	}
	if len(codex.Requests()) != 0 {
		t.Fatal("the old runtime ran")
	}
	for _, run := range f.st.Runs {
		if run.Status == store.AIRunRunning {
			t.Fatalf("run left running: %+v", run)
		}
	}
}

// A chosen API runtime whose key was deleted after it was chosen refuses the
// run; the other configured runtimes, and their providers, run nothing.
func TestStartWithUnconfiguredChoiceRunsNothing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	codex, openai, claude := readyRuntime(nil, goodOutput), apiRuntime(RuntimeOpenAI), apiRuntime(RuntimeClaude)
	svc := newRegistryService(t, f.st, Config{EnvEnabled: boolPtr(true)}, codex, openai, claude)
	for _, key := range []string{RuntimeOpenAI + ":api_key", RuntimeClaude + ":api_key"} {
		_ = f.st.UpsertAIConsent(ctx, 1, key, DisclosureVersion)
	}
	if _, err := svc.SelectRuntime(ctx, RuntimeOpenAI, "a"); err != nil {
		t.Fatal(err)
	}
	openai.StatusValue = airuntime.Status{Reason: "OpenAI API key is not set"}

	if _, err := svc.Start(ctx, f.sc, f.wk); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("start on a keyless choice: %v", err)
	}
	if len(codex.Requests())+len(openai.Requests())+len(claude.Requests()) != 0 || len(f.st.Runs) != 0 {
		t.Fatalf("a run started: codex %d, openai %d, claude %d, runs %d", len(codex.Requests()), len(openai.Requests()), len(claude.Requests()), len(f.st.Runs))
	}
}

// The selection says why reports cannot run in the server's words: nothing
// chosen, or a choice this server cannot run.
func TestSelectionReasonCodes(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	openai := apiRuntime(RuntimeOpenAI)
	svc := newRegistryService(t, st, Config{}, openai)

	if _, sel, err := svc.RuntimeViews(ctx); err != nil || sel.ReasonCode != ReasonRuntimeNotSelected || sel.Reason == "" {
		t.Fatalf("nothing chosen = %+v, %v", sel, err)
	}
	_ = st.SetAIRuntimeChoice(ctx, RuntimeOpenAI, "a")
	if _, sel, _ := svc.RuntimeViews(ctx); sel.ReasonCode != "" || sel.Reason != "" {
		t.Fatalf("usable choice = %+v", sel)
	}
	openai.StatusValue = airuntime.Status{Reason: "OpenAI API key is not set"}
	if _, sel, _ := svc.RuntimeViews(ctx); sel.ReasonCode != ReasonRuntimeUnavailable || !strings.Contains(sel.Reason, "OpenAI API key is not set") {
		t.Fatalf("unusable choice = %+v", sel)
	}
}
