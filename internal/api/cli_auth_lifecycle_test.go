package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"cctrace/internal/store"
)

type cliLifecycleStore struct {
	*mockStore
	user *store.DashboardUser
}

func (s *cliLifecycleStore) GetDashboardUserByCctraceUserID(context.Context, string) (*store.DashboardUser, error) {
	return s.user, nil
}

func TestCLIAuthPasswordChangeGate(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, pending := range []bool{true, false} {
		t.Run(map[bool]string{true: "temporary", false: "changed"}[pending], func(t *testing.T) {
			user := &store.DashboardUser{ID: 42, IsActive: true, PasswordHash: string(hash), MustChangePassword: pending, ApiToken: "cct_existing"}
			touched := false
			s := &Server{store: &cliLifecycleStore{mockStore: &mockStore{
				getDashboardUserByAPITokenFn: func(context.Context, string) (*store.DashboardUser, error) { touched = true; return user, nil },
				setDashboardUserAPITokenFn:   func(context.Context, int64, string) error { touched = true; return nil },
			}, user: user}}
			rec := httptest.NewRecorder()
			s.handleCLIAuth(rec, httptest.NewRequest("POST", "/api/cli/auth", strings.NewReader(`{"user_id":"test","password":"password"}`)))
			if pending {
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "password_change_required") || strings.Contains(rec.Body.String(), "api_token") || touched {
					t.Fatalf("temporary credentials accessed long-lived token: status=%d body=%s touched=%v", rec.Code, rec.Body.String(), touched)
				}
			} else if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cct_existing") {
				t.Fatalf("normal authentication failed: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
