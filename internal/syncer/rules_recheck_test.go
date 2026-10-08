package syncer

import (
	"testing"

	"cctrace/internal/gitctx"
)

// These tests reach sendProjectRules through the no-records path only: the
// first pass sends the session's one record, and every later pass has nothing
// new, so the metadata they act on comes from the metaTTL cache and nothing on
// the record path can refresh it first. Forgetting metaSent is what sends the
// idle pass on to the metadata upsert and the rule scan, as an expired metaTTL
// would.
//
// Like the rest of this package's resolveGit tests, they swap package state and
// cannot run in parallel.

// A due scan of a cwd that now holds an excluded repository sends no rules, and
// finds that out with one lookup.
func TestSendProjectRules_idleDueScanRechecksAllowlist(t *testing.T) {
	root := ruleRepo(t)
	meta := gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/example-org/cctrace", RepositoryIDSource: "resolved", CommitSHA: "aaa", Branch: "main"}
	calls := stubResolveGit(t, func(string) gitctx.Context { return meta })
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, _ := newFreshMetaSyncer(t, endpoint, []string{root}, []string{"github.com/example-org/"})

	syncPass(t, s)
	if got := len(srv.sentRules()); got != 1 {
		t.Fatalf("first pass sent %d rule requests, want 1", got)
	}

	meta = gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/org/repo", RepositoryIDSource: "resolved", CommitSHA: "bbb", Branch: "main"}
	advance(ruleScanTTL)
	clear(s.metaSent)
	clear(calls)
	syncPass(t, s)
	if extra := srv.sentRules()[1:]; len(extra) != 0 {
		t.Fatalf("rules sent for a cwd that left the allowlist: %+v", extra)
	}
	if calls[root] != 1 {
		t.Errorf("resolveGit called %d times for one due scan, want 1", calls[root])
	}
}

// A due scan sends the commit and branch HEAD has now, even when the metadata
// that let the scan start is an hour-cache entry from before HEAD moved.
func TestSendProjectRules_idleDueScanSendsFreshHead(t *testing.T) {
	root := ruleRepo(t)
	meta := gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/example-org/cctrace", RepositoryIDSource: "resolved", CommitSHA: "aaa", Branch: "main"}
	stubResolveGit(t, func(string) gitctx.Context { return meta })
	advance := useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, _ := newFreshMetaSyncer(t, endpoint, []string{root}, nil)

	syncPass(t, s)
	meta.CommitSHA, meta.Branch = "bbb", "feature"
	advance(ruleScanTTL)
	clear(s.metaSent)
	syncPass(t, s)

	rules := srv.sentRules()
	if len(rules) != 2 {
		t.Fatalf("sent %d rule requests, want 2", len(rules))
	}
	if got := rules[1]; got.CommitSHA != "bbb" || got.Branch != "feature" {
		t.Errorf("rules sent with commit %q branch %q, want bbb feature", got.CommitSHA, got.Branch)
	}
}

// Inside ruleScanTTL the scan is parked, and the park is decided before the
// fresh lookup: an idle pass over a parked repository runs no git at all.
func TestSendProjectRules_parkedScanRunsNoLookup(t *testing.T) {
	root := ruleRepo(t)
	meta := gitctx.Context{RepositoryRoot: root, RepositoryID: "github.com/example-org/cctrace", RepositoryIDSource: "resolved", CommitSHA: "aaa", Branch: "main"}
	calls := stubResolveGit(t, func(string) gitctx.Context { return meta })
	advance := useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, _ := newFreshMetaSyncer(t, endpoint, []string{root}, nil)

	syncPass(t, s)
	advance(ruleScanTTL / 2)
	clear(s.metaSent)
	clear(calls)
	syncPass(t, s)
	if calls[root] != 0 {
		t.Errorf("resolveGit called %d times while the scan is parked, want 0", calls[root])
	}
}
