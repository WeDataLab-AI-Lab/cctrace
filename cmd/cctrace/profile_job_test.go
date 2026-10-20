package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

func TestProfileJobIndependentAuthServerAndCursor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"one", "two"} {
		name := name
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/sync" {
				w.Write([]byte(`{}`))
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+name {
				t.Errorf("token attribution %s: %s", name, got)
			}
			var payload syncer.SyncPayload
			if json.NewDecoder(r.Body).Decode(&payload) != nil {
				t.Error("bad upload")
			}
			if payload.UserID != name || payload.ProfileEmail != name+"@fixture.invalid" {
				t.Errorf("identity mix: %+v", payload)
			}
			if name == "one" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"inserted":1}`))
		}))
		defer server.Close()
		p := profile.NewDefault()
		p.Server.Endpoint = server.URL
		p.Server.SyncEndpoint = server.URL
		p.Server.AuthToken = name
		p.User = profile.UserInfo{ID: name, Name: name, Email: name + "@fixture.invalid", Team: "fixture"}
		p.ClaudeConfigDir = filepath.Join(home, name)
		file := filepath.Join(p.ClaudeConfigDir, "projects", "fixture", "session.jsonl")
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		line := `{"type":"user","timestamp":"2026-10-12T09:00:00Z","sessionId":"fixture","uuid":"fixture-` + name + `","message":{"role":"user","content":"fixture"}}` + "\n"
		if err := os.WriteFile(file, []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
		if err := profile.SaveNamed(p, name); err != nil {
			t.Fatal(err)
		}
		entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load(name)
		if err != nil {
			t.Fatal(err)
		}
		state, err := syncer.LoadState(filepath.Join(entry.RuntimeDir, "sync-state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := state.Save(); err != nil {
			t.Fatal(err)
		}
		spec := profileJobSpec(entry, daemonOverrides{})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		release, err := spec.Acquire(ctx, localdaemon.Allocation{Allowed: spec.Sources})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var progress localdaemon.Progress
		err = spec.Run(ctx, func(p localdaemon.Progress) { progress = p })
		release()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		state, err = syncer.LoadState(filepath.Join(entry.RuntimeDir, "sync-state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if name == "one" {
			if progress.ErrorCode != "auth_error" || state.GetOffset(file) != 0 {
				t.Fatalf("auth failure incorrectly advanced: %+v %d", progress, state.GetOffset(file))
			}
		} else if progress.State != "healthy" || state.GetOffset(file) != int64(len(line)) {
			t.Fatalf("healthy job did not advance its own cursor: %+v %d", progress, state.GetOffset(file))
		}
	}
}

// An explicit codex_home is rechecked before it is used (profile.CheckCodexHome)
// by every collection path. The managed job builds its sources and scan roots
// itself, so a home replaced by a symlink after configuration must be left out
// there too, not scanned and not claimed as a route.
func TestProfileJobSpecSkipsCodexHomeThatFailsTheCheck(t *testing.T) {
	useTempSyncHome(t)
	logged := captureJobLog(t)
	goodHome, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	linkedHome := filepath.Join(filepath.Dir(target), "linked-"+filepath.Base(target))
	if err := os.Symlink(target, linkedHome); err != nil {
		t.Fatal(err)
	}
	build := func(name, codexHome string) localdaemon.Spec {
		p := profile.NewDefault()
		p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
		p.Options.CodexSyncEnabled = true
		p.CodexHome = codexHome
		p.User = profile.UserInfo{ID: name, Name: name, Email: name + "@fixture.invalid", Team: "fixture"}
		if err := profile.SaveNamed(p, name); err != nil {
			t.Fatal(err)
		}
		entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load(name)
		if err != nil {
			t.Fatal(err)
		}
		return profileJobSpec(entry, daemonOverrides{})
	}
	hasSource := func(spec localdaemon.Spec, dir string) bool {
		for _, source := range spec.Sources {
			if source == "codex:"+dir {
				return true
			}
		}
		return false
	}
	if spec := build("good", goodHome); !hasSource(spec, goodHome) {
		t.Fatalf("a home that passes the check is not a source: %v", spec.Sources)
	}
	spec := build("linked", linkedHome)
	if hasSource(spec, linkedHome) || hasSource(spec, target) {
		t.Fatalf("a home that fails the check became a source: %v", spec.Sources)
	}

	// The skip is said once, when the job takes its lease, not on every
	// refresh that rebuilds the spec.
	if strings.Contains(logged.String(), "codex_home") {
		t.Fatalf("building the specs already reported the skip: %q", logged.String())
	}
	release, err := spec.Acquire(context.Background(), localdaemon.Allocation{Allowed: spec.Sources})
	if err != nil {
		t.Fatal(err)
	}
	release()
	const warning = "profile=linked [codex-sync] explicit codex_home is unavailable; it was not scanned"
	if got := strings.Count(logged.String(), warning); got != 1 {
		t.Fatalf("the skipped home was reported %d times, want once: %q", got, logged.String())
	}
	if strings.Contains(logged.String(), linkedHome) || strings.Contains(logged.String(), target) {
		t.Fatalf("the report names the home: %q", logged.String())
	}
}

// captureJobLog routes what the jobs log into the returned buffer. It is read
// after the job's pass or lease calls return, which is when the writes are done.
func captureJobLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logged bytes.Buffer
	prevOut, prevInstalled := log.Writer(), daemonLogInstalled
	log.SetOutput(&logged)
	daemonLogInstalled = true
	t.Cleanup(func() { log.SetOutput(prevOut); daemonLogInstalled = prevInstalled })
	return &logged
}

// The spec's check can be hours older than the lease that scans, so the lease
// checks again: a home that became a symlink after the spec was built is not
// scanned, and the skip is said once per lease, however many passes it runs.
func TestProfileJobLeaseLeavesOutCodexHomeThatFailsAfterTheSpec(t *testing.T) {
	useTempSyncHome(t)
	logged := captureJobLog(t)
	codexHome, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer server.Close()
	p := profile.NewDefault()
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Server.Endpoint, p.Server.SyncEndpoint = server.URL, server.URL
	p.Options.CodexSyncEnabled = true
	p.CodexHome = codexHome
	p.User = profile.UserInfo{ID: "one", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
	if err := profile.SaveNamed(p, "one"); err != nil {
		t.Fatal(err)
	}
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load("one")
	if err != nil {
		t.Fatal(err)
	}
	spec := profileJobSpec(entry, daemonOverrides{})
	if !slices.Contains(spec.Sources, "codex:"+codexHome) {
		t.Fatalf("the home that passed the check is not a source: %v", spec.Sources)
	}
	if err := os.RemoveAll(codexHome); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, codexHome); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	release, err := spec.Acquire(ctx, localdaemon.Allocation{Allowed: spec.Sources})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for range 2 {
		if err := spec.Run(ctx, func(localdaemon.Progress) {}); err != nil {
			t.Fatal(err)
		}
	}
	out := logged.String()
	if got := strings.Count(out, "explicit codex_home is unavailable"); got != 1 {
		t.Fatalf("the unavailable home was reported %d times, want once: %q", got, out)
	}
	if strings.Contains(out, "1 home(s)") || strings.Contains(out, codexHome) || strings.Contains(out, elsewhere) {
		t.Fatalf("the Codex pass scanned or named the home: %q", out)
	}
}

// What decides who may take a source over, and whether a profile with nothing to
// upload still runs, comes from the profile's own settings.
func TestProfileJobSpecCarriesTheSettingsThatDecideASourcesNextOwner(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Options.CollectRepositoryPrefixes = []string{"host/org/"}
	p.Options.RedactUserPrompts = true
	p.Options.RedactToolDetails = true
	p.Options.ExcludeAccounts = []string{"a1", "codex:a2"}
	p.Options.LocalAnalysisEnabled = true
	p.User = profile.UserInfo{ID: "one", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
	if err := profile.SaveNamed(p, "one"); err != nil {
		t.Fatal(err)
	}
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load("one")
	if err != nil {
		t.Fatal(err)
	}
	spec := profileJobSpec(entry, daemonOverrides{})
	want := localdaemon.Filters{RepoPrefixes: []string{"host/org/"}, RedactPrompts: true, RedactTools: true, ExcludeAccounts: []string{"a1", "codex:a2"}}
	if !reflect.DeepEqual(spec.Filters, want) || !spec.LocalAnalysis {
		t.Fatalf("filters %+v local %v, want %+v and local analysis", spec.Filters, spec.LocalAnalysis, want)
	}
	// The spec owns copies: a later edit of the profile does not reach into it.
	entry.Profile.Options.CollectRepositoryPrefixes[0] = "changed"
	if spec.Filters.RepoPrefixes[0] != "host/org/" {
		t.Fatal("the spec shares the profile's slices")
	}
}
