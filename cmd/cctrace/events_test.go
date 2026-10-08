package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// events is the one command that needs a session id: asking for "all events" over a
// window returns a firehose nobody reads.
func TestEventsRequiresASession(t *testing.T) {
	err := runEvents(http.DefaultClient, "", "tok", "", 50, true, nil)
	if err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("error = %v, want a refusal naming --session", err)
	}
}

func TestEventsSendsSessionFilter(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	if err := runEvents(http.DefaultClient, srv.URL, "tok", "ses-1", 50, true, nil); err != nil {
		t.Fatalf("runEvents: %v", err)
	}
	if gotPath != "/api/open/v1/events" {
		t.Errorf("path = %q, want the open API", gotPath)
	}
	if !strings.Contains(gotQuery, "session_id=ses-1") {
		t.Errorf("query %q missing the session filter", gotQuery)
	}
}

func TestEventsTableReadsBareArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"ts":"2026-09-17T00:00:00Z","event_name":"api_request","model":"opus","cost_usd":0.25,"input_tokens":12}]`))
	}))
	defer srv.Close()

	var out strings.Builder
	if err := runEvents(http.DefaultClient, srv.URL, "tok", "ses-1", 50, false, &out); err != nil {
		t.Fatalf("runEvents: %v", err)
	}
	for _, want := range []string{"api_request", "0.250000", "12"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}
