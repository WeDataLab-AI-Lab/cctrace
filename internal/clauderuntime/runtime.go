package clauderuntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cctrace/internal/airuntime"
)

var ephemeral = map[string]any{"type": "ephemeral"}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type messageResponse struct {
	Model      string            `json:"model"`
	StopReason string            `json:"stop_reason"`
	Content    []json.RawMessage `json:"content"`
	Usage      struct {
		InputTokens              int64 `json:"input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
	} `json:"usage"`
}

func runErr(code, message string, sentinel error) *airuntime.RunError {
	return airuntime.NewRunError(code, message, sentinel)
}

// Run drives POST /v1/messages until the model ends its turn. Tool calls run
// one at a time in response order; the next request carries the assistant
// content unchanged (thinking blocks included) and every tool_result in one
// user message.
func (r *Runtime) Run(ctx context.Context, req airuntime.RunRequest, sink func(airuntime.Event)) (*airuntime.Result, error) {
	start := time.Now()
	if sink == nil {
		sink = func(airuntime.Event) {}
	}
	key, err := r.apiKey(ctx)
	if errors.Is(err, errNoKey) {
		return nil, runErr("not_configured", "Anthropic API key is not set", airuntime.ErrNotConfigured)
	}
	if err != nil {
		return nil, runErr("runtime_unavailable", "could not read the Anthropic API key", airuntime.ErrUnavailable)
	}
	runCtx, cancel := airuntime.Deadline(ctx, req.WallClock)
	defer cancel()

	body, err := r.requestBody(req)
	if err != nil {
		return nil, err
	}
	tools := map[string]airuntime.Tool{}
	for _, tool := range req.Tools {
		tools[tool.Name] = tool
	}
	messages := []any{map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "text", "text": req.Prompt}},
	}}
	model := body["model"].(string)
	budget := airuntime.Budget{Max: req.MaxTotalTokens}
	limit := airuntime.ToolLimit{Max: req.MaxToolCalls}
	// Only the withholding half applies here. Forcing a tool call is the other
	// half of the same defence, but forced tool_choice returns 400 on Claude
	// Fable 5.1, so this runtime asks for tools and waits instead.
	timing := airuntime.SchemaTiming{HasTools: len(req.Tools) > 0, HasSchema: len(req.OutputSchema) > 0}
	toolsRan := false
	outputConfig, _ := body["output_config"].(map[string]any)
	answerFormat := outputConfig["format"]
	seq := 0

	for {
		body["messages"] = messages
		// The schema goes on only once a tool has run: asked for both at once,
		// a model can answer "I will look these up and then write", call
		// nothing, and finish empty because that sentence fits the schema.
		if outputConfig != nil {
			if timing.AttachSchema(toolsRan) {
				outputConfig["format"] = answerFormat
			} else {
				delete(outputConfig, "format")
			}
			if len(outputConfig) == 0 {
				delete(body, "output_config")
			} else {
				body["output_config"] = outputConfig
			}
		}
		var resp messageResponse
		if err := r.do(runCtx, key, http.MethodPost, "/v1/messages", body, &resp); err != nil {
			if cerr := ctxError(ctx, runCtx, req); cerr != nil {
				return nil, cerr
			}
			return nil, runError(err)
		}
		if resp.Model != "" {
			model = resp.Model
		}
		// Anthropic reports uncached, cache-read and cache-write input apart;
		// Usage.InputTokens is all input with CachedInputTokens a subset of it.
		u := resp.Usage
		if err := budget.Add(airuntime.Usage{
			Reported:          true,
			InputTokens:       u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
			CachedInputTokens: u.CacheReadInputTokens,
			OutputTokens:      u.OutputTokens,
		}, sink); err != nil {
			return nil, err
		}

		switch resp.StopReason {
		case "tool_use":
			var results []any
			refused, ran := false, false
			for _, raw := range resp.Content {
				var b contentBlock
				if json.Unmarshal(raw, &b) != nil || b.Type != "tool_use" {
					continue
				}
				// Stop between calls too, so a canceled run starts no more handlers.
				if cerr := ctxError(ctx, runCtx, req); cerr != nil {
					return nil, cerr
				}
				seq++
				inv, limited, invoked := r.callTool(runCtx, req, tools, b, seq, sink)
				refused, ran = refused || limited, ran || invoked
				results = append(results, map[string]any{
					"type": "tool_result", "tool_use_id": b.ID, "is_error": !inv.Success,
					"content": airuntime.TruncateText(inv.Output.Text, airuntime.MaxToolOutputBytes, airuntime.TruncatedMark),
				})
			}
			if len(results) == 0 {
				return nil, runErr("protocol_error", "tool_use stop without tool_use blocks", airuntime.ErrProtocol)
			}
			// One response is one step past the limit however many calls it
			// holds, so a batch that overshoots still gets its refusals answered.
			if ran {
				toolsRan = true
			}
			if err := limit.AfterBatch(refused, ran); err != nil {
				return nil, err
			}
			messages = append(messages,
				map[string]any{"role": "assistant", "content": resp.Content},
				map[string]any{"role": "user", "content": results})
		case "end_turn", "stop_sequence":
			var text strings.Builder
			for _, raw := range resp.Content {
				var b contentBlock
				if json.Unmarshal(raw, &b) == nil && b.Type == "text" {
					text.WriteString(b.Text)
				}
			}
			final := text.String()
			sink(airuntime.Event{Kind: airuntime.EventTextDelta, Text: final})
			if len(req.OutputSchema) > 0 {
				if err := validateJSON(req.OutputSchema, final); err != nil {
					// The answer itself stays out of the message; the path and
					// rule are enough to see what broke.
					return nil, runErr("schema_violation", "final answer does not match the output schema: "+schemaReason(err), airuntime.ErrProtocol)
				}
			}
			return &airuntime.Result{FinalText: final, Model: model, Usage: budget.Usage(), Duration: time.Since(start)}, nil
		case "max_tokens":
			return nil, airuntime.StopError(airuntime.StopOutputLimit, fmt.Sprintf("response hit max_tokens %d", r.cfg.MaxTokens))
		case "model_context_window_exceeded":
			return nil, airuntime.StopError(airuntime.StopContextWindow, "conversation outgrew the model's context window")
		case "refusal":
			return nil, airuntime.StopError(airuntime.StopRefused, "model declined the request")
		default:
			return nil, airuntime.StopError(airuntime.StopUnknown, "unexpected stop_reason "+resp.StopReason)
		}
	}
}

// ctxError tells a caller's cancel from the turn's own wall clock; nil while
// both contexts are live.
func ctxError(ctx, runCtx context.Context, req airuntime.RunRequest) *airuntime.RunError {
	return airuntime.CtxError(ctx, runCtx, req.WallClock)
}

// schemaReason drops a JSON syntax error's detail, which can quote the answer.
func schemaReason(err error) string {
	if msg := err.Error(); strings.HasPrefix(msg, "$") {
		return msg
	}
	return "not a single JSON value"
}

func (r *Runtime) requestBody(req airuntime.RunRequest) (map[string]any, error) {
	model := req.Model
	if model == "" {
		model = r.cfg.DefaultModel
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": r.cfg.MaxTokens,
		// Automatic caching puts a breakpoint on the last block, so each tool
		// loop request reads the conversation the previous one wrote.
		"cache_control": ephemeral,
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, tool := range req.Tools {
			schema := tool.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			tools[i] = map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": schema}
		}
		// Tools render first, so a breakpoint on the last one caches them all
		// even when there are no instructions.
		tools[len(tools)-1]["cache_control"] = ephemeral
		body["tools"] = tools
	}
	if req.Instructions != "" {
		body["system"] = []any{map[string]any{"type": "text", "text": req.Instructions, "cache_control": ephemeral}}
	}
	if c, ok := lookupModel(model); ok && c.adaptive {
		// Opus 5, Sonnet 5 and Fable 5.1 think adaptively by default, Opus 4.8
		// only when asked; saying so explicitly is accepted by all of them.
		body["thinking"] = map[string]any{"type": "adaptive"}
	}
	outputConfig := map[string]any{}
	if req.ReasoningEffort != "" {
		if c, ok := lookupModel(model); ok && len(c.efforts) > 0 {
			outputConfig["effort"] = req.ReasoningEffort
		}
	}
	// The final answer is constrained with structured outputs
	// (output_config.format) rather than a forced submit_report tool: it is
	// GA on every curated model, works alongside client tools in the same
	// request, and forced tool_choice returns 400 on Claude Fable 5.1.
	// Constraints it cannot compile are stripped here and checked on the answer.
	if len(req.OutputSchema) > 0 {
		schema, err := sendableSchema(req.OutputSchema)
		if err != nil {
			return nil, runErr("invalid_request", "output schema is not valid JSON", airuntime.ErrProtocol)
		}
		outputConfig["format"] = map[string]any{"type": "json_schema", "schema": schema}
	}
	if len(outputConfig) > 0 {
		body["output_config"] = outputConfig
	}
	return body, nil
}

// callTool runs one tool_use block. limited reports a call refused for
// MaxToolCalls; invoked a call that reached its handler.
func (r *Runtime) callTool(ctx context.Context, req airuntime.RunRequest, tools map[string]airuntime.Tool, b contentBlock,
	seq int, sink func(airuntime.Event)) (inv airuntime.Invocation, limited, invoked bool) {
	input := b.Input
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage(`{}`)
	}
	var args map[string]any
	_ = json.Unmarshal(input, &args)
	sink(airuntime.Event{Kind: airuntime.EventToolCall, Seq: seq, Tool: b.Name, Args: args})

	tool, known := tools[b.Name]
	switch {
	case req.MaxToolCalls > 0 && seq > req.MaxToolCalls:
		inv = airuntime.Invocation{
			Output: airuntime.ToolOutput{Text: airuntime.ToolLimitRefusal(req.MaxToolCalls)},
			Status: airuntime.ToolStatusFailed,
		}
		limited = true
	case !known:
		inv = airuntime.Invocation{Output: airuntime.ToolOutput{Text: "unknown tool " + b.Name}, Status: airuntime.ToolStatusFailed}
	default:
		inv = airuntime.InvokeTool(ctx, tool, input, req.ToolTimeout)
		invoked = true
	}
	sink(airuntime.Event{
		Kind: airuntime.EventToolResult, Seq: seq, Tool: b.Name, Args: inv.Output.ArgsSummary,
		Status: inv.Status, Rows: inv.Output.Rows, DurationMs: int(inv.Duration.Milliseconds()),
	})
	return inv, limited, invoked
}
