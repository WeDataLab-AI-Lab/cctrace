package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// deleteReq builds a delete call already carrying the CSRF header and an
// authenticated user, so each test varies only the thing it is about.
func deleteReq(sessionID string, user *auth.DashboardUser, body string) *http.Request {
	r := httptest.NewRequest(http.MethodDelete, "/api/sessions/"+sessionID, strings.NewReader(body))
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r.SetPathValue("session_id", sessionID)
	return r.WithContext(auth.WithUser(r.Context(), user))
}

func admin() *auth.DashboardUser {
	return &auth.DashboardUser{Role: "admin", Email: "admin@example.com"}
}

// owner carries a CctraceUserID because that is what ownership is matched on in
// the default configuration: useUserIDAccessControl defaults to true, so
// resolveUserAccessParams answers with the user id and leaves the email blank. A
// dashboard user without one resolves to the no-access sentinel and can own
// nothing -- which is the intended behaviour, not a gap.
func owner() *auth.DashboardUser {
	return &auth.DashboardUser{Role: "user", Email: "owner@example.com", CctraceUserID: "u-owner"}
}

// ownedByCaller wires the mock so the session belongs to owner().
func ownedByCaller(m *mockStore) {
	m.sessionOwnerFn = func(string) (string, string, error) {
		return "owner@example.com", "u-owner", nil
	}
}

func TestHandleDeleteSession_authorizationMatrix(t *testing.T) {
	cases := []struct {
		name         string
		user         *auth.DashboardUser
		allowOwner   bool
		ownerMatches bool
		blockProject bool
		wantStatus   int
		wantDeleted  bool
	}{
		{"admin deletes anything", admin(), true, false, false, http.StatusOK, true},
		{"admin unaffected by the owner policy", admin(), false, false, false, http.StatusOK, true},
		{"admin may block the project", admin(), true, false, true, http.StatusOK, true},
		{"owner deletes own session", owner(), true, true, false, http.StatusOK, true},
		{"owner refused when policy is off", owner(), false, true, false, http.StatusForbidden, false},
		{"owner refused on someone else's session", owner(), true, false, false, http.StatusForbidden, false},
		{"owner cannot block a project", owner(), true, true, true, http.StatusForbidden, false},
		{"owner may not block when the policy is off", owner(), false, true, true, http.StatusForbidden, false},
		{"owner may not block someone else's project", owner(), true, false, true, http.StatusForbidden, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deleted := false
			m := &mockStore{
				deletionPolicyFn: func() (*store.DeletionPolicy, error) {
					return &store.DeletionPolicy{AllowOwnerDelete: tc.allowOwner}, nil
				},
				deleteSessionFn: func(sessionID, actor, reason string, blockProject, purgeProject bool) (*store.DeleteSessionResult, error) {
					deleted = true
					return &store.DeleteSessionResult{MarkedSessions: 1, ProjectBlocked: blockProject}, nil
				},
			}
			if tc.ownerMatches {
				ownedByCaller(m)
			} else {
				m.sessionOwnerFn = func(string) (string, string, error) {
					return "someone-else@example.com", "u-other", nil
				}
			}

			srv := newTestServer(m, nil)
			body := `{"block_project":false}`
			if tc.blockProject {
				body = `{"block_project":true}`
			}
			rec := httptest.NewRecorder()
			srv.handleDeleteSession(rec, deleteReq("sess-1", tc.user, body))

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if deleted != tc.wantDeleted {
				t.Errorf("store delete called = %t, want %t", deleted, tc.wantDeleted)
			}
		})
	}
}

func TestHandleDeleteSession_rejectsEmptySessionID(t *testing.T) {
	called := false
	m := &mockStore{
		deleteSessionFn: func(string, string, string, bool, bool) (*store.DeleteSessionResult, error) {
			called = true
			return &store.DeleteSessionResult{}, nil
		},
	}
	srv := newTestServer(m, nil)

	r := httptest.NewRequest(http.MethodDelete, "/api/sessions/", strings.NewReader("{}"))
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r.SetPathValue("session_id", "")
	r = r.WithContext(auth.WithUser(r.Context(), admin()))
	rec := httptest.NewRecorder()
	srv.handleDeleteSession(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if called {
		t.Error("the store was asked to delete the empty session id; that value is not " +
			"an empty filter — on prod it matched 3.06M otel_metrics rows")
	}
}

func TestHandleDeleteSession_userWithoutCctraceIDOwnsNothing(t *testing.T) {
	// A dashboard account never linked to a cctrace user id resolves to the
	// no-access sentinel. It must not be able to match a session whose owner fields
	// happen to be empty, or an unlinked account would inherit orphaned sessions.
	m := &mockStore{
		sessionOwnerFn: func(string) (string, string, error) { return "", noAccessSentinel, nil },
		deletionPolicyFn: func() (*store.DeletionPolicy, error) {
			return &store.DeletionPolicy{AllowOwnerDelete: true}, nil
		},
	}
	srv := newTestServer(m, nil)
	unlinked := &auth.DashboardUser{Role: "user", Email: "unlinked@example.com"}
	rec := httptest.NewRecorder()
	srv.handleDeleteSession(rec, deleteReq("sess-1", unlinked, "{}"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for an account with no cctrace user id", rec.Code)
	}
}

func TestHandleDeleteSession_unknownSessionIsNotOwned(t *testing.T) {
	// SessionOwner answers ("", "", nil) for a session it cannot find. That must read
	// as "not yours", not as a match against a caller whose fields are also empty.
	m := &mockStore{
		sessionOwnerFn: func(string) (string, string, error) { return "", "", nil },
		deletionPolicyFn: func() (*store.DeletionPolicy, error) {
			return &store.DeletionPolicy{AllowOwnerDelete: true}, nil
		},
	}
	srv := newTestServer(m, nil)
	rec := httptest.NewRecorder()
	srv.handleDeleteSession(rec, deleteReq("ghost", owner(), "{}"))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a session with no owner on record", rec.Code)
	}
}

func TestHandleDeleteSession_missingBodyIsAPlainDelete(t *testing.T) {
	var gotBlock bool
	m := &mockStore{
		deleteSessionFn: func(_, _, _ string, blockProject, _ bool) (*store.DeleteSessionResult, error) {
			gotBlock = blockProject
			return &store.DeleteSessionResult{}, nil
		},
	}
	srv := newTestServer(m, nil)
	r := httptest.NewRequest(http.MethodDelete, "/api/sessions/sess-1", nil)
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r.SetPathValue("session_id", "sess-1")
	r = r.WithContext(auth.WithUser(r.Context(), admin()))
	rec := httptest.NewRecorder()
	srv.handleDeleteSession(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; an absent body is a plain delete, not a bad request", rec.Code)
	}
	if gotBlock {
		t.Error("block_project defaulted to true with no body")
	}
}

func TestHandleDeleteSession_rejectsMalformedBody(t *testing.T) {
	called := false
	m := &mockStore{
		deleteSessionFn: func(string, string, string, bool, bool) (*store.DeleteSessionResult, error) {
			called = true
			return &store.DeleteSessionResult{}, nil
		},
	}
	srv := newTestServer(m, nil)
	rec := httptest.NewRecorder()
	srv.handleDeleteSession(rec, deleteReq("sess-1", admin(), `{"block_project":`))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for malformed JSON", rec.Code)
	}
	if called {
		t.Error("malformed options were silently reinterpreted as a plain delete")
	}
}

func TestHandleSetDeletionPolicy_requiresExplicitValue(t *testing.T) {
	called := false
	m := &mockStore{
		setDeletionPolicyFn: func(bool, string) error { called = true; return nil },
	}
	srv := newTestServer(m, nil)

	// A missing field would decode to false and quietly switch the policy off, so it
	// is a 400 rather than a default.
	r := httptest.NewRequest(http.MethodPut, "/api/admin/deletion-policy", strings.NewReader(`{}`))
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r = r.WithContext(auth.WithUser(r.Context(), admin()))
	rec := httptest.NewRecorder()
	srv.handleSetDeletionPolicy(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a body with no allow_owner_delete", rec.Code)
	}
	if called {
		t.Error("policy written from an absent field")
	}
}

func TestHandleSetDeletionPolicy_adminOnly(t *testing.T) {
	m := &mockStore{}
	srv := newTestServer(m, nil)
	r := httptest.NewRequest(http.MethodPut, "/api/admin/deletion-policy",
		strings.NewReader(`{"allow_owner_delete":true}`))
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r = r.WithContext(auth.WithUser(r.Context(), owner()))
	rec := httptest.NewRecorder()
	srv.handleSetDeletionPolicy(rec, r)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; the policy governs every user of the dashboard", rec.Code)
	}
}

func TestHandleGetDeletionPolicy_readableByNonAdmin(t *testing.T) {
	// The dashboard needs this to decide whether to draw a delete button. Hiding it
	// from non-admins would only mean drawing a button that 403s.
	m := &mockStore{
		deletionPolicyFn: func() (*store.DeletionPolicy, error) {
			return &store.DeletionPolicy{AllowOwnerDelete: true}, nil
		},
	}
	srv := newTestServer(m, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/deletion-policy", nil)
	r = r.WithContext(auth.WithUser(r.Context(), owner()))
	rec := httptest.NewRecorder()
	srv.handleGetDeletionPolicy(rec, r)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got store.DeletionPolicy
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.AllowOwnerDelete {
		t.Error("allow_owner_delete did not survive the round trip")
	}
}

// Only administrators may unblock projects.
func TestHandleUnblockProject_adminOnly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		user       *auth.DashboardUser
		allowOwner bool
		wantStatus int
	}{
		{"owner refused when the policy allows", owner(), true, http.StatusForbidden},
		{"owner refused when the policy is off", owner(), false, http.StatusForbidden},
		{"admin unaffected by the policy", admin(), false, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &mockStore{
				deletionPolicyFn: func() (*store.DeletionPolicy, error) {
					return &store.DeletionPolicy{AllowOwnerDelete: tc.allowOwner}, nil
				},
			}
			srv := newTestServer(m, nil)
			r := httptest.NewRequest(http.MethodDelete, "/api/blocked-projects",
				strings.NewReader(`{"project_hash":"ph-1"}`))
			r.Header.Set("X-Requested-With", "XMLHttpRequest")
			r = r.WithContext(auth.WithUser(r.Context(), tc.user))
			rec := httptest.NewRecorder()
			srv.handleUnblockProject(rec, r)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

var _ = context.Background

// Project deletion affects all owners and requires an administrator.
func TestHandleDeleteProject_adminOnly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		user       *auth.DashboardUser
		allowOwner bool
		wantStatus int
		wantCalled bool
	}{
		{"owner refused when the policy allows", owner(), true, http.StatusForbidden, false},
		{"owner refused when the policy is off", owner(), false, http.StatusForbidden, false},
		{"admin unaffected by the policy", admin(), false, http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			m := &mockStore{
				deletionPolicyFn: func() (*store.DeletionPolicy, error) {
					return &store.DeletionPolicy{AllowOwnerDelete: tc.allowOwner}, nil
				},
				deleteProjectFn: func(projectHashes []string, actor, reason string, blockProject bool) (*store.DeleteSessionResult, error) {
					called = true
					return &store.DeleteSessionResult{ProjectHash: projectHashes[0], ProjectBlocked: blockProject}, nil
				},
			}
			srv := newTestServer(m, nil)
			r := httptest.NewRequest(http.MethodDelete, "/api/projects",
				strings.NewReader(`{"project_hashes":["ph-1"],"block_project":true}`))
			r.Header.Set("X-Requested-With", "XMLHttpRequest")
			r = r.WithContext(auth.WithUser(r.Context(), tc.user))
			rec := httptest.NewRecorder()
			srv.handleDeleteProject(rec, r)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if called != tc.wantCalled {
				t.Errorf("store called = %t, want %t", called, tc.wantCalled)
			}
		})
	}
}

// An empty hash is not an empty filter. session_records holds rows under it, so a
// statement that took one would reach every project at once.
func TestHandleDeleteProject_rejectsEmptyHash(t *testing.T) {
	m := &mockStore{
		deletionPolicyFn: func() (*store.DeletionPolicy, error) {
			return &store.DeletionPolicy{AllowOwnerDelete: true}, nil
		},
		deleteProjectFn: func([]string, string, string, bool) (*store.DeleteSessionResult, error) {
			t.Fatal("store must not be reached with an empty project hash")
			return nil, nil
		},
	}
	srv := newTestServer(m, nil)
	r := httptest.NewRequest(http.MethodDelete, "/api/projects", strings.NewReader(`{"project_hashes":[]}`))
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r = r.WithContext(auth.WithUser(r.Context(), admin()))
	rec := httptest.NewRecorder()
	srv.handleDeleteProject(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleDeleteProject_rejectsMalformedBody(t *testing.T) {
	m := &mockStore{
		deleteProjectFn: func([]string, string, string, bool) (*store.DeleteSessionResult, error) {
			t.Fatal("store must not be reached with malformed JSON")
			return nil, nil
		},
	}
	srv := newTestServer(m, nil)
	r := httptest.NewRequest(http.MethodDelete, "/api/projects", strings.NewReader(`{"project_hashes":["ph-1"],"reason":`))
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	r = r.WithContext(auth.WithUser(r.Context(), admin()))
	rec := httptest.NewRecorder()
	srv.handleDeleteProject(rec, r)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for malformed JSON", rec.Code)
	}
}

func TestHandleDeleteSession_projectFlagsRequireAdmin(t *testing.T) {
	for _, body := range []string{`{"purge_project":true}`, `{"block_project":true}`, `{"purge_project":true,"block_project":true}`} {
		for _, user := range []*auth.DashboardUser{owner(), admin()} {
			t.Run(user.Role+body, func(t *testing.T) {
				called := false
				var options struct {
					Block bool `json:"block_project"`
					Purge bool `json:"purge_project"`
				}
				if err := json.Unmarshal([]byte(body), &options); err != nil {
					t.Fatal(err)
				}
				m := &mockStore{
					deletionPolicyFn: func() (*store.DeletionPolicy, error) {
						return &store.DeletionPolicy{AllowOwnerDelete: true}, nil
					},
					deleteSessionFn: func(_, _, _ string, block, purge bool) (*store.DeleteSessionResult, error) {
						called = true
						if block != options.Block || purge != options.Purge {
							t.Errorf("store flags = (%t, %t), want (%t, %t)", block, purge, options.Block, options.Purge)
						}
						return &store.DeleteSessionResult{}, nil
					},
				}
				ownedByCaller(m)
				rec := httptest.NewRecorder()
				newTestServer(m, nil).handleDeleteSession(rec, deleteReq("sess-1", user, body))
				want := http.StatusForbidden
				if user.Role == "admin" {
					want = http.StatusOK
				}
				if rec.Code != want {
					t.Errorf("status = %d, want %d", rec.Code, want)
				}
				if called != (user.Role == "admin") {
					t.Errorf("store delete called = %t", called)
				}
			})
		}
	}
}

func TestProjectReadEndpoints_adminOnly(t *testing.T) {
	for _, user := range []*auth.DashboardUser{owner(), admin()} {
		srv := newTestServer(&mockStore{}, nil)
		for name, handler := range map[string]http.HandlerFunc{"count": srv.handleProjectSessionCount, "blocked": srv.handleListBlockedProjects} {
			t.Run(user.Role+name, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodGet, "/api/projects/session-count?project_hash=ph-1", nil)
				r = r.WithContext(auth.WithUser(r.Context(), user))
				rec := httptest.NewRecorder()
				handler(rec, r)
				want := http.StatusForbidden
				if user.Role == "admin" {
					want = http.StatusOK
				}
				if rec.Code != want {
					t.Errorf("status = %d, want %d", rec.Code, want)
				}
			})
		}
	}
}
