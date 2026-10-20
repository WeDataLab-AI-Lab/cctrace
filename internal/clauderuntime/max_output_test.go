package clauderuntime

import (
	"testing"

	"cctrace/internal/airuntime"
)

func TestSessionRecordOutputTokenCapDoesNotChangeWeeklyDefault(t *testing.T) {
	rt := New(Config{DefaultModel: "claude-sonnet-5", MaxTokens: 16_000})
	body, err := rt.requestBody(airuntime.RunRequest{Model: "claude-sonnet-5", MaxOutputTokens: 12_000})
	if err != nil {
		t.Fatal(err)
	}
	if got := body["max_tokens"]; got != int64(12_000) {
		t.Fatalf("record max_tokens = %#v, want 12000", got)
	}
	body, err = rt.requestBody(airuntime.RunRequest{Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if got := body["max_tokens"]; got != int64(16_000) {
		t.Fatalf("default max_tokens = %#v, want 16000", got)
	}
}
