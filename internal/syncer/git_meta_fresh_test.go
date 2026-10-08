package syncer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"cctrace/internal/gitctx"
	"cctrace/internal/store"
)

// The tests in this file swap the package-level resolveGit and nowFn, so none of
// them can run in parallel with another test of this package.

// stubResolveGit answers every git lookup from resolve, as certain, and counts
// the calls per cwd.
func stubResolveGit(t *testing.T, resolve func(cwd string) gitctx.Context) map[string]int {
	t.Helper()
	calls := map[string]int{}
	prev := resolveGit
	resolveGit = func(cwd string) (gitctx.Context, error) {
		calls[cwd]++
		return resolve(cwd), nil
	}
	t.Cleanup(func() { resolveGit = prev })
	return calls
}

// freshMetaServer accepts every record and rule upload and keeps what it was sent.
type freshMetaServer struct {
	mu    sync.Mutex
	syncs []SyncPayload
	rules []store.ProjectRuleIngestRequest
	// refuse, when set, answers a sync request with 503 instead of keeping it.
	refuse func(SyncPayload) bool
	// tooLarge, when set, answers a sync request with 413 instead of keeping
	// it, so the client splits the batch.
	tooLarge func(SyncPayload) bool
}

func newFreshMetaServer(t *testing.T) (*freshMetaServer, string) {
	t.Helper()
	fs := &freshMetaServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync":
			var p SyncPayload
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Errorf("decode sync body: %v", err)
			}
			fs.mu.Lock()
			refused := fs.refuse != nil && fs.refuse(p)
			tooLarge := fs.tooLarge != nil && fs.tooLarge(p)
			if !refused && !tooLarge {
				fs.syncs = append(fs.syncs, p)
			}
			fs.mu.Unlock()
			if tooLarge {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			if refused {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(syncResponse{Inserted: len(p.Records)})
		case "/api/project-rules":
			var req store.ProjectRuleIngestRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode project-rules body: %v", err)
			}
			fs.mu.Lock()
			fs.rules = append(fs.rules, req)
			fs.mu.Unlock()
			_, _ = w.Write([]byte(`{"inserted_rules":1,"inserted_versions":1}`))
		case "/api/sync/capabilities":
			_ = json.NewEncoder(w).Encode(syncCapabilities{Reenrich: true})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return fs, srv.URL
}

func (fs *freshMetaServer) sentRules() []store.ProjectRuleIngestRequest {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]store.ProjectRuleIngestRequest(nil), fs.rules...)
}

// newFreshMetaSyncer returns a syncer over one Claude session file per entry of
// cwds, each already holding one record that the first pass will send.
func newFreshMetaSyncer(t *testing.T, endpoint string, cwds []string, collectPrefixes []string) (*Syncer, []string) {
	t.Helper()
	claudeDir := t.TempDir()
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	var paths []string
	for i, cwd := range cwds {
		dir := filepath.Join(claudeDir, "projects", "-proj-"+string(rune('a'+i)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir session dir: %v", err)
		}
		p := writeClaudeSession(t, dir, "session.jsonl", cwd)
		state.SetOffset(p, 0)
		paths = append(paths, p)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}
	return New(claudeDir, "user@example.com", "u1", state, NewClient(endpoint, "", ""), collectPrefixes), paths
}

// appendClaudeRecord appends one new user record, so the next pass has something
// to send for the file.
func appendClaudeRecord(t *testing.T, path, cwd, ts string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer f.Close()
	line := `{"type":"user","timestamp":"` + ts + `","sessionId":"session.jsonl","cwd":` + strconvQuote(cwd) + `,"message":{"role":"user","content":"again"}}` + "\n"
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append session: %v", err)
	}
}

func syncPass(t *testing.T, s *Syncer) int {
	t.Helper()
	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	return n
}

// The cached metadata only decides that a rule scan is due. A cwd whose
// repository was replaced within metaTTL by one outside the allowlist must not
// have its current rule files sent under the identity of the repository it used
// to be.
func TestSyncOnce_ruleScanRechecksAllowlistOnFreshGit(t *testing.T) {
	root := ruleRepo(t)
	meta := gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/example-org/cctrace", CommitSHA: "aaa", Branch: "main"}
	stubResolveGit(t, func(string) gitctx.Context { return meta })
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, paths := newFreshMetaSyncer(t, endpoint, []string{root}, []string{"github.com/example-org/"})

	syncPass(t, s)
	if got := len(srv.sentRules()); got != 1 {
		t.Fatalf("first pass sent %d rule requests, want 1", got)
	}

	meta = gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/org/repo", CommitSHA: "bbb", Branch: "main"}
	advance(ruleScanTTL)
	appendClaudeRecord(t, paths[0], root, "2026-05-20T00:00:01Z")
	syncPass(t, s)
	if extra := srv.sentRules()[1:]; len(extra) != 0 {
		t.Fatalf("rules sent after the cwd left the allowlist: %+v", extra)
	}
}

// A scan that is due sends the commit and branch HEAD has now, not the ones
// cached up to metaTTL ago, and resolves git once for it rather than once per
// session file in the repository.
func TestSyncOnce_ruleScanSendsFreshCommitAndBranch(t *testing.T) {
	root := ruleRepo(t)
	meta := gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/example-org/cctrace", CommitSHA: "aaa", Branch: "main"}
	calls := stubResolveGit(t, func(string) gitctx.Context { return meta })
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, paths := newFreshMetaSyncer(t, endpoint, []string{root, root}, nil)

	syncPass(t, s)
	if got := len(srv.sentRules()); got != 1 {
		t.Fatalf("first pass sent %d rule requests, want 1", got)
	}

	meta.CommitSHA, meta.Branch = "bbb", "feature"
	advance(ruleScanTTL)
	for _, p := range paths {
		appendClaudeRecord(t, p, root, "2026-05-20T00:00:01Z")
	}
	clear(calls)
	syncPass(t, s)
	rules := srv.sentRules()
	if len(rules) != 2 {
		t.Fatalf("sent %d rule requests in total, want 2 (one scan per pass past ruleScanTTL)", len(rules))
	}
	if got := rules[1]; got.CommitSHA != "bbb" || got.Branch != "feature" {
		t.Errorf("rules sent with commit %q branch %q, want bbb feature", got.CommitSHA, got.Branch)
	}
	if calls[root] != 1 {
		t.Errorf("resolveGit called %d times for one due scan over %d files, want 1", calls[root], len(paths))
	}
}
