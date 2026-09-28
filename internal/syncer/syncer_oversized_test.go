package syncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/jsonlscan"
)

// TestSyncOnce_PersistsOffsetWhenScanSkipsOversizedLine guards against the
// oversized-line re-drain loop: a scan that skips a >MaxLineBytes line consumes
// bytes but produces no records, and the advanced offset must still be persisted
// or every poll re-reads the same giant line forever.
func TestSyncOnce_PersistsOffsetWhenScanSkipsOversizedLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 0})
	}))
	defer srv.Close()

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	filePath := filepath.Join(sessionDir, "s.jsonl")
	line := make([]byte, jsonlscan.MaxLineBytes+2)
	for i := range line {
		line[i] = 'x'
	}
	line[len(line)-1] = '\n'
	if err := os.WriteFile(filePath, line, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fi, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Existing state (not a first run) already tracking this file at offset 0.
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(filePath, 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s := New(claudeDir, "sync-test@example.invalid", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	if got := state.GetOffset(filePath); got != fi.Size() {
		t.Fatalf("offset after oversized-only scan = %d, want %d (file size)", got, fi.Size())
	}
	// The advanced offset must also survive a daemon restart.
	reloaded, err := LoadState(statePath)
	if err != nil {
		t.Fatalf("LoadState after sync: %v", err)
	}
	if got := reloaded.GetOffset(filePath); got != fi.Size() {
		t.Fatalf("persisted offset = %d, want %d (file size)", got, fi.Size())
	}
}
