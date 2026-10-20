package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// getMaintenanceStatus serves GET /api/maintenance/status through the routes.
func getMaintenanceStatus(srv *Server, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/maintenance/status", nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: cookie})
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// The dashboard shows its note while the token rule is pending and drops it
// once it is not, so both fields are always in the answer -- step too, empty
// when every registered step is done.
func TestMaintenanceStatusReportsTheTransition(t *testing.T) {
	for _, want := range []store.RequestSetMigrationStatus{
		{TokenRulePending: true, Step: "session_records_message_id_v1"},
		{TokenRulePending: true, Step: ""},
		{TokenRulePending: false, Step: ""},
	} {
		rec := getMaintenanceStatus(newTestServer(&mockStore{requestSetStatus: want}, nil), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%+v: status = %d (%s)", want, rec.Code, rec.Body.String())
		}
		var body struct {
			TokenRulePending *bool   `json:"token_rule_pending"`
			Step             *string `json:"step"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%+v: body = %s: %v", want, rec.Body.String(), err)
		}
		if body.TokenRulePending == nil || *body.TokenRulePending != want.TokenRulePending ||
			body.Step == nil || *body.Step != want.Step {
			t.Errorf("body = %s, want token_rule_pending %v and step %q", rec.Body.String(), want.TokenRulePending, want.Step)
		}
	}
}

// A status that cannot be read is an error like any other handler's, not a
// guess: the dashboard keeps what it last knew rather than take false for done.
func TestMaintenanceStatusSessionAnalysis(t *testing.T) {
	facts := store.SessionFactStatus{
		Mode:         "on_demand",
		RulesVersion: 1,
		Build:        store.SessionFactBuildStatus{State: "done_with_failures", Failed: 1, FailedSessions: []store.SessionFactFailedSession{{SessionID: "failed", Attempts: 10, LastError: "timeout"}}},
		Queue:        store.SessionFactQueueStatus{Depth: 3},
	}
	srv := newTestServer(&mockStore{sessionFactStatus: facts}, nil).WithSessionAnalysisEnabled(true)
	rec := getMaintenanceStatus(srv, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		SessionAnalysis store.SessionFactStatus `json:"session_analysis"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.SessionAnalysis.Enabled || body.SessionAnalysis.Mode != "on_demand" || body.SessionAnalysis.Build.Failed != 1 || body.SessionAnalysis.Queue.Depth != 3 {
		t.Fatalf("maintenance status: %+v", body.SessionAnalysis)
	}
	srv.WithSessionAnalysisEnabled(false)
	rec = getMaintenanceStatus(srv, "")
	if !strings.Contains(rec.Body.String(), `"enabled":false`) || !strings.Contains(rec.Body.String(), `"mode":"disabled"`) {
		t.Fatalf("default off status: %s", rec.Body.String())
	}
	srv = newTestServer(&mockStore{sessionFactStatusErr: errors.New("read analysis status")}, nil)
	if rec := getMaintenanceStatus(srv, ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("analysis store failure = %d, want 500", rec.Code)
	}
}

func TestMaintenanceStatusReportsAStoreError(t *testing.T) {
	m := &mockStore{requestSetStatusErr: errors.New("read request set migration status: connection reset")}
	rec := getMaintenanceStatus(newTestServer(m, nil), "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d (%s), want 500", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !strings.Contains(rec.Body.String(), "connection reset") {
		t.Errorf("body = %s (%v), want the error", rec.Body.String(), err)
	}
	if _, ok := body["token_rule_pending"]; ok {
		t.Errorf("body = %s, want no token_rule_pending beside the error", rec.Body.String())
	}
}

// Behind the dashboard sign-in, and open to every signed-in user rather than
// admins only: the note is on every page.
func TestMaintenanceStatusNeedsASignedInUser(t *testing.T) {
	mgr, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(&mockStore{requestSetStatus: store.RequestSetMigrationStatus{TokenRulePending: true}}, mgr)
	if rec := getMaintenanceStatus(srv, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a sign-in: status = %d (%s), want 401", rec.Code, rec.Body.String())
	}
	access, err := mgr.GenerateAccessToken(7, "user@example.com", "user", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if rec := getMaintenanceStatus(srv, access); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"token_rule_pending":true`) {
		t.Fatalf("signed in as a user: status = %d (%s), want 200 with the status", rec.Code, rec.Body.String())
	}
}
