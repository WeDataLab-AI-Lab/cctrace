package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cctrace/internal/auth"
	"cctrace/internal/store"

	"golang.org/x/crypto/bcrypt"
)

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.CountDashboardUsers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": count == 0})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	// CSRF check
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	if s.setupToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Setup-Token")), []byte(s.setupToken)) != 1 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid setup token or setup disabled"})
		return
	}

	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if body.Email == "" || body.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email and password required"})
		return
	}
	if len(body.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password must be at least 8 characters"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), 12)
	if err != nil {
		writeErr(w, err)
		return
	}

	user := &store.DashboardUser{
		Email:        body.Email,
		PasswordHash: string(hash),
		Role:         "admin",
		Name:         body.Name,
	}
	err = s.store.CreateInitialDashboardUser(r.Context(), user)
	if errors.Is(err, store.ErrSetupAlreadyCompleted) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "setup already completed"})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	log.Printf("[audit] action=setup actor=%s detail=first_admin_created", user.Email)

	s.setAuthCookies(w, r, user)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"user": map[string]interface{}{
			"id":                   user.ID,
			"email":                user.Email,
			"role":                 user.Role,
			"name":                 user.Name,
			"cctrace_user_id":      user.CctraceUserID,
			"must_change_password": false,
		},
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	user, err := s.store.GetDashboardUserByEmail(r.Context(), body.Email)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	if !user.IsActive {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "account is disabled"})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(body.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}

	log.Printf("[audit] action=login actor=%s", user.Email)

	s.setAuthCookies(w, r, user)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user": map[string]interface{}{
			"id":                   user.ID,
			"email":                user.Email,
			"role":                 user.Role,
			"name":                 user.Name,
			"cctrace_user_id":      user.CctraceUserID,
			"must_change_password": user.MustChangePassword,
		},
	})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("cctrace_refresh")
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no refresh token"})
		return
	}

	claims, err := s.jwtMgr.ValidateToken(cookie.Value)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired refresh token"})
		return
	}
	if claims.Type != "refresh" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token type"})
		return
	}

	// Re-fetch user to get current role/name/active status
	user, err := s.store.GetDashboardUserByID(r.Context(), claims.UserID)
	if err != nil || !user.IsActive {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "user not found or disabled"})
		return
	}

	// Generate new access token only
	accessToken, err := s.jwtMgr.GenerateAccessToken(user.ID, user.Email, user.Role, user.Name, user.CctraceUserID, user.MustChangePassword)
	if err != nil {
		writeErr(w, err)
		return
	}

	s.setAccessCookie(w, r, accessToken)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user": map[string]interface{}{
			"id":                   user.ID,
			"email":                user.Email,
			"role":                 user.Role,
			"name":                 user.Name,
			"cctrace_user_id":      user.CctraceUserID,
			"must_change_password": user.MustChangePassword,
		},
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":                   user.ID,
		"email":                user.Email,
		"role":                 user.Role,
		"name":                 user.Name,
		"cctrace_user_id":      user.CctraceUserID,
		"must_change_password": user.MustChangePassword,
	})
}

func (s *Server) handleListOwnAPITokens(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	tokens, err := s.store.ListDashboardUserAPITokens(r.Context(), user.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if tokens == nil {
		tokens = []*store.DashboardAPIToken{}
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Server) handleCreateOwnAPIToken(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var body struct {
		Name           string     `json:"name"`
		ExpirationMode string     `json:"expiration_mode"`
		ExpiresAt      *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be between 1 and 64 characters"})
		return
	}
	expiresAt, err := validateAPITokenExpiration(body.ExpirationMode, body.ExpiresAt, true)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	apiToken, err := auth.GenerateAPIToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	metadata, err := s.store.CreateDashboardUserAPIToken(r.Context(), user.ID, body.Name, apiToken, "web", expiresAt)
	if err != nil {
		writeErr(w, err)
		return
	}

	log.Printf("[audit] action=create_own_api_token actor=%s token_id=%d", user.Email, metadata.ID)
	writeJSON(w, http.StatusCreated, newAPITokenSecretResponse(metadata, apiToken))
}

func (s *Server) handleUpdateOwnAPIToken(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	tokenID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || tokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid token id"})
		return
	}
	var body struct {
		IsActive       *bool      `json:"is_active"`
		ExpirationMode string     `json:"expiration_mode"`
		ExpiresAt      *time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if (body.IsActive == nil) == (body.ExpirationMode == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provide either is_active or expiration_mode"})
		return
	}

	var metadata *store.DashboardAPIToken
	if body.IsActive != nil {
		metadata, err = s.store.SetDashboardUserAPITokenActive(r.Context(), user.ID, tokenID, *body.IsActive)
	} else {
		expiresAt, validationErr := validateAPITokenExpiration(body.ExpirationMode, body.ExpiresAt, false)
		if validationErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": validationErr.Error()})
			return
		}
		metadata, err = s.store.SetDashboardUserAPITokenExpiration(r.Context(), user.ID, tokenID, expiresAt)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if metadata == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
		return
	}

	log.Printf("[audit] action=update_own_api_token actor=%s token_id=%d", user.Email, tokenID)
	writeJSON(w, http.StatusOK, metadata)
}

func (s *Server) handleRotateOwnAPIToken(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	tokenID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || tokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid token id"})
		return
	}
	apiToken, err := auth.GenerateAPIToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	metadata, err := s.store.RotateDashboardUserAPIToken(r.Context(), user.ID, tokenID, apiToken)
	if err != nil {
		writeErr(w, err)
		return
	}
	if metadata == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
		return
	}

	log.Printf("[audit] action=rotate_own_api_token actor=%s token_id=%d", user.Email, tokenID)
	writeJSON(w, http.StatusOK, newAPITokenSecretResponse(metadata, apiToken))
}

func (s *Server) handleDeleteOwnAPIToken(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	tokenID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || tokenID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid token id"})
		return
	}
	deleted, err := s.store.DeleteDashboardUserAPIToken(r.Context(), user.ID, tokenID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
		return
	}

	log.Printf("[audit] action=revoke_own_api_token actor=%s token_id=%d", user.Email, tokenID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func newAPITokenSecretResponse(metadata *store.DashboardAPIToken, secret string) map[string]interface{} {
	return map[string]interface{}{
		"id":          metadata.ID,
		"name":        metadata.Name,
		"token_hint":  metadata.TokenHint,
		"created_via": metadata.CreatedVia,
		"is_active":   metadata.IsActive,
		"is_expired":  metadata.IsExpired,
		"expires_at":  metadata.ExpiresAt,
		"created_at":  metadata.CreatedAt,
		"rotated_at":  metadata.RotatedAt,
		"api_token":   secret,
	}
}

func validateAPITokenExpiration(mode string, expiresAt *time.Time, allowDefault bool) (*time.Time, error) {
	if mode == "" && allowDefault {
		return nil, nil
	}
	if mode == "unlimited" {
		return nil, nil
	}
	if mode != "custom" {
		return nil, fmt.Errorf("expiration_mode must be unlimited or custom")
	}
	if expiresAt == nil || !expiresAt.After(time.Now()) {
		return nil, fmt.Errorf("expires_at must be a future RFC3339 timestamp")
	}
	return expiresAt, nil
}

// setAuthCookies sets both access and refresh JWT cookies.
func (s *Server) setAuthCookies(w http.ResponseWriter, r *http.Request, user *store.DashboardUser) {
	accessToken, err := s.jwtMgr.GenerateAccessToken(user.ID, user.Email, user.Role, user.Name, user.CctraceUserID, user.MustChangePassword)
	if err != nil {
		log.Printf("[auth] access token generation failed: %v", err)
		return
	}
	refreshToken, err := s.jwtMgr.GenerateRefreshToken(user.ID, user.Email)
	if err != nil {
		log.Printf("[auth] refresh token generation failed: %v", err)
		return
	}

	secure := s.isSecureRequest(r)
	http.SetCookie(w, &http.Cookie{
		Name:     "cctrace_token",
		Value:    accessToken,
		Path:     "/",
		MaxAge:   900, // 15 minutes
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "cctrace_refresh",
		Value:    refreshToken,
		Path:     "/api/auth/refresh",
		MaxAge:   604800, // 7 days
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) setAccessCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := s.isSecureRequest(r)
	http.SetCookie(w, &http.Cookie{
		Name:     "cctrace_token",
		Value:    token,
		Path:     "/",
		MaxAge:   900, // 15 minutes
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   "cctrace_token",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name:   "cctrace_refresh",
		Value:  "",
		Path:   "/api/auth/refresh",
		MaxAge: -1,
	})
}

func (s *Server) isSecureRequest(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || s.cookieSecure
}

func (s *Server) handleCLIAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID   string `json:"user_id"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	user, ok := s.authenticateCLIPassword(w, r, body.UserID, body.Password)
	if !ok {
		return
	}

	// Issue per-user API token if not already set
	apiToken := user.ApiToken
	if apiToken != "" {
		if _, err := s.store.GetDashboardUserByApiToken(r.Context(), apiToken); err != nil {
			apiToken = ""
		}
	}
	if apiToken == "" {
		var err error
		apiToken, err = auth.GenerateAPIToken()
		if err != nil {
			writeErr(w, err)
			return
		}
		if err := s.store.SetDashboardUserApiToken(r.Context(), user.ID, apiToken); err != nil {
			writeErr(w, err)
			return
		}
	}

	log.Printf("[audit] action=cli_auth user_id=%s email=%s", body.UserID, user.Email)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":                 user.Name,
		"email":                user.Email,
		"team":                 user.Team,
		"must_change_password": user.MustChangePassword,
		"api_token":            apiToken,
	})
}

// authenticateCLIPassword checks a cctrace user ID and password the way every
// CLI credential exchange must: active account, matching password, and no
// pending temporary password. It writes the refusal itself.
func (s *Server) authenticateCLIPassword(w http.ResponseWriter, r *http.Request, userID, password string) (*store.DashboardUser, bool) {
	if userID == "" || password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and password required"})
		return nil, false
	}
	user, err := s.store.GetDashboardUserByCctraceUserID(r.Context(), userID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return nil, false
	}
	if !user.IsActive {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "account is disabled"})
		return nil, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return nil, false
	}
	if user.MustChangePassword {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"error":                "password_change_required",
			"must_change_password": true,
		})
		return nil, false
	}
	return user, true
}

// cliReadTokenPrefix names read tokens issued by `cctrace auth read`; the device
// suffix tells machines apart in Settings.
const cliReadTokenPrefix = "CLI read token"

// handleCLIReadToken issues a token for the read-only Open API to a CLI user who
// re-enters their password.
//
// The token `cctrace init` stores is for uploading and is refused by the read API
// on purpose (#292): those tokens were never issued for reading, and opening the
// read API to all of them at once would have happened without anyone asking.
// This is the asking. The token is stored as created_via cli_read: it reads like
// a Settings token and can be revoked there, and the read API refuses it once its
// owner is an administrator (store.ErrCLIReadTokenAdmin).
//
// Administrators are refused here too: their tokens read every user's data, and
// a CLI token lives in plaintext in a profile file.
//
// replace names the token the client currently holds. Only that exact CLI read
// token is removed; matching by name removed tokens on other machines sharing a
// hostname, and Settings tokens that happened to share the name.
func (s *Server) handleCLIReadToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID   string `json:"user_id"`
		Password string `json:"password"`
		Device   string `json:"device"`
		Replace  string `json:"replace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	user, ok := s.authenticateCLIPassword(w, r, body.UserID, body.Password)
	if !ok {
		return
	}
	switch user.Role {
	case "user":
	case "admin":
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "administrator tokens read every user's data; create one in the dashboard under Settings > API Access Tokens",
			"code":  "admin_read_token_forbidden",
		})
		return
	default:
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	token, err := auth.GenerateAPIToken()
	if err != nil {
		writeErr(w, err)
		return
	}
	created, err := s.store.CreateDashboardUserAPIToken(r.Context(), user.ID, cliReadTokenName(body.Device), token, "cli_read", nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Removed only after the new token exists, so a failure here leaves an extra
	// token to revoke rather than a request that took the old one and gave nothing.
	replaced := false
	if body.Replace != "" {
		if replaced, err = s.store.DeleteCLIReadToken(r.Context(), user.ID, body.Replace); err != nil {
			writeErr(w, err)
			return
		}
	}

	log.Printf("[audit] action=cli_read_token user_id=%s token_id=%d replaced=%t", body.UserID, created.ID, replaced)
	writeJSON(w, http.StatusOK, map[string]string{"api_token": token, "name": created.Name})
}

// cliReadTokenName keeps the device label printable and the whole name within
// the 64 characters Settings allows.
func cliReadTokenName(device string) string {
	var b strings.Builder
	for _, r := range device {
		if unicode.IsPrint(r) && !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	label := b.String()
	if label == "" {
		return cliReadTokenPrefix
	}
	// Settings counts bytes, so trim whole runes until the name fits.
	for maxBytes := 64 - len(cliReadTokenPrefix+" ()"); len(label) > maxBytes; {
		_, size := utf8.DecodeLastRuneInString(label)
		label = label[:len(label)-size]
	}
	return cliReadTokenPrefix + " (" + label + ")"
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if body.CurrentPassword == "" || body.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "current_password and new_password required"})
		return
	}
	if len(body.NewPassword) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password must be at least 8 characters"})
		return
	}

	// Fetch full user to verify current password
	dbUser, err := s.store.GetDashboardUserByID(r.Context(), user.ID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "user not found"})
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(dbUser.PasswordHash), []byte(body.CurrentPassword)); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), 12)
	if err != nil {
		writeErr(w, err)
		return
	}

	if err := s.store.UpdateDashboardUserPassword(r.Context(), user.ID, string(hash), false); err != nil {
		writeErr(w, err)
		return
	}

	log.Printf("[audit] action=change_password actor=%s", user.Email)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func csrfCheck(r *http.Request) bool {
	if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
		return true
	}
	return r.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// withCSRF protects browser authentication and dashboard routes. Token-only
// ingestion and open API routes do not use this middleware.
func (s *Server) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		reason := s.csrfFailure(r)
		if reason != "" {
			log.Printf("[api] CSRF rejected: method=%s path=%s reason=%s", r.Method, r.URL.Path, reason)
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed: " + reason})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) csrfFailure(r *http.Request) string {
	origin := r.Header.Get("Origin")
	referer := false
	if origin == "" {
		origin = r.Header.Get("Referer")
		referer = true
	}
	if origin == "" {
		return "missing Origin/Referer headers"
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "invalid Origin/Referer"
	}
	if !referer && (u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery) {
		return "invalid Origin"
	}
	// Compare the browser authority with the preserved Host. TLS may terminate
	// at a reverse proxy; untrusted forwarding headers are deliberately ignored.
	if u.Host != r.Host && !s.isAllowedOrigin(u.Scheme+"://"+u.Host) {
		return "Origin/Referer is not allowed"
	}
	if !csrfCheck(r) {
		return "missing or invalid X-Requested-With"
	}
	return ""
}
