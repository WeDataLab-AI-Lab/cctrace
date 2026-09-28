package clauderuntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"cctrace/internal/airuntime"
)

// fakeMessages answers POST /v1/messages from a script, one entry per request,
// repeating the last entry, and records each request body.
type fakeMessages struct {
	mu       sync.Mutex
	script   []func(w http.ResponseWriter, r *http.Request)
	requests []map[string]any
}

func (f *fakeMessages) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		n := len(f.requests)
		f.requests = append(f.requests, body)
		step := f.script[min(n, len(f.script)-1)]
		f.mu.Unlock()
		step(w, r)
	}
}

func (f *fakeMessages) request(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[i]
}

func reply(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }
}

const usageJSON = `"usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":5,"output_tokens":7}`

func toolUse(blocks ...string) string {
	return `{"id":"msg","model":"claude-sonnet-5","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"","signature":"sig"},` +
		strings.Join(blocks, ",") + `],` + usageJSON + `}`
}

func useBlock(id, name, input string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s}`, id, name, input)
}

func final(text string) string {
	b, _ := json.Marshal(text)
	return `{"id":"msg","model":"claude-sonnet-5-20260101","stop_reason":"end_turn","content":[{"type":"text","text":` + string(b) + `}],` + usageJSON + `}`
}

const outSchema = `{"type":"object","additionalProperties":false,"required":["summary"],"properties":{"summary":{"type":"string","maxLength":20}}}`

func okTool(name string) airuntime.Tool {
	return airuntime.Tool{
		Name: name, Description: "d " + name, InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, args json.RawMessage) (airuntime.ToolOutput, error) {
			return airuntime.ToolOutput{Text: "result of " + name, Rows: 3}, nil
		},
	}
}

type events struct {
	mu   sync.Mutex
	list []airuntime.Event
}

func (e *events) sink(ev airuntime.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, ev)
}

func (e *events) of(kind airuntime.EventKind) []airuntime.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []airuntime.Event
	for _, ev := range e.list {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func baseRequest() airuntime.RunRequest {
	return airuntime.RunRequest{
		Instructions: "be brief", Prompt: "weekly report",
		Tools:        []airuntime.Tool{okTool("get_a"), okTool("get_b")},
		OutputSchema: json.RawMessage(outSchema),
		ToolTimeout:  time.Second, WallClock: 10 * time.Second,
	}
}

func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k < 0 {
				k += len(a)
			}
			if k < 0 || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func TestRunTwoToolCallsThenFinal(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
		reply(toolUse(useBlock("tu1", "get_a", `{"week":"w1"}`))),
		reply(toolUse(useBlock("tu2", "get_b", `{}`))),
		reply(final(`{"summary":"done"}`)),
	}}
	r, _ := newTestRuntime(t, f.handler(t))
	ev := &events{}
	res, err := r.Run(context.Background(), baseRequest(), ev.sink)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalText != `{"summary":"done"}` || res.Model != "claude-sonnet-5-20260101" {
		t.Fatalf("result = %+v", res)
	}
	want := airuntime.Usage{Reported: true, InputTokens: 3 * 115, CachedInputTokens: 3 * 100, OutputTokens: 3 * 7}
	if res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
	if u := ev.of(airuntime.EventUsage); len(u) != 3 || *u[2].Usage != want || u[0].Usage.InputTokens != 115 {
		t.Fatalf("usage events = %+v", u)
	}
	calls, results := ev.of(airuntime.EventToolCall), ev.of(airuntime.EventToolResult)
	if len(calls) != 2 || calls[0].Seq != 1 || calls[0].Tool != "get_a" || calls[0].Args["week"] != "w1" || calls[1].Seq != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	if len(results) != 2 || results[1].Seq != 2 || results[0].Status != airuntime.ToolStatusOK || results[0].Rows != 3 {
		t.Fatalf("results = %+v", results)
	}

	first := f.request(0)
	if first["model"] != "claude-sonnet-5" || first["max_tokens"] == nil {
		t.Errorf("model/max_tokens = %v/%v", first["model"], first["max_tokens"])
	}
	if dig(first, "system", -1, "cache_control", "type") != "ephemeral" || dig(first, "system", 0, "text") != "be brief" {
		t.Errorf("system = %v", first["system"])
	}
	if dig(first, "tools", 0, "name") != "get_a" || dig(first, "tools", 0, "input_schema", "type") != "object" ||
		dig(first, "tools", -1, "cache_control", "type") != "ephemeral" {
		t.Errorf("tools = %v", first["tools"])
	}
	// The answer schema is withheld until a tool has run, so the first request
	// carries none; its envelope is checked on the request that does carry it.
	if _, ok := first["output_config"]; ok {
		t.Errorf("the answer schema went out before any tool had run: %v", first["output_config"])
	}
	afterTools := f.request(1)
	if dig(afterTools, "output_config", "format", "type") != "json_schema" ||
		dig(afterTools, "output_config", "format", "schema", "properties", "summary", "maxLength") != nil ||
		dig(afterTools, "output_config", "format", "schema", "properties", "summary", "type") != "string" {
		t.Errorf("output_config = %v", afterTools["output_config"])
	}
	if dig(first, "messages", 0, "content", 0, "text") != "weekly report" {
		t.Errorf("messages = %v", first["messages"])
	}

	second := f.request(1)
	if dig(second, "messages", 1, "role") != "assistant" || dig(second, "messages", 1, "content", 0, "signature") != "sig" {
		t.Errorf("assistant turn not echoed unchanged: %v", dig(second, "messages", 1))
	}
	tr := dig(second, "messages", 2, "content", 0)
	if dig(tr, "type") != "tool_result" || dig(tr, "tool_use_id") != "tu1" || dig(tr, "is_error") != false ||
		dig(tr, "content") != "result of get_a" {
		t.Errorf("tool_result = %v", tr)
	}
	if n := len(dig(f.request(2), "messages").([]any)); n != 5 {
		t.Errorf("third request has %d messages, want 5", n)
	}
}

func TestRunSeveralToolUsesInOneResponse(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
		reply(toolUse(useBlock("tu1", "get_b", `{}`), useBlock("tu2", "get_a", `{}`))),
		reply(final(`{"summary":"ok"}`)),
	}}
	r, _ := newTestRuntime(t, f.handler(t))
	ev := &events{}
	if _, err := r.Run(context.Background(), baseRequest(), ev.sink); err != nil {
		t.Fatal(err)
	}
	calls := ev.of(airuntime.EventToolCall)
	if len(calls) != 2 || calls[0].Tool != "get_b" || calls[0].Seq != 1 || calls[1].Tool != "get_a" || calls[1].Seq != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	content := dig(f.request(1), "messages", 2, "content").([]any)
	if len(content) != 2 || dig(content, 0, "tool_use_id") != "tu1" || dig(content, 1, "tool_use_id") != "tu2" {
		t.Fatalf("tool results = %v", content)
	}
}

func TestRunToolFailureAndTimeoutAreErrorResults(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
		reply(toolUse(useBlock("tu1", "broken", `{}`), useBlock("tu2", "slow", `{}`), useBlock("tu3", "nope", `{}`))),
		reply(final(`{"summary":"ok"}`)),
	}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.ToolTimeout = 50 * time.Millisecond
	req.Tools = []airuntime.Tool{
		{Name: "broken", Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
			return airuntime.ToolOutput{}, errors.New("bad week")
		}},
		{Name: "slow", Handler: func(ctx context.Context, _ json.RawMessage) (airuntime.ToolOutput, error) {
			<-ctx.Done()
			return airuntime.ToolOutput{}, ctx.Err()
		}},
	}
	ev := &events{}
	if _, err := r.Run(context.Background(), req, ev.sink); err != nil {
		t.Fatal(err)
	}
	results := ev.of(airuntime.EventToolResult)
	if len(results) != 3 || results[0].Status != airuntime.ToolStatusFailed || results[1].Status != airuntime.ToolStatusTimeout ||
		results[2].Status != airuntime.ToolStatusFailed {
		t.Fatalf("results = %+v", results)
	}
	content := dig(f.request(1), "messages", 2, "content")
	for i := 0; i < 3; i++ {
		if dig(content, i, "is_error") != true {
			t.Errorf("result %d not is_error: %v", i, dig(content, i))
		}
	}
	if !strings.Contains(fmt.Sprint(dig(content, 0, "content")), "bad week") {
		t.Errorf("failure text = %v", dig(content, 0, "content"))
	}
}

func runErrCode(t *testing.T, err error, code string, sentinel error) {
	t.Helper()
	var re *airuntime.RunError
	if !errors.As(err, &re) || re.Code != code || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %s wrapping %v", err, code, sentinel)
	}
}

func TestRunToolCallLimit(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
		reply(toolUse(useBlock("tu", "get_a", `{}`))),
	}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.MaxToolCalls = 1
	ev := &events{}
	_, err := r.Run(context.Background(), req, ev.sink)
	runErrCode(t, err, "tool_call_limit", airuntime.ErrBudget)
	results := ev.of(airuntime.EventToolResult)
	if len(results) != 6 || results[0].Status != airuntime.ToolStatusOK || results[1].Status != airuntime.ToolStatusFailed {
		t.Fatalf("results = %+v", results)
	}
	if !strings.Contains(fmt.Sprint(dig(f.request(2), "messages", -1, "content", 0, "content")), "limit") {
		t.Errorf("refusal text = %v", dig(f.request(2), "messages", -1))
	}
}

func TestRunSeveralToolUsesPastLimitStillReachModel(t *testing.T) {
	var blocks []string
	for i := 0; i < 8; i++ {
		blocks = append(blocks, useBlock(fmt.Sprintf("tu%d", i), "get_a", `{}`))
	}
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
		reply(toolUse(blocks...)),
		reply(final(`{"summary":"ok"}`)),
	}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.MaxToolCalls = 3
	ev := &events{}
	res, err := r.Run(context.Background(), req, ev.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != `{"summary":"ok"}` || len(f.requests) != 2 {
		t.Fatalf("result %+v after %d requests", res, len(f.requests))
	}
	content := dig(f.request(1), "messages", 2, "content").([]any)
	refused := 0
	for _, c := range content {
		if strings.Contains(fmt.Sprint(dig(c, "content")), "limit") {
			refused++
		}
	}
	if len(content) != 8 || refused != 5 {
		t.Fatalf("tool results = %d, refused = %d, want 8 and 5", len(content), refused)
	}
}

func TestRunUnknownToolPastLimitCounts(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
		reply(toolUse(useBlock("tu", "nope", `{}`))),
	}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.MaxToolCalls = 1
	_, err := r.Run(context.Background(), req, nil)
	runErrCode(t, err, "tool_call_limit", airuntime.ErrBudget)
	if len(f.requests) != 6 {
		t.Fatalf("requests = %d, want 6 (1 within the limit + 5 refused)", len(f.requests))
	}
}

func TestRunLongToolOutputIsTruncated(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
		reply(toolUse(useBlock("tu1", "big", `{}`))),
		reply(final(`{"summary":"ok"}`)),
	}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.Tools = []airuntime.Tool{{Name: "big", Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
		return airuntime.ToolOutput{Text: strings.Repeat("가", airuntime.MaxToolOutputBytes)}, nil
	}}}
	if _, err := r.Run(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	out, _ := dig(f.request(1), "messages", 2, "content", 0, "content").(string)
	if len(out) > airuntime.MaxToolOutputBytes+len(airuntime.TruncatedMark) || !strings.HasSuffix(out, airuntime.TruncatedMark) || !utf8.ValidString(out) {
		t.Fatalf("output is %d bytes, marked %v, valid UTF-8 %v", len(out), strings.HasSuffix(out, airuntime.TruncatedMark), utf8.ValidString(out))
	}
}

func TestRunTokenBudget(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
		reply(toolUse(useBlock("tu", "get_a", `{}`))),
	}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.MaxTotalTokens = 200 // 122 per response
	_, err := r.Run(context.Background(), req, nil)
	runErrCode(t, err, "budget_exceeded", airuntime.ErrBudget)
	if len(f.requests) != 2 {
		t.Fatalf("requests = %d, want stop after the second response", len(f.requests))
	}
}

func hang(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }

func TestRunWallClock(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){hang}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.WallClock = 50 * time.Millisecond
	_, err := r.Run(context.Background(), req, nil)
	runErrCode(t, err, "time_limit", airuntime.ErrTimeLimit)
}

func TestRunCanceled(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){hang}}
	r, _ := newTestRuntime(t, f.handler(t))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := r.Run(ctx, baseRequest(), nil)
	runErrCode(t, err, "canceled", airuntime.ErrCanceled)
}

func TestRunSchemaViolation(t *testing.T) {
	for _, text := range []string{`not json`, `{"summary":"this summary is far too long"}`, `{"summary":"x","extra":1}`} {
		f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(final(text))}}
		r, _ := newTestRuntime(t, f.handler(t))
		_, err := r.Run(context.Background(), baseRequest(), nil)
		runErrCode(t, err, "schema_violation", airuntime.ErrProtocol)
		if strings.Contains(err.Error(), "far too long") {
			t.Errorf("model output in error: %v", err)
		}
	}
}

func TestRunStopReasons(t *testing.T) {
	cases := []struct{ stop, code string }{
		{"max_tokens", "output_limit"},
		{"refusal", "refused"},
		{"pause_turn", "protocol_error"},
	}
	for _, tc := range cases {
		body := `{"model":"claude-sonnet-5","stop_reason":"` + tc.stop + `","content":[{"type":"text","text":"{"}],` + usageJSON + `}`
		f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(body)}}
		r, _ := newTestRuntime(t, f.handler(t))
		_, err := r.Run(context.Background(), baseRequest(), nil)
		var re *airuntime.RunError
		if !errors.As(err, &re) || re.Code != tc.code {
			t.Errorf("%s: err = %v, want %s", tc.stop, err, tc.code)
		}
	}
}

func TestRunHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		code     string
		sentinel error
	}{
		{400, "invalid_request", airuntime.ErrProtocol},
		{401, "auth_failed", airuntime.ErrNotLoggedIn},
		{403, "permission_denied", airuntime.ErrUnavailable},
		{429, "rate_limited", airuntime.ErrUnavailable},
		{500, "provider_unavailable", airuntime.ErrUnavailable},
		{529, "provider_unavailable", airuntime.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
				func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tc.status)
					fmt.Fprintf(w, `{"type":"error","error":{"type":"x","message":"key %s prompt weekly report"}}`, testKey)
				},
			}}
			r, logs := newTestRuntime(t, f.handler(t))
			_, err := r.Run(context.Background(), baseRequest(), nil)
			runErrCode(t, err, tc.code, tc.sentinel)
			if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "weekly report") {
				t.Errorf("provider body in error: %v", err)
			}
			if strings.Contains(logs.String(), testKey) || !strings.Contains(logs.String(), "[redacted]") {
				t.Errorf("logs = %q, want key masked", logs)
			}
		})
	}
}

func TestRunNoKey(t *testing.T) {
	r := New(Config{APIKey: func(context.Context) (string, error) { return "", nil }})
	_, err := r.Run(context.Background(), baseRequest(), nil)
	runErrCode(t, err, "not_configured", airuntime.ErrNotConfigured)
}

func TestRunModelEffortAndPlainText(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(final("plain answer"))}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.Model, req.ReasoningEffort, req.OutputSchema, req.Instructions = "claude-opus-4-8", "low", nil, ""
	res, err := r.Run(context.Background(), req, nil)
	if err != nil || res.FinalText != "plain answer" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	body := f.request(0)
	if body["model"] != "claude-opus-4-8" || dig(body, "output_config", "effort") != "low" ||
		dig(body, "output_config", "format") != nil || dig(body, "thinking", "type") != "adaptive" || body["system"] != nil {
		t.Fatalf("body = %v", body)
	}

	f2 := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(final("x"))}}
	r2, _ := newTestRuntime(t, f2.handler(t))
	req.Model, req.ReasoningEffort = "claude-haiku-4-5-20251001", ""
	if _, err := r2.Run(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if b := f2.request(0); b["thinking"] != nil || b["output_config"] != nil {
		t.Fatalf("haiku body = %v", b)
	}
}

func TestRunEffortOnlyForSupportingModels(t *testing.T) {
	// Haiku 4.5 doesn't support effort; it should not be in the request body
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(final("answer"))}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.Model = "claude-haiku-4-5-20251001"
	req.ReasoningEffort = "low"
	req.OutputSchema = nil
	req.Instructions = ""
	if _, err := r.Run(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	body := f.request(0)
	if dig(body, "output_config", "effort") != nil {
		t.Errorf("haiku should not have output_config.effort; got %v", dig(body, "output_config"))
	}

	// Sonnet 5 supports effort; it should be in the request body
	f2 := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(final("answer"))}}
	r2, _ := newTestRuntime(t, f2.handler(t))
	req.Model = "claude-sonnet-5"
	if _, err := r2.Run(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	body2 := f2.request(0)
	if dig(body2, "output_config", "effort") != "low" {
		t.Errorf("sonnet should have output_config.effort='low'; got %v", dig(body2, "output_config"))
	}
}

func TestRunCacheControlAtBodyAndLastBlockLevels(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(final("ok"))}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.Instructions = "instructions"
	req.OutputSchema = nil
	if _, err := r.Run(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	body := f.request(0)

	// Body level cache_control must exist
	if dig(body, "cache_control", "type") != "ephemeral" {
		t.Errorf("body-level cache_control missing: %v", dig(body, "cache_control"))
	}

	// Last tool must have cache_control
	tools := dig(body, "tools").([]any)
	if dig(tools, -1, "cache_control", "type") != "ephemeral" {
		t.Errorf("last tool cache_control missing: %v", dig(tools, -1))
	}
	// Non-last tools must NOT have cache_control
	if dig(tools, 0, "cache_control") != nil {
		t.Errorf("first tool should not have cache_control: %v", dig(tools, 0))
	}

	// System message must have cache_control on the last item
	if dig(body, "system", 0, "cache_control", "type") != "ephemeral" {
		t.Errorf("system cache_control missing: %v", dig(body, "system", 0))
	}
}

func TestRunAdaptiveThinkingOnlyForSupportingModels(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "claude-sonnet-5", "claude-fable-5-1", "claude-opus-4-8"} {
		f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(final("ok"))}}
		r, _ := newTestRuntime(t, f.handler(t))
		req := baseRequest()
		req.Model = model
		req.OutputSchema = nil
		req.Instructions = ""
		if _, err := r.Run(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
		if dig(f.request(0), "thinking", "type") != "adaptive" {
			t.Errorf("%s should have adaptive thinking", model)
		}
	}

	// Haiku 4.5 doesn't support adaptive thinking
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(final("ok"))}}
	r, _ := newTestRuntime(t, f.handler(t))
	req := baseRequest()
	req.Model = "claude-haiku-4-5-20251001"
	req.OutputSchema = nil
	req.Instructions = ""
	if _, err := r.Run(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if dig(f.request(0), "thinking") != nil {
		t.Errorf("haiku should not have thinking field")
	}
}

func TestRunCancelSkipsRemainingTools(t *testing.T) {
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){
		reply(toolUse(useBlock("tu1", "cancel", `{}`), useBlock("tu2", "get_a", `{}`))),
	}}
	r, _ := newTestRuntime(t, f.handler(t))
	ctx, cancel := context.WithCancel(context.Background())
	req := baseRequest()
	var ran atomic.Int32
	req.Tools = []airuntime.Tool{
		{Name: "cancel", Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
			cancel()
			return airuntime.ToolOutput{}, nil
		}},
		{Name: "get_a", Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
			ran.Add(1)
			return airuntime.ToolOutput{}, nil
		}},
	}
	ev := &events{}
	_, err := r.Run(ctx, req, ev.sink)
	runErrCode(t, err, "canceled", airuntime.ErrCanceled)
	if ran.Load() != 0 || len(ev.of(airuntime.EventToolCall)) != 1 {
		t.Fatalf("ran=%d calls=%+v", ran.Load(), ev.of(airuntime.EventToolCall))
	}
}

func TestRunContextWindowStopIsBudget(t *testing.T) {
	body := `{"model":"claude-sonnet-5","stop_reason":"model_context_window_exceeded","content":[],` + usageJSON + `}`
	f := &fakeMessages{script: []func(http.ResponseWriter, *http.Request){reply(body)}}
	r, _ := newTestRuntime(t, f.handler(t))
	_, err := r.Run(context.Background(), baseRequest(), nil)
	runErrCode(t, err, "context_window_exceeded", airuntime.ErrBudget)
}
