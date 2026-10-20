package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

func daemonProfileFlag(id string) (string, error) {
	if id == "default" {
		return "default", nil
	}
	if strings.HasPrefix(id, "named:") {
		name := strings.TrimPrefix(id, "named:")
		if err := profile.ValidateName(name); err == nil {
			return name, nil
		}
	}
	return "", errors.New("invalid stable profile ID")
}
func managedProfile(name string) (string, string) {
	if name == "" {
		name = os.Getenv("CCTRACE_PROFILE")
	}
	if name == "" || name == "default" {
		return "default", "default"
	}
	return name, profile.ProfileID(name)
}

func runCompatibleSync(dryRun bool, claudeDir string, watch, daemon, once, stop bool, interval time.Duration, name, email, endpoint string, local bool, ack string, finalize bool) error {
	if once || dryRun || finalize || !watch && !daemon && !stop {
		return runSync(dryRun, claudeDir, watch, daemon, once, stop, interval, name, email, endpoint, local, ack, finalize)
	}
	root := profile.DefaultDir()
	_, id := managedProfile(name)
	if stop {
		ctx, cancel := context.WithTimeout(context.Background(), syncLockWait)
		defer cancel()
		if name == "" && os.Getenv("CCTRACE_PROFILE") == "" {
			return localdaemon.Command(ctx, root, "shutdown", "")
		}
		return localdaemon.Command(ctx, root, "stop", id)
	}
	if daemon {
		return runManagedStart(claudeDir, interval, name, email, endpoint, local)
	}
	return runSupervisor(syncRunOptions{interval: interval, profileName: name, claudeDir: claudeDir, profileEmail: email, endpoint: endpoint, local: local, startAckFile: ack})
}

func runManagedStart(claudeDir string, interval time.Duration, name, email, endpoint string, local bool) error {
	root := profile.DefaultDir()
	resolved, id := managedProfile(name)
	hook := readHookPayload()
	if hook.TranscriptPath != "" {
		hookCollectFile = hook.TranscriptPath
	}
	if hook.HookEventName == "SessionEnd" && hook.SessionID != "" {
		legacyName := resolved
		if legacyName == "default" {
			legacyName = ""
		}
		recordSessionEnd(legacyName, claudeDir, hook)
	}
	if name != "" || os.Getenv("CCTRACE_PROFILE") != "" {
		entry, err := (profile.Registry{Root: root}).Load(resolved)
		if err != nil {
			return err
		}
		if entry.CollectionState != "enabled" {
			return errors.New("profile collection is disabled or unconfigured")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), syncLockWait)
	defer cancel()
	if running, err := localdaemon.Running(root); err == nil && running {
		if claudeDir != "" || email != "" || endpoint != "" || local {
			return errors.New("runtime overrides require stopping the local daemon first; configure the profile before restarting")
		}
		if name == "" && os.Getenv("CCTRACE_PROFILE") == "" {
			return nil
		}
		return localdaemon.Command(ctx, root, "start", id)
	}
	ack, err := createSyncTempPath("", "daemon-start-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(ack)
	args := []string{"sync", "--supervisor", "--interval", interval.String(), "--start-ack-file", ack}
	if name != "" {
		args = append(args, "--profile", name)
	}
	if claudeDir != "" {
		args = append(args, "--claude-dir", claudeDir)
	}
	if email != "" {
		args = append(args, "--profile-email", email)
	}
	if endpoint != "" {
		args = append(args, "--endpoint", endpoint)
	}
	if local {
		args = append(args, "--local")
	}
	if collect := hookCollectFile; collect != "" {
		args = append(args, "--collect-file", collect)
	}
	pid, err := spawnSyncProcessFn(args, "")
	if err != nil {
		return err
	}
	started, err := waitForSyncStartAckFn(ack, syncStartAckWait)
	if err != nil {
		return fmt.Errorf("wait for local daemon start (pid %d): %w", pid, err)
	}
	if started.Status == syncStartAckError {
		return fmt.Errorf("local daemon start: %s", started.Error)
	}
	if started.Status != syncStartAckStarted && started.Status != syncStartAckAlreadyRunning {
		return errors.New("local daemon start was not acknowledged")
	}
	if name != "" || os.Getenv("CCTRACE_PROFILE") != "" {
		if err := localdaemon.Command(ctx, root, "start", id); err != nil {
			return err
		}
	}
	fmt.Printf("  Local collection daemon running (pid %d)\n", pid)
	return nil
}

func runManagedFinalize(claudeDir, name, email, endpoint string, local bool) error {
	root := profile.DefaultDir()
	_, id := managedProfile(name)
	ctx, cancel := context.WithTimeout(context.Background(), syncFinalizeWait)
	defer cancel()
	if running, err := localdaemon.Running(root); err == nil && running {
		return localdaemon.Command(ctx, root, "collect", id)
	}
	// A SessionEnd hook still gets its bounded one-shot pass when no singleton
	// owner is running. It never stops the other profiles' long-running jobs.
	_, err := spawnSyncProcessFn(buildFinalizeWorkerArgs(claudeDir, name, email, endpoint, local), name)
	return err
}

func runManagedAction(ctx context.Context, action, name string, asJSON bool) error {
	root := profile.DefaultDir()
	if action == "status" {
		return runManagedStatus(os.Stdout, asJSON, time.Now())
	}
	ctx, cancel := context.WithTimeout(ctx, syncLockWait)
	defer cancel()
	if name != "" || os.Getenv("CCTRACE_PROFILE") != "" {
		_, id := managedProfile(name)
		return localdaemon.Command(ctx, root, "restart", id)
	}
	running, err := localdaemon.Running(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if running {
		if err := localdaemon.Command(ctx, root, "shutdown", ""); err != nil {
			return err
		}
	}
	return runManagedStart("", 30*time.Second, "", "", "", false)
}

func runSupervisor(opts syncRunOptions) error {
	root := profile.DefaultDir()
	_, id := managedProfile(opts.profileName)
	overrides := daemonOverrides{ProfileID: id, ClaudeDir: opts.claudeDir, Email: opts.profileEmail, Endpoint: opts.endpoint, Local: opts.local, CollectFile: hookCollectFile, Interval: opts.interval}
	load := func() ([]localdaemon.Spec, error) {
		return loadManagedProfiles(profile.Registry{Root: root}, profile.LegacyDesktopRoot(), overrides)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	daemonNotifySignals(sigs)
	defer signal.Stop(sigs)
	go func() {
		select {
		case <-ctx.Done():
		case <-sigs:
			cancel()
		}
	}()
	if syncLogToFile {
		defer installDaemonLog("")()
	}
	updated := make(chan struct{}, 1)
	updateDone := make(chan struct{})
	policyDone := make(chan struct{})
	updateStarted := false
	s := localdaemon.New(localdaemon.Config{Root: root, Version: version, Interval: opts.interval, Load: load, Logf: diagf})
	err := s.Run(ctx, func(rt localdaemon.Runtime) {
		_ = writeSyncStartAck(opts.startAckFile, syncStartAck{Status: syncStartAckStarted, InstanceID: rt.InstanceID})
		updateStarted = true
		go func() {
			defer close(policyDone)
			managedPolicyLoop(ctx, profile.Registry{Root: root}, watchUpdateCheckInterval)
		}()
		go func() {
			defer close(updateDone)
			if version != "dev" {
				managedUpdateLoop(ctx, profile.Registry{Root: root}, watchUpdateCheckInterval, func() { updated <- struct{}{}; cancel() })
			}
		}()
	})
	cancel()
	if updateStarted {
		<-updateDone
		<-policyDone
	}
	select {
	case <-updated:
		if err != nil {
			return err
		}
		executable, err := syncExecutablePathFn()
		if err != nil {
			return err
		}
		if err := reexecDaemonParentFn(executable); err != nil {
			return fmt.Errorf("local daemon update restart failed: %w", err)
		}
		return nil
	default:
	}
	if errors.Is(err, localdaemon.ErrAlreadyRunning) {
		_ = writeSyncStartAck(opts.startAckFile, syncStartAck{Status: syncStartAckAlreadyRunning})
		if opts.profileName != "" {
			controlCtx, controlCancel := context.WithTimeout(context.Background(), syncLockWait)
			defer controlCancel()
			return localdaemon.Command(controlCtx, root, "start", id)
		}
		return nil
	}
	if err != nil {
		_ = writeSyncStartAck(opts.startAckFile, syncStartAck{Status: syncStartAckError, Error: err.Error()})
	}
	return err
}

func loadManagedProfiles(registry profile.Registry, legacyRoot string, overrides daemonOverrides) ([]localdaemon.Spec, error) {
	conflicts, err := registry.MigrateDesktop(legacyRoot)
	if err != nil {
		return nil, err
	}
	blocked := map[string]bool{}
	for _, conflict := range conflicts {
		blocked[profile.ProfileID(conflict.Name)] = true
	}
	entries, err := registry.List()
	if err != nil {
		return nil, err
	}
	specs := make([]localdaemon.Spec, 0, len(entries))
	for _, entry := range entries {
		spec := profileJobSpec(entry, overrides)
		if blocked[entry.ID] {
			spec.BlockedReason = "migration_conflict"
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func managedUpdateLoop(ctx context.Context, registry profile.Registry, interval time.Duration, applied func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		entries, err := registry.Enabled()
		if err == nil {
			for _, entry := range entries {
				if ctx.Err() != nil {
					return
				}
				name := entry.Name
				if name == "default" {
					name = ""
				}
				if result := applyDaemonParentUpdateFn(ctx, name, "", false); result.Applied {
					applied()
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
