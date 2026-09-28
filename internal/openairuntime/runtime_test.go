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
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"cctrace/internal/airuntime"
)

const testKey = "sk-proj-test0123456789abcdefSECRET"

var answerSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["answer"],"properties":{"answer":{"type":"string","maxLength":10}}}`)

// fakeAPI is a Responses API stand-in. respond gets the request number
// (0-based, per path) and the decoded body.
type fakeAPI struct {
	srv     *httptest.Server
	mu      sync.Mutex
	bodies  []map[string]any
	auth    []string
	calls   map[string]int
	respond func(r *http.Request, n int, body map[string]any) (int, string)
}

func newFake(t *testing.T, respond func(r *http.Request, n int, body map[string]any) (int, string)) *fakeAPI {
	t.Helper()
	f := &fakeAPI{respond: respond, calls: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		n := f.calls[r.URL.Path]
		f.calls[r.URL.Path]++
		if r.URL.Path == "/responses" {
			f.bodies = append(f.bodies, body)
		}
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		status, out := f.respond(r, n, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) body(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

func (f *fakeAPI) authAt(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth[i]
}

func (f *fakeAPI) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func newTestRuntime(url, key string, logs *bytes.Buffer) *Runtime {
	if logs == nil {
		logs = &bytes.Buffer{}
	}
	return New(Config{
		APIKey:  func(context.Context) (string, error) { return key, nil },
		BaseURL: url,
		Logger:  slog.New(slog.NewTextHandler(logs, nil)),
	})
}

func response(model string, in, cached, out int64, items ...string) string {
	return fmt.Sprintf(`{"id":"resp_x","object":"response","status":"completed","model":%q,"output":[%s],"usage":{"input_tokens":%d,"input_tokens_details":{"cached_tokens":%d},"output_tokens":%d,"total_tokens":%d}}`,
		model, strings.Join(items, ","), in, cached, out, in+out)
}

func fnCall(callID, name, args string) string {
	a, _ := json.Marshal(args)
	return fmt.Sprintf(`{"type":"function_call","id":"fc_%s","call_id":%q,"name":%q,"arguments":%s,"status":"completed"}`, callID, callID, name, a)
}

func message(text string) string {
	t, _ := json.Marshal(text)
	return fmt.Sprintf(`{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":%s,"annotations":[]}]}`, t)
}

const reasoningItem = `{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc-abc"}`

type recorder struct {
	mu     sync.Mutex
	events []airuntime.Event
}

func (r *recorder) sink(e airuntime.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) kinds(kind airuntime.EventKind) []airuntime.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []airuntime.Event
	for _, e := range r.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func lookupTool(calls *[]string, mu *sync.Mutex) airuntime.Tool {
	return airuntime.Tool{
		Name:        "lookup",
		Description: "looks things up",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`),
		Handler: func(ctx context.Context, args json.RawMessage) (airuntime.ToolOutput, error) {
			mu.Lock()
			*calls = append(*calls, string(args))
			mu.Unlock()
			return airuntime.ToolOutput{Text: "result for " + string(args), Rows: 3}, nil
		},
	}
}

func runErrCode(t *testing.T, err error, code string, sentinel error) {
	t.Helper()
	var re *airuntime.RunError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RunError %s", err, code)
	}
	if re.Code != code || !errors.Is(err, sentinel) {
		t.Fatalf("err = %v (code %q), want code %q wrapping %v", err, re.Code, code, sentinel)
	}
}

func TestRunTwoToolCallsThenAnswer(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		switch n {
		case 0:
			return 200, response("gpt-5.6-terra", 100, 0, 20, reasoningItem, fnCall("call_1", "lookup", `{"q":"a"}`))
		case 1:
			return 200, response("gpt-5.6-terra", 150, 80, 30, fnCall("call_2", "lookup", `{"q":"b"}`))
		default:
			return 200, response("gpt-5.6-terra", 200, 120, 40, message(`{"answer":"done"}`))
		}
	})
	var calls []string
	var mu sync.Mutex
	rt := newTestRuntime(f.srv.URL, testKey, nil)
	rec := &recorder{}
	res, err := rt.Run(context.Background(), airuntime.RunRequest{
		ReasoningEffort: "low",
		Instructions:    "be brief",
		Prompt:          "weekly report",
		Tools:           []airuntime.Tool{lookupTool(&calls, &mu)},
		OutputSchema:    answerSchema,
	}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != `{"answer":"done"}` || res.Model != "gpt-5.6-terra" {
		t.Fatalf("result = %+v", res)
	}
	want := airuntime.Usage{Reported: true, InputTokens: 450, CachedInputTokens: 200, OutputTokens: 90}
	if res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
	if len(calls) != 2 || calls[0] != `{"q":"a"}` || calls[1] != `{"q":"b"}` {
		t.Fatalf("handler calls = %v", calls)
	}

	first := f.body(0)
	if first["store"] != false || first["model"] != DefaultModel || first["instructions"] != "be brief" {
		t.Fatalf("first request = %v", first)
	}
	if first["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatalf("reasoning = %v", first["reasoning"])
	}
	if _, ok := first["previous_response_id"]; ok {
		t.Fatal("previous_response_id sent with store:false")
	}
	tool := first["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "lookup" || tool["parameters"] == nil {
		t.Fatalf("tool = %v", tool)
	}
	// The schema rides the request that can use it: with tools present it is
	// withheld until one has run, so its envelope is checked on that request.
	if _, ok := first["text"]; ok {
		t.Error("the answer schema went out before any tool had run")
	}
	format := f.body(1)["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true || format["schema"] == nil || format["name"] == "" {
		t.Fatalf("format = %v", format)
	}
	if f.authAt(0) != "Bearer "+testKey {
		t.Fatalf("auth header = %q", f.authAt(0))
	}

	// The second request replays the prompt, every output item and the tool output.
	input := f.body(1)["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("second input = %v", input)
	}
	if input[1].(map[string]any)["encrypted_content"] != "enc-abc" {
		t.Fatalf("reasoning item not replayed: %v", input[1])
	}
	out := input[3].(map[string]any)
	if out["type"] != "function_call_output" || out["call_id"] != "call_1" || out["output"] != `result for {"q":"a"}` {
		t.Fatalf("tool output item = %v", out)
	}
	if got := len(f.body(2)["input"].([]any)); got != 6 {
		t.Fatalf("third input has %d items, want 6", got)
	}

	callEvents, results := rec.kinds(airuntime.EventToolCall), rec.kinds(airuntime.EventToolResult)
	if len(callEvents) != 2 || callEvents[0].Seq != 1 || callEvents[1].Seq != 2 || callEvents[0].Args["q"] != "a" {
		t.Fatalf("tool_call events = %+v", callEvents)
	}
	if len(results) != 2 || results[1].Seq != 2 || results[0].Status != airuntime.ToolStatusOK || results[0].Rows != 3 {
		t.Fatalf("tool_result events = %+v", results)
	}
	usage := rec.kinds(airuntime.EventUsage)
	if len(usage) != 3 || *usage[2].Usage != want || usage[0].Usage.InputTokens != 100 {
		t.Fatalf("usage events = %+v", usage)
	}
}

func TestRunParallelCallsKeepSeqOrder(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			return 200, response("m", 10, 0, 5, fnCall("call_a", "slow", `{}`), fnCall("call_b", "fast", `{}`))
		}
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	tools := []airuntime.Tool{
		{Name: "slow", Handler: func(ctx context.Context, _ json.RawMessage) (airuntime.ToolOutput, error) {
			time.Sleep(50 * time.Millisecond)
			return airuntime.ToolOutput{Text: "slow done"}, nil
		}},
		{Name: "fast", Handler: func(ctx context.Context, _ json.RawMessage) (airuntime.ToolOutput, error) {
			return airuntime.ToolOutput{Text: "fast done"}, nil
		}},
	}
	rec := &recorder{}
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", Tools: tools, OutputSchema: answerSchema}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	results := rec.kinds(airuntime.EventToolResult)
	if len(results) != 2 || results[0].Seq != 1 || results[0].Tool != "slow" || results[1].Seq != 2 || results[1].Tool != "fast" {
		t.Fatalf("results = %+v", results)
	}
	input := f.body(1)["input"].([]any)
	a, b := input[3].(map[string]any), input[4].(map[string]any)
	if a["call_id"] != "call_a" || a["output"] != "slow done" || b["call_id"] != "call_b" || b["output"] != "fast done" {
		t.Fatalf("outputs = %v %v", a, b)
	}
}

func TestRunParallelCallsRunConcurrently(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			return 200, response("m", 10, 0, 5, fnCall("call_a", "a", `{}`), fnCall("call_b", "b", `{}`))
		}
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	// Each handler returns only once both have started, so handlers run one
	// after another would time out.
	var started sync.WaitGroup
	started.Add(2)
	both := make(chan struct{})
	go func() { started.Wait(); close(both) }()
	handler := func(ctx context.Context, _ json.RawMessage) (airuntime.ToolOutput, error) {
		started.Done()
		select {
		case <-both:
			return airuntime.ToolOutput{Text: "done"}, nil
		case <-ctx.Done():
			return airuntime.ToolOutput{}, ctx.Err()
		}
	}
	rec := &recorder{}
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", OutputSchema: answerSchema, ToolTimeout: 2 * time.Second,
		Tools: []airuntime.Tool{{Name: "a", Handler: handler}, {Name: "b", Handler: handler}},
	}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if results := rec.kinds(airuntime.EventToolResult); len(results) != 2 || results[0].Status != airuntime.ToolStatusOK || results[1].Status != airuntime.ToolStatusOK {
		t.Fatalf("results = %+v, want both ok", results)
	}
}

func TestRunToolFailureTimeoutAndUnknownGoBackToModel(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			return 200, response("m", 10, 0, 5, fnCall("c1", "broken", `{}`), fnCall("c2", "hang", `{}`), fnCall("c3", "nope", `{}`))
		}
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	tools := []airuntime.Tool{
		{Name: "broken", Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
			return airuntime.ToolOutput{}, errors.New("bad week")
		}},
		{Name: "hang", Handler: func(ctx context.Context, _ json.RawMessage) (airuntime.ToolOutput, error) {
			<-ctx.Done()
			return airuntime.ToolOutput{}, ctx.Err()
		}},
	}
	rec := &recorder{}
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", Tools: tools, OutputSchema: answerSchema, ToolTimeout: 30 * time.Millisecond,
	}, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	results := rec.kinds(airuntime.EventToolResult)
	if len(results) != 3 || results[0].Status != airuntime.ToolStatusFailed || results[1].Status != airuntime.ToolStatusTimeout || results[2].Status != airuntime.ToolStatusFailed {
		t.Fatalf("results = %+v", results)
	}
	input := f.body(1)["input"].([]any)
	outputs := []string{}
	for _, it := range input[4:] {
		outputs = append(outputs, it.(map[string]any)["output"].(string))
	}
	if outputs[0] != "tool failed: bad week" || !strings.Contains(outputs[1], "timed out") || !strings.Contains(outputs[2], "unknown tool") {
		t.Fatalf("outputs = %q", outputs)
	}
}

func TestRunToolCallLimit(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("m", 10, 0, 5, fnCall(fmt.Sprintf("c%d", n), "lookup", `{}`))
	})
	var calls []string
	var mu sync.Mutex
	rec := &recorder{}
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", Tools: []airuntime.Tool{lookupTool(&calls, &mu)}, OutputSchema: answerSchema, MaxToolCalls: 1,
	}, rec.sink)
	runErrCode(t, err, "tool_call_limit", airuntime.ErrBudget)
	if len(calls) != 1 {
		t.Fatalf("handler ran %d times, want 1", len(calls))
	}
	if got := f.count("/responses"); got != 6 {
		t.Fatalf("requests = %d, want 6 (1 allowed + 5 refused)", got)
	}
	out := f.body(2)["input"].([]any)
	if last := out[len(out)-1].(map[string]any)["output"].(string); !strings.Contains(last, "limit") {
		t.Fatalf("refusal output = %q", last)
	}
}

func TestRunParallelCallsPastLimitStillReachModel(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			var items []string
			for i := 0; i < 8; i++ {
				items = append(items, fnCall(fmt.Sprintf("c%d", i), "lookup", `{}`))
			}
			return 200, response("m", 10, 0, 5, items...)
		}
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	var calls []string
	var mu sync.Mutex
	res, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", Tools: []airuntime.Tool{lookupTool(&calls, &mu)}, OutputSchema: answerSchema, MaxToolCalls: 3,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != `{"answer":"ok"}` || len(calls) != 3 || f.count("/responses") != 2 {
		t.Fatalf("result %+v, handler runs %d, requests %d", res, len(calls), f.count("/responses"))
	}
	input := f.body(1)["input"].([]any)
	refusals := 0
	for _, it := range input {
		if m := it.(map[string]any); m["type"] == "function_call_output" && strings.Contains(m["output"].(string), "limit") {
			refusals++
		}
	}
	if refusals != 5 {
		t.Fatalf("refusals sent back = %d, want 5", refusals)
	}
}

func TestRunUnknownToolPastLimitCounts(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("m", 10, 0, 5, fnCall(fmt.Sprintf("c%d", n), "nope", `{}`))
	})
	var calls []string
	var mu sync.Mutex
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", Tools: []airuntime.Tool{lookupTool(&calls, &mu)}, OutputSchema: answerSchema, MaxToolCalls: 1,
	}, nil)
	runErrCode(t, err, "tool_call_limit", airuntime.ErrBudget)
	if got := f.count("/responses"); got != 6 {
		t.Fatalf("requests = %d, want 6 (1 within the limit + 5 refused)", got)
	}
}

func TestRunLongToolOutputIsTruncated(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			return 200, response("m", 10, 0, 5, fnCall("c1", "big", `{}`))
		}
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	tool := airuntime.Tool{Name: "big", Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
		return airuntime.ToolOutput{Text: strings.Repeat("가", airuntime.MaxToolOutputBytes)}, nil
	}}
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", Tools: []airuntime.Tool{tool}, OutputSchema: answerSchema,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	input := f.body(1)["input"].([]any)
	out := input[len(input)-1].(map[string]any)["output"].(string)
	if len(out) > airuntime.MaxToolOutputBytes+len(airuntime.TruncatedMark) || !strings.HasSuffix(out, airuntime.TruncatedMark) || !utf8.ValidString(out) {
		t.Fatalf("output is %d bytes, marked %v, valid UTF-8 %v", len(out), strings.HasSuffix(out, airuntime.TruncatedMark), utf8.ValidString(out))
	}
}

func TestRunTokenBudget(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("m", 600, 0, 100, fnCall(fmt.Sprintf("c%d", n), "lookup", `{}`))
	})
	var calls []string
	var mu sync.Mutex
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", Tools: []airuntime.Tool{lookupTool(&calls, &mu)}, OutputSchema: answerSchema, MaxTotalTokens: 1000,
	}, nil)
	runErrCode(t, err, "budget_exceeded", airuntime.ErrBudget)
	if f.count("/responses") != 2 {
		t.Fatalf("requests = %d, want 2", f.count("/responses"))
	}
}

func blockingFake(t *testing.T) *fakeAPI {
	return newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return 500, `{}`
	})
}

func TestRunWallClock(t *testing.T) {
	f := blockingFake(t)
	start := time.Now()
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", OutputSchema: answerSchema, WallClock: 50 * time.Millisecond,
	}, nil)
	runErrCode(t, err, "time_limit", airuntime.ErrTimeLimit)
	if time.Since(start) > 2*time.Second {
		t.Fatal("Run did not stop at the wall clock")
	}
}

func TestRunCanceled(t *testing.T) {
	f := blockingFake(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(ctx, airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema, WallClock: time.Minute}, nil)
	runErrCode(t, err, "canceled", airuntime.ErrCanceled)
}

func TestRunCanceledDuringTool(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("m", 10, 0, 5, fnCall("c1", "hang", `{}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	tools := []airuntime.Tool{{Name: "hang", Handler: func(hctx context.Context, _ json.RawMessage) (airuntime.ToolOutput, error) {
		cancel()
		<-hctx.Done()
		return airuntime.ToolOutput{}, hctx.Err()
	}}}
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(ctx, airuntime.RunRequest{Prompt: "p", Tools: tools, OutputSchema: answerSchema}, nil)
	runErrCode(t, err, "canceled", airuntime.ErrCanceled)
	if f.count("/responses") != 1 {
		t.Fatalf("requests = %d, want 1", f.count("/responses"))
	}
}

func TestRunSchemaViolation(t *testing.T) {
	for name, final := range map[string]string{
		"too long":    `{"answer":"much longer than ten"}`,
		"not json":    `here is your report`,
		"extra field": `{"answer":"ok","x":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
				return 200, response("m", 10, 0, 5, message(final))
			})
			_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
			runErrCode(t, err, "schema_violation", airuntime.ErrProtocol)
		})
	}
}

func TestRunUnexpectedOutputItem(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("m", 10, 0, 5, `{"type":"web_search_call","id":"ws_1","status":"completed"}`)
	})
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
	runErrCode(t, err, "unexpected_tool", airuntime.ErrUnexpectedTool)
}

func TestRunRefusalIsProtocolError(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("m", 10, 0, 5, `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"refusal","refusal":"I can't help with that"}]}`)
	})
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
	runErrCode(t, err, "protocol_error", airuntime.ErrProtocol)
}

func TestRunTextBesideFunctionCallIsNotTheAnswer(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			return 200, response("m", 10, 0, 5, message("let me look"), fnCall("c1", "lookup", `{}`))
		}
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	var calls []string
	var mu sync.Mutex
	res, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", Tools: []airuntime.Tool{lookupTool(&calls, &mu)}, OutputSchema: answerSchema,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != `{"answer":"ok"}` || len(calls) != 1 {
		t.Fatalf("result %+v, handler runs %d", res, len(calls))
	}
	input := f.body(1)["input"].([]any)
	if len(input) != 4 || input[1].(map[string]any)["type"] != "message" || input[3].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("second input = %v, want prompt, message, call and output", input)
	}
}

func TestRunNonObjectOutputItemIsProtocolError(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("m", 10, 0, 5, `"not an object"`)
	})
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
	runErrCode(t, err, "protocol_error", airuntime.ErrProtocol)
}

func TestRunIncompleteResponse(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"model":"m","output":[],"usage":{"input_tokens":5,"output_tokens":5}}`
	})
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
	runErrCode(t, err, "output_limit", airuntime.ErrBudget)
}

func TestRunProviderStringsStayOutOfMessages(t *testing.T) {
	cases := map[string]struct {
		body     string
		code     string
		sentinel error
	}{
		"unrequested item": {response("m", 10, 0, 5, `{"type":"PROVIDERTYPE_call","id":"x"}`), "unexpected_tool", airuntime.ErrUnexpectedTool},
		"content filter":   {`{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"model":"m","output":[]}`, "protocol_error", airuntime.ErrProtocol},
		"odd status":       {`{"status":"PROVIDERSTATUS","model":"m","output":[]}`, "protocol_error", airuntime.ErrProtocol},
		"failed":           {`{"status":"failed","error":{"code":"server_error","message":"PROVIDERMESSAGE"},"model":"m","output":[]}`, "turn_failed", airuntime.ErrUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(*http.Request, int, map[string]any) (int, string) { return 200, tc.body })
			logs := &bytes.Buffer{}
			_, err := newTestRuntime(f.srv.URL, testKey, logs).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
			runErrCode(t, err, tc.code, tc.sentinel)
			if strings.Contains(err.Error(), "PROVIDER") || strings.Contains(err.Error(), "content_filter") {
				t.Fatalf("user message carries a provider string: %v", err)
			}
			if logs.Len() == 0 {
				t.Fatal("the provider detail was not logged")
			}
		})
	}
}

func TestRunHTTPErrorMapping(t *testing.T) {
	cases := []struct {
		status   int
		code     string
		sentinel error
	}{
		{401, "auth_failed", airuntime.ErrNotLoggedIn},
		{403, "permission_denied", airuntime.ErrUnavailable},
		{429, "rate_limited", airuntime.ErrUnavailable},
		{500, "provider_unavailable", airuntime.ErrUnavailable},
		{503, "provider_unavailable", airuntime.ErrUnavailable},
		{400, "invalid_request", airuntime.ErrProtocol},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
				return tc.status, `{"error":{"message":"provider detail RAWBODY","type":"x","code":"y"}}`
			})
			// A strict schema: a 400 that does not name text.format is still
			// not retried.
			_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
			runErrCode(t, err, tc.code, tc.sentinel)
			if got := f.count("/responses"); got != 1 {
				t.Fatalf("requests = %d, want 1", got)
			}
			if strings.Contains(err.Error(), "RAWBODY") {
				t.Fatalf("user message carries the response body: %v", err)
			}
		})
	}
}

func TestRunNetworkErrorIsUnavailable(t *testing.T) {
	f := newFake(t, func(*http.Request, int, map[string]any) (int, string) { return 200, "" })
	f.srv.Close()
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
	runErrCode(t, err, "provider_unavailable", airuntime.ErrUnavailable)
}

func TestRunStoreIsFalseInEveryRequest(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			return 200, response("m", 10, 0, 5, fnCall("c", "lookup", `{}`))
		}
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	var calls []string
	var mu sync.Mutex
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", Tools: []airuntime.Tool{lookupTool(&calls, &mu)}, OutputSchema: answerSchema,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Every request must have store: false
	for i := 0; i < f.count("/responses"); i++ {
		if body := f.body(i); body["store"] != false {
			t.Errorf("request %d: store = %v, want false", i, body["store"])
		}
	}
}

func TestRunReasoningItemsReplayedInNextInput(t *testing.T) {
	// Simulate API that returns reasoning in the first response
	reasoningJSON := `{"type":"reasoning","id":"r_1","reasoning":"thinking hard...","status":"completed"}`
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			// First response: reasoning item + function call
			return 200, response("m", 10, 0, 5,
				reasoningJSON,
				fnCall("c", "lookup", `{}`))
		}
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	var calls []string
	var mu sync.Mutex
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", Tools: []airuntime.Tool{lookupTool(&calls, &mu)}, OutputSchema: answerSchema,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The second request's input should contain the reasoning item from the first response
	secondInput := f.body(1)["input"].([]any)
	if len(secondInput) < 3 {
		t.Fatalf("second input = %d items, want at least 3 (user prompt + reasoning + function_call_output)", len(secondInput))
	}
	// Check that reasoning item is in the input
	found := false
	for _, item := range secondInput {
		raw, _ := json.Marshal(item)
		if strings.Contains(string(raw), "reasoning") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("reasoning item not found in second input: %v", secondInput)
	}
}

func TestRunReasoningEffortOnlyForSupportingModels(t *testing.T) {
	// gpt-6-astra doesn't support "none" effort; it should not be sent
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			return 200, response("gpt-6-astra", 10, 0, 5, message(`{"answer":"ok"}`))
		}
		return 200, response("gpt-6-astra", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	r := New(Config{APIKey: func(context.Context) (string, error) { return testKey, nil }, BaseURL: f.srv.URL})
	// Override catalog to test the gpt-6-astra behavior
	req := airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema, ReasoningEffort: "none"}
	req.Model = "gpt-6-astra"
	_, err := r.Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// gpt-6-astra doesn't support "none", so reasoning.effort should not be in the request
	if reasoning := f.body(0)["reasoning"]; reasoning != nil {
		t.Errorf("gpt-6-astra should not have reasoning effort for 'none'; got %v", reasoning)
	}

	// gpt-5.6-luna supports "none" effort; it should be sent
	f2 := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("gpt-5.6-luna", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	r2 := New(Config{APIKey: func(context.Context) (string, error) { return testKey, nil }, BaseURL: f2.srv.URL})
	req.Model = "gpt-5.6-luna"
	_, err = r2.Run(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// gpt-5.6-luna supports "none", so reasoning.effort should be in the request
	reasoning := f2.body(0)["reasoning"]
	if reasoning == nil || reasoning.(map[string]any)["effort"] != "none" {
		t.Errorf("gpt-5.6-luna should have reasoning.effort='none'; got %v", reasoning)
	}
}

func TestRunKeyNeverLoggedOrReturned(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 401, fmt.Sprintf(`{"error":{"message":"Incorrect API key provided: %s. Also sk-proj-abc***xyz.","type":"invalid_request_error","code":"invalid_api_key"}}`, testKey)
	})
	logs := &bytes.Buffer{}
	_, err := newTestRuntime(f.srv.URL, testKey, logs).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
	runErrCode(t, err, "auth_failed", airuntime.ErrNotLoggedIn)
	if logs.Len() == 0 {
		t.Fatal("the failure was not logged")
	}
	for _, s := range []string{logs.String(), err.Error()} {
		if strings.Contains(s, "SECRET") || strings.Contains(s, "sk-proj-abc") {
			t.Fatalf("key leaked: %s", s)
		}
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection reset while sending " + testKey)
}

func TestRunTransportErrorIsRedacted(t *testing.T) {
	logs := &bytes.Buffer{}
	rt := New(Config{
		APIKey:     func(context.Context) (string, error) { return testKey, nil },
		BaseURL:    "http://api.invalid",
		HTTPClient: &http.Client{Transport: failingTransport{}},
		Logger:     slog.New(slog.NewTextHandler(logs, nil)),
	})
	_, err := rt.Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
	runErrCode(t, err, "provider_unavailable", airuntime.ErrUnavailable)
	if logs.Len() == 0 {
		t.Fatal("the transport error was not logged")
	}
	for _, s := range []string{logs.String(), err.Error()} {
		if strings.Contains(s, "SECRET") {
			t.Fatalf("key leaked: %s", s)
		}
	}
}

func TestRunNotConfigured(t *testing.T) {
	f := newFake(t, func(*http.Request, int, map[string]any) (int, string) { return 200, "" })
	_, err := newTestRuntime(f.srv.URL, "", nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p"}, nil)
	runErrCode(t, err, "not_configured", airuntime.ErrNotConfigured)
	rt := New(Config{APIKey: func(context.Context) (string, error) { return "", errors.New("db down " + testKey) }, BaseURL: f.srv.URL})
	_, err = rt.Run(context.Background(), airuntime.RunRequest{Prompt: "p"}, nil)
	runErrCode(t, err, "runtime_unavailable", airuntime.ErrUnavailable)
	if f.count("/responses") != 0 {
		t.Fatal("request sent without a key")
	}
}

func TestRunStrictOffForIncompatibleSchema(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	schema := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}}}`)
	if _, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: schema}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if format := f.body(0)["text"].(map[string]any)["format"].(map[string]any); format["strict"] != false {
		t.Fatalf("strict = %v, want false", format["strict"])
	}
}

func TestRunStrictRejectedRetriesWithoutStrict(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if body["text"].(map[string]any)["format"].(map[string]any)["strict"] == true {
			return 400, `{"error":{"message":"Invalid schema: maxLength is not permitted","type":"invalid_request_error","param":"text.format.schema"}}`
		}
		return 200, response("m", 10, 0, 5, message(`{"answer":"ok"}`))
	})
	res, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{Prompt: "p", OutputSchema: answerSchema}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != `{"answer":"ok"}` || f.count("/responses") != 2 {
		t.Fatalf("result %+v after %d requests", res, f.count("/responses"))
	}
}

func TestRunUnrelated400_NotRetriedWithoutStrict(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 400, `{"error":{"message":"Unsupported value: 'none' is not supported with this model.","type":"invalid_request_error","param":"reasoning.effort","code":"unsupported_value"}}`
	})
	_, err := newTestRuntime(f.srv.URL, testKey, nil).Run(context.Background(), airuntime.RunRequest{
		Prompt: "p", OutputSchema: answerSchema, ReasoningEffort: "none",
	}, nil)
	runErrCode(t, err, "invalid_request", airuntime.ErrProtocol)
	if got := f.count("/responses"); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestLoggedBodyRedactsBeforeCuttingOnRuneBoundary(t *testing.T) {
	// A cut first would leave "sk-pr", too short for the key pattern.
	if got := loggedBody([]byte(strings.Repeat("x", maxLoggedBody-5)+testKey), testKey); strings.Contains(got, "sk-pr") {
		t.Fatalf("key prefix survived the cut: %q", got[len(got)-20:])
	}
	if got := loggedBody([]byte("x"+strings.Repeat("가", maxLoggedBody)), testKey); !utf8.ValidString(got) || len(got) > maxLoggedBody {
		t.Fatalf("logged body is %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}

func TestInfo(t *testing.T) {
	info := New(Config{}).Info()
	if info.Key != "openai-api" || info.Model != "gpt-5.6-terra" || info.AuthMode != airuntime.AuthModeAPIKey {
		t.Fatalf("info = %+v", info)
	}
}

// Asking for the answer schema before any tool has run puts the model in a
// bind and it takes the cheaper way out: on the chat/completions path
// z-ai/glm-5.3 answered "I will look up the segments and then write", called
// nothing, and the run finished empty because that sentence fit the schema.
// This runtime attached the schema from the first request and forced nothing,
// so it was one bad turn away from the same ending.
func TestSchemaIsWithheldUntilAToolHasRun(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if n == 0 {
			return 200, response("m", 1, 0, 1, fnCall("call_1", "lookup", `{"q":"a"}`))
		}
		return 200, response("m", 1, 0, 1, message(`{"answer":"ok"}`))
	})
	rt := newTestRuntime(f.srv.URL, "k", nil)

	var calls []string
	var mu sync.Mutex
	req := airuntime.RunRequest{
		Prompt:       "test",
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["answer"],"properties":{"answer":{"type":"string"}}}`),
		Tools:        []airuntime.Tool{lookupTool(&calls, &mu)},
	}
	if _, err := rt.Run(context.Background(), req, func(airuntime.Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := f.body(0)
	if _, ok := first["text"]; ok {
		t.Error("the answer schema went out before any tool had run")
	}
	if first["tool_choice"] != "required" {
		t.Errorf("first request tool_choice = %v, want required", first["tool_choice"])
	}

	second := f.body(1)
	if _, ok := second["text"]; !ok {
		t.Error("the answer schema was still withheld after a tool ran")
	}
	if _, ok := second["tool_choice"]; ok {
		t.Error("tool calls stayed forced after a tool ran; the turn could never end")
	}
}

// With no tools there is nothing to wait for.
func TestSchemaGoesOutAtOnceWithoutTools(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		return 200, response("m", 1, 0, 1, message(`{"answer":"ok"}`))
	})
	rt := newTestRuntime(f.srv.URL, "k", nil)

	req := airuntime.RunRequest{Prompt: "test", OutputSchema: json.RawMessage(`{"type":"object"}`)}
	if _, err := rt.Run(context.Background(), req, func(airuntime.Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := f.body(0)["text"]; !ok {
		t.Error("the schema was withheld although there were no tools to wait for")
	}
	if _, ok := f.body(0)["tool_choice"]; ok {
		t.Error("forced a tool call with no tools to call")
	}
}
