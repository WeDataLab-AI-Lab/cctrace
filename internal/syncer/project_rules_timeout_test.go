package syncer

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

// TestSyncOnce_ProjectRuleScanBlocked_DoesNotStallPass covers the production
// stack the deadline alone cannot reach: the scan is parked inside a filesystem
// syscall, so it never observes ctx at all. The sync pass must still return and
// keep collecting session records.
func TestSyncOnce_ProjectRuleScanBlocked_DoesNotStallPass(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# Claude Rules\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
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
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, name := range []string{"session1.jsonl", "session2.jsonl"} {
		state.SetOffset(writeClaudeSession(t, sessionDir, name, repo), 0)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)

	type passResult struct {
		n   int
		err error
	}
	done := make(chan passResult, 1)
	go func() {
		n, err := s.SyncOnce(context.Background())
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

func writeClaudeSession(t *testing.T, dir, name, cwd string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"` + name + `","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	return p
}

// TestSyncOnce_ProjectRuleScanTimeout_DoesNotStopRecordSync reproduces the
// production hang: projectrule.Scan blocked inside WalkDir on a slow filesystem
// and took the whole sync daemon down with it. The scan must run under its own
// deadline, and a scan that gives up must not stop session records from syncing.
func TestSyncOnce_ProjectRuleScanTimeout_DoesNotStopRecordSync(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# Claude Rules\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	initGitRepo(t, repo)

	scanCalls, scanDeadline := stubProjectRuleScan(t, context.DeadlineExceeded)

	var syncCount, ruleReqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync":
			syncCount++
			_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
		case "/api/project-rules":
			ruleReqCount++
			_ = json.NewEncoder(w).Encode(store.ProjectRuleIngestResponse{})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, name := range []string{"session1.jsonl", "session2.jsonl"} {
		state.SetOffset(writeClaudeSession(t, sessionDir, name, repo), 0)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	if n != 2 {
		t.Fatalf("synced records = %d, want 2 (a rule-scan timeout must not stop collection)", n)
	}
	if syncCount == 0 {
		t.Fatal("/api/sync was never called")
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

// TestSyncOnce_NonGitCWD_NotScanned pins the root cause of the production hang:
// a session whose cwd is not a git repository used to promote that cwd to a scan
// root, so "/" or "$HOME" got walked in full. Such a cwd must not be scanned at
// all — it is unbounded, and its rule files belong to unrelated repositories
// that would be merged under one project hash.
func TestSyncOnce_NonGitCWD_NotScanned(t *testing.T) {
	cwd := t.TempDir() // deliberately not a git repository
	if err := os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), []byte("# Claude Rules\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}

	scanCalls, _ := stubProjectRuleScan(t, nil)

	var syncCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/sync" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		syncCount++
		_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
	}))
	defer srv.Close()

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-nogit")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(writeClaudeSession(t, sessionDir, "session1.jsonl", cwd), 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	n, err := s.SyncOnce(context.Background())
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

// TestSyncOnce_ProjectRuleScanFailure_NotRetriedWithinTTL extends the cost
// ceiling past the timeout case. A stalled mount fails the root stat with EIO or
// ETIMEDOUT rather than a context error, and a repository with no rule files
// yields nothing at all — both used to skip the TTL stamp, so every remaining
// session file in the repo paid for the same failing walk again.
func TestSyncOnce_ProjectRuleScanFailure_NotRetriedWithinTTL(t *testing.T) {
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
				_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
			}))
			defer srv.Close()

			claudeDir := t.TempDir()
			sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
			if err := os.MkdirAll(sessionDir, 0o755); err != nil {
				t.Fatalf("mkdir session dir: %v", err)
			}
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatalf("LoadState: %v", err)
			}
			for _, name := range []string{"session1.jsonl", "session2.jsonl", "session3.jsonl"} {
				state.SetOffset(writeClaudeSession(t, sessionDir, name, repo), 0)
			}
			if err := state.Save(); err != nil {
				t.Fatalf("Save state: %v", err)
			}

			s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
			if _, err := s.SyncOnce(context.Background()); err != nil {
				t.Fatalf("SyncOnce: %v", err)
			}
			if *scanCalls != 1 {
				t.Fatalf("rule scans = %d, want 1 (a failed scan must be suppressed for the TTL)", *scanCalls)
			}
		})
	}
}

// TestSyncOnce_ProjectRuleScanTimeout_NotRetriedWithinTTL pins the cost ceiling:
// a repository whose scan timed out must not be rescanned for every remaining
// session file, or a single slow tree still stalls the whole pass.
func TestSyncOnce_ProjectRuleScanTimeout_NotRetriedWithinTTL(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# Claude Rules\n"), 0o644); err != nil {
		t.Fatalf("write CLAUDE.md: %v", err)
	}
	initGitRepo(t, repo)

	scanCalls, _ := stubProjectRuleScan(t, context.DeadlineExceeded)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync":
			_ = json.NewEncoder(w).Encode(syncResponse{Inserted: 1})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, name := range []string{"session1.jsonl", "session2.jsonl", "session3.jsonl"} {
		state.SetOffset(writeClaudeSession(t, sessionDir, name, repo), 0)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}

	s := New(claudeDir, "user@example.com", "u1", state, NewClient(srv.URL, "", ""), nil)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if *scanCalls != 1 {
		t.Fatalf("rule scans = %d, want 1 (a timed-out repo must be suppressed for the TTL)", *scanCalls)
	}
}
