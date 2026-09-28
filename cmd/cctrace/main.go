package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	os.Exit(execute(newRootCmd(), os.Stderr))
}

// execute runs the command tree and reports a failure exactly once. cobra's own
// error print is silenced so it does not repeat what is written here.
func execute(root *cobra.Command, stderr io.Writer) int {
	root.SilenceErrors = true
	if err := root.Execute(); err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	return 0
}

// newRootCmd assembles the command tree. It is separate from main so a test can
// enumerate what `cctrace --help` will list without running the binary -- the
// documented command table is checked against this set.
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "cctrace",
		Short:   "Claude Code Trace - OTEL telemetry profile manager",
		Version: version,
	}

	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(resetCmd())
	rootCmd.AddCommand(syncCmd())
	rootCmd.AddCommand(backfillQuotaCmd())
	rootCmd.AddCommand(reportCmd())
	rootCmd.AddCommand(usageCmd())
	rootCmd.AddCommand(lsCmd())
	rootCmd.AddCommand(eventsCmd())
	rootCmd.AddCommand(projectsCmd())
	rootCmd.AddCommand(openAPIResourceCmd("tools", true))
	rootCmd.AddCommand(openAPIResourceCmd("plugins", true))
	rootCmd.AddCommand(openAPIResourceCmd("skills", true))
	rootCmd.AddCommand(openAPIResourceCmd("rules", false))
	rootCmd.AddCommand(openAPIResourceCmd("organization-insights", true))
	rootCmd.AddCommand(insightsCmd())
	rootCmd.AddCommand(authCmd())
	rootCmd.AddCommand(sessionsCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(killCmd())
	rootCmd.AddCommand(uninstallCmd())
	rootCmd.AddCommand(profileCmd())
	rootCmd.AddCommand(envCmd())

	return rootCmd
}
