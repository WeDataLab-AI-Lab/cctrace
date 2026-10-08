package syncer

import (
	"context"
	"path/filepath"
	"testing"

	"cctrace/internal/gitctx"
)

// This test swaps resolveGit and nowFn, so it cannot run in parallel with the
// rest of the package.

// A file can get part of its tail through before a later request fails: under
// an allowlist each identity is its own group of requests, and a long tail is
// several batches anyway. What got through got through. The pass used to drop
// that count with the error, report zero sent, and record a transport stall --
// on which the daemon backs off every agent's collection, and after long
// enough exits -- in a pass whose first request the server had just accepted.
func TestSyncOnce_countsWhatAFileSentBeforeItFailed(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(allowedRepoTwo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	appendLines(t, filepath.Join(dir, "s1.jsonl"), sessionLine("/work/a", "1"), sessionLine("/work/b", "2"))
	srv.refuseText(t, "2")

	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	wantSent(t, srv, "1")
	if pass := s.LastPass(); n != 1 || pass.Sent != 1 || pass.FilesFailed != 1 {
		t.Fatalf("SyncOnce = %d, pass = %+v; want 1 sent and 1 file failed", n, pass)
	}
	if tf := reloadState(t, s).TransportFailure; tf != nil {
		t.Fatalf("recorded a transport stall %+v in a pass that got a record through", tf)
	}
}

// The same loss one level down. A batch the server refuses as too large is
// split in half, and the first half can be accepted before the second fails.
// The client reports the accepted count with the error; the sender returned
// before adding it. The offset stays at the start of the group -- that is the
// boundary a group is retried from -- but the row that got through counts.
func TestSyncOnce_countsTheAcceptedHalfOfASplitBatch(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"), sessionLine("/work/a", "2"))
	srv.tooLarge = func(payload SyncPayload) bool { return len(payload.Records) > 1 }
	srv.refuseText(t, "2")

	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	wantSent(t, srv, "1")
	if pass := s.LastPass(); n != 1 || pass.Sent != 1 || pass.FilesFailed != 1 {
		t.Fatalf("SyncOnce = %d, pass = %+v; want 1 sent and 1 file failed", n, pass)
	}
	wantOffset(t, s, p, 0)
	if tf := reloadState(t, s).TransportFailure; tf != nil {
		t.Fatalf("recorded a transport stall %+v in a pass that got a record through", tf)
	}
}
