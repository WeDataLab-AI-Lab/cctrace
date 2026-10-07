package api

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"

	"golang.org/x/crypto/bcrypt"
)

func generateTempPassword() (string, error) {
	const charset = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 12)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return string(b), nil
}

func requireAdmin(user *auth.DashboardUser, w http.ResponseWriter) bool {
	if user.Role != "admin" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
		return false
	}
	return true
}

func adminFromRequest(w http.ResponseWriter, r *http.Request) (*auth.DashboardUser, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, false
	}
	if !requireAdmin(user, w) {
		return nil, false
	}
	return user, true
}

func (s *Server) handleListDashboardUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	users, err := s.store.ListDashboardUsers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}

	// Strip password hashes from response
	type userResp struct {
		ID            int64  `json:"id"`
		Email         string `json:"email"`
		Role          string `json:"role"`
		Name          string `json:"name"`
		Team          string `json:"team"`
		IsActive      bool   `json:"is_active"`
		CreatedAt     string `json:"created_at"`
		CctraceUserID string `json:"cctrace_user_id"`
		HasApiToken   bool   `json:"has_api_token"`
	}
	result := make([]userResp, len(users))
	for i, u := range users {
		result[i] = userResp{
			ID:            u.ID,
			Email:         u.Email,
			Role:          u.Role,
			Name:          u.Name,
			Team:          u.Team,
			IsActive:      u.IsActive,
			CreatedAt:     u.CreatedAt.Format("2006-01-02T15:04:05Z"),
			CctraceUserID: u.CctraceUserID,
			HasApiToken:   u.ApiToken != "",
		}
	}
	writeJSON(w, http.StatusOK, result)
}

// handleUserNameMap returns {cctrace_user_id: name} for all dashboard users.
// Available to all authenticated users (no admin required).
// visibleUserNames maps cctrace_user_id -> name for every user that has both.
//
// #299 narrowed this to the caller's own entry under user_id isolation, to match
// the isolation model the docs describe. Nothing in the product ever matched it:
// isolation covers session CONTENTS, and it never covered the cost and usage
// aggregates. Those name every user's spend and always have. So the narrowing hid
// no data -- the rows were already on screen -- it only removed the labels from
// them, and Overview drew other users' handles next to bars whose amounts were
// fully visible. A redaction that leaves the value and takes the name protects
// nobody and costs the reader the one thing that made the chart legible.
//
// If aggregates should ever be scoped per user, that belongs in the queries that
// produce them, not in the map that labels their output.
func visibleUserNames(users []*store.DashboardUser) map[string]string {
	result := make(map[string]string, len(users))
	for _, u := range users {
		if u.CctraceUserID == "" || u.Name == "" {
			continue
		}
		result[u.CctraceUserID] = u.Name
	}
	return result
}

func (s *Server) handleUserNameMap(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListDashboardUsers(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, visibleUserNames(users))
}

func (s *Server) handleCreateDashboardUser(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}

	var body struct {
		Email         string `json:"email"`
		Name          string `json:"name"`
		Team          string `json:"team"`
		Role          string `json:"role"`
		CctraceUserID string `json:"cctrace_user_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var missing []string
	if body.Email == "" {
		missing = append(missing, "email")
	}
	if body.Name == "" {
		missing = append(missing, "name")
	}
	if body.Team == "" {
		missing = append(missing, "team")
	}
	if body.Role == "" {
		missing = append(missing, "role")
	}
	if body.CctraceUserID == "" {
		missing = append(missing, "cctrace_user_id")
	}
	if len(missing) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "required fields: " + strings.Join(missing, ", ")})
		return
	}
	if body.Role != "admin" && body.Role != "user" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role must be admin or user"})
		return
	}

	tempPass, err := generateTempPassword()
	if err != nil {
		writeErr(w, err)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(tempPass), passwordHashCost)
	if err != nil {
		writeErr(w, err)
		return
	}

	newUser := &store.DashboardUser{
		Email:              body.Email,
		PasswordHash:       string(hash),
		Role:               body.Role,
		Name:               body.Name,
		Team:               body.Team,
		CctraceUserID:      body.CctraceUserID,
		MustChangePassword: true,
	}
	newUser, err = s.store.CreateDashboardUser(r.Context(), newUser)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique") {
			msg := "email already exists"
			if strings.Contains(err.Error(), "cctrace_user_id") {
				msg = "cctrace_user_id already assigned to another user"
			}
			writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
			return
		}
		writeErr(w, err)
		return
	}

	log.Printf("[audit] action=create_user actor=%s target=%s role=%s", user.Email, newUser.Email, newUser.Role)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":                   newUser.ID,
		"email":                newUser.Email,
		"role":                 newUser.Role,
		"name":                 newUser.Name,
		"team":                 newUser.Team,
		"is_active":            newUser.IsActive,
		"created_at":           newUser.CreatedAt.Format("2006-01-02T15:04:05Z"),
		"cctrace_user_id":      newUser.CctraceUserID,
		"must_change_password": newUser.MustChangePassword,
		"temp_password":        tempPass,
	})
}

func (s *Server) handleUpdateDashboardUser(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}

	var body store.UpdateDashboardUserParams
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if body.Role != nil && *body.Role != "admin" && *body.Role != "user" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role must be admin or user"})
		return
	}
	// Create requires a team and `cctrace init` refuses a profile without one,
	// so an update may change it but not clear it.
	if body.Team != nil && strings.TrimSpace(*body.Team) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "team must not be empty"})
		return
	}

	if err := s.store.UpdateDashboardUser(r.Context(), id, body); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique") {
			msg := "email already exists"
			if strings.Contains(err.Error(), "cctrace_user_id") {
				msg = "cctrace_user_id already assigned to another user"
			}
			writeJSON(w, http.StatusConflict, map[string]string{"error": msg})
			return
		}
		writeErr(w, err)
		return
	}

	updated, err := s.store.GetDashboardUserByID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}

	log.Printf("[audit] action=update_user actor=%s target=%s detail=%+v", user.Email, updated.Email, body)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":              updated.ID,
		"email":           updated.Email,
		"role":            updated.Role,
		"name":            updated.Name,
		"team":            updated.Team,
		"is_active":       updated.IsActive,
		"cctrace_user_id": updated.CctraceUserID,
	})
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}

	tempPass, err := generateTempPassword()
	if err != nil {
		writeErr(w, err)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(tempPass), passwordHashCost)
	if err != nil {
		writeErr(w, err)
		return
	}

	if err := s.store.UpdateDashboardUserPassword(r.Context(), id, string(hash), true); err != nil {
		writeErr(w, err)
		return
	}

	target, _ := s.store.GetDashboardUserByID(r.Context(), id)
	targetEmail := fmt.Sprintf("id:%d", id)
	if target != nil {
		targetEmail = target.Email
	}
	log.Printf("[audit] action=reset_password actor=%s target=%s", user.Email, targetEmail)

	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "temp_password": tempPass})
}

func (s *Server) handleRevokeApiToken(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}

	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user id"})
		return
	}

	if err := s.store.DeleteAllDashboardUserAPITokens(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}

	target, _ := s.store.GetDashboardUserByID(r.Context(), id)
	targetEmail := fmt.Sprintf("id:%d", id)
	if target != nil {
		targetEmail = target.Email
	}
	log.Printf("[audit] action=revoke_api_token actor=%s target=%s", user.Email, targetEmail)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleBackfillPreview measures what the login_email backfill would change,
// without changing it. It exists so the row count is read from the same code
// that will run the UPDATE rather than from a hand-written estimate.
func (s *Server) handleBackfillPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	// No `since` means the unbounded pass, which is what a production run is
	// being approved for; the periodic pass bounds itself.
	var since time.Time
	if t, err := optionalQueryTime(r, "since"); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	} else if t != nil {
		since = *t
	}

	preview, err := s.store.PreviewBackfillSessionRecordLoginEmail(r.Context(), since)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// handleInferencePreview measures what the user-timeline inference pass (#346)
// would change, without changing it.
//
// It is a second endpoint rather than more fields on handleBackfillPreview
// because the two measure different passes and their numbers are not comparable:
// backfill-preview's EmptyRows counts only rows with a session_id (the only ones
// a session-scoped pass could ever reach) while this one counts every
// unattributed row, and this pass reports users and an excluded-account tiebreak
// count that the other has no analogue for. Folding them into one body would also
// change a response shape operators already read, and force whoever wants one
// pass to run and parse the other.
func (s *Server) handleInferencePreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	// Same convention as handleBackfillPreview: no `since` means the unbounded
	// pass, which is what a production run is approved for.
	var since time.Time
	if t, err := optionalQueryTime(r, "since"); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	} else if t != nil {
		since = *t
	}

	preview, err := s.store.PreviewInferSessionRecordLoginEmail(r.Context(), since)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// handleCodexInferredRevertPreview measures what reverting #524's OTEL-timeline
// attribution on Codex rows would clear, without changing it. Unscoped like the
// pass it previews: the defect is not confined to a recent window, and a partial
// revert would leave the rest of the fiction on screen.
func (s *Server) handleCodexInferredRevertPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	preview, err := s.store.PreviewCodexInferredRevert(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// handleCodexAccountPreview measures what FillCodexAccountFromQuota would write:
// the account_id it would carry from quota_samples onto Codex records that have
// none, without changing anything.
func (s *Server) handleCodexAccountPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	// Same convention as handleBackfillPreview: no `since` means the unbounded
	// pass, which is what a production run is approved for.
	var since time.Time
	if t, err := optionalQueryTime(r, "since"); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	} else if t != nil {
		since = *t
	}

	preview, err := s.store.PreviewCodexAccountFill(r.Context(), since)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// handleCodexAttributionPreview measures what AttributeCodexLoginEmailFromQuota
// would write: the subscription address each Codex account bills, taken from the
// account-to-address mapping quota_samples observed, without changing anything.
func (s *Server) handleCodexAttributionPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	// Same convention as handleBackfillPreview: no `since` means the unbounded
	// pass, which is what a production run is approved for.
	var since time.Time
	if t, err := optionalQueryTime(r, "since"); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	} else if t != nil {
		since = *t
	}

	preview, err := s.store.PreviewCodexQuotaAttribution(r.Context(), since)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// handleAccountSwitchStats reports which user_ids carry more than one login
// account. It is the durable counterpart to the `user_id_email_mismatch` audit
// log, which lives in process memory and resets on restart.
func (s *Server) handleAccountSwitchStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	var since time.Time
	if t, err := optionalQueryTime(r, "since"); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	} else if t != nil {
		since = *t
	}

	stats, err := s.store.AccountSwitchStats(r.Context(), since)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleOtelUserIDs(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	ids, err := s.store.ListOtelUserIDs(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, ids)
}
