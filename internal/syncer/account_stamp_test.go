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

	"cctrace/internal/store"
)

// writeClaudeConfig writes the .claude.json an alternate (non-default) Claude
// home keeps inside itself.
func writeClaudeConfig(t *testing.T, claudeDir, accountUUID string) {
	t.Helper()
	body := `{"oauthAccount":{"accountUuid":"` + accountUUID + `"}}`
	if err := os.WriteFile(filepath.Join(claudeDir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
}

// writeSessionAt writes one Claude JSONL record with a chosen timestamp, so a
// test can place records before or after an observation.
func writeSessionAt(t *testing.T, dir, name, sessionID string, ts time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	line := `{"type":"user","timestamp":"` + ts.UTC().Format(time.RFC3339) +
		`","sessionId":"` + sessionID + `","uuid":"` + sessionID + `-u1","cwd":"/tmp",` +
		`"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	return p
}

// collectPayloads returns a server that records every /api/sync envelope.
func collectPayloads(t *testing.T, out *[]SyncPayload) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/sync" {
			body, _ := io.ReadAll(r.Body)
			var p SyncPayload
			if err := json.Unmarshal(body, &p); err == nil {
				*out = append(*out, p)
			}
		}
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
}

// TestSyncStampsAccountPerRecord is the Claude half of #220: records must carry
// the account that was active when they were written, so a mid-session /login
// splits the session's usage between the two accounts instead of merging it.
func TestSyncStampsAccountPerRecord(t *testing.T) {
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeClaudeConfig(t, claudeDir, "acct-one")

	var payloads []SyncPayload
	srv := collectPayloads(t, &payloads)
	defer srv.Close()

	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	// Records are dated after the pass starts, so they fall inside the interval
	// the pass's own observation opens.
	future := time.Now().Add(time.Hour)
	state.SetOffset(writeSessionAt(t, sessionDir, "s1.jsonl", "s1", future), 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	first := allRecords(payloads)
	if len(first) == 0 {
		t.Fatal("no records sent")
	}
	for _, r := range first {
		if r.AccountID != "acct-one" {
			t.Fatalf("account_id = %q, want acct-one", r.AccountID)
		}
	}

	// The user runs /login. session_id is unchanged; only the account is.
	writeClaudeConfig(t, claudeDir, "acct-two")
	payloads = nil
	state.SetOffset(writeSessionAt(t, sessionDir, "s2.jsonl", "s2", future.Add(time.Hour)), 0)

	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	second := allRecords(payloads)
	if len(second) == 0 {
		t.Fatal("no records sent after the switch")
	}
	for _, r := range second {
		if r.AccountID != "acct-two" {
			t.Fatalf("post-switch account_id = %q, want acct-two", r.AccountID)
		}
	}
}

// Records older than the first observation stay blank. The sync pass knows which
// account is active *now*, not which one wrote a record from before it ever
// looked, and a confident wrong label is worse than an absent one.
func TestSyncLeavesPreObservationRecordsUnstamped(t *testing.T) {
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeClaudeConfig(t, claudeDir, "acct-one")

	var payloads []SyncPayload
	srv := collectPayloads(t, &payloads)
	defer srv.Close()

	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	past := time.Now().Add(-30 * 24 * time.Hour)
	state.SetOffset(writeSessionAt(t, sessionDir, "old.jsonl", "old", past), 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	recs := allRecords(payloads)
	if len(recs) == 0 {
		t.Fatal("no records sent")
	}
	for _, r := range recs {
		if r.AccountID != "" {
			t.Fatalf("account_id = %q, want empty for a record predating the first observation", r.AccountID)
		}
	}
}

// A home that never logged in produces no observation at all, so its records
// stay blank rather than borrowing another home's account.
func TestSyncWithoutConfigLeavesRecordsUnstamped(t *testing.T) {
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No .claude.json written.

	var payloads []SyncPayload
	srv := collectPayloads(t, &payloads)
	defer srv.Close()

	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(writeSessionAt(t, sessionDir, "s1.jsonl", "s1", time.Now().Add(time.Hour)), 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if len(state.ClaudeAccountObservations) != 0 {
		t.Fatalf("observations = %d, want 0 for a home that never logged in", len(state.ClaudeAccountObservations))
	}
	for _, r := range allRecords(payloads) {
		if r.AccountID != "" {
			t.Fatalf("account_id = %q, want empty", r.AccountID)
		}
	}
}

// allRecords flattens every record across the captured payloads.
func allRecords(payloads []SyncPayload) []*store.SessionRecord {
	var out []*store.SessionRecord
	for _, p := range payloads {
		out = append(out, p.Records...)
	}
	return out
}
