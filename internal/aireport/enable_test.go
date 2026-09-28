package aireport

import (
	"context"
	"errors"
	"sync"
	"testing"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

func boolPtr(v bool) *bool { return &v }

// The admin's switch > CCTRACE_AI_ENABLED > a set CCTRACE_AI_RUNTIME (the old
// way of turning reports on) > off.
func TestEnablementPrecedence(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		cfg    Config
		admin  *bool
		want   bool
		source string
	}{
		{"nothing", Config{}, nil, false, SourceDefault},
		{"legacy runtime", Config{EnvRuntime: RuntimeOpenAI}, nil, true, SourceEnv},
		{"env off beats legacy runtime", Config{EnvRuntime: RuntimeOpenAI, EnvEnabled: boolPtr(false)}, nil, false, SourceEnv},
		{"env on", Config{EnvEnabled: boolPtr(true)}, nil, true, SourceEnv},
		{"admin off beats env on", Config{EnvEnabled: boolPtr(true)}, boolPtr(false), false, SourceAdmin},
		{"admin on beats nothing", Config{}, boolPtr(true), true, SourceAdmin},
	}
	for _, c := range cases {
		st := NewMemStore()
		if c.admin != nil {
			_ = st.SetAIEnabledChoice(ctx, c.admin, "a")
		}
		svc := newRegistryService(t, st, c.cfg, apiRuntime(RuntimeOpenAI))
		got, err := svc.Enablement(ctx)
		if err != nil || got.Enabled != c.want || got.Source != c.source {
			t.Errorf("%s = %+v, %v", c.name, got, err)
		}
	}
	// The env value shown beside an admin override includes the legacy rule.
	st := NewMemStore()
	_ = st.SetAIEnabledChoice(ctx, boolPtr(false), "a")
	got, _ := newRegistryService(t, st, Config{EnvRuntime: RuntimeOpenAI}, apiRuntime(RuntimeOpenAI)).Enablement(ctx)
	if got.EnvEnabled == nil || !*got.EnvEnabled {
		t.Fatalf("env beside admin = %+v", got)
	}
}

// Off refuses every run and consent before anything reaches a runtime, while
// the model settings and catalog stay manageable.
func TestDisabledRefusesStartAndConsent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	rt := catalogRuntime()
	svc := newRegistryService(t, f.st, Config{EnvRuntime: DefaultRuntimeKey, EnvEnabled: boolPtr(false)}, rt)

	if _, err := svc.Start(ctx, f.sc, f.wk); !errors.Is(err, ErrRuntimeDisabled) {
		t.Fatalf("start: %v", err)
	}
	if err := svc.GrantConsent(ctx, f.sc, testRuntimeKey, DisclosureVersion); !errors.Is(err, ErrRuntimeDisabled) {
		t.Fatalf("consent: %v", err)
	}
	if len(rt.Requests()) != 0 || len(f.st.Runs) != 0 {
		t.Fatalf("disabled run reached the runtime: %d requests, %d runs", len(rt.Requests()), len(f.st.Runs))
	}
	if models, err := svc.Models(ctx, ""); err != nil || len(models) == 0 {
		t.Fatalf("models while off = %v, %v", models, err)
	}
	if _, err := svc.SettingsFor(ctx, DefaultRuntimeKey); err != nil {
		t.Fatalf("settings while off: %v", err)
	}
	if _, _, err := svc.RuntimeViews(ctx); err != nil {
		t.Fatalf("views while off: %v", err)
	}
}

// Turning reports on needs a chosen runtime this server built; off and clear
// never do.
func TestSetEnabled(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	svc := newRegistryService(t, st, Config{}, nil, apiRuntime(RuntimeOpenAI))

	if _, err := svc.SetEnabled(ctx, boolPtr(true), "a"); !errors.Is(err, ErrNoRuntimeChosen) {
		t.Fatalf("on without a runtime: %v", err)
	}
	_ = st.SetAIRuntimeChoice(ctx, DefaultRuntimeKey, "a")
	if _, err := svc.SetEnabled(ctx, boolPtr(true), "a"); !errors.Is(err, ErrNoRuntimeChosen) {
		t.Fatalf("on with an unbuilt runtime: %v", err)
	}
	if st.Enabled != nil {
		t.Fatalf("refused switch stored: %+v", st.Enabled)
	}
	if got, err := svc.SetEnabled(ctx, boolPtr(false), "a"); err != nil || got.Enabled || got.Source != SourceAdmin {
		t.Fatalf("off = %+v, %v", got, err)
	}
	if _, err := svc.SelectRuntime(ctx, RuntimeOpenAI, "a"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.SetEnabled(ctx, boolPtr(true), "admin@example.com")
	if err != nil || !got.Enabled || got.Source != SourceAdmin || st.Enabled.UpdatedBy != "admin@example.com" {
		t.Fatalf("on = %+v, %v", got, err)
	}
	if got, err = svc.SetEnabled(ctx, nil, "a"); err != nil || got.Enabled || got.Source != SourceDefault {
		t.Fatalf("clear = %+v, %v", got, err)
	}
}

// Turning reports off stops the runs already going: they end canceled with
// runtime_disabled and call no tool after the switch has answered.
func TestDisableCancelsActiveRuns(t *testing.T) {
	ctx := context.Background()
	rt := readyRuntime([]airuntime.FakeStep{
		{CallTool: "query_segments", Args: []byte(`{}`)},
		{CallTool: "compare_week", Args: []byte(`{}`)},
	}, goodOutput)
	f := newFixture(t, rt)
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var calls []string
	f.svc.toolHook = func(name string) {
		mu.Lock()
		calls = append(calls, name)
		mu.Unlock()
		if name == "query_segments" {
			close(entered)
			<-release
		}
	}
	run, err := f.svc.Start(ctx, f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if _, err := f.svc.SetEnabled(ctx, boolPtr(false), "a"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	close(release)
	got := waitRun(t, f.st, run.ID, store.AIRunCanceled)
	if got.ErrorCode != "runtime_disabled" {
		t.Fatalf("run = %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("tools called after disable: %v", calls)
	}
}

// A run that passed the switch before an admin turned reports off must not
// start after the switch has answered.
func TestStartAcrossDisableFails(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, nil)
	gs := &gatedStore{MemStore: f.st, entered: make(chan struct{}), release: make(chan struct{})}
	codex := readyRuntime(nil, goodOutput)
	svc := NewService(gs, []airuntime.Runtime{codex}, Config{EnvRuntime: DefaultRuntimeKey})
	t.Cleanup(func() { svc.Shutdown(context.Background()) })

	errc := make(chan error, 1)
	go func() {
		_, err := svc.Start(ctx, f.sc, f.wk)
		errc <- err
	}()
	<-gs.entered
	if _, err := svc.SetEnabled(ctx, boolPtr(false), "a"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	close(gs.release)
	if err := <-errc; !errors.Is(err, ErrRuntimeChanging) {
		t.Fatalf("start across disable: %v", err)
	}
	if len(codex.Requests()) != 0 {
		t.Fatal("the runtime ran after reports were turned off")
	}
}

// Turning reports on refuses a chosen runtime that is built but not
// configured, the same case the admin screen blocks, and stores nothing.
func TestSetEnabledRefusesUnconfiguredRuntime(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	openai := apiRuntime(RuntimeOpenAI)
	openai.StatusValue = airuntime.Status{Reason: "OpenAI API key is not set"}
	svc := newRegistryService(t, st, Config{EnvRuntime: RuntimeOpenAI}, openai)

	if _, err := svc.SetEnabled(ctx, boolPtr(true), "a"); !errors.Is(err, ErrRuntimeNotConfigured) {
		t.Fatalf("on with an unconfigured runtime: %v", err)
	}
	if st.Enabled != nil {
		t.Fatalf("refused switch stored: %+v", st.Enabled)
	}
}
