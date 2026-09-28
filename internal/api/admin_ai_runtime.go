package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
)

// handleSetAdminAIRuntime saves the admin's runtime; "" clears the choice,
// handing it back to CCTRACE_AI_RUNTIME_DEFAULT, or to no runtime when unset.
func (s *Server) handleSetAdminAIRuntime(w http.ResponseWriter, r *http.Request) {
	user, ok := adminAISession(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Runtime *string `json:"runtime"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil || body.Runtime == nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "runtime 이 필요합니다")
		return
	}
	svc := s.aiReports
	if svc == nil {
		writeAICatalogError(w, airuntime.ErrNotConfigured)
		return
	}
	ctx := r.Context()
	sel, err := svc.SelectRuntime(ctx, *body.Runtime, user.Email)
	switch {
	case errors.Is(err, aireport.ErrUnknownRuntime):
		writeAIError(w, http.StatusBadRequest, "unknown_runtime", "알 수 없는 AI 런타임입니다")
		return
	case errors.Is(err, aireport.ErrRuntimeUnimplemented):
		writeAIError(w, http.StatusConflict, "runtime_unimplemented", "이 런타임은 아직 구현되지 않아 선택할 수 없습니다")
		return
	case errors.Is(err, airuntime.ErrNotConfigured):
		// The runtime's reason is on its row in GET /api/admin/ai.
		writeAIError(w, http.StatusConflict, "runtime_unconfigured", "구성되지 않은 런타임은 선택할 수 없습니다")
		return
	case errors.Is(err, aireport.ErrRunInProgress):
		writeAIError(w, http.StatusConflict, "run_in_progress", "리포트 생성 중에는 런타임을 바꿀 수 없습니다")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	var settings *adminAISettingsJSON
	if sel.Key != "" {
		st, err := svc.SettingsFor(ctx, sel.Key)
		if err != nil {
			writeErr(w, err)
			return
		}
		js := settingsJSON(st)
		settings = &js
	}
	writeJSON(w, http.StatusOK, map[string]any{"selected_runtime": sel.Key, "runtime_source": sel.Source, "settings": settings})
}

// writeAIKeyError names the fix each refusal needs. Nothing here carries the
// key: a store failure is logged with the key cut out, and answered with a
// fixed message.
func writeAIKeyError(w http.ResponseWriter, err error, key, secretsReason string) {
	switch {
	case errors.Is(err, aireport.ErrUnknownProvider):
		writeAIError(w, http.StatusNotFound, "unknown_provider", "알 수 없는 공급자입니다")
	case errors.Is(err, aireport.ErrInvalidAPIKey):
		writeAIError(w, http.StatusBadRequest, "invalid_request", "api_key 가 비어 있거나 형식이 올바르지 않습니다")
	case errors.Is(err, airuntime.ErrEnvManaged):
		writeAIError(w, http.StatusConflict, "env_managed", "환경변수로 관리 중인 키는 화면에서 바꿀 수 없습니다")
	case errors.Is(err, aireport.ErrSecretsUnavailable):
		if secretsReason == "" {
			secretsReason = "키를 암호화할 비밀값(CCTRACE_SECRETS_KEY 또는 JWT_SECRET)이 없습니다"
		}
		writeAIError(w, http.StatusServiceUnavailable, "secrets_unavailable", secretsReason)
	default:
		msg := err.Error()
		if key != "" {
			msg = strings.ReplaceAll(msg, key, "[redacted]")
		}
		log.Printf("[admin-ai] provider key change failed: %s", msg)
		writeAIError(w, http.StatusInternalServerError, "credential_store_failed", "키를 저장하지 못했습니다")
	}
}

// handleSetAdminAIProviderKey registers {provider}'s API key. The key is read
// from this body only, stored sealed, and never echoed or logged.
func (s *Server) handleSetAdminAIProviderKey(w http.ResponseWriter, r *http.Request) {
	user, ok := adminAISession(w, r, true)
	if !ok {
		return
	}
	var body struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "api_key 가 필요합니다")
		return
	}
	if s.aiReports == nil {
		writeAICatalogError(w, airuntime.ErrNotConfigured)
		return
	}
	kr := s.aiReports.Keyring()
	info, err := kr.SetKey(r.Context(), r.PathValue("provider"), body.APIKey, user.Email)
	if err != nil {
		writeAIKeyError(w, err, strings.TrimSpace(body.APIKey), kr.SecretsReason())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": credentialJSON(info)})
}

func (s *Server) handleDeleteAdminAIProviderKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminAISession(w, r, true); !ok {
		return
	}
	if s.aiReports == nil {
		writeAICatalogError(w, airuntime.ErrNotConfigured)
		return
	}
	kr := s.aiReports.Keyring()
	info, err := kr.DeleteKey(r.Context(), r.PathValue("provider"))
	if err != nil {
		writeAIKeyError(w, err, "", kr.SecretsReason())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": credentialJSON(info)})
}
