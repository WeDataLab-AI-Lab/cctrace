package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
)

// writeAIDisabled is the refusal every report start and consent gets while AI
// reports are off.
func writeAIDisabled(w http.ResponseWriter) {
	writeAIError(w, http.StatusServiceUnavailable, "runtime_disabled", "AI 리포트가 꺼져 있습니다. 관리자에게 요청하세요")
}

// handleSetAdminAIEnabled saves the admin's AI report switch: true, false, or
// null to hand the decision back to the environment.
func (s *Server) handleSetAdminAIEnabled(w http.ResponseWriter, r *http.Request) {
	user, ok := adminAISession(w, r, true)
	if !ok {
		return
	}
	// A raw field: a missing "enabled" must not decode to null and clear the switch.
	var body map[string]json.RawMessage
	var enabled *bool
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil || body["enabled"] == nil || json.Unmarshal(body["enabled"], &enabled) != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "enabled 는 true, false 또는 null 이어야 합니다")
		return
	}
	svc := s.aiReports
	if svc == nil {
		writeAICatalogError(w, airuntime.ErrNotConfigured)
		return
	}
	e, err := svc.SetEnabled(r.Context(), enabled, user.Email)
	switch {
	case errors.Is(err, aireport.ErrNoRuntimeChosen):
		writeAIError(w, http.StatusConflict, "runtime_not_selected", "AI 리포트를 켜려면 이 서버에서 쓸 수 있는 런타임을 먼저 선택하세요")
	case errors.Is(err, aireport.ErrRuntimeNotConfigured):
		writeAIError(w, http.StatusConflict, "runtime_not_configured", "선택한 런타임이 설정되지 않아 AI 리포트를 켤 수 없습니다")
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, http.StatusOK, enablementJSON(e))
	}
}

func enablementJSON(e aireport.Enablement) map[string]any {
	return map[string]any{"enabled": e.Enabled, "enabled_source": e.Source, "env_enabled": e.EnvEnabled}
}
