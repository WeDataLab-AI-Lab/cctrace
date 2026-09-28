package aireport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

const testRuntimeKey = "codex-app-server:chatgpt"

func readyRuntime(steps []airuntime.FakeStep, final string) *airuntime.FakeRuntime {
	return &airuntime.FakeRuntime{
		InfoValue:   airuntime.Info{Key: "codex-app-server", Model: "gpt-test", AuthMode: airuntime.AuthModeChatGPT},
		StatusValue: airuntime.Status{Configured: true, Available: true},
		Steps:       steps,
		FinalText:   final,
		Usage:       airuntime.Usage{Reported: true, InputTokens: 100, OutputTokens: 10},
	}
}

type fixture struct {
	st  *MemStore
	sc  Scope
	wk  Week
	svc *Service
}

func newFixture(t *testing.T, rt airuntime.Runtime) *fixture {
	t.Helper()
	wk := testWeek(t)
	st := NewMemStore()
	st.AddSegment("me", segAt(10, wk))
	sc := Scope{DashboardUserID: 1, UserID: "me"}
	if err := st.UpsertAIConsent(context.Background(), 1, testRuntimeKey, DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, []airuntime.Runtime{rt}, Config{EnvRuntime: DefaultRuntimeKey})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		svc.Shutdown(ctx)
	})
	return &fixture{st: st, sc: sc, wk: wk, svc: svc}
}

func waitRun(t *testing.T, st *MemStore, id int64, status string) *store.AIReportRun {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := st.GetAIReportRun(context.Background(), id)
		if run != nil && run.Status == status {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	run, _ := st.GetAIReportRun(context.Background(), id)
	t.Fatalf("run %d status = %+v, want %s", id, run, status)
	return nil
}

const goodOutput = `{"summary":"요약","items":[{"segment_id":"10","title":"t","reason":"r"}]}`

func TestStartCompletesRunAndUpsertsReportOnce(t *testing.T) {
	rt := readyRuntime([]airuntime.FakeStep{
		{CallTool: "query_segments", Args: json.RawMessage(`{"limit":5,"prompt":"leak"}`)},
		{CallTool: "read_segment", Args: json.RawMessage(`{"segment_id":"10"}`)},
	}, goodOutput)
	f := newFixture(t, rt)

	run, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.AIRunRunning || run.Runtime != "codex-app-server" || run.Model != DefaultModel || run.AuthMode != "chatgpt" {
		t.Fatalf("run = %+v", run)
	}
	done := waitRun(t, f.st, run.ID, store.AIRunCompleted)
	if !done.Usage.Reported || done.Usage.InputTokens != 100 {
		t.Fatalf("usage = %+v", done.Usage)
	}
	if f.st.CompleteCalls != 1 {
		t.Fatalf("CompleteAIReportRun calls = %d", f.st.CompleteCalls)
	}
	rep, _ := f.st.GetAIReport(context.Background(), 1, f.wk.ID)
	if rep == nil || rep.RunID != run.ID || len(rep.Items) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	calls, _ := f.st.ListAIToolCalls(context.Background(), run.ID, 0)
	// The unknown "prompt" argument fails the first call; it must still not reach the log.
	if len(calls) != 2 || calls[0].Status != store.AIToolCallFailed || calls[1].Status != store.AIToolCallOK || calls[1].Tool != "read_segment" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if string(calls[0].Args) == "" || json.Valid(calls[0].Args) == false || containsKey(calls[0].Args, "prompt") {
		t.Fatalf("args not summarized: %s", calls[0].Args)
	}
	req := rt.Requests()[0]
	if req.MaxToolCalls != 30 || req.MaxTotalTokens != 600000 || req.ToolTimeout != 15*time.Second || req.WallClock != 8*time.Minute {
		t.Fatalf("budget = %+v", req)
	}
	if len(req.OutputSchema) == 0 || len(req.Tools) != 3 || req.Instructions == "" || req.Prompt == "" {
		t.Fatal("request incomplete")
	}
}

func containsKey(raw json.RawMessage, key string) bool {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	_, ok := m[key]
	return ok
}

func TestStartRejectsSecondRunForSameUser(t *testing.T) {
	f := newFixture(t, readyRuntime([]airuntime.FakeStep{{WaitForCancel: true}}, ""))
	first, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Start(context.Background(), f.sc, f.wk)
	var already *AlreadyRunningError
	if !errors.As(err, &already) || !errors.Is(err, ErrAlreadyRunning) || already.RunID != first.ID {
		t.Fatalf("err = %v", err)
	}
}

func TestStartMapsDatabaseUniqueViolation(t *testing.T) {
	f := newFixture(t, readyRuntime(nil, goodOutput))
	// A running row the service does not know about, e.g. written by another instance.
	id, _ := f.st.CreateAIReportRun(context.Background(), &store.AIReportRun{DashboardUserID: 1, Week: f.wk.ID, Status: store.AIRunRunning})
	_, err := f.svc.Start(context.Background(), f.sc, f.wk)
	var already *AlreadyRunningError
	if !errors.As(err, &already) || already.RunID != id {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelKeepsPreviousReport(t *testing.T) {
	f := newFixture(t, readyRuntime(nil, goodOutput))
	first, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, f.st, first.ID, store.AIRunCompleted)

	secondRT := readyRuntime([]airuntime.FakeStep{{WaitForCancel: true}}, "")
	f.svc.runtimes, f.svc.byKey = []airuntime.Runtime{secondRT}, map[string]airuntime.Runtime{DefaultRuntimeKey: secondRT}
	second, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Cancel(context.Background(), Scope{DashboardUserID: 2, UserID: "other"}, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's cancel err = %v", err)
	}
	if err := f.svc.Cancel(context.Background(), f.sc, second.ID); err != nil {
		t.Fatal(err)
	}
	canceled := waitRun(t, f.st, second.ID, store.AIRunCanceled)
	if canceled.ErrorCode != "canceled" {
		t.Fatalf("code = %q", canceled.ErrorCode)
	}
	rep, _ := f.st.GetAIReport(context.Background(), 1, f.wk.ID)
	if rep == nil || rep.RunID != first.ID {
		t.Fatalf("previous report lost: %+v", rep)
	}
	if err := f.svc.Cancel(context.Background(), f.sc, second.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("cancel finished run err = %v", err)
	}
}

func TestInvalidOutputFailsWithoutReport(t *testing.T) {
	f := newFixture(t, readyRuntime(nil, `not json`))
	run, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitRun(t, f.st, run.ID, store.AIRunFailed)
	if failed.ErrorCode != "invalid_output" {
		t.Fatalf("code = %q", failed.ErrorCode)
	}
	if rep, _ := f.st.GetAIReport(context.Background(), 1, f.wk.ID); rep != nil {
		t.Fatalf("report written: %+v", rep)
	}
}

func TestRuntimeErrorsMapToCodes(t *testing.T) {
	for err, code := range map[error]string{
		airuntime.ErrTimeLimit:                    "time_limit",
		airuntime.ErrBudget:                       "budget",
		airuntime.ErrUnexpectedTool:               "unexpected_tool",
		&airuntime.RunError{Code: "auth_expired"}: "auth_expired",
		errors.New("boom"):                        "runtime_error",
	} {
		rt := readyRuntime(nil, "")
		rt.Err = err
		f := newFixture(t, rt)
		run, startErr := f.svc.Start(context.Background(), f.sc, f.wk)
		if startErr != nil {
			t.Fatal(startErr)
		}
		if got := waitRun(t, f.st, run.ID, store.AIRunFailed); got.ErrorCode != code {
			t.Errorf("%v -> %q, want %q", err, got.ErrorCode, code)
		}
	}
}

func TestStartPreconditions(t *testing.T) {
	ctx := context.Background()

	f := newFixture(t, nil)
	if _, err := f.svc.Start(ctx, f.sc, f.wk); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("nil runtime err = %v", err)
	}

	rt := readyRuntime(nil, goodOutput)
	rt.StatusValue = airuntime.Status{Configured: true, Reason: "인증 만료"}
	f = newFixture(t, rt)
	if _, err := f.svc.Start(ctx, f.sc, f.wk); !errors.Is(err, airuntime.ErrUnavailable) {
		t.Fatalf("unavailable err = %v", err)
	}

	f = newFixture(t, readyRuntime(nil, goodOutput))
	noConsent := Scope{DashboardUserID: 2, UserID: "me"}
	if _, err := f.svc.Start(ctx, noConsent, f.wk); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("no consent err = %v", err)
	}
	_ = f.st.UpsertAIConsent(ctx, 2, testRuntimeKey, "old-version")
	if _, err := f.svc.Start(ctx, noConsent, f.wk); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("stale consent err = %v", err)
	}

	empty := Scope{DashboardUserID: 1, UserID: "nobody"}
	if _, err := f.svc.Start(ctx, empty, f.wk); !errors.Is(err, ErrNoRecords) {
		t.Fatalf("no records err = %v", err)
	}
	if len(f.st.Runs) != 0 {
		t.Fatalf("rejected starts created runs: %d", len(f.st.Runs))
	}
}

func TestRecoverOnBootFailsRunningRuns(t *testing.T) {
	f := newFixture(t, readyRuntime(nil, goodOutput))
	id, _ := f.st.CreateAIReportRun(context.Background(), &store.AIReportRun{DashboardUserID: 1, Week: f.wk.ID, Status: store.AIRunRunning})
	if err := f.svc.RecoverOnBoot(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, _ := f.st.GetAIReportRun(context.Background(), id)
	if run.Status != store.AIRunFailed || run.ErrorCode != "server_restarted" {
		t.Fatalf("run = %+v", run)
	}
}

func TestShutdownFailsRunningRuns(t *testing.T) {
	f := newFixture(t, readyRuntime([]airuntime.FakeStep{{WaitForCancel: true}}, ""))
	run, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	f.svc.Shutdown(ctx)
	got, _ := f.st.GetAIReportRun(context.Background(), run.ID)
	if got.Status != store.AIRunFailed || got.ErrorCode != "server_shutdown" {
		t.Fatalf("run = %+v", got)
	}
	select {
	case <-f.svc.StreamsDone():
	default:
		t.Fatal("StreamsDone not closed after Shutdown")
	}
	if _, err := f.svc.Start(context.Background(), f.sc, f.wk); !errors.Is(err, airuntime.ErrUnavailable) {
		t.Fatalf("start after shutdown err = %v", err)
	}
}

func TestSubscribeReplaysThenStreamsWithoutDuplicates(t *testing.T) {
	gate := make(chan struct{})
	release := make(chan struct{})
	rt := readyRuntime(nil, goodOutput)
	f := newFixture(t, rt)
	// Hold the run after the first tool call so a subscriber can join mid-run.
	f.svc.toolHook = func(name string) {
		if name == "compare_week" {
			close(gate)
			<-release
		}
	}
	rt.Steps = []airuntime.FakeStep{
		{CallTool: "query_segments", Args: json.RawMessage(`{}`)},
		{CallTool: "compare_week", Args: json.RawMessage(`{}`)},
		{CallTool: "read_segment", Args: json.RawMessage(`{"segment_id":"10"}`)},
	}
	run, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	<-gate
	ch, unsub := f.svc.Subscribe(run.ID, 1)
	defer unsub()
	close(release)

	seen := map[string]int{}
	for ev := range ch {
		seen[string(ev.Kind)+itoa(ev.Seq)]++
	}
	if seen["tool_call1"] != 0 || seen["tool_result1"] != 0 {
		t.Fatalf("replayed events at or before afterSeq: %v", seen)
	}
	for _, k := range []string{"tool_call2", "tool_result2", "tool_call3", "tool_result3"} {
		if seen[k] != 1 {
			t.Fatalf("%s seen %d times: %v", k, seen[k], seen)
		}
	}
	waitRun(t, f.st, run.ID, store.AIRunCompleted)
}

func TestConsentStateAndGrant(t *testing.T) {
	f := newFixture(t, readyRuntime(nil, goodOutput))
	ctx := context.Background()
	other := Scope{DashboardUserID: 5, UserID: "x"}
	cs, err := f.svc.Consent(ctx, other)
	if err != nil || cs.Granted || cs.RuntimeKey != testRuntimeKey || cs.DisclosureVersion != DisclosureVersion {
		t.Fatalf("consent = %+v err=%v", cs, err)
	}
	if err := f.svc.GrantConsent(ctx, other, "codex-app-server:api_key", DisclosureVersion); !errors.Is(err, ErrConsentMismatch) {
		t.Fatalf("mismatch err = %v", err)
	}
	if err := f.svc.GrantConsent(ctx, other, testRuntimeKey, DisclosureVersion); err != nil {
		t.Fatal(err)
	}
	cs, _ = f.svc.Consent(ctx, other)
	if !cs.Granted || cs.GrantedAt == nil {
		t.Fatalf("consent after grant = %+v", cs)
	}

	unconfigured := newFixture(t, nil)
	if err := unconfigured.svc.GrantConsent(ctx, other, "codex-app-server:none", DisclosureVersion); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("unconfigured grant err = %v", err)
	}
}

// A driver that reports tokens only through events must not have them erased
// by a Result without usage.
func TestCompletionKeepsStreamedUsage(t *testing.T) {
	rt := readyRuntime([]airuntime.FakeStep{{Event: &airuntime.Event{Kind: airuntime.EventUsage, Usage: &airuntime.Usage{Reported: true, InputTokens: 77}}}}, goodOutput)
	rt.Usage = airuntime.Usage{}
	f := newFixture(t, rt)
	run, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitRun(t, f.st, run.ID, store.AIRunCompleted); !got.Usage.Reported || got.Usage.InputTokens != 77 {
		t.Fatalf("usage = %+v", got.Usage)
	}
}

type panicRuntime struct{ *airuntime.FakeRuntime }

func (p panicRuntime) Run(context.Context, airuntime.RunRequest, func(airuntime.Event)) (*airuntime.Result, error) {
	panic("driver bug")
}

func TestRuntimePanicFailsRunAndFreesSlot(t *testing.T) {
	f := newFixture(t, panicRuntime{readyRuntime(nil, goodOutput)})
	f.svc.cfg.MaxConcurrent = 1
	f.svc.sem = make(chan struct{}, 1)
	for i := 0; i < 2; i++ {
		run, err := f.svc.Start(context.Background(), f.sc, f.wk)
		if err != nil {
			t.Fatal(err)
		}
		if got := waitRun(t, f.st, run.ID, store.AIRunFailed); got.ErrorCode != "protocol_error" {
			t.Fatalf("code = %q", got.ErrorCode)
		}
		deadline := time.Now().Add(time.Second)
		for {
			f.svc.mu.Lock()
			busy := len(f.svc.byUser) > 0
			f.svc.mu.Unlock()
			if !busy || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

// A run whose goroutine is gone (a failed final write) must still be cancelable,
// or the user is locked out of new runs until a restart.
func TestCancelOrphanedRunningRun(t *testing.T) {
	f := newFixture(t, readyRuntime(nil, goodOutput))
	id, _ := f.st.CreateAIReportRun(context.Background(), &store.AIReportRun{DashboardUserID: 1, Week: f.wk.ID, Status: store.AIRunRunning})
	if err := f.svc.Cancel(context.Background(), f.sc, id); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetAIReportRun(context.Background(), id); got.Status != store.AIRunCanceled {
		t.Fatalf("run = %+v", got)
	}
}

// The report key has no time zone, so item metadata must be read in the window
// the report was generated for, not the one the viewer asked with.
func TestWeekViewReadsItemsInReportWindow(t *testing.T) {
	f := newFixture(t, readyRuntime(nil, goodOutput))
	ctx := context.Background()
	seoul, err := ParseISOWeek("2026-W37", "Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	// Monday 03:00 Seoul is Sunday 18:00 UTC: inside the Seoul week, before the UTC one.
	early := store.AISegment{ID: 42, StartTs: seoul.Since.Add(3 * time.Hour)}
	f.st.AddSegment("me", early)
	id, _ := f.st.CreateAIReportRun(ctx, &store.AIReportRun{DashboardUserID: 1, Week: seoul.ID, Status: store.AIRunRunning})
	_ = f.st.CompleteAIReportRun(ctx, id, store.AIReport{DashboardUserID: 1, Week: seoul.ID, TZ: seoul.TZ, Since: seoul.Since, Until: seoul.Until,
		Items: []store.AIReportItem{{SegmentID: "42", Title: "t", Reason: "r"}}}, store.AIUsage{}, 0)

	utc := f.wk // 2026-W37 in UTC
	view, err := f.svc.WeekView(ctx, f.sc, utc)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Segments[42]; !ok {
		t.Fatalf("item dropped when viewed in another tz: %v", view.Segments)
	}
}

func TestGetRunIsOwnerOnly(t *testing.T) {
	f := newFixture(t, readyRuntime(nil, goodOutput))
	id, _ := f.st.CreateAIReportRun(context.Background(), &store.AIReportRun{DashboardUserID: 1, Week: f.wk.ID, Status: store.AIRunCompleted})
	if _, err := f.svc.GetRun(context.Background(), Scope{DashboardUserID: 2}, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := f.svc.GetRun(context.Background(), f.sc, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	if run, err := f.svc.GetRun(context.Background(), f.sc, id); err != nil || run.ID != id {
		t.Fatalf("own run = %+v err=%v", run, err)
	}
}

// blockingStore holds CompleteAIReportRun until released, so a test can act in
// the window between the run's validated answer and its final write.
type blockingStore struct {
	*MemStore
	entered, release chan struct{}
}

func (b *blockingStore) CompleteAIReportRun(ctx context.Context, runID int64, r store.AIReport, u store.AIUsage, dropped int) error {
	close(b.entered)
	<-b.release
	return b.MemStore.CompleteAIReportRun(ctx, runID, r, u, dropped)
}

// A cancel that arrives while the finished report is being written must not
// throw the report away or record the run as canceled.
func TestCancelDuringFinalWriteKeepsCompletedReport(t *testing.T) {
	base := newFixture(t, nil)
	bs := &blockingStore{MemStore: base.st, entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewService(bs, []airuntime.Runtime{readyRuntime(nil, goodOutput)}, Config{EnvRuntime: DefaultRuntimeKey})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		svc.Shutdown(ctx)
	})
	run, err := svc.Start(context.Background(), base.sc, base.wk)
	if err != nil {
		t.Fatal(err)
	}
	<-bs.entered
	if err := svc.Cancel(context.Background(), base.sc, run.ID); !errors.Is(err, ErrNotRunning) {
		t.Errorf("cancel during final write err = %v, want ErrNotRunning", err)
	}
	close(bs.release)
	done := waitRun(t, base.st, run.ID, store.AIRunCompleted)
	if done.ErrorCode != "" {
		t.Fatalf("run = %+v", done)
	}
	if rep, _ := base.st.GetAIReport(context.Background(), 1, base.wk.ID); rep == nil || rep.RunID != run.ID {
		t.Fatalf("report = %+v", rep)
	}
}

// The database refuses a second running run for the user in any week; 409 must
// still name that run, not 0.
func TestStartNamesRunningRunOfAnotherWeek(t *testing.T) {
	f := newFixture(t, readyRuntime(nil, goodOutput))
	id, _ := f.st.CreateAIReportRun(context.Background(), &store.AIReportRun{DashboardUserID: 1, Week: "2026-W30", Status: store.AIRunRunning})
	_, err := f.svc.Start(context.Background(), f.sc, f.wk)
	var already *AlreadyRunningError
	if !errors.As(err, &already) || already.RunID != id {
		t.Fatalf("err = %v, want run %d", err, id)
	}
}

// The runtime's resolved model is recorded when no model was configured.
func TestCompletionRecordsRuntimeModel(t *testing.T) {
	rt := readyRuntime(nil, goodOutput)
	rt.InfoValue.Model = ""
	rt.ResultModel = "gpt-resolved"
	f := newFixture(t, rt)
	run, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	if got := waitRun(t, f.st, run.ID, store.AIRunCompleted); got.Model != "gpt-resolved" {
		t.Fatalf("model = %q", got.Model)
	}
}

// Consent to the previous disclosure does not cover the current one.
func TestStartRefusesConsentToPreviousDisclosure(t *testing.T) {
	ctx := context.Background()
	rt := readyRuntime(nil, goodOutput)
	f := newFixture(t, rt)
	if err := f.st.UpsertAIConsent(ctx, 1, testRuntimeKey, "2026-09-14"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Start(ctx, f.sc, f.wk); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("start with 2026-09-14 consent: %v", err)
	}
	if len(rt.Requests()) != 0 {
		t.Fatal("the runtime ran without current consent")
	}
}
