package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

func managedLegacyName(name string) string {
	if name == "default" {
		return ""
	}
	return name
}

// startingManagedJobKey marks a context as belonging to a managed job on its way
// into its start, with the profile name it starts.
type startingManagedJobKey struct{}

// withStartingManagedJob marks ctx as the start of the managed job of profile name.
func withStartingManagedJob(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, startingManagedJobKey{}, name)
}

// startingManagedJob reports whether ctx is the start of the managed job of
// profile name: work done for it runs inside the supervisor, in the job's own
// goroutine, so it must not wait on a supervisor command the job itself would
// have to answer.
func startingManagedJob(ctx context.Context, name string) bool {
	starting, ok := ctx.Value(startingManagedJobKey{}).(string)
	return ok && starting == name
}

func managedPolicyLoop(ctx context.Context, registry profile.Registry, interval time.Duration) {
	last := make(map[string]time.Time)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		entries, err := registry.Enabled()
		if err == nil {
			for _, entry := range entries {
				checked := last[entry.ID]
				maybeStartPolicyCoordinator(managedLegacyName(entry.Name), true, false, false, false, "", &checked)
				last[entry.ID] = checked
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func managedPolicyCommand(ctx context.Context, name string, rt *syncRuntime, action string) error {
	root := profile.DefaultDir()
	if rt.SupervisorRoot != root {
		return errors.New("policy supervisor root does not match this profile registry")
	}
	owner, err := localdaemon.ReadRuntime(root)
	if err != nil || owner.PID != rt.PID || owner.StartedAt.After(rt.StartedAt) {
		return errors.New("policy supervisor ownership could not be verified")
	}
	ctx, cancel := policyStepCtx(ctx, syncLockWait)
	defer cancel()
	if name == "" {
		name = "default"
	}
	return localdaemon.Command(ctx, root, action, profile.ProfileID(name))
}

func init() {
	prepareManagedProfile = func(ctx context.Context, entry profile.Entry) (func(), error) {
		name := managedLegacyName(entry.Name)
		release, err := policySyncStartupBarrier(withStartingManagedJob(ctx, name), name, "", false)
		if err != nil {
			return nil, err
		}
		// The job's spec was built from the profile as it was when the supervisor
		// loaded it. A policy that committed while the job waited for the barrier
		// has rewritten that profile, and the job must not start on the old spec:
		// its client, endpoint and cursors are the old ones. The supervisor
		// retries, and loads the moved profile for the retry.
		current, err := (profile.Registry{Root: profile.DefaultDir()}).Load(entry.Name)
		if err != nil {
			release()
			return nil, err
		}
		if current.Revision != entry.Revision {
			release()
			return nil, fmt.Errorf("profile changed while waiting for the policy lock: %w", localdaemon.ErrPolicyLockBusy)
		}
		return release, nil
	}
	startManagedProfileRuntime = func(ctx context.Context, entry profile.Entry, dir string) (func(), error) {
		if !entry.Profile.Options.LocalAnalysisEnabled {
			return func() {}, nil
		}
		name := managedLegacyName(entry.Name)
		policy, err := readLocalPolicy(name)
		if err != nil {
			return nil, err
		}
		if !policy.Enabled {
			return func() {}, nil
		}
		api := startLocalAnalysis(ctx, name, entry.Profile, dir)
		if api == nil {
			return nil, errors.New("local analysis could not start")
		}
		return api.Close, nil
	}
}
