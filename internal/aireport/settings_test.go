package aireport

import (
	"context"
	"errors"
	"testing"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

func TestResolveSettingsPrecedence(t *testing.T) {
	cases := []struct {
		name                string
		admin               *store.AISettings
		envModel, envEffort string
		model, modelSrc     string
		effort, effortSrc   string
	}{
		{"nothing set", nil, "", "", DefaultModel, SourceDefault, "", SourceDefault},
		{"env only", nil, "gpt-env", "low", "gpt-env", SourceEnv, "low", SourceEnv},
		{"admin over env", &store.AISettings{Model: "gpt-admin", ReasoningEffort: "high"}, "gpt-env", "low", "gpt-admin", SourceAdmin, "high", SourceAdmin},
		// Each item is decided on its own.
		{"admin model, env effort", &store.AISettings{Model: "gpt-admin"}, "gpt-env", "low", "gpt-admin", SourceAdmin, "low", SourceEnv},
		{"admin effort, default model", &store.AISettings{ReasoningEffort: "high"}, "", "", DefaultModel, SourceDefault, "high", SourceAdmin},
		{"blank env is unset", nil, "  ", " ", DefaultModel, SourceDefault, "", SourceDefault},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveSettings(c.admin, RuntimeEnv{Model: c.envModel, ReasoningEffort: c.envEffort}, DefaultModel)
			if got.Model != c.model || got.ModelSource != c.modelSrc || got.ReasoningEffort != c.effort || got.ReasoningEffortSource != c.effortSrc {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// The effective settings are read when a run starts, so a change applies to
// the next run without a restart, and the run records the model it asked for.
func TestStartUsesCurrentSettings(t *testing.T) {
	rt := readyRuntime(nil, goodOutput)
	f := newFixture(t, rt)
	f.svc.cfg.Env = map[string]RuntimeEnv{DefaultRuntimeKey: {Model: "gpt-env", ReasoningEffort: "low"}}

	run, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	if run.Model != "gpt-env" {
		t.Fatalf("run model = %q", run.Model)
	}
	waitRun(t, f.st, run.ID, store.AIRunCompleted)

	if err := f.st.SetAISettings(context.Background(), "codex-app-server", "gpt-admin", "", "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	run, err = f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitRun(t, f.st, run.ID, store.AIRunCompleted); got.Model != "gpt-admin" {
		t.Fatalf("second run model = %q", got.Model)
	}
	reqs := rt.Requests()
	if len(reqs) != 2 || reqs[0].Model != "gpt-env" || reqs[0].ReasoningEffort != "low" ||
		reqs[1].Model != "gpt-admin" || reqs[1].ReasoningEffort != "low" {
		t.Fatalf("requests = %+v", reqs)
	}
}

func catalogRuntime() *airuntime.FakeRuntime {
	rt := readyRuntime(nil, goodOutput)
	efforts := []airuntime.ReasoningEffortOption{{ReasoningEffort: "medium"}, {ReasoningEffort: "high"}}
	rt.ModelsValue = []airuntime.Model{
		{ID: DefaultModel, DisplayName: "Terra", DefaultReasoningEffort: "medium", SupportedReasoningEfforts: efforts},
		{ID: "gpt-small", DisplayName: "Small", DefaultReasoningEffort: "medium", SupportedReasoningEfforts: efforts[:1]},
	}
	return rt
}

func TestSetSettingsValidatesAgainstCatalog(t *testing.T) {
	rt := catalogRuntime()
	f := newFixture(t, rt)
	ctx := context.Background()

	if _, err := f.svc.SetSettings(ctx, "", "gpt-nope", "", "", "a@example.com"); !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("unknown model: %v", err)
	}
	if _, err := f.svc.SetSettings(ctx, "", "gpt-small", "high", "", "a@example.com"); !errors.Is(err, ErrInvalidReasoningEffort) {
		t.Fatalf("unsupported effort: %v", err)
	}
	// An effort alone is checked against the model it will run with.
	if _, err := f.svc.SetSettings(ctx, "", "", "ultra", "", "a@example.com"); !errors.Is(err, ErrInvalidReasoningEffort) {
		t.Fatalf("effort for default model: %v", err)
	}
	// An empty effort inherits the environment's, which must fit the new model
	// too, or the next run asks the model for an effort it rejects.
	f.svc.cfg.Env = map[string]RuntimeEnv{DefaultRuntimeKey: {ReasoningEffort: "high"}}
	if _, err := f.svc.SetSettings(ctx, "", "gpt-small", "", "", "a@example.com"); !errors.Is(err, ErrInvalidReasoningEffort) {
		t.Fatalf("inherited env effort: %v", err)
	}
	f.svc.cfg.Env = nil
	if len(f.st.Settings) != 0 {
		t.Fatalf("rejected writes stored %+v", f.st.Settings)
	}

	got, err := f.svc.SetSettings(ctx, "", " gpt-small ", "medium", "", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-small" || got.ModelSource != SourceAdmin || got.ReasoningEffort != "medium" || f.st.Settings["codex-app-server"].UpdatedBy != "a@example.com" {
		t.Fatalf("settings = %+v stored = %+v", got, f.st.Settings)
	}

	// Clearing needs no catalog: it must work while the catalog is down.
	rt.ModelsErr = airuntime.ErrUnavailable
	got, err = f.svc.SetSettings(ctx, "", "", "", "", "b@example.com")
	if err != nil || got.ModelSource != SourceDefault || got.ReasoningEffortSource != SourceDefault {
		t.Fatalf("clear = %+v, %v", got, err)
	}
	if _, err := f.svc.SetSettings(ctx, "", "gpt-small", "", "", "b@example.com"); !errors.Is(err, airuntime.ErrUnavailable) {
		t.Fatalf("catalog down: %v", err)
	}
}

func TestModelsNeedsCatalogRuntime(t *testing.T) {
	svc := NewService(NewMemStore(), []airuntime.Runtime{nil}, Config{})
	if _, err := svc.Models(context.Background(), ""); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("nil runtime: %v", err)
	}
	f := newFixture(t, catalogRuntime())
	models, err := f.svc.Models(context.Background(), "")
	if err != nil || len(models) != 2 {
		t.Fatalf("models = %v, %v", models, err)
	}
}
