package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cctrace/internal/desktop"
	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

func promptHomeDecisions(ir *inputReader, w io.Writer, homes []profile.HomeMembership) ([]profile.HomeMembership, error) {
	if len(homes) == 0 {
		return nil, errors.New("no provider homes found; create or select provider homes before onboarding")
	}
	decisions := append([]profile.HomeMembership{}, homes...)
	for i, h := range decisions {
		fmt.Fprintf(w, "%s home: %s\n", h.Provider, h.Home)
		choice := strings.ToLower(ir.Prompt("  Session transmission (allow/exclude; required)", ""))
		if choice != "allow" && choice != "exclude" {
			return nil, errors.New("every home requires an explicit transmission decision; nothing saved")
		}
		quota := strings.ToLower(ir.Prompt("  Enable usage lookup independently? (yes/no; required)", ""))
		if quota != "yes" && quota != "no" {
			return nil, errors.New("every home requires an explicit usage decision; nothing saved")
		}
		decisions[i].Transmission = choice
		decisions[i].QuotaEnabled = quota == "yes"
	}
	fmt.Fprintln(w, "Home decision summary (initial allow -> default; exclude -> personal; established membership stays in place):")
	for _, h := range decisions {
		fmt.Fprintf(w, "  %s %s: transmission=%s usage=%t\n", h.Provider, h.Home, h.Transmission, h.QuotaEnabled)
	}
	if strings.ToLower(ir.Prompt("  Confirm this complete home summary? (yes/no)", "")) != "yes" {
		return nil, errors.New("home decisions cancelled; nothing saved")
	}
	return decisions, nil
}

type cliHomeSetup struct {
	service      *desktop.Service
	discoveryRef string
	decisions    []profile.HomeMembership
}

func homeSetupService() (*desktop.Service, error) {
	support := profile.LegacyDesktopRoot()
	if support == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		support = filepath.Join(base, "cctrace-desktop")
	}
	config := desktop.Config{AppSupportRoot: support, ProfileRoot: profile.DefaultDir(), ProviderMode: true}
	base, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if home, e := profile.NormalizeCodexHome(filepath.Join(base, ".claude")); e == nil {
		config.ClaudeHome = home
	}
	if home, e := profile.NormalizeCodexHome(filepath.Join(base, ".codex")); e == nil {
		config.CodexHome = home
	}
	return desktop.New(config)
}
func homeSetupCall(ctx context.Context, s *desktop.Service, op string, payload any, target any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	response := s.Handle(ctx, desktop.Request{Version: desktop.ProtocolVersion, Kind: "request", ID: "cli-home-consent", Op: op, Payload: raw})
	if response.Error != nil {
		return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
	}
	if target == nil {
		return nil
	}
	data, err := json.Marshal(response.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func beginCLIHomeSetup(ctx context.Context, ir *inputReader, w io.Writer) (*cliHomeSetup, error) {
	service, err := homeSetupService()
	if err != nil {
		return nil, err
	}
	if err = homeSetupCall(ctx, service, "hello", map[string]bool{"multi_home_profiles": true}, nil); err != nil {
		return nil, err
	}
	var found struct {
		DiscoveryRef string                   `json:"discovery_ref"`
		Homes        []profile.HomeMembership `json:"homes"`
	}
	if err = homeSetupCall(ctx, service, "profiles.onboarding.discover", map[string]any{}, &found); err != nil {
		return nil, err
	}
	for i, h := range found.Homes {
		fmt.Fprintf(w, "  %d. %s %s\n", i+1, h.Provider, h.Home)
	}
	choice := strings.ToLower(strings.TrimSpace(ir.Prompt("  Select homes to enroll/review (all or comma-separated numbers; others remain untouched)", "")))
	selected := []profile.HomeMembership{}
	if choice == "all" {
		selected = found.Homes
	} else {
		seen := map[int]bool{}
		for _, part := range strings.Split(choice, ",") {
			i, e := strconv.Atoi(strings.TrimSpace(part))
			if e != nil || i < 1 || i > len(found.Homes) || seen[i] {
				return nil, errors.New("explicit discovered home selection is required")
			}
			seen[i] = true
			selected = append(selected, found.Homes[i-1])
		}
	}
	decisions, err := promptHomeDecisions(ir, w, selected)
	if err != nil {
		return nil, err
	}
	return &cliHomeSetup{service: service, discoveryRef: found.DiscoveryRef, decisions: decisions}, nil
}
func (setup *cliHomeSetup) commit(ctx context.Context, ir *inputReader, p *profile.Profile) error {
	payload := map[string]any{"discovery_ref": setup.discoveryRef, "decisions": setup.decisions}

	if p != nil {
		payload["server"] = p.Server
		payload["user"] = p.User
		payload["collection"] = profile.HomeCollection{Claude: p.CollectionEnabled("claude"), Codex: p.CollectionEnabled("codex")}
	}
	var preview struct {
		PreviewRef string               `json:"preview_ref"`
		Effect     string               `json:"effect"`
		Profiles   []profile.HomeUpdate `json:"profiles"`
	}
	if err := homeSetupCall(ctx, setup.service, "profiles.onboarding.preview", payload, &preview); err != nil {
		return err
	}
	fmt.Println(preview.Effect)
	for _, entry := range preview.Profiles {
		fmt.Printf("  Profile %s: %d home(s)\n", entry.Name, len(entry.Homes))
	}
	if strings.ToLower(ir.Prompt("  Commit this summary and server configuration? (yes/no)", "")) != "yes" {
		return errors.New("onboarding cancelled; no new profile or provider settings saved")
	}
	return homeSetupCall(ctx, setup.service, "profiles.onboarding.apply", map[string]any{"preview_ref": preview.PreviewRef, "confirm": true}, nil)
}

func profileOnboardingCmd() *cobra.Command {
	var decisionsFile string
	var confirm bool
	var recoverPending bool
	cmd := &cobra.Command{Use: "onboard", Short: "Discover every home and confirm shared membership, transmission and independent usage choices", RunE: func(cmd *cobra.Command, _ []string) error {
		ir := newInputReader()
		if recoverPending {
			if decisionsFile != "" {
				return errors.New("--recover cannot be combined with --decisions")
			}
			if !confirm {
				return errors.New("recovery requires --confirm")
			}
			service, err := homeSetupService()
			if err != nil {
				return err
			}
			return homeSetupCall(cmd.Context(), service, "profiles.onboarding.recover", map[string]any{"confirm": true}, nil)
		}
		if decisionsFile == "" {
			setup, err := beginCLIHomeSetup(cmd.Context(), ir, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return setup.commit(cmd.Context(), ir, nil)
		}
		data, err := os.ReadFile(decisionsFile)
		if err != nil {
			return err
		}
		decisions, decodeErr := profile.DecodeHomeDecisions(data)
		if decodeErr != nil {
			return errors.New("decisions file must be a JSON array of explicit home decisions")
		}
		service, err := homeSetupService()
		if err != nil {
			return err
		}
		var discovered struct {
			DiscoveryRef string `json:"discovery_ref"`
		}
		if err = homeSetupCall(cmd.Context(), service, "profiles.onboarding.discover", map[string]any{}, &discovered); err != nil {
			return err
		}
		var preview map[string]any
		if err = homeSetupCall(cmd.Context(), service, "profiles.onboarding.preview", map[string]any{"discovery_ref": discovered.DiscoveryRef, "decisions": decisions}, &preview); err != nil {
			return err
		}
		encoded, _ := json.MarshalIndent(preview, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		if !confirm {
			return nil
		}
		return homeSetupCall(cmd.Context(), service, "profiles.onboarding.apply", map[string]any{"preview_ref": preview["preview_ref"], "confirm": true}, nil)
	}}
	cmd.Flags().StringVar(&decisionsFile, "decisions", "", "JSON decisions for explicitly selected discovered homes; without --confirm prints a read-only preview")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Confirm the complete explicit decision file")
	cmd.Flags().BoolVar(&recoverPending, "recover", false, "Restore an interrupted migration from hash-fenced private originals")
	return cmd
}
