package profile

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// OnboardingHomes is a read-only census including legacy registration-excluded
// homes and registered homes even when collection/authentication is disabled.
func (r Registry) OnboardingHomes(userHome string) ([]HomeMembership, error) {
	base, err := NormalizeCodexHome(userHome)
	if err != nil {
		return nil, err
	}
	found, err := r.DiscoverHomes(base)
	if err != nil {
		return nil, err
	}
	if !found.Complete {
		return nil, ErrUnsafe
	}
	entries, err := r.List()
	if err != nil {
		return nil, err
	}
	result := []HomeMembership{}
	seen := map[string]bool{}
	add := func(h HomeMembership) error {
		home, e := NormalizeCodexHome(h.Home)
		if e != nil {
			return e
		}
		h.Home = home
		key := h.Provider + "\x00" + home
		if seen[key] {
			return nil
		}
		seen[key] = true
		result = append(result, h)
		return nil
	}
	for _, entry := range entries {
		p := entry.Profile
		if p.HomesVersion != 0 || p.Homes != nil {
			if p.ValidateHomes() != nil {
				return nil, ErrUnsafe
			}
			for _, h := range p.Homes {
				if err := add(h); err != nil {
					return nil, err
				}
			}
			continue
		}
		for _, provider := range []string{"claude", "codex"} {
			home := p.ClaudeConfigDir
			if provider == "codex" {
				home = p.CodexHome
			}
			if home == "" {
				if entry.Name != "default" {
					continue
				}
				home = filepath.Join(base, "."+provider)
				if _, e := os.Stat(home); errors.Is(e, os.ErrNotExist) {
					continue
				}
			}
			if err := add(HomeMembership{Provider: provider, Home: home, Transmission: "undecided"}); err != nil {
				return nil, err
			}
		}
	}
	for _, c := range found.Candidates {
		if err := add(HomeMembership{Provider: c.Provider, Home: c.Home, Transmission: "undecided"}); err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider != result[j].Provider {
			return result[i].Provider < result[j].Provider
		}
		return result[i].Home < result[j].Home
	})
	return result, nil
}

// PlanOnboarding requires one explicit decision for every selected discovered home.
// Unselected candidates and existing explicit memberships remain untouched.
// Initial assignment is allow->default/exclude->personal; subsequent edits use
// UpdateHomes/ApplyHomeUpdates and never infer membership from transmission.
func (r Registry) PlanOnboarding(userHome string, decisions []HomeMembership) ([]HomeUpdate, error) {
	observed, err := r.OnboardingHomes(userHome)
	if err != nil {
		return nil, err
	}
	if len(observed) == 0 || len(decisions) > len(observed) || len(decisions) == 0 {
		return nil, ErrUnsafe
	}
	check := Profile{HomesVersion: 1, Homes: decisions}
	if check.ValidateHomes() != nil {
		return nil, ErrUnsafe
	}
	byKey := map[string]HomeMembership{}
	for _, h := range decisions {
		if h.Transmission == "undecided" {
			return nil, ErrUnsafe
		}
		byKey[h.Provider+"\x00"+h.Home] = h
	}
	census := map[string]bool{}
	for _, h := range observed {
		census[h.Provider+"\x00"+h.Home] = true
	}
	for key := range byKey {
		if !census[key] {
			return nil, ErrConflict
		}
	}
	entries, err := r.List()
	if err != nil {
		return nil, err
	}
	updates := []HomeUpdate{}
	indices := map[string]int{}
	assigned := map[string]bool{}
	for _, entry := range entries {
		indices[entry.Name] = len(updates)
		homes := []HomeMembership{}
		if entry.Profile.HomesVersion != 0 {
			for _, old := range entry.Profile.Homes {
				key := old.Provider + "\x00" + old.Home
				h, ok := byKey[key]
				if !ok {
					h = old
				}
				homes = append(homes, h)
				assigned[key] = true
			}
		}
		if entry.Profile.HomesVersion == 0 && (entry.Name == "default" || entry.Name == "personal") {
			for _, provider := range []string{"claude", "codex"} {
				home := entry.Profile.ClaudeConfigDir
				if provider == "codex" {
					home = entry.Profile.CodexHome
				}
				if home == "" && entry.Name == "default" {
					home = filepath.Join(userHome, "."+provider)
					if _, e := os.Stat(home); errors.Is(e, os.ErrNotExist) {
						continue
					}
				}
				if home == "" {
					continue
				}
				canonical, e := NormalizeCodexHome(home)
				if e != nil {
					return nil, e
				}
				if _, ok := byKey[provider+"\x00"+canonical]; !ok {
					return nil, ErrUnsafe
				}
			}
		}
		u := HomeUpdate{Name: entry.Name, Revision: entry.Revision, Homes: homes}
		if entry.Profile.HomesVersion == 0 {
			home := entry.Profile.ClaudeConfigDir
			if home == "" && entry.Name == "default" {
				home = filepath.Join(userHome, ".claude")
			}
			if canonical, e := NormalizeCodexHome(home); home != "" && e == nil {
				u.LegacyClaudeHome = canonical
			}
		}
		if entry.Profile.HomesVersion == 0 && entry.Name != "default" && entry.Name != "personal" {
			memberCount, selected := 0, 0
			for _, provider := range []string{"claude", "codex"} {
				home := entry.Profile.ClaudeConfigDir
				if provider == "codex" {
					home = entry.Profile.CodexHome
				}
				if home == "" {
					continue
				}
				memberCount++
				canonical, e := NormalizeCodexHome(home)
				if e != nil {
					return nil, e
				}
				if _, ok := byKey[provider+"\x00"+canonical]; ok {
					selected++
				}
			}
			if selected > 0 && selected != memberCount {
				return nil, ErrUnsafe
			}
			if selected == 0 {
				continue
			}
			u.Retire = true
		}
		updates = append(updates, u)
	}
	for _, name := range []string{"default", "personal"} {
		if _, ok := indices[name]; !ok {
			indices[name] = len(updates)
			updates = append(updates, HomeUpdate{Name: name, Homes: []HomeMembership{}})
		}
	}
	for _, h := range decisions {
		if assigned[h.Provider+"\x00"+h.Home] {
			continue
		}
		target := "personal"
		if h.Transmission == "allow" {
			target = "default"
		}
		i := indices[target]
		updates[i].Homes = append(updates[i].Homes, h)
	}
	for i := range updates {
		for _, h := range updates[i].Homes {
			if _, selected := byKey[h.Provider+"\x00"+h.Home]; selected {
				updates[i].DiscoveryHomes = append(updates[i].DiscoveryHomes, h)
			}
		}
	}
	return updates, nil
}
