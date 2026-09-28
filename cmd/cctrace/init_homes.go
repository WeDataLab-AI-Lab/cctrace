package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cctrace/internal/envgen"
	"cctrace/internal/profile"
)

// detectAndOfferAdditionalHomes scans $HOME for .claude-* directories
// that don't have a named profile yet, and offers to create one.
func detectAndOfferAdditionalHomes(ir *inputReader, baseProfile *profile.Profile) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	// Find .claude-* directories (excluding .claude itself and .cctrace)
	entries, err := os.ReadDir(home)
	if err != nil {
		return
	}

	// Collect existing profile ClaudeConfigDirs
	configuredDirs := map[string]bool{
		filepath.Join(home, ".claude"): true, // default profile
	}
	if names, err := profile.ListNamed(); err == nil {
		for _, name := range names {
			if np, err := profile.LoadNamed(name); err == nil {
				dir := np.ClaudeConfigDir
				if dir == "" {
					dir = filepath.Join(home, ".claude")
				}
				configuredDirs[dir] = true
			}
		}
	}

	// Find unconfigured .claude-* dirs
	var unconfigured []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, ".claude-") {
			continue
		}
		if name == ".claude-mem" {
			continue
		}
		// Must contain projects/ or settings.json to be a real Claude home
		dir := filepath.Join(home, name)
		hasProjects := false
		hasSettings := false
		if _, err := os.Stat(filepath.Join(dir, "projects")); err == nil {
			hasProjects = true
		}
		if _, err := os.Stat(filepath.Join(dir, "settings.json")); err == nil {
			hasSettings = true
		}
		if !hasProjects && !hasSettings {
			continue
		}
		if configuredDirs[dir] {
			continue
		}
		unconfigured = append(unconfigured, dir)
	}

	if len(unconfigured) == 0 {
		return
	}

	fmt.Printf("  Detected %d additional Claude home(s):\n", len(unconfigured))
	for i, dir := range unconfigured {
		fmt.Printf("    %d. %s\n", i+1, dir)
	}

	fmt.Println()
	answer := ir.Prompt("  Set up sync for which? (all/none/1,2,...)", "all")
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer == "none" || answer == "n" || answer == "" {
		return
	}

	selected := make(map[int]bool)
	if answer == "all" || answer == "y" {
		for i := range unconfigured {
			selected[i] = true
		}
	} else {
		for _, s := range strings.Split(answer, ",") {
			s = strings.TrimSpace(s)
			idx, err := strconv.Atoi(s)
			if err != nil || idx < 1 || idx > len(unconfigured) {
				fmt.Printf("  [!] Invalid selection: %s (skipped)\n", s)
				continue
			}
			selected[idx-1] = true
		}
	}

	if len(selected) == 0 {
		return
	}

	for i, dir := range unconfigured {
		if !selected[i] {
			continue
		}
		// Derive profile name from dir: .claude-2 → claude-2
		dirName := filepath.Base(dir)
		profileName := strings.TrimPrefix(dirName, ".")

		np := profile.NewDefault()
		np.User = baseProfile.User
		np.Server = baseProfile.Server
		np.Options = baseProfile.Options
		np.ClaudeConfigDir = dir

		if err := profile.EnsureNamedDir(profileName); err != nil {
			fmt.Printf("  [!] Failed to create profile %s: %v\n", profileName, err)
			continue
		}
		if err := profile.SaveNamed(np, profileName); err != nil {
			fmt.Printf("  [!] Failed to save profile %s: %v\n", profileName, err)
			continue
		}

		// Apply OTEL + hooks to this Claude home's settings.json
		if err := envgen.ApplyToClaudeSettings(np); err != nil {
			fmt.Printf("  [!] Failed to apply settings for %s: %v\n", profileName, err)
			continue
		}

		settingsPath, _ := envgen.ClaudeSettingsPath(np)
		fmt.Printf("  [OK] Profile %q → %s\n", profileName, settingsPath)
	}
	fmt.Println()
}

// checkClaudeDirConflict checks if another named profile uses the same ClaudeConfigDir.
// Returns the conflicting profile name, or "" if no conflict.
func checkClaudeDirConflict(currentProfile, claudeDir string) string {
	// Resolve effective dir for comparison
	effectiveDir := claudeDir
	if effectiveDir == "" {
		home, _ := os.UserHomeDir()
		effectiveDir = filepath.Join(home, ".claude")
	}

	// Check default profile
	if existing, err := profile.Load(); err == nil {
		otherDir := existing.ClaudeConfigDir
		if otherDir == "" {
			home, _ := os.UserHomeDir()
			otherDir = filepath.Join(home, ".claude")
		}
		if otherDir == effectiveDir {
			return "(default)"
		}
	}

	// Check other named profiles
	names, err := profile.ListNamed()
	if err != nil {
		return ""
	}
	for _, name := range names {
		if name == currentProfile {
			continue
		}
		if other, err := profile.LoadNamed(name); err == nil {
			otherDir := other.ClaudeConfigDir
			if otherDir == "" {
				home, _ := os.UserHomeDir()
				otherDir = filepath.Join(home, ".claude")
			}
			if otherDir == effectiveDir {
				return name
			}
		}
	}
	return ""
}

// expandTilde replaces a leading "~/" with the user's home directory.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
