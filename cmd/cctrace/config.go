package main

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"cctrace/internal/codexlog"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	var profileName string

	cmd := &cobra.Command{
		Use:   "config",
		Short: "View or modify profile settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigList(profileName)
		},
	}

	cmd.PersistentFlags().StringVar(&profileName, "profile", "", "Named profile to use")

	cmd.AddCommand(configGetCmd(&profileName))
	cmd.AddCommand(configSetCmd(&profileName))
	cmd.AddCommand(configListCmd(&profileName))

	return cmd
}

func configListCmd(profileName *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all settings",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigList(*profileName)
		},
	}
}

func configGetCmd(profileName *string) *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Get a setting value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigGet(*profileName, args[0])
		},
	}
}

func configSetCmd(profileName *string) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a setting value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigSet(*profileName, args[0], args[1])
		},
	}
}

func runConfigList(profileName string) error {
	p, err := loadProfile(profileName)
	if err != nil {
		return err
	}

	fmt.Println()
	if profileName != "" {
		fmt.Printf("  Settings [profile: %s]\n", profileName)
	} else {
		fmt.Println("  Settings")
	}
	fmt.Println("  " + strings.Repeat("─", 50))

	settings := flattenProfile(p)
	for _, s := range settings {
		fmt.Printf("  %-30s %s\n", s.key, s.value)
	}
	fmt.Println()
	return nil
}

func runConfigGet(profileName string, key string) error {
	p, err := loadProfile(profileName)
	if err != nil {
		return err
	}

	settings := flattenProfile(p)
	for _, s := range settings {
		if s.key == key {
			fmt.Println(s.value)
			return nil
		}
	}
	return fmt.Errorf("unknown key: %s", key)
}

func runConfigSet(profileName string, key, value string) error {
	p, err := loadProfile(profileName)
	if err != nil {
		return err
	}

	if err := setProfileField(p, key, value); err != nil {
		return err
	}

	p.UpdatedAt = time.Now().UTC()

	if err := saveProfile(p, profileName); err != nil {
		return err
	}

	// Re-apply OTEL settings if server-related keys changed
	if strings.HasPrefix(key, "server.") || strings.HasPrefix(key, "user.") || strings.HasPrefix(key, "options.") {
		if err := envgen.ApplyToClaudeSettings(p); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: failed to update Claude settings: %v\n", err)
		}
	}

	if key == "server.ca_cert_file" {
		// What was stored, not what was typed: "~" expanded and made absolute.
		value = p.Server.CACertFile
	}
	fmt.Printf("  %s = %s\n", key, value)
	if key == "server.ca_cert_file" || key == "server.protocol" || key == "server.endpoint" {
		noteClaudeProtocolOverride(os.Stdout, p)
	}
	if key == "server.ca_cert_file" {
		// The sync daemon builds its HTTP client once, at start, so a running one
		// keeps the CA it started with. There is no reload; say how to apply it.
		fmt.Println("  A running sync daemon keeps the CA it started with. Restart it to apply this:")
		fmt.Println("  'cctrace sync --stop', then start it again as usual (the next Claude Code")
		fmt.Println("  session starts it, or 'cctrace sync --daemon').")
		fmt.Println("  Codex's [otel] block is rewritten with the new CA when sync next starts.")
	}
	return nil
}

type setting struct {
	key   string
	value string
}

func flattenProfile(p *profile.Profile) []setting {
	mask := func(s string) string {
		if s == "" {
			return "(not set)"
		}
		if len(s) <= 8 {
			return "****"
		}
		return s[:4] + "****" + s[len(s)-4:]
	}

	return []setting{
		{"user.id", strOr(p.User.ID, "(not set)")},
		{"user.name", strOr(p.User.Name, "(not set)")},
		{"user.email", strOr(p.User.Email, "(not set)")},
		{"user.team", strOr(p.User.Team, "(not set)")},
		{"server.endpoint", strOr(p.Server.Endpoint, "(not set)")},
		{"server.sync_endpoint", strOr(p.Server.SyncEndpoint, "(not set)")},
		{"server.protocol", strOr(p.Server.Protocol, "grpc")},
		{"server.auth_token", mask(p.Server.AuthToken)},
		{"server.read_token", mask(p.Server.ReadToken)},
		{"server.ca_cert_file", strOr(p.Server.CACertFile, "(system roots)")},
		{"options.sync_enabled", strconv.FormatBool(p.Options.SyncEnabled)},
		{"options.redact_user_prompts", strconv.FormatBool(p.Options.RedactUserPrompts)},
		{"options.redact_tool_details", strconv.FormatBool(p.Options.RedactToolDetails)},
		{"options.codex_sync_enabled", strconv.FormatBool(p.Options.CodexSyncEnabled)},
		{"options.gjc_sync_enabled", strconv.FormatBool(p.Options.GjcSyncEnabled)},
		{"options.omo_sync_enabled", strconv.FormatBool(p.Options.OmoSyncEnabled)},
		{"options.metrics_export_interval", strconv.Itoa(p.Options.MetricsExportInterval)},
		{"options.logs_export_interval", strconv.Itoa(p.Options.LogsExportInterval)},
		{"options.collect_repository_prefixes", strOr(strings.Join(p.Options.CollectRepositoryPrefixes, ","), "(all)")},
		{"options.exclude_accounts", strOr(strings.Join(p.Options.ExcludeAccounts, ","), "(none)")},
		{"options.codex_dirs", strOr(strings.Join(p.Options.CodexDirs, ","), "(default only)")},
		{"options.gjc_dirs", strOr(strings.Join(p.Options.GjcDirs, ","), "(default only)")},
		{"options.omo_dirs", strOr(strings.Join(p.Options.OmoDirs, ","), "(default only)")},
	}
}

func strOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func setProfileField(p *profile.Profile, key, value string) error {
	switch key {
	case "user.id":
		p.User.ID = value
	case "user.name":
		p.User.Name = value
	case "user.email":
		p.User.Email = value
	case "user.team":
		p.User.Team = value
	case "server.endpoint":
		p.Server.Endpoint = value
	case "server.sync_endpoint":
		p.Server.SyncEndpoint = value
	case "server.protocol":
		p.Server.Protocol = value
	case "server.auth_token":
		p.Server.AuthToken = value
	case "server.read_token":
		p.Server.ReadToken = value
	case "server.ca_cert_file":
		path, err := normalizeCACertFile(value)
		if err != nil {
			return err
		}
		p.Server.CACertFile = path
	case "options.sync_enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool value: %s (use true/false)", value)
		}
		p.Options.SyncEnabled = b
	case "options.redact_user_prompts":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool value: %s (use true/false)", value)
		}
		p.Options.RedactUserPrompts = b
	case "options.redact_tool_details":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool value: %s (use true/false)", value)
		}
		p.Options.RedactToolDetails = b
	case "options.codex_sync_enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool value: %s (use true/false)", value)
		}
		setCodexSync(p, b)
	case "options.gjc_sync_enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool value: %s (use true/false)", value)
		}
		p.Options.GjcSyncEnabled = b
	case "options.omo_sync_enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid bool value: %s (use true/false)", value)
		}
		p.Options.OmoSyncEnabled = b
	case "options.metrics_export_interval":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid int value: %s", value)
		}
		p.Options.MetricsExportInterval = n
	case "options.logs_export_interval":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid int value: %s", value)
		}
		p.Options.LogsExportInterval = n
	case "options.collect_repository_prefixes":
		// Comma-separated list of repository id prefixes. Empty clears the
		// allowlist (collect all repositories).
		var prefixes []string
		for _, part := range strings.Split(value, ",") {
			if t := strings.TrimSpace(part); t != "" {
				prefixes = append(prefixes, t)
			}
		}
		p.Options.CollectRepositoryPrefixes = prefixes
	case "options.exclude_accounts":
		// Comma-separated account ids, each optionally provider-qualified.
		// Empty clears the list.
		var accounts []string
		for _, part := range strings.Split(value, ",") {
			t := strings.TrimSpace(part)
			if t == "" {
				continue
			}
			if strings.Contains(t, "@") {
				return fmt.Errorf("invalid account %q: use the billing account id, not a login address (session records carry no address)", t)
			}
			if provider, id, ok := strings.Cut(t, ":"); ok {
				t = strings.ToLower(strings.TrimSpace(provider)) + ":" + strings.TrimSpace(id)
			}
			accounts = append(accounts, t)
		}
		p.Options.ExcludeAccounts = accounts
	case "options.codex_dirs":
		dirs, err := parseHomeDirs(value, "codex")
		if err != nil {
			return err
		}
		p.Options.CodexDirs = dirs
	case "options.gjc_dirs":
		dirs, err := parseHomeDirs(value, "gjc")
		if err != nil {
			return err
		}
		p.Options.GjcDirs = dirs
	case "options.omo_dirs":
		dirs, err := parseHomeDirs(value, "omo")
		if err != nil {
			return err
		}
		p.Options.OmoDirs = dirs
	default:
		return fmt.Errorf("unknown key: %s\nAvailable keys: user.id, user.name, user.email, user.team, server.endpoint, server.sync_endpoint, server.protocol, server.auth_token, server.read_token, server.ca_cert_file, options.sync_enabled, options.redact_user_prompts, options.redact_tool_details, options.codex_sync_enabled, options.gjc_sync_enabled, options.omo_sync_enabled, options.metrics_export_interval, options.logs_export_interval, options.collect_repository_prefixes, options.exclude_accounts, options.codex_dirs, options.gjc_dirs, options.omo_dirs", key)
	}
	return nil
}

// normalizeCACertFile checks a server.ca_cert_file value and returns the path to
// store. An empty value clears the setting.
//
// The path is "~"-expanded and made absolute because it is read later by other
// processes -- the sync daemon, Claude Code, Codex -- none of which start in the
// directory the user typed it in. The file must already hold a PEM certificate:
// a wrong path stored here would surface only as a TLS failure on every channel,
// long after the command that caused it.
func normalizeCACertFile(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	// The path is echoed to the terminal and written into Claude Code's and
	// Codex's configuration; a control character is never a file name someone
	// meant, and printed back it can rewrite the line it appears on.
	if strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("ca cert file %q contains a control character", trimmed)
	}
	path, err := filepath.Abs(codexlog.ExpandHome(trimmed))
	if err != nil {
		return "", fmt.Errorf("ca cert file %s: %w", trimmed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("ca cert file: %w", err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(raw) {
		return "", fmt.Errorf("ca cert file %s holds no PEM certificate", path)
	}
	return path, nil
}

// parseHomeDirs parses the comma-separated value of an extra-home-directories
// option (options.codex_dirs, options.gjc_dirs) into a list of directories.
// label names the kind of directory in error messages (e.g. "codex", "gjc").
// An empty value clears the list (scan only the default home).
//
// Each entry is "~"-expanded and must be an existing absolute directory: a
// relative or missing path would otherwise be dropped without a word at sync
// time. Because "," is the entry separator, a directory whose name contains a
// comma cannot be expressed here; it splits into pieces that do not exist, so
// the resulting error carries that hint instead of silently storing garbage.
func parseHomeDirs(value, label string) ([]string, error) {
	var dirs []string
	for _, part := range strings.Split(value, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		expanded := codexlog.ExpandHome(trimmed)
		if !filepath.IsAbs(expanded) {
			return nil, fmt.Errorf("%s dir must be an absolute path or start with '~': %s%s", label, trimmed, codexDirCommaHint(value))
		}
		fi, err := os.Stat(expanded)
		if err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("%s dir does not exist or is not a directory: %s%s", label, expanded, codexDirCommaHint(value))
		}
		dirs = append(dirs, expanded)
	}
	return dirs, nil
}

// codexDirCommaHint explains the comma-splitting rule, but only when the value
// actually contains a comma and could therefore have been split apart.
func codexDirCommaHint(value string) string {
	if !strings.Contains(value, ",") {
		return ""
	}
	return "\n  note: ',' separates entries, so a directory whose name contains ',' cannot be configured here"
}

func loadProfile(profileName string) (*profile.Profile, error) {
	if profileName != "" {
		return profile.LoadNamed(profileName)
	}
	if !profile.Exists() {
		return nil, fmt.Errorf("no profile found; run 'cctrace init' first")
	}
	return profile.Load()
}

func saveProfile(p *profile.Profile, profileName string) error {
	if profileName != "" {
		return profile.SaveNamed(p, profileName)
	}
	return profile.Save(p)
}
