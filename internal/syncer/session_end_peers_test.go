package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// aliasOf makes a second spelling of dir: a link standing for it.
func aliasOf(t *testing.T, dir string) string {
	t.Helper()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	return alias
}

func newPeerState(t *testing.T) (*State, string) {
	t.Helper()
	state, err := LoadState(filepath.Join(t.TempDir(), "peer-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return state, SessionEndDir(state.path)
}

// A session end names its home by the spelling of the hook's transcript path.
// The Syncer of the folder sends it whatever the spelling, because the folder is
// what the account and the project belong to.
func TestSessionEndOfTheSameFolderUnderAnotherSpellingIsSent(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	writeClaudeConfig(t, claudeDir, "acct-work")
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	alias := aliasOf(t, claudeDir)
	transcript := filepath.Join(alias, "projects", "-Users-me-Code-Repo", "sess-a.jsonl")
	path := recordEnd(t, state, SessionEnd{SessionID: "sess-a", Reason: "clear", EndedAt: time.Now(), TranscriptPath: transcript, CWD: "/elsewhere", ClaudeHome: alias})

	flushOnce(t, s)
	events := eventsOf(srv.requests())
	if len(events) != 1 || events[0].SessionID != "sess-a" || events[0].ProjectHash != "-Users-me-Code-Repo" || events[0].AccountID != "acct-work" {
		t.Fatalf("events %+v, want sess-a under the folder's project and account: %s", events, srv.bodies())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a delivered session end stays: %v", err)
	}
}

func TestSessionEndOfADifferentFolderIsStillLeftAlone(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir, other := t.TempDir(), t.TempDir()
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	path := recordEnd(t, state, SessionEnd{SessionID: "sess-x", EndedAt: time.Now(), CWD: "/work", ClaudeHome: other})
	flushOnce(t, s)
	if len(eventsOf(srv.requests())) != 0 {
		t.Fatal("another folder's session end was sent")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("another folder's session end was taken: %v", err)
	}
}

// The profile that owns a shared Claude folder sends the session ends that the
// profiles left out of it recorded, since only the owner's Syncer collects the
// folder. The excluded profile's job need not run for that.
func TestOwnersSyncerSendsTheSessionEndsOfProfilesLeftOutOfTheFolder(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	writeClaudeConfig(t, claudeDir, "acct-work")
	s, ownState := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	peer, peerDir := newPeerState(t)
	s.SetPeerPending([]string{peerDir})

	alias := aliasOf(t, claudeDir)
	now := time.Now()
	own := recordEnd(t, ownState, SessionEnd{SessionID: "sess-own", EndedAt: now, CWD: "/work", ClaudeHome: claudeDir})
	sibling := recordEnd(t, peer, SessionEnd{SessionID: "sess-peer", Reason: "logout", EndedAt: now.Add(time.Second),
		TranscriptPath: filepath.Join(alias, "projects", "-Users-me-Code-Repo", "sess-peer.jsonl"), CWD: "/work", ClaudeHome: alias})
	foreign := recordEnd(t, peer, SessionEnd{SessionID: "sess-foreign", EndedAt: now, CWD: "/work", ClaudeHome: t.TempDir()})

	flushOnce(t, s)
	events := eventsOf(srv.requests())
	if len(events) != 2 || events[0].SessionID != "sess-own" || events[1].SessionID != "sess-peer" {
		t.Fatalf("events %+v, want the owner's and the sibling's, oldest first: %s", events, srv.bodies())
	}
	if events[1].ProjectHash != "-Users-me-Code-Repo" || events[1].AccountID != "acct-work" {
		t.Fatalf("the sibling's session end went out as %+v", events[1])
	}
	for _, gone := range []string{own, sibling} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s was not consumed: %v", filepath.Base(gone), err)
		}
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("a session end of some other folder was taken from the sibling's queue: %v", err)
	}
}

func TestOwnersSyncerExpiresTheSiblingsOldSessionEnds(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	peer, peerDir := newPeerState(t)
	s.SetPeerPending([]string{peerDir})
	stale := recordEnd(t, peer, SessionEnd{SessionID: "sess-old", EndedAt: time.Now(), CWD: "/work", ClaudeHome: t.TempDir()})
	old := time.Now().Add(-pendingSessionEndTTL - time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	flushOnce(t, s)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("an expired session end stays in the sibling's queue, where it counts toward its cap: %v", err)
	}
}

func TestOwnersSyncerIgnoresASiblingQueueThatIsMissingOrIsItsOwn(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	s, ownState := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	s.SetPeerPending([]string{filepath.Join(t.TempDir(), "never", "pending"), SessionEndDir(ownState.path), ""})
	recordEnd(t, ownState, SessionEnd{SessionID: "sess-own", EndedAt: time.Now(), CWD: "/work", ClaudeHome: claudeDir})
	flushOnce(t, s)
	if events := eventsOf(srv.requests()); len(events) != 1 {
		t.Fatalf("events %+v: a queue named twice must not send twice", events)
	}
	if strings.Contains(logs.String(), "session ends:") {
		t.Fatalf("a sibling that has no queue is the usual case, not something to log:\n%s", logs.String())
	}
}

// The same end recorded in two queues is one event: sending it twice in a
// flush would only be de-duplicated by the server, and leaving one copy behind
// would send it again on the next.
func TestAnEndRecordedInTwoQueuesIsSentAndConsumedOnce(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	s, ownState := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	peer, peerDir := newPeerState(t)
	s.SetPeerPending([]string{peerDir})
	end := SessionEnd{SessionID: "sess-twice", EndedAt: time.Now(), CWD: "/work", ClaudeHome: claudeDir}
	a, b := recordEnd(t, ownState, end), recordEnd(t, peer, end)
	flushOnce(t, s)
	if events := eventsOf(srv.requests()); len(events) != 1 {
		t.Fatalf("events %+v, want one", events)
	}
	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s was left behind: %v", p, err)
		}
	}
}
