package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthenticateUserPasswordChangeRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"password_change_required","must_change_password":true}`))
	}))
	defer server.Close()
	info, err := authenticateUser(server.URL, "user", "temporary", "")
	if info != nil || err == nil || !strings.Contains(err.Error(), "change your password on the dashboard") {
		t.Fatalf("expected actionable password change error, got info=%+v err=%v", info, err)
	}
}
