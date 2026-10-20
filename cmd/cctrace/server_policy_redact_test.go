package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/configtxn"
	"cctrace/internal/profile"
)

// HOME and the policy runtime seams are process-wide, so none of these tests run
// in parallel.

// explicitHomeScanned gives the default profile an explicit Codex home that sync
// scans too, as the desktop app sets one up (codex_home is the first of
// codex_dirs), and returns it.
func explicitHomeScanned(t *testing.T) string {
	t.Helper()
	home, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	editSibling(t, "", func(p *profile.Profile) {
		p.CodexHome, p.Options.CodexDirs = home, []string{home}
		p.Options.CodexSyncEnabled, p.Options.CodexSyncDeclined = true, false
	})
	return home
}

func TestRedactPolicyReasonMasksEveryProfilesExplicitCodexHome(t *testing.T) {
	useTempSyncHome(t)
	first, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A home inside another's is masked whole, not left half-named.
	inner := filepath.Join(first, "nested")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, home := range map[string]string{"": first, "work": second, "inner": inner} {
		p := profile.NewDefault()
		p.CodexHome = home
		if err := saveProfile(p, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, reason, want string }{
		{"default profile's home", "open " + filepath.Join(first, "config.toml") + ": denied", "open [explicit Codex home]/config.toml: denied"},
		{"named profile's home", "policy lock busy: " + filepath.Join(second, "config.toml.lock"), "policy lock busy: [explicit Codex home]/config.toml.lock"},
		{"nested home", filepath.Join(inner, "config.toml") + ": unmanaged", "[explicit Codex home]/config.toml: unmanaged"},
		{"both in one reason", first + " and " + second, "[explicit Codex home] and [explicit Codex home]"},
		{"home ends the reason", "no such directory " + first, "no such directory [explicit Codex home]"},
		{"home before a colon", first + ": denied", "[explicit Codex home]: denied"},
		// A directory whose name only starts with the home's is another directory.
		{"sibling directory sharing the prefix", "open " + first + "-work/config.toml", ""},
		{"home inside a longer name", "open mounted" + first, ""},
		{"quoted home", "\"" + first + "\"", "\"[explicit Codex home]\""},
		{"no path", "policy lock busy", "policy lock busy"},
		{"another path", "open " + filepath.Join(t.TempDir(), "settings.json"), ""},
	} {
		want := tc.want
		if want == "" {
			want = tc.reason
		}
		// The separator is the platform's; the mask is written with a slash.
		got := strings.ReplaceAll(redactPolicyReason(tc.reason), string(filepath.Separator), "/")
		want = strings.ReplaceAll(want, string(filepath.Separator), "/")
		if got != want {
			t.Errorf("%s: %q, want %q", tc.name, got, want)
		}
	}
}

func TestRedactPolicyReasonWithoutProfilesChangesNothing(t *testing.T) {
	useTempSyncHome(t)
	reason := "open " + filepath.Join(t.TempDir(), "config.toml")
	if got := redactPolicyReason(reason); got != reason {
		t.Fatalf("redacted %q with no profile on the machine", got)
	}
}

// A reason names the lock it waited for, and that lock is in the explicit home.
// The server's report must not carry the path.
func TestPolicyReportOmitsExplicitCodexHomeFromBusyLock(t *testing.T) {
	policyStateFixture(t, false)
	home := explicitHomeScanned(t)
	shortPolicyLockWait(t)
	lockPath, err := canonicalPolicyPath(filepath.Join(home, "config.toml.lock"))
	if err != nil {
		t.Fatal(err)
	}
	holdPolicyLock(t, lockPath)

	err = runPolicyWithinBound(t)

	wantPolicyLockBusy(t, err)
	if !strings.Contains(err.Error(), home) {
		t.Fatalf("the run's own error %q no longer names the lock; the check below proves nothing", err)
	}
	report, _, rerr := buildPolicyReport("", err)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(report.CheckReason, home) || !strings.HasPrefix(report.CheckReason, "policy lock busy: [explicit Codex home]") {
		t.Fatalf("report reason %q", report.CheckReason)
	}
}

// A failure is recorded in the state and reported. Neither carries the path, and
// the report still finds the record of the failure it describes, so it names the
// sequence that failed; a repeat of the failure is still recorded once.
func TestPolicyFailureRecordOmitsExplicitCodexHome(t *testing.T) {
	pol, _ := policyStateFixture(t, false)
	home := explicitHomeScanned(t)
	original := applyServerPolicyChangesFn
	t.Cleanup(func() { applyServerPolicyChangesFn = original })
	applyServerPolicyChangesFn = func(string, []configtxn.Change) error {
		return fmt.Errorf("write %s: disk full", filepath.Join(home, "config.toml"))
	}
	want := "write [explicit Codex home]" + string(filepath.Separator) + "config.toml: disk full"

	_, err := runServerPolicy(context.Background(), "", false)

	if err == nil || !strings.Contains(err.Error(), home) {
		t.Fatalf("run ended with %v, want the injected failure naming the home", err)
	}
	state, serr := readServerPolicyState("")
	if serr != nil {
		t.Fatal(serr)
	}
	n := len(state.History)
	if state.Status != "failed" || state.Reason != want || n == 0 || state.History[n-1].Reason != want {
		t.Fatalf("state %+v; want a failure recorded as %q", state, want)
	}
	report, _, rerr := buildPolicyReport("", err)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if report.CheckReason != want || report.CheckSequence != pol.Sequence {
		t.Fatalf("report reason %q sequence %d; want %q at sequence %d", report.CheckReason, report.CheckSequence, want, pol.Sequence)
	}
	if _, err := runServerPolicy(context.Background(), "", false); err == nil {
		t.Fatal("the repeat did not fail")
	}
	if again, serr := readServerPolicyState(""); serr != nil || len(again.History) != n {
		t.Fatalf("history %d entries after the repeat, want %d: %v", len(again.History), n, serr)
	}
}

// The note on the default Codex config names it, and the default folder can be a
// profile's explicit home.
func TestPolicyKeptNoteOmitsExplicitCodexHome(t *testing.T) {
	f := unmovedSiblingRun(t)
	f.writeConfig(t, f.block(t, f.home.Server.Endpoint, sharedRunTokenHome, ""))
	editSibling(t, "", func(p *profile.Profile) { p.CodexHome = f.codexDir })
	note := "default Codex config kept: [explicit Codex home]" + string(filepath.Separator) + "config.toml"

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	if state := f.state(t, ""); state.Status != "applied" || state.Reason != note {
		t.Fatalf("state %q %q; want applied with %q", state.Status, state.Reason, note)
	}
	wantOKReport(t, note)
}

// A recorded write that no longer fits the file is cleared with a note in the
// history, and the note names the file. The history is read like the report.
func TestPolicyStaleSharedWriteHistoryOmitsExplicitCodexHome(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	editSibling(t, "", func(p *profile.Profile) { p.CodexHome = f.codexDir })
	state := f.state(t, "")
	state.SharedWrites[0].OldSHA256 = "digest-of-another-block"
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "server-policy-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	want := "stale shared Codex write: [explicit Codex home]" + string(filepath.Separator) + "config.toml"

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := f.state(t, "")
	last := got.History[len(got.History)-1]
	if len(got.SharedWrites) != 0 || last.Status != "cleared" || last.Reason != want {
		t.Fatalf("history ends %+v with %d writes left; want %q cleared", last, len(got.SharedWrites), want)
	}
}
