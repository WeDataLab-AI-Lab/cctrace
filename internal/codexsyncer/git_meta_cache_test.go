package codexsyncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/gitctx"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// Every unchanged session file reaches the project-rules path on every pass.
// Resolving git afresh there forked up to five processes per file -- tens of
// thousands per pass for a few hundred distinct cwds -- so the metadata is
// reused per cwd until it is gitMetaTTL old.
func TestSyncOnce_unchangedFilesResolveGitOncePerCwd(t *testing.T) {
	calls := map[string]int{}
	prevResolve := resolveGit
	resolveGit = func(cwd string) gitctx.Context {
		calls[cwd]++
		// No repository root: sendProjectRules returns before any rule scan.
		return gitctx.Context{RepositoryID: "local:" + cwd}
	}
	t.Cleanup(func() { resolveGit = prevResolve })
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	prevNow := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = prevNow })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"inserted":0,"skipped":0}`))
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	sessDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatal(err)
	}
	cwds := []string{"/work/a", "/work/a", "/work/a", "/work/b", "/work/b"}
	for i, cwd := range cwds {
		name := "rollout-2026-08-24T09-00-0" + string(rune('0'+i)) + "-019db82c-66c5-7160-a6a7-b76dc2dd72c" + string(rune('0'+i)) + ".jsonl"
		writeCodexFixture(t, sessDir, name, []string{
			`{"type":"session_meta","timestamp":"2026-08-24T09:00:00.000Z","payload":{"cwd":"` + cwd + `","model_provider":"openai","originator":"codex-tui"}}`,
			`{"type":"turn_context","timestamp":"2026-08-24T09:00:01.000Z","payload":{"cwd":"` + cwd + `","model":"gpt-5"}}`,
		})
	}

	state, _ := syncer.LoadState(filepath.Join(t.TempDir(), "state.json"))
	cs := New([]string{home}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "token", ""), nil)
	ctx := context.Background()
	if _, err := cs.SyncOnce(ctx); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	clear(calls)

	for pass := range 2 {
		if _, err := cs.SyncOnce(ctx); err != nil {
			t.Fatalf("SyncOnce %d: %v", pass, err)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("resolved cwds = %v, want /work/a and /work/b", calls)
	}
	for cwd, n := range calls {
		if n != 1 {
			t.Errorf("%s resolved %d times over two passes of %d files, want 1", cwd, n, len(cwds))
		}
	}

	// Past the TTL the metadata is read again, once per cwd.
	now = now.Add(gitMetaTTL)
	if _, err := cs.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce after TTL: %v", err)
	}
	for cwd, n := range calls {
		if n != 2 {
			t.Errorf("%s resolved %d times after the TTL, want 2", cwd, n)
		}
	}
}

// Records carry the commit and branch they were made on, so the path that sends
// them must not take its git metadata from the cache: a commit made within the
// TTL would be attributed to the one before it.
func TestSyncOnce_recordSendResolvesGitEveryPass(t *testing.T) {
	calls := 0
	prevResolve := resolveGit
	resolveGit = func(cwd string) gitctx.Context {
		calls++
		return gitctx.Context{RepositoryID: "local:" + cwd}
	}
	t.Cleanup(func() { resolveGit = prevResolve })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"inserted":1,"skipped":0}`))
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	sessDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := writeCodexFixture(t, sessDir, "rollout-2026-08-24T09-00-00-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl", []string{
		`{"type":"session_meta","timestamp":"2026-08-24T09:00:00.000Z","payload":{"cwd":"/work/a","model_provider":"openai","originator":"codex-tui"}}`,
		`{"type":"turn_context","timestamp":"2026-08-24T09:00:01.000Z","payload":{"cwd":"/work/a","model":"gpt-5"}}`,
	})
	appendUserTurn := func(ts string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(`{"type":"response_item","timestamp":"` + ts + `","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}` + "\n"); err != nil {
			t.Fatal(err)
		}
	}

	state, _ := syncer.LoadState(filepath.Join(t.TempDir(), "state.json"))
	cs := New([]string{home}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "token", ""), nil)
	ctx := context.Background()
	if _, err := cs.SyncOnce(ctx); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	calls = 0

	for i, ts := range []string{"2026-08-24T09:00:02.000Z", "2026-08-24T09:00:03.000Z"} {
		appendUserTurn(ts)
		n, err := cs.SyncOnce(ctx)
		if err != nil {
			t.Fatalf("SyncOnce %d: %v", i, err)
		}
		if n == 0 {
			t.Fatalf("SyncOnce %d sent no records; the test is not exercising the send path", i)
		}
	}
	if calls != 2 {
		t.Errorf("resolveGit called %d times over two passes that sent records, want 2", calls)
	}
}

// rulesScanFixture writes three unchanged session files in one cwd whose rules
// are scannable, and returns a syncer pointed at a server that records every
// project-rules request, after the passes that send the rules once. resolveGit
// is left to the caller.
func rulesScanFixture(t *testing.T, collectPrefixes []string) (*CodexSyncer, *[]store.ProjectRuleIngestRequest) {
	t.Helper()
	var sent []store.ProjectRuleIngestRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/project-rules" {
			var req store.ProjectRuleIngestRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode project-rules body: %v", err)
			}
			sent = append(sent, req)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	home := t.TempDir()
	sessDir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessDir, 0755); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		name := "rollout-2026-08-24T09-00-0" + string(rune('0'+i)) + "-019db82c-66c5-7160-a6a7-b76dc2dd72c" + string(rune('0'+i)) + ".jsonl"
		writeCodexFixture(t, sessDir, name, []string{
			`{"type":"session_meta","timestamp":"2026-08-24T09:00:00.000Z","payload":{"cwd":"/work/app","model_provider":"openai","originator":"codex-tui"}}`,
			`{"type":"turn_context","timestamp":"2026-08-24T09:00:01.000Z","payload":{"cwd":"/work/app","model":"gpt-5"}}`,
		})
	}
	state, _ := syncer.LoadState(filepath.Join(t.TempDir(), "state.json"))
	cs := New([]string{home}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "token", ""), collectPrefixes)
	// The first pass skips files that pre-date it; the next one scans their rules.
	for pass := range 2 {
		if _, err := cs.SyncOnce(context.Background()); err != nil {
			t.Fatalf("SyncOnce %d: %v", pass, err)
		}
	}
	if len(sent) != 1 {
		t.Fatalf("setup passes sent %d rule requests, want 1", len(sent))
	}
	return cs, &sent
}

// The cache only decides that a scan is due. A cwd whose repository was
// replaced within gitMetaTTL by one outside the allowlist must not have its
// current rule files sent under the identity of the repository it used to be.
func TestSyncOnce_ruleScanRechecksAllowlistOnFreshGit(t *testing.T) {
	root := ruleRepo(t)
	meta := gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/example-org/cctrace", CommitSHA: "aaa", Branch: "main"}
	prevResolve := resolveGit
	resolveGit = func(string) gitctx.Context { return meta }
	t.Cleanup(func() { resolveGit = prevResolve })
	advance := useFakeClock(t)

	cs, sent := rulesScanFixture(t, []string{"github.com/example-org/"})
	ctx := context.Background()

	meta = gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/org/repo", CommitSHA: "bbb", Branch: "main"}
	advance(ruleScanTTL)
	if _, err := cs.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce after the repository changed: %v", err)
	}
	if extra := (*sent)[1:]; len(extra) != 0 {
		t.Fatalf("rules sent after the cwd left the allowlist: %+v", extra)
	}
}

// A scan that is due sends the commit and branch HEAD has now, not the ones
// cached up to gitMetaTTL ago, and resolves git once for it rather than once
// per session file.
func TestSyncOnce_ruleScanSendsFreshCommitAndBranch(t *testing.T) {
	root := ruleRepo(t)
	meta := gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/example-org/cctrace", CommitSHA: "aaa", Branch: "main"}
	calls := 0
	prevResolve := resolveGit
	resolveGit = func(string) gitctx.Context { calls++; return meta }
	t.Cleanup(func() { resolveGit = prevResolve })
	advance := useFakeClock(t)

	cs, sent := rulesScanFixture(t, nil)
	ctx := context.Background()

	meta.CommitSHA, meta.Branch = "bbb", "feature"
	advance(ruleScanTTL)
	calls = 0
	if _, err := cs.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce after HEAD moved: %v", err)
	}
	if len(*sent) != 2 {
		t.Fatalf("sent %d rule requests in total, want 2 (one scan per pass past ruleScanTTL)", len(*sent))
	}
	if got := (*sent)[1]; got.CommitSHA != "bbb" || got.Branch != "feature" {
		t.Errorf("rules sent with commit %q branch %q, want bbb feature", got.CommitSHA, got.Branch)
	}
	if calls != 1 {
		t.Errorf("resolveGit called %d times for one due scan over 3 files, want 1", calls)
	}
}
