package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"cctrace/internal/envgen"
	"cctrace/internal/gjclog"
	"cctrace/internal/omolog"
	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

// defaultSyncEndpoint and defaultOTELEndpoint are the addresses `cctrace init`
// offers as prompt defaults. They are deployment-specific, so they are injected
// at link time rather than hardcoded:
//
//	-ldflags "-X main.defaultSyncEndpoint=http://host:18080"
//
// deploy/push.sh and the Makefile both supply them from deploy/local-defaults.env,
// which is gitignored. A build made without that file leaves them empty, and init
// then requires the user to enter each address.
//
// Both must stay uninitialized: the Go linker can only overwrite a string
// variable that has no initializer.
var (
	defaultSyncEndpoint string
	defaultOTELEndpoint string
)

// normalizeEndpoint trims the input and assumes plain http:// when no scheme is
// given, so users can type a bare host:port.
func normalizeEndpoint(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		v = "http://" + v
	}
	return v
}

// plaintextLeavesTheLocalNetwork reports whether an endpoint sends over plain
// HTTP to somewhere the local network does not reach. The sync channel carries
// conversation transcripts and the OTEL channel carries telemetry; unlike the
// update channel, neither is a published binary whose integrity a signature
// already covers, so the confidentiality plain HTTP gives up is the whole point
// (#533).
//
// It warns rather than refuses. Plain HTTP inside a company network is a
// legitimate deployment and refusing it would break installs that are doing
// nothing wrong. The boundary is the one validateUpdateEndpointStrict already
// draws, reused so the two channels cannot disagree about what "local" means:
// literal loopback, private-range and link-local addresses, plus the name
// "localhost". A hostname is not exempt -- the client cannot reason about what
// DNS will resolve it to.
func plaintextLeavesTheLocalNetwork(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(parsed.Scheme, "http") {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return false
	}
	return true
}

// warnPlaintextEndpoint says so once per endpoint, naming which one and what it
// carries, so a person reading install output can tell a deliberate internal
// deployment from an address about to cross the public internet in the clear.
//
// carries is named per channel rather than shared: the two endpoints do not send
// the same thing, and "conversation transcripts" is the part that makes this
// worth a warning at all.
func warnPlaintextEndpoint(label, carries, endpoint string) {
	if !plaintextLeavesTheLocalNetwork(endpoint) {
		return
	}
	fmt.Printf("  [!] %s %s is plain HTTP to a non-local address.\n", label, endpoint)
	fmt.Printf("      %s will cross the network unencrypted.\n", carries)
	fmt.Printf("      Use https:// if the server terminates TLS.\n")
}

// warnCodexHTTPSEndpoint follows every write of a Codex [otel] block. Codex
// (measured on 0.153.4) opens no connection to an https:// OTLP metrics endpoint
// and logs nothing, so the block would collect nothing without a word (#644).
func warnCodexHTTPSEndpoint(endpoint string) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return
	}
	fmt.Printf("  [!] Codex metrics endpoint %s uses https://.\n", endpoint)
	fmt.Printf("      Codex sends no OTLP metrics to https:// and reports no error (#644).\n")
	fmt.Printf("      Codex tool metrics stay empty until it points at plain http:// (port 4318).\n")
}

// promptEndpoint asks for an endpoint and does not move on until it has one.
//
// defaultVal is empty whenever the binary was built without
// deploy/local-defaults.env, which is the normal case for a public build. Pressing
// Enter there would otherwise carry an empty endpoint through the user-ID and
// password prompts before failing at the "required" check further down — long
// after the mistake was made.
//
// Attempts are bounded like the password prompt below so a non-interactive stdin
// terminates: a piped or redirected run reads EOF as an empty line, which would
// otherwise loop forever.
func promptEndpoint(ir *inputReader, label, defaultVal string) (string, error) {
	name := strings.TrimSpace(label)
	for attempt := 1; attempt <= 3; attempt++ {
		endpoint := normalizeEndpoint(ir.Prompt(label, defaultVal))
		if endpoint != "" {
			return endpoint, nil
		}
		if attempt < 3 {
			fmt.Printf("  [!] %s is required. Try again (%d/3).\n", name, attempt)
		}
	}
	return "", fmt.Errorf("%s is required", name)
}

func initCmd() *cobra.Command {
	var profileName string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize cctrace profile and environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, profileName)
		},
	}

	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to initialize (e.g. work, personal)")
	return cmd
}

// nextStepsLines renders the closing checklist init prints.
//
// Built as lines rather than printed inline so the wording can be asserted:
// this text is the only place a first-time user is told what init did and did
// not do, and the steps are numbered, so a step appearing or disappearing
// renumbers the ones after it.
//
// There is no password step: init only reaches this point after authenticating,
// and the server refuses a temporary password (#766).
func nextStepsLines(syncEnabled bool) []string {
	lines := []string{
		"  Next steps:",
		"  1. Restart Claude Code to activate telemetry.",
	}
	if !syncEnabled {
		return lines
	}
	return append(lines,
		"  2. Session log sync starts on its own from the next Claude Code session.",
		"     The hook is already written to settings.json; nothing is syncing yet.",
		"     To sync now without waiting, run 'cctrace sync'",
		"     (or 'cctrace sync --daemon' to leave one running).",
		"     Sessions from before this install are not backfilled.",
	)
}

func runInit(cmd *cobra.Command, profileName string) (runErr error) {
	ir := newInputReader()

	isNamed := profileName != ""

	if isNamed {
		if err := profile.ValidateName(profileName); err != nil {
			return err
		}
	}

	// Load existing profile for defaults (re-init scenario)
	var existing *profile.Profile
	if isNamed {
		if p, err := profile.LoadNamed(profileName); err == nil {
			existing = p
		}
	} else if profile.Exists() {
		if p, err := profile.Load(); err == nil {
			existing = p
		} else {
			fmt.Fprintf(os.Stderr, "Warning: could not load existing profile: %v\n", err)
		}
	}

	// If a profile exists, offer the menu built by initReRunOptions (which drops
	// the Codex entry when there is no Codex install to patch).
	if existing != nil {
		profilePath := profile.DefaultPath()
		if isNamed {
			if np, err := profile.NamedPath(profileName); err == nil {
				profilePath = np
			}
		}
		fmt.Println()
		fmt.Printf("  Profile already exists: %s\n", profilePath)
		opts := initReRunOptions(codexInstalled())
		for i, o := range opts {
			fmt.Printf("    %d) %s\n", i+1, o.label)
		}
		raw := ir.Prompt("  Choose ["+reRunPromptRange(opts)+"]", "1")
		action, ok := reRunChoice(opts, raw)
		if !ok {
			return fmt.Errorf("invalid choice: %q", strings.TrimSpace(raw))
		}
		switch action {
		case reRunPatchCodex:
			return runCodexPatch(existing, isNamed, profileName)
		case reRunApplyEnv:
			if isNamed {
				return applyNamedProfile(profileName)
			}
			return applyDefaultProfile()
		case reRunCancel:
			fmt.Println("  Cancelled.")
			return nil
		case reRunFullSetup:
			// Continue with full init flow below. Create backup first; restore on error.
			backupPath, err := backupProfile(isNamed, profileName)
			if err != nil {
				return fmt.Errorf("backup existing profile: %w", err)
			}
			defer func() {
				if runErr != nil {
					if err := restoreFromBackup(backupPath, isNamed, profileName); err != nil {
						fmt.Fprintf(os.Stderr, "  Warning: failed to restore backup: %v\n", err)
					} else {
						fmt.Fprintln(os.Stderr, "  Restored previous profile from backup.")
					}
				} else {
					_ = os.Remove(backupPath)
				}
			}()
		}
	}

	p := profile.NewDefault()
	if existing != nil {
		// Preserve existing values as defaults
		p.User = existing.User
		p.Server = existing.Server
		p.Options = existing.Options
		p.CreatedAt = existing.CreatedAt
		p.ClaudeConfigDir = existing.ClaudeConfigDir
	}

	fmt.Println()
	if isNamed {
		fmt.Printf("  Claude Code Trace Setup [profile: %s]\n", profileName)
	} else {
		fmt.Println("  Claude Code Trace Setup")
	}
	fmt.Println("  ========================")

	// Step 1: Server endpoints
	var err error
	if p.Server.SyncEndpoint == "" {
		p.Server.SyncEndpoint = defaultSyncEndpoint
	}
	p.Server.SyncEndpoint, err = promptEndpoint(ir, "  Sync endpoint", p.Server.SyncEndpoint)
	if err != nil {
		return err
	}

	if p.Server.Endpoint == "" {
		p.Server.Endpoint = defaultOTELEndpoint
	}
	p.Server.Endpoint, err = promptEndpoint(ir, "  OTEL endpoint", p.Server.Endpoint)
	if err != nil {
		return err
	}

	// After both, so the two warnings appear together rather than bracketing the
	// second prompt.
	warnPlaintextEndpoint("Sync endpoint", "Conversation transcripts", p.Server.SyncEndpoint)
	warnPlaintextEndpoint("OTEL endpoint", "Usage telemetry", p.Server.Endpoint)

	// Step 2: User ID (strip @domain if entered as email)
	p.User.ID = ir.Prompt("  User ID (company e-mail id, e.g. 'user123')", "")
	if at := strings.Index(p.User.ID, "@"); at > 0 {
		p.User.ID = p.User.ID[:at]
	}

	dropStaleReadToken(existing, p)

	// Step 3: Authenticate with server (required)
	if p.Server.SyncEndpoint == "" || p.User.ID == "" {
		return fmt.Errorf("sync endpoint and user ID are required")
	}
	var authedPassword string
	for attempt := 1; attempt <= 3; attempt++ {
		password := ir.PromptPassword("  Temporary password")
		if password == "" {
			if attempt < 3 {
				fmt.Printf("  [!] Password is required. Try again (%d/3).\n", attempt)
				continue
			}
			return fmt.Errorf("authentication cancelled")
		}
		info, err := authenticateUser(p.Server.SyncEndpoint, p.User.ID, password, p.Server.AuthToken)
		if err == nil {
			if info.Name != "" {
				p.User.Name = info.Name
			}
			if info.Email != "" {
				p.User.Email = info.Email
			}
			if info.Team != "" {
				p.User.Team = info.Team
			}
			if info.ApiToken != "" {
				p.Server.AuthToken = info.ApiToken
			}
			fmt.Printf("  [OK] Authenticated: %s <%s>\n", info.Name, info.Email)
			if info.MustChangePassword {
				fmt.Println("  [!] Please change your password on the dashboard.")
			}
			authedPassword = password
			break
		}
		if attempt < 3 {
			fmt.Printf("  [!] Authentication failed: %v. Try again (%d/3).\n", err, attempt)
		} else {
			fmt.Printf("  [!] Authentication failed after 3 attempts: %v\n", err)
			return fmt.Errorf("authentication failed")
		}
	}

	// Step 4: Name/Email/Team — from server (read-only)
	fmt.Printf("  Name: %s\n", p.User.Name)
	fmt.Printf("  Email: %s\n", p.User.Email)
	if p.User.Team != "" {
		fmt.Printf("  Team: %s\n", p.User.Team)
	}

	// Claude config dir (optional, named profiles only)
	if isNamed {
		raw := ir.Prompt("  Claude config directory (Enter for default ~/.claude)", p.ClaudeConfigDir)
		p.ClaudeConfigDir = expandTilde(strings.TrimSpace(raw))

		// Warn if another profile uses the same ClaudeConfigDir
		if conflict := checkClaudeDirConflict(profileName, p.ClaudeConfigDir); conflict != "" {
			fmt.Printf("  [!] Warning: profile %q also uses this Claude directory.\n", conflict)
			fmt.Println("      Each profile should point to a different Claude home to avoid overwriting settings.")
		}
	}

	// Session log sync toggle
	syncDefault := "y"
	if !p.Options.SyncEnabled {
		syncDefault = "n"
	}
	syncInput := ir.Prompt("  Enable session log sync? (y/n)", syncDefault)
	p.Options.SyncEnabled = strings.ToLower(strings.TrimSpace(syncInput)) != "n"

	// Validate
	if err := profile.Validate(p); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	if isNamed {
		if err := profile.EnsureNamedDir(profileName); err != nil {
			return fmt.Errorf("failed to create profile directory: %w", err)
		}
		if err := profile.SaveNamed(p, profileName); err != nil {
			return fmt.Errorf("failed to save profile: %w", err)
		}
		namedPath, _ := profile.NamedPath(profileName)
		fmt.Printf("\n  [OK] Profile saved to %s\n", namedPath)
	} else {
		// Ensure directory
		if err := profile.EnsureDir(); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}
		// Save profile
		if err := profile.Save(p); err != nil {
			return fmt.Errorf("failed to save profile: %w", err)
		}
		fmt.Printf("\n  [OK] Profile saved to %s\n", profile.DefaultPath())
	}

	// Apply OTEL environment to Claude Code settings.json
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		return fmt.Errorf("failed to apply settings: %w", err)
	}

	settingsPath, _ := envgen.ClaudeSettingsPath(p)
	fmt.Printf("  [OK] OTEL environment applied to %s\n", settingsPath)

	// The upload token just stored cannot read the Open API; offer a read token
	// while the password is still at hand. This is after the last step that can
	// fail init: a failure restores the previous profile from backup, which would
	// otherwise drop a token the server has already issued.
	previousReadToken := p.Server.ReadToken
	offerReadToken(p, authedPassword, hostname(), ir.Prompt, os.Stdout)
	if p.Server.ReadToken != previousReadToken {
		if err := saveProfile(p, profileName); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: read token not saved (%v); run 'cctrace auth read' to create another.\n", err)
		} else {
			updated, failed := propagateReadToken(p, profileName, previousReadToken)
			reportPropagation(os.Stdout, updated, failed)
		}
	}

	offerCodexSync(ir, p, profileName)

	// Detect gjc and offer to enable sync. Unlike Codex, gjc has no OTEL
	// exporter to configure and no session-start hook, so enabling it alone
	// does not start collection.
	if _, err := os.Stat(gjclog.DefaultGjcDir()); err == nil {
		fmt.Println()
		fmt.Println("  gjc detected (~/.gjc found).")
		answer := ir.Prompt("  Enable gjc session sync? [Y/n]", "Y")
		if answer == "" || answer == "Y" || answer == "y" {
			p.Options.GjcSyncEnabled = true
			if err := saveProfile(p, profileName); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: save profile: %v\n", err)
			}
			fmt.Println("  [OK] gjc sync enabled.")
			fmt.Println("  [!] gjc has no session-start hook, so collection does not start by")
			fmt.Println("      itself. Run 'cctrace sync --watch' (or a periodic 'cctrace sync').")
			fmt.Println("  [!] gjc's token log lives inside each project directory")
			fmt.Println("      (<cwd>/.gjc/_session-*/token-logs/); deleting a project directory")
			fmt.Println("      loses that part of its history.")
		}
	}

	// Detect omo and offer to enable sync. Same caveat as gjc: no OTEL
	// exporter, no session-start hook.
	if _, err := os.Stat(omolog.DefaultOmoDir()); err == nil {
		fmt.Println()
		fmt.Println("  omo detected (~/.omo found).")
		answer := ir.Prompt("  Enable omo session sync? [Y/n]", "Y")
		if answer == "" || answer == "Y" || answer == "y" {
			p.Options.OmoSyncEnabled = true
			if err := saveProfile(p, profileName); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: save profile: %v\n", err)
			}
			fmt.Println("  [OK] omo sync enabled.")
			fmt.Println("  [!] omo has no session-start hook, so collection does not start by")
			fmt.Println("      itself. Run 'cctrace sync --watch' (or a periodic 'cctrace sync').")
		}
	}

	fmt.Println()
	if !envgen.IsCctraceInPath() {
		if !offerInstallToPath(ir) {
			printPathInstallGuide()
		}
	}
	for _, line := range nextStepsLines(p.Options.SyncEnabled) {
		fmt.Println(line)
	}
	fmt.Println()
	fmt.Println("  Run 'cctrace status' to verify your configuration.")
	fmt.Println()

	// Detect additional .claude-* directories and offer to set up named profiles
	if !isNamed {
		detectAndOfferAdditionalHomes(ir, p)
	}

	return nil
}
