package api

import (
	"net/http"

	"cctrace/internal/auth"
	"cctrace/internal/sessionview"
	"cctrace/internal/store"
)

// Source records are independent of optional AI records and analysis facts. The
// same visible-session view and caller scope as ordinary conversation reads apply.
func (s *Server) handleSessionInspection(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 100)
	offset := queryInt(r, "offset", 0)
	kind := r.URL.Query().Get("kind")
	if limit < 1 || limit > 200 || offset < 0 || (kind != "" && kind != "all" && kind != "context") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_inspection_request"})
		return
	}
	f := store.SessionRecordFilter{SessionID: r.PathValue("session_id"), Limit: limit + 1, Offset: offset, Order: "asc", ContextOnly: kind == "context",
		ProfileEmail: r.URL.Query().Get("profile_email"), UserID: r.URL.Query().Get("user_id"), LoginEmail: r.URL.Query().Get("login_email"), RelatedToolID: r.URL.Query().Get("tool_use_id")}
	if user, ok := auth.UserFromContext(r.Context()); ok && user != nil && user.Role == "user" {
		f.ProfileEmail = ""
		f.UserID = ""
		f.LoginEmail = ""
		s.applyUserSessionFilter(&f, user, "handleSessionInspection")
	}
	list := s.store.ListSessionLineage
	records, err := list(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	response := map[string]interface{}{"records": inspectionRecords(records[:min(len(records), limit)])}
	if len(records) > limit {
		response["next_offset"] = offset + limit
	}
	if id := r.URL.Query().Get("record_id"); id != "" {
		selectedFilter := f
		selectedFilter.ContextOnly = false
		selectedFilter.RelatedToolID = ""
		selectedFilter.Offset = 0
		selectedFilter.Limit = 1
		selectedFilter.Anchor = id
		selectedFilter.ExactAnchor = true
		selectedFilter.SourceFile = r.URL.Query().Get("source_file")
		selectedOffset := 0
		selectedFilter.StartOffset = &selectedOffset
		selected, readErr := list(r.Context(), selectedFilter)
		if readErr != nil {
			writeErr(w, readErr)
			return
		}
		if len(selected) == 0 || selectedOffset < 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "record_not_found"})
			return
		}
		response["selected_record"] = inspectionRecords(selected)[0]
	}
	writeJSON(w, http.StatusOK, response)
}

func inspectionRecords(records []*store.SessionRecord) []sessionRecordWithView {
	out := make([]sessionRecordWithView, 0, len(records))
	for _, record := range records {
		out = append(out, sessionRecordWithView{SessionRecord: record, Raw: record.Raw, View: sessionview.Normalize(record)})
	}
	return out
}
