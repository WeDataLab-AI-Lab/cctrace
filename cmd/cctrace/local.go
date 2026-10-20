package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"cctrace/internal/codexlog"
	"cctrace/internal/localapi"
	"cctrace/internal/profile"
	"cctrace/internal/sessionlog"
)

type localServeOptions struct {
	Address                        string
	Origins, ClaudeDirs, CodexDirs []string
}

func localCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "local", Short: "Read local sessions without synchronization"}
	opts := localServeOptions{}
	serve := &cobra.Command{Use: "serve", Short: "Serve local sessions on loopback", RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runLocalServe(ctx, cmd.OutOrStdout(), opts)
	}}
	serve.Flags().StringVar(&opts.Address, "address", "127.0.0.1:0", "literal loopback address")
	serve.Flags().StringSliceVar(&opts.Origins, "origin", nil, "allowed web origin (repeatable; required)")
	serve.Flags().StringSliceVar(&opts.ClaudeDirs, "claude-dir", nil, "Claude configuration directory (repeatable)")
	serve.Flags().StringSliceVar(&opts.CodexDirs, "codex-dir", nil, "Codex home directory (repeatable)")
	_ = serve.MarkFlagRequired("origin")
	cmd.AddCommand(serve)
	cmd.AddCommand(localPolicyCommands()...)
	return cmd
}
func runLocalServe(ctx context.Context, out io.Writer, opts localServeOptions) error {
	if len(opts.Origins) == 0 {
		return errors.New("--origin is required")
	}
	host, _, err := net.SplitHostPort(opts.Address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("--address must be a literal loopback IP")
	}
	useProfileHomes := len(opts.ClaudeDirs) == 0 && len(opts.CodexDirs) == 0
	if useProfileHomes {
		opts.ClaudeDirs = []string{sessionlog.DefaultClaudeDir()}
		opts.CodexDirs = codexlog.ResolveScanDirs(nil)
	}
	if useProfileHomes {
		opts.ClaudeDirs = append(opts.ClaudeDirs, localProfileHomes("claude")...)
		opts.CodexDirs = append(opts.CodexDirs, localProfileHomes("codex")...)
	}
	for i, dir := range opts.ClaudeDirs {
		opts.ClaudeDirs[i] = filepath.Clean(codexlog.ExpandHome(strings.TrimSpace(dir)))
	}
	for i, dir := range opts.CodexDirs {
		opts.CodexDirs[i] = filepath.Clean(codexlog.ExpandHome(strings.TrimSpace(dir)))
	}
	listener, err := net.Listen("tcp", opts.Address)
	if err != nil {
		return err
	}
	defer listener.Close()
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	server, err := localapi.New(localapi.Options{
		Address: listener.Addr().String(), Token: token, AllowedOrigins: opts.Origins,
		ClaudeDirs: opts.ClaudeDirs, CodexDirs: opts.CodexDirs,
		Profiles: localProfileCatalog(), SelectionPath: profile.SelectionPath(),
	})
	if err != nil {
		return err
	}
	bootstrap := struct {
		Endpoint           string   `json:"endpoint"`
		Token              string   `json:"token"`
		ProfileFingerprint string   `json:"profile_fingerprint"`
		Origins            []string `json:"origins"`
	}{"http://" + listener.Addr().String(), token, server.Fingerprint(), opts.Origins}
	if err = json.NewEncoder(out).Encode(bootstrap); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Local session API active; stop with Ctrl-C. Keep the token private.")
	return server.Serve(ctx, listener)
}

// localProfileHomes adds only explicitly configured profile homes. CodexDirs
// and CODEX_HOME remain scan roots and never become execution-home choices.
func localProfileHomes(agent string) []string {
	names, err := profile.ListNamed()
	if err != nil {
		return nil
	}
	profileNames := append([]string{"default"}, names...)
	seen := make(map[string]bool)
	var homes []string
	for _, name := range profileNames {
		p, err := loadAgentProfile(name)
		if err != nil {
			continue
		}
		home := ""
		if agent == "claude" {
			home = p.ClaudeConfigDir
			if home == "" {
				continue
			}
			abs, err := filepath.Abs(codexlog.ExpandHome(home))
			if err != nil {
				continue
			}
			home, err = filepath.EvalSymlinks(abs)
			if err != nil {
				continue
			}
			info, err := os.Stat(home)
			if err != nil || !info.IsDir() {
				continue
			}
		} else if agent == "codex" {
			if profile.CheckCodexHome(p.CodexHome) != nil {
				continue
			}
			home = p.CodexHome
		} else {
			return nil
		}
		if !seen[home] {
			seen[home] = true
			homes = append(homes, home)
		}
	}
	return homes
}

// localProfileCatalog reads only locally cached provider account ids. It never
// returns profile paths, email addresses, or auth file contents to the bridge.
func localProfileCatalog() []localapi.ProfileIdentity {
	names, err := profile.ListNamed()
	if err != nil {
		return nil
	}
	profileNames := append([]string{"default"}, names...)
	var result []localapi.ProfileIdentity
	for _, agent := range []string{"claude", "codex"} {
		provider := "anthropic"
		if agent == "codex" {
			provider = "openai"
		}
		for _, name := range profileNames {
			p, err := loadAgentProfile(name)
			if err != nil {
				continue
			}
			_, accountID, err := agentProfileIdentity(agent, p)
			if err != nil || accountID == "" {
				continue
			}
			result = append(result, localapi.ProfileIdentity{
				Agent: agent, ProfileName: name, BillingProvider: provider, AccountID: accountID,
			})
		}
	}
	return result
}
