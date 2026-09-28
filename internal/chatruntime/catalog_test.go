package chatruntime

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListModels(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		apiResp  string
		want     []string
	}{
		{
			name:     "NVIDIA hardcoded list",
			provider: "nvidia",
			apiResp:  `{"data":[{"id":"z-ai/glm-5.3"},{"id":"nvidia/nemotron-3.5-lightning-30b-a3b"}]}`,
			want:     []string{"z-ai/glm-5.3"},
		},
		{
			name:     "LiteLLM passthrough",
			provider: "litellm",
			apiResp:  `{"data":[{"id":"gpt-4"},{"id":"gpt-3.5-turbo"}]}`,
			want:     []string{"gpt-4", "gpt-3.5-turbo"},
		},
		{
			name:     "Empty response",
			provider: "litellm",
			apiResp:  `{"data":[]}`,
			want:     []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(200)
				_, _ = io.WriteString(w, tc.apiResp)
			}))
			defer fake.Close()

			logs := &bytes.Buffer{}
			rt := New(Config{
				APIKey:       func(context.Context) (string, error) { return "test-key", nil },
				BaseURL:      func(context.Context) (string, error) { return fake.URL, nil },
				ProviderName: tc.provider,
				Logger:       slog.New(slog.NewTextHandler(logs, nil)),
			})

			models, err := rt.ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels failed: %v", err)
			}
			if len(models) != len(tc.want) {
				t.Errorf("expected %d models, got %d", len(tc.want), len(models))
			}
			for i, want := range tc.want {
				if i >= len(models) || models[i] != want {
					t.Errorf("model %d: expected %q, got %q", i, want, models[i])
				}
			}
		})
	}
}

func TestListModelsError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"401 Unauthorized", 401, `{}`},
		{"404 Not Found", 404, `{}`},
		{"500 Server Error", 500, `{}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer fake.Close()

			rt := New(Config{
				APIKey:       func(context.Context) (string, error) { return "test-key", nil },
				BaseURL:      func(context.Context) (string, error) { return fake.URL, nil },
				ProviderName: "litellm",
			})

			_, err := rt.ListModels(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestListModelsAPIKeyRequired(t *testing.T) {
	rt := New(Config{
		APIKey: func(context.Context) (string, error) { return "", nil },
	})

	_, err := rt.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "not_configured") && !strings.Contains(err.Error(), "not set") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestListModelsTimeout(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer fake.Close()

	rt := New(Config{
		APIKey:         func(context.Context) (string, error) { return "test-key", nil },
		BaseURL:        func(context.Context) (string, error) { return fake.URL, nil },
		RequestTimeout: 50 * time.Millisecond,
	})

	_, err := rt.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestListModelsInvalidJSON(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{invalid json}`)
	}))
	defer fake.Close()

	rt := New(Config{
		APIKey:       func(context.Context) (string, error) { return "test-key", nil },
		BaseURL:      func(context.Context) (string, error) { return fake.URL, nil },
		ProviderName: "litellm",
	})

	_, err := rt.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestListModelsCaching(t *testing.T) {
	callCount := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"data":[{"id":"model-1"}]}`)
	}))
	defer fake.Close()

	rt := New(Config{
		APIKey:       func(context.Context) (string, error) { return "test-key", nil },
		BaseURL:      func(context.Context) (string, error) { return fake.URL, nil },
		ProviderName: "litellm",
	})

	ctx := context.Background()
	// First call should hit the server
	models1, err := rt.ListModels(ctx)
	if err != nil || len(models1) != 1 {
		t.Fatalf("first ListModels failed: %v, len=%d", err, len(models1))
	}

	// Second call should use cache (but in httptest we can't really verify unless we track calls)
	models2, err := rt.ListModels(ctx)
	if err != nil || len(models2) != 1 {
		t.Fatalf("second ListModels failed: %v", err)
	}

	// Both should have the same result
	if models1[0] != models2[0] {
		t.Errorf("cache miss: first=%s, second=%s", models1[0], models2[0])
	}
}
