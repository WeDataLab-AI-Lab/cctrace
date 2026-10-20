package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cctrace/internal/profile"
)

// detectAndOfferAdditionalHomes uses the same read-only candidates and shared
// registration as the desktop. Collection is configured separately.
func detectAndOfferAdditionalHomes(ir *inputReader, _ *profile.Profile) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	registry := profile.Registry{Root: profile.DefaultDir()}
	found, err := registry.DiscoverHomes(home)
	if err != nil {
		fmt.Println("  [!] Provider homes could not be discovered")
		return
	}
	if !found.Complete {
		fmt.Println("  [!] Some profile definitions could not be read; showing available candidates")
	}
	if len(found.Candidates) == 0 {
		return
	}
	fmt.Printf("  Detected %d unregistered Claude/Codex home(s):\n", len(found.Candidates))
	for i, candidate := range found.Candidates {
		state := ""
		if candidate.Excluded {
			state = " [excluded]"
		}
		fmt.Printf("    %d. %s: %s%s\n", i+1, candidate.Provider, candidate.Home, state)
	}
	answer := strings.ToLower(strings.TrimSpace(ir.Prompt("  Register which profiles? Collection stays off (all/none/1,2,...; exclude 1,2; restore 1,2)", "none")))
	action := "register"
	for _, verb := range []string{"exclude", "restore"} {
		if strings.HasPrefix(answer, verb+" ") {
			action = verb
			answer = strings.TrimSpace(strings.TrimPrefix(answer, verb+" "))
			break
		}
	}
	selected := make(map[int]bool)
	if answer == "all" {
		for i := range found.Candidates {
			selected[i] = action != "register" || !found.Candidates[i].Excluded
		}
	} else {
		for _, part := range strings.Split(answer, ",") {
			index, err := strconv.Atoi(strings.TrimSpace(part))
			if err == nil && index > 0 && index <= len(found.Candidates) {
				selected[index-1] = true
			}
		}
	}
	for i, candidate := range found.Candidates {
		if !selected[i] {
			continue
		}
		if action != "register" {
			if err := registry.SetHomeExcluded(candidate, action == "exclude"); err != nil {
				fmt.Printf("  [!] Home choice for %q could not be saved; discover homes again\n", candidate.Name)
			} else if action == "exclude" {
				fmt.Printf("  [OK] Home %q excluded from registration\n", candidate.Name)
			} else {
				fmt.Printf("  [OK] Home %q restored; run init again to explicitly register it\n", candidate.Name)
			}
			continue
		}
		if candidate.Excluded {
			fmt.Printf("  [!] Home %q is excluded; use restore %d before selecting registration\n", candidate.Name, i+1)
			continue
		}
		if _, err := registry.RegisterHomeCandidate(candidate, false); err != nil {
			fmt.Printf("  [!] Profile %q could not be registered; discover homes again\n", candidate.Name)
			continue
		}
		fmt.Printf("  [OK] Profile %q registered; collection and usage lookup stay off\n", candidate.Name)
	}
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
