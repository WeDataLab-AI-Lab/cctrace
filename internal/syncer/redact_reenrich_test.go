package syncer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		t.Run(tc.name, func(t *testing.T) {
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
			client := NewClient(srv.URL, "", "")
			client.SetRedactPolicy(tc.policy)
			s := New(dir, "", "", state, client, nil)
			n, err := s.ReenrichOnce(context.Background())
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
			for secret, redacted := range map[string]bool{"synthetic-claude-prompt": tc.policy.UserPrompts, "synthetic-claude-tool": tc.policy.ToolDetails || tc.policy.UserPrompts} {
				if strings.Contains(string(body), secret) == redacted {
					t.Errorf("secret %s presence inconsistent with redacted=%v: %s", secret, redacted, body)
				}
			}
			if strings.Contains(string(body), redactedMarker) != tc.policy.Enabled() {
				t.Errorf("unexpected marker presence: %s", body)
			}
		})
	}
}
