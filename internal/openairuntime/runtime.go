// Package openairuntime runs a report turn on the OpenAI Responses API. There
// is no agent service in between: cctraced sends POST /responses, runs the
// function calls the model asks for itself, and sends their outputs back until
// the model answers.
package openairuntime

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

// RuntimeKey identifies this runtime in Info, consent keys and the API.
const RuntimeKey = "openai-api"

const (
	DefaultBaseURL = "https://api.openai.com/v1"
	DefaultModel   = "gpt-5.6-terra"
	// defaultRequestTimeout bounds one HTTP exchange; a reasoning response can
	// take minutes, and the run's WallClock is the real limit.
	defaultRequestTimeout = 10 * time.Minute
	maxBodyBytes          = 32 << 20
	// maxLoggedBody keeps a provider error readable in the log without the
	// log line growing with whatever the provider sent.
	maxLoggedBody = 2000
)

type Config struct {
	// APIKey is asked on every run and catalog refresh, so a key changed in
	// the environment or the admin screen applies without a restart.
	APIKey         func(ctx context.Context) (string, error)
	BaseURL        string
	HTTPClient     *http.Client
	DefaultModel   string
	RequestTimeout time.Duration
	Logger         *slog.Logger
}

type Runtime struct {
	cfg Config
	now func() time.Time

	modelsMu      sync.Mutex
	models        *cachedModels
	modelsFlight  singleflight.Group
	modelsTimeout time.Duration
}

func New(cfg Config) *Runtime {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = DefaultModel
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Runtime{cfg: cfg, now: time.Now, modelsTimeout: modelsRequestTimeout}
}

func (r *Runtime) Info() airuntime.Info {
	return airuntime.Info{Key: RuntimeKey, Model: r.cfg.DefaultModel, AuthMode: airuntime.AuthModeAPIKey}
}

func (r *Runtime) apiKey(ctx context.Context) (string, error) {
	if r.cfg.APIKey == nil {
		return "", nil
	}
	key, err := r.cfg.APIKey(ctx)
	if err != nil {
		r.cfg.Logger.Warn("openai api key lookup failed", "error", redact(err.Error(), ""))
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
		return nil, runErr("runtime_unavailable", "could not read the OpenAI API key", airuntime.ErrUnavailable)
	}
	if key == "" {
		return nil, runErr("not_configured", "OpenAI API key is not set", airuntime.ErrNotConfigured)
	}

	runCtx, cancel := airuntime.Deadline(ctx, req.WallClock)
	defer cancel()

	t := &turn{
		r: r, key: key, ctx: ctx, runCtx: runCtx, req: req, sink: sink,
		budget: airuntime.Budget{Max: req.MaxTotalTokens},
		timing: airuntime.SchemaTiming{HasTools: len(req.Tools) > 0, HasSchema: len(req.OutputSchema) > 0},
		model:  req.Model,
		strict: len(req.OutputSchema) > 0 && strictCompatible(req.OutputSchema),
		tools:  map[string]airuntime.Tool{},
	}
	if t.model == "" {
		t.model = r.cfg.DefaultModel
	}
	for _, tool := range req.Tools {
		t.tools[tool.Name] = tool
	}
	t.batch = airuntime.ToolBatch{
		Tools:   t.tools,
		Limit:   &airuntime.ToolLimit{Max: req.MaxToolCalls},
		Timeout: req.ToolTimeout,
		Sink:    sink,
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
	strict      bool
	tools       map[string]airuntime.Tool

	// input is the whole conversation so far, resent on every request.
	input  []json.RawMessage
	budget airuntime.Budget
	batch  airuntime.ToolBatch
	timing airuntime.SchemaTiming
	// toolsRan turns off the forced tool call once the model has actually used
	// a tool; leaving it forced would never let the turn end.
	toolsRan  bool
	respModel string
}

type apiResponse struct {
	Status            string            `json:"status"`
	Model             string            `json:"model"`
	Output            []json.RawMessage `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	// input_tokens already counts cached_tokens, the same meaning as
	// airuntime.Usage (api-reference/responses, usage object).
	Usage *struct {
		InputTokens        int64 `json:"input_tokens"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

type functionCall struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type outputMessage struct {
	Phase   string `json:"phase"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (t *turn) run() (string, error) {
	prompt, _ := json.Marshal(map[string]any{"role": "user", "content": t.req.Prompt})
	t.input = []json.RawMessage{prompt}
	for {
		sentSchema := t.timing.AttachSchema(t.toolsRan)
		resp, err := t.r.create(t.runCtx, t.key, t.body())
		var he *httpError
		// The fallback follows the schema, not the first request: the schema is
		// withheld until a tool has run, so the 400 it can draw arrives later.
		if sentSchema && t.strict && errors.As(err, &he) && he.status == http.StatusBadRequest && strings.HasPrefix(he.param, "text.format") {
			// The strict check follows the documented subset, which a model
			// may still refuse. One retry without strict costs a request; the
			// answer is validated here either way. A 400 about anything else
			// would fail the same way again.
			t.strict = false
			resp, err = t.r.create(t.runCtx, t.key, t.body())
		}
		if err != nil {
			return "", t.requestError(err)
		}
		if err := t.account(resp); err != nil {
			return "", err
		}

		var calls []functionCall
		var messages []outputMessage
		for _, raw := range resp.Output {
			var head struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &head) != nil {
				return "", runErr("protocol_error", "malformed output item", airuntime.ErrProtocol)
			}
			switch head.Type {
			case "reasoning":
			case "message":
				var m outputMessage
				if json.Unmarshal(raw, &m) != nil {
					return "", runErr("protocol_error", "malformed message item", airuntime.ErrProtocol)
				}
				messages = append(messages, m)
			case "function_call":
				var c functionCall
				if json.Unmarshal(raw, &c) != nil || c.CallID == "" {
					return "", runErr("protocol_error", "malformed function_call item", airuntime.ErrProtocol)
				}
				calls = append(calls, c)
			default:
				// The request offers only function tools; anything else was
				// not asked for.
				t.r.cfg.Logger.Warn("openai response contained an unrequested item", "type", head.Type)
				return "", runErr("unexpected_tool", "response contained an item that was not requested", airuntime.ErrUnexpectedTool)
			}
			// store:false keeps nothing server-side, so previous_response_id
			// has nothing to point at. Every output item is replayed instead;
			// reasoning items carry encrypted_content by default in stateless
			// mode (guides/reasoning, "Preserve reasoning without stored
			// responses").
			t.input = append(t.input, raw)
		}
		if len(calls) == 0 {
			return t.final(messages)
		}
		outputs, stopErr := t.runTools(calls)
		if t.runCtx.Err() != nil {
			return "", t.ctxError()
		}
		if stopErr != nil {
			return "", stopErr
		}
		t.input = append(t.input, outputs...)
	}
}

func (t *turn) body() map[string]any {
	b := map[string]any{"model": t.model, "input": t.input, "store": false}
	if t.req.Instructions != "" {
		// Instructions are not carried between responses, so every request
		// sends them.
		b["instructions"] = t.req.Instructions
	}
	if len(t.req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(t.req.Tools))
		for _, tool := range t.req.Tools {
			params := tool.InputSchema
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object"}`)
			}
			// strict is left out: Responses makes a function strict when its
			// schema allows and falls back to best effort otherwise
			// (guides/function-calling, "Strict mode"). Handlers validate
			// their own arguments.
			tools = append(tools, map[string]any{
				"type": "function", "name": tool.Name, "description": tool.Description, "parameters": params,
			})
		}
		b["tools"] = tools
		if t.timing.ForceTool(t.toolsRan) {
			b["tool_choice"] = "required"
		}
	}
	if t.req.ReasoningEffort != "" {
		// Only send effort if the model supports it
		if m := lookupModel(t.model); m != nil && m.SupportsEffort(t.req.ReasoningEffort) {
			b["reasoning"] = map[string]any{"effort": t.req.ReasoningEffort}
		}
	}
	if t.timing.AttachSchema(t.toolsRan) {
		b["text"] = map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "report", "schema": t.req.OutputSchema, "strict": t.strict,
		}}
	}
	return b
}

// account adds one response's usage and checks the budget and the status.
func (t *turn) account(resp *apiResponse) error {
	if resp.Model != "" {
		t.respModel = resp.Model
	}
	if resp.Usage != nil {
		if err := t.budget.Add(airuntime.Usage{
			Reported:          true,
			InputTokens:       resp.Usage.InputTokens,
			CachedInputTokens: resp.Usage.InputTokensDetails.CachedTokens,
			OutputTokens:      resp.Usage.OutputTokens,
		}, t.sink); err != nil {
			return err
		}
	}
	// The budget is checked after the response arrives, so a final answer
	// whose response crosses it is dropped, as in the Codex runtime. The turn
	// stops at the budget either way; max_output_tokens is not sent because it
	// also counts reasoning tokens and would cut answers short.
	// What the provider said goes to the log; messages stay fixed.
	switch resp.Status {
	case "completed":
		return nil
	case "incomplete":
		reason := ""
		if resp.IncompleteDetails != nil {
			reason = resp.IncompleteDetails.Reason
		}
		if reason == "max_output_tokens" {
			return airuntime.StopError(airuntime.StopOutputLimit, "response hit the output token limit")
		}
		t.r.cfg.Logger.Warn("openai response incomplete", "reason", reason)
		return airuntime.StopError(airuntime.StopUnknown, "response ended incomplete")
	case "failed":
		if resp.Error != nil {
			t.r.cfg.Logger.Warn("openai response failed", "code", resp.Error.Code, "message", redact(resp.Error.Message, t.key))
		} else {
			t.r.cfg.Logger.Warn("openai response failed without an error object")
		}
		return airuntime.StopError(airuntime.StopFailed, "OpenAI failed to generate the response")
	default:
		t.r.cfg.Logger.Warn("openai response ended with an unknown status", "status", resp.Status)
		return airuntime.StopError(airuntime.StopUnknown, "response ended with an unknown status")
	}
}

// final takes the answer from messages marked final_answer when the model
// marks phases, else from every message.
func (t *turn) final(messages []outputMessage) (string, error) {
	phased := false
	for _, m := range messages {
		phased = phased || m.Phase == "final_answer"
	}
	var text strings.Builder
	for _, m := range messages {
		if phased && m.Phase != "final_answer" {
			continue
		}
		for _, c := range m.Content {
			switch c.Type {
			case "output_text":
				text.WriteString(c.Text)
			case "refusal":
				return "", runErr("protocol_error", "model refused to answer", airuntime.ErrProtocol)
			}
		}
	}
	final := text.String()
	if final == "" {
		return "", runErr("protocol_error", "response ended without a final answer", airuntime.ErrProtocol)
	}
	if len(t.req.OutputSchema) > 0 {
		if err := airuntime.ValidateJSON(t.req.OutputSchema, final); err != nil {
			t.r.cfg.Logger.Warn("openai final answer does not match the output schema", "error", err, "strict", t.strict)
			return "", runErr("schema_violation", "final answer does not match the output schema", airuntime.ErrProtocol)
		}
	}
	return final, nil
}

// runTools runs one response's calls concurrently and returns their outputs in
// call order, which is also Seq order. Every call gets an output, since the
// next request must answer each call_id. The error is set when the model keeps
// calling past MaxToolCalls.
func (t *turn) runTools(calls []functionCall) ([]json.RawMessage, error) {
	batch := make([]airuntime.ToolCall, len(calls))
	for i, c := range calls {
		args := c.Arguments
		if args == "" {
			// Responses sends "" for a call that takes no arguments; the batch
			// reads an empty string as a malformed call.
			args = "{}"
		}
		batch[i] = airuntime.ToolCall{ID: c.CallID, Name: c.Name, Args: json.RawMessage(args)}
	}

	results, ran, stop := t.batch.Run(t.runCtx, batch)
	if ran {
		t.toolsRan = true
	}

	outputs := make([]json.RawMessage, 0, len(results))
	for _, res := range results {
		output := airuntime.TruncateText(res.Inv.Output.Text, airuntime.MaxToolOutputBytes, airuntime.TruncatedMark)
		item, _ := json.Marshal(map[string]any{"type": "function_call_output", "call_id": res.Call.ID, "output": output})
		outputs = append(outputs, item)
	}
	if stop != nil {
		return outputs, stop
	}
	return outputs, nil
}

// ctxError tells a caller's cancel from the turn's own wall clock.
func (t *turn) ctxError() error {
	if err := airuntime.CtxError(t.ctx, t.runCtx, t.req.WallClock); err != nil {
		return err
	}
	// Callers reach here only after a request already failed, so the turn ends
	// either way; the shared decision returns nil while both contexts are live.
	return runErr("time_limit", fmt.Sprintf("turn exceeded %s", t.req.WallClock), airuntime.ErrTimeLimit)
}

// requestError maps a failed request to a code and a fixed message. What the
// provider said is in the log only.
func (t *turn) requestError(err error) error {
	if t.runCtx.Err() != nil {
		return t.ctxError()
	}
	var he *httpError
	switch {
	case errors.As(err, &he) && he.status == http.StatusUnauthorized:
		return runErr("auth_failed", "OpenAI rejected the API key", airuntime.ErrNotLoggedIn)
	case he != nil && he.status == http.StatusForbidden:
		// The key is valid but not allowed this model, region or project.
		return runErr("permission_denied", "OpenAI API key is not permitted to make this request", airuntime.ErrUnavailable)
	case he != nil && he.status == http.StatusTooManyRequests:
		return runErr("rate_limited", "OpenAI rate limit or quota reached", airuntime.ErrUnavailable)
	case he != nil && he.status >= 500:
		return runErr("provider_unavailable", "OpenAI API is unavailable", airuntime.ErrUnavailable)
	case he != nil:
		return runErr("invalid_request", "OpenAI rejected the request", airuntime.ErrProtocol)
	case errors.Is(err, airuntime.ErrProtocol):
		return runErr("protocol_error", "OpenAI response could not be read", airuntime.ErrProtocol)
	default:
		return runErr("provider_unavailable", "could not reach the OpenAI API", airuntime.ErrUnavailable)
	}
}

func (r *Runtime) create(ctx context.Context, key string, body map[string]any) (*apiResponse, error) {
	raw, err := r.do(ctx, key, http.MethodPost, "/responses", body)
	if err != nil {
		return nil, err
	}
	var resp apiResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		r.cfg.Logger.Warn("openai response could not be decoded", "error", err)
		return nil, fmt.Errorf("%w: decode response: %v", airuntime.ErrProtocol, err)
	}
	return &resp, nil
}

// httpError is a non-200 answer. param and code are the provider's error
// fields, kept to tell causes apart; they never reach a user message.
type httpError struct {
	status      int
	param, code string
}

func (e *httpError) Error() string { return fmt.Sprintf("OpenAI API returned HTTP %d", e.status) }

// do sends one request and returns the body of a 200. Failures are logged
// with the key redacted and come back as *httpError or the transport error.
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
	req, err := http.NewRequestWithContext(reqCtx, method, r.cfg.BaseURL+path, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		r.cfg.Logger.Warn("openai request failed", "method", method, "path", path, "error", redact(err.Error(), key))
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		r.cfg.Logger.Warn("openai response read failed", "method", method, "path", path, "error", redact(err.Error(), key))
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		r.cfg.Logger.Warn("openai request rejected", "method", method, "path", path, "status", resp.StatusCode, "body", loggedBody(raw, key))
		var e struct {
			Error struct {
				Param string `json:"param"`
				Code  string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return nil, &httpError{status: resp.StatusCode, param: e.Error.Param, code: e.Error.Code}
	}
	return raw, nil
}

// keyPattern catches OpenAI keys other than the one in use, including the
// partly masked form a 401 message echoes.
var keyPattern = regexp.MustCompile(`sk-[A-Za-z0-9_*.\-]{3,}`)

// loggedBody is a rejected response's body as it goes to the log. It masks
// before cutting, or a key straddling the cut survives as a short prefix the
// key pattern no longer matches, and cuts on a rune boundary.
func loggedBody(raw []byte, key string) string {
	return airuntime.TruncateText(redact(string(raw), key), maxLoggedBody, "")
}

func redact(s, key string) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "[redacted]")
	}
	return keyPattern.ReplaceAllString(s, "sk-[redacted]")
}
