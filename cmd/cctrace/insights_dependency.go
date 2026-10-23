package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"cctrace/internal/codexlog"
	"cctrace/internal/dependencyaudit"
	"cctrace/internal/openinsights"
	"cctrace/internal/profile"
	"cctrace/internal/sessionlog"
)

// insightsDependencyCmd finds where work returned to the person in local
// session files: the default homes and every home a profile registers. For Claude it
// then adds what only the server holds, when the profile can read it. The
// agent is required so that one agent's transcripts are not read for a skill
// running on the other before the person agreed.
func insightsDependencyCmd() *cobra.Command {
	var window, agent, profileName string
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "dependency",
		Short:        "Find where work returned to you in local sessions, with the server's approvals when it can be read",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if agent != "claude" && agent != "codex" && agent != "all" {
				return fmt.Errorf("--agent must be claude, codex or all")
			}
			span, err := parseWindow(window)
			if err != nil {
				return err
			}
			if profileName == "" {
				profileName = os.Getenv("CCTRACE_PROFILE")
			}
			var p *profile.Profile
			if profileName != "" {
				if p, err = loadProfile(profileName); err != nil {
					return fmt.Errorf("load profile: %w", err)
				}
			} else if loaded, err := loadProfile(""); err == nil {
				p = loaded
			}
			now := time.Now().UTC().Truncate(time.Second)
			opts := dependencyaudit.Options{
				Agent: agent, Since: now.Add(-span), Now: now,
				ClaudeDirs: []string{sessionlog.DefaultClaudeDir()}, CodexDirs: codexlog.ResolveScanDirs(nil),
				Transmission: map[dependencyaudit.HomeKey]string{},
			}
			for _, name := range []string{"claude", "codex"} {
				for _, h := range dependencyProfileHomes(name) {
					if name == "claude" {
						opts.ClaudeDirs = append(opts.ClaudeDirs, h.path)
					} else {
						opts.CodexDirs = append(opts.CodexDirs, h.path)
					}
					opts.Transmission[dependencyaudit.HomeKey{Agent: name, Path: h.path}] = h.transmission
				}
			}
			res, err := dependencyaudit.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if agent != "codex" {
				readDependencyServer(cmd.Context(), p, res)
			}
			return writeInsights(cmd.OutOrStdout(), res, asJSON)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Sessions to read: claude, codex, or all (required)")
	cmd.Flags().StringVar(&window, "since", "30d", "Window length (30d, 24h, 90m)")
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile whose server and tokens the server read uses (overrides CCTRACE_PROFILE); the local homes read are the same")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output compact JSON")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

// readDependencyServer adds the server's Claude decisions and practices to a
// local result. Support is read from the capability flag, never guessed from
// an empty answer. Whatever cannot be read is left out with a caveat; the
// local result stands either way.
func readDependencyServer(ctx context.Context, p *profile.Profile, res *dependencyaudit.Result) {
	note := func(code, detail string) {
		res.Caveats = append(res.Caveats, dependencyaudit.Caveat{Code: code, Detail: detail})
	}
	const missing = "approvals recorded only on the server (other devices, older clients, deleted session files) are not included"
	failed := func() { note("server_read_failed", "a server read failed; "+missing+", nor the practices comparison") }
	endpoint := ""
	if p != nil {
		endpoint = profileHTTPAPIEndpoint(p)
	}
	if endpoint == "" {
		note("server_not_configured", "no profile names a server; "+missing+", nor the practices comparison")
		return
	}
	token := openAPIToken(p)
	if token == "" {
		note("no_read_token", "the profile has no read token (cctrace auth read issues one); "+missing+", nor the practices comparison")
		return
	}
	tr, err := serverTransport(p.Server.CACertFile)
	if err != nil {
		failed()
		return
	}
	client := func(token string) *openinsights.Client {
		c := openinsights.NewClient(endpoint, token)
		if tr != nil {
			c.SetTransport(tr)
		}
		return c
	}
	// The capability route takes the upload token; the read token is refused there.
	supported, uploadRefused := false, p.Server.AuthToken == ""
	if !uploadRefused {
		supported, err = client(p.Server.AuthToken).DecisionSourceSupported(ctx)
		uploadRefused = errors.Is(err, openinsights.ErrUploadTokenRefused)
		if err != nil && !uploadRefused {
			failed()
			return
		}
	}
	read := client(token)
	ev := &dependencyaudit.ServerEvidence{Agents: []string{"claude"}, DecisionSource: "unsupported"}
	if uploadRefused {
		ev.DecisionSource = "unknown"
	}
	practices, err := read.Practices(ctx)
	var apiErr *openinsights.APIError
	switch {
	case err == nil:
		ev.Practices = dependencyaudit.ServerPractices(practices)
	case errors.As(err, &apiErr) && (apiErr.Code == "ingestion_token" || apiErr.Status == http.StatusUnauthorized):
		note("no_read_token", "the server refused the profile's token for reading (cctrace auth read issues a read token); "+missing+", nor the practices comparison")
		return
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		note("practices_unsupported", "the server has no practices endpoint; the practices comparison is not included")
	default:
		failed()
		return
	}
	res.Server = ev
	if uploadRefused {
		note("no_upload_token", "the profile has no upload token (auth_token), or the server refused it, so whether the server supports decision sources cannot be asked; "+missing)
		return
	}
	if !supported {
		note("decision_source_unsupported", "the server does not announce open_api_decision_source; "+missing)
		return
	}
	ev.DecisionSource = "supported"
	scope, err := read.Scope(ctx)
	switch {
	case err != nil:
		note("server_read_failed", "a server read failed; "+missing)
		return
	case scope == openinsights.ScopeAdmin:
		note("admin_scope", "read with an administrator token, which covers every user; server approvals are not merged")
		return
	case scope == openinsights.ScopeUnknown:
		note("role_unknown", "the token's role could not be confirmed, so it is treated as an administrator's; server approvals are not merged")
		return
	}
	w := openinsights.Window{Since: res.Window.Since, Until: res.Window.Until}
	rows, truncated, err := read.DecisionEvents(ctx, w, dependencyaudit.ServerDecisionSources)
	if err != nil {
		note("server_read_failed", "a server read failed; "+missing)
		return
	}
	ev.Decisions = res.MergeDecisions(rows, truncated)
	if truncated {
		note("server_decisions_truncated", "the server's decisions exceeded the row cap; the oldest are missing")
	}
}

// dependencyHome is a home a profile registers, by its canonical path, with
// the transmission choice the registering profiles made.
type dependencyHome struct{ path, transmission string }

// dependencyProfileHomes lists every home the profiles register for agent:
// each membership of a multi-home profile, whatever its transmission choice,
// or a single-home profile's explicit home (allow). Reading them here sends
// nothing. A home profiles register with different choices is "mixed".
// local serve keeps its own, narrower rules (localProfileHomes).
func dependencyProfileHomes(agent string) []dependencyHome {
	names, err := profile.ListNamed()
	if err != nil {
		return nil
	}
	var homes []dependencyHome
	index := map[string]int{}
	for _, name := range append([]string{"default"}, names...) {
		p, err := loadAgentProfile(name)
		if err != nil {
			continue
		}
		for _, m := range p.ProviderHomes(agent, "") {
			abs, err := filepath.Abs(codexlog.ExpandHome(m.Home))
			if err != nil {
				continue
			}
			home, err := filepath.EvalSymlinks(abs)
			if err != nil {
				continue
			}
			if info, err := os.Stat(home); err != nil || !info.IsDir() {
				continue
			}
			if i, ok := index[home]; ok {
				if homes[i].transmission != m.Transmission {
					homes[i].transmission = "mixed"
				}
				continue
			}
			index[home] = len(homes)
			homes = append(homes, dependencyHome{home, m.Transmission})
		}
	}
	return homes
}
