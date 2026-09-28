package codexsyncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/syncer"
)

func writeCodexAuth(t *testing.T, home, accountID string) {
	t.Helper()
	body := `{"OPENAI_API_KEY":null,"tokens":{"access_token":"secret","account_id":"` + accountID + `"}}`
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
}

func rolloutLinesAt(ts string) []string {
	return []string{
		`{"type":"session_meta","timestamp":"` + ts + `","payload":{"cwd":"/Users/alice/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"` + ts + `","payload":{"cwd":"/Users/alice/myproject","model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"` + ts + `","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	}
}

// sentAccountIDs returns the account_id of every record in every captured sync
// request, in send order. A record that carries none yields "".
func sentAccountIDs(t *testing.T, captured []map[string]interface{}) []string {
	t.Helper()
	var out []string
	for _, body := range captured {
		records, _ := body["records"].([]interface{})
		for _, r := range records {
			rec, _ := r.(map[string]interface{})
			id, _ := rec["account_id"].(string)
			out = append(out, id)
		}
	}
	return out
}

// sentAccountIDBySession maps each sent record's session id to its account id.
// Records from different Codex homes are told apart by the session uuid in
// their rollout filename.
func sentAccountIDBySession(t *testing.T, captured []map[string]interface{}) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, body := range captured {
		records, _ := body["records"].([]interface{})
		for _, r := range records {
			rec, _ := r.(map[string]interface{})
			sid, _ := rec["session_id"].(string)
			id, _ := rec["account_id"].(string)
			out[sid] = id
		}
	}
	return out
}

// The pass reads the account id once and logs it as an observation. Records
// that predate that first observation stay unattributed: the account that was
// active back then was never seen, and today's is a guess.
func TestCodexSyncer_SyncOnce_ObservesAccountAndLeavesOlderRecordsUnstamped(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	home := t.TempDir()
	writeCodexAuth(t, home, "acct-now")
	path := writeRollout(t, home, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl",
		rolloutLinesAt("2026-04-23T11:30:12.000Z"))
	state.SetOffset(path, 0)

	cs := New([]string{home}, "user@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	if len(state.CodexAccountObservations) != 1 || state.CodexAccountObservations[0].AccountID != "acct-now" {
		t.Fatalf("observations = %+v, want one acct-now entry", state.CodexAccountObservations)
	}
	ids := sentAccountIDs(t, *captured)
	if len(ids) == 0 {
		t.Fatal("no records were sent")
	}
	for _, id := range ids {
		if id != "" {
			t.Fatalf("record predating the first observation was stamped %q", id)
		}
	}
}

func TestCodexSyncer_SyncOnce_StampsRecordsAfterObservation(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	home := t.TempDir()
	writeCodexAuth(t, home, "acct-a")
	state.ObserveCodexAccount(time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC), absHome(t, home), "acct-a")

	newer := writeRollout(t, home, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl",
		rolloutLinesAt("2026-04-23T11:30:12.000Z"))
	older := writeRollout(t, home, "rollout-2026-04-23T09-00-00-bbbbbbbb-0000-0000-0000-00000000000b.jsonl",
		rolloutLinesAt("2026-04-23T09:00:12.000Z"))
	state.SetOffset(newer, 0)
	state.SetOffset(older, 0)

	cs := New([]string{home}, "user@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	// Re-reading the same id is a token refresh, not a switch: no new entry.
	if len(state.CodexAccountObservations) != 1 {
		t.Fatalf("observations = %+v, want 1", state.CodexAccountObservations)
	}
	ids := sentAccountIDs(t, *captured)
	if len(ids) != 2 {
		t.Fatalf("sent account ids = %v, want 2 records", ids)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["acct-a"] || !got[""] {
		t.Fatalf("sent account ids = %v, want one acct-a and one empty", ids)
	}
}

// After an account switch each record must land on the account that was active
// at its own timestamp, not on the newest one.
func TestCodexSyncer_SyncOnce_StampsEachSideOfAnAccountSwitch(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	home := t.TempDir()
	writeCodexAuth(t, home, "acct-b")
	state.ObserveCodexAccount(time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC), absHome(t, home), "acct-a")
	state.ObserveCodexAccount(time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC), absHome(t, home), "acct-b")

	first := writeRollout(t, home, "rollout-2026-04-23T11-00-00-aaaaaaaa-0000-0000-0000-00000000000a.jsonl",
		rolloutLinesAt("2026-04-23T11:00:12.000Z"))
	second := writeRollout(t, home, "rollout-2026-04-23T13-00-00-bbbbbbbb-0000-0000-0000-00000000000b.jsonl",
		rolloutLinesAt("2026-04-23T13:00:12.000Z"))
	state.SetOffset(first, 0)
	state.SetOffset(second, 0)

	cs := New([]string{home}, "user@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	ids := sentAccountIDs(t, *captured)
	if len(ids) != 2 {
		t.Fatalf("sent account ids = %v, want 2 records", ids)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["acct-a"] || !got["acct-b"] {
		t.Fatalf("sent account ids = %v, want one acct-a and one acct-b", ids)
	}
}

// absHome mirrors how the syncer normalizes a configured Codex home before it
// keys observations by it.
func absHome(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs %s: %v", dir, err)
	}
	return abs
}

const (
	uuidA = "00000000-0000-0000-0000-000000000001"
	uuidB = "00000000-0000-0000-0000-000000000002"
	// testProfileEmail matches the value the other tests in this package pass.
	testProfileEmail = "user@example.com"
)

// Every configured home is observed, not just the first one that happens to be
// logged in: a second home is normally a second account, and stopping at the
// first would leave it unobservable.
func TestCodexSyncer_SyncOnce_ObservesEveryHome(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	homeA, homeB := t.TempDir(), t.TempDir()
	writeCodexAuth(t, homeA, "acct-a")
	writeCodexAuth(t, homeB, "acct-b")

	cs := New([]string{homeA, homeB}, testProfileEmail, "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	got := map[string]string{}
	for _, obs := range state.CodexAccountObservations {
		got[obs.Home] = obs.AccountID
	}
	if got[absHome(t, homeA)] != "acct-a" || got[absHome(t, homeB)] != "acct-b" {
		t.Fatalf("observations = %+v, want acct-a for %s and acct-b for %s",
			state.CodexAccountObservations, homeA, homeB)
	}
}

// A record is billed to the account of the home its own file lives in. Two
// homes with different accounts must not bleed into each other.
func TestCodexSyncer_SyncOnce_StampsPerHomeAccount(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	homeA, homeB := t.TempDir(), t.TempDir()
	writeCodexAuth(t, homeA, "acct-a")
	writeCodexAuth(t, homeB, "acct-b")
	t0 := time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC)
	state.ObserveCodexAccount(t0, absHome(t, homeA), "acct-a")
	state.ObserveCodexAccount(t0, absHome(t, homeB), "acct-b")

	fileA := writeRollout(t, homeA, "rollout-2026-04-23T11-30-10-"+uuidA+".jsonl",
		rolloutLinesAt("2026-04-23T11:30:12.000Z"))
	fileB := writeRollout(t, homeB, "rollout-2026-04-23T11-40-10-"+uuidB+".jsonl",
		rolloutLinesAt("2026-04-23T11:40:12.000Z"))
	state.SetOffset(fileA, 0)
	state.SetOffset(fileB, 0)

	cs := New([]string{homeA, homeB}, testProfileEmail, "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	got := sentAccountIDBySession(t, *captured)
	if got[uuidA] != "acct-a" {
		t.Fatalf("home A record account = %q, want acct-a (all = %v)", got[uuidA], got)
	}
	if got[uuidB] != "acct-b" {
		t.Fatalf("home B record account = %q, want acct-b (all = %v)", got[uuidB], got)
	}
}

// A home with no auth.json has no observed account. Its records stay empty
// rather than inheriting the account of the home that does have one.
func TestCodexSyncer_SyncOnce_UnauthenticatedHomeStaysUnstamped(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	homeA, homeB := t.TempDir(), t.TempDir()
	writeCodexAuth(t, homeA, "acct-a")
	state.ObserveCodexAccount(time.Date(2026, 4, 23, 11, 0, 0, 0, time.UTC), absHome(t, homeA), "acct-a")

	fileA := writeRollout(t, homeA, "rollout-2026-04-23T11-30-10-"+uuidA+".jsonl",
		rolloutLinesAt("2026-04-23T11:30:12.000Z"))
	fileB := writeRollout(t, homeB, "rollout-2026-04-23T11-40-10-"+uuidB+".jsonl",
		rolloutLinesAt("2026-04-23T11:40:12.000Z"))
	state.SetOffset(fileA, 0)
	state.SetOffset(fileB, 0)

	cs := New([]string{homeA, homeB}, testProfileEmail, "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	got := sentAccountIDBySession(t, *captured)
	if got[uuidA] != "acct-a" {
		t.Fatalf("home A record account = %q, want acct-a (all = %v)", got[uuidA], got)
	}
	if got[uuidB] != "" {
		t.Fatalf("unauthenticated home record account = %q, want empty (all = %v)", got[uuidB], got)
	}
}
