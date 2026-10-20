package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"cctrace/internal/auth"
	"cctrace/internal/store"
)

func TestAuthMeReportsUserIDAccessControl(t *testing.T) {
	mgr, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"admin", "user"} {
		for _, uidMode := range []bool{true, false} {
			name := role + "/legacy"
			if uidMode {
				name = role + "/uid"
			}
			t.Run(name, func(t *testing.T) {
				srv := NewServer(&mockStore{}, mgr)
				srv.useUserIDAccessControl = uidMode
				token, err := mgr.GenerateAccessToken(7, "owner@example.com", role, "Owner", "owner-uid", false)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
				req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: token})
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				var body map[string]interface{}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				mode, ok := body["user_id_access_control"].(bool)
				if !ok || mode != uidMode {
					t.Fatalf("access mode=%v want=%t", body["user_id_access_control"], uidMode)
				}
				if body["id"] != float64(7) || body["email"] != "owner@example.com" || body["role"] != role || body["name"] != "Owner" || body["cctrace_user_id"] != "owner-uid" || body["must_change_password"] != false {
					t.Fatalf("existing identity fields changed: %+v", body)
				}
				unauthenticated := httptest.NewRecorder()
				srv.Handler().ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
				if unauthenticated.Code != http.StatusUnauthorized {
					t.Fatalf("unauthenticated status=%d", unauthenticated.Code)
				}
			})
		}
	}
}

func TestAuthLoginRefreshReportsUserIDAccessControl(t *testing.T) {
	mgr, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("temporary"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"admin", "user"} {
		for _, uidMode := range []bool{true, false} {
			name := role + "/legacy"
			if uidMode {
				name = role + "/uid"
			}
			t.Run(name, func(t *testing.T) {
				user := &store.DashboardUser{ID: 7, Email: "owner@example.com", Role: role, Name: "Owner", CctraceUserID: "owner-uid", PasswordHash: string(hash), IsActive: true, MustChangePassword: true}
				st := &tokenPasswordStore{mockStore: &mockStore{}, user: user}
				srv := NewServer(st, mgr)
				srv.useUserIDAccessControl = uidMode
				request := func(path, body string, cookies []*http.Cookie) *httptest.ResponseRecorder {
					req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					req.Header.Set("Origin", "http://"+req.Host)
					req.Header.Set("X-Requested-With", "XMLHttpRequest")
					for _, cookie := range cookies {
						req.AddCookie(cookie)
					}
					rec := httptest.NewRecorder()
					srv.Handler().ServeHTTP(rec, req)
					return rec
				}
				assertUser := func(rec *httptest.ResponseRecorder) {
					t.Helper()
					if rec.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
					}
					var body struct {
						User map[string]interface{} `json:"user"`
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					mode, ok := body.User["user_id_access_control"].(bool)
					if !ok || mode != uidMode {
						t.Fatalf("access mode=%v want=%t", body.User["user_id_access_control"], uidMode)
					}
					if len(body.User) != 7 || body.User["id"] != float64(user.ID) || body.User["email"] != user.Email || body.User["role"] != user.Role || body.User["name"] != user.Name || body.User["cctrace_user_id"] != user.CctraceUserID || body.User["must_change_password"] != user.MustChangePassword {
						t.Fatalf("existing identity fields changed: %+v", body.User)
					}
				}
				assertAccess := func(cookie *http.Cookie) {
					t.Helper()
					claims, err := mgr.ValidateToken(cookie.Value)
					if err != nil {
						t.Fatal(err)
					}
					if claims.Type != "" || claims.UserID != user.ID || claims.Subject != user.Email || claims.Role != user.Role || claims.Name != user.Name || claims.CctraceUserID != user.CctraceUserID || claims.MustChangePassword != user.MustChangePassword {
						t.Fatalf("access claims changed: %+v", claims)
					}
					if cookie.Path != "/" || !cookie.HttpOnly || cookie.MaxAge != 900 || cookie.SameSite != http.SameSiteLaxMode {
						t.Fatalf("access cookie changed: name=%q path=%q httpOnly=%t secure=%t maxAge=%d sameSite=%d valuePresent=%t", cookie.Name, cookie.Path, cookie.HttpOnly, cookie.Secure, cookie.MaxAge, cookie.SameSite, cookie.Value != "")
					}
				}
				login := request("/api/auth/login", `{"email":"owner@example.com","password":"temporary"}`, nil)
				assertUser(login)
				cookies := login.Result().Cookies()
				if len(cookies) != 2 {
					t.Fatalf("login cookie count=%d", len(cookies))
				}
				var access, refresh *http.Cookie
				for _, cookie := range cookies {
					switch cookie.Name {
					case "cctrace_token":
						access = cookie
					case "cctrace_refresh":
						refresh = cookie
					}
				}
				if access == nil || refresh == nil {
					t.Fatal("missing auth cookies")
				}
				assertAccess(access)
				claims, err := mgr.ValidateToken(refresh.Value)
				if err != nil || claims.Type != "refresh" || claims.UserID != user.ID || refresh.Path != "/api/auth/refresh" || refresh.MaxAge != 604800 || !refresh.HttpOnly || refresh.SameSite != http.SameSiteStrictMode {
					t.Fatalf("refresh cookie changed: name=%q path=%q httpOnly=%t secure=%t maxAge=%d sameSite=%d valuePresent=%t claimsPresent=%t validationFailed=%t", refresh.Name, refresh.Path, refresh.HttpOnly, refresh.Secure, refresh.MaxAge, refresh.SameSite, refresh.Value != "", claims != nil, err != nil)
				}
				user.Name, user.MustChangePassword = "Updated", false
				refreshed := request("/api/auth/refresh", "", []*http.Cookie{refresh})
				assertUser(refreshed)
				newCookies := refreshed.Result().Cookies()
				if len(newCookies) != 1 || newCookies[0].Name != "cctrace_token" {
					names := make([]string, len(newCookies))
					for i, cookie := range newCookies {
						names[i] = cookie.Name
					}
					t.Fatalf("refresh must only replace access cookie: names=%q", names)
				}
				assertAccess(newCookies[0])
				for _, tc := range []struct {
					name, path, body string
					cookies          []*http.Cookie
				}{
					{"wrong_password", "/api/auth/login", `{"email":"owner@example.com","password":"wrong"}`, nil},
					{"missing_refresh", "/api/auth/refresh", "", nil},
					{"invalid_refresh", "/api/auth/refresh", "", []*http.Cookie{{Name: "cctrace_refresh", Value: "invalid"}}},
					{"access_as_refresh", "/api/auth/refresh", "", []*http.Cookie{{Name: "cctrace_refresh", Value: access.Value}}},
				} {
					rec := request(tc.path, tc.body, tc.cookies)
					if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 || strings.Contains(rec.Body.String(), "user_id_access_control") {
						t.Fatalf("%s status=%d body=%s", tc.name, rec.Code, rec.Body.String())
					}
				}
				user.IsActive = false
				for _, path := range []string{"/api/auth/login", "/api/auth/refresh"} {
					rec := request(path, `{"email":"owner@example.com","password":"temporary"}`, []*http.Cookie{refresh})
					if rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
						t.Fatalf("disabled account path=%s status=%d", path, rec.Code)
					}
				}
			})
		}
	}
}

// The dashboard read API takes a bearer token on the same rule as the Open API
// (#702): tokens a person created for reading (web, cli_read), and no cli_read
// token of an administrator. The upload token `cctrace init` stores in plaintext
// used to pass here, and for an administrator that meant /api/admin/* too.
func TestDashboardReadTokenFollowsOpenAPIRule(t *testing.T) {
	mgr, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	admin := &store.DashboardUser{ID: 1, Email: "admin@ex.com", Role: "admin", IsActive: true}
	m := &mockStore{
		// The unfiltered lookup accepts everything, as it did before the fix.
		getDashboardUserByAPITokenFn: func(context.Context, string) (*store.DashboardUser, error) {
			return admin, nil
		},
		getDashboardUserByOpenAPITokenFn: func(_ context.Context, token string) (*store.DashboardUser, error) {
			switch token {
			case "upload":
				return nil, store.ErrTokenNotOpenAPI
			case "cli-read-of-admin":
				return nil, store.ErrCLIReadTokenAdmin
			case "web":
				return &store.DashboardUser{ID: 7, Email: "alice@ex.com", Role: "user", IsActive: true}, nil
			case "web-inactive":
				return &store.DashboardUser{ID: 8, Email: "bob@ex.com", Role: "user", IsActive: false}, nil
			}
			return nil, errors.New("not found")
		},
	}
	srv := NewServer(m, mgr)
	get := func(target, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	t.Run("upload token is refused with a hint", func(t *testing.T) {
		rec := get("/api/admin/users", "upload")
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"code":"ingestion_token"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("cli_read token of an administrator is refused", func(t *testing.T) {
		rec := get("/api/admin/users", "cli-read-of-admin")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"admin_read_token_forbidden"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("web token is accepted", func(t *testing.T) {
		rec := get("/api/auth/me", "web")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "alice@ex.com") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("inactive owner is refused", func(t *testing.T) {
		if rec := get("/api/auth/me", "web-inactive"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("browser cookie is unaffected", func(t *testing.T) {
		access, err := mgr.GenerateAccessToken(1, "admin@ex.com", "admin", "", "", false)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		req.AddCookie(&http.Cookie{Name: "cctrace_token", Value: access})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "admin@ex.com") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
}
