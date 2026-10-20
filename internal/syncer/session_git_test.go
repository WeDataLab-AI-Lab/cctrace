package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// editLine is an assistant line running one edit tool on path, in the shape
// Claude Code writes it.
func editLine(sid, uuid string, at time.Time, tool, path string) string {
	key := "file_path"
	if tool == "NotebookEdit" {
		key = "notebook_path"
	}
	b, _ := json.Marshal(map[string]any{
		"type": "assistant", "uuid": uuid, "timestamp": at.UTC().Format(time.RFC3339), "sessionId": sid,
		"message": map[string]any{"id": "msg-" + uuid, "role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "toolu-" + uuid, "name": tool, "input": map[string]any{key: path, "old_string": "a", "new_string": "b"}},
		}},
	})
	return string(b)
}

// writeSubagentTranscript writes <project>/<sid>/subagents/<name>.jsonl beside
// the main transcript writeTranscript writes.
func writeSubagentTranscript(t *testing.T, claudeDir, sid, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", "-tmp-snapshot", sid, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitStateRepo is a work tree with committed.txt, modified.txt, old.txt and
// deleted.txt committed, then modified.txt changed and old.txt renamed to
// renamed.txt, staged.
func gitStateRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "git-state@example.test")
	runGit(t, repo, "config", "user.name", "Git State")
	for _, name := range []string{"committed.txt", "modified.txt", "old.txt", "deleted.txt"} {
		writeFile(t, filepath.Join(repo, name), name+"\n")
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "fixture")
	writeFile(t, filepath.Join(repo, "modified.txt"), "changed\n")
	runGit(t, repo, "mv", "old.txt", "renamed.txt")
	return repo
}

// sentGit is the git counts of the last snapshot of sid the server received,
// nil when none carried any.
func sentGit(reqs []SessionObservationsRequest, sid string) *SnapshotGit {
	for i := len(reqs) - 1; i >= 0; i-- {
		for _, it := range reqs[i].Snapshots {
			if it.SessionID == sid {
				return it.Snapshot.Git
			}
		}
	}
	return nil
}

func snapshotsSent(reqs []SessionObservationsRequest) int {
	n := 0
	for _, r := range reqs {
		n += len(r.Snapshots)
	}
	return n
}

// A session's edited files are counted against git status in the work tree each
// lies in: a committed and unchanged file is not uncommitted, a modified, an
// untracked, a staged rename's destination and a deleted one are; a path in no
// repository is unresolved, a relative one is not kept, and a path through a
// symlink counts as the file it names. A subagent's edits count for its
// session. The counts are taken again metaTTL later, so a commit made after the
// session lowers them. No path ever leaves the machine.
func TestSessionGitCountsUncommittedEditedFiles(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	for _, name := range []string{"untracked.txt", "notebook.ipynb", "sub.txt", "linked.txt"} {
		writeFile(t, filepath.Join(repo, name), name+"\n")
	}
	outside := filepath.Join(t.TempDir(), "outside-file.txt")
	writeFile(t, outside, "x\n")
	link := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}

	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git"
	writeTranscript(t, claudeDir, sid,
		convLine(sid, "u-1", at),
		editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "committed.txt")),
		editLine(sid, "a-2", at, "Edit", filepath.Join(repo, "modified.txt")),
		editLine(sid, "a-3", at, "Write", filepath.Join(repo, "untracked.txt")),
		editLine(sid, "a-4", at, "MultiEdit", filepath.Join(repo, "renamed.txt")),
		editLine(sid, "a-5", at, "Edit", filepath.Join(repo, "deleted.txt")),
		editLine(sid, "a-6", at, "NotebookEdit", filepath.Join(repo, "notebook.ipynb")),
		editLine(sid, "a-7", at, "Edit", filepath.Join(link, "linked.txt")),
		editLine(sid, "a-8", at, "Write", outside),
		editLine(sid, "a-9", at, "Edit", "relative.txt"),
		editLine(sid, "a-10", at, "Edit", filepath.Join(repo, "modified.txt")),
		// Read while its permission prompt waits, before the file exists, and
		// again by its other spelling: one file.
		editLine(sid, "a-11", at, "Write", filepath.Join(link, "pending.txt")),
		editLine(sid, "a-12", at, "Edit", filepath.Join(repo, "pending.txt")),
	)
	writeSubagentTranscript(t, claudeDir, sid, "agent-a1", editLine(sid, "s-1", at, "Edit", filepath.Join(repo, "sub.txt")))
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	// Removed after its edit was read, as a session removes a file it wrote;
	// written after its line was read.
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "pending.txt"), "written\n")

	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 10, UncommittedEditedFiles: 8, Repositories: 1, Unresolved: 1}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Fatalf("git counts %+v, want %+v", got, want)
	}

	// The person commits after the session. Within metaTTL nothing is asked
	// again; past it the count comes down.
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "work")
	advance(metaTTL / 2)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := snapshotsSent(srv.requests()); n != 1 {
		t.Fatalf("%d snapshots sent, want the first alone within metaTTL", n)
	}
	advance(metaTTL / 2)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want = SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 10, UncommittedEditedFiles: 0, Repositories: 1, Unresolved: 1}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Fatalf("after the commit: git counts %+v, want %+v", got, want)
	}
	// Counted again to the same numbers, the snapshot is not sent again.
	advance(metaTTL)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := snapshotsSent(srv.requests()); n != 2 {
		t.Fatalf("%d snapshots sent, want two: unchanged counts are not sent again", n)
	}

	leaks := []string{filepath.Base(repo), filepath.Base(link), "outside-file", "committed.txt", "modified.txt", "untracked.txt",
		"renamed.txt", "deleted.txt", "notebook.ipynb", "sub.txt", "linked.txt", "relative.txt", "pending.txt"}
	for _, body := range srv.bodies() {
		for _, leak := range leaks {
			if strings.Contains(body, leak) {
				t.Errorf("a path (%s) left the machine: %s", leak, body)
			}
		}
	}
	// The time goes out as changed_at: when the counts last changed.
	if bodies := strings.Join(srv.bodies(), "\n"); strings.Count(bodies, `"changed_at"`) != 2 {
		t.Errorf("want each snapshot's counts with changed_at: %s", bodies)
	}
}

// A pass that reads an edit has the next flush count again, metaTTL or not.
func TestSessionGitCountsAgainAfterAnEdit(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-pending"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(repo, "later.txt"), "later\n")
	appendLines(t, path, editLine(sid, "a-2", at.Add(time.Minute), "Write", filepath.Join(repo, "later.txt")))
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sentGit(srv.requests(), sid); got == nil || got.EditedFiles != 2 || got.UncommittedEditedFiles != 2 {
		t.Fatalf("after a new edit: git counts %+v, want 2 edited, 2 uncommitted", got)
	}
}

// A work tree whose git status fails counts its paths unresolved.
func TestSessionGitCountsAFailedStatusAsUnresolved(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-status-fails"
	writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A corrupt index: git still names the work tree, and its status fails.
	writeFile(t, filepath.Join(repo, ".git", "index"), "not an index")
	advance(metaTTL)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 1, UncommittedEditedFiles: 0, Repositories: 1, Unresolved: 1}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Errorf("after a failed status: git counts %+v, want %+v", got, want)
	}
}

// When git cannot say which work tree a path lies in -- here it cannot run at
// all -- the session keeps the counts it has, and its revision: nothing is sent
// until git answers again.
func TestSessionGitFailedLookupLeavesThePreviousCounts(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-failing"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := snapshotState(t, state, path)
	before, revision, checked := *st.Snapshot.Git, st.Revision, state.Files[path].SessionGit.CheckedAt

	runGit(t, repo, "commit", "-am", "work")
	t.Setenv("PATH", t.TempDir())
	advance(metaTTL)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = snapshotState(t, state, path)
	if *st.Snapshot.Git != before || st.Revision != revision {
		t.Errorf("without git: counts %+v revision %d, want %+v and %d", *st.Snapshot.Git, st.Revision, before, revision)
	}
	if got := state.Files[path].SessionGit.CheckedAt; !got.Equal(checked) {
		t.Errorf("checked at %v without an answer, want %v kept: only counts taken move it", got, checked)
	}
	if n := snapshotsSent(srv.requests()); n != 1 {
		t.Errorf("%d snapshots sent, want the first alone", n)
	}
}

// When the snapshot the counts go into cannot be built -- here its transcript is
// not there to rebuild it from -- the session is not marked counted: it backs
// off as when git gives no answer, and the next try puts the counts in.
func TestSessionGitRetriesWhenItsSnapshotCannotBeBuilt(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-rebuild"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	// As a transcript collected before snapshots existed: its first counts
	// rebuild the snapshot from the file.
	fs := state.Files[path]
	fs.SessionSnapshot, fs.NoSessionSnapshot = nil, false
	if err := os.Rename(path, path+".away"); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gs := fs.SessionGit; !gs.Pending || !gs.CheckedAt.IsZero() || gs.NoAnswers != 1 {
		t.Fatalf("a session whose counts went nowhere: %+v, want it uncounted, tried once", gs)
	}
	if err := os.Rename(path+".away", path); err != nil {
		t.Fatal(err)
	}
	advance(retryBackoff(1))
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sentGit(srv.requests(), sid); got == nil || got.UncommittedEditedFiles != 1 {
		t.Errorf("the next flush sent git counts %+v, want the session counted", got)
	}
}

// Counting has a budget within the flush. A session not counted in time stays
// due, and the flush still sends what it has.
func TestSessionGitCountingStopsAtItsBudget(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-budget"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)

	previous := sessionGitCountBudget
	sessionGitCountBudget = 0
	err := s.FlushObservations(context.Background())
	sessionGitCountBudget = previous
	if err != nil {
		t.Fatal(err)
	}
	if reqs := srv.requests(); len(reqs) != 1 || reqs[0].HeartbeatAt == nil || sentGit(reqs, sid) != nil {
		t.Fatalf("with no time to count: requests %v, want the heartbeat alone", srv.bodies())
	}
	if gs := state.Files[path].SessionGit; !gs.Pending || !gs.CheckedAt.IsZero() {
		t.Fatalf("a session not counted was marked counted: %+v", gs)
	}

	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sentGit(srv.requests(), sid); got == nil || got.UncommittedEditedFiles != 1 {
		t.Errorf("the next flush sent git counts %+v, want the session counted", got)
	}
}

// A snapshot that carries git counts goes under the account of the session's
// newest record, as one built from its lines does -- not under whichever
// account is signed in when its files are counted.
func TestSessionGitSnapshotGoesUnderTheSessionsAccount(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-account"
	// No metadata line: nothing but the counts makes this session a snapshot.
	writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	home := normalizeHome(claudeDir)
	state.ObserveClaudeAccount(at.Add(-time.Hour), home, "acct-session")
	mustSync(t, s)
	state.ObserveClaudeAccount(nowFn().Add(-time.Minute), home, "acct-now")
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	var accounts []string
	for _, r := range srv.requests() {
		for _, it := range r.Snapshots {
			if it.SessionID == sid {
				accounts = append(accounts, it.AccountID)
			}
		}
	}
	if !slices.Equal(accounts, []string{"acct-session"}) {
		t.Errorf("the snapshot went under %v, want acct-session alone", accounts)
	}
}

// A week after its last activity a session's paths are dropped and its counts
// stay as last taken: nothing is counted or sent any more.
func TestSessionGitDropsThePathsAWeekAfterTheSession(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-old"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := *snapshotState(t, state, path).Snapshot.Git

	runGit(t, repo, "commit", "-am", "work")
	advance(sessionGitWindow)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadState(state.path)
	if err != nil {
		t.Fatal(err)
	}
	fs := reloaded.Files[path]
	if fs.SessionGit != nil {
		t.Errorf("a week later the session still keeps %d paths", len(fs.SessionGit.Edited))
	}
	if got := *fs.SessionSnapshot.Snapshot.Git; got != before {
		t.Errorf("counts %+v, want the last ones taken, %+v", got, before)
	}
	if n := snapshotsSent(srv.requests()); n != 1 {
		t.Errorf("%d snapshots sent, want the first alone", n)
	}
}

// One session keeps at most maxEditedPaths paths, and its counts say they cover
// the kept ones (truncated names git).
func TestSessionGitCapsTheEditedPaths(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	dir := t.TempDir()
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-many"
	lines := []string{convLine(sid, "u-1", at)}
	for i := 0; i <= maxEditedPaths; i++ {
		lines = append(lines, editLine(sid, fmt.Sprintf("a-%d", i), at, "Write", filepath.Join(dir, fmt.Sprintf("f-%04d.txt", i))))
	}
	path, _ := writeTranscript(t, claudeDir, sid, lines...)
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gs := state.Files[path].SessionGit; len(gs.Edited) != maxEditedPaths || !gs.Truncated {
		t.Fatalf("%d paths kept, truncated %v; want %d and truncated", len(gs.Edited), gs.Truncated, maxEditedPaths)
	}
	snap := snapshotState(t, state, path).Snapshot
	if snap.Git == nil || snap.Git.EditedFiles != maxEditedPaths || !slices.Contains(snap.Truncated, "git") {
		t.Errorf("snapshot git %+v truncated %v; want %d edited files and git named in truncated", snap.Git, snap.Truncated, maxEditedPaths)
	}
}
