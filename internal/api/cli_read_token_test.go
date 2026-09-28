package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"cctrace/internal/store"
)

type readTokenCalls struct {
	created  []store.DashboardAPIToken
	replaced []string
}

func readTokenServer(t *testing.T, user *store.DashboardUser) (*Server, *readTokenCalls) {
	t.Helper()
	calls := &readTokenCalls{}
	m := &mockStore{
		createDashboardUserAPITokenFn: func(_ context.Context, userID int64, name, token, createdVia string, expiresAt *time.Time) (*store.DashboardAPIToken, error) {
			if !strings.HasPrefix(token, "cct_") {
				t.Errorf("token %q is not a cct_ token", token)
			}
			created := store.DashboardAPIToken{UserID: userID, Name: name, CreatedVia: createdVia, ExpiresAt: expiresAt, IsActive: true}
			calls.created = append(calls.created, created)
			return &created, nil
		},
		deleteCLIReadTokenFn: func(_ context.Context, userID int64, secret string) (bool, error) {
			if userID != user.ID {
				t.Errorf("replaced a token of user %d", userID)
			}
			calls.replaced = append(calls.replaced, secret)
			return true, nil
		},
	}
	return newTestServer(&cliLifecycleStore{mockStore: m, user: user}, nil), calls
}

func postReadToken(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/cli/read-token", strings.NewReader(body)))
	return rec
}

func hashed(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

// A CLI read token is stored as created_via cli_read so the read API can refuse
// it at request time if its owner later becomes an administrator. Replacement
// names the exact token the client holds; matching by name deleted tokens on
// other machines with the same hostname and Settings tokens that happened to
// share the name.
func TestCLIReadTokenIssuesAndReplacesOnlyTheHeldToken(t *testing.T) {
	user := &store.DashboardUser{ID: 7, Role: "user", IsActive: true, PasswordHash: hashed(t, "pw")}
	s, calls := readTokenServer(t, user)

	rec := postReadToken(s, `{"user_id":"alice","password":"pw","device":"laptop","replace":"cct_held"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		APIToken string `json:"api_token"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !strings.HasPrefix(body.APIToken, "cct_") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if len(calls.created) != 1 {
		t.Fatalf("created %d tokens", len(calls.created))
	}
	c := calls.created[0]
	if c.UserID != 7 || c.CreatedVia != "cli_read" || c.Name != "CLI read token (laptop)" || c.ExpiresAt != nil || body.Name != c.Name {
		t.Fatalf("created %+v, response name %q", c, body.Name)
	}
	if len(calls.replaced) != 1 || calls.replaced[0] != "cct_held" {
		t.Fatalf("replaced %v, want exactly the held token", calls.replaced)
	}

	s, calls = readTokenServer(t, user)
	if rec := postReadToken(s, `{"user_id":"alice","password":"pw","device":"laptop"}`); rec.Code != http.StatusOK {
		t.Fatalf("first issuance status %d", rec.Code)
	}
	if len(calls.replaced) != 0 {
		t.Fatalf("first issuance replaced %v", calls.replaced)
	}
}

func TestCLIReadTokenRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		user     store.DashboardUser
		password string
		status   int
		want     string
	}{
		"administrator":      {store.DashboardUser{ID: 1, Role: "admin", IsActive: true}, "pw", http.StatusForbidden, "admin_read_token_forbidden"},
		"wrong password":     {store.DashboardUser{ID: 1, Role: "user", IsActive: true}, "nope", http.StatusUnauthorized, "invalid credentials"},
		"disabled":           {store.DashboardUser{ID: 1, Role: "user", IsActive: false}, "pw", http.StatusUnauthorized, "account is disabled"},
		"temporary password": {store.DashboardUser{ID: 1, Role: "user", IsActive: true, MustChangePassword: true}, "pw", http.StatusForbidden, "password_change_required"},
		"unsupported role":   {store.DashboardUser{ID: 1, Role: "viewer", IsActive: true}, "pw", http.StatusForbidden, "forbidden"},
	} {
		t.Run(name, func(t *testing.T) {
			user := tc.user
			user.PasswordHash = hashed(t, "pw")
			s, calls := readTokenServer(t, &user)

			rec := postReadToken(s, `{"user_id":"alice","password":"`+tc.password+`","device":"laptop"}`)

			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "cct_") || len(calls.created)+len(calls.replaced) != 0 {
				t.Fatalf("refused request touched tokens: %+v body=%s", calls, rec.Body.String())
			}
		})
	}
}

func TestCLIReadTokenDeviceNameIsBoundedAndPrintable(t *testing.T) {
	user := &store.DashboardUser{ID: 7, Role: "user", IsActive: true, PasswordHash: hashed(t, "pw")}
	for device, want := range map[string]string{
		"":                       "CLI read token",
		"  work\nbox\t ":         "CLI read token (workbox)",
		strings.Repeat("a", 100): "CLI read token (" + strings.Repeat("a", 47) + ")",
	} {
		s, calls := readTokenServer(t, user)
		payload, _ := json.Marshal(map[string]string{"user_id": "alice", "password": "pw", "device": device})
		if rec := postReadToken(s, string(payload)); rec.Code != http.StatusOK {
			t.Fatalf("device %q: status %d", device, rec.Code)
		}
		if got := calls.created[0].Name; got != want || len(got) > 64 {
			t.Errorf("device %q: name %q, want %q", device, got, want)
		}
	}
}

// The administrator refusal holds at request time, not only at issuance: a user
// promoted after getting a CLI read token must not start reading every user.
func TestOpenAPIRefusesCLIReadTokenOfAdministrator(t *testing.T) {
	m := &mockStore{
		getDashboardUserByOpenAPITokenFn: func(context.Context, string) (*store.DashboardUser, error) {
			return nil, store.ErrCLIReadTokenAdmin
		},
	}
	rec := openAPIRequest(t, newTestServer(m, nil), http.MethodGet, "/api/open/v1/projects", "Bearer cct_promoted")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "admin_read_token_forbidden") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
