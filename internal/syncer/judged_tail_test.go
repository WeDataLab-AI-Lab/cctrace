package syncer

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"cctrace/internal/gitctx"
)

// The tests in this file swap resolveGit, nowFn and listSessionFiles, so they
// cannot run in parallel with the rest of the package.
//
// Under an allowlist a record leaves only when the repository it was written
// in is allowed. "Sent" below is always read off the server, by the text of
// the records it received.

func wantSent(t *testing.T, srv *freshMetaServer, want ...string) {
	t.Helper()
	if got := srv.sentTexts(t); !slices.Equal(got, want) {
		t.Fatalf("server received %v, want %v", got, want)
	}
}

func wantOffset(t *testing.T, s *Syncer, path string, want int64) {
	t.Helper()
	if got := s.state.GetOffset(path); got != want {
		t.Fatalf("offset = %d, want %d (file is %d bytes)", got, want, fileSize(t, path))
	}
}

// A tail that passes through an excluded repository sends only the allowed
// runs. The excluded run used to leave under the first cwd's identity.
func TestJudgedTail_excludedRunInsideTailIsConsumed(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"), sessionLine("/work/b", "secret"), sessionLine("", "secret too"), sessionLine("/work/a", "3"))

	syncPass(t, s)

	wantSent(t, srv, "1", "3")
	wantOffset(t, s, p, fileSize(t, p))
}

// The file that makes the syncer notice a replaced repository is itself
// holding lines from before. They are below the mark taken at that moment, so
// they are judged against the repository the cwd held before as well, and do
// not leave under the allowed one that replaced it.
//
// The clock moves inside every lookup, as a real one does. Lines written under
// the new repository after the transition was recorded are sent: position, not
// time, decides, so a scan that began before the lookup finished costs nothing.
func TestJudgedTail_transitionToAllowedConsumesWhatWasPending(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	advance := useFakeClock(t)
	stubResolveGitChecked(t, func(cwd string) (gitctx.Context, error) {
		advance(time.Millisecond)
		return repos[cwd], nil
	})
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "excluded 0"))
	syncPass(t, s)

	appendLines(t, p, sessionLine("/work/r", "excluded 1"))
	repos["/work/r"] = repoAt(allowedRepo)
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, p, fileSize(t, p))

	appendLines(t, p, sessionLine("/work/r", "allowed 1"))
	syncPass(t, s)
	appendLines(t, p, sessionLine("/work/r", "allowed 2"))
	syncPass(t, s)
	wantSent(t, srv, "allowed 1", "allowed 2")
}

// Another file notices the replacement first. The target file then holds a
// complete line and one still being written, both from the excluded
// repository. The unfinished line stays behind the offset, and when it is
// completed it is still below the mark: it started there.
func TestJudgedTail_unfinishedLineFromBeforeTheTransitionIsNotSent(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	first, target := filepath.Join(dir, "s1.jsonl"), filepath.Join(sessionDir(t, dir, "-proj-b"), "s2.jsonl")
	appendLines(t, first, sessionLine("/work/r", "first 0"))
	appendLines(t, target, sessionLine("/work/r", "target 0"))
	syncPass(t, s)

	appendLines(t, target, sessionLine("/work/r", "complete"))
	appendBytes(t, target, sessionLine("/work/r", "unfinished"))
	unfinishedAt := fileSize(t, target) - int64(len(sessionLine("/work/r", "unfinished")))
	appendLines(t, first, sessionLine("/work/r", "first 1"))
	repos["/work/r"] = repoAt(allowedRepo)
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, target, unfinishedAt)

	appendBytes(t, target, "\n")
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, target, fileSize(t, target))

	appendLines(t, target, sessionLine("/work/r", "allowed"))
	syncPass(t, s)
	wantSent(t, srv, "allowed")
}

// The mark is a position, so what the lines say about time does not matter: a
// file the state has never seen, holding lines with no timestamp and lines
// dated in the future, is judged whole against the previous repository.
func TestJudgedTail_newFileIsJudgedByPositionNotTimestamps(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo), "/work/seen": repoAt(allowedRepo)}
	stubRepos(t, repos)
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	known := filepath.Join(dir, "known.jsonl")
	appendLines(t, known, sessionLine("/work/r", "known 0"))
	syncPass(t, s)

	fresh := filepath.Join(dir, "new.jsonl")
	appendLines(t, fresh,
		lineWith("user", `"cwd":"/work/r",`, "undated"),
		lineWith("user", `"timestamp":"2099-01-01T00:00:00Z","cwd":"/work/r",`, "future"),
		sessionLine("/work/r", "dated"),
		lineWith("user", ``, "undated and no cwd"))
	repos["/work/r"] = repoAt(allowedRepo)
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, fresh, fileSize(t, fresh))

	appendLines(t, fresh, lineWith("user", `"cwd":"/work/r",`, "after"))
	syncPass(t, s)
	wantSent(t, srv, "after")
}

// A file created after the pass listed its files, while the lookup that finds
// the transition is running, is marked as well: the transition lists again.
func TestJudgedTail_fileCreatedDuringTheTransitionPassIsMarked(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	useFakeClock(t)
	var late string
	stubResolveGitChecked(t, func(cwd string) (gitctx.Context, error) {
		if late != "" {
			appendLines(t, late, sessionLine("/work/r", "written before the lookup returned"))
			late = ""
		}
		return repos[cwd], nil
	})
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "excluded 0"))
	syncPass(t, s)

	appendLines(t, p, sessionLine("/work/r", "excluded 1"))
	repos["/work/r"] = repoAt(allowedRepo)
	late = filepath.Join(dir, "s0-late.jsonl")
	lateFile := late
	syncPass(t, s)
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, lateFile, fileSize(t, lateFile))

	appendLines(t, lateFile, sessionLine("/work/r", "allowed"))
	syncPass(t, s)
	wantSent(t, srv, "allowed")
}

// The marks are kept in the state file, so a restart between the transition
// and the completion of an unfinished line changes nothing.
func TestJudgedTail_taintSurvivesRestart(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "excluded 0"))
	syncPass(t, s)

	appendLines(t, p, sessionLine("/work/r", "excluded 1"))
	appendBytes(t, p, sessionLine("/work/r", "unfinished"))
	repos["/work/r"] = repoAt(allowedRepo)
	syncPass(t, s)

	s = restartSyncer(t, s)
	appendBytes(t, p, "\n")
	appendLines(t, p, sessionLine("/work/r", "allowed"))
	syncPass(t, s)
	wantSent(t, srv, "allowed")
}

// What is below the mark may have been written under either repository: the
// replacement happened some time before it was noticed. So it is sent only
// when both are allowed, and then under the current one. A cwd that moved from
// an allowed repository to an excluded one loses what was pending, including
// the line that made the syncer look.
func TestJudgedTail_pendingAtTransitionNeedsBothAllowed(t *testing.T) {
	cases := []struct {
		name      string
		from, to  string
		wantAfter []string
	}{
		{"allowed to excluded", allowedRepo, excludedRepo, []string{"before"}},
		{"allowed to allowed", allowedRepo, allowedRepoTwo, []string{"before", "pending", "after"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repos := map[string]gitctx.Context{"/work/r": repoAt(tc.from)}
			stubRepos(t, repos)
			useFakeClock(t)
			srv, endpoint := newFreshMetaServer(t)
			s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
			p := filepath.Join(dir, "s1.jsonl")
			appendLines(t, p, sessionLine("/work/r", "before"))
			syncPass(t, s)

			appendLines(t, p, sessionLine("/work/r", "pending"))
			repos["/work/r"] = repoAt(tc.to)
			syncPass(t, s)
			appendLines(t, p, sessionLine("/work/r", "after"))
			syncPass(t, s)

			wantSent(t, srv, tc.wantAfter...)
			wantOffset(t, s, p, fileSize(t, p))
			if posts := srv.recordPosts(); len(posts) > 1 && posts[1].RepositoryID != tc.to {
				t.Fatalf("pending records sent under %q, want the current %q", posts[1].RepositoryID, tc.to)
			}
		})
	}
}

// With several marks on one cwd of one file, a line is sent only if every
// repository whose mark is still above it is allowed. A run of one cwd is cut
// at each mark.
func TestJudgedTail_everyTaintAboveALineMustBeAllowed(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/r": repoAt(allowedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "under both"))
	low := fileSize(t, p)
	appendLines(t, p, sessionLine("/work/r", "under the allowed one"))
	high := fileSize(t, p)
	appendLines(t, p, sessionLine("/work/r", "above both"))
	s.state.CWDIdentity = map[string]*CWDIdentity{"/work/r": {RepositoryID: allowedRepo, Source: "resolved"}}
	s.state.Taints = map[string][]Taint{p: {
		{CWD: "/work/r", Until: high, PrevRepositoryID: allowedRepoTwo},
		{CWD: "/work/r", Until: low, PrevRepositoryID: excludedRepo},
		{CWD: "/work/elsewhere", Until: high, PrevRepositoryID: excludedRepo},
	}}

	syncPass(t, s)

	wantSent(t, srv, "under the allowed one", "above both")
}

// A transition that could not be recorded -- the session files could not be
// listed -- leaves the cwd's records unjudged: nothing of that cwd is sent or
// consumed until the marks exist.
func TestJudgedTail_unrecordedTransitionHolds(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "excluded 0"))
	syncPass(t, s)
	held := fileSize(t, p)

	failing := true
	prev := listSessionFiles
	listSessionFiles = func(claudeDir string) ([]string, error) {
		if failing {
			return nil, errors.New("listing failed")
		}
		return prev(claudeDir)
	}
	t.Cleanup(func() { listSessionFiles = prev })
	appendLines(t, p, sessionLine("/work/r", "excluded 1"))
	repos["/work/r"] = repoAt(allowedRepo)
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, p, held)

	failing = false
	advance(lookupRetryInterval)
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, p, fileSize(t, p))
}

// The same hold, reached the way it happens: the directory of another session
// in the same cwd cannot be read when the replacement is noticed. Its file
// holds a row written under the excluded repository, and a pass's own listing
// just leaves the file out. Recording the transition then would leave that row
// unmarked -- and once the directory is readable again nothing would look for
// it: the recorded identity already matches. So the cwd holds until every
// session file can be listed, and the row is never sent.
func TestJudgedTail_unreadableDirectoryAtTransitionHoldsUntilMarked(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	hidden := sessionDir(t, dir, "-proj-b")
	sibling, private := filepath.Join(dir, "s1.jsonl"), filepath.Join(hidden, "s2.jsonl")
	appendLines(t, sibling, sessionLine("/work/r", "sibling 0"))
	appendLines(t, private, sessionLine("/work/r", "private 0"))
	syncPass(t, s)
	held := fileSize(t, sibling)

	appendLines(t, private, sessionLine("/work/r", "private row"))
	restore := unreadable(t, hidden)
	repos["/work/r"] = repoAt(allowedRepo)
	appendLines(t, sibling, sessionLine("/work/r", "sibling 1"))
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, sibling, held)
	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != excludedRepo {
		t.Fatalf("CWDIdentity moved to %s with a project directory unread", got)
	}

	restore()
	advance(lookupRetryInterval)
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, private, fileSize(t, private))

	appendLines(t, private, sessionLine("/work/r", "allowed"))
	syncPass(t, s)
	wantSent(t, srv, "allowed")
}

// A lookup that did not hear git's answer says nothing about the repository.
// Its fallback identity used to read as "not allowed", and the tail was
// consumed for good on one timeout. The run is held instead: what is in front
// of it is sent and the offset stops at its first record. The file is then
// left alone for lookupRetryInterval -- not scanned, its cwds not looked up --
// and when git answers, the rest goes.
func TestJudgedTail_uncertainRunHoldsFromItsFirstRecord(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)}
	calls := stubRepos(t, repos)
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"))
	heldAt := fileSize(t, p)
	appendLines(t, p, sessionLine("/work/c", "2"), sessionLine("", "2 meta"), sessionLine("/work/a", "3"))

	syncPass(t, s)
	wantSent(t, srv, "1")
	wantOffset(t, s, p, heldAt)
	if got := reloadState(t, s).Files[p]; got.Offset != heldAt || got.CWD != "/work/a" {
		t.Fatalf("saved offset %d cwd %q, want %d /work/a", got.Offset, got.CWD, heldAt)
	}

	clear(calls)
	appendLines(t, p, sessionLine("/work/a", "4"))
	advance(lookupRetryInterval - time.Second)
	syncPass(t, s)
	wantSent(t, srv, "1")
	if len(calls) != 0 {
		t.Fatalf("lookups inside the retry interval: %v, want none", calls)
	}

	advance(time.Second)
	syncPass(t, s)
	wantSent(t, srv, "1")
	wantOffset(t, s, p, heldAt)
	if calls["/work/c"] != 1 {
		t.Fatalf("%d lookups of the held cwd after the retry interval, want 1", calls["/work/c"])
	}

	repos["/work/c"] = repoAt(allowedRepoTwo)
	advance(lookupRetryInterval)
	syncPass(t, s)
	wantSent(t, srv, "1", "2", "2 meta", "3", "4")
	wantOffset(t, s, p, fileSize(t, p))
}

// When only part of a tail is consumed, the attribution state saved with the
// offset is the state at that record, not at the end of what was read: the
// records behind it are read again from there.
func TestJudgedTail_partialAdvanceKeepsAttributionStateAtTheOffset(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(allowedRepoTwo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, lineWith("assistant", `"cwd":"/work/a","attributionSkill":"deploy",`, "1"))
	afterFirst := fileSize(t, p)
	appendLines(t, p, sessionLine("/work/b", "a new prompt ends the skill"))

	srv.refuseText(t, "a new prompt ends the skill")
	syncPassFailing(t, s)

	wantOffset(t, s, p, afterFirst)
	if got := reloadState(t, s).Files[p].LastAttributionSkill; got != "deploy" {
		t.Fatalf("saved skill %q with the offset, want deploy", got)
	}
}

// Each run of records goes out in a request of its own identity. Stamping a
// record with another repository inside one envelope cannot say "this record
// has no subpath and no branch" -- the server fills blanks from the envelope --
// and two worktrees of one remote would share whichever HEAD came first.
func TestJudgedTail_oneRequestPerIdentity(t *testing.T) {
	main := gitctx.Context{RepositoryID: allowedRepo, RepositoryIDSource: "resolved", RepoSubpath: "src/", RepoSubpathPresent: true, CommitSHA: "aaa", Branch: "main"}
	feature := main
	feature.RepoSubpath, feature.CommitSHA, feature.Branch = "", "bbb", "feature"
	detached := gitctx.Context{RepositoryID: allowedRepoTwo, RepositoryIDSource: "resolved", RepoSubpathPresent: true, CommitSHA: "ccc"}
	stubRepos(t, map[string]gitctx.Context{"/work/main/src": main, "/work/feature": feature, "/work/detached": detached})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p,
		sessionLine("/work/main/src", "1"), sessionLine("", "1 meta"),
		sessionLine("/work/feature", "2"),
		sessionLine("/work/detached", "3"),
		sessionLine("/work/main/src", "4"))

	syncPass(t, s)

	posts := srv.recordPosts()
	want := []gitctx.Context{main, feature, detached, main}
	if len(posts) != len(want) {
		t.Fatalf("got %d record requests, want %d", len(posts), len(want))
	}
	for i, post := range posts {
		got := gitctx.Context{RepositoryID: post.RepositoryID, RepositoryIDSource: post.RepositoryIDSource, RepoSubpath: post.RepoSubpath, RepoSubpathPresent: post.RepoSubpathPresent, CommitSHA: post.CommitSHA, Branch: post.Branch}
		if got != want[i] {
			t.Errorf("request %d envelope = %+v, want %+v", i, got, want[i])
		}
		if post.ProjectHash != "-proj-a" {
			t.Errorf("request %d project hash = %q, want the file's", i, post.ProjectHash)
		}
		for _, r := range post.Records {
			if r.RepositoryID != "" || r.CommitSHA != "" || r.Branch != "" || r.RepoSubpath != "" {
				t.Errorf("request %d carries a record stamped with its own identity: %+v", i, r)
			}
		}
	}
	wantSent(t, srv, "1", "1 meta", "2", "3", "4")
}

// Each request that gets through moves the offset to the end of what it
// carried. When a later one fails, the earlier ones are not sent again.
func TestJudgedTail_offsetAdvancesWithEachRequest(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(allowedRepoTwo), "/work/x": repoAt(excludedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"))
	afterFirst := fileSize(t, p)
	appendLines(t, p, sessionLine("/work/x", "excluded"), sessionLine("/work/b", "2"), sessionLine("/work/a", "3"))

	stop := srv.refuseText(t, "2")
	syncPassFailing(t, s)
	wantSent(t, srv, "1")
	wantOffset(t, s, p, afterFirst)

	stop()
	syncPass(t, s)
	wantSent(t, srv, "1", "2", "3")
	wantOffset(t, s, p, fileSize(t, p))
}

// A file whose first lines carry no cwd -- most real session files -- has them
// judged with the first cwd that follows. While no line has one yet, nothing
// is consumed and nothing is held: the file is simply read again.
func TestJudgedTail_leadingLinesWithoutCWDTakeTheFirstOne(t *testing.T) {
	calls := stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, lineWith("queue-operation", ``, "queued"), lineWith("queue-operation", ``, "dequeued"))

	syncPass(t, s)
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, p, 0)
	if len(calls) != 0 {
		t.Fatalf("lookups with no cwd to look up: %v", calls)
	}

	appendLines(t, p, sessionLine("/work/a", "1"))
	syncPass(t, s)
	wantSent(t, srv, "queued", "dequeued", "1")
	wantOffset(t, s, p, fileSize(t, p))
}

// A hold on the very first record consumes nothing, not even an unreadable
// line in front of it. Moving the offset there would make the file one that
// "starts part-way in" with no cwd before it, and its leading lines would
// stop taking the first cwd that follows.
func TestJudgedTail_holdOnFirstRecordLeavesTheOffsetAtZero(t *testing.T) {
	repos := map[string]gitctx.Context{}
	stubRepos(t, repos)
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, "not json", sessionLine("", "meta"), sessionLine("/work/c", "1"))

	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, p, 0)

	repos["/work/c"] = repoAt(allowedRepo)
	advance(lookupRetryInterval)
	syncPass(t, s)
	wantSent(t, srv, "meta", "1")
}

// A tail that starts with lines carrying no cwd continues from the cwd the
// consumed bytes ended in, not from the head of the file. Each case reaches
// that point a different way; in all of them the session started in an
// allowed repository and moved to an excluded one, so the head would send
// what must not leave.
func TestJudgedTail_tailWithoutCWDContinuesFromTheLastOne(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)}
	history := []string{sessionLine("/work/a", "old 1"), sessionLine("/work/b", "old 2"), sessionLine("", "old meta")}

	t.Run("after the first sync skipped the file", func(t *testing.T) {
		stubRepos(t, repos)
		useFakeClock(t)
		srv, endpoint := newFreshMetaServer(t)
		s, dir := newFirstRunSyncer(t, endpoint, allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		appendLines(t, p, history...)
		syncPass(t, s)

		appendLines(t, p, sessionLine("", "secret"))
		syncPass(t, s)
		wantSent(t, srv)
		wantOffset(t, s, p, fileSize(t, p))
	})

	t.Run("after a shrink reset", func(t *testing.T) {
		stubRepos(t, repos)
		useFakeClock(t)
		srv, endpoint := newFreshMetaServer(t)
		s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		appendLines(t, p, sessionLine("/work/a", "1"), sessionLine("/work/a", "2"), sessionLine("/work/a", "3"), sessionLine("/work/a", "4"))
		syncPass(t, s)
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatalf("rewrite: %v", err)
		}
		appendLines(t, p, history...)
		syncPass(t, s)

		appendLines(t, p, sessionLine("", "secret"))
		syncPass(t, s)
		wantSent(t, srv, "1", "2", "3", "4")
		wantOffset(t, s, p, fileSize(t, p))
	})

	t.Run("from a state written before the cwd was recorded", func(t *testing.T) {
		stubRepos(t, repos)
		useFakeClock(t)
		srv, endpoint := newFreshMetaServer(t)
		s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		appendLines(t, p, history...)
		s.state.SetOffset(p, fileSize(t, p))

		appendLines(t, p, sessionLine("", "secret"))
		syncPass(t, s)
		wantSent(t, srv)
		wantOffset(t, s, p, fileSize(t, p))
		if got := reloadState(t, s).Files[p].CWD; got != "/work/b" {
			t.Fatalf("recovered CWD = %q, want /work/b", got)
		}
	})
}

// The other direction of the same rule: the head is excluded and the session
// has moved to an allowed repository, so judging by the head would drop what
// should be collected.
func TestJudgedTail_tailWithoutCWDIsNotJudgedByAnExcludedHead(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/b", "old 1"), sessionLine("/work/a", "old 2"))
	s.state.SetOffset(p, fileSize(t, p))

	appendLines(t, p, sessionLine("", "kept"))
	syncPass(t, s)
	wantSent(t, srv, "kept")
}

// When the cwd the consumed bytes ended in cannot be found, lines without one
// are not guessed at: they hold, and the file's head is not consulted.
func TestJudgedTail_unknownLastCWDHolds(t *testing.T) {
	calls := stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newFirstRunSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("", "old meta"))
	syncPass(t, s)
	held := fileSize(t, p)

	appendLines(t, p, sessionLine("", "unknown"), sessionLine("/work/a", "known"))
	syncPass(t, s)
	wantSent(t, srv)
	wantOffset(t, s, p, held)
	if calls["/work/a"] != 1 {
		t.Fatalf("%d lookups of the cwd behind the hold, want 1: every cwd of a tail is looked up", calls["/work/a"])
	}
}

// The scanner drops a line over its size limit without yielding a record, and
// with it whatever the line said about the cwd. The session below moved to an
// excluded repository in such a line; the small line after it carries no cwd
// and would continue from the allowed one the file's state still names. So a
// skipped line breaks the chain: what follows has an unknown cwd until a line
// names one, and is held, not sent.
func TestJudgedTail_skippedOversizedLineBreaksTheCWDChain(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)}

	t.Run("skipped line and the line after it in one pass", func(t *testing.T) {
		stubRepos(t, repos)
		useFakeClock(t)
		srv, endpoint := newFreshMetaServer(t)
		s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		appendLines(t, p, sessionLine("/work/a", "1"))
		syncPass(t, s)

		appendLines(t, p, oversizedLine(t, "/work/b"))
		small := fileSize(t, p)
		appendLines(t, p, sessionLine("", "b-private-small"))
		syncPass(t, s)
		wantSent(t, srv, "1")
		// The skipped line is passed -- it is not read again every retry --
		// and the state says the chain is broken there.
		wantOffset(t, s, p, small)
		if fs := reloadState(t, s).Files[p]; fs.CWD != "" || !fs.CWDUnknown {
			t.Fatalf("CWD = %q unknown = %v past a skipped line, want unknown", fs.CWD, fs.CWDUnknown)
		}
	})

	t.Run("skipped line alone, the line after it in a later pass", func(t *testing.T) {
		stubRepos(t, repos)
		useFakeClock(t)
		srv, endpoint := newFreshMetaServer(t)
		s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		appendLines(t, p, sessionLine("/work/a", "1"))
		syncPass(t, s)

		appendLines(t, p, oversizedLine(t, "/work/b"))
		small := fileSize(t, p)
		syncPass(t, s)
		wantOffset(t, s, p, small)
		if fs := reloadState(t, s).Files[p]; fs.CWD != "" || !fs.CWDUnknown {
			t.Fatalf("CWD = %q unknown = %v after a pass that only skipped a line, want unknown", fs.CWD, fs.CWDUnknown)
		}

		appendLines(t, p, sessionLine("", "b-private-small"))
		syncPass(t, s)
		wantSent(t, srv, "1")
		wantOffset(t, s, p, small)
	})

	t.Run("skipped line at the end of a tail that was sent", func(t *testing.T) {
		stubRepos(t, repos)
		useFakeClock(t)
		srv, endpoint := newFreshMetaServer(t)
		s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		appendLines(t, p, sessionLine("/work/a", "1"), oversizedLine(t, "/work/b"))
		small := fileSize(t, p)
		syncPass(t, s)
		wantSent(t, srv, "1")
		wantOffset(t, s, p, small)

		appendLines(t, p, sessionLine("", "b-private-small"))
		syncPass(t, s)
		wantSent(t, srv, "1")
	})

	t.Run("a line that names its cwd restores the chain", func(t *testing.T) {
		stubRepos(t, repos)
		useFakeClock(t)
		srv, endpoint := newFreshMetaServer(t)
		s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		appendLines(t, p, sessionLine("/work/a", "1"))
		syncPass(t, s)

		appendLines(t, p, oversizedLine(t, "/work/b"), sessionLine("/work/a", "2"), sessionLine("", "2 meta"))
		syncPass(t, s)
		wantSent(t, srv, "1", "2", "2 meta")
		wantOffset(t, s, p, fileSize(t, p))
	})

	t.Run("before the first cwd of a new file", func(t *testing.T) {
		stubRepos(t, repos)
		useFakeClock(t)
		srv, endpoint := newFreshMetaServer(t)
		s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		appendLines(t, p, sessionLine("", "lead"), oversizedLine(t, "/work/b"), sessionLine("", "b-private-small"), sessionLine("/work/a", "1"))
		syncPass(t, s)
		wantSent(t, srv)
		wantOffset(t, s, p, 0)
	})
}

// With an allowlist an idle file whose cwd git cannot answer for is not
// refreshed under a fallback identity, and git is asked again once per
// lookupRetryInterval rather than once per pass.
func TestJudgedTail_idleUncertainLookupIsRetriedAtTheInterval(t *testing.T) {
	calls := stubResolveGitChecked(t, uncertainLookup)
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"))
	s.state.SetOffset(p, fileSize(t, p))

	for i := 0; i < 5; i++ {
		syncPass(t, s)
		advance(time.Second)
	}
	if calls["/work/a"] != 1 {
		t.Fatalf("five idle passes ran %d lookups, want 1", calls["/work/a"])
	}
	advance(lookupRetryInterval)
	syncPass(t, s)
	if calls["/work/a"] != 2 {
		t.Fatalf("%d lookups after the retry interval, want 2", calls["/work/a"])
	}
	if got := len(srv.sentSyncs()); got != 0 {
		t.Fatalf("sent %d requests for a cwd git could not answer for", got)
	}
}

// Records of an excluded account are dropped inside a judged run like anywhere
// else, and the offset moves past them.
func TestJudgedTail_excludedAccountRecordsAreConsumed(t *testing.T) {
	stubResolveGit(t, func(string) gitctx.Context { return repoAt(allowedRepo) })
	claudeDir, sessionPath, state := exclusionFixture(t, "acct-personal")
	var payloads []SyncPayload
	var queries [][]AccountRef
	srv := exclusionServer(t, map[string]bool{}, &payloads, &queries)
	client := NewClient(srv.URL, "", "")
	client.SetExcludedAccounts([]string{"anthropic:acct-personal"})
	s := New(claudeDir, "p@example.com", "u1", state, client, allowExampleOrg)

	syncPass(t, s)

	if n := len(allRecords(payloads)); n != 0 {
		t.Fatalf("sent %d records of an excluded account", n)
	}
	wantOffset(t, s, sessionPath, fileSize(t, sessionPath))
}
