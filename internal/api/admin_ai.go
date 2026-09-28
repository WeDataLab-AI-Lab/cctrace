package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type adminAICredentialJSON struct {
	Provider  string     `json:"provider"`
	Source    string     `json:"source"`
	KeyHint   *string    `json:"key_hint"`
	EnvVar    string     `json:"env_var"`
	Reason    *string    `json:"reason"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// credentialJSON shows where a key comes from and its last four characters,
// never the key.
func credentialJSON(c aireport.CredentialInfo) adminAICredentialJSON {
	return adminAICredentialJSON{
		Provider: c.Provider, Source: c.Source, KeyHint: nullableString(c.KeyHint),
		EnvVar: c.EnvVar, Reason: nullableString(c.Reason), UpdatedAt: c.UpdatedAt,
	}
}

type adminAIRuntimeJSON struct {
	Key                    string                 `json:"key"`
	Implemented            bool                   `json:"implemented"`
	Configured             bool                   `json:"configured"`
	Available              bool                   `json:"available"`
	Reason                 *string                `json:"reason"`
	Selected               bool                   `json:"selected"`
	Provider               string                 `json:"provider"`
	AuthMode               string                 `json:"auth_mode"`
	AccountEmail           string                 `json:"account_email"`
	PlanType               string                 `json:"plan_type"`
	UsedPercent            *float64               `json:"used_percent"`
	PersonalAccountWarning bool                   `json:"personal_account_warning"`
	Credential             *adminAICredentialJSON `json:"credential"`
}

type adminAISettingsSourceJSON struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	BaseURL         string `json:"base_url"`
}

// adminAISettingsJSON is what the next run on runtime uses. reasoning_effort ""
// with source "default" means the model's own default effort.
type adminAISettingsJSON struct {
	Runtime            string                    `json:"runtime"`
	Model              string                    `json:"model"`
	ReasoningEffort    string                    `json:"reasoning_effort"`
	BaseURL            string                    `json:"base_url"`
	Source             adminAISettingsSourceJSON `json:"source"`
	EnvModel           string                    `json:"env_model"`
	EnvReasoningEffort string                    `json:"env_reasoning_effort"`
	EnvBaseURL         string                    `json:"env_base_url"`
}

func settingsJSON(st aireport.Settings) adminAISettingsJSON {
	return adminAISettingsJSON{
		Runtime: st.Runtime, Model: st.Model, ReasoningEffort: st.ReasoningEffort, BaseURL: st.BaseURL,
		Source:   adminAISettingsSourceJSON{Model: st.ModelSource, ReasoningEffort: st.ReasoningEffortSource, BaseURL: st.BaseURLSource},
		EnvModel: st.EnvModel, EnvReasoningEffort: st.EnvReasoningEffort, EnvBaseURL: st.EnvBaseURL,
	}
}

type adminAIEffortJSON struct {
	ReasoningEffort string `json:"reasoning_effort"`
	Description     string `json:"description"`
}

type adminAIModelJSON struct {
	ID                        string              `json:"id"`
	DisplayName               string              `json:"display_name"`
	Description               string              `json:"description"`
	IsDefault                 bool                `json:"is_default"`
	DefaultReasoningEffort    string              `json:"default_reasoning_effort"`
	SupportedReasoningEfforts []adminAIEffortJSON `json:"supported_reasoning_efforts"`
}

// adminAISession admits an admin on a browser session. Settings and keys are
// refused to a CLI token, a file on a client PC, as account management is.
// Writes check CSRF first.
func adminAISession(w http.ResponseWriter, r *http.Request, write bool) (*auth.DashboardUser, bool) {
	if write && !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return nil, false
	}
	if auth.IsTokenAuth(r.Context()) {
		writeAIError(w, http.StatusForbidden, "session_required", "AI 설정은 브라우저 로그인 세션에서만 바꿀 수 있습니다")
		return nil, false
	}
	return adminFromRequest(w, r)
}

// handleAdminAI is the admin view: whether reports run, every runtime's status
// and key source, the active runtime's settings, and this week's run totals. No report content is
// reachable from here.
func (s *Server) handleAdminAI(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminAISession(w, r, false); !ok {
		return
	}
	runtimes := make([]adminAIRuntimeJSON, 0, len(aireport.RuntimeKeys))
	usage := &store.AIUsageSummary{}
	var settings *adminAISettingsJSON
	model, selected, source, envRuntime, selectionCode, selectionReason := "", "", aireport.SourceDefault, "", "", ""
	enablement := aireport.Enablement{Source: aireport.SourceDefault}
	var secretsReason string
	svc := s.aiReports
	if svc == nil {
		for _, key := range aireport.RuntimeKeys {
			runtimes = append(runtimes, adminAIRuntimeJSON{Key: key, Implemented: !aireport.Unimplemented(key), Provider: aireport.RuntimeProviders[key]})
		}
	} else {
		ctx := r.Context()
		views, sel, err := svc.RuntimeViews(ctx)
		if err != nil {
			writeErr(w, err)
			return
		}
		if enablement, err = svc.Enablement(ctx); err != nil {
			writeErr(w, err)
			return
		}
		secretsReason = svc.Keyring().SecretsReason()
		for _, v := range views {
			rt := adminAIRuntimeJSON{
				Key: v.Key, Implemented: !aireport.Unimplemented(v.Key), Configured: v.Status.Configured, Available: v.Status.Available,
				Reason: nullableString(v.Status.Reason), Selected: v.Selected, Provider: v.Provider,
				AuthMode: v.Info.AuthMode, AccountEmail: v.Status.AccountEmail, PlanType: v.Status.PlanType, UsedPercent: v.Status.UsedPercent,
				PersonalAccountWarning: v.Info.AuthMode == airuntime.AuthModeChatGPT,
			}
			if v.Credential != nil {
				c := credentialJSON(*v.Credential)
				rt.Credential = &c
			}
			runtimes = append(runtimes, rt)
		}
		selected, source, envRuntime, selectionCode, selectionReason = sel.Key, sel.Source, sel.EnvRuntime, sel.ReasonCode, sel.Reason
		// No settings without a chosen runtime this server knows: there is no
		// runtime they would apply to.
		if slices.Contains(aireport.RuntimeKeys, sel.Key) {
			st, err := svc.SettingsFor(ctx, sel.Key)
			if err != nil {
				writeErr(w, err)
				return
			}
			js := settingsJSON(st)
			settings, model = &js, st.Model
		}
		// The server has no viewer time zone here; the week is counted in UTC.
		week, err := aireport.WeekContaining(time.Now(), "UTC")
		if err != nil {
			writeErr(w, err)
			return
		}
		if usage, err = svc.UsageSince(ctx, week.Since); err != nil {
			writeErr(w, err)
			return
		}
		if usage == nil {
			usage = &store.AIUsageSummary{}
		}
	}
	// Shown even with no runtime built: the screen still says what an admin
	// would be turning on, which is the built-in Monday 06:00, off.
	schedule := scheduleJSON(aireport.ResolveSchedule(nil, nil, ""))
	if svc != nil {
		saved, err := svc.AutoSchedule(r.Context())
		if err != nil {
			writeErr(w, err)
			return
		}
		schedule = scheduleJSON(saved)
	}
	resp := enablementJSON(enablement)
	for k, v := range map[string]any{
		"runtimes":              runtimes,
		"selected_runtime":      selected,
		"runtime_source":        source,
		"env_runtime":           envRuntime,
		"selection_reason":      nullableString(selectionReason),
		"selection_reason_code": nullableString(selectionCode),
		"secrets_reason":        nullableString(secretsReason),
		"model":                 model,
		"settings":              settings,
		"schedule":              schedule,
		"usage_this_week":       usage,
	} {
		resp[k] = v
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeAICatalogError tells the admin which fix a failed catalog read needs:
// name a known runtime, configure it, log it in, or look at the runtime itself.
func writeAICatalogError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, aireport.ErrUnknownRuntime):
		writeAIError(w, http.StatusBadRequest, "unknown_runtime", "알 수 없는 AI 런타임입니다")
	case errors.Is(err, airuntime.ErrNotConfigured):
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "AI 런타임이 설정되지 않았습니다")
	case errors.Is(err, airuntime.ErrNotLoggedIn):
		writeAIError(w, http.StatusServiceUnavailable, "runtime_not_logged_in", "AI 런타임에 로그인되어 있지 않습니다")
	default:
		writeAIError(w, http.StatusBadGateway, "catalog_unavailable", "모델 목록을 불러오지 못했습니다: "+err.Error())
	}
}

// handleAdminAIModels lists ?runtime='s catalog, the active runtime's without it.
// Like the rest of AI settings it takes a browser session, not a CLI token.
func (s *Server) handleAdminAIModels(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminAISession(w, r, false); !ok {
		return
	}
	if s.aiReports == nil {
		writeAICatalogError(w, airuntime.ErrNotConfigured)
		return
	}
	models, err := s.aiReports.Models(r.Context(), r.URL.Query().Get("runtime"))
	if err != nil {
		writeAICatalogError(w, err)
		return
	}
	out := make([]adminAIModelJSON, 0, len(models))
	for _, m := range models {
		efforts := make([]adminAIEffortJSON, 0, len(m.SupportedReasoningEfforts))
		for _, e := range m.SupportedReasoningEfforts {
			efforts = append(efforts, adminAIEffortJSON{ReasoningEffort: e.ReasoningEffort, Description: e.Description})
		}
		out = append(out, adminAIModelJSON{
			ID: m.ID, DisplayName: m.DisplayName, Description: m.Description, IsDefault: m.IsDefault,
			DefaultReasoningEffort: m.DefaultReasoningEffort, SupportedReasoningEfforts: efforts,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

// handleSetAdminAISettings saves the admin's model and effort for ?runtime=,
// the active runtime without it. Empty values clear the override, handing that
// item back to the environment.
func (s *Server) handleSetAdminAISettings(w http.ResponseWriter, r *http.Request) {
	user, ok := adminAISession(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Model           *string `json:"model"`
		ReasoningEffort *string `json:"reasoning_effort"`
		BaseURL         *string `json:"base_url"`
	}
	// Pointers: a missing field must not decode to "" and silently clear a setting.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model == nil || body.ReasoningEffort == nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "model 과 reasoning_effort 가 모두 필요합니다")
		return
	}
	baseURL := ""
	if body.BaseURL != nil {
		baseURL = *body.BaseURL
	}
	if s.aiReports == nil {
		writeAICatalogError(w, airuntime.ErrNotConfigured)
		return
	}
	st, err := s.aiReports.SetSettings(r.Context(), r.URL.Query().Get("runtime"), *body.Model, *body.ReasoningEffort, baseURL, user.Email)
	switch {
	case errors.Is(err, aireport.ErrInvalidModel):
		// The message names the model and its source: an effort-only save can
		// fail on the environment's model, which the admin never sent.
		writeAIError(w, http.StatusBadRequest, "invalid_model", "모델 목록에 없는 모델입니다: "+err.Error())
	case errors.Is(err, aireport.ErrInvalidReasoningEffort):
		writeAIError(w, http.StatusBadRequest, "invalid_reasoning_effort", "이 모델이 지원하지 않는 reasoning effort 입니다: "+err.Error())
	case errors.Is(err, aireport.ErrInvalidBaseURL):
		writeAIError(w, http.StatusBadRequest, "invalid_base_url", err.Error())
	case errors.Is(err, aireport.ErrUnknownRuntime), errors.Is(err, airuntime.ErrNotConfigured), errors.Is(err, airuntime.ErrNotLoggedIn), errors.Is(err, airuntime.ErrUnavailable):
		writeAICatalogError(w, err)
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"settings": settingsJSON(st)})
	}
}
