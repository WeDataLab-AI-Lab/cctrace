package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"cctrace/internal/codexsyncer"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

func runSyncReenrich(claudeDir string, profileName string, profileEmail string, endpointOverride string, local bool) error {
	if profileName == "" {
		profileName = os.Getenv("CCTRACE_PROFILE")
	}

	p, err := loadSyncProfile(profileName)
	if err != nil {
		return err
	}
	if !p.Options.SyncEnabled {
		fmt.Println("  Session log sync is disabled in profile. Run 'cctrace init' to enable.")
		return nil
	}

	runtimeEndpoint := p.Server.Endpoint
	if local {
		runtimeEndpoint = applyLocalDevEndpoints(p, &endpointOverride)
	}
	if runtimeEndpoint == "" {
		return fmt.Errorf("no server endpoint configured; run 'cctrace init' to set one")
	}
	autoMigrateCodex(p, profileName, runtimeEndpoint)

	fl, err := acquireSyncLock(profileName, syncLockWaitDuration)
	if err != nil {
		if errors.Is(err, errSyncAlreadyRunning) {
			return errSyncAlreadyRunning
		}
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer fl.Unlock()

	state, err := syncer.LoadState(syncStatePathForProfile(profileName))
	if err != nil {
		return fmt.Errorf("load sync state: %w", err)
	}
	ep := profileHTTPAPIEndpoint(p)
	if endpointOverride != "" {
		ep = endpointOverride
	}
	client := newSyncClient(p, ep, version, profileName)
	ctx := context.Background()

	resolvedClaudeDir := resolveClaudeDir(p, claudeDir)
	resolvedEmail := resolveProfileEmail(p, profileEmail)
	resolvedUserID := resolveUserID(p)
	total, err := syncer.New(resolvedClaudeDir, resolvedEmail, resolvedUserID, state, client, p.Options.CollectRepositoryPrefixes).ReenrichOnce(ctx)
	if err != nil {
		return fmt.Errorf("reenrich: %w", err)
	}

	if os.Getenv("CCTRACE_CODEX_SYNC") == "true" || p.Options.CodexSyncEnabled {
		codexState, cerr := syncer.LoadState(codexsyncer.StatePathForProfile(profileName))
		if cerr != nil {
			return fmt.Errorf("codex reenrich load sync state: %w", cerr)
		}
		cs := codexsyncer.New(resolveCodexScanDirs(os.Stderr, p), resolvedEmail, resolvedUserID, codexState, client, p.Options.CollectRepositoryPrefixes)
		cn, cerr := cs.ReenrichOnce(ctx)
		if cerr != nil {
			return fmt.Errorf("codex reenrich: %w", cerr)
		}
		total += cn
	}

	fmt.Printf("  Re-enriched %d records\n", total)
	return nil
}

func syncStatePathForProfile(profileName string) string {
	if profileName != "" {
		if dir, err := profile.NamedDir(profileName); err == nil {
			return filepath.Join(dir, "sync-state.json")
		}
	}
	return syncer.DefaultStatePath()
}
