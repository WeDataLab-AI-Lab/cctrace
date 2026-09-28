// Package chatruntime runs a report turn on OpenAI-compatible chat/completions API.
// It supports NVIDIA and LiteLLM providers that implement the /v1/chat/completions endpoint.
package chatruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"cctrace/internal/airuntime"
)

// RuntimeKey identifies this runtime in Info.
const RuntimeKey = "chat-completions"

const (
	// The request paths below already carry /v1, so a base URL is the host root.
	DefaultBaseURL        = "https://api.openai.com"
	defaultRequestTimeout = 10 * time.Minute
	maxBodyBytes          = 32 << 20
	maxLoggedBody         = 2000
)

type Config struct {
	APIKey func(ctx context.Context) (string, error)
	// RuntimeKey is what Info reports, so the service can find this instance by
	// the key an admin selected. One implementation serves several runtimes --
	// they differ only by base URL and key -- so a fixed key would make every
	// instance answer to the same name and none to "nvidia-api" or
	// "litellm-api". Empty keeps RuntimeKey, for callers with one instance.
	RuntimeKey string
	// BaseURL is resolved per request, like APIKey. The LiteLLM address is the
	// one provider setting an admin types in and saves, and reading it once at
	// boot meant a saved address did nothing until the daemon restarted.
	BaseURL        func(ctx context.Context) (string, error)
	HTTPClient     *http.Client
	DefaultModel   string
	ProviderName   string // "nvidia" or "litellm"
	RequestTimeout time.Duration
	Logger         *slog.Logger
}

type Runtime struct {
	cfg Config
	now func() time.Time

	modelsMu     sync.Mutex
	models       map[string]modelsCacheEntry
	modelsFlight singleflight.Group
}

func New(cfg Config) *Runtime {
	if cfg.RuntimeKey == "" {
		cfg.RuntimeKey = RuntimeKey
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = "gpt-3.5-turbo"
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Runtime{cfg: cfg, now: time.Now, models: map[string]modelsCacheEntry{}}
}

// baseURL resolves the address for one request and normalises it. Every
// provider documents its endpoint as ".../v1" and an admin copies it that way;
// the request paths add /v1 themselves, so accepting the suffix here is the
// difference between working and a 404 on /v1/v1/chat/completions that reads
// like a missing model.
func (r *Runtime) baseURL(ctx context.Context) (string, error) {
	raw := DefaultBaseURL
	if r.cfg.BaseURL != nil {
		v, err := r.cfg.BaseURL(ctx)
		if err != nil {
			return "", runErr("runtime_unavailable", "could not read the runtime address", airuntime.ErrUnavailable)
		}
		if strings.TrimSpace(v) == "" {
			return "", runErr("not_configured", "runtime address is not set", airuntime.ErrNotConfigured)
		}
		raw = v
	}
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	return strings.TrimSuffix(raw, "/v1"), nil
}

func (r *Runtime) Info() airuntime.Info {
	return airuntime.Info{
		Key:      r.cfg.RuntimeKey,
		Model:    r.cfg.DefaultModel,
		AuthMode: airuntime.AuthModeAPIKey,
	}
}

func (r *Runtime) Status(ctx context.Context) airuntime.Status {
	key, err := r.apiKey(ctx)
	if err != nil {
		return airuntime.Status{Reason: "API key not available"}
	}
	if key == "" {
		return airuntime.Status{Reason: "API key not configured"}
	}
	// A self-hosted provider needs an address as much as a key. Reporting it
	// configured without one puts a runtime on the screen that fails at the
	// first call instead of saying what is missing.
	if _, err := r.baseURL(ctx); err != nil {
		return airuntime.Status{Reason: "런타임 주소가 설정되지 않았습니다"}
	}
	return airuntime.Status{Configured: true, Available: true}
}

func (r *Runtime) apiKey(ctx context.Context) (string, error) {
	if r.cfg.APIKey == nil {
		return "", nil
	}
	key, err := r.cfg.APIKey(ctx)
	if err != nil {
		r.cfg.Logger.Warn("api key lookup failed", "error", redact(err.Error(), ""))
	}
	return strings.TrimSpace(key), err
}

func runErr(code, message string, sentinel error) *airuntime.RunError {
	return airuntime.NewRunError(code, message, sentinel)
}

func (r *Runtime) Run(ctx context.Context, req airuntime.RunRequest, sink func(airuntime.Event)) (*airuntime.Result, error) {
	start := time.Now()
	if sink == nil {
		sink = func(airuntime.Event) {}
	}
	key, err := r.apiKey(ctx)
	if err != nil {
		return nil, runErr("runtime_unavailable", "could not read API key", airuntime.ErrUnavailable)
	}
	if key == "" {
		return nil, runErr("not_configured", "API key is not set", airuntime.ErrNotConfigured)
	}

	runCtx, cancel := airuntime.Deadline(ctx, req.WallClock)
	defer cancel()

	t := &turn{
		r:      r,
		key:    key,
		ctx:    ctx,
		runCtx: runCtx,
		req:    req,
		sink:   sink,
		budget: airuntime.Budget{Max: req.MaxTotalTokens},
		model:  req.Model,
		tools:  map[string]airuntime.Tool{},
	}
	if t.model == "" {
		t.model = r.cfg.DefaultModel
	}
	t.batch = airuntime.ToolBatch{
		Tools:   t.tools,
		Limit:   &airuntime.ToolLimit{Max: req.MaxToolCalls},
		Timeout: req.ToolTimeout,
		Sink:    sink,
	}
	for _, tool := range req.Tools {
		t.tools[tool.Name] = tool
	}
	final, err := t.run()
	if err != nil {
		return nil, err
	}
	model := t.respModel
	if model == "" {
		model = t.model
	}
	return &airuntime.Result{FinalText: final, Model: model, Usage: t.budget.Usage(), Duration: time.Since(start)}, nil
}

type turn struct {
	r           *Runtime
	key         string
	ctx, runCtx context.Context
	req         airuntime.RunRequest
	sink        func(airuntime.Event)
	model       string
	tools       map[string]airuntime.Tool

	messages      []message
	budget        airuntime.Budget
	batch         airuntime.ToolBatch
	respModel     string
	schemaRetried bool
	// toolsRan turns off the first turn's forced tool call once the model has
	// actually used a tool; leaving it forced would never let the turn end.
	toolsRan bool
}

type message struct {
	Role       string      `json:"role"`
	Content    interface{} `json:"content"`
	ToolCalls  []toolCall  `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	Name       string      `json:"name,omitempty"`
}

type toolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type apiResponse struct {
	ID      string    `json:"id"`
	Object  string    `json:"object"`
	Created int64     `json:"created"`
	Model   string    `json:"model"`
	Choices []choice  `json:"choices"`
	Usage   *usage    `json:"usage"`
	Error   *apiError `json:"error"`
}

type choice struct {
	Index        int             `json:"index"`
	Message      responseMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

type responseMessage struct {
	Role      string      `json:"role"`
	Content   interface{} `json:"content"`
	ToolCalls []toolCall  `json:"tool_calls"`
}

type usage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (t *turn) run() (string, error) {
	// Instructions carry the report's rules and the demand to read segments with
	// the tools. Dropping them left the model with a schema and no task: it
	// answered with a plan ("I will query the segments and then write"), called
	// no tool, and the run completed empty because the schema was satisfied.
	t.messages = nil
	if t.req.Instructions != "" {
		t.messages = append(t.messages, message{Role: "system", Content: t.req.Instructions})
	}
	t.messages = append(t.messages, message{Role: "user", Content: t.req.Prompt})
	for {
		resp, err := t.create(t.runCtx)
		if err != nil {
			return "", t.requestError(err)
		}
		if err := t.account(resp); err != nil {
			return "", err
		}

		if len(resp.Choices) == 0 {
			return "", runErr("protocol_error", "empty choices in response", airuntime.ErrProtocol)
		}
		choice := resp.Choices[0]

		// "length" means the model ran out of output tokens mid-answer. What
		// came back still parses and still looks like a final answer, so a turn
		// that does not read this field stores a half-written report as a
		// finished one. openairuntime reads response.status for the same thing
		// and clauderuntime reads stop_reason.
		if choice.FinishReason == "length" {
			return "", airuntime.StopError(airuntime.StopOutputLimit, "response hit the output token limit")
		}

		// Extract tool calls and text from response
		var calls []toolCall
		var text string
		if len(choice.Message.ToolCalls) > 0 {
			calls = choice.Message.ToolCalls
		} else if choice.Message.Content != nil {
			if str, ok := choice.Message.Content.(string); ok {
				text = str
			}
		}

		// A tool-call turn's content is replayed as-is, except an explicit empty
		// string: NVIDIA's vLLM gateway answers a tool-call turn with content: ""
		// and then rejects that same string on the next request with "Empty
		// content is not allowed for assistant messages" (400, issue #700).
		// content: null and an omitted key are both accepted, so an empty string
		// becomes nil here, which this struct's plain `interface{}` field
		// marshals as null.
		assistantContent := choice.Message.Content
		if len(calls) > 0 {
			if s, ok := assistantContent.(string); ok && s == "" {
				assistantContent = nil
			}
		}

		// Add assistant message to conversation
		t.messages = append(t.messages, message{
			Role:      "assistant",
			Content:   assistantContent,
			ToolCalls: calls,
		})

		if len(calls) == 0 {
			// No more tool calls, return final text
			if text == "" {
				return "", runErr("protocol_error", "response ended without a final answer", airuntime.ErrProtocol)
			}
			if len(t.req.OutputSchema) > 0 {
				if err := airuntime.ValidateJSON(t.req.OutputSchema, text); err != nil {
					if !t.schemaRetried {
						t.schemaRetried = true
						continue
					}
					t.r.cfg.Logger.Warn("final answer does not match the output schema", "error", err)
					return "", runErr("schema_violation", "final answer does not match the output schema", airuntime.ErrProtocol)
				}
			}
			return text, nil
		}

		// Once a tool has run, the data the report needs is on its way in, so the
		// next turn stops forcing a call and may answer instead.
		t.toolsRan = true

		// Run tools and get results
		outputs, stopErr := t.runTools(calls)
		if t.runCtx.Err() != nil {
			return "", t.ctxError()
		}
		if stopErr != nil {
			return "", stopErr
		}

		// Add tool results to messages
		t.messages = append(t.messages, outputs...)
	}
}

func (t *turn) account(resp *apiResponse) error {
	if resp.Model != "" {
		t.respModel = resp.Model
	}
	if resp.Usage != nil {
		delta := airuntime.Usage{
			Reported:     true,
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}
		if resp.Usage.PromptTokensDetails != nil {
			delta.CachedInputTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
		if err := t.budget.Add(delta, t.sink); err != nil {
			return err
		}
	}
	return nil
}

func (t *turn) runTools(calls []toolCall) ([]message, error) {
	batch := make([]airuntime.ToolCall, len(calls))
	for i, c := range calls {
		batch[i] = airuntime.ToolCall{ID: c.ID, Name: c.Function.Name, Args: json.RawMessage(c.Function.Arguments)}
	}

	results, _, stop := t.batch.Run(t.runCtx, batch)

	outputs := make([]message, 0, len(results))
	for _, res := range results {
		outputs = append(outputs, message{
			Role:       "tool",
			Content:    airuntime.TruncateText(res.Inv.Output.Text, airuntime.MaxToolOutputBytes, airuntime.TruncatedMark),
			ToolCallID: res.Call.ID,
			Name:       res.Call.Name,
		})
	}
	if stop != nil {
		return outputs, stop
	}
	return outputs, nil
}

func (t *turn) ctxError() error {
	if err := airuntime.CtxError(t.ctx, t.runCtx, t.req.WallClock); err != nil {
		return err
	}
	if t.ctx.Err() != nil {
		return runErr("canceled", "run canceled", airuntime.ErrCanceled)
	}
	return runErr("time_limit", fmt.Sprintf("turn exceeded %s", t.req.WallClock), airuntime.ErrTimeLimit)
}

// routingMiss reports whether a 404 body is the HTTP router saying the path does
// not exist, rather than the provider saying it does not serve that model.
// NVIDIA answers "404 page not found" and LiteLLM {"detail":"Not Found"}; a
// missing model comes back with the model id and a provider error shape.
func routingMiss(body string) bool {
	b := strings.ToLower(body)
	return strings.Contains(b, "page not found") ||
		strings.Contains(b, `"detail":"not found"`) ||
		strings.Contains(b, `"detail": "not found"`)
}

func (t *turn) requestError(err error) error {
	if t.runCtx.Err() != nil {
		return t.ctxError()
	}
	// An error this runtime already classified -- an unset address, say --
	// keeps its code. Re-reading it as a transport failure would report
	// "could not reach the provider" for something never sent.
	var already *airuntime.RunError
	if errors.As(err, &already) {
		return already
	}
	var he *httpError
	switch {
	case errors.As(err, &he) && he.status == http.StatusUnauthorized:
		return runErr("auth_failed", "API key was rejected", airuntime.ErrNotLoggedIn)
	case he != nil && he.status == http.StatusForbidden:
		return runErr("permission_denied", "API key is not permitted to make this request", airuntime.ErrUnavailable)
	case he != nil && he.status == http.StatusNotFound && routingMiss(he.body):
		// A 404 from the router, not from the model registry: the base URL points
		// somewhere that has no chat/completions. Saying "model not available"
		// here sent a wrong-address report chasing the model catalog instead.
		return runErr("endpoint_not_found", "base URL has no chat/completions endpoint", airuntime.ErrUnavailable)
	case he != nil && he.status == http.StatusNotFound:
		return runErr("model_unavailable", "model not available for this account", airuntime.ErrUnavailable)
	case he != nil && he.status == http.StatusTooManyRequests:
		return runErr("rate_limited", "rate limit exceeded", airuntime.ErrUnavailable)
	case he != nil && he.status >= 500:
		return runErr("provider_unavailable", "provider API is unavailable", airuntime.ErrUnavailable)
	case he != nil:
		return runErr("invalid_request", "provider rejected the request", airuntime.ErrProtocol)
	case errors.Is(err, airuntime.ErrProtocol):
		return runErr("protocol_error", "response could not be read", airuntime.ErrProtocol)
	default:
		return runErr("provider_unavailable", "could not reach the provider API", airuntime.ErrUnavailable)
	}
}

func (t *turn) create(ctx context.Context) (*apiResponse, error) {
	body := map[string]any{
		"model":    t.model,
		"messages": t.messages,
	}
	if len(t.req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(t.req.Tools))
		for _, tool := range t.req.Tools {
			params := tool.InputSchema
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object"}`)
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tool.Name,
					"description": tool.Description,
					"parameters":  params,
				},
			})
		}
		body["tools"] = tools
		// Asking for the answer schema before any tool has run puts the model in
		// a bind, and it takes the cheaper way out: z-ai/glm-5.3 answered "I will
		// look up the segments and then write", called nothing, and the run
		// finished empty because that sentence fit the schema. So the first turn
		// forces a call and withholds the schema; once data is coming in, the
		// tools stay available and the schema joins them.
		if !t.toolsRan {
			body["tool_choice"] = "required"
		}
	}
	if len(t.req.OutputSchema) > 0 && (t.toolsRan || len(t.req.Tools) == 0) {
		var schema any
		_ = json.Unmarshal(t.req.OutputSchema, &schema)
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "output",
				"schema": schema,
				"strict": true,
			},
		}
	}

	return t.send(ctx, body)
}

// send posts one assembled body and decodes the reply. It is separate from
// create so the tool-carrying request can return early without repeating it.
func (t *turn) send(ctx context.Context, body map[string]any) (*apiResponse, error) {
	raw, err := t.r.do(t.runCtx, t.key, http.MethodPost, "/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}
	var resp apiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		limit := len(raw)
		if limit > 500 {
			limit = 500
		}
		t.r.cfg.Logger.Warn("response could not be decoded", "error", err, "raw", string(raw[:limit]))
		return nil, fmt.Errorf("%w: decode response: %v", airuntime.ErrProtocol, err)
	}
	return &resp, nil
}

type httpError struct {
	status int
	// body is bounded and kept only to tell apart failures that share a status:
	// a 404 from the router versus one from the model registry.
	body string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("API returned HTTP %d", e.status)
}

func (r *Runtime) do(ctx context.Context, key, method, path string, body any) ([]byte, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(b)
	}
	reqCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	base, err := r.baseURL(reqCtx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(reqCtx, method, base+path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		r.cfg.Logger.Warn("request failed", "method", method, "path", path, "error", redact(err.Error(), key))
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		r.cfg.Logger.Warn("response read failed", "method", method, "path", path, "error", redact(err.Error(), key))
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		r.cfg.Logger.Warn("request rejected", "method", method, "path", path, "status", resp.StatusCode, "body", loggedBody(raw, key))
		// The body is kept, bounded, because a 404 means two different things:
		// this provider has no such model, or the base URL points somewhere with
		// no chat/completions at all. Only the body tells them apart.
		body := string(raw)
		if len(body) > maxLoggedBody {
			body = body[:maxLoggedBody]
		}
		return nil, &httpError{status: resp.StatusCode, body: body}
	}
	return raw, nil
}

var keyPattern = regexp.MustCompile(`[A-Za-z0-9_\-\.]{20,}`)

func loggedBody(raw []byte, key string) string {
	s := redact(string(raw), key)
	if len(s) <= maxLoggedBody {
		return s
	}
	return s[:maxLoggedBody] + "..."
}

func redact(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "[redacted]")
	}
	return keyPattern.ReplaceAllString(s, "[redacted]")
}
