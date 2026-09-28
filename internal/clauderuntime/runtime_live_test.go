//go:build claudelive

package clauderuntime

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

// go test -tags claudelive ./internal/clauderuntime with
// CCTRACE_ANTHROPIC_LIVE_KEY set calls the real API and spends tokens.
func liveRuntime(t *testing.T) *Runtime {
	key := os.Getenv("CCTRACE_ANTHROPIC_LIVE_KEY")
	if key == "" {
		t.Skip("CCTRACE_ANTHROPIC_LIVE_KEY not set")
	}
	return New(Config{
		APIKey:       func(context.Context) (string, error) { return key, nil },
		DefaultModel: os.Getenv("CCTRACE_ANTHROPIC_LIVE_MODEL"),
	})
}

func TestLiveModels(t *testing.T) {
	models, err := liveRuntime(t).Models(context.Background())
	if err != nil || len(models) == 0 {
		t.Fatalf("models=%+v err=%v", models, err)
	}
	t.Logf("models: %+v", models)
}

func TestLiveRunWithToolAndSchema(t *testing.T) {
	r := liveRuntime(t)
	// Haiku 4.5 answers 400 to output_config.effort, so the effort a live run
	// sends comes from the catalog rather than from a constant: asking for one
	// the model has no support for tests the harness, not the runtime.
	model := os.Getenv("CCTRACE_ANTHROPIC_LIVE_MODEL")
	if model == "" {
		model = defaultModel
	}
	effort := ""
	if c, ok := lookupModel(model); ok && len(c.efforts) > 0 {
		effort = c.efforts[0]
	}
	req := airuntime.RunRequest{
		Instructions:    "Answer using the tool result only.",
		Prompt:          "Call get_count once, then report the count in the summary.",
		ReasoningEffort: effort,
		Tools: []airuntime.Tool{{
			Name: "get_count", Description: "Returns this week's session count.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
				return airuntime.ToolOutput{Text: `{"count":42}`, Rows: 1}, nil
			},
		}},
		OutputSchema:   json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","maxLength":200}}}`),
		ToolTimeout:    10 * time.Second,
		WallClock:      3 * time.Minute,
		MaxToolCalls:   3,
		MaxTotalTokens: 200000,
	}
	res, err := r.Run(context.Background(), req, func(ev airuntime.Event) { t.Logf("event %+v", ev) })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result %+v", res)
}
