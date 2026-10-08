package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/gitctx"
	"cctrace/internal/jsonlscan"
)

// Helpers for the tests that drive a session file's tail through SyncOnce with
// a stubbed git. Those tests swap resolveGit and nowFn, so none of them can run
// in parallel with another test of this package.

const (
	// allowedRepo and allowedRepoTwo match allowExampleOrg; excludedRepo does not.
	allowedRepo    = "github.com/example-org/cctrace"
	allowedRepoTwo = "github.com/example-org/other-repo"
	excludedRepo   = "github.com/org/repo"
)

var allowExampleOrg = []string{"github.com/example-org/"}

// repoAt is a certain lookup of a checkout of id.
func repoAt(id string) gitctx.Context {
	return gitctx.Context{RepositoryID: id, RepositoryIDSource: "resolved", RepositoryName: gitctx.RepositoryNameFromID(id)}
}

// notARepo is what a certain lookup of a directory outside any repository
// returns: a non-empty, hash-only local id marked as a fallback.
func notARepo() gitctx.Context {
	return gitctx.Context{RepositoryID: "local:89abcdef01234567", RepositoryIDSource: "fallback"}
}

// uncertainLookup is what gitctx.ResolveChecked returns when git timed out on
// cwd: the same kind of non-empty local fallback, and an error saying it is
// not a fact about cwd.
func uncertainLookup(string) (gitctx.Context, error) {
	return gitctx.Context{RepositoryID: "local:0123456789abcdef", RepositoryIDSource: "fallback"},
		fmt.Errorf("%w: git rev-parse --show-toplevel exited -1", gitctx.ErrUncertain)
}

// stubResolveGitChecked answers every git lookup from resolve, which can also
// report an uncertain lookup, and counts the calls per cwd.
func stubResolveGitChecked(t *testing.T, resolve func(cwd string) (gitctx.Context, error)) map[string]int {
	t.Helper()
	calls := map[string]int{}
	prev := resolveGit
	resolveGit = func(cwd string) (gitctx.Context, error) {
		calls[cwd]++
		return resolve(cwd)
	}
	t.Cleanup(func() { resolveGit = prev })
	return calls
}

// stubRepos answers each cwd's lookup from repos, which the test may change
// between passes. A cwd missing from repos is an uncertain lookup.
func stubRepos(t *testing.T, repos map[string]gitctx.Context) map[string]int {
	t.Helper()
	return stubResolveGitChecked(t, func(cwd string) (gitctx.Context, error) {
		if g, ok := repos[cwd]; ok {
			return g, nil
		}
		return uncertainLookup(cwd)
	})
}

// newTailSyncer returns a syncer whose state already exists, so a session file
// created afterwards is collected from its first byte, and the directory to
// create session files in.
func newTailSyncer(t *testing.T, endpoint string, collectPrefixes []string) (*Syncer, string) {
	t.Helper()
	claudeDir := t.TempDir()
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}
	dir := filepath.Join(claudeDir, "projects", "-proj-a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	return New(claudeDir, "user@example.com", "u1", state, NewClient(endpoint, "", ""), collectPrefixes), dir
}

// sessionDir creates another project directory next to dir, the one
// newTailSyncer returned.
func sessionDir(t *testing.T, dir, name string) string {
	t.Helper()
	d := filepath.Join(filepath.Dir(dir), name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	return d
}

// sessionLine is one user record in cwd saying text. An empty cwd writes a
// record without one, as Claude's metadata lines are.
func sessionLine(cwd, text string) string {
	cwdField := ""
	if cwd != "" {
		cwdField = `"cwd":` + strconvQuote(cwd) + `,`
	}
	return `{"type":"user","timestamp":"` + nowFn().UTC().Format(time.RFC3339Nano) + `","sessionId":"s1",` + cwdField + `"message":{"role":"user","content":` + strconvQuote(text) + `}}`
}

// lineWith is one record of the given type with extra top-level fields
// (written as JSON members, each ending in a comma) ahead of the message.
func lineWith(recordType, extra, text string) string {
	return `{"type":"` + recordType + `","sessionId":"s1",` + extra + `"message":{"role":"user","content":` + strconvQuote(text) + `}}`
}

// retryHeld runs n passes, each one retry interval after the last.
func retryHeld(t *testing.T, s *Syncer, advance func(time.Duration), n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		advance(lookupRetryInterval)
		syncPass(t, s)
	}
}

// oversizedLine narrows the scanner's line limit for the test and returns a
// record in cwd that is over it, so the scan skips the line without yielding
// it. The real limit is 16MiB; nothing here depends on the size.
func oversizedLine(t *testing.T, cwd string) string {
	t.Helper()
	prev := jsonlscan.MaxLineBytes
	jsonlscan.MaxLineBytes = 1024
	t.Cleanup(func() { jsonlscan.MaxLineBytes = prev })
	return sessionLine(cwd, strings.Repeat("x", 2048))
}

// appendLines appends each line with its newline, creating the file.
func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		appendBytes(t, path, l+"\n")
	}
}

// appendBytes appends s as it is, so a test can leave a last line incomplete.
func appendBytes(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatalf("append session: %v", err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return fi.Size()
}

// expireMetaCache stands in for metaTTL running out: cwdCache ages on the wall
// clock, which the fake clock does not move.
func expireMetaCache(s *Syncer) {
	s.mu.Lock()
	clear(s.cwdCache)
	s.mu.Unlock()
}

func (fs *freshMetaServer) sentSyncs() []SyncPayload {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]SyncPayload(nil), fs.syncs...)
}

// recordPosts returns the live sync requests that carried records, leaving out
// the metadata-only upserts of an idle pass and re-enrichment.
func (fs *freshMetaServer) recordPosts() []SyncPayload {
	var posts []SyncPayload
	for _, p := range fs.sentSyncs() {
		if len(p.Records) > 0 && !p.Reenrich {
			posts = append(posts, p)
		}
	}
	return posts
}

// sentTexts returns the message text of every record sent, in order.
func (fs *freshMetaServer) sentTexts(t *testing.T) []string {
	t.Helper()
	var texts []string
	for _, p := range fs.recordPosts() {
		for _, r := range p.Records {
			texts = append(texts, recordText(t, r.Raw))
		}
	}
	return texts
}

// recordText returns the message text sessionLine wrote into a record.
func recordText(t *testing.T, raw []byte) string {
	t.Helper()
	var r struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode sent record: %v", err)
	}
	return r.Message.Content
}

// refuseText makes the server answer 503 to any request carrying a record
// whose text is text, until the returned function is called.
func (fs *freshMetaServer) refuseText(t *testing.T, text string) (stop func()) {
	t.Helper()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.refuse = func(p SyncPayload) bool {
		for _, r := range p.Records {
			if recordText(t, r.Raw) == text {
				return true
			}
		}
		return false
	}
	return func() {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		fs.refuse = nil
	}
}

// syncPassFailing runs a pass in which a file is expected to fail its send.
func syncPassFailing(t *testing.T, s *Syncer) {
	t.Helper()
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if s.LastPass().FilesFailed == 0 {
		t.Fatal("pass reported no failed file, want the refused send counted")
	}
}

// restartSyncer returns a new syncer over the same Claude home and the state
// the old one saved, as a restarted daemon would have.
func restartSyncer(t *testing.T, s *Syncer) *Syncer {
	t.Helper()
	return New(s.claudeDir, s.profileEmail, s.userID, reloadState(t, s), s.client, s.collectPrefixes)
}

// reenrich runs one re-enrichment and returns the text of every record it
// posted.
func (fs *freshMetaServer) reenrich(t *testing.T, s *Syncer) []string {
	t.Helper()
	before := len(fs.sentSyncs())
	if _, err := s.ReenrichOnce(context.Background()); err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}
	var texts []string
	for _, p := range fs.sentSyncs()[before:] {
		if !p.Reenrich {
			t.Fatalf("re-enrichment sent a live sync request: %+v", p)
		}
		for _, r := range p.Records {
			texts = append(texts, recordText(t, r.Raw))
		}
	}
	return texts
}
