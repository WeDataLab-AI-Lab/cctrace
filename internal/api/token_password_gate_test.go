package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

type tokenPasswordStore struct {
	*mockStore
	user *store.DashboardUser
}

func (s *tokenPasswordStore) GetDashboardUserByEmail(context.Context, string) (*store.DashboardUser, error) {
	return s.user, nil
}
func (s *tokenPasswordStore) GetDashboardUserByID(context.Context, int64) (*store.DashboardUser, error) {
	return s.user, nil
}
func (s *tokenPasswordStore) UpdateDashboardUserPassword(_ context.Context, _ int64, hash string, pending bool) error {
	s.user.PasswordHash = hash
	s.user.MustChangePassword = pending
	return nil
}

func TestTokenRoutesRequirePasswordChange(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("temporary"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"create", "POST", "/api/auth/api-tokens", `{"name":"CI"}`, 201},
		{"rotate", "POST", "/api/auth/api-tokens/7/rotate", `{}`, 200},
		{"reactivate", "PATCH", "/api/auth/api-tokens/7", `{"is_active":true}`, 200},
		{"extend", "PATCH", "/api/auth/api-tokens/7", `{"expiration_mode":"unlimited"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			token := func() (*store.DashboardAPIToken, error) { calls++; return &store.DashboardAPIToken{ID: 7}, nil }
			st := &tokenPasswordStore{mockStore: &mockStore{
				createDashboardUserAPITokenFn: func(context.Context, int64, string, string, string, *time.Time) (*store.DashboardAPIToken, error) {
					return token()
				},
				rotateDashboardUserAPITokenFn: func(context.Context, int64, int64, string) (*store.DashboardAPIToken, error) { return token() },
				setDashboardUserTokenActiveFn: func(context.Context, int64, int64, bool) (*store.DashboardAPIToken, error) { return token() },
				setDashboardUserExpirationFn:  func(context.Context, int64, int64, *time.Time) (*store.DashboardAPIToken, error) { return token() },
			}, user: &store.DashboardUser{ID: 42, Email: "test@example.com", Role: "user", IsActive: true, PasswordHash: string(hash), MustChangePassword: true}}
			jwt, err := auth.NewJWTManager(strings.Repeat("s", 32))
			if err != nil {
				t.Fatal(err)
			}
			srv := NewServer(st, jwt)
			request := func(method, path, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Origin", "http://"+req.Host)
				req.Header.Set("X-Requested-With", "XMLHttpRequest")
				for _, c := range cookies {
					req.AddCookie(c)
				}
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				return rec
			}
			login := request("POST", "/api/auth/login", `{"email":"test@example.com","password":"temporary"}`, nil)
			if login.Code != 200 {
				t.Fatalf("temporary password must permit login: %d %s", login.Code, login.Body.String())
			}
			cookies := login.Result().Cookies()
			denied := request(tc.method, tc.path, tc.body, cookies)
			if denied.Code != 403 || !strings.Contains(denied.Body.String(), "change your password") || calls != 0 || strings.Contains(denied.Body.String(), "api_token") {
				t.Errorf("token ability not blocked: %d %s calls=%d", denied.Code, denied.Body.String(), calls)
			}
			changed := request("POST", "/api/auth/change-password", `{"current_password":"temporary","new_password":"changed-password"}`, cookies)
			if changed.Code != 200 {
				t.Fatalf("password change blocked: %d %s", changed.Code, changed.Body.String())
			}
			calls = 0
			allowed := request(tc.method, tc.path, tc.body, cookies)
			if allowed.Code != tc.status || calls != 1 {
				t.Fatalf("changed password still blocked: %d %s calls=%d", allowed.Code, allowed.Body.String(), calls)
			}
			// A JWT issued before an administrator reset must not bypass the DB gate.
			login = request("POST", "/api/auth/login", `{"email":"test@example.com","password":"changed-password"}`, nil)
			st.user.MustChangePassword = true
			calls = 0
			denied = request(tc.method, tc.path, tc.body, login.Result().Cookies())
			if denied.Code != 403 || calls != 0 {
				t.Errorf("stale unrestricted JWT bypassed reset: %d calls=%d", denied.Code, calls)
			}
		})
	}
}
