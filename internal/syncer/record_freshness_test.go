package syncer

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"cctrace/internal/gitctx"
)

// The tests in this file swap resolveGit and nowFn, so they cannot run in
// parallel with the rest of the package.
//
// They pin the path with no allowlist: what goes out, where the offset ends
// and how many requests carry it stay what they were, and the one thing that
// changes is that records carry this pass's lookup -- all of it: repository
// identity, commit and branch -- instead of one cached up to metaTTL earlier.

// A backlog written on main and sent after a checkout of feature carries
// feature, as it would have had the pass run then. What "current" is not is
// the commit the line was written at.
func TestRecordFreshness_sendsThisPassCommitAndBranch(t *testing.T) {
	g := gitctx.Context{RepositoryID: allowedRepo, RepositoryIDSource: "resolved", CommitSHA: "aaa", Branch: "main"}
	stubResolveGit(t, func(string) gitctx.Context { return g })
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"))
	syncPass(t, s)

	g.CommitSHA, g.Branch = "bbb", "feature"
	advance(time.Minute)
	appendLines(t, p, sessionLine("/work/a", "2"))
	syncPass(t, s)

	posts := srv.recordPosts()
	if last := posts[len(posts)-1]; last.CommitSHA != "bbb" || last.Branch != "feature" {
		t.Fatalf("records sent with %q %q, want bbb feature", last.CommitSHA, last.Branch)
	}
}

// The lookup is used whole. When the cwd certainly holds another repository
// now, the next record goes out under that repository -- id, remote and name
// together with its commit and branch -- and not an hour later. Keeping the
// cached identity while taking the new commit would file one repository's
// commit under another's name.
func TestRecordFreshness_emptyAllowlistSendsANewRepositoryAtOnce(t *testing.T) {
	g := gitctx.Context{RepositoryID: allowedRepo, RepositoryIDSource: "resolved", RepositoryName: "cctrace", GitRemoteURL: "https://" + allowedRepo + ".git", RepoSubpath: "src/", RepoSubpathPresent: true, CommitSHA: "aaa", Branch: "main"}
	stubResolveGit(t, func(string) gitctx.Context { return g })
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"))
	syncPass(t, s)

	// The new checkout is at its root: an empty subpath that is present, which
	// must replace the cached "src/" rather than be filled in from it.
	g = gitctx.Context{RepositoryID: allowedRepoTwo, RepositoryIDSource: "resolved", RepositoryName: "other-repo", GitRemoteURL: "https://" + allowedRepoTwo + ".git", RepoSubpathPresent: true, CommitSHA: "bbb", Branch: "feature"}
	advance(time.Minute)
	appendLines(t, p, sessionLine("/work/a", "2"))
	syncPass(t, s)

	posts := srv.recordPosts()
	last := posts[len(posts)-1]
	got := gitctx.Context{RepositoryID: last.RepositoryID, RepositoryIDSource: last.RepositoryIDSource, RepositoryName: last.RepositoryName, GitRemoteURL: last.GitRemoteURL, RepoSubpath: last.RepoSubpath, RepoSubpathPresent: last.RepoSubpathPresent, CommitSHA: last.CommitSHA, Branch: last.Branch}
	if got != g {
		t.Fatalf("second record sent under %+v, want this pass's lookup %+v", got, g)
	}
}

// The fresh lookup costs one per cwd per pass with records, shared by every
// file in that cwd; a pass with nothing new costs none.
func TestRecordFreshness_oneLookupPerCWDPerPass(t *testing.T) {
	calls := stubResolveGit(t, func(string) gitctx.Context { return repoAt(allowedRepo) })
	advance := useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p1, p2 := filepath.Join(dir, "s1.jsonl"), filepath.Join(dir, "s2.jsonl")
	appendLines(t, p1, sessionLine("/work/a", "1"))
	appendLines(t, p2, sessionLine("/work/a", "1"))
	syncPass(t, s)

	advance(time.Minute)
	appendLines(t, p1, sessionLine("/work/a", "2"))
	appendLines(t, p2, sessionLine("/work/a", "2"))
	clear(calls)
	syncPass(t, s)
	if calls["/work/a"] != 1 {
		t.Fatalf("pass with records in two files of one cwd ran %d lookups, want 1", calls["/work/a"])
	}

	clear(calls)
	syncPass(t, s)
	if calls["/work/a"] != 0 {
		t.Fatalf("idle pass ran %d lookups, want 0", calls["/work/a"])
	}
}

// A tail that crosses cwds is still one request under the first cwd's
// envelope, carrying every record, with the offset at the end of the file:
// without an allowlist nothing is judged, split or held.
func TestRecordFreshness_emptyAllowlistSendsTailAsOneRequest(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("", "head"), sessionLine("/work/a", "1"), sessionLine("/work/b", "2"), sessionLine("/work/gone", "3"))

	if n := syncPass(t, s); n != 4 {
		t.Fatalf("SyncOnce sent %d records, want 4", n)
	}
	posts := srv.recordPosts()
	if len(posts) != 1 || posts[0].RepositoryID != allowedRepo {
		t.Fatalf("got %d record requests (first under %q), want 1 under %q", len(posts), posts[0].RepositoryID, allowedRepo)
	}
	if got, want := srv.sentTexts(t), []string{"head", "1", "2", "3"}; !slices.Equal(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
	if got := s.state.GetOffset(p); got != fileSize(t, p) {
		t.Fatalf("offset = %d, want the end of the file %d", got, fileSize(t, p))
	}
}

// With no allowlist an uncertain lookup holds nothing, and the records go out
// under the last certain identity still in the metaTTL cache -- what the cache
// served before records looked git up every pass -- rather than a fallback.
// While git keeps failing it is asked again once per lookupRetryInterval, not
// once per pass.
func TestRecordFreshness_emptyAllowlistUncertainUsesWarmCache(t *testing.T) {
	certain := true
	calls := stubResolveGitChecked(t, func(cwd string) (gitctx.Context, error) {
		if certain {
			return repoAt(allowedRepo), nil
		}
		return uncertainLookup(cwd)
	})
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"))
	syncPass(t, s)

	certain = false
	clear(calls)
	for i := 0; i < 3; i++ {
		advance(time.Second)
		appendLines(t, p, sessionLine("/work/a", "more"))
		syncPass(t, s)
	}
	posts := srv.recordPosts()
	if len(posts) != 4 {
		t.Fatalf("got %d record requests, want 4: an uncertain lookup holds nothing here", len(posts))
	}
	for _, post := range posts {
		if post.RepositoryID != allowedRepo {
			t.Fatalf("records sent under %q, want the cached %q", post.RepositoryID, allowedRepo)
		}
	}
	if calls["/work/a"] != 1 {
		t.Fatalf("three failing passes inside the retry interval ran %d lookups, want 1", calls["/work/a"])
	}

	advance(lookupRetryInterval)
	appendLines(t, p, sessionLine("/work/a", "later"))
	syncPass(t, s)
	if calls["/work/a"] != 2 {
		t.Fatalf("%d lookups after the retry interval, want 2", calls["/work/a"])
	}
}

// With nothing certain cached, the uncertain fallback is what goes out, as
// before -- there is nothing better to send.
func TestRecordFreshness_emptyAllowlistUncertainColdSendsFallback(t *testing.T) {
	stubResolveGitChecked(t, uncertainLookup)
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	appendLines(t, filepath.Join(dir, "s1.jsonl"), sessionLine("/work/a", "1"))
	syncPass(t, s)

	want, _ := uncertainLookup("/work/a")
	posts := srv.recordPosts()
	if len(posts) != 1 || posts[0].RepositoryID != want.RepositoryID {
		t.Fatalf("record requests = %+v, want one under the fallback %q", posts, want.RepositoryID)
	}
}

// A cwd whose repository is gone answers, certainly, with a local fallback.
// The warm cache used to keep serving the repository it had resolved until
// metaTTL ran out, and the records still carry it.
func TestRecordFreshness_emptyAllowlistLostRepositoryKeepsWarmIdentity(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)}
	stubRepos(t, repos)
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"))
	syncPass(t, s)

	repos["/work/a"] = notARepo()
	for i := 0; i < 2; i++ {
		advance(time.Minute)
		appendLines(t, p, sessionLine("/work/a", "after"))
		syncPass(t, s)
	}
	posts := srv.recordPosts()
	if len(posts) != 3 {
		t.Fatalf("got %d record requests, want 3", len(posts))
	}
	for _, post := range posts {
		if post.RepositoryID != allowedRepo {
			t.Fatalf("records sent under %q, want the warm %q", post.RepositoryID, allowedRepo)
		}
	}

	// Once the cached entry has aged out, the fallback is the answer.
	expireMetaCache(s)
	advance(time.Minute)
	appendLines(t, p, sessionLine("/work/a", "cold"))
	syncPass(t, s)
	posts = srv.recordPosts()
	if got := posts[len(posts)-1].RepositoryID; got != notARepo().RepositoryID {
		t.Fatalf("records sent under %q after the cache aged out, want the fallback", got)
	}

	// None of this is a recorded transition: without an allowlist the state
	// keeps no identity per cwd and marks no file.
	if st := reloadState(t, s); len(st.CWDIdentity) != 0 || len(st.Taints) != 0 {
		t.Fatalf("state recorded CWDIdentity %v Taints %v without an allowlist", st.CWDIdentity, st.Taints)
	}
}

// Idle files whose cwd git cannot answer for cost one lookup between them, have
// their metadata upserted once each with the fallback, and are then left alone
// for metaTTL like any other idle file: a git that stays broken does not cost a
// lookup per file or per pass.
func TestRecordFreshness_emptyAllowlistIdleFailureIsNotRetriedEveryPass(t *testing.T) {
	calls := stubResolveGitChecked(t, uncertainLookup)
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	other := sessionDir(t, dir, "-proj-b")
	for _, p := range []string{filepath.Join(dir, "s1.jsonl"), filepath.Join(other, "s2.jsonl")} {
		appendLines(t, p, sessionLine("/work/a", "1"))
		s.state.SetOffset(p, fileSize(t, p))
	}

	for i := 0; i < 5; i++ {
		syncPass(t, s)
		advance(time.Minute)
	}
	if calls["/work/a"] != 1 {
		t.Fatalf("five idle passes over two files ran %d lookups, want 1", calls["/work/a"])
	}
	if got := len(srv.sentSyncs()); got != 2 {
		t.Fatalf("five idle passes sent %d metadata upserts, want one per project", got)
	}
	for _, hash := range []string{"-proj-a", "-proj-b"} {
		if _, sent := s.metaSent[hash]; !sent {
			t.Fatalf("metaSent not recorded for %s after the upsert", hash)
		}
	}
}
