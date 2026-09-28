package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"cctrace/internal/auth"
	"cctrace/internal/ingestblock"
	"cctrace/internal/store"
)

type deleteSessionRequest struct {
	BlockProject bool `json:"block_project"`
	// PurgeProject also removes the project's other stored sessions. Separate from
	// BlockProject because they are separate decisions: one erases history, the
	// other refuses the future, and a caller can want either alone.
	PurgeProject bool   `json:"purge_project"`
	Reason       string `json:"reason"`
}

// handleDeleteSession removes one session and, optionally, stops the project it
// belongs to from being collected again.
//
// Owners need the owner-delete policy and session ownership. Project-wide
// deletion and collection blocking require an administrator.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		// Refused here as well as in the store. An empty session id is not an empty
		// filter -- session_records holds rows under it, and on prod otel_metrics held
		// 3.06M rows under it, belonging to every user.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session id is required"})
		return
	}

	var req deleteSessionRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}

	if req.PurgeProject || req.BlockProject {
		if _, ok := adminFromRequest(w, r); !ok {
			return
		}
	}

	isAdmin := user.Role == "admin"
	if !isAdmin {
		allowed, err := s.ownerMayDelete(r, user, sessionID)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !allowed {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "not permitted to delete this session"})
			return
		}
	}

	result, err := s.store.DeleteSession(r.Context(), sessionID, user.Email, req.Reason, req.BlockProject, req.PurgeProject)
	if err != nil {
		if errors.Is(err, store.ErrEmptySessionID) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeErr(w, err)
		return
	}

	// Push into the cache immediately rather than waiting for the refresh tick: a
	// `sync --watch` running at a one-second interval will otherwise re-upload the
	// session inside that window, and the delete the user just confirmed appears to
	// have done nothing.
	if s.ingestBlock != nil {
		s.ingestBlock.AddSession(sessionID)
		if result.ProjectBlocked {
			s.ingestBlock.AddProject(result.ProjectHash)
		}
		// A purge without a block leaves the project collectable, so its purged
		// sessions are the only thing standing between the sweep and a re-sync. The
		// tombstones are already written; the cache has to learn them without waiting
		// for the tick, same as the clicked session.
		if result.PurgedSessions > 0 && !result.ProjectBlocked {
			if err := s.refreshIngestBlocklist(r.Context()); err != nil {
				log.Printf("[api] blocklist refresh after purge: %v", err)
			}
		}
	}

	// Reclaim in the background. The rows are already invisible, so this is not on
	// the critical path -- and it is the part that takes 15 to 30 seconds, which is
	// exactly why it is not in front of the person who clicked.
	//
	// context.WithoutCancel: the request's context is done the moment this handler
	// returns, and a sweep tied to it would be cancelled before it deleted anything.
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
		defer cancel()
		n, err := s.store.SweepDeletedSessions(ctx, int(result.MarkedSessions)+1)
		if err != nil {
			// Not escalated: the tombstones stand, so nothing came back into view. The
			// periodic sweep retries whatever is left.
			log.Printf("[api] sweep after delete (%d rows reclaimed): %v", n, err)
			return
		}
		log.Printf("[api] sweep after delete: %d rows reclaimed for %d sessions", n, result.MarkedSessions)
	}()

	log.Printf("[api] session deleted: %s by %s (sessions=%d purged=%d blocked=%t)",
		sessionID, user.Email, result.MarkedSessions, result.PurgedSessions, result.ProjectBlocked)
	writeJSON(w, http.StatusOK, result)
}

// ownerMayDelete reports whether a non-admin caller may delete this session.
//
// Both halves must hold: the policy allows owner deletes at all, and the session
// actually belongs to the caller. A session with no owner on record is not
// deletable by a non-admin -- unknown ownership is not the same as ownership.
func (s *Server) ownerMayDelete(r *http.Request, user *auth.DashboardUser, sessionID string) (bool, error) {
	policy, err := s.store.GetDeletionPolicy(r.Context())
	if err != nil {
		return false, err
	}
	if !policy.AllowOwnerDelete {
		return false, nil
	}
	return s.sessionOwnedBy(r, user, sessionID)
}

// handleDeleteProject removes a whole project for all owners and is admin-only.
func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}

	var req struct {
		// A list because the picker shows identities: one row can stand for several
		// project hashes, the same repository opened from different worktrees.
		ProjectHashes []string `json:"project_hashes"`
		BlockProject  bool     `json:"block_project"`
		Reason        string   `json:"reason"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}
	if len(req.ProjectHashes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project hash is required"})
		return
	}

	result, err := s.store.DeleteProject(r.Context(), req.ProjectHashes, user.Email, req.Reason, req.BlockProject)
	if err != nil {
		if errors.Is(err, store.ErrEmptyProjectHash) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeErr(w, err)
		return
	}

	// Same reason as the session route: a watching sync re-uploads inside the refresh
	// window, and the delete the user just confirmed looks like it did nothing.
	// A block covers every session in the project at once; without one the tombstones
	// are the only thing standing between the sweep and a re-sync, so the cache has
	// to be rebuilt from the table rather than fed id by id.
	if s.ingestBlock != nil {
		if result.ProjectBlocked {
			for _, h := range req.ProjectHashes {
				s.ingestBlock.AddProject(h)
			}
		} else if result.MarkedSessions > 0 {
			if err := s.refreshIngestBlocklist(r.Context()); err != nil {
				log.Printf("[api] blocklist refresh after project delete: %v", err)
			}
		}
	}

	if result.MarkedSessions > 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
			defer cancel()
			n, err := s.store.SweepDeletedSessions(ctx, int(result.MarkedSessions)+1)
			if err != nil {
				log.Printf("[api] sweep after project delete (%d rows reclaimed): %v", n, err)
				return
			}
			log.Printf("[api] sweep after project delete: %d rows reclaimed for %d sessions", n, result.MarkedSessions)
		}()
	}

	log.Printf("[api] project deleted: %v by %s (sessions=%d blocked=%t)",
		req.ProjectHashes, user.Email, result.MarkedSessions, result.ProjectBlocked)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleProjectSessionCount(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}
	n, err := s.store.ProjectSessionCount(r.Context(),
		r.URL.Query().Get("project_hash"), r.URL.Query().Get("exclude_session_id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

// refreshIngestBlocklist reloads the cache from storage immediately.
func (s *Server) refreshIngestBlocklist(ctx context.Context) error {
	return s.ingestBlock.Refresh(ctx, func(c context.Context) (*ingestblock.Sets, error) {
		bl, err := s.store.LoadIngestBlocklist(c)
		if err != nil {
			return nil, err
		}
		return &ingestblock.Sets{DeletedSessions: bl.DeletedSessions, BlockedProjects: bl.BlockedProjects}, nil
	})
}

// handleGetDeletionPolicy is readable by any signed-in user: the dashboard needs
// it to decide whether to show a delete button, and hiding the answer would only
// mean showing a button that 403s.
func (s *Server) handleGetDeletionPolicy(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	policy, err := s.store.GetDeletionPolicy(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (s *Server) handleSetDeletionPolicy(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	user, ok := adminFromRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		AllowOwnerDelete *bool `json:"allow_owner_delete"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AllowOwnerDelete == nil {
		// A pointer, not a bool: a missing field would otherwise decode to false and
		// silently turn the policy off.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "allow_owner_delete is required"})
		return
	}
	if err := s.store.SetDeletionPolicy(r.Context(), *body.AllowOwnerDelete, user.Email); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"allow_owner_delete": *body.AllowOwnerDelete})
}

// handleListBlockedProjects is admin-only. The list names projects by their
// directory name, which is information about other people's work.
func (s *Server) handleListBlockedProjects(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}
	list, err := s.store.ListBlockedProjects(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleUnblockProject(w http.ResponseWriter, r *http.Request) {
	if !csrfCheck(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF validation failed"})
		return
	}
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}
	projectHash := r.URL.Query().Get("project_hash")
	if projectHash == "" {
		var body struct {
			ProjectHash string `json:"project_hash"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			projectHash = body.ProjectHash
		}
	}
	if projectHash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_hash is required"})
		return
	}
	if err := s.store.UnblockProject(r.Context(), projectHash); err != nil {
		writeErr(w, err)
		return
	}
	// The cache is not edited here. It only ever grows between refreshes, so an
	// unblock waits for the next tick -- collecting a few seconds late is recoverable,
	// while dropping live data because a stale entry said "blocked" is not.
	writeJSON(w, http.StatusOK, map[string]bool{"unblocked": true})
}
