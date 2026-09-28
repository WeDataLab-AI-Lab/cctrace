package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// sseKeepAlive is the interval between ": ping" comments on a progress stream,
// so proxies do not close an idle connection during a long tool call.
var sseKeepAlive = 15 * time.Second

// sseResubscribeWait is the pause before following a run again after the live
// channel closed while the database still says running.
var sseResubscribeWait = time.Second

// sseMaxEmptyResubscribes ends a stream whose run is running in the database
// but has nothing executing it; the client falls back to polling GET.
const sseMaxEmptyResubscribes = 3

// WithAIReports attaches the weekly AI report service. Without it every AI
// report route answers 503 runtime_unconfigured.
func (s *Server) WithAIReports(svc *aireport.Service) *Server {
	s.aiReports = svc
	return s
}

// RunAIReportSchedule starts the automatic runs due at now and answers how many
// it started. The ticker calls it; now is a parameter so the decision is
// testable without waiting for a clock.
//
// A deployment with no AI runtime still runs the ticker, so a nil service is a
// quiet no-op rather than a panic.
func (s *Server) RunAIReportSchedule(ctx context.Context, now time.Time) (int, error) {
	if s.aiReports == nil {
		return 0, nil
	}
	return s.aiReports.ScheduleTick(ctx, now, s.scheduleScope)
}

// scheduleScope builds a scheduled run's scope the same way a request's is
// built, branch for branch with resolveUserAccessParams. The two must not
// drift: if they did, the weekly report that arrives by itself would quietly
// cover a different set of sessions than the one a user sees when they press
// the button. A user with no cctrace id gets the sentinel here too, so their
// automatic run finds nothing rather than everything.
func (s *Server) scheduleScope(c store.AIScheduleCandidate) aireport.Scope {
	sc := aireport.Scope{DashboardUserID: c.DashboardUserID}
	switch {
	case !s.useUserIDAccessControl:
		sc.ProfileEmail = c.Email
	case c.CctraceUserID != "":
		sc.UserID = c.CctraceUserID
	default:
		sc.UserID = noAccessSentinel
	}
	return sc
}

func writeAIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": code, "message": message})
}

// aiRequest resolves the caller's scope and the service, writing the response
// when either is missing. Runs and reports belong to the dashboard user; the
// segment scope is resolveUserAccessParams' answer, the same as other reads.
func (s *Server) aiRequest(w http.ResponseWriter, r *http.Request) (*aireport.Service, aireport.Scope, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, aireport.Scope{}, false
	}
	if s.aiReports == nil {
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "AI 런타임이 설정되지 않았습니다")
		return nil, aireport.Scope{}, false
	}
	profileEmail, _, userID := s.resolveUserAccessParams(user)
	return s.aiReports, aireport.Scope{DashboardUserID: user.ID, ProfileEmail: profileEmail, UserID: userID}, true
}

func parseAIWeek(week, tz string) (aireport.Week, bool) {
	if tz != "" && !tzRegexp.MatchString(tz) {
		return aireport.Week{}, false
	}
	wk, err := aireport.ParseISOWeek(week, tz)
	return wk, err == nil
}

// aiRuntimeJSON's Enabled is false while an admin or the environment has not
// turned AI reports on; nothing can start then.
type aiRuntimeJSON struct {
	Enabled    bool    `json:"enabled"`
	Key        string  `json:"key"`
	Configured bool    `json:"configured"`
	Available  bool    `json:"available"`
	Reason     *string `json:"reason"`
}

type aiConsentStateJSON struct {
	Granted           bool   `json:"granted"`
	RuntimeKey        string `json:"runtime_key"`
	DisclosureVersion string `json:"disclosure_version"`
}

type aiToolCallJSON struct {
	Seq        int             `json:"seq"`
	Tool       string          `json:"tool"`
	Args       json.RawMessage `json:"args"`
	Status     string          `json:"status"`
	ResultRows *int            `json:"result_rows"`
	DurationMs *int            `json:"duration_ms"`
}

type aiRunErrorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type aiRunJSON struct {
	ID         int64            `json:"id"`
	Status     string           `json:"status"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at"`
	Error      *aiRunErrorJSON  `json:"error"`
	ToolCalls  []aiToolCallJSON `json:"tool_calls"`
}

// aiUsageJSON leaves tokens null when the runtime did not report them.
type aiUsageJSON struct {
	Reported          bool   `json:"reported"`
	InputTokens       *int64 `json:"input_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
	OutputTokens      *int64 `json:"output_tokens"`
}

type aiItemMetaJSON struct {
	SessionID      string    `json:"session_id"`
	StartTs        time.Time `json:"start_ts"`
	ProjectName    string    `json:"project_name"`
	Agent          string    `json:"agent"`
	TypedTurnCount int64     `json:"typed_turn_count"`
	ToolCallCount  int64     `json:"tool_call_count"`
	ToolFailCount  int64     `json:"tool_fail_count"`
}

type aiItemJSON struct {
	SegmentID string         `json:"segment_id"`
	Title     string         `json:"title"`
	Reason    string         `json:"reason"`
	Meta      aiItemMetaJSON `json:"meta"`
}

type aiProcessJSON struct {
	ToolCalls    []aiToolCallJSON `json:"tool_calls"`
	SegmentsRead int              `json:"segments_read"`
}

type aiReportJSON struct {
	RunID       int64         `json:"run_id"`
	GeneratedAt time.Time     `json:"generated_at"`
	TZ          string        `json:"tz"`
	Runtime     string        `json:"runtime"`
	Model       string        `json:"model"`
	Usage       aiUsageJSON   `json:"usage"`
	DurationMs  int64         `json:"duration_ms"`
	Summary     string        `json:"summary"`
	Items       []aiItemJSON  `json:"items"`
	Process     aiProcessJSON `json:"process"`
}

type aiReportsResponse struct {
	Week         string             `json:"week"`
	TZ           string             `json:"tz"`
	Since        time.Time          `json:"since"`
	Until        time.Time          `json:"until"`
	InProgress   bool               `json:"in_progress"`
	Runtime      aiRuntimeJSON      `json:"runtime"`
	Consent      aiConsentStateJSON `json:"consent"`
	SegmentCount int64              `json:"segment_count"`
	// Schedule is when this week's report would run by itself, and when the next
	// firing is. The screen prints both, so a user knows a report is coming
	// without pressing anything.
	Schedule aiUserScheduleJSON `json:"schedule"`
	Run      *aiRunJSON         `json:"run"`
	Report   *aiReportJSON      `json:"report"`
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toolCallsJSON(calls []store.AIToolCall) []aiToolCallJSON {
	out := make([]aiToolCallJSON, 0, len(calls))
	for _, c := range calls {
		args := c.Args
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		out = append(out, aiToolCallJSON{Seq: c.Seq, Tool: c.Tool, Args: args, Status: c.Status, ResultRows: c.ResultRows, DurationMs: c.DurationMs})
	}
	return out
}

// aiRunErrorMessages is what a user reads for a run's error code. The stored
// error_message holds runtime output (process errors, paths, protocol text)
// and goes only to the log and operators.
var aiRunErrorMessages = map[string]string{
	"canceled":                 "사용자가 중지했습니다",
	"time_limit":               "시간 제한을 넘었습니다",
	"budget":                   "사용량 한도를 넘었습니다",
	"budget_exceeded":          "토큰 한도를 넘었습니다",
	"tool_call_limit":          "도구 호출 한도를 넘었습니다",
	"output_limit":             "응답이 너무 깁니다",
	"unexpected_tool":          "허용되지 않은 도구를 사용하려 했습니다",
	"invalid_output":           "AI 응답 형식이 올바르지 않습니다",
	"protocol_error":           "AI 런타임 응답을 해석하지 못했습니다",
	"runtime_unavailable":      "AI 런타임에 연결할 수 없습니다",
	"runtime_unconfigured":     "AI 런타임이 설정되지 않았습니다",
	"not_configured":           "AI 런타임이 설정되지 않았습니다",
	"turn_failed":              "AI 모델 요청이 실패했습니다",
	"store_error":              "결과를 저장하지 못했습니다",
	"server_shutdown":          "서버가 종료되어 중단됐습니다",
	"server_restarted":         "서버가 재시작되어 중단됐습니다",
	"auth_failed":              "AI 제공자 인증에 실패했습니다",
	"context_window_exceeded":  "대화가 모델의 컨텍스트 한도를 넘었습니다",
	"empty_report":             "모델이 빈 리포트를 반환했습니다",
	"endpoint_not_found":       "AI 런타임 주소를 찾을 수 없습니다",
	"invalid_request":          "AI 제공자가 요청을 거부했습니다",
	"model_unavailable":        "선택한 모델을 사용할 수 없습니다",
	"permission_denied":        "AI 제공자 접근 권한이 없습니다",
	"provider_unavailable":     "AI 제공자에 연결할 수 없습니다",
	"rate_limited":             "AI 제공자 요청 한도에 걸렸습니다",
	"refused":                  "모델이 요청을 거부했습니다",
	"runtime_account_changing": "AI 연결 계정을 바꾸는 중이라 리포트를 생성할 수 없습니다",
	"runtime_changing":         "런타임이 변경되어 중단됐습니다",
	"runtime_error":            "AI 런타임에서 오류가 발생했습니다",
	"schema_violation":         "AI 응답이 형식을 지키지 않았습니다",
}

// aiRuntimeUnavailableReason replaces the runtime's own reason for users.
const aiRuntimeUnavailableReason = "관리자 화면에서 원인을 확인할 수 있습니다"

func runJSON(run *store.AIReportRun, calls []store.AIToolCall) *aiRunJSON {
	out := &aiRunJSON{ID: run.ID, Status: run.Status, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, ToolCalls: toolCallsJSON(calls)}
	if run.ErrorCode != "" {
		msg, ok := aiRunErrorMessages[run.ErrorCode]
		if !ok {
			msg = "분석 중 오류가 발생했습니다"
		}
		out.Error = &aiRunErrorJSON{Code: run.ErrorCode, Message: msg}
	}
	return out
}

func usageJSON(u store.AIUsage) aiUsageJSON {
	if !u.Reported {
		return aiUsageJSON{}
	}
	return aiUsageJSON{Reported: true, InputTokens: &u.InputTokens, CachedInputTokens: &u.CachedInputTokens, OutputTokens: &u.OutputTokens}
}

// segmentsRead counts distinct segments read_segment returned successfully.
func segmentsRead(calls []store.AIToolCall) int {
	seen := map[string]bool{}
	for _, c := range calls {
		if c.Tool != "read_segment" || c.Status != store.AIToolCallOK {
			continue
		}
		var args struct {
			SegmentID string `json:"segment_id"`
		}
		if json.Unmarshal(c.Args, &args) == nil && args.SegmentID != "" {
			seen[args.SegmentID] = true
		}
	}
	return len(seen)
}

func reportJSON(v *aireport.WeekView) *aiReportJSON {
	rep := v.Report
	out := &aiReportJSON{
		RunID: rep.RunID, GeneratedAt: rep.GeneratedAt, TZ: rep.TZ, Summary: rep.Summary,
		Items:   []aiItemJSON{},
		Process: aiProcessJSON{ToolCalls: toolCallsJSON(v.ReportToolCalls), SegmentsRead: segmentsRead(v.ReportToolCalls)},
	}
	if run := v.ReportRun; run != nil {
		out.Runtime, out.Model, out.Usage = run.Runtime, run.Model, usageJSON(run.Usage)
		if run.FinishedAt != nil {
			out.DurationMs = run.FinishedAt.Sub(run.StartedAt).Milliseconds()
		}
	}
	for _, it := range rep.Items {
		id, err := strconv.ParseInt(it.SegmentID, 10, 64)
		seg, ok := v.Segments[id]
		if err != nil || !ok {
			continue // no longer visible to the caller
		}
		out.Items = append(out.Items, aiItemJSON{
			SegmentID: it.SegmentID, Title: it.Title, Reason: it.Reason,
			Meta: aiItemMetaJSON{
				SessionID: seg.SessionID, StartTs: seg.StartTs, ProjectName: seg.ProjectName, Agent: seg.Agent,
				TypedTurnCount: seg.TypedTurnCount, ToolCallCount: seg.ToolCallCount, ToolFailCount: seg.ToolFailCount,
			},
		})
	}
	return out
}

// handleGetAIReports serves facts for one week. Which screen state applies is
// decided by the client from these facts, in one place.
func (s *Server) handleGetAIReports(w http.ResponseWriter, r *http.Request) {
	svc, sc, ok := s.aiRequest(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	wk, ok := parseAIWeek(q.Get("week"), q.Get("tz"))
	if !ok {
		writeAIError(w, http.StatusBadRequest, "invalid_week", "week 는 2026-W37 형식, tz 는 IANA 시간대여야 합니다")
		return
	}
	ctx := r.Context()
	view, err := svc.WeekView(ctx, sc, wk)
	if err != nil {
		writeErr(w, err)
		return
	}
	consent, err := svc.Consent(ctx, sc)
	if err != nil {
		writeErr(w, err)
		return
	}
	enablement, err := svc.Enablement(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Off asks no provider: a runtime's status can call its API with the key.
	info, status := airuntime.Info{Key: consent.Runtime}, airuntime.Status{}
	if enablement.Enabled {
		info, status = svc.RuntimeStatus(ctx)
	}
	var reason *string
	if status.Reason != "" {
		reason = nullableString(aiRuntimeUnavailableReason)
	}
	// No zone is offered here on purpose. The service decides it -- saved zone,
	// then the zone of the last report, then the deployment's default
	// (CCTRACE_AI_DEFAULT_TZ, UTC when unset) -- and the scheduler reads the
	// same order. Passing the browser's zone instead made the screen print a
	// firing nobody would make: a Seoul user with no report saw 06:00 and the
	// run went at 06:00 UTC, nine hours later, with nothing reporting an error.
	schedule, err := svc.UserSchedule(ctx, sc.DashboardUserID, "")
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := aiReportsResponse{
		Week: wk.ID, TZ: wk.TZ, Since: wk.Since, Until: wk.Until, InProgress: wk.InProgress(time.Now()),
		Runtime:      aiRuntimeJSON{Enabled: enablement.Enabled, Key: info.Key, Configured: status.Configured, Available: status.Available, Reason: reason},
		Consent:      aiConsentStateJSON{Granted: consent.Granted, RuntimeKey: consent.RuntimeKey, DisclosureVersion: consent.DisclosureVersion},
		SegmentCount: view.SegmentCount,
		Schedule:     userScheduleJSON(schedule, time.Now()),
	}
	if view.Run != nil {
		resp.Run = runJSON(view.Run, view.RunToolCalls)
	}
	if view.Report != nil {
		resp.Report = reportJSON(view)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStartAIReport(w http.ResponseWriter, r *http.Request) {
	svc, sc, ok := s.aiRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		Week string `json:"week"`
		TZ   string `json:"tz"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_week", "요청 본문을 읽을 수 없습니다")
		return
	}
	wk, ok := parseAIWeek(body.Week, body.TZ)
	if !ok {
		writeAIError(w, http.StatusBadRequest, "invalid_week", "week 는 2026-W37 형식, tz 는 IANA 시간대여야 합니다")
		return
	}
	if wk.IsFuture(time.Now()) {
		writeAIError(w, http.StatusBadRequest, "future_week", "아직 시작하지 않은 주입니다")
		return
	}
	run, err := svc.Start(r.Context(), sc, wk)
	var already *aireport.AlreadyRunningError
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{"run": runJSON(run, nil)})
	case errors.As(err, &already):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "already_running", "message": "이미 실행 중인 분석이 있습니다", "run_id": already.RunID})
	case errors.Is(err, aireport.ErrRuntimeDisabled):
		writeAIDisabled(w)
	case errors.Is(err, aireport.ErrConsentRequired):
		writeAIError(w, http.StatusForbidden, "consent_required", "데이터 전송 동의가 필요합니다")
	case errors.Is(err, aireport.ErrAccountChanging):
		writeAIError(w, http.StatusConflict, "runtime_account_changing", "AI 연결 계정을 바꾸는 중이라 리포트를 생성할 수 없습니다")
	case errors.Is(err, aireport.ErrRuntimeChanging):
		writeAIError(w, http.StatusConflict, "runtime_changing", "AI 런타임을 바꾸는 중이라 리포트를 생성할 수 없습니다")
	case errors.Is(err, aireport.ErrNoRecords):
		writeAIError(w, http.StatusUnprocessableEntity, "no_records", "이 주에 작업 구간이 없습니다")
	case errors.Is(err, airuntime.ErrNotConfigured):
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "AI 런타임이 설정되지 않았습니다")
	case errors.Is(err, airuntime.ErrUnavailable):
		log.Printf("[ai-reports] start refused: %v", err)
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unavailable", "AI 런타임에 연결할 수 없습니다")
	default:
		writeErr(w, err)
	}
}

// aiRun resolves {id} to the caller's run. Another user's run is 404 for
// everyone, administrators included.
func (s *Server) aiRun(w http.ResponseWriter, r *http.Request) (*aireport.Service, aireport.Scope, *store.AIReportRun, bool) {
	svc, sc, ok := s.aiRequest(w, r)
	if !ok {
		return nil, sc, nil, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAIError(w, http.StatusNotFound, "not_found", "실행을 찾을 수 없습니다")
		return nil, sc, nil, false
	}
	run, err := svc.GetRun(r.Context(), sc, id)
	if errors.Is(err, aireport.ErrNotFound) {
		writeAIError(w, http.StatusNotFound, "not_found", "실행을 찾을 수 없습니다")
		return nil, sc, nil, false
	}
	if err != nil {
		writeErr(w, err)
		return nil, sc, nil, false
	}
	return svc, sc, run, true
}

func (s *Server) handleCancelAIRun(w http.ResponseWriter, r *http.Request) {
	svc, sc, run, ok := s.aiRun(w, r)
	if !ok {
		return
	}
	switch err := svc.Cancel(r.Context(), sc, run.ID); {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "canceling"})
	case errors.Is(err, aireport.ErrNotFound):
		writeAIError(w, http.StatusNotFound, "not_found", "실행을 찾을 수 없습니다")
	case errors.Is(err, aireport.ErrNotRunning):
		writeAIError(w, http.StatusConflict, "not_running", "실행 중이 아닙니다")
	default:
		writeErr(w, err)
	}
}

// sseWriter writes one progress stream and remembers which tool frames it has
// sent, so the database replay and the live broker can overlap without repeats.
type sseWriter struct {
	w    http.ResponseWriter
	rc   *http.ResponseController
	sent map[string]bool
	err  error
	// watermark is the highest seq at or below which every result has been
	// sent (starting at the client's Last-Event-ID). It, not the result's own
	// seq, goes in id:, because results finish out of order and a reconnect
	// replays only seqs above Last-Event-ID.
	watermark int
	finished  map[int]bool
}

func (sw *sseWriter) frame(id int, event string, data any) {
	if sw.err != nil {
		return
	}
	payload, err := json.Marshal(data)
	if err != nil {
		sw.err = err
		return
	}
	if id > 0 {
		_, sw.err = fmt.Fprintf(sw.w, "id: %d\nevent: %s\ndata: %s\n\n", id, event, payload)
	} else {
		_, sw.err = fmt.Fprintf(sw.w, "event: %s\ndata: %s\n\n", event, payload)
	}
	if sw.err == nil {
		sw.err = sw.rc.Flush()
	}
}

func (sw *sseWriter) toolCall(seq int, tool string, args any) {
	// No id: here. Last-Event-ID must only advance past finished calls, or a
	// reconnect after a call frame would skip that call's result.
	if key := "call:" + strconv.Itoa(seq); !sw.sent[key] {
		sw.sent[key] = true
		sw.frame(0, "tool_call", map[string]any{"seq": seq, "tool": tool, "args": args})
	}
}

func (sw *sseWriter) toolResult(seq int, status string, rows, durMs *int) {
	if key := "result:" + strconv.Itoa(seq); !sw.sent[key] {
		sw.sent[key] = true
		if sw.finished == nil {
			sw.finished = map[int]bool{}
		}
		sw.finished[seq] = true
		for sw.finished[sw.watermark+1] {
			sw.watermark++
			delete(sw.finished, sw.watermark)
		}
		sw.frame(sw.watermark, "tool_result", map[string]any{"seq": seq, "status": status, "result_rows": rows, "duration_ms": durMs})
	}
}

func (sw *sseWriter) replay(ctx context.Context, svc *aireport.Service, runID int64, afterSeq int) {
	calls, err := svc.ToolCalls(ctx, runID, afterSeq)
	if err != nil {
		sw.err = err
		return
	}
	for _, c := range toolCallsJSON(calls) {
		sw.toolCall(c.Seq, c.Tool, c.Args)
		if c.Status != store.AIToolCallRunning {
			sw.toolResult(c.Seq, c.Status, c.ResultRows, c.DurationMs)
		}
	}
}

func (sw *sseWriter) event(ev airuntime.Event) {
	switch ev.Kind {
	case airuntime.EventToolCall:
		sw.toolCall(ev.Seq, ev.Tool, ev.Args)
	case airuntime.EventToolResult:
		rows, dur := ev.Rows, ev.DurationMs
		sw.toolResult(ev.Seq, ev.Status, &rows, &dur)
	case airuntime.EventUsage:
		if ev.Usage != nil {
			sw.frame(0, "usage", usageJSON(store.AIUsage{Reported: ev.Usage.Reported, InputTokens: ev.Usage.InputTokens,
				CachedInputTokens: ev.Usage.CachedInputTokens, OutputTokens: ev.Usage.OutputTokens}))
		}
	}
}

// terminal ends the stream: status, then report for a completed run.
func (sw *sseWriter) terminal(run *store.AIReportRun) {
	sw.frame(0, "status", struct {
		Status string          `json:"status"`
		Error  *aiRunErrorJSON `json:"error,omitempty"`
	}{run.Status, runJSON(run, nil).Error})
	if run.Status == store.AIRunCompleted {
		sw.frame(0, "report", map[string]int64{"run_id": run.ID})
	}
}

// handleAIRunEvents streams a run's progress as SSE: tool calls already logged
// after Last-Event-ID (or ?after_seq), then live events, then status and, for a
// completed run, report. The database is the source of truth; whenever the live
// channel closes the handler re-reads it before deciding the run is over.
func (s *Server) handleAIRunEvents(w http.ResponseWriter, r *http.Request) {
	svc, sc, run, ok := s.aiRun(w, r)
	if !ok {
		return
	}
	afterSeq := 0
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		afterSeq, _ = strconv.Atoi(v)
	} else if v := r.URL.Query().Get("after_seq"); v != "" {
		afterSeq, _ = strconv.Atoi(v)
	}
	if afterSeq < 0 {
		afterSeq = 0
	}

	rc := http.NewResponseController(w)
	// A run lasts minutes; the server's 30 s WriteTimeout would cut the stream.
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	sw := &sseWriter{w: w, rc: rc, sent: map[string]bool{}, watermark: afterSeq}
	sw.replay(ctx, svc, run.ID, afterSeq)
	if run.Status != store.AIRunRunning {
		sw.terminal(run)
		return
	}
	_ = rc.Flush()

	ch, unsub := svc.Subscribe(run.ID, afterSeq)
	defer func() { unsub() }()
	ping := time.NewTicker(sseKeepAlive)
	defer ping.Stop()
	emptyResubscribes := 0
	for sw.err == nil {
		select {
		case <-ctx.Done():
			return
		case <-svc.StreamsDone():
			return
		case <-ping.C:
			if _, sw.err = fmt.Fprint(w, ": ping\n\n"); sw.err == nil {
				sw.err = rc.Flush()
			}
		case ev, open := <-ch:
			if open {
				emptyResubscribes = 0
				sw.event(ev)
				continue
			}
			sw.replay(ctx, svc, run.ID, afterSeq)
			latest, err := svc.GetRun(ctx, sc, run.ID)
			if err != nil {
				return
			}
			if latest.Status != store.AIRunRunning {
				sw.terminal(latest)
				return
			}
			// Dropped for falling behind: wait briefly and follow again. A run that
			// keeps closing at once has nothing executing it; stop without a status.
			if emptyResubscribes++; emptyResubscribes > sseMaxEmptyResubscribes {
				return
			}
			unsub()
			select {
			case <-ctx.Done():
				return
			case <-svc.StreamsDone():
				return
			case <-time.After(sseResubscribeWait):
			}
			ch, unsub = svc.Subscribe(run.ID, 0)
		}
	}
}
