package api

import (
	"encoding/json"
	"net/http"

	"cctrace/internal/auth"
	"cctrace/internal/sessionview"
	"cctrace/internal/store"
)

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	f := store.SessionRecordFilter{
		ProfileEmail: r.URL.Query().Get("profile_email"),
		SessionID:    r.URL.Query().Get("session_id"),
		Limit:        queryInt(r, "limit", 100),
		Offset:       queryInt(r, "offset", 0),
		Order:        r.URL.Query().Get("order"),
	}
	if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == "user" {
		s.applyUserSessionFilter(&f, user, "handleListSessions")
	}

	// lineage=1 stitches the session with its branch/clear ancestors (subagent
	// files already share the session_id) for the assembled conversation view.
	list := s.store.ListSessionRecords
	if r.URL.Query().Get("lineage") == "1" {
		list = s.store.ListSessionLineage
	}

	records, err := list(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Agent runtimes differ in raw shape; the viewer renders view and nothing else.
	// raw stays for the individual raw-record list. The assembled view passes raw=0:
	// it polls every loaded page, and raw would roughly double each response.
	omitRaw := r.URL.Query().Get("raw") == "0"
	response := make([]sessionRecordWithView, 0, len(records))
	for _, record := range records {
		item := sessionRecordWithView{SessionRecord: record, View: sessionview.Normalize(record)}
		if !omitRaw {
			item.Raw = record.Raw
		}
		response = append(response, item)
	}
	writeJSON(w, http.StatusOK, response)
}

type sessionRecordWithView struct {
	*store.SessionRecord
	// Shadows the embedded Raw so it can be left out entirely rather than sent as null.
	Raw  json.RawMessage  `json:"raw,omitempty"`
	View sessionview.View `json:"view"`
}

func (s *Server) handleListSessionSummaries(w http.ResponseWriter, r *http.Request) {
	profileEmail := r.URL.Query().Get("profile_email")
	userID := ""
	if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == "user" {
		profileEmail, _, userID = s.resolveUserAccessParams(user)
	}
	limit := queryInt(r, "limit", 200)
	result, err := s.store.ListSessionSummaries(r.Context(), profileEmail, userID, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListSessionOverview(w http.ResponseWriter, r *http.Request) {
	since, err := optionalQueryTime(r, "since")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	until, err := optionalQueryTime(r, "until")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	profileEmail := r.URL.Query().Get("profile_email")
	loginEmail := r.URL.Query().Get("login_email")
	userID := ""
	if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == "user" {
		profileEmail, loginEmail, userID = s.resolveUserAccessParams(user)
	}
	result, err := s.store.ListSessionOverviews(r.Context(), store.SessionOverviewFilter{
		ProfileEmail:  profileEmail,
		LoginEmail:    loginEmail,
		UserID:        userID,
		Since:         since,
		Until:         until,
		Limit:         queryInt(r, "limit", 200),
		FoldLineage:   r.URL.Query().Get("assembled") == "1",
		Source:        r.URL.Query().Get("source"),
		Offset:        queryInt(r, "offset", 0),
		ProjectHashes: queryProjectHashes(r, "project_hashes"),
		Agent:         r.URL.Query().Get("agent"),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleSessionAccountSegments breaks one session into the stretches that
// belonged to each account. The list shows one row per session, so this is where
// a mid-session /login becomes readable.
func (s *Server) handleSessionAccountSegments(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session_id required"})
		return
	}
	// role=user breaks down only their own sessions. Someone else's and a missing
	// one answer the same 404, so the id cannot be used to learn which exist.
	if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == "user" {
		owned, err := s.sessionOwnedBy(r, user, sessionID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !owned {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
	}
	segments, err := s.store.SessionAccountSegments(r.Context(), sessionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, segments)
}

func (s *Server) handleCountSessionOverview(w http.ResponseWriter, r *http.Request) {
	since, err := optionalQueryTime(r, "since")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	until, err := optionalQueryTime(r, "until")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	profileEmail := r.URL.Query().Get("profile_email")
	loginEmail := r.URL.Query().Get("login_email")
	userID := ""
	restricted := false
	if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == "user" {
		profileEmail, loginEmail, userID = s.resolveUserAccessParams(user)
		restricted = true
	}
	count, err := s.store.CountSessionOverviews(r.Context(), store.SessionOverviewFilter{
		ProfileEmail:  profileEmail,
		LoginEmail:    loginEmail,
		UserID:        userID,
		Since:         since,
		Until:         until,
		FoldLineage:   r.URL.Query().Get("assembled") == "1",
		Source:        r.URL.Query().Get("source"),
		ProjectHashes: queryProjectHashes(r, "project_hashes"),
		Agent:         r.URL.Query().Get("agent"),
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	// Sessions an account filter necessarily drops, so the UI can say "N sessions
	// with no known account" instead of just showing a smaller list. Only computed
	// when an account filter is actually narrowing the view.
	//
	// Skipped for restricted users: their scope is pinned by user_id (or profile_email
	// in legacy mode), not by an account filter, so removing the account filter to count
	// unattributed sessions would count other people's sessions too.
	unattributed := 0
	if loginEmail != "" && !restricted {
		unattributed, err = s.store.CountSessionOverviews(r.Context(), store.SessionOverviewFilter{
			ProfileEmail:     profileEmail,
			LoginEmail:       loginEmail,
			UserID:           userID,
			Since:            since,
			Until:            until,
			FoldLineage:      r.URL.Query().Get("assembled") == "1",
			Source:           r.URL.Query().Get("source"),
			ProjectHashes:    queryCSV(r, "project_hashes"),
			Agent:            r.URL.Query().Get("agent"),
			OnlyUnattributed: true,
		})
		if err != nil {
			writeErr(w, err)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]int{"count": count, "unattributed": unattributed})
}
