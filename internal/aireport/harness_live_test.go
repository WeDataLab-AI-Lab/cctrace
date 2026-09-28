package aireport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/chatruntime"
	"cctrace/internal/clauderuntime"
	"cctrace/internal/codexappserver"
	"cctrace/internal/openairuntime"
	"cctrace/internal/store"
)

// The harness iterates on the report prompt against real data without an image
// build or a deploy. It assembles the same run Service assembles -- BuildTools,
// BuildPrompt, OutputSchema, then ValidateOutput -- and deliberately skips the
// service itself, which persists runs and enforces consent and concurrency.
// None of that is part of the prompt under test.
//
// It carries no build tag on purpose: gated only by CCTRACE_AI_HARNESS_DSN, it
// stays visible as a SKIP in a plain `go test ./internal/aireport/`, where a
// tagged file would silently not exist. Every run spends real tokens on
// whichever account the chosen runtime is logged in as.
//
// See docs/guides/guide-deployment.md §11.7 for the tunnel and the variables.

// harnessEnv reads the first variable that holds a value, so a runtime's own
// live-test names keep working unchanged.
func harnessEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// harnessRuntime is one candidate: configured says its environment is present,
// build is deferred so an unselected runtime is never constructed.
type harnessRuntime struct {
	key        string
	configured bool
	build      func(*testing.T) (airuntime.Runtime, string)
}

func harnessRuntimes() []harnessRuntime {
	codexHome := harnessEnv("CCTRACE_CODEX_LIVE_HOME")
	openaiKey := harnessEnv("CCTRACE_OPENAI_LIVE_KEY", "OPENAI_API_KEY")
	claudeKey := harnessEnv("CCTRACE_ANTHROPIC_LIVE_KEY", "ANTHROPIC_API_KEY")
	litellmKey := harnessEnv("CCTRACE_LITELLM_LIVE_KEY")
	litellmURL := harnessEnv("CCTRACE_LITELLM_LIVE_BASE_URL")
	return []harnessRuntime{
		{DefaultRuntimeKey, codexHome != "", func(t *testing.T) (airuntime.Runtime, string) {
			model := harnessEnv("CCTRACE_CODEX_LIVE_MODEL")
			cfg := codexappserver.RuntimeConfig{
				Home:            codexHome,
				Model:           model,
				ReasoningEffort: harnessEnv("CCTRACE_CODEX_LIVE_EFFORT"),
				APIKey:          harnessEnv("CODEX_API_KEY"),
				Disable:         []string{"apps"},
			}
			// PrepareHome rewrites config.toml, so the home must be a dedicated
			// one; never ~/.codex.
			if err := codexappserver.PrepareHome(cfg); err != nil {
				t.Fatalf("PrepareHome(%s): %v", codexHome, err)
			}
			return codexappserver.NewRuntime(cfg), model
		}},
		{RuntimeOpenAI, openaiKey != "", func(*testing.T) (airuntime.Runtime, string) {
			model := harnessEnv("CCTRACE_OPENAI_LIVE_MODEL")
			return openairuntime.New(openairuntime.Config{
				APIKey:       func(context.Context) (string, error) { return openaiKey, nil },
				DefaultModel: model,
			}), model
		}},
		{RuntimeClaude, claudeKey != "", func(*testing.T) (airuntime.Runtime, string) {
			model := harnessEnv("CCTRACE_ANTHROPIC_LIVE_MODEL")
			return clauderuntime.New(clauderuntime.Config{
				APIKey:       func(context.Context) (string, error) { return claudeKey, nil },
				DefaultModel: model,
			}), model
		}},
		{RuntimeLiteLLM, litellmKey != "" && litellmURL != "", func(*testing.T) (airuntime.Runtime, string) {
			model := harnessEnv("CCTRACE_LITELLM_LIVE_MODEL")
			return chatruntime.New(chatruntime.Config{
				APIKey:       func(context.Context) (string, error) { return litellmKey, nil },
				RuntimeKey:   RuntimeLiteLLM,
				BaseURL:      func(context.Context) (string, error) { return litellmURL, nil },
				DefaultModel: model,
				ProviderName: "litellm",
			}), model
		}},
	}
}

// pickHarnessRuntime returns the one configured runtime. Several configured at
// once is ambiguous rather than a default, so it demands an explicit choice.
func pickHarnessRuntime(t *testing.T) (airuntime.Runtime, string, string) {
	t.Helper()
	want := harnessEnv("CCTRACE_AI_HARNESS_RUNTIME")
	var ready []harnessRuntime
	for _, c := range harnessRuntimes() {
		if c.configured {
			ready = append(ready, c)
		}
	}
	if len(ready) == 0 {
		t.Skip("no ai runtime configured: set CCTRACE_CODEX_LIVE_HOME, CCTRACE_OPENAI_LIVE_KEY, CCTRACE_ANTHROPIC_LIVE_KEY, or CCTRACE_LITELLM_LIVE_KEY + CCTRACE_LITELLM_LIVE_BASE_URL")
	}
	keys := make([]string, len(ready))
	for i, c := range ready {
		keys[i] = c.key
	}
	if want == "" {
		if len(ready) > 1 {
			t.Fatalf("%d runtimes configured (%s): set CCTRACE_AI_HARNESS_RUNTIME to one of them", len(ready), strings.Join(keys, ", "))
		}
		want = ready[0].key
	}
	for _, c := range ready {
		if c.key == want {
			rt, model := c.build(t)
			return rt, c.key, model
		}
	}
	t.Fatalf("CCTRACE_AI_HARNESS_RUNTIME=%q is not configured; configured: %s", want, strings.Join(keys, ", "))
	return nil, "", ""
}

// harnessScope resolves the dashboard user and the segment scope the same way
// api.aiRequest does: the cctrace user id when the account carries one, the
// profile email otherwise. Getting this wrong reads someone else's week or
// nobody's.
func harnessScope(ctx context.Context, t *testing.T, st *store.PgStore, email string) Scope {
	t.Helper()
	u, err := st.GetDashboardUserByEmail(ctx, email)
	if err != nil {
		t.Fatalf("look up %s: %v", email, err)
	}
	if u == nil {
		t.Fatalf("no dashboard user with email %s in this database", email)
	}
	sc := Scope{DashboardUserID: u.ID}
	if u.CctraceUserID != "" {
		sc.UserID = u.CctraceUserID
	} else {
		sc.ProfileEmail = u.Email
	}
	return sc
}

type harnessToolCall struct {
	Seq  int            `json:"seq"`
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
	Rows int            `json:"rows"`
	Err  string         `json:"error,omitempty"`
}

type harnessResult struct {
	Week       string               `json:"week"`
	TZ         string               `json:"tz"`
	User       string               `json:"user"`
	Runtime    string               `json:"runtime"`
	Model      string               `json:"model"`
	ToolCalls  []harnessToolCall    `json:"tool_calls"`
	Summary    string               `json:"summary"`
	Items      []store.AIReportItem `json:"items"`
	Dropped    int                  `json:"dropped_items"`
	Valid      bool                 `json:"valid"`
	Reason     string               `json:"invalid_reason,omitempty"`
	Usage      airuntime.Usage      `json:"usage"`
	DurationMs int64                `json:"duration_ms"`
	FinalText  string               `json:"final_text"`
}

// TestHarnessWeeklyPrompt runs one report against a real database with the
// package Instructions or a variant read from a file. Variants live in files so
// comparing two of them never means editing and rebuilding the package.
func TestHarnessWeeklyPrompt(t *testing.T) {
	dsn := harnessEnv("CCTRACE_AI_HARNESS_DSN")
	if dsn == "" {
		t.Skip("CCTRACE_AI_HARNESS_DSN not set")
	}
	rt, runtimeName, wantModel := pickHarnessRuntime(t)

	email := harnessEnv("CCTRACE_AI_HARNESS_EMAIL")
	if email == "" {
		t.Fatal("CCTRACE_AI_HARNESS_EMAIL not set: the harness runs as one dashboard user")
	}
	tz := harnessEnv("CCTRACE_AI_HARNESS_TZ")
	if tz == "" {
		tz = "Asia/Seoul"
	}
	instructions := Instructions
	if path := harnessEnv("CCTRACE_AI_HARNESS_INSTRUCTIONS"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read instructions %s: %v", path, err)
		}
		instructions = string(b)
	}

	ctx := context.Background()
	var wk Week
	var err error
	if id := harnessEnv("CCTRACE_AI_HARNESS_WEEK"); id != "" {
		wk, err = ParseISOWeek(id, tz)
	} else {
		// The week that just ended: the current one has no full data yet.
		var cur Week
		if cur, err = WeekContaining(time.Now(), tz); err == nil {
			wk = cur.Previous()
		}
	}
	if err != nil {
		t.Fatalf("week: %v", err)
	}

	st, err := store.NewPgStore(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to CCTRACE_AI_HARNESS_DSN: %v", err)
	}
	defer st.Close()
	sc := harnessScope(ctx, t, st, email)

	agg, err := st.AIWeekAggregate(ctx, segmentScope(sc, wk))
	if err != nil {
		t.Fatalf("week aggregate: %v", err)
	}
	t.Logf("week=%s tz=%s user=%s (dashboard_user_id=%d user_id=%q profile_email=%q) runtime=%s model=%s instructions=%d chars",
		wk.ID, wk.TZ, email, sc.DashboardUserID, sc.UserID, sc.ProfileEmail, runtimeName, wantModel, len(instructions))

	out := harnessResult{Week: wk.ID, TZ: wk.TZ, User: email, Runtime: runtimeName, Model: wantModel}
	var mu sync.Mutex
	tools := BuildTools(st, sc, wk)
	for i := range tools {
		name, handler := tools[i].Name, tools[i].Handler
		tools[i].Handler = func(ctx context.Context, args json.RawMessage) (airuntime.ToolOutput, error) {
			res, err := handler(ctx, args)
			mu.Lock()
			defer mu.Unlock()
			call := harnessToolCall{Seq: len(out.ToolCalls) + 1, Tool: name, Args: res.ArgsSummary, Rows: res.Rows}
			if err != nil {
				call.Err = err.Error()
			}
			out.ToolCalls = append(out.ToolCalls, call)
			t.Logf("tool %d: %s %v -> rows=%d%s", call.Seq, call.Tool, call.Args, call.Rows, errSuffix(err))
			return res, err
		}
	}

	// The budgets match NewService's defaults, so a variant is measured under
	// the limits the deployed run would give it.
	req := airuntime.RunRequest{
		Model:           wantModel,
		ReasoningEffort: harnessEnv("CCTRACE_CODEX_LIVE_EFFORT"),
		Instructions:    instructions,
		Prompt:          BuildPrompt(wk, agg),
		Tools:           tools,
		OutputSchema:    OutputSchema,
		ToolTimeout:     15 * time.Second,
		WallClock:       8 * time.Minute,
		MaxToolCalls:    30,
		MaxTotalTokens:  600_000,
	}

	res, runErr := rt.Run(ctx, req, func(airuntime.Event) {})
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	out.Usage, out.DurationMs, out.FinalText = res.Usage, res.Duration.Milliseconds(), res.FinalText
	if res.Model != "" {
		out.Model = res.Model
	}

	report, dropped, valErr := ValidateOutput(ctx, st, sc, wk, res.FinalText)
	out.Dropped, out.Valid = dropped, valErr == nil
	if valErr != nil {
		out.Reason = valErr.Error()
	} else {
		out.Summary, out.Items = report.Summary, report.Items
	}

	t.Logf("summary: %s", out.Summary)
	for i, it := range out.Items {
		t.Logf("item %d: segment_id=%s title=%s reason=%s", i+1, it.SegmentID, it.Title, it.Reason)
	}
	t.Logf("usage: %+v duration=%s dropped_items=%d", out.Usage, res.Duration, dropped)
	if valErr != nil {
		t.Errorf("ValidateOutput rejected the answer: %v\nfinal text: %s", valErr, res.FinalText)
	} else {
		t.Logf("ValidateOutput accepted the answer")
	}

	if path := harnessEnv("CCTRACE_AI_HARNESS_OUT"); path != "" {
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("result written to %s", path)
	}
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf(" error=%v", err)
}
