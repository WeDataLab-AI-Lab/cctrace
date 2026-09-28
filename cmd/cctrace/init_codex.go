package main

import (
	"fmt"
	"os"
	"time"

	"cctrace/internal/codexconfig"
	"cctrace/internal/codexlog"
	"cctrace/internal/profile"
)

// offerCodexSync detects Codex CLI during init and offers to configure its OTEL
// integration.
func offerCodexSync(ir *inputReader, p *profile.Profile, profileName string) {
	if _, err := os.Stat(codexlog.DefaultCodexDir()); err != nil {
		return
	}
	fmt.Println()
	fmt.Println("  Codex CLI detected (~/.codex found).")
	answer := ir.Prompt("  Enable Codex session sync? [Y/n]", "Y")
	enabled := answer == "" || answer == "Y" || answer == "y"
	setCodexSync(p, enabled)
	if err := saveProfile(p, profileName); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: save profile: %v\n", err)
	}
	if !enabled {
		return
	}
	if err := codexconfig.WriteOtelBlock(codexlog.DefaultCodexDir(), p.Server.Endpoint, p.Server.AuthToken); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: Codex OTEL config: %v\n", err)
		return
	}
	fmt.Println("  [OK] Codex OTEL configured (~/.codex/config.toml)")
	// Explicit init replaces a stale token in extra homes; sync only names it.
	healExtraCodexHomes(p, p.Server.Endpoint, false)
	warnCodexHTTPSEndpoint(p.Server.Endpoint)
}

// runCodexPatch updates only the Codex-related parts of an existing profile.
// It does NOT prompt for password or re-authenticate; reuses the stored AuthToken.
func runCodexPatch(p *profile.Profile, isNamed bool, profileName string) error {
	codexDir := codexlog.DefaultCodexDir()
	if _, err := os.Stat(codexDir); err != nil {
		fmt.Println()
		fmt.Println("  Codex CLI not detected (~/.codex not found).")
		fmt.Println("  Install Codex first, then re-run 'cctrace init'.")
		return nil
	}

	if p.Server.Endpoint == "" {
		return fmt.Errorf("existing profile has no OTEL endpoint; run 'cctrace init' with full re-setup")
	}

	fmt.Println()
	fmt.Println("  Patching Codex integration...")

	// Update profile flags only.
	setCodexSync(p, true)
	p.UpdatedAt = time.Now().UTC()

	if err := codexconfig.WriteOtelBlock(codexDir, p.Server.Endpoint, p.Server.AuthToken); err != nil {
		return fmt.Errorf("write codex config.toml: %w", err)
	}
	// Explicit init replaces a stale token in extra homes; sync only names it.
	healExtraCodexHomes(p, p.Server.Endpoint, false)

	if isNamed {
		if err := profile.SaveNamed(p, profileName); err != nil {
			return fmt.Errorf("save profile: %w", err)
		}
	} else {
		if err := profile.Save(p); err != nil {
			return fmt.Errorf("save profile: %w", err)
		}
	}

	fmt.Printf("  [OK] Codex sync enabled in profile.\n")
	fmt.Printf("  [OK] Codex OTEL configured (%s/config.toml)\n", codexDir)
	warnCodexHTTPSEndpoint(p.Server.Endpoint)
	fmt.Println()
	fmt.Println("  Run 'cctrace sync' to start collecting Codex sessions.")
	return nil
}

// backupProfile copies the existing profile.json to a sibling .bak.<timestamp> file.
// Returns the backup path so the caller can restore on failure.
func backupProfile(isNamed bool, profileName string) (string, error) {
	src := profile.DefaultPath()
	if isNamed {
		np, err := profile.NamedPath(profileName)
		if err != nil {
			return "", err
		}
		src = np
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	backup := fmt.Sprintf("%s.bak.%d", src, time.Now().Unix())
	if err := os.WriteFile(backup, data, 0600); err != nil {
		return "", err
	}
	return backup, nil
}

// restoreFromBackup overwrites the profile path with the backup file.
func restoreFromBackup(backupPath string, isNamed bool, profileName string) error {
	dst := profile.DefaultPath()
	if isNamed {
		np, err := profile.NamedPath(profileName)
		if err != nil {
			return err
		}
		dst = np
	}
	data, err := os.ReadFile(backupPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0600); err != nil {
		return err
	}
	_ = os.Remove(backupPath)
	return nil
}
