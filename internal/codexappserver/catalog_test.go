package codexappserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

func loggedInHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func spawnCount(t *testing.T) func() int {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "runs")
	t.Setenv(fakeRunCounterEnv, counter)
	return func() int {
		b, _ := os.ReadFile(counter)
		return strings.Count(string(b), "run")
	}
}

// model/list pages are followed to the end and hidden models are dropped.
func TestModelsListsAllPagesWithoutHidden(t *testing.T) {
	fakeServer(t, fakeModels)
	runs := spawnCount(t)
	rt := NewRuntime(RuntimeConfig{Home: loggedInHome(t)}).(airuntime.ModelCatalog)

	models, err := rt.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0].ID != "gpt-5.6-terra" || models[1].ID != "gpt-5.4-mini" {
		t.Fatalf("models = %+v", models)
	}
	m := models[0]
	if m.DisplayName != "GPT-5.6 Terra" || !m.IsDefault || m.DefaultReasoningEffort != "medium" ||
		len(m.SupportedReasoningEfforts) != 2 || !m.SupportsEffort("high") || m.SupportsEffort("xhigh") {
		t.Fatalf("model = %+v", m)
	}

	// A second call inside the cache window starts no process.
	if _, err := rt.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := runs(); n != 1 {
		t.Fatalf("processes started = %d, want 1", n)
	}
}

func TestModelsCacheExpires(t *testing.T) {
	fakeServer(t, fakeModels)
	runs := spawnCount(t)
	now := time.Now()
	prev := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = prev })
	rt := NewRuntime(RuntimeConfig{Home: loggedInHome(t)}).(airuntime.ModelCatalog)

	if _, err := rt.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(modelCacheTTL)
	if _, err := rt.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := runs(); n != 2 {
		t.Fatalf("processes started = %d, want 2", n)
	}
}

// The three failures the admin screen tells apart.
func TestModelsDistinguishesFailures(t *testing.T) {
	if _, err := NewRuntime(RuntimeConfig{}).(airuntime.ModelCatalog).Models(context.Background()); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("no home: %v", err)
	}

	fakeServer(t, fakeModels)
	if _, err := NewRuntime(RuntimeConfig{Home: t.TempDir()}).(airuntime.ModelCatalog).Models(context.Background()); !errors.Is(err, airuntime.ErrNotLoggedIn) {
		t.Fatalf("no login: %v", err)
	}

	fakeServer(t, fakeModelsError)
	_, err := NewRuntime(RuntimeConfig{Home: loggedInHome(t)}).(airuntime.ModelCatalog).Models(context.Background())
	if !errors.Is(err, airuntime.ErrUnavailable) || !strings.Contains(err.Error(), "catalog down") {
		t.Fatalf("server error: %v", err)
	}
}
