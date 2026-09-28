package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
	"cctrace/internal/store"
)

var aiCaller = &auth.DashboardUser{ID: 1, Email: "me@example.com", Role: "user", CctraceUserID: "caller"}

func aiRuntime(steps ...airuntime.FakeStep) *airuntime.FakeRuntime {
	return &airuntime.FakeRuntime{
		InfoValue:   airuntime.Info{Key: "codex-app-server", Model: "gpt-test", AuthMode: airuntime.AuthModeChatGPT},
		StatusValue: airuntime.Status{Configured: true, Available: true, AccountEmail: "ops@example.com", PlanType: "pro"},
		Steps:       steps,
		FinalText:   `{"summary":"요약","items":[]}`,
	}
}

type aiTest struct {
	srv *Server
	st  *aireport.MemStore
	svc *aireport.Service
	wk  aireport.Week
}

// newAITest serves as user, with the dashboard middleware replaced by one that
// signs the request in.
func newAITest(t *testing.T, rt airuntime.Runtime, user *auth.DashboardUser) *aiTest {
	t.Helper()
	st := aireport.NewMemStore()
	var runtime airuntime.Runtime // stays a nil interface when rt is nil
	if rt != nil {
		runtime = rt
	}
	svc := aireport.NewService(st, []airuntime.Runtime{runtime}, aireport.Config{EnvRuntime: aireport.DefaultRuntimeKey})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		svc.Shutdown(ctx)
	})
	signIn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
		})
	}
	srv := newServer(&mockStore{}, nil, signIn).WithAIReports(svc)
	wk, err := aireport.ParseISOWeek("2026-W37", "Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	return &aiTest{srv: srv, st: st, svc: svc, wk: wk}
}

func (a *aiTest) do(method, path, body string, csrf bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if csrf {
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	rec := httptest.NewRecorder()
	a.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func (a *aiTest) consent(t *testing.T, userID int64) {
	t.Helper()
	if err := a.st.UpsertAIConsent(context.Background(), userID, "codex-app-server:chatgpt", aireport.DisclosureVersion); err != nil {
		t.Fatal(err)
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("status %d body %q: %v", rec.Code, rec.Body.String(), err)
	}
	return out
}

func TestGetAIReportsUsesCallerScopeAndFillsMeta(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	in := a.wk.Since.Add(time.Hour)
	a.st.AddSegment("caller", store.AISegment{ID: 10, SessionID: "sess", StartTs: in, ProjectName: "cctrace", Agent: "claude", TypedTurnCount: 18, ToolCallCount: 41, ToolFailCount: 3})
	a.st.AddSegment("caller", store.AISegment{ID: 11, StartTs: in})
	a.st.AddSegment("someone", store.AISegment{ID: 12, StartTs: in})

	ctx := context.Background()
	runID, _ := a.st.CreateAIReportRun(ctx, &store.AIReportRun{DashboardUserID: 1, Week: a.wk.ID, Status: store.AIRunRunning, Runtime: "codex-app-server", Model: "gpt-test", StartedAt: time.Now().Add(-time.Minute)})
	_ = a.st.AppendAIToolCall(ctx, runID, store.AIToolCall{Seq: 1, Tool: "read_segment", Args: json.RawMessage(`{"segment_id":"10"}`), Status: "running"})
	_ = a.st.FinishAIToolCall(ctx, runID, 1, "ok", 4, 0, 30)
	_ = a.st.AppendAIToolCall(ctx, runID, store.AIToolCall{Seq: 2, Tool: "read_segment", Args: json.RawMessage(`{"segment_id":"10"}`), Status: "running"})
	_ = a.st.CompleteAIReportRun(ctx, runID, store.AIReport{DashboardUserID: 1, Week: a.wk.ID, TZ: "Asia/Seoul", Summary: "요약",
		Items: []store.AIReportItem{{SegmentID: "10", Title: "t", Reason: "r"}, {SegmentID: "99", Title: "gone", Reason: "r"}}}, store.AIUsage{}, 0)

	rec := a.do(http.MethodGet, "/api/ai-reports?week=2026-W37&tz=Asia/Seoul", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["segment_count"] != 2.0 || body["week"] != "2026-W37" || body["tz"] != "Asia/Seoul" {
		t.Fatalf("body = %v", body)
	}
	rt := body["runtime"].(map[string]any)
	if rt["configured"] != true || rt["available"] != true || rt["reason"] != nil || rt["key"] != "codex-app-server" {
		t.Fatalf("runtime = %v", rt)
	}
	consent := body["consent"].(map[string]any)
	if consent["granted"] != false || consent["runtime_key"] != "codex-app-server:chatgpt" {
		t.Fatalf("consent = %v", consent)
	}
	run := body["run"].(map[string]any)
	if run["status"] != "completed" || run["error"] != nil || len(run["tool_calls"].([]any)) != 2 {
		t.Fatalf("run = %v", run)
	}
	report := body["report"].(map[string]any)
	items := report["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("invisible item not dropped: %v", items)
	}
	meta := items[0].(map[string]any)["meta"].(map[string]any)
	if meta["project_name"] != "cctrace" || meta["tool_fail_count"] != 3.0 || meta["session_id"] != "sess" {
		t.Fatalf("meta = %v", meta)
	}
	usage := report["usage"].(map[string]any)
	if usage["reported"] != false || usage["input_tokens"] != nil {
		t.Fatalf("unreported usage = %v", usage)
	}
	process := report["process"].(map[string]any)
	if process["segments_read"] != 1.0 || len(process["tool_calls"].([]any)) != 2 {
		t.Fatalf("process = %v", process)
	}
	if report["runtime"] != "codex-app-server" || report["model"] != "gpt-test" {
		t.Fatalf("report = %v", report)
	}
}

func TestGetAIReportsEmptyWeekAndBadInput(t *testing.T) {
	a := newAITest(t, nil, aiCaller)
	rec := a.do(http.MethodGet, "/api/ai-reports?week=2026-W37", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["run"] != nil || body["report"] != nil || body["tz"] != "UTC" {
		t.Fatalf("body = %v", body)
	}
	if rt := body["runtime"].(map[string]any); rt["configured"] != false {
		t.Fatalf("runtime = %v", rt)
	}
	for _, q := range []string{"", "?week=2026-37", "?week=2026-W37&tz=Nowhere/City"} {
		rec := a.do(http.MethodGet, "/api/ai-reports"+q, "", false)
		if rec.Code != http.StatusBadRequest || decodeBody(t, rec)["error"] != "invalid_week" {
			t.Errorf("%q: status %d body %s", q, rec.Code, rec.Body.String())
		}
	}
}

func TestAIReportsWithoutServiceIsUnconfigured(t *testing.T) {
	srv := newTestServer(&mockStore{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/ai-reports?week=2026-W37", nil)
	req = req.WithContext(auth.WithUser(req.Context(), aiCaller))
	rec := httptest.NewRecorder()
	srv.handleGetAIReports(rec, req)
	if rec.Code != http.StatusServiceUnavailable || decodeBody(t, rec)["error"] != "runtime_unconfigured" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestPostAIReportsRequiresCSRF(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	rec := a.do(http.MethodPost, "/api/ai-reports", `{"week":"2026-W37","tz":"Asia/Seoul"}`, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
	if len(a.st.Runs) != 0 {
		t.Fatal("run created without CSRF")
	}
}

func TestPostAIReportsStartsRun(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	a.consent(t, 1)
	a.st.AddSegment("caller", store.AISegment{ID: 10, StartTs: a.wk.Since.Add(time.Hour)})
	rec := a.do(http.MethodPost, "/api/ai-reports", `{"week":"2026-W37","tz":"Asia/Seoul"}`, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	run := decodeBody(t, rec)["run"].(map[string]any)
	if run["status"] != "running" || run["id"] == nil {
		t.Fatalf("run = %v", run)
	}
	stored, _ := a.st.GetAIReportRun(context.Background(), int64(run["id"].(float64)))
	if stored.ScopeUserID != "caller" || stored.ScopeProfileEmail != "" || stored.TZ != "Asia/Seoul" {
		t.Fatalf("stored scope = %+v", stored)
	}
}

func TestPostAIReportsErrorCodes(t *testing.T) {
	future := time.Now().AddDate(0, 0, 14)
	fy, fw := future.ISOWeek()
	futureWeek := fmt.Sprintf("%04d-W%02d", fy, fw)

	unavailable := aiRuntime()
	unavailable.StatusValue = airuntime.Status{Configured: true, Reason: "인증 만료"}

	hold := aiRuntime(airuntime.FakeStep{WaitForCancel: true})

	cases := []struct {
		name   string
		rt     airuntime.Runtime
		setup  func(t *testing.T, a *aiTest)
		body   string
		status int
		code   string
	}{
		{"invalid week", aiRuntime(), nil, `{"week":"W37"}`, 400, "invalid_week"},
		{"bad json", aiRuntime(), nil, `{`, 400, "invalid_week"},
		{"future week", aiRuntime(), nil, `{"week":"` + futureWeek + `"}`, 400, "future_week"},
		{"unconfigured", nil, nil, `{"week":"2026-W37"}`, 503, "runtime_unconfigured"},
		{"unavailable", unavailable, nil, `{"week":"2026-W37"}`, 503, "runtime_unavailable"},
		{"consent", aiRuntime(), nil, `{"week":"2026-W37"}`, 403, "consent_required"},
		{"no records", aiRuntime(), func(t *testing.T, a *aiTest) { a.consent(t, 1) }, `{"week":"2026-W37"}`, 422, "no_records"},
		{"already running", hold, func(t *testing.T, a *aiTest) {
			a.consent(t, 1)
			a.st.AddSegment("caller", store.AISegment{ID: 10, StartTs: a.wk.Since.Add(time.Hour)})
			if _, err := a.svc.Start(context.Background(), aireport.Scope{DashboardUserID: 1, UserID: "caller"}, a.wk); err != nil {
				t.Fatal(err)
			}
		}, `{"week":"2026-W37","tz":"Asia/Seoul"}`, 409, "already_running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAITest(t, tc.rt, aiCaller)
			if tc.setup != nil {
				tc.setup(t, a)
			}
			rec := a.do(http.MethodPost, "/api/ai-reports", tc.body, true)
			body := decodeBody(t, rec)
			if rec.Code != tc.status || body["error"] != tc.code {
				t.Fatalf("status %d body %v", rec.Code, body)
			}
			if tc.code == "already_running" && body["run_id"] != 1.0 {
				t.Fatalf("run_id = %v", body["run_id"])
			}
		})
	}
}

func TestOtherUsersRunIsNotFound(t *testing.T) {
	admin := &auth.DashboardUser{ID: 2, Email: "admin@example.com", Role: "admin", CctraceUserID: "admin"}
	a := newAITest(t, aiRuntime(), admin)
	id, _ := a.st.CreateAIReportRun(context.Background(), &store.AIReportRun{DashboardUserID: 1, Week: "2026-W37", Status: store.AIRunRunning})
	path := "/api/ai-reports/runs/" + aiItoa(id)
	if rec := a.do(http.MethodGet, path+"/events", "", false); rec.Code != http.StatusNotFound {
		t.Fatalf("events status %d", rec.Code)
	}
	if rec := a.do(http.MethodDelete, path, "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("delete status %d", rec.Code)
	}
	if rec := a.do(http.MethodGet, "/api/ai-reports/runs/abc/events", "", false); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id status %d", rec.Code)
	}
}

func TestDeleteRun(t *testing.T) {
	a := newAITest(t, aiRuntime(airuntime.FakeStep{WaitForCancel: true}), aiCaller)
	a.consent(t, 1)
	a.st.AddSegment("caller", store.AISegment{ID: 10, StartTs: a.wk.Since.Add(time.Hour)})
	run, err := a.svc.Start(context.Background(), aireport.Scope{DashboardUserID: 1, UserID: "caller"}, a.wk)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/ai-reports/runs/" + aiItoa(run.ID)
	if rec := a.do(http.MethodDelete, path, "", false); rec.Code != http.StatusForbidden {
		t.Fatalf("delete without CSRF status %d", rec.Code)
	}
	rec := a.do(http.MethodDelete, path, "", true)
	if rec.Code != http.StatusAccepted || decodeBody(t, rec)["status"] != "canceling" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := a.st.GetAIReportRun(context.Background(), run.ID)
		if got.Status == store.AIRunCanceled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run = %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec := a.do(http.MethodDelete, path, "", true); rec.Code != http.StatusConflict {
		t.Fatalf("second delete status %d", rec.Code)
	}
}

type sseFrame struct {
	id, event, data string
}

func aiItoa(n int64) string { return strconv.FormatInt(n, 10) }

func readSSE(t *testing.T, resp *http.Response, stop func(sseFrame) bool) []sseFrame {
	t.Helper()
	var frames []sseFrame
	var cur sseFrame
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur.event != "" || cur.data != "" {
				frames = append(frames, cur)
				if stop != nil && stop(cur) {
					return frames
				}
			}
			cur = sseFrame{}
		case strings.HasPrefix(line, ":"):
			frames = append(frames, sseFrame{event: "comment", data: line})
			if stop != nil && stop(frames[len(frames)-1]) {
				return frames
			}
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return frames
}

func openStream(t *testing.T, ts *httptest.Server, runID int64, header http.Header) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/ai-reports/runs/"+aiItoa(runID)+"/events", nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestRunEventsReplaysFinishedRunAndCloses(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	ctx := context.Background()
	id, _ := a.st.CreateAIReportRun(ctx, &store.AIReportRun{DashboardUserID: 1, Week: a.wk.ID, Status: store.AIRunRunning})
	_ = a.st.AppendAIToolCall(ctx, id, store.AIToolCall{Seq: 1, Tool: "query_segments", Args: json.RawMessage(`{"limit":20}`), Status: "running"})
	_ = a.st.FinishAIToolCall(ctx, id, 1, "ok", 20, 0, 41)
	_ = a.st.AppendAIToolCall(ctx, id, store.AIToolCall{Seq: 2, Tool: "compare_week", Args: json.RawMessage(`{}`), Status: "running"})
	_ = a.st.FinishAIToolCall(ctx, id, 2, "timeout", 0, 0, 15000)
	_ = a.st.CompleteAIReportRun(ctx, id, store.AIReport{DashboardUserID: 1, Week: a.wk.ID, Items: []store.AIReportItem{}}, store.AIUsage{}, 0)

	ts := httptest.NewServer(a.srv.Handler())
	defer ts.Close()
	resp := openStream(t, ts, id, http.Header{"Last-Event-Id": {"1"}})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("status %d headers %v", resp.StatusCode, resp.Header)
	}
	frames := readSSE(t, resp, nil)
	var names []string
	for _, f := range frames {
		names = append(names, f.event)
	}
	if strings.Join(names, ",") != "tool_call,tool_result,status,report" {
		t.Fatalf("frames = %+v", frames)
	}
	// id: marks a finished call only, so Last-Event-ID never skips a pending result.
	if frames[0].id != "" || frames[1].id != "2" || !strings.Contains(frames[1].data, `"status":"timeout"`) || !strings.Contains(frames[1].data, `"duration_ms":15000`) {
		t.Fatalf("tool frames = %+v", frames[:2])
	}
	if frames[2].data != `{"status":"completed"}` || frames[3].data != `{"run_id":`+aiItoa(id)+`}` {
		t.Fatalf("terminal frames = %+v", frames[2:])
	}
}

func TestRunEventsStreamsLiveRunUntilTerminal(t *testing.T) {
	old := sseKeepAlive
	sseKeepAlive = 20 * time.Millisecond
	defer func() { sseKeepAlive = old }()

	rt := aiRuntime(
		airuntime.FakeStep{CallTool: "query_segments", Args: json.RawMessage(`{"limit":3}`)},
		airuntime.FakeStep{WaitForCancel: true},
	)
	a := newAITest(t, rt, aiCaller)
	a.consent(t, 1)
	a.st.AddSegment("caller", store.AISegment{ID: 10, StartTs: a.wk.Since.Add(time.Hour)})
	run, err := a.svc.Start(context.Background(), aireport.Scope{DashboardUserID: 1, UserID: "caller"}, a.wk)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(a.srv.Handler())
	defer ts.Close()
	resp := openStream(t, ts, run.ID, nil)

	var sawResult, sawPing, canceled bool
	frames := readSSE(t, resp, func(f sseFrame) bool {
		sawResult = sawResult || f.event == "tool_result"
		sawPing = sawPing || (f.event == "comment" && f.data == ": ping")
		if sawResult && sawPing && !canceled {
			canceled = true
			if err := a.svc.Cancel(context.Background(), aireport.Scope{DashboardUserID: 1}, run.ID); err != nil {
				t.Errorf("cancel: %v", err)
			}
		}
		return f.event == "status"
	})
	if !sawPing {
		t.Fatal("no keep-alive ping")
	}
	counts := map[string]int{}
	for _, f := range frames {
		counts[f.event]++
	}
	last := frames[len(frames)-1]
	if counts["tool_call"] != 1 || counts["tool_result"] != 1 || last.event != "status" || !strings.HasPrefix(last.data, `{"status":"canceled","error":{"code":"canceled","message":"`) {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestRunEventsResumeAfterPendingCallKeepsItsResult(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	ctx := context.Background()
	id, _ := a.st.CreateAIReportRun(ctx, &store.AIReportRun{DashboardUserID: 1, Week: a.wk.ID, Status: store.AIRunRunning})
	_ = a.st.AppendAIToolCall(ctx, id, store.AIToolCall{Seq: 1, Tool: "query_segments", Args: json.RawMessage(`{}`), Status: "running"})
	_ = a.st.FinishAIToolCall(ctx, id, 1, "ok", 1, 0, 5)
	_ = a.st.AppendAIToolCall(ctx, id, store.AIToolCall{Seq: 2, Tool: "compare_week", Args: json.RawMessage(`{}`), Status: "running"})
	_ = a.st.FinishAIToolCall(ctx, id, 2, "ok", 1, 0, 5)
	_ = a.st.FailAIReportRun(ctx, id, store.AIRunFailed, "time_limit", "")

	ts := httptest.NewServer(a.srv.Handler())
	defer ts.Close()
	// The client last saw id 1 (seq 1 finished) and seq 2's call without an id.
	frames := readSSE(t, openStream(t, ts, id, http.Header{"Last-Event-Id": {"1"}}), nil)
	var results int
	for _, f := range frames {
		if f.event == "tool_result" && strings.Contains(f.data, `"seq":2`) {
			results++
		}
	}
	if results != 1 {
		t.Fatalf("frames = %+v", frames)
	}
}

// A run left running with no goroutine behind it (its final write failed) must
// not hold a stream open forever re-reading the database.
func TestRunEventsGiveUpOnOrphanedRun(t *testing.T) {
	old := sseResubscribeWait
	sseResubscribeWait = 10 * time.Millisecond
	defer func() { sseResubscribeWait = old }()

	a := newAITest(t, aiRuntime(), aiCaller)
	id, _ := a.st.CreateAIReportRun(context.Background(), &store.AIReportRun{DashboardUserID: 1, Week: a.wk.ID, Status: store.AIRunRunning})
	ts := httptest.NewServer(a.srv.Handler())
	defer ts.Close()
	resp := openStream(t, ts, id, nil)
	done := make(chan []sseFrame)
	go func() { done <- readSSE(t, resp, nil) }()
	select {
	case frames := <-done:
		for _, f := range frames {
			if f.event == "status" {
				t.Fatalf("orphaned run must not be reported terminal: %+v", frames)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream never ended")
	}
}

func TestRunEventsEndOnServerShutdown(t *testing.T) {
	a := newAITest(t, aiRuntime(airuntime.FakeStep{WaitForCancel: true}), aiCaller)
	a.consent(t, 1)
	a.st.AddSegment("caller", store.AISegment{ID: 10, StartTs: a.wk.Since.Add(time.Hour)})
	run, err := a.svc.Start(context.Background(), aireport.Scope{DashboardUserID: 1, UserID: "caller"}, a.wk)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(a.srv.Handler())
	defer ts.Close()
	resp := openStream(t, ts, run.ID, nil)
	a.svc.CloseStreams()
	done := make(chan struct{})
	go func() {
		readSSE(t, resp, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream stayed open after CloseStreams")
	}
}

// Tool results can finish out of seq order. id: must stay at the highest seq
// below which every result was sent, or a reconnect with that Last-Event-ID
// replays only later seqs and loses the earlier call's result.
func TestSSEWriterIDIsContiguousCompletionWatermark(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &sseWriter{w: rec, rc: http.NewResponseController(rec), sent: map[string]bool{}}
	sw.toolCall(1, "query_segments", map[string]any{})
	sw.toolCall(2, "compare_week", map[string]any{})
	sw.toolCall(3, "read_segment", map[string]any{})
	one, two, three := 1, 2, 3
	sw.toolResult(2, "ok", &two, &two)
	sw.toolResult(1, "ok", &one, &one)
	sw.toolResult(3, "ok", &three, &three)

	resp := &http.Response{Body: io.NopCloser(strings.NewReader(rec.Body.String()))}
	var ids []string
	for _, f := range readSSE(t, resp, nil) {
		if f.event == "tool_result" {
			ids = append(ids, f.id)
		}
	}
	if strings.Join(ids, ",") != ",2,3" {
		t.Fatalf("tool_result ids = %q, want no id while seq 1 is pending, then 2, then 3", ids)
	}
}

// A stream resumed after Last-Event-ID starts its watermark there.
func TestSSEWriterWatermarkStartsAtAfterSeq(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &sseWriter{w: rec, rc: http.NewResponseController(rec), sent: map[string]bool{}, watermark: 4}
	six, five := 6, 5
	sw.toolResult(6, "ok", &six, &six)
	sw.toolResult(5, "ok", &five, &five)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(rec.Body.String()))}
	var ids []string
	for _, f := range readSSE(t, resp, nil) {
		ids = append(ids, f.id)
	}
	if strings.Join(ids, ",") != "4,6" {
		t.Fatalf("ids = %q, want 4 then 6", ids)
	}
}

// Runtime and run errors carry process output, paths and protocol text. Users
// get a code and a fixed sentence; the raw text stays in the log and admin API.
func TestAIReportsDoNotExposeInternalErrorText(t *testing.T) {
	const raw = "account/rateLimits/read: codex app-server error -32601: /srv/secret/path"
	rt := aiRuntime()
	rt.StatusValue = airuntime.Status{Configured: true, Reason: raw}
	a := newAITest(t, rt, aiCaller)
	ctx := context.Background()
	id, _ := a.st.CreateAIReportRun(ctx, &store.AIReportRun{DashboardUserID: 1, Week: a.wk.ID, Status: store.AIRunRunning})
	_ = a.st.FailAIReportRun(ctx, id, store.AIRunFailed, "runtime_unavailable", raw)

	rec := a.do(http.MethodGet, "/api/ai-reports?week=2026-W37&tz=Asia/Seoul", "", false)
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("GET exposed internal text: %s", rec.Body.String())
	}
	body := decodeBody(t, rec)
	if reason, _ := body["runtime"].(map[string]any)["reason"].(string); reason == "" {
		t.Errorf("unavailable runtime lost its reason: %v", body["runtime"])
	}
	runErr := body["run"].(map[string]any)["error"].(map[string]any)
	if runErr["code"] != "runtime_unavailable" || runErr["message"] == "" {
		t.Errorf("run error = %v, want the code and a fixed message", runErr)
	}

	post := a.do(http.MethodPost, "/api/ai-reports", `{"week":"2026-W37"}`, true)
	if post.Code != http.StatusServiceUnavailable || strings.Contains(post.Body.String(), "secret") {
		t.Fatalf("POST status %d exposed internal text: %s", post.Code, post.Body.String())
	}
}
