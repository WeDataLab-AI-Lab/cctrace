package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cctrace/internal/profile"
)

type onboardingSnapshot struct {
	DiscoveryRef string                   `json:"discovery_ref"`
	Complete     bool                     `json:"complete"`
	ObservedAt   string                   `json:"observed_at"`
	Homes        []profile.HomeMembership `json:"homes"`
	stamps       []os.FileInfo
}

func (s *Service) onboardingRoot() (string, error) {
	if s.homeDiscoveryRoot != "" {
		return s.homeDiscoveryRoot, nil
	}
	if s.fixtureRoot != "" {
		return "", profile.ErrUnsafe
	}
	return os.UserHomeDir()
}
func (s *Service) discoverOnboarding(raw json.RawMessage) (any, *ErrorObject) {
	if !emptyPayload(raw) {
		return nil, payloadError()
	}
	if s.registry == nil {
		return nil, sharedProfileError(profile.ErrUnsafe)
	}
	root, err := s.onboardingRoot()
	if err != nil {
		return nil, sharedProfileError(err)
	}
	homes, err := s.registry.OnboardingHomes(root)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	ref, err := newSelectionReference()
	if err != nil {
		return nil, accountError("storage_error")
	}
	snap := onboardingSnapshot{DiscoveryRef: ref, Complete: true, ObservedAt: s.now().UTC().Format(time.RFC3339Nano), Homes: homes}
	for _, h := range homes {
		info, err := os.Stat(h.Home)
		if err != nil {
			return nil, accountError("invalid_home")
		}
		snap.stamps = append(snap.stamps, info)
	}
	s.mu.Lock()
	s.onboardingSnapshot = &snap
	s.mu.Unlock()
	return snap, nil
}
func (s *Service) previewOnboarding(raw json.RawMessage) (any, *ErrorObject) {
	var p struct {
		Collection   *profile.HomeCollection `json:"collection,omitempty"`
		DiscoveryRef string                  `json:"discovery_ref"`
		Decisions    json.RawMessage         `json:"decisions"`
		Server       *profile.ServerInfo     `json:"server,omitempty"`
		User         *profile.UserInfo       `json:"user,omitempty"`
	}
	if decodePayload(raw, &p) != nil || p.Decisions == nil || !accountOpaque.MatchString(p.DiscoveryRef) {
		return nil, payloadError()
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, key := range []string{"server", "user", "collection"} {
		if value, exists := fields[key]; exists && string(value) == "null" {
			return nil, payloadError()
		}
	}
	if value, exists := fields["collection"]; exists {
		var flags map[string]json.RawMessage
		if decodePayload(value, &flags) != nil || len(flags) != 2 {
			return nil, payloadError()
		}
		for _, key := range []string{"claude", "codex"} {
			var flag bool
			if len(flags[key]) == 0 || string(flags[key]) == "null" || json.Unmarshal(flags[key], &flag) != nil {
				return nil, payloadError()
			}
		}
	}
	decisions, decisionErr := profile.DecodeHomeDecisions(p.Decisions)
	if decisionErr != nil {
		return nil, payloadError()
	}
	s.mu.Lock()
	snap := s.onboardingSnapshot
	s.mu.Unlock()
	if snap == nil || snap.DiscoveryRef != p.DiscoveryRef {
		return nil, sharedProfileError(profile.ErrConflict)
	}
	for i, h := range snap.Homes {
		info, err := os.Stat(h.Home)
		if err != nil || !os.SameFile(snap.stamps[i], info) {
			return nil, sharedProfileError(profile.ErrConflict)
		}
	}
	root, err := s.onboardingRoot()
	if err != nil {
		return nil, sharedProfileError(err)
	}
	updates, err := s.registry.PlanOnboarding(root, decisions)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	for i := range updates {
		u := &updates[i]
		if u.Name != "default" {
			continue
		}
		old, e := s.registry.Load("default")
		configured := profile.NewDefault()
		if e == nil {
			configured = old.Profile
		} else {
			configured.Options.SyncEnabled = false
		}
		if p.Server != nil {
			configured.Server = *p.Server
		}
		if p.User != nil {
			configured.User = *p.User
		}
		if p.Collection != nil {
			configured.HomeCollection = p.Collection
			configured.HomesVersion = 1
			configured.Options.SyncEnabled = false
			configured.Options.CodexSyncEnabled = false
		}
		if configured.Server.Endpoint != "" || configured.Server.SyncEndpoint != "" {
			configured.RegistrationOnly = false
		}
		for _, h := range u.Homes {
			if h.Transmission == "allow" && (configured.Server.Endpoint == "" && configured.Server.SyncEndpoint == "" || configured.User.ID == "" && configured.User.Email == "") {
				return nil, &ErrorObject{Code: "server_setup_required", Message: "Configure the server and user before confirming transmission."}
			}
		}
		if p.Server != nil || p.User != nil || p.Collection != nil {
			u.Definition = configured
		}
	}
	plan := consentPlan{kind: "onboarding", Profiles: updates, Effect: "Confirm all home transmission and independent usage choices. Initial allowed homes join default; excluded homes join personal with collection stopped. Existing explicit membership is preserved. No provider credential content is copied.", migration: true}
	for _, u := range updates {
		if u.Name == "default" {
			configured := profile.NewDefault()
			configured.Options.SyncEnabled = false
			if old, e := s.registry.Load("default"); e == nil {
				configured = old.Profile
			}
			if u.Definition != nil {
				configured = u.Definition
			}
			plan.Effect += fmt.Sprintf(" Default collection execution: Claude=%t, Codex=%t; server configured=%t. Existing provider processes must restart after any listed exporter cleanup.", configured.CollectionEnabled("claude"), configured.CollectionEnabled("codex"), configured.Server.Endpoint != "" || configured.Server.SyncEndpoint != "")
		}
	}

	selected := map[string]bool{}
	for _, h := range decisions {
		selected[h.Provider+"\x00"+h.Home] = true
	}
	for _, u := range updates {
		quota := profile.HomeUpdate{Name: u.Name}
		for _, h := range u.Homes {
			if selected[h.Provider+"\x00"+h.Home] {
				quota.Homes = append(quota.Homes, h)
			}
		}
		if len(quota.Homes) > 0 {
			plan.quotaUpdates = append(plan.quotaUpdates, quota)
		}
	}
	for _, h := range decisions {
		plan.homes = append(plan.homes, h.Home)
	}
	// Include only canonical existing homes; provider configuration is handled
	// separately from source consent and never implied by an account exclusion.
	for _, h := range plan.homes {
		if !filepath.IsAbs(h) {
			return nil, payloadError()
		}
	}
	return s.storeConsentPlan(plan)
}
