package api

import (
	"net/http"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// noAccessSentinel is a value guaranteed to match no real data.
// Used when CctraceUserID is not set — ensures empty results instead of full access.
const noAccessSentinel = "__no_access__"

// projectRuleScope returns the visibility scope for the current request.
// restricted is true for role=user, whose rule access is limited to
// repositories they have session history for; admins are unrestricted.
func (s *Server) projectRuleScope(r *http.Request) (userID, profileEmail string, restricted bool) {
	if user, ok := auth.UserFromContext(r.Context()); ok && user.Role == "user" {
		restricted = true
		if s.useUserIDAccessControl {
			if user.CctraceUserID != "" {
				userID = user.CctraceUserID
			} else {
				userID = noAccessSentinel
			}
		} else {
			profileEmail = user.Email
		}
	}
	return
}

// enforceProjectRuleAccess gates detail/comment/change-reason handlers so a
// restricted user cannot reach a rule's body by enumerating ids. Returns false
// (and writes the response) when access is denied or lookup fails.
func (s *Server) enforceProjectRuleAccess(w http.ResponseWriter, r *http.Request, ruleID int64) bool {
	userID, profileEmail, restricted := s.projectRuleScope(r)
	if !restricted {
		return true
	}
	visible, err := s.store.ProjectRuleVisibleTo(r.Context(), ruleID, userID, profileEmail)
	if err != nil {
		writeErr(w, err)
		return false
	}
	if !visible {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return false
	}
	return true
}

// resolveUserAccessParams returns (profileEmail, loginEmail, userID) for role=user access control.
// When useUserIDAccessControl is enabled and CctraceUserID is set, filters by userID only.
// When CctraceUserID is empty, returns sentinel values — user sees no data until admin sets it.
// When flag is disabled, falls back to legacy profile_email behavior.
// Callers relying on this: CostByTeam takes no loginEmail argument, so group_by=team
// scoping is correct only because every branch below returns "" for it. If that ever
// changes, CostByTeam must gain the parameter first -- otherwise a non-admin reads
// another team's cost.
func (s *Server) resolveUserAccessParams(user *auth.DashboardUser) (profileEmail, loginEmail, userID string) {
	if !s.useUserIDAccessControl {
		return user.Email, "", ""
	}
	if user.CctraceUserID != "" {
		return "", "", user.CctraceUserID
	}
	// CctraceUserID empty → sentinel ensures SQL filter matches nothing → empty result
	return "", "", noAccessSentinel
}

// sessionOwnedBy reports whether the session belongs to user, matched the same
// way resolveUserAccessParams scopes their reads. A session with no recorded
// owner belongs to no one.
func (s *Server) sessionOwnedBy(r *http.Request, user *auth.DashboardUser, sessionID string) (bool, error) {
	profileEmail, userID, err := s.store.SessionOwner(r.Context(), sessionID)
	if err != nil {
		return false, err
	}
	if profileEmail == "" && userID == "" {
		return false, nil
	}
	callerProfile, _, callerUserID := s.resolveUserAccessParams(user)
	if callerUserID != "" && callerUserID != noAccessSentinel && callerUserID == userID {
		return true, nil
	}
	if callerProfile != "" && callerProfile == profileEmail {
		return true, nil
	}
	return false, nil
}

// applyUserSessionFilter applies role=user access control to a SessionRecordFilter.
// When useUserIDAccessControl is enabled, filters by CctraceUserID.
// If CctraceUserID is empty, no filter is applied — user sees no data until admin sets it.
// When flag is disabled, uses legacy profile_email behavior.
func (s *Server) applyUserSessionFilter(f *store.SessionRecordFilter, user *auth.DashboardUser, _ string) {
	if !s.useUserIDAccessControl {
		f.ProfileEmail = user.Email
		return
	}
	if user.CctraceUserID != "" {
		f.UserID = user.CctraceUserID
	} else {
		f.UserID = noAccessSentinel
	}
}
