package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A client clock a year ahead must not pin a session's rows for that year. The
// API skips such a report rather than storing it, so the next report from a
// corrected clock is the one the tables hold. Run through the real handler and
// Postgres, because the property is what ends up in the row.
func TestSessionObservations_clockAheadDoesNotPinRows(t *testing.T) {
	s := acquireSyncStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE session_snapshots, session_live_states, session_observation_events, collector_heartbeats CASCADE`); err != nil {
		t.Fatal(err)
	}
	ts, _ := newSyncServer(t, s)
	defer ts.Close()

	post := func(at time.Time, title, status string) map[string]int {
		t.Helper()
		when := at.UTC().Format(time.RFC3339Nano)
		body := fmt.Sprintf(`{"profile_email":"p@example.com","user_id":"u-1",
			"snapshots":[{"session_id":"s-clock","project_hash":"ph","observed_at":%q,"snapshot":{"t":%q}}],
			"live_states":[{"session_id":"s-clock","project_hash":"ph","pid":1,"proc_start":"p","status":%q,"heartbeat_at":%q}]}`,
			when, title, status, when)
		resp, err := http.Post(ts.URL+"/api/session-observations", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var out map[string]int
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if got := post(time.Now().AddDate(1, 0, 0), "future", "busy"); got["skipped"] != 2 || got["stored"] != 0 {
		t.Fatalf("a year ahead: response %v, want both items skipped", got)
	}
	if got := post(time.Now(), "correct", "idle"); got["stored"] != 2 {
		t.Fatalf("the corrected report: response %v, want both items stored", got)
	}
	var title, status string
	if err := s.Pool().QueryRow(ctx, `SELECT s.snapshot->>'t', l.status
		FROM session_snapshots s JOIN session_live_states l USING (session_id)
		WHERE s.session_id = 's-clock'`).Scan(&title, &status); err != nil {
		t.Fatal(err)
	}
	if title != "correct" || status != "idle" {
		t.Errorf("rows hold %q/%q, want the corrected report's", title, status)
	}
}
