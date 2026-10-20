package syncer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/store"
)

func TestAuditContentRespectsEitherRedactionPolicy(t *testing.T) {
	t.Parallel()
	for _, policy := range []RedactPolicy{{UserPrompts: true}, {ToolDetails: true}} {
		for _, raw := range []string{
			`{"attachment":{"type":"instructions","files":[{"path":"/work/PRIVATE-PATH.md","content":"PRIVATE-INSTRUCTIONS"}]}}`,
			`{"attachment":{"type":"hook_success","hookName":"Read","content":"PRIVATE-HOOK","stdout":"PRIVATE-OUT","stderr":"PRIVATE-ERR","command":"PRIVATE-COMMAND"}}`,
			`{"attachment":{"type":"async_hook_response","response":{"hookSpecificOutput":{"additionalContext":"PRIVATE-CONTEXT"}},"stdout":"PRIVATE-ASYNC"}}`,
			`{"payload":{"base_instructions":{"text":"PRIVATE-BASE"},"developer_instructions":"PRIVATE-DEV","user_instructions":"PRIVATE-USER"}}`,
			`{"payload":{"collaboration_mode":{"settings":{"developer_instructions":"PRIVATE-NESTED"}}}}`,
		} {
			r := &store.SessionRecord{Raw: json.RawMessage(raw)}
			Redact([]*store.SessionRecord{r}, policy)
			if strings.Contains(string(r.Raw), "PRIVATE-") {
				t.Errorf("policy %+v leaked audit content: %s", policy, r.Raw)
			}
		}
	}
}

func TestAuditReenrichRestoresOnlyRequestedPermittedContent(t *testing.T) {
	t.Parallel()
	for _, include := range []bool{false, true} {
		for _, policy := range []RedactPolicy{{}, {UserPrompts: true}, {ToolDetails: true}} {
			srv, sent := reenrichScopeServer(t, nil)
			home := t.TempDir()
			path, starts := writeScopeSession(t, home, []string{
				`{"type":"attachment","uuid":"before-consent","timestamp":"2026-10-17T00:00:00Z","sessionId":"audit","attachment":{"type":"instructions","files":[{"path":"/PRECONSENT.md","content":"PRECONSENT-CONTENT"}]}}`,
				`{"type":"attachment","uuid":"instructions","timestamp":"2026-10-17T00:00:01Z","sessionId":"audit","attachment":{"type":"instructions","files":[{"path":"/work/CLAUDE.md","content":"AUDIT-INSTRUCTIONS"},{"path":"/work/AGENTS.md","content":"AUDIT-AGENTS"}]}}`,
				`{"type":"attachment","uuid":"hook","timestamp":"2026-10-17T00:00:02Z","sessionId":"audit","attachment":{"type":"async_hook_response","hookName":"Stop","response":{"hookSpecificOutput":{"additionalContext":"AUDIT-HOOK"}},"stdout":"AUDIT-STDOUT"}}`,
			})
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			state.SetOffset(path, fi.Size())
			state.Files[path].SkippedAtFirstSync = starts[1]
			client := NewClient(srv.URL, "", "")
			client.SetRedactPolicy(policy)
			s := New(home, "", "", state, client, nil)
			if _, err := s.ReenrichOnce(context.Background(), include); err != nil {
				t.Fatal(err)
			}
			records := sent()
			if len(records) != 2 {
				t.Fatalf("sent %d records, want only the two permitted records", len(records))
			}
			body, err := json.Marshal(records)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "PRECONSENT") {
				t.Errorf("skipped history was uploaded")
			}
			for _, text := range []string{"AUDIT-INSTRUCTIONS", "AUDIT-AGENTS", "AUDIT-HOOK", "AUDIT-STDOUT"} {
				if got, want := strings.Contains(string(body), text), include && !policy.Enabled(); got != want {
					t.Errorf("include=%v policy=%+v %s sent=%v want=%v", include, policy, text, got, want)
				}
			}
			if state.GetOffset(path) != fi.Size() {
				t.Error("recovery changed the tail offset")
			}
		}
	}
}
