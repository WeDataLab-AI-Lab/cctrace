package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cctrace/internal/openinsights"
)

var insightsNow = time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)

func TestInsightsCostComparesAdjacentWindowsAndAttributesProjects(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		paths = append(paths, r.URL.Path)
		if q.Get("project_hash") != "" {
			t.Errorf("cost queried by project hash: %s", r.URL.RawQuery)
		}
		current := q.Get("since") == "2026-09-08T00:00:00Z"
		switch r.URL.Path {
		case "/api/open/v1/organization-insights":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		case "/api/open/v1/usage":
			if current {
				_, _ = w.Write([]byte(`{"items":[{"key":"opus","cost_usd":9},{"key":"sonnet","cost_usd":1}]}`))
			} else {
				_, _ = w.Write([]byte(`{"items":[{"key":"opus","cost_usd":2}]}`))
			}
		case "/api/open/v1/sessions":
			if current {
				_, _ = w.Write([]byte(`[{"session_id":"c1","model":"opus","project_name":"web","cost_usd":9},{"session_id":"c2","model":"opus","cost_usd":1}]`))
			} else {
				_, _ = w.Write([]byte(`[{"session_id":"p1","model":"opus","cost_usd":2}]`))
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	var out strings.Builder
	err := runInsightsCost(t.Context(), openinsights.NewClient(srv.URL, "tok"), insightsNow, 7*24*time.Hour, 5, true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 5 {
		t.Errorf("made %d requests, want 5 (role probe, then sessions and usage per window): %v", len(paths), paths)
	}
	var got openinsights.CostResult
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("output is not a cost result: %v\n%s", err, out.String())
	}
	if len(got.ByModel) != 2 || got.ByModel[0].Key != "opus" || got.ByModel[0].DeltaUSD != 7 {
		t.Fatalf("by_model = %+v", got.ByModel)
	}
	if got.DeltaCostUSD != 8 || got.TopDriver == nil || got.TopDriver.Key != "web" {
		t.Fatalf("delta=%v top=%+v", got.DeltaCostUSD, got.TopDriver)
	}
}

func TestInsightsContextResolvesProjectNameAndRejectsUnknownHash(t *testing.T) {
	var eventsQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/open/v1/organization-insights":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		case "/api/open/v1/projects":
			_, _ = w.Write([]byte(`{"items":[{"project_hash":"-web","project_name":"web"}]}`))
		case "/api/open/v1/events":
			eventsQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`[{"ts":"2026-09-14T00:00:00Z","session_id":"s","input_tokens":10,"cache_read_tokens":90,"cache_create_tokens":0}]`))
		}
	}))
	defer srv.Close()
	client := openinsights.NewClient(srv.URL, "tok")

	var out strings.Builder
	if err := runInsightsContext(t.Context(), client, insightsNow, 24*time.Hour, "-web", 5, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(eventsQuery, "project_hash=-web") || !strings.Contains(eventsQuery, "since=2026-09-14T00") {
		t.Errorf("events query = %q", eventsQuery)
	}
	var got openinsights.ContextResult
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Project != "web" || got.Overall.HitRate == nil || *got.Overall.HitRate != 0.9 {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(out.String(), "-web") {
		t.Errorf("output leaks a project hash: %s", out.String())
	}

	err := runInsightsContext(t.Context(), client, insightsNow, 24*time.Hour, "-nope", 5, true, &out)
	if err == nil || !strings.Contains(err.Error(), "cctrace projects") {
		t.Fatalf("unknown project err = %v", err)
	}
}

func TestInsightsExplainsIngestionToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"this token is issued for ingestion","code":"ingestion_token"}`))
	}))
	defer srv.Close()

	err := runInsightsContext(t.Context(), openinsights.NewClient(srv.URL, "secret-tok"), insightsNow, 24*time.Hour, "", 5, true, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "cctrace auth read") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-tok") {
		t.Fatal("error echoes the token")
	}
}

func TestInsightsMarksTruncatedWindow(t *testing.T) {
	res := openinsights.ContextResult{}
	markContextTruncated(&res, []openinsights.Event{{Ts: insightsNow}, {Ts: insightsNow.Add(-time.Hour)}})
	if !res.Truncated || res.CoveredSince == nil || !res.CoveredSince.Equal(insightsNow.Add(-time.Hour)) {
		t.Fatalf("got %+v", res)
	}
	if len(res.Caveats) != 1 || res.Caveats[0].Code != "truncated" {
		t.Fatalf("caveats %+v", res.Caveats)
	}
}

func TestInsightsContextAcceptsProjectWithoutName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/open/v1/organization-insights":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		case "/api/open/v1/projects":
			_, _ = w.Write([]byte(`{"items":[{"project_hash":"-users-alice-repo"}]}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	var out strings.Builder
	if err := runInsightsContext(t.Context(), openinsights.NewClient(srv.URL, "tok"), insightsNow, time.Hour, "-users-alice-repo", 5, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"project":"(unnamed)"`) || strings.Contains(out.String(), "alice") {
		t.Fatalf("output = %s", out.String())
	}
}

func TestInsightsRejectsNegativeLimit(t *testing.T) {
	cmd := insightsCmd()
	cmd.SetArgs([]string{"context", "--limit", "-1"})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestInsightsWithholdDetailForAdministratorToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/open/v1/organization-insights":
			_, _ = w.Write([]byte(`{"available":false,"minimum_users":5}`))
		case "/api/open/v1/usage":
			_, _ = w.Write([]byte(`{"items":[]}`))
		case "/api/open/v1/sessions":
			_, _ = w.Write([]byte(`[{"session_id":"someone-else","user_id":"u2","project_name":"their-repo","cost_usd":3}]`))
		case "/api/open/v1/events":
			_, _ = w.Write([]byte(`[{"ts":"2026-09-14T00:00:00Z","session_id":"someone-else","user_id":"u2","cache_read_tokens":200000}]`))
		}
	}))
	defer srv.Close()
	client := openinsights.NewClient(srv.URL, "admin-tok")

	var cost, ctx strings.Builder
	if err := runInsightsCost(t.Context(), client, insightsNow, 24*time.Hour, 5, true, &cost); err != nil {
		t.Fatal(err)
	}
	if err := runInsightsContext(t.Context(), client, insightsNow, 24*time.Hour, "", 5, true, &ctx); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"cost": cost.String(), "context": ctx.String()} {
		if strings.Contains(out, "someone-else") || strings.Contains(out, "their-repo") || !strings.Contains(out, "admin_scope") {
			t.Errorf("%s output = %s", name, out)
		}
	}
}
