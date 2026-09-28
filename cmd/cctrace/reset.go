package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"cctrace/internal/envgen"
	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

func resetCmd() *cobra.Command {
	var force bool
	var all bool
	var profileName string

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Remove cctrace profile, env files, and shell modifications",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReset(force, all, profileName)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&all, "all", false, "Remove default profile AND all named profiles")
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to reset (removes entire profile directory)")
	return cmd
}

func runReset(force bool, all bool, profileName string) error {
	if !force {
		reader := bufio.NewReader(os.Stdin)
		if all {
			fmt.Print("  Remove ALL cctrace configuration (default + all named profiles)? [y/N]: ")
		} else if profileName != "" {
			fmt.Printf("  Remove named profile %q? [y/N]: ", profileName)
		} else {
			fmt.Print("  Remove default cctrace configuration? [y/N]: ")
		}
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("  Cancelled.")
			return nil
		}
	}

	fmt.Println()

	if profileName != "" {
		return runResetNamed(profileName)
	}

	if err := runResetDefault(); err != nil {
		return err
	}

	if all {
		return runResetAllNamed()
	}

	// Warn about remaining named profiles
	names, err := profile.ListNamed()
	if err == nil && len(names) > 0 {
		fmt.Printf("  Note: %d named profile(s) still exist: %s\n", len(names), strings.Join(names, ", "))
		fmt.Println("  Use 'cctrace reset --all' to remove everything.")
		fmt.Println()
	}

	return nil
}

func runResetAllNamed() error {
	names, err := profile.ListNamed()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := runResetNamed(name); err != nil {
			fmt.Printf("  [!] Failed to reset profile %q: %v\n", name, err)
		}
	}
	return nil
}

func runResetNamed(profileName string) error {
	namedDir, err := profile.NamedDir(profileName)
	if err != nil {
		return err
	}

	if _, err := os.Stat(namedDir); os.IsNotExist(err) {
		fmt.Printf("  [--] Profile %q already absent\n", profileName)
		fmt.Println()
		return nil
	}

	// Remove OTEL env vars from Claude settings.json before deleting profile
	if p, err := profile.LoadNamed(profileName); err == nil {
		if err := envgen.RemoveFromClaudeSettings(p); err == nil {
			settingsPath, _ := envgen.ClaudeSettingsPath(p)
			fmt.Printf("  [OK] Removed OTEL vars + sync hooks from %s\n", settingsPath)
		}
	}

	if err := profile.RemoveNamed(profileName); err != nil {
		return fmt.Errorf("failed to remove profile %q: %w", profileName, err)
	}
	fmt.Printf("  [OK] Removed profile %q (%s)\n", profileName, namedDir)
	fmt.Println()
	return nil
}

func runResetDefault() error {
	// 0. Remove OTEL env vars from Claude settings.json
	if p, err := profile.Load(); err == nil {
		if err := envgen.RemoveFromClaudeSettings(p); err == nil {
			settingsPath, _ := envgen.ClaudeSettingsPath(p)
			fmt.Printf("  [OK] Removed OTEL vars + sync hooks from %s\n", settingsPath)
		}
	}

	// 1. Remove profile.json
	profilePath := profile.DefaultPath()
	if profile.Exists() {
		if err := profile.Remove(); err != nil {
			return fmt.Errorf("failed to remove profile: %w", err)
		}
		fmt.Printf("  [OK] Removed %s\n", profilePath)
	} else {
		fmt.Printf("  [--] %s (already absent)\n", profilePath)
	}

	// 2. Remove env.sh and env.ps1
	fmt.Println()
	return nil
}
