package chatruntime

import (
	"context"
	"errors"
	"testing"

	"cctrace/internal/airuntime"
)

// The LiteLLM address is the one provider setting an admin types in, and it is
// saved to ai_runtime_settings. The runtime read it once at boot from the
// environment, so a saved address did nothing until the daemon restarted.
// Resolving it per request, the way the API key already is, makes the saved
// value take effect on the next run.
func TestBaseURLIsResolvedPerRequest(t *testing.T) {
	first := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, completion("test-model", "from the first address")
	})
	second := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, completion("test-model", "from the second address")
	})

	addr := first.srv.URL
	rt := New(Config{
		APIKey:       func(context.Context) (string, error) { return "k", nil },
		BaseURL:      func(context.Context) (string, error) { return addr, nil },
		DefaultModel: "test-model",
		ProviderName: "litellm",
	})

	res, err := rt.Run(context.Background(), airuntime.RunRequest{Prompt: "one"}, func(airuntime.Event) {})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if res.FinalText != "from the first address" {
		t.Errorf("first run went elsewhere: %q", res.FinalText)
	}

	// The admin saves a new address; no restart.
	addr = second.srv.URL
	res, err = rt.Run(context.Background(), airuntime.RunRequest{Prompt: "two"}, func(airuntime.Event) {})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.FinalText != "from the second address" {
		t.Errorf("the saved address did not take effect: %q", res.FinalText)
	}
	if first.count("/v1/chat/completions") != 1 || second.count("/v1/chat/completions") != 1 {
		t.Errorf("requests split %d/%d, want 1/1", first.count("/v1/chat/completions"), second.count("/v1/chat/completions"))
	}
}

// An address typed with the documented "/v1" suffix still has to work, and the
// normalisation now happens per request rather than once in New.
func TestResolvedBaseURLIsNormalised(t *testing.T) {
	f := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, completion("test-model", "done")
	})
	rt := New(Config{
		APIKey:       func(context.Context) (string, error) { return "k", nil },
		BaseURL:      func(context.Context) (string, error) { return f.srv.URL + "/v1/", nil },
		DefaultModel: "test-model",
		ProviderName: "litellm",
	})

	if _, err := rt.Run(context.Background(), airuntime.RunRequest{Prompt: "x"}, func(airuntime.Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.count("/v1/chat/completions"); got != 1 {
		t.Errorf("calls to /v1/chat/completions = %d, want 1", got)
	}
	if got := f.count("/v1/v1/chat/completions"); got != 0 {
		t.Errorf("the path was doubled %d times", got)
	}
}

// A runtime whose address cannot be resolved is not configured, the same as one
// without a key: the admin screen has to say so rather than fail at the request.
func TestUnresolvableBaseURLIsNotConfigured(t *testing.T) {
	rt := New(Config{
		APIKey:       func(context.Context) (string, error) { return "k", nil },
		BaseURL:      func(context.Context) (string, error) { return "", nil },
		DefaultModel: "test-model",
		ProviderName: "litellm",
	})

	_, err := rt.Run(context.Background(), airuntime.RunRequest{Prompt: "x"}, func(airuntime.Event) {})
	if err == nil {
		t.Fatal("a runtime with no address ran")
	}
	var re *airuntime.RunError
	if !errors.As(err, &re) || re.Code != "not_configured" {
		t.Errorf("err = %v, want not_configured", err)
	}
}

// A self-hosted runtime with a key but no address is not ready, and the admin
// screen has to say which half is missing.
func TestStatusReportsAMissingAddress(t *testing.T) {
	rt := New(Config{
		APIKey:       func(context.Context) (string, error) { return "k", nil },
		BaseURL:      func(context.Context) (string, error) { return "", nil },
		ProviderName: "litellm",
	})
	st := rt.Status(context.Background())
	if st.Configured || st.Reason == "" {
		t.Errorf("status = %+v, want unconfigured with a reason", st)
	}

	withAddr := New(Config{
		APIKey:       func(context.Context) (string, error) { return "k", nil },
		BaseURL:      func(context.Context) (string, error) { return "https://litellm.example.test", nil },
		ProviderName: "litellm",
	})
	if st := withAddr.Status(context.Background()); !st.Configured {
		t.Errorf("status with an address = %+v, want configured", st)
	}
}
