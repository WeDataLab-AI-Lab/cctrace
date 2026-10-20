package main

import (
	"fmt"

	"cctrace/internal/codexconfig"
	"cctrace/internal/envgen"
	"cctrace/internal/profile"
)

// Called only after the complete onboarding/server summary was confirmed and
// persisted. Execution consent and transmission permission must both allow setup.
func applyConfirmedHomeIntegrations(p *profile.Profile) error {
	if p.HomesVersion != 1 {
		return fmt.Errorf("provider integration requires completed home onboarding")
	}
	return p.WithDefinitionFence(func() error {
		if p.CollectionEnabled("claude") {
			if err := envgen.ApplyToClaudeSettings(p); err != nil {
				return err
			}
		}
		return applyConfirmedCodexHomes(p)
	})
}
func applyConfirmedCodexHomes(p *profile.Profile) error {
	if !p.CollectionEnabled("codex") {
		return nil
	}
	for _, h := range p.ProviderHomes("codex", "") {
		if h.Transmission != "allow" {
			continue
		}
		if err := profile.CheckCodexHome(h.Home); err != nil {
			return err
		}
		if err := codexconfig.WriteOtelBlock(h.Home, p.Server.Endpoint, p.Server.AuthToken, p.Server.CACertFile); err != nil {
			return err
		}
	}
	return nil
}
