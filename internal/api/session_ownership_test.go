package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type segmentsCaptureStore struct {
	*mockStore
	calls int
}

func (s *segmentsCaptureStore) SessionAccountSegments(context.Context, string) ([]*store.SessionAccountSegment, error) {
	s.calls++
	return []*store.SessionAccountSegment{}, nil
}

// newSegmentsStore owns session "mine" by user_id "caller" / profile
// "me@example.com" and session "theirs" by someone else; any other id is absent.
func newSegmentsStore() *segmentsCaptureStore {
	m := &mockStore{sessionOwnerFn: func(id string) (string, string, error) {
		switch id {
		case "mine":
			return "me@example.com", "caller", nil
		case "theirs":
			return "other@example.com", "other", nil
		}
		return "", "", nil
	}}
	return &segmentsCaptureStore{mockStore: m}
}

// A role=user caller breaks down only their own sessions; someone else's and a
// missing one answer the same 404, so the route does not reveal which exist.
func TestSessionAccountSegments_RoleUserOwnSessionOnly(t *testing.T) {
	caller := &auth.DashboardUser{Role: "user", Email: "me@example.com", CctraceUserID: "caller"}
	for _, userIDMode := range []bool{true, false} {
		for _, tc := range []struct {
			session string
			want    int
		}{
			{"mine", http.StatusOK},
			{"theirs", http.StatusNotFound},
			{"missing", http.StatusNotFound},
		} {
			t.Run(tc.session, func(t *testing.T) {
				m := newSegmentsStore()
				srv := newTestServer(m, nil)
				srv.useUserIDAccessControl = userIDMode

				rec := serveScopedAs(srv, caller, "/api/session-account-segments?session_id="+tc.session)
				if rec.Code != tc.want {
					t.Fatalf("userIDMode=%v status = %d, want %d (body %s)", userIDMode, rec.Code, tc.want, rec.Body.String())
				}
				if wantCalls := map[bool]int{true: 1, false: 0}[tc.want == http.StatusOK]; m.calls != wantCalls {
					t.Fatalf("segments store calls = %d, want %d", m.calls, wantCalls)
				}
			})
		}
	}
}

func TestSessionAccountSegments_AdminReadsAnySession(t *testing.T) {
	m := newSegmentsStore()
	srv := newTestServer(m, nil)

	rec := serveScopedAs(srv, &auth.DashboardUser{Role: "admin"}, "/api/session-account-segments?session_id=theirs")
	if rec.Code != http.StatusOK || m.calls != 1 {
		t.Fatalf("admin status = %d, store calls = %d; want 200 and 1", rec.Code, m.calls)
	}
}

// serveScopedAs runs path through the real mux as user (nil for no user).
func serveScopedAs(srv *Server, user *auth.DashboardUser, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != nil {
		req = req.WithContext(auth.WithUser(req.Context(), user))
	}
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}
