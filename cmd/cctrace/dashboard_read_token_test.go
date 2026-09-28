package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/profile"
)

// sessions and report read dashboard routes, which since #702 refuse the upload
// token exactly as the Open API does. They have to send the read token.
func TestDashboardReadCommandsSendReadToken(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	saveReadTokenProfile(t, srv.URL, "cct_read")

	if err := runSessions("", "", 5, true); err != nil {
		t.Fatalf("runSessions: %v", err)
	}
	if err := runReport(time.Hour, true); err != nil {
		t.Fatalf("runReport: %v", err)
	}
	for _, path := range []string{"/api/sessions", "/api/cost/by-user", "/api/cost/by-team", "/api/tools"} {
		if got := seen[path]; got != "Bearer cct_read" {
			t.Errorf("%s auth = %q, want the read token", path, got)
		}
	}
}

// Without a read token the server refuses the upload token; the command has to
// say how to get one rather than print a raw 401.
func TestDashboardReadCommandsExplainUploadTokenRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"ingestion_token","error":"this token is issued for ingestion"}`))
	}))
	defer srv.Close()
	saveReadTokenProfile(t, srv.URL, "")

	for name, run := range map[string]func() error{
		"sessions": func() error { return runSessions("", "", 5, true) },
		"report":   func() error { return runReport(time.Hour, true) },
	} {
		err := run()
		if err == nil || !strings.Contains(err.Error(), "cctrace auth read") {
			t.Errorf("%s: error = %v, want it to name 'cctrace auth read'", name, err)
		}
	}
}

func saveReadTokenProfile(t *testing.T, endpoint, readToken string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p := profile.NewDefault()
	p.User.ID = "alice"
	p.Server.SyncEndpoint = endpoint
	p.Server.AuthToken = "cct_upload"
	p.Server.ReadToken = readToken
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
}
