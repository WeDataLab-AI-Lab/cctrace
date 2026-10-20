package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"cctrace/internal/envgen"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
	"cctrace/internal/store"
)

func TestServerPolicySnapshotRevisionGuard(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "unknown_field_changed"}[changed], func(t *testing.T) {
			useTempSyncHome(t)
			p := profile.NewDefault()
			p.Server.SyncEndpoint = "http://cctrace.company.example:4318"
			p.Server.Endpoint = "http://cctrace.company.example:4317"
			p.Server.AuthToken = "fixture-token"
			p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
			p.Options.CodexSyncDeclined = true
			if err := profile.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := envgen.ApplyToClaudeSettings(p); err != nil {
				t.Fatal(err)
			}
			if changed {
				data, err := os.ReadFile(profile.DefaultPath())
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]json.RawMessage
				if err := json.Unmarshal(data, &doc); err != nil {
					t.Fatal(err)
				}
				doc["future_setting"] = json.RawMessage(`{"enabled":true}`)
				data, err = json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(profile.DefaultPath(), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ca, err := os.ReadFile(writeTestCAFile(t, t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			pol := serverpolicy.Policy{Sequence: 1, CAPEM: string(ca), To: serverpolicy.Endpoints{Sync: "https://cctrace.company.example:8443", OTEL: "https://cctrace.company.example:4318", Protocol: "http/protobuf"}}
			_, _, err = planServerPolicy(p, "", pol, "digest", serverPolicyState{})
			if changed {
				if err == nil || !strings.Contains(err.Error(), "profile changed") {
					t.Fatalf("concurrent unknown field change accepted: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unchanged saved profile rejected: %v", err)
			}
		})
	}
}

func TestServerPolicyPlanPreservesUserCA(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.Server.SyncEndpoint, p.Server.Endpoint = "http://cctrace.company.example:4318", "http://cctrace.company.example:4317"
	p.Server.AuthToken = "fixture-token"
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Options.CodexSyncDeclined = true
	p.Server.CACertFile = writeTestCAFile(t, t.TempDir())
	oldCA, _ := os.ReadFile(p.Server.CACertFile)
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		t.Fatal(err)
	}
	newCAPath := writeTestCAFile(t, t.TempDir())
	newCA, _ := os.ReadFile(newCAPath)
	pol := serverpolicy.Policy{Sequence: 1, CAPEM: string(newCA), To: serverpolicy.Endpoints{Sync: "https://cctrace.company.example:8443", OTEL: "https://cctrace.company.example:4318", Protocol: "http/protobuf"}}
	changes, state, err := planServerPolicy(p, "", pol, "digest", serverPolicyState{})
	if err != nil {
		t.Fatal(err)
	}
	if state.UserCAFile != p.Server.CACertFile {
		t.Fatal("lost user CA provenance")
	}
	found := false
	for _, c := range changes {
		if c.Path == state.ManagedCA {
			found = true
			if !strings.Contains(string(c.After), string(oldCA)) || !strings.Contains(string(c.After), string(newCA)) {
				t.Fatal("managed trust lost an existing CA")
			}
		}
		if c.Path == p.Server.CACertFile {
			t.Fatal("planner overwrites user CA file")
		}
	}
	if !found {
		t.Fatal("no staged CA")
	}
	stored, _ := profile.Load()
	if stored.Server != p.Server {
		t.Fatal("planning changed the profile")
	}
}

func TestServerPolicyPlanDefersSharedClaudeHome(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Options.CodexSyncDeclined = true
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := profile.SaveNamed(p, "other"); err != nil {
		t.Fatal(err)
	}
	_, _, err := planServerPolicy(p, "", serverpolicy.Policy{}, "", serverPolicyState{})
	const want = `shared settings with profiles "other" (claude); policy deferred`
	if err == nil || err.Error() != want {
		t.Fatalf("shared settings: got %v, want %q", err, want)
	}
}

// saveSharingProfile saves a named profile with the given Claude home and extra
// Codex folders. Every profile that leaves Codex sync on scans the temp home's
// default Codex folder, so two of them share its config.toml.
func saveSharingProfile(t *testing.T, name, claudeDir string, codexSync bool, codexDirs ...string) {
	t.Helper()
	p := profile.NewDefault()
	p.ClaudeConfigDir = claudeDir
	p.Options.CodexSyncDeclined = !codexSync
	p.Options.CodexDirs = codexDirs
	if err := profile.SaveNamed(p, name); err != nil {
		t.Fatal(err)
	}
}

// A profile that stops at the first shared settings file makes its owner fix one
// profile, run again, and meet the next. The reason names every profile the
// deferral waits on, by name and kind, so one pass through them is enough. A
// Codex config in the default folder is not one of them: every profile writes it.
// Not parallel: useTempSyncHome sets HOME with t.Setenv.
func TestServerPolicyDeferralListsAllSharedProfiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		// extra is a Codex folder the planned profile lists besides the default one.
		extra string
		setup func(t *testing.T, claude, extra string)
		want  string
	}{
		{
			// Created out of name order; the kinds interleave in the output.
			name:  "one Claude home and two Codex homes",
			extra: "codex-extra",
			setup: func(t *testing.T, claude, extra string) {
				saveSharingProfile(t, "mid", filepath.Join(t.TempDir(), "mid"), true, extra)
				saveSharingProfile(t, "zeta", claude, false)
				saveSharingProfile(t, "alpha", filepath.Join(t.TempDir(), "alpha"), true, extra)
			},
			want: `shared settings with profiles "alpha" (codex), "mid" (codex), "zeta" (claude); policy deferred`,
		},
		{
			name:  "one profile sharing both",
			extra: "codex-extra",
			setup: func(t *testing.T, claude, extra string) {
				saveSharingProfile(t, "other", claude, true, extra)
			},
			want: `shared settings with profiles "other" (claude), "other" (codex); policy deferred`,
		},
		{
			name: "the default Codex folder is not listed",
			setup: func(t *testing.T, claude, _ string) {
				saveSharingProfile(t, "mid", filepath.Join(t.TempDir(), "mid"), true)
				saveSharingProfile(t, "zeta", claude, false)
				saveSharingProfile(t, "alpha", filepath.Join(t.TempDir(), "alpha"), true)
			},
			want: `shared settings with profiles "zeta" (claude); policy deferred`,
		},
		{
			name: "one profile sharing Claude and the default Codex folder",
			setup: func(t *testing.T, claude, _ string) {
				saveSharingProfile(t, "other", claude, true)
			},
			want: `shared settings with profiles "other" (claude); policy deferred`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := useTempSyncHome(t)
			if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			claude := filepath.Join(t.TempDir(), "claude")
			p := profile.NewDefault()
			p.ClaudeConfigDir = claude
			var extra string
			if tc.extra != "" {
				extra = filepath.Join(home, tc.extra)
				if err := os.MkdirAll(extra, 0o700); err != nil {
					t.Fatal(err)
				}
				p.Options.CodexDirs = []string{extra}
			}
			tc.setup(t, claude, extra)

			_, err := ensureExclusivePolicySettings(p, "work", serverpolicy.Policy{})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if got := policyCheckResult(err); got != store.ClientPolicyCheckDeferred {
				t.Fatalf("reported as %q, want %q", got, store.ClientPolicyCheckDeferred)
			}
		})
	}
}

// The server and the client both cut a reason at ClientPolicyReasonMax runes. The
// result is classified before the cut, so a reason that grew with the profile
// count would still read as deferred, but the operator would see it without its
// suffix and without the last profiles.
// Not parallel: useTempSyncHome sets HOME with t.Setenv.
func TestServerPolicyDeferralReasonBounded(t *testing.T) {
	const profiles = 40
	home := useTempSyncHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(t.TempDir(), "claude")
	for i := 0; i < profiles; i++ {
		saveSharingProfile(t, fmt.Sprintf("profile-%02d-with-a-long-name", i), claude, true)
	}
	p := profile.NewDefault()
	p.ClaudeConfigDir = claude

	_, err := ensureExclusivePolicySettings(p, "work", serverpolicy.Policy{})
	if err == nil {
		t.Fatal("shared Claude settings accepted")
	}
	reason := err.Error()
	if n := utf8.RuneCountInString(reason); n > store.ClientPolicyReasonMax {
		t.Errorf("reason is %d runes, over the %d the report keeps", n, store.ClientPolicyReasonMax)
	}
	if !strings.HasSuffix(reason, policyDeferredSuffix) {
		t.Errorf("reason %q does not end with %q", reason, policyDeferredSuffix)
	}
	if got := cutPolicyReason(reason); got != reason {
		t.Errorf("the report cuts the reason:\n got %q\nwant %q", got, reason)
	}
	listed := strings.Count(reason, " (claude)")
	if listed == 0 || listed >= profiles {
		t.Fatalf("%d of %d profiles listed; the fixture no longer overflows the reason", listed, profiles)
	}
	if strings.Contains(reason, "(codex)") {
		t.Errorf("reason %q lists the default Codex folder", reason)
	}
	if want := fmt.Sprintf(" and %d more; %s", profiles-listed, policyDeferredSuffix); !strings.HasSuffix(reason, want) {
		t.Errorf("reason %q does not end with the count of the unlisted: %q", reason, want)
	}
	// It names as many as fit: one more would overflow.
	var named []string
	for i := 0; i <= listed && i < profiles; i++ {
		named = append(named, fmt.Sprintf(`"profile-%02d-with-a-long-name" (claude)`, i))
	}
	longer := fmt.Sprintf("shared settings with profiles %s and %d more; %s", strings.Join(named, ", "), profiles-len(named), policyDeferredSuffix)
	if n := utf8.RuneCountInString(longer); n <= store.ClientPolicyReasonMax {
		t.Errorf("naming %d profiles takes %d runes and fits, but the reason names %d", len(named), n, listed)
	}
	if !strings.HasPrefix(reason, `shared settings with profiles "profile-00-with-a-long-name" (claude), "profile-01-with-a-long-name" (claude)`) {
		t.Errorf("reason %q does not list the profiles in name order", reason)
	}
}

// unreadableProfiles saves a named profile whose files the check cannot read.
var unreadableProfiles = []struct {
	name string
	save func(t *testing.T, name string)
}{
	{"symlinked Claude settings", func(t *testing.T, name string) {
		claude := filepath.Join(t.TempDir(), "claude")
		if err := os.MkdirAll(claude, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(claude, "settings.json")); err != nil {
			t.Fatal(err)
		}
		saveSharingProfile(t, name, claude, false)
	}},
	{"malformed profile.json", func(t *testing.T, name string) {
		saveSharingProfile(t, name, filepath.Join(t.TempDir(), "claude"), false)
		path, err := profile.NamedPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}},
}

// A profile the check cannot read must not cost the owner the conflicts found
// among the others: the run would end as a failure, which reads as something to
// retry, when what it waits on is a person.
func TestServerPolicyDeferralKeepsConflictsWhenAnotherProfileCannotBeRead(t *testing.T) {
	for _, bad := range unreadableProfiles {
		t.Run(bad.name, func(t *testing.T) {
			useTempSyncHome(t)
			claude := filepath.Join(t.TempDir(), "claude")
			p := profile.NewDefault()
			p.ClaudeConfigDir = claude
			def := profile.NewDefault()
			def.ClaudeConfigDir = claude
			def.Options.CodexSyncDeclined = true
			if err := profile.Save(def); err != nil {
				t.Fatal(err)
			}
			// One unreadable profile on each side of the conflicts.
			bad.save(t, "alpha")
			saveSharingProfile(t, "mid", claude, false)
			bad.save(t, "zeta")

			_, err := ensureExclusivePolicySettings(p, "work", serverpolicy.Policy{})
			const want = `shared settings with profiles "" (claude), "mid" (claude); policy deferred`
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	}
}

// With no conflict to report, the profile that cannot be read is what the run
// fails on.
func TestServerPolicyDeferralReportsUnreadableProfileWithoutConflict(t *testing.T) {
	for _, bad := range unreadableProfiles {
		t.Run(bad.name, func(t *testing.T) {
			useTempSyncHome(t)
			p := profile.NewDefault()
			p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
			bad.save(t, "zeta")

			_, err := ensureExclusivePolicySettings(p, "work", serverpolicy.Policy{})
			if err == nil {
				t.Fatal("an unreadable profile was passed over")
			}
			if got := policyCheckResult(err); got != store.ClientPolicyCheckFailed {
				t.Fatalf("%v reported as %q, want %q", err, got, store.ClientPolicyCheckFailed)
			}
		})
	}
}

// A profile that does not sync Codex shares no Codex file with anyone, so the
// other profiles' Codex files are not read for it: a link there is no reason to
// fail a deferral about the Claude home.
func TestServerPolicyDeferralSkipsOtherCodexFilesWhenCodexSyncIsOff(t *testing.T) {
	home := useTempSyncHome(t)
	codex := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codex, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(codex, "config.toml")); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(t.TempDir(), "claude")
	def := profile.NewDefault()
	def.ClaudeConfigDir = claude
	if err := profile.Save(def); err != nil {
		t.Fatal(err)
	}
	p := profile.NewDefault()
	p.ClaudeConfigDir = claude
	p.Options.CodexSyncDeclined = true

	_, err := ensureExclusivePolicySettings(p, "work", serverpolicy.Policy{})
	const want = `shared settings with profiles "" (claude); policy deferred`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestServerPolicyPlanDoesNotRestoreClearedUserCA(t *testing.T) {
	pol, p := policyStateFixture(t, false)
	// A user explicitly cleared server.ca_cert_file after a previous policy.
	// The previous provenance must not resurrect that removed trust root.
	state := serverPolicyState{Sequence: 1, ManagedCA: filepath.Join(t.TempDir(), "previous-managed.pem"), UserCAFile: writeTestCAFile(t, t.TempDir())}
	changes, next, err := planServerPolicy(p, "", pol, "digest", state)
	if err != nil {
		t.Fatal(err)
	}
	if next.UserCAFile != "" {
		t.Fatal("restored user CA that was explicitly cleared")
	}
	for _, change := range changes {
		if change.Path == next.ManagedCA && string(change.After) != pol.CAPEM {
			t.Fatal("new managed bundle includes cleared user trust")
		}
	}
}

func TestServerPolicySequenceDecision(t *testing.T) {
	t.Parallel()
	to := serverpolicy.Endpoints{Sync: "https://cctrace.company.example:8443", OTEL: "https://cctrace.company.example:4318", Protocol: "http/protobuf"}
	state := serverPolicyState{Sequence: 4, Digest: "accepted"}
	for _, test := range []struct {
		seq                  uint64
		digest               string
		wantErr, wantApplied bool
	}{
		{3, "old", true, false}, {4, "different", true, false}, {4, "accepted", false, true}, {5, "new", false, false},
	} {
		applied, err := serverPolicySequence(serverpolicy.Policy{Sequence: test.seq, To: to}, test.digest, state, to)
		if (err != nil) != test.wantErr || applied != test.wantApplied {
			t.Fatalf("seq %d got %v,%v", test.seq, applied, err)
		}
	}
	if _, err := serverPolicySequence(serverpolicy.Policy{Sequence: 4, To: to}, "accepted", state, serverpolicy.Endpoints{}); err == nil {
		t.Fatal("same policy overwrites manual endpoint changes")
	}
}

func TestServerPolicyNamedProfileWithoutDefault(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Options.CodexSyncDeclined = true
	if err := profile.SaveNamed(p, "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureExclusivePolicySettings(p, "work", serverpolicy.Policy{}); err != nil {
		t.Fatal(err)
	}
}

func TestServerPolicyPlanRejectsProfileEditedDuringFetch(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.Options.CodexSyncDeclined = true
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	changed := *p
	changed.User.Name = "concurrent user edit"
	if err := profile.Save(&changed); err != nil {
		t.Fatal(err)
	}
	_, _, err := planServerPolicy(p, "", serverpolicy.Policy{Sequence: 1}, "digest", serverPolicyState{})
	if err == nil || !strings.Contains(err.Error(), "profile changed") {
		t.Fatalf("accepted stale profile: %v", err)
	}
}

func TestServerPolicyPlanRejectsEditedManagedCAOnNewSequence(t *testing.T) {
	useTempSyncHome(t)
	p := profile.NewDefault()
	p.Options.CodexSyncDeclined = true
	p.ClaudeConfigDir = filepath.Join(t.TempDir(), "claude")
	p.Server.CACertFile = writeTestCAFile(t, t.TempDir())
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	state := serverPolicyState{Sequence: 1, ManagedCA: p.Server.CACertFile, ManagedCASHA256: "original-digest"}
	_, _, err := planServerPolicy(p, "", serverpolicy.Policy{Sequence: 2}, "new", state)
	if err == nil || !strings.Contains(err.Error(), "managed CA") {
		t.Fatalf("discarded user CA edit: %v", err)
	}
}
