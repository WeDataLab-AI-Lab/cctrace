package syncer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A re-enrichment batch carries a line's collected fields and never its message,
// so the conversation stays on the machine under every policy, the policy off
// included -- with or without content asked for. (Redaction of the content
// fields it can carry is TestRedactionMatrixOnTheWire.)
func TestClaudeReenrichRedactTransport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy RedactPolicy
	}{
		{"tool-only", RedactPolicy{ToolDetails: true}},
		{"prompt-only", RedactPolicy{UserPrompts: true}},
		{"both", RedactPolicy{UserPrompts: true, ToolDetails: true}},
		{"off", RedactPolicy{}},
	} {
		for _, includeContent := range []bool{false, true} {
			name := tc.name
			if includeContent {
				name += "/include-content"
			}
			t.Run(name, func(t *testing.T) {
				got, wire, srv := reenrichIdentityCaptureServer(t)
				defer srv.Close()
				dir := t.TempDir()
				sessionDir := filepath.Join(dir, "projects", "-tmp-redact")
				if err := os.MkdirAll(sessionDir, 0755); err != nil {
					t.Fatal(err)
				}
				fixture := `{"type":"user","timestamp":"2026-09-08T00:00:00Z","sessionId":"redact-session","uuid":"turn-1","message":{"role":"user","content":[{"type":"text","text":"synthetic-claude-prompt"},{"type":"tool_use","id":"tool-1","name":"shell","input":{"command":"synthetic-claude-tool"}}]}}` + "\n"
				if err := os.WriteFile(filepath.Join(sessionDir, "session.jsonl"), []byte(fixture), 0600); err != nil {
					t.Fatal(err)
				}
				state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
				if err != nil {
					t.Fatal(err)
				}
				state.SetOffset(filepath.Join(sessionDir, "session.jsonl"), int64(len(fixture)))
				client := NewClient(srv.URL, "", "")
				client.SetRedactPolicy(tc.policy)
				s := New(dir, "", "", state, client, nil)
				n, err := s.ReenrichOnce(context.Background(), includeContent)
				if err != nil {
					t.Fatal(err)
				}
				if n != 1 || len(got.Records) != 1 || !got.Reenrich {
					t.Fatalf("updated=%d records=%d reenrich=%v", n, len(got.Records), got.Reenrich)
				}
				body, err := json.Marshal(wire)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"synthetic-claude-prompt", "synthetic-claude-tool"} {
					if strings.Contains(string(body), secret) {
						t.Errorf("includeContent=%v: %s left the machine: %s", includeContent, secret, body)
					}
				}
				if strings.Contains(string(body), `"message"`) {
					t.Errorf("includeContent=%v: a re-enrichment batch carried message: %s", includeContent, body)
				}
			})
		}
	}
}
