package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"cctrace/internal/codexlog"
	"cctrace/internal/profile"
)

// HOME, the signing key and the policy lock wait are process-wide, so none of
// these tests run in parallel.

// probeWatch answers the fixture destination as a healthy server, counts the
// requests of the connectivity test and notes how many found another profile's
// sync startup unable to get through the policy barrier: nothing else may wait on a
// server that is slow to answer.
type probeWatch struct {
	requests, held atomic.Int32
}

// watchPolicyProbe starts watching. onRequest, when given, runs in the destination's
// handler for every request and returns the status to answer with, 0 for 200.
func watchPolicyProbe(t *testing.T, onRequest func(r *http.Request) int) *probeWatch {
	t.Helper()
	w := &probeWatch{}
	original := policyBarrierWait
	policyBarrierWait = testPolicyLockWait
	t.Cleanup(func() { policyBarrierWait = original })
	onPolicyDestination(t, func(rw http.ResponseWriter, r *http.Request) {
		w.requests.Add(1)
		if release, err := policySyncStartupBarrier(context.Background(), "other", "", false); err != nil {
			w.held.Add(1)
		} else {
			release()
		}
		status := 200
		if onRequest != nil {
			if got := onRequest(r); got != 0 {
				status = got
			}
		}
		rw.WriteHeader(status)
	})
	return w
}

// A connectivity test against a server that is slow, or that fails and is tried
// again, must not make every other profile's sync startup and policy run wait on
// the global lock the whole time.
func TestServerPolicyProbeDoesNotHoldTheGlobalLock(t *testing.T) {
	for name, checkOnly := range map[string]bool{"apply": false, "check": true} {
		t.Run(name, func(t *testing.T) {
			policyStateFixture(t, false)
			watch := watchPolicyProbe(t, nil)

			if _, err := runServerPolicy(context.Background(), "", checkOnly); err != nil {
				t.Fatal(err)
			}

			if got := watch.requests.Load(); got != 4 {
				t.Fatalf("%d probe requests, want 4", got)
			}
			if held := watch.held.Load(); held != 0 {
				t.Fatalf("%d of the probe's requests could not pass the policy barrier", held)
			}
			wantFree(t, globalPolicyLockPath(t))
		})
	}
}

// rotateToken changes the profile's token the way another command would while the
// probe is in flight, which the probe's own Authorization header depends on.
func rotateToken(t *testing.T, round *atomic.Int32) func(*http.Request) int {
	return func(r *http.Request) int {
		if r.URL.Path != "/api/health" {
			return 0
		}
		p, err := loadProfile("")
		if err == nil {
			p.Server.AuthToken = "rotated-" + strconv.Itoa(int(round.Add(1)))
			err = saveProfile(p, "")
		}
		if err != nil {
			t.Error(err) // the handler runs on the server's goroutine; Error is safe there
		}
		return 0
	}
}

// What was tested is not what will be committed once the token or the trust bundle
// changed: the run lets go of its locks, tests again and, if it is still not the
// same, leaves the policy to the next run. It never tests under the lock.
func TestServerPolicyProbesAgainOnceWhenTheTestedInputsChange(t *testing.T) {
	t.Run("changed once", func(t *testing.T) {
		pol, _ := policyStateFixture(t, false)
		var round atomic.Int32
		rotate := rotateToken(t, &round)
		watch := watchPolicyProbe(t, func(r *http.Request) int {
			if round.Load() == 0 {
				return rotate(r)
			}
			return 0
		})

		if _, err := runServerPolicy(context.Background(), "", false); err != nil {
			t.Fatal(err)
		}

		if got := watch.requests.Load(); got != 8 {
			t.Fatalf("%d probe requests, want the test and one more", got)
		}
		if held := watch.held.Load(); held != 0 {
			t.Fatalf("%d of the probe's requests could not pass the policy barrier", held)
		}
		if got := readStateSequence(t); got != pol.Sequence {
			t.Fatalf("applied sequence %d, want %d", got, pol.Sequence)
		}
		wantFree(t, globalPolicyLockPath(t))
	})
	t.Run("changed again", func(t *testing.T) {
		policyStateFixture(t, false)
		var round atomic.Int32
		watch := watchPolicyProbe(t, rotateToken(t, &round))

		_, err := runServerPolicy(context.Background(), "", false)

		if !errors.Is(err, errPolicySettingsChanged) {
			t.Fatalf("run ended with %v, want the settings changed during the test", err)
		}
		if got := watch.requests.Load(); got != 8 {
			t.Fatalf("%d probe requests, want two tests and none under the lock", got)
		}
		if held := watch.held.Load(); held != 0 {
			t.Fatalf("%d of the probe's requests could not pass the policy barrier", held)
		}
		if got := readStateSequence(t); got != 0 {
			t.Fatalf("applied sequence %d after a run that gave up", got)
		}
		wantFree(t, globalPolicyLockPath(t))
	})
}

// A profile the policy does not reach, one it already moved and one it defers on
// never get a request: the test sends the profile's token to the new server, and
// there is nothing to send it for.
func TestServerPolicySendsNoProbeWithNothingToApply(t *testing.T) {
	t.Run("already applied", func(t *testing.T) {
		policyStateFixture(t, false)
		if _, err := runServerPolicy(context.Background(), "", false); err != nil {
			t.Fatal(err)
		}
		watch := watchPolicyProbe(t, nil)

		if _, err := runServerPolicy(context.Background(), "", false); err != nil {
			t.Fatal(err)
		}

		if got := watch.requests.Load(); got != 0 {
			t.Fatalf("%d probe requests for a policy already applied", got)
		}
	})
	t.Run("another installation", func(t *testing.T) {
		policyStateFixture(t, false)
		if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "installation-id"), []byte(uuid.NewString()), 0600); err != nil {
			t.Fatal(err)
		}
		watch := watchPolicyProbe(t, nil)

		_, err := runServerPolicy(context.Background(), "", false)

		if err == nil || !strings.Contains(err.Error(), "another installation") {
			t.Fatalf("run ended with %v, want the policy to name another installation", err)
		}
		if got := watch.requests.Load(); got != 0 {
			t.Fatalf("%d probe requests for a policy that does not reach this profile", got)
		}
	})
	t.Run("deferred", func(t *testing.T) {
		_, p := policyStateFixture(t, false)
		sibling := profile.NewDefault()
		sibling.ClaudeConfigDir = p.ClaudeConfigDir
		if err := profile.SaveNamed(sibling, "sibling"); err != nil {
			t.Fatal(err)
		}
		watch := watchPolicyProbe(t, nil)

		_, err := runServerPolicy(context.Background(), "", false)

		if err == nil || policyCheckResult(err) != "deferred" {
			t.Fatalf("run ended with %v, want a deferral", err)
		}
		if got := watch.requests.Load(); got != 0 {
			t.Fatalf("%d probe requests for a policy the profile defers on", got)
		}
	})
}

// A run whose connectivity test fails after another run has applied the same
// policy is out of date: it must not record its failure over the state that run
// left.
func TestServerPolicyOutdatedProbeFailureDoesNotOverwriteAppliedState(t *testing.T) {
	pol, _ := policyStateFixture(t, false)
	original := policyLockWait
	policyLockWait = time.Second
	t.Cleanup(func() { policyLockWait = original })
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	watchPolicyProbe(t, func(*http.Request) int {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
			return http.StatusServiceUnavailable
		}
		return 0
	})
	slow := make(chan error, 1)
	go func() { _, err := runServerPolicy(context.Background(), "", false); slow <- err }()
	<-entered

	// The slow run is inside its test. Nothing it holds keeps this one out.
	_, err := runServerPolicy(context.Background(), "", false)
	close(release)
	slowErr := <-slow

	if err != nil {
		t.Fatalf("the run that overtook the slow test: %v", err)
	}
	if slowErr == nil || !strings.Contains(slowErr.Error(), "503") {
		t.Fatalf("the slow run ended with %v, want its failed test", slowErr)
	}
	state, readErr := readServerPolicyState("")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state.Status != "applied" || state.Sequence != pol.Sequence {
		t.Fatalf("state %+v; want the policy applied by the run that overtook the slow one", state)
	}
	for _, record := range state.History {
		if record.Status == "failed" {
			t.Fatalf("history %+v records the outdated failure", state.History)
		}
	}
	wantFree(t, globalPolicyLockPath(t))
}

// What a run reads without the lock may be half of a transaction another run has
// under way, so an error or a deferral it ends in is provisional until it is read
// again under the lock. A run that cannot get the lock to confirm it says so, and
// records nothing; once it has the lock it reports and records what it finds there.
func TestServerPolicyDeferralIsConfirmedUnderTheGlobalLock(t *testing.T) {
	_, p := policyStateFixture(t, false)
	sibling := profile.NewDefault()
	sibling.ClaudeConfigDir = p.ClaudeConfigDir
	if err := profile.SaveNamed(sibling, "sibling"); err != nil {
		t.Fatal(err)
	}
	shortPolicyLockWait(t)
	release := holdPolicyLock(t, globalPolicyLockPath(t))

	err := runPolicyWithinBound(t)

	wantPolicyLockBusy(t, err)
	wantNoPolicyState(t)
	release()
	_, err = runServerPolicy(context.Background(), "", false)
	if err == nil || policyCheckResult(err) != "deferred" {
		t.Fatalf("run with the lock free ended with %v, want the deferral", err)
	}
	state, readErr := readServerPolicyState("")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state.Status != "failed" || !strings.HasSuffix(state.Reason, policyDeferredSuffix) {
		t.Fatalf("state %+v; want the deferral recorded", state)
	}
}

// An error the first read ends in can be gone when the run reads again under the
// lock: another command rewrote the profile while the plan was being made. The run
// plans again instead of reporting, and recording, an error that no longer holds.
func TestServerPolicyPlansAgainWhenTheErrorIsGoneUnderTheLock(t *testing.T) {
	pol, p := policyStateFixture(t, false)
	// Two Codex homes that exist: the default one and one more, so the plan compares them.
	if err := os.MkdirAll(codexlog.ExpandHome(codexlog.DefaultCodexDir()), 0700); err != nil {
		t.Fatal(err)
	}
	p.Options.CodexSyncDeclined = false
	p.Options.CodexDirs = []string{t.TempDir()}
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	// The profile is rewritten once, while the first plan compares the Codex homes.
	var rewritten atomic.Bool
	original := policyStatFn
	t.Cleanup(func() { policyStatFn = original })
	policyStatFn = func(name string) (os.FileInfo, error) {
		if rewritten.CompareAndSwap(false, true) {
			current, err := loadProfile("")
			if err == nil {
				current.UpdatedAt = time.Now().UTC()
				err = saveProfile(current, "")
			}
			if err != nil {
				t.Error(err)
			}
		}
		return os.Stat(name)
	}

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	if !rewritten.Load() {
		t.Fatal("the fixture never rewrote the profile during the first plan")
	}
	state, err := readServerPolicyState("")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "applied" || state.Sequence != pol.Sequence {
		t.Fatalf("state %+v; want the policy applied", state)
	}
	for _, record := range state.History {
		if record.Status == "failed" {
			t.Fatalf("history %+v records the error that did not hold", state.History)
		}
	}
}
