package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

const testOutputSchema = `{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`

func echoTool(handler func(ctx context.Context, args json.RawMessage) (airuntime.ToolOutput, error)) airuntime.Tool {
	if handler == nil {
		handler = func(_ context.Context, args json.RawMessage) (airuntime.ToolOutput, error) {
			var in struct {
				Q string `json:"q"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return airuntime.ToolOutput{}, err
			}
			return airuntime.ToolOutput{Text: "echo:" + in.Q, Rows: 1, ArgsSummary: map[string]any{"q": in.Q}}, nil
		}
	}
	return airuntime.Tool{
		Name:        "echo",
		Description: "Echo the query back.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"],"additionalProperties":false}`),
		Handler:     handler,
	}
}

func baseRequest(tools ...airuntime.Tool) airuntime.RunRequest {
	return airuntime.RunRequest{
		Instructions: "You write weekly reports.",
		Prompt:       "Summarize the week.",
		Tools:        tools,
		OutputSchema: json.RawMessage(testOutputSchema),
		ToolTimeout:  5 * time.Second,
		WallClock:    20 * time.Second,
	}
}

// runtimeFor starts a fake behavior and returns a runtime aimed at it plus
// the capture file the child writes.
func runtimeFor(t *testing.T, behavior string, cfg RuntimeConfig) (airuntime.Runtime, string) {
	t.Helper()
	fakeServer(t, behavior)
	capture := filepath.Join(t.TempDir(), "capture")
	t.Setenv(fakeCaptureEnv, capture)
	if cfg.Home == "" {
		cfg.Home = t.TempDir()
	}
	return NewRuntime(cfg), capture
}

type sinkRecorder struct {
	mu     sync.Mutex
	events []airuntime.Event
	calls  chan struct{}
}

func newSink() *sinkRecorder { return &sinkRecorder{calls: make(chan struct{}, 16)} }

func (s *sinkRecorder) sink(e airuntime.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	if e.Kind == airuntime.EventToolCall {
		s.calls <- struct{}{}
	}
}

func (s *sinkRecorder) kinds(kind airuntime.EventKind) []airuntime.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []airuntime.Event
	for _, e := range s.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// captured returns the fake's header line and the client messages by method
// (responses, which have no method, under "").
func captured(t *testing.T, path string) (map[string]any, map[string][]map[string]any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var head map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("capture header: %v", err)
	}
	byMethod := map[string][]map[string]any{}
	for _, l := range lines[1:] {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("client wrote non-JSON %q", l)
		}
		method, _ := m["method"].(string)
		byMethod[method] = append(byMethod[method], m)
	}
	return head, byMethod
}

func params(t *testing.T, msgs []map[string]any, method string) map[string]any {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("%s sent %d times, want 1", method, len(msgs))
	}
	p, _ := msgs[0]["params"].(map[string]any)
	return p
}

func TestRunToolsHappyPath(t *testing.T) {
	rt, capture := runtimeFor(t, fakeToolsHappy, RuntimeConfig{Model: "gpt-test", Disable: []string{"apps"}})
	rec := newSink()

	res, err := rt.Run(context.Background(), baseRequest(echoTool(nil)), rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var final []fakeFinalTool
	if err := json.Unmarshal([]byte(res.FinalText), &final); err != nil {
		t.Fatalf("final text %q is not the final_answer item: %v", res.FinalText, err)
	}
	if len(final) != 2 || !final[0].Success || final[0].Text != "echo:alpha" || final[1].Text != "echo:beta" {
		t.Fatalf("tool answers = %+v", final)
	}
	want := airuntime.Usage{Reported: true, InputTokens: 100, CachedInputTokens: 40, OutputTokens: 20}
	if res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
	if res.Duration <= 0 {
		t.Error("duration not set")
	}
	// The configured model is a request; thread/start answers with the one used.
	if res.Model != "gpt-fake-default" {
		t.Errorf("model = %q, want the thread/start answer", res.Model)
	}

	calls, results := rec.kinds(airuntime.EventToolCall), rec.kinds(airuntime.EventToolResult)
	if len(calls) != 2 || len(results) != 2 {
		t.Fatalf("tool events = %d calls, %d results", len(calls), len(results))
	}
	seqs := map[int]bool{}
	for _, c := range calls {
		seqs[c.Seq] = true
		if c.Tool != "echo" || c.Args["q"] == nil {
			t.Errorf("tool_call = %+v", c)
		}
	}
	if !seqs[1] || !seqs[2] {
		t.Errorf("tool_call seqs = %v, want 1 and 2", seqs)
	}
	for _, r := range results {
		if r.Status != airuntime.ToolStatusOK || r.Rows != 1 || r.Args["q"] == nil {
			t.Errorf("tool_result = %+v", r)
		}
	}
	if u := rec.kinds(airuntime.EventUsage); len(u) != 1 || u[0].Usage == nil || u[0].Usage.OutputTokens != 20 {
		t.Errorf("usage events = %+v", u)
	}

	head, sent := captured(t, capture)
	argv, _ := head["argv"].([]any)
	// Built-in tools off (shell, exec, image, web search, sleep, image generation),
	// then the configured features.
	wantArgv := `[app-server --disable shell_tool --disable unified_exec --disable view_image --disable sleep_tool --disable image_generation -c web_search="disabled" --disable apps]`
	if got := fmt.Sprint(argv); got != wantArgv {
		t.Errorf("argv = %s, want %s", got, wantArgv)
	}
	init := params(t, sent["initialize"], "initialize")
	if caps, _ := init["capabilities"].(map[string]any); caps["experimentalApi"] != true {
		t.Errorf("initialize capabilities = %v, want experimentalApi", init["capabilities"])
	}
	if len(sent["initialized"]) != 1 {
		t.Errorf("initialized sent %d times", len(sent["initialized"]))
	}
	thread := params(t, sent["thread/start"], "thread/start")
	if thread["ephemeral"] != true {
		t.Errorf("thread/start ephemeral = %v", thread["ephemeral"])
	}
	// Empty environments: no shell, apply_patch or view_image for the thread.
	if envs, ok := thread["environments"].([]any); !ok || len(envs) != 0 {
		t.Errorf("thread/start environments = %#v, want []", thread["environments"])
	}
	// No sandbox. Every current model carries tool_mode=code_mode_only, so a
	// tool call runs through the code-mode host, and that host's sandbox is
	// bubblewrap, which cannot create a namespace inside the container: every
	// call answered "the shell sandbox is missing `bwrap`" and reports came back
	// with 0 tool calls. The container is the boundary instead -- childEnv keeps
	// the database DSN and the secrets out of this process.
	if thread["sandbox"] != "danger-full-access" {
		t.Errorf("thread/start sandbox = %v, want danger-full-access", thread["sandbox"])
	}
	if thread["developerInstructions"] != "You write weekly reports." {
		t.Errorf("thread/start developerInstructions = %v", thread["developerInstructions"])
	}
	tools, _ := thread["dynamicTools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("dynamicTools = %v", thread["dynamicTools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "echo" || tool["description"] != "Echo the query back." {
		t.Errorf("dynamic tool = %v", tool)
	}
	if schema, _ := tool["inputSchema"].(map[string]any); schema["type"] != "object" {
		t.Errorf("inputSchema = %v", tool["inputSchema"])
	}
	turn := params(t, sent["turn/start"], "turn/start")
	if turn["threadId"] != "th1" {
		t.Errorf("turn/start threadId = %v", turn["threadId"])
	}
	if schema, _ := turn["outputSchema"].(map[string]any); schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Errorf("outputSchema = %v", turn["outputSchema"])
	}
	input, _ := turn["input"].([]any)
	if len(input) != 1 || input[0].(map[string]any)["text"] != "Summarize the week." {
		t.Errorf("turn input = %v", turn["input"])
	}
	if len(sent["turn/interrupt"]) != 0 {
		t.Error("a completed turn must not be interrupted")
	}
}

// A run's model and effort are decided per request, not at boot: they go on
// turn/start (and the model on thread/start, whose answer confirms it).
func TestRunPassesRequestModelAndEffort(t *testing.T) {
	rt, capture := runtimeFor(t, fakeToolsHappy, RuntimeConfig{Model: "gpt-boot"})
	req := baseRequest(echoTool(nil))
	req.Model, req.ReasoningEffort = "gpt-5.6-terra", "high"

	res, err := rt.Run(context.Background(), req, newSink().sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Model != "gpt-5.6-terra" {
		t.Errorf("model = %q, want the requested model", res.Model)
	}
	_, sent := captured(t, capture)
	if thread := params(t, sent["thread/start"], "thread/start"); thread["model"] != "gpt-5.6-terra" {
		t.Errorf("thread/start model = %v", thread["model"])
	}
	turn := params(t, sent["turn/start"], "turn/start")
	if turn["model"] != "gpt-5.6-terra" || turn["effort"] != "high" {
		t.Errorf("turn/start model = %v effort = %v", turn["model"], turn["effort"])
	}
}

// Without a requested model or effort, turn/start carries neither and the
// home's config.toml decides.
func TestRunOmitsEmptyModelAndEffort(t *testing.T) {
	rt, capture := runtimeFor(t, fakeToolsHappy, RuntimeConfig{})
	if _, err := rt.Run(context.Background(), baseRequest(echoTool(nil)), newSink().sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, sent := captured(t, capture)
	turn := params(t, sent["turn/start"], "turn/start")
	if _, ok := turn["model"]; ok {
		t.Errorf("turn/start model = %v, want absent", turn["model"])
	}
	if _, ok := turn["effort"]; ok {
		t.Errorf("turn/start effort = %v, want absent", turn["effort"])
	}
}

// Past MaxToolCalls the call is answered success:false so the model can finish
// instead of the turn failing.
func TestRunAnswersCallsPastLimitWithFailure(t *testing.T) {
	rt, _ := runtimeFor(t, fakeToolsHappy, RuntimeConfig{})
	rec := newSink()
	req := baseRequest(echoTool(nil))
	req.MaxToolCalls = 1

	res, err := rt.Run(context.Background(), req, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var final []fakeFinalTool
	_ = json.Unmarshal([]byte(res.FinalText), &final)
	ok := 0
	for _, f := range final {
		if f.Success {
			ok++
		}
	}
	if len(final) != 2 || ok != 1 {
		t.Fatalf("answers = %+v, want exactly one success", final)
	}
	failed := 0
	for _, r := range rec.kinds(airuntime.EventToolResult) {
		if r.Status == airuntime.ToolStatusFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed tool_result events = %d, want 1", failed)
	}
}

// A handler that never returns is answered at ToolTimeout. The server has no
// timeout of its own (experiment §4.8), so this is the only one.
func TestRunAnswersSlowToolWithTimeout(t *testing.T) {
	rt, _ := runtimeFor(t, fakeToolsHappy, RuntimeConfig{})
	rec := newSink()
	req := baseRequest(echoTool(func(ctx context.Context, _ json.RawMessage) (airuntime.ToolOutput, error) {
		<-ctx.Done()
		return airuntime.ToolOutput{}, ctx.Err()
	}))
	req.ToolTimeout = 100 * time.Millisecond

	res, err := rt.Run(context.Background(), req, rec.sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var final []fakeFinalTool
	_ = json.Unmarshal([]byte(res.FinalText), &final)
	if len(final) != 2 || final[0].Success || final[1].Success {
		t.Fatalf("answers = %+v, want both success:false", final)
	}
	for _, r := range rec.kinds(airuntime.EventToolResult) {
		if r.Status != airuntime.ToolStatusTimeout {
			t.Errorf("tool_result status = %q, want timeout", r.Status)
		}
	}
}

func TestRunWallClockInterruptsThenKills(t *testing.T) {
	rt, capture := runtimeFor(t, fakeToolsHang, RuntimeConfig{})
	prev := interruptGrace
	interruptGrace = 300 * time.Millisecond
	t.Cleanup(func() { interruptGrace = prev })
	req := baseRequest(echoTool(nil))
	req.WallClock = 500 * time.Millisecond

	before := descendantPIDs(t)
	start := time.Now()
	_, err := rt.Run(context.Background(), req, newSink().sink)
	if !errors.Is(err, airuntime.ErrTimeLimit) {
		t.Fatalf("err = %v, want ErrTimeLimit", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %s", elapsed)
	}
	var re *airuntime.RunError
	if !errors.As(err, &re) || re.Code == "" {
		t.Fatalf("err = %#v, want a RunError with a code", err)
	}
	_, sent := captured(t, capture)
	intr := params(t, sent["turn/interrupt"], "turn/interrupt")
	if intr["threadId"] != "th1" || intr["turnId"] != "tu1" {
		t.Errorf("turn/interrupt params = %v", intr)
	}
	assertNoNewDescendants(t, before)
}

func TestRunCancelInterrupts(t *testing.T) {
	rt, capture := runtimeFor(t, fakeToolsHang, RuntimeConfig{})
	prev := interruptGrace
	interruptGrace = 300 * time.Millisecond
	t.Cleanup(func() { interruptGrace = prev })
	rec := newSink()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-rec.calls
		cancel()
	}()

	before := descendantPIDs(t)
	_, err := rt.Run(ctx, baseRequest(echoTool(nil)), rec.sink)
	if !errors.Is(err, airuntime.ErrCanceled) {
		t.Fatalf("err = %v, want ErrCanceled", err)
	}
	_, sent := captured(t, capture)
	if len(sent["turn/interrupt"]) != 1 {
		t.Errorf("turn/interrupt sent %d times, want 1", len(sent["turn/interrupt"]))
	}
	assertNoNewDescendants(t, before)
}

// A child that stops reading stdin blocks the loop inside a write, where it
// cannot see the wall clock. The kill has to come from outside the loop.
func TestRunKillsChildThatStopsReading(t *testing.T) {
	rt, _ := runtimeFor(t, fakeToolsStall, RuntimeConfig{})
	prev := interruptGrace
	interruptGrace = 300 * time.Millisecond
	t.Cleanup(func() { interruptGrace = prev })
	req := baseRequest(echoTool(func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
		return airuntime.ToolOutput{Text: strings.Repeat("x", 1<<20)}, nil
	}))
	req.WallClock = 500 * time.Millisecond

	before := descendantPIDs(t)
	start := time.Now()
	_, err := rt.Run(context.Background(), req, newSink().sink)
	if !errors.Is(err, airuntime.ErrTimeLimit) {
		t.Fatalf("err = %v, want ErrTimeLimit", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %s with a stalled child", elapsed)
	}
	assertNoNewDescendants(t, before)
}

// The child is model-driven. The daemon's own settings (database DSN, JWT
// secret, an inherited API key) must not reach it.
func TestRunDoesNotPassDaemonEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://secret")
	t.Setenv("CODEX_API_KEY", "sk-from-parent")
	t.Setenv("HOME", "/daemon/home")
	rt, capture := runtimeFor(t, fakeSchemaGarbage, RuntimeConfig{})
	if _, err := rt.Run(context.Background(), baseRequest(echoTool(nil)), newSink().sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	head, _ := captured(t, capture)
	if head["databaseURL"] != false {
		t.Error("DATABASE_URL reached the child")
	}
	if head["codexApiKey"] != false {
		t.Error("the parent's CODEX_API_KEY reached a ChatGPT-login run")
	}
	if head["path"] != true {
		t.Error("PATH did not reach the child")
	}
	// A shell the model reaches must not find the daemon user's dotfiles.
	if home, _ := head["home"].(string); home == "/daemon/home" || !strings.Contains(home, "cctrace-ai-run-") {
		t.Errorf("HOME = %q, want the run's empty work directory", home)
	}
}

// Past the limit the model is told to answer. One that keeps calling anyway
// would burn the wall clock; five refused calls in a row end the turn.
func TestRunStopsAfterRepeatedCallsPastLimit(t *testing.T) {
	rt, capture := runtimeFor(t, fakeToolsFlood, RuntimeConfig{})
	req := baseRequest(echoTool(nil))
	req.MaxToolCalls = 1
	_, err := rt.Run(context.Background(), req, newSink().sink)
	if !errors.Is(err, airuntime.ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	_, sent := captured(t, capture)
	if len(sent["turn/interrupt"]) != 1 {
		t.Errorf("turn/interrupt sent %d times, want 1", len(sent["turn/interrupt"]))
	}
}

// The server echoes tool output inside item/completed. Output near the line
// cap would end a healthy turn, so it is cut well below it.
func TestRunTruncatesToolOutputBelowLineCap(t *testing.T) {
	const lineCap = 64 << 10
	rt, _ := runtimeFor(t, fakeToolsHappy, RuntimeConfig{MaxLineBytes: lineCap})
	req := baseRequest(echoTool(func(context.Context, json.RawMessage) (airuntime.ToolOutput, error) {
		return airuntime.ToolOutput{Text: strings.Repeat("가", 1<<18)}, nil
	}))
	res, err := rt.Run(context.Background(), req, newSink().sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var final []fakeFinalTool
	if err := json.Unmarshal([]byte(res.FinalText), &final); err != nil || len(final) != 2 {
		t.Fatalf("final = %q: %v", res.FinalText, err)
	}
	for _, f := range final {
		if !f.Success || len(f.Text) == 0 || len(f.Text) > lineCap/4 {
			t.Errorf("tool text length = %d, want 0 < n <= %d", len(f.Text), lineCap/4)
		}
	}
}

// An item the report never asked for (a connector call, a shell command)
// stops the turn rather than letting it run. A tool call that arrives after
// that is refused without running its handler.
func TestRunStopsOnUnexpectedItem(t *testing.T) {
	rt, capture := runtimeFor(t, fakeUnexpectedItem, RuntimeConfig{})
	var handled atomic.Int32
	tool := echoTool(nil)
	inner := tool.Handler
	tool.Handler = func(ctx context.Context, args json.RawMessage) (airuntime.ToolOutput, error) {
		handled.Add(1)
		return inner(ctx, args)
	}
	_, err := rt.Run(context.Background(), baseRequest(tool), newSink().sink)
	if n := handled.Load(); n != 0 {
		t.Errorf("handler ran %d times after the turn was stopped", n)
	}
	if !errors.Is(err, airuntime.ErrUnexpectedTool) {
		t.Fatalf("err = %v, want ErrUnexpectedTool", err)
	}
	if !strings.Contains(err.Error(), "mcpToolCall") {
		t.Errorf("err = %v, want the item type named", err)
	}
	_, sent := captured(t, capture)
	if len(sent["turn/interrupt"]) != 1 {
		t.Errorf("turn/interrupt sent %d times, want 1", len(sent["turn/interrupt"]))
	}
}

// Whether the final answer fits the schema is the report's call
// (aireport.ValidateOutput); the runtime hands the text over unchanged.
func TestRunPassesNonJSONFinalAnswerThrough(t *testing.T) {
	rt, _ := runtimeFor(t, fakeSchemaGarbage, RuntimeConfig{})
	res, err := rt.Run(context.Background(), baseRequest(echoTool(nil)), newSink().sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "this is not json {" {
		t.Fatalf("final text = %q", res.FinalText)
	}
	if res.Usage.Reported {
		t.Error("usage reported without a tokenUsage event")
	}
}

func TestRunRejectsApprovalRequest(t *testing.T) {
	rt, capture := runtimeFor(t, fakeApprovalRequest, RuntimeConfig{})
	res, err := rt.Run(context.Background(), baseRequest(echoTool(nil)), newSink().sink)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalText != "done" {
		t.Fatalf("final text = %q", res.FinalText)
	}
	_, sent := captured(t, capture)
	var rejected bool
	for _, m := range sent[""] {
		if id, _ := m["id"].(float64); id == 0 && m["error"] != nil && m["result"] == nil {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("approval request not answered with an error: %v", sent[""])
	}
}

func TestRunBoundsStream(t *testing.T) {
	rt, _ := runtimeFor(t, fakeBigStream, RuntimeConfig{MaxStreamBytes: 64 << 10})
	before := descendantPIDs(t)
	_, err := rt.Run(context.Background(), baseRequest(echoTool(nil)), newSink().sink)
	if !errors.Is(err, airuntime.ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	assertNoNewDescendants(t, before)
}

func TestRunInterruptsPastTokenBudget(t *testing.T) {
	rt, capture := runtimeFor(t, fakeTokenBudget, RuntimeConfig{})
	req := baseRequest(echoTool(nil))
	req.MaxTotalTokens = 1000
	_, err := rt.Run(context.Background(), req, newSink().sink)
	if !errors.Is(err, airuntime.ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	_, sent := captured(t, capture)
	if len(sent["turn/interrupt"]) != 1 {
		t.Errorf("turn/interrupt sent %d times, want 1", len(sent["turn/interrupt"]))
	}
}

func TestRunSurfacesProtocolGarbage(t *testing.T) {
	fakeServer(t, fakeGarbage)
	rt := NewRuntime(RuntimeConfig{Home: t.TempDir()})
	_, err := rt.Run(context.Background(), baseRequest(echoTool(nil)), newSink().sink)
	if !errors.Is(err, airuntime.ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
}

func TestRunReportsServerThatExitsAsUnavailable(t *testing.T) {
	fakeServer(t, fakeExit3)
	rt := NewRuntime(RuntimeConfig{Home: t.TempDir()})
	_, err := rt.Run(context.Background(), baseRequest(echoTool(nil)), newSink().sink)
	if !errors.Is(err, airuntime.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestRunRequiresHomeAndBinary(t *testing.T) {
	if _, err := NewRuntime(RuntimeConfig{}).Run(context.Background(), baseRequest(), nil); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("no home: err = %v, want ErrNotConfigured", err)
	}
	prev := lookPathFn
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { lookPathFn = prev })
	if _, err := NewRuntime(RuntimeConfig{Home: t.TempDir()}).Run(context.Background(), baseRequest(), nil); !errors.Is(err, airuntime.ErrUnavailable) {
		t.Fatalf("no binary: err = %v, want ErrUnavailable", err)
	}
}

// app-server ignores CODEX_API_KEY; the key reaches it through the home's
// auth.json (PrepareHome), so the environment stays free of it.
func TestRunPassesHomeButNotAPIKeyToChild(t *testing.T) {
	home := t.TempDir()
	rt, capture := runtimeFor(t, fakeSchemaGarbage, RuntimeConfig{Home: home, APIKey: "sk-test"})
	seen := filepath.Join(t.TempDir(), "home")
	t.Setenv(fakeHomeSinkEnv, seen)
	if _, err := rt.Run(context.Background(), baseRequest(echoTool(nil)), newSink().sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := os.ReadFile(seen)
	if string(got) != home {
		t.Errorf("CODEX_HOME = %q, want %q", got, home)
	}
	head, _ := captured(t, capture)
	if head["codexApiKey"] != false {
		t.Error("CODEX_API_KEY reached the child")
	}
}

func TestInfo(t *testing.T) {
	home := t.TempDir()
	info := NewRuntime(RuntimeConfig{Home: home, Model: "gpt-test", APIKey: "sk"}).Info()
	if info.Key != RuntimeKey || info.Model != "gpt-test" || info.AuthMode != airuntime.AuthModeAPIKey {
		t.Fatalf("info = %+v", info)
	}
}

func TestStatusReadsAccountThroughFetch(t *testing.T) {
	fakeServer(t, fakeReply)
	withReply(t, multiBucketReply)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewRuntime(RuntimeConfig{Home: home}).Status(context.Background())
	if !st.Configured || !st.Available || st.Reason != "" {
		t.Fatalf("status = %+v", st)
	}
	if st.AccountEmail != "login@example.test" || st.PlanType != "pro" {
		t.Errorf("account = %q plan = %q", st.AccountEmail, st.PlanType)
	}
	if st.UsedPercent == nil || *st.UsedPercent != 27 {
		t.Errorf("used percent = %v, want the codex bucket's 27", st.UsedPercent)
	}
}

// An API key has no ChatGPT rate-limit meter; failing to read one does not
// make the runtime unavailable.
func TestStatusAPIKeyWithoutRateLimits(t *testing.T) {
	fakeServer(t, fakeRPCError)
	st := NewRuntime(RuntimeConfig{Home: t.TempDir(), APIKey: "sk-test"}).Status(context.Background())
	if !st.Configured || !st.Available || st.UsedPercent != nil {
		t.Fatalf("status = %+v, want available without a used percent", st)
	}

	// The same for a key registered from the screen, with no CODEX_API_KEY.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := NewRuntime(RuntimeConfig{Home: home}).Status(context.Background()); !st.Available {
		t.Fatalf("key file status = %+v, want available", st)
	}
}

func TestFetchDoesNotPassDaemonEnvironment(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://secret")
	fakeServer(t, fakeReply)
	withReply(t, multiBucketReply)
	sink := filepath.Join(t.TempDir(), "env")
	t.Setenv(fakeEnvSinkEnv, sink)
	if _, err := Fetch(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	b, err := os.ReadFile(sink)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]bool
	_ = json.Unmarshal(b, &env)
	if env["databaseURL"] || !env["path"] {
		t.Fatalf("child env = %s, want PATH without DATABASE_URL", b)
	}
}

func TestStatusWithoutLogin(t *testing.T) {
	fakeServer(t, fakeReply)
	st := NewRuntime(RuntimeConfig{Home: t.TempDir()}).Status(context.Background())
	if !st.Configured || st.Available || st.Reason == "" {
		t.Fatalf("status = %+v, want configured but unavailable with a reason", st)
	}
	if st := NewRuntime(RuntimeConfig{}).Status(context.Background()); st.Configured {
		t.Fatalf("status without home = %+v, want not configured", st)
	}
}

func TestStatusReportsFetchFailure(t *testing.T) {
	fakeServer(t, fakeRPCError)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := NewRuntime(RuntimeConfig{Home: home}).Status(context.Background())
	if st.Available || !strings.Contains(st.Reason, "not logged in") {
		t.Fatalf("status = %+v", st)
	}
}
