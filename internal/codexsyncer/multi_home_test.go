package codexsyncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/syncer"
)

// writeRollout creates <home>/sessions/<name> containing lines and returns its path.
func writeRollout(t *testing.T, home, name string, lines []string) string {
	t.Helper()
	sessDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	return writeCodexFixture(t, sessDir, name, lines)
}

func rolloutLines() []string {
	return []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/Users/alice/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/Users/alice/myproject","model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	}
}

func TestCodexSyncer_findAllFiles_UnionsHomesAndDeduplicates(t *testing.T) {
	homeA := t.TempDir()
	homeB := t.TempDir()
	fileA := writeRollout(t, homeA, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl", rolloutLines())
	fileB := writeRollout(t, homeB, "rollout-2026-04-23T12-00-00-bbbbbbbb-0000-0000-0000-00000000000b.jsonl", rolloutLines())

	cs := New([]string{homeA, homeB, homeA}, "user@example.com", "uid-001", newTestState(t), nil, nil)
	files, err := cs.findAllFiles()
	if err != nil {
		t.Fatalf("findAllFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("findAllFiles = %v, want 2 files", files)
	}
	found := map[string]bool{}
	for _, f := range files {
		found[f] = true
	}
	for _, want := range []string{fileA, fileB} {
		abs, _ := filepath.Abs(want)
		if !found[abs] {
			t.Fatalf("findAllFiles = %v, missing %s", files, abs)
		}
	}
}

func TestCodexSyncer_findAllFiles_SkipsMissingHome(t *testing.T) {
	homeA := t.TempDir()
	fileA := writeRollout(t, homeA, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl", rolloutLines())
	missing := filepath.Join(t.TempDir(), "gone")

	cs := New([]string{missing, homeA}, "user@example.com", "uid-001", newTestState(t), nil, nil)
	files, err := cs.findAllFiles()
	if err != nil {
		t.Fatalf("findAllFiles: %v", err)
	}
	abs, _ := filepath.Abs(fileA)
	if len(files) != 1 || files[0] != abs {
		t.Fatalf("findAllFiles = %v, want [%s]", files, abs)
	}
}

func TestCodexSyncer_SyncOnce_CollectsFromEveryHome(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)

	homeA := t.TempDir()
	homeB := t.TempDir()
	pathA := writeRollout(t, homeA, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl", rolloutLines())
	pathB := writeRollout(t, homeB, "rollout-2026-04-23T12-00-00-bbbbbbbb-0000-0000-0000-00000000000b.jsonl", rolloutLines())
	// Pre-seed offsets so the pass is not treated as a first run (which skips to EOF).
	state.SetOffset(pathA, 0)
	state.SetOffset(pathB, 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	cs := New([]string{homeA, homeB}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("synced records = %d, want 2 (one per home)", n)
	}
	if len(*captured) != 2 {
		t.Fatalf("sync requests = %d, want 2", len(*captured))
	}
}

func TestCodexSyncer_ReenrichOnce_CollectsFromEveryHome(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"reenrich": true})
			return
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 0, "updated": 1})
	}))
	defer srv.Close()

	homeA := t.TempDir()
	homeB := t.TempDir()
	writeRollout(t, homeA, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl", rolloutLines())
	writeRollout(t, homeB, "rollout-2026-04-23T12-00-00-bbbbbbbb-0000-0000-0000-00000000000b.jsonl", rolloutLines())

	cs := New([]string{homeA, homeB}, "user@example.com", "uid-001", newTestState(t), syncer.NewClient(srv.URL, "", ""), nil)
	n, err := cs.ReenrichOnce(context.Background())
	if err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("reenriched records = %d, want 2 (one per home)", n)
	}
	if requests != 2 {
		t.Fatalf("reenrich requests = %d, want 2", requests)
	}
}
