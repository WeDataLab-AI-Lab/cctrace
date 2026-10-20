package api

import (
	"net/http"
	"strconv"
	"time"

	"cctrace/internal/store"
)

func (s *Server) sessionGroupFilter(w http.ResponseWriter, r *http.Request, paging ...bool) (store.SessionOverviewFilter, bool) {
	user, email, uid, ok := s.personalCaller(w, r)
	if !ok {
		return store.SessionOverviewFilter{}, false
	}
	if uid == noAccessSentinel || (email == "" && uid == "") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return store.SessionOverviewFilter{}, false
	}
	q := r.URL.Query()
	for name, values := range q {
		switch name {
		case "since", "until", "project_hashes", "agent", "source", "classification", "assembled", "login_email",
			"include", "q", "plan", "category", "sort", "bucket", "kind", "component", "screen":
		case "group_key", "cursor", "limit":
			if len(paging) == 0 || !paging[0] {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported filter"})
				return store.SessionOverviewFilter{}, false
			}
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported filter"})
			return store.SessionOverviewFilter{}, false
		}
		if len(values) != 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ambiguous filter"})
			return store.SessionOverviewFilter{}, false
		}
	}
	since, until, err := optionalQueryTimeRange(r)
	if err != nil || since != nil && until != nil && !since.Before(*until) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid time range"})
		return store.SessionOverviewFilter{}, false
	}
	analysisFilter := q.Has("q") || q.Has("plan") || q.Has("category") || q.Has("sort") ||
		q.Has("bucket") || q.Has("kind") || q.Has("component") || q.Has("screen")
	if q.Has("include") && q.Get("include") != "analysis" || analysisFilter && q.Get("include") != "analysis" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid analysis scope"})
		return store.SessionOverviewFilter{}, false
	}
	if q.Get("include") == "analysis" && !s.sessionAnalysisEnabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session_analysis_disabled"})
		return store.SessionOverviewFilter{}, false
	}
	f := store.SessionOverviewFilter{DashboardUserID: user.ID, PersonalEmail: email, PersonalUserID: uid,
		Since: since, Until: until, ProjectHashes: queryProjectHashes(r, "project_hashes"), Agent: q.Get("agent"),
		Source: q.Get("source"), Classification: q.Get("classification"), FoldLineage: q.Get("assembled") == "1"}
	analysis, valid := sessionOverviewAnalysisParams(s, w, r, user)
	if !valid {
		return store.SessionOverviewFilter{}, false
	}
	f.IncludeAnalysis, f.AnalysisOwner = analysis.IncludeAnalysis, analysis.AnalysisOwner
	f.IncludeSessionRecords, f.AnalysisBucket = analysis.IncludeSessionRecords, analysis.AnalysisBucket
	f.AnalysisCategory, f.AnalysisPlan = analysis.AnalysisCategory, analysis.AnalysisPlan
	f.AnalysisQuery, f.AnalysisSort = analysis.AnalysisQuery, analysis.AnalysisSort
	f.AnalysisKind, f.AnalysisComponent, f.AnalysisScreen = analysis.AnalysisKind, analysis.AnalysisComponent, analysis.AnalysisScreen
	if user.Role != "user" {
		f.LoginEmail = q.Get("login_email")
	}
	if uid != "" {
		f.UserID = uid
	} else {
		f.ProfileEmail = email
	}
	if (q.Has("project_hashes") && len(f.ProjectHashes) == 0) ||
		(q.Has("assembled") && q.Get("assembled") != "0" && q.Get("assembled") != "1") ||
		(f.Agent != "" && f.Agent != "claude" && f.Agent != "codex" && f.Agent != "other") ||
		(f.Source != "" && f.Source != "interactive" && f.Source != "headless") ||
		(f.Classification != "" && !validOverviewClassification(f.Classification)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filter"})
		return store.SessionOverviewFilter{}, false
	}
	return f, true
}

func (s *Server) handleGetSessionGroups(w http.ResponseWriter, r *http.Request) {
	f, ok := s.sessionGroupFilter(w, r)
	if !ok {
		return
	}
	result, err := s.store.GetSessionGroups(r.Context(), f)
	if err != nil {
		personalError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleGetSessionGroupMembers(w http.ResponseWriter, r *http.Request) {
	f, ok := s.sessionGroupFilter(w, r, true)
	if !ok {
		return
	}
	limit := 100
	var err error
	if r.URL.Query().Has("limit") {
		limit, err = strconv.Atoi(r.URL.Query().Get("limit"))
	}
	key := r.URL.Query().Get("group_key")
	if err != nil || limit < 1 || limit > 100 || key == "" || len(key) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid group page"})
		return
	}
	result, err := s.store.GetSessionGroupMembers(r.Context(), f, key, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		personalError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpdateSessionGroupDismissals(w http.ResponseWriter, r *http.Request) {
	f, ok := s.sessionGroupFilter(w, r)
	if !ok {
		return
	}
	var req struct {
		GroupingVersion int                           `json:"grouping_version"`
		GroupKey        string                        `json:"group_key"`
		Items           []store.SessionGroupDismissal `json:"items"`
	}
	if !personalBody(r, &req) || req.GroupingVersion != 1 || len(req.Items) == 0 || len(req.Items) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	for _, item := range req.Items {
		if item.SessionID == "" || len(item.SessionID) > 256 || item.EndedAt.IsZero() || item.EndedAt.After(time.Now().Add(time.Minute)) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid group member"})
			return
		}
	}
	if err := s.store.UpdateSessionGroupDismissals(r.Context(), f, req.Items, r.Method == http.MethodPut, req.GroupKey); err != nil {
		personalError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"acknowledged": true, "count": len(req.Items), "grouping_version": req.GroupingVersion})
}
