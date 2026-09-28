package chatruntime

import (
	"bytes"
	"context"
	"encoding/json"
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

const testKey = "test-api-key"

var answerSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["answer"],"properties":{"answer":{"type":"string","maxLength":10}}}`)

// fakeAPI is a chat/completions API stand-in.
type fakeAPI struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	auth   []string
	calls  map[string]int
	// respond(path, requestNumber, decodedBody) -> (statusCode, responseBody)
	respond func(path string, n int, body map[string]any) (int, string)
}

func newFake(t *testing.T, respond func(path string, n int, body map[string]any) (int, string)) *fakeAPI {
	t.Helper()
	f := &fakeAPI{respond: respond, calls: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		n := f.calls[r.URL.Path]
		f.calls[r.URL.Path]++
		if r.URL.Path == "/v1/chat/completions" {
			f.bodies = append(f.bodies, body)
		}
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		status, out := f.respond(r.URL.Path, n, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// Providers document their endpoint as ".../v1" and the request paths add /v1
// themselves. A base URL given that way used to produce /v1/v1/chat/completions,
// which every provider answers with a 404 -- reported as a missing model, so the
// address was the last thing anyone suspected.
func TestBaseURL_V1SuffixIsNotDoubled(t *testing.T) {
	f := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, completion("test-model", "done")
	})
	rt := newTestRuntime(t, f.srv.URL+"/v1", "k", "nvidia", nil)

	_, err := rt.Run(context.Background(), airuntime.RunRequest{Prompt: "test"}, func(airuntime.Event) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.count("/v1/chat/completions"); got != 1 {
		t.Errorf("calls to /v1/chat/completions = %d, want 1 (path was doubled)", got)
	}
	if got := f.count("/v1/v1/chat/completions"); got != 0 {
		t.Errorf("base URL's /v1 was appended twice: %d calls to /v1/v1/chat/completions", got)
	}
}

// The instructions hold the report's rules and the demand to use the tools. A
// request that carries only the prompt leaves the model with a schema and no
// task: it answers with a plan, calls nothing, and the run completes empty
// because the schema was satisfied.
func TestInstructionsAreSentAsSystemMessage(t *testing.T) {
	f := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, completion("test-model", "done")
	})
	rt := newTestRuntime(t, f.srv.URL, "k", "nvidia", nil)

	req := airuntime.RunRequest{Instructions: "read every segment with the tools", Prompt: "weekly report"}
	if _, err := rt.Run(context.Background(), req, func(airuntime.Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	msgs, _ := f.body(0)["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v, want system then user", msgs)
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "read every segment with the tools" {
		t.Errorf("instructions not sent as the system message: %v", first)
	}
	second, _ := msgs[1].(map[string]any)
	if second["role"] != "user" || second["content"] != "weekly report" {
		t.Errorf("prompt not sent as the user message: %v", second)
	}
}

// Tools and the answer schema must not be demanded in the same breath. Asked
// for both, z-ai/glm-5.3 wrote "I will look up the segments and then write",
// called nothing, and the run completed empty because that sentence satisfied
// the schema. Tools come first, forced; the schema is asked for only once the
// model stops calling them.
func TestSchemaIsWithheldUntilToolsAreDone(t *testing.T) {
	step := 0
	f := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		if step == 0 {
			step++
			return 200, toolCallResponse("search", `{"query":"x"}`)
		}
		return 200, completion("test-model", `{"answer":"ok"}`)
	})
	rt := newTestRuntime(t, f.srv.URL, "k", "nvidia", nil)

	req := airuntime.RunRequest{
		Prompt:       "weekly report",
		OutputSchema: answerSchema,
		Tools: []airuntime.Tool{{
			Name: "search", Description: "d",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
				return airuntime.ToolOutput{Text: "result"}, nil
			},
		}},
	}
	if _, err := rt.Run(context.Background(), req, func(airuntime.Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	first := f.body(0)
	if first["tool_choice"] != "required" {
		t.Errorf("first request did not force a tool call: tool_choice=%v", first["tool_choice"])
	}
	if _, ok := first["response_format"]; ok {
		t.Error("first request asked for the answer schema while tools were still offered")
	}

	last := f.body(f.count("/v1/chat/completions") - 1)
	if _, ok := last["response_format"]; !ok {
		t.Error("final request did not ask for the answer schema")
	}
	if _, ok := last["tool_choice"]; ok {
		t.Error("final request still forced a tool call, so the turn could never end")
	}
}

func (f *fakeAPI) body(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

func (f *fakeAPI) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

func newTestRuntime(t *testing.T, url, key, provider string, logs *bytes.Buffer) *Runtime {
	t.Helper()
	if logs == nil {
		logs = &bytes.Buffer{}
	}
	return New(Config{
		APIKey:       func(context.Context) (string, error) { return key, nil },
		BaseURL:      func(context.Context) (string, error) { return url, nil },
		ProviderName: provider,
		DefaultModel: "test-model",
		Logger:       slog.New(slog.NewTextHandler(logs, nil)),
	})
}

// completion returns a minimal successful response.
func completion(model string, content string) string {
	contentJSON, _ := json.Marshal(content)
	return `{
		"id": "chatcmpl-test",
		"object": "chat.completion",
		"created": 1234567890,
		"model": "` + model + `",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": ` + string(contentJSON) + `
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5,
			"prompt_tokens_details": {
				"cached_tokens": 0
			}
		}
	}`
}

// toolCallResponse returns a response with a tool_call.
func toolCallResponse(name, args string) string {
	argsJSON, _ := json.Marshal(args)
	return `{
		"id": "chatcmpl-test",
		"object": "chat.completion",
		"created": 1234567890,
		"model": "test-model",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_` + name + `",
					"type": "function",
					"function": {
						"name": "` + name + `",
						"arguments": ` + string(argsJSON) + `
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5
		}
	}`
}

// toolCallResponseEmptyContent mirrors toolCallResponse but sends content as
// an explicit empty string, the way NVIDIA's vLLM gateway answers a
// tool-call turn (issue #700). toolCallResponse sends content: null, which
// already round-trips safely -- using it here would make this test pass
// without exercising the bug.
func toolCallResponseEmptyContent(name, args string) string {
	argsJSON, _ := json.Marshal(args)
	return `{
		"id": "chatcmpl-test",
		"object": "chat.completion",
		"created": 1234567890,
		"model": "test-model",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [{
					"id": "call_` + name + `",
					"type": "function",
					"function": {
						"name": "` + name + `",
						"arguments": ` + string(argsJSON) + `
					}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5
		}
	}`
}

// A vLLM gateway rejects an assistant message whose content is an explicit
// empty string on a tool-call turn: "Empty content is not allowed for
// assistant messages" (400). The gateway itself answers a tool-call turn
// with content: "" (issue #700), so replaying that string verbatim on the
// next request breaks every tool-call turn past the first one over that
// provider. content: null and an omitted key are both accepted.
func TestEmptyAssistantContentIsNotEchoedBack(t *testing.T) {
	step := 0
	f := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		if step == 0 {
			step++
			return 200, toolCallResponseEmptyContent("search", `{"query":"x"}`)
		}
		step++
		return 200, completion("test-model", "done")
	})
	rt := newTestRuntime(t, f.srv.URL, "k", "nvidia", nil)

	req := airuntime.RunRequest{
		Prompt: "test",
		Tools: []airuntime.Tool{{
			Name: "search", Description: "d",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
				return airuntime.ToolOutput{Text: "result"}, nil
			},
		}},
	}
	if _, err := rt.Run(context.Background(), req, func(airuntime.Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	second := f.body(1)
	msgs, _ := second["messages"].([]any)
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		if content, exists := msg["content"]; exists && content == "" {
			t.Fatalf("assistant message replayed an empty string content, which a vLLM gateway rejects with 400: %v", msg)
		}
	}
}

func TestBasicCompletion(t *testing.T) {
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		if n != 0 {
			t.Fatalf("expected 1 request, got %d", n+1)
		}
		return 200, completion("test-model", "Hello, world!")
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	ctx := context.Background()
	req := airuntime.RunRequest{
		Model:  "test-model",
		Prompt: "Say hello",
	}
	var events []airuntime.Event
	result, err := rt.Run(ctx, req, func(ev airuntime.Event) {
		events = append(events, ev)
	})

	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.FinalText != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got %q", result.FinalText)
	}
	if result.Model != "test-model" {
		t.Errorf("expected model 'test-model', got %q", result.Model)
	}
	if !result.Usage.Reported {
		t.Error("usage not reported")
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 {
		t.Errorf("usage mismatch: in=%d, out=%d", result.Usage.InputTokens, result.Usage.OutputTokens)
	}
}

func TestToolCallRoundtrip(t *testing.T) {
	logs := &bytes.Buffer{}
	step := 0
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		if path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", path)
		}
		switch step {
		case 0:
			step++
			return 200, toolCallResponse("search", `{"query":"hello"}`)
		case 1:
			step++
			// Verify tool result was added to messages
			msgs := body["messages"].([]any)
			found := false
			for _, m := range msgs {
				msg := m.(map[string]any)
				if msg["role"] == "tool" {
					found = true
					break
				}
			}
			if !found {
				t.Fatal("tool result message not found in second request")
			}
			return 200, completion("test-model", "search result: hello")
		default:
			t.Fatalf("unexpected request %d", step)
		}
		return 500, `{"error":"unexpected"}`
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", logs)

	toolRun := false
	ctx := context.Background()
	req := airuntime.RunRequest{
		Model:  "test-model",
		Prompt: "Search for hello",
		Tools: []airuntime.Tool{
			{
				Name:        "search",
				Description: "search tool",
				InputSchema: json.RawMessage(`{"type":"object"}`),
				Handler: func(ctx context.Context, args json.RawMessage) (airuntime.ToolOutput, error) {
					toolRun = true
					return airuntime.ToolOutput{Text: "hello world"}, nil
				},
			},
		},
	}
	result, err := rt.Run(ctx, req, func(ev airuntime.Event) {})

	if err != nil {
		t.Logf("Logs:\n%s", logs.String())
		t.Fatalf("Run failed: %v", err)
	}
	if !toolRun {
		t.Error("tool was not executed")
	}
	if result.FinalText != "search result: hello" {
		t.Errorf("expected 'search result: hello', got %q", result.FinalText)
	}
}

func TestMissingAPIKey(t *testing.T) {
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, completion("test-model", "ok")
	})
	rt := newTestRuntime(t, fake.srv.URL, "", "test", nil)

	ctx := context.Background()
	result, err := rt.Run(ctx, airuntime.RunRequest{Prompt: "test"}, func(airuntime.Event) {})

	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "not_configured") {
		t.Errorf("expected 'not_configured', got %v", err)
	}
	if result != nil {
		t.Error("expected nil result on error")
	}
}

func TestAPIKeyNotExposed(t *testing.T) {
	logs := &bytes.Buffer{}
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		// Trigger an error to generate logs
		return 500, `{"error":{"message":"Server error"}}`
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", logs)

	ctx := context.Background()
	_, _ = rt.Run(ctx, airuntime.RunRequest{Prompt: "test"}, func(airuntime.Event) {})

	// Check logs don't contain the API key
	logContent := logs.String()
	if strings.Contains(logContent, testKey) {
		t.Errorf("API key leaked in logs: %s", logContent)
	}
}

func TestInvalidJSONResponse(t *testing.T) {
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, `{invalid json}`
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	ctx := context.Background()
	result, err := rt.Run(ctx, airuntime.RunRequest{Prompt: "test"}, func(airuntime.Event) {})

	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if result != nil {
		t.Error("expected nil result on error")
	}
}

func TestHTTPErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantCode string
	}{
		{
			name:     "401 Unauthorized",
			status:   401,
			body:     `{"error":{"message":"Unauthorized"}}`,
			wantCode: "auth_failed",
		},
		{
			name:     "404 Not found",
			status:   404,
			body:     `{"error":{"message":"Not found for account"}}`,
			wantCode: "model_unavailable",
		},
		{
			name:     "429 Rate limit",
			status:   429,
			body:     `{"error":{"message":"Rate limit exceeded"}}`,
			wantCode: "rate_limited",
		},
		{
			name:     "500 Server error",
			status:   500,
			body:     `{"error":{"message":"Server error"}}`,
			wantCode: "provider_unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
				return tc.status, tc.body
			})
			rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

			ctx := context.Background()
			_, err := rt.Run(ctx, airuntime.RunRequest{Prompt: "test"}, func(airuntime.Event) {})

			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantCode) {
				t.Errorf("expected error code %q, got %v", tc.wantCode, err)
			}
		})
	}
}

func TestWallClock(t *testing.T) {
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		time.Sleep(200 * time.Millisecond)
		return 200, completion("test-model", "ok")
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	ctx := context.Background()
	req := airuntime.RunRequest{
		Prompt:    "test",
		WallClock: 100 * time.Millisecond,
	}
	_, err := rt.Run(ctx, req, func(airuntime.Event) {})

	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "time_limit") {
		t.Errorf("expected 'time_limit', got %v", err)
	}
}

func TestStructuredOutput(t *testing.T) {
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		// First response with valid JSON matching schema
		if n == 0 {
			return 200, completion("test-model", `{"answer":"yes"}`)
		}
		return 500, `{}`
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	ctx := context.Background()
	req := airuntime.RunRequest{
		Prompt:       "Is this a test?",
		OutputSchema: answerSchema,
	}
	result, err := rt.Run(ctx, req, func(airuntime.Event) {})

	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.FinalText != `{"answer":"yes"}` {
		t.Errorf("expected JSON response, got %q", result.FinalText)
	}
}

func TestStructuredOutputRetry(t *testing.T) {
	step := 0
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		// First request: invalid JSON
		if n == 0 {
			step++
			return 200, completion("test-model", `not valid json`)
		}
		// Second request: valid JSON (retry)
		step++
		return 200, completion("test-model", `{"answer":"yes"}`)
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	ctx := context.Background()
	req := airuntime.RunRequest{
		Prompt:       "Is this a test?",
		OutputSchema: answerSchema,
	}
	result, err := rt.Run(ctx, req, func(airuntime.Event) {})

	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.FinalText != `{"answer":"yes"}` {
		t.Errorf("expected JSON response after retry, got %q", result.FinalText)
	}
	if fake.count("/v1/chat/completions") != 2 {
		t.Errorf("expected 2 requests, got %d", fake.count("/v1/chat/completions"))
	}
}

func TestStructuredOutputRetryFailure(t *testing.T) {
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		// Always return invalid JSON
		return 200, completion("test-model", `not valid json`)
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	ctx := context.Background()
	req := airuntime.RunRequest{
		Prompt:       "Is this a test?",
		OutputSchema: answerSchema,
	}
	_, err := rt.Run(ctx, req, func(airuntime.Event) {})

	if err == nil {
		t.Fatal("expected error for invalid schema output")
	}
	if !strings.Contains(err.Error(), "protocol_error") && !strings.Contains(err.Error(), "schema") {
		t.Errorf("expected schema error, got %v", err)
	}
	// Should have tried at most 2 times (initial + 1 retry)
	if fake.count("/v1/chat/completions") > 2 {
		t.Errorf("expected at most 2 requests, got %d", fake.count("/v1/chat/completions"))
	}
}

func TestUsageMapping(t *testing.T) {
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, `{
			"id": "chatcmpl-test",
			"object": "chat.completion",
			"created": 1234567890,
			"model": "test-model",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "ok"
				},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 50,
				"prompt_tokens_details": {
					"cached_tokens": 20
				}
			}
		}`
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	ctx := context.Background()
	result, err := rt.Run(ctx, airuntime.RunRequest{Prompt: "test"}, func(airuntime.Event) {})

	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Usage.InputTokens != 100 {
		t.Errorf("expected 100 input tokens, got %d", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 50 {
		t.Errorf("expected 50 output tokens, got %d", result.Usage.OutputTokens)
	}
	if result.Usage.CachedInputTokens != 20 {
		t.Errorf("expected 20 cached tokens, got %d", result.Usage.CachedInputTokens)
	}
}

func TestMaxToolCalls(t *testing.T) {
	// Return tool calls repeatedly so we exceed MaxToolCalls and maxCallsPastLimit
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		// Keep returning tool calls until we've made enough requests
		if n < 10 {
			return 200, toolCallResponse("search", `{}`)
		}
		return 200, completion("test-model", "final answer")
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	ctx := context.Background()
	req := airuntime.RunRequest{
		Prompt:       "test",
		MaxToolCalls: 1,
		Tools: []airuntime.Tool{
			{
				Name:        "search",
				Description: "search",
				InputSchema: json.RawMessage(`{"type":"object"}`),
				Handler: func(ctx context.Context, args json.RawMessage) (airuntime.ToolOutput, error) {
					return airuntime.ToolOutput{Text: "result"}, nil
				},
			},
		},
	}
	_, err := rt.Run(ctx, req, func(airuntime.Event) {})

	if err == nil {
		t.Fatal("expected error for exceeding MaxToolCalls")
	}
	if !strings.Contains(err.Error(), "tool_call_limit") {
		t.Errorf("expected 'tool_call_limit', got %v", err)
	}
}

// completionWithFinish is completion() with the finish reason as a parameter.
func completionWithFinish(model, content, finish string) string {
	contentJSON, _ := json.Marshal(content)
	return `{
		"id": "chatcmpl-test",
		"object": "chat.completion",
		"created": 1234567890,
		"model": "` + model + `",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": ` + string(contentJSON) + `
			},
			"finish_reason": "` + finish + `"
		}]
	}`
}

// answerSchema caps "answer" at ten characters, so a longer one breaks the
// schema while staying valid JSON. Every structured-output test above used
// `not valid json`, which json.Unmarshal rejects on its own -- which is why
// they all passed against a validateJSON that parsed both sides and compared
// neither. On the NVIDIA and LiteLLM paths that made schema violations
// undetectable in principle.
func TestSchemaViolationIsDetected(t *testing.T) {
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, completion("test-model", `{"answer":"far longer than ten characters"}`)
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	_, err := rt.Run(context.Background(), airuntime.RunRequest{Prompt: "test", OutputSchema: answerSchema}, func(airuntime.Event) {})

	if err == nil {
		t.Fatal("a schema-violating answer was accepted as the final answer")
	}
	if !strings.Contains(err.Error(), "schema_violation") {
		t.Errorf("err = %v, want schema_violation", err)
	}
}

// finish_reason "length" means the model hit the output token limit mid-answer.
// What came back still looks like a final answer, so without reading the field
// the turn ends normally and a half-written report is stored as a finished one.
// openairuntime catches this through response.status and clauderuntime through
// stop_reason; this runtime parsed finish_reason and never read it.
func TestTruncatedAnswerIsNotAcceptedAsFinal(t *testing.T) {
	fake := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		return 200, completionWithFinish("test-model", `{"answer":"ok"}`, "length")
	})
	rt := newTestRuntime(t, fake.srv.URL, testKey, "test", nil)

	_, err := rt.Run(context.Background(), airuntime.RunRequest{Prompt: "test", OutputSchema: answerSchema}, func(airuntime.Event) {})

	if err == nil {
		t.Fatal("a truncated response was accepted as the final answer")
	}
	if !strings.Contains(err.Error(), "output_limit") {
		t.Errorf("err = %v, want output_limit", err)
	}
}

// Every request resends the whole conversation, so an untruncated tool output
// is paid for again on each later turn. openairuntime and clauderuntime both
// cap it; this runtime sent the text as it came.
func TestLongToolOutputIsTruncated(t *testing.T) {
	f := newFake(t, func(path string, n int, body map[string]any) (int, string) {
		if n == 0 {
			return 200, toolCallResponse("search", `{}`)
		}
		return 200, completion("test-model", "done")
	})
	rt := newTestRuntime(t, f.srv.URL, testKey, "test", nil)

	req := airuntime.RunRequest{
		Prompt: "test",
		Tools: []airuntime.Tool{{
			Name:        "search",
			Description: "search",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Handler: func(ctx context.Context, args json.RawMessage) (airuntime.ToolOutput, error) {
				return airuntime.ToolOutput{Text: strings.Repeat("가", airuntime.MaxToolOutputBytes)}, nil
			},
		}},
	}
	if _, err := rt.Run(context.Background(), req, func(airuntime.Event) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	msgs, _ := f.body(1)["messages"].([]any)
	var out string
	for _, m := range msgs {
		if msg, ok := m.(map[string]any); ok && msg["role"] == "tool" {
			out, _ = msg["content"].(string)
		}
	}
	if out == "" {
		t.Fatalf("no tool message in the second request: %v", msgs)
	}
	if len(out) > airuntime.MaxToolOutputBytes+len(airuntime.TruncatedMark) || !strings.HasSuffix(out, airuntime.TruncatedMark) || !utf8.ValidString(out) {
		t.Fatalf("tool output is %d bytes, marked %v, valid UTF-8 %v", len(out), strings.HasSuffix(out, airuntime.TruncatedMark), utf8.ValidString(out))
	}
}
