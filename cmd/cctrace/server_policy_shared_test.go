package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/codexconfig"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
)

// HOME, the sync locks and the profile store are process-wide, so none of
// these tests run in parallel.

var (
	sharedPolicyFrom = serverpolicy.Endpoints{Sync: "http://localhost:4318", OTEL: "http://localhost:4317", Protocol: "grpc"}
	sharedPolicyTo   = serverpolicy.Endpoints{Sync: "https://localhost:8443", OTEL: "https://localhost:4318", Protocol: "http/protobuf"}
	// A daemon record with what a handover needs.
	liveSharedDaemon = syncRuntime{Interval: time.Minute}
)

type sharedCodexFixture struct {
	name     string // the profile the policy is planned for
	sibling  string // the profile sharing its Codex config
	a        *profile.Profile
	pol      serverpolicy.Policy
	codexDir string
	lock     *flock.Flock // the sibling's sync lock, held as its live daemon would
}

// newSharedCodexFixture builds the case the sibling rule lets through: the
// default profile and "home" share the default Codex home, the policy targets
// both from the endpoints they use, "home" has a live sync daemon, and the
// shared config holds the default profile's block. Each test breaks one part.
func newSharedCodexFixture(t *testing.T) *sharedCodexFixture {
	return newSharedCodexFixtureFor(t, "", "home")
}

func newSharedCodexFixtureFor(t *testing.T, name, sibling string) *sharedCodexFixture {
	t.Helper()
	home := useTempSyncHome(t)
	f := &sharedCodexFixture{
		name:     name,
		sibling:  sibling,
		codexDir: filepath.Join(home, ".codex"),
		pol:      serverpolicy.Policy{Sequence: 3, CASHA256: "fixture", From: []serverpolicy.Endpoints{sharedPolicyFrom}, To: sharedPolicyTo},
	}
	if err := os.MkdirAll(f.codexDir, 0700); err != nil {
		t.Fatal(err)
	}
	f.a = f.addSibling(t, name, sharedPolicyFrom)
	if err := envgen.ApplyToClaudeSettings(f.a); err != nil {
		t.Fatal(err)
	}
	f.addSibling(t, sibling, sharedPolicyFrom)
	f.lock = startSharedPolicyDaemon(t, sibling, liveSharedDaemon)
	f.writeBlock(t, f.a.Server.Endpoint, f.a.Server.AuthToken, "")
	return f
}

func saveSharedPolicyProfile(t *testing.T, name string, at serverpolicy.Endpoints) *profile.Profile {
	t.Helper()
	p := profile.NewDefault()
	p.Server.SyncEndpoint, p.Server.Endpoint, p.Server.Protocol = at.Sync, at.OTEL, at.Protocol
	p.Server.AuthToken = "fixture-token-" + name
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	if err := saveProfile(p, name); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *sharedCodexFixture) target(t *testing.T, name string) {
	t.Helper()
	target, err := serverPolicyTarget(name)
	if err != nil {
		t.Fatal(err)
	}
	f.pol.Targets = append(f.pol.Targets, target)
}

// addSibling saves a targeted profile writing the shared Codex config.
func (f *sharedCodexFixture) addSibling(t *testing.T, name string, at serverpolicy.Endpoints) *profile.Profile {
	t.Helper()
	p := saveSharedPolicyProfile(t, name, at)
	f.target(t, name)
	return p
}

// editSibling changes a saved profile.
func editSibling(t *testing.T, name string, edit func(*profile.Profile)) {
	t.Helper()
	p, err := loadProfile(name)
	if err != nil {
		t.Fatal(err)
	}
	edit(p)
	if err := saveProfile(p, name); err != nil {
		t.Fatal(err)
	}
}

func moveSibling(t *testing.T, name string, at serverpolicy.Endpoints) {
	t.Helper()
	editSibling(t, name, func(p *profile.Profile) {
		p.Server.SyncEndpoint, p.Server.Endpoint, p.Server.Protocol = at.Sync, at.OTEL, at.Protocol
	})
}

func (f *sharedCodexFixture) writeBlock(t *testing.T, endpoint, token, ca string) {
	t.Helper()
	f.writeBlockIn(t, f.codexDir, endpoint, token, ca)
}

func (f *sharedCodexFixture) writeBlockIn(t *testing.T, dir, endpoint, token, ca string) {
	t.Helper()
	if err := codexconfig.WriteOtelBlock(dir, endpoint, token, ca); err != nil {
		t.Fatal(err)
	}
}

func (f *sharedCodexFixture) caPath(t *testing.T, name string) string {
	t.Helper()
	path, err := policyCAPath(name, f.pol)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// startSharedPolicyDaemon records rt as a watcher would and holds the sync lock
// for the rest of the test.
func startSharedPolicyDaemon(t *testing.T, name string, rt syncRuntime) *flock.Flock {
	t.Helper()
	rt.PID, rt.InstanceID = os.Getpid(), "fixture"
	// Not atomic, unlike a watcher's: nothing reads it concurrently here.
	b, err := json.Marshal(rt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFilePath(name), b, 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireSyncLock(name, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	return lock
}

func (f *sharedCodexFixture) plan() error {
	_, _, err := planServerPolicy(f.a, f.name, f.pol, "digest", serverPolicyState{})
	return err
}

// useExtraFolder moves the shared config out of the default Codex folder: every
// profile lists a folder of its own instead, the default folder is gone, and the
// config there holds the planned profile's block. The sibling rule is strict there.
func (f *sharedCodexFixture) useExtraFolder(t *testing.T) {
	t.Helper()
	extra := filepath.Join(t.TempDir(), "codex-extra")
	if err := os.MkdirAll(extra, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.codexDir); err != nil {
		t.Fatal(err)
	}
	names, err := profile.ListNamed()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range append([]string{""}, names...) {
		editSibling(t, name, func(p *profile.Profile) { p.Options.CodexDirs = []string{extra} })
	}
	if f.a, err = loadProfile(f.name); err != nil {
		t.Fatal(err)
	}
	f.codexDir = extra
	f.writeBlock(t, f.a.Server.Endpoint, f.a.Server.AuthToken, "")
}

// config is the shared config.toml, canonical.
func (f *sharedCodexFixture) config(t *testing.T) string {
	t.Helper()
	path, err := canonicalPolicyPath(filepath.Join(f.codexDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// planOK plans the policy and checks that it goes ahead without touching the
// shared config, in or out of the transaction, and returns the state it commits.
func (f *sharedCodexFixture) planOK(t *testing.T) serverPolicyState {
	t.Helper()
	config := f.config(t)
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	changes, state, err := planServerPolicy(f.a, f.name, f.pol, "digest", serverPolicyState{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, c := range changes {
		if c.Path == config {
			t.Fatal("shared Codex config planned inside this profile's transaction")
		}
	}
	if after, err := os.ReadFile(config); err != nil || string(after) != string(before) {
		t.Fatalf("planning wrote the shared config: %v", err)
	}
	return state
}

// wantMove checks the planned state records the profile's move of the shared
// config to its new block, carried out after the commit, with no note.
func (f *sharedCodexFixture) wantMove(t *testing.T, state serverPolicyState) {
	t.Helper()
	if len(state.SharedWrites) != 1 || state.SharedWrites[0].Path != f.config(t) || state.Reason != "" {
		t.Fatalf("shared writes %+v, reason %q; want the move recorded and no note", state.SharedWrites, state.Reason)
	}
}

// wantKept checks the planned state leaves the shared config as it is, says so,
// and still moves the profile.
func (f *sharedCodexFixture) wantKept(t *testing.T, state serverPolicyState) {
	t.Helper()
	want := "default Codex config kept: " + f.config(t)
	n := len(state.History)
	if len(state.SharedWrites) != 0 || state.Status != "applied" || state.Reason != want || n == 0 || state.History[n-1].Status != "applied" || state.History[n-1].Reason != want {
		t.Fatalf("state %+v; want no shared write, applied, and the note %q", state, want)
	}
}

// checkUnmovedSibling runs setup, which makes the sibling "home" one that does not
// move with this profile, on a case sharing the default Codex folder and on one
// sharing only an extra folder. In the default folder the profile moves anyway and
// its block there is carried over after the commit; elsewhere the policy defers.
func checkUnmovedSibling(t *testing.T, setup func(*testing.T, *sharedCodexFixture)) {
	t.Helper()
	t.Run("default_folder", func(t *testing.T) {
		f := newSharedCodexFixture(t)
		setup(t, f)
		f.wantMove(t, f.planOK(t))
	})
	t.Run("extra_folder", func(t *testing.T) {
		f := newSharedCodexFixture(t)
		f.useExtraFolder(t)
		setup(t, f)
		wantSharedDeferral(t, f.plan(), "home", "codex")
	})
}

// wantSharedDeferral checks the reason names exactly these conflicts, given as
// profile and kind pairs.
func wantSharedDeferral(t *testing.T, err error, conflicts ...string) {
	t.Helper()
	var items []string
	for i := 0; i < len(conflicts); i += 2 {
		items = append(items, fmt.Sprintf("%q (%s)", conflicts[i], conflicts[i+1]))
	}
	want := "shared settings with profiles " + strings.Join(items, ", ") + "; policy deferred"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestSharedPolicyTargetedSiblingsProceed(t *testing.T) {
	f := newSharedCodexFixture(t)
	changes, _, err := planServerPolicy(f.a, "", f.pol, "digest", serverPolicyState{})
	if err != nil {
		t.Fatal(err)
	}
	config, err := canonicalPolicyPath(filepath.Join(f.codexDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// Written after commit, not inside this profile's transaction.
	for _, c := range changes {
		if c.Path == config {
			t.Fatal("shared Codex config planned inside this profile's transaction")
		}
	}
	// Every other block a profile moving under this policy may have left there.
	for _, block := range []struct{ name, endpoint, token, ca string }{
		{"own_new", sharedPolicyTo.OTEL, f.a.Server.AuthToken, f.caPath(t, "")},
		{"sibling_old", sharedPolicyFrom.OTEL, "fixture-token-home", ""},
		{"sibling_new", sharedPolicyTo.OTEL, "fixture-token-home", f.caPath(t, "home")},
	} {
		f.writeBlock(t, block.endpoint, block.token, block.ca)
		if _, err := ensureExclusivePolicySettings(f.a, "", f.pol); err != nil {
			t.Errorf("%s: %v", block.name, err)
		}
	}
}

// A sibling already at the destination writes the destination's block from its
// own sessions, so it needs neither a daemon nor a place among this policy's
// targets: a later policy leaves applied profiles out of them.
func TestSharedPolicySiblingAtDestinationProceeds(t *testing.T) {
	f := newSharedCodexFixture(t)
	workCA := filepath.Join(t.TempDir(), "earlier-policy.pem")
	work := saveSharedPolicyProfile(t, "work", sharedPolicyTo)
	work.Server.CACertFile = workCA
	if err := saveProfile(work, "work"); err != nil {
		t.Fatal(err)
	}
	if err := f.plan(); err != nil {
		t.Fatal(err)
	}
	f.writeBlock(t, sharedPolicyTo.OTEL, work.Server.AuthToken, workCA)
	if _, err := ensureExclusivePolicySettings(f.a, "", f.pol); err != nil {
		t.Fatalf("block of the sibling at the destination: %v", err)
	}
}

func TestSharedPolicyUntargetedSibling(t *testing.T) {
	checkUnmovedSibling(t, func(t *testing.T, f *sharedCodexFixture) {
		f.pol.Targets = f.pol.Targets[:1]
		// A sibling that does move must not be named with the one that does not.
		f.addSibling(t, "alpha", sharedPolicyFrom)
		startSharedPolicyDaemon(t, "alpha", liveSharedDaemon)
	})
}

func TestSharedPolicyOtherServerSibling(t *testing.T) {
	checkUnmovedSibling(t, func(t *testing.T, f *sharedCodexFixture) {
		moveSibling(t, "home", serverpolicy.Endpoints{Sync: "http://trace.example.test:4318", OTEL: "http://trace.example.test:4317", Protocol: "grpc"})
	})
}

// The policy names exact endpoints. Another spelling of the same server is not
// one of them, so that sibling would keep sending where it does now.
func TestSharedPolicySameHostVariantSibling(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*serverpolicy.Endpoints)
	}{
		{"ip_spelling", func(e *serverpolicy.Endpoints) { e.OTEL = "http://127.0.0.1:4317" }},
		{"other_port", func(e *serverpolicy.Endpoints) { e.OTEL = "http://localhost:14317" }},
		{"other_protocol", func(e *serverpolicy.Endpoints) { e.Protocol = "http/protobuf" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkUnmovedSibling(t, func(t *testing.T, _ *sharedCodexFixture) {
				at := sharedPolicyFrom
				tc.mutate(&at)
				moveSibling(t, "home", at)
			})
		})
	}
}

// Only a running daemon that can take the policy over applies it for its
// profile. Without one the sibling's next session rewrites the shared block to
// its old server. A record that cannot be read counts the same: the sibling does
// not move, whatever it is that the owner then sees.
func TestSharedPolicySiblingWithoutLiveSync(t *testing.T) {
	record := func(rt syncRuntime) func(*testing.T, *sharedCodexFixture) {
		return func(t *testing.T, _ *sharedCodexFixture) {
			rt.PID = os.Getpid()
			if err := writeJSONFile(runtimeFilePath("home"), rt); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name string
		stop func(*testing.T, *sharedCodexFixture)
	}{
		{"no_record", func(t *testing.T, _ *sharedCodexFixture) {
			if err := os.Remove(runtimeFilePath("home")); err != nil {
				t.Fatal(err)
			}
		}},
		// The record outlives a crashed daemon; only the lock proves it alive.
		{"stale_record", func(t *testing.T, f *sharedCodexFixture) {
			if err := f.lock.Unlock(); err != nil {
				t.Fatal(err)
			}
		}},
		{"unreadable_record", func(t *testing.T, _ *sharedCodexFixture) {
			if err := os.Remove(runtimeFilePath("home")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(runtimeFilePath("home"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"no_restart_metadata", record(syncRuntime{})},
		{"local", record(syncRuntime{Interval: time.Minute, Local: true})},
		{"endpoint_override", record(syncRuntime{Interval: time.Minute, EndpointOverride: sharedPolicyFrom.OTEL})},
		{"other_claude_dir", func(t *testing.T, f *sharedCodexFixture) {
			record(syncRuntime{Interval: time.Minute, ClaudeDir: t.TempDir()})(t, f)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkUnmovedSibling(t, tc.stop)
		})
	}
}

// A block that is not any moving profile's was edited by hand. In the default
// Codex folder it is left as it is and the profile moves anyway: every profile
// rewrites that file from its own sessions, so it is never this policy's alone.
// Elsewhere the file belongs to the profiles that list the folder, and the policy
// waits on whoever edited it.
func TestSharedPolicyCustomizedBlock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(*testing.T, *sharedCodexFixture)
	}{
		{"extra_option", func(t *testing.T, f *sharedCodexFixture) {
			path := filepath.Join(f.codexDir, "config.toml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Replace(string(data), "[otel]", "[otel]\nlog_user_prompt = true", 1)), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown_server", func(t *testing.T, f *sharedCodexFixture) {
			f.writeBlock(t, "http://trace.example.test:4317", f.a.Server.AuthToken, "")
		}},
		{"unknown_token", func(t *testing.T, f *sharedCodexFixture) {
			f.writeBlock(t, sharedPolicyFrom.OTEL, "fixture-token-other", "")
		}},
	} {
		t.Run(tc.name+"/default_folder", func(t *testing.T) {
			f := newSharedCodexFixture(t)
			tc.write(t, f)
			f.wantKept(t, f.planOK(t))
		})
		t.Run(tc.name+"/extra_folder", func(t *testing.T) {
			f := newSharedCodexFixture(t)
			f.useExtraFolder(t)
			tc.write(t, f)
			if err := f.plan(); err == nil || err.Error() != "shared Codex settings edited; policy deferred" {
				t.Fatalf("customized shared block: %v", err)
			}
		})
	}
}

// cctrace writes single-line blocks only, so a multi-line root otel key is nobody's
// block. In the default Codex folder it is left alone like any other content; in
// another folder the plan fails on it, as it does for a profile that shares nothing.
func TestSharedPolicyMultilineRootOtelKey(t *testing.T) {
	const multiline = "otel.exporter = \"\"\"\nx\n\"\"\"\n"
	write := func(t *testing.T, f *sharedCodexFixture) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(f.codexDir, "config.toml"), []byte(multiline), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("default_folder", func(t *testing.T) {
		f := newSharedCodexFixture(t)
		write(t, f)
		f.wantKept(t, f.planOK(t))
	})
	t.Run("extra_folder", func(t *testing.T) {
		f := newSharedCodexFixture(t)
		f.useExtraFolder(t)
		write(t, f)
		if err := f.plan(); !errors.Is(err, codexconfig.ErrMultilineRootOtelKey) {
			t.Fatalf("plan: %v; want the multi-line key to fail it", err)
		}
	})
}

// What the default Codex folder holds decides what the profile does with it. Only
// its own old block is rewritten; a block that is any other's is left alone and
// said so, and the profile moves in every case.
func TestSharedPolicyDefaultFolderByContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		// unmoved makes the sibling "home" one that does not move.
		unmoved bool
		write   func(*testing.T, *sharedCodexFixture)
		kept    bool
	}{
		{"own_old_block", false, func(*testing.T, *sharedCodexFixture) {}, false},
		{"own_new_block", false, func(t *testing.T, f *sharedCodexFixture) {
			f.writeBlock(t, sharedPolicyTo.OTEL, f.a.Server.AuthToken, f.caPath(t, f.name))
		}, false},
		{"moving_sibling_old_block", false, func(t *testing.T, f *sharedCodexFixture) {
			f.writeBlock(t, sharedPolicyFrom.OTEL, "fixture-token-home", "")
		}, false},
		{"moving_sibling_new_block", false, func(t *testing.T, f *sharedCodexFixture) {
			f.writeBlock(t, sharedPolicyTo.OTEL, "fixture-token-home", f.caPath(t, "home"))
		}, false},
		{"unmoved_sibling_block", true, func(t *testing.T, f *sharedCodexFixture) {
			f.writeBlock(t, sharedPolicyFrom.OTEL, "fixture-token-home", "")
		}, true},
		{"unmoved_sibling_own_old_block", true, func(*testing.T, *sharedCodexFixture) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedCodexFixture(t)
			if tc.unmoved {
				f.pol.Targets = f.pol.Targets[:1]
			}
			tc.write(t, f)
			state := f.planOK(t)
			if tc.kept {
				f.wantKept(t, state)
			} else {
				f.wantMove(t, state)
			}
		})
	}
}

// Relaxing Claude would race on all of settings.json and compare token headers,
// so a shared Claude home defers even when the sibling moves too.
func TestSharedClaudeSettingsAlwaysDefers(t *testing.T) {
	f := newSharedCodexFixture(t)
	editSibling(t, "home", func(p *profile.Profile) { p.ClaudeConfigDir = f.a.ClaudeConfigDir })
	wantSharedDeferral(t, f.plan(), "home", "claude")
}

// The default profile is a sibling like any other when the policy is for a
// named one.
func TestSharedPolicyNamedProfileWithDefaultSibling(t *testing.T) {
	f := newSharedCodexFixtureFor(t, "work", "")
	if err := f.plan(); err != nil {
		t.Fatal(err)
	}
	f.pol.Targets = f.pol.Targets[:1]
	f.wantMove(t, f.planOK(t))

	f = newSharedCodexFixtureFor(t, "work", "")
	f.useExtraFolder(t)
	f.pol.Targets = f.pol.Targets[:1]
	wantSharedDeferral(t, f.plan(), "", "codex")
}

// Profiles sharing more than the default Codex home share every one of those
// configs. Each outside the default folder must hold a block of a moving profile;
// the default folder's block may be anything.
func TestSharedPolicyEveryCodexPathChecked(t *testing.T) {
	f := newSharedCodexFixture(t)
	extra := filepath.Join(t.TempDir(), "codex-extra")
	if err := os.MkdirAll(extra, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "home"} {
		editSibling(t, name, func(p *profile.Profile) { p.Options.CodexDirs = []string{extra} })
	}
	a, err := loadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	f.a = a
	f.writeBlockIn(t, extra, sharedPolicyFrom.OTEL, "fixture-token-home", "")
	if _, err := ensureExclusivePolicySettings(f.a, "", f.pol); err != nil {
		t.Fatalf("both shared configs hold moving blocks: %v", err)
	}
	f.writeBlockIn(t, extra, "http://trace.example.test:4317", "fixture-token-home", "")
	if _, err := ensureExclusivePolicySettings(f.a, "", f.pol); err == nil || err.Error() != "shared Codex settings edited; policy deferred" {
		t.Fatalf("edited second shared config: %v", err)
	}
	f.writeBlockIn(t, extra, sharedPolicyFrom.OTEL, "fixture-token-home", "")
	f.writeBlock(t, "http://trace.example.test:4317", "fixture-token-home", "")
	if _, err := ensureExclusivePolicySettings(f.a, "", f.pol); err != nil {
		t.Fatalf("edited default folder config: %v", err)
	}
}

// A policy for every profile on the server names each sibling too; the rest of
// the rule (endpoints the policy moves from, a daemon able to take it over)
// still holds.
func TestSharedPolicyAllTargetsCoverSiblings(t *testing.T) {
	f := newSharedCodexFixture(t)
	f.pol.Targets, f.pol.AllTargets = nil, true
	if err := f.plan(); err != nil {
		t.Fatalf("sibling under a policy for all targets: %v", err)
	}
	checkUnmovedSibling(t, func(t *testing.T, f *sharedCodexFixture) {
		f.pol.Targets, f.pol.AllTargets = nil, true
		if err := os.Remove(runtimeFilePath("home")); err != nil {
			t.Fatal(err)
		}
	})
}
