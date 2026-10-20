package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cctrace/internal/claudeauth"
	"cctrace/internal/codexlog"
	"cctrace/internal/codexsyncer"
	"cctrace/internal/envgen"
	"cctrace/internal/gjclog"
	"cctrace/internal/gjcsyncer"
	"cctrace/internal/omolog"
	"cctrace/internal/omosyncer"
	"cctrace/internal/profile"
	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
	"cctrace/internal/syncer"
	"cctrace/internal/usage"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
)

var errSyncAlreadyRunning = errors.New("another sync is already running")

const daemonParentReexecEnv = "CCTRACE_DAEMON_PARENT_REEXEC"

type daemonParentUpdateResult struct {
	ServerVersion string
	Applied       bool
}

// syncLogToFile mirrors the hidden --log-to-file flag. spawnSyncProcess sets it
// on every child it starts, which is exactly the set of processes whose stdout
// and stderr point at the crash file. A package var rather than another
// parameter on runSync's already long signature, matching the seams above.
var syncLogToFile bool

const syncLockWait = 35 * time.Second
const syncWatchStartWait = 250 * time.Millisecond

var watchUpdateCheckInterval = 5 * time.Minute

var (
	runSyncStartFn          = runManagedStart
	runSyncFinalizeFn       = runManagedFinalize
	runSyncFinalizeWorkerFn = runSyncFinalizeWorker
	runSyncReenrichFn       = runSyncReenrich
	runSyncAllFn            = runSyncAll
	syncLockWaitDuration    = syncLockWait
)

type syncRunOptions struct {
	dryRun         bool
	claudeDir      string
	watch          bool
	daemon         bool
	daemonOnce     bool
	stop           bool
	interval       time.Duration
	profileName    string
	profileEmail   string
	endpoint       string
	autoProfile    bool
	local          bool
	startAckFile   string
	finalizeWorker bool
	// includeContent is `sync reenrich --include-content`; see runSyncReenrich.
	includeContent bool
	supervisor     bool
	profileID      string
	// jsonOutput is `sync status --json`: the snapshot as the daemon wrote it.
	jsonOutput bool
}

func syncCmd() *cobra.Command {
	opts := syncRunOptions{}

	cmd := &cobra.Command{
		Use:   "sync [start|finalize|reenrich]",
		Short: "Sync Claude Code session logs to the trace server",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			action := ""
			if len(args) == 1 {
				action = args[0]
				if action != "start" && action != "finalize" && action != "reenrich" && action != "restart" && action != "status" {
					return fmt.Errorf("unknown sync action %q", action)
				}
			}
			if opts.includeContent && action != "reenrich" {
				return fmt.Errorf("--include-content applies only to 'cctrace sync reenrich'")
			}
			if opts.jsonOutput && action != "status" {
				return fmt.Errorf("--json applies only to 'cctrace sync status'")
			}
			if opts.profileID != "" {
				if opts.daemonOnce || opts.dryRun || opts.finalizeWorker || action == "reenrich" || action != "start" && action != "finalize" && action != "restart" && !opts.daemon && !opts.watch && !opts.stop && !opts.supervisor {
					return errors.New("profile-id is only supported for collection control")
				}
				name, err := daemonProfileFlag(opts.profileID)
				if err != nil {
					return err
				}
				if opts.profileName != "" && opts.profileName != name {
					return errors.New("profile and profile-id disagree")
				}
				opts.profileName = name
			}
			if opts.supervisor {
				return runSupervisor(opts)
			}
			if action == "restart" || action == "status" {
				return runManagedAction(cmd.Context(), action, opts.profileName, opts.jsonOutput)
			}

			if opts.autoProfile && opts.profileName == "" {
				resolvedDir := opts.claudeDir
				if resolvedDir == "" {
					resolvedDir = os.Getenv("CLAUDE_CONFIG_DIR")
				}
				if resolvedDir != "" {
					name, err := profile.ResolveByClaudeConfigDir(resolvedDir)
					if err != nil {
						return err
					}
					if name != "" {
						opts.profileName = name
					}
				}
			}

			if opts.finalizeWorker {
				return runSyncFinalizeWorkerFn(opts.claudeDir, opts.profileName, opts.profileEmail, opts.endpoint, opts.local)
			}

			switch action {
			case "start":
				return runSyncStartFn(opts.claudeDir, opts.interval, opts.profileName, opts.profileEmail, opts.endpoint, opts.local)
			case "finalize":
				return runSyncFinalizeFn(opts.claudeDir, opts.profileName, opts.profileEmail, opts.endpoint, opts.local)
			case "reenrich":
				return runSyncReenrichFn(opts.claudeDir, opts.profileName, opts.profileEmail, opts.endpoint, opts.local, opts.includeContent)
			}

			// No profile specified + one-shot mode: sync all profiles.
			if opts.profileName == "" && !opts.autoProfile && !opts.daemon && !opts.daemonOnce && !opts.watch && !opts.stop && opts.claudeDir == "" && opts.startAckFile == "" {
				return runSyncAllFn(opts.dryRun, opts.interval, opts.profileEmail, opts.endpoint, opts.local)
			}
			return runSyncFn(opts.dryRun, opts.claudeDir, opts.watch, opts.daemon, opts.daemonOnce, opts.stop, opts.interval, opts.profileName, opts.profileEmail, opts.endpoint, opts.local, opts.startAckFile, opts.finalizeWorker)
		},
	}

	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Show what would be synced without sending")
	cmd.Flags().StringVar(&opts.claudeDir, "claude-dir", "", "Claude config directory (default: ~/.claude)")
	cmd.Flags().BoolVar(&opts.watch, "watch", false, "Run continuously, polling for new records")
	cmd.Flags().BoolVar(&opts.daemon, "daemon", false, "Run sync in background as a daemon process")
	cmd.Flags().BoolVar(&opts.daemonOnce, "once", false, "With --daemon: sync once then exit (for SessionEnd hook)")
	cmd.Flags().BoolVar(&opts.stop, "stop", false, "Stop a running daemon")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "With status: print the daemon's snapshot as JSON instead of a table")
	cmd.Flags().DurationVar(&opts.interval, "interval", 30*time.Second, "Poll interval for --watch mode")
	cmd.Flags().StringVar(&opts.profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	cmd.Flags().BoolVar(&opts.autoProfile, "auto-profile", false, "Auto-select profile from CLAUDE_CONFIG_DIR environment variable")
	cmd.Flags().StringVar(&opts.profileEmail, "profile-email", "", "Override profile email (overrides CCTRACE_PROFILE_EMAIL env var)")
	cmd.Flags().StringVar(&opts.endpoint, "endpoint", "", "Override server HTTP endpoint (e.g. http://localhost:8080)")
	cmd.Flags().BoolVar(&opts.local, "local", false, "Use local cctraced endpoints from env/.env (or localhost defaults)")
	cmd.Flags().BoolVar(&opts.includeContent, "include-content", false, "With reenrich: also recover instruction files, hook output, system messages, answers, queued prompts, plans and recaps within the permitted collection range, under the current redaction options")
	cmd.Flags().StringVar(&opts.startAckFile, "start-ack-file", "", "Internal start acknowledgement file")
	cmd.Flags().StringVar(&hookCollectFile, "collect-file", "", "Internal: session file to collect from the start on the first pass")
	_ = cmd.Flags().MarkHidden("collect-file")
	cmd.Flags().BoolVar(&opts.finalizeWorker, "finalize-worker", false, "Internal finalize worker mode")
	cmd.Flags().BoolVar(&syncPolicyHandoff, "policy-handoff", false, "Internal policy restart")
	_ = cmd.Flags().MarkHidden("policy-handoff")
	cmd.Flags().BoolVar(&syncLogToFile, "log-to-file", false, "Internal: route diagnostics to the profile log file")
	cmd.Flags().BoolVar(&opts.supervisor, "supervisor", false, "Internal singleton local collection daemon")
	cmd.Flags().StringVar(&opts.profileID, "profile-id", "", "Stable profile ID for collection control")
	_ = cmd.Flags().MarkHidden("supervisor")
	_ = cmd.Flags().MarkHidden("start-ack-file")
	_ = cmd.Flags().MarkHidden("finalize-worker")
	_ = cmd.Flags().MarkHidden("log-to-file")
	return cmd
}

// hookCollectFile names the session file the first pass must read whole rather than
// skip to EOF. Set from the hidden --collect-file flag that the daemon parent adds
// when a Claude Code hook gave it the running session's transcript path. In the
// daemon parent it is that path, read from the hook's stdin -- or, in a
// replacement started after a self-update, from the same flag.
var hookCollectFile string

// newSyncClient builds the upload client with the profile's privacy policy already
// installed.
//
// It exists so the policy cannot be attached in two places and forgotten in a
// third. Three call sites construct this client, and a redaction setting that
// applies to two of them is worse than none: the operator sees the flag, some
// records honour it, and nothing says which.
//
// The profile's CA is installed here for the same reason. A CA file that cannot
// be read fails construction, so the pass that wanted it fails and says why,
// instead of every upload failing on a certificate nobody named.
func newSyncClient(p *profile.Profile, endpoint, version, profileName string) (*syncer.Client, error) {
	tr, err := serverTransport(p.Server.CACertFile)
	if err != nil {
		return nil, err
	}
	client := syncer.NewClient(endpoint, p.Server.AuthToken, version)
	_ = client.SetTransferDiagnostics(filepath.Join(syncProfileDir(profileName), "transfer-diagnostics.json"))
	if tr != nil {
		client.SetTransport(tr)
	}
	client.SetRedactPolicy(syncer.RedactPolicy{
		UserPrompts: p.Options.RedactUserPrompts,
		ToolDetails: p.Options.RedactToolDetails,
	})
	client.SetExcludedAccounts(p.Options.ExcludeAccounts)
	// Installed here for the same reason as the policy above: the self-update
	// state is per install, and a sync path that forgets to report it makes that
	// install indistinguishable from a current one (#750).
	client.SetUpdateStallReporter(func() *store.ClientUpdateStall { return updateStallReport(profileName) })
	return client, nil
}

// updateStallReport is what this install says about its own self-update.
//
// It never returns nil: a build that carries this function can report, and the
// empty report is how it says there is nothing wrong. Nil is reserved for builds
// that predate the field, and the server keeps the two apart -- reading silence
// as health is what left one install a month behind with nobody able to see it.
func updateStallReport(profileName string) *store.ClientUpdateStall {
	st, ok := readUpdateStall(profileName)
	if !ok {
		return &store.ClientUpdateStall{}
	}
	return &store.ClientUpdateStall{
		TargetVersion: st.Version,
		Consecutive:   st.Consecutive,
		FirstFailedAt: st.FirstAt,
		LastFailedAt:  st.LastAt,
		Reason:        st.Reason,
	}
}

func runSync(dryRun bool, claudeDir string, watch bool, daemon bool, daemonOnce bool, stop bool, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool, startAckFile string, finalizeWorker bool) error {
	// Resolve profile name: flag > env var
	if profileName == "" {
		profileName = os.Getenv("CCTRACE_PROFILE")
	}

	// Before anything else, including the lock: "another daemon is already
	// running" is ordinary activity and belongs in sync.log. Reporting it before
	// the logger exists would put it in the crash file instead, where routine
	// output both obscures crash evidence and hastens the truncation that
	// discards it.
	if syncLogToFile {
		defer installDaemonLog(profileName)()
	}

	if stop {
		runtimeState, ok, err := readSyncRuntime(profileName)
		if err != nil {
			return fmt.Errorf("read watcher runtime: %w", err)
		}
		if !ok {
			lockFree, lockErr := isSyncLockFree(profileName)
			if lockErr != nil {
				return fmt.Errorf("check sync lock: %w", lockErr)
			}
			if lockFree {
				_ = os.Remove(pidFilePath(profileName))
				_ = os.Remove(runtimeFilePath(profileName))
				_ = os.Remove(stopRequestFilePath(profileName))
				return fmt.Errorf("no running daemon found")
			}
			return fmt.Errorf("running daemon does not support graceful stop; restart with current binary")
		}
		if err := writeSyncStopRequest(profileName, syncStopRequest{
			InstanceID:  runtimeState.InstanceID,
			RequestedAt: time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("write stop request: %w", err)
		}
		stopped, err := waitForWatcherExitFn(profileName, runtimeState.InstanceID, syncLockWait)
		if err != nil {
			return fmt.Errorf("wait for daemon shutdown: %w", err)
		}
		if !stopped {
			return fmt.Errorf("timed out waiting for daemon shutdown")
		}
		fmt.Printf("  Stopped sync daemon (pid %d)\n", runtimeState.PID)
		return nil
	}

	if syncPolicyHandoff && (!watch || startAckFile == "" || daemon || dryRun || local || endpointOverride != "" || finalizeWorker) {
		return errors.New("invalid policy handoff invocation")
	}
	// The hook's payload is read first, and once: before the self-update check,
	// which can hand this process over to a replacement whose stdin this one has
	// drained, and before anything that takes a lock. A session end goes to the
	// pending directory here because the --once child exits at once when a daemon
	// holds the sync lock; the daemon's next flush sends it then, and the child's
	// own flush otherwise. It is recorded before the policy barrier too: a startup
	// that gives up waiting on the barrier would otherwise lose the one report the
	// hook makes.
	if daemon {
		hook := readHookPayload()
		if hook.HookEventName == "SessionEnd" && hook.SessionID != "" {
			recordSessionEnd(profileName, claudeDir, hook)
		}
		// Kept where a replacement parent finds it on its command line
		// (daemonParentReexecArgs); it is the running session's transcript,
		// which the child below collects from the start.
		if hook.TranscriptPath != "" {
			hookCollectFile = hook.TranscriptPath
		}
	}
	// A foreground `sync --watch` has no daemon parent to update it. Check before
	// collection starts and replace this exact invocation, preserving all flags,
	// environment and stream semantics. The marker is consumed by the replacement
	// process so a stale version response cannot create a re-exec loop.
	//
	// The check, and the download behind it, run before the policy barrier: they
	// take as long as the server and the network like, and the barrier is the
	// global policy lock every other profile's startup and policy run waits on.
	var lastWatchUpdateCheck time.Time
	if watch && version != "dev" && !syncPolicyHandoff {
		lastWatchUpdateCheck = time.Now()
		reexeced := os.Getenv(daemonParentReexecEnv) == "1"
		if reexeced {
			_ = os.Unsetenv(daemonParentReexecEnv)
		}
		executable, executableErr := syncExecutablePathFn()
		update := applyDaemonParentUpdateFn(context.Background(), profileName, endpointOverride, local)
		if update.Applied && !reexeced {
			if executableErr != nil {
				diagf("update: resolve watch executable: %v; continuing with running process", executableErr)
			} else if err := reexecDaemonParentFn(executable); err == nil {
				return nil
			} else {
				diagf("update: restart watch process: %v; continuing with running process", err)
			}
		}
	}

	releasePolicyBarrier, barrierErr := policySyncStartupBarrier(context.Background(), profileName, startAckFile, syncPolicyHandoff)
	if barrierErr != nil {
		return barrierErr
	}
	releaseBarrier := func() {
		if releasePolicyBarrier != nil {
			releasePolicyBarrier()
			releasePolicyBarrier = nil
		}
	}
	defer releaseBarrier()
	// The barrier makes a startup wait out a policy application and its recovery.
	// The daemon parent has nothing the barrier protects to do after that: it checks
	// for an update and starts the watcher, which takes the barrier for itself.
	if daemon {
		releaseBarrier()
	}

	var lastPolicyCheck time.Time
	if syncPolicyHandoff {
		lastPolicyCheck = time.Now()
	}
	if !daemon && !syncPolicyHandoff {
		if watch {
			maybeStartPolicyCoordinator(profileName, true, dryRun, local, finalizeWorker, endpointOverride, &lastPolicyCheck)
		} else {
			// Register before acquiring the sync lock: deferred lock cleanup runs
			// first, so one-shot policy checks never race their own collection.
			defer maybeStartPolicyCoordinator(profileName, true, dryRun, local, finalizeWorker, endpointOverride, &lastPolicyCheck)
		}
	}

	if daemon {
		reexeced := os.Getenv(daemonParentReexecEnv) == "1"
		if reexeced {
			_ = os.Unsetenv(daemonParentReexecEnv)
		}
		executable, executableErr := syncExecutablePathFn()
		update := applyDaemonParentUpdateFn(context.Background(), profileName, endpointOverride, local)
		if update.Applied && !reexeced {
			// These two report the one outcome #458 could not distinguish: the
			// binary on disk was replaced, but this process is still the old one.
			// They used to write straight to stderr because no daemon parent
			// installed the log; the hook command now carries --log-to-file, so it
			// does, and raw stderr here is discarded by the caller that ran the
			// hook. diagf still writes the identical bytes to stderr when the log
			// is not installed, so an interactive run is unchanged.
			if executableErr != nil {
				diagf("update: resolve daemon parent executable: %v; continuing with compatibility fallback", executableErr)
			} else if err := reexecDaemonParentFn(executable); err == nil {
				return nil
			} else {
				diagf("update: restart daemon parent: %v; continuing with compatibility fallback", err)
			}
		}

		// #103: if a watch child is running an OLDER version, gracefully stop it so
		// the fresh spawn below re-execs the now-current binary (new code).
		if !daemonOnce {
			replaceOutdatedWatchChild(profileName, update.ServerVersion)
		}

		args := []string{"sync"}
		if daemonOnce {
			args = append(args, "--once")
		} else {
			args = append(args, "--watch", "--interval", interval.String())
		}
		if claudeDir != "" {
			args = append(args, "--claude-dir", claudeDir)
		}
		if profileName != "" {
			args = append(args, "--profile", profileName)
		}
		if profileEmail != "" {
			args = append(args, "--profile-email", profileEmail)
		}
		if endpointOverride != "" {
			args = append(args, "--endpoint", endpointOverride)
		}
		if local {
			args = append(args, "--local")
		}
		// A SessionStart hook arrives with the running session's transcript path on
		// stdin. Handing it to the child exempts that one file from the first-run
		// skip: the skip is meant for history that predates the install, and the
		// session the user is sitting in is not history. Only the parent can read
		// this — the child's stdin is redirected away.
		if hookCollectFile != "" {
			args = append(args, "--collect-file", hookCollectFile)
		}
		pid, err := spawnSyncProcessFn(args, profileName)
		if err != nil {
			return err
		}

		fmt.Printf("  Sync daemon started (pid %d)\n", pid)
		fmt.Printf("  Log: %s\n", syncLogPath(profileName))
		fmt.Printf("  Stop: cctrace sync --stop\n")
		return nil
	}

	// Load profile
	var p *profile.Profile
	var err error
	if profileName != "" {
		p, err = profile.LoadNamed(profileName)
		if err != nil {
			return err
		}
	} else {
		if !profile.Exists() {
			return fmt.Errorf("no profile found; run 'cctrace init' first")
		}
		p, err = profile.Load()
		if err != nil {
			return fmt.Errorf("load profile: %w", err)
		}
	}

	processIOSource := *p // immutable persisted consent before explicit runtime endpoint overrides
	if (p.HomesVersion != 0 || p.Homes != nil) && p.CollectionEnabled("claude") {
		homes := p.TransmissionHomes("claude")
		if len(homes) != 1 || claudeDir != "" && filepath.Clean(claudeDir) != homes[0] {
			return fmt.Errorf("explicit multi-home collection requires the shared managed daemon")
		}
		claudeDir = homes[0]
	}
	if !p.CollectionEnabled("claude") {
		// Codex telemetry has its own direct OTLP/HTTP exporter and must not
		// depend on Claude JSONL collection being enabled. This lets upgrades
		// repair an existing Codex installation even for users who opted out of
		// Claude session-log sync.
		if !syncPolicyHandoff && p.HomesVersion == 0 && p.Homes == nil {
			autoMigrateCodex(p, profileName, p.Server.Endpoint)
		}
		fmt.Println("  Session log sync is disabled in profile. Run 'cctrace init' to enable.")
		return nil
	}

	runtimeEndpoint := p.Server.Endpoint
	if local {
		runtimeEndpoint = applyLocalDevEndpoints(p, &endpointOverride)
	}
	if runtimeEndpoint == "" {
		return fmt.Errorf("no server endpoint configured; run 'cctrace init' to set one")
	}

	if !syncPolicyHandoff && p.HomesVersion == 0 && p.Homes == nil {
		autoMigrateCodex(p, profileName, runtimeEndpoint)
	}

	resolvedClaudeDir := resolveClaudeDir(p, claudeDir)
	resolvedEmail := resolveProfileEmail(p, profileEmail)
	resolvedUserID := resolveUserID(p)

	if dryRun {
		return runSyncDry(resolvedClaudeDir, resolvedEmail, p, newLineGate(profileName, true))
	}

	// The daemon lets one profile read a source at a time, and a run outside it
	// holds nothing that keeps it off a source another profile owns. So it reads
	// what the daemon allows the profile, line by line, and a profile that is
	// allowed nothing uploads nothing: the session end the hook's parent recorded
	// waits in the pending directory.
	var gate *lineGate
	if !watch || daemonOnce {
		gate = newLineGate(profileName, false)
		defer gate.close()
		if gate.allHeld() {
			label, _ := managedProfile(profileName)
			diagf("profile=%s [sync] every source of this profile is held by another profile (duplicate_route); nothing was collected or uploaded", label)
			return nil
		}
	}

	lockWait := syncLockWaitDuration
	if daemonOnce {
		lockWait = 0
	}
	var fl *flock.Flock
	var watcherRuntime *syncRuntime
	if watch {
		fl, err = acquireWatchSyncLock(profileName)
	} else {
		fl, err = acquireSyncLock(profileName, lockWait)
	}
	if err != nil {
		if errors.Is(err, errSyncAlreadyRunning) {
			if watch {
				_ = writeSyncStartAck(startAckFile, syncStartAck{Status: syncStartAckAlreadyRunning})
				diagf("[sync] another daemon is already running, exiting")
				return nil
			}
			if daemonOnce {
				return nil
			}
			return errSyncAlreadyRunning
		}
		return fmt.Errorf("acquire lock: %w", err)
	}
	// Keep cleanup tied to the current lock/runtime values. A Windows update
	// handoff must release both before spawning the replacement, while a failed
	// handoff reacquires them and continues collecting in this process.
	defer func() {
		if watcherRuntime != nil {
			finishWatchRuntime(profileName, watcherRuntime)
		}
		if fl != nil {
			_ = fl.Unlock()
		}
	}()

	// Self-heal Claude settings.json when an upgraded binary would generate
	// different hooks/env (content-hash mismatch). Best-effort and non-fatal.
	// Runs on the watch child and one-shot paths — the daemon parent already
	// returned and dry-run/stop exited earlier; skip the finalize worker.
	if !finalizeWorker && !syncPolicyHandoff {
		// Self-heal the settings.json for the claude dir actually being synced:
		// the --claude-dir flag wins over the profile's configured dir.
		hp := *p
		hp.ClaudeConfigDir = resolvedClaudeDir
		if changed, herr := envgen.EnsureClaudeSettingsCurrent(&hp); herr != nil {
			diagf("[settings] self-heal skipped: %v", herr)
		} else if changed {
			diagf("[settings] Claude settings.json refreshed to current cctrace version.")
		}
	}

	if watch {
		watcherRuntime = &syncRuntime{
			PID:        os.Getpid(),
			InstanceID: newSyncInstanceID(),
			StartedAt:  time.Now().UTC(),
			Version:    version,
			Interval:   interval, ClaudeDir: resolvedClaudeDir, ProfileEmail: resolvedEmail, EndpointOverride: endpointOverride, Local: local,
		}
		if err := startWatchRuntime(profileName, watcherRuntime); err != nil {
			_ = writeSyncStartAck(startAckFile, syncStartAck{Status: syncStartAckError, Error: err.Error()})
			return fmt.Errorf("write pid file: %w", err)
		}
	}

	// What the barrier protects is done: the profile was read and the settings
	// repaired from it under the barrier, and the sync lock and, for a watcher, its
	// runtime are published, so a policy application now finds this startup and
	// stops or hands it over. Loading cursors, building the client and the update
	// check below can take longer and need no barrier. A policy handoff keeps its
	// per-profile startup lock until it has acknowledged its start.
	if !syncPolicyHandoff {
		releaseBarrier()
	}

	statePath := syncer.DefaultStatePath()
	if profileName != "" {
		if dir, err := profile.NamedDir(profileName); err == nil {
			statePath = filepath.Join(dir, "sync-state.json")
		}
	}
	state, err := syncer.LoadState(statePath)
	if err != nil {
		if watch {
			_ = writeSyncStartAck(startAckFile, syncStartAck{Status: syncStartAckError, Error: fmt.Sprintf("load sync state: %v", err)})
		}
		return fmt.Errorf("load sync state: %w", err)
	}
	// Before the first pass, so SyncOnce sees a file it already knows and reads it
	// from the start. Seeding does not persist, so state.IsNew() stays true and
	// every other pre-existing file is still skipped.
	seedCollectFile(state, hookCollectFile)

	// Resolve sync endpoint: flag > sync_endpoint > endpoint (fallback)
	ep := p.Server.SyncEndpoint
	if ep == "" {
		ep = p.Server.Endpoint
	}
	if endpointOverride != "" {
		ep = endpointOverride
	}
	client, err := newSyncClient(p, ep, version, profileName)
	if err != nil {
		if watch {
			_ = writeSyncStartAck(startAckFile, syncStartAck{Status: syncStartAckError, Error: err.Error()})
		}
		return err
	}

	ctx := context.Background()
	var stopCh <-chan struct{}
	var localAnalysisAPI *localAnalysis
	if watch {
		ctx, stopCh = newWatchSyncContext(ctx, profileName, watcherRuntime.InstanceID)
		localAnalysisAPI = startLocalAnalysis(ctx, profileName, p, resolvedClaudeDir)
		defer func() { localAnalysisAPI.Close() }()
	}

	// Check for updates before syncing (skip in daemon/watch/dry-run mode).
	if version != "dev" && !daemon && !watch && !dryRun && !finalizeWorker {
		applyUpdateIfAvailable(ctx, client, p.Server.CACertFile, ep, profileName)
	}

	s := syncer.New(resolvedClaudeDir, resolvedEmail, resolvedUserID, state, client, p.Options.CollectRepositoryPrefixes)
	// The Claude folder is one line. When another profile reads it, this run has no
	// Claude pass, and does not send that folder's session ends either: the
	// profile that reads the folder sends them, under its own filters.
	claudeHeld := len(gate.apply(ctx, "claude", []string{resolvedClaudeDir})) == 0
	if watch {
		_ = writeSyncStartAck(startAckFile, syncStartAck{Status: syncStartAckStarted, InstanceID: watcherRuntime.InstanceID})
	}

	releaseBarrier()
	if syncPolicyHandoff {
		os.Args = withoutPolicyHandoffArgs(os.Args)
		syncPolicyHandoff = false
	}

	total := 0
	initialErr := error(nil)
	n, err := 0, error(nil)
	if !claudeHeld {
		n, err = s.SyncOnce(ctx)
	}
	if err != nil {
		if watch && errors.Is(err, context.Canceled) {
			return nil
		}
		if !watch {
			return fmt.Errorf("sync: %w", err)
		}
		initialErr = err
	} else {
		total = n
		// A first pass where every file failed reports no error, so without this
		// the watch loop would start with its backoff cleared and hammer a server
		// it has already failed to reach once per interval.
		if stats := s.LastPass(); passStalled(stats) {
			initialErr = stats.LastErr
		}
	}

	// Codex syncer (opt-in via CCTRACE_CODEX_SYNC=true or profile.Options.CodexSyncEnabled)
	var cs *codexsyncer.CodexSyncer
	if p.CollectionEnabled("codex") || p.HomesVersion == 0 && os.Getenv("CCTRACE_CODEX_SYNC") == "true" {
		codexDirs := resolveCodexScanDirs(diagWriter(), p)
		codexHeld := false
		if all := codexDirs; gate != nil {
			codexDirs = gate.apply(ctx, "codex", all)
			codexHeld = len(codexDirs) == 0 && len(all) > 0
		}
		codexStatePath := codexsyncer.StatePathForProfile(profileName)
		codexState, cerr := syncer.LoadState(codexStatePath)
		if codexHeld {
			// Every Codex folder is another profile's to read.
		} else if cerr != nil {
			diagf("[codex-sync] load state: %v", cerr)
		} else {
			cs = codexsyncer.New(codexDirs, resolvedEmail, resolvedUserID, codexState, client, p.Options.CollectRepositoryPrefixes)
			cs.SetRedactedHomes(p.CodexHome)
			// The Codex session snapshots go in this process's one flush, under
			// the accounts of the Codex homes this syncer reads.
			s.ServeCodexSnapshots(codexState, codexDirs)
			cn, cerr := cs.SyncOnce(ctx)
			if cerr != nil {
				if !watch || !errors.Is(cerr, context.Canceled) {
					diagf("[codex-sync] %v", cerr)
				}
			} else {
				total += cn
			}
		}
	}

	// Gjc syncer (opt-in via CCTRACE_GJC_SYNC=true or profile.Options.GjcSyncEnabled)
	var gs *gjcsyncer.GjcSyncer
	if os.Getenv("CCTRACE_GJC_SYNC") == "true" || p.Options.GjcSyncEnabled {
		gjcDirs := resolveGjcScanDirs(diagWriter(), p)
		gjcHeld := false
		if all := gjcDirs; gate != nil {
			gjcDirs = gate.apply(ctx, "gjc", all)
			gjcHeld = len(gjcDirs) == 0 && len(all) > 0
		}
		gjcState, gerr := syncer.LoadState(gjcStatePathForProfile(profileName))
		if gjcHeld {
			// Every gjc folder is another profile's to read.
		} else if gerr != nil {
			diagf("[gjc-sync] load state: %v", gerr)
		} else {
			gs = gjcsyncer.New(gjcDirs, resolvedEmail, resolvedUserID, gjcState, client, p.Options.CollectRepositoryPrefixes)
			gn, gerr := gs.SyncOnce(ctx)
			if gerr != nil {
				if !watch || !errors.Is(gerr, context.Canceled) {
					diagf("[gjc-sync] %v", gerr)
				}
			} else {
				total += gn
			}
		}
	}

	// Omo syncer (opt-in via CCTRACE_OMO_SYNC=true or profile.Options.OmoSyncEnabled)
	var om *omosyncer.OmoSyncer
	if os.Getenv("CCTRACE_OMO_SYNC") == "true" || p.Options.OmoSyncEnabled {
		omoState, oerr := syncer.LoadState(omoStatePathForProfile(profileName))
		omoDirs := resolveOmoScanDirs(diagWriter(), p)
		omoHeld := false
		if all := omoDirs; gate != nil {
			omoDirs = gate.apply(ctx, "omo", all)
			omoHeld = len(omoDirs) == 0 && len(all) > 0
		}
		if omoHeld {
			// Every omo folder is another profile's to read.
		} else if oerr != nil {
			diagf("[omo-sync] load state: %v", oerr)
		} else {
			om = omosyncer.New(omoDirs, resolvedEmail, resolvedUserID, omoState, client, p.Options.CollectRepositoryPrefixes)
			on, oerr := om.SyncOnce(ctx)
			if oerr != nil {
				if !watch || !errors.Is(oerr, context.Canceled) {
					diagf("[omo-sync] %v", oerr)
				}
			} else {
				total += on
			}
		}
	}

	infof("Synced %d records", total)

	// Session observations go once after the pass, whether or not it sent
	// anything: the heartbeat says the collector is running, which a failing
	// record sync does not change. A one-shot run -- `cctrace sync`, each profile
	// of runSyncAll, the --once child the SessionEnd hook starts, the finalize
	// worker -- gets no other chance; watch mode then keeps its own interval
	// below. Every one of them holds the sync lock here, so no two flush at once.
	if claudeHeld {
		// The Claude folder's items are the holder's to send. This run's own
		// Codex snapshots and its heartbeat still go.
		s.LeaveClaudeHome()
	}
	switch {
	case watch:
		flushSessionObservations(ctx, s)
	default:
		flushSessionObservationsBeforeExit(ctx, s)
	}
	if err := flushProcessIOQueue(ctx, processIOQueue(profileName), client, resolvedEmail, resolvedUserID, p.Options.ExcludeAccounts, &processIOSource); err != nil {
		diagf("[process-io] observation upload: %v", err)
	}
	lastObservationFlush := time.Now()

	if !watch {
		return nil
	}

	// Start jitter to spread sync traffic across multiple daemons
	startJitter := time.Duration(rand.Int64N(int64(interval)))
	select {
	case <-stopCh:
		return nil
	case <-time.After(startJitter):
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	cumulative := total
	// lastSendOK dates the stalled-pass line. It starts at the loop, not at the
	// epoch, so the first failing pass says "no success for 0s" rather than
	// claiming a stall this process cannot vouch for. How long collection has
	// really been failing is the state file's business (TransportFailure), which
	// outlives this process.
	lastSendOK := time.Now()

	const usagePollInterval = 5 * time.Minute
	var lastUsagePoll time.Time

	var bo backoffState
	if initialErr != nil {
		bo.recordFailure()
		var re *syncer.RetryableError
		if errors.As(initialErr, &re) && re.RetryAfter > time.Until(bo.nextRetry) {
			bo.nextRetry = time.Now().Add(re.RetryAfter)
		}
		// A stall gets the line that names the counts, because a first pass whose
		// every file failed is the shape #712 arrived in and "initial sync failed"
		// does not say that nothing at all got through.
		if stats := s.LastPass(); passStalled(stats) {
			bo.honourRetryAfter(stats.RetryAfter)
			progressf("%s", stalledPassLine(stats, 0, time.Until(bo.nextRetry).Round(time.Second)))
		} else {
			progressf("[sync] initial sync failed, retry in %v", time.Until(bo.nextRetry).Round(time.Second))
		}
	} else {
		progressf("[sync] pass: +%d records (total %d), watching every %s", total, cumulative, interval)
	}
	for {
		select {
		case <-stopCh:
			progressf("[sync] stopped (%d records total)\n", cumulative)
			return nil
		case <-ticker.C:
			maybeStartPolicyCoordinator(profileName, watch, dryRun, local, finalizeWorker, endpointOverride, &lastPolicyCheck)
			// Direct watch deployments have no recurring daemon parent invocation.
			// Check at a pass boundary, never while a sync pass is in flight. A
			// successful signed update releases runtime ownership before re-exec so
			// Windows replacements do not lose the single-instance lock race.
			if version != "dev" && time.Since(lastWatchUpdateCheck) >= watchUpdateCheckInterval {
				lastWatchUpdateCheck = time.Now()
				executable, executableErr := syncExecutablePathFn()
				update := applyDaemonParentUpdateFn(ctx, profileName, endpointOverride, local)
				if update.Applied {
					if executableErr != nil {
						diagf("[update] resolve watch executable: %v; continuing with running process", executableErr)
					} else {
						localAnalysisAPI.Close()
						runtimeToRestore := watcherRuntime
						finishWatchRuntime(profileName, watcherRuntime)
						watcherRuntime = nil
						if err := fl.Unlock(); err != nil {
							return fmt.Errorf("release sync lock for update handoff: %w", err)
						}
						fl = nil
						if err := reexecDaemonParentFn(executable); err == nil {
							return nil
						} else {
							diagf("[update] restart watch process: %v; resuming collection", err)
						}

						// Re-exec failed after ownership was released. Reclaim it rather
						// than silently interrupting collection. If another watcher won
						// the lock, it owns collection now and this process can exit.
						fl, err = acquireWatchSyncLock(profileName)
						if errors.Is(err, errSyncAlreadyRunning) {
							return nil
						}
						if err != nil {
							return fmt.Errorf("reacquire sync lock after failed update handoff: %w", err)
						}
						watcherRuntime = runtimeToRestore
						if err := startWatchRuntime(profileName, watcherRuntime); err != nil {
							return fmt.Errorf("restore watch runtime after failed update handoff: %w", err)
						}
						localAnalysisAPI = startLocalAnalysis(ctx, profileName, p, resolvedClaudeDir)
					}
				}
			}

			// Quota polling: fetch and send to server every usagePollInterval.
			//
			// This sits before the sync branches, and outside them, because the
			// quota API is an independent self-contained path. It used to sit
			// after bo.recordSuccess(), so a session-log sync that was failing —
			// or merely backing off — stopped usage collection along with it.
			// The account keeps burning down either way, so that interval was
			// not deferred, it was lost: the API reports the window's current
			// state, never its history.
			//
			// The stamp advances whether or not the attempt worked. Advancing it
			// only on success left the interval permanently satisfied while
			// anything was failing, so the poll ran on every tick instead of every
			// five minutes — once a second under the installed hook. Against a 429
			// that is self-perpetuating: the retries keep the limit tripped. One
			// client logged 1,293,935 such attempts.
			//
			if time.Since(lastUsagePoll) >= usagePollInterval {
				fetchAndSendQuota(ctx, client, resolvedClaudeDir, resolvedEmail, resolvedUserID)
				// The Codex five-hour window rides the same interval and the
				// same reasoning: it is not deferred by a failing session sync
				// because a rate-limit window reports its current state and
				// never its history, so an interval skipped is an interval
				// lost. Unlike the call above it starts a child process, so it
				// sits inside the interval check rather than beside it, and the
				// stamp advances either way — advancing it only on success is
				// what turned five minutes into one second last time.
				if cs != nil {
					cs.PollAppServerQuota(ctx)
				}
				lastUsagePoll = time.Now()
			}

			// Session observations ride their own interval beside the quota poll,
			// outside the sync branches below, for the same reasons: a failing or
			// backing-off record sync must not stop the collector's heartbeat, and
			// the stamp advances whether or not the flush worked, so a failure
			// costs one attempt per interval rather than one per tick. The syncer
			// parks the flush itself on a 404, a 429 or a refused envelope.
			if time.Since(lastObservationFlush) >= observationFlushInterval {
				flushSessionObservations(ctx, s)
				if err := flushProcessIOQueue(ctx, processIOQueue(profileName), client, resolvedEmail, resolvedUserID, p.Options.ExcludeAccounts, &processIOSource); err != nil {
					diagf("[process-io] observation upload: %v", err)
				}
				lastObservationFlush = time.Now()
			}

			if bo.shouldSkip() {
				continue
			}
			n, err := s.SyncOnce(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					progressf("[sync] stopped (%d records total)\n", cumulative)
					return nil
				}
				bo.recordFailure()
				var re *syncer.RetryableError
				if errors.As(err, &re) && re.RetryAfter > time.Until(bo.nextRetry) {
					bo.nextRetry = time.Now().Add(re.RetryAfter)
				}
				progressf("[sync] failed, retry in %v", time.Until(bo.nextRetry).Round(time.Second))
				continue
			}
			// A pass can complete without error and still have sent nothing,
			// because a per-file failure is logged and skipped rather than
			// returned -- so a pass in which every file failed used to land here,
			// on recordSuccess, with the backoff disarmed. That is how one daemon
			// retried once a second for two days while its line said "34079
			// records synced" (#712).
			stats := s.LastPass()
			if passStalled(stats) {
				// Probed against the home actually being synced, the way the
				// settings self-heal above resolves it: --claude-dir wins over the
				// profile, and the successor hook lives in that home's
				// settings.json.
				successor := *p
				successor.ClaudeConfigDir = resolvedClaudeDir
				bo.recordFailure()
				bo.honourRetryAfter(stats.RetryAfter)
				stalledFor := time.Since(lastSendOK)
				progressf("%s", stalledPassLine(stats, stalledFor,
					time.Until(bo.nextRetry).Round(time.Second)))
				if releaseLockForStall(state.TransportFailure, stalledFor, envgen.SyncStartHookInstalled(&successor)) {
					progressf("[sync] no successful transfer for %s; releasing the sync lock so the next daemon can take over -- offsets are untouched, restart now with: cctrace sync --daemon",
						stalledFor.Round(time.Second))
					return nil
				}
				continue
			}
			bo.recordSuccess()
			lastSendOK = time.Now()
			added := n
			if cs != nil {
				cn, cerr := cs.SyncOnce(ctx)
				added += noteSatelliteResult("codex-sync", cn, cerr)
			}
			if gs != nil {
				gn, gerr := gs.SyncOnce(ctx)
				added += noteSatelliteResult("gjc-sync", gn, gerr)
			}
			if om != nil {
				on, oerr := om.SyncOnce(ctx)
				added += noteSatelliteResult("omo-sync", on, oerr)
			}
			cumulative += added
			progressf("[sync] pass: +%d records (total %d), watching every %s", added, cumulative, interval)
		}
	}
}

// applyDaemonParentUpdateIfAvailable self-updates the binary in place when the
// server has a newer version. The caller uses Applied to replace its own old code
// before constructing child arguments (#208), and ServerVersion to replace an
// outdated running watch child (#103). A zero result means the version is
// unknown/unreachable/"dev", or an update was needed but failed.
func applyDaemonParentUpdateIfAvailable(ctx context.Context, profileName string, endpointOverride string, local bool) daemonParentUpdateResult {
	if version == "dev" {
		fmt.Println("  [update] dev build; self-update skipped")
		return daemonParentUpdateResult{}
	}

	p, err := loadSyncProfile(profileName)
	if err != nil {
		return daemonParentUpdateResult{}
	}

	resolvedEndpointOverride := endpointOverride
	if local {
		applyLocalDevEndpoints(p, &resolvedEndpointOverride)
	}

	ep := profileHTTPAPIEndpoint(p)
	if resolvedEndpointOverride != "" {
		ep = resolvedEndpointOverride
	}
	if ep == "" {
		return daemonParentUpdateResult{}
	}

	client, err := newSyncClient(p, ep, version, profileName)
	if err != nil {
		diagf("update: %v", err)
		return daemonParentUpdateResult{}
	}
	serverVer, err := client.CheckVersion(ctx)
	if err != nil || serverVer == "" || serverVer == "dev" {
		return daemonParentUpdateResult{}
	}
	result := daemonParentUpdateResult{ServerVersion: serverVer}
	if !semverGT(serverVer, version) {
		clearUpdateStall(profileName)
		return result
	}
	// A self-update that failed is a standing condition, not a transient one: the
	// production case was an install directory owned by root, where the
	// replacement file cannot be created at all. Retrying on the next five-minute
	// tick fetched the same artifact 867 times in a week and changed nothing
	// (#623), so the next attempt is spaced out instead. A manual `cctrace sync`
	// still tries at once, which is the escape hatch for someone who has just
	// fixed their install.
	//
	// The early return is a zero result, matching the failure path below rather
	// than the up-to-date one above: the binary on disk is still old, so handing
	// the caller a ServerVersion would have it stop and respawn the watch child on
	// every check.
	now := updateClock()
	if !updateStallReady(profileName, serverVer, now) {
		return daemonParentUpdateResult{}
	}
	fmt.Printf("  Updating cctrace %s → %s ...\n", version, serverVer)
	if err := downloadAndApplyUpdate(ctx, p.Server.CACertFile, ep, serverVer); err != nil {
		// See applyUpdateIfAvailable: the watch child reaches this same
		// function before it starts collecting, and its stderr is the crash
		// file. diagf keeps the interactive `--daemon` parent on stderr and
		// puts the child's failure in sync.log (#458).
		diagf("update: %v", err)
		noteUpdateFailure(profileName, serverVer, err.Error(), now)
		return daemonParentUpdateResult{}
	}
	clearUpdateStall(profileName)
	result.Applied = true
	return result
}

// replaceOutdatedWatchChild gracefully stops a running watch child whose stamped
// version is STRICTLY older than serverVer, so the daemon parent's subsequent
// spawn re-execs the now-current binary (#103). It is a no-op — returning false —
// when serverVer is empty, no child is running, the child is already current, or
// the child version is empty/unparseable (semverGT is false), which avoids a
// restart loop with pre-stamp legacy daemons. It never hard-kills: if the child
// does not exit, the caller's spawn is a no-op under the single-instance lock, so
// no duplicate daemon is created.
func replaceOutdatedWatchChild(profileName string, serverVer string) bool {
	if serverVer == "" {
		return false
	}
	rt, ok, err := readSyncRuntime(profileName)
	if err != nil || !ok || rt == nil || !semverGT(serverVer, rt.Version) {
		return false
	}
	fmt.Printf("  Replacing outdated sync daemon (%s → %s)...\n", rt.Version, serverVer)
	_ = writeSyncStopRequest(profileName, syncStopRequest{InstanceID: rt.InstanceID, RequestedAt: time.Now().UTC()})
	_, _ = waitForWatcherExitFn(profileName, rt.InstanceID, daemonRespawnStopWait)
	return true
}

// resolveClaudeDir resolves the Claude config directory with precedence:
// 1. --claude-dir flag, 2. profile.ClaudeConfigDir, 3. sessionlog default.
func resolveClaudeDir(p *profile.Profile, flagValue string) string {
	if p.HomesVersion != 0 || p.Homes != nil {
		allowed := p.TransmissionHomes("claude")
		chosen := flagValue
		if chosen == "" {
			chosen = p.ClaudeConfigDir
		}
		for _, home := range allowed {
			if home == filepath.Clean(chosen) {
				return home
			}
		}
		if chosen != "" {
			return ""
		}
		if len(allowed) == 1 {
			return allowed[0]
		}
		return ""
	}

	if flagValue != "" {
		return flagValue
	}
	if p.ClaudeConfigDir != "" {
		return p.ClaudeConfigDir
	}
	return sessionlog.DefaultClaudeDir()
}

// resolveProfileEmail resolves the profile email with precedence:
// 1. --profile-email flag, 2. CCTRACE_PROFILE_EMAIL env var, 3. profile email.
func resolveProfileEmail(p *profile.Profile, flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("CCTRACE_PROFILE_EMAIL"); env != "" {
		return env
	}
	return p.User.Email
}

// resolveUserID resolves the user ID from the profile.
func resolveUserID(p *profile.Profile) string {
	return p.User.ID
}

// fetchAndSendQuota fetches the Anthropic OAuth usage and posts it to the server.
//
// Failures are reported and dropped rather than signalled back. The caller polls
// on a fixed interval and usage.Fetch caches failures — honouring Retry-After on
// a 429 — so a broken endpoint costs one log line per interval instead of a
// retry loop. Returning success would invite branching on it again, which is
// what collapsed the interval into a per-tick retry in the first place.
// The config dir is threaded through because credentials and identity are both
// per-dir. Fetching with the default keychain entry while labelling with a
// profile's identity does not leave a field blank — it fills it with the wrong
// account, confidently.
func fetchAndSendQuota(ctx context.Context, client *syncer.Client, claudeDir, profileEmail, userID string) {
	u, err := usage.Fetch(claudeDir)
	if err != nil {
		progressf("[quota] fetch failed: %v", err)
		return
	}
	q := &syncer.QuotaPayload{
		ProfileEmail: profileEmail,
		UserID:       userID,
	}
	// Windows are read through Window(kind), which prefers the typed limits[]
	// array and falls back to the fixed fields. five_hour lost its resets_at in
	// the current response and only limits[] still carries it.
	if w := u.Window(usage.KindSession); w != nil {
		q.FiveHourPct = w.Utilization
		q.FiveHourResetsAt = w.ResetsAt
	}
	if w := u.Window(usage.KindWeeklyAll); w != nil {
		q.SevenDayPct = w.Utilization
		q.SevenDayResetsAt = w.ResetsAt
	}
	// seven_day_sonnet is left empty on purpose. The API returns null for it and
	// the nearest live entry is weekly_scoped, whose scoped model was observed
	// as "Fable" — writing that into a column named after Sonnet would make the
	// name a lie. The scoped window is carried in the history rows instead.
	if err := client.SendQuota(ctx, q); err != nil {
		progressf("[quota] send failed: %v", err)
	}

	sendQuotaSamples(ctx, client, claudeDir, profileEmail, u)
}

// observationFlushInterval is how often watch mode sends the collector heartbeat
// and the dirty session snapshots. Var, not const, so tests can shrink it.
var observationFlushInterval = 60 * time.Second

// flushSessionObservations is watch mode's flush of the heartbeat and the dirty
// session snapshots. A failure is reported and dropped, as the quota poll's is:
// what did not go stays dirty for the next flush, and the syncer parks itself
// where the server asked it to wait.
func flushSessionObservations(ctx context.Context, s *syncer.Syncer) {
	if err := s.FlushObservations(ctx); err != nil && ctx.Err() == nil {
		progressf("[observations] %v", err)
	}
}

// oneShotObservationFlushTimeout bounds the flush of a run about to exit. It
// runs with the sync lock held, so an observations route that hangs would hold
// off the next run the hook starts; what it did not send stays dirty for the
// next flush. Var, not const, so tests can shrink it.
var oneShotObservationFlushTimeout = 10 * time.Second

// flushSessionObservationsBeforeExit is the flush of a run that exits right
// after it: `cctrace sync`, each profile of runSyncAll, the --once child, the
// finalize worker and `cctrace sync reenrich`. An older server's 404 is not
// reported -- the park it starts ends with the process, and there is nothing to
// do about it -- and any other failure is printed as a whole line on stderr, not
// as watch mode's status line, which a terminal leaves open for the shell prompt
// to land on.
func flushSessionObservationsBeforeExit(ctx context.Context, s *syncer.Syncer) {
	ctx, cancel := context.WithTimeout(ctx, oneShotObservationFlushTimeout)
	defer cancel()
	if err := s.FlushObservations(ctx); err != nil && !errors.Is(err, syncer.ErrSessionObservationsUnsupported) {
		diagf("[observations] %v", err)
	}
}

// quotaSamplesUnsupported latches once an older server has answered 404, so the
// history attempt is made at most once per process rather than every five
// minutes for the life of the daemon.
var quotaSamplesUnsupported atomic.Bool

var quotaOwnerSamplesUnsupported atomic.Bool

// sendQuotaSamples posts the same reading as history rows.
//
// This is additive to the snapshot above. The snapshot is what an older server
// understands, so it keeps being sent and a 404 here costs the history only —
// not the current value.
//
// The account is read from the same config dir the token came from. Reading one
// from the profile and the other from the default location is what turns a
// missing label into a wrong one.
func sendQuotaSamples(ctx context.Context, client *syncer.Client, claudeDir, profileEmail string, u *usage.Response) {
	acct, err := claudeauth.ReadAccount(claudeDir)
	if err != nil {
		progressf("[quota] account read failed: %v", err)
		return
	}
	if acct.AccountUUID == "" {
		// Unattributable rather than broken: a home that never logged in has no
		// account, and inventing one would put this reading on another
		// account's line.
		//
		// Saying so is not the same as acting on it. This returned silently, and
		// silence made the state unobservable from either end: the snapshot above
		// keeps arriving, so the server sees a live client with no history and
		// cannot tell an unattributable one from an old, an idle, or an
		// uninstalled one -- while on the machine itself the log named the
		// condition one line up, for the error case, and named nothing here. It
		// is reported at the same per-tick rate as that neighbour because the
		// rate is what put it in reach of the `tail` the troubleshooting docs
		// ask for; a once-per-process line would have been scrolled away by the
		// watch loop long before anyone looked.
		if !quotaSamplesUnsupported.Load() {
			progressf("[quota] no account uuid for %s; history skipped", claudeDir)
		}
		return
	}

	if err := usage.CheckAccountBinding(claudeDir, acct.AccountUUID, u); err != nil {
		// The whole tick goes, not the odd-looking window. Every field in these
		// rows is a real reading; the only thing wrong with them is whose they
		// are, so there is nothing here or downstream that could pick the bad
		// ones out afterwards. Until now this condition wrote the rows and said
		// nothing at all, which is how it took a chart artefact to find it.
		progressf("[quota] %v; history for this tick discarded", err)
		return
	}
	ownerSamples := u.OwnerSamples(acct.AccountUUID)
	if len(ownerSamples) > 0 && !quotaOwnerSamplesUnsupported.Load() {
		switch err := client.SendQuotaOwnerSamples(ctx, ownerSamples); {
		case err == nil:
		case errors.Is(err, syncer.ErrQuotaOwnerSamplesUnsupported):
			quotaOwnerSamplesUnsupported.Store(true)
			progressf("[quota] server has no private usage endpoint; private upload skipped")
		default:
			progressf("[quota] private sample upload failed: %v", err)
		}
	}
	if quotaSamplesUnsupported.Load() {
		return
	}

	samples := u.Samples(acct.AccountUUID, acct.EmailAddress, profileEmail)
	if len(samples) == 0 {
		return
	}
	switch err := client.SendQuotaSamples(ctx, samples); {
	case err == nil:
	case errors.Is(err, syncer.ErrQuotaSamplesUnsupported):
		quotaSamplesUnsupported.Store(true)
		progressf("[quota] server has no history endpoint; snapshots only")
	default:
		progressf("[quota] history send failed: %v", err)
	}
}

// runSyncAll syncs the default profile and all named profiles sequentially.
func runSyncAll(dryRun bool, interval time.Duration, profileEmail string, endpointOverride string, local bool) error {
	names, err := profile.ListNamed()
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}

	profiles := []string{""} // default first
	profiles = append(profiles, names...)

	var totalSynced int
	var errs []string
	for _, name := range profiles {
		label := "default"
		if name != "" {
			label = name
		}
		if err := runSyncFn(dryRun, "", false, false, false, false, interval, name, profileEmail, endpointOverride, local, "", false); err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", label, err))
		} else {
			totalSynced++
		}
	}
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, " ", e)
		}
	}
	_ = totalSynced
	return nil
}

func lockFilePath(profileName string) string {
	if profileName != "" {
		if dir, err := profile.NamedDir(profileName); err == nil {
			return filepath.Join(dir, "sync.lock")
		}
	}
	return filepath.Join(profile.DefaultDir(), "sync.lock")
}

func pidFilePath(profileName string) string {
	if profileName != "" {
		if dir, err := profile.NamedDir(profileName); err == nil {
			return filepath.Join(dir, "sync.pid")
		}
	}
	return filepath.Join(profile.DefaultDir(), "sync.pid")
}

func writePID(path string, pid int) error {
	return os.WriteFile(path, []byte(fmt.Sprintf("%d", pid)), 0644)
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return 0, fmt.Errorf("invalid pid file: %w", err)
	}
	return pid, nil
}

func acquireSyncLock(profileName string, wait time.Duration) (*flock.Flock, error) {
	return acquireFileLock(lockFilePath(profileName), wait)
}

func waitForSyncUnlock(profileName string, wait time.Duration) error {
	return waitForFileUnlock(lockFilePath(profileName), wait)
}

func acquireFileLock(path string, wait time.Duration) (*flock.Flock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	fl := flock.New(path)
	deadline := time.Now().Add(wait)
	for {
		locked, err := fl.TryLock()
		if err != nil {
			return nil, err
		}
		if locked {
			return fl, nil
		}
		if wait <= 0 || time.Now().After(deadline) {
			return nil, errSyncAlreadyRunning
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitForFileUnlock(path string, wait time.Duration) error {
	fl, err := acquireFileLock(path, wait)
	if err != nil {
		return err
	}
	return fl.Unlock()
}

func newWatchSyncContext(parent context.Context, profileName string, instanceID string) (context.Context, <-chan struct{}) {
	ctx, cancel := context.WithCancel(parent)
	sigs := make(chan os.Signal, 1)
	stopped := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			signal.Stop(sigs)
			cancel()
			close(stopped)
		})
	}
	daemonNotifySignals(sigs)
	go func() {
		select {
		case <-sigs:
			stop()
		case <-ctx.Done():
			stop()
		}
	}()
	go func() {
		ticker := time.NewTicker(syncPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				req, ok, err := readSyncStopRequest(profileName)
				if err != nil || !ok {
					continue
				}
				if req.InstanceID == instanceID {
					stop()
					return
				}
			}
		}
	}()
	return ctx, stopped
}

func acquireWatchSyncLock(profileName string) (*flock.Flock, error) {
	fl, err := acquireSyncLock(profileName, 0)
	if err == nil {
		return fl, nil
	}
	if !errors.Is(err, errSyncAlreadyRunning) {
		return nil, err
	}

	// Avoid treating transient lock probes (e.g. isSyncLockFree) as "already running".
	// If a runtime file exists, a watcher is very likely active: fail fast.
	if _, ok, rtErr := readSyncRuntime(profileName); rtErr != nil {
		return nil, rtErr
	} else if ok {
		return nil, errSyncAlreadyRunning
	}

	return acquireSyncLock(profileName, syncWatchStartWait)
}

func runSyncDry(claudeDir, profileEmail string, p *profile.Profile, gate *lineGate) error {
	if len(gate.apply(context.Background(), "claude", []string{claudeDir})) == 0 {
		fmt.Printf("\n  Claude sessions are held by another profile; not scanned\n")
		printCodexDryRun(os.Stdout, p, gate)
		printGjcDryRun(os.Stdout, p, gate)
		printOmoDryRun(os.Stdout, p, gate)
		return nil
	}
	files, err := sessionlog.FindJSONLFiles(claudeDir)
	if err != nil {
		return fmt.Errorf("find jsonl files: %w", err)
	}

	statePath := syncer.DefaultStatePath()
	state, _ := syncer.LoadState(statePath)

	total := 0
	for _, f := range files {
		offset := state.GetOffset(f)
		records, _, err := sessionlog.ScanFile(f, offset)
		if err != nil {
			fmt.Printf("  [SKIP] %s: %v\n", f, err)
			continue
		}
		if len(records) > 0 {
			total += len(records)
			fmt.Printf("  [NEW]  %s (+%d records)\n", f, len(records))
		}
	}
	fmt.Printf("\n  Total new records: %d (dry run, not sent)\n", total)
	printCodexDryRun(os.Stdout, p, gate)
	printGjcDryRun(os.Stdout, p, gate)
	printOmoDryRun(os.Stdout, p, gate)
	return nil
}

// printCodexDryRun reports which Codex homes a real sync would scan and how
// many session files each holds, so a misconfigured options.codex_dirs is
// visible without running an actual sync.
func printCodexDryRun(w io.Writer, p *profile.Profile, gate *lineGate) {
	if p == nil {
		return
	}
	if !p.CollectionEnabled("codex") && (p.HomesVersion != 0 || os.Getenv("CCTRACE_CODEX_SYNC") != "true") {
		if len(p.Options.CodexDirs) > 0 {
			fmt.Fprintf(w, "\n  Codex sync is disabled; %d configured codex dir(s) would not be scanned\n", len(p.Options.CodexDirs))
		}
		return
	}
	all := resolveCodexScanDirs(w, p)
	dirs := gate.apply(context.Background(), "codex", all)
	fmt.Fprintf(w, "\n  Codex homes scanned: %d\n", len(dirs))
	if held := len(all) - len(dirs); held > 0 {
		fmt.Fprintf(w, "    %d held by another profile; not scanned\n", held)
	}
	for _, dir := range dirs {
		display := dir
		if isExplicitCodexHome(p, dir) {
			display = "[explicit Codex home]"
		}
		files, err := codexlog.FindJSONLFiles(dir)
		if err != nil {
			if display == "[explicit Codex home]" {
				fmt.Fprintln(w, "    [SKIP] explicit Codex home could not be scanned")
			} else {
				fmt.Fprintf(w, "    [SKIP] %s: %v\n", display, err)
			}
			continue
		}
		fmt.Fprintf(w, "    %s (%d session files)\n", display, len(files))
	}
}

// gjcStatePathForProfile returns the gjc syncer state path for the given
// profile, mirroring codexsyncer.StatePathForProfile: named profiles get
// their own per-profile state file so concurrent syncs don't share read
// offsets. Defined here rather than in internal/gjcsyncer because that
// package (already finished and verified) exposes no such helper.
func gjcStatePathForProfile(profileName string) string {
	if profileName != "" {
		if dir, err := profile.NamedDir(profileName); err == nil {
			return filepath.Join(dir, "gjc-sync-state.json")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cctrace", "gjc-sync-state.json")
}

// omoStatePathForProfile returns the omo syncer state path for the given
// profile. omosyncer.DefaultStatePath covers only the default (unnamed)
// profile; named profiles get their own state file here, matching the other
// syncers.
func omoStatePathForProfile(profileName string) string {
	if profileName != "" {
		if dir, err := profile.NamedDir(profileName); err == nil {
			return filepath.Join(dir, "omo-sync-state.json")
		}
	}
	return omosyncer.DefaultStatePath()
}

// resolveGjcScanDirs returns every gjc home directory that should be
// scanned: the default home (~/.gjc) plus the profile's configured extra
// dirs, deduplicated by absolute path. It reports (via w) any configured
// dir that does not exist, mirroring resolveCodexScanDirs above: gjc has
// no ResolveScanDirs helper of its own since internal/gjclog is already
// finished and verified.
func resolveGjcScanDirs(w io.Writer, p *profile.Profile) []string {
	candidates := make([]string, 0, len(p.Options.GjcDirs)+1)
	candidates = append(candidates, gjclog.DefaultGjcDir())
	candidates = append(candidates, p.Options.GjcDirs...)

	seen := make(map[string]struct{}, len(candidates))
	dirs := make([]string, 0, len(candidates))
	for _, raw := range candidates {
		c := strings.TrimSpace(raw)
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(codexlog.ExpandHome(c))
		if err != nil {
			fmt.Fprintf(w, "  [gjc-sync] configured gjc dir is invalid, skipped: %s\n", c)
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		if fi, statErr := os.Stat(abs); statErr != nil || !fi.IsDir() {
			fmt.Fprintf(w, "  [gjc-sync] configured gjc dir is not an existing directory, skipped: %s\n", c)
			continue
		}
		dirs = append(dirs, abs)
	}
	return dirs
}

// resolveOmoScanDirs returns every omo home directory that should be scanned:
// the default home (~/.omo) plus the profile's configured extra dirs,
// deduplicated by absolute path. Same shape as resolveGjcScanDirs above -- omo
// went without this for longer than the others, which is how a whole session
// root stayed unscanned (#247, #250, #280).
func resolveOmoScanDirs(w io.Writer, p *profile.Profile) []string {
	candidates := make([]string, 0, len(p.Options.OmoDirs)+1)
	candidates = append(candidates, omolog.DefaultOmoDir())
	candidates = append(candidates, p.Options.OmoDirs...)

	seen := make(map[string]struct{}, len(candidates))
	dirs := make([]string, 0, len(candidates))
	for _, raw := range candidates {
		c := strings.TrimSpace(raw)
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(codexlog.ExpandHome(c))
		if err != nil {
			fmt.Fprintf(w, "  [omo-sync] configured omo dir is invalid, skipped: %s\n", c)
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		if fi, statErr := os.Stat(abs); statErr != nil || !fi.IsDir() {
			fmt.Fprintf(w, "  [omo-sync] configured omo dir is not an existing directory, skipped: %s\n", c)
			continue
		}
		dirs = append(dirs, abs)
	}
	return dirs
}

// printGjcDryRun reports which gjc homes a real sync would scan and how many
// session files each holds, so a misconfigured options.gjc_dirs is visible
// without running an actual sync.
func printGjcDryRun(w io.Writer, p *profile.Profile, gate *lineGate) {
	if p == nil {
		return
	}
	if os.Getenv("CCTRACE_GJC_SYNC") != "true" && !p.Options.GjcSyncEnabled {
		if len(p.Options.GjcDirs) > 0 {
			fmt.Fprintf(w, "\n  Gjc sync is disabled; %d configured gjc dir(s) would not be scanned\n", len(p.Options.GjcDirs))
		}
		return
	}
	all := resolveGjcScanDirs(w, p)
	dirs := gate.apply(context.Background(), "gjc", all)
	fmt.Fprintf(w, "\n  Gjc homes scanned: %d\n", len(dirs))
	if held := len(all) - len(dirs); held > 0 {
		fmt.Fprintf(w, "    %d held by another profile; not scanned\n", held)
	}
	for _, dir := range dirs {
		files, err := gjclog.FindSessionFiles(dir)
		if err != nil {
			fmt.Fprintf(w, "    [SKIP] %s: %v\n", dir, err)
			continue
		}
		fmt.Fprintf(w, "    %s (%d session files)\n", dir, len(files))
	}
}

// printOmoDryRun reports whether the omo home a real sync would scan holds
// any session files, so a disabled or empty setup is visible without running
// an actual sync. Omo, unlike Codex and gjc, only ever scans a single home.
func printOmoDryRun(w io.Writer, p *profile.Profile, gate *lineGate) {
	if p == nil {
		return
	}
	if os.Getenv("CCTRACE_OMO_SYNC") != "true" && !p.Options.OmoSyncEnabled {
		if len(p.Options.OmoDirs) > 0 {
			fmt.Fprintf(w, "\n  Omo sync is disabled; %d configured omo dir(s) would not be scanned\n", len(p.Options.OmoDirs))
		}
		return
	}
	all := resolveOmoScanDirs(w, p)
	dirs := gate.apply(context.Background(), "omo", all)
	fmt.Fprintf(w, "\n  Omo homes scanned: %d\n", len(dirs))
	if held := len(all) - len(dirs); held > 0 {
		fmt.Fprintf(w, "    %d held by another profile; not scanned\n", held)
	}
	for _, dir := range dirs {
		files, err := omolog.FindSessionFiles(dir)
		if err != nil {
			fmt.Fprintf(w, "    [SKIP] %s: %v\n", dir, err)
			continue
		}
		fmt.Fprintf(w, "    %s (%d session files)\n", dir, len(files))
	}
}
