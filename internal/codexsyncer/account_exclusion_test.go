package codexsyncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"cctrace/internal/syncer"
)

// exclusionServer answers /api/sync/exclusions from excluded (provider:id keys)
// and records each /api/sync body.
func exclusionServer(t *testing.T, excluded map[string]bool) (*httptest.Server, *[]map[string]interface{}) {
	t.Helper()
	var captured []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/sync/exclusions" {
			var q struct {
				Accounts []syncer.AccountRef `json:"accounts"`
			}
			_ = json.NewDecoder(r.Body).Decode(&q)
			out := []syncer.AccountRef{}
			for _, a := range q.Accounts {
				if excluded[a.Key()] {
					out = append(out, a)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": out})
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/api/sync" {
			captured = append(captured, body)
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func assertConsumed(t *testing.T, state *syncer.State, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.GetOffset(path); got != fi.Size() {
		t.Fatalf("%s offset = %d, want %d: dropped records must be consumed, not retried", path, got, fi.Size())
	}
}

// #716: a Codex account listed in options.exclude_accounts is not sent, and its
// rollout is consumed so the next pass does not read it again.
func TestCodexSyncer_DropsLocallyExcludedAccount(t *testing.T) {
	srv, captured := exclusionServer(t, nil)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")
	client.SetExcludedAccounts([]string{"openai:acct-personal"})

	home := t.TempDir()
	writeCodexAuth(t, home, "acct-personal")
	state.ObserveCodexAccount(time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC), absHome(t, home), "acct-personal")
	path := writeRollout(t, home, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl",
		rolloutLinesAt("2026-04-23T11:30:12.000Z"))
	state.SetOffset(path, 0)

	cs := New([]string{home}, "p@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if ids := sentAccountIDs(t, *captured); len(ids) != 0 {
		t.Fatalf("sent %d records of a locally excluded account", len(ids))
	}
	assertConsumed(t, state, path)
}

// #715 layer 3: the server's answer drops the excluded account's records and
// leaves the rest alone -- a record written before the account was first
// observed carries no account and is still sent.
func TestCodexSyncer_DropsServerExcludedAccountOnly(t *testing.T) {
	srv, captured := exclusionServer(t, map[string]bool{"openai:acct-personal": true})
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	home := t.TempDir()
	writeCodexAuth(t, home, "acct-personal")
	state.ObserveCodexAccount(time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC), absHome(t, home), "acct-personal")
	excluded := writeRollout(t, home, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl",
		rolloutLinesAt("2026-04-23T11:30:12.000Z"))
	unattributed := writeRollout(t, home, "rollout-2026-04-23T09-00-00-bbbbbbbb-0000-0000-0000-00000000000b.jsonl",
		rolloutLinesAt("2026-04-23T09:00:12.000Z"))
	state.SetOffset(excluded, 0)
	state.SetOffset(unattributed, 0)

	cs := New([]string{home}, "p@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	ids := sentAccountIDs(t, *captured)
	if len(ids) == 0 {
		t.Fatal("the unattributed rollout was dropped along with the excluded one")
	}
	for _, id := range ids {
		if id != "" {
			t.Fatalf("sent a record of account %q, which the server excludes", id)
		}
	}
	assertConsumed(t, state, excluded)
	assertConsumed(t, state, unattributed)
	if _, ok := state.ServerExcludedAccounts["openai:acct-personal"]; !ok {
		t.Errorf("state does not record the server's exclusion: %v", state.ServerExcludedAccounts)
	}
}
