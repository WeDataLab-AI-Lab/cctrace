package chatruntime

import (
	"context"
	"testing"

	"cctrace/internal/airuntime"
)

func TestSessionRecordOutputTokenCapReachesLocalProvider(t *testing.T) {
	fake := newFake(t, func(path string, _ int, _ map[string]any) (int, string) {
		if path != "/v1/chat/completions" {
			return 404, `{"detail":"Not Found"}`
		}
		return 200, completion("synthetic-model", `{"headline":"synthetic"}`)
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "litellm", nil)
	if _, err := rt.Run(context.Background(), airuntime.RunRequest{Prompt: "synthetic", MaxOutputTokens: 12000}, nil); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.bodies) != 1 {
		t.Fatalf("request count = %d, want 1", len(fake.bodies))
	}
	if got := fake.bodies[0]["max_tokens"]; got != float64(12000) {
		t.Fatalf("max_tokens = %#v, want 12000", got)
	}
}
