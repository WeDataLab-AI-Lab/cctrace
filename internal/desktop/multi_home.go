package desktop

import (
	"context"
	"encoding/json"
	"os"
	"slices"

	"cctrace/internal/profile"
)

type homePlan struct {
	PreviewRef   string                   `json:"preview_ref"`
	ProfileID    string                   `json:"profile_id"`
	Name         string                   `json:"name"`
	Revision     string                   `json:"revision"`
	Homes        []profile.HomeMembership `json:"homes"`
	Effect       string                   `json:"effect"`
	identities   []os.FileInfo
	census       map[string]string
	quotaChanges []profile.HomeMembership
}

func (s *Service) previewHomes(raw json.RawMessage) (any, *ErrorObject) {
	var p struct {
		Name     string          `json:"name"`
		Revision string          `json:"revision"`
		Homes    json.RawMessage `json:"homes"`
	}
	if decodePayload(raw, &p) != nil || p.Name == "" || p.Revision == "" || p.Homes == nil {
		return nil, payloadError()
	}
	if s.registry == nil {
		return nil, sharedProfileError(profile.ErrUnsafe)
	}
	homes, decisionErr := profile.DecodeHomeDecisions(p.Homes)
	if decisionErr != nil {
		return nil, payloadError()
	}
	entry, err := s.registry.Load(p.Name)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	if entry.Revision != p.Revision {
		return nil, sharedProfileError(profile.ErrConflict)
	}
	if entry.Profile.HomesVersion != 1 {
		return nil, &ErrorObject{Code: "onboarding_required", Message: "Enroll homes through the complete discovery and onboarding flow."}
	}
	if len(homes) != len(entry.Profile.Homes) {
		return nil, &ErrorObject{Code: "onboarding_required", Message: "Membership topology changes require a complete onboarding migration."}
	}
	for _, requested := range homes {
		enrolled := false
		for _, existing := range entry.Profile.Homes {
			if requested.Provider == existing.Provider && requested.Home == existing.Home {
				enrolled = true
				if requested.Transmission != existing.Transmission || requested.QuotaEnabled != existing.QuotaEnabled {
					return nil, &ErrorObject{Code: "transmission_preview_required", Message: "Review transmission changes with all linked homes."}
				}
			}
		}
		if !enrolled {
			return nil, &ErrorObject{Code: "onboarding_required", Message: "New homes require discovery and complete onboarding decisions."}
		}
	}
	candidate := *entry.Profile
	candidate.HomesVersion = 1
	candidate.Homes = homes
	if candidate.ValidateHomes() != nil {
		return nil, payloadError()
	}
	plan := homePlan{ProfileID: entry.ID, Name: p.Name, Revision: p.Revision, Homes: slices.Clone(homes), Effect: "Change home membership and transmission/usage consent only; collection execution and server policy stay unchanged."}
	for _, h := range homes {
		valid, e := s.accountHome(h.Home)
		if e != nil || valid != h.Home {
			return nil, accountError("invalid_home")
		}
		info, e := os.Stat(h.Home)
		if e != nil {
			return nil, accountError("invalid_home")
		}
		plan.identities = append(plan.identities, info)
	}
	plan.census = map[string]string{}
	entries, e := s.registry.List()
	if e != nil {
		return nil, sharedProfileError(e)
	}
	for _, entry := range entries {
		plan.census[entry.Name] = entry.Revision
	}
	for _, h := range homes {
		for _, old := range entry.Profile.Homes {
			if h.Provider == old.Provider && h.Home == old.Home && h.QuotaEnabled != old.QuotaEnabled {
				plan.quotaChanges = append(plan.quotaChanges, h)
			}
		}
	}
	plan.PreviewRef, err = newSelectionReference()
	if err != nil {
		return nil, accountError("storage_error")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only the latest preview is confirmable. Cancellation has no durable effect.
	s.homePlans = map[string]homePlan{plan.PreviewRef: plan}
	return plan, nil
}
func (s *Service) applyHomes(ctx context.Context, raw json.RawMessage) (any, *ErrorObject) {
	var p struct {
		PreviewRef string `json:"preview_ref"`
		Confirm    bool   `json:"confirm"`
	}
	if decodePayload(raw, &p) != nil || !p.Confirm || !accountOpaque.MatchString(p.PreviewRef) {
		return nil, payloadError()
	}
	s.mu.Lock()
	plan, ok := s.homePlans[p.PreviewRef]
	delete(s.homePlans, p.PreviewRef)
	s.mu.Unlock()
	if !ok {
		return nil, sharedProfileError(profile.ErrConflict)
	}
	for i, h := range plan.Homes {
		info, err := os.Stat(h.Home)
		if err != nil || !os.SameFile(plan.identities[i], info) {
			return nil, sharedProfileError(profile.ErrConflict)
		}
	}
	updates := []profile.HomeUpdate{{Name: plan.Name, Revision: plan.Revision, Homes: plan.Homes}}
	release, err := s.quiesceConsentProfiles(ctx, updates)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	defer release()
	var finalize func(string) error
	if len(plan.quotaChanges) > 0 {
		s.accountsMu.Lock()
		defer s.accountsMu.Unlock()
		if e := s.loadAccountsLocked(); e != nil {
			return nil, e
		}
		finalize = func(dir string) error {
			return s.finalizeHomeQuotaConsent(dir, []profile.HomeUpdate{{Name: plan.Name, Homes: plan.quotaChanges}})
		}
	}
	entries, err := s.registry.ApplyHomeUpdatesWithCensus(updates, false, plan.census, finalize)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return profileAddResult{Profile: s.sharedMapping(entries[0])}, nil
}
