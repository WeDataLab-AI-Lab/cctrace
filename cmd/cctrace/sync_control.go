package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cctrace/internal/atomicfile"
	"cctrace/internal/profile"
)

const (
	syncStartAckWait = 2 * time.Second
	syncFinalizeWait = 2 * time.Second
	syncPollInterval = 50 * time.Millisecond
	// daemonRespawnStopWait bounds how long the daemon parent waits for an
	// outdated watch child to exit before spawning a fresh one (#103).
	daemonRespawnStopWait      = 5 * time.Second
	syncStartAckStarted        = "started"
	syncStartAckAlreadyRunning = "already-running"
	syncStartAckError          = "error"
)

type syncStartAck struct {
	Status     string `json:"status"`
	InstanceID string `json:"instance_id,omitempty"`
	Error      string `json:"error,omitempty"`
}

type syncRuntime struct {
	PID        int       `json:"pid"`
	InstanceID string    `json:"instance_id"`
	StartedAt  time.Time `json:"started_at"`
	// Version stamps the build version of the running watch child so a daemon
	// parent can detect an outdated child and replace it after a self-update (#103).
	Version string `json:"version,omitempty"`
}

type syncExit struct {
	InstanceID string    `json:"instance_id"`
	ExitedAt   time.Time `json:"exited_at"`
}

type syncStopRequest struct {
	InstanceID  string    `json:"instance_id"`
	RequestedAt time.Time `json:"requested_at"`
}

var (
	spawnSyncProcessFn        = spawnSyncProcess
	waitForSyncStartAckFn     = waitForSyncStartAck
	waitForWatcherExitFn      = waitForWatcherExit
	runSyncFn                 = runSync
	syncExecutablePathFn      = os.Executable
	startProcessFn            = os.StartProcess
	releaseProcessFn          = (*os.Process).Release
	applyDaemonParentUpdateFn = applyDaemonParentUpdateIfAvailable
	reexecDaemonParentFn      = reexecDaemonParent
)

func daemonParentReexecArgs(executable string) []string {
	return append([]string{executable}, os.Args[1:]...)
}

func daemonParentReexecEnvironment() []string {
	prefix := daemonParentReexecEnv + "="
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if len(entry) < len(prefix) || !strings.EqualFold(entry[:len(prefix)], prefix) {
			env = append(env, entry)
		}
	}
	return append(env, prefix+"1")
}

func runSyncStart(claudeDir string, interval time.Duration, profileName string, profileEmail string, endpointOverride string, local bool) error {
	p, err := loadSyncProfile(profileName)
	if err != nil {
		return err
	}
	if !p.Options.SyncEnabled {
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

	ackPath, err := createSyncTempPath(profileName, "sync-start-ack-*.json")
	if err != nil {
		return fmt.Errorf("create start acknowledgement path: %w", err)
	}
	defer os.Remove(ackPath)

	args := []string{"sync", "--watch", "--interval", interval.String(), "--start-ack-file", ackPath}
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

	pid, err := spawnSyncProcessFn(args, profileName)
	if err != nil {
		return err
	}

	ack, err := waitForSyncStartAckFn(ackPath, syncStartAckWait)
	if err != nil {
		return fmt.Errorf("wait for sync start acknowledgement (pid %d): %w", pid, err)
	}

	switch ack.Status {
	case syncStartAckStarted:
		fmt.Println("cctrace sync started")
		return nil
	case syncStartAckAlreadyRunning:
		fmt.Println("cctrace sync already running")
		return nil
	case syncStartAckError:
		if ack.Error == "" {
			return errors.New("sync start failed")
		}
		return errors.New(ack.Error)
	default:
		return fmt.Errorf("unknown sync start acknowledgement status %q", ack.Status)
	}
}

func runSyncFinalize(claudeDir string, profileName string, profileEmail string, endpointOverride string, local bool) error {
	runtimeState, _, err := readSyncRuntime(profileName)
	if err != nil {
		return fmt.Errorf("read watcher runtime: %w", err)
	}

	if runtimeState != nil {
		if err := writeSyncStopRequest(profileName, syncStopRequest{
			InstanceID:  runtimeState.InstanceID,
			RequestedAt: time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("write stop request: %w", err)
		}
	}

	stopped, err := waitForWatcherExitFn(profileName, runtimeInstanceID(runtimeState), syncFinalizeWait)
	if err != nil {
		return err
	}
	if _, err := spawnSyncProcessFn(buildFinalizeWorkerArgs(claudeDir, profileName, profileEmail, endpointOverride, local), profileName); err != nil {
		return err
	}
	if stopped {
		fmt.Println("cctrace sync stopped")
	} else {
		fmt.Println("cctrace sync still running")
	}
	return nil
}

func runSyncFinalizeWorker(claudeDir string, profileName string, profileEmail string, endpointOverride string, local bool) error {
	fl, err := acquireFileLock(finalizeLockFilePath(profileName), 0)
	if err != nil {
		if errors.Is(err, errSyncAlreadyRunning) {
			return nil
		}
		return fmt.Errorf("acquire finalize lock: %w", err)
	}
	defer fl.Unlock()

	return runSyncFn(false, claudeDir, false, false, false, false, 30*time.Second, profileName, profileEmail, endpointOverride, local, "", true)
}

func loadSyncProfile(profileName string) (*profile.Profile, error) {
	if profileName != "" {
		return profile.LoadNamed(profileName)
	}
	if !profile.Exists() {
		return nil, fmt.Errorf("no profile found; run 'cctrace init' first")
	}
	p, err := profile.Load()
	if err != nil {
		return nil, fmt.Errorf("load profile: %w", err)
	}
	return p, nil
}

func buildFinalizeWorkerArgs(claudeDir string, profileName string, profileEmail string, endpointOverride string, local bool) []string {
	args := []string{"sync", "--finalize-worker"}
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
	return args
}

func spawnSyncProcess(args []string, profileName string) (int, error) {
	exe, err := syncExecutablePathFn()
	if err != nil {
		return 0, fmt.Errorf("resolve executable: %w", err)
	}
	if err := os.MkdirAll(syncProfileDir(profileName), 0700); err != nil {
		return 0, fmt.Errorf("create sync profile dir: %w", err)
	}

	// sync.log itself is owned by the child's rotating writer (internal/synclog).
	// Handing the child a second, inherited descriptor for the same path would
	// survive rotation and keep appending to the renamed inode, scattering output
	// across backups. Create the file so the path the daemon advertises exists,
	// then hand it over.
	logFile, err := os.OpenFile(syncLogPath(profileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return 0, fmt.Errorf("open log file: %w", err)
	}
	logFile.Close()

	// Raw stdout/stderr carry only what the Go runtime writes directly — panics
	// and fatal errors. Pointing them at os.DevNull would lose crash causes, so
	// they get their own small file instead.
	crashFile, err := openSyncCrashLog(profileName)
	if err != nil {
		return 0, fmt.Errorf("open crash log: %w", err)
	}

	// Set here rather than by each caller that builds args: this is the one place
	// that decides a child's streams go to the crash file, so it is also the
	// place that must tell the child to log elsewhere. Splitting the two lets a
	// new spawn path forget the flag and quietly fill the crash log with routine
	// output.
	args = append(args, "--log-to-file")

	proc, err := startProcessFn(exe, append([]string{exe}, args...), &os.ProcAttr{
		Dir:   "/",
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, crashFile, crashFile},
		Sys:   daemonSysProcAttr(),
	})
	if err != nil {
		crashFile.Close()
		return 0, fmt.Errorf("start process: %w", err)
	}
	crashFile.Close()
	pid := proc.Pid
	_ = releaseProcessFn(proc)
	return pid, nil
}

func syncProfileDir(profileName string) string {
	if profileName != "" {
		if dir, err := profile.NamedDir(profileName); err == nil {
			return dir
		}
	}
	return profile.DefaultDir()
}

func syncLogPath(profileName string) string {
	return filepath.Join(syncProfileDir(profileName), "sync.log")
}

func syncCrashLogPath(profileName string) string {
	return filepath.Join(syncProfileDir(profileName), "sync-crash.log")
}

// syncCrashLogMaxSize bounds the crash log. Nothing rotates it — the child holds
// the descriptor for its whole life — so it is trimmed at spawn time instead.
const syncCrashLogMaxSize = 1 << 20

// openSyncCrashLog opens the crash log for append, starting over if it has grown
// past its cap. Appending rather than truncating on every spawn matters because
// spawns are frequent: a session hook attempts one per session, and most exit
// immediately as duplicates. Truncating each time would erase a crash record
// before anyone read it.
func openSyncCrashLog(profileName string) (*os.File, error) {
	path := syncCrashLogPath(profileName)
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() > syncCrashLogMaxSize {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0644)
}

func runtimeFilePath(profileName string) string {
	return filepath.Join(syncProfileDir(profileName), "sync-runtime.json")
}

func exitFilePath(profileName string) string {
	return filepath.Join(syncProfileDir(profileName), "sync-exit.json")
}

func finalizeLockFilePath(profileName string) string {
	return filepath.Join(syncProfileDir(profileName), "sync-finalize.lock")
}

func stopRequestFilePath(profileName string) string {
	return filepath.Join(syncProfileDir(profileName), "sync-stop.json")
}

func createSyncTempPath(profileName string, pattern string) (string, error) {
	dir := syncProfileDir(profileName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}

func startWatchRuntime(profileName string, rt *syncRuntime) error {
	if err := os.MkdirAll(syncProfileDir(profileName), 0700); err != nil {
		return err
	}
	_ = os.Remove(exitFilePath(profileName))
	_ = os.Remove(stopRequestFilePath(profileName))
	if err := writePID(pidFilePath(profileName), rt.PID); err != nil {
		return err
	}
	if err := writeJSONFile(runtimeFilePath(profileName), rt); err != nil {
		_ = os.Remove(pidFilePath(profileName))
		return err
	}
	return nil
}

func finishWatchRuntime(profileName string, rt *syncRuntime) {
	_ = writeJSONFile(exitFilePath(profileName), syncExit{
		InstanceID: rt.InstanceID,
		ExitedAt:   time.Now().UTC(),
	})
	_ = os.Remove(stopRequestFilePath(profileName))
	// Remove the pid file only if it still points at this watcher, preserving the
	// quick stop/start race fix from develop.
	_ = removePIDFileIfCurrent(pidFilePath(profileName), rt.PID)
	_ = os.Remove(runtimeFilePath(profileName))
}

func writeSyncStartAck(path string, ack syncStartAck) error {
	if path == "" {
		return nil
	}
	return writeJSONFile(path, ack)
}

func waitForSyncStartAck(path string, timeout time.Duration) (*syncStartAck, error) {
	deadline := time.Now().Add(timeout)
	for {
		ack, ok, err := readSyncStartAck(path)
		if err == nil && ok {
			return ack, nil
		}
		// A transient read/open error (e.g. Windows "file is being used by
		// another process" while the child writes the ack) must be retried
		// until the deadline rather than aborting immediately.
		if time.Now().After(deadline) {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("timed out after %s", timeout)
		}
		time.Sleep(syncPollInterval)
	}
}

func readSyncStartAck(path string) (*syncStartAck, bool, error) {
	var ack syncStartAck
	ok, err := readJSONFile(path, &ack)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &ack, true, nil
}

func readSyncRuntime(profileName string) (*syncRuntime, bool, error) {
	var rt syncRuntime
	ok, err := readJSONFile(runtimeFilePath(profileName), &rt)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &rt, true, nil
}

func readSyncExit(profileName string) (*syncExit, bool, error) {
	var exit syncExit
	ok, err := readJSONFile(exitFilePath(profileName), &exit)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &exit, true, nil
}

func writeSyncStopRequest(profileName string, req syncStopRequest) error {
	return writeJSONFile(stopRequestFilePath(profileName), req)
}

func readSyncStopRequest(profileName string) (*syncStopRequest, bool, error) {
	var req syncStopRequest
	ok, err := readJSONFile(stopRequestFilePath(profileName), &req)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &req, true, nil
}

func runtimeInstanceID(rt *syncRuntime) string {
	if rt == nil {
		return ""
	}
	return rt.InstanceID
}

func currentWatcherPID(profileName string, rt *syncRuntime) (int, bool, error) {
	if rt != nil && rt.PID > 0 {
		return rt.PID, true, nil
	}
	pid, err := readPID(pidFilePath(profileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, nil
	}
	return pid, true, nil
}

func waitForWatcherExit(profileName string, instanceID string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		stopped, err := watcherStopped(profileName, instanceID)
		if err != nil {
			return false, err
		}
		if stopped {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(syncPollInterval)
	}
}

func watcherStopped(profileName string, instanceID string) (bool, error) {
	lockFree, err := isSyncLockFree(profileName)
	if err != nil {
		return false, err
	}
	if !lockFree {
		return false, nil
	}

	// The sync lock is the source of truth: if it's free, the watcher process that held it is gone.
	// Runtime/exit files can be stale after crashes, so they must not keep us in a "running" state.
	return true, nil
}

func isSyncLockFree(profileName string) (bool, error) {
	fl, err := acquireSyncLock(profileName, 0)
	if err != nil {
		if errors.Is(err, errSyncAlreadyRunning) {
			return false, nil
		}
		return false, err
	}
	if err := fl.Unlock(); err != nil {
		return false, err
	}
	return true, nil
}

func writeJSONFile(path string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0600)
}

func readJSONFile(path string, dest interface{}) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return false, nil
	}
	return true, nil
}

func newSyncInstanceID() string {
	return fmt.Sprintf("%d-%016x", time.Now().UnixNano(), time.Now().UnixNano()^int64(os.Getpid()))
}
