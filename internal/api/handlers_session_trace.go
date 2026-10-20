package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"cctrace/internal/auth"
	"cctrace/internal/sessionview"
	"cctrace/internal/store"
)

func (s *Server) traceCaller(w http.ResponseWriter, r *http.Request) (store.TraceStore, store.SessionAnalysisAccess, bool) {
	var a store.SessionAnalysisAccess
	if !s.sessionAnalysisEnabled {
		writeJSON(w, 404, map[string]string{"error": "session_analysis_disabled"})
		return nil, a, false
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return nil, a, false
	}
	st, ok := s.store.(store.TraceStore)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "trace_unavailable"})
		return nil, a, false
	}
	a.User = user.Role == "user"
	a.DashboardUserID = user.ID
	if s.useUserIDAccessControl {
		a.OwnerUserID = user.CctraceUserID
	} else {
		a.OwnerProfileEmail = user.Email
	}
	if a.User {
		a.ProfileEmail, _, a.UserID = s.resolveUserAccessParams(user)
	} else {
		a.ProfileEmail = r.URL.Query().Get("profile_email")
		a.LoginEmail = r.URL.Query().Get("login_email")
		a.UserID = r.URL.Query().Get("user_id")
	}
	return st, a, true
}
func traceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrTraceInvalid):
		writeJSON(w, 400, map[string]string{"error": "invalid trace range or cursor"})
	case errors.Is(err, store.ErrTraceSnapshot):
		writeJSON(w, 409, map[string]string{"error": "expired trace snapshot"})
	case errors.Is(err, store.ErrSessionAnalysisNotFound):
		writeJSON(w, 404, map[string]string{"error": "not found"})
	default:
		writeErr(w, err)
	}
}
func (s *Server) handleTraceDemand(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.traceCaller(w, r)
	if !ok {
		return
	}
	var d store.TraceDemandRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&d) != nil || !d.Valid() {
		traceErr(w, store.ErrTraceInvalid)
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		traceErr(w, store.ErrTraceInvalid)
		return
	}
	v, err := st.TraceDemand(r.Context(), r.PathValue("session_id"), a, d)
	if err != nil {
		traceErr(w, err)
		return
	}
	status := http.StatusOK
	if v.State == "building" {
		status = http.StatusAccepted
	}
	writeJSON(w, status, v)
}
func (s *Server) handleTraceRelease(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.traceCaller(w, r)
	if !ok {
		return
	}
	if err := st.TraceRelease(r.Context(), r.PathValue("session_id"), a, r.PathValue("lease_id")); err != nil {
		traceErr(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleTraceOverview(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.traceCaller(w, r)
	if !ok {
		return
	}
	v, err := st.TraceOverview(r.Context(), r.PathValue("session_id"), a, r.URL.Query().Get("snapshot_id"))
	if err != nil {
		traceErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) handleTraceEvents(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.traceCaller(w, r)
	if !ok {
		return
	}
	from, err := optionalQueryTime(r, "from")
	if err != nil {
		traceErr(w, store.ErrTraceInvalid)
		return
	}
	to, err := optionalQueryTime(r, "to")
	if err != nil || from == nil || to == nil || !from.Before(*to) {
		traceErr(w, store.ErrTraceInvalid)
		return
	}
	limit := 1000
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			traceErr(w, store.ErrTraceInvalid)
			return
		}
	}
	v, err := st.TraceEvents(r.Context(), r.PathValue("session_id"), a, store.TraceEventsRequest{SnapshotID: r.URL.Query().Get("snapshot_id"), From: from, To: to, Cursor: r.URL.Query().Get("cursor"), Limit: limit})
	if err != nil {
		traceErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) handleTraceContext(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.traceCaller(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	if (q.Get("event_id") == "") == (q.Get("turn_key") == "") {
		traceErr(w, store.ErrTraceInvalid)
		return
	}
	v, err := st.TraceContext(r.Context(), r.PathValue("session_id"), a, store.TraceContextRequest{SnapshotID: q.Get("snapshot_id"), EventID: q.Get("event_id"), TurnKey: q.Get("turn_key"), Cursor: q.Get("cursor")})
	if err != nil {
		traceErr(w, err)
		return
	}
	// Share the canonical conversation projection with the basic viewer.
	items := make([]sessionRecordWithView, 0, len(v.Records))
	for _, rec := range v.Records {
		items = append(items, sessionRecordWithView{SessionRecord: rec, Raw: rec.Raw, View: sessionview.Normalize(rec)})
	}
	writeJSON(w, 200, map[string]any{"session_id": v.SessionID, "snapshot_id": v.SnapshotID, "event_id": v.EventID, "records": items, "next_cursor": v.NextCursor})
}
