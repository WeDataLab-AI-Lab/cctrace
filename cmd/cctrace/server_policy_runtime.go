package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cctrace/internal/configtxn"
	"cctrace/internal/envgen"
	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

var (
	applyServerPolicyChangesFn = applyServerPolicyChanges
	policyReadRuntimeFn        = readSyncRuntime
	policySyncFreeFn           = isSyncLockFree
	policyStopRuntimeFn        = stopServerPolicyRuntime
	policyStartRuntimeFn       = startServerPolicyRuntime
	// policySupervisorRunningFn says whether the local collection daemon runs.
	policySupervisorRunningFn = func() bool {
		running, err := localdaemon.Running(profile.DefaultDir())
		return err == nil && running
	}
)

var syncPolicyHandoff bool

// policyBarrierWait is the most a sync startup waits for the policy lock, and for
// the recovery it carries out under it.
var policyBarrierWait = syncLockWait

type policyHandoff struct {
	Runtime *syncRuntime `json:"runtime"`
	AckPath string       `json:"ack_path,omitempty"`
	Done    bool         `json:"done,omitempty"`
}

func readPolicyHandoff(name string) (*policyHandoff, error) {
	p, err := policyPath(name, "server-policy-handoff.json")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var h policyHandoff
	if err = json.Unmarshal(b, &h); err != nil {
		return nil, err
	}
	return &h, nil
}
func writePolicyHandoff(name string, h *policyHandoff) error {
	p, err := policyPath(name, "server-policy-handoff.json")
	if err != nil {
		return err
	}
	return writeJSONFile(p, h)
}
func finishPolicyHandoff(name string, h *policyHandoff) error {
	h.Done = true
	h.AckPath = ""
	return writePolicyHandoff(name, h)
}

func applyServerPolicyChanges(name string, changes []configtxn.Change) error {
	dir, err := policyPath(name, "server-policy-txn")
	if err != nil {
		return err
	}
	free, err := policySyncFreeFn(name)
	if err != nil {
		return err
	}
	if free {
		return configtxn.Apply(dir, changes, nil)
	}
	rt, ok, err := policyReadRuntimeFn(name)
	if err != nil {
		return err
	}
	if !ok || rt == nil || rt.Interval <= 0 {
		return errors.New("running sync lacks restart metadata; restart it with the current binary first")
	}
	if rt.Local || rt.EndpointOverride != "" {
		return errors.New("cannot migrate sync running with local or endpoint overrides")
	}

	p, err := loadProfile(name)
	if err != nil {
		return err
	}
	sameClaude, err := runtimeClaudeDirMatches(p, rt)
	if err != nil {
		return err
	}
	if !sameClaude {
		return errors.New("cannot migrate sync running with a different Claude directory")
	}
	h := &policyHandoff{Runtime: rt}
	if err = writePolicyHandoff(name, h); err != nil {
		return err
	}
	// The application waits as long as each step takes, up to the step's own bound;
	// it is not under a startup's barrier, whose waits the recovery below shares.
	ctx := context.Background()
	if err = policyStopRuntimeFn(ctx, name); err != nil {
		return err
	}
	err = configtxn.Apply(dir, changes, func() error {
		if startErr := policyStartRuntimeFn(ctx, name, rt); startErr != nil {
			if stopErr := quiescePolicyHandoff(ctx, name); stopErr != nil {
				return errors.Join(configtxn.ErrDeferRecovery, startErr, stopErr)
			}
			return startErr
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, configtxn.ErrDeferRecovery) {
			return err
		}
		return errors.Join(err, recoverServerPolicyChangesLocked(ctx, name))
	}
	// Reload the intent because start recorded the unique acknowledgement path.
	h, err = readPolicyHandoff(name)
	if err != nil {
		return err
	}
	return finishPolicyHandoff(name, h)
}

// runtimeClaudeDirMatches reports whether a running sync reads the Claude
// settings profile p points at. A handover restarts it with the same flags, so
// one started on another directory would keep that directory's settings. The
// directories are compared as directories, not by name: ~/.claude and ~/.CLAUDE
// are one on a case-insensitive disk.
func runtimeClaudeDirMatches(p *profile.Profile, rt *syncRuntime) (bool, error) {
	expected, err := envgen.ClaudeSettingsPath(p)
	if err != nil {
		return false, err
	}
	expected, err = canonicalPolicyPath(expected)
	if err != nil {
		return false, err
	}
	if rt.ClaudeDir == "" {
		return true, nil
	}
	actual, err := canonicalPolicyPath(filepath.Join(rt.ClaudeDir, "settings.json"))
	if err != nil {
		return false, err
	}
	return samePolicyEntry(actual, expected), nil
}

// The caller holds the global policy lock. The startup lock closes the gap
// between spawning a child and that child publishing runtime ownership.
//
// The locks are waited for until ctx ends. A policy run and a sync startup both
// bound their wait; neither goes on over a journal it did not recover.
func recoverServerPolicyChanges(ctx context.Context, name string) error {
	dir, err := policyPath(name, "server-policy-txn")
	if err != nil {
		return err
	}
	h, err := readPolicyHandoff(name)
	if err != nil {
		return err
	}
	_, journalErr := os.Stat(filepath.Join(dir, "journal.json"))
	if journalErr != nil && !os.IsNotExist(journalErr) {
		return journalErr
	}
	if os.IsNotExist(journalErr) && (h == nil || h.Done) {
		return nil
	}
	return withPolicyRecoverySettingsLock(ctx, name, func() error { return recoverServerPolicyChangesLocked(ctx, name) })
}

// A recovery uses the same settings lock as ordinary init/config writers. Policy
// application already owns this lock and calls the locked helper on failure.
func withPolicyRecoverySettingsLock(ctx context.Context, name string, fn func() error) error {
	p, err := loadProfile(name)
	if err != nil {
		return fmt.Errorf("load profile for policy recovery: %w", err)
	}

	release, err := lockPolicySettings(ctx, p, true)
	if err != nil {
		return fmt.Errorf("lock settings for policy recovery: %w", err)
	}
	defer release()
	return fn()
}

// recoverServerPolicyChangesLocked rolls back an interrupted application and
// restarts what it stopped. A managed job recovering its own profile on the way
// into its start (see startingManagedJob) is that restart: the supervisor is the
// process this runs in, and commanding it would wait on the very job that waits
// here, so the handoff is finished and the job goes on.
func recoverServerPolicyChangesLocked(ctx context.Context, name string) error {
	dir, err := policyPath(name, "server-policy-txn")
	if err != nil {
		return err
	}
	h, err := readPolicyHandoff(name)
	if err != nil {
		return err
	}
	if h == nil || h.Done {
		return configtxn.Recover(dir)
	}
	if err = quiescePolicyHandoff(ctx, name); err != nil {
		return errors.Join(configtxn.ErrDeferRecovery, err)
	}
	if err = configtxn.Recover(dir); err != nil {
		return err
	}
	if h.Runtime != nil && !startingManagedJob(ctx, name) {
		if err = policyStartRuntimeFn(ctx, name, h.Runtime); err != nil {
			return errors.Join(err, quiescePolicyHandoff(ctx, name))
		}
	}
	return finishPolicyHandoff(name, h)
}

// policyStepWait is how long one step of a handoff may wait: its own bound, or
// what ctx has left when that is less. A step with a ctx that ends first gives up
// at ctx's end, so a recovery in a startup's barrier stays within the barrier.
func policyStepWait(ctx context.Context, bound time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline); left < bound {
			return max(left, 0)
		}
	}
	return bound
}

// policyStepCtx bounds a step by its own limit and by ctx.
func policyStepCtx(ctx context.Context, bound time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, bound)
}

func quiescePolicyHandoff(ctx context.Context, name string) error {
	path, err := policyPath(name, "server-policy-startup.lock")
	if err != nil {
		return err
	}
	lockCtx, cancel := policyStepCtx(ctx, syncLockWait)
	defer cancel()
	lock, err := lockPolicyFile(lockCtx, path)
	if err != nil {
		// What this has always reported when the startup lock stays held.
		var busy *policyLockBusyError
		if errors.As(err, &busy) {
			return errSyncAlreadyRunning
		}
		return err
	}
	defer lock.Unlock()
	h, err := readPolicyHandoff(name)
	if err != nil {
		return err
	}
	if h != nil {
		h.AckPath = ""
		if err = writePolicyHandoff(name, h); err != nil {
			return err
		}
	}
	return policyStopRuntimeFn(ctx, name)
}
func stopServerPolicyRuntime(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if rt, ok, err := readSyncRuntime(name); err == nil && ok && rt.SupervisorRoot != "" {
		return managedPolicyCommand(ctx, name, rt, "stop")
	}
	free, err := isSyncLockFree(name)
	if err != nil || free {
		return err
	}
	rt, ok, err := readSyncRuntime(name)
	if err != nil {
		return err
	}
	if !ok || rt == nil {
		return errors.New("running sync has no runtime metadata")
	}
	if err = writeSyncStopRequest(name, syncStopRequest{InstanceID: rt.InstanceID, RequestedAt: time.Now().UTC()}); err != nil {
		return err
	}
	// The wait polls for the exit and cannot be cancelled, so it is given only the
	// time ctx has left.
	stopped, err := waitForWatcherExitFn(name, rt.InstanceID, policyStepWait(ctx, syncLockWait))
	if err != nil {
		return err
	}
	if !stopped {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return errors.New("sync did not stop for policy handoff")
	}
	return nil
}
func startServerPolicyRuntime(ctx context.Context, name string, rt *syncRuntime) error {
	if rt.SupervisorRoot != "" {
		return managedPolicyCommand(ctx, name, rt, "start")
	}
	// An old stand-alone watcher is replaced by a start of the local collection
	// daemon. When the daemon already runs, it runs this profile from now on, and a
	// replacement process could only answer "already running" -- which a recovery
	// meets again on every run, because it restarts what was recorded, not what
	// runs. Ask the daemon for the profile instead; it loads the committed profile.
	if policySupervisorRunningFn() {
		return startProfileOnSupervisor(ctx, name)
	}
	ack, err := createSyncTempPath(name, "policy-start-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(ack)
	h, err := readPolicyHandoff(name)
	if err != nil {
		return err
	}
	if h == nil {
		return errors.New("missing policy handoff intent")
	}
	h.AckPath = ack
	if err = writePolicyHandoff(name, h); err != nil {
		return err
	}
	args := []string{"sync", "--watch", "--policy-handoff", "--interval", rt.Interval.String(), "--start-ack-file", ack}
	if name != "" {
		args = append(args, "--profile", name)
	}
	if rt.ClaudeDir != "" {
		args = append(args, "--claude-dir", rt.ClaudeDir)
	}
	if rt.ProfileEmail != "" {
		args = append(args, "--profile-email", rt.ProfileEmail)
	}
	if _, err = spawnSyncProcessFn(args, name); err != nil {
		return err
	}
	a, err := waitForSyncStartAckFn(ack, policyStepWait(ctx, syncStartAckWait))
	if err != nil {
		return err
	}
	// The daemon can start between the look above and the replacement, which then
	// answers "already running". It asks the daemon for a named profile after it has
	// answered, and for the default profile not at all, so ask for the profile here:
	// a start the daemon has already seen changes nothing.
	if a.Status == syncStartAckAlreadyRunning && policySupervisorRunningFn() {
		return startProfileOnSupervisor(ctx, name)
	}
	if a.Status != syncStartAckStarted || a.InstanceID == "" {
		return fmt.Errorf("policy sync start failed: %s %s", a.Status, a.Error)
	}
	return nil
}

// startProfileOnSupervisor asks the running local collection daemon to run the
// profile. It does not need the profile's runtime record, which only exists once
// the daemon's job for the profile has started.
func startProfileOnSupervisor(ctx context.Context, name string) error {
	ctx, cancel := policyStepCtx(ctx, syncLockWait)
	defer cancel()
	if name == "" {
		name = "default"
	}
	return localdaemon.Command(ctx, profile.DefaultDir(), "start", profile.ProfileID(name))
}

// policySyncStartupBarrier makes a sync startup wait for any policy application
// and recovery to finish, and returns what lets go of the barrier. A startup that
// cannot get through in policyBarrierWait, the recovery it needs included, ends
// with localdaemon.ErrPolicyLockBusy: its journal stays as it is, and nothing
// starts collecting on settings that are half changed. ctx ends the wait early.
func policySyncStartupBarrier(ctx context.Context, name, ack string, handoff bool) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, policyBarrierWait)
	defer cancel()
	filename := "server-policy.lock"
	owner := ""
	if handoff {
		filename = "server-policy-startup.lock"
		owner = name
	}
	path, err := policyPath(owner, filename)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := lockPolicyFile(ctx, path)
	if err != nil {
		return nil, barrierWaitError(ctx, err)
	}
	release := func() { _ = lock.Unlock() }
	if handoff {
		h, e := readPolicyHandoff(name)
		if e != nil || h == nil || h.Done || ack == "" || h.AckPath != ack {
			release()
			return nil, errors.Join(errors.New("invalid or cancelled policy handoff"), e)
		}
	}
	if !handoff {
		if err := recoverServerPolicyChanges(ctx, name); err != nil {
			release()
			return nil, barrierWaitError(ctx, err)
		}
	}
	return release, nil
}

// barrierWaitError names a wait that ran out in a sync startup's terms: a lock
// that stayed busy, or a step of the recovery that the barrier's time ended. It is
// not "another sync is already running": a policy application or another
// profile's startup held the lock.
func barrierWaitError(ctx context.Context, err error) error {
	var busy *policyLockBusyError
	if errors.As(err, &busy) || errors.Is(err, errSyncAlreadyRunning) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return localdaemon.ErrPolicyLockBusy
	}
	return err
}

func maybeStartPolicyCoordinator(name string, eligible, dryRun, local, finalize bool, endpoint string, last *time.Time) {
	if !eligible || dryRun || local || finalize || endpoint != "" || (policyPublicKeyBase64 == "" && updatePublicKeyBase64 == "") {
		return
	}
	if !last.IsZero() && time.Since(*last) < 5*time.Minute {
		return
	}
	*last = time.Now()
	args := []string{"config", "policy", "apply", "--automatic"}
	if name != "" {
		args = append(args, "--profile", name)
	}
	if _, err := spawnSyncProcessFn(args, name); err != nil {
		diagf("[policy] start coordinator: %v", err)
	}
}

// A handoff token authorizes one startup only. Future binary-update reexecs must
// enter through the ordinary policy barrier rather than reuse the consumed token.
func withoutPolicyHandoffArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--policy-handoff" || strings.HasPrefix(arg, "--policy-handoff=") || strings.HasPrefix(arg, "--start-ack-file=") {
			continue
		}
		if arg == "--start-ack-file" {
			i++
			continue
		}
		out = append(out, arg)
	}
	return out
}
