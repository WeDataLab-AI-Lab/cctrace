package profile

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// HomeMembership separates profile ownership, transmission consent and quota
// consent. Collection execution remains an independent profile option.
type HomeMembership struct {
	Provider     string `json:"provider"`
	Home         string `json:"home"`
	Transmission string `json:"transmission"`
	QuotaEnabled bool   `json:"quota_enabled"`
}

func (p *Profile) ValidateHomes() error {
	if p.HomesVersion == 0 && p.Homes == nil {
		return nil
	}
	if p.HomesVersion != 1 || p.Homes == nil || len(p.Homes) > 256 {
		return ErrUnsafe
	}
	seen := map[string]bool{}
	for _, h := range p.Homes {
		if h.Provider != "claude" && h.Provider != "codex" {
			return ErrUnsafe
		}
		if !filepath.IsAbs(h.Home) || filepath.Clean(h.Home) != h.Home || len(h.Home) > 4096 || strings.ContainsFunc(h.Home, func(r rune) bool { return r < 32 || r == 127 }) {
			return ErrUnsafe
		}
		if h.Transmission != "allow" && h.Transmission != "exclude" && h.Transmission != "undecided" {
			return ErrUnsafe
		}
		key := h.Provider + "\x00" + h.Home
		if seen[key] {
			return ErrUnsafe
		}
		seen[key] = true
	}
	return nil
}

// ProviderHomes never consults process environment once membership is explicit.
// The caller supplies the legacy execution fallback (not additional scan roots).
func (p *Profile) ProviderHomes(provider, fallback string) []HomeMembership {
	if p.HomesVersion != 0 || p.Homes != nil {
		if p.ValidateHomes() != nil {
			return nil
		}
		result := []HomeMembership{}
		for _, h := range p.Homes {
			if h.Provider == provider {
				result = append(result, h)
			}
		}
		return result
	}
	home := p.ClaudeConfigDir
	if provider == "codex" {
		home = p.CodexHome
	}
	if home == "" {
		home = fallback
	}
	if home == "" {
		return nil
	}
	return []HomeMembership{{Provider: provider, Home: home, Transmission: "allow"}}
}

func (p *Profile) TransmissionHomes(provider string) []string {
	result := []string{}
	for _, h := range p.ProviderHomes(provider, "") {
		if h.Transmission == "allow" && CheckCodexHome(h.Home) == nil {
			result = append(result, h.Home)
		}
	}
	return result
}

// UpdateHomes is the capability-aware CAS writer. Legacy mapping edits are
// refused for explicit memberships rather than silently discarding consent.
func (r Registry) UpdateHomes(name, expected string, homes []HomeMembership) (entry Entry, err error) {
	err = withProfileLock(r.Root, func() error {
		old, e := r.Load(name)
		if e != nil {
			return e
		}
		if expected == "" || expected != old.Revision {
			return ErrConflict
		}
		p := old.Profile
		p.HomesVersion = 1
		p.Homes = slices.Clone(homes)
		p.FenceLegacyCollector()
		if e = p.ValidateHomes(); e != nil {
			return e
		}
		for _, h := range p.Homes {
			if CheckCodexHome(h.Home) != nil || removalWithin(r.Root, h.Home) || removalWithin(h.Home, r.Root) {
				return fmt.Errorf("invalid membership home: %w", ErrUnsafe)
			}
		}
		path, _ := r.Path(name)
		if e = saveToUnlocked(p, path); e != nil {
			return e
		}
		entry, e = r.Load(name)
		return e
	})
	return
}

// HomeCollection is understood only by a capable collector. Migration clears
// legacy sync toggles so an old binary cannot collect explicit-home profiles.
type HomeCollection struct {
	Claude bool `json:"claude"`
	Codex  bool `json:"codex"`
}

func (p *Profile) CollectionEnabled(provider string) bool {
	if p.HomesVersion != 0 && p.HomeCollection != nil {
		if provider == "claude" {
			return p.HomeCollection.Claude
		}
		return p.HomeCollection.Codex
	}
	if provider == "claude" {
		return p.Options.SyncEnabled
	}
	return p.Options.CodexSyncEnabled
}
func (p *Profile) FenceLegacyCollector() {
	if p.HomesVersion == 0 {
		return
	}
	if p.HomeCollection == nil {
		p.HomeCollection = &HomeCollection{Claude: p.Options.SyncEnabled, Codex: p.Options.CodexSyncEnabled}
	}
	p.Options.SyncEnabled = false
	p.Options.CodexSyncEnabled = false
	p.Options.CodexSyncDeclined = true
}

func (p *Profile) SetCollectionEnabled(provider string, enabled bool) {
	if p.HomesVersion != 0 {
		p.FenceLegacyCollector()

		if provider == "claude" {
			p.HomeCollection.Claude = enabled
		} else {
			p.HomeCollection.Codex = enabled
		}
		return
	}
	if provider == "claude" {
		p.Options.SyncEnabled = enabled
	} else {
		p.Options.CodexSyncEnabled = enabled
		p.Options.CodexSyncDeclined = !enabled
	}
}

// MarshalJSON keeps explicit empty membership distinguishable from legacy
// absence without adding any fields to a legacy profile definition.
func (p Profile) MarshalJSON() ([]byte, error) {
	type plain Profile
	if p.HomesVersion == 0 && p.Homes == nil {
		return json.Marshal(plain(p))
	}
	homes := p.Homes
	if homes == nil {
		homes = []HomeMembership{}
	}
	return json.Marshal(struct {
		*plain
		Homes []HomeMembership `json:"homes"`
	}{plain: (*plain)(&p), Homes: homes})
}

// Nested policy formats are closed: an older capable writer must refuse fields
// it cannot preserve or interpret instead of dropping a future consent axis.
func (h *HomeMembership) UnmarshalJSON(data []byte) error {
	wrapped := append([]byte{'['}, data...)
	wrapped = append(wrapped, ']')
	homes, err := DecodeHomeDecisions(wrapped)
	if err != nil {
		return err
	}
	*h = homes[0]
	return nil
}
func (c *HomeCollection) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 2 {
		return ErrUnsafe
	}
	for _, key := range []string{"claude", "codex"} {
		if len(fields[key]) == 0 || strings.TrimSpace(string(fields[key])) == "null" {
			return ErrUnsafe
		}
	}

	type plain HomeCollection
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var value plain
	if err := dec.Decode(&value); err != nil {
		return err
	}
	*c = HomeCollection(value)
	return nil
}
