package syncer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"
)

// A filesystem call that never returns -- a hung network mount -- costs the
// flush its budget and no more: the flush sends what it has, the session whose
// path hangs is asked again after its backoff, and while that job still hangs
// no second one starts for it. Once the mount answers, the session is counted.
func TestSessionGitDoesNotWaitForAHungFilesystem(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	hung := filepath.Join(t.TempDir(), "hung-mount")
	if err := os.MkdirAll(hung, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const hungSid, okSid = "sess-git-hung", "sess-git-ok"
	hungPath, _ := writeTranscript(t, claudeDir, hungSid, convLine(hungSid, "u-1", at), editLine(hungSid, "a-1", at, "Write", filepath.Join(hung, "f.txt")))
	writeTranscript(t, claudeDir, okSid, convLine(okSid, "u-2", at), editLine(okSid, "a-2", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)

	release := make(chan struct{})
	var released sync.Once
	var hungCalls atomic.Int32
	previousStat, previousBudget := statFn, sessionGitCountBudget
	statFn = func(p string) (os.FileInfo, error) {
		if strings.Contains(p, "hung-mount") {
			hungCalls.Add(1)
			<-release
		}
		return previousStat(p)
	}
	sessionGitCountBudget = time.Second
	t.Cleanup(func() {
		released.Do(func() { close(release) })
		waitGitJobs(t, s)
		statFn, sessionGitCountBudget = previousStat, previousBudget
	})

	start := time.Now()
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the flush took %s with a filesystem call hung", elapsed.Round(time.Millisecond))
	}
	if got := sentGit(srv.requests(), okSid); got == nil || got.UncommittedEditedFiles != 1 {
		t.Errorf("the other session: git counts %+v, want it counted", got)
	}
	gs := state.Files[hungPath].SessionGit
	if !gs.Pending || !gs.CheckedAt.IsZero() || gs.NoAnswers != 1 || gs.TriedAt.IsZero() {
		t.Errorf("the hung session: %+v, want it uncounted, with no answer once", gs)
	}

	// Due again once its backoff has passed, its job still running: no answer
	// again, and no second job.
	advance(retryBackoff(1))
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := hungCalls.Load(); n != 1 {
		t.Errorf("the hung path was asked about %d times; a job still running must not be started again", n)
	}
	if gs.NoAnswers != 2 {
		t.Errorf("%d no answers, want the job still running counted as one", gs.NoAnswers)
	}

	released.Do(func() { close(release) })
	waitGitJobs(t, s)
	advance(retryBackoff(2))
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sentGit(srv.requests(), hungSid); got == nil || got.EditedFiles != 1 || got.Unresolved != 1 {
		t.Errorf("once the mount answered: git counts %+v, want the path counted, in no work tree", got)
	}
	if gs.NoAnswers != 0 || !gs.TriedAt.IsZero() {
		t.Errorf("counted, the session still backs off: %+v", gs)
	}
}

// A git status that does not answer costs the flush its budget and no more, as
// a hung filesystem does: the session keeps its counts and backs off. The work
// tree is known from an earlier count, so finding the paths asks no git and the
// status is reached.
func TestSessionGitDoesNotWaitForAHungGit(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-hung-status"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	hang := filepath.Join(t.TempDir(), "hang")
	calls := installGitWrapper(t, "case \"$*\" in *\" status \"*) [ -f '"+hang+"' ] && sleep 5;; esac\n")
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}

	writeFile(t, hang, "")
	appendLines(t, path, editLine(sid, "a-2", at, "Edit", filepath.Join(repo, "modified.txt")))
	mustSync(t, s)
	previous := sessionGitCountBudget
	sessionGitCountBudget = 2 * time.Second // finding paths gets one second
	t.Cleanup(func() {
		waitGitJobs(t, s)
		sessionGitCountBudget = previous
	})
	start := time.Now()
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The budget ends the flush at two seconds. A flush that waited for the status
	// would last until git's own limits cut it, three seconds however long it
	// hangs (gitctx commandTimeout and commandWaitDelay): the bound sits between.
	if elapsed := time.Since(start); elapsed > 2900*time.Millisecond {
		t.Errorf("the flush took %s with git status hung", elapsed.Round(time.Millisecond))
	}
	// The wrapper logs a call from the shell it starts, which may not have run
	// yet when the flush, having given up on the status, returns.
	deadline := time.Now().Add(time.Second)
	for callsWith(calls(), " status ") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := callsWith(calls(), " status "); n != 2 {
		t.Fatalf("fixture: %d status calls, want the hung one asked; calls: %q", n, calls())
	}
	if gs := state.Files[path].SessionGit; !gs.Pending || gs.NoAnswers != 1 || gs.TriedAt.IsZero() {
		t.Errorf("the session: %+v, want it uncounted, with no answer once", gs)
	}
}

// When git gives no answer -- here the status is killed -- the session keeps
// its counts, its revision and the time they were last taken, and is asked
// again after a backoff that doubles: not on every flush.
func TestSessionGitKeepsTheCountsWhenGitGivesNoAnswer(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-no-answer"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := snapshotState(t, state, path)
	before, revision, checked := *st.Snapshot.Git, st.Revision, state.Files[path].SessionGit.CheckedAt

	runGit(t, repo, "commit", "-am", "work")
	calls := installGitWrapper(t, "case \"$*\" in *\" status \"*) kill -9 $$;; esac\n")
	advance(metaTTL)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = snapshotState(t, state, path)
	if *st.Snapshot.Git != before || st.Revision != revision {
		t.Errorf("after a status that gave no answer: counts %+v revision %d, want %+v and %d", *st.Snapshot.Git, st.Revision, before, revision)
	}
	if got := state.Files[path].SessionGit.CheckedAt; !got.Equal(checked) {
		t.Errorf("checked at %v after no answer, want %v kept", got, checked)
	}
	for i, step := range []struct {
		wait  time.Duration
		calls int
	}{{0, 1}, {5 * time.Minute, 2}, {5 * time.Minute, 2}, {5 * time.Minute, 3}} {
		advance(step.wait)
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
		if n := callsWith(calls(), " status "); n != step.calls {
			t.Errorf("flush %d, %s on: %d status calls, want %d", i+1, step.wait, n, step.calls)
		}
	}
}

// A work tree whose job from an earlier flush is still asking git is no
// answer for the sessions in it: they back off, and no second job starts.
func TestSessionGitCountsAWorkTreeStillBeingAskedAsNoAnswer(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	root, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-root-busy"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	busy := "root\x00" + root
	s.gitJobs.mu.Lock()
	s.gitJobs.running = map[string]bool{busy: true}
	s.gitJobs.mu.Unlock()
	t.Cleanup(func() {
		s.gitJobs.mu.Lock()
		delete(s.gitJobs.running, busy)
		s.gitJobs.mu.Unlock()
		waitGitJobs(t, s)
	})
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gs := state.Files[path].SessionGit; !gs.Pending || gs.NoAnswers != 1 || gs.TriedAt.IsZero() {
		t.Errorf("the session: %+v, want it uncounted, with no answer once", gs)
	}
}

// The backoff starts at five minutes and doubles up to metaTTL.
func TestSessionGitRetryBackoffDoublesUpToMetaTTL(t *testing.T) {
	for n, want := range map[int]time.Duration{1: 5 * time.Minute, 2: 10 * time.Minute, 3: 20 * time.Minute, 4: 40 * time.Minute, 5: metaTTL, 50: metaTTL} {
		if got := retryBackoff(n); got != want {
			t.Errorf("retryBackoff(%d) = %s, want %s", n, got, want)
		}
	}
}

// A session git never answers for does not keep its paths for ever: past its
// window they go once its backoff has run up to its cap -- not before, and not
// while it is still backing off.
func TestSessionGitDropsThePathsOfASessionGitNeverAnswersFor(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-never-answers"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	t.Setenv("PATH", t.TempDir()) // git cannot run at all
	advance(sessionGitWindow)
	tries := 1
	for retryBackoff(tries) < metaTTL {
		tries++
	}
	for n := 1; n <= tries; n++ {
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
		if gs := state.Files[path].SessionGit; gs == nil || gs.NoAnswers != n {
			t.Fatalf("try %d: git state %+v, want the paths kept and %d tries", n, gs, n)
		}
		if n < tries {
			advance(retryBackoff(n))
		}
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.Files[path].SessionGit == nil {
		t.Fatal("the paths were dropped while the session was still backing off")
	}
	advance(retryBackoff(tries))
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gs := state.Files[path].SessionGit; gs != nil {
		t.Errorf("a session git never answered for keeps its %d paths past its window and its backoff", len(gs.Edited))
	}
}

// A session first read past its window, whose first tries git does not answer,
// is not dropped for them: it is counted once git answers.
func TestSessionGitCountsALateReadSessionGitAnswersLate(t *testing.T) {
	advance := fakeClock(t)
	old := nowFn().Add(-10 * 24 * time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-late-answer"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", old), editLine(sid, "a-1", old, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	withGit := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	for n := 1; n <= 2; n++ {
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
		if gs := state.Files[path].SessionGit; gs == nil || gs.NoAnswers != n {
			t.Fatalf("try %d: git state %+v, want the paths kept and %d tries", n, gs, n)
		}
		advance(retryBackoff(n))
	}
	t.Setenv("PATH", withGit)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sentGit(srv.requests(), sid); got == nil || got.UncommittedEditedFiles != 1 {
		t.Errorf("git counts %+v, want the late-read session counted once git answered", got)
	}
}

// A name can be written in either Unicode normal form, on disk and in a tool's
// input. Edited paths, work trees and status paths compare in NFC, git is asked
// about both forms, and two spellings of one file are one file -- here with git
// giving names back as they are on disk (core.precomposeunicode off), in a work
// tree whose own name is NFD. A file inside its .git is still known for one.
func TestSessionGitMatchesNamesWrittenInEitherNormalForm(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found")
	}
	repo := filepath.Join(t.TempDir(), norm.NFD.String("저장소"))
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "core.precomposeunicode", "false")
	onDiskNFD, onDiskNFC := norm.NFD.String("한글.txt"), norm.NFC.String("가나.txt")
	writeFile(t, filepath.Join(repo, onDiskNFD), "untracked\n")
	writeFile(t, filepath.Join(repo, onDiskNFC), "untracked\n")
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-nfd"
	writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at),
		editLine(sid, "a-1", at, "Write", filepath.Join(repo, norm.NFC.String("한글.txt"))),
		editLine(sid, "a-2", at, "Edit", filepath.Join(repo, onDiskNFC)),
		editLine(sid, "a-3", at, "Edit", filepath.Join(repo, norm.NFD.String("가나.txt"))),
		editLine(sid, "a-4", at, "Edit", filepath.Join(repo, ".git", "description")),
	)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 3, UncommittedEditedFiles: 2, Repositories: 1, Unresolved: 1}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Errorf("git counts %+v, want %+v", got, want)
	}
}

// The sync pass keeps edited paths as written and never touches the filesystem
// for them: resolving them, and asking git, wait for the flush.
func TestSessionGitSyncPassLeavesTheFilesystemAlone(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-sync-pass"
	writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	var calls atomic.Int32
	previousStat, previousEval := statFn, evalSymlinksFn
	statFn = func(p string) (os.FileInfo, error) { calls.Add(1); return previousStat(p) }
	evalSymlinksFn = func(p string) (string, error) { calls.Add(1); return previousEval(p) }
	t.Cleanup(func() {
		waitGitJobs(t, s)
		statFn, evalSymlinksFn = previousStat, previousEval
	})
	mustSync(t, s)
	if n := calls.Load(); n != 0 {
		t.Errorf("the sync pass made %d filesystem calls for edited paths", n)
	}
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() == 0 || sentGit(srv.requests(), sid) == nil {
		t.Error("the flush did not count the session")
	}
}

// git status is asked about the edited paths alone, each a literal pathspec, in
// calls that fit a command line: a work tree's many other untracked files cost
// nothing, a name with a glob character or a leading colon means itself, and a
// rename is counted by its destination.
func TestSessionGitAsksStatusOnlyAboutTheEditedPaths(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := namedRepo(t, "pathspec-repo", "")
	for _, name := range []string{":colon.txt", "a[1].txt", "old.txt"} {
		writeFile(t, filepath.Join(repo, name), name+"\n")
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "names")
	writeFile(t, filepath.Join(repo, ":colon.txt"), "changed\n")
	writeFile(t, filepath.Join(repo, "a[1].txt"), "changed\n")
	writeFile(t, filepath.Join(repo, "a1.txt"), "a glob would match me\n")
	runGit(t, repo, "mv", "old.txt", "renamed.txt")
	for i := 0; i < 1500; i++ {
		writeFile(t, filepath.Join(repo, "heap", fmt.Sprintf("%04d.txt", i)), "x\n")
	}
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-pathspec"
	lines := []string{convLine(sid, "u-1", at),
		editLine(sid, "a-1", at, "Edit", filepath.Join(repo, ":colon.txt")),
		editLine(sid, "a-2", at, "Edit", filepath.Join(repo, "a[1].txt")),
		editLine(sid, "a-3", at, "Edit", filepath.Join(repo, "renamed.txt")),
	}
	for i := 0; i < 400; i++ {
		name := filepath.Join(repo, "edited", fmt.Sprintf("%03d-%s.txt", i, strings.Repeat("n", 80)))
		writeFile(t, name, "new\n")
		lines = append(lines, editLine(sid, fmt.Sprintf("w-%d", i), at, "Write", name))
	}
	writeTranscript(t, claudeDir, sid, lines...)
	calls := installGitWrapper(t, "")
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 403, UncommittedEditedFiles: 403, Repositories: 1}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Errorf("git counts %+v, want %+v", got, want)
	}
	status := callsWith(calls(), "pathspec-repo", " status ")
	if status < 2 || status != callsWith(calls(), "pathspec-repo", " status ", "-- :(literal)") {
		t.Errorf("%d status calls; want the pathspecs split over several, each asking literal paths alone", status)
	}
	for _, c := range calls() {
		if strings.Contains(c, " status ") && strings.Contains(c, "heap/") {
			t.Errorf("status asked about a file nobody edited: %.200s", c)
		}
	}
}

// A directory's work tree is looked for once per metaTTL, with no git, and git
// is asked to check it once per metaTTL: a count in between takes both from
// the cache.
func TestSessionGitFindsAWorkTreeOncePerMetaTTL(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := namedRepo(t, "cached-repo", "")
	writeFile(t, filepath.Join(repo, "second.txt"), "x\n")
	writeFile(t, filepath.Join(repo, "third.txt"), "x\n")
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-cache"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	calls := installGitWrapper(t, "")
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	var probes atomic.Int32
	previousStat := statFn
	statFn = func(p string) (os.FileInfo, error) {
		if filepath.Base(p) == ".git" {
			probes.Add(1)
		}
		return previousStat(p)
	}
	t.Cleanup(func() {
		waitGitJobs(t, s)
		statFn = previousStat
	})
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := probes.Load()
	if first == 0 {
		t.Fatal("fixture: the work tree was not looked for")
	}
	checks := func() int { return callsWith(calls(), "cached-repo", "rev-parse --show-toplevel") }
	if n := checks(); n != 1 {
		t.Errorf("%d checks of the work tree in the first count, want 1", n)
	}
	appendLines(t, path, editLine(sid, "a-2", at, "Write", filepath.Join(repo, "second.txt")))
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := probes.Load(); n != first {
		t.Errorf("%d more .git probes within metaTTL, want the cached work tree", n-first)
	}
	if n := checks(); n != 1 {
		t.Errorf("%d checks of the work tree within metaTTL, want the cached one", n)
	}
	advance(metaTTL)
	appendLines(t, path, editLine(sid, "a-3", nowFn(), "Write", filepath.Join(repo, "third.txt")))
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := probes.Load(); n == first {
		t.Error("no .git probe past metaTTL, want the work tree looked for again")
	}
	if n := checks(); n != 2 {
		t.Errorf("%d checks of the work tree past metaTTL, want a second", n)
	}
}

// The reviewer's case: a session that edited files in 60 directories, with a
// git that takes 50ms more a call, is counted in its first flush. Finding the
// work tree asks no git, and git is asked once for the work tree.
func TestSessionGitCountsALargeSessionWithASlowGit(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := namedRepo(t, "large-repo", "")
	const sid = "sess-git-large"
	lines := []string{convLine(sid, "u-1", at)}
	for i := 0; i < 60; i++ {
		name := filepath.Join(repo, fmt.Sprintf("dir-%02d", i), "new.txt")
		writeFile(t, name, "x\n")
		lines = append(lines, editLine(sid, fmt.Sprintf("a-%d", i), at, "Write", name))
	}
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	writeTranscript(t, claudeDir, sid, lines...)
	calls := installGitWrapper(t, "sleep 0.05\n")
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	previous := sessionGitCountBudget
	sessionGitCountBudget = 2 * time.Second // finding paths gets one second
	t.Cleanup(func() {
		waitGitJobs(t, s)
		sessionGitCountBudget = previous
	})
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 60, UncommittedEditedFiles: 60, Repositories: 1}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Errorf("git counts %+v, want %+v in the first flush", got, want)
	}
	if n := callsWith(calls(), "large-repo", " status "); n != 1 {
		t.Errorf("%d status calls, want one for the work tree", n)
	}
}

// On a filesystem slow enough that finding a large session's work trees takes
// longer than the budget, each try takes up where the last stopped: the session
// is counted within a few tries, not begun again from the start each time.
func TestSessionGitFindsWorkTreesAcrossTriesCutShort(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := namedRepo(t, "slow-repo", "")
	const sid = "sess-git-slow-fs"
	lines := []string{convLine(sid, "u-1", at)}
	for i := 0; i < 60; i++ {
		name := filepath.Join(repo, fmt.Sprintf("dir-%02d", i), "new.txt")
		writeFile(t, name, "x\n")
		lines = append(lines, editLine(sid, fmt.Sprintf("a-%d", i), at, "Write", name))
	}
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	path, _ := writeTranscript(t, claudeDir, sid, lines...)
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	previousStat, previousBudget := statFn, sessionGitCountBudget
	statFn = func(p string) (os.FileInfo, error) {
		time.Sleep(50 * time.Millisecond) // a mount that takes its time
		return previousStat(p)
	}
	sessionGitCountBudget = 2 * time.Second
	t.Cleanup(func() {
		waitGitJobs(t, s)
		statFn, sessionGitCountBudget = previousStat, previousBudget
	})
	for try := 1; try <= 6; try++ {
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
		waitGitJobs(t, s)
		if got := sentGit(srv.requests(), sid); got != nil {
			if got.EditedFiles != 60 || got.UncommittedEditedFiles != 60 {
				t.Errorf("git counts %+v, want the 60 files", got)
			}
			if try == 1 {
				t.Error("fixture: counted in one try, so no try was cut short")
			}
			return
		}
		advance(retryBackoff(state.Files[path].SessionGit.NoAnswers))
	}
	t.Fatal("not counted in 6 tries")
}

// A path git cannot place is unresolved: under a .git git cannot use, which
// git answers about with a failure and which is no repository, and inside .git
// itself. A linked worktree's .git file names its work tree.
func TestSessionGitCountsPathsGitCannotPlaceUnresolved(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	broken := filepath.Join(t.TempDir(), "broken")
	writeFile(t, filepath.Join(broken, ".git"), "not a gitdir\n")
	writeFile(t, filepath.Join(broken, "sub", "f.txt"), "x\n")
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo, "worktree", "add", "-b", "linked", linked)
	writeFile(t, filepath.Join(linked, "new.txt"), "x\n")
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-cannot-place"
	writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at),
		editLine(sid, "a-1", at, "Edit", filepath.Join(broken, "sub", "f.txt")),
		editLine(sid, "a-2", at, "Write", filepath.Join(repo, ".git", "hooks", "pre-commit")),
		editLine(sid, "a-3", at, "Edit", filepath.Join(repo, "modified.txt")),
		editLine(sid, "a-4", at, "Write", filepath.Join(linked, "new.txt")),
	)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 4, UncommittedEditedFiles: 2, Repositories: 2, Unresolved: 2}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Errorf("git counts %+v, want %+v", got, want)
	}
}

// An answer from a job an earlier flush gave up on does not take the place of
// a newer one: such a job can finish long after a later flush's.
func TestSessionGitRootCacheKeepsTheNewerAnswer(t *testing.T) {
	var c rootCache
	c.put([]string{"/w/dir"}, "/w/new", t0.Add(time.Minute))
	c.put([]string{"/w/dir"}, "/w/old", t0)
	if got, _ := c.get("/w/dir"); got != "/w/new" {
		t.Errorf("work tree %q after an older answer, want the newer kept", got)
	}
	c.put([]string{"/w/dir"}, "/w/newest", t0.Add(2*time.Minute))
	if got, _ := c.get("/w/dir"); got != "/w/newest" {
		t.Errorf("work tree %q after a newer answer, want it taken", got)
	}
}

// Where git names another top level than the .git the search found, status is
// asked there, and the paths are taken relative to it: a file modified under
// an empty .git directory, which git does not take for a repository, is
// uncommitted in the work tree around it, and so is a file left untracked in
// the work tree core.worktree points a repository at. A file beside that work
// tree, and the work tree itself, which git cannot place in it, are unresolved;
// status is not asked about the whole tree.
func TestSessionGitAsksStatusWhereGitPutsTheWorkTree(t *testing.T) {
	fakeClock(t)
	at := nowFn().Add(-time.Hour)
	outer := namedRepo(t, "outer-repo", "")
	writeFile(t, filepath.Join(outer, "sub", "file.txt"), "committed\n")
	runGit(t, outer, "add", ".")
	runGit(t, outer, "commit", "-m", "sub")
	writeFile(t, filepath.Join(outer, "sub", "file.txt"), "changed\n")
	if err := os.Mkdir(filepath.Join(outer, "sub", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(parent, "configured")
	writeFile(t, filepath.Join(tree, "untracked.txt"), "x\n")
	writeFile(t, filepath.Join(parent, "beside.txt"), "x\n")
	runGit(t, parent, "init", "-b", "main")
	runGit(t, parent, "config", "core.worktree", tree)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-top-level"
	writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at),
		editLine(sid, "a-1", at, "Edit", filepath.Join(outer, "sub", "file.txt")),
		editLine(sid, "a-2", at, "Write", filepath.Join(tree, "untracked.txt")),
		editLine(sid, "a-3", at, "Write", filepath.Join(parent, "beside.txt")),
		editLine(sid, "a-4", at, "Write", tree),
	)
	calls := installGitWrapper(t, "")
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := SnapshotGit{ChangedAt: nowFn().UTC(), EditedFiles: 4, UncommittedEditedFiles: 2, Repositories: 2, Unresolved: 2}
	if got := sentGit(srv.requests(), sid); got == nil || *got != want {
		t.Errorf("git counts %+v, want %+v", got, want)
	}
	for _, c := range calls() {
		if strings.Contains(c+" ", " :(literal). ") {
			t.Errorf("status asked about the whole work tree: %.200s", c)
		}
	}
}

// When git gives no answer to the check of a work tree -- here it is killed --
// the session keeps its counts and revision, as for any git that does not
// answer.
func TestSessionGitKeepsTheCountsWhenTheWorkTreeCheckGivesNoAnswer(t *testing.T) {
	advance := fakeClock(t)
	at := nowFn().Add(-time.Hour)
	repo := gitStateRepo(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-git-check-no-answer"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", at), editLine(sid, "a-1", at, "Edit", filepath.Join(repo, "modified.txt")))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := snapshotState(t, state, path)
	before, revision := *st.Snapshot.Git, st.Revision
	runGit(t, repo, "commit", "-am", "work")
	calls := installGitWrapper(t, "case \"$*\" in *\"rev-parse --show-toplevel\"*) kill -9 $$;; esac\n")
	advance(metaTTL)
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if callsWith(calls(), "rev-parse --show-toplevel") == 0 {
		t.Fatal("fixture: the work tree was not checked again metaTTL later")
	}
	st = snapshotState(t, state, path)
	if *st.Snapshot.Git != before || st.Revision != revision {
		t.Errorf("after a check with no answer: counts %+v revision %d, want %+v and %d", *st.Snapshot.Git, st.Revision, before, revision)
	}
	if gs := state.Files[path].SessionGit; gs.NoAnswers != 1 {
		t.Errorf("%d no answers, want the check counted as one", gs.NoAnswers)
	}
}
