package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The notice asks the operator to raise a server limit. Once the file gets
// through -- the limit was raised, or the run of large records ended -- that
// request is stale, and a warning that outlives its cause teaches the reader to
// ignore the next real one. Measured on dev before this: the notice survived a
// clean sync that recovered the held record.
func TestSyncClearsBodyLimitBlockAfterACleanPass(t *testing.T) {
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var payloads []SyncPayload
	srv := collectPayloads(t, &payloads)
	defer srv.Close()

	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	path := writeSessionAt(t, sessionDir, "s1.jsonl", "s1", time.Now().Add(time.Hour))
	state.SetOffset(path, 0)
	// The state a stalled client carries into the pass that finally succeeds.
	state.Files[path].BlockedByBodyLimit = true
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if len(allRecords(payloads)) == 0 {
		t.Fatal("nothing was sent, so the pass proves nothing about clearing")
	}
	if state.Files[path].BlockedByBodyLimit {
		t.Error("the hold survived a pass in which every batch got through")
	}
}
