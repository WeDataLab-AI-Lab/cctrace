package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

const (
	maxQuotaOwnerSamplesBatch = store.MaxQuotaOwnerSamplesPerRequest
	maxQuotaOwnerSamplesBody  = 1 << 20
	maxQuotaBindingBody       = 16 << 10
)

type quotaBindingView struct {
	ID              int64      `json:"id"`
	BillingProvider string     `json:"billing_provider"`
	AccountID       string     `json:"account_id"`
	Status          string     `json:"status"`
	RequestedAt     time.Time  `json:"requested_at"`
	VerifiedAt      *time.Time `json:"verified_at,omitempty"`
}

type adminQuotaBindingView struct {
	ID                    int64      `json:"id"`
	DashboardUserID       int64      `json:"dashboard_user_id"`
	OwnerEmail            string     `json:"owner_email"`
	BillingProvider       string     `json:"billing_provider"`
	AccountID             string     `json:"account_id"`
	Status                string     `json:"status"`
	RequestedAt           time.Time  `json:"requested_at"`
	VerifiedAt            *time.Time `json:"verified_at,omitempty"`
	ApprovedBy            *int64     `json:"approved_by,omitempty"`
	VerificationMethod    string     `json:"verification_method,omitempty"`
	VerificationReference string     `json:"verification_reference,omitempty"`
}

func quotaBindingResponse(binding *store.QuotaAccountBinding) quotaBindingView {
	return quotaBindingView{
		ID: binding.ID, BillingProvider: binding.BillingProvider, AccountID: binding.AccountID,
		Status: binding.Status, RequestedAt: binding.RequestedAt, VerifiedAt: binding.VerifiedAt,
	}
}

func adminQuotaBindingResponse(binding *store.QuotaAccountBinding) adminQuotaBindingView {
	return adminQuotaBindingView{
		ID: binding.ID, DashboardUserID: binding.DashboardUserID, OwnerEmail: binding.OwnerEmail,
		BillingProvider: binding.BillingProvider, AccountID: binding.AccountID, Status: binding.Status,
		RequestedAt: binding.RequestedAt, VerifiedAt: binding.VerifiedAt, ApprovedBy: binding.ApprovedBy,
		VerificationMethod: binding.VerificationMethod, VerificationReference: binding.VerificationReference,
	}
}

func quotaOwnerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrQuotaBindingNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found", "code": "quota_binding_not_found"})
	case errors.Is(err, store.ErrQuotaBindingUnverified):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "quota account is not approved", "code": "quota_binding_unverified"})
	case errors.Is(err, store.ErrQuotaAccountExcluded):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "quota account is excluded", "code": "quota_account_excluded"})
	case errors.Is(err, store.ErrQuotaBindingInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid quota account request", "code": "invalid_quota_binding"})
	default:
		writeErr(w, err)
	}
}

func quotaDashboardOwner(w http.ResponseWriter, r *http.Request) (*auth.DashboardUser, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil || user.ID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, false
	}
	return user, true
}

func (s *Server) handleListMyQuotaBindings(w http.ResponseWriter, r *http.Request) {
	user, ok := quotaDashboardOwner(w, r)
	if !ok {
		return
	}
	bindings, err := s.store.ListQuotaAccountBindings(r.Context(), user.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	result := make([]quotaBindingView, 0, len(bindings))
	for _, binding := range bindings {
		result = append(result, quotaBindingResponse(binding))
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleApplyQuotaBinding(w http.ResponseWriter, r *http.Request) {
	user, ok := quotaDashboardOwner(w, r)
	if !ok {
		return
	}
	var body struct {
		BillingProvider string `json:"billing_provider"`
		AccountID       string `json:"account_id"`
	}
	if !quotaOwnerBody(r, maxQuotaBindingBody, &body) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid quota binding request"})
		return
	}
	binding, err := s.store.ApplyQuotaAccountBinding(r.Context(), user.ID, body.BillingProvider, body.AccountID)
	if err != nil {
		quotaOwnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, quotaBindingResponse(binding))
}

func (s *Server) handleListAdminQuotaBindings(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminFromRequest(w, r)
	if !ok {
		return
	}
	_ = admin
	bindings, err := s.store.ListAdminQuotaAccountBindings(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		quotaOwnerError(w, err)
		return
	}
	result := make([]adminQuotaBindingView, 0, len(bindings))
	for _, binding := range bindings {
		result = append(result, adminQuotaBindingResponse(binding))
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleApproveQuotaBinding(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminFromRequest(w, r)
	if !ok {
		return
	}
	bindingID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || bindingID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid quota binding id"})
		return
	}
	var body struct {
		VerificationMethod    string `json:"verification_method"`
		VerificationReference string `json:"verification_reference"`
		VerifiedExternally    *bool  `json:"verified_externally"`
	}
	if !quotaOwnerBody(r, maxQuotaBindingBody, &body) || body.VerifiedExternally == nil || !*body.VerifiedExternally {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "independent provider verification confirmation required"})
		return
	}
	binding, err := s.store.ApproveQuotaAccountBinding(r.Context(), bindingID, admin.ID, body.VerificationMethod, body.VerificationReference)
	if err != nil {
		quotaOwnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adminQuotaBindingResponse(binding))
}

func (s *Server) handleRevokeQuotaBinding(w http.ResponseWriter, r *http.Request) {
	admin, ok := adminFromRequest(w, r)
	if !ok {
		return
	}
	bindingID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || bindingID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid quota binding id"})
		return
	}
	binding, err := s.store.RevokeQuotaAccountBinding(r.Context(), bindingID, admin.ID)
	if err != nil {
		quotaOwnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adminQuotaBindingResponse(binding))
}

func (s *Server) handleGetMyQuotaUsage(w http.ResponseWriter, r *http.Request) {
	user, ok := quotaDashboardOwner(w, r)
	if !ok {
		return
	}
	now := time.Now().UTC()
	from, to, err := quotaUsageRange(r, now)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	accounts, err := s.store.ListQuotaUsage(r.Context(), user.ID, from, to, now)
	if err != nil {
		quotaOwnerError(w, err)
		return
	}
	if accounts == nil {
		accounts = []*store.QuotaUsageAccount{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "accounts": accounts})
}

func (s *Server) handleGetMyQuotaWindowComparison(w http.ResponseWriter, r *http.Request) {
	user, ok := quotaDashboardOwner(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	windowMinutes, err := strconv.Atoi(q.Get("window_minutes"))
	if err != nil || (windowMinutes != 300 && windowMinutes != 10080) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "window_minutes must be 300 or 10080"})
		return
	}
	limitWindows := 0
	if raw := q.Get("limit_windows"); raw != "" {
		limitWindows, err = strconv.Atoi(raw)
		if err != nil || limitWindows < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit_windows"})
			return
		}
	}
	comparison, err := s.store.CompareQuotaUsageWindows(r.Context(), user.ID, q.Get("billing_provider"), q.Get("account_id"), windowMinutes, limitWindows, time.Now().UTC())
	if err != nil {
		quotaOwnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comparison)
}

func (s *Server) handleInsertMyQuotaOwnerSamples(w http.ResponseWriter, r *http.Request) {
	token := auth.IngestTokenFromContext(r.Context())
	if token == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "per-user upload token required", "code": "owner_token_required"})
		return
	}
	user, err := s.store.GetDashboardUserByApiToken(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cannot resolve upload token"})
		return
	}
	if user == nil || !user.IsActive || user.ID <= 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "upload token has no active owner"})
		return
	}
	var body struct {
		Samples []*store.QuotaOwnerSample `json:"samples"`
	}
	if !quotaOwnerBody(r, maxQuotaOwnerSamplesBody, &body) || len(body.Samples) > maxQuotaOwnerSamplesBatch {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid quota sample batch"})
		return
	}
	inserted, err := s.store.InsertQuotaOwnerSamples(r.Context(), user.ID, body.Samples)
	if err != nil {
		quotaOwnerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": len(body.Samples), "inserted": inserted})
}

func quotaOwnerBody(r *http.Request, limit int64, out any) bool {
	if r.Body == nil {
		return false
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, limit+1))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		return false
	}
	var extra any
	return errors.Is(dec.Decode(&extra), io.EOF)
}

func quotaUsageRange(r *http.Request, now time.Time) (time.Time, time.Time, error) {
	q := r.URL.Query()
	to := now
	if raw := q.Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to must be an RFC3339 timestamp")
		}
		to = parsed
	}
	from := to.Add(-7 * 24 * time.Hour)
	if raw := q.Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from must be an RFC3339 timestamp")
		}
		from = parsed
	}
	if !to.After(from) || to.After(now.Add(5*time.Minute)) || to.Sub(from) > store.MaxQuotaUsageRange {
		return time.Time{}, time.Time{}, errors.New("quota usage range must be positive and at most 90 days")
	}
	return from, to, nil
}
