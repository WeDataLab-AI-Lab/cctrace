package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/syncer"
)

// withHookStdin gives this process the stdin Claude Code gives a hook: a pipe
// holding the payload, closed after it. hookCollectFile, which the parent sets
// from the payload, is restored with it.
func withHookStdin(t *testing.T, payload map[string]any) {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	w.Close()
	prevStdin, prevCollect := os.Stdin, hookCollectFile
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin, hookCollectFile = prevStdin, prevCollect
		r.Close()
	})
}

func pendingSessionEnds(t *testing.T) []syncer.SessionEnd {
	t.Helper()
	dir := syncer.SessionEndDir(syncer.DefaultStatePath())
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var ends []syncer.SessionEnd
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var end syncer.SessionEnd
		if err := json.Unmarshal(data, &end); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		ends = append(ends, end)
	}
	return ends
}

// The SessionEnd hook's parent reads its payload before anything else -- before
// the self-update check, which already finds the session end recorded -- and
// leaves it beside the profile's sync state. The --once child it starts then
// collects the transcript and sends the session end before it exits.
func TestSessionEndHookReachesTheServer(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	var rec observationRecorder
	claudeDir := observationsProfile(t, rec.serve(t).URL, onceSession...)
	transcript := filepath.Join(claudeDir, "projects", "-tmp-repo", "obs-1.jsonl")
	withHookStdin(t, map[string]any{
		"session_id": "obs-1", "transcript_path": transcript, "cwd": "/tmp/repo",
		"hook_event_name": "SessionEnd", "reason": "prompt_input_exit",
	})
	recordedFirst := false
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		recordedFirst = len(pendingSessionEnds(t)) == 1
		return daemonParentUpdateResult{}
	}
	var childArgs []string
	spawnSyncProcessFn = func(args []string, _ string) (int, error) {
		childArgs = args
		return 123, nil
	}

	before := time.Now()
	var runErr error
	captureOutput(t, func() {
		runErr = runSync(false, claudeDir, false, true, true, false, time.Second, "", "", "", false, "", false)
	})
	if runErr != nil {
		t.Fatalf("daemon parent: %v", runErr)
	}
	if !recordedFirst {
		t.Error("the session end was not recorded before the self-update check")
	}
	ends := pendingSessionEnds(t)
	if len(ends) != 1 {
		t.Fatalf("%d pending session ends, want 1", len(ends))
	}
	end := ends[0]
	if end.SessionID != "obs-1" || end.Reason != "prompt_input_exit" || end.TranscriptPath != transcript ||
		end.CWD != "/tmp/repo" || end.EndedAt.Before(before) || end.ClaudeHome != claudeDir {
		t.Errorf("pending session end %+v", end)
	}
	if want := []string{"sync", "--once", "--claude-dir", claudeDir, "--collect-file", transcript}; !slices.Equal(childArgs, want) {
		t.Errorf("child args %q, want %q", childArgs, want)
	}

	// The child, as spawned: no daemon holds the lock, so it syncs and flushes.
	if err := runSync(false, claudeDir, false, false, true, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("--once child: %v", err)
	}
	bodies := strings.Join(rec.sent(), "\n")
	if !strings.Contains(bodies, `"kind":"session_end"`) || !strings.Contains(bodies, `"reason":"prompt_input_exit"`) ||
		!strings.Contains(bodies, `"project_hash":"-tmp-repo"`) {
		t.Errorf("observation requests %s, want the session end of obs-1", bodies)
	}
	if n := len(pendingSessionEnds(t)); n != 0 {
		t.Errorf("%d session ends still pending after the child's flush", n)
	}
}

// A hook of one Claude home can end a session whose transcript sits in another:
// the session end is recorded under the home of its transcript, which that
// home's Syncer then sends, and under --claude-dir only without a transcript.
func TestSessionEndHookRecordsTheTranscriptsHome(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		return daemonParentUpdateResult{}
	}
	spawnSyncProcessFn = func([]string, string) (int, error) { return 123, nil }
	hookHome, transcriptHome := t.TempDir(), t.TempDir()
	transcript := filepath.Join(transcriptHome, "projects", "-work", "s-other-home.jsonl")
	withHookStdin(t, map[string]any{
		"session_id": "s-other-home", "transcript_path": transcript, "cwd": "/work",
		"hook_event_name": "SessionEnd", "reason": "logout",
	})
	captureOutput(t, func() {
		if err := runSync(false, hookHome, false, true, true, false, time.Second, "", "", "", false, "", false); err != nil {
			t.Errorf("daemon parent: %v", err)
		}
	})
	ends := pendingSessionEnds(t)
	if len(ends) != 1 || ends[0].ClaudeHome != transcriptHome {
		t.Errorf("recorded %+v, want it under the transcript's home %s", ends, transcriptHome)
	}
}

// A replacement parent already given the transcript path -- in either spelling
// of the flag -- is not handed it twice.
func TestReplacementParentArgsDoNotRepeatTheCollectFile(t *testing.T) {
	prevArgs, prevCollect := os.Args, hookCollectFile
	t.Cleanup(func() { os.Args, hookCollectFile = prevArgs, prevCollect })
	hookCollectFile = "/home/me/.claude/projects/-work/s.jsonl"
	for _, given := range [][]string{
		{"--collect-file", hookCollectFile},
		{"--collect-file=" + hookCollectFile},
	} {
		os.Args = append([]string{"cctrace", "sync", "--daemon"}, given...)
		args := daemonParentReexecArgs("/install/cctrace")
		n := 0
		for _, arg := range args {
			if strings.HasPrefix(arg, "--collect-file") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("args %q carry the flag %d times, want once", args, n)
		}
	}
}

// With a daemon running, the --once child finds the sync lock taken and exits at
// once, flushing nothing: the session end waits in the pending directory, and
// the next flush of whoever holds the lock sends it.
func TestSessionEndWaitsForTheDaemonHoldingTheLock(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	var rec observationRecorder
	claudeDir := observationsProfile(t, rec.serve(t).URL, onceSession...)
	if err := syncer.RecordSessionEnd(syncer.SessionEndDir(syncer.DefaultStatePath()), syncer.SessionEnd{
		SessionID: "obs-1", Reason: "clear", EndedAt: time.Now(), CWD: "/tmp/repo", ClaudeHome: claudeDir,
	}); err != nil {
		t.Fatal(err)
	}
	daemonLock, err := acquireSyncLock("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := runSync(false, claudeDir, false, false, true, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("--once child: %v", err)
	}
	if n := len(rec.sent()); n != 0 {
		t.Errorf("the child flushed %d requests while the daemon held the lock", n)
	}
	if n := len(pendingSessionEnds(t)); n != 1 {
		t.Fatalf("%d pending session ends, want the one the child left", n)
	}

	if err := daemonLock.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatal(err)
	}
	if bodies := strings.Join(rec.sent(), "\n"); !strings.Contains(bodies, `"kind":"session_end"`) {
		t.Errorf("the next flush sent %s, want the session end", bodies)
	}
	if n := len(pendingSessionEnds(t)); n != 0 {
		t.Errorf("%d session ends still pending", n)
	}
}

// A SessionStart hook records no session end; its transcript still goes to the
// child to be collected from the start, as before.
func TestSessionStartHookRecordsNoSessionEnd(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	claudeDir := t.TempDir()
	transcript := filepath.Join(claudeDir, "projects", "-work", "s-start.jsonl")
	withHookStdin(t, map[string]any{
		"session_id": "s-start", "transcript_path": transcript, "cwd": "/work",
		"hook_event_name": "SessionStart", "source": "startup",
	})
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		return daemonParentUpdateResult{}
	}
	var childArgs []string
	spawnSyncProcessFn = func(args []string, _ string) (int, error) {
		childArgs = args
		return 123, nil
	}
	captureOutput(t, func() {
		if err := runSync(false, claudeDir, false, true, false, false, time.Second, "", "", "", false, "", false); err != nil {
			t.Errorf("daemon parent: %v", err)
		}
	})
	if n := len(pendingSessionEnds(t)); n != 0 {
		t.Errorf("a SessionStart left %d pending session ends", n)
	}
	if i := slices.Index(childArgs, "--collect-file"); i < 0 || i+1 >= len(childArgs) || childArgs[i+1] != transcript {
		t.Errorf("child args %q, want --collect-file %s", childArgs, transcript)
	}
}

// The parent reads its stdin before the self-update check. A replacement started
// after an update inherits that stdin already read, so it is handed the
// transcript path on its command line instead -- the way the child is.
func TestReplacementParentGetsTheTranscriptPath(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	claudeDir := t.TempDir()
	transcript := filepath.Join(claudeDir, "projects", "-work", "s-update.jsonl")
	withHookStdin(t, map[string]any{
		"session_id": "s-update", "transcript_path": transcript, "cwd": "/work",
		"hook_event_name": "SessionEnd", "reason": "logout",
	})
	syncExecutablePathFn = func() (string, error) { return "/install/cctrace", nil }
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		return daemonParentUpdateResult{ServerVersion: "v0.7.22", Applied: true}
	}
	var replacementArgs []string
	reexecDaemonParentFn = func(executable string) error {
		replacementArgs = daemonParentReexecArgs(executable)
		return nil
	}
	if err := runSync(false, claudeDir, false, true, true, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("daemon parent: %v", err)
	}
	if i := slices.Index(replacementArgs, "--collect-file"); i < 0 || i+1 >= len(replacementArgs) || replacementArgs[i+1] != transcript {
		t.Errorf("replacement args %q, want --collect-file %s", replacementArgs, transcript)
	}
	if n := len(pendingSessionEnds(t)); n != 1 {
		t.Errorf("%d pending session ends, want the one recorded before the update", n)
	}
}

// The SessionEnd hook runs once per session, and its payload is on stdin only
// once. A startup that waits out the policy lock and gives up must not lose the
// report with it: the parent records the session end before it asks for the
// barrier, and the next flush sends it.
func TestSessionEndHookIsRecordedWhenTheStartupBarrierTimesOut(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	narrowBarrierWait(t)
	claudeDir := t.TempDir()
	transcript := filepath.Join(claudeDir, "projects", "-tmp-repo", "obs-2.jsonl")
	withHookStdin(t, map[string]any{
		"session_id": "obs-2", "transcript_path": transcript, "cwd": "/tmp/repo",
		"hook_event_name": "SessionEnd", "reason": "prompt_input_exit",
	})
	spawnSyncProcessFn = func([]string, string) (int, error) {
		t.Error("a startup that never passed the barrier spawned a child")
		return 0, nil
	}
	holdPolicyLock(t, globalPolicyLockPath(t))

	err := runSync(false, claudeDir, false, true, true, false, time.Second, "", "", "", false, "", false)

	if !errors.Is(err, localdaemon.ErrPolicyLockBusy) {
		t.Fatalf("daemon parent ended with %v, want the policy lock wait", err)
	}
	ends := pendingSessionEnds(t)
	if len(ends) != 1 || ends[0].SessionID != "obs-2" || ends[0].TranscriptPath != transcript {
		t.Fatalf("pending session ends %+v, want the one the hook reported", ends)
	}
}
