package codexsyncer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexlog"
	"cctrace/internal/projecthash"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// newTestServer creates an httptest.Server that captures sync requests.
func newTestServer(t *testing.T) (*httptest.Server, *[]map[string]interface{}) {
	t.Helper()
	var captured []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		captured = append(captured, body)
		json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func newTestState(t *testing.T) *syncer.State {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := syncer.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func writeCodexFixture(t *testing.T, dir, filename string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	f.Close()
	return path
}

func TestCodexSyncer_SyncOnce_NoFiles(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	os.MkdirAll(filepath.Join(codexDir, "sessions"), 0755)

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 records, got %d", n)
	}
}

func TestCodexSyncer_SyncOnce_StopsOnCancelledContext(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})
	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := cs.SyncOnce(ctx); err == nil || err != context.Canceled {
		t.Fatalf("SyncOnce err = %v, want context.Canceled", err)
	}
}

func TestCodexSyncer_SyncOnce_StopsWhenSendDeadlineExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
	}))
	defer srv.Close()
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})
	state.SetOffset(path, 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	if _, err := cs.SyncOnce(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SyncOnce err = %v, want context deadline", err)
	}
}

func TestCodexSyncer_ReenrichOnce_ScansWholeFileWithoutAdvancingState(t *testing.T) {
	var got syncer.SyncPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]bool{"reenrich": true})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/sync" {
			t.Fatalf("request = %s %s, want POST /api/sync", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 0, "updated": 2})
	}))
	defer srv.Close()
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	first := `{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`
	second := `{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}}`
	path := writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/Users/alice/myproject","model_provider":"openai","originator":"codex-tui"}}`,
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/Users/alice/myproject","model":"gpt-5"}}`,
		first,
		second,
	})
	state.SetOffset(path, int64(len(first)+1))

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := cs.ReenrichOnce(context.Background())
	if err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("reenriched records = %d, want 2", n)
	}
	if !got.Reenrich {
		t.Fatal("reenrich flag was not sent")
	}
	if got.Agent != "codex" {
		t.Fatalf("agent = %q, want codex", got.Agent)
	}
	if len(got.Records) != 2 {
		t.Fatalf("request records = %d, want 2", len(got.Records))
	}
	if got.Records[1].Model != "gpt-5" {
		t.Fatalf("assistant model = %q, want gpt-5", got.Records[1].Model)
	}
	if got.Records[1].SourceFile != filepath.Base(path) || got.Records[1].Entrypoint != "cli" {
		t.Fatalf("codex enrichment fields missing: %+v", got.Records[1])
	}
	if state.GetOffset(path) != int64(len(first)+1) {
		t.Fatalf("state offset changed to %d, want %d", state.GetOffset(path), len(first)+1)
	}
}

func TestCodexSyncer_ReenrichOnce_ReturnsUnsupportedServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/sync/capabilities" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/sync" {
			t.Fatal("unsupported server must not receive reenrich POST")
		}
	}))
	defer srv.Close()
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-bbbbbbbb-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/Users/alice/myproject","model_provider":"openai","originator":"codex-tui"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	_, err := cs.ReenrichOnce(context.Background())
	if !errors.Is(err, syncer.ErrReenrichUnsupported) {
		t.Fatalf("ReenrichOnce error = %v, want ErrReenrichUnsupported", err)
	}
}

func TestCodexSyncer_SyncOnce_SkipNewOnFirstRun(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// Pre-existing file with content
	writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl", []string{
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	// New state → skip existing files
	if n != 0 {
		t.Errorf("expected 0 records on first run (skip existing), got %d", n)
	}
	if len(*captured) != 0 {
		t.Errorf("expected no HTTP calls on first run, got %d", len(*captured))
	}
}

func TestCodexSyncer_SyncOnce_SkipsAllExistingOnFirstRun(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t) // fresh state file -> IsNew() == true
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// Several pre-existing files. On the very first run ALL must be skipped to
	// EOF (no history backfill), not just the first file processed.
	for _, name := range []string{
		"rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-000000000001.jsonl",
		"rollout-2026-04-23T11-30-11-bbbbbbbb-0000-0000-0000-000000000002.jsonl",
		"rollout-2026-04-23T11-30-12-cccccccc-0000-0000-0000-000000000003.jsonl",
	} {
		writeCodexFixture(t, sessDir, name, []string{
			`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
			`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}}`,
		})
	}

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("first run must skip ALL existing files, synced %d", n)
	}
	if len(*captured) != 0 {
		t.Errorf("first run must make no HTTP calls, got %d", len(*captured))
	}
}

// TestCodexSyncer_SyncOnce_LogsSkippedFilesOnFirstRun: Codex has no SessionStart
// hook, so turning the integration on IS the first run — every rollout already on
// disk is skipped whole, not partially. The user runs `cctrace sync`, reads
// "Synced 0 records", opens an empty dashboard, and has nothing to go on. The
// skip is correct; its silence is not.
func TestCodexSyncer_SyncOnce_LogsSkippedFilesOnFirstRun(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)
	name := "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-000000000001.jsonl"
	writeCodexFixture(t, sessDir, name, []string{
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}
	// Once the file is in state it is no longer a skip; a notice repeated every
	// pass stops being read.
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}

	out := buf.String()
	if n := strings.Count(out, "skipped (pre-existing"); n != 1 {
		t.Fatalf("expected exactly 1 skip log across 2 passes, got %d; log:\n%s", n, out)
	}
	if !strings.Contains(out, filepath.Join(sessDir, name)) {
		t.Fatalf("skip log does not name the file; log:\n%s", out)
	}
}

// TestCodexSyncer_FirstRunWindowClosesWithNoFiles: same defect as the Claude
// side — a first pass that finds nothing writes no state, so the state stays
// "new" and the first rollout to appear afterwards is skipped whole as if it
// were history. Codex is the likelier victim: enabling the integration is itself
// the first run, and a user who turns it on before starting codex loses their
// opening session entirely.
func TestCodexSyncer_FirstRunWindowClosesWithNoFiles(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (empty pass): %v", err)
	}

	writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce (second pass): %v", err)
	}
	if n == 0 || len(*captured) == 0 {
		t.Fatal("a rollout created after the first run was skipped as pre-existing history")
	}
}

func TestCodexSyncer_SyncOnce_SkipsRepoOutsideAllowlist(t *testing.T) {
	srv, captured := newTestServer(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	state.Save()
	state, _ = syncer.LoadState(statePath)

	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// cwd "/myproject" is not a git checkout, so it resolves to a local: id,
	// which is outside the ExampleOrg allowlist and must be dropped.
	writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/myproject","model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, []string{"github.com/ExampleOrg/"})
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 records for repo outside allowlist, got %d", n)
	}
	if len(*captured) != 0 {
		t.Errorf("expected no HTTP calls for repo outside allowlist, got %d", len(*captured))
	}
}

func TestCodexSyncer_SyncOnce_NewFile(t *testing.T) {
	srv, captured := newTestServer(t)
	// State already has file → not new state
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	// Mark state as not-new by saving once
	state.Save()
	state, _ = syncer.LoadState(statePath)

	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/myproject","model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 records (user+assistant), got %d", n)
	}
	if len(*captured) == 0 {
		t.Fatal("expected HTTP call to server")
	}
	req := (*captured)[0]
	if req["agent"] != "codex" {
		t.Errorf("agent = %v, want codex", req["agent"])
	}
}

// TestCodexSyncer_SyncOnce_SendsSourceFileAndEntrypoint guards the realtime scan
// path (not toStoreRecord in isolation): the enrichment columns were previously
// filled only in reenrich.go, so a function-level test would have passed while
// the path that actually runs in production shipped empty source_file.
func TestCodexSyncer_SyncOnce_SendsSourceFileAndEntrypoint(t *testing.T) {
	srv, captured := newTestServer(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	state.Save()
	state, _ = syncer.LoadState(statePath)

	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	const fixtureName = "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl"
	writeCodexFixture(t, sessDir, fixtureName, []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/myproject","model_provider":"openai","originator":"codex-tui"}}`,
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/myproject","model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if len(*captured) == 0 {
		t.Fatal("expected HTTP call to server")
	}
	records, ok := (*captured)[0]["records"].([]interface{})
	if !ok || len(records) == 0 {
		t.Fatalf("expected records in payload, got %v", (*captured)[0]["records"])
	}
	for i, raw := range records {
		rec, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("record %d not an object: %v", i, raw)
		}
		if rec["source_file"] != fixtureName {
			t.Errorf("record %d source_file = %v, want %q", i, rec["source_file"], fixtureName)
		}
		if rec["entrypoint"] != "cli" {
			t.Errorf("record %d entrypoint = %v, want cli", i, rec["entrypoint"])
		}
	}
}

func TestCodexSyncer_SyncOnce_SendsProjectRulesFromSessionCWD(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Codex Rules\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	initGitRepo(t, repo)

	var ruleReq *store.ProjectRuleIngestRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync":
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
		case "/api/project-rules":
			var req store.ProjectRuleIngestRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode project rules: %v", err)
			}
			ruleReq = &req
			_ = json.NewEncoder(w).Encode(store.ProjectRuleIngestResponse{InsertedRules: 1, InsertedVersions: 1})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	state, _ = syncer.LoadState(statePath)

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":` + quoteJSON(repo) + `,"model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":` + quoteJSON(repo) + `,"model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("synced records = %d, want 1", n)
	}
	if ruleReq == nil {
		t.Fatal("project rule ingest request was not sent")
	}
	if ruleReq.Agent != "codex" {
		t.Fatalf("agent = %q, want codex", ruleReq.Agent)
	}
	if ruleReq.RepositoryID != "github.com/org/codex-rules" {
		t.Fatalf("repository_id = %q, want github.com/org/codex-rules", ruleReq.RepositoryID)
	}
	if ruleReq.RepositoryKey != "github.com/org/codex-rules" {
		t.Fatalf("repository_key = %q, want github.com/org/codex-rules", ruleReq.RepositoryKey)
	}
	if ruleReq.CommitSHA == "" {
		t.Fatal("commit_sha is empty")
	}
	if ruleReq.Branch != "main" {
		t.Fatalf("branch = %q, want main", ruleReq.Branch)
	}
	if len(ruleReq.Rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(ruleReq.Rules))
	}
	if ruleReq.Rules[0].RulePath != "AGENTS.md" || ruleReq.Rules[0].Status != "active" {
		t.Fatalf("unexpected rule snapshot: %+v", ruleReq.Rules[0])
	}
}

func TestCodexSyncer_ProjectRules403_NotRetriedWithinTTL(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Codex Rules\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	initGitRepo(t, repo)

	var ruleReqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
		case "/api/project-rules":
			ruleReqCount++
			w.WriteHeader(http.StatusForbidden)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	state, _ = syncer.LoadState(statePath)

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	for _, name := range []string{
		"rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-000000000001.jsonl",
		"rollout-2026-04-23T11-30-11-bbbbbbbb-0000-0000-0000-000000000002.jsonl",
	} {
		writeCodexFixture(t, sessDir, name, []string{
			`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":` + quoteJSON(repo) + `,"model_provider":"openai"}}`,
			`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		})
	}

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if ruleReqCount != 1 {
		t.Fatalf("project-rules requests = %d, want 1 (403 must be cached per repo)", ruleReqCount)
	}
}

func TestCodexSyncer_ProjectRules404_DisablesRuleSyncForSession(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Codex Rules\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	initGitRepo(t, repo)

	var ruleReqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
		case "/api/project-rules":
			ruleReqCount++
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	state, _ = syncer.LoadState(statePath)

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	for _, name := range []string{
		"rollout-2026-04-23T11-30-12-aaaaaaaa-0000-0000-0000-000000000001.jsonl",
		"rollout-2026-04-23T11-30-13-bbbbbbbb-0000-0000-0000-000000000002.jsonl",
	} {
		writeCodexFixture(t, sessDir, name, []string{
			`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":` + quoteJSON(repo) + `,"model_provider":"openai"}}`,
			`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		})
	}

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if ruleReqCount != 1 {
		t.Fatalf("project-rules requests = %d, want 1 (404 must disable rule sync for session)", ruleReqCount)
	}
}

func TestCodexSyncer_IncrementalUsagePreservesMetadata(t *testing.T) {
	srv, captured := newTestServer(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	state.Save()
	state, _ = syncer.LoadState(statePath)

	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	path := writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/Users/alice/myproject","model_provider":"openai","originator":"codex-tui"}}`,
		`{"type":"turn_context","timestamp":"2026-04-23T11:30:11.000Z","payload":{"cwd":"/Users/alice/myproject","model":"gpt-5"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	if n, err := cs.SyncOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("first SyncOnce: n=%d err=%v", n, err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-04-23T11:30:20.000Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`)
	f.Close()

	if n, err := cs.SyncOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("second SyncOnce: n=%d err=%v", n, err)
	}
	if len(*captured) != 2 {
		t.Fatalf("captured requests = %d, want 2", len(*captured))
	}
	req := (*captured)[1]
	if req["project_hash"] != "-users-alice-myproject" {
		t.Fatalf("project_hash = %v, want -users-alice-myproject", req["project_hash"])
	}
	records := req["records"].([]interface{})
	rec := records[0].(map[string]interface{})
	if rec["model"] != "gpt-5" {
		t.Fatalf("usage model = %v, want gpt-5", rec["model"])
	}
	if rec["record_type"] != "usage" {
		t.Fatalf("record_type = %v, want usage", rec["record_type"])
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/org/codex-rules.git")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "branch", "-M", "main")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(out))
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestCodexSyncer_IncrementalUsageSkipsDuplicateTotal(t *testing.T) {
	srv, captured := newTestServer(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	state.Save()
	state, _ = syncer.LoadState(statePath)

	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	path := writeCodexFixture(t, sessDir, "rollout-2026-05-15T18-24-24-019e2af3-89a7-7051-a7de-f084dcc39019.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-05-15T09:24:24.000Z","payload":{"cwd":"/Users/alice/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-05-15T09:24:25.000Z","payload":{"cwd":"/Users/alice/myproject","model":"gpt-5.5"}}`,
		`{"type":"event_msg","timestamp":"2026-05-15T09:26:02.304Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	if n, err := cs.SyncOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("first SyncOnce: n=%d err=%v", n, err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-05-15T09:26:46.078Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`)
	f.Close()

	if n, err := cs.SyncOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("duplicate SyncOnce: n=%d err=%v", n, err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured requests after duplicate = %d, want 1", len(*captured))
	}

	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-05-15T09:29:59.139Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":175,"cached_input_tokens":70,"output_tokens":25},"last_token_usage":{"input_tokens":75,"cached_input_tokens":30,"output_tokens":5}}}}`)
	f.Close()

	if n, err := cs.SyncOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("next SyncOnce: n=%d err=%v", n, err)
	}
	if len(*captured) != 2 {
		t.Fatalf("captured requests = %d, want 2", len(*captured))
	}
	req := (*captured)[1]
	records := req["records"].([]interface{})
	rec := records[0].(map[string]interface{})
	if rec["input_tokens"] != float64(75) || rec["cache_read_tokens"] != float64(30) || rec["output_tokens"] != float64(5) {
		t.Fatalf("usage tokens = %v/%v/%v, want 75/30/5", rec["input_tokens"], rec["cache_read_tokens"], rec["output_tokens"])
	}
}

func TestCodexSyncer_UpgradeStateBackfillsUsageMetadata(t *testing.T) {
	srv, captured := newTestServer(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	state.Save()
	state, _ = syncer.LoadState(statePath)

	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	prefix := []string{
		`{"type":"session_meta","timestamp":"2026-05-15T09:24:24.000Z","payload":{"cwd":"/Users/alice/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-05-15T09:24:25.000Z","payload":{"cwd":"/Users/alice/myproject","model":"gpt-5.5"}}`,
		`{"type":"event_msg","timestamp":"2026-05-15T09:26:02.304Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	}
	path := writeCodexFixture(t, sessDir, "rollout-2026-05-15T18-24-24-019e2af3-89a7-7051-a7de-f084dcc39019.jsonl", prefix)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffsetWithMetadata(path, fi.Size(), "/Users/alice/myproject", "gpt-5.5")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-05-15T09:26:46.078Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`)
	f.Close()

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	if n, err := cs.SyncOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("SyncOnce: n=%d err=%v", n, err)
	}
	if len(*captured) != 0 {
		t.Fatalf("captured requests = %d, want 0", len(*captured))
	}
	fs := state.Files[path]
	if fs == nil || !fs.TokenUsageScanned || !fs.HasTotalTokenUsage {
		t.Fatalf("usage metadata was not persisted: %+v", fs)
	}
}

func TestCodexSyncer_UpgradeStateBackfillsForkBoundary(t *testing.T) {
	srv, captured := newTestServer(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	state.Save()
	state, _ = syncer.LoadState(statePath)

	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0o755)

	path := writeCodexFixture(t, sessDir, "rollout-2026-07-27T15-29-17-019fa243-73ec-76c3-a282-609995a6fcbe.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-07-27T06:29:17.513Z","payload":{"id":"child-session","forked_from_id":"parent-session","thread_source":"subagent","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"session_meta","timestamp":"2026-07-27T06:29:17.513Z","payload":{"id":"parent-session","thread_source":"user","cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"turn_context","timestamp":"2026-07-27T06:29:17.513Z","payload":{"cwd":"/myproject","model":"gpt-5.6-sol"}}`,
		`{"type":"event_msg","timestamp":"2026-07-27T06:29:17.514Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}}}}`,
	})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffsetWithMetadata(path, fi.Size(), "/myproject", "gpt-5.6-sol")
	fs := state.Files[path]
	fs.TokenUsageScanned = true
	fs.HasTotalTokenUsage = true
	fs.TotalInputTokens = 100
	fs.TotalCachedInputTokens = 40
	fs.TotalOutputTokens = 20

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, `{"type":"inter_agent_communication_metadata","timestamp":"2026-07-27T06:29:19.131Z","payload":{"trigger_turn":true}}`)
	fmt.Fprintln(f, `{"type":"event_msg","timestamp":"2026-07-27T06:29:26.820Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":175,"cached_input_tokens":70,"output_tokens":25},"last_token_usage":{"input_tokens":75,"cached_input_tokens":30,"output_tokens":5}}}}`)
	f.Close()

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	if n, err := cs.SyncOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("SyncOnce: n=%d err=%v", n, err)
	}
	if len(*captured) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(*captured))
	}
	records := (*captured)[0]["records"].([]interface{})
	rec := records[0].(map[string]interface{})
	if rec["input_tokens"] != float64(75) || rec["cache_read_tokens"] != float64(30) || rec["output_tokens"] != float64(5) {
		t.Fatalf("usage tokens = %v/%v/%v, want 75/30/5", rec["input_tokens"], rec["cache_read_tokens"], rec["output_tokens"])
	}
	if !state.Files[path].CodexSubagentFork ||
		!state.Files[path].CodexForkHistoryCopied ||
		!state.Files[path].CodexForkBoundaryReached {
		t.Fatalf("fork metadata was not persisted: %+v", state.Files[path])
	}
}

func TestCodexSyncer_PersistsForkHistoryCopied(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	meta := codexlog.Metadata{
		ForkMetadataScanned: true,
		IsSubagentFork:      true,
		ForkHistoryCopied:   true,
	}

	setStateFileMetadata(state, path, 42, meta)

	fs := state.Files[path]
	if fs == nil || !fs.CodexForkHistoryCopied {
		t.Fatalf("fork history metadata was not persisted: %+v", fs)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	state, err = syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	cs := &CodexSyncer{state: state}
	got := cs.fileMetadata(path)
	if !got.ForkHistoryCopied {
		t.Fatalf("fork history metadata was not restored: %+v", got)
	}
	if !metadataChanged(codexlog.Metadata{}, got) {
		t.Fatal("fork history metadata change was not detected")
	}
	merged := mergeMetadata(codexlog.Metadata{}, got)
	if !merged.ForkHistoryCopied {
		t.Fatalf("fork history metadata was not merged: %+v", merged)
	}
}

func TestCodexSyncer_toStoreRecord_User(t *testing.T) {
	r := &codexlog.Record{
		SessionID:  "ses-001",
		Timestamp:  time.Date(2026, 4, 23, 11, 30, 12, 0, time.UTC),
		RecordType: "user",
		CWD:        "/myproject",
		Model:      "gpt-5",
		Content:    "hello",
	}
	sr := toStoreRecord(r, "user@example.com", "uid-001", "ses-001", "rollout-ses-001.jsonl", "cli")
	if sr == nil {
		t.Fatal("expected record, got nil")
	}
	if sr.Agent != "codex" {
		t.Errorf("Agent = %q, want codex", sr.Agent)
	}
	if sr.BillingProvider != "openai" {
		t.Errorf("BillingProvider = %q, want openai", sr.BillingProvider)
	}
	if sr.RecordType != "user" {
		t.Errorf("RecordType = %q, want user", sr.RecordType)
	}
	if sr.ProfileEmail != "user@example.com" {
		t.Errorf("ProfileEmail = %q, want user@example.com", sr.ProfileEmail)
	}
	if sr.Model != "gpt-5" {
		t.Errorf("Model = %q, want gpt-5", sr.Model)
	}
}

func TestCodexSyncer_toStoreRecord_SkillCommandName(t *testing.T) {
	r := &codexlog.Record{
		SessionID:  "ses-001",
		Timestamp:  time.Date(2026, 4, 23, 11, 30, 12, 0, time.UTC),
		RecordType: "user",
		Content:    "<skill>\n<name>commit</name>\n<path>/tmp/commit/SKILL.md</path>\n</skill>",
	}

	sr := toStoreRecord(r, "user@example.com", "uid-001", "ses-001", "rollout-ses-001.jsonl", "cli")
	if sr == nil {
		t.Fatal("expected record, got nil")
	}
	if sr.CommandName != "commit" {
		t.Fatalf("CommandName = %q, want commit", sr.CommandName)
	}
}

func TestAvoidSessionRecordConflict_AdjustsDuplicateKeys(t *testing.T) {
	ts := time.Date(2026, 4, 23, 11, 30, 12, 0, time.UTC)
	first := &store.SessionRecord{SessionID: "ses-001", Ts: ts, RecordType: "user", ProfileEmail: "user@example.com"}
	second := &store.SessionRecord{SessionID: "ses-001", Ts: ts, RecordType: "user", ProfileEmail: "user@example.com", CommandName: "commit"}
	nudger := syncer.NewConflictNudger(nil)

	nudger.Apply(first)
	nudger.Apply(second)

	if !first.Ts.Equal(ts) {
		t.Fatalf("first timestamp changed: got %s, want %s", first.Ts, ts)
	}
	if !second.Ts.Equal(ts.Add(time.Microsecond)) {
		t.Fatalf("second timestamp = %s, want %s", second.Ts, ts.Add(time.Microsecond))
	}
}

func TestCodexSyncer_toStoreRecord_ToolIdentity(t *testing.T) {
	cases := []struct {
		rec              codexlog.Record
		wantName, wantID string
	}{
		{codexlog.Record{RecordType: "tool_call", ToolName: "bash", CallID: "c1"}, "bash", "c1"},
		{codexlog.Record{RecordType: "tool_output", CallID: "c1"}, "", "c1"},
		{codexlog.Record{RecordType: "assistant", ToolName: "bash", CallID: "c1"}, "", ""},
	}
	for _, tc := range cases {
		sr := toStoreRecord(&tc.rec, "e", "u", "s", "f.jsonl", "cli")
		if sr.ToolName != tc.wantName || sr.ToolCallID != tc.wantID {
			t.Errorf("%s: ToolName = %q, ToolCallID = %q; want %q, %q", tc.rec.RecordType, sr.ToolName, sr.ToolCallID, tc.wantName, tc.wantID)
		}
	}
}

func TestCodexSyncer_toStoreRecord_SkipEmpty(t *testing.T) {
	r := &codexlog.Record{
		SessionID:  "ses-001",
		RecordType: "", // empty → should be skipped
	}
	sr := toStoreRecord(r, "user@example.com", "uid-001", "ses-001", "rollout-ses-001.jsonl", "cli")
	if sr != nil {
		t.Errorf("expected nil for empty RecordType, got %+v", sr)
	}
}

// Codex deliberately does NOT rewind on a shrunk file, unlike internal/syncer.
//
// Rewinding re-sends records, and that is only safe when a record's stored
// identity comes from its content. Claude records carry the uuid from their
// JSONL line, so a re-send collides with itself and is absorbed. Codex records
// whose line has no timestamp have neither a timestamp nor a uuid: they are
// stored at the zero time and told apart only by the microsecond bump that
// syncer.ConflictNudger applies in scan order. A rewound pass restarts
// that counter, so a genuinely new record lands on a key an older record
// already holds, is dropped by ON CONFLICT DO NOTHING, and is gone for good
// once the offset advances past it (verified against TimescaleDB).
//
// Rewinding also invalidates the persisted fork metadata: the offset goes back
// but ForkBoundaryReached stays set, which opens the inherited-history gate and
// sends the parent's copied transcript as the child session's own records.
//
// Fixing this needs a stable per-record identity for timestamp-less Codex
// records — what PR #131 attempted and closed. Until then a shrunk Codex file
// stalls, which loses nothing already stored.
func TestCodexSyncer_SyncOnce_DoesNotRewindShrunkFile(t *testing.T) {
	srv, captured := newTestServer(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	state.Save()
	state, _ = syncer.LoadState(statePath)

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl"
	path := writeCodexFixture(t, sessDir, name, []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}]}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"two"}]}}`,
	})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffsetWithMetadata(path, fi.Size(), "/myproject", "gpt-5")

	// Replaced by a shorter file at the same path.
	writeCodexFixture(t, sessDir, name, []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:14.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fresh"}]}}`,
	})

	cs := New([]string{codexDir}, "user@test.local", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 0 || len(*captured) != 0 {
		t.Fatalf("codex must not re-send a shrunk file; synced=%d requests=%d", n, len(*captured))
	}
	// The offset drops to the file's current size, not to zero: the bytes on
	// disk were already sent or are a rewrite remnant, and leaving the offset
	// above the size is what stranded later growth.
	shrunkInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.GetOffset(path); got != shrunkInfo.Size() {
		t.Fatalf("offset = %d, want the current size %d (reset, not rewind)", got, shrunkInfo.Size())
	}
}

// TestCodexSyncer_SyncOnce_ResumesAfterShrunkFileGrowsAgain covers the loss the
// reset exists to prevent: a rollout file rewritten smaller and then appended
// to. Holding the stored offset strands every byte written until the file
// climbs back past it.
func TestCodexSyncer_SyncOnce_ResumesAfterShrunkFileGrowsAgain(t *testing.T) {
	srv, captured := newTestServer(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	state.Save()
	state, _ = syncer.LoadState(statePath)

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl"
	meta := `{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/myproject","model_provider":"openai"}}`
	path := writeCodexFixture(t, sessDir, name, []string{
		meta,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}]}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"two"}]}}`,
	})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffsetWithMetadata(path, fi.Size(), "/myproject", "gpt-5")

	// Rewritten smaller, as a rollout is when its body is dropped.
	writeCodexFixture(t, sessDir, name, []string{meta})

	cs := New([]string{codexDir}, "user@test.local", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (shrunk): %v", err)
	}
	if len(*captured) != 0 {
		t.Fatalf("the shrunk file must not be re-sent; requests=%d", len(*captured))
	}

	// The session resumes: new content, never sent, below the stored offset.
	writeCodexFixture(t, sessDir, name, []string{
		meta,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:20.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"resumed"}]}}`,
	})
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (regrown): %v", err)
	}

	var found bool
	for _, req := range *captured {
		if strings.Contains(fmt.Sprint(req), "resumed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("content written after the shrink was never collected; requests=%d", len(*captured))
	}
}

// TestCodexSyncer_SyncOnce_LogsShrunkFileOnceThenSuppresses verifies the
// shrunk-file reset is logged once (so it is distinguishable from a quiet
// file) and that repeated passes over the same still-shrunk file do not repeat
// the log every poll.
func TestCodexSyncer_SyncOnce_LogsShrunkFileOnceThenSuppresses(t *testing.T) {
	srv, _ := newTestServer(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, _ := syncer.LoadState(statePath)
	state.Save()
	state, _ = syncer.LoadState(statePath)

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "rollout-2026-04-23T11-30-10-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl"
	path := writeCodexFixture(t, sessDir, name, []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}]}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:13.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"two"}]}}`,
	})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffsetWithMetadata(path, fi.Size(), "/myproject", "gpt-5")

	// Replaced by a shorter file at the same path.
	writeCodexFixture(t, sessDir, name, []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/myproject","model_provider":"openai"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:14.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fresh"}]}}`,
	})

	cs := New([]string{codexDir}, "user@test.local", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}

	out := buf.String()
	count := strings.Count(out, "shrunk, offset reset")
	if count != 1 {
		t.Fatalf("expected exactly 1 stall log across 2 passes, got %d; log:\n%s", count, out)
	}
}

// The hash has to match the directory name Claude Code creates for the same
// working directory, leading separator included. This test used to assert the
// trimmed spelling, which is what split one directory across two keys (#303).
// The rule itself is pinned in internal/projecthash.
func TestProjectHashMatchesTheCanonicalRule(t *testing.T) {
	cases := []struct {
		cwd  string
		want string
	}{
		{"/Users/alice/myapp", "-users-alice-myapp"},
		{"/home/user/project", "-home-user-project"},
		{`C:\work\app`, "c--work-app"},
		{"", ""},
	}
	for _, c := range cases {
		got := projecthash.FromPath(c.cwd)
		if got != c.want {
			t.Errorf("FromPath(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

func TestProjectHashFromPathIsStableFallback(t *testing.T) {
	a := projectHashFromPath("/tmp/session-a.jsonl")
	b := projectHashFromPath("/tmp/session-a.jsonl")
	c := projectHashFromPath("/tmp/session-b.jsonl")
	if a == "" || !strings.HasPrefix(a, "codex-") {
		t.Fatalf("projectHashFromPath returned invalid hash %q", a)
	}
	if a != b {
		t.Fatalf("same path produced different hashes: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different paths produced same fallback hash: %q", a)
	}
}

func TestProjectNameFromCWD(t *testing.T) {
	cases := []struct {
		cwd  string
		want string
	}{
		{"/Users/alice/myapp", "myapp"},
		{"/home/user/project", "project"},
		{`C:\project\ods\.agents`, ".agents"},
		{"", ""},
	}
	for _, c := range cases {
		got := projecthash.NameFromPath(c.cwd)
		if got != c.want {
			t.Errorf("NameFromPath(%q) = %q, want %q", c.cwd, got, c.want)
		}
	}
}

// Compile-time check that store.SessionRecord has Agent field.
var _ = store.SessionRecord{Agent: "codex", BillingProvider: "openai"}

// Codex emits no prompt_source, so the session classifier has to read entrypoint to tell
// a human TUI session from an automated one. Everything used to land on "cli", which put
// `codex exec`, the TS SDK and harness-delegated runs in the interactive bucket.
// A subagent fork carries the SAME originator as the human session it was spawned
// from. Measured on a real run: three subagent rollouts, each with
//
//	{"originator":"codex-tui","thread_source":"subagent","forked_from_id":"<parent session id>"}
//
// So originator alone cannot answer "did a person type this" — codex-tui names both
// the human TUI and everything the model forks off it. Only thread_source separates
// them, and it is the fork flag the parser already computes.
func TestEntrypointFromOriginator(t *testing.T) {
	cases := []struct {
		originator string
		subagent   bool
		want       string
	}{
		{originator: "codex-tui", want: "cli"},
		{originator: "codex_exec", want: "codex_exec"},
		{originator: "codex_sdk_ts", want: "codex_sdk_ts"},
		{originator: "Claude Code", want: "claude-code"},
		{originator: "", want: ""},

		// The fork flag wins over every originator, including the empty one: a
		// rollout we know was spawned by an agent must not fall through to the
		// legacy benefit-of-the-doubt path meant for pre-originator files.
		{originator: "codex-tui", subagent: true, want: "codex-subagent"},
		{originator: "codex_exec", subagent: true, want: "codex-subagent"},
		{originator: "", subagent: true, want: "codex-subagent"},
	}
	for _, tc := range cases {
		if got := entrypointFromOriginator(tc.originator, tc.subagent); got != tc.want {
			t.Errorf("entrypointFromOriginator(%q, subagent=%v) = %q, want %q",
				tc.originator, tc.subagent, got, tc.want)
		}
	}
}

// TestSyncOnce_SubagentRolloutGetsSubagentEntrypoint checks the wiring, not the
// mapping: the fork flag has to travel parser -> metadata -> the entrypoint the
// record is stored with. It was already computed and already reached the sync
// state; it just never got handed to the field the classifier reads.
//
// The fixture mirrors a measured rollout — child session_meta first, parent's
// embedded right after — because that ordering is what the parser's stickiness
// exists to survive.
func TestSyncOnce_SubagentRolloutGetsSubagentEntrypoint(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// State must not be new, or the first-run skip swallows the file before any of
	// this is exercised.
	state.SetOffset(filepath.Join(sessDir, "placeholder"), 0)
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	writeCodexFixture(t, sessDir, "rollout-2026-08-13T11-00-00-019ffb4b-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-08-13T11:00:00.000Z","payload":{"originator":"codex-tui","thread_source":"subagent","forked_from_id":"00000000-0000-7000-8000-00000000feed"}}`,
		`{"type":"session_meta","timestamp":"2026-08-13T11:00:00.000Z","payload":{"originator":"codex-tui","thread_source":"user"}}`,
		`{"type":"response_item","timestamp":"2026-08-13T11:00:01.000Z","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
	})

	if _, err := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil).
		SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	var seen int
	for _, payload := range *captured {
		records, _ := payload["records"].([]interface{})
		for _, raw := range records {
			rec, _ := raw.(map[string]interface{})
			seen++
			if got, _ := rec["entrypoint"].(string); got != "codex-subagent" {
				t.Errorf("record %d entrypoint = %q, want codex-subagent", seen, got)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no records reached the server")
	}
}

// TestSubagentEntrypointIsNotInteractive pins the reason the value above matters:
// the session classifier buckets Codex on entrypoint alone (`entrypoint IN (”,'cli')`),
// so anything that maps to "cli" is shown as a session a person had. Keep this in
// step with sessionInteractiveCol in internal/store.
func TestSubagentEntrypointIsNotInteractive(t *testing.T) {
	interactive := func(entrypoint string) bool {
		return entrypoint == "" || entrypoint == "cli"
	}
	if !interactive(entrypointFromOriginator("codex-tui", false)) {
		t.Error("a human TUI session must stay interactive")
	}
	if interactive(entrypointFromOriginator("codex-tui", true)) {
		t.Error("a subagent fork must not be classified as interactive")
	}
}

func TestToStoreRecordEntrypoint(t *testing.T) {
	rec := &codexlog.Record{RecordType: "user", Timestamp: time.Now()}
	if got := toStoreRecord(rec, "e", "u", "s", "f.jsonl", "cli").Entrypoint; got != "cli" {
		t.Errorf("Entrypoint = %q, want cli", got)
	}
	if got := toStoreRecord(rec, "e", "u", "s", "f.jsonl", "codex_exec").Entrypoint; got != "codex_exec" {
		t.Errorf("Entrypoint = %q, want codex_exec", got)
	}
}

// A resumed scan must not lose the originator learned from the file's first session_meta.
func TestMergeMetadataKeepsOriginator(t *testing.T) {
	base := codexlog.Metadata{Originator: "codex_exec"}
	if got := mergeMetadata(base, codexlog.Metadata{CWD: "/p"}).Originator; got != "codex_exec" {
		t.Errorf("Originator = %q, want codex_exec preserved", got)
	}
	if got := mergeMetadata(codexlog.Metadata{}, codexlog.Metadata{Originator: "codex-tui"}).Originator; got != "codex-tui" {
		t.Errorf("Originator = %q, want codex-tui adopted", got)
	}
}

// The rescan of a consumed prefix saves what it rebuilt only when
// metadataChanged reports a difference, and the originator can be the only one.
func TestMetadataChangedSeesOriginator(t *testing.T) {
	if !metadataChanged(codexlog.Metadata{CWD: "/p"}, codexlog.Metadata{CWD: "/p", Originator: "codex_exec"}) {
		t.Error("a rebuilt originator was not reported as a change")
	}
}
