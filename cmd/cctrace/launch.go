package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"cctrace/internal/claudeauth"
	"cctrace/internal/codexauth"
	"cctrace/internal/processio"
	"cctrace/internal/profile"
	"cctrace/internal/projecthash"
)

// CODEX_HOME isolation has been exercised only with this installed version;
// unknown versions stay unavailable until the same synthetic-home check passes.
const verifiedCodexLaunchVersion = "codex-cli 0.162.0"

func launchCmd() *cobra.Command {
	var agent, profileName, ioMode, selectedHome string
	var observeProcessIO bool
	cmd := &cobra.Command{
		Use:                "launch --agent claude|codex [--profile name] [--observe-process-io] [--io-mode pipes] -- [agent args...]",
		Short:              "Launch Claude Code or Codex with the selected local profile",
		DisableFlagParsing: false,
		Args:               cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAgentLaunchWithIO(cmd.Context(), agent, profileName, args, observeProcessIO, ioMode, cmd.Flags().Changed("io-mode"), cmd.ErrOrStderr(), selectedHome)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Agent to launch (claude or codex)")
	cmd.Flags().StringVar(&selectedHome, "home", "", "Explicit enrolled execution home when a profile has multiple homes")
	cmd.Flags().StringVar(&profileName, "profile", "", "Explicit profile override (default or named profile)")
	cmd.Flags().BoolVar(&observeProcessIO, "observe-process-io", false, "Opt in to process stream counters; input byte counts can include secret input, but raw stream bytes are never stored or uploaded")
	cmd.Flags().StringVar(&ioMode, "io-mode", "pty", "With --observe-process-io: pty (default) or explicit pipes for separate stdin/stdout/stderr")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

var processIOMappingTimeout = 30 * time.Second
var processIOSessionMappedFn = func(processio.SessionMapping) {}

func runAgentLaunchWithIO(ctx context.Context, agent, explicitProfile string, args []string, observeProcessIO bool, ioMode string, ioModeExplicit bool, notice io.Writer, selectedHomes ...string) error {
	if agent != "claude" && agent != "codex" {
		return fmt.Errorf("--agent must be claude or codex")
	}
	if !observeProcessIO && ioModeExplicit {
		return errors.New("--io-mode requires --observe-process-io")
	}
	if observeProcessIO && ioMode != "pty" && ioMode != "pipes" {
		return errors.New("--io-mode must be pty or pipes")
	}
	if observeProcessIO && ioMode == "pty" {
		if err := processio.CheckPTYSupport(os.Stdin, os.Stdout, os.Stderr); err != nil {
			return err
		}
	}
	name := explicitProfile
	if name == "" {
		selection, err := profile.ReadSelection(profile.SelectionPath())
		if err != nil {
			return fmt.Errorf("read selected profile: %w", err)
		}
		if agent == "claude" {
			name = selection.Claude
		} else {
			name = selection.Codex
		}
		if name == "" {
			name = "default"
		}
	}
	p, err := loadAgentProfile(name)
	if err != nil {
		return fmt.Errorf("selected %s profile %q is unavailable: %w", agent, name, err)
	}
	if len(selectedHomes) > 0 && selectedHomes[0] != "" {
		selected, e := profile.NormalizeCodexHome(selectedHomes[0])
		if e != nil {
			return e
		}
		enrolled := false
		for _, h := range p.ProviderHomes(agent, "") {
			if h.Home == selected {
				enrolled = true
			}
		}
		if p.HomesVersion != 1 || !enrolled {
			return errors.New("--home must name an enrolled home")
		}
		if agent == "claude" {
			p.ClaudeConfigDir = selected
		} else {
			p.CodexHome = selected
		}
	}
	home, err := agentProfileHome(agent, p)
	if err != nil {
		return fmt.Errorf("selected %s profile %q is unavailable: %w", agent, name, err)
	}
	if observeProcessIO {
		if err := processio.CheckSessionMappingSupport(agent, home); err != nil {
			return fmt.Errorf("safe %s process-to-session mapping is unavailable: %w", agent, err)
		}
	}
	home, accountID, err := agentProfileIdentity(agent, p)
	if err != nil {
		return fmt.Errorf("selected %s profile %q is unavailable: %w", agent, name, err)
	}
	binary, err := exec.LookPath(agent)
	if err != nil {
		return fmt.Errorf("find %s executable: %w", agent, err)
	}
	if agent == "codex" {
		if err := verifyCodexLaunchVersion(ctx, binary); err != nil {
			return err
		}
	}
	child := exec.CommandContext(ctx, binary, args...)
	if observeProcessIO {
		child = exec.Command(binary, args...)
	}
	child.Env = replaceEnvironmentValue(os.Environ(), map[string]string{
		"claude": "CLAUDE_CONFIG_DIR",
		"codex":  "CODEX_HOME",
	}[agent], home)
	if observeProcessIO {
		if err := runObservedAgentLaunch(ctx, child, agent, home, accountID, name, p, ioMode, notice); err != nil {
			return err
		}
		return nil
	}
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Run(); err != nil {
		return fmt.Errorf("%s exited: %w", agent, err)
	}
	return nil
}

func runObservedAgentLaunch(ctx context.Context, child *exec.Cmd, agent, home, accountID, profileName string, p *profile.Profile, ioMode string, notice io.Writer) error {
	if strings.TrimSpace(p.Server.AuthToken) == "" {
		return errors.New("process I/O observation needs this profile's cctrace upload token")
	}
	endpoint := profileSyncEndpoint(p)
	if strings.TrimSpace(endpoint) == "" {
		return errors.New("process I/O observation needs this profile's cctrace sync endpoint")
	}
	if p.HomesVersion != 0 || p.Homes != nil {
		allowed := false
		for _, h := range p.ProviderHomes(agent, "") {
			if h.Home == home && h.Transmission == "allow" {
				allowed = true
			}
		}
		if !allowed || !p.CollectionEnabled(agent) {
			return errors.New("the selected home has not enabled collection and transmission")
		}
	}
	provider := "anthropic"
	if agent == "codex" {
		provider = "openai"
	}
	profileEmail := resolveProfileEmail(p, "")
	userID := resolveUserID(p)
	if processIOAccountExcluded(p.Options.ExcludeAccounts, provider, accountID) {
		return errors.New("the selected profile excludes this billing account from session collection")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("read launch working directory: %w", err)
	}
	project := projecthash.FromPath(cwd)
	if project == "" {
		return errors.New("the launch working directory has no project identity for session observations")
	}
	client, err := newSyncClient(p, endpoint, version, profileName)
	if err != nil {
		return fmt.Errorf("configure process I/O uploader: %w", err)
	}
	queue := processIOQueue(profileName)
	epoch, err := newProcessIOEpoch()
	if err != nil {
		return fmt.Errorf("create process I/O epoch: %w", err)
	}
	if notice != nil {
		fmt.Fprintln(notice, "Process I/O counters are enabled. Input byte counts may include secret input; raw stream bytes are never stored or uploaded.")
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	mapCtx, cancelMap := context.WithTimeout(runCtx, processIOMappingTimeout)
	defer cancelMap()
	type mappingResult struct {
		mapping processio.SessionMapping
		err     error
	}
	mapped := make(chan struct{})
	var mapping mappingResult
	var mappingOnce sync.Once
	mappingLost := false
	options := processio.RelayOptions{
		Mode: processio.Mode(ioMode), Command: child, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		ReportInterval: processIOReportInterval,
		OnStarted: func(pid int) {
			go func() {
				value, err := processio.WaitForSessionMapping(mapCtx, agent, home, pid)
				mappingOnce.Do(func() {
					mapping = mappingResult{mapping: value, err: err}
					close(mapped)
				})
				if err != nil {
					cancelRun()
				} else {
					processIOSessionMappedFn(value)
				}
			}()
		},
		OnSnapshot: func(pid int, report processio.Report) error {
			select {
			case <-mapped:
			default:
				if report.Final {
					return processio.ErrSessionMappingUnavailable
				}
				return nil
			}
			if mapping.err != nil || mapping.mapping.PID != pid || mapping.mapping.Agent != agent || mapping.mapping.ProcStart == "" {
				return processio.ErrSessionMappingUnavailable
			}
			if mappingLost {
				return processio.ErrSessionMappingUnavailable
			}
			status, statusSource, statusChangedAt := mapping.mapping.Status, mapping.mapping.StatusSource, mapping.mapping.StatusChangedAt
			if !report.Final {
				current, statusErr := processio.VerifySessionMapping(agent, home, pid, mapping.mapping)
				if statusErr != nil {
					mappingLost = true
					return statusErr
				}
				status, statusSource, statusChangedAt = current.Status, current.StatusSource, current.StatusChangedAt
			}
			if status != "busy" && status != "idle" && status != "waiting" && status != "shell" {
				status, statusSource, statusChangedAt = "unknown", "process_io_unknown", nil
			}
			if statusChangedAt != nil && statusChangedAt.After(time.Now().Add(4*time.Minute)) {
				statusChangedAt = nil
			}
			if report.Final {
				status, statusSource, statusChangedAt = "gone", "process_io_lifecycle", &report.AsOf
			}
			queued := processio.QueuedReport{
				SourceHome: home, SourceRevision: p.DefinitionRevision(),
				ProfileEmail: profileEmail, UserID: userID,
				SessionID: mapping.mapping.SessionID, Agent: agent, ProjectHash: project,
				BillingProvider: provider, AccountID: accountID, PID: pid, ProcStart: mapping.mapping.ProcStart,
				Status: status, StatusSource: statusSource, StatusChangedAt: statusChangedAt, IOEpoch: epoch, Report: report,
			}
			if err := queue.Upsert(queued); err != nil {
				return fmt.Errorf("persist process I/O counters: %w", err)
			}
			current := p
			if p.HomesVersion != 0 || p.Homes != nil {
				entry, e := (profile.Registry{Root: profile.DefaultDir()}).Load(func() string {
					if profileName == "" {
						return "default"
					}
					return profileName
				}())
				if e != nil {
					return e
				}
				current = entry.Profile
			}
			if err := flushProcessIOQueueFor(runCtx, queue, client, profileEmail, userID, current.Options.ExcludeAccounts, true, func(r processio.QueuedReport) bool { return processIOHomeAllowed(current, r) }); err != nil {
				return fmt.Errorf("upload process I/O counters: %w", err)
			}
			return nil
		},
	}
	_, err = processio.Run(runCtx, options)
	if err != nil {
		var mappingErr error
		if errors.Is(err, processio.ErrSessionMappingUnavailable) {
			mappingErr = processIOMappingError(agent)
		}
		if mappingErr != nil {
			return errors.Join(mappingErr, err)
		}
		return fmt.Errorf("%s process I/O launch: %w", agent, err)
	}
	return nil
}

func newProcessIOEpoch() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	encoded := hex.EncodeToString(bytes[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func verifyCodexLaunchVersion(ctx context.Context, binary string) error {
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return fmt.Errorf("verify Codex CLI version for isolated CODEX_HOME launch: %w", err)
	}
	if strings.TrimSpace(string(output)) != verifiedCodexLaunchVersion {
		return fmt.Errorf("codex CLI version is not verified for isolated CODEX_HOME launch (verified: %s)", verifiedCodexLaunchVersion)
	}
	return nil
}

func loadAgentProfile(name string) (*profile.Profile, error) {
	if name == "default" {
		return profile.Load()
	}
	return profile.LoadNamed(name)
}

// agentProfileIdentity returns the local execution home and exact provider
// account id, if available. Only callers operating on the local machine may
// use the path; the dashboard bridge returns names and account ids only.
func agentProfileIdentity(agent string, p *profile.Profile) (string, string, error) {
	home, err := agentProfileHome(agent, p)
	if err != nil {
		return "", "", err
	}
	switch agent {
	case "claude":
		accountID, err := claudeauth.ReadAccountUUID(home)
		if err != nil {
			return "", "", errors.New("local Claude account metadata could not be read")
		}
		return home, accountID, nil
	case "codex":
		accountID, err := codexauth.ReadAccountID(home)
		if err != nil {
			return "", "", errors.New("local Codex account metadata could not be read")
		}
		return home, accountID, nil
	default:
		return "", "", fmt.Errorf("unsupported agent %q", agent)
	}
}

func agentProfileHome(agent string, p *profile.Profile) (string, error) {
	if p.HomesVersion != 0 || p.Homes != nil {
		homes := p.ProviderHomes(agent, "")
		primary := p.ClaudeConfigDir
		if agent == "codex" {
			primary = p.CodexHome
		}
		for _, h := range homes {
			if h.Home == primary && profile.CheckCodexHome(h.Home) == nil {
				return h.Home, nil
			}
		}
		if len(homes) > 1 {
			return "", errors.New("multiple enrolled homes require an explicit --home selection")
		}
		if len(homes) == 0 || profile.CheckCodexHome(homes[0].Home) != nil {
			return "", errors.New("explicit execution home unavailable")
		}
		return homes[0].Home, nil
	}
	if p == nil {
		return "", errors.New("profile is unavailable")
	}
	switch agent {
	case "claude":
		home := p.ClaudeConfigDir
		if home == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			home = filepath.Join(userHome, ".claude")
		}
		home, err := filepath.Abs(home)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(home)
		if err != nil || !info.IsDir() {
			return "", errors.New("claude home is unavailable")
		}
		return home, nil
	case "codex":
		if err := profile.CheckCodexHome(p.CodexHome); err != nil {
			return "", err
		}
		return p.CodexHome, nil
	default:
		return "", fmt.Errorf("unsupported agent %q", agent)
	}
}

func replaceEnvironmentValue(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(name, key) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, key+"="+value)
}
