package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/codexconfig"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
	"cctrace/internal/store"
)

// HOME and the policy lock wait are process-wide, so none of these tests run in
// parallel.

const testPolicyLockWait = 100 * time.Millisecond

// shortPolicyLockWait narrows the wait a policy run gives the locks.
func shortPolicyLockWait(t *testing.T) {
	t.Helper()
	original := policyLockWait
	policyLockWait = testPolicyLockWait
	t.Cleanup(func() { policyLockWait = original })
}

// holdPolicyLock takes the lock file at path as another process would, and
// returns what lets go of it. It lets go at the end of the test in any case.
func holdPolicyLock(t *testing.T, path string) func() {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	release := func() { _ = lock.Unlock() }
	t.Cleanup(release)
	return release
}

func globalPolicyLockPath(t *testing.T) string {
	t.Helper()
	path, err := policyPath("", "server-policy.lock")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// claudeSettingsLockPath is the writers' lock of the profile's Claude settings.
func claudeSettingsLockPath(t *testing.T, p *profile.Profile) string {
	t.Helper()
	settings, err := envgen.ClaudeSettingsPath(p)
	if err != nil {
		t.Fatal(err)
	}
	path, err := canonicalPolicyPath(settings + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// runPolicyWithinBound runs a policy apply and fails the test if it outlasts its
// lock wait by more than a generous margin. It returns the run's error.
func runPolicyWithinBound(t *testing.T) error {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { _, err := runServerPolicy(context.Background(), "", false); done <- err }()
	select {
	case err := <-done:
		if elapsed := time.Since(start); elapsed < testPolicyLockWait {
			t.Errorf("gave up after %v, before the %v it waits", elapsed, testPolicyLockWait)
		}
		return err
	case <-time.After(testPolicyLockWait + 10*time.Second):
		t.Fatalf("the run is still waiting well past its %v lock wait", testPolicyLockWait)
		return nil
	}
}

func wantPolicyLockBusy(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), "policy lock busy") {
		t.Fatalf("run ended with %v, want a policy lock busy error", err)
	}
	if got := policyCheckResult(err); got != store.ClientPolicyCheckFailed {
		t.Fatalf("reported as %q, want %q: a busy lock clears by itself, it is not a deferral", got, store.ClientPolicyCheckFailed)
	}
}

// wantBusyReport checks what the run reports to the server.
func wantBusyReport(t *testing.T, runErr error, applied, attempted uint64) {
	t.Helper()
	report, _, err := buildPolicyReport("", runErr)
	if err != nil {
		t.Fatal(err)
	}
	if report.CheckResult != store.ClientPolicyCheckFailed || !strings.HasPrefix(report.CheckReason, "policy lock busy") ||
		report.AppliedSequence != applied || report.CheckSequence != attempted {
		t.Fatalf("report %+v; want failed, policy lock busy, applied %d, attempted %d", report, applied, attempted)
	}
}

func wantNoPolicyState(t *testing.T) {
	t.Helper()
	statePath, err := policyPath("", "server-policy-state.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("a run that changed nothing wrote the state file: %v", err)
	}
}

// wantFree checks nothing holds the lock any more: the run let go of what it took.
func wantFree(t *testing.T, path string) {
	t.Helper()
	lock := flock.New(path)
	ok, err := lock.TryLock()
	if err != nil || !ok {
		t.Fatalf("%s still held after the run: %v", path, err)
	}
	_ = lock.Unlock()
}

// wantAppliesOnceFree runs again with nothing in the way: the policy goes through.
func wantAppliesOnceFree(t *testing.T, sequence uint64) {
	t.Helper()
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run once the lock was free: %v", err)
	}
	if got := readStateSequence(t); got != sequence {
		t.Fatalf("applied sequence %d, want %d", got, sequence)
	}
}

// Another run holds the global policy lock. This one gives up after its wait,
// touches no state file, and says why in its report.
func TestServerPolicyRunGivesUpOnBusyGlobalLock(t *testing.T) {
	pol, _ := policyStateFixture(t, false)
	shortPolicyLockWait(t)
	release := holdPolicyLock(t, globalPolicyLockPath(t))

	err := runPolicyWithinBound(t)

	wantPolicyLockBusy(t, err)
	wantNoPolicyState(t)
	wantBusyReport(t, err, 0, 0)
	release()
	wantAppliesOnceFree(t, pol.Sequence)
}

// A writer of the Claude settings holds their lock. The run holds the global lock
// by then and must let it go when it gives up.
func TestServerPolicyRunGivesUpOnBusySettingsLock(t *testing.T) {
	pol, p := policyStateFixture(t, false)
	shortPolicyLockWait(t)
	release := holdPolicyLock(t, claudeSettingsLockPath(t, p))

	err := runPolicyWithinBound(t)

	wantPolicyLockBusy(t, err)
	wantNoPolicyState(t)
	wantBusyReport(t, err, 0, 0)
	wantFree(t, globalPolicyLockPath(t))
	release()
	wantAppliesOnceFree(t, pol.Sequence)
}

// The same wait bounds a recovery the run carries out itself. It has changed
// nothing when its lock stays busy: the handoff stays as it was and the state
// file stays unwritten.
func TestServerPolicyRunGivesUpOnBusyLockWhileRecovering(t *testing.T) {
	_, p := policyStateFixture(t, false)
	shortPolicyLockWait(t)
	if err := writePolicyHandoff("", &policyHandoff{Runtime: &syncRuntime{Interval: time.Second}}); err != nil {
		t.Fatal(err)
	}
	handoff, err := policyPath("", "server-policy-handoff.json")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(handoff)
	if err != nil {
		t.Fatal(err)
	}
	holdPolicyLock(t, claudeSettingsLockPath(t, p))

	err = runPolicyWithinBound(t)

	wantPolicyLockBusy(t, err)
	wantNoPolicyState(t)
	if after, err := os.ReadFile(handoff); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("a recovery that never got its lock changed the handoff: %v", err)
	}
	wantFree(t, globalPolicyLockPath(t))
}

// recordedSharedWrite leaves a recorded write an earlier move made, in a Codex
// config this policy does not plan, and returns the config and what it held.
func recordedSharedWrite(t *testing.T, p *profile.Profile) (string, []byte) {
	t.Helper()
	old := codexconfig.PolicyBlock{Endpoint: "http://localhost:4317", Token: p.Server.AuthToken}
	next := codexconfig.PolicyBlock{Endpoint: p.Server.Endpoint, Token: p.Server.AuthToken, CA: p.Server.CACertFile}
	dir := t.TempDir()
	if err := codexconfig.WriteOtelBlock(dir, old.Endpoint, old.Token, old.CA); err != nil {
		t.Fatal(err)
	}
	config, err := canonicalPolicyPath(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := codexconfig.PlanSharedServerPolicyConfig(content, old, next, nil)
	if err != nil || plan.Holds != codexconfig.SharedBlockOld {
		t.Fatalf("fixture block: %+v %v", plan, err)
	}
	recorded, err := json.Marshal(serverPolicyState{Status: "applied", SharedWrites: []sharedPolicyWrite{{Path: config, OldSHA256: plan.OldSHA256, NewSHA256: plan.NewSHA256, OldEndpoint: old.Endpoint}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "server-policy-state.json"), recorded, 0600); err != nil {
		t.Fatal(err)
	}
	return config, content
}

// Once the run has changed something, a lock it then cannot get ends the run the
// way any other failure does: recorded in the state, with the sequence it was
// working on, and reported.
func TestServerPolicyRunRecordsBusyLockAfterCarryingOutAWrite(t *testing.T) {
	pol, p := policyStateFixture(t, false)
	shortPolicyLockWait(t)
	config, content := recordedSharedWrite(t, p)
	holdPolicyLock(t, claudeSettingsLockPath(t, p))

	err := runPolicyWithinBound(t)

	wantPolicyLockBusy(t, err)
	if got, readErr := os.ReadFile(config); readErr != nil || bytes.Equal(got, content) {
		t.Fatalf("the recorded write was not carried out: %v", readErr)
	}
	state, readErr := readServerPolicyState("")
	if readErr != nil {
		t.Fatal(readErr)
	}
	n := len(state.History)
	if len(state.SharedWrites) != 0 || state.Status != "failed" || !strings.HasPrefix(state.Reason, "policy lock busy") ||
		n == 0 || state.History[n-1].Status != "failed" || state.History[n-1].Sequence != pol.Sequence {
		t.Fatalf("state %+v; want the write done and the failure recorded at sequence %d", state, pol.Sequence)
	}
	wantBusyReport(t, err, 0, pol.Sequence)
	wantFree(t, globalPolicyLockPath(t))
}

// A recovery the run carried out is a change too. The lock that then stays busy
// is one the recovery did not need: the profile gained a Codex folder while the
// recovery's start hook ran, so the apply wants that config's lock as well.
func TestServerPolicyRunRecordsBusyLockAfterRecovery(t *testing.T) {
	pol, _ := policyStateFixture(t, false)
	shortPolicyLockWait(t)
	if err := writePolicyHandoff("", &policyHandoff{Runtime: &syncRuntime{Interval: time.Second}}); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(t.TempDir(), "codex-extra")
	oldStop, oldStart := policyStopRuntimeFn, policyStartRuntimeFn
	t.Cleanup(func() { policyStopRuntimeFn, policyStartRuntimeFn = oldStop, oldStart })
	policyStopRuntimeFn = func(context.Context, string) error { return nil }
	// The hook runs on the run's goroutine, so it reports by returning an error,
	// which fails the recovery, and never through t.
	var held *flock.Flock
	t.Cleanup(func() {
		if held != nil {
			_ = held.Unlock()
		}
	})
	policyStartRuntimeFn = func(context.Context, string, *syncRuntime) error {
		p, err := loadProfile("")
		if err != nil {
			return err
		}
		p.Options.CodexSyncDeclined, p.Options.CodexDirs = false, []string{extra}
		if err := saveProfile(p, ""); err != nil {
			return err
		}
		if err := os.MkdirAll(extra, 0700); err != nil {
			return err
		}
		lockPath, err := canonicalPolicyPath(filepath.Join(extra, "config.toml.lock"))
		if err != nil {
			return err
		}
		held = flock.New(lockPath)
		return held.Lock()
	}

	err := runPolicyWithinBound(t)

	wantPolicyLockBusy(t, err)
	handoff, readErr := readPolicyHandoff("")
	if readErr != nil || handoff == nil || !handoff.Done {
		t.Fatalf("handoff %+v %v; want the recovery finished", handoff, readErr)
	}
	state, readErr := readServerPolicyState("")
	if readErr != nil {
		t.Fatal(readErr)
	}
	n := len(state.History)
	if state.Status != "failed" || !strings.HasPrefix(state.Reason, "policy lock busy") || n == 0 || state.History[n-1].Sequence != pol.Sequence {
		t.Fatalf("state %+v; want the failure recorded at sequence %d", state, pol.Sequence)
	}
	wantBusyReport(t, err, 0, pol.Sequence)
	wantFree(t, globalPolicyLockPath(t))
}

// narrowSettingsLockWait gives the run a long bound on its locks as a whole and a
// short one on each set of settings files, so a test tells which of the two a wait
// followed. It returns the long bound.
func narrowSettingsLockWait(t *testing.T) time.Duration {
	t.Helper()
	original, originalSettings := policyLockWait, policySettingsLockWait
	policyLockWait, policySettingsLockWait = 5*time.Second, testPolicyLockWait
	t.Cleanup(func() { policyLockWait, policySettingsLockWait = original, originalSettings })
	return policyLockWait
}

// wantShortWait checks a wait followed the settings locks' short bound, not the
// run's long one.
func wantShortWait(t *testing.T, elapsed, long time.Duration) {
	t.Helper()
	if elapsed < testPolicyLockWait || elapsed >= long/2 {
		t.Fatalf("waited %v; want the %v of a settings lock, well under the run's %v", elapsed, testPolicyLockWait, long)
	}
}

// A run holds the global lock while it takes the settings files' locks, and the
// writers of those files hold them for a moment. Another profile's startup waits
// on the global lock meanwhile, so the run does not wait for a settings lock as
// long as it does for the global one.
func TestServerPolicyRunWaitsBrieflyForSettingsLock(t *testing.T) {
	_, p := policyStateFixture(t, false)
	long := narrowSettingsLockWait(t)
	holdPolicyLock(t, claudeSettingsLockPath(t, p))

	start := time.Now()
	_, err := runServerPolicy(context.Background(), "", false)
	elapsed := time.Since(start)

	wantPolicyLockBusy(t, err)
	wantShortWait(t, elapsed, long)
	wantNoPolicyState(t)
	wantFree(t, globalPolicyLockPath(t))
}

// The same short wait bounds the settings lock a recovery takes.
func TestServerPolicyRunWaitsBrieflyForSettingsLockWhileRecovering(t *testing.T) {
	_, p := policyStateFixture(t, false)
	long := narrowSettingsLockWait(t)
	if err := writePolicyHandoff("", &policyHandoff{Runtime: &syncRuntime{Interval: time.Second}}); err != nil {
		t.Fatal(err)
	}
	holdPolicyLock(t, claudeSettingsLockPath(t, p))

	start := time.Now()
	_, err := runServerPolicy(context.Background(), "", false)
	elapsed := time.Since(start)

	wantPolicyLockBusy(t, err)
	wantShortWait(t, elapsed, long)
	wantFree(t, globalPolicyLockPath(t))
}

// And the locks of the configs a recorded write waits on. A write that cannot get
// its lock never stops the run: the policy still applies.
func TestServerPolicyRunWaitsBrieflyForSettingsLockWhileDraining(t *testing.T) {
	pol, p := policyStateFixture(t, false)
	long := narrowSettingsLockWait(t)
	config, content := recordedSharedWrite(t, p)
	holdPolicyLock(t, config+".lock")

	start := time.Now()
	_, err := runServerPolicy(context.Background(), "", false)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("run ended with %v, want the policy applied", err)
	}
	wantShortWait(t, elapsed, long)
	if got, readErr := os.ReadFile(config); readErr != nil || !bytes.Equal(got, content) {
		t.Fatalf("a write whose lock was busy changed the config: %v", readErr)
	}
	if got := readStateSequence(t); got != pol.Sequence {
		t.Fatalf("the policy was not applied: sequence %d, want %d", got, pol.Sequence)
	}
}

// A run that is asked to stop stops waiting for a lock, whatever bound it has
// left, and reports the cancellation, not a busy lock: the journal of a recovery it
// never started stays as it is.
func TestServerPolicyRunStopsWaitingForALockWhenCancelled(t *testing.T) {
	policyStateFixture(t, false)
	original := policyLockWait
	policyLockWait = time.Hour
	t.Cleanup(func() { policyLockWait = original })
	if err := writePolicyHandoff("", &policyHandoff{Runtime: &syncRuntime{Interval: time.Second}}); err != nil {
		t.Fatal(err)
	}
	holdPolicyLock(t, globalPolicyLockPath(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := runServerPolicy(ctx, "", false)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run ended with %v, want the cancellation", err)
	}
	if time.Since(start) >= 10*time.Second {
		t.Fatalf("a cancelled run waited %v for the lock", time.Since(start))
	}
	handoff, readErr := readPolicyHandoff("")
	if readErr != nil || handoff == nil || handoff.Done {
		t.Fatalf("handoff %+v %v; want the recovery it never started still pending", handoff, readErr)
	}
}

// The lock an explicit Codex home's writers hold is named in the busy error by its
// path, and the home is a directory the user chose: what the run records in the
// state, after it has changed something, and what it reports say "[explicit Codex
// home]" instead.
func TestServerPolicyBusyExplicitHomeLockNamesNoHomeInTheRecordOrTheReport(t *testing.T) {
	e := newExplicitHomeRun(t)
	e.write(t, e.before(t, sharedRunTokenA))
	shortPolicyLockWait(t)
	p, err := loadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	// A write an earlier move left is carried out first, so the busy lock that
	// follows is a failure after a change, which is recorded.
	recordedSharedWrite(t, p)
	lockPath, err := canonicalPolicyPath(e.configPath() + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	holdPolicyLock(t, lockPath)

	err = runPolicyWithinBound(t)

	wantPolicyLockBusy(t, err)
	const want = "policy lock busy: [explicit Codex home]"
	state, readErr := readServerPolicyState("")
	if readErr != nil {
		t.Fatal(readErr)
	}
	n := len(state.History)
	if state.Status != "failed" || !strings.HasPrefix(state.Reason, want) || strings.Contains(state.Reason, e.home) ||
		n == 0 || !strings.HasPrefix(state.History[n-1].Reason, want) || strings.Contains(state.History[n-1].Reason, e.home) {
		t.Fatalf("state %+v; want the failure recorded without the home's path", state)
	}
	report, _, rerr := buildPolicyReport("", err)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.HasPrefix(report.CheckReason, want) || strings.Contains(report.CheckReason, e.home) {
		t.Fatalf("report reason %q; want the home masked", report.CheckReason)
	}
}
