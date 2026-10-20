package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cctrace/internal/profile"
)

const (
	daemonControlTimeout   = 45 * time.Second
	daemonControlFreshness = 120 * time.Second
)

type daemonControlDependencies struct {
	profileDefaultDir func() string
	resolveCLI        func(string) (string, error)
	runCommand        func(context.Context, string, []string, []string) error
	lockProbe         func(string) daemonLockState
	timeout           time.Duration
}

type daemonRestartResult struct {
	ProfileID string `json:"profile_id"`
	State     string `json:"state"`
}

func newDaemonControlDependencies(overrides *daemonControlDependencies) *daemonControlDependencies {
	dependencies := daemonControlDependencies{
		profileDefaultDir: profile.DefaultDir,
		resolveCLI:        resolveInstalledCctrace,
		runCommand:        runDaemonControlCommand,
		lockProbe:         probeDaemonLock,
		timeout:           daemonControlTimeout,
	}
	if overrides == nil {
		return &dependencies
	}
	if overrides.profileDefaultDir != nil {
		dependencies.profileDefaultDir = overrides.profileDefaultDir
	}
	if overrides.resolveCLI != nil {
		dependencies.resolveCLI = overrides.resolveCLI
	}
	if overrides.runCommand != nil {
		dependencies.runCommand = overrides.runCommand
	}
	if overrides.lockProbe != nil {
		dependencies.lockProbe = overrides.lockProbe
	}
	if overrides.timeout > 0 {
		dependencies.timeout = overrides.timeout
	}
	return &dependencies
}

func isDaemonControlCapabilityHello(request Request) bool {
	var fields map[string]json.RawMessage
	return request.Op == "hello" && json.Unmarshal(request.Payload, &fields) == nil &&
		len(fields) == 1 && bytes.Equal(bytes.TrimSpace(fields["daemon_control"]), []byte("true"))
}

func daemonControlAdvertised(response Response) bool {
	hello, ok := response.Result.(helloResult)
	return ok && hello.Capabilities.DaemonControl != nil && *hello.Capabilities.DaemonControl
}

func (s *Service) daemonControlAvailable() bool {
	if s == nil || !s.providerMode || s.fixtureRoot != "" || !s.controlRootOK || s.daemonControl == nil {
		return false
	}
	root := s.daemonControl.profileDefaultDir()
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root != s.daemonRoot || !daemonControlRootSafe(root) {
		return false
	}
	path, err := s.daemonControl.resolveCLI(root)
	return err == nil && path != "" && filepath.IsAbs(path)
}

func daemonControlRootSafe(root string) bool {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func (s *Service) restartDaemon(ctx context.Context, raw json.RawMessage) (any, *ErrorObject) {
	var fields map[string]json.RawMessage
	whole := decodePayload(raw, &fields) == nil && len(fields) == 1 && bytes.Equal(bytes.TrimSpace(fields["scope"]), []byte(`"daemon"`))
	profileID, ok := daemonRestartProfileID(raw)
	if !ok && !whole {
		return nil, payloadError()
	}
	if !s.daemonControlAvailable() {
		return nil, daemonControlUnavailable()
	}
	if !s.controlMu.TryLock() {
		return nil, &ErrorObject{Code: "daemon_busy", Message: "another daemon control operation is in progress"}
	}
	defer s.controlMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, &ErrorObject{Code: "request_cancelled", Message: "request was cancelled by the helper supervisor"}
	}
	controlCtx, cancel := context.WithTimeout(ctx, s.daemonControl.timeout)
	defer cancel()
	if current, managed := s.readSupervisorStatus(); managed || whole {
		return s.restartSupervisor(controlCtx, current, profileID, whole)
	}

	current, controlErr := s.controlProfileStatus(profileID)
	if controlErr != nil {
		return nil, controlErr
	}
	name, named := daemonProfileName(profileID)
	if current.Daemon.State == "running" {
		latest, err := s.controlProfileStatus(profileID)
		if err != nil {
			return nil, err
		}
		if latest.Daemon.State == "running" {
			args := daemonSyncCommandArgs("stop", named, name)
			if err := s.runDaemonControlCommand(controlCtx, args); err != nil {
				return nil, daemonControlCommandFailure(controlCtx, "daemon_stop_failed", "daemon did not stop cleanly")
			}
			latest, err = s.controlProfileStatus(profileID)
			if err != nil {
				return nil, err
			}
			if latest.Daemon.State != "stopped" || latest.Daemon.Evidence != "lock_free" {
				return nil, &ErrorObject{Code: "daemon_stop_unconfirmed", Message: "daemon shutdown could not be confirmed"}
			}
		}
		current = latest
	}
	if current.Daemon.State != "stopped" || current.Daemon.Evidence != "lock_free" {
		return nil, &ErrorObject{Code: "daemon_state_changed", Message: "daemon state changed before restart"}
	}
	latest, err := s.controlProfileStatus(profileID)
	if err != nil {
		return nil, err
	}
	if latest.Daemon.State != "stopped" || latest.Daemon.Evidence != "lock_free" {
		return nil, &ErrorObject{Code: "daemon_state_changed", Message: "daemon state changed before restart"}
	}
	args := daemonSyncCommandArgs("start", named, name)
	if err := s.runDaemonControlCommand(controlCtx, args); err != nil {
		return nil, daemonControlCommandFailure(controlCtx, "daemon_start_failed", "daemon start was not acknowledged")
	}
	confirmed, controlErr := s.controlProfileStatus(profileID)
	if controlErr != nil || confirmed.Daemon.State != "running" || confirmed.Daemon.Evidence != "lock_held" {
		if controlErr != nil && errors.Is(controlCtx.Err(), context.DeadlineExceeded) {
			return nil, daemonControlCommandFailure(controlCtx, "daemon_start_failed", "daemon start was not acknowledged")
		}
		return nil, &ErrorObject{Code: "daemon_start_unconfirmed", Message: "daemon start could not be confirmed"}
	}
	if controlCtx.Err() != nil {
		return nil, daemonControlCommandFailure(controlCtx, "daemon_start_failed", "daemon start was not acknowledged")
	}
	return daemonRestartResult{ProfileID: profileID, State: "restarted"}, nil
}

func daemonControlUnavailable() *ErrorObject {
	return &ErrorObject{Code: "feature_unavailable", Message: "daemon control is not available in this helper mode"}
}

func daemonRestartProfileID(raw json.RawMessage) (string, bool) {
	var fields map[string]json.RawMessage
	if decodePayload(raw, &fields) != nil || len(fields) != 1 {
		return "", false
	}
	profileID, ok := requiredString(fields, "profile_id")
	if !ok {
		return "", false
	}
	if profileID == "default" {
		return profileID, true
	}
	if strings.HasPrefix(profileID, "named:") && validNamedStatusName(strings.TrimPrefix(profileID, "named:")) {
		return profileID, true
	}
	return "", false
}

func daemonProfileName(profileID string) (string, bool) {
	if profileID == "default" {
		return "", false
	}
	return strings.TrimPrefix(profileID, "named:"), true
}

func (s *Service) controlProfileStatus(profileID string) (daemonProfileStatus, *ErrorObject) {
	if !s.controlRootOK || !daemonControlRootSafe(s.daemonRoot) {
		return daemonProfileStatus{}, daemonControlUnavailable()
	}
	status := s.readLiveDaemonStatus()
	now := s.now().UTC()
	if !status.Complete || !validStatusTime(status.ObservedAt, now, true) {
		return daemonProfileStatus{}, &ErrorObject{Code: "daemon_state_unknown", Message: "daemon state is incomplete or stale"}
	}
	observedAt, err := time.Parse(time.RFC3339Nano, status.ObservedAt)
	if err != nil || now.Sub(observedAt) > daemonControlFreshness {
		return daemonProfileStatus{}, &ErrorObject{Code: "daemon_state_unknown", Message: "daemon state is incomplete or stale"}
	}
	for _, candidate := range status.Profiles {
		if candidate.ID != profileID {
			continue
		}
		if candidate.Collection.Failure != nil && candidate.Collection.Failure.Class == "server" {
			return daemonProfileStatus{}, &ErrorObject{Code: "daemon_server_rate_limited", Message: "server rate limit is active; wait before restarting"}
		}
		if candidate.ErrorCode != nil || candidate.SyncEnabled == nil {
			return daemonProfileStatus{}, &ErrorObject{Code: "daemon_state_unknown", Message: "profile state could not be verified"}
		}
		if !*candidate.SyncEnabled {
			return daemonProfileStatus{}, &ErrorObject{Code: "daemon_profile_unavailable", Message: "profile is not enabled for daemon sync"}
		}
		if candidate.Daemon.State != "running" && candidate.Daemon.State != "stopped" ||
			candidate.Daemon.State == "running" && candidate.Daemon.Evidence != "lock_held" ||
			candidate.Daemon.State == "stopped" && candidate.Daemon.Evidence != "lock_free" {
			return daemonProfileStatus{}, &ErrorObject{Code: "daemon_state_unknown", Message: "daemon state could not be verified"}
		}
		return candidate, nil
	}
	return daemonProfileStatus{}, &ErrorObject{Code: "daemon_profile_unavailable", Message: "profile is unavailable for daemon control"}
}

func (s *Service) runDaemonControlCommand(ctx context.Context, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root := s.daemonControl.profileDefaultDir()
	executable, err := s.daemonControl.resolveCLI(root)
	if err != nil || executable == "" {
		return errors.New("installed CLI unavailable")
	}
	return s.daemonControl.runCommand(ctx, executable, args, daemonControlEnvironment(root))
}

func daemonSyncCommandArgs(action string, named bool, name string) []string {
	var args []string
	if action == "stop" {
		args = []string{"sync", "--stop"}
	} else {
		args = []string{"sync", "start"}
	}
	if named {
		args = append(args, "--profile", name)
	}
	return args
}

func daemonControlCommandFailure(ctx context.Context, code, message string) *ErrorObject {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &ErrorObject{Code: "daemon_control_timeout", Message: "daemon restart timed out"}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return &ErrorObject{Code: "request_cancelled", Message: "request was cancelled by the helper supervisor"}
	}
	return &ErrorObject{Code: code, Message: message}
}

func daemonControlEnvironment(profileRoot string) []string {
	allowed := map[string]string{}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || strings.EqualFold(name, "HOME") || !daemonControlEnvAllowed(name) {
			continue
		}
		allowed[strings.ToUpper(name)] = name + "=" + value
	}
	allowed["HOME"] = "HOME=" + filepath.Dir(profileRoot)
	keys := make([]string, 0, len(allowed))
	for key := range allowed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, allowed[key])
	}
	return env
}

func daemonControlEnvAllowed(name string) bool {
	switch strings.ToUpper(name) {
	case "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "LC_CTYPE", "SYSTEMROOT", "WINDIR",
		"PATH", "USER", "LOGNAME", "SHELL", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR":
		return true
	default:
		return strings.HasPrefix(strings.ToUpper(name), "LC_")
	}
}

func resolveInstalledCctrace(profileRoot string) (string, error) {
	home := filepath.Dir(profileRoot)
	candidates := []string{
		filepath.Join(home, ".local", "bin", "cctrace"),
		"/usr/local/bin/cctrace",
		"/opt/homebrew/bin/cctrace",
	}
	for _, candidate := range candidates {
		if trustedExecutable(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("installed CLI unavailable")
}

func trustedExecutable(path string) bool {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(absolute, current)
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if index == len(parts)-1 {
			return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
		}
		if !info.IsDir() {
			return false
		}
	}
	return false
}

func runDaemonControlCommand(ctx context.Context, executable string, args, env []string) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = env
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}
