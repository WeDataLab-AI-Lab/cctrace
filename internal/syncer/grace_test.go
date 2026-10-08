package syncer

import (
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/gitctx"
)

// The tests in this file swap resolveGit and nowFn, so they cannot run in
// parallel with the rest of the package.

// graceFixture is a session in a cwd that resolved to an allowed repository,
// had one record sent, and then -- with one more record pending -- certainly
// reports no repository at all.
func graceFixture(t *testing.T) (s *Syncer, srv *freshMetaServer, repos map[string]gitctx.Context, advance func(time.Duration), p string) {
	t.Helper()
	repos = map[string]gitctx.Context{"/work/r": repoAt(allowedRepo)}
	stubRepos(t, repos)
	advance = useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p = filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "1"))
	syncPass(t, s)
	appendLines(t, p, sessionLine("/work/r", "2"))
	repos["/work/r"] = notARepo()
	return s, srv, repos, advance, p
}

// A cwd that was a repository a moment ago and now certainly is none looks
// exactly like a volume not remounted after wake, or a share reconnecting.
// Believing it at once would drop an allowed repository's records for good, so
// for repositoryLostGrace its records are held; when the repository answers
// again they are sent, and nothing was a transition.
func TestGrace_lostRepositoryHoldsAndRecovers(t *testing.T) {
	s, srv, repos, advance, p := graceFixture(t)
	before := s.state.GetOffset(p)

	syncPass(t, s)
	retryHeld(t, s, advance, 3)
	wantSent(t, srv, "1")
	wantOffset(t, s, p, before)
	st := reloadState(t, s)
	if id := st.CWDIdentity["/work/r"]; id.RepositoryID != allowedRepo || id.FallbackSince.IsZero() {
		t.Fatalf("CWDIdentity = %+v, want %s with the grace started", id, allowedRepo)
	}

	repos["/work/r"] = repoAt(allowedRepo)
	retryHeld(t, s, advance, 1)
	wantSent(t, srv, "1", "2")
	st = reloadState(t, s)
	if id := st.CWDIdentity["/work/r"]; id.RepositoryID != allowedRepo || !id.FallbackSince.IsZero() {
		t.Fatalf("CWDIdentity = %+v after the repository came back, want the grace ended", id)
	}
	if len(st.Taints) != 0 {
		t.Fatalf("Taints = %+v, want none: nothing changed repository", st.Taints)
	}
}

// When the cwd keeps reporting no repository for the whole grace it is
// believed: that is a transition like any other, to a local identity the
// allowlist does not cover, and the cwd's records are consumed. Until the
// grace has passed they are still there -- including across a restart, which
// does not start the grace over.
func TestGrace_lostRepositoryIsBelievedAfterTheGrace(t *testing.T) {
	s, srv, _, advance, p := graceFixture(t)
	before := s.state.GetOffset(p)
	syncPass(t, s)

	s = restartSyncer(t, s)
	advance(repositoryLostGrace - time.Second)
	syncPass(t, s)
	wantOffset(t, s, p, before)

	advance(lookupRetryInterval)
	syncPass(t, s)
	wantSent(t, srv, "1")
	wantOffset(t, s, p, fileSize(t, p))
	st := reloadState(t, s)
	if id := st.CWDIdentity["/work/r"]; id.RepositoryID != notARepo().RepositoryID || !id.FallbackSince.IsZero() {
		t.Fatalf("CWDIdentity = %+v, want the local fallback recorded", id)
	}
}

// A cwd that never resolved to a repository gets no grace: there is nothing
// its "no repository" could be a brief loss of.
func TestGrace_neverResolvedCWDIsJudgedAtOnce(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/plain": notARepo()})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/plain", "1"))

	syncPass(t, s)

	wantSent(t, srv)
	wantOffset(t, s, p, fileSize(t, p))
	if id := reloadState(t, s).CWDIdentity["/work/plain"]; !id.FallbackSince.IsZero() {
		t.Fatalf("CWDIdentity = %+v, want no grace", id)
	}
}
