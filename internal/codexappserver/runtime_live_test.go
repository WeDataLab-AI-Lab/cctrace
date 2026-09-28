//go:build codexlive

package codexappserver

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

// TestLiveRunOneTurn drives the real codex against a dedicated home with one
// fixture tool. It spends one model turn, so it is manual only:
//
//	mkdir -p /tmp/cctrace-live-home && ln -s ~/.codex/auth.json /tmp/cctrace-live-home/auth.json
//	CCTRACE_CODEX_LIVE_HOME=/tmp/cctrace-live-home CCTRACE_CODEX_LIVE_MODEL=<model> \
//	  go test -tags codexlive ./internal/codexappserver -run Live -v
//
// Never point it at ~/.codex: PrepareHome replaces config.toml.
func TestLiveRunOneTurn(t *testing.T) {
	home := os.Getenv("CCTRACE_CODEX_LIVE_HOME")
	if home == "" {
		t.Skip("CCTRACE_CODEX_LIVE_HOME not set")
	}
	cfg := RuntimeConfig{
		Home:            home,
		Model:           os.Getenv("CCTRACE_CODEX_LIVE_MODEL"),
		ReasoningEffort: "low",
		APIKey:          os.Getenv("CODEX_API_KEY"),
		Disable:         []string{"apps"},
	}
	if err := PrepareHome(cfg); err != nil {
		t.Fatalf("PrepareHome: %v", err)
	}

	calls := 0
	tool := airuntime.Tool{
		Name:        "query_segments",
		Description: "Return the user's work segments for an ISO week.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"week":{"type":"string"}},"required":["week"],"additionalProperties":false}`),
		Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
			calls++
			return airuntime.ToolOutput{
				Text: `{"segments":[{"segment_id":"seg-001","project":"fixture-a","minutes":95},{"segment_id":"seg-002","project":"fixture-b","minutes":210}]}`,
				Rows: 2,
			}, nil
		},
	}
	req := airuntime.RunRequest{
		Instructions:   "You summarize fixture work segments. Use only the query_segments tool.",
		Prompt:         "query_segments 도구로 week=2026-W37 을 조회하고 가장 긴 구간 1개를 골라 한 줄로 요약해.",
		Tools:          []airuntime.Tool{tool},
		OutputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary","segment_id"],"properties":{"summary":{"type":"string"},"segment_id":{"type":"string"}}}`),
		ToolTimeout:    10 * time.Second,
		WallClock:      90 * time.Second,
		MaxToolCalls:   3,
		MaxTotalTokens: 80_000,
	}

	rt := NewRuntime(cfg)
	t.Logf("info: %+v", rt.Info())
	res, err := rt.Run(context.Background(), req, func(e airuntime.Event) {
		if e.Kind != airuntime.EventTextDelta {
			t.Logf("event: %+v", e)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("final: %s usage: %+v duration: %s", res.FinalText, res.Usage, res.Duration)
	if calls == 0 {
		t.Error("the tool was never called")
	}
	var out struct {
		Summary   string `json:"summary"`
		SegmentID string `json:"segment_id"`
	}
	if err := json.Unmarshal([]byte(res.FinalText), &out); err != nil || out.SegmentID == "" {
		t.Errorf("final answer %q does not follow the schema: %v", res.FinalText, err)
	}
	if !res.Usage.Reported {
		t.Error("usage not reported")
	}
}
