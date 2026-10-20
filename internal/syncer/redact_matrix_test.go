package syncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// redactMatrixLines puts one distinct marker in every representation a piece of
// content can take on the way out: the AskUserQuestion answer as tool_result text
// and as toolUseResult.answers, the question and options as tool_use input, a
// plan as ExitPlanMode input and as a plan file attachment, a prompt typed while
// the agent was busy, Claude Code's recap, a task subject, Bash output, a
// background task's result both queued and as the user line it becomes, the
// summary a compacted session continues from, and the tool output Claude Code
// writes as conversation text (an SDK harness's tool result, a ! command's
// output, a local command's output).
var redactMatrixLines = []string{
	`{"type":"user","uuid":"m-1","timestamp":"2026-10-01T00:00:00Z","sessionId":"redact-matrix","promptSource":"typed","message":{"role":"user","content":[{"type":"text","text":"M-PROMPT"}]}}`,
	`{"type":"assistant","uuid":"m-2","timestamp":"2026-10-01T00:00:01Z","sessionId":"redact-matrix","message":{"role":"assistant","model":"m","content":[` +
		`{"type":"tool_use","id":"toolu_q","name":"AskUserQuestion","input":{"questions":[{"question":"M-AUQ-QUESTION","header":"H","multiSelect":false,"options":[{"label":"M-AUQ-LABEL","description":"M-AUQ-DESC"}]}]}},` +
		`{"type":"tool_use","id":"toolu_p","name":"ExitPlanMode","input":{"plan":"M-EXITPLAN"}},` +
		`{"type":"tool_use","id":"toolu_b","name":"Bash","input":{"command":"M-BASH-CMD"}}]}}`,
	`{"type":"user","uuid":"m-3","timestamp":"2026-10-01T00:00:02Z","sessionId":"redact-matrix","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_q","content":"Your questions have been answered: \"M-AUQ-QUESTION\"=\"M-ANSWER-TR\""}]},` +
		`"toolUseResult":{"questions":[{"question":"M-AUQ-QUESTION"}],"answers":{"M-AUQ-QUESTION":"M-ANSWER-TUR"},"annotations":{"M-AUQ-QUESTION":{"notes":"M-ANNOT"}}}}`,
	`{"type":"user","uuid":"m-4","timestamp":"2026-10-01T00:00:03Z","sessionId":"redact-matrix","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_b","content":[{"type":"text","text":"M-BASH-OUT"}]}]},` +
		`"toolUseResult":{"stdout":"M-STDOUT","stderr":"","interrupted":false,"isImage":false}}`,
	`{"type":"attachment","uuid":"m-5","timestamp":"2026-10-01T00:00:04Z","sessionId":"redact-matrix","attachment":{"type":"queued_command","prompt":"M-QUEUED","commandMode":"prompt"}}`,
	`{"type":"attachment","uuid":"m-6","timestamp":"2026-10-01T00:00:05Z","sessionId":"redact-matrix","attachment":{"type":"plan_file_reference","planFilePath":"/plans/M-PLANPATH.md","planContent":"M-PLANFILE"}}`,
	`{"type":"attachment","uuid":"m-7","timestamp":"2026-10-01T00:00:06Z","sessionId":"redact-matrix","attachment":{"type":"plan_mode","planFilePath":"/plans/M-PLANMODEPATH.md","planExists":true}}`,
	`{"type":"attachment","uuid":"m-8","timestamp":"2026-10-01T00:00:07Z","sessionId":"redact-matrix","attachment":{"type":"task_reminder","content":[{"id":"1","subject":"M-TASK","status":"pending"}]}}`,
	`{"type":"system","uuid":"m-9","timestamp":"2026-10-01T00:00:08Z","sessionId":"redact-matrix","subtype":"away_summary","content":"M-AWAY"}`,
	`{"type":"attachment","uuid":"m-10","timestamp":"2026-10-01T00:00:09Z","sessionId":"redact-matrix","attachment":{"type":"queued_command","commandMode":"task-notification",` +
		`"prompt":"<task-notification><summary>M-BGTASK-QUEUED</summary><output-file>/tmp/out</output-file></task-notification>"}}`,
	`{"type":"user","uuid":"m-11","timestamp":"2026-10-01T00:00:10Z","sessionId":"redact-matrix","origin":{"kind":"task-notification"},` +
		`"message":{"role":"user","content":"<task-notification><summary>M-BGTASK-USER</summary></task-notification>"}}`,
	`{"type":"user","uuid":"m-12","timestamp":"2026-10-01T00:00:11Z","sessionId":"redact-matrix","isCompactSummary":true,` +
		`"message":{"role":"user","content":"This session is being continued from a previous conversation. M-COMPACT"}}`,
	`{"type":"user","uuid":"m-13","timestamp":"2026-10-01T00:00:12Z","sessionId":"redact-matrix","entrypoint":"sdk-ts",` +
		`"message":{"role":"user","content":[{"type":"text","text":"Tool result (Bash, id=toolu_b): M-SDK-RESULT"}]}}`,
	`{"type":"user","uuid":"m-14","timestamp":"2026-10-01T00:00:13Z","sessionId":"redact-matrix",` +
		`"message":{"role":"user","content":"<bash-input>ls</bash-input><bash-stdout>M-BANG-OUT</bash-stdout><bash-stderr></bash-stderr>"}}`,
	`{"type":"user","uuid":"m-15","timestamp":"2026-10-01T00:00:14Z","sessionId":"redact-matrix",` +
		`"message":{"role":"user","content":"<local-command-stdout>M-LOCALCMD-OUT</local-command-stdout>"}}`,
}

// redactMatrixMarkers says which policy removes each marker. A marker survives a
// policy unless one of its enabled halves removes it. field marks the ones a
// collected content field carries, which re-enrichment sends only when asked for
// content; every other marker rides in message, which re-enrichment never sends.
var redactMatrixMarkers = []struct {
	text           string
	prompts, tools bool
	field          bool
}{
	{"M-PROMPT", true, false, false},
	{"M-AUQ-QUESTION", true, true, true}, // also the key toolUseResult.answers files the answer under
	{"M-AUQ-LABEL", true, true, false},
	{"M-AUQ-DESC", true, true, false},
	{"M-ANSWER-TR", true, true, false},
	{"M-ANSWER-TUR", true, true, true},
	{"M-ANNOT", true, true, true},
	{"M-EXITPLAN", true, true, false},
	{"M-BASH-CMD", true, true, false},
	{"M-BASH-OUT", true, true, false},
	{"M-QUEUED", true, false, true},
	{"M-PLANFILE", true, true, true},
	{"M-PLANPATH", false, true, true},
	{"M-PLANMODEPATH", false, true, true},
	{"M-TASK", true, true, true},
	{"M-AWAY", true, true, true},
	{"M-BGTASK-QUEUED", true, true, true},
	{"M-BGTASK-USER", true, true, false},
	{"M-COMPACT", true, true, false},
	// The documented exceptions (site-docs/reference/privacy.md): tool output
	// Claude Code writes as conversation text, which only the prompt policy
	// reaches. They predate the path rules; a change that closes one updates
	// the document with this table.
	{"M-SDK-RESULT", true, false, false},
	{"M-BANG-OUT", true, false, false},
	{"M-LOCALCMD-OUT", true, false, false},
}

// Every representation of the same content gets the same treatment, on the bytes
// that actually leave the machine, through every route raw takes out: a sync
// batch carries the whole line; a re-enrichment batch carries the collected
// fields alone -- never message -- and their content fields only when asked
// for. The policy-off column is what keeps this honest: it proves each marker
// made it into the payloads that carry it, so a marker missing under a policy
// was removed, not never sent.
func TestRedactionMatrixOnTheWire(t *testing.T) {
	policies := []struct {
		name   string
		policy RedactPolicy
	}{
		{"off", RedactPolicy{}},
		{"prompts", RedactPolicy{UserPrompts: true}},
		{"tools", RedactPolicy{ToolDetails: true}},
		{"both", RedactPolicy{UserPrompts: true, ToolDetails: true}},
	}
	for _, path := range []string{"sync", "reenrich", "reenrich-include-content"} {
		for _, p := range policies {
			t.Run(path+"/"+p.name, func(t *testing.T) {
				body := redactMatrixWire(t, path, p.policy)
				for _, m := range redactMatrixMarkers {
					removed := (p.policy.UserPrompts && m.prompts) || (p.policy.ToolDetails && m.tools) ||
						(path != "sync" && !m.field) || (path == "reenrich" && m.field)
					if present := strings.Contains(body, m.text); present == removed {
						t.Errorf("%s: present=%v, want present=%v", m.text, present, !removed)
					}
				}
				// toolUseResult keeps no stdout under any policy: collection drops
				// it before redaction ever runs.
				if strings.Contains(body, "M-STDOUT") {
					t.Error("toolUseResult.stdout reached the wire")
				}
				// A default re-enrichment sends nothing a policy could replace.
				wantMarker := p.policy.Enabled() && path != "reenrich"
				if strings.Contains(body, redactedMarker) != wantMarker {
					t.Errorf("marker present=%v, want %v", strings.Contains(body, redactedMarker), wantMarker)
				}
			})
		}
	}
}

// redactMatrixWire runs the real sync or reenrich pass over the fixture and
// returns every /api/sync body the server received.
func redactMatrixWire(t *testing.T, path string, policy RedactPolicy) string {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/sync/capabilities":
			_ = json.NewEncoder(w).Encode(syncCapabilities{Reenrich: true})
		case "/api/sync":
			body, _ := io.ReadAll(r.Body)
			var p SyncPayload
			_ = json.Unmarshal(body, &p)
			mu.Lock()
			bodies = append(bodies, string(body))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(syncResponse{Inserted: len(p.Records), Updated: len(p.Records)})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []AccountRef{}})
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "projects", "-tmp-redact-matrix")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte(strings.Join(redactMatrixLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Sync starts from zero; reenrich revisits the fixture's collected prefix.
	collectedOffset := int64(0)
	if path != "sync" {
		collectedOffset = int64(len(strings.Join(redactMatrixLines, "\n") + "\n"))
	}
	state.SetOffset(sessionPath, collectedOffset)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	client := NewClient(srv.URL, "", "")
	client.SetRedactPolicy(policy)
	s := New(dir, "p@example.com", "u1", state, client, nil)

	var n int
	switch path {
	case "sync":
		n, err = s.SyncOnce(context.Background())
	case "reenrich":
		n, err = s.ReenrichOnce(context.Background(), false)
	case "reenrich-include-content":
		n, err = s.ReenrichOnce(context.Background(), true)
	}
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if n != len(redactMatrixLines) {
		t.Fatalf("%s sent %d records, want %d", path, n, len(redactMatrixLines))
	}
	mu.Lock()
	defer mu.Unlock()
	return strings.Join(bodies, "\n")
}
