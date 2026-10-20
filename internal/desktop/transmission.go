package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"

	"cctrace/internal/profile"
)

type consentPlan struct {
	ProviderEffects []providerConsentEffect `json:"provider_effects"`
	providerPlans   []providerFilePlan
	quotaUpdates    []profile.HomeUpdate
	kind            string
	servers         []profile.ServerInfo
	census          map[string]string
	PreviewRef      string               `json:"preview_ref"`
	TargetHomes     []string             `json:"target_homes"`
	Provider        string               `json:"provider,omitempty"`
	Home            string               `json:"home,omitempty"`
	Excluded        *bool                `json:"excluded,omitempty"`
	Profiles        []profile.HomeUpdate `json:"profiles"`
	Effect          string               `json:"effect"`
	accountRef      string
	generation      uint64
	identityKey     string
	billing         string
	homes           []string
	stamps          []os.FileInfo
	migration       bool
}

func (s *Service) previewTransmission(raw json.RawMessage) (any, *ErrorObject) {
	var p struct {
		Provider   string `json:"provider"`
		Home       string `json:"home"`
		Excluded   *bool  `json:"excluded"`
		AccountRef string `json:"account_ref,omitempty"`
		Generation uint64 `json:"generation,omitempty"`
	}
	if decodePayload(raw, &p) != nil || p.Excluded == nil || (p.Provider != "claude" && p.Provider != "codex") || (p.AccountRef == "") != (p.Generation == 0) {
		return nil, payloadError()
	}
	home, err := s.accountHome(p.Home)
	if err != nil {
		return nil, accountError("invalid_home")
	}
	plan := consentPlan{kind: "transmission", Provider: p.Provider, Home: home, Excluded: p.Excluded, Profiles: []profile.HomeUpdate{}, Effect: "Change only local session transmission permission for the listed homes; membership, collection execution, usage selection and server policy remain unchanged.", homes: []string{home}}
	if p.AccountRef == "" {
		s.accountsMu.Lock()
		e := s.loadAccountsLocked()
		known := false
		if e == nil {
			for _, a := range s.accounts.Accounts {
				if a.Provider == p.Provider && accountHomeMatches(a, home) && a.IdentityKey != "" && s.localAccountBillingID(a.Provider, home) != "" {
					known = true
				}
			}
		}
		s.accountsMu.Unlock()
		if e != nil {
			return nil, e
		}
		if known {
			return nil, &ErrorObject{Code: "account_ref_required", Message: "Refresh accounts and confirm all previously linked homes for this account."}
		}
	}
	if p.AccountRef != "" {
		s.accountsMu.Lock()
		if e := s.loadAccountsLocked(); e != nil {
			s.accountsMu.Unlock()
			return nil, e
		}
		i := s.accountIndexLocked(p.AccountRef, p.Generation)
		if i < 0 {
			s.accountsMu.Unlock()
			return nil, accountError("account_mismatch")
		}
		a := s.accounts.Accounts[i]
		s.accountsMu.Unlock()
		if a.Provider != p.Provider || !accountHomeMatches(a, home) || !s.accountDefinitionsCurrent(a) {
			return nil, accountError("account_mismatch")
		}
		plan.accountRef = p.AccountRef
		plan.generation = p.Generation
		plan.identityKey = a.IdentityKey
		// Unknown identities stay path-scoped; never infer a billing relationship.
		if a.Proof != "" && safeBillingID(a.BillingID) {
			plan.billing = a.BillingID
			plan.homes = append([]string{a.Home}, a.Aliases...)
		} else if a.IdentityKey != "" {
			id := s.localAccountBillingID(a.Provider, a.Home)
			if id == "" {
				plan.identityKey = ""
				plan.Effect += " Current billing identity is unavailable; this confirmation changes only the selected home path and leaves billing-account filters unchanged."
			} else {
				plan.identityKey = a.IdentityKey
				plan.billing = id
				plan.homes = append([]string{a.Home}, a.Aliases...)
			}
		}
	}
	if s.registry == nil {
		return nil, sharedProfileError(profile.ErrUnsafe)
	}
	entries, err := s.registry.List()
	if err != nil {
		return nil, sharedProfileError(err)
	}
	for _, entry := range entries {
		homes := s.profileMemberships(entry)
		changed := false
		for i, h := range homes {
			if h.Provider == p.Provider && slices.Contains(plan.homes, h.Home) {
				if *p.Excluded {
					homes[i].Transmission = "exclude"
				} else {
					homes[i].Transmission = "allow"
				}
				changed = true
			}
		}
		if changed {
			update := profile.HomeUpdate{Name: entry.Name, Revision: entry.Revision, Homes: homes}
			if plan.billing != "" {
				namespace := "anthropic"
				if p.Provider == "codex" {
					namespace = "openai"
				}
				excluded := []string{}
				for _, v := range entry.Profile.Options.ExcludeAccounts {
					if !*p.Excluded && strings.TrimSpace(v) == plan.billing {
						return nil, accountError("exclusion_ambiguous")
					}
					if strings.TrimSpace(v) != namespace+":"+plan.billing {
						excluded = append(excluded, v)
					}
				}
				if *p.Excluded {
					excluded = append(excluded, namespace+":"+plan.billing)
				}
				update.ExcludeAccounts = &excluded
			}
			plan.Profiles = append(plan.Profiles, update)
		}
	}
	if len(plan.Profiles) == 0 {
		return nil, accountError("exclusion_not_applicable")
	}
	return s.storeConsentPlan(plan)
}

func (s *Service) profileMemberships(entry profile.Entry) []profile.HomeMembership {
	p := entry.Profile
	if p.HomesVersion != 0 || p.Homes != nil {
		return slices.Clone(p.Homes)
	}
	result := []profile.HomeMembership{}
	for _, provider := range []string{"claude", "codex"} {
		home, configured := s.accountConfiguredHome(p, provider, entry.Name)
		if home == "" || !configured {
			continue
		}
		valid, err := s.accountHome(home)
		if err != nil {
			continue
		}
		h := profile.HomeMembership{Provider: provider, Home: valid, Transmission: "allow"}
		if excluded := s.localAccountExcluded(provider, valid, p.Options.ExcludeAccounts); excluded != nil && *excluded {
			h.Transmission = "exclude"
		}
		result = append(result, h)
	}
	return result
}
func (s *Service) storeConsentPlan(plan consentPlan) (any, *ErrorObject) {
	plan.TargetHomes = append([]string{}, plan.homes...)
	plan.ProviderEffects = []providerConsentEffect{}
	entries, err := s.registry.List()
	if err != nil {
		return nil, sharedProfileError(err)
	}
	plan.census = map[string]string{}
	for _, entry := range entries {
		plan.census[entry.Name] = entry.Revision
		plan.servers = append(plan.servers, entry.Profile.Server)
	}
	for i := range plan.Profiles {
		u := &plan.Profiles[i]
		u.DirectoryIdentities = map[string]string{}
		for _, h := range u.Homes {
			identity, e := profile.DirectoryIdentity(h.Home)
			if e != nil {
				return nil, sharedProfileError(e)
			}
			u.DirectoryIdentities[h.Home] = identity
		}
		if u.Revision != "" && plan.census[u.Name] != u.Revision {
			return nil, sharedProfileError(profile.ErrConflict)
		}
		if u.Definition != nil {
			plan.servers = append(plan.servers, u.Definition.Server)
		}
	}
	if err := s.planHomeProviderConsent(&plan); err != nil {
		return nil, sharedProfileError(err)
	}
	for _, home := range plan.homes {
		info, err := os.Stat(home)
		if err != nil {
			return nil, accountError("invalid_home")
		}
		plan.stamps = append(plan.stamps, info)
	}
	ref, err := newSelectionReference()
	if err != nil {
		return nil, accountError("storage_error")
	}
	plan.PreviewRef = ref
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consentPlans = map[string]consentPlan{ref: plan}
	return plan, nil
}
func (s *Service) applyConsent(ctx context.Context, raw json.RawMessage, kind string) (any, *ErrorObject) {
	migration := kind == "onboarding"
	var p struct {
		PreviewRef string `json:"preview_ref"`
		Confirm    bool   `json:"confirm"`
	}
	if decodePayload(raw, &p) != nil || !p.Confirm || !accountOpaque.MatchString(p.PreviewRef) {
		return nil, payloadError()
	}
	s.mu.Lock()
	plan, ok := s.consentPlans[p.PreviewRef]
	delete(s.consentPlans, p.PreviewRef)
	s.mu.Unlock()
	if !ok || plan.kind != kind {
		return nil, sharedProfileError(profile.ErrConflict)
	}
	for i, home := range plan.homes {
		info, err := os.Stat(home)
		if err != nil || !os.SameFile(plan.stamps[i], info) || profile.CheckCodexHome(home) != nil {
			return nil, sharedProfileError(profile.ErrConflict)
		}
	}
	if plan.accountRef != "" {
		s.accountsMu.Lock()
		defer s.accountsMu.Unlock()
		i := s.accountIndexLocked(plan.accountRef, plan.generation)
		if i < 0 || !s.accountDefinitionsCurrent(s.accounts.Accounts[i]) {
			return nil, accountError("account_mismatch")
		}
		if plan.billing != "" {
			bounded, cancel := context.WithTimeout(ctx, providerReadTimeout)
			defer cancel()
			for _, home := range plan.homes {
				var id string
				var err error
				if plan.identityKey != "" {
					var proof string
					proof, id, err = s.accountMetadataProof(bounded, plan.Provider, home)
					sum := sha256.Sum256([]byte(proof))
					if err == nil && hex.EncodeToString(sum[:]) != plan.identityKey {
						err = profile.ErrConflict
					}
				} else {
					id, err = s.accountBillingIdentity(bounded, plan.Provider, home)
				}
				if err != nil || id != plan.billing {
					return nil, accountError("account_verification_failed")
				}
			}
		}
	}
	release, stopErr := s.quiesceConsentProfiles(ctx, plan.Profiles)
	if stopErr != nil {
		return nil, sharedProfileError(stopErr)
	}
	defer release()
	finalize := func(directory string) error { return s.applyHomeProviderConsent(directory, plan) }
	if migration {
		s.accountsMu.Lock()
		defer s.accountsMu.Unlock()
		if e := s.loadAccountsLocked(); e != nil {
			return nil, e
		}
		finalize = func(directory string) error {
			if err := s.applyHomeProviderConsent(directory, plan); err != nil {
				return err
			}
			return s.finalizeHomeQuotaConsent(directory, plan.quotaUpdates)
		}
	}
	entries, err := s.registry.ApplyHomeUpdatesWithCensus(plan.Profiles, migration, plan.census, finalize)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := profileListResult{Profiles: []profileMapping{}}
	for _, entry := range entries {
		result.Profiles = append(result.Profiles, s.sharedMapping(entry))
	}
	return result, nil
}
