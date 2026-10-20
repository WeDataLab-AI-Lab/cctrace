package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"

	"cctrace/internal/atomicfile"
	"cctrace/internal/configtxn"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
)

// policyPublicKeyBase64 is the trust root reserved for server policies, set like
// updatePublicKeyBase64:
//
//	-ldflags "-X main.policyPublicKeyBase64=<base64-encoded 32-byte Ed25519 public key>"
//
// A policy rewrites every endpoint a client talks to, so it gets a key that can
// sign nothing else. The update key is still trusted for policies so a lost
// policy key can be recovered, but the update path never trusts this one.
var policyPublicKeyBase64 string

// trustedPolicyKeys is the policy key and the update key together. Both accept a
// comma-separated list, as parseUpdatePublicKeys does, and one malformed entry
// fails the whole set rather than leaving the client trusting fewer keys than
// it was built with.
func trustedPolicyKeys() ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	seen := map[string]bool{}
	for _, source := range []struct{ name, encoded string }{{"policy", policyPublicKeyBase64}, {"update", updatePublicKeyBase64}} {
		for _, field := range strings.Split(source.encoded, ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(field)
			if err != nil || len(raw) != ed25519.PublicKeySize {
				return nil, fmt.Errorf("invalid %s public key %q: expected base64-encoded %d-byte Ed25519 key", source.name, field, ed25519.PublicKeySize)
			}
			if !seen[string(raw)] {
				seen[string(raw)] = true
				keys = append(keys, ed25519.PublicKey(raw))
			}
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("policy public key is not configured; rebuild with main.policyPublicKeyBase64 or main.updatePublicKeyBase64")
	}
	return keys, nil
}

type serverPolicyStatus struct {
	serverpolicy.Target
	serverPolicyState
	RecoveryPending bool `json:"recovery_pending,omitempty"`
}

func serverPolicyCmd(name *string) *cobra.Command {
	cmd := &cobra.Command{Use: "policy", Short: "Inspect and apply signed server configuration policies"}
	for _, action := range []string{"status", "check", "apply"} {
		var automatic, logToFile bool
		sub := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			resolved := *name
			if resolved == "" {
				resolved = os.Getenv("CCTRACE_PROFILE")
			}
			if logToFile {
				defer installDaemonLog(resolved)()
			}
			var result any
			var err error
			if action == "status" {
				target, e := serverPolicyTarget(resolved)
				if e != nil {
					return e
				}
				state, e := readServerPolicyState(resolved)
				if e != nil {
					return e
				}
				result = serverPolicyStatus{Target: target, serverPolicyState: state, RecoveryPending: serverPolicyRecoveryPending(resolved)}
			} else {
				result, err = runServerPolicy(cmd.Context(), resolved, action == "check")
				// After every apply, whatever it found: a daemon runs one every five
				// minutes, which is what puts a device in front of the operator before
				// any policy names it. A check stays read-only.
				if action == "apply" {
					reportServerPolicy(cmd.Context(), resolved, err)
				}
			}
			if err != nil {
				return err
			}
			if automatic {
				return nil
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}}
		if action == "apply" {
			sub.Flags().BoolVar(&automatic, "automatic", false, "Background policy check")
			sub.Flags().BoolVar(&logToFile, "log-to-file", false, "Write diagnostics to sync log")
			_ = sub.Flags().MarkHidden("automatic")
			_ = sub.Flags().MarkHidden("log-to-file")
		}
		cmd.AddCommand(sub)
	}
	return cmd
}

func serverPolicyRecoveryPending(name string) bool {
	path, err := policyPath(name, filepath.Join("server-policy-txn", "journal.json"))
	if err != nil {
		return true
	}
	if _, err := os.Stat(path); err == nil || !os.IsNotExist(err) {
		return true
	}
	h, err := readPolicyHandoff(name)
	return err != nil || (h != nil && !h.Done)
}

// errPolicySettingsChanged ends a run whose plan or connectivity test no longer
// matches what would be committed, twice in a row: the next run starts over.
var errPolicySettingsChanged = errors.New("settings changed while the policy was being checked; retry")

// policyPass is what applying a fetched policy comes to, worked out from the
// files as they were when it was made.
type policyPass struct {
	profile *profile.Profile
	changes []configtxn.Change
	next    serverPolicyState
	// ca is the trust bundle the policy installs, user roots included: the one the
	// connectivity test has to use.
	ca []byte
	// locked are the settings files whose locks the pass took.
	locked map[string]bool
	// current is set when the policy is already in effect: there is nothing to apply.
	current *serverPolicyStatus
}

// sameProbe reports whether the connectivity test made for tested also covers p:
// it sends the profile's token to the new server and trusts the planned bundle.
func (p *policyPass) sameProbe(tested *policyPass) bool {
	return bytes.Equal(p.ca, tested.ca) && p.profile.Server.AuthToken == tested.profile.Server.AuthToken
}

// plannedPolicyCA is the managed CA a plan writes.
func plannedPolicyCA(changes []configtxn.Change, next serverPolicyState) ([]byte, error) {
	for _, change := range changes {
		if change.Path == next.ManagedCA {
			return change.After, nil
		}
	}
	return nil, fmt.Errorf("planned policy has no managed CA")
}

func runServerPolicy(ctx context.Context, name string, checkOnly bool) (result any, retErr error) {
	target, err := serverPolicyTarget(name)
	if err != nil {
		return nil, err
	}
	lockPath, err := policyPath("", "server-policy.lock")
	if err != nil {
		return nil, err
	}
	// Every lock this run waits for -- the global one and the settings files' --
	// shares one deadline, so a holder that never lets go costs a bounded time.
	lockCtx, cancelLocks := context.WithTimeout(ctx, policyLockWait)
	defer cancelLocks()
	var lock *flock.Flock
	locked := false
	acquire := func() error {
		if locked {
			return nil
		}
		l, err := lockPolicyFile(lockCtx, lockPath)
		if err != nil {
			return err
		}
		lock, locked = l, true
		return nil
	}
	unlockGlobal := func() error {
		if err := lock.Unlock(); err != nil {
			return err
		}
		locked = false
		return nil
	}
	// changed says this run has already changed something on disk: a recovery it
	// carried out, or a recorded shared write. A lock it cannot get after that
	// ends the run as any failure does, recorded in the state -- if the global
	// lock is free to record it: see the recorder below.
	changed := false
	recoverChanges := func() error {
		pending := serverPolicyRecoveryPending(name)
		if err := recoverServerPolicyChanges(lockCtx, name); err != nil {
			return err
		}
		changed = changed || pending
		return nil
	}
	defer func() {
		if locked {
			_ = lock.Unlock()
		}
	}()
	// Whatever wrapped it, a busy lock is reported under its own name.
	defer func() {
		var busy *policyLockBusyError
		if errors.As(retErr, &busy) {
			retErr = busy
		}
	}()
	if serverPolicyRecoveryPending(name) {
		if checkOnly {
			return nil, fmt.Errorf("interrupted policy application; run config policy apply to recover")
		}
		if err := acquire(); err != nil {
			return nil, err
		}
		if err := recoverChanges(); err != nil {
			return nil, err
		}
		if err := lock.Unlock(); err != nil {
			return nil, err
		}
		locked = false
	}
	// Shared writes a committed policy left for later run after recovery, which
	// may roll back the state recording them, and before the fetch, so they are
	// carried out whatever the server answers. A failure here never stops the run.
	var pendingErr error
	recordedWrites := 0
	if recorded, err := readServerPolicyState(name); !checkOnly && err == nil && len(recorded.SharedWrites) > 0 {
		recordedWrites = len(recorded.SharedWrites)
		if err := acquire(); err != nil {
			pendingErr = errSharedPolicyWritePending
		} else {
			pendingErr = drainRecordedSharedPolicyWrites(lockCtx, name)
			if err := lock.Unlock(); err != nil {
				return nil, err
			}
			locked = false
		}
	}
	state, err := readServerPolicyState(name)
	if err != nil {
		return nil, err
	}
	if len(state.SharedWrites) < recordedWrites {
		changed = true
	}
	attemptSequence := state.Sequence
	// attemptStale, while the run has not yet taken the global lock for its commit,
	// tells whether the state has moved on from the one the run read: another run
	// applied a policy meanwhile. What this run then fails on is out of date, and
	// recording it would put a failure over that run's success.
	var attemptStale func(latest serverPolicyState) bool
	defer func() {
		// A run that changed nothing before it gave up leaves the state file as it
		// was. What it records is the busy lock under its own name, as it reports it.
		var busy *policyLockBusyError
		if errors.As(retErr, &busy) {
			retErr = busy
			if !changed {
				return
			}
		}
		if retErr == nil || checkOnly || serverPolicyRecoveryPending(name) {
			return
		}
		// Recording needs the global lock. A run that gave up on that very lock cannot
		// record its failure; only the report, sent after this returns, carries it.
		if err := acquire(); err != nil {
			return
		}
		latest, e := readServerPolicyState(name)
		if e != nil {
			return
		}
		if attemptStale != nil && attemptStale(latest) {
			return
		}
		// The state is read by people the report is shared with, so it keeps the
		// reason as the report gives it.
		reason := redactPolicyReason(retErr.Error())
		// A deferral repeats every run until its owner acts; one record of it is
		// enough, and more would push the applied policies out of the history.
		if n := len(latest.History); n > 0 && latest.Status == "failed" && latest.Reason == reason &&
			latest.History[n-1].Sequence == attemptSequence && latest.History[n-1].Status == "failed" && latest.History[n-1].Reason == reason {
			return
		}
		appendPolicyRecord(&latest, attemptSequence, "failed", reason)
		path, e := policyPath(name, "server-policy-state.json")
		if e != nil {
			return
		}
		b, e := json.MarshalIndent(latest, "", "  ")
		if e == nil {
			_ = atomicfile.Write(path, b, 0600)
		}
	}()
	// A failed run reports its own failure; one that otherwise went well reports
	// the shared writes still waiting. Deferred after the recorder, so it runs
	// first and the recorder keeps what the run reports.
	defer func() {
		if retErr == nil && pendingErr != nil {
			result, retErr = nil, pendingErr
		}
	}()
	p, err := loadProfile(name)
	if err != nil {
		return nil, err
	}
	keys, err := trustedPolicyKeys()
	if err != nil {
		return nil, err
	}
	pol, digest, err := fetchServerPolicyWithBootstrap(ctx, p, target, keys, time.Now().UTC(), state)
	if err != nil {
		return nil, err
	}
	if pol == nil {
		return serverPolicyStatus{Target: target, serverPolicyState: state}, nil
	}
	// evaluate works out from the files as they are what applying the policy comes
	// to. With takeSettingsLocks it also takes the locks of the settings files the policy writes,
	// and the caller lets go of them through the function it returns.
	evaluate := func(takeSettingsLocks bool) (*policyPass, func(), error) {
		read, err := readServerPolicyState(name)
		if err != nil {
			return nil, nil, err
		}
		p, err := loadProfile(name)
		if err != nil {
			return nil, nil, err
		}
		attemptSequence = pol.Sequence
		if !takeSettingsLocks {
			attemptStale = func(latest serverPolicyState) bool {
				return latest.Sequence != read.Sequence || latest.Digest != read.Digest
			}
		}
		// Verify the signed recipient even on the idempotent path, whose old
		// endpoints no longer match after the first successful transition.
		if !pol.Covers(target) {
			return nil, nil, fmt.Errorf("policy targets another installation or profile")
		}
		applied, err := serverPolicySequence(*pol, digest, read, policyEndpoints(p))
		if err != nil {
			return nil, nil, err
		}
		if applied {
			b, err := os.ReadFile(read.ManagedCA)
			if err != nil {
				return nil, nil, err
			}
			sum := sha256.Sum256(b)
			if p.Server.CACertFile != read.ManagedCA || hex.EncodeToString(sum[:]) != read.ManagedCASHA256 {
				return nil, nil, fmt.Errorf("managed CA was changed; policy deferred")
			}
			return &policyPass{current: &serverPolicyStatus{Target: target, serverPolicyState: read}}, func() {}, nil
		}
		if err = serverpolicy.Match(*pol, target, policyEndpoints(p)); err != nil {
			return nil, nil, err
		}
		releaseSettings, lockedSettings := func() {}, map[string]bool(nil)
		if takeSettingsLocks {
			releaseSettings, lockedSettings, err = lockPolicySettingsPaths(lockCtx, p, !checkOnly)
			if err != nil {
				return nil, nil, err
			}
		}
		changes, next, err := planServerPolicyUnderLocks(p, name, *pol, digest, read, lockedSettings)
		if err == nil && takeSettingsLocks && !checkOnly {
			err = validatePolicySettingsLocks(changes, lockedSettings)
		}
		var ca []byte
		if err == nil {
			ca, err = plannedPolicyCA(changes, next)
		}
		if err != nil {
			releaseSettings()
			return nil, nil, err
		}
		return &policyPass{profile: p, changes: changes, next: next, ca: ca, locked: lockedSettings}, releaseSettings, nil
	}
	// settle waits out a policy application another run has under way, and recovers
	// one that was interrupted, so a pass reads files that are not half written.
	settle := func() error {
		if !serverPolicyRecoveryPending(name) {
			return nil
		}
		if err := acquire(); err != nil {
			return err
		}
		if checkOnly {
			// The application under way has finished by now; one still pending was
			// interrupted, and only an apply recovers it.
			if serverPolicyRecoveryPending(name) {
				return fmt.Errorf("interrupted policy application; run config policy apply to recover")
			}
		} else if err := recoverChanges(); err != nil {
			return err
		}
		return unlockGlobal()
	}
	// commit takes the global lock, plans again from the files as they are now and,
	// if that is what the connectivity test covered, writes it. Otherwise it lets go
	// of everything and says so, and the run starts over. The global lock stays
	// held to the end of the run in every other case, so a failure is recorded
	// before another run can move the state.
	//
	// With no tested pass it only confirms: the first plan, made without the lock,
	// ended in an error, which can be half of a transaction another run had under
	// way. Whatever the plan under the lock ends in is final -- an error, or a policy
	// already in effect. A plan that now works is not tested yet, so the run starts
	// over.
	commit := func(tested *policyPass) (result any, same bool, err error) {
		if err := acquire(); err != nil {
			return nil, true, err
		}
		attemptStale = nil
		if checkOnly {
			if serverPolicyRecoveryPending(name) {
				return nil, true, fmt.Errorf("interrupted policy application; run config policy apply to recover")
			}
		} else if err := recoverChanges(); err != nil {
			return nil, true, err
		}
		// Another coordinator or manual command may have changed these while the
		// discovery request and the test were in flight. Match and plan only from
		// fresh state.
		pass, release, err := evaluate(true)
		if err != nil {
			return nil, true, err
		}
		releaseSettings := sync.OnceFunc(release)
		defer releaseSettings()
		if pass.current != nil {
			return *pass.current, true, nil
		}
		if tested == nil || !pass.sameProbe(tested) {
			releaseSettings()
			if err := unlockGlobal(); err != nil {
				return nil, true, err
			}
			return nil, false, nil
		}
		changes, next := pass.changes, pass.next
		if checkOnly {
			paths := make([]string, 0, len(changes))
			for _, change := range changes {
				paths = append(paths, change.Path)
			}
			return struct {
				Policy  *serverpolicy.Policy `json:"policy"`
				Changes []string             `json:"changes"`
			}{pol, paths}, true, nil
		}
		if err = applyServerPolicyChangesFn(name, changes); err != nil {
			return nil, true, err
		}
		// Still under the settings locks taken above: the shared Codex configs this
		// commit left out are written now.
		pendingErr = drainSharedPolicyWrites(name, pass.locked)
		if latest, err := readServerPolicyState(name); err == nil {
			next = latest
		}
		fmt.Fprintln(os.Stderr, "Server policy applied. Running Claude Code and Codex sessions use the new settings on their next launch.")
		return serverPolicyStatus{Target: target, serverPolicyState: next}, true, nil
	}
	// The plan and the connectivity test take no lock: the new server may answer as
	// slowly as it likes, and every other profile's sync startup and policy run
	// waits on the global lock. A pass without it is only a read of the files, so a
	// profile with nothing to apply, or one that defers, ends without the test and
	// without sending its token anywhere -- an error or a deferral only once it is
	// confirmed under the lock, since a read without it can be torn.
	//
	// The commit then plans again under the lock, and the test stands only if it
	// covered what the commit writes. If another command changed the token or the
	// trust bundle meanwhile the run starts over once; changed again, it leaves the
	// policy to the next run rather than test under the lock.
	for round := 0; ; round++ {
		if err := settle(); err != nil {
			return nil, err
		}
		pass, _, err := evaluate(false)
		if err != nil {
			if result, final, err := commit(nil); final {
				return result, err
			}
			if round > 0 {
				return nil, errPolicySettingsChanged
			}
			continue
		}
		if pass.current != nil {
			return *pass.current, nil
		}
		// Probe exactly the trust bundle that will be committed, including preserved
		// user roots required by corporate TLS proxies.
		probePolicy := *pol
		probePolicy.CAPEM = string(pass.ca)
		if err = probeServerPolicy(ctx, pass.profile, probePolicy); err != nil {
			return nil, err
		}
		result, same, err := commit(pass)
		if same {
			return result, err
		}
		if round > 0 {
			return nil, errPolicySettingsChanged
		}
	}
}
