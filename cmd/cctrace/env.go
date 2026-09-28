package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"cctrace/internal/envgen"
	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

func envCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Manage OTEL environment and hook registration",
	}
	cmd.AddCommand(envApplyCmd())
	return cmd
}

func envApplyCmd() *cobra.Command {
	var all bool
	var profileName string

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Re-apply OTEL env and sync hooks using the current binary path",
		Long: `Re-registers Claude Code settings.json hooks to point at the currently
running cctrace binary. Use this after patching or moving the binary
to ensure hooks execute the correct version.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			bin := currentBinaryPath()
			fmt.Printf("  Binary: %s\n\n", bin)

			if profileName != "" {
				return applyNamedProfile(profileName)
			}

			if err := applyDefaultProfile(); err != nil {
				return err
			}

			if all {
				names, err := profile.ListNamed()
				if err != nil {
					return fmt.Errorf("list named profiles: %w", err)
				}
				for _, name := range names {
					if err := applyNamedProfile(name); err != nil {
						fmt.Fprintf(os.Stderr, "  [!] %s: %v\n", name, err)
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Apply to default profile and all named profiles")
	cmd.Flags().StringVar(&profileName, "profile", "", "Apply to a specific named profile only")
	return cmd
}

func applyDefaultProfile() error {
	p, err := profile.Load()
	if err != nil {
		return fmt.Errorf("load default profile: %w", err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		return fmt.Errorf("apply default profile: %w", err)
	}
	settingsPath, _ := envgen.ClaudeSettingsPath(p)
	fmt.Printf("  [OK] default → %s\n", settingsPath)
	return nil
}

func applyNamedProfile(name string) error {
	p, err := profile.LoadNamed(name)
	if err != nil {
		return fmt.Errorf("load profile %q: %w", name, err)
	}
	if err := envgen.ApplyToClaudeSettings(p); err != nil {
		return fmt.Errorf("apply profile %q: %w", name, err)
	}
	settingsPath, _ := envgen.ClaudeSettingsPath(p)
	fmt.Printf("  [OK] %s → %s\n", name, settingsPath)
	return nil
}

// currentBinaryPath returns the absolute path of the running binary in a
// human-readable form (Windows paths converted to forward-slash notation).
func currentBinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "cctrace"
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if runtime.GOOS == "windows" {
		exe = filepath.ToSlash(exe)
		if len(exe) >= 2 && exe[1] == ':' {
			exe = "/" + strings.ToLower(string(exe[0])) + exe[2:]
		}
	}
	return exe
}
