package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"cctrace/internal/desktop"
	"cctrace/internal/profile"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, input io.Reader, output, diagnostics io.Writer) int {
	return runWithProviderReader(args, input, output, diagnostics, nil)
}

func runWithProviderReader(args []string, input io.Reader, output, diagnostics io.Writer, providerReader desktop.ProviderQuotaReader) int {
	flags := flag.NewFlagSet("cctrace-desktop-helper", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	appSupport := flags.String("app-support", "", "isolated absolute application support root")
	homeDiscoveryRoot := flags.String("home-discovery-root", "", "fixture-only canonical private provider-home discovery root (requires fixture-root and profile-root)")
	fixtureRoot := flags.String("fixture-root", "", "isolated absolute fixture root")
	profileRoot := flags.String("profile-root", "", "explicit absolute shared profile registry root")
	daemonRoot := flags.String("daemon-root", "", "explicit absolute CLI daemon status root")
	clockValue := flags.String("clock", "", "fixed RFC3339 clock for fixture freshness")
	providerMode := flags.Bool("provider-mode", false, "read quota from explicitly configured provider homes")
	claudeHome := flags.String("claude-home", "", "explicit Claude configuration home for provider mode")
	codexHome := flags.String("codex-home", "", "explicit Codex home for provider mode")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(diagnostics, "usage: cctrace-desktop-helper --app-support <absolute-root> [--fixture-root <absolute-root> | --provider-mode [--daemon-root <absolute-root>] [--profile-root <absolute-root>] [--claude-home <absolute-home>] [--codex-home <absolute-home>]] [--clock <RFC3339>]\n")
			return 0
		}
		_, _ = io.WriteString(diagnostics, "configuration_error\n")
		return 2
	}
	if flags.NArg() != 0 || *appSupport == "" {
		_, _ = io.WriteString(diagnostics, "configuration_error\n")
		return 2
	}
	if *homeDiscoveryRoot != "" && (*providerMode || *fixtureRoot == "" || *profileRoot == "") {
		_, _ = io.WriteString(diagnostics, "configuration_error\n")
		return 2
	}
	if *providerMode && *clockValue != "" {
		_, _ = io.WriteString(diagnostics, "configuration_error\n")
		return 2
	}

	config := desktop.Config{
		HomeDiscoveryRoot: *homeDiscoveryRoot,
		AppSupportRoot:    *appSupport,
		FixtureRoot:       *fixtureRoot,
		DaemonRoot:        *daemonRoot,
		ProviderMode:      *providerMode,
		ClaudeHome:        *claudeHome,
		CodexHome:         *codexHome,
		ProviderReader:    providerReader,
		ProfileRoot:       *profileRoot,
	}
	if *providerMode && *profileRoot == "" {
		config.ProfileRoot = profile.DefaultDir()
		if *daemonRoot != "" {
			config.ProfileRoot = *daemonRoot
		}
	}
	if *clockValue != "" {
		fixedTime, err := time.Parse(time.RFC3339Nano, *clockValue)
		if err != nil {
			_, _ = io.WriteString(diagnostics, "configuration_error\n")
			return 2
		}
		config.Now = func() time.Time { return fixedTime }
	}
	service, err := desktop.New(config)
	if err != nil {
		_, _ = io.WriteString(diagnostics, "configuration_error\n")
		return 2
	}
	if err := desktop.Serve(context.Background(), service, input, output); err != nil {
		_, _ = fmt.Fprintln(diagnostics, "protocol_stream_error")
		return 1
	}
	return 0
}
