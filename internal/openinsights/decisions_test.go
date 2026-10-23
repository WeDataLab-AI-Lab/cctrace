package openinsights

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// Only the capability flag says yes. A missing route is an older server; a
// refused upload token (none, revoked, or a read token in its place) means
// support cannot be asked about, which is its own answer.
func TestClientDecisionSourceSupportedOnlyFromTheFlag(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		body    string
		want    bool
		refused bool
	}{
		"flag":            {http.StatusOK, `{"reenrich":true,"session_observations":true,"open_api_decision_source":true}`, true, false},
		"no flag":         {http.StatusOK, `{"reenrich":true,"session_observations":true}`, false, false},
		"flag false":      {http.StatusOK, `{"open_api_decision_source":false}`, false, false},
		"no upload token": {http.StatusUnauthorized, `{"error":"unauthorized"}`, false, true},
		"read token":      {http.StatusForbidden, `{"code":"read_only_token","error":"read-only token"}`, false, true},
		"proxy 403":       {http.StatusForbidden, `<html>blocked</html>`, false, false},
		"route missing":   {http.StatusNotFound, `404 page not found`, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/sync/capabilities" || r.Header.Get("Authorization") != "Bearer read-token" {
					t.Errorf("request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := c.DecisionSourceSupported(context.Background())
			if got != tc.want || errors.Is(err, ErrUploadTokenRefused) != tc.refused || err != nil && !tc.refused {
				t.Fatalf("DecisionSourceSupported = %v, %v; want %v, refused %v", got, err, tc.want, tc.refused)
			}
		})
	}
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	if _, err := c.DecisionSourceSupported(context.Background()); err == nil || errors.Is(err, ErrUploadTokenRefused) {
		t.Fatal("a server error was read as an answer")
	}
}

// The request names the decision event and sources; a row repeated across a
// page boundary, or under another session, is the same tool_use_id and is kept
// once, as the merge identifies a call by it; rows
// without a tool_use_id are kept apart, and a row that is not such a decision
// is dropped whatever the server sent.
func TestClientDecisionEventsFiltersAndDedupes(t *testing.T) {
	var queries []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/open/v1/events" {
			t.Errorf("path %s", r.URL.Path)
		}
		queries = append(queries, r.URL.RawQuery)
		page := `[{"event_name":"tool_decision","session_id":"s1","tool_use_id":"toolu_a","decision_source":"user_temporary","tool_name":"Bash","ts":"2026-09-10T00:00:00Z"},
			{"event_name":"tool_decision","session_id":"s1","tool_use_id":"","decision_source":"hook","tool_name":"Read","ts":"2026-09-10T00:00:01Z"},
			{"event_name":"tool_decision","session_id":"s2","tool_use_id":"toolu_b","decision_source":"config","tool_name":"Bash","ts":"2026-09-10T00:00:03Z"},
			{"event_name":"api_request","session_id":"s2","ts":"2026-09-10T00:00:04Z"}]`
		if r.URL.Query().Get("offset") == "4" {
			page = `[{"event_name":"tool_decision","session_id":"s1","tool_use_id":"toolu_a","decision_source":"user_temporary","tool_name":"Bash","ts":"2026-09-10T00:00:00Z"},
				{"event_name":"tool_decision","session_id":"s9","tool_use_id":"toolu_a","decision_source":"user_temporary","tool_name":"Bash","ts":"2026-09-10T00:00:00Z"},
				{"event_name":"tool_decision","session_id":"s2","tool_use_id":"","decision_source":"hook","tool_name":"Read","ts":"2026-09-10T00:00:02Z"}]`
		}
		_, _ = w.Write([]byte(page))
	})
	c.pageSize = 4
	got, truncated, err := c.DecisionEvents(context.Background(), Window{t1, t2}, []string{"user_temporary", "hook"})
	if err != nil || truncated {
		t.Fatal(err, truncated)
	}
	want := "decision_source=user_temporary%2Chook&event_name=tool_decision&limit=4&offset=0&since=2026-09-08T00%3A00%3A00Z&until=2026-09-15T00%3A00%3A00Z"
	if len(queries) != 2 || queries[0] != want {
		t.Fatalf("queries = %v", queries)
	}
	raw, _ := json.Marshal(got)
	if len(got) != 3 || got[0].ToolUseID != "toolu_a" || got[1].SessionID != "s1" || got[2].SessionID != "s2" {
		t.Fatalf("decisions = %s", raw)
	}
}

func TestClientPracticesDecodesTheComparison(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/open/v1/practices" || r.URL.RawQuery != "" {
			t.Errorf("request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"window_start":"2026-08-16T00:00:00Z","window_end":"2026-09-15T00:00:00Z",
			"rows":[{"kind":"skill","name":"review","users_with":4,"users_without":6,"metrics":{"manual_approvals_per_1000":{"with":10,"without":30},"human_rejections_per_1000":{"with":1,"without":2}}},{"kind":"plugin","name":"rare","users_with":1}],
			"self":{"manual_approvals_per_1000":12.5,"human_rejections_per_1000":null},"caveats":["correlation only"]}`))
	})
	got, err := c.Practices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.WindowEnd.Equal(t2) || len(got.Rows) != 2 || got.Rows[0].Metrics == nil || got.Rows[0].Metrics.ManualApprovalsPer1000.Without != 30 ||
		*got.Rows[0].UsersWithout != 6 || got.Rows[1].Metrics != nil || got.Rows[1].UsersWithout != nil ||
		*got.Self.ManualApprovalsPer1000 != 12.5 || got.Self.HumanRejectionsPer1000 != nil || len(got.Caveats) != 1 {
		raw, _ := json.Marshal(got)
		t.Fatalf("practices = %s", raw)
	}
}
