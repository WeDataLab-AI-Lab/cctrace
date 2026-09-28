package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cctrace/internal/envgen"
	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

func profileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage named profiles",
	}
	cmd.AddCommand(profileAddCmd())
	cmd.AddCommand(profileListCmd())
	cmd.AddCommand(profileRemoveCmd())
	return cmd
}

// profileAddCmd adds a named profile by cloning the default profile
// and pointing it at a different Claude home directory.
func profileAddCmd() *cobra.Command {
	var home string

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a named profile pointing to a different Claude home",
		Long: `Add a named profile that inherits the default profile's server/user/options
and points to a different Claude home directory (--home).

Example:
  cctrace profile add claude-4 --home ~/.claude-4`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if err := profile.ValidateName(name); err != nil {
				return err
			}

			// Refuse to overwrite an existing profile without explicit reset.
			if path, _ := profile.NamedPath(name); path != "" {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("profile %q already exists; run 'cctrace reset --profile %s' first", name, name)
				}
			}

			// Load default profile as base.
			base, err := profile.Load()
			if err != nil {
				return fmt.Errorf("default profile not found; run 'cctrace init' first: %w", err)
			}

			// Resolve and expand home path.
			claudeHome := expandTilde(home)
			if !filepath.IsAbs(claudeHome) {
				cwd, _ := os.Getwd()
				claudeHome = filepath.Join(cwd, claudeHome)
			}

			// Warn if the directory doesn't exist yet (but allow it).
			if _, err := os.Stat(claudeHome); os.IsNotExist(err) {
				fmt.Printf("  [WARN] Claude home %q does not exist yet.\n", claudeHome)
			}

			// Check that another profile isn't already using this Claude home.
			names, _ := profile.ListNamed()
			for _, n := range names {
				existing, err := profile.LoadNamed(n)
				if err != nil {
					continue
				}
				if existing.ClaudeConfigDir == claudeHome {
					return fmt.Errorf("profile %q already uses Claude home %q", n, claudeHome)
				}
			}

			now := time.Now().UTC()
			np := &profile.Profile{
				Version:         base.Version,
				User:            base.User,
				Server:          base.Server,
				Options:         base.Options,
				CreatedAt:       now,
				UpdatedAt:       now,
				ClaudeConfigDir: claudeHome,
			}

			if err := profile.SaveNamed(np, name); err != nil {
				return fmt.Errorf("save profile: %w", err)
			}

			fmt.Printf("  Profile %q created → %s\n", name, claudeHome)

			// Apply OTEL environment to the target Claude home's settings.json.
			if err := envgen.ApplyToClaudeSettings(np); err != nil {
				fmt.Printf("  [WARN] Could not apply OTEL env to settings.json: %v\n", err)
			} else {
				settingsPath, _ := envgen.ClaudeSettingsPath(np)
				fmt.Printf("  OTEL env applied → %s\n", settingsPath)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&home, "home", "", "Claude home directory (e.g. ~/.claude-4)")
	_ = cmd.MarkFlagRequired("home")
	return cmd
}

func profileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List named profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			names, err := profile.ListNamed()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Fprintln(out, "  No named profiles.")
				return nil
			}
			for _, name := range names {
				p, err := profile.LoadNamed(name)
				if err != nil {
					fmt.Fprintf(out, "  [%s]  (error: %v)\n", name, err)
					continue
				}
				home := p.ClaudeConfigDir
				if home == "" {
					home = "(default)"
				}
				fmt.Fprintf(out, "  [%s]  %s\n", name, home)
			}
			return nil
		},
	}
}

func profileRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a named profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := profile.ValidateName(name); err != nil {
				return err
			}
			if !force {
				fmt.Printf("Remove profile %q? [y/N] ", name)
				var answer string
				fmt.Scanln(&answer)
				if !strings.EqualFold(strings.TrimSpace(answer), "y") {
					fmt.Println("Aborted.")
					return nil
				}
			}
			if err := profile.RemoveNamed(name); err != nil {
				return err
			}
			fmt.Printf("  Profile %q removed.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation")
	return cmd
}
