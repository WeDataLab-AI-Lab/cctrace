package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResourceCommandsReadOpenAPIWithWindow(t *testing.T) {
	for _, resource := range []string{"tools", "plugins", "skills", "organization-insights"} {
		t.Run(resource, func(t *testing.T) {
			var path, query, authorization string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path, query, authorization = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`[]`))
			}))
			defer srv.Close()

			var out strings.Builder
			if err := runOpenAPIResource(http.DefaultClient, srv.URL, "read-token", resource, 7*24*time.Hour, "codex", "", "", 100, true, &out); err != nil {
				t.Fatalf("runOpenAPIResource: %v", err)
			}
			if path != "/api/open/v1/"+resource {
				t.Errorf("path = %q", path)
			}
			for _, want := range []string{"since=", "until="} {
				if !strings.Contains(query, want) {
					t.Errorf("query %q missing %q", query, want)
				}
			}
			if (resource == "tools" || resource == "organization-insights") && strings.Contains(query, "agent=") {
				t.Errorf("%s query %q includes unsupported agent filter", resource, query)
			}
			if resource == "plugins" || resource == "skills" {
				if !strings.Contains(query, "agent=codex") {
					t.Errorf("query %q missing agent filter", query)
				}
			}
			if authorization != "Bearer read-token" {
				t.Errorf("Authorization = %q", authorization)
			}
			if out.String() != "[]" {
				t.Errorf("raw output = %q", out.String())
			}
		})
	}
}

func TestRulesCommandDoesNotInventATimeFilter(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	if err := runOpenAPIResource(http.DefaultClient, srv.URL, "tok", "rules", 0, "codex", "active", "lint", 25, true, nil); err != nil {
		t.Fatalf("runOpenAPIResource: %v", err)
	}
	for _, forbidden := range []string{"since=", "until="} {
		if strings.Contains(query, forbidden) {
			t.Errorf("rules query %q contains unsupported %q", query, forbidden)
		}
	}
	for _, want := range []string{"agent=codex", "status=active", "query=lint", "limit=25"} {
		if !strings.Contains(query, want) {
			t.Errorf("rules query %q missing %q", query, want)
		}
	}
}
