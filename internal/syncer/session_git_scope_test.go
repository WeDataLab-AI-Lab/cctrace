package syncer

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// installGitWrapper puts a git on PATH that records each call's arguments, a
// line per call, runs script -- a shell snippet that may stand in for git --
// and then the real git. It returns a reader of the recorded calls.
//
// It runs the wrapper once before it returns, and the reader never lists that
// call: on a loaded machine the first run of a script just written can take
// longer than the two seconds gitctx gives a call, and a count whose first git
// call it was would get no answer.
func installGitWrapper(t *testing.T, script string) func() []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git wrapper is a shell script")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + calls + "'\n" + script + "exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The warm-up: how it ends does not matter, and the line it logged is dropped.
	_ = exec.Command(filepath.Join(dir, "git"), "--version").Run()
	_ = os.Remove(calls)
	return func() []string {
		b, _ := os.ReadFile(calls)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// callsWith counts the recorded calls that hold every one of parts.
func callsWith(calls []string, parts ...string) int {
	n := 0
	for _, c := range calls {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(c, p)
		}
		if all {
			n++
		}
	}
	return n
}

// cwdLine is a user line that names the session's working directory.
func cwdLine(sid, uuid string, at time.Time, cwd string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "user", "uuid": uuid, "timestamp": at.UTC().Format(time.RFC3339), "sessionId": sid, "cwd": cwd,
		"message": map[string]any{"role": "user", "content": "hi"},
	})
	return string(b)
}

// namedRepo is a work tree in a directory called name, with modified.txt
// committed and then changed, and origin set to remote unless it is "".
func namedRepo(t *testing.T, name, remote string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "git-state@example.test")
	runGit(t, repo, "config", "user.name", "Git State")
	if remote != "" {
		runGit(t, repo, "remote", "add", "origin", remote)
	}
	writeFile(t, filepath.Join(repo, "modified.txt"), "modified.txt\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "fixture")
	writeFile(t, filepath.Join(repo, "modified.txt"), "changed\n")
	return repo
}

// waitGitJobs waits until no counting job is running, abandoned ones included.
func waitGitJobs(t *testing.T, s *Syncer) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.gitJobs.mu.Lock()
		n := len(s.gitJobs.running)
		s.gitJobs.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d git jobs still running", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// allowlistSyncer is a Syncer over claudeDir that collects github.com/org/allowed alone.
func allowlistSyncer(t *testing.T, claudeDir, endpoint string) (*Syncer, *State) {
	t.Helper()
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	return New(claudeDir, "p@example.com", "u1", state, NewClient(endpoint, "", ""), []string{"github.com/org/allowed"}), state
}

// With options.collect_repository_prefixes set, only the edited files in
// repositories it collects are counted. One in a repository it leaves out, or
// in none, is not counted at all, and git status never runs in that repository.
func TestSessionGitLeavesRepositoriesOutsideTheAllowlistAlone(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	allowed := namedRepo(t, "allowed-repo", "https://github.com/org/allowed.git")
	denied := namedRepo(t, "denied-repo", "https://github.com/org/elsewhere.git")
	outside := filepath.Join(t.TempDir(), "outside-file.txt")
	writeFile(t, outside, "x\n")
	calls := installGitWrapper(t, "")
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-allowlist"
	path, _ := writeTranscript(t, claudeDir, sid,
		cwdLine(sid, "u-1", at, allowed),
		editLine(sid, "a-1", at, "Edit", filepath.Join(allowed, "modified.txt")),
		editLine(sid, "a-2", at, "Edit", filepath.Join(denied, "modified.txt")),
		editLine(sid, "a-3", at, "Write", outside),
	)
	s, _ := allowlistSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 1, UncommittedEditedFiles: 1, Repositories: 1}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Fatalf("git counts %+v, want %+v", got, want)
	}
	if n := callsWith(calls(), "allowed-repo", " status "); n == 0 {
		t.Error("git status never ran in the allowed repository")
	}
	if n := callsWith(calls(), "denied-repo", " status "); n != 0 {
		t.Errorf("git status ran %d times in a repository the allowlist leaves out", n)
	}

	// Counted again once known to be left out, it is still not asked.
	appendLines(t, path, cwdLine(sid, "u-2", at, allowed), editLine(sid, "a-4", at, "Write", filepath.Join(denied, "new.txt")))
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := callsWith(calls(), "denied-repo", " status "); n != 0 {
		t.Errorf("git status ran %d times in a repository known to be left out", n)
	}
}

// A main transcript sync never collected -- every pass of it in a repository
// the allowlist leaves out -- gets no git snapshot, though a subagent of its
// session edited files in an allowed one.
func TestSessionGitNeverCollectedMainGetsNoSnapshot(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	allowed := namedRepo(t, "allowed-repo", "https://github.com/org/allowed.git")
	denied := namedRepo(t, "denied-repo", "https://github.com/org/elsewhere.git")
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-never-collected"
	main, _ := writeTranscript(t, claudeDir, sid, cwdLine(sid, "u-1", at, denied))
	writeSubagentTranscript(t, claudeDir, sid, "agent-a1",
		cwdLine(sid, "s-0", at, allowed), editLine(sid, "s-1", at, "Edit", filepath.Join(allowed, "modified.txt")))
	s, state := allowlistSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if gs := state.Files[main].SessionGit; gs == nil {
		t.Fatal("fixture: the subagent's edit did not reach the session")
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := state.Files[main].SessionSnapshot; st != nil {
		t.Errorf("a main transcript never collected got a snapshot: %+v", st.Snapshot)
	}
	if gs := state.Files[main].SessionGit; gs.Pending || gs.CheckedAt.IsZero() {
		t.Errorf("the session stays due, so git would run for it every flush: %+v", gs)
	}
	for _, body := range srv.bodies() {
		if strings.Contains(body, sid) {
			t.Errorf("the session was sent: %s", body)
		}
	}
}

// The counts say they are partial when the client never read some of the
// session's edits, and a partial count is read as a lower bound.
func TestSessionGitCountsArePartialWhenEditsWentUnread(t *testing.T) {
	for _, tc := range []struct {
		name    string
		partial bool
		run     func(t *testing.T, claudeDir, sid, repo string, s *Syncer, state *State, advance func(time.Duration))
	}{
		{"read from the start", false, func(t *testing.T, claudeDir, sid, repo string, s *Syncer, state *State, _ func(time.Duration)) {
			at := nowFn().Add(-time.Hour)
			writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
			mustSync(t, s)
		}},
		{"read from past its start", true, func(t *testing.T, claudeDir, sid, repo string, s *Syncer, state *State, _ func(time.Duration)) {
			at := nowFn().Add(-time.Hour)
			path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "committed.txt")))
			fi, _ := os.Stat(path)
			state.SetOffset(path, fi.Size()) // collected before this client
			appendLines(t, path, editLine(sid, "a-2", at, "Edit", filepath.Join(repo, "modified.txt")))
			mustSync(t, s)
		}},
		{"collected part not at its start", true, func(t *testing.T, claudeDir, sid, repo string, s *Syncer, state *State, _ func(time.Duration)) {
			at := nowFn().Add(-time.Hour)
			first := convLine(sid, "u-1", at)
			path, _ := writeTranscript(t, claudeDir, sid, first, editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "committed.txt")))
			mustSync(t, s)
			if err := os.Truncate(path, int64(len(first)+1)); err != nil {
				t.Fatal(err)
			}
			mustSync(t, s) // finds it shrunk: collection restarts there
			appendLines(t, path, editLine(sid, "a-2", at, "Edit", filepath.Join(repo, "modified.txt")))
			mustSync(t, s)
		}},
		{"begun again after the window", true, func(t *testing.T, claudeDir, sid, repo string, s *Syncer, state *State, advance func(time.Duration)) {
			at := nowFn().Add(-time.Hour)
			path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "committed.txt")))
			mustSync(t, s)
			if err := s.FlushObservations(context.Background()); err != nil {
				t.Fatal(err)
			}
			advance(sessionGitWindow + time.Hour)
			if err := s.FlushObservations(context.Background()); err != nil {
				t.Fatal(err)
			}
			if state.Files[path].SessionGit != nil {
				t.Fatal("fixture: the paths were not dropped")
			}
			later := nowFn().Add(-time.Minute)
			appendLines(t, path, editLine(sid, "a-2", later, "Edit", filepath.Join(repo, "modified.txt")))
			mustSync(t, s)
		}},
		{"subagent read while the session had no main transcript", true, func(t *testing.T, claudeDir, sid, repo string, s *Syncer, state *State, _ func(time.Duration)) {
			at := nowFn().Add(-time.Hour)
			writeSubagentTranscript(t, claudeDir, sid, "agent-a1", editLine(sid, "s-1", at, "Edit", filepath.Join(repo, "committed.txt")))
			mustSync(t, s)
			writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
			mustSync(t, s)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			advance := fakeClock(t)
			repo := gitStateRepo(t)
			srv := newObservationServer(t)
			claudeDir := t.TempDir()
			const sid = "sess-git-partial"
			s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
			tc.run(t, claudeDir, sid, repo, s, state, advance)
			if err := s.FlushObservations(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := sentGit(srv.requests(), sid)
			if got == nil || got.Partial != tc.partial || got.UncommittedEditedFiles != 1 {
				t.Errorf("git counts %+v, want partial %v and the modified file uncommitted", got, tc.partial)
			}
		})
	}
}

// Re-enrichment fills the set of a session active within the window from the
// part sync collected before this client read its edits, so its counts are
// whole; a session past the window is left alone.
func TestReenrichFillsTheEditsOfARecentSession(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	old := nowFn().Add(-10 * 24 * time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const recent, stale = "sess-git-recent", "sess-git-stale"
	recentPath, _ := writeTranscript(t, claudeDir, recent, convLine(recent, "u-1", at), editLine(recent, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	stalePath, _ := writeTranscript(t, claudeDir, stale, convLine(stale, "u-2", old), editLine(stale, "a-2", old, "Edit", filepath.Join(repo, "committed.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	for _, path := range []string{recentPath, stalePath} {
		fi, _ := os.Stat(path)
		state.SetOffset(path, fi.Size()) // collected before this client
	}
	if _, err := s.ReenrichOnce(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if fs := state.Files[recentPath]; fs.SessionGit == nil || len(fs.SessionGit.Edited) != 1 || fs.EditsFrom == nil || *fs.EditsFrom != 0 {
		t.Fatalf("recent session: git state %+v, edits from %v; want its edit, read from the start", fs.SessionGit, fs.EditsFrom)
	}
	if fs := state.Files[stalePath]; fs.SessionGit != nil {
		t.Errorf("a session past the window was filled: %+v", fs.SessionGit)
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 1, UncommittedEditedFiles: 1, Repositories: 1}
	if got := sentGit(srv.requests(), recent); got == nil || *got != want {
		t.Errorf("git counts %+v, want %+v, not partial", got, want)
	}
}

// git is not asked about a session whose snapshot would go under an excluded
// account -- the account of its newest record, whoever is signed in now: that
// snapshot is consumed unsent anyway.
func TestSessionGitLeavesAnExcludedAccountsSessionAlone(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := namedRepo(t, "excluded-repo", "")
	calls := installGitWrapper(t, "")
	srv := newObservationServer(t)
	srv.setExclusions(http.StatusOK, map[string]bool{"anthropic:acct-personal": true})
	claudeDir := t.TempDir()
	const sid = "sess-git-excluded"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	state.ObserveClaudeAccount(at.Add(-time.Hour), normalizeHome(claudeDir), "acct-personal")
	mustSync(t, s)
	// Signed in to another account by the time the files would be counted.
	state.ObserveClaudeAccount(nowFn().Add(-time.Minute), normalizeHome(claudeDir), "acct-work")
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := callsWith(calls(), "excluded-repo"); n != 0 {
		t.Errorf("git ran %d times for an excluded account's session", n)
	}
	for _, body := range srv.bodies() {
		if strings.Contains(body, sid) {
			t.Errorf("the excluded account's session was sent: %s", body)
		}
	}

	// Settled, not left due for ever: past its window its paths go, as a
	// counted session's do.
	advance(sessionGitWindow)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gs := state.Files[path].SessionGit; gs != nil {
		t.Errorf("an excluded account's session keeps its %d paths past its window", len(gs.Edited))
	}
}

// A set begun again after its paths were dropped is partial until
// re-enrichment fills it from the start of every transcript of the session.
func TestReenrichMakesASetBegunAgainWhole(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-whole-again"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	advance(sessionGitWindow)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	appendLines(t, path, editLine(sid, "a-2", nowFn().Add(-time.Minute), "Edit", filepath.Join(repo, "renamed.txt")))
	mustSync(t, s)
	if gs := state.Files[path].SessionGit; gs == nil || !gs.Partial {
		t.Fatalf("fixture: git state %+v, want a set begun again, partial", gs)
	}
	if _, err := s.ReenrichOnce(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 2, UncommittedEditedFiles: 2, Repositories: 1}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Errorf("git counts %+v, want %+v: whole again", got, want)
	}
}

// The repository counting looks up for a work tree stays in counting's own
// cache: the record sync's cache decides which records are collected.
func TestSessionGitKeepsItsRepositoryLookupsToItself(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	allowed := namedRepo(t, "allowed-repo", "https://github.com/org/allowed.git")
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-own-cache"
	writeTranscript(t, claudeDir, sid, cwdLine(sid, "u-1", at, allowed), editLine(sid, "a-1", at, "Edit", filepath.Join(allowed, "modified.txt")))
	s, _ := allowlistSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	recordCache := func() map[string]*projectMeta {
		s.mu.Lock()
		defer s.mu.Unlock()
		return maps.Clone(s.cwdCache)
	}
	before := recordCache()
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.gitRepos) == 0 {
		t.Fatal("fixture: counting looked up no repository")
	}
	if after := recordCache(); !maps.Equal(before, after) {
		t.Errorf("counting changed the record sync's cache: %d entries before, %d after", len(before), len(after))
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 1, UncommittedEditedFiles: 1, Repositories: 1}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Errorf("git counts %+v, want %+v", got, want)
	}
}

// A repository lookup git gives no answer to -- here it is killed -- is no
// answer, not a repository without an origin: the counts and the revision stay.
func TestSessionGitKeepsTheCountsWhenTheRepositoryLookupGivesNoAnswer(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	allowed := namedRepo(t, "allowed-repo", "https://github.com/org/allowed.git")
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-lookup-no-answer"
	path, _ := writeTranscript(t, claudeDir, sid, cwdLine(sid, "u-1", at, allowed), editLine(sid, "a-1", at, "Edit", filepath.Join(allowed, "modified.txt")))
	s, state := allowlistSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := snapshotState(t, state, path)
	before, revision := *st.Snapshot.Git, st.Revision
	calls := installGitWrapper(t, "case \"$*\" in *\"remote get-url\"*) kill -9 $$;; esac\n")
	advance(metaTTL)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if callsWith(calls(), "remote get-url") == 0 {
		t.Fatal("the repository was not looked up again metaTTL later")
	}
	st = snapshotState(t, state, path)
	if *st.Snapshot.Git != before || st.Revision != revision {
		t.Errorf("after a lookup with no answer: counts %+v revision %d, want %+v and %d", *st.Snapshot.Git, st.Revision, before, revision)
	}
}

// A session whose last record is past the window when it is first read is
// counted once, and its paths are dropped after that.
func TestSessionGitCountsALateReadSessionOnce(t *testing.T) {
	advance := fakeClock(t)
	old := nowFn().Add(-10 * 24 * time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-late"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", old), editLine(sid, "a-1", old, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sentGit(srv.requests(), sid); got == nil || got.UncommittedEditedFiles != 1 {
		t.Fatalf("git counts %+v, want the late-read session counted once", got)
	}
	advance(time.Minute)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.Files[path].SessionGit != nil {
		t.Error("the paths of a session past the window stayed after it was counted")
	}
}
