package codexsyncer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/projectrule"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// stubProjectRuleScan replaces the rule scanner for the duration of a test and
// reports how many times it was invoked plus the deadline it was handed.
func stubProjectRuleScan(t *testing.T, err error) (calls *int, deadline *time.Duration) {
	t.Helper()
	var n int
	var d time.Duration
	orig := projectRuleScan
	projectRuleScan = func(ctx context.Context, opts projectrule.ScanOptions) ([]*store.ProjectRuleSnapshot, error) {
		n++
		if dl, ok := ctx.Deadline(); ok {
			d = time.Until(dl)
		}
		return nil, err
	}
	t.Cleanup(func() { projectRuleScan = orig })
	return &n, &d
}

// stubBlockingProjectRuleScan replaces the rule scanner with one that ignores
// its context entirely and returns only when release is closed, standing in for
// a walk parked in an uninterruptible readdir syscall.
func stubBlockingProjectRuleScan(t *testing.T, release <-chan struct{}) *int64 {
	t.Helper()
	var calls int64
	orig := projectRuleScan
	projectRuleScan = func(context.Context, projectrule.ScanOptions) ([]*store.ProjectRuleSnapshot, error) {
		atomic.AddInt64(&calls, 1)
		<-release
		return nil, nil
	}
	t.Cleanup(func() { projectRuleScan = orig })
	return &calls
}

func setProjectRuleScanTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := projectRuleScanTimeout
	projectRuleScanTimeout = d
	t.Cleanup(func() { projectRuleScanTimeout = orig })
}

// TestCodexSyncer_ProjectRuleScanBlocked_DoesNotStallPass covers the production
// stack the deadline alone cannot reach: the scan is parked inside a filesystem
// syscall, so it never observes ctx at all. The sync pass must still return and
// keep collecting session records.
func TestCodexSyncer_ProjectRuleScanBlocked_DoesNotStallPass(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Codex Rules\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	initGitRepo(t, repo)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	scanCalls := stubBlockingProjectRuleScan(t, release)
	setProjectRuleScanTimeout(t, 100*time.Millisecond)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/sync" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
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

	type passResult struct {
		n   int
		err error
	}
	done := make(chan passResult, 1)
	go func() {
		n, err := cs.SyncOnce(context.Background())
		done <- passResult{n: n, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("SyncOnce: %v", got.err)
		}
		if got.n != 2 {
			t.Fatalf("synced records = %d, want 2 (a blocked rule scan must not stop collection)", got.n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SyncOnce did not return: a blocked rule scan still holds the whole pass")
	}

	if got := atomic.LoadInt64(scanCalls); got != 1 {
		t.Fatalf("rule scans = %d, want 1 (a stuck root must not be rescanned behind the stuck walk)", got)
	}
}

// TestCodexSyncer_ProjectRuleScanTimeout_DoesNotStopRecordSync reproduces the
// production hang: projectrule.Scan blocked inside WalkDir on a slow filesystem
// and took the whole sync daemon down with it. The scan must run under its own
// deadline, and a scan that gives up must not stop session records from syncing.
func TestCodexSyncer_ProjectRuleScanTimeout_DoesNotStopRecordSync(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Codex Rules\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	initGitRepo(t, repo)

	scanCalls, scanDeadline := stubProjectRuleScan(t, context.DeadlineExceeded)

	var syncCount, ruleReqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync":
			syncCount++
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
		case "/api/project-rules":
			ruleReqCount++
			_ = json.NewEncoder(w).Encode(store.ProjectRuleIngestResponse{})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
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
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	if n != 2 {
		t.Fatalf("synced records = %d, want 2 (a rule-scan timeout must not stop collection)", n)
	}
	if syncCount != 2 {
		t.Fatalf("/api/sync requests = %d, want 2", syncCount)
	}
	if ruleReqCount != 0 {
		t.Fatalf("/api/project-rules requests = %d, want 0 (no rules were scanned)", ruleReqCount)
	}
	if *scanDeadline <= 0 || *scanDeadline > projectRuleScanTimeout {
		t.Fatalf("scan deadline = %v, want (0, %v]", *scanDeadline, projectRuleScanTimeout)
	}
	if *scanCalls != 1 {
		t.Fatalf("rule scans = %d, want 1", *scanCalls)
	}
}

// TestCodexSyncer_NonGitCWD_NotScanned pins the root cause of the production
// hang: sendProjectRules fell back to the session cwd when git reported no
// repository, so cwds like "/" or "$HOME" (both present in real Codex sessions)
// became scan roots and the walk never finished. A non-git cwd must not be
// scanned at all — it is unbounded, and its rule files belong to unrelated
// repositories that would be merged under one project hash.
func TestCodexSyncer_NonGitCWD_NotScanned(t *testing.T) {
	cwd := t.TempDir() // deliberately not a git repository
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# Codex Rules\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	scanCalls, _ := stubProjectRuleScan(t, nil)

	var syncCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/sync" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		syncCount++
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
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
	writeCodexFixture(t, sessDir, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-000000000001.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":` + quoteJSON(cwd) + `,"model_provider":"openai"}}`,
		`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	n, err := cs.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if n != 1 || syncCount == 0 {
		t.Fatalf("synced records = %d, /api/sync calls = %d, want records to keep syncing", n, syncCount)
	}
	if *scanCalls != 0 {
		t.Fatalf("rule scans = %d, want 0 (a non-git cwd must never become a scan root)", *scanCalls)
	}
}

// TestCodexSyncer_ProjectRuleScanFailure_NotRetriedWithinTTL extends the cost
// ceiling past the timeout case. A stalled mount fails the root stat with EIO or
// ETIMEDOUT rather than a context error, and a repository with no rule files
// yields nothing at all — both used to skip the TTL stamp, so every remaining
// session file in the repo paid for the same failing walk again.
func TestCodexSyncer_ProjectRuleScanFailure_NotRetriedWithinTTL(t *testing.T) {
	cases := []struct {
		name    string
		scanErr error
	}{
		{name: "root io error", scanErr: errors.New("input/output error")},
		{name: "no rules found", scanErr: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# repo\n"), 0o644); err != nil {
				t.Fatalf("write README: %v", err)
			}
			initGitRepo(t, repo)

			scanCalls, _ := stubProjectRuleScan(t, tc.scanErr)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/api/sync" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
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
				"rollout-2026-04-23T11-30-12-cccccccc-0000-0000-0000-000000000003.jsonl",
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
			if *scanCalls != 1 {
				t.Fatalf("rule scans = %d, want 1 (a failed scan must be suppressed for the TTL)", *scanCalls)
			}
		})
	}
}

// TestCodexSyncer_ProjectRuleScanTimeout_NotRetriedWithinTTL pins the cost
// ceiling: a repository whose scan timed out must not be rescanned for every
// remaining session file, or a single slow tree still stalls the whole pass.
func TestCodexSyncer_ProjectRuleScanTimeout_NotRetriedWithinTTL(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Codex Rules\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	initGitRepo(t, repo)

	scanCalls, _ := stubProjectRuleScan(t, context.DeadlineExceeded)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync":
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
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
		"rollout-2026-04-23T11-30-12-cccccccc-0000-0000-0000-000000000003.jsonl",
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
	if *scanCalls != 1 {
		t.Fatalf("rule scans = %d, want 1 (a timed-out repo must be suppressed for the TTL)", *scanCalls)
	}
}
