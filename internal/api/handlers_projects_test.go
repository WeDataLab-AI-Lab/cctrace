package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// The picker feeds the session list, so it has to be asked the same question the list
// is asked. Without these parameters the dropdown offered projects the list then had
// nothing to show for (#352). All four are checked here because the session list filters
// on all four; source/agent alone left the account selector reproducing the bug.
func TestListProjects_PassesEverySessionListFilter(t *testing.T) {
	var captured store.ProjectFilter
	m := &mockStore{
		listProjectsFn: func(ctx context.Context, f store.ProjectFilter) ([]*store.Project, error) {
			captured = f
			return nil, nil
		},
	}
	ts := httptest.NewServer(newTestServer(m, nil).Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/projects?source=interactive&agent=claude&login_email=me%40example.com&assembled=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	want := store.ProjectFilter{
		LoginEmail:  "me@example.com",
		Source:      "interactive",
		Agent:       "claude",
		FoldLineage: true,
	}
	if captured != want {
		t.Fatalf("filter = %#v, want %#v", captured, want)
	}
}

// A restricted user's scope is not something a query parameter may widen: the account
// keys are overwritten from the session, never merged with what was asked for.
func TestListProjects_RestrictedUserScopeIgnoresQueryAccount(t *testing.T) {
	var captured store.ProjectFilter
	m := &mockStore{
		listProjectsFn: func(ctx context.Context, f store.ProjectFilter) ([]*store.Project, error) {
			captured = f
			return nil, nil
		},
	}
	srv := newTestServer(m, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/projects?login_email=someone-else%40example.com&source=interactive", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{
		Role:          "user",
		Email:         "login@example.com",
		CctraceUserID: "otel-user-1",
	}))
	rec := httptest.NewRecorder()
	srv.handleListProjects(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if captured.UserID != "otel-user-1" {
		t.Fatalf("user_id = %q, want the caller's own", captured.UserID)
	}
	if captured.LoginEmail == "someone-else@example.com" {
		t.Fatal("query login_email must not survive for a restricted user")
	}
	if captured.Source != "interactive" {
		t.Fatalf("source = %q, want the query's own narrowing to survive", captured.Source)
	}
}

func TestListProjects_NoFiltersStaysUnscoped(t *testing.T) {
	var captured store.ProjectFilter
	m := &mockStore{
		listProjectsFn: func(ctx context.Context, f store.ProjectFilter) ([]*store.Project, error) {
			captured = f
			return nil, nil
		},
	}
	ts := httptest.NewServer(newTestServer(m, nil).Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if captured != (store.ProjectFilter{}) {
		t.Fatalf("expected an empty filter, got %#v", captured)
	}
}
