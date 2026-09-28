package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// captureDaemonLog routes progressf/diagf at a buffer the way the spawned
// daemon routes them at sync.log.
func captureDaemonLog(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	prevOut, prevFlags, prevInstalled := log.Writer(), log.Flags(), daemonLogInstalled
	log.SetOutput(&buf)
	log.SetFlags(0)
	daemonLogInstalled = true
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		daemonLogInstalled = prevInstalled
	})
	return &buf
}

// The watch loop's line counted records since the process started, so a stalled
// daemon printed the same number every pass -- 34079 for two days -- and the log
// deduper folded the repeat as an unchanged line (#712). What a reader needs on
// it is this pass: what went, what failed, and how long nothing has worked.
func TestStalledPassLine(t *testing.T) {
	sendErr := errors.New(`http post: Post "http://10.20.30.40:18080/api/sync": dial tcp: connect: no route to host`)

	stats := syncer.PassStats{Sent: 0, FilesFailed: 12, LastErr: sendErr}
	if !passStalled(stats) {
		t.Fatal("a pass with failures and nothing sent is not being read as stalled")
	}
	if passStalled(syncer.PassStats{Sent: 3, FilesFailed: 1}) {
		t.Error("a pass that sent records was read as stalled")
	}
	if passStalled(syncer.PassStats{}) {
		t.Error("an idle pass was read as stalled")
	}

	stalled := stalledPassLine(stats, 2*time.Hour+14*time.Minute, 5*time.Minute)
	for _, want := range []string{"0 sent", "12 files failed", "no success for 2h14m", "no route to host", "retry in 5m0s"} {
		if !strings.Contains(stalled, want) {
			t.Errorf("stalled line %q missing %q", stalled, want)
		}
	}
}

// A satellite syncer's error used to be dropped by `if cerr == nil`, so a Codex
// or gjc collection could fail every pass with nothing anywhere. The initial
// pass in the same function already reported these; only watch mode did not.
func TestSatelliteResultReportsErrors(t *testing.T) {
	buf := captureDaemonLog(t)

	if got := noteSatelliteResult("codex-sync", 7, nil); got != 7 {
		t.Errorf("records added = %d, want 7", got)
	}
	if got := noteSatelliteResult("codex-sync", 3, errors.New("load state: boom")); got != 0 {
		t.Errorf("records added on failure = %d, want 0", got)
	}
	out := buf.String()
	if !strings.Contains(out, "[codex-sync] load state: boom") {
		t.Errorf("satellite failure not reported: %q", out)
	}
}

// End to end: a watch pass against a server that refuses everything has to say
// so on its own line, and the pass must not count as a success -- that is what
// left the backoff disarmed and the retry running once a second for two days.
func TestWatchReportsAStalledPass(t *testing.T) {
	useTempSyncHome(t)
	buf := captureDaemonLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sync":
			http.Error(w, "nope", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]int{"inserted_rules": 0, "inserted_versions": 0})
		}
	}))
	defer srv.Close()

	claudeDir := filepath.Join(t.TempDir(), ".claude")
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-repo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	line := `{"type":"user","timestamp":"2026-05-20T00:00:00Z","sessionId":"stall-1","cwd":` +
		quoteJSONString(t.TempDir()) + `,"message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(sessionPath, []byte(line), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.SyncEndpoint = srv.URL
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
	ackPath := filepath.Join(t.TempDir(), "ack.json")
	stopped := stopWatchWhen(t, ackPath, transportFailureRecorded)

	runWatchUntilStopped(t, "watch reports a stalled pass", func() error {
		return runSync(false, claudeDir, true, false, false, false, time.Millisecond, "", "", "", false, ackPath, false)
	})
	<-stopped

	out := buf.String()
	// The count is pluralised, so the assertion holds the parts that do not
	// change: nothing sent, and something failed.
	if !strings.Contains(out, "0 sent,") || !strings.Contains(out, "failed") {
		t.Errorf("no stalled-pass line in the log: %q", out)
	}
	if strings.Contains(out, "records synced") {
		t.Errorf("a pass that sent nothing still reported records synced: %q", out)
	}

	// The fact also has to outlive the log, which rotates.
	persisted, err := syncer.LoadState(syncer.DefaultStatePath())
	if err != nil {
		t.Fatalf("LoadState after watch: %v", err)
	}
	if persisted.TransportFailure == nil {
		t.Error("no TransportFailure recorded for a pass that sent nothing")
	}
}

// One file is "1 file failed". The line is read by a person scanning sync.log.
func TestStalledPassLineSingularFile(t *testing.T) {
	got := stalledPassLine(syncer.PassStats{FilesFailed: 1}, time.Minute, time.Second)
	if !strings.Contains(got, "1 file failed") {
		t.Errorf("stalledPassLine = %q", got)
	}
}

// Under a minute the line reports seconds. Rounding a six-second stall to "0s"
// reads as a bug in the line rather than a fact about the daemon.
func TestStalledPassLineRoundsSubMinuteToSeconds(t *testing.T) {
	got := stalledPassLine(syncer.PassStats{FilesFailed: 1}, 6*time.Second, time.Second)
	if !strings.Contains(got, "no success for 6s") {
		t.Errorf("stalledPassLine = %q", got)
	}
}

// transportFailureRecorded reports whether a pass has written its stall record,
// which is the evidence that at least one failing pass completed.
func transportFailureRecorded() bool {
	st, err := syncer.LoadState(syncer.DefaultStatePath())
	return err == nil && st.TransportFailure != nil
}
