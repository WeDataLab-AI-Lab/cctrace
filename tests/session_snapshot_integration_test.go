package tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexsyncer"
	"cctrace/internal/sessionlog"
	"cctrace/internal/syncer"
)

// The real client against the real API server and Postgres: a session's
// metadata lines arrive as its session_snapshots row, a title under the prompts
// policy arrives redacted while the PR link and the cost do not, and every flush
// moves the collector's heartbeat -- the one without a snapshot too.
func TestSessionSnapshotsReachTheServer(t *testing.T) {
	s := acquireSyncStore(t)
	truncateSyncTables(t, s)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE session_snapshots, collector_heartbeats CASCADE`); err != nil {
		t.Fatal(err)
	}
	srv, token := newSyncServer(t, s)
	defer srv.Close()

	const profileEmail, userID = "p@example.com", "u-snapshot"
	collect := func(sid string, policy syncer.RedactPolicy) *syncer.Syncer {
		t.Helper()
		claudeDir := t.TempDir()
		dir := filepath.Join(claudeDir, "projects", "-tmp-snapshot-int")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		lines := []string{
			`{"type":"user","uuid":"` + sid + `-u1","timestamp":"2026-10-01T00:00:00Z","sessionId":"` + sid + `","message":{"role":"user","content":"hi"}}`,
			`{"type":"ai-title","aiTitle":"` + sid + `-AI-TITLE","sessionId":"` + sid + `"}`,
			`{"type":"custom-title","customTitle":"` + sid + `-CUSTOM","sessionId":"` + sid + `"}`,
			`{"type":"agent-name","agentName":"` + sid + `-AGENT","sessionId":"` + sid + `"}`,
			`{"type":"cost-state","sessionId":"` + sid + `","startTime":1759300000000,"totalCostUSD":4.5,"totalLinesAdded":12,"totalLinesRemoved":3,` +
				`"totalAPIDuration":1,"totalAPIDurationWithoutRetries":1,"totalToolDuration":1,"totalDuration":1,"modelUsage":{"claude-opus-5-5[1m]":{"costUSD":4.5}},"hasUnknownModelCost":false}`,
			`{"type":"pr-link","sessionId":"` + sid + `","prNumber":42,"prUrl":"https://github.com/o/r/pull/42","prRepository":"o/r","timestamp":"2026-10-01T00:05:00Z"}`,
		}
		if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		state, err := syncer.LoadState(filepath.Join(t.TempDir(), "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := state.Save(); err != nil { // known, so the first pass reads the file
			t.Fatal(err)
		}
		client := syncer.NewClient(srv.URL, token, "test")
		client.SetRedactPolicy(policy)
		sy := syncer.New(claudeDir, profileEmail, userID, state, client, nil)
		if n, err := sy.SyncOnce(ctx); err != nil || n != len(lines) {
			t.Fatalf("%s: sync sent %d, err %v; want %d", sid, n, err, len(lines))
		}
		if err := sy.FlushObservations(ctx); err != nil {
			t.Fatalf("%s: flush: %v", sid, err)
		}
		return sy
	}
	heartbeat := func() time.Time {
		t.Helper()
		var at time.Time
		if err := s.Pool().QueryRow(ctx, `SELECT heartbeat_at FROM collector_heartbeats WHERE user_id = $1 AND profile_email = $2`,
			userID, profileEmail).Scan(&at); err != nil {
			t.Fatalf("read heartbeat: %v", err)
		}
		return at
	}

	collect("snap-plain", syncer.RedactPolicy{})
	first := heartbeat()
	redacting := collect("snap-redacted", syncer.RedactPolicy{UserPrompts: true})

	type row struct{ snapshot, projectHash, agent, billing string }
	read := func(sid string) row {
		t.Helper()
		var r row
		var version int
		if err := s.Pool().QueryRow(ctx, `SELECT snapshot::text, project_hash, agent, billing_provider, collect_version
			FROM session_snapshots WHERE session_id = $1 AND user_id = $2 AND profile_email = $3`, sid, userID, profileEmail).
			Scan(&r.snapshot, &r.projectHash, &r.agent, &r.billing, &version); err != nil {
			t.Fatalf("read %s: %v", sid, err)
		}
		if r.projectHash != "-tmp-snapshot-int" || r.agent != "claude" || r.billing != "anthropic" || version != sessionlog.CollectVersion {
			t.Errorf("%s row: %+v collect_version %d", sid, r, version)
		}
		return r
	}
	plain := read("snap-plain").snapshot
	for _, want := range []string{`"ai_title": "snap-plain-AI-TITLE"`, `"custom_title": "snap-plain-CUSTOM"`, `"agent_name": "snap-plain-AGENT"`,
		`"url": "https://github.com/o/r/pull/42"`, `"total_cost_usd": 4.5`, `"models": ["claude-opus-5-5[1m]"]`} {
		if !strings.Contains(plain, want) {
			t.Errorf("snap-plain row lacks %s: %s", want, plain)
		}
	}
	redacted := read("snap-redacted").snapshot
	if strings.Contains(redacted, "snap-redacted-") {
		t.Errorf("a title left the machine under the prompts policy: %s", redacted)
	}
	if n := strings.Count(redacted, "[redacted by cctrace client]"); n != 3 {
		t.Errorf("%d redaction markers, want the two titles and the name: %s", n, redacted)
	}
	if !strings.Contains(redacted, `"url": "https://github.com/o/r/pull/42"`) || !strings.Contains(redacted, `"total_cost_usd": 4.5`) {
		t.Errorf("outcome metadata did not arrive with the redacted snapshot: %s", redacted)
	}

	second := heartbeat()
	time.Sleep(10 * time.Millisecond)
	if err := redacting.FlushObservations(ctx); err != nil { // nothing dirty: the heartbeat alone
		t.Fatal(err)
	}
	if third := heartbeat(); !second.After(first) || !third.After(second) {
		t.Errorf("heartbeat at %v, %v, %v: want each flush to move it", first, second, third)
	}
}

// A title that goes A, B and back to A, with a send after each pass, ends at A in
// the row: each send is dated by when the snapshot changed, and the server keeps
// the newer of two.
func TestSessionSnapshotTitleABAEndsAtA(t *testing.T) {
	s := acquireSyncStore(t)
	truncateSyncTables(t, s)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE session_snapshots, collector_heartbeats CASCADE`); err != nil {
		t.Fatal(err)
	}
	srv, token := newSyncServer(t, s)
	defer srv.Close()

	const sid = "snap-aba"
	claudeDir := t.TempDir()
	dir := filepath.Join(claudeDir, "projects", "-tmp-snapshot-aba")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sid+".jsonl")
	user := `{"type":"user","uuid":"aba-u1","timestamp":"2026-10-01T00:00:00Z","sessionId":"` + sid + `","message":{"role":"user","content":"hi"}}` + "\n"
	if err := os.WriteFile(path, []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := syncer.LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	sy := syncer.New(claudeDir, "p@example.com", "u-aba", state, syncer.NewClient(srv.URL, token, "test"), nil)

	stored := func() string {
		t.Helper()
		var title string
		if err := s.Pool().QueryRow(ctx, `SELECT snapshot->>'ai_title' FROM session_snapshots WHERE session_id = $1`, sid).Scan(&title); err != nil {
			t.Fatalf("read the row: %v", err)
		}
		return title
	}
	for _, title := range []string{"TITLE-A", "TITLE-B", "TITLE-A"} {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(`{"type":"ai-title","aiTitle":"` + title + `","sessionId":"` + sid + `"}` + "\n"); err != nil {
			t.Fatal(err)
		}
		f.Close()
		// Distinct change times, so the order the row ends in is the order sent.
		time.Sleep(2 * time.Millisecond)
		if _, err := sy.SyncOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if err := sy.FlushObservations(ctx); err != nil {
			t.Fatal(err)
		}
		if got := stored(); got != title {
			t.Fatalf("after sending %s the row holds %s", title, got)
		}
	}
}

// The real client against the real API server and Postgres: a Codex rollout's
// turns arrive as its session_snapshots row -- a codex session, billed to
// openai under the account of its Codex home -- with codex_turns as the client
// built them and none of the rollout's text.
func TestCodexSnapshotReachesTheServer(t *testing.T) {
	s := acquireSyncStore(t)
	truncateSyncTables(t, s)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE session_snapshots, collector_heartbeats CASCADE`); err != nil {
		t.Fatal(err)
	}
	srv, token := newSyncServer(t, s)
	defer srv.Close()

	const sid, profileEmail, userID = "019d0000-0000-7000-8000-00000000c0de", "p@example.com", "u-codex-snapshot"
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "rollout-2026-10-01T09-00-00-"+sid+".jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-10-01T09:00:00.000Z","payload":{"id":"` + sid + `","cwd":"/tmp/codex-int","originator":"codex-tui","model_provider":"openai"}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:00:01.000Z","payload":{"type":"task_started","turn_id":"turn-1","started_at":1790845201,"model_context_window":258400,"collaboration_mode_kind":"default"}}`,
		`{"type":"turn_context","timestamp":"2026-10-01T09:00:01.100Z","payload":{"turn_id":"turn-1","cwd":"/tmp/codex-int","model":"test-model","effort":"high","collaboration_mode":{"mode":"default","settings":{"developer_instructions":"INT-DEVELOPER-TEXT"}}}}`,
		`{"type":"response_item","timestamp":"2026-10-01T09:00:02.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:00:06.000Z","payload":{"type":"task_complete","turn_id":"turn-1","completed_at":1790845206,"duration_ms":5000,"time_to_first_token_ms":800,"last_agent_message":"INT-FINAL-REPLY"}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:01:00.125Z","payload":{"type":"task_started","turn_id":"turn-2","root_turn_id":"turn-1","started_at":1790845260.25,"collaboration_mode_kind":"plan"}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:01:10.000Z","payload":{"type":"turn_aborted","turn_id":"turn-2","reason":"interrupted","completed_at":1790845270,"duration_ms":9750}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:02:00.000Z","payload":{"type":"task_started","turn_id":"turn-3","started_at":1790845320}}`,
		`{"type":"event_msg","timestamp":"2026-10-01T09:02:30.000Z","payload":{"type":"task_complete","turn_id":"turn-3","completed_at":1790845350,"error":{"message":"INT-ERROR-TEXT","codex_error_info":"context_window_exceeded"}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	codexState, err := syncer.LoadState(filepath.Join(t.TempDir(), "codex-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	codexState.SetOffset(path, 0)
	absHome, err := filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	codexState.ObserveCodexAccount(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), absHome, "acct-codex-int")
	if err := codexState.Save(); err != nil {
		t.Fatal(err)
	}
	client := syncer.NewClient(srv.URL, token, "test")
	if _, err := codexsyncer.New([]string{home}, profileEmail, userID, codexState, client, nil).SyncOnce(ctx); err != nil {
		t.Fatalf("codex sync: %v", err)
	}
	claudeState, err := syncer.LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := claudeState.Save(); err != nil {
		t.Fatal(err)
	}
	sy := syncer.New(t.TempDir(), profileEmail, userID, claudeState, client, nil)
	sy.ServeCodexSnapshots(codexState, []string{home})
	if err := sy.FlushObservations(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	var snapshot, agent, billing, account string
	if err := s.Pool().QueryRow(ctx, `SELECT snapshot::text, agent, billing_provider, account_id
		FROM session_snapshots WHERE session_id = $1 AND user_id = $2 AND profile_email = $3`, sid, userID, profileEmail).
		Scan(&snapshot, &agent, &billing, &account); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if agent != "codex" || billing != "openai" || account != "acct-codex-int" {
		t.Errorf("row is %s/%s/%s, want codex/openai/acct-codex-int", agent, billing, account)
	}
	var stored struct {
		CodexTurns []map[string]any `json:"codex_turns"`
	}
	if err := json.Unmarshal([]byte(snapshot), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.CodexTurns) != 3 {
		t.Fatalf("codex_turns %s, want three turns", snapshot)
	}
	first, second, third := stored.CodexTurns[0], stored.CodexTurns[1], stored.CodexTurns[2]
	if first["turn_id"] != "turn-1" || first["end"] != "complete" || first["started_at"] != "2026-10-01T09:00:01Z" ||
		first["ended_at"] != "2026-10-01T09:00:06Z" || first["duration_ms"] != float64(5000) || first["model"] != "test-model" ||
		first["effort"] != "high" || first["model_context_window"] != float64(258400) {
		t.Errorf("first turn %v", first)
	}
	if second["turn_id"] != "turn-2" || second["root_turn_id"] != "turn-1" || second["end"] != "aborted" ||
		second["abort_reason"] != "interrupted" || second["started_at"] != "2026-10-01T09:01:00.125Z" ||
		second["timestamps_exact"] != true || second["ended_at"] != "2026-10-01T09:01:10Z" || second["duration_ms"] != float64(9750) {
		t.Errorf("second turn %v", second)
	}
	if third["turn_id"] != "turn-3" || third["end"] != "error" || third["codex_error_info"] != "context_window_exceeded" {
		t.Errorf("third turn %v", third)
	}
	for _, text := range []string{"INT-FINAL-REPLY", "INT-DEVELOPER-TEXT", "INT-ERROR-TEXT", "hello"} {
		if strings.Contains(snapshot, text) {
			t.Errorf("%s reached the snapshot row: %s", text, snapshot)
		}
	}
}
