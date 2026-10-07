package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/jsonlscan"
	"cctrace/internal/projectrule"
	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
)

func TestRequestBodyBoundaryMeasurement(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"ascii", "a"}, {"quote", `\"`}, {"backslash", `\\`}, {"control", `\u0001`}, {"html", "<"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Five independent 16MB encodings. The race detector instruments every
			// byte touched, which turned 1.5s of work into 20s -- the second
			// largest cost in the whole unit suite. Nothing here is shared.
			t.Parallel()
			prefix := `{"type":"user","timestamp":"2026-09-09T00:00:00Z","uuid":"fixed","sessionId":"`
			suffix := `","message":{"role":"user","content":"hello"}}`
			n := (jsonlscan.MaxLineBytes - 1 - len(prefix) - len(suffix)) / len(tc.token)
			line := prefix + strings.Repeat(tc.token, n) + suffix
			var rec sessionlog.Record
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatal(err)
			}
			rec.RawLine = []byte(line)
			converted := toStoreRecord(&rec, "profile@example.invalid", "u1", "project")
			wire, err := encodeRequestJSON(SyncPayload{ProfileEmail: "profile@example.invalid", UserID: "u1", ProjectHash: "project", Records: []*store.SessionRecord{converted}})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("source=%d raw=%d single_record_wire=%d", len(line), len(converted.Raw), len(wire))
		})
	}
}

func TestProjectRuleBoundaryMeasurement(t *testing.T) {
	for _, token := range []string{"a", "\"", "\\", "\x01", "<"} {
		t.Run(fmt.Sprintf("byte-%02x", token[0]), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(strings.Repeat(token, 1<<20)), 0600); err != nil {
				t.Fatal(err)
			}
			rules, err := projectrule.Scan(context.Background(), projectrule.ScanOptions{Agent: projectrule.AgentClaude, RepositoryRoot: dir})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := encodeRequestJSON(&store.ProjectRuleIngestRequest{ProfileEmail: "profile@example.invalid", UserID: "u1", Agent: "claude", RepositoryID: "example.invalid/org/repo", Rules: rules})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("rule_count=%d source=%d wire=%d", len(rules), 1<<20, len(wire))
		})
	}
}
