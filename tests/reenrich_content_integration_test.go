package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

// storedRaws returns every stored raw of a session, one per line.
func storedRaws(t *testing.T, ctx context.Context, s *store.PgStore, sessionID string) string {
	t.Helper()
	rows, err := s.Pool().Query(ctx, `SELECT raw::text FROM session_records WHERE session_id = $1 ORDER BY ts`, sessionID)
	if err != nil {
		t.Fatalf("read raws: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out = append(out, raw)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

// A session collected while both redaction options were on, by a client that
// kept none of the collected fields, is re-enriched after the options were
// turned off. A default re-enrichment brings the signals and nothing the
// redaction withheld: the content fields stay on the machine and message is
// never merged. Asked for content, it attaches the content fields under the
// policy in force now -- which is what --include-content warns about -- and
// still never touches message.
func TestReenrichDoesNotUnredact(t *testing.T) {
	s := acquireSyncStore(t)
	truncateSyncTables(t, s)
	srv, token := newSyncServer(t, s)
	defer srv.Close()
	ctx := context.Background()

	const sid = "reenrich-unredact"
	head := func(n int, kind, uuid string) string {
		return `{"type":"` + kind + `","uuid":"` + uuid + `","timestamp":"2026-10-01T00:00:0` + string(rune('0'+n)) + `Z","sessionId":"` + sid + `"`
	}
	lines := []string{
		head(0, "user", "u-1") + `,"promptSource":"typed","message":{"role":"user","content":"PLAIN-PROMPT"}}`,
		head(1, "assistant", "a-1") + `,"message":{"role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_q","name":"AskUserQuestion","input":{"questions":[{"question":"PLAIN-QUESTION"}]}}]}}`,
		head(2, "user", "u-2") + `,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_q","content":"PLAIN-RESULT"}]},` +
			`"toolUseResult":{"questions":[{"question":"PLAIN-QUESTION"}],"answers":{"PLAIN-QUESTION":"PLAIN-ANSWER"}}}`,
		head(3, "attachment", "t-1") + `,"attachment":{"type":"queued_command","commandMode":"prompt","prompt":"PLAIN-QUEUED"}}`,
		head(4, "attachment", "t-2") + `,"attachment":{"type":"plan_file_reference","planFilePath":"/plans/PLAIN-PATH.md","planContent":"PLAIN-PLAN"}}`,
		head(5, "attachment", "t-3") + `,"attachment":{"type":"task_reminder","content":[{"id":"1","subject":"PLAIN-TASK","status":"pending"}]}}`,
		head(6, "system", "y-1") + `,"subtype":"away_summary","content":"PLAIN-RECAP"}`,
	}
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp-reenrich-unredact")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := syncer.LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(sessionPath, 0) // known to state, so the first-run skip does not apply
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	redacting := syncer.NewClient(srv.URL, token, "test")
	redacting.SetRedactPolicy(syncer.RedactPolicy{UserPrompts: true, ToolDetails: true})
	if n, err := syncer.New(claudeDir, "p@example.com", "u1", state, redacting, nil).SyncOnce(ctx); err != nil || n != len(lines) {
		t.Fatalf("redacted sync: sent %d, err %v; want %d", n, err, len(lines))
	}
	// As a client before CollectVersion stored them: the same redacted lines,
	// without any of the collected fields.
	if _, err := s.Pool().Exec(ctx, `UPDATE session_records SET raw = raw - $1::text[] WHERE session_id = $2`, sessionlog.CollectedKeys, sid); err != nil {
		t.Fatal(err)
	}
	if stored := storedRaws(t, ctx, s, sid); strings.Contains(stored, "PLAIN-") || strings.Contains(stored, "cctraceCollect") {
		t.Fatalf("fixture is not a redacted pre-collection session:\n%s", stored)
	}

	relaxed := syncer.NewClient(srv.URL, token, "test")
	if _, err := syncer.New(claudeDir, "p@example.com", "u1", state, relaxed, nil).ReenrichOnce(ctx, false); err != nil {
		t.Fatalf("default reenrich: %v", err)
	}
	stored := storedRaws(t, ctx, s, sid)
	if strings.Contains(stored, "PLAIN-") {
		t.Errorf("a default re-enrichment un-redacted the session:\n%s", stored)
	}
	// The version the client in this tree writes, not a literal: it was 1 until the
	// commit calls were collected (CollectVersion 2).
	marked := fmt.Sprintf(`"cctraceCollect": %d`, sessionlog.CollectVersion)
	if got := strings.Count(stored, marked); got != len(lines) {
		t.Errorf("%d of %d rows marked; want the signals merged into every one:\n%s", got, len(lines), stored)
	}
	for _, signal := range []string{`"type": "queued_command"`, `"type": "plan_file_reference"`, `"type": "task_reminder"`, `"subtype": "away_summary"`} {
		if !strings.Contains(stored, signal) {
			t.Errorf("signal %s did not arrive:\n%s", signal, stored)
		}
	}
	// The rows whose lines held content say so: answers, queued prompt, plan
	// file, task subjects, recap.
	if got := strings.Count(stored, `"cctraceWithheld"`); got != 5 {
		t.Errorf("%d rows name withheld content, want 5:\n%s", got, stored)
	}

	if _, err := syncer.New(claudeDir, "p@example.com", "u1", state, relaxed, nil).ReenrichOnce(ctx, true); err != nil {
		t.Fatalf("reenrich with content: %v", err)
	}
	stored = storedRaws(t, ctx, s, sid)
	for _, content := range []string{"PLAIN-ANSWER", "PLAIN-QUEUED", "PLAIN-PLAN", "PLAIN-PATH", "PLAIN-TASK", "PLAIN-RECAP"} {
		if !strings.Contains(stored, content) {
			t.Errorf("asked for content, %s was not attached:\n%s", content, stored)
		}
	}
	for _, message := range []string{"PLAIN-PROMPT", "PLAIN-RESULT"} {
		if strings.Contains(stored, message) {
			t.Errorf("message content %s was merged; message is never re-enrichment's to fill:\n%s", message, stored)
		}
	}
}
