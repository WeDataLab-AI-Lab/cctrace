package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/sessionview"
	"cctrace/internal/store"
)

const sessionOverviewReadTimeout = 5 * time.Second

func addSessionOverviewTiming(w http.ResponseWriter, stage string, started time.Time) {
	w.Header().Add("Server-Timing", fmt.Sprintf("%s;dur=%.3f", stage, float64(time.Since(started))/float64(time.Millisecond)))
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	anchorTime, parseErr := optionalQueryTime(r, "anchor_ts")
	if parseErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid anchor_ts"})
		return
	}
	f := store.SessionRecordFilter{
		ProfileEmail: r.URL.Query().Get("profile_email"),
		SessionID:    r.URL.Query().Get("session_id"),
		Limit:        queryInt(r, "limit", 100),
		Offset:       queryInt(r, "offset", 0),
		Order:        r.URL.Query().Get("order"),
		Conversation: r.URL.Query().Get("view") == "conversation",
		Anchor:       r.URL.Query().Get("anchor"),
		AnchorTime:   anchorTime,
		TurnKey:      r.URL.Query().Get("turn_key"),
		Direction:    r.URL.Query().Get("direction"),
		SourceFile:   r.URL.Query().Get("source_file"),
		Start:        r.URL.Query().Get("start"),
	}
	startOffset := f.Offset
	originFound := false
	if f.Direction != "" {
		f.OriginFound = &originFound
	}
	if f.Direction != "" && (f.Direction != "previous-request" && f.Direction != "next-request" || (f.Anchor == "" && f.TurnKey == "" && f.AnchorTime == nil)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid direction"})
		return
	}
	if f.Start != "" && f.Start != "latest-request" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid start"})
		return
	}
	if f.Anchor != "" || f.TurnKey != "" || f.AnchorTime != nil || f.Start != "" {
		f.StartOffset = &startOffset
		f.Order = "asc"
	}
	if user, ok := auth.UserFromContext(r.Context()); ok && user != nil {
		switch user.Role {
		case "user":
			f.ProfileEmail = ""
			f.UserID = ""
			f.LoginEmail = ""
			s.applyUserSessionFilter(&f, user, "handleListSessions")
		case "admin":
			f.UserID = r.URL.Query().Get("user_id")
			f.LoginEmail = r.URL.Query().Get("login_email")
		}
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
	if f.StartOffset != nil {
		if startOffset < 0 {
			code := "record_not_found"
			if f.Direction != "" && originFound {
				code = "no_adjacent_request"
			}
			writeJSON(w, http.StatusNotFound, map[string]string{"error": code})
			return
		}
		w.Header().Set("X-Session-Start-Offset", fmt.Sprint(startOffset))
	}
	// Transcript bodies must never land in a shared or browser cache: they carry one
	// user's prompts and tool output. `private` alone would still persist locally.
	w.Header().Set("Cache-Control", "no-store")
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
	writeLargeJSON(w, r, http.StatusOK, response)
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
	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), sessionOverviewReadTimeout)
	defer cancel()
	r = r.WithContext(ctx)

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
	classification := r.URL.Query().Get("classification")
	if classification != "" && !validOverviewClassification(classification) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid classification"})
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	personalID, personalEmail, personalUID := personalOverviewCaller(s, user)
	analysis, valid := sessionOverviewAnalysisParams(s, w, r, user)
	if !valid {
		return
	}
	result, err := s.store.ListSessionOverviews(r.Context(), store.SessionOverviewFilter{
		IncludeAnalysis: analysis.IncludeAnalysis, AnalysisOwner: analysis.AnalysisOwner,
		AnalysisCategory: analysis.AnalysisCategory, AnalysisPlan: analysis.AnalysisPlan,
		AnalysisQuery: analysis.AnalysisQuery, AnalysisSort: analysis.AnalysisSort,
		IncludeSessionRecords: analysis.IncludeSessionRecords, AnalysisBucket: analysis.AnalysisBucket,
		AnalysisKind: analysis.AnalysisKind, AnalysisComponent: analysis.AnalysisComponent, AnalysisScreen: analysis.AnalysisScreen,
		Classification:  classification,
		DashboardUserID: personalID,
		PersonalEmail:   personalEmail,
		PersonalUserID:  personalUID,
		ProfileEmail:    profileEmail,
		LoginEmail:      loginEmail,
		UserID:          userID,
		Since:           since,
		Until:           until,
		Limit:           queryInt(r, "limit", 200),
		FoldLineage:     r.URL.Query().Get("assembled") == "1",
		Source:          r.URL.Query().Get("source"),
		Offset:          queryInt(r, "offset", 0),
		ProjectHashes:   queryProjectHashes(r, "project_hashes"),
		Agent:           r.URL.Query().Get("agent"),
	})
	addSessionOverviewTiming(w, "overview", started)
	if err != nil {
		writeErr(w, err)
		return
	}
	if user, ok := auth.UserFromContext(r.Context()); !analysis.IncludeAnalysis && ok && user != nil && len(result) > 0 {
		email, _, uid := s.resolveUserAccessParams(user)
		ids := make([]string, 0, len(result))
		for _, row := range result {
			ids = append(ids, row.SessionID)
		}
		started = time.Now()
		values, err := s.store.PersonalSessionOverviewValues(r.Context(), user.ID, email, uid, ids)
		addSessionOverviewTiming(w, "personal", started)
		if err != nil {
			writeErr(w, err)
			return
		}
		for _, row := range result {
			if personal, ok := values[row.SessionID]; ok {
				classification := personal.Classification
				row.Classification = &classification
				row.FirstLine = personal.FirstLine
			}
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func validOverviewClassification(classification string) bool {
	switch classification {
	case "unsorted", "consulting", "working", "later", "done":
		return true
	}
	return false
}

func personalOverviewCaller(s *Server, user *auth.DashboardUser) (int64, string, string) {
	if user == nil {
		return 0, "", ""
	}
	email, _, uid := s.resolveUserAccessParams(user)
	return user.ID, email, uid
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
	ctx, cancel := context.WithTimeout(r.Context(), sessionOverviewReadTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	includeClassificationCounts := true
	if values, present := r.URL.Query()["include_classification_counts"]; present {
		if len(values) != 1 || (values[0] != "0" && values[0] != "1") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid include_classification_counts"})
			return
		}
		includeClassificationCounts = values[0] == "1"
	}

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
	classification := r.URL.Query().Get("classification")
	if classification != "" && !validOverviewClassification(classification) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid classification"})
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	personalID, personalEmail, personalUID := personalOverviewCaller(s, user)
	analysis, valid := sessionOverviewAnalysisParams(s, w, r, user)
	if !valid {
		return
	}
	filter := store.SessionOverviewFilter{
		IncludeAnalysis: analysis.IncludeAnalysis, AnalysisOwner: analysis.AnalysisOwner,
		AnalysisCategory: analysis.AnalysisCategory, AnalysisPlan: analysis.AnalysisPlan,
		AnalysisQuery: analysis.AnalysisQuery, AnalysisSort: analysis.AnalysisSort,
		IncludeSessionRecords: analysis.IncludeSessionRecords, AnalysisBucket: analysis.AnalysisBucket,
		AnalysisKind: analysis.AnalysisKind, AnalysisComponent: analysis.AnalysisComponent, AnalysisScreen: analysis.AnalysisScreen,
		Classification:  classification,
		DashboardUserID: personalID,
		PersonalEmail:   personalEmail,
		PersonalUserID:  personalUID,
		ProfileEmail:    profileEmail,
		LoginEmail:      loginEmail,
		UserID:          userID,
		Since:           since,
		Until:           until,
		FoldLineage:     r.URL.Query().Get("assembled") == "1",
		Source:          r.URL.Query().Get("source"),
		ProjectHashes:   queryProjectHashes(r, "project_hashes"),
		Agent:           r.URL.Query().Get("agent"),
	}
	var count, unattributed int
	var buckets map[string]int
	started := time.Now()
	if filter.IncludeAnalysis {
		count, buckets, unattributed, err = s.store.SessionOverviewAnalysisCountWithClassification(r.Context(), filter, loginEmail != "" && !restricted)
		addSessionOverviewTiming(w, "analysis_count", started)
	} else {
		count, err = s.store.CountSessionOverviews(r.Context(), filter)
		addSessionOverviewTiming(w, "count", started)
		if err == nil && includeClassificationCounts && personalID != 0 && (personalEmail != "" || personalUID != "" && personalUID != noAccessSentinel) {
			bucketFilter := filter
			bucketFilter.Classification = ""
			started = time.Now()
			buckets, err = s.store.SessionOverviewClassificationCounts(r.Context(), bucketFilter)
			addSessionOverviewTiming(w, "classification", started)
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	response := map[string]any{"count": count}
	if buckets != nil {
		response["classification_counts"] = buckets
	}
	if analysis.IncludeSessionRecords && analysis.AnalysisOwner {
		started = time.Now()
		judgementCounts, err := s.store.SessionOverviewJudgementBucketCounts(r.Context(), filter)
		addSessionOverviewTiming(w, "judgement", started)
		if err != nil {
			writeErr(w, err)
			return
		}
		response["judgement_bucket_counts"] = judgementCounts
	}

	// Sessions an account filter necessarily drops, so the UI can say "N sessions
	// with no known account" instead of just showing a smaller list. Only computed
	// when an account filter is actually narrowing the view.
	//
	// Skipped for restricted users: their scope is pinned by user_id (or profile_email
	// in legacy mode), not by an account filter, so removing the account filter to count
	// unattributed sessions would count other people's sessions too.
	if !filter.IncludeAnalysis && loginEmail != "" && !restricted {
		started = time.Now()
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
		addSessionOverviewTiming(w, "unattributed", started)
		if err != nil {
			writeErr(w, err)
			return
		}
	}

	response["unattributed"] = unattributed
	writeJSON(w, http.StatusOK, response)
}
