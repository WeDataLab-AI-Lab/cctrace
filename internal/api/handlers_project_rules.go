package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"cctrace/internal/store"
)

func (s *Server) handleProjectRulesIngest(w http.ResponseWriter, r *http.Request) {
	var req store.ProjectRuleIngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req.ProfileEmail = s.aliases.Resolve(req.ProfileEmail)
	if req.Agent == "" {
		req.Agent = "claude"
	}
	resp, err := s.store.IngestProjectRules(r.Context(), &req)
	if err != nil {
		if errors.Is(err, store.ErrProjectRuleAccessDenied) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "repository ownership required"})
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListProjectRules(w http.ResponseWriter, r *http.Request) {
	f := store.ProjectRuleFilter{
		Agent:         r.URL.Query().Get("agent"),
		RepositoryKey: r.URL.Query().Get("repository_key"),
		RepositoryID:  r.URL.Query().Get("repository_id"),
		ProjectHash:   queryProjectHash(r, "project_hash"),
		Status:        r.URL.Query().Get("status"),
		Query:         r.URL.Query().Get("query"),
		Limit:         queryInt(r, "limit", 100),
		Offset:        queryInt(r, "offset", 0),
	}
	if userID, profileEmail, restricted := s.projectRuleScope(r); restricted {
		f.UserID = userID
		f.ProfileEmail = profileEmail
	}
	resp, err := s.store.ListProjectRules(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	if resp == nil {
		resp = &store.ProjectRuleListResponse{Items: []*store.ProjectRuleListItem{}}
	}
	if resp.Items == nil {
		resp.Items = []*store.ProjectRuleListItem{}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetProjectRuleDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	if !s.enforceProjectRuleAccess(w, r, id) {
		return
	}
	contentVersionID := int64(queryInt(r, "selected_version_id", 0))
	detail, err := s.store.GetProjectRuleDetail(r.Context(), id, contentVersionID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleCreateProjectRuleComment(w http.ResponseWriter, r *http.Request) {
	ruleID, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	if !s.enforceProjectRuleAccess(w, r, ruleID) {
		return
	}
	var req store.CreateProjectRuleCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	authorEmail, authorUserID := dashboardAuthor(r)
	comment, err := s.store.CreateProjectRuleComment(r.Context(), ruleID, &req, authorEmail, authorUserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, comment)
}

func (s *Server) handleUpdateProjectRuleChangeReason(w http.ResponseWriter, r *http.Request) {
	ruleID, ok := pathInt64(w, r, "id")
	if !ok {
		return
	}
	versionID, ok := pathInt64(w, r, "version_id")
	if !ok {
		return
	}
	if !s.enforceProjectRuleAccess(w, r, ruleID) {
		return
	}
	var req store.UpdateProjectRuleChangeReasonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	authorEmail, authorUserID := dashboardAuthor(r)
	version, err := s.store.UpdateProjectRuleChangeReason(r.Context(), ruleID, versionID, req.ChangeReason, authorEmail, authorUserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, version)
}
