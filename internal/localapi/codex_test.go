package localapi

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexRecordsAndLifecycle(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sessions, "rollout-2026-01-01T00-00-00-01234567-89ab-cdef-0123-456789abcdef.jsonl")
	lines := []string{
		`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"01234567-89ab-cdef-0123-456789abcdef","cwd":"/sample","originator":"codex-tui"}}`,
		`{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-one"}}`,
		`{"timestamp":"2026-01-01T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"timestamp":"2026-01-01T00:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":12,"cached_input_tokens":3,"output_tokens":4}}}}`,
		`{"timestamp":"2026-01-01T00:00:04Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-one"}}`,
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
	code, body := request(t, s, token, "/v1/sessions")
	if code != 200 {
		t.Fatalf("list %d %s", code, body)
	}
	var list ListResponse
	_ = json.Unmarshal(body, &list)
	if len(list.Sessions) != 1 {
		t.Fatalf("list %s", body)
	}
	id := list.Sessions[0].LocalID
	code, body = request(t, s, token, "/v1/sessions/"+id+"/records")
	if code != 200 {
		t.Fatalf("records %d %s", code, body)
	}
	var records RecordsResponse
	_ = json.Unmarshal(body, &records)
	if len(records.Records) == 0 {
		t.Fatal("missing Codex records")
	}
	code, body = request(t, s, token, "/v1/sessions/"+id+"/analysis")
	if code != 200 {
		t.Fatalf("analysis %d %s", code, body)
	}
	var analysis AnalysisResponse
	_ = json.Unmarshal(body, &analysis)
	if analysis.Summary.TurnCount == nil || *analysis.Summary.TurnCount != 1 || len(analysis.Turns) != 1 || analysis.Turns[0].Status != "ended" {
		t.Fatalf("lifecycle not observed: %s", body)
	}
	if analysis.Summary.InputTokens == nil || *analysis.Summary.InputTokens != 9 || analysis.Summary.CacheReadTokens == nil || *analysis.Summary.CacheReadTokens != 3 {
		t.Fatal("Codex cache reads were counted twice or usage was lost")
	}
	for _, record := range records.Records {
		found := false
		for _, row := range analysis.Timeline.Rows {
			if row.UUID == record.UUID {
				found = true
			}
		}
		if !found {
			t.Fatal("record identity is absent from its analysis timeline")
		}
	}
	code, body = request(t, s, token, "/v1/sessions/"+id+"/records?limit=1")
	var first RecordsResponse
	if code != 200 || json.Unmarshal(body, &first) != nil || len(first.Records) != 1 || first.NextCursor == nil {
		t.Fatalf("Codex first page status %d", code)
	}
	code, body = request(t, s, token, "/v1/sessions/"+id+"/records?limit=1&cursor="+*first.NextCursor)
	var second RecordsResponse
	if code != 200 || json.Unmarshal(body, &second) != nil || len(second.Records) != 1 || second.NextCursor != nil {
		t.Fatalf("Codex second page status %d", code)
	}
	if second.Records[0].RecordType != "usage" {
		t.Fatal("Codex page did not resume with usage")
	}
}
