package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"cctrace/internal/codexlog"
	"cctrace/internal/profile"
)

// resolveCodexScanDirs returns every Codex home directory to scan for a
// profile: the default home, CODEX_HOME, and the profile's options.codex_dirs,
// unioned in that order.
//
// Extra homes configured in the profile that cannot be scanned (missing,
// unreadable, or not a directory) are reported to w so collection never stops
// silently — the exact failure mode this wiring exists to prevent.
func resolveCodexScanDirs(w io.Writer, p *profile.Profile) []string {
	dirs := codexlog.ResolveScanDirs(p.Options.CodexDirs)
	scanned := make(map[string]struct{}, len(dirs))
	for _, d := range dirs {
		scanned[d] = struct{}{}
	}
	for _, raw := range p.Options.CodexDirs {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if abs, err := filepath.Abs(codexlog.ExpandHome(trimmed)); err == nil {
			if _, ok := scanned[abs]; ok {
				continue
			}
		}
		fmt.Fprintf(w, "  [codex-sync] configured codex dir is not an existing directory, skipped: %s\n", trimmed)
	}
	return dirs
}
