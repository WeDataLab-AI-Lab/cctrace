package codexsyncer

import (
	"os"
	"path/filepath"

	"cctrace/internal/profile"
)

// DefaultCodexStatePath returns the global Codex syncer state path used by the
// default (unnamed) profile.
func DefaultCodexStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cctrace", "codex-sync-state.json")
}

// StatePathForProfile returns the Codex syncer state path for the given profile.
// Named profiles get their own per-profile state file (mirroring the Claude
// syncer) so that concurrent syncs from different profiles do not share read
// offsets — sharing one global file lets whichever profile runs first consume
// records and advance the offset, starving the others. An empty name (default
// profile) or an invalid name falls back to the global path.
func StatePathForProfile(profileName string) string {
	if profileName == "" {
		return DefaultCodexStatePath()
	}
	dir, err := profile.NamedDir(profileName)
	if err != nil {
		return DefaultCodexStatePath()
	}
	return filepath.Join(dir, "codex-sync-state.json")
}
