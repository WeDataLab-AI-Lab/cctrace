package localapi

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A message typed while the previous turn ran is written seconds before its own
// turn starts. The local analysis must follow the message's recorded turn id,
// as the server does, instead of dropping or shifting it by time.
func TestCodexLocalAnalysisPlacesInputsByTheirTurnID(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sessions, "rollout-2026-01-01T00-00-00-01234567-89ab-cdef-0123-456789abcdef.jsonl")
	meta := func(turn string) string {
		return `"metadata":{"retained_source":{"id":{"role":"user","turn_id":"` + turn + `"}}}`
	}
	lines := []string{
		`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"01234567-89ab-cdef-0123-456789abcdef","cwd":"/sample","originator":"codex-tui"}}`,
		`{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-one"}}`,
		`{"timestamp":"2026-01-01T00:00:02Z","type":"response_item",` + meta("turn-one") + `,"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first request"}]}}`,
		`{"timestamp":"2026-01-01T00:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":12,"cached_input_tokens":3,"output_tokens":4}}}}`,
		`{"timestamp":"2026-01-01T00:00:10Z","type":"response_item",` + meta("turn-two") + `,"payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second request"}]}}`,
		`{"timestamp":"2026-01-01T00:00:12Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-one"}}`,
		`{"timestamp":"2026-01-01T00:00:20Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-two"}}`,
		`{"timestamp":"2026-01-01T00:00:21Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":12,"cached_input_tokens":3,"output_tokens":4}}}}`,
		`{"timestamp":"2026-01-01T00:00:22Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-two"}}`,
	}
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	s, err := New(Options{Address: "127.0.0.1:12345", Token: token, CodexDirs: []string{dir}, AllowedOrigins: []string{"http://127.0.0.1:60895"}})
	if err != nil {
		t.Fatal(err)
	}
	_, body := request(t, s, token, "/v1/sessions")
	var list ListResponse
	_ = json.Unmarshal(body, &list)
	if len(list.Sessions) != 1 {
		t.Fatalf("list %s", body)
	}
	code, body := request(t, s, token, "/v1/sessions/"+list.Sessions[0].LocalID+"/analysis")
	if code != 200 {
		t.Fatalf("analysis %d %s", code, body)
	}
	var analysis AnalysisResponse
	_ = json.Unmarshal(body, &analysis)
	want := []string{"first request", "second request"}
	if len(analysis.Turns) != len(want) {
		t.Fatalf("turns: %s", body)
	}
	for i, preview := range want {
		turn := analysis.Turns[i]
		if turn.Preview == nil || *turn.Preview != preview || turn.RequestNo == nil || *turn.RequestNo != i+1 {
			t.Fatalf("turn %d = %+v, want request %d opened by %q", i, turn, i+1, preview)
		}
	}
}
