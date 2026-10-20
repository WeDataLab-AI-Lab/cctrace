package desktop

import (
	"context"
	"encoding/json"
	"strings"

	"cctrace/internal/profile"
	"cctrace/internal/usage"
)

func (s *Service) accountBillingIdentity(ctx context.Context, provider, home string) (string, error) {
	if s.accountIdentityReader != nil {
		return s.accountIdentityReader(ctx, provider, home)
	}
	if provider == "codex" {
		id, _, e := boundedCodexAccount(home)
		return id, e
	}
	result, e := usage.FetchAccountIdentityContext(ctx, home)
	if e != nil {
		return "", e
	}
	return result.AccountID, nil
}
func (s *Service) excludeQuotaAccount(ctx context.Context, raw json.RawMessage) (any, *ErrorObject) {
	fields, ref, gen, e := accountPayload(raw, "excluded")
	if e != nil {
		return nil, e
	}
	var excluded bool
	if json.Unmarshal(fields["excluded"], &excluded) != nil || strings.TrimSpace(string(fields["excluded"])) == "null" {
		return nil, payloadError()
	}
	s.accountsMu.Lock()
	if e = s.loadAccountsLocked(); e != nil {
		s.accountsMu.Unlock()
		return nil, e
	}
	issues, issue := s.reconcileAccountsLocked()
	if issue != nil {
		s.accountsMu.Unlock()
		return nil, issue
	}
	if len(issues) > 0 {
		s.accountsMu.Unlock()
		return nil, accountError("discovery_incomplete")
	}
	i := s.accountIndexLocked(ref, gen)
	if i < 0 {
		s.accountsMu.Unlock()
		return nil, accountError("account_mismatch")
	}
	a := s.accounts.Accounts[i]
	s.accountsMu.Unlock()
	if s.registry == nil || len(a.Profiles) == 0 {
		return nil, accountError("exclusion_not_applicable")
	}
	for _, link := range a.Profiles {
		entry, err := s.registry.Load(link.Name)
		if err != nil {
			return nil, accountError("profile_conflict")
		}
		if entry.Profile.HomesVersion != 0 {
			return nil, &ErrorObject{Code: "capability_required", Message: "Review multi-home transmission changes through preview and confirmation."}
		}
	}
	if !s.accountDefinitionsCurrent(a) {
		return nil, accountError("account_mismatch")
	}
	bounded, cancel := context.WithTimeout(ctx, providerReadTimeout)
	defer cancel()
	type mutation struct {
		entry   profile.Entry
		entries []string
	}
	planned := []mutation{}
	billing := ""
	for _, link := range a.Profiles {
		entry, err := s.registry.Load(link.Name)
		if err != nil || entry.Revision != a.Definitions[link.ID] {
			return nil, accountError("profile_conflict")
		}
		home, configured := s.accountConfiguredHome(entry.Profile, a.Provider, entry.Name)
		namespace := "anthropic"
		if a.Provider == "codex" {
			namespace = "openai"
		}
		valid, err := s.accountHome(home)
		if err != nil || !configured || !accountHomeMatches(a, valid) {
			return nil, accountError("account_mismatch")
		}
		id, err := s.accountBillingIdentity(bounded, a.Provider, valid)
		if err != nil || id == "" || !safeBillingID(id) {
			return nil, accountError("account_verification_failed")
		}
		if billing != "" && billing != id {
			return nil, accountError("account_mismatch")
		}
		billing = id
		entries := []string{}
		for _, value := range entry.Profile.Options.ExcludeAccounts {
			trimmed := strings.TrimSpace(value)
			if !excluded && trimmed == id {
				return nil, accountError("exclusion_ambiguous")
			}
			parts := strings.SplitN(trimmed, ":", 2)
			target := len(parts) == 2 && strings.ToLower(strings.TrimSpace(parts[0])) == namespace && strings.TrimSpace(parts[1]) == id
			if !target {
				entries = append(entries, value)
			}
		}
		if excluded && !localExcludes(entries, a.Provider, id) {
			entries = append(entries, namespace+":"+id)
		}
		planned = append(planned, mutation{entry: entry, entries: entries})
	}
	// Revalidate ownership after metadata proof, before the first profile mutation.
	s.accountsMu.Lock()
	if s.accountIndexLocked(ref, gen) < 0 {
		s.accountsMu.Unlock()
		return nil, accountError("account_mismatch")
	}
	s.accountsMu.Unlock()
	for _, m := range planned {
		entry, err := s.registry.Load(m.entry.Name)
		if err != nil || entry.Revision != m.entry.Revision {
			return nil, accountError("profile_conflict")
		}
	}
	// Each CAS shares the normal profile lock. A partial failure remains an explicit
	// error; re-list shows actual mixed state, never false all-success or unsafe rollback.
	for _, m := range planned {
		if _, err := s.registry.UpdateExcludedAccounts(m.entry.Name, m.entry.Revision, m.entries); err != nil {
			return nil, accountError("profile_conflict")
		}
	}
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	i = s.accountIndexLocked(ref, gen)
	if i < 0 {
		return nil, accountError("account_mismatch")
	}
	next := s.accountsCloneLocked()
	row := &next.Accounts[i]
	if rotateAccount(row) != nil {
		return nil, accountError("account_unavailable")
	}
	for j, link := range row.Profiles {
		entry, err := s.registry.Load(link.Name)
		if err != nil {
			return nil, accountError("profile_conflict")
		}
		v, verified := s.checkedAccountExclusion(a, entry, billing, excluded)
		if !verified {
			return nil, accountError("profile_conflict")
		}
		row.Definitions[link.ID] = entry.Revision
		row.Profiles[j].Excluded = &v
	}
	if e = s.commitAccountsLocked(next); e != nil {
		return nil, e
	}
	s.cancelAccountFlight(ref)
	return s.accountsViewLocked(), nil
}
func safeBillingID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func (s *Service) accountMetadataProof(ctx context.Context, provider, home string) (string, string, error) {
	if provider == "codex" {
		id, _, e := boundedCodexAccount(home)
		if e != nil || id == "" {
			return "", "", errCodexIdentityUnavailable
		}
		return "codex\x00" + id + "\x00codex-bucket", id, nil
	}
	result, e := usage.FetchAccountIdentityContext(ctx, home)
	if e != nil {
		return "", "", e
	}
	return "claude\x00" + result.AccountID + "\x00" + result.OrganizationID + "\x00all-model-five-seven", result.AccountID, nil
}

func (s *Service) checkedAccountExclusion(a quotaAccountRecord, entry profile.Entry, billing string, requested bool) (bool, bool) {
	home, configured := s.accountConfiguredHome(entry.Profile, a.Provider, entry.Name)
	valid, err := s.accountHome(home)
	if err != nil || !configured || !accountHomeMatches(a, valid) {
		return false, false
	}
	actual := localExcludes(entry.Profile.Options.ExcludeAccounts, a.Provider, billing)
	return actual, actual == requested
}
