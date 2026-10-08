package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The hash is the point of this command: it is what --project takes, and nothing
// else prints it.
func TestProjectsShowsTheHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/open/v1/projects" {
			t.Errorf("path = %q, want the open API", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"items":[{"project_hash":"abc","project_name":"alpha","repository_name":"org/alpha"}],"total":1}`))
	}))
	defer srv.Close()

	var out strings.Builder
	if err := runProjects(http.DefaultClient, srv.URL, "tok", false, &out); err != nil {
		t.Fatalf("runProjects: %v", err)
	}
	for _, want := range []string{"alpha", "org/alpha", "abc", "HASH"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

// A list cut short without saying so is the failure the notice exists to prevent.
func TestProjectsSaysWhenTheListIsCutShort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"project_hash":"abc","project_name":"alpha"}],"total":9}`))
	}))
	defer srv.Close()

	var out strings.Builder
	if err := runProjects(http.DefaultClient, srv.URL, "tok", false, &out); err != nil {
		t.Fatalf("runProjects: %v", err)
	}
	if !strings.Contains(out.String(), "showing 1 of 9") {
		t.Errorf("truncated list did not say so:\n%s", out.String())
	}
}
