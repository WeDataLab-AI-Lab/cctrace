package api

import (
	"errors"
	"net/http"
	"strconv"

	"cctrace/internal/auth"
	"cctrace/internal/sessionview"
	"cctrace/internal/store"
)

func (s *Server) handleTurnContext(w http.ResponseWriter, r *http.Request) {
	if !s.sessionAnalysisEnabled {
		writeJSON(w, 404, map[string]string{"error": "session_analysis_disabled"})
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeJSON(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	st, ok := s.store.(store.TurnContextStore)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "turn_context_unavailable"})
		return
	}
	a := store.SessionAnalysisAccess{User: user.Role == "user", DashboardUserID: user.ID}
	if s.useUserIDAccessControl {
		a.OwnerUserID = user.CctraceUserID
	} else {
		a.OwnerProfileEmail = user.Email
	}
	q := r.URL.Query()
	if a.User {
		a.ProfileEmail, _, a.UserID = s.resolveUserAccessParams(user)
	} else {
		a.ProfileEmail, a.LoginEmail, a.UserID = q.Get("profile_email"), q.Get("login_email"), q.Get("user_id")
	}
	limit := 20
	if raw := q.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeJSON(w, 400, map[string]string{"error": "invalid turn context request"})
			return
		}
	}
	v, err := st.TurnContext(r.Context(), r.PathValue("session_id"), r.PathValue("turn_key"), a, store.TurnContextRequest{Revision: q.Get("revision"), Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrTurnContextInvalid):
			writeJSON(w, 400, map[string]string{"error": "invalid turn context request"})
		case errors.Is(err, store.ErrTurnContextRevision):
			writeJSON(w, 409, map[string]string{"error": "expired turn context revision"})
		case errors.Is(err, store.ErrTurnContextTurnNotFound):
			writeJSON(w, 404, map[string]string{"error": "turn_not_found"})
		case errors.Is(err, store.ErrSessionAnalysisNotFound):
			writeJSON(w, 404, map[string]string{"error": "not found"})
		default:
			writeErr(w, err)
		}
		return
	}
	items := make([]sessionRecordWithView, 0, len(v.Records))
	for _, rec := range v.Records {
		items = append(items, sessionRecordWithView{SessionRecord: rec, View: sessionview.Normalize(rec)})
	}
	writeJSON(w, 200, map[string]any{"session_id": v.SessionID, "turn_key": v.TurnKey, "state": v.State, "revision": v.Revision, "records": items, "next_cursor": v.NextCursor})
}
