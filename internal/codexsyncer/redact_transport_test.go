package codexsyncer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/codexlog"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
)

func TestCodexRedactTransport(t *testing.T) {
	for _, format := range []string{"new", "old"} {
		for _, reenrich := range []bool{false, true} {
			mode := "send"
			if reenrich {
				mode = "reenrich"
			}
			for _, tc := range []struct {
				name   string
				policy syncer.RedactPolicy
			}{
				{"tool-only", syncer.RedactPolicy{ToolDetails: true}},
				{"prompt-only", syncer.RedactPolicy{UserPrompts: true}},
				{"both", syncer.RedactPolicy{UserPrompts: true, ToolDetails: true}},
				{"off", syncer.RedactPolicy{}},
			} {
				t.Run(format+"/"+mode+"/"+tc.name, func(t *testing.T) {
					home := t.TempDir()
					dir := filepath.Join(home, "sessions")
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(dir, "rollout-2026-09-08T00-00-00-12345678-1234-1234-1234-123456789abc.jsonl")
					fixture := `{"type":"response_item","timestamp":"2026-09-08T00:00:00Z","payload":{"type":"function_call","name":"shell","arguments":"synthetic-new-argument"}}
{"type":"response_item","timestamp":"2026-09-08T00:00:02Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"synthetic-user-prompt"}]}}
`
					if format == "old" {
						fixture = `{"type":"function_call","timestamp":"2026-09-08T00:00:00Z","name":"shell","arguments":"synthetic-new-argument"}
{"type":"message","timestamp":"2026-09-08T00:00:02Z","role":"user","content":[{"type":"input_text","text":"synthetic-user-prompt"}]}
`
					}
					if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
						t.Fatal(err)
					}
					var body []byte
					posts := 0
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/api/sync/capabilities" {
							_, _ = w.Write([]byte(`{"reenrich":true}`))
							return
						}
						if r.URL.Path != "/api/sync" || r.Method != http.MethodPost {
							t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
							http.NotFound(w, r)
							return
						}
						posts++
						var err error
						body, err = io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
						}
						_, _ = w.Write([]byte(`{"inserted":2,"updated":2}`))
					}))
					defer srv.Close()
					client := syncer.NewClient(srv.URL, "", "")
					client.SetRedactPolicy(tc.policy)
					var n int
					var err error
					if reenrich {
						state, e := syncer.LoadState(filepath.Join(t.TempDir(), "state.json"))
						if e != nil {
							t.Fatal(e)
						}
						s := New([]string{home}, "", "", state, client, nil)
						n, err = s.ReenrichOnce(context.Background())
					} else {
						parsed, _, e := codexlog.ScanFile(path, 0, "test-session")
						if e != nil {
							t.Fatal(e)
						}
						records := make([]*store.SessionRecord, 0, len(parsed))
						for _, r := range parsed {
							records = append(records, toStoreRecord(r, "", "", "test-session", filepath.Base(path), "cli"))
						}
						n, err = client.Send(context.Background(), "codex", "", "", "", "", syncer.ProjectIdentity{}, records)
					}
					if err != nil {
						t.Fatal(err)
					}
					var payload syncer.SyncPayload
					if err := json.Unmarshal(body, &payload); err != nil {
						t.Fatal(err)
					}
					if posts != 1 || n != 2 || len(payload.Records) != 2 || payload.Reenrich != reenrich {
						t.Fatalf("posts=%d count=%d records=%d reenrich=%v", posts, n, len(payload.Records), payload.Reenrich)
					}
					for secret, redacted := range map[string]bool{"synthetic-new-argument": tc.policy.ToolDetails, "synthetic-user-prompt": tc.policy.UserPrompts} {
						if strings.Contains(string(body), secret) == redacted {
							t.Errorf("secret %s presence inconsistent with redacted=%v: %s", secret, redacted, body)
						}
					}
					if strings.Contains(string(body), "[redacted by cctrace client]") != tc.policy.Enabled() {
						t.Errorf("unexpected marker presence: %s", body)
					}
				})
			}
		}
	}

}
