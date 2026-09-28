package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// exclusionServer records /api/sync envelopes and every exclusion query, and
// answers the query with whichever asked accounts are in excluded. A nil
// excluded map makes the server an older one that has no such route.
func exclusionServer(t *testing.T, excluded map[string]bool, payloads *[]SyncPayload, queries *[][]AccountRef) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/sync/exclusions":
			if excluded == nil {
				http.NotFound(w, r)
				return
			}
			var q struct {
				Accounts []AccountRef `json:"accounts"`
			}
			_ = json.Unmarshal(body, &q)
			*queries = append(*queries, q.Accounts)
			out := []AccountRef{}
			for _, a := range q.Accounts {
				if excluded[a.BillingProvider+":"+a.AccountID] {
					out = append(out, a)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": out})
		case "/api/sync":
			var p SyncPayload
			if err := json.Unmarshal(body, &p); err == nil {
				*payloads = append(*payloads, p)
			}
			_ = json.NewEncoder(w).Encode(syncResponse{Inserted: len(p.Records)})
		default:
			_ = json.NewEncoder(w).Encode(syncResponse{})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func appendRecordAt(t *testing.T, path, sessionID, uuid string, ts time.Time) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	line := `{"type":"user","timestamp":"` + ts.UTC().Format(time.RFC3339) +
		`","sessionId":"` + sessionID + `","uuid":"` + uuid + `","cwd":"/tmp",` +
		`"message":{"role":"user","content":"hello"}}` + "\n"
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func exclusionFixture(t *testing.T, account string) (claudeDir, sessionPath string, state *State) {
	t.Helper()
	claudeDir = t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeClaudeConfig(t, claudeDir, account)
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	// Dated after the pass's own observation, so the records carry the account.
	sessionPath = writeSessionAt(t, sessionDir, "s1.jsonl", "s1", time.Now().Add(time.Hour))
	state.SetOffset(sessionPath, 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return claudeDir, sessionPath, state
}

// #716: records of an account the person listed locally never leave the
// machine. They still move the offset, exactly as sent records do: otherwise
// every pass would read them again forever, and once the person switched back
// to the work account its records would sit behind them.
func TestSyncDropsLocallyExcludedAccountAndMovesOn(t *testing.T) {
	claudeDir, sessionPath, state := exclusionFixture(t, "acct-personal")
	var payloads []SyncPayload
	var queries [][]AccountRef
	srv := exclusionServer(t, map[string]bool{}, &payloads, &queries)

	client := NewClient(srv.URL, "", "")
	client.SetExcludedAccounts([]string{"anthropic:acct-personal"})
	s := New(claudeDir, "p@example.com", "u1", state, client, nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n := len(allRecords(payloads)); n != 0 {
		t.Fatalf("sent %d records of a locally excluded account", n)
	}
	if len(queries) != 0 {
		t.Errorf("asked the server about an account already excluded locally: %v", queries)
	}
	fi, _ := os.Stat(sessionPath)
	if got := state.GetOffset(sessionPath); got != fi.Size() {
		t.Fatalf("offset = %d, want %d: excluded records must be consumed, not retried", got, fi.Size())
	}

	// Back on the work account: only the new record goes, the excluded one is
	// not resent.
	writeClaudeConfig(t, claudeDir, "acct-work")
	appendRecordAt(t, sessionPath, "s1", "s1-u2", time.Now().Add(2*time.Hour))
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	recs := allRecords(payloads)
	if len(recs) != 1 || recs[0].UUID != "s1-u2" || recs[0].AccountID != "acct-work" {
		t.Fatalf("sent %+v, want only the work account's new record", recs)
	}
}

// An entry without a provider matches that id under any provider.
func TestSyncDropsBareAccountIDEntry(t *testing.T) {
	claudeDir, _, state := exclusionFixture(t, "acct-personal")
	var payloads []SyncPayload
	var queries [][]AccountRef
	srv := exclusionServer(t, map[string]bool{}, &payloads, &queries)

	client := NewClient(srv.URL, "", "")
	client.SetExcludedAccounts([]string{"acct-personal"})
	s := New(claudeDir, "p@example.com", "u1", state, client, nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n := len(allRecords(payloads)); n != 0 {
		t.Fatalf("sent %d records of a locally excluded account", n)
	}
}

// #715 layer 3: an account the server excludes is not sent either. The server
// is asked once, about the accounts the pass actually saw, and the
// answer is kept in state so `cctrace status` can say what is being skipped.
func TestSyncDropsServerExcludedAccount(t *testing.T) {
	advance := fakeClock(t)
	claudeDir, sessionPath, state := exclusionFixture(t, "acct-personal")
	sessionDir := filepath.Dir(sessionPath)
	state.SetOffset(writeSessionAt(t, sessionDir, "s2.jsonl", "s2", time.Now().Add(time.Hour)), 0)
	var payloads []SyncPayload
	var queries [][]AccountRef
	excluded := map[string]bool{"anthropic:acct-personal": true}
	srv := exclusionServer(t, excluded, &payloads, &queries)

	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n := len(allRecords(payloads)); n != 0 {
		t.Fatalf("sent %d records of a server-excluded account", n)
	}
	if len(queries) != 1 {
		t.Fatalf("queried %d times over two files, want once", len(queries))
	}
	if len(queries[0]) != 1 || queries[0][0] != (AccountRef{BillingProvider: "anthropic", AccountID: "acct-personal"}) {
		t.Errorf("query = %+v, want only the account the pass saw", queries[0])
	}
	fi, _ := os.Stat(sessionPath)
	if got := state.GetOffset(sessionPath); got != fi.Size() {
		t.Fatalf("offset = %d, want %d", got, fi.Size())
	}
	if _, ok := state.ServerExcludedAccounts["anthropic:acct-personal"]; !ok {
		t.Errorf("server-excluded accounts in state = %v, want anthropic:acct-personal", state.ServerExcludedAccounts)
	}

	// Once the answer expires the server is asked again, so an exclusion lifted
	// there takes effect without restarting the daemon -- and only what arrived
	// since goes.
	delete(excluded, "anthropic:acct-personal")
	advance(exclusionAnswerTTL)
	appendRecordAt(t, sessionPath, "s1", "s1-u2", time.Now().Add(2*time.Hour))
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("queries after two passes = %d, want 2", len(queries))
	}
	recs := allRecords(payloads)
	if len(recs) != 1 || recs[0].UUID != "s1-u2" {
		t.Fatalf("sent %+v after the exclusion was lifted, want only the new record", recs)
	}
	if len(state.ServerExcludedAccounts) != 0 {
		t.Errorf("state still reports %v excluded after the server lifted it", state.ServerExcludedAccounts)
	}
}

// An older server has no exclusion route. That means nothing is excluded
// server-side, not that sync stops.
func TestSyncSendsWhenServerHasNoExclusionRoute(t *testing.T) {
	claudeDir, _, state := exclusionFixture(t, "acct-work")
	var payloads []SyncPayload
	var queries [][]AccountRef
	srv := exclusionServer(t, nil, &payloads, &queries)

	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n := len(allRecords(payloads)); n != 1 {
		t.Fatalf("sent %d records, want 1", n)
	}
}

// A query that fails is not an answer. Sending anyway would put an excluded
// account's conversation on the wire; the file is held and retried instead,
// the way a failed send is.
func TestSyncHoldsFileWhenExclusionQueryFails(t *testing.T) {
	claudeDir, sessionPath, state := exclusionFixture(t, "acct-work")
	var sent int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/exclusions" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		sent++
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	t.Cleanup(srv.Close)

	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if sent != 0 {
		t.Fatalf("sent %d requests without knowing whether the account is excluded", sent)
	}
	if got := state.GetOffset(sessionPath); got != 0 {
		t.Fatalf("offset = %d, want 0: the records must be retried", got)
	}
	if s.LastPass().FilesFailed != 1 {
		t.Errorf("FilesFailed = %d, want 1: a failed query is a failure on the way to the server", s.LastPass().FilesFailed)
	}
}
