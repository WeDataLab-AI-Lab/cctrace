package desktop

import (
	"encoding/json"
	"errors"
	"math"
	"os"

	"cctrace/internal/profile"
)

func sharedProfileError(err error) *ErrorObject {
	code, message := "storage_error", "profile registry is unavailable"
	switch {
	case errors.Is(err, profile.ErrConflict):
		code, message = "profile_conflict", "profile changed; refresh before selecting or editing"
	case errors.Is(err, profile.ErrExists):
		code, message = "profile_exists", "profile already exists"
	case errors.Is(err, profile.ErrUnsafe):
		code, message = "unsafe_storage", "configured profiles and runtime data require explicit CLI management"
	case errors.Is(err, os.ErrNotExist):
		code, message = "profile_not_found", "profile does not exist"
	}
	return &ErrorObject{Code: code, Message: message}
}
func (s *Service) sharedMapping(entry profile.Entry) profileMapping {
	p := entry.Profile
	removable := s.registry.CanRemoveMapping(entry)
	mapping := profileMapping{ID: entry.ID, Name: entry.Name, Revision: entry.Revision, ClaudeHome: p.ClaudeConfigDir, CodexHome: p.CodexHome, CodexDirs: p.Options.CodexDirs, CollectionState: entry.CollectionState, ConfigurationComplete: &entry.ConfigurationComplete, Removable: &removable}
	if s.multiHomeProfiles.Load() {
		mapping.HomeCollection = p.HomeCollection
		mapping.HomesVersion = p.HomesVersion
		if p.HomesVersion == 1 {
			homes := p.Homes
			if homes == nil {
				homes = []profile.HomeMembership{}
			}
			mapping.Homes = &homes
		}
	}
	return mapping
}
func (s *Service) listSharedProfiles() (any, *ErrorObject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conflicts, err := s.registry.MigrateDesktop(s.appSupportRoot)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	entries, err := s.registry.List()
	if err != nil {
		return nil, sharedProfileError(err)
	}
	mappings := make([]profileMapping, 0, len(entries))
	for _, entry := range entries {
		mappings = append(mappings, s.sharedMapping(entry))
	}
	return profileListResult{Profiles: mappings, Conflicts: conflicts}, nil
}
func (s *Service) sharedHomes(claudeHome, codexHome string) (string, string, *ErrorObject) {
	claude, err := s.validateHome(claudeHome)
	if err != nil {
		return "", "", &ErrorObject{Code: "invalid_home", Message: "mapped home must be an absolute existing directory outside app-support"}
	}
	codex, err := s.validateHome(codexHome)
	if err != nil {
		return "", "", &ErrorObject{Code: "invalid_home", Message: "mapped home must be an absolute existing directory outside app-support"}
	}
	for _, home := range []string{claude, codex} {
		if home != "" && (isWithin(resolvePathForComparison(s.registry.Root), home) || isWithin(home, resolvePathForComparison(s.registry.Root))) {
			return "", "", &ErrorObject{Code: "invalid_home", Message: "mapped homes must be outside profile storage"}
		}
	}
	return claude, codex, nil
}
func (s *Service) addSharedProfile(raw json.RawMessage) (any, *ErrorObject) {
	var fields map[string]json.RawMessage
	if decodePayload(raw, &fields) != nil || len(fields) != 3 {
		return nil, payloadError()
	}
	name, n := requiredString(fields, "name")
	claude, c := requiredString(fields, "claude_home")
	codex, x := requiredString(fields, "codex_home")
	if !n || !c || !x {
		return nil, payloadError()
	}
	if profile.ValidateName(name) != nil {
		return nil, &ErrorObject{Code: "invalid_profile_name", Message: "profile name is invalid"}
	}
	claude, codex, e := s.sharedHomes(claude, codex)
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.registry.MigrateDesktop(s.appSupportRoot); err != nil {
		return nil, sharedProfileError(err)
	}
	entry, err := s.registry.RegisterMapping(name, claude, codex)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	return profileAddResult{Profile: s.sharedMapping(entry)}, nil
}
func (s *Service) updateSharedProfile(raw json.RawMessage) (any, *ErrorObject) {
	if s.registry == nil {
		return nil, &ErrorObject{Code: "unsupported_operation", Message: "shared profile registry is unavailable"}
	}
	var payload struct {
		Name       string `json:"name"`
		Revision   string `json:"revision"`
		ClaudeHome string `json:"claude_home"`
		CodexHome  string `json:"codex_home"`
	}
	if decodePayload(raw, &payload) != nil || payload.Name == "" || payload.Revision == "" {
		return nil, payloadError()
	}
	claude, codex, e := s.sharedHomes(payload.ClaudeHome, payload.CodexHome)
	if e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if conflicts, err := s.registry.MigrateDesktop(s.appSupportRoot); err != nil {
		return nil, sharedProfileError(err)
	} else {
		for _, c := range conflicts {
			if c.Name == payload.Name {
				return nil, &ErrorObject{Code: "migration_conflict", Message: "select the CLI or desktop folders before editing"}
			}
		}
	}
	entry, err := s.registry.UpdateMapping(payload.Name, payload.Revision, claude, codex)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	s.invalidateSharedProfileSelectionsLocked(payload.Name)
	return profileAddResult{Profile: s.sharedMapping(entry)}, nil
}
func (s *Service) resolveSharedProfile(raw json.RawMessage) (any, *ErrorObject) {
	if s.registry == nil {
		return nil, &ErrorObject{Code: "unsupported_operation", Message: "shared profile registry is unavailable"}
	}
	var payload struct {
		Name            string `json:"name"`
		Source          string `json:"source"`
		CLIRevision     string `json:"cli_revision"`
		DesktopRevision string `json:"desktop_revision"`
	}
	if decodePayload(raw, &payload) != nil || payload.Name == "" || payload.CLIRevision == "" || payload.DesktopRevision == "" || (payload.Source != "cli" && payload.Source != "desktop") {
		return nil, payloadError()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.registry.ResolveMigration(s.appSupportRoot, payload.Name, payload.Source, payload.CLIRevision, payload.DesktopRevision)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	s.invalidateSharedProfileSelectionsLocked(payload.Name)
	entry, err := s.registry.Load(payload.Name)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	return profileAddResult{Profile: s.sharedMapping(entry)}, nil
}
func (s *Service) invalidateSharedProfileSelectionsLocked(name string) {
	for provider, state := range s.quotaSelections {
		if state.Profile != name {
			continue
		}
		if state.Generation == math.MaxUint64 {
			state.Enabled = false
		} else {
			state.Generation++
		}
		state.DefinitionRevision = ""
		state.AuthIdentity = ""
		ref, err := newSelectionReference()
		if err != nil {
			state.Enabled = false
		} else {
			state.SelectionRef = ref
		}
		s.clearQuotaCache(provider)
		s.cancelQuotaProviderFlight(provider)
	}
}
func (s *Service) previewSharedProfileRemoval(raw json.RawMessage) (any, *ErrorObject) {
	if s.registry == nil {
		return nil, &ErrorObject{Code: "unsupported_operation", Message: "shared profile registry is unavailable"}
	}
	var payload struct {
		Name string `json:"name"`
	}
	if decodePayload(raw, &payload) != nil || payload.Name == "" {
		return nil, payloadError()
	}
	entry, err := s.registry.Load(payload.Name)
	if err != nil {
		return nil, sharedProfileError(err)
	}
	mapping := s.sharedMapping(entry)
	return struct {
		Profile profileMapping `json:"profile"`
		Effect  string         `json:"effect"`
	}{mapping, "Remove only the disabled registration metadata; provider folders, credentials, history and runtime data stay in place."}, nil
}
func (s *Service) removeSharedProfile(raw json.RawMessage) (any, *ErrorObject) {
	var payload struct {
		Name     string `json:"name"`
		Revision string `json:"revision"`
		Confirm  bool   `json:"confirm"`
	}
	if decodePayload(raw, &payload) != nil || payload.Name == "" || payload.Revision == "" || !payload.Confirm {
		return nil, payloadError()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.registry.RemoveMapping(payload.Name, payload.Revision); err != nil {
		return nil, sharedProfileError(err)
	}
	result := profileRemoveResult{Name: payload.Name, Removed: true}
	providers, err := s.resetQuotaSelectionsForProfileLocked(payload.Name)
	if err != nil {
		return result, &ErrorObject{Code: "storage_error", Message: "profile removed but usage selection requires refresh"}
	}
	for _, provider := range providers {
		s.clearQuotaCache(provider)
		s.cancelQuotaProviderFlight(provider)
	}
	return result, nil
}

// External CLI saves are observed before publishing a selection or serving cache.
func (s *Service) refreshSharedSelectionLocked(provider string) *ErrorObject {
	state := s.quotaSelections[provider]
	entry, err := s.registry.Load(state.Profile)
	if errors.Is(err, os.ErrNotExist) && state.Profile == "default" && state.DefinitionRevision == "" {
		return nil
	}
	if err != nil {
		return &ErrorObject{Code: "selection_unavailable", Message: "selected profile is unavailable"}
	}
	if state.DefinitionRevision == "" {
		state.DefinitionRevision = entry.Revision
		return nil
	}
	if state.DefinitionRevision == entry.Revision {
		return nil
	}
	if state.Generation == math.MaxUint64 {
		return &ErrorObject{Code: "selection_unavailable", Message: "selection generation is unavailable"}
	}
	ref, err := newSelectionReference()
	if err != nil {
		return &ErrorObject{Code: "selection_unavailable", Message: "selection reference is unavailable"}
	}
	s.clearQuotaCache(provider)
	s.cancelQuotaProviderFlight(provider)
	state.SelectionRef = ref
	state.Generation++
	state.AuthIdentity = ""
	state.DefinitionRevision = entry.Revision
	return nil
}
