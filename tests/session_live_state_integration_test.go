package tests

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"cctrace/internal/syncer"
)

// The real client against the real API server and Postgres. A running Claude
// Code process -- this test process, under its own pid and start time -- arrives
// as its session_live_states row; the server writes its status changes into
// session_observation_events, and the gone once its process file disappears.
// The SessionEnd hook's pending file arrives as a session_end event and leaves
// the pending directory.
func TestLiveStateAndSessionEndReachTheServer(t *testing.T) {
	s := acquireSyncStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE session_live_states, session_observation_events, collector_heartbeats`); err != nil {
		t.Fatal(err)
	}
	srv, token := newSyncServer(t, s)
	defer srv.Close()

	const sid, profileEmail, userID = "live-int", "p@example.com", "u-live"
	claudeDir := t.TempDir()
	sessionsDir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid, procStart, pidDomain := thisProcessAsClaudeCodeWritesIt(t)
	processFile := filepath.Join(sessionsDir, strconv.Itoa(pid)+".json")
	writeProcessFile := func(status string, changedAt time.Time) {
		t.Helper()
		b, err := json.Marshal(map[string]any{
			"pid": pid, "sessionId": sid, "cwd": "/tmp/live-int", "procStart": procStart, "pidDomain": pidDomain,
			"kind": "interactive", "entrypoint": "cli", "name": "INT-NAME", "nameSource": "derived",
			"status": status, "statusUpdatedAt": changedAt.UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(processFile, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	statePath := filepath.Join(t.TempDir(), "sync-state.json")
	state, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	sy := syncer.New(claudeDir, profileEmail, userID, state, syncer.NewClient(srv.URL, token, "test"), nil)
	flush := func() {
		t.Helper()
		if err := sy.FlushObservations(ctx); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}
	type row struct {
		status, waitingKind, name, nameSource, projectHash, procStart string
		pid                                                           int64
		changedAt, heartbeatAt                                        time.Time
	}
	readRow := func() row {
		t.Helper()
		var r row
		if err := s.Pool().QueryRow(ctx, `SELECT status, waiting_kind, name, name_source, project_hash, proc_start, pid,
				status_changed_at, heartbeat_at
			FROM session_live_states WHERE session_id = $1 AND user_id = $2 AND profile_email = $3`, sid, userID, profileEmail).
			Scan(&r.status, &r.waitingKind, &r.name, &r.nameSource, &r.projectHash, &r.procStart, &r.pid, &r.changedAt, &r.heartbeatAt); err != nil {
			t.Fatalf("read the live state row: %v", err)
		}
		return r
	}

	t1 := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	t2 := t1.Add(30 * time.Second)
	writeProcessFile("busy", t1)
	flush()
	writeProcessFile("idle", t2)
	flush()
	r := readRow()
	if r.status != "idle" || r.waitingKind != "" || r.name != "INT-NAME" || r.nameSource != "derived" ||
		r.projectHash != "-tmp-live-int" || r.procStart != procStart || r.pid != int64(pid) || !r.changedAt.Equal(t2) {
		t.Errorf("live state row %+v", r)
	}

	endedAt := time.Now().Truncate(time.Microsecond)
	if err := syncer.RecordSessionEnd(syncer.SessionEndDir(statePath), syncer.SessionEnd{
		SessionID: sid, Reason: "prompt_input_exit", EndedAt: endedAt, CWD: "/tmp/live-int", ClaudeHome: claudeDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(processFile); err != nil {
		t.Fatal(err)
	}
	flush()
	if r := readRow(); r.status != "gone" || !r.changedAt.Equal(r.heartbeatAt) || !r.changedAt.After(t2) {
		t.Errorf("after the process file went: %+v, want gone, dated by when it was found", r)
	}

	rows, err := s.Pool().Query(ctx, `SELECT kind, event_id, observed_at, COALESCE(payload->>'from', ''), COALESCE(payload->>'to', ''),
			COALESCE(payload->>'reason', '')
		FROM session_observation_events WHERE session_id = $1 ORDER BY observed_at, kind`, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var history []string
	for rows.Next() {
		var kind, eventID, from, to, reason string
		var at time.Time
		if err := rows.Scan(&kind, &eventID, &at, &from, &to, &reason); err != nil {
			t.Fatal(err)
		}
		switch kind {
		case "state_change":
			history = append(history, "state_change "+from+">"+to+" "+at.UTC().Format(time.RFC3339Nano))
		case "session_end":
			if len(eventID) != 32 || !at.Equal(endedAt) {
				t.Errorf("session_end event %s at %v, want a 32-digit id at %v", eventID, at, endedAt)
			}
			history = append(history, "session_end "+reason)
		default:
			history = append(history, kind)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"state_change >busy " + t1.UTC().Format(time.RFC3339Nano),
		"state_change busy>idle " + t2.UTC().Format(time.RFC3339Nano),
		"session_end prompt_input_exit",
		"gone",
	}
	if strings.Join(history, "\n") != strings.Join(want, "\n") {
		t.Errorf("history:\n%s\nwant:\n%s", strings.Join(history, "\n"), strings.Join(want, "\n"))
	}
	if entries, err := os.ReadDir(syncer.SessionEndDir(statePath)); err != nil || len(entries) != 0 {
		t.Errorf("pending directory after the 200: %d files, err %v", len(entries), err)
	}
}

// thisProcessAsClaudeCodeWritesIt is the test process's pid, and its procStart
// and pidDomain as Claude Code writes them: the start time from `LC_ALL=C TZ=UTC
// ps -o lstart=`, and the platform on darwin or, on Linux, the platform, machine
// id and pid namespace.
func thisProcessAsClaudeCodeWritesIt(t *testing.T) (int, string, string) {
	t.Helper()
	pid := os.Getpid()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	pidDomain := "darwin"
	if runtime.GOOS == "linux" {
		machineID, _ := os.ReadFile("/etc/machine-id")
		namespace, _ := os.Readlink("/proc/self/ns/pid")
		pidDomain = "linux:" + strings.TrimSpace(string(machineID)) + ":" + namespace
	}
	return pid, strings.TrimSpace(string(out)), pidDomain
}

// A process switching between two sessions and back, twice, as /resume does:
// Claude Code rewrites updatedAt on each switch and leaves statusUpdatedAt as it
// was. Through the real server, each session's history alternates in order --
// every return after the gone it follows, none dropped as a repeat -- and the
// session the process is on ends alive.
func TestSwitchedBackSessionEndsAliveInHistory(t *testing.T) {
	s := acquireSyncStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE session_live_states, session_observation_events, collector_heartbeats`); err != nil {
		t.Fatal(err)
	}
	srv, token := newSyncServer(t, s)
	defer srv.Close()

	const userID, profileEmail = "u-switch", "p@example.com"
	claudeDir := t.TempDir()
	sessionsDir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid, procStart, pidDomain := thisProcessAsClaudeCodeWritesIt(t)
	statusAt := time.Now().Add(-10 * time.Minute)
	state, err := syncer.LoadState(filepath.Join(t.TempDir(), "sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	sy := syncer.New(claudeDir, profileEmail, userID, state, syncer.NewClient(srv.URL, token, "test"), nil)
	for _, sid := range []string{"switch-A", "switch-B", "switch-A", "switch-B", "switch-A"} {
		b, err := json.Marshal(map[string]any{
			"pid": pid, "sessionId": sid, "cwd": "/tmp/switch-int", "procStart": procStart, "pidDomain": pidDomain,
			"kind": "interactive", "name": "SWITCH", "nameSource": "derived", "status": "busy",
			"statusUpdatedAt": statusAt.UnixMilli(), "updatedAt": time.Now().UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionsDir, strconv.Itoa(pid)+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // a switch, then a flush that finds it
		if err := sy.FlushObservations(ctx); err != nil {
			t.Fatalf("flush: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	history := func(sid string) string {
		t.Helper()
		rows, err := s.Pool().Query(ctx, `SELECT COALESCE(payload->>'from', ''), payload->>'to'
			FROM session_observation_events WHERE session_id = $1 AND user_id = $2 ORDER BY observed_at`, sid, userID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var steps []string
		for rows.Next() {
			var from, to string
			if err := rows.Scan(&from, &to); err != nil {
				t.Fatal(err)
			}
			steps = append(steps, from+">"+to)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return strings.Join(steps, " ")
	}
	if got, want := history("switch-A"), ">busy busy>gone gone>busy busy>gone gone>busy"; got != want {
		t.Errorf("switch-A history %q, want %q", got, want)
	}
	if got, want := history("switch-B"), ">busy busy>gone gone>busy busy>gone"; got != want {
		t.Errorf("switch-B history %q, want %q", got, want)
	}
	var status string
	if err := s.Pool().QueryRow(ctx, `SELECT status FROM session_live_states WHERE session_id = 'switch-A' AND user_id = $1`, userID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "busy" {
		t.Errorf("switch-A's live row is %s, want busy", status)
	}
}
