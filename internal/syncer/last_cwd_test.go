package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/gitctx"
)

// The SyncOnce tests in this file swap resolveGit and nowFn, so they cannot run
// in parallel with the rest of the package.

// shrinkLastCWDWindow makes the backward search start with a window smaller
// than one line and stop at max, so growing and giving up are both reachable
// with a file of a few lines.
func shrinkLastCWDWindow(t *testing.T, max int64) {
	t.Helper()
	prevWindow, prevMax := lastCWDWindow, lastCWDWindowMax
	lastCWDWindow, lastCWDWindowMax = 16, max
	t.Cleanup(func() { lastCWDWindow, lastCWDWindowMax = prevWindow, prevMax })
}

func TestLastCWDBefore(t *testing.T) {
	a, b, bare := sessionLine("/work/a", "1")+"\n", sessionLine("/work/b", "2")+"\n", sessionLine("", "meta")+"\n"
	cases := []struct {
		name    string
		content string
		offset  int64
		want    string
	}{
		{"last line carries it", a + b, int64(len(a + b)), "/work/b"},
		{"lines without one are passed over", a + b + bare + bare, int64(len(a + b + bare + bare)), "/work/b"},
		{"only lines before the offset count", a + bare + b, int64(len(a + bare)), "/work/a"},
		{"a line that is not JSON is passed over", a + "not json\n\n", int64(len(a) + 10), "/work/a"},
		{"no line carries one", bare + bare, int64(len(bare + bare)), ""},
		{"nothing before the offset", a, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "s.jsonl")
			if err := os.WriteFile(p, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := lastCWDBefore(p, tc.offset); got != tc.want {
				t.Fatalf("lastCWDBefore = %q, want %q", got, tc.want)
			}
		})
	}
}

// The search reads a window ending at the offset and widens it fourfold while
// it finds nothing, so a long stretch of metadata lines does not hide the cwd
// before it -- up to a bound, past which the cwd counts as unknown rather than
// the whole file being read.
func TestLastCWDBefore_widensUpToABound(t *testing.T) {
	a, bare := sessionLine("/work/a", "1")+"\n", sessionLine("", "meta")+"\n"
	content := a + strings.Repeat(bare, 5)
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	shrinkLastCWDWindow(t, int64(len(content))*4)
	if got := lastCWDBefore(p, int64(len(content))); got != "/work/a" {
		t.Fatalf("lastCWDBefore = %q, want /work/a found by widening", got)
	}

	lastCWDWindowMax = int64(len(bare))
	if got := lastCWDBefore(p, int64(len(content))); got != "" {
		t.Fatalf("lastCWDBefore = %q past the bound, want unknown", got)
	}
}

// Every advance that read records leaves the cwd they ended in on the file's
// state, including one carried over lines that have none.
func TestAdvanceRecordsLastCWD(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(allowedRepo)})
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p := filepath.Join(dir, "s1.jsonl")

	appendLines(t, p, sessionLine("/work/a", "1"), sessionLine("/work/b", "2"), sessionLine("", "meta"))
	syncPass(t, s)
	if got := reloadState(t, s).Files[p].CWD; got != "/work/b" {
		t.Fatalf("CWD = %q after a tail ending in /work/b, want /work/b", got)
	}

	appendLines(t, p, sessionLine("", "meta"))
	syncPass(t, s)
	if got := reloadState(t, s).Files[p].CWD; got != "/work/b" {
		t.Fatalf("CWD = %q after a tail without one, want /work/b kept", got)
	}
}

// newFirstRunSyncer returns a syncer whose state does not exist yet, so its
// first pass skips every file already on disk.
func newFirstRunSyncer(t *testing.T, endpoint string, collectPrefixes []string) (*Syncer, string) {
	t.Helper()
	claudeDir := t.TempDir()
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	dir := filepath.Join(claudeDir, "projects", "-proj-a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	return New(claudeDir, "user@example.com", "u1", state, NewClient(endpoint, "", ""), collectPrefixes), dir
}

// The first sync skips a file to its end without reading it. The cwd its
// skipped bytes ended in is read back from the file, because the lines that
// follow may not say: the head of the file is where the session started, not
// where it is.
func TestFirstRunSkipRecoversLastCWD(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{})
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newFirstRunSyncer(t, endpoint, allowExampleOrg)
	moved, bare := filepath.Join(dir, "moved.jsonl"), filepath.Join(dir, "bare.jsonl")
	appendLines(t, moved, sessionLine("/work/a", "1"), sessionLine("/work/b", "2"), sessionLine("", "meta"))
	appendLines(t, bare, sessionLine("", "meta"))

	syncPass(t, s)

	st := reloadState(t, s)
	if fs := st.Files[moved]; fs.CWD != "/work/b" || fs.CWDUnknown {
		t.Fatalf("moved: CWD = %q unknown = %v, want /work/b", fs.CWD, fs.CWDUnknown)
	}
	if fs := st.Files[bare]; fs.CWD != "" || !fs.CWDUnknown {
		t.Fatalf("bare: CWD = %q unknown = %v, want unknown", fs.CWD, fs.CWDUnknown)
	}
}

// A shrunk file's offset is reset to its new size, again without reading. The
// cwd recorded for the old content says nothing about the remnant, so it is
// replaced by the remnant's last cwd, or marked unknown.
func TestShrinkResetRecoversLastCWD(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p, q := filepath.Join(dir, "s1.jsonl"), filepath.Join(dir, "s2.jsonl")
	for _, f := range []string{p, q} {
		appendLines(t, f, sessionLine("/work/a", "1"), sessionLine("/work/a", "2"), sessionLine("/work/a", "3"))
	}
	syncPass(t, s)
	if got := s.state.Files[p].CWD; got != "/work/a" {
		t.Fatalf("CWD = %q before the rewrite, want /work/a", got)
	}

	if err := os.WriteFile(p, []byte(sessionLine("/work/b", "remnant")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.WriteFile(q, []byte(sessionLine("", "meta")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	syncPass(t, s)

	st := reloadState(t, s)
	if fs := st.Files[p]; fs.CWD != "/work/b" || fs.CWDUnknown {
		t.Fatalf("remnant with a cwd: CWD = %q unknown = %v, want /work/b", fs.CWD, fs.CWDUnknown)
	}
	if fs := st.Files[q]; fs.CWD != "" || !fs.CWDUnknown {
		t.Fatalf("remnant without a cwd: CWD = %q unknown = %v, want unknown", fs.CWD, fs.CWDUnknown)
	}
}

// Without an allowlist nothing reads the recorded cwd, so the two unread
// advances do not read the file for it either: they clear what is recorded,
// and it is recovered when an allowlist first needs it.
func TestUnreadAdvanceWithoutAllowlistOnlyClearsCWD(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"), sessionLine("/work/a", "2"))
	syncPass(t, s)

	if err := os.WriteFile(p, []byte(sessionLine("/work/b", "remnant")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	syncPass(t, s)
	if fs := reloadState(t, s).Files[p]; fs.CWD != "" || fs.CWDUnknown {
		t.Fatalf("CWD = %q unknown = %v after a shrink without an allowlist, want cleared", fs.CWD, fs.CWDUnknown)
	}
}
