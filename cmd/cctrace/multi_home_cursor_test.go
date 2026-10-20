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

// The previous-holder resolver uses the shared HOME registry; cases are serial.
func TestClaudeHandoffResolvesSenderHomeCursorAndDeliversPendingTail(t *testing.T) {
	for _, tc := range []struct {
		name                                            string
		senderGeneric, targetGeneric, retired, existing bool
	}{
		{"generic-to-hash", true, false, false, false}, {"hash-to-generic-existing-target", false, true, false, true}, {"retired-generic-to-hash-existing-target", true, false, true, true}, {"retired-hash-to-generic-existing-target", false, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			home, _ := profile.NormalizeCodexHome(t.TempDir())
			file := filepath.Join(home, "projects", "fixture", "cursor.jsonl")
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			line := func(id string) string {
				return `{"type":"user","timestamp":"2026-10-01T00:00:00Z","sessionId":"cursor-session","uuid":"` + id + `","message":{"role":"user","content":"` + id + `"}}` + "\n"
			}
			prefix, tail := line("sent-old"), line("pending-tail")
			if err := os.WriteFile(file, []byte(prefix+tail), 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var delivered []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := io.ReadAll(r.Body)
				if r.URL.Path == "/api/sync" {
					mu.Lock()
					delivered = append(delivered, string(data))
					mu.Unlock()
				}
				_, _ = w.Write([]byte(`{"inserted":1}`))
			}))
			defer server.Close()
			makeEntry := func(name string, generic bool) profile.Entry {
				t.Helper()
				p := profile.NewDefault()
				p.User.ID = "fixture"
				p.User.Email = "fixture@example.test"
				p.Server.Endpoint = server.URL
				p.Server.SyncEndpoint = server.URL
				p.HomesVersion = 1
				p.Homes = []profile.HomeMembership{{Provider: "claude", Home: home, Transmission: "allow"}}
				if generic {
					p.ClaudeConfigDir = home
				}
				if err := profile.SaveNamed(p, name); err != nil {
					t.Fatal(err)
				}
				entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load(name)
				if err != nil {
					t.Fatal(err)
				}
				return entry
			}
			sender := makeEntry("prev", tc.senderGeneric)
			target := makeEntry("next", tc.targetGeneric)
			senderFile, targetFile := claudeStateFile(sender.Profile, home), claudeStateFile(target.Profile, home)
			state, err := syncer.LoadState(filepath.Join(sender.RuntimeDir, senderFile))
			if err != nil {
				t.Fatal(err)
			}
			state.SetOffset(file, int64(len(prefix)))
			if err = state.Save(); err != nil {
				t.Fatal(err)
			}
			if senderFile != targetFile {
				wrong, err := syncer.LoadState(filepath.Join(sender.RuntimeDir, targetFile))
				if err != nil {
					t.Fatal(err)
				}
				wrong.SetOffset(file, int64(len(prefix+tail)))
				if err = wrong.Save(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.existing {
				old, err := syncer.LoadState(filepath.Join(target.RuntimeDir, targetFile))
				if err != nil {
					t.Fatal(err)
				}
				old.SetOffset(file, int64(len(prefix+tail)))
				if err = old.Save(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.retired {
				r := profile.Registry{Root: profile.DefaultDir()}
				if _, err := r.ApplyHomeUpdates([]profile.HomeUpdate{{Name: sender.Name, Revision: sender.Revision, Homes: []profile.HomeMembership{}, Retire: true}}, true); err != nil {
					t.Fatal(err)
				}
			}
			job := &profileJob{entry: target, endpoint: server.URL, claudeDir: home, claudeDirs: []string{home}}
			allocation := localdaemon.Allocation{Allowed: []string{"claude:" + home}, Handoffs: []localdaemon.Handoff{{Line: "claude:" + home, From: sender.ID, ID: "home-cursor-handoff"}}}
			if err := job.prepare(context.Background(), allocation); err != nil {
				t.Fatal(err)
			}
			for _, pass := range job.passes {
				if _, err := pass(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			mu.Lock()
			body := strings.Join(delivered, "\n")
			mu.Unlock()
			if !strings.Contains(body, "pending-tail") || strings.Contains(body, "sent-old") {
				t.Fatalf("sender cursor tail lost or historical record duplicated: %s", body)
			}
		})
	}
}
