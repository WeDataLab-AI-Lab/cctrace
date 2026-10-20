package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestConversationAdminScopeAndUserForgery(t *testing.T) {
	t.Parallel()
	for _, uidMode := range []bool{false, true} {
		for _, lineage := range []string{"", "&lineage=1"} {
			var got store.SessionRecordFilter
			m := &mockStore{listSessionRecordsFn: func(_ context.Context, f store.SessionRecordFilter) ([]*store.SessionRecord, error) {
				got = f
				return []*store.SessionRecord{}, nil
			}}
			s := newTestServer(m, nil)
			s.useUserIDAccessControl = uidMode
			request := func(user *auth.DashboardUser) {
				t.Helper()
				r := httptest.NewRequest("GET", "/api/sessions?session_id=session&profile_email=chosen@example.test&user_id=chosen-uid&login_email=chosen-login@example.test"+lineage, nil)
				r = r.WithContext(auth.WithUser(r.Context(), user))
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("status=%d", w.Code)
				}
			}
			request(&auth.DashboardUser{ID: 1, Role: "admin", Email: "admin@example.test"})
			if got.UserID != "chosen-uid" || got.ProfileEmail != "chosen@example.test" || got.LoginEmail != "chosen-login@example.test" {
				t.Fatalf("admin scope=%+v", got)
			}
			request(&auth.DashboardUser{ID: 2, Role: "user", Email: "owner@example.test", CctraceUserID: "owner-uid"})
			if got.LoginEmail != "" {
				t.Fatalf("forged login survived=%+v", got)
			}
			if uidMode {
				if got.UserID != "owner-uid" || got.ProfileEmail != "" {
					t.Fatalf("UID scope=%+v", got)
				}
			} else {
				if got.ProfileEmail != "owner@example.test" || got.UserID != "" {
					t.Fatalf("legacy scope=%+v", got)
				}
			}
		}
	}
}
