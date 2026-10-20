package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"cctrace/internal/desktop"
	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

func homePolicyService(cmd *cobra.Command) (*desktop.Service, error) {
	service, err := homeSetupService()
	if err != nil {
		return nil, err
	}
	for _, capability := range []string{"multi_home_profiles", "quota_account_homes", "quota_accounts"} {
		if err = homeSetupCall(cmd.Context(), service, "hello", map[string]bool{capability: true}, nil); err != nil {
			return nil, err
		}
	}
	return service, nil
}
func printAndApplyHomePreview(cmd *cobra.Command, service *desktop.Service, kind string, payload any, confirm bool) error {
	var preview map[string]any
	if err := homeSetupCall(cmd.Context(), service, "profiles."+kind+".preview", payload, &preview); err != nil {
		return err
	}
	encoded, _ := json.MarshalIndent(preview, "", "  ")
	fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
	if !confirm {
		return nil
	}
	return homeSetupCall(cmd.Context(), service, "profiles."+kind+".apply", map[string]any{"preview_ref": preview["preview_ref"], "confirm": true}, nil)
}
func profileTransmissionCmd() *cobra.Command {
	var provider, home, excluded string
	var confirm bool
	cmd := &cobra.Command{Use: "transmission", Short: "Preview local home/account transmission effects; --confirm applies the exact current preview", RunE: func(cmd *cobra.Command, _ []string) error {
		if provider != "claude" && provider != "codex" {
			return errors.New("--provider must be claude or codex")
		}
		if !cmd.Flags().Changed("excluded") {
			return errors.New("--excluded requires explicit true or false")
		}
		value, err := strconv.ParseBool(excluded)
		if err != nil {
			return err
		}
		canonical, err := profile.NormalizeCodexHome(home)
		if err != nil || canonical == "" {
			return errors.New("--home requires an existing provider directory")
		}
		service, err := homePolicyService(cmd)
		if err != nil {
			return err
		}
		var census struct {
			Complete bool `json:"complete"`
			Accounts []struct {
				Provider   string   `json:"provider"`
				Ref        string   `json:"account_ref"`
				Generation uint64   `json:"generation"`
				Home       string   `json:"config_home"`
				Homes      []string `json:"config_homes"`
			} `json:"accounts"`
		}
		if err = homeSetupCall(cmd.Context(), service, "quota.accounts.list", map[string]any{}, &census); err != nil {
			return err
		}
		if !census.Complete {
			return errors.New("account census incomplete; refresh profiles before confirming transmission")
		}
		payload := map[string]any{"provider": provider, "home": canonical, "excluded": value}
		for _, a := range census.Accounts {
			matches := a.Home == canonical
			for _, h := range a.Homes {
				matches = matches || h == canonical
			}
			if a.Provider == provider && matches {
				payload["account_ref"] = a.Ref
				payload["generation"] = a.Generation
				break
			}
		}
		return printAndApplyHomePreview(cmd, service, "transmission", payload, confirm)
	}}
	cmd.Flags().StringVar(&provider, "provider", "", "claude or codex")
	cmd.Flags().StringVar(&home, "home", "", "Existing enrolled provider home")
	cmd.Flags().StringVar(&excluded, "excluded", "", "Explicit true or false; independent of collection and usage")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Confirm all current home/profile/provider-file effects shown in the preview")
	return cmd
}
func profileMembershipCmd() *cobra.Command {
	var provider, home, source, target string
	var confirm bool
	cmd := &cobra.Command{Use: "move-home", Short: "Preview an enrolled home's profile move, including destination collection eligibility", RunE: func(cmd *cobra.Command, _ []string) error {
		canonical, err := profile.NormalizeCodexHome(home)
		if err != nil || canonical == "" {
			return errors.New("--home requires an existing enrolled directory")
		}
		r := profile.Registry{Root: profile.DefaultDir()}
		from, err := r.Load(source)
		if err != nil {
			return err
		}
		to, err := r.Load(target)
		if err != nil {
			return err
		}
		service, err := homePolicyService(cmd)
		if err != nil {
			return err
		}
		return printAndApplyHomePreview(cmd, service, "membership", map[string]any{"provider": provider, "home": canonical, "source": source, "source_revision": from.Revision, "target": target, "target_revision": to.Revision}, confirm)
	}}
	cmd.Flags().StringVar(&provider, "provider", "", "claude or codex")
	cmd.Flags().StringVar(&home, "home", "", "Existing enrolled provider home")
	cmd.Flags().StringVar(&source, "source", "", "Current profile name")
	cmd.Flags().StringVar(&target, "target", "", "Destination profile name")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "Confirm the current source/target revisions and actual destination eligibility")
	return cmd
}
func profileAccountsCmd() *cobra.Command {
	return &cobra.Command{Use: "accounts", Short: "Show authoritative current usage opt-ins and validated connected homes as JSON", RunE: func(cmd *cobra.Command, _ []string) error {
		service, err := homePolicyService(cmd)
		if err != nil {
			return err
		}
		var result map[string]any
		if err = homeSetupCall(cmd.Context(), service, "quota.accounts.list", map[string]any{}, &result); err != nil {
			return err
		}
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return nil
	}}
}
