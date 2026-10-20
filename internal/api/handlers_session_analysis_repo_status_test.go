package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestSessionAnalysisRepoStatusPreservesAnalysisState(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"building", "ready", "none"} {
		for _, owner := range []bool{true, false} {
			role := "admin"
			if owner {
				role = "user"
			}
			t.Run(state+"/"+role, func(t *testing.T) {
				t.Parallel()
				m := &mockStore{sessionAnalysisReadFn: func(id string, access store.SessionAnalysisAccess) (map[string]interface{}, error) {
					if !access.RepoStatusOnly || access.User != owner {
						t.Fatalf("repo-status access=%+v", access)
					}
					return map[string]interface{}{
						"session_id": id, "owner_view": owner, "analysis_state": state,
						"header": map[string]interface{}{"repo_status": map[string]interface{}{"branch": "private-branch"}},
						"turns":  []string{"private analysis detail"},
					}, nil
				}}
				s := newTestServer(m, nil).WithSessionAnalysisEnabled(true).WithSessionRecordsEnabled(true)
				r := httptest.NewRequest(http.MethodGet, "/api/sessions/session-state/analysis?view=repo-status", nil)
				r = r.WithContext(auth.WithUser(r.Context(), &auth.DashboardUser{ID: 7, Role: role, Email: "owner@example.com"}))
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				var body map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["analysis_state"] != state || body["session_id"] != "session-state" || body["owner_view"] != owner {
					t.Fatalf("repo-status lost analysis state: %s", w.Body.String())
				}
				if len(body) != 4 || (body["repo_status"] != nil) != owner {
					t.Fatalf("repo-status projection escaped owner scope: %s", w.Body.String())
				}
			})
		}
	}
}
