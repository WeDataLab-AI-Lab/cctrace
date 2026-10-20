package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type timelineMock struct {
	*mockStore
	filter store.SessionRecordFilter
}

func (m *timelineMock) ConversationTimeline(_ context.Context, f store.SessionRecordFilter) (store.ConversationTimeline, error) {
	m.filter = f
	return store.ConversationTimeline{}, nil
}

// The timeline reveals when another user worked, so a role=user caller's own
// scope must replace whatever the query string asks for.
func TestConversationTimelineScopesUserToOwnRecords(t *testing.T) {
	for _, tc := range []struct {
		name      string
		byUserID  bool
		wantEmail string
		wantUser  string
	}{
		{name: "email", wantEmail: "owner@example.com"},
		{name: "user_id", byUserID: true, wantUser: "uid-owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &timelineMock{mockStore: &mockStore{}}
			s := newTestServer(m, nil)
			s.useUserIDAccessControl = tc.byUserID
			user := &auth.DashboardUser{ID: 7, Role: "user", Email: "owner@example.com", CctraceUserID: "uid-owner"}
			r := httptest.NewRequest(http.MethodGet, "/api/sessions/session-1/conversation/timeline?profile_email=other@example.com&user_id=other&login_email=x@example.com", nil)
			r = r.WithContext(auth.WithUser(r.Context(), user))
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			f := m.filter
			if f.SessionID != "session-1" || f.ProfileEmail != tc.wantEmail || f.UserID != tc.wantUser || f.LoginEmail != "" {
				t.Fatalf("filter=%+v", f)
			}
		})
	}
}
