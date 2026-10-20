package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func (s *Server) personalCaller(w http.ResponseWriter, r *http.Request) (*auth.DashboardUser, string, string, bool) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return nil, "", "", false
	}
	email, _, uid := s.resolveUserAccessParams(user)
	return user, email, uid, true
}

func personalError(w http.ResponseWriter, err error, latest interface{}) {
	switch {
	case errors.Is(err, store.ErrPersonalNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, store.ErrPersonalInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid personal state"})
	case errors.Is(err, store.ErrPersonalConflict):
		writeJSON(w, http.StatusConflict, map[string]interface{}{"error": "revision conflict", "current": latest})
	default:
		writeErr(w, err)
	}
}

func personalBody(r *http.Request, out interface{}) bool {
	if r.Body == nil {
		return false
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 32769))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		return false
	}
	var extra interface{}
	return errors.Is(dec.Decode(&extra), io.EOF)
}

func (s *Server) handleGetPersonalSession(w http.ResponseWriter, r *http.Request) {
	user, email, uid, ok := s.personalCaller(w, r)
	if !ok {
		return
	}
	state, err := s.store.GetPersonalSession(r.Context(), user.ID, r.PathValue("session_id"), email, uid)
	if err != nil {
		personalError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleGetPersonalSessionOverview(w http.ResponseWriter, r *http.Request) {
	user, email, uid, ok := s.personalCaller(w, r)
	if !ok {
		return
	}
	result, err := s.store.GetPersonalSessionOverview(r.Context(), user.ID, r.PathValue("session_id"), email, uid)
	if err != nil {
		personalError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handlePutPersonalSession(w http.ResponseWriter, r *http.Request) {
	user, email, uid, ok := s.personalCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		Classification   *string `json:"classification"`
		Note             *string `json:"note"`
		ExpectedRevision *int64  `json:"expected_revision"`
	}
	if !personalBody(r, &req) || req.ExpectedRevision == nil || (req.Classification == nil && req.Note == nil) || (req.Note != nil && (!utf8.ValidString(*req.Note) || len(*req.Note) > 16384)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	state, err := s.store.PutPersonalSession(r.Context(), user.ID, r.PathValue("session_id"), email, uid, req.Classification, req.Note, *req.ExpectedRevision)
	if err != nil {
		personalError(w, err, state)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleDeletePersonalSession(w http.ResponseWriter, r *http.Request) {
	user, email, uid, ok := s.personalCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !personalBody(r, &req) || req.ExpectedRevision == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected_revision required"})
		return
	}
	state, err := s.store.DeletePersonalSession(r.Context(), user.ID, r.PathValue("session_id"), email, uid, *req.ExpectedRevision)
	if err != nil {
		personalError(w, err, state)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *Server) handleGetPersonalGlobalNote(w http.ResponseWriter, r *http.Request) {
	user, _, _, ok := s.personalCaller(w, r)
	if !ok {
		return
	}
	result, err := s.store.GetPersonalGlobalNote(r.Context(), user.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handlePutPersonalGlobalNote(w http.ResponseWriter, r *http.Request) {
	user, _, _, ok := s.personalCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		Text             *string `json:"text"`
		ExpectedRevision *int64  `json:"expected_revision"`
	}
	if !personalBody(r, &req) || req.Text == nil || req.ExpectedRevision == nil || !utf8.ValidString(*req.Text) || len(*req.Text) > 16384 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	result, err := s.store.PutPersonalGlobalNote(r.Context(), user.ID, *req.Text, *req.ExpectedRevision, false)
	if err != nil {
		personalError(w, err, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleDeletePersonalGlobalNote(w http.ResponseWriter, r *http.Request) {
	user, _, _, ok := s.personalCaller(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !personalBody(r, &req) || req.ExpectedRevision == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected_revision required"})
		return
	}
	result, err := s.store.PutPersonalGlobalNote(r.Context(), user.ID, "", *req.ExpectedRevision, true)
	if err != nil {
		personalError(w, err, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handlePersonalSessionNotes(w http.ResponseWriter, r *http.Request) {
	user, email, uid, ok := s.personalCaller(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid limit"})
			return
		}
	}
	result, err := s.store.PersonalSessionNotes(r.Context(), user.ID, email, uid, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		personalError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handlePersonalSessionCounts(w http.ResponseWriter, r *http.Request) {
	user, email, uid, ok := s.personalCaller(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	for name := range query {
		switch name {
		case "since", "until", "project_hashes", "agent", "source", "classification", "assembled":
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported filter"})
			return
		}
		if len(query[name]) != 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ambiguous filter"})
			return
		}
	}
	since, until, err := optionalQueryTimeRange(r)
	if err != nil || (since != nil && until != nil && !since.Before(*until)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid time range"})
		return
	}
	filter := store.PersonalCountFilter{Since: since, Until: until,
		ProjectHashes: queryProjectHashes(r, "project_hashes"), Agent: query.Get("agent"),
		Source: query.Get("source"), Classification: query.Get("classification"),
		FoldLineage: query.Get("assembled") == "1"}
	if (query.Has("project_hashes") && len(filter.ProjectHashes) == 0) ||
		(query.Has("assembled") && query.Get("assembled") != "0" && query.Get("assembled") != "1") ||
		(filter.Agent != "" && filter.Agent != "claude" && filter.Agent != "codex" && filter.Agent != "other") ||
		(filter.Source != "" && filter.Source != "interactive" && filter.Source != "headless") ||
		(filter.Classification != "" && filter.Classification != "unsorted" && filter.Classification != "consulting" && filter.Classification != "working" && filter.Classification != "later" && filter.Classification != "done") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filter"})
		return
	}
	result, err := s.store.PersonalSessionCounts(r.Context(), user.ID, email, uid, filter)
	if err != nil {
		personalError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
