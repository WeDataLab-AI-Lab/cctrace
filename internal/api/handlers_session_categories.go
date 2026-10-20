package api

import (
	"net/http"

	"cctrace/internal/auth"
	"cctrace/internal/sessionrecord"
	"cctrace/internal/store"
)

func sessionOverviewAnalysisParams(s *Server, w http.ResponseWriter, r *http.Request, user *auth.DashboardUser) (store.SessionOverviewFilter, bool) {
	q := r.URL.Query()
	if !s.sessionAnalysisEnabled || q.Get("include") != "analysis" {
		return store.SessionOverviewFilter{}, true
	}
	f := store.SessionOverviewFilter{IncludeAnalysis: true, AnalysisSort: q.Get("sort"),
		AnalysisCategory: q.Get("category"), AnalysisPlan: q.Get("plan"), AnalysisQuery: q.Get("q"),
		AnalysisBucket: q.Get("bucket"), AnalysisKind: q.Get("kind"), AnalysisComponent: q.Get("component"), AnalysisScreen: q.Get("screen")}
	if user != nil {
		f.AnalysisOwner = user.Role == "user"
	}
	p5Filter := f.AnalysisBucket != "" || f.AnalysisKind != "" || f.AnalysisComponent != "" || f.AnalysisScreen != ""
	if p5Filter && !s.sessionRecordsEnabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session_records_disabled"})
		return store.SessionOverviewFilter{}, false
	}
	if p5Filter && !f.AnalysisOwner {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner_scope_required"})
		return store.SessionOverviewFilter{}, false
	}
	if f.AnalysisBucket != "" && f.AnalysisBucket != "done" && f.AnalysisBucket != "leftover" && f.AnalysisBucket != "ended_midway" && f.AnalysisBucket != "unknown" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid bucket"})
		return store.SessionOverviewFilter{}, false
	}
	if f.AnalysisKind != "" && !validSessionRecordKind(f.AnalysisKind) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid kind"})
		return store.SessionOverviewFilter{}, false
	}
	if f.AnalysisComponent != "" {
		if normalized, ok := sessionrecord.NormalizeTag(f.AnalysisComponent, true); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid component"})
			return store.SessionOverviewFilter{}, false
		} else {
			f.AnalysisComponent = normalized
		}
	}
	if f.AnalysisScreen != "" {
		if normalized, ok := sessionrecord.NormalizeTag(f.AnalysisScreen, true); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid screen"})
			return store.SessionOverviewFilter{}, false
		} else {
			f.AnalysisScreen = normalized
		}
	}
	f.IncludeSessionRecords = s.sessionRecordsEnabled && f.AnalysisOwner
	if f.AnalysisCategory != "" {
		if !validSessionAnalysisCategory(f.AnalysisCategory) || !f.AnalysisOwner {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid category"})
			return store.SessionOverviewFilter{}, false
		}
	}
	if f.AnalysisSort != "" && f.AnalysisSort != "start" && f.AnalysisSort != "activity" && f.AnalysisSort != "cost" && f.AnalysisSort != "requests" ||
		f.AnalysisPlan != "" && f.AnalysisPlan != "file" && f.AnalysisPlan != "mode" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid analysis filter"})
		return store.SessionOverviewFilter{}, false
	}
	return f, true
}

func validSessionRecordKind(value string) bool {
	switch value {
	case "implementation", "research", "maintenance", "consultation", "bugfix", "docs":
		return true
	}
	return false
}

func validSessionAnalysisCategory(value string) bool {
	switch value {
	case "waiting", "running", "ended", "stale_live", "open_question", "idle", "collector_delayed", "recent", "inactive":
		return true
	}
	return false
}

func (s *Server) handleSessionOverviewCategories(w http.ResponseWriter, r *http.Request) {
	if !s.sessionAnalysisEnabled {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session_analysis_disabled"})
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil || user.Role != "user" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "owner required"})
		return
	}
	profile, _, uid := s.resolveUserAccessParams(user)
	if user.ID == 0 || s.useUserIDAccessControl && (uid == noAccessSentinel || uid == "") || !s.useUserIDAccessControl && profile == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	since, until, err := optionalQueryTimeRange(r)
	if err != nil || since != nil && until != nil && !since.Before(*until) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid time range"})
		return
	}
	q := r.URL.Query()
	source := q.Get("source")
	if source == "" {
		source = "interactive"
	}
	if source != "interactive" && source != "headless" && source != "all" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid source"})
		return
	}
	classification := q.Get("classification")
	if classification != "" && !validOverviewClassification(classification) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid classification"})
		return
	}
	filter := store.SessionOverviewFilter{IncludeAnalysis: true, AnalysisOwner: true,
		PersonalUserID: uid, PersonalEmail: profile, DashboardUserID: user.ID,
		Since: since, Until: until, Source: source, FoldLineage: q.Get("assembled") == "1",
		ProjectHashes: queryProjectHashes(r, "project_hashes"), Agent: q.Get("agent"), Classification: classification}
	if uid != "" {
		filter.UserID = uid
	} else {
		filter.ProfileEmail = profile
	}
	counts, err := s.store.SessionOverviewCategoryCounts(r.Context(), filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, counts)
}
