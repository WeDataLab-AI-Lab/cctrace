package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/sessionrecord"
	"cctrace/internal/store"
)

type sessionRecordAdminStore interface {
	GetSessionAIRecordSettings(context.Context) (*store.SessionAIRecordSettings, error)
	SetSessionAIRecordSettings(context.Context, store.SessionAIRecordSettings, string) error
	GetAIRecordRuntimeSettings(context.Context, string) (*store.SessionAIRecordRuntimeSettings, error)
	SetAIRecordRuntimeSettings(context.Context, string, string, string, string) error
	GetSessionRecordVocabulary(context.Context, string) (store.SessionRecordVocabulary, error)
	SetSessionRecordVocabulary(context.Context, string, string, []string, string) error
	SetSessionRecordVocabularies(context.Context, string, []string, []string, string) error
	MergeSessionRecordVocabulary(context.Context, string, string, string, string, string) error
	SessionAIRecordAdminMetrics(context.Context, string, string, time.Time, string) (map[string]interface{}, error)
}

func (s *Server) handleSetAdminAIRecords(w http.ResponseWriter, r *http.Request) {
	user, ok := adminAISession(w, r, true)
	if !ok {
		return
	}
	repo, ok := s.store.(sessionRecordAdminStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "records_unavailable", "세션 기록 설정 저장소를 사용할 수 없습니다")
		return
	}
	var body struct {
		Enabled             *bool   `json:"enabled"`
		Runtime             *string `json:"runtime"`
		DailyTokenCap       *int64  `json:"daily_token_cap"`
		BackfillEnabled     *bool   `json:"backfill_enabled"`
		BackfillDays        *int    `json:"backfill_days"`
		BackfillTokenBudget *int64  `json:"backfill_token_budget"`
		Model               *string `json:"model"`
		ReasoningEffort     *string `json:"reasoning_effort"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.Enabled == nil || body.Runtime == nil || body.BackfillEnabled == nil || body.Model == nil || body.ReasoningEffort == nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "enabled, runtime, backfill_enabled, model, reasoning_effort가 필요합니다")
		return
	}
	runtime := strings.TrimSpace(*body.Runtime)
	if runtime != "" && runtime != aireport.RuntimeOpenAI && runtime != aireport.RuntimeClaude && runtime != aireport.RuntimeLiteLLM {
		writeAIError(w, http.StatusBadRequest, "runtime_unsupported", "세션 기록은 OpenAI API, Claude API, LiteLLM API만 사용할 수 있습니다")
		return
	}
	if body.DailyTokenCap != nil && *body.DailyTokenCap < 0 || body.BackfillDays != nil && *body.BackfillDays < 0 || body.BackfillTokenBudget != nil && *body.BackfillTokenBudget < 0 {
		writeAIError(w, http.StatusBadRequest, "invalid_budget", "한도와 백필 기간은 0 이상이어야 합니다")
		return
	}
	runtimeTarget := runtime
	if runtimeTarget == "" && s.aiReports != nil {
		selection, err := s.aiReports.Selection(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		runtimeTarget = selection.Key
	}
	if *body.Enabled {
		if s.aiReports == nil {
			writeAICatalogError(w, airuntime.ErrNotConfigured)
			return
		}
		if runtimeTarget != aireport.RuntimeOpenAI && runtimeTarget != aireport.RuntimeClaude && runtimeTarget != aireport.RuntimeLiteLLM {
			writeAIError(w, http.StatusBadRequest, "runtime_unsupported", "세션 기록을 켜려면 API 런타임을 선택해야 합니다")
			return
		}
		if _, _, _, err := s.aiReports.RuntimeForSessionRecords(r.Context(), runtimeTarget); err != nil {
			writeAICatalogError(w, err)
			return
		}
	}
	model, effort := strings.TrimSpace(*body.Model), strings.TrimSpace(*body.ReasoningEffort)
	if runtimeTarget != "" && model != "" {
		if s.aiReports == nil {
			writeAICatalogError(w, airuntime.ErrNotConfigured)
			return
		}
		models, err := s.aiReports.Models(r.Context(), runtimeTarget)
		if err != nil {
			writeAICatalogError(w, err)
			return
		}
		var selected *airuntime.Model
		for i := range models {
			if models[i].ID == model {
				selected = &models[i]
				break
			}
		}
		if selected == nil {
			writeAIError(w, http.StatusBadRequest, "invalid_model", "모델 목록에 없는 모델입니다")
			return
		}
		if effort != "" && !selected.SupportsEffort(effort) {
			writeAIError(w, http.StatusBadRequest, "invalid_reasoning_effort", "이 모델이 지원하지 않는 reasoning effort입니다")
			return
		}
	}
	settings := store.SessionAIRecordSettings{Enabled: *body.Enabled, Runtime: runtime, DailyTokenCap: body.DailyTokenCap,
		BackfillEnabled: *body.BackfillEnabled, BackfillDays: body.BackfillDays, BackfillTokenBudget: body.BackfillTokenBudget}
	err := s.mutateSessionRecordPolicy(func() error {
		if runtimeTarget != "" {
			if err := repo.SetAIRecordRuntimeSettings(r.Context(), runtimeTarget, model, effort, user.Email); err != nil {
				return err
			}
		}
		return repo.SetSessionAIRecordSettings(r.Context(), settings, user.Email)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"settings": settings, "runtime_model": store.SessionAIRecordRuntimeSettings{Runtime: runtimeTarget, Model: model, ReasoningEffort: effort}})
}

func (s *Server) handleGetAdminAIRecordVocabulary(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminAISession(w, r, false); !ok {
		return
	}
	repo, ok := s.store.(sessionRecordAdminStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "records_unavailable", "세션 기록 어휘 저장소를 사용할 수 없습니다")
		return
	}
	repository := strings.TrimSpace(r.URL.Query().Get("repository_key"))
	if repository == "" {
		writeAIError(w, http.StatusBadRequest, "repository_required", "repository_key가 필요합니다")
		return
	}
	vocabulary, err := repo.GetSessionRecordVocabulary(r.Context(), repository)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vocabulary)
}

func (s *Server) handleSetAdminAIRecordVocabulary(w http.ResponseWriter, r *http.Request) {
	user, ok := adminAISession(w, r, true)
	if !ok {
		return
	}
	repo, ok := s.store.(sessionRecordAdminStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "records_unavailable", "세션 기록 어휘 저장소를 사용할 수 없습니다")
		return
	}
	var body struct {
		RepositoryKey string    `json:"repository_key"`
		Components    *[]string `json:"components"`
		Screens       *[]string `json:"screens"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.Components == nil || body.Screens == nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "repository_key, components, screens가 필요합니다")
		return
	}
	repository := strings.TrimSpace(body.RepositoryKey)
	if repository == "" {
		writeAIError(w, http.StatusBadRequest, "repository_required", "repository_key가 필요합니다")
		return
	}
	err := s.mutateSessionRecordPolicy(func() error {
		return repo.SetSessionRecordVocabularies(r.Context(), repository, *body.Components, *body.Screens, user.Email)
	})
	if err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_vocabulary", err.Error())
		return
	}
	vocabulary, err := repo.GetSessionRecordVocabulary(r.Context(), repository)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vocabulary)
}

func (s *Server) handleMergeAdminAIRecordVocabulary(w http.ResponseWriter, r *http.Request) {
	user, ok := adminAISession(w, r, true)
	if !ok {
		return
	}
	repo, ok := s.store.(sessionRecordAdminStore)
	if !ok {
		writeAIError(w, http.StatusServiceUnavailable, "records_unavailable", "세션 기록 어휘 저장소를 사용할 수 없습니다")
		return
	}
	var body struct {
		RepositoryKey string `json:"repository_key"`
		Axis          string `json:"axis"`
		From          string `json:"from"`
		To            string `json:"to"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "repository_key, axis, from, to가 필요합니다")
		return
	}
	err := s.mutateSessionRecordPolicy(func() error {
		return repo.MergeSessionRecordVocabulary(r.Context(), strings.TrimSpace(body.RepositoryKey), body.Axis, body.From, body.To, user.Email)
	})
	if err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_vocabulary_merge", err.Error())
		return
	}
	vocabulary, err := repo.GetSessionRecordVocabulary(r.Context(), strings.TrimSpace(body.RepositoryKey))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, vocabulary)
}

func (s *Server) sessionRecordAdminView(ctx context.Context, repo sessionRecordAdminStore) (map[string]interface{}, error) {
	runtime, runtimeKey, available, reason, settings, err := s.currentSessionRecordRuntime(ctx)
	if err != nil {
		return nil, err
	}
	tz := "UTC"
	if s.sessionRecordsWorker != nil && s.sessionRecordsWorker.DefaultTZ != "" {
		tz = s.sessionRecordsWorker.DefaultTZ
	}
	view, err := repo.SessionAIRecordAdminMetrics(ctx, runtimeKey, sessionrecord.RecordDisclosureVersion, time.Now().UTC(), tz)
	if err != nil {
		return nil, err
	}
	view["flag"] = s.sessionRecordsEnabled
	view["enabled"] = settings.Enabled
	view["prompt_version"] = sessionrecord.PromptVersion
	view["runtime"] = runtime
	view["runtime_key"] = runtimeKey
	view["runtime_ok"] = available
	view["runtime_reason"] = reason
	view["settings"] = settings
	model := ""
	runtimeSettings := &store.SessionAIRecordRuntimeSettings{Runtime: runtime}
	if runtime != "" {
		saved, err := repo.GetAIRecordRuntimeSettings(ctx, runtime)
		if err != nil {
			return nil, err
		}
		runtimeSettings = saved
		model = saved.Model
		if model == "" && s.aiReports != nil {
			weekly, err := s.aiReports.SettingsFor(ctx, runtime)
			if err == nil {
				model = weekly.Model
			}
		}
	}
	view["model"] = model
	view["runtime_settings"] = runtimeSettings
	worker := sessionrecord.WorkerStatus{State: "off"}
	if s.sessionRecordsWorker != nil {
		worker = s.sessionRecordsWorker.Status()
	}
	view["worker"] = worker
	return view, nil
}

func (s *Server) addSessionRecordAdminToAIResponse(ctx context.Context, resp map[string]interface{}) error {
	repo, ok := s.store.(sessionRecordAdminStore)
	if !ok {
		return nil
	}
	view, err := s.sessionRecordAdminView(ctx, repo)
	if err != nil {
		return err
	}
	resp["records"] = view
	return nil
}
