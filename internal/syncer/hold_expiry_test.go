package syncer

import (
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/gitctx"
)

// The tests in this file swap resolveGit, nowFn and heldExpiry, so they cannot
// run in parallel with the rest of the package.

// shortHeldExpiry makes a hold expire after n retries instead of a day of
// them, so a test crosses the bound in a handful of passes.
func shortHeldExpiry(t *testing.T, retries int) {
	t.Helper()
	prev := heldExpiry
	heldExpiry = time.Duration(retries) * lookupRetryInterval
	t.Cleanup(func() { heldExpiry = prev })
}

// A hold is written down per cwd -- since when, why, and how long it has been
// seen failing -- so `cctrace status` can say a session is waiting, and the
// record goes when git answers.
func TestHold_isRecordedAndClearedWhenGitAnswers(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)}
	stubRepos(t, repos)
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"), sessionLine("/work/c", "2"))
	began := nowFn()

	syncPass(t, s)
	retryHeld(t, s, advance, 2)
	want := HeldRun{Since: began, Reason: HeldReasonGitUncertain, Observed: 2 * lookupRetryInterval, LastFailedAt: nowFn()}
	held := reloadState(t, s).Files[p].Held
	if len(held) != 1 || held["/work/c"] != want {
		t.Fatalf("Held = %+v, want /work/c: %+v", held, want)
	}

	repos["/work/c"] = repoAt(allowedRepo)
	retryHeld(t, s, advance, 1)
	wantSent(t, srv, "1", "2")
	if st := reloadState(t, s).Files[p]; len(st.Held) != 0 || st.HoldExpired != nil {
		t.Fatalf("after git answered: Held = %+v HoldExpired = %+v, want neither", st.Held, st.HoldExpired)
	}
}

// The grace of a cwd that lost its repository is a hold like the others: it is
// written down with its own reason while it lasts, and gone once the loss is
// believed.
func TestHold_repositoryLostGraceIsRecorded(t *testing.T) {
	s, _, _, advance, p := graceFixture(t)
	syncPass(t, s)
	if got := reloadState(t, s).Files[p].Held["/work/r"].Reason; got != HeldReasonRepositoryLostGrace {
		t.Fatalf("held reason = %q, want %s", got, HeldReasonRepositoryLostGrace)
	}

	advance(repositoryLostGrace)
	syncPass(t, s)
	if st := reloadState(t, s).Files[p]; len(st.Held) != 0 || st.HoldExpired != nil {
		t.Fatalf("after the grace: Held = %+v HoldExpired = %+v, want neither", st.Held, st.HoldExpired)
	}
}

// A hold that never ends would stall the file, and every allowed run behind
// it, forever. Once it has been seen failing for heldExpiry its cwd counts as
// an unknown repository -- not allowed -- and only that cwd's records are
// dropped: the rest of the tail is judged as usual, and the loss is recorded.
func TestHold_expiresIntoALossOfThatRunOnly(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	advance := useFakeClock(t)
	shortHeldExpiry(t, 3)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"))
	heldAt := fileSize(t, p)
	appendLines(t, p, sessionLine("/work/c", "2"), sessionLine("/work/a", "3"), sessionLine("/work/c", "4"), sessionLine("/work/a", "5"))

	syncPass(t, s)
	retryHeld(t, s, advance, 2)
	wantSent(t, srv, "1")
	wantOffset(t, s, p, heldAt)
	if st := reloadState(t, s).Files[p]; st.HoldExpired != nil {
		t.Fatalf("HoldExpired = %+v before the hold expired", st.HoldExpired)
	}

	retryHeld(t, s, advance, 1)
	wantSent(t, srv, "1", "3", "5")
	wantOffset(t, s, p, fileSize(t, p))
	st := reloadState(t, s).Files[p]
	if want := (HoldExpired{At: nowFn(), Reason: HeldReasonGitUncertain, CWD: "/work/c"}); st.HoldExpired == nil || *st.HoldExpired != want {
		t.Fatalf("HoldExpired = %+v, want %+v", st.HoldExpired, want)
	}
	if len(st.Held) != 0 {
		t.Fatalf("Held = %+v after the expired run was passed, want none: the next failure starts a new clock", st.Held)
	}

	// The next uncertain run of that cwd starts over.
	appendLines(t, p, sessionLine("/work/c", "6"))
	syncPass(t, s)
	if got := reloadState(t, s).Files[p].Held["/work/c"].Observed; got != 0 {
		t.Fatalf("a new hold began with %s already observed", got)
	}
}

// Lines whose cwd could not be recovered expire the same way, and only they:
// once a line names its cwd, what follows is judged normally.
func TestHold_unknownCWDExpiresAndTheRestIsJudged(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	advance := useFakeClock(t)
	shortHeldExpiry(t, 2)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newFirstRunSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("", "old meta"))
	syncPass(t, s)

	appendLines(t, p, sessionLine("", "unknown"), sessionLine("/work/a", "known"), sessionLine("", "known meta"))
	syncPass(t, s)
	retryHeld(t, s, advance, 2)

	wantSent(t, srv, "known", "known meta")
	st := reloadState(t, s).Files[p]
	if st.HoldExpired == nil || st.HoldExpired.Reason != HeldReasonCWDUnknown {
		t.Fatalf("HoldExpired = %+v, want reason %s", st.HoldExpired, HeldReasonCWDUnknown)
	}
	if st.CWD != "/work/a" || st.CWDUnknown {
		t.Fatalf("CWD = %q unknown = %v after a line named it, want /work/a", st.CWD, st.CWDUnknown)
	}
}

// The loss is recorded by the save that moves the offset past the dropped
// run, and by no other. In the pass that decides the hold has expired, a send
// further on fails: the offset has not passed the run, so nothing is lost yet
// -- and when git answers on the next pass, nothing ever is. A loss notice
// written at the decision would have stayed in status for good.
func TestHold_expiryIsNotRecordedUntilTheOffsetPassesIt(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)}
	stubRepos(t, repos)
	advance := useFakeClock(t)
	shortHeldExpiry(t, 2)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/c", "held"), sessionLine("/work/a", "behind"))
	syncPass(t, s)
	retryHeld(t, s, advance, 1)

	stop := srv.refuseText(t, "behind")
	advance(lookupRetryInterval)
	syncPassFailing(t, s)
	wantOffset(t, s, p, 0)
	if st := reloadState(t, s).Files[p]; st.HoldExpired != nil {
		t.Fatalf("HoldExpired = %+v although the offset never passed the run", st.HoldExpired)
	}

	stop()
	repos["/work/c"] = repoAt(allowedRepoTwo)
	syncPass(t, s)
	wantSent(t, srv, "held", "behind")
	if st := reloadState(t, s).Files[p]; st.HoldExpired != nil || len(st.Held) != 0 {
		t.Fatalf("after recovery: HoldExpired = %+v Held = %+v, want neither", st.HoldExpired, st.Held)
	}
}

// What expires a hold is time seen failing, not time since it began. A daemon
// that was stopped for two days has not been retrying for two days, and its
// first failure after the restart counts for one retry.
func TestHold_downtimeDoesNotCountTowardsExpiry(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	advance := useFakeClock(t)
	shortHeldExpiry(t, 4)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/c", "held"))
	syncPass(t, s)
	retryHeld(t, s, advance, 1)

	s = restartSyncer(t, s)
	advance(48 * time.Hour)
	syncPass(t, s)

	wantSent(t, srv)
	wantOffset(t, s, p, 0)
	st := reloadState(t, s).Files[p]
	if st.HoldExpired != nil {
		t.Fatalf("HoldExpired = %+v after one failure following downtime", st.HoldExpired)
	}
	if got, want := st.Held["/work/c"].Observed, 3*lookupRetryInterval; got != want {
		t.Fatalf("Observed = %s, want %s: one retry before the stop, at most two intervals for the gap", got, want)
	}
}
