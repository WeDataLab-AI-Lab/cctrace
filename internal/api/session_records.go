package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
	"cctrace/internal/sessionrecord"
	"cctrace/internal/store"
)

const sessionRecordDisclosureText = `세션이 조용해지면 대화에서 개요·검토 사항·작업 상황·태그를 만들기 위해 선택한 AI 공급자에 입력을 보냅니다. 보내는 내용은 직접 입력과 작업 중 입력, 질문과 답, 계획과 결정, 각 턴의 마지막 응답, 도구 이름·횟수·실패 횟수, 편집 파일 경로, Bash 명령 종류, 커밋 제목·task 제목·PR 번호입니다. 도구 출력, 파일 내용, 명령 인자·검색어·URL, 중간 응답·사고, 서브에이전트 대화, 계정과 세션 식별자는 보내지 않습니다. 입력에 직접 붙여 넣은 비밀이나 제3자 글은 걸러지지 않습니다. 공급자의 보관 기간은 확인되지 않습니다. 기록은 이 서버에 저장되고 세션 삭제 시 함께 지워집니다. 공급자가 이미 받은 원문은 서버에서 회수하거나 삭제할 수 없습니다.`

var sessionRecordConsentSends = []string{
	"직접 입력과 작업 중 입력, /명령 입력",
	"에이전트 질문·선택지와 사용자의 답, 계획과 승인·반려",
	"턴마다 에이전트의 마지막 응답",
	"도구 이름·횟수·실패 횟수, 편집 파일 경로, Bash 명령 종류",
	"커밋 제목, task 제목, PR 번호",
}

var sessionRecordConsentNotSends = []string{
	"도구 출력, 파일 내용, 도구 실행 명령의 인자·검색어·URL",
	"에이전트 중간 응답·사고, 서브에이전트 대화",
	"이메일·계정·세션 식별자, 제외된 계정·세션, 다른 사용자의 기록",
}

type sessionRecordSettingsStore interface {
	GetSessionAIRecordSettings(context.Context) (*store.SessionAIRecordSettings, error)
}
type sessionRecordConsentStore interface {
	sessionRecordSettingsStore
	SessionRecordConsent(context.Context, int64, string, string) (bool, *time.Time, error)
	GrantSessionRecordConsent(context.Context, int64, string, string) error
	RevokeSessionRecordConsent(context.Context, int64, string) error
}
type sessionRecordSearchStore interface {
	SearchMySessionRecords(context.Context, string, string, string, bool, bool, int) ([]store.SessionRecordSearchResult, error)
}
type sessionRecordRequestStore interface {
	RequestSessionRecord(context.Context, string, string, string, bool, int64, string, string, bool, time.Time) error
	RequestSessionRecordRange(context.Context, int64, string, string, bool, string, string, time.Time, time.Time, time.Time) error
}
type sessionRecordStatusStore interface {
	SessionRecordRangeStatus(context.Context, int64, string, string, bool, string, string, time.Time, time.Time, time.Time) (store.SessionRecordRangeStatus, error)
}
type sessionRecordDeleteStore interface {
	DeleteOwnSessionAIRecords(context.Context, string, string, bool) (int64, error)
}

type sessionRecordConsentResponse struct {
	RuntimeKey        string     `json:"runtime_key"`
	DisclosureVersion string     `json:"disclosure_version"`
	Granted           bool       `json:"granted"`
	GrantedAt         *time.Time `json:"granted_at"`
	Runtime           string     `json:"runtime"`
	Provider          string     `json:"provider"`
	ProviderRetention string     `json:"provider_retention"`
	Disclosure        string     `json:"disclosure"`
	Sends             []string   `json:"sends"`
	NotSends          []string   `json:"not_sends"`
	Enabled           bool       `json:"enabled"`
	RuntimeAvailable  bool       `json:"runtime_available"`
	RuntimeReason     string     `json:"runtime_reason,omitempty"`
}

func (s *Server) currentSessionRecordRuntime(ctx context.Context) (string, string, bool, string, *store.SessionAIRecordSettings, error) {
	settingsStore, ok := s.store.(sessionRecordSettingsStore)
	if !ok {
		return "", "", false, "runtime_unconfigured", nil, fmt.Errorf("session record settings unavailable")
	}
	settings, err := settingsStore.GetSessionAIRecordSettings(ctx)
	if err != nil {
		return "", "", false, "", nil, err
	}
	runtime := strings.TrimSpace(settings.Runtime)
	if runtime == "" && s.aiReports != nil {
		selection, selectErr := s.aiReports.Selection(ctx)
		if selectErr != nil {
			return "", "", false, "", settings, selectErr
		}
		runtime = selection.Key
	}
	if runtime == "" {
		return "", "", false, "runtime_unconfigured", settings, nil
	}
	info := airuntime.Info{Key: runtime, AuthMode: airuntime.AuthModeAPIKey}
	available, reason := false, "runtime_unconfigured"
	if runtime != aireport.RuntimeOpenAI && runtime != aireport.RuntimeClaude && runtime != aireport.RuntimeLiteLLM {
		if runtime == aireport.DefaultRuntimeKey || runtime == "nvidia-api" {
			if runtime == aireport.DefaultRuntimeKey {
				info.AuthMode = airuntime.AuthModeNone
			}
			return runtime, sessionrecord.RuntimeConsentKey(info), false, "runtime_unsupported", settings, nil
		}
	}
	if s.aiReports != nil {
		rt, _, status, runtimeErr := s.aiReports.RuntimeForSessionRecords(ctx, runtime)
		if runtimeErr == nil && rt != nil {
			info = rt.Info()
			available = status.Configured && status.Available
			if status.Reason != "" {
				reason = status.Reason
			} else if !available {
				reason = "runtime_unavailable"
			} else {
				reason = ""
			}
		} else if runtimeErr != nil {
			reason = "runtime_unconfigured"
			if errors.Is(runtimeErr, aireport.ErrRuntimeUnimplemented) {
				reason = "runtime_unsupported"
			}
		}
	}
	return runtime, sessionrecord.RuntimeConsentKey(info), available, reason, settings, nil
}

func (s *Server) requireSessionRecords(w http.ResponseWriter) bool {
	if !s.sessionRecordsEnabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session_records_disabled"})
		return false
	}
	return true
}

func (s *Server) sessionRecordCaller(r *http.Request) (*auth.DashboardUser, string, string, bool, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		return nil, "", "", s.useUserIDAccessControl, false
	}
	profileEmail, _, userID := s.resolveUserAccessParams(user)
	if s.useUserIDAccessControl && userID == noAccessSentinel {
		return user, userID, profileEmail, true, false
	}
	if !s.useUserIDAccessControl && profileEmail == "" {
		return user, userID, profileEmail, false, false
	}
	return user, userID, profileEmail, s.useUserIDAccessControl, true
}

func (s *Server) mutateSessionRecordPolicy(fn func() error) error {
	if s.sessionRecordGate == nil {
		return fn()
	}
	return s.sessionRecordGate.Mutate(fn)
}

func (s *Server) withSessionRecordPolicyMutation(next http.Handler) http.Handler {
	if s.sessionRecordGate == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.sessionRecordGate.Mutate(func() error { next.ServeHTTP(w, r); return nil }); err != nil {
			writeErr(w, err)
		}
	})
}

func (s *Server) handleGetSessionRecordConsent(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionRecords(w) {
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	repo, ok := s.store.(sessionRecordConsentStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "세션 기록 저장소가 설정되지 않았습니다")
		return
	}
	runtime, key, available, reason, settings, err := s.currentSessionRecordRuntime(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	granted, at := false, (*time.Time)(nil)
	if key != "" {
		granted, at, err = repo.SessionRecordConsent(r.Context(), user.ID, key, sessionrecord.RecordDisclosureVersion)
		if err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, sessionRecordConsentResponse{RuntimeKey: key, DisclosureVersion: sessionrecord.RecordDisclosureVersion, Granted: granted, GrantedAt: at,
		Runtime: runtime, Provider: aireport.ProviderLabels[runtime], ProviderRetention: aireport.RetentionFor(runtime), Disclosure: sessionRecordDisclosureText,
		Sends: sessionRecordConsentSends, NotSends: sessionRecordConsentNotSends, Enabled: settings.Enabled, RuntimeAvailable: available, RuntimeReason: reason})
}

func (s *Server) handlePostSessionRecordConsent(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionRecords(w) {
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var body struct {
		RuntimeKey        string `json:"runtime_key"`
		DisclosureVersion string `json:"disclosure_version"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "요청 본문을 읽을 수 없습니다")
		return
	}
	repo, ok := s.store.(sessionRecordConsentStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "세션 기록 저장소가 설정되지 않았습니다")
		return
	}
	_, key, available, reason, settings, err := s.currentSessionRecordRuntime(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if !settings.Enabled {
		writeAIError(w, http.StatusServiceUnavailable, "records_disabled", "관리자가 세션 기록을 껐습니다")
		return
	}
	if !available {
		writeAIError(w, http.StatusServiceUnavailable, reason, "선택된 API 런타임을 사용할 수 없습니다")
		return
	}
	if key == "" || body.RuntimeKey != key || body.DisclosureVersion != sessionrecord.RecordDisclosureVersion {
		writeAIError(w, http.StatusConflict, "consent_mismatch", "동의 문구 또는 런타임이 바뀌었습니다. 다시 확인해 주세요")
		return
	}
	if err := s.mutateSessionRecordPolicy(func() error {
		return repo.GrantSessionRecordConsent(r.Context(), user.ID, key, sessionrecord.RecordDisclosureVersion)
	}); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSessionRecordConsent(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionRecords(w) {
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	repo, ok := s.store.(sessionRecordConsentStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "세션 기록 저장소가 설정되지 않았습니다")
		return
	}
	_, key, _, _, _, err := s.currentSessionRecordRuntime(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if key == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.mutateSessionRecordPolicy(func() error { return repo.RevokeSessionRecordConsent(r.Context(), user.ID, key) }); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSessionRecordSearch(w http.ResponseWriter, r *http.Request) {
	user, ownerID, email, useUserID, hasScope := s.sessionRecordCaller(r)
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !hasScope {
		writeJSON(w, http.StatusOK, []store.SessionRecordSearchResult{})
		return
	}
	query := r.URL.Query().Get("q")
	limit := queryInt(r, "limit", 50)
	repo, ok := s.store.(sessionRecordSearchStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "search_unavailable", "세션 검색을 사용할 수 없습니다")
		return
	}
	results, err := repo.SearchMySessionRecords(r.Context(), query, ownerID, email, useUserID, s.sessionRecordsEnabled, limit)
	if err != nil {
		if strings.Contains(err.Error(), "2 to 100") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleSessionRecordStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionRecords(w) {
		return
	}
	user, ownerID, email, useUserID, hasScope := s.sessionRecordCaller(r)
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !hasScope {
		writeJSON(w, http.StatusOK, store.SessionRecordRangeStatus{State: "fresh", Blocked: map[string]int{}})
		return
	}
	from, err := optionalQueryTime(r, "from")
	if err != nil || from == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "from is required"})
		return
	}
	to, err := optionalQueryTime(r, "to")
	if err != nil || to == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "to is required"})
		return
	}
	_, key, runtimeAvailable, runtimeReason, _, err := s.currentSessionRecordRuntime(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	repo, ok := s.store.(sessionRecordStatusStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "status_unavailable", "세션 기록 상태를 읽을 수 없습니다")
		return
	}
	status, err := repo.SessionRecordRangeStatus(r.Context(), user.ID, ownerID, email, useUserID, key, sessionrecord.RecordDisclosureVersion, *from, *to, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrSessionCalendarRange) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_range"})
			return
		}
		writeErr(w, err)
		return
	}
	if !runtimeAvailable && status.Stale > 0 {
		reason := "no_runtime"
		if runtimeReason == "runtime_unsupported" {
			reason = runtimeReason
		}
		status.Blocked[reason] += status.Stale
		status.Stale = 0
		if status.State == "stale" {
			status.State = "fresh"
		}
	}
	if !runtimeAvailable {
		reason := "no_runtime"
		if runtimeReason == "runtime_unsupported" {
			reason = runtimeReason
		}
		if count := status.Blocked["consent_required"]; count > 0 {
			status.Blocked[reason] += count
			delete(status.Blocked, "consent_required")
		}
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleSessionRecordRefresh(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionRecords(w) {
		return
	}
	user, ownerID, email, useUserID, hasScope := s.sessionRecordCaller(r)
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !hasScope {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "identity_unavailable"})
		return
	}
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "from과 to가 필요합니다")
		return
	}
	from, e1 := time.Parse(time.RFC3339, body.From)
	to, e2 := time.Parse(time.RFC3339, body.To)
	if e1 != nil || e2 != nil || !from.Before(to) || to.Sub(from) > 31*24*time.Hour {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_range"})
		return
	}
	_, key, available, reason, settings, err := s.currentSessionRecordRuntime(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if !settings.Enabled {
		writeAIError(w, http.StatusServiceUnavailable, "records_disabled", "관리자가 세션 기록을 껐습니다")
		return
	}
	if !available {
		writeAIError(w, http.StatusServiceUnavailable, reason, "선택된 API 런타임을 사용할 수 없습니다")
		return
	}
	consents, ok := s.store.(sessionRecordConsentStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "세션 기록 저장소가 설정되지 않았습니다")
		return
	}
	granted, _, err := consents.SessionRecordConsent(r.Context(), user.ID, key, sessionrecord.RecordDisclosureVersion)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !granted {
		writeAIError(w, http.StatusForbidden, "consent_required", "현재 런타임의 세션 기록 동의가 필요합니다")
		return
	}
	repo, ok := s.store.(sessionRecordRequestStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "request_unavailable", "생성 요청을 저장할 수 없습니다")
		return
	}
	err = s.mutateSessionRecordPolicy(func() error {
		return repo.RequestSessionRecordRange(r.Context(), user.ID, ownerID, email, useUserID, key, sessionrecord.RecordDisclosureVersion, from, to, time.Now().UTC())
	})
	if err != nil {
		writeSessionRecordRequestError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleSessionRecordRegenerate(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionRecords(w) {
		return
	}
	user, ownerID, email, useUserID, hasScope := s.sessionRecordCaller(r)
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !hasScope {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "identity_unavailable"})
		return
	}
	_, key, available, reason, settings, err := s.currentSessionRecordRuntime(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if !settings.Enabled {
		writeAIError(w, http.StatusServiceUnavailable, "records_disabled", "관리자가 세션 기록을 껐습니다")
		return
	}
	if !available {
		writeAIError(w, http.StatusServiceUnavailable, reason, "선택된 API 런타임을 사용할 수 없습니다")
		return
	}
	consents, ok := s.store.(sessionRecordConsentStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "세션 기록 저장소가 설정되지 않았습니다")
		return
	}
	granted, _, err := consents.SessionRecordConsent(r.Context(), user.ID, key, sessionrecord.RecordDisclosureVersion)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !granted {
		writeAIError(w, http.StatusForbidden, "consent_required", "현재 런타임의 세션 기록 동의가 필요합니다")
		return
	}
	repo, ok := s.store.(sessionRecordRequestStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "request_unavailable", "생성 요청을 저장할 수 없습니다")
		return
	}
	err = s.mutateSessionRecordPolicy(func() error {
		return repo.RequestSessionRecord(r.Context(), r.PathValue("session_id"), ownerID, email, useUserID, user.ID, key, sessionrecord.RecordDisclosureVersion, true, time.Now().UTC())
	})
	if err != nil {
		writeSessionRecordRequestError(w, err)
		return
	}
	response := map[string]interface{}{"state": "queued"}
	workerStatus := sessionrecord.WorkerStatus{}
	if s.sessionRecordWorkerStatus != nil {
		workerStatus = s.sessionRecordWorkerStatus()
	} else if s.sessionRecordsWorker != nil {
		workerStatus = s.sessionRecordsWorker.Status()
	}
	if workerStatus.State == "paused" && workerStatus.PausedReason == "daily_cap" &&
		workerStatus.PausedUntil != nil && workerStatus.PausedUntil.After(time.Now().UTC()) {
		response = map[string]interface{}{"state": "waiting_budget", "reason": "waiting_budget", "retry_at": workerStatus.PausedUntil}
	}
	writeJSON(w, http.StatusAccepted, response)
}

func (s *Server) handleDeleteOwnSessionRecords(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("scope") != "mine" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "scope=mine is required"})
		return
	}
	user, ownerID, email, useUserID, hasScope := s.sessionRecordCaller(r)
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !hasScope {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	repo, ok := s.store.(sessionRecordDeleteStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "delete_unavailable", "세션 기록을 삭제할 수 없습니다")
		return
	}
	var deleted int64
	err := s.mutateSessionRecordPolicy(func() error {
		var e error
		deleted, e = repo.DeleteOwnSessionAIRecords(r.Context(), ownerID, email, useUserID)
		return e
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": deleted})
}

func writeSessionRecordRequestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSessionRecordRateLimited):
		writeAIError(w, http.StatusTooManyRequests, "rate_limited", "이 세션은 10분에 한 번 다시 만들 수 있습니다")
	case errors.Is(err, store.ErrSessionRecordBusy):
		writeAIError(w, http.StatusConflict, "generation_in_progress", "생성이 이미 진행 중입니다")
	case errors.Is(err, store.ErrSessionRecordNotEligible):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, store.ErrSessionRecordDisabled):
		writeAIError(w, http.StatusServiceUnavailable, "records_disabled", "관리자가 세션 기록을 껐습니다")
	case errors.Is(err, store.ErrSessionCalendarRange):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_range"})
	default:
		writeErr(w, err)
	}
}
