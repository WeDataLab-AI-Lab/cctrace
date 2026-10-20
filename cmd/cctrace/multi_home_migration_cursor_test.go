package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// HOME selects the migration registry, so both forms run serially.
func TestLegacyClaudePrimaryCanonicalPinPreservesGenericCursorTail(t *testing.T) {
	for _, logical := range []bool{false, true} {
		t.Run(map[bool]string{false: "implicit-default", true: "nonempty-symlink"}[logical], func(t *testing.T) {
			base, _ := profile.NormalizeCodexHome(useTempSyncHome(t))
			home := filepath.Join(base, ".claude")
			scalar := ""
			if logical {
				home = filepath.Join(base, "actual-claude")
				scalar = filepath.Join(base, ".claude-logical")
			}
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			if logical {
				if err := os.Symlink(home, scalar); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(home, "projects", "fixture", "session.jsonl")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			line := func(id string) string {
				return `{"type":"user","timestamp":"2026-10-01T00:00:00Z","sessionId":"migration-session","uuid":"` + id + `","message":{"role":"user","content":"` + id + `"}}` + "\n"
			}
			prefix, tail := line("already-sent"), line("migration-tail")
			if err := os.WriteFile(file, []byte(prefix+tail), 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var sent []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				if r.URL.Path == "/api/sync" {
					mu.Lock()
					sent = append(sent, string(b))
					mu.Unlock()
				}
				_, _ = io.WriteString(w, `{"inserted":1}`)
			}))
			defer server.Close()
			p := profile.NewDefault()
			p.User.ID = "fixture"
			p.User.Email = "fixture@example.test"
			p.Server.Endpoint = server.URL
			p.Server.SyncEndpoint = server.URL
			p.ClaudeConfigDir = scalar
			if err := profile.Save(p); err != nil {
				t.Fatal(err)
			}
			r := profile.Registry{Root: profile.DefaultDir()}
			cursor := filepath.Join(r.Root, "sync-state.json")
			state, err := syncer.LoadState(cursor)
			if err != nil {
				t.Fatal(err)
			}
			state.SetOffset(file, int64(len(prefix)))
			if err = state.Save(); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(cursor)
			updates, err := r.PlanOnboarding(base, []profile.HomeMembership{{Provider: "claude", Home: home, Transmission: "allow", QuotaEnabled: true}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.ApplyHomeUpdates(updates, true); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(cursor)
			if string(before) != string(after) {
				t.Fatal("migration rewrote original generic cursor")
			}
			entry, err := r.Load("default")
			if err != nil {
				t.Fatal(err)
			}
			job := &profileJob{entry: entry, endpoint: server.URL, claudeDir: home, claudeDirs: []string{home}}
			if err = job.prepare(context.Background(), localdaemon.Allocation{Allowed: []string{"claude:" + home}}); err != nil {
				t.Fatal(err)
			}
			for _, pass := range job.passes {
				if _, err = pass(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			body := strings.Join(sent, "\n")
			mu.Unlock()
			if !strings.Contains(body, "migration-tail") || strings.Contains(body, "already-sent") {
				t.Fatalf("canonical migration lost tail or replayed cursor: %s", body)
			}
			if entry.Profile.ClaudeConfigDir != home {
				t.Fatal("legacy physical primary was not canonical-pinned")
			}
		})
	}
}
