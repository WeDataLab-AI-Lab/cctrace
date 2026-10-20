package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/processio"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// managedObservationsProfile writes a named profile that syncs to endpoint, with
// the one session onceSession describes already known to its state so the first
// pass reads it rather than skipping it. edit changes the profile before it is
// saved. It returns the registry entry and the session file.
func managedObservationsProfile(t *testing.T, name, endpoint string, edit ...func(*profile.Profile)) (profile.Entry, string) {
	t.Helper()
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	sessionFile := filepath.Join(claudeDir, "projects", "-tmp-repo", "obs-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionFile, []byte(strings.Join(onceSession, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile.NewDefault()
	p.User = profile.UserInfo{ID: "user-1", Name: name, Email: "owner@example.test", Team: "fixture"}
	p.Server.Endpoint = endpoint
	p.Server.SyncEndpoint = endpoint
	p.ClaudeConfigDir = claudeDir
	for _, change := range edit {
		change(p)
	}
	if err := profile.SaveNamed(p, name); err != nil {
		t.Fatal(err)
	}
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load(name)
	if err != nil {
		t.Fatal(err)
	}
	state, err := syncer.LoadState(filepath.Join(entry.RuntimeDir, "sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(sessionFile, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	return entry, sessionFile
}

// startManagedJob takes the job's lease for the rest of the test and returns a
// function that runs one pass under it and reports what it reported.
func startManagedJob(t *testing.T, spec localdaemon.Spec) func() localdaemon.Progress {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	release, err := spec.Acquire(ctx, localdaemon.Allocation{Allowed: spec.Sources})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return func() localdaemon.Progress {
		t.Helper()
		var progress localdaemon.Progress
		if err := spec.Run(ctx, func(p localdaemon.Progress) { progress = p }); err != nil {
			t.Fatal(err)
		}
		return progress
	}
}

// watchManagedFlushes returns a channel that gets one value for every flush a
// lease completes, so a test waits for a flush instead of for the clock. Set
// before the lease is taken.
func watchManagedFlushes(t *testing.T) <-chan struct{} {
	t.Helper()
	flushed := make(chan struct{}, 256)
	prev := managedFlushDone
	managedFlushDone = func() { flushed <- struct{}{} }
	t.Cleanup(func() { managedFlushDone = prev })
	return flushed
}

func waitForFlush(t *testing.T, flushed <-chan struct{}) {
	t.Helper()
	select {
	case <-flushed:
	case <-time.After(10 * time.Second):
		t.Fatal("no flush completed")
	}
}

// setFlushTimings narrows the interval of the flush loop and the bound on each
// send for the rest of the test. Set before the lease is taken.
func setFlushTimings(t *testing.T, interval, bound time.Duration) {
	t.Helper()
	prevInterval, prevBound := observationFlushInterval, managedObservationFlushTimeout
	observationFlushInterval, managedObservationFlushTimeout = interval, bound
	t.Cleanup(func() { observationFlushInterval, managedObservationFlushTimeout = prevInterval, prevBound })
}

// A profile the daemon collects has no other process to report for it: the
// --once child the SessionEnd hook starts gives up at once on the daemon's
// lease. So the lease sends what a watcher's pass sends -- the heartbeat, the
// session snapshots, the pending session ends and the process I/O queue -- once
// the first pass is done.
func TestManagedLeaseFlushesObservationsAfterItsFirstPass(t *testing.T) {
	useTempSyncHome(t)
	var rec observationRecorder
	entry, sessionFile := managedObservationsProfile(t, "one", rec.serve(t).URL)
	pending := syncer.SessionEndDir(filepath.Join(entry.RuntimeDir, "sync-state.json"))
	err := syncer.RecordSessionEnd(pending, syncer.SessionEnd{
		SessionID: "obs-1", Reason: "prompt_input_exit", EndedAt: time.Now(), TranscriptPath: sessionFile, CWD: "/tmp/repo",
	})
	if err != nil {
		t.Fatal(err)
	}
	queue := processio.Queue{Path: filepath.Join(entry.RuntimeDir, processIOQueueFile)}
	if err := queue.Upsert(queuedProcessIOForTest("io-session", "acct-a")); err != nil {
		t.Fatal(err)
	}
	flushed := watchManagedFlushes(t)
	pass := startManagedJob(t, profileJobSpec(entry, daemonOverrides{}))

	pass()
	waitForFlush(t, flushed)
	sent := strings.Join(rec.sent(), "\n")
	for _, want := range []string{`"heartbeat_at"`, "ONCE-TITLE", `"kind":"session_end"`, "io-session"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the lease did not send %s: %s", want, sent)
		}
	}
	if left, _ := os.ReadDir(pending); len(left) != 0 {
		t.Errorf("%d session ends still pending after they were sent", len(left))
	}
	if left, err := queue.List(); err != nil || len(left) != 0 {
		t.Errorf("process I/O queue holds %d reports after they were sent, %v", len(left), err)
	}
}

// The heartbeat is not paced by the record sync. The supervisor delays a failing
// job's next pass by a growing backoff, or by the server's Retry-After, and the
// server takes a collector for stale after three minutes: a batch that keeps
// failing, or a 429, must not make the collector and its live sessions look
// gone. Here the one pass fails and no other runs, and the heartbeats go on.
func TestManagedHeartbeatsContinueWhileTheRecordSyncFails(t *testing.T) {
	useTempSyncHome(t)
	var syncRequests, heartbeats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			syncRequests.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
		case "/api/session-observations":
			heartbeats.Add(1)
			_, _ = w.Write([]byte(`{"accepted":1,"stored":1}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	entry, _ := managedObservationsProfile(t, "one", server.URL)
	setFlushTimings(t, 5*time.Millisecond, time.Second)
	flushed := watchManagedFlushes(t)
	pass := startManagedJob(t, profileJobSpec(entry, daemonOverrides{}))

	progress := pass()
	if progress.State != "retrying" {
		t.Fatalf("the failing pass reported %+v, want retrying", progress)
	}
	failedSyncs := syncRequests.Load()
	for range 4 {
		waitForFlush(t, flushed)
	}
	if got := heartbeats.Load(); got < 4 {
		t.Fatalf("%d heartbeats while no pass ran, want one per flush", got)
	}
	if got := syncRequests.Load(); got != failedSyncs {
		t.Fatalf("record sync requests went from %d to %d with no pass running", failedSyncs, got)
	}
}

// Each send of a flush has a bound: a route that hangs gives up and the next
// interval tries again, instead of the loop stopping on it.
func TestManagedFlushGivesUpOnAHangingRoute(t *testing.T) {
	useTempSyncHome(t)
	var hung atomic.Int32
	unblock := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session-observations" {
			hung.Add(1)
			// The server sees the client leave only once the body is read.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-unblock:
			}
			return
		}
		_, _ = w.Write([]byte(`{"inserted":2,"updated":2}`))
	}))
	defer server.Close()
	defer close(unblock)
	entry, _ := managedObservationsProfile(t, "one", server.URL)
	setFlushTimings(t, 5*time.Millisecond, 20*time.Millisecond)
	flushed := watchManagedFlushes(t)
	pass := startManagedJob(t, profileJobSpec(entry, daemonOverrides{}))

	pass()
	waitForFlush(t, flushed)
	waitForFlush(t, flushed)
	if got := hung.Load(); got < 2 {
		t.Fatalf("%d requests reached the hanging route, want the loop to have tried again", got)
	}
}

// The managed flush settles the reports of its own profile and nothing else's. A
// launcher can run under an email the profile does not carry
// (CCTRACE_PROFILE_EMAIL), so a report of another identity stays in the queue,
// to be expired by its retention, not dropped by a job that cannot tell.
func TestManagedFlushKeepsProcessIOReportsOfAnotherIdentity(t *testing.T) {
	useTempSyncHome(t)
	var rec observationRecorder
	entry, _ := managedObservationsProfile(t, "one", rec.serve(t).URL)
	queue := processio.Queue{Path: filepath.Join(entry.RuntimeDir, processIOQueueFile)}
	own := queuedProcessIOForTest("own-session", "acct-a")
	other := queuedProcessIOForTest("other-session", "acct-a")
	other.ProfileEmail = "launcher@example.test"
	for _, report := range []processio.QueuedReport{own, other} {
		if err := queue.Upsert(report); err != nil {
			t.Fatal(err)
		}
	}
	flushed := watchManagedFlushes(t)
	startManagedJob(t, profileJobSpec(entry, daemonOverrides{}))()
	waitForFlush(t, flushed)

	sent := strings.Join(rec.sent(), "\n")
	if !strings.Contains(sent, "own-session") || strings.Contains(sent, "other-session") {
		t.Fatalf("the flush sent %s, want only the profile's own report", sent)
	}
	left, err := queue.List()
	if err != nil || len(left) != 1 || left[0].SessionID != "other-session" {
		t.Fatalf("queue after the flush: %+v, %v; want the other identity's report kept", left, err)
	}
}

// The default profile's queue is the one in the profile directory itself, where
// `cctrace launch` writes it, not under profiles/.
func TestManagedFlushSendsTheDefaultProfilesProcessIOQueue(t *testing.T) {
	useTempSyncHome(t)
	var rec observationRecorder
	observationsProfile(t, rec.serve(t).URL, onceSession...)
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load("default")
	if err != nil {
		t.Fatal(err)
	}
	report := queuedProcessIOForTest("default-io-session", "acct-a")
	report.ProfileEmail, report.UserID = "user@example.com", "u1"
	if err := processIOQueue("").Upsert(report); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(profile.DefaultDir(), processIOQueueFile); processIOQueue("").Path != want {
		t.Fatalf("default queue at %s, want %s", processIOQueue("").Path, want)
	}
	flushed := watchManagedFlushes(t)
	startManagedJob(t, profileJobSpec(entry, daemonOverrides{}))()
	waitForFlush(t, flushed)

	if sent := strings.Join(rec.sent(), "\n"); !strings.Contains(sent, "default-io-session") {
		t.Fatalf("the default profile's queued report was not sent: %s", sent)
	}
	if left, err := processIOQueue("").List(); err != nil || len(left) != 0 {
		t.Fatalf("default queue holds %d reports after they were sent, %v", len(left), err)
	}
}

// What the observation flush hits never changes what the pass reports about
// collection: an older server answering its route with an error leaves the
// profile healthy. The flush says so in the daemon's log, under the profile.
func TestManagedFlushFailureKeepsCollectionHealthy(t *testing.T) {
	useTempSyncHome(t)
	logged := captureJobLog(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			_, _ = w.Write([]byte(`{"inserted":2,"updated":2}`))
		default:
			http.Error(w, "unavailable", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	entry, _ := managedObservationsProfile(t, "one", server.URL)
	queue := processio.Queue{Path: filepath.Join(entry.RuntimeDir, processIOQueueFile)}
	if err := queue.Upsert(queuedProcessIOForTest("io-session", "acct-a")); err != nil {
		t.Fatal(err)
	}
	flushed := watchManagedFlushes(t)
	progress := startManagedJob(t, profileJobSpec(entry, daemonOverrides{}))()
	waitForFlush(t, flushed)

	if progress.State != "healthy" || progress.ErrorCode != "" {
		t.Fatalf("progress after a failing observation flush: %+v", progress)
	}
	if left, err := queue.List(); err != nil || len(left) != 1 {
		t.Fatalf("a refused process I/O report was dropped: %d, %v", len(left), err)
	}
	for _, want := range []string{"profile=one [observations] ", "profile=one [process-io] observation upload: "} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log lacks %q: %q", want, logged.String())
		}
	}
}

// managedCodexProfile is managedObservationsProfile with Codex collection on
// and an explicit codex_home holding one whole-turn rollout the Codex state
// already knows. It returns the entry, the home and the rollout's session id.
func managedCodexProfile(t *testing.T, name, endpoint string) (profile.Entry, string, string) {
	t.Helper()
	codexHome, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := managedObservationsProfile(t, name, endpoint, func(p *profile.Profile) {
		p.Options.CodexSyncEnabled = true
		p.CodexHome = codexHome
	})
	const sid = "00000000-0000-0000-0000-00000000c0de"
	rollout := filepath.Join(codexHome, "sessions", "rollout-2026-10-01T09-00-00-"+sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-10-01T09:00:00.000Z","payload":{"id":"` + sid + `","cwd":"/tmp/codex-repo","originator":"codex-tui","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:00:01.000Z","payload":{"type":"task_started","turn_id":"codex-turn-1","started_at":1790845201,"collaboration_mode_kind":"default"}}`,
		`{"type":"response_item","timestamp":"2026-10-01T09:00:02.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:00:06.000Z","payload":{"type":"task_complete","turn_id":"codex-turn-1","completed_at":1790845206,"duration_ms":5000}}`,
	}
	if err := os.WriteFile(rollout, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := syncer.LoadState(filepath.Join(entry.RuntimeDir, "codex-sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(rollout, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	return entry, codexHome, sid
}

// The managed job sets up its Codex syncer as a one-shot run does: the Codex
// session snapshots go in the pass's one flush, under the Claude syncer.
func TestManagedPassFlushesCodexSnapshots(t *testing.T) {
	useTempSyncHome(t)
	var rec observationRecorder
	entry, _, sid := managedCodexProfile(t, "one", rec.serve(t).URL)
	flushed := watchManagedFlushes(t)
	startManagedJob(t, profileJobSpec(entry, daemonOverrides{}))()
	waitForFlush(t, flushed)
	bodies := rec.sent()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"session_id":"`+sid+`","agent":"codex"`) || !strings.Contains(bodies[0], `"turn_id":"codex-turn-1"`) {
		t.Fatalf("observation requests %v, want one carrying the Codex snapshot", bodies)
	}
}

// An explicit codex_home is not named in what the managed job logs: the Codex
// syncer shows it as a placeholder, as it does for a one-shot run.
func TestManagedPassRedactsExplicitCodexHomeInLogs(t *testing.T) {
	useTempSyncHome(t)
	var rec observationRecorder
	entry, codexHome, _ := managedCodexProfile(t, "one", rec.serve(t).URL)
	logged := captureJobLog(t)
	flushed := watchManagedFlushes(t)
	startManagedJob(t, profileJobSpec(entry, daemonOverrides{}))()
	waitForFlush(t, flushed)
	if strings.Contains(logged.String(), codexHome) {
		t.Errorf("the log names the explicit Codex home: %q", logged.String())
	}
	if !strings.Contains(logged.String(), "[explicit Codex home]") {
		t.Errorf("the Codex scan was not logged with the placeholder: %q", logged.String())
	}
}

// A profile left out of a shared Claude folder has no collector for it, so its
// SessionEnd hook's events would expire in its own queue. The profile that holds
// the folder is told whose queues to read and sends them with its own, though the
// excluded profile's job never runs.
func TestManagedLeaseSendsTheSessionEndsOfProfilesLeftOutOfItsFolder(t *testing.T) {
	useTempSyncHome(t)
	var rec observationRecorder
	entry, sessionFile := managedObservationsProfile(t, "one", rec.serve(t).URL)
	p := profile.NewDefault()
	p.User = profile.UserInfo{ID: "user-2", Name: "two", Email: "two@example.test", Team: "fixture"}
	p.Server.Endpoint, p.Server.SyncEndpoint = "http://127.0.0.1:1", "http://127.0.0.1:1"
	p.ClaudeConfigDir = entry.Profile.ClaudeConfigDir
	if err := profile.SaveNamed(p, "two"); err != nil {
		t.Fatal(err)
	}
	sibling, err := (profile.Registry{Root: profile.DefaultDir()}).Load("two")
	if err != nil {
		t.Fatal(err)
	}
	siblingQueue := syncer.SessionEndDir(filepath.Join(sibling.RuntimeDir, "sync-state.json"))
	if err := syncer.RecordSessionEnd(siblingQueue, syncer.SessionEnd{SessionID: "left-out", Reason: "clear", EndedAt: time.Now(), TranscriptPath: sessionFile, CWD: "/tmp/repo"}); err != nil {
		t.Fatal(err)
	}
	spec := profileJobSpec(entry, daemonOverrides{})
	flushed := watchManagedFlushes(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := spec.Acquire(ctx, localdaemon.Allocation{Allowed: spec.Sources, PeerPending: []string{sibling.RuntimeDir}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := spec.Run(ctx, func(localdaemon.Progress) {}); err != nil {
		t.Fatal(err)
	}
	waitForFlush(t, flushed)
	if sent := strings.Join(rec.sent(), "\n"); !strings.Contains(sent, "left-out") {
		t.Fatalf("the sibling's session end was not sent: %s", sent)
	}
	if left, _ := os.ReadDir(siblingQueue); len(left) != 0 {
		t.Fatalf("%d session ends still in the sibling's queue after they were sent", len(left))
	}
}

// The hook records only into its own profile's queue. It never goes looking for
// the profile that holds the folder, so nothing it needs can be unreadable.
func TestSessionEndHookRecordsOnlyIntoItsOwnQueue(t *testing.T) {
	home := useTempSyncHome(t)
	for _, name := range []string{"one", "two"} {
		p := profile.NewDefault()
		p.User = profile.UserInfo{ID: name, Name: name, Email: name + "@example.test", Team: "fixture"}
		p.ClaudeConfigDir = filepath.Join(home, "shared-claude")
		if err := profile.SaveNamed(p, name); err != nil {
			t.Fatal(err)
		}
	}
	// The registry can no longer be read: another profile's file is damaged.
	damaged, err := (profile.Registry{Root: profile.DefaultDir()}).Path("one")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(damaged, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (profile.Registry{Root: profile.DefaultDir()}).List(); err == nil {
		t.Fatal("fixture: the registry is still readable")
	}
	recordSessionEnd("two", "", hookPayload{SessionID: "hooked", CWD: "/tmp/repo", TranscriptPath: filepath.Join(home, "shared-claude", "projects", "-p", "hooked.jsonl")})
	own := syncer.SessionEndDir(syncStatePathForProfile("two"))
	if left, err := os.ReadDir(own); err != nil || len(left) != 1 {
		t.Fatalf("the hook's own queue holds %d files, %v", len(left), err)
	}
	if left, err := os.ReadDir(syncer.SessionEndDir(syncStatePathForProfile("one"))); err == nil && len(left) != 0 {
		t.Fatalf("the hook wrote into another profile's queue: %d files", len(left))
	}
}

// A profile left out of its Claude folder still collects its own Codex home, so
// it still sends that home's session snapshots, with its heartbeat. What belongs
// to the Claude folder -- its snapshots, the session ends the hook queued -- is
// the holder's to send, under the holder's filters.
func TestManagedLeaseWithoutItsClaudeFolderFlushesItsCodexSnapshots(t *testing.T) {
	useTempSyncHome(t)
	var rec observationRecorder
	entry, codexHome, sid := managedCodexProfile(t, "one", rec.serve(t).URL)
	pending := syncer.SessionEndDir(filepath.Join(entry.RuntimeDir, "sync-state.json"))
	if err := syncer.RecordSessionEnd(pending, syncer.SessionEnd{SessionID: "obs-1", Reason: "clear", EndedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	spec := profileJobSpec(entry, daemonOverrides{})
	line := "codex:" + codexHome
	if !slices.Contains(spec.Sources, line) {
		t.Fatalf("fixture: sources %v", spec.Sources)
	}
	flushed := watchManagedFlushes(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := spec.Acquire(ctx, localdaemon.Allocation{Allowed: []string{line}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := spec.Run(ctx, func(localdaemon.Progress) {}); err != nil {
		t.Fatal(err)
	}
	waitForFlush(t, flushed)
	sent := strings.Join(rec.sent(), "\n")
	if !strings.Contains(sent, `"session_id":"`+sid+`","agent":"codex"`) || !strings.Contains(sent, `"heartbeat_at"`) {
		t.Fatalf("observation requests %q, want the Codex snapshot and the heartbeat", sent)
	}
	if strings.Contains(sent, "ONCE-TITLE") || strings.Contains(sent, `"kind":"session_end"`) {
		t.Fatalf("the Claude folder's items were sent by a profile left out of it: %q", sent)
	}
	if left, _ := os.ReadDir(pending); len(left) != 1 {
		t.Fatalf("the queued session end is the holder's to send; %d left", len(left))
	}
}
