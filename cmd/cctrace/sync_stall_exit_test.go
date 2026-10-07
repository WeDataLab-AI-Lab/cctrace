package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// useStallExitAfter shortens the stall a watcher tolerates before releasing the
// lock, so a test does not have to wait it out.
func useStallExitAfter(t *testing.T, d time.Duration) {
	t.Helper()
	previous := syncStallExitAfter
	syncStallExitAfter = d
	t.Cleanup(func() { syncStallExitAfter = previous })
}

// stallWatchProfile writes a profile and one queued session against endpoint,
// and returns the Claude home and the ack path for a watch run.
func stallWatchProfile(t *testing.T, endpoint string) (claudeDir, ackPath string) {
	t.Helper()
	claudeDir = filepath.Join(t.TempDir(), ".claude")
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"stall-exit","cwd":` +
		quoteJSONString(t.TempDir()) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.SyncEndpoint = endpoint
	p.ClaudeConfigDir = claudeDir
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	state, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.SetOffset(sessionPath, 0)
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	return claudeDir, filepath.Join(t.TempDir(), "ack.json")
}

// syncStallServer refuses /api/sync with status and answers everything else.
func syncStallServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync" {
			http.Error(w, "refused", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0, "inserted_versions": 0})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// #712 was fixed by hand, by killing the daemon: the replacement process worked
// on its first pass against the same server, from the same machine, over the
// same network. Whatever the resident process had got itself into, a new one did
// not have it -- and nothing in the client could reach that conclusion, because
// a watcher holds the single-instance lock until it exits, and every daemon the
// hook started for three days found the lock taken and left.
//
// So a watcher that has sent nothing for long enough stops holding the lock. It
// does not respawn itself: the next session's SessionStart hook starts a fresh
// process, which is both the thing that actually recovered collection and the
// moment new sessions exist to collect. Offsets are untouched, so that process
// backfills everything (the real one sent a 21.6MB session on its first pass).
func TestWatchReleasesTheLockAfterASustainedStall(t *testing.T) {
	useTempSyncHome(t)
	useShortBackoff(t)
	useStallExitAfter(t, 0)
	srv := syncStallServer(t, http.StatusInternalServerError)
	claudeDir, ackPath := stallWatchProfile(t, srv.URL)

	// No stop request is written: the watcher has to reach this on its own.
	runWatchUntilStopped(t, "watch releases the lock after a stall", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, ackPath, false)
	})

	free, err := isSyncLockFree("")
	if err != nil {
		t.Fatalf("isSyncLockFree: %v", err)
	}
	if !free {
		t.Error("the lock is still held after the watcher exited")
	}
	if _, err := os.Stat(runtimeFilePath("")); !os.IsNotExist(err) {
		t.Errorf("runtime file still present after exit, stat err=%v", err)
	}
	// The evidence has to survive the exit, or status goes quiet exactly when
	// nothing is collecting.
	persisted, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if persisted.TransportFailure == nil {
		t.Error("the stall record did not outlive the daemon that wrote it")
	}
}

// A refusal is not a stuck process. A 429 needs waiting, and replacing the
// process throws away the backoff that was honouring Retry-After -- the
// self-perpetuating retry that cost one client 1,293,935 attempts.
func TestWatchDoesNotReleaseTheLockOnARateLimit(t *testing.T) {
	useTempSyncHome(t)
	useStallExitAfter(t, 0)
	srv := syncStallServer(t, http.StatusTooManyRequests)
	claudeDir, ackPath := stallWatchProfile(t, srv.URL)
	stopped := stopWatchWhen(t, ackPath, transportFailureRecorded)

	runWatchUntilStopped(t, "watch keeps the lock on a 429", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, ackPath, false)
	})
	<-stopped

	persisted, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if persisted.TransportFailure == nil {
		t.Fatal("no record written for a rate-limited pass")
	}
	if persisted.TransportFailure.Class != syncer.TransportFailureClassServer {
		t.Errorf("Class = %q, want %q", persisted.TransportFailure.Class, syncer.TransportFailureClassServer)
	}
}

// A 413 holds one file at one oversized record. It is reported by its own
// notice and is not a stall at all, so the watcher keeps collecting everything
// else and nothing is recorded against the path.
func TestWatchKeepsCollectingPastABodyLimitHold(t *testing.T) {
	useTempSyncHome(t)
	useStallExitAfter(t, 0)
	srv := syncStallServer(t, http.StatusRequestEntityTooLarge)
	claudeDir, ackPath := stallWatchProfile(t, srv.URL)
	stopped := stopWatchWhen(t, ackPath, bodyLimitHoldRecorded)

	runWatchUntilStopped(t, "watch keeps the lock on a 413", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, ackPath, false)
	})
	<-stopped

	persisted, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if persisted.TransportFailure != nil {
		t.Errorf("TransportFailure = %+v, want nil for a body-limit hold", persisted.TransportFailure)
	}
}

// Under the threshold the watcher stays: a stall shorter than this is a Wi-Fi
// roam or a server redeploy, and a daemon that quits on those would be replaced
// by the hook every few minutes for no reason.
func TestWatchKeepsTheLockBeforeTheThreshold(t *testing.T) {
	useTempSyncHome(t)
	useStallExitAfter(t, time.Hour)
	srv := syncStallServer(t, http.StatusInternalServerError)
	claudeDir, ackPath := stallWatchProfile(t, srv.URL)
	stopped := stopWatchWhenReady(t, ackPath)

	runWatchUntilStopped(t, "watch keeps the lock under the threshold", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, ackPath, false)
	})
	<-stopped
}

// The exit is an exception to "keep trying", so every condition it depends on is
// pinned here rather than only through the watch run above: a 30-minute path
// costs a test 30 minutes of clock, and these branches are what decide it.
func TestReleaseLockForStall(t *testing.T) {
	transport := &syncer.TransportFailure{Class: syncer.TransportFailureClassTransport}
	server := &syncer.TransportFailure{Class: syncer.TransportFailureClassServer}
	long := syncStallExitAfter
	short := syncStallExitAfter - time.Second

	cases := []struct {
		name         string
		record       *syncer.TransportFailure
		stalledFor   time.Duration
		hasSuccessor bool
		want         bool
	}{
		{"a long transport stall with a successor releases the lock", transport, long, true, true},
		{"no successor keeps the daemon trying", transport, long, false, false},
		{"a server refusal never releases the lock", server, long, true, false},
		{"under the threshold keeps the lock", transport, short, true, false},
		{"no record keeps the lock", nil, long, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := releaseLockForStall(tc.record, tc.stalledFor, tc.hasSuccessor); got != tc.want {
				t.Errorf("releaseLockForStall = %v, want %v", got, tc.want)
			}
		})
	}
}

// bodyLimitHoldRecorded reports whether a pass reached the 413 and recorded the
// hold -- the proof that a send was attempted, which the test needs before it can
// assert that no stall was recorded for it.
func bodyLimitHoldRecorded() bool {
	st, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		return false
	}
	for _, fs := range st.Files {
		if fs != nil && fs.BlockedByBodyLimit {
			return true
		}
	}
	return false
}

// useShortBackoff shortens the retry schedule for tests that have to let a real
// backoff elapse before the next pass runs. The delay is not what they assert;
// at the production 5s base, waiting for it was the whole cost of the test.
func useShortBackoff(t *testing.T) {
	t.Helper()
	previous := backoffBaseUnit
	backoffBaseUnit = 5 * time.Millisecond
	t.Cleanup(func() { backoffBaseUnit = previous })
}
