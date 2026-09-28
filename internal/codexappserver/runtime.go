package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cctrace/internal/airuntime"
)

// RuntimeKey identifies this runtime in Info, consent keys and the API.
const RuntimeKey = "codex-app-server"

const (
	defaultMaxLineBytes   = int64(4 << 20)
	defaultMaxStreamBytes = int64(64 << 20)
)

// interruptGrace is how long a turn gets to acknowledge turn/interrupt before
// the process group is killed. The real server answers in the same
// millisecond (experiment §6.1); the grace covers one that does not.
var interruptGrace = 5 * time.Second

// childEnvTestPrefix lets tests pass variables that steer the fake server.
// Empty in production.
var childEnvTestPrefix string

// Item types a report turn may produce. userMessage is the prompt echoed back
// (experiment §6.1). Anything else — a shell command, a connector call, a file
// change — was not asked for and stops the turn.
var expectedItems = map[string]bool{
	"userMessage": true, "agentMessage": true, "reasoning": true, "dynamicToolCall": true,
}

// builtinToolArgs turn off codex's own tools, so the model has only the
// report's dynamicTools. Feature names are from `codex features list` 0.154.0
// and the web_search value from the config reference. With toolLimitsConfig,
// a captured model request (TestLiveModelRequestToolList, experiment §6.3)
// leaves the report's tools plus code mode's exec/wait carrying them and
// request_user_input, whose server request Run refuses.
var builtinToolArgs = []string{
	"--disable", "shell_tool", "--disable", "unified_exec", "--disable", "view_image",
	"--disable", "sleep_tool", "--disable", "image_generation",
	"-c", `web_search="disabled"`,
}

// maxCallsPastLimit is how many refused calls in a row past MaxToolCalls end
// the turn: a model told to answer that keeps calling is only burning time.
const maxCallsPastLimit = 5

type RuntimeConfig struct {
	// Home is the dedicated CODEX_HOME. Never ~/.codex, whose otel exporter
	// and hooks would mix report runs into real telemetry.
	Home            string
	Model           string
	ReasoningEffort string
	// APIKey, when set, is written to Home/auth.json by PrepareHome (app-server
	// ignores CODEX_API_KEY); otherwise the ChatGPT login there is used.
	APIKey string
	// Disable lists features passed as `app-server --disable <name>`.
	Disable        []string
	MaxLineBytes   int64
	MaxStreamBytes int64
}

// CodexRuntime runs one report turn per process: spawn, ephemeral thread, one
// turn, kill. Nothing is pooled, so no state crosses users.
type CodexRuntime struct {
	cfg  RuntimeConfig
	acct accountState
}

func NewRuntime(cfg RuntimeConfig) airuntime.Runtime {
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = defaultMaxLineBytes
	}
	if cfg.MaxStreamBytes <= 0 {
		cfg.MaxStreamBytes = defaultMaxStreamBytes
	}
	return &CodexRuntime{cfg: cfg}
}

func (r *CodexRuntime) Info() airuntime.Info {
	return airuntime.Info{Key: RuntimeKey, Model: r.cfg.Model, AuthMode: AuthMode(r.cfg.Home, r.cfg.APIKey)}
}

// Status reuses Fetch and its cache, so polling it costs one process per cache
// interval at most.
func (r *CodexRuntime) Status(ctx context.Context) airuntime.Status {
	if r.cfg.Home == "" {
		return airuntime.Status{Reason: "codex home is not set"}
	}
	st := airuntime.Status{Configured: true}
	mode := AuthMode(r.cfg.Home, r.cfg.APIKey)
	if mode == airuntime.AuthModeNone {
		st.Reason = "not logged in: no API key and no auth.json in the codex home"
		return st
	}
	snap, err := Fetch(ctx, r.cfg.Home)
	if err != nil {
		// An API key has no ChatGPT rate-limit meter to read: only the reading
		// is missing, not the runtime. Unverified which reads succeed for a key.
		if mode == airuntime.AuthModeAPIKey {
			st.Available = true
			return st
		}
		st.Reason = err.Error()
		return st
	}
	st.Available = true
	st.AccountEmail = snap.LoginEmail
	for _, rd := range snap.Readings {
		if rd.LimitID != "codex" {
			if st.PlanType == "" {
				st.PlanType = rd.PlanType
			}
			continue
		}
		if rd.PlanType != "" {
			st.PlanType = rd.PlanType
		}
		if st.UsedPercent == nil || rd.UsedPercent > *st.UsedPercent {
			v := rd.UsedPercent
			st.UsedPercent = &v
		}
	}
	return st
}

func runErr(code, message string, sentinel error) *airuntime.RunError {
	return &airuntime.RunError{Code: code, Message: message, Err: sentinel}
}

func (r *CodexRuntime) Run(ctx context.Context, req airuntime.RunRequest, sink func(airuntime.Event)) (*airuntime.Result, error) {
	start := time.Now()
	if sink == nil {
		sink = func(airuntime.Event) {}
	}
	if r.cfg.Home == "" {
		return nil, runErr("not_configured", "codex home is not set", airuntime.ErrNotConfigured)
	}
	bin, err := lookPathFn("codex")
	if err != nil {
		return nil, runErr("runtime_unavailable", "codex CLI not found on PATH", airuntime.ErrUnavailable)
	}
	// An empty working directory, so the thread's cwd holds nothing to read.
	workDir, err := os.MkdirTemp("", "cctrace-ai-run-*")
	if err != nil {
		return nil, runErr("runtime_unavailable", err.Error(), airuntime.ErrUnavailable)
	}
	defer os.RemoveAll(workDir)

	runCtx, cancel := ctx, context.CancelFunc(func() {})
	if req.WallClock > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.WallClock)
	}
	defer cancel()

	args := append([]string{"app-server"}, builtinToolArgs...)
	for _, feature := range r.cfg.Disable {
		args = append(args, "--disable", feature)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = childEnv(r.cfg, workDir)
	cmd.Dir = workDir
	cmd.Stderr = nil
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, runErr("runtime_unavailable", err.Error(), airuntime.ErrUnavailable)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, runErr("runtime_unavailable", err.Error(), airuntime.ErrUnavailable)
	}
	if err := cmd.Start(); err != nil {
		return nil, runErr("runtime_unavailable", "start codex app-server: "+err.Error(), airuntime.ErrUnavailable)
	}
	// One teardown for every exit, as in Fetch: kill the group before reaping.
	grace := interruptGrace
	var reapMu sync.Mutex
	reaped := false
	exited := make(chan struct{})
	defer func() {
		close(exited)
		_ = stdin.Close()
		reapMu.Lock()
		killGroup(cmd.Process)
		reaped = true
		reapMu.Unlock()
		_ = cmd.Wait()
	}()
	// The turn loop checks the clock only between messages. A write to a child
	// that stopped reading stdin never comes back to it, so the kill also comes
	// from here, grace after the deadline or cancel. The lock keeps it from
	// signalling a pid that teardown has already reaped.
	go func() {
		select {
		case <-runCtx.Done():
		case <-exited:
			return
		}
		select {
		case <-time.After(grace):
		case <-exited:
			return
		}
		reapMu.Lock()
		defer reapMu.Unlock()
		if !reaped {
			killGroup(cmd.Process)
		}
	}()
	c := newConn(stdin, stdout, r.cfg.MaxLineBytes, r.cfg.MaxStreamBytes)
	defer c.Close()

	threadID, turnID, model, err := startTurn(runCtx, c, req, workDir)
	if err != nil {
		return nil, setupError(ctx, runCtx, req, err)
	}
	t := &turnLoop{
		ctx: ctx, runCtx: runCtx, conn: c, req: req, sink: sink,
		threadID: threadID, turnID: turnID,
		tools:       map[string]airuntime.Tool{},
		graceFor:    grace,
		maxToolText: int(r.cfg.MaxLineBytes / 8),
	}
	for _, tool := range req.Tools {
		t.tools[tool.Name] = tool
	}
	final, err := t.run()
	if err != nil {
		return nil, err
	}
	if model == "" {
		model = req.Model
	}
	if model == "" {
		model = r.cfg.Model
	}
	return &airuntime.Result{FinalText: final, Model: model, Usage: t.usage, Duration: time.Since(start)}, nil
}

// childEnvNames is what the child inherits: locating binaries, a home and temp
// directory, locale, TLS roots, proxies, and the Windows basics. Names are
// compared upper-cased because Windows treats them case-insensitively.
var childEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TMPDIR": true, "TMP": true, "TEMP": true, "LANG": true, "TZ": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"SYSTEMROOT": true, "COMSPEC": true, "PATHEXT": true, "APPDATA": true, "LOCALAPPDATA": true, "USERPROFILE": true,
}

// childEnv is an allowlist. The child is model-driven and a safe read-only
// command may run without approval, so the daemon's own settings (database
// DSN, secrets, an inherited CODEX_API_KEY that would silently turn a
// ChatGPT-login run into an API-key run) must not be in its environment.
// home, when set, replaces HOME (and USERPROFILE) so a command that does run
// finds an empty directory instead of the daemon user's dotfiles; codex itself
// reads only CODEX_HOME.
func childEnv(cfg RuntimeConfig, home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		upper := strings.ToUpper(name)
		if home != "" && (upper == "HOME" || upper == "USERPROFILE") {
			continue
		}
		if childEnvNames[upper] || strings.HasPrefix(upper, "LC_") ||
			(childEnvTestPrefix != "" && strings.HasPrefix(name, childEnvTestPrefix)) {
			env = append(env, kv)
		}
	}
	if home != "" {
		env = append(env, "HOME="+home, "USERPROFILE="+home)
	}
	env = append(env, "CODEX_HOME="+cfg.Home)
	return env
}

type setupStageError struct {
	stage string
	err   error
}

func (e *setupStageError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *setupStageError) Unwrap() error { return e.err }

// startTurn returns the thread and turn ids and the model thread/start says
// the thread uses, which is how a run without CCTRACE_AI_MODEL_DEFAULT learns codex's
// default model name.
func startTurn(ctx context.Context, c *conn, req airuntime.RunRequest, cwd string) (threadID, turnID, model string, err error) {
	if _, err := c.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": clientName, "version": clientVersion},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		return "", "", "", &setupStageError{"initialize", err}
	}
	if err := c.Notify("initialized", nil); err != nil {
		return "", "", "", &setupStageError{"initialize", err}
	}

	tools := make([]map[string]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		schema := tool.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		tools = append(tools, map[string]any{
			"type": "function", "name": tool.Name, "description": tool.Description, "inputSchema": schema,
		})
	}
	thread := map[string]any{
		"ephemeral":      true,
		"cwd":            cwd,
		"approvalPolicy": "untrusted",
		// No sandbox. Every model in the catalog carries tool_mode=code_mode_only,
		// so a report's tools are called from the code-mode host, and that host
		// sandboxes with bubblewrap -- which cannot create a namespace inside the
		// container (the host kernel restricts unprivileged user namespaces).
		// Every call answered "the shell sandbox is missing `bwrap`" and reports
		// came back having read nothing. The container is the boundary instead:
		// the thread has no shell or apply_patch (empty environments), its tools
		// only read cctrace's own data, and childEnv keeps the database DSN and
		// the secrets out of this process.
		"sandbox": "danger-full-access",
		// Empty disables environment access: no shell, apply_patch or
		// view_image for the thread (ThreadStartParams, experimental).
		"environments": []any{},
		"dynamicTools": tools,
	}
	if req.Instructions != "" {
		thread["developerInstructions"] = req.Instructions
	}
	// The model goes on thread/start too, so its answer names the model the
	// thread really runs rather than config.toml's.
	if req.Model != "" {
		thread["model"] = req.Model
	}
	raw, err := c.Call(ctx, "thread/start", thread)
	if err != nil {
		return "", "", "", &setupStageError{"thread/start", err}
	}
	var ts struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &ts) != nil || ts.Thread.ID == "" {
		return "", "", "", &setupStageError{"thread/start", fmt.Errorf("%w: no thread id in %s", airuntime.ErrProtocol, raw)}
	}

	turn := map[string]any{
		"threadId": ts.Thread.ID,
		"input":    []any{map[string]any{"type": "text", "text": req.Prompt}},
	}
	if len(req.OutputSchema) > 0 {
		turn["outputSchema"] = req.OutputSchema
	}
	// Per-turn overrides (TurnStartParams), so a changed setting applies to the
	// next run without rewriting config.toml or restarting the server.
	if req.Model != "" {
		turn["model"] = req.Model
	}
	if req.ReasoningEffort != "" {
		turn["effort"] = req.ReasoningEffort
	}
	raw, err = c.Call(ctx, "turn/start", turn)
	if err != nil {
		return "", "", "", &setupStageError{"turn/start", err}
	}
	var tr struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &tr) != nil || tr.Turn.ID == "" {
		return "", "", "", &setupStageError{"turn/start", fmt.Errorf("%w: no turn id in %s", airuntime.ErrProtocol, raw)}
	}
	return ts.Thread.ID, tr.Turn.ID, ts.Model, nil
}

// setupError maps a failure before the turn is running. No turn exists yet,
// so there is nothing to interrupt; the deferred kill is enough.
func setupError(ctx, runCtx context.Context, req airuntime.RunRequest, err error) error {
	var stage *setupStageError
	errors.As(err, &stage)
	switch {
	case ctx.Err() != nil:
		return runErr("canceled", "canceled before the turn started", airuntime.ErrCanceled)
	case runCtx.Err() != nil:
		return runErr("time_limit", fmt.Sprintf("turn did not start within %s", req.WallClock), airuntime.ErrTimeLimit)
	case errors.Is(err, errOutputLimit):
		return runErr("output_limit", err.Error(), airuntime.ErrBudget)
	case errors.Is(err, airuntime.ErrProtocol):
		return runErr("protocol_error", err.Error(), airuntime.ErrProtocol)
	case stage != nil && stage.stage == "initialize":
		// The server never came up: missing login, broken install, crash.
		return runErr("runtime_unavailable", err.Error(), airuntime.ErrUnavailable)
	default:
		return runErr("protocol_error", err.Error(), airuntime.ErrProtocol)
	}
}

type turnLoop struct {
	ctx, runCtx      context.Context
	conn             *conn
	req              airuntime.RunRequest
	sink             func(airuntime.Event)
	threadID, turnID string
	tools            map[string]airuntime.Tool
	graceFor         time.Duration
	// maxToolText keeps tool output well under the line cap: the server echoes
	// it back JSON-escaped inside item/completed.
	maxToolText int

	seq int
	// pastLimit counts calls refused in a row for MaxToolCalls.
	pastLimit int
	usage     airuntime.Usage
	stopErr   error
	grace     *time.Timer
}

type toolDone struct {
	id   json.RawMessage
	seq  int
	name string
	inv  airuntime.Invocation
}

func (t *turnLoop) run() (string, error) {
	done := make(chan toolDone)
	// Closed on return so handler goroutines still running never block, and
	// never reach sink after Run has returned.
	finished := make(chan struct{})
	defer close(finished)
	defer func() {
		if t.grace != nil {
			t.grace.Stop()
		}
	}()

	ctxDone := t.runCtx.Done()
	var graceC <-chan time.Time
	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			t.stop(t.ctxError())
		case <-graceC:
			return "", t.stopErr
		case d := <-done:
			t.finishTool(d)
		case m, ok := <-t.conn.Incoming():
			if !ok {
				return "", t.streamEnded()
			}
			if final, end, err := t.handle(m, done, finished); end {
				return final, err
			}
		}
		if t.stopErr != nil && graceC == nil {
			ctxDone = nil
			t.grace = time.NewTimer(t.graceFor)
			graceC = t.grace.C
		}
	}
}

// stop records why the turn must end and asks the server to end it. The first
// reason wins; the caller's loop then waits interruptGrace for turn/completed
// before the deferred teardown kills the process.
func (t *turnLoop) stop(err error) {
	if t.stopErr != nil {
		return
	}
	t.stopErr = err
	_ = t.conn.Send("turn/interrupt", map[string]any{"threadId": t.threadID, "turnId": t.turnID})
}

// ctxError tells a caller's cancel from the turn's own wall clock.
func (t *turnLoop) ctxError() error {
	if t.ctx.Err() != nil {
		return runErr("canceled", "run canceled", airuntime.ErrCanceled)
	}
	return runErr("time_limit", fmt.Sprintf("turn exceeded %s", t.req.WallClock), airuntime.ErrTimeLimit)
}

func (t *turnLoop) streamEnded() error {
	if t.stopErr != nil {
		return t.stopErr
	}
	// The watchdog killed a child the loop could not reach in time.
	if t.runCtx.Err() != nil {
		return t.ctxError()
	}
	err := t.conn.Err()
	switch {
	case errors.Is(err, errOutputLimit):
		return runErr("output_limit", err.Error(), airuntime.ErrBudget)
	case errors.Is(err, airuntime.ErrProtocol):
		return runErr("protocol_error", err.Error(), airuntime.ErrProtocol)
	default:
		return runErr("runtime_unavailable", fmt.Sprintf("codex app-server stopped during the turn: %v", err), airuntime.ErrUnavailable)
	}
}

func (t *turnLoop) handle(m rpcMessage, done chan<- toolDone, finished <-chan struct{}) (string, bool, error) {
	if len(m.ID) > 0 {
		if m.Method == "item/tool/call" {
			t.startTool(m, done, finished)
		} else {
			// Approvals, user input, elicitation: a report turn has no one to
			// ask, so every other server request is refused.
			_ = t.conn.RespondError(m.ID, -32601, "cctrace does not handle "+m.Method)
		}
		return "", false, nil
	}

	switch m.Method {
	case "item/started":
		var p struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			t.stop(runErr("protocol_error", "malformed item/started", airuntime.ErrProtocol))
		} else if !expectedItems[p.Item.Type] {
			t.stop(runErr("unexpected_tool", "turn started a "+p.Item.Type+" item", airuntime.ErrUnexpectedTool))
		}
	case "item/agentMessage/delta":
		var p struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(m.Params, &p) == nil && p.Delta != "" {
			t.sink(airuntime.Event{Kind: airuntime.EventTextDelta, Text: p.Delta})
		}
	case "thread/tokenUsage/updated":
		var p struct {
			TokenUsage struct {
				Total struct {
					InputTokens       int64 `json:"inputTokens"`
					CachedInputTokens int64 `json:"cachedInputTokens"`
					OutputTokens      int64 `json:"outputTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(m.Params, &p) != nil {
			break
		}
		total := p.TokenUsage.Total
		t.usage = airuntime.Usage{Reported: true, InputTokens: total.InputTokens, CachedInputTokens: total.CachedInputTokens, OutputTokens: total.OutputTokens}
		u := t.usage
		t.sink(airuntime.Event{Kind: airuntime.EventUsage, Usage: &u})
		if max := t.req.MaxTotalTokens; max > 0 && u.InputTokens+u.OutputTokens > max {
			t.stop(runErr("budget_exceeded", fmt.Sprintf("turn used more than %d tokens", max), airuntime.ErrBudget))
		}
	case "turn/completed":
		final, err := t.completed(m.Params)
		return final, true, err
	}
	return "", false, nil
}

func (t *turnLoop) completed(params json.RawMessage) (string, error) {
	if t.stopErr != nil {
		return "", t.stopErr
	}
	var p struct {
		Turn struct {
			Status string `json:"status"`
			Items  []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Phase string `json:"phase"`
			} `json:"items"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", runErr("protocol_error", "malformed turn/completed", airuntime.ErrProtocol)
	}
	switch p.Turn.Status {
	case "completed":
		final, found := "", false
		for _, item := range p.Turn.Items {
			if item.Type == "agentMessage" && item.Phase == "final_answer" {
				final, found = item.Text, true
			}
		}
		if !found {
			return "", runErr("protocol_error", "turn completed without a final answer", airuntime.ErrProtocol)
		}
		return final, nil
	case "failed":
		msg := "turn failed"
		if p.Turn.Error != nil && p.Turn.Error.Message != "" {
			msg = p.Turn.Error.Message
		}
		return "", runErr("turn_failed", msg, airuntime.ErrUnavailable)
	default:
		return "", runErr("protocol_error", "turn ended with status "+p.Turn.Status, airuntime.ErrProtocol)
	}
}

func (t *turnLoop) startTool(m rpcMessage, done chan<- toolDone, finished <-chan struct{}) {
	var p struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(m.Params, &p)
	if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
		p.Arguments = json.RawMessage(`{}`)
	}
	t.seq++
	seq := t.seq
	var args map[string]any
	_ = json.Unmarshal(p.Arguments, &args)
	t.sink(airuntime.Event{Kind: airuntime.EventToolCall, Seq: seq, Tool: p.Tool, Args: args})

	tool, known := t.tools[p.Tool]
	switch {
	case t.stopErr != nil:
		// The turn is being interrupted; its result would be discarded.
		t.finishTool(toolDone{id: m.ID, seq: seq, name: p.Tool, inv: airuntime.Invocation{
			Output: airuntime.ToolOutput{Text: "turn is stopping; tool not run"}, Status: airuntime.ToolStatusFailed,
		}})
	case !known:
		t.finishTool(toolDone{id: m.ID, seq: seq, name: p.Tool, inv: airuntime.Invocation{
			Output: airuntime.ToolOutput{Text: "unknown tool " + p.Tool}, Status: airuntime.ToolStatusFailed,
		}})
	case t.req.MaxToolCalls > 0 && seq > t.req.MaxToolCalls:
		t.finishTool(toolDone{id: m.ID, seq: seq, name: p.Tool, inv: airuntime.Invocation{
			Output: airuntime.ToolOutput{Text: fmt.Sprintf("tool call limit of %d reached; answer with what you have", t.req.MaxToolCalls)},
			Status: airuntime.ToolStatusFailed,
		}})
		if t.pastLimit++; t.pastLimit >= maxCallsPastLimit {
			t.stop(runErr("tool_call_limit", fmt.Sprintf("model kept calling tools past the limit of %d", t.req.MaxToolCalls), airuntime.ErrBudget))
		}
	default:
		t.pastLimit = 0
		// Off the loop, so usage, interrupts and other calls keep flowing
		// while a handler runs.
		go func() {
			inv := airuntime.InvokeTool(t.runCtx, tool, p.Arguments, t.req.ToolTimeout)
			select {
			case done <- toolDone{id: m.ID, seq: seq, name: tool.Name, inv: inv}:
			case <-finished:
			}
		}()
	}
}

func (t *turnLoop) finishTool(d toolDone) {
	t.sink(airuntime.Event{
		Kind: airuntime.EventToolResult, Seq: d.seq, Tool: d.name, Args: d.inv.Output.ArgsSummary,
		Status: d.inv.Status, Rows: d.inv.Output.Rows, DurationMs: int(d.inv.Duration.Milliseconds()),
	})
	text := d.inv.Output.Text
	if limit := t.maxToolText; len(text) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "\n[output truncated]"
	}
	_ = t.conn.Respond(d.id, map[string]any{
		"success":      d.inv.Success,
		"contentItems": []any{map[string]any{"type": "inputText", "text": text}},
	})
}
