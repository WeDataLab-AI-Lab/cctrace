package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"cctrace/internal/auth"
)

// The Account chips sit above screens that show every member's usage, and the
// handlers behind them apply login_email to everyone's rows. Offering a role=user
// viewer only the accounts in their own rows left a teammate's shared account
// counted under All but impossible to select.
func TestAccounts_RestrictedUserSeesEveryAccount(t *testing.T) {
	m := &mockStore{
		listLoginAccountsFn: func(ctx context.Context, userID string) ([]string, error) {
			if userID != "" {
				return []string{"own@example.com"}, nil
			}
			return []string{"own@example.com", "teammate@example.com"}, nil
		},
	}
	srv := newTestServer(m, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.DashboardUser{
		Role:          "user",
		Email:         "own@example.com",
		CctraceUserID: "otel-user-1",
	}))
	rec := httptest.NewRecorder()
	srv.handleAccounts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var got []string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := []string{"own@example.com", "teammate@example.com"}; !slices.Equal(got, want) {
		t.Fatalf("accounts = %v, want %v", got, want)
	}
}

// scope=mine is a role=user display choice that trims the chips back to the
// accounts in the caller's own rows. It is resolved the same way every other
// role=user read is, and it never changes what an admin is offered.
func TestAccounts_MineScopeTrimsOnlyRoleUser(t *testing.T) {
	linked := &auth.DashboardUser{Role: "user", Email: "own@example.com", CctraceUserID: "otel-user-1"}
	unlinked := &auth.DashboardUser{Role: "user", Email: "own@example.com"}
	admin := &auth.DashboardUser{Role: "admin", Email: "admin@example.com", CctraceUserID: "otel-admin"}

	cases := []struct {
		name       string
		user       *auth.DashboardUser
		query      string
		wantUserID string
	}{
		{"user default lists everyone's", linked, "", ""},
		{"unlinked user default lists everyone's", unlinked, "", ""},
		{"user mine lists own", linked, "?scope=mine", "otel-user-1"},
		{"unlinked user mine matches nothing", unlinked, "?scope=mine", noAccessSentinel},
		{"admin mine is ignored", admin, "?scope=mine", ""},
		{"unknown scope lists everyone's", linked, "?scope=team", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotUserID := "<not called>"
			m := &mockStore{
				listLoginAccountsFn: func(ctx context.Context, userID string) ([]string, error) {
					gotUserID = userID
					return []string{}, nil
				},
			}
			srv := newTestServer(m, nil)
			req := httptest.NewRequest(http.MethodGet, "/api/accounts"+tc.query, nil)
			req = req.WithContext(auth.WithUser(req.Context(), tc.user))
			rec := httptest.NewRecorder()
			srv.handleAccounts(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			if gotUserID != tc.wantUserID {
				t.Fatalf("store user_id = %q, want %q", gotUserID, tc.wantUserID)
			}
		})
	}
}
