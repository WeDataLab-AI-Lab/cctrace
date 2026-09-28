package codexsyncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cctrace/internal/syncer"
)

// newReenrichTestServer reports reenrich support and captures every POST
// /api/sync body sent to it.
func newReenrichTestServer(t *testing.T) (*httptest.Server, *[]map[string]interface{}) {
	t.Helper()
	var captured []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"reenrich": true})
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		captured = append(captured, body)
		_ = json.NewEncoder(w).Encode(map[string]int{"updated": 1})
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

// A reenriched record is attributed the same way a live sync attributes one:
// by its own timestamp against the home's observation history. A record
// written before the home's account was ever observed must stay unstamped
// rather than inheriting an account observed only later.
func TestCodexSyncer_ReenrichOnce_StampsAccountIDAtRecordTimestamp(t *testing.T) {
	srv, captured := newReenrichTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	home := t.TempDir()
	observedAt := time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC)
	state.ObserveCodexAccount(observedAt, absHome(t, home), "acct-a")

	writeRollout(t, home, "rollout-2026-04-23T09-00-00-"+uuidA+".jsonl",
		rolloutLinesAt("2026-04-23T09:00:12.000Z"))
	writeRollout(t, home, "rollout-2026-04-23T13-00-00-"+uuidB+".jsonl",
		rolloutLinesAt("2026-04-23T13:00:12.000Z"))

	cs := New([]string{home}, testProfileEmail, "uid-001", state, client, nil)
	if _, err := cs.ReenrichOnce(context.Background()); err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}

	got := sentAccountIDBySession(t, *captured)
	if got[uuidA] != "" {
		t.Fatalf("record predating observation account = %q, want empty (all = %v)", got[uuidA], got)
	}
	if got[uuidB] != "acct-a" {
		t.Fatalf("record after observation account = %q, want acct-a (all = %v)", got[uuidB], got)
	}
}
