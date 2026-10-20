package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// This store deliberately has no TraceStore methods: canonical reads cannot
// require a trace worker, lease, or snapshot.
type turnContextAPIMock struct {
	*mockStore
	access  store.SessionAnalysisAccess
	request store.TurnContextRequest
	reads   int
	err     error
}

func (m *turnContextAPIMock) TurnContext(_ context.Context, id, turn string, a store.SessionAnalysisAccess, q store.TurnContextRequest) (store.TurnContextResponse, error) {
	m.access, m.request = a, q
	m.reads++
	return store.TurnContextResponse{SessionID: id, TurnKey: turn, State: "ready", Revision: "current", Records: []*store.SessionRecord{{SessionID: id, UUID: "record", RecordType: "user", Agent: "claude", Raw: json.RawMessage(`{"private_field":"must not leak","message":{"role":"user","content":"visible message"}}`)}}}, m.err
}

func TestTurnContextAPIIndependentSafeViewAndScope(t *testing.T) {
	t.Parallel()
	m := &turnContextAPIMock{mockStore: &mockStore{}}
	s := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
	r := httptest.NewRequest("GET", "/api/sessions/session/turns/turn/context?revision=expected&cursor=opaque&profile_email=foreign&login_email=foreign&user_id=foreign&raw=1", nil)
	r = r.WithContext(auth.WithUser(r.Context(), &auth.DashboardUser{ID: 7, Role: "user", CctraceUserID: "owner"}))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("canonical read status=%d body=%s", w.Code, w.Body.String())
	}
	if m.reads != 1 || m.access.UserID != "owner" || !m.access.User || m.access.LoginEmail != "" || m.request.Limit != 20 || m.request.Revision != "expected" || m.request.Cursor != "opaque" {
		t.Fatalf("scope/request escaped: access=%+v request=%+v reads=%d", m.access, m.request, m.reads)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session_id", "turn_key", "state", "revision", "records", "next_cursor"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing wire field %s", key)
		}
	}
	if strings.Contains(w.Body.String(), `"raw"`) || strings.Contains(w.Body.String(), "must not leak") || !strings.Contains(w.Body.String(), "visible message") || !strings.Contains(w.Body.String(), `"view"`) {
		t.Fatalf("unsafe or absent projection: %s", w.Body.String())
	}
}

func TestTurnContextAPILimitsGateAndErrors(t *testing.T) {
	t.Parallel()
	m := &turnContextAPIMock{mockStore: &mockStore{}}
	s := newTestServer(m, nil).WithSessionAnalysisEnabled(true)
	user := &auth.DashboardUser{ID: 8, Role: "admin"}
	request := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/sessions/session/turns/turn/context"+query, nil)
		r = r.WithContext(auth.WithUser(r.Context(), user))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=-1", "?limit=x"} {
		if w := request(query); w.Code != 400 {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
	if m.reads != 0 {
		t.Fatal("invalid pagination reached store")
	}
	if w := request("?limit=100&profile_email=profile&login_email=account&user_id=uid"); w.Code != 200 || m.request.Limit != 100 || m.access.ProfileEmail != "profile" || m.access.LoginEmail != "account" || m.access.UserID != "uid" {
		t.Fatalf("admin scope: %d %+v", w.Code, m.access)
	}
	for _, test := range []struct {
		err    error
		status int
	}{{store.ErrTurnContextRevision, 409}, {store.ErrTurnContextInvalid, 400}, {store.ErrSessionAnalysisNotFound, 404}, {store.ErrTurnContextTurnNotFound, 404}, {errors.New("store failed"), 500}} {
		m.err = test.err
		if w := request(""); w.Code != test.status {
			t.Fatalf("error %v: %d %s", test.err, w.Code, w.Body.String())
		}
	}
	m.err = store.ErrTurnContextTurnNotFound
	if w := request(""); !strings.Contains(w.Body.String(), "turn_not_found") {
		t.Fatalf("missing turn distinction: %s", w.Body.String())
	}
	s.WithSessionAnalysisEnabled(false)
	if w := request(""); w.Code != 404 {
		t.Fatalf("disabled: %d", w.Code)
	}
}
