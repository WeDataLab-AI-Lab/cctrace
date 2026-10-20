package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// uploadCounter serves the sync routes and counts the record uploads.
func uploadCounter(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync" {
			uploads.Add(1)
			_, _ = w.Write([]byte(`{"inserted":2,"updated":2}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server, &uploads
}

// daemonRecord is what a running daemon says one of its jobs reads: the lines in
// allowed, and, left out, the lines in held. The route ids are made up.
func daemonRecord(revision string, allowed []string, held ...string) localdaemon.JobAllocation {
	job := localdaemon.JobAllocation{Revision: revision, Allowed: allowed}
	if job.Allowed == nil {
		job.Allowed = []string{}
	}
	for _, line := range held {
		job.Excluded = append(job.Excluded, localdaemon.ExcludedLine{Line: line, Route: "4c7e19ab"})
	}
	return job
}

func registryRevision(t *testing.T, name string) string {
	t.Helper()
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return entry.Revision
}

// A profile the daemon lets read nothing holds no lease, so the SessionEnd hook's
// --once child finds the profile's lock free. The daemon decides what each
// profile reads so that it never forwards a source another profile owns; the
// child must not undo that by collecting all of the profile's sources itself.
// The hook's parent has already left the session end in the pending directory,
// where it waits for the route to clear.
func TestOneShotRunOfAProfileTheDaemonLetsReadNothingUploadsNothing(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	server, uploads := uploadCounter(t)
	entry, sessionFile := managedObservationsProfile(t, "dup", server.URL)
	claudeDir := entry.Profile.ClaudeConfigDir
	publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{"named:dup": daemonRecord(entry.Revision, nil, "claude:"+claudeDir)}, nil)

	withHookStdin(t, map[string]any{
		"session_id": "obs-1", "transcript_path": sessionFile, "cwd": "/tmp/repo",
		"hook_event_name": "SessionEnd", "reason": "prompt_input_exit",
	})
	applyDaemonParentUpdateFn = func(context.Context, string, string, bool) daemonParentUpdateResult {
		return daemonParentUpdateResult{}
	}
	var childArgs []string
	spawnSyncProcessFn = func(args []string, _ string) (int, error) {
		childArgs = args
		return 123, nil
	}
	captureOutput(t, func() {
		if err := runSync(false, claudeDir, false, true, true, false, time.Second, "dup", "", "", false, "", false); err != nil {
			t.Fatalf("daemon parent: %v", err)
		}
	})
	if len(childArgs) == 0 {
		t.Fatal("the hook's parent started no --once child")
	}

	var runErr error
	_, stderr := captureOutput(t, func() {
		runErr = runSync(false, claudeDir, false, false, true, false, time.Second, "dup", "", "", false, "", false)
	})
	if runErr != nil {
		t.Fatalf("--once child: %v", runErr)
	}
	if got := uploads.Load(); got != 0 {
		t.Fatalf("the child of a profile the daemon holds back uploaded %d times", got)
	}
	if !strings.Contains(stderr, "profile=dup ") || !strings.Contains(stderr, "duplicate_route") {
		t.Errorf("stderr %q does not say, under the profile, why nothing was collected", stderr)
	}
	pending, err := os.ReadDir(syncer.SessionEndDir(filepath.Join(entry.RuntimeDir, "sync-state.json")))
	if err != nil || len(pending) != 1 {
		t.Errorf("pending session ends %d, %v; the held profile keeps the one the hook recorded", len(pending), err)
	}
	state, err := syncer.LoadState(filepath.Join(entry.RuntimeDir, "sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if offset := state.GetOffset(sessionFile); offset != 0 {
		t.Errorf("the held profile's cursor moved to %d", offset)
	}
}

// Only a profile the daemon's record says is held back is held: one it lets
// read, or one it says nothing of, is synced by the one-shot run as before, and
// so is any profile when no daemon runs -- a record left behind by one that has
// exited says nothing about today.
func TestOneShotRunSyncsUnlessRunningDaemonHoldsTheProfilesLines(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		jobs    func(entry profile.Entry) map[string]localdaemon.JobAllocation
	}{
		{"daemon lets the profile read", true, func(e profile.Entry) map[string]localdaemon.JobAllocation {
			return map[string]localdaemon.JobAllocation{e.ID: daemonRecord(e.Revision, []string{"claude:" + e.Profile.ClaudeConfigDir})}
		}},
		{"daemon holds another profile's line", true, func(e profile.Entry) map[string]localdaemon.JobAllocation {
			return map[string]localdaemon.JobAllocation{"named:other": daemonRecord("1", nil, "claude:"+e.Profile.ClaudeConfigDir)}
		}},
		{"a record is left behind by a daemon that has exited", false, func(e profile.Entry) map[string]localdaemon.JobAllocation {
			return map[string]localdaemon.JobAllocation{e.ID: daemonRecord(e.Revision, nil, "claude:"+e.Profile.ClaudeConfigDir)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			server, uploads := uploadCounter(t)
			entry, _ := managedObservationsProfile(t, "dup", server.URL)
			if tc.running {
				publishDaemon(t, "inst-gate", tc.jobs(entry), nil)
			} else {
				writeJSONFile0600(t, filepath.Join(profile.DefaultDir(), "daemon-allocations.json"), localdaemon.AllocationsFile{InstanceID: "gone", Jobs: tc.jobs(entry)})
			}
			captureOutput(t, func() {
				if err := runSync(false, entry.Profile.ClaudeConfigDir, false, false, true, false, time.Second, "dup", "", "", false, "", false); err != nil {
					t.Fatalf("--once: %v", err)
				}
			})
			if uploads.Load() == 0 {
				t.Fatal("the one-shot run uploaded nothing")
			}
		})
	}
}

// A record that is not the running daemon's current one for the profile is not
// followed: the registry decides, as it does when no daemon runs, and a profile
// that shares nothing reads everything.
func TestOneShotRunPlansFromTheRegistryWhenTheDaemonsRecordIsNotCurrent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		instance string
		revision string
	}{
		{"left by another run of the daemon", "an-earlier-daemon", ""},
		{"the profile has been edited since", "inst-gate", "an-older-revision"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			server, uploads := uploadCounter(t)
			entry, _ := managedObservationsProfile(t, "dup", server.URL)
			revision := tc.revision
			if revision == "" {
				revision = entry.Revision
			}
			publishDaemon(t, tc.instance, map[string]localdaemon.JobAllocation{entry.ID: daemonRecord(revision, nil, "claude:"+entry.Profile.ClaudeConfigDir)}, nil)
			captureOutput(t, func() {
				if err := runSync(false, entry.Profile.ClaudeConfigDir, false, false, true, false, time.Second, "dup", "", "", false, "", false); err != nil {
					t.Fatalf("--once: %v", err)
				}
			})
			if uploads.Load() == 0 {
				t.Fatal("a record that is not the running daemon's current one held the profile back")
			}
		})
	}
}

// The default profile is "" to the one-shot run and "default" to the daemon's
// record, and a named profile is "named:<name>" there. The mapping holds in both
// directions: the default profile is held when the daemon holds "default", and is
// not held for a named profile the daemon holds.
func TestOneShotRunOfTheDefaultProfileFollowsTheRecordOfDefault(t *testing.T) {
	for _, tc := range []struct {
		name        string
		heldID      string
		wantUploads bool
	}{
		{"daemon holds the default profile", "default", false},
		{"daemon holds a named profile only", "named:dup", true},
		{"daemon holds a profile named like the default", "named:default", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			server, uploads := uploadCounter(t)
			claudeDir := observationsProfile(t, server.URL, onceSession...)
			publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{
				tc.heldID: daemonRecord(registryRevision(t, "default"), nil, "claude:"+claudeDir),
			}, nil)
			var runErr error
			_, stderr := captureOutput(t, func() {
				runErr = runSync(false, claudeDir, false, false, true, false, time.Second, "", "", "", false, "", false)
			})
			if runErr != nil {
				t.Fatalf("--once: %v", runErr)
			}
			if got := uploads.Load() > 0; got != tc.wantUploads {
				t.Fatalf("uploaded=%v, want %v (stderr %q)", got, tc.wantUploads, stderr)
			}
			if !tc.wantUploads && !strings.Contains(stderr, "profile=default ") {
				t.Errorf("stderr %q does not name the default profile", stderr)
			}
		})
	}
}

// The record is the daemon's own file. One that cannot be read -- not JSON, or
// writable by others, which ReadJSON refuses -- says nothing about the profile,
// and the registry decides.
func TestOneShotRunIgnoresADaemonRecordItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"not JSON", []byte(`{"jobs":{"named:dup":`), 0o600},
		{"writable by others", []byte(`{"instance_id":"inst-gate","jobs":{"named:dup":{"allowed":[]}}}`), 0o666},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			server, uploads := uploadCounter(t)
			entry, _ := managedObservationsProfile(t, "dup", server.URL)
			publishDaemon(t, "inst-gate", nil, nil)
			path := filepath.Join(profile.DefaultDir(), "daemon-allocations.json")
			if err := os.WriteFile(path, tc.data, tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			captureOutput(t, func() {
				if err := runSync(false, entry.Profile.ClaudeConfigDir, false, false, true, false, time.Second, "dup", "", "", false, "", false); err != nil {
					t.Fatalf("--once: %v", err)
				}
			})
			if uploads.Load() == 0 {
				t.Fatal("the one-shot run uploaded nothing for a record it could not read")
			}
		})
	}
}

// `sync --watch --once` is a one-shot run for the gate too, whatever it does with
// the watch flag: it must not collect what the daemon holds back. Without the
// gate it would not return -- it would watch -- so the call is bounded.
func TestWatchOnceRunOfAProfileTheDaemonHoldsBackUploadsNothing(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	server, uploads := uploadCounter(t)
	entry, _ := managedObservationsProfile(t, "dup", server.URL)
	publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{entry.ID: daemonRecord(entry.Revision, nil, "claude:"+entry.Profile.ClaudeConfigDir)}, nil)
	done := make(chan error, 1)
	go func() {
		done <- runSync(false, entry.Profile.ClaudeConfigDir, true, false, true, false, time.Second, "dup", "", "", false, "", false)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("--watch --once: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--watch --once of a held profile did not return")
	}
	if got := uploads.Load(); got != 0 {
		t.Fatalf("--watch --once uploaded %d times", got)
	}
}

// mixedProfile is a profile that collects Claude and one Codex folder, and the
// rollout in that folder, with the session in Claude's known to its state.
func mixedProfile(t *testing.T, name, endpoint string) (entry profile.Entry, sessionFile, codexLine, rollout string) {
	t.Helper()
	codexHome := filepath.Join(t.TempDir(), "codex")
	rollout = writeCodexRollout(t, codexHome, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl")
	entry, sessionFile = managedObservationsProfile(t, name, endpoint, func(p *profile.Profile) {
		p.Options.CodexSyncEnabled = true
		p.Options.CodexDirs = []string{codexHome}
	})
	dirs := resolveCodexScanDirs(io.Discard, entry.Profile)
	if len(dirs) != 1 {
		t.Fatalf("codex dirs = %v", dirs)
	}
	return entry, sessionFile, "codex:" + dirs[0], rollout
}

// Some of a profile's sources are held back and the rest are not: the run reads
// the rest, one kind at a time.
func TestOneShotRunReadsOnlyTheLinesTheDaemonAllows(t *testing.T) {
	for _, tc := range []struct {
		name               string
		holdClaude         bool
		claudeRead, codexR bool
	}{
		{"the Codex folder is held", false, true, false},
		{"the Claude folder is held", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempSyncHome(t)
			resetSyncControlHooks(t)
			server, uploads := uploadCounter(t)
			entry, sessionFile, codexLine, rollout := mixedProfile(t, "mix", server.URL)
			claudeLine := "claude:" + entry.Profile.ClaudeConfigDir
			held, allowed := codexLine, claudeLine
			if tc.holdClaude {
				held, allowed = claudeLine, codexLine
			}
			publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{entry.ID: daemonRecord(entry.Revision, []string{allowed}, held)}, nil)
			_, stderr := captureOutput(t, func() {
				if err := runSync(false, entry.Profile.ClaudeConfigDir, false, false, true, false, time.Second, "mix", "", "", false, "", false); err != nil {
					t.Fatalf("--once: %v", err)
				}
			})
			// What was left alone is said, by kind and route, never by folder.
			if !strings.Contains(stderr, "profile=mix [routes] kind=") || !strings.Contains(stderr, "was not read") {
				t.Errorf("stderr %q does not say what was left alone", stderr)
			}
			for _, folder := range []string{entry.Profile.ClaudeConfigDir, filepath.Dir(rollout), filepath.Dir(filepath.Dir(rollout))} {
				if strings.Contains(stderr, folder) {
					t.Errorf("stderr names the folder %q: %s", folder, stderr)
				}
			}

			claudeState, err := syncer.LoadState(filepath.Join(entry.RuntimeDir, "sync-state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := claudeState.GetOffset(sessionFile) > 0; got != tc.claudeRead {
				t.Errorf("Claude read=%v, want %v", got, tc.claudeRead)
			}
			if got := (uploads.Load() > 0); got != tc.claudeRead {
				t.Errorf("uploads=%d with Claude read=%v", uploads.Load(), tc.claudeRead)
			}
			codexState, err := syncer.LoadState(filepath.Join(entry.RuntimeDir, "codex-sync-state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if got := codexState.GetOffset(rollout) > 0; got != tc.codexR {
				t.Errorf("Codex read=%v, want %v", got, tc.codexR)
			}
		})
	}
}

// With no daemon the registry decides who reads a source two profiles share, and
// the one that loses reads none of it, whichever of them runs.
func TestOneShotRunWithNoDaemonLeavesASharedFolderToTheProfileThatWouldHoldIt(t *testing.T) {
	home := useTempSyncHome(t)
	resetSyncControlHooks(t)
	server, _ := uploadCounter(t)
	shared := filepath.Join(home, "shared-codex")
	rollout := writeCodexRollout(t, shared, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl")
	same := func(p *profile.Profile) {
		p.User = profile.UserInfo{ID: "user-1", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
		p.Server.Endpoint, p.Server.SyncEndpoint = server.URL, server.URL
		p.Options.SyncEnabled = true
		p.Options.CodexSyncEnabled = true
		p.Options.CodexDirs = []string{shared}
	}
	holder, _ := codexProfile(t, "holder", shared, same)
	work, _ := codexProfile(t, "work", shared, same)

	for _, run := range []struct {
		entry profile.Entry
		read  bool
	}{{work, false}, {holder, true}} {
		captureOutput(t, func() {
			if err := runSync(false, run.entry.Profile.ClaudeConfigDir, false, false, true, false, time.Second, run.entry.Name, "", "", false, "", false); err != nil {
				t.Fatalf("%s: %v", run.entry.Name, err)
			}
		})
		state, err := syncer.LoadState(filepath.Join(run.entry.RuntimeDir, "codex-sync-state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if got := state.GetOffset(rollout) > 0; got != run.read {
			t.Errorf("%s read the shared folder=%v, want %v", run.entry.Name, got, run.read)
		}
	}
	if _, err := os.Stat(filepath.Join(profile.DefaultDir(), "daemon-routes.json")); !os.IsNotExist(err) {
		t.Fatalf("a one-shot run decided something for good: %v", err)
	}
}

// A re-enrichment reads the Claude folder, so it follows the same decision.
func TestReenrichLeavesAFolderTheDaemonHoldsBack(t *testing.T) {
	for _, held := range []bool{true, false} {
		useTempSyncHome(t)
		var posts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/sync/capabilities":
				_, _ = w.Write([]byte(`{"reenrich":true}`))
			case "/api/sync":
				posts.Add(1)
				_, _ = w.Write([]byte(`{"updated":1}`))
			default:
				_, _ = w.Write([]byte(`{"accounts":[]}`))
			}
		}))
		claudeDir := observationsProfile(t, server.URL, onceSession...)
		// Re-enrichment revisits consumed bytes only. This route test starts
		// with an already collected transcript, not an unread offset-zero tail.
		transcript := filepath.Join(claudeDir, "projects", "-tmp-repo", "obs-1.jsonl")
		info, err := os.Stat(transcript)
		if err != nil {
			t.Fatal(err)
		}
		state, err := syncer.LoadState(syncer.DefaultStatePath())
		if err != nil {
			t.Fatal(err)
		}
		state.SetOffset(transcript, info.Size())
		if err := state.Save(); err != nil {
			t.Fatal(err)
		}
		if held {
			publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{"default": daemonRecord(registryRevision(t, "default"), nil, "claude:"+claudeDir)}, nil)
		}
		captureOutput(t, func() {
			if err := runSyncReenrich(claudeDir, "", "", "", false, false); err != nil {
				t.Errorf("reenrich (held=%v): %v", held, err)
			}
		})
		server.Close()
		if got := posts.Load() > 0; got == held {
			t.Errorf("held=%v but reenrich posted %d times", held, posts.Load())
		}
	}
}
