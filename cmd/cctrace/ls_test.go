package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `cctrace ls` lists sessions through the public contract, the same one external
// scripts read. It is a separate command from `cctrace sessions` rather than a
// replacement: that one returns raw session records, including the transcript, and
// the open API deliberately carries no transcript at all. Moving it would have been
// a quiet feature removal for anyone piping its --json.

func TestLsReadsOpenAPIAndSendsWindow(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if err := runLs(srv.URL, "tok", 24*3600*1e9, "", 20, true, nil); err != nil {
		t.Fatalf("runLs: %v", err)
	}
	if gotPath != "/api/open/v1/sessions" {
		t.Errorf("path = %q, want the open API", gotPath)
	}
	for _, want := range []string{"since=2", "until=2", "limit=20"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
}

// The table has to show cost. A session list that reports only tokens repeats the
// gap the usage endpoint shipped with. The server answers with a bare array, as
// handleOpenAPIListSessions writes it.
func TestLsTableShowsCost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"session_id":"abc123","agent":"claude","model":"sonnet","cost_usd":1.2345,"input_tokens":10,"output_tokens":2,"event_count":7}]`))
	}))
	defer srv.Close()

	var out strings.Builder
	if err := runLs(srv.URL, "tok", 24*3600*1e9, "", 20, false, &out); err != nil {
		t.Fatalf("runLs: %v", err)
	}
	got := out.String()
	for _, want := range []string{"abc123", "1.2345", "COST"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "raise --limit") {
		t.Errorf("hinted at more rows when the page was not full:\n%s", got)
	}
}

// The array carries no total, so a full page is the only sign more rows exist.
func TestLsHintsWhenPageIsFull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"session_id":"a"},{"session_id":"b"}]`))
	}))
	defer srv.Close()

	var out strings.Builder
	if err := runLs(srv.URL, "tok", 24*3600*1e9, "", 2, false, &out); err != nil {
		t.Fatalf("runLs: %v", err)
	}
	if !strings.Contains(out.String(), "raise --limit") {
		t.Errorf("full page without a hint:\n%s", out.String())
	}
}

func TestLsSurfacesAPIRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"ingestion_token","error":"this token is issued for ingestion; create a token under Settings > API Access Tokens to read the API"}`))
	}))
	defer srv.Close()

	err := runLs(srv.URL, "cli-token", 24*3600*1e9, "", 20, true, nil)
	if err == nil || !strings.Contains(err.Error(), "cctrace auth read") {
		t.Fatalf("error = %v, want the command that creates a read token", err)
	}
	if strings.Contains(err.Error(), "cli-token") {
		t.Fatalf("error echoes the token: %v", err)
	}
}

// guide-open-api.md documents `ls --project HASH`; the flag has to reach the
// project_hash filter the endpoint accepts.
func TestLsSendsProjectFilter(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if err := runLs(srv.URL, "tok", 24*3600*1e9, "-users-me-repo", 20, true, nil); err != nil {
		t.Fatalf("runLs: %v", err)
	}
	if !strings.Contains(gotQuery, "project_hash=-users-me-repo") {
		t.Errorf("query %q missing project_hash", gotQuery)
	}
	if err := runLs(srv.URL, "tok", 24*3600*1e9, "", 20, true, nil); err != nil {
		t.Fatalf("runLs: %v", err)
	}
	if strings.Contains(gotQuery, "project_hash") {
		t.Errorf("empty --project sent a filter: %q", gotQuery)
	}
}

// The store turns limit<=0 into its own default, so "--limit 0" silently listed
// up to 200 rows with no hint that more existed.
func TestLsAndEventsRejectNonPositiveLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request sent with an invalid limit: %s", r.URL.RawQuery)
	}))
	defer srv.Close()

	if err := runLs(srv.URL, "tok", 24*3600*1e9, "", 0, false, nil); err == nil || !strings.Contains(err.Error(), "--limit") {
		t.Errorf("ls err = %v", err)
	}
	if err := runEvents(srv.URL, "tok", "ses-1", -1, false, nil); err == nil || !strings.Contains(err.Error(), "--limit") {
		t.Errorf("events err = %v", err)
	}
}
