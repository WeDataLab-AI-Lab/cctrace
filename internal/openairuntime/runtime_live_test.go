//go:build openailive

package openairuntime

import (
	"context"
	"encoding/json"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

// TestLiveRunOneToolCall spends real tokens, so it is manual only:
//
//	CCTRACE_OPENAI_LIVE_KEY=sk-... [CCTRACE_OPENAI_LIVE_MODEL=<model>] \
//	  go test -tags openailive ./internal/openairuntime -run Live -v
func TestLiveRunOneToolCall(t *testing.T) {
	key := os.Getenv("CCTRACE_OPENAI_LIVE_KEY")
	if key == "" {
		t.Skip("CCTRACE_OPENAI_LIVE_KEY not set")
	}
	rt := New(Config{
		APIKey:       func(context.Context) (string, error) { return key, nil },
		DefaultModel: os.Getenv("CCTRACE_OPENAI_LIVE_MODEL"),
	})
	ctx := context.Background()
	models, err := rt.Models(ctx)
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	t.Logf("models: %+v", models)

	var calls atomic.Int32
	tool := airuntime.Tool{
		Name:        "week_total",
		Description: "Returns the number of sessions this week.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
			calls.Add(1)
			return airuntime.ToolOutput{Text: `{"sessions":42}`, Rows: 1}, nil
		},
	}
	// The report's own schema shape, maxLength and maxItems included, so a
	// strict rejection shows up here.
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["summary","items"],"properties":{
"summary":{"type":"string","maxLength":200},
"items":{"type":"array","maxItems":1,"items":{"type":"object","additionalProperties":false,"required":["title"],"properties":{"title":{"type":"string","maxLength":40}}}}}}`)
	res, err := rt.Run(ctx, airuntime.RunRequest{
		ReasoningEffort: "low",
		Instructions:    "Call week_total once, then answer in the schema.",
		Prompt:          "How many sessions were there this week?",
		Tools:           []airuntime.Tool{tool},
		OutputSchema:    schema,
		ToolTimeout:     10 * time.Second,
		WallClock:       3 * time.Minute,
		MaxToolCalls:    3,
		MaxTotalTokens:  50_000,
	}, func(e airuntime.Event) { t.Logf("event %s seq=%d tool=%s status=%s", e.Kind, e.Seq, e.Tool, e.Status) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("model=%s usage=%+v final=%s", res.Model, res.Usage, res.FinalText)
	if calls.Load() == 0 || !res.Usage.Reported {
		t.Fatalf("calls=%d usage=%+v", calls.Load(), res.Usage)
	}
}
