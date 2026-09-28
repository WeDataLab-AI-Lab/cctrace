package api

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/store"

	"golang.org/x/crypto/bcrypt"
)

func TestSetupRejectsMissingToken(t *testing.T) {
	jwt, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(&mockStore{}, jwt)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(`{"email":"admin@example.com","password":"password123"}`))
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	rec := httptest.NewRecorder()
	srv.handleSetup(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSetupToken(t *testing.T) {
	for _, tc := range []struct {
		name, configured, supplied string
		storeErr                   error
		status                     int
	}{
		{"missing", "secret", "", nil, 403},
		{"wrong", "secret", "wrong", nil, 403},
		{"disabled", "", "secret", nil, 403},
		{"success", "secret", "secret", nil, 201},
		{"completed", "secret", "secret", store.ErrSetupAlreadyCompleted, 409},
		{"database failure", "secret", "secret", errors.New("database failed"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var audit bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&audit)
			defer log.SetOutput(previous)
			calls := 0
			m := &mockStore{createInitialDashboardUserFn: func(_ context.Context, u *store.DashboardUser) error {
				calls++
				if u.Role != "admin" || u.Email != "admin@example.com" {
					t.Fatalf("unexpected user: %+v", u)
				}
				if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("password123")); err != nil {
					t.Fatal(err)
				}
				u.ID = 42
				return tc.storeErr
			}}
			jwt, err := auth.NewJWTManager(strings.Repeat("s", 32))
			if err != nil {
				t.Fatal(err)
			}
			srv := NewServer(m, jwt).WithSetupToken(tc.configured)
			req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(`{"email":"admin@example.com","password":"password123"}`))
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			req.Header.Set("X-Setup-Token", tc.supplied)
			rec := httptest.NewRecorder()
			srv.handleSetup(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("expected %d got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
			if tc.status == 403 && calls != 0 {
				t.Fatal("unauthorized request reached store")
			}
			if tc.status == 201 {
				if len(rec.Result().Cookies()) != 2 || !strings.Contains(audit.String(), "action=setup actor=admin@example.com") {
					t.Fatal("missing cookies or audit")
				}
			} else if len(rec.Result().Cookies()) != 0 || strings.Contains(audit.String(), "[audit] action=setup") {
				t.Fatal("failed setup emitted cookies or audit")
			}
		})
	}
}

func TestSetupRateLimit(t *testing.T) {
	srv := NewServer(&mockStore{}, nil)
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", nil)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		want := http.StatusForbidden
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("request %d: got %d want %d", i, rec.Code, want)
		}
	}
}

func TestSetupDatabaseFailureDoesNotConsumeToken(t *testing.T) {
	calls := 0
	m := &mockStore{createInitialDashboardUserFn: func(_ context.Context, u *store.DashboardUser) error {
		calls++
		if calls == 1 {
			return errors.New("temporary database failure")
		}
		u.ID = 42
		return nil
	}}
	jwt, err := auth.NewJWTManager(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(m, jwt).WithSetupToken("secret")
	for _, want := range []int{500, 201} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(`{"email":"admin@example.com","password":"password123"}`))
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("X-Setup-Token", "secret")
		rec := httptest.NewRecorder()
		srv.handleSetup(rec, req)
		if rec.Code != want {
			t.Fatalf("got %d want %d", rec.Code, want)
		}
	}
}
