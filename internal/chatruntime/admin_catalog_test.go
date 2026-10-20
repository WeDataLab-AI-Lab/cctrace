package chatruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/airuntime"
)

func TestAdminModelCatalogUsesConfiguredProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic-model-key" {
			t.Errorf("wrong catalog request")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"synthetic-model"}]}`))
	}))
	defer server.Close()
	runtime := New(Config{APIKey: func(context.Context) (string, error) { return "synthetic-model-key", nil },
		BaseURL: func(context.Context) (string, error) { return server.URL, nil }, ProviderName: "litellm"})
	catalog, ok := any(runtime).(airuntime.ModelCatalog)
	if !ok {
		t.Fatal("runtime does not implement the administrator model catalog")
	}
	models, err := catalog.Models(t.Context())
	if err != nil || len(models) != 1 || models[0].ID != "synthetic-model" || models[0].DisplayName != "synthetic-model" {
		t.Fatalf("catalog: %#v, %v", models, err)
	}
	if len(models[0].SupportedReasoningEfforts) != 0 {
		t.Fatal("provider did not advertise reasoning efforts")
	}
}

func TestAdminModelCatalogReportsMissingConfiguration(t *testing.T) {
	runtime := New(Config{})
	catalog, ok := any(runtime).(airuntime.ModelCatalog)
	if !ok {
		t.Fatal("runtime does not implement the administrator model catalog")
	}
	_, err := catalog.Models(t.Context())
	if !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("missing credentials: %v", err)
	}
}
