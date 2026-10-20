package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

func profileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage shared profiles",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if recover, _ := cmd.Flags().GetBool("recover"); recover {
				return nil
			}
			_, err := (profile.Registry{Root: profile.DefaultDir()}).MigrateDesktop(profile.LegacyDesktopRoot())
			return err
		},
	}
	cmd.AddCommand(profileOnboardingCmd())
	cmd.AddCommand(profileTransmissionCmd())
	cmd.AddCommand(profileMembershipCmd())
	cmd.AddCommand(profileAccountsCmd())
	cmd.AddCommand(profileAddCmd())
	cmd.AddCommand(profileListCmd())
	cmd.AddCommand(profileRemoveCmd())
	cmd.AddCommand(profileClaimCmd())
	cmd.AddCommand(profileSelectCmd())
	cmd.AddCommand(profileSelectedCmd())
	cmd.AddCommand(profileRegisterCmd())
	cmd.AddCommand(profileEditCmd())
	cmd.AddCommand(profileResolveCmd())
	return cmd
}

// profileAddCmd retains the old command spelling while directing all new-home
// enrollment, including legacy registries, through the required consent flow.
func profileAddCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "add <name>", Short: "Enroll new homes through profile onboard", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if err := profile.ValidateName(args[0]); err != nil {
			return err
		}
		return fmt.Errorf("new provider homes require cctrace profile onboard and explicit transmission decisions; profile add does not clone collection settings")
	}}
	cmd.Flags().String("home", "", "Provider home (enroll with profile onboard)")
	cmd.Flags().String("codex-home", "", "Codex home (enroll with profile onboard)")
	_ = cmd.MarkFlagRequired("home")
	return cmd
}

func profileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List named profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			registry := profile.Registry{Root: profile.DefaultDir()}
			entries, err := registry.List()
			if err != nil {
				return err
			}
			named := 0
			for _, entry := range entries {
				p := entry.Profile
				if entry.Name != "default" {
					named++
				}
				if p.HomesVersion == 1 {
					fmt.Fprintf(out, "  [%s] id=%s collection=%s configuration_complete=%t revision=%s\n", entry.Name, entry.ID, entry.CollectionState, entry.ConfigurationComplete, entry.Revision)
					fmt.Fprintf(out, "    Collection execution: Claude=%t Codex=%t\n", p.CollectionEnabled("claude"), p.CollectionEnabled("codex"))
					for _, h := range p.Homes {
						fmt.Fprintf(out, "    Home: provider=%s path=%s transmission=%s usage_enrollment_consent=%t\n", h.Provider, h.Home, h.Transmission, h.QuotaEnabled)
					}
					fmt.Fprintln(out, "    Current usage preferences: cctrace profile accounts")
					continue
				}
				home := p.ClaudeConfigDir
				if home == "" {
					home = "(default)"
				}
				codex := p.CodexHome
				if codex == "" {
					codex = "(default)"
				}
				fmt.Fprintf(out, "  [%s]  %s  id=%s collection=%s configuration_complete=%t revision=%s\n", entry.Name, home, entry.ID, entry.CollectionState, entry.ConfigurationComplete, entry.Revision)
				fmt.Fprintf(out, "    Codex home: %s\n", codex)
				for _, dir := range p.Options.CodexDirs {
					fmt.Fprintf(out, "    Additional Codex scan: %s\n", dir)
				}
			}
			conflicts, err := registry.MigrateDesktop(profile.LegacyDesktopRoot())
			if err != nil {
				return err
			}
			for _, conflict := range conflicts {
				fmt.Fprintf(out, "  Conflict [%s]: CLI Claude=%s Codex=%s; previous app Claude=%s Codex=%s\n", conflict.Name, conflict.CLIClaudeHome, conflict.CLICodexHome, conflict.DesktopClaudeHome, conflict.DesktopCodexHome)
				fmt.Fprintf(out, "    Select folders explicitly with profile resolve; cli_revision=%s desktop_revision=%s\n", conflict.CLIRevision, conflict.DesktopRevision)
			}
			if named == 0 {
				fmt.Fprintln(out, "  No named profiles.")
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

func profileSelectCmd() *cobra.Command {
	var agent string
	var expectedRevision uint64
	cmd := &cobra.Command{
		Use:   "select <name|default>",
		Short: "Choose the profile for the next agent launch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if agent != "claude" && agent != "codex" {
				return fmt.Errorf("--agent must be claude or codex")
			}
			p, err := loadAgentProfile(args[0])
			if err != nil {
				return err
			}
			if _, err := agentProfileHome(agent, p); err != nil {
				return fmt.Errorf("selected %s profile %q is unavailable: %w", agent, args[0], err)
			}
			var expected *uint64
			if cmd.Flags().Changed("revision") {
				expected = &expectedRevision
			}
			selection, err := profile.UpdateSelection(profile.SelectionPath(), agent, args[0], expected)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s profile=%s revision=%d\n", agent, args[0], selection.Revision)
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "claude", "Agent to select for (claude or codex)")
	cmd.Flags().Uint64Var(&expectedRevision, "revision", 0, "Require this current selection revision (compare-and-swap)")
	return cmd
}

func profileSelectedCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "selected",
		Short: "Show the profile selected for the next launch",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if agent != "claude" && agent != "codex" {
				return fmt.Errorf("--agent must be claude or codex")
			}
			selection, err := profile.ReadSelection(profile.SelectionPath())
			if err != nil {
				return err
			}
			name := selection.Claude
			if agent == "codex" {
				name = selection.Codex
			}
			if name == "" {
				name = "default"
			}
			p, err := loadAgentProfile(name)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "%s profile=%s status=unavailable reason=%q revision=%d\n", agent, name, "profile missing", selection.Revision)
				return nil
			}
			_, accountID, homeErr := agentProfileIdentity(agent, p)
			status := "ready"
			reason := ""
			if homeErr != nil {
				status = "unavailable"
				reason = homeErr.Error()
			} else if agent == "codex" {
				binary, err := exec.LookPath("codex")
				if err != nil {
					status = "unavailable"
					reason = "Codex CLI executable is unavailable"
				} else if err := verifyCodexLaunchVersion(cmd.Context(), binary); err != nil {
					status = "unavailable"
					reason = "Codex CLI version is not verified for isolated CODEX_HOME launch"
				} else if accountID == "" {
					status = "home-ready-account-unavailable"
					reason = "local authentication metadata has no account id"
				}
			} else if accountID == "" {
				status = "home-ready-account-unavailable"
				reason = "local authentication metadata has no account id"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s profile=%s status=%s", agent, name, status)
			if reason != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " reason=%q", reason)
			}
			fmt.Fprintf(cmd.OutOrStdout(), " revision=%d\n", selection.Revision)
			return nil
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "claude", "Agent to inspect (claude or codex)")
	return cmd
}

// Registration has the same disabled effect as the desktop; legacy add still
// clones server/user/options and applies provider integration settings.
func profileRegisterCmd() *cobra.Command {
	var claude, codex string
	cmd := &cobra.Command{Use: "register <name>", Short: "Register folders with collection disabled and provider settings untouched", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		registry := profile.Registry{Root: profile.DefaultDir()}
		entry, err := registry.RegisterMapping(args[0], profileFolder(claude), profileFolder(codex))
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  Registered %s; collection is off. Configure collection explicitly with cctrace init --profile %s.\n", entry.ID, entry.Name)
		return nil
	}}
	cmd.Flags().StringVar(&claude, "home", "", "Claude home folder (optional)")
	cmd.Flags().StringVar(&codex, "codex-home", "", "Codex execution home folder (optional)")
	return cmd
}
func profileEditCmd() *cobra.Command {
	var claude, codex, expected string
	cmd := &cobra.Command{Use: "edit <name>", Short: "Edit shared profile folders without changing provider settings", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		registry := profile.Registry{Root: profile.DefaultDir()}
		conflicts, err := registry.MigrateDesktop(profile.LegacyDesktopRoot())
		if err != nil {
			return err
		}
		for _, conflict := range conflicts {
			if conflict.Name == args[0] {
				return fmt.Errorf("profile has a migration conflict; use profile resolve first")
			}
		}
		existing, err := registry.Load(args[0])
		if err != nil {
			return err
		}
		if expected == "" {
			expected = existing.Revision
		}
		if !cmd.Flags().Changed("home") {
			claude = existing.Profile.ClaudeConfigDir
		}
		if !cmd.Flags().Changed("codex-home") {
			codex = existing.Profile.CodexHome
		}
		entry, err := registry.UpdateMapping(args[0], expected, profileFolder(claude), profileFolder(codex))
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  Updated %s; collection state remains %s and provider settings are untouched.\n", entry.ID, entry.CollectionState)
		return nil
	}}
	cmd.Flags().StringVar(&claude, "home", "", "Claude home folder")
	cmd.Flags().StringVar(&codex, "codex-home", "", "Codex execution home folder")
	cmd.Flags().StringVar(&expected, "revision", "", "Expected revision from profile list (reject stale edits)")
	return cmd
}
func profileFolder(value string) string {
	if value == "" {
		return ""
	}
	value = expandTilde(value)
	if !filepath.IsAbs(value) {
		cwd, _ := os.Getwd()
		value = filepath.Join(cwd, value)
	}
	return filepath.Clean(value)
}

func profileResolveCmd() *cobra.Command {
	var source, cliRevision, desktopRevision string
	cmd := &cobra.Command{Use: "resolve <name>", Short: "Select CLI or previous app folders without copying provider data", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if source != "cli" && source != "desktop" {
			return fmt.Errorf("source must be cli or desktop")
		}
		if cliRevision == "" || desktopRevision == "" {
			return fmt.Errorf("both revisions from profile list are required")
		}
		err := (profile.Registry{Root: profile.DefaultDir()}).ResolveMigration(profile.LegacyDesktopRoot(), args[0], source, cliRevision, desktopRevision)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  Selected %s folders for %s; original definitions and provider files remain in place.\n", source, profile.ProfileID(args[0]))
		return nil
	}}
	cmd.Flags().StringVar(&source, "source", "", "Folders to select: cli or desktop")
	cmd.Flags().StringVar(&cliRevision, "cli-revision", "", "CLI revision from profile list")
	cmd.Flags().StringVar(&desktopRevision, "desktop-revision", "", "Previous app revision from profile list")
	return cmd
}
