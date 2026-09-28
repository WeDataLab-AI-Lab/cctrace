package openinsights

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL+"/", "read-token")
	var slept []time.Duration
	c.wait = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return c, &slept
}

func TestClientEventsPagesBareArraysWithFixedWindow(t *testing.T) {
	var queries []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/open/v1/events" || r.Header.Get("Authorization") != "Bearer read-token" {
			t.Errorf("request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		queries = append(queries, r.URL.RawQuery)
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		n := 0
		switch offset {
		case 0, 2:
			n = 2
		case 4:
			n = 1
		}
		page := make([]Event, n)
		for i := range page {
			page[i] = Event{SessionID: fmt.Sprint("s", offset+i)}
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	c.pageSize = 2

	got, truncated, err := c.Events(context.Background(), Window{t1, t2}, "-proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || truncated {
		t.Fatalf("got %d rows truncated=%v", len(got), truncated)
	}
	want := "limit=2&offset=0&project_hash=-proj&since=2026-09-08T00%3A00%3A00Z&until=2026-09-15T00%3A00%3A00Z"
	if len(queries) != 3 || queries[0] != want {
		t.Fatalf("queries = %v", queries)
	}
}

func TestClientStopsAtRowCap(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		o := r.URL.Query().Get("offset")
		_ = json.NewEncoder(w).Encode([]Session{{SessionID: "a" + o}, {SessionID: "b" + o}})
	})
	c.pageSize, c.maxRows = 2, 3

	got, truncated, err := c.Sessions(context.Background(), Window{t1, t2}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !truncated {
		t.Fatalf("got %d rows truncated=%v", len(got), truncated)
	}
}

func TestClientRetriesRateLimitWithRetryAfter(t *testing.T) {
	var calls atomic.Int32
	c, slept := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.Header().Set("Retry-After", "999")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	})

	if _, _, err := c.Sessions(context.Background(), Window{t1, t2}, ""); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 2 || (*slept)[0] != 2*time.Second || (*slept)[1] != maxRetryAfter {
		t.Fatalf("slept %v", *slept)
	}
}

func TestClientGivesUpAfterRepeatedRateLimits(t *testing.T) {
	c, slept := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limit exceeded"}`))
	})

	_, _, err := c.Sessions(context.Background(), Window{t1, t2}, "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v", err)
	}
	if len(*slept) != maxRetries || (*slept)[0] != time.Second {
		t.Fatalf("slept %v", *slept)
	}
}

func TestClientSurfacesIngestionTokenCode(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"this token is issued for ingestion","code":"ingestion_token"}`))
	})

	_, err := c.Projects(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "ingestion_token" || apiErr.Status != 401 {
		t.Fatalf("err = %#v", err)
	}
}

func TestClientProjectsReadsItems(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/open/v1/projects" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"items":[{"project_hash":"-a","project_name":"web"}],"total":1}`))
	})

	got, err := c.Projects(context.Background())
	if err != nil || len(got) != 1 || got[0].ProjectName != "web" || got[0].ProjectHash != "-a" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestClientModelCostsReadsGroupedUsage(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/open/v1/usage" || r.URL.Query().Get("group_by") != "model" || r.URL.Query().Get("until") == "" {
			t.Errorf("request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"items":[{"key":"opus","cost_usd":1.5,"input_tokens":3}],"total":1}`))
	})

	got, err := c.ModelCosts(context.Background(), Window{t1, t2})
	if err != nil || len(got) != 1 || got[0].Model != "opus" || got[0].CostUSD != 1.5 {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestClientTransportErrorsOmitTheQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing listens: the request fails before any response
	c := NewClient(srv.URL, "tok")

	_, _, err := c.Sessions(context.Background(), Window{t1, t2}, "-users-alice-secret-repo")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "alice") || strings.Contains(err.Error(), "?") {
		t.Fatalf("error leaks the query: %v", err)
	}
	if !strings.Contains(err.Error(), "/api/open/v1/sessions") {
		t.Fatalf("error lost the path: %v", err)
	}
}

func TestClientDropsRowsRepeatedByOffsetDrift(t *testing.T) {
	// A row synced mid-download shifts later pages by one, so the first row of
	// page two repeats the last row of page one.
	pages := map[string]string{
		"0": `[{"session_id":"a","cost_usd":1},{"session_id":"b","cost_usd":2}]`,
		"2": `[{"session_id":"b","cost_usd":2},{"session_id":"c","cost_usd":3}]`,
		"4": `[]`,
	}
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(pages[r.URL.Query().Get("offset")]))
	})
	c.pageSize = 2

	got, _, err := c.Sessions(context.Background(), Window{t1, t2}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3: %+v", len(got), got)
	}

	evPages := map[string]string{
		"0": `[{"ts":"2026-09-10T00:00:02Z","session_id":"s","input_tokens":1},{"ts":"2026-09-10T00:00:01Z","session_id":"s","input_tokens":1}]`,
		"2": `[{"ts":"2026-09-10T00:00:01Z","session_id":"s","input_tokens":1}]`,
	}
	c2, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(evPages[r.URL.Query().Get("offset")]))
	})
	c2.pageSize = 2
	events, _, err := c2.Events(context.Background(), Window{t1, t2}, "")
	if err != nil || len(events) != 2 {
		t.Fatalf("got %d events err %v, want 2", len(events), err)
	}
}

func TestClientRowCountEqualToCapIsNotTruncated(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			_, _ = w.Write([]byte(`[{"session_id":"a"},{"session_id":"b"}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})
	c.pageSize, c.maxRows = 2, 2

	got, truncated, err := c.Sessions(context.Background(), Window{t1, t2}, "")
	if err != nil || len(got) != 2 || truncated {
		t.Fatalf("got %d truncated=%v err=%v", len(got), truncated, err)
	}
}

func TestClientRateLimitWaitHonoursCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, err := NewClient(srv.URL, "tok").Sessions(ctx, Window{t1, t2}, "")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestClientFailsWhenAFullPageAddsNothing(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"session_id":"a"},{"session_id":"b"}]`))
	})
	c.pageSize = 2

	done := make(chan error, 1)
	go func() { _, _, err := c.Sessions(context.Background(), Window{t1, t2}, ""); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for a stalled pagination")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pagination did not stop")
	}
}

func TestClientScopeProbesTheAdminOnlyEndpoint(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
		want   Scope
	}{
		"admin":          {http.StatusOK, `{"available":false,"minimum_users":5}`, ScopeAdmin},
		"user":           {http.StatusForbidden, `{"error":"forbidden"}`, ScopeUser},
		"proxy 403":      {http.StatusForbidden, `<html>blocked</html>`, ScopeUnknown},
		"route missing":  {http.StatusNotFound, `404 page not found`, ScopeUnknown},
		"other 403 body": {http.StatusForbidden, `{"error":"feature disabled","code":"disabled"}`, ScopeUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/open/v1/organization-insights" {
					t.Errorf("path %s", r.URL.Path)
				}
				since, _ := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
				until, _ := time.Parse(time.RFC3339, r.URL.Query().Get("until"))
				if d := until.Sub(since); d <= 0 || d > time.Minute {
					t.Errorf("probe window %v, want a tiny window so the aggregate stays cheap", d)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := c.Scope(context.Background())
			if err != nil || got != tc.want {
				t.Fatalf("Scope = %v, %v; want %v", got, err, tc.want)
			}
		})
	}

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"ingestion_token"}`))
	})
	if _, err := c.Scope(context.Background()); err == nil {
		t.Fatal("an auth failure must not read as a scope")
	}
}
