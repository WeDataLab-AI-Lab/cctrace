package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/codexlog"
	"cctrace/internal/codexsyncer"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// observationsProfile writes a profile pointing at endpoint and one session the
// state already knows, so the first pass reads it rather than skipping it.
func observationsProfile(t *testing.T, endpoint string, lines ...string) string {
	t.Helper()
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "obs-1.jsonl")
	if err := os.WriteFile(sessionPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.User.ID = "u1"
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.SyncEndpoint = endpoint
	p.ClaudeConfigDir = claudeDir
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	state, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(sessionPath, 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	return claudeDir
}

// observationRecorder answers sync and re-enrichment and records every
// session-observations body.
type observationRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (o *observationRecorder) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync/capabilities":
			_, _ = w.Write([]byte(`{"reenrich":true}`))
		case "/api/sync":
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 2, "updated": 2})
		case "/api/session-observations":
			o.mu.Lock()
			o.bodies = append(o.bodies, string(body))
			o.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]int{"accepted": 1, "stored": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (o *observationRecorder) sent() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.bodies...)
}

var onceSession = []string{
	`{"type":"user","uuid":"u-1","timestamp":"2026-10-01T00:00:00Z","sessionId":"obs-1","message":{"role":"user","content":"hi"}}`,
	`{"type":"ai-title","aiTitle":"ONCE-TITLE","sessionId":"obs-1"}`,
}

// Every run that is not a watcher gets one chance to report, right after its
// pass: the SessionEnd hook's --once child, a plain `cctrace sync`, each profile
// of the run over all profiles, and the finalize worker. Each holds the sync
// lock while it flushes.
func TestOneShotRunsFlushObservationsAfterTheirPass(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(claudeDir string) error
	}{
		{"--once", func(claudeDir string) error {
			return runSync(false, claudeDir, false, false, true, false, time.Second, "", "", "", false, "", false)
		}},
		{"cctrace sync", func(claudeDir string) error {
			return runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false)
		}},
		{"every profile", func(string) error {
			return runSyncAll(false, time.Second, "", "", false)
		}},
		{"finalize worker", func(claudeDir string) error {
			return runSyncFinalizeWorker(claudeDir, "", "", "", false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			var rec observationRecorder
			claudeDir := observationsProfile(t, rec.serve(t).URL, onceSession...)
			if err := tc.run(claudeDir); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			bodies := rec.sent()
			if len(bodies) != 1 || !strings.Contains(bodies[0], `"heartbeat_at"`) || !strings.Contains(bodies[0], "ONCE-TITLE") {
				t.Fatalf("observation requests %v, want one carrying the heartbeat and the snapshot", bodies)
			}
		})
	}
}

// `cctrace sync reenrich` sends what it backfilled before it exits -- it holds
// the sync lock, so no daemon is flushing meanwhile -- and says how many
// snapshots it backfilled. By default the titles stay on the machine.
func TestRunSyncReenrichFlushesTheSnapshotsItBackfilled(t *testing.T) {
	for _, includeContent := range []bool{false, true} {
		t.Run(fmt.Sprintf("include-content=%v", includeContent), func(t *testing.T) {
			useTempSyncHome(t)
			var rec observationRecorder
			claudeDir := observationsProfile(t, rec.serve(t).URL, append(onceSession,
				`{"type":"pr-link","sessionId":"obs-1","prNumber":3,"prUrl":"https://github.com/o/r/pull/3","prRepository":"o/r","timestamp":"2026-10-01T00:01:00Z"}`)...)
			state, err := syncer.LoadState(syncer.DefaultStatePath())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(claudeDir, "projects", "-tmp-repo", "obs-1.jsonl")
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			state.SetOffset(path, fi.Size()) // collected before snapshots existed
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}

			var runErr error
			stdout, _ := captureOutput(t, func() { runErr = runSyncReenrich(claudeDir, "", "", "", false, includeContent) })
			if runErr != nil {
				t.Fatalf("runSyncReenrich: %v", runErr)
			}
			if !strings.Contains(stdout, "backfilled 1 session snapshots") {
				t.Errorf("exit message %q does not count the backfilled snapshot", stdout)
			}
			bodies := rec.sent()
			if len(bodies) != 1 || !strings.Contains(bodies[0], `"session_id":"obs-1"`) || !strings.Contains(bodies[0], "pull/3") {
				t.Fatalf("observation requests %v, want one carrying the backfilled snapshot", bodies)
			}
			if strings.Contains(bodies[0], "ONCE-TITLE") != includeContent {
				t.Errorf("title sent=%v, want %v: %s", !includeContent, includeContent, bodies[0])
			}
		})
	}
}

// A watcher reports itself once its first pass is done rather than a whole
// interval later.
func TestWatchFlushesObservationsAfterItsFirstPass(t *testing.T) {
	useTempSyncHome(t)
	previous := observationFlushInterval
	observationFlushInterval = time.Hour
	t.Cleanup(func() { observationFlushInterval = previous })

	var observations atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/session-observations" {
			observations.Add(1)
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
	}))
	defer srv.Close()
	claudeDir := observationsProfile(t, srv.URL,
		`{"type":"user","uuid":"u-1","timestamp":"2026-10-01T00:00:00Z","sessionId":"obs-1","message":{"role":"user","content":"hi"}}`)
	ackPath := filepath.Join(t.TempDir(), "ack.json")
	stopped := stopWatchWhen(t, ackPath, func() bool { return observations.Load() >= 1 })

	runWatchUntilStopped(t, "watch flushes after its first pass", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, ackPath, false)
	})
	<-stopped
	if n := observations.Load(); n != 1 {
		t.Errorf("%d observation requests within the first interval, want the one after the first pass", n)
	}
}

// The interval stamp advances whether or not the flush worked. Advancing it only
// on success would turn a failing flush into one attempt per tick -- once a
// millisecond here, once a second under the installed hook -- which is what the
// quota poll's comment describes happening against a 429.
func TestWatchKeepsTheFlushIntervalWhileFlushesFail(t *testing.T) {
	useTempSyncHome(t)
	const flushEvery = 100 * time.Millisecond
	previous := observationFlushInterval
	observationFlushInterval = flushEvery
	t.Cleanup(func() { observationFlushInterval = previous })

	var observations atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/session-observations" {
			observations.Add(1)
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 1})
	}))
	defer srv.Close()
	claudeDir := observationsProfile(t, srv.URL,
		`{"type":"user","uuid":"u-1","timestamp":"2026-10-01T00:00:00Z","sessionId":"obs-1","message":{"role":"user","content":"hi"}}`)
	ackPath := filepath.Join(t.TempDir(), "ack.json")
	start := time.Now()
	stopped := stopWatchWhen(t, ackPath, func() bool { return time.Since(start) > 6*flushEvery })

	runWatchUntilStopped(t, "watch keeps the flush interval", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, ackPath, false)
	})
	<-stopped
	elapsed := time.Since(start)
	if n, most := observations.Load(), int64(elapsed/flushEvery)+2; n > most {
		t.Errorf("%d observation requests in %s, want at most %d: a failing flush is retried per tick", n, elapsed.Round(time.Millisecond), most)
	}
}

// The heartbeat rides its own interval, apart from the record sync: a watcher
// whose every sync fails keeps reporting that it is running while its backoff
// holds the next sync attempt seconds away.
func TestWatchFlushesObservationsWhileRecordSyncFails(t *testing.T) {
	useTempSyncHome(t)
	previous := observationFlushInterval
	observationFlushInterval = 20 * time.Millisecond
	t.Cleanup(func() { observationFlushInterval = previous })

	var observations atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync":
			http.Error(w, "down", http.StatusInternalServerError)
		case "/api/session-observations":
			observations.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]int{"accepted": 0})
		default:
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0, "inserted_versions": 0})
		}
	}))
	defer srv.Close()
	claudeDir := observationsProfile(t, srv.URL,
		`{"type":"user","uuid":"u-1","timestamp":"2026-10-01T00:00:00Z","sessionId":"obs-1","message":{"role":"user","content":"hi"}}`)
	ackPath := filepath.Join(t.TempDir(), "ack.json")
	// The first failed pass arms a backoff of about five seconds; two seconds
	// stay inside it.
	start := time.Now()
	stopped := stopWatchWhen(t, ackPath, func() bool { return observations.Load() >= 4 || time.Since(start) > 2*time.Second })

	runWatchUntilStopped(t, "watch flushes while sync fails", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, ackPath, false)
	})
	<-stopped
	if n := observations.Load(); n < 4 {
		t.Errorf("%d observation requests, want the first pass's and the interval's while sync was backing off", n)
	}
}

// observationFailingServer accepts sync and answers the observation route with
// status, or holds it until the client gives up when status is 0.
func observationFailingServer(t *testing.T, status int) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync/capabilities":
			_, _ = w.Write([]byte(`{"reenrich":true}`))
			return
		case "/api/session-observations":
		default:
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted": 2, "updated": 2})
			return
		}
		if status == 0 {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		http.Error(w, "observations refused", status)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first: Close waits for the handler
	return srv.URL
}

// A run about to exit has nothing to say about an older server's 404: the park it
// starts ends with the process. Watch mode's "parked for 1h" would only be noise.
// A pending session end it could not send stays for a later flush.
func TestOneShotFlushSaysNothingAboutAnOlderServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(claudeDir string) error
	}{
		{"cctrace sync", func(claudeDir string) error {
			return runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false)
		}},
		{"cctrace sync reenrich", func(claudeDir string) error {
			return runSyncReenrich(claudeDir, "", "", "", false, false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			claudeDir := observationsProfile(t, observationFailingServer(t, http.StatusNotFound), onceSession...)
			if err := syncer.RecordSessionEnd(syncer.SessionEndDir(syncer.DefaultStatePath()), syncer.SessionEnd{
				SessionID: "obs-1", Reason: "clear", EndedAt: time.Now(), CWD: "/tmp/repo", ClaudeHome: claudeDir,
			}); err != nil {
				t.Fatal(err)
			}
			var runErr error
			stdout, stderr := captureOutput(t, func() { runErr = tc.run(claudeDir) })
			if runErr != nil {
				t.Fatal(runErr)
			}
			if out := stdout + stderr; strings.Contains(out, "[observations]") || strings.Contains(out, "parked") {
				t.Errorf("%s reported the 404:\n%s", tc.name, out)
			}
			if n := len(pendingSessionEnds(t)); n != 1 {
				t.Errorf("%d pending session ends after the 404, want the one kept", n)
			}
		})
	}
}

// Any other failure is printed as a whole line on stderr. Watch mode's status
// line repaints in place and leaves the line open, so in a terminal the shell
// prompt would land on it once the run exits.
func TestOneShotFlushPrintsAFailureAsAWholeLine(t *testing.T) {
	useTempSyncHome(t)
	previous := progressIsTTY
	progressIsTTY = true
	t.Cleanup(func() { progressIsTTY = previous })
	claudeDir := observationsProfile(t, observationFailingServer(t, http.StatusServiceUnavailable), onceSession...)
	var runErr error
	stdout, stderr := captureOutput(t, func() {
		runErr = runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false)
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(stderr, "[observations] server returned 503\n") || strings.Contains(stdout, "[observations]") {
		t.Errorf("stdout %q, stderr %q: want the failure as one line on stderr", stdout, stderr)
	}
}

// The flush runs with the sync lock held, so an observations route that hangs
// must not keep a run that is about to exit -- and the lock -- for the transfer
// deadline. It gives up at its own bound; what it did not send stays dirty.
func TestOneShotFlushGivesUpAtItsBound(t *testing.T) {
	useTempSyncHome(t)
	previous := oneShotObservationFlushTimeout
	oneShotObservationFlushTimeout = 200 * time.Millisecond
	t.Cleanup(func() { oneShotObservationFlushTimeout = previous })
	claudeDir := observationsProfile(t, observationFailingServer(t, 0), onceSession...)
	start := time.Now()
	var runErr error
	_, stderr := captureOutput(t, func() {
		runErr = runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false)
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the run took %s with the observations route hanging, want it bounded", elapsed.Round(time.Millisecond))
	}
	if !strings.Contains(stderr, "[observations]") {
		t.Errorf("stderr %q: the flush that gave up was not reported", stderr)
	}
}

// codexRolloutKnown enables Codex sync and writes one rollout of a whole turn
// under the temp home's Codex home, known to the Codex state -- read from its
// start, or, when collected, already collected before snapshots existed -- and
// returns its session id.
func codexRolloutKnown(t *testing.T, collected bool) string {
	t.Helper()
	t.Setenv("CCTRACE_CODEX_SYNC", "true")
	const sid = "00000000-0000-0000-0000-00000000c0de"
	dir := filepath.Join(codexlog.DefaultCodexDir(), "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-10-01T09-00-00-"+sid+".jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-10-01T09:00:00.000Z","payload":{"id":"` + sid + `","cwd":"/tmp/codex-repo","originator":"codex-tui","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:00:01.000Z","payload":{"type":"task_started","turn_id":"codex-turn-1","started_at":1790845201,"collaboration_mode_kind":"default"}}`,
		`{"type":"response_item","timestamp":"2026-10-01T09:00:02.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:00:06.000Z","payload":{"type":"task_complete","turn_id":"codex-turn-1","completed_at":1790845206,"duration_ms":5000}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := syncer.LoadState(codexsyncer.StatePathForProfile(""))
	if err != nil {
		t.Fatal(err)
	}
	offset := int64(0)
	if collected {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		offset = fi.Size()
	}
	state.SetOffset(path, offset)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	return sid
}

// A run that syncs Codex sends its Codex session snapshots in its one flush,
// beside the Claude home's.
func TestOneShotRunFlushesCodexSnapshots(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	var rec observationRecorder
	claudeDir := observationsProfile(t, rec.serve(t).URL, onceSession...)
	sid := codexRolloutKnown(t, false)
	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatal(err)
	}
	bodies := rec.sent()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"session_id":"`+sid+`","agent":"codex"`) ||
		!strings.Contains(bodies[0], `"turn_id":"codex-turn-1"`) || !strings.Contains(bodies[0], "ONCE-TITLE") {
		t.Fatalf("observation requests %v, want one carrying the Claude and the Codex snapshot", bodies)
	}
}

// `cctrace sync reenrich` backfills the Codex snapshots too, sends them in the
// same flush as Claude's and counts them.
func TestRunSyncReenrichFlushesCodexSnapshotsWithClaudes(t *testing.T) {
	useTempSyncHome(t)
	var rec observationRecorder
	claudeDir := observationsProfile(t, rec.serve(t).URL, onceSession...)
	state, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(claudeDir, "projects", "-tmp-repo", "obs-1.jsonl")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(path, fi.Size()) // collected before snapshots existed
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	sid := codexRolloutKnown(t, true)

	var runErr error
	stdout, _ := captureOutput(t, func() { runErr = runSyncReenrich(claudeDir, "", "", "", false, true) })
	if runErr != nil {
		t.Fatalf("runSyncReenrich: %v", runErr)
	}
	if !strings.Contains(stdout, "backfilled 2 session snapshots") {
		t.Errorf("exit message %q does not count both backfilled snapshots", stdout)
	}
	bodies := rec.sent()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"session_id":"obs-1"`) || !strings.Contains(bodies[0], `"session_id":"`+sid+`","agent":"codex"`) {
		t.Fatalf("observation requests %v, want one carrying both backfilled snapshots", bodies)
	}
}

// heldClaudeCodexProfile makes "work" with Codex collection on, as
// managedCodexProfile does, and "holder" on the same destination and Claude
// folder, which it reads in work's place: holder sorts first. Work's Codex home
// is its own. It returns work's entry and the rollout's session id.
func heldClaudeCodexProfile(t *testing.T, endpoint string) (profile.Entry, string) {
	t.Helper()
	work, _, sid := managedCodexProfile(t, "work", endpoint)
	p := profile.NewDefault()
	p.User = work.Profile.User
	p.Server.Endpoint, p.Server.SyncEndpoint = endpoint, endpoint
	p.ClaudeConfigDir = work.Profile.ClaudeConfigDir
	if err := profile.SaveNamed(p, "holder"); err != nil {
		t.Fatal(err)
	}
	return work, sid
}

// A run of a profile left out of its Claude folder sends its own Codex
// snapshots and its heartbeat in its one flush, and nothing of the Claude folder.
func TestOneShotRunWithoutItsClaudeFolderFlushesItsCodexSnapshots(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	var rec observationRecorder
	_, sid := heldClaudeCodexProfile(t, rec.serve(t).URL)
	if err := runSync(false, "", false, false, false, false, time.Second, "work", "", "", false, "", false); err != nil {
		t.Fatal(err)
	}
	bodies := rec.sent()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"session_id":"`+sid+`","agent":"codex"`) || !strings.Contains(bodies[0], `"heartbeat_at"`) {
		t.Fatalf("observation requests %v, want one carrying the Codex snapshot and the heartbeat", bodies)
	}
	if strings.Contains(bodies[0], "ONCE-TITLE") {
		t.Fatalf("the Claude folder's snapshot was sent by a profile left out of it: %v", bodies)
	}
}

// `cctrace sync reenrich` of a profile left out of its Claude folder sends the
// Codex snapshots it backfilled.
func TestRunSyncReenrichWithoutItsClaudeFolderFlushesItsCodexSnapshots(t *testing.T) {
	useTempSyncHome(t)
	var rec observationRecorder
	work, sid := heldClaudeCodexProfile(t, rec.serve(t).URL)
	state, err := syncer.LoadState(filepath.Join(work.RuntimeDir, "codex-sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for path := range state.Files {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		state.SetOffset(path, fi.Size()) // collected before snapshots existed
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	var runErr error
	captureOutput(t, func() { runErr = runSyncReenrich("", "work", "", "", false, false) })
	if runErr != nil {
		t.Fatalf("runSyncReenrich: %v", runErr)
	}
	bodies := rec.sent()
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"session_id":"`+sid+`","agent":"codex"`) {
		t.Fatalf("observation requests %v, want one carrying the backfilled Codex snapshot", bodies)
	}
	if strings.Contains(bodies[0], `"agent":"claude"`) {
		t.Fatalf("the Claude folder's snapshot was sent by a profile left out of it: %v", bodies)
	}
}
