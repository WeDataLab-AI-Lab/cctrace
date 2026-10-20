package syncer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSyncOnceNewFileBodyLimitPersistsBlockWithoutAdvancing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync" {
			w.WriteHeader(413)
		} else {
			w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "projects", "fixture", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"type":"user","timestamp":"2026-10-12T09:00:00Z","sessionId":"fixture","uuid":"fixture","message":{"role":"user","content":"fixture"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "sync-state.json")
	state, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	s := New(dir, "fixture@fixture.invalid", "fixture", state, NewClient(server.URL, "fixture", "dev"), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if state.Files[file] == nil || !state.Files[file].BlockedByBodyLimit || state.GetOffset(file) != 0 {
		t.Fatalf("new blocked file not durably visible: %+v", state.Files[file])
	}
}
