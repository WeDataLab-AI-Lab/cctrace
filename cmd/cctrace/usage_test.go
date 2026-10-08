package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The CLI is the first consumer of the read API, which is the point: if the contract
// breaks, we find out before anyone outside does. So these check what actually goes
// on the wire, not just what comes back.

func TestUsageSendsAbsoluteWindowAndGroup(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAuth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()

	if err := runUsage(http.DefaultClient, srv.URL, "tok", 7*24*3600*1e9, "user", true, nil); err != nil {
		t.Fatalf("runUsage: %v", err)
	}
	if gotPath != "/api/open/v1/usage" {
		t.Errorf("path = %q, want the open API, not an internal endpoint", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	// The API takes absolute times. "7d" is a convenience of the CLI and must not
	// leak into the contract.
	for _, key := range []string{"since=2", "until=2", "group_by=user"} {
		if !strings.Contains(gotQuery, key) {
			t.Errorf("query %q is missing %q", gotQuery, key)
		}
	}
	if strings.Contains(gotQuery, "7d") {
		t.Errorf("query %q leaked the relative window", gotQuery)
	}
}

// A refusal from the API is the API telling the user something. Printing "server
// returned 400" and swallowing the body hides the sentence that says what to do.
func TestUsageSurfacesAPIRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"ingestion_token","error":"this token is issued for ingestion; create a token under Settings > API Access Tokens to read the API"}`))
	}))
	defer srv.Close()

	err := runUsage(http.DefaultClient, srv.URL, "cli-token", 24*3600*1e9, "", true, nil)
	if err == nil {
		t.Fatal("want an error for a 401")
	}
	if !strings.Contains(err.Error(), "Settings") {
		t.Fatalf("error = %q, want it to carry the API's own instruction", err)
	}
}

func TestUsageJSONPassesThroughUnchanged(t *testing.T) {
	body := `{"items":[{"key":"someone","cost_usd":1.5,"request_count":3}],"total":1}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	var out strings.Builder
	if err := runUsage(http.DefaultClient, srv.URL, "tok", 24*3600*1e9, "user", true, &out); err != nil {
		t.Fatalf("runUsage: %v", err)
	}
	var got, want map[string]any
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	_ = json.Unmarshal([]byte(body), &want)
	if got["total"] != want["total"] {
		t.Fatalf("total = %v, want %v", got["total"], want["total"])
	}
}

func TestUsageSupportsACompletedComparisonWindow(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"session_count":0}`))
	}))
	defer srv.Close()

	if err := runUsageWindow(http.DefaultClient, srv.URL, "tok", 14*24*time.Hour, 7*24*time.Hour, "", true, nil); err != nil {
		t.Fatalf("runUsageWindow: %v", err)
	}
	if strings.Contains(gotQuery, "14d") || strings.Contains(gotQuery, "7d") {
		t.Errorf("query %q leaked relative durations", gotQuery)
	}
	if !strings.Contains(gotQuery, "since=2") || !strings.Contains(gotQuery, "until=2") {
		t.Errorf("query %q missing absolute bounds", gotQuery)
	}
}
