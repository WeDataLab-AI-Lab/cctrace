package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/auth"
)

type adminAILoginJSON struct {
	LoginID         string    `json:"login_id"`
	VerificationURL string    `json:"verification_url"`
	UserCode        string    `json:"user_code"`
	ExpiresAt       time.Time `json:"expires_at"`
	Status          string    `json:"status"`
	Error           *string   `json:"error"`
}

func loginJSON(l airuntime.DeviceLogin) adminAILoginJSON {
	return adminAILoginJSON{
		LoginID: l.ID, VerificationURL: l.VerificationURL, UserCode: l.UserCode,
		ExpiresAt: l.ExpiresAt.UTC(), Status: l.State, Error: nullableString(loginErrorCode(l.Error)),
	}
}

// loginErrorCode passes the runtime's codes and turns anything else into
// login_failed, so runtime text never reaches the screen.
func loginErrorCode(e string) string {
	switch e {
	case "", airuntime.LoginErrorFailed, airuntime.LoginErrorRuntimeExited, airuntime.LoginErrorAuthFileLinked:
		return e
	}
	return airuntime.LoginErrorFailed
}

type adminAIAccountJSON struct {
	AuthMode          string            `json:"auth_mode"`
	Email             string            `json:"email"`
	PlanType          string            `json:"plan_type"`
	EnvManaged        bool              `json:"env_managed"`
	AuthFileIsSymlink bool              `json:"auth_file_is_symlink"`
	Login             *adminAILoginJSON `json:"login"`
}

// writeAIAccountError names the fix each refusal needs. A runtime failure is
// answered with a fixed message: its text (paths, server output, perhaps part
// of a key) goes to the log only, with key, when set, cut from it.
func writeAIAccountError(w http.ResponseWriter, err error, key string) {
	switch {
	case errors.Is(err, airuntime.ErrNotConfigured):
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "AI 런타임이 설정되지 않았습니다")
	case errors.Is(err, airuntime.ErrEnvManaged):
		writeAIError(w, http.StatusConflict, "env_managed", "환경변수 CODEX_API_KEY 로 관리 중인 계정은 화면에서 바꿀 수 없습니다")
	case errors.Is(err, airuntime.ErrAuthFileIsSymlink):
		writeAIError(w, http.StatusConflict, "auth_file_is_symlink", "런타임의 auth.json 이 심볼릭 링크라 계정을 바꿀 수 없습니다")
	case errors.Is(err, aireport.ErrRunInProgress):
		writeAIError(w, http.StatusConflict, "run_in_progress", "리포트 생성 중에는 계정을 바꿀 수 없습니다")
	case errors.Is(err, airuntime.ErrLoginInProgress):
		writeAIError(w, http.StatusConflict, "login_in_progress", "진행 중인 로그인이 있습니다")
	case errors.Is(err, airuntime.ErrLoginNotFound):
		writeAIError(w, http.StatusNotFound, "login_not_found", "로그인을 찾을 수 없습니다")
	default:
		msg := err.Error()
		if key != "" {
			msg = strings.ReplaceAll(msg, key, "[redacted]")
		}
		log.Printf("[admin-ai] account change failed: %s", msg)
		writeAIError(w, http.StatusBadGateway, "login_failed", "계정을 변경하지 못했습니다")
	}
}

// adminAccounts resolves the runtime's account management for an admin,
// writing the response when either is missing. Writes check CSRF first.
func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request, write bool) (airuntime.AccountManager, bool) {
	if write && !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return nil, false
	}
	// A CLI token is a file on a client PC; whoever copies it must not read a
	// pending device code and enter it into their own account first.
	if auth.IsTokenAuth(r.Context()) {
		writeAIError(w, http.StatusForbidden, "session_required", "계정 관리는 브라우저 로그인 세션에서만 할 수 있습니다")
		return nil, false
	}
	if _, ok := adminFromRequest(w, r); !ok {
		return nil, false
	}
	if s.aiReports == nil {
		writeAIAccountError(w, airuntime.ErrNotConfigured, "")
		return nil, false
	}
	m, err := s.aiReports.Accounts()
	if err != nil {
		writeAIAccountError(w, err, "")
		return nil, false
	}
	return m, true
}

func (s *Server) handleAdminAIAccount(w http.ResponseWriter, r *http.Request) {
	m, ok := s.adminAccounts(w, r, false)
	if !ok {
		return
	}
	acct, err := m.Account(r.Context())
	if err != nil {
		writeAIAccountError(w, err, "")
		return
	}
	out := adminAIAccountJSON{
		AuthMode: acct.AuthMode, Email: acct.Email, PlanType: acct.PlanType,
		EnvManaged: acct.EnvManaged, AuthFileIsSymlink: acct.AuthFileIsSymlink,
	}
	if acct.Login != nil {
		l := loginJSON(*acct.Login)
		out.Login = &l
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdminAILogin starts a device code login or stores an API key. The key
// is read from this body only and is not stored, logged or echoed.
func (s *Server) handleAdminAILogin(w http.ResponseWriter, r *http.Request) {
	m, ok := s.adminAccounts(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Type   string `json:"type"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "type 이 필요합니다")
		return
	}
	switch body.Type {
	case "chatgpt_device_code":
		l, err := m.StartDeviceLogin(r.Context())
		if err != nil {
			writeAIAccountError(w, err, "")
			return
		}
		writeJSON(w, http.StatusOK, loginJSON(l))
	case "api_key":
		if strings.TrimSpace(body.APIKey) == "" {
			writeAIError(w, http.StatusBadRequest, "invalid_request", "api_key 가 필요합니다")
			return
		}
		if err := m.LoginAPIKey(r.Context(), body.APIKey); err != nil {
			writeAIAccountError(w, err, strings.TrimSpace(body.APIKey))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": airuntime.LoginSucceeded})
	default:
		writeAIError(w, http.StatusBadRequest, "invalid_request", "type 은 chatgpt_device_code 또는 api_key 입니다")
	}
}

func (s *Server) handleAdminAILoginStatus(w http.ResponseWriter, r *http.Request) {
	m, ok := s.adminAccounts(w, r, false)
	if !ok {
		return
	}
	l, err := m.DeviceLogin(r.PathValue("id"))
	if err != nil {
		writeAIAccountError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, loginJSON(l))
}

func (s *Server) handleCancelAdminAILogin(w http.ResponseWriter, r *http.Request) {
	m, ok := s.adminAccounts(w, r, true)
	if !ok {
		return
	}
	l, err := m.CancelDeviceLogin(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAIAccountError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, loginJSON(l))
}

func (s *Server) handleAdminAILogout(w http.ResponseWriter, r *http.Request) {
	m, ok := s.adminAccounts(w, r, true)
	if !ok {
		return
	}
	if err := m.Logout(r.Context()); err != nil {
		writeAIAccountError(w, err, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}
