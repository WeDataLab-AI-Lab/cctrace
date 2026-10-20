package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type traceIndexMock struct {
	*mockStore
	access store.SessionAnalysisAccess
}

func (m *traceIndexMock) SessionTraceIndex(_ context.Context, id string, a store.SessionAnalysisAccess) (store.SessionTraceIndex, error) {
	m.access = a
	return store.SessionTraceIndex{SessionID: id, State: "ready"}, nil
}

// A role=user caller must be scoped the same way as the analysis read. Legacy
// email mode is the branch that regressed when only CctraceUserID was copied.
func TestSessionTraceIndexScopesUserInBothAccessModes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		byUserID   bool
		wantEmail  string
		wantUserID string
	}{
		{name: "email", byUserID: false, wantEmail: "owner@example.com"},
		{name: "user_id", byUserID: true, wantUserID: "uid-owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &traceIndexMock{mockStore: &mockStore{}}
			s := newTestServer(m, nil)
			s.WithSessionAnalysisEnabled(true)
			s.useUserIDAccessControl = tc.byUserID
			user := &auth.DashboardUser{ID: 7, Role: "user", Email: "owner@example.com", CctraceUserID: "uid-owner"}
			r := httptest.NewRequest(http.MethodGet, "/api/sessions/session-1/trace/index?profile_email=other@example.com&user_id=other", nil)
			r = r.WithContext(auth.WithUser(r.Context(), user))
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if !m.access.User || m.access.DashboardUserID != 7 {
				t.Fatalf("access=%+v", m.access)
			}
			if m.access.ProfileEmail != tc.wantEmail || m.access.UserID != tc.wantUserID {
				t.Fatalf("access=%+v, want email=%q user=%q", m.access, tc.wantEmail, tc.wantUserID)
			}
		})
	}
}
