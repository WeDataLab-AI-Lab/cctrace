package desktop

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"

	"cctrace/internal/profile"
)

const (
	quotaSelectionStoreName = "quota-selections.json"
	quotaPollInterval       = 300
	maxQuotaSelectionBytes  = 64 << 10
)

type quotaSelectionPreference struct {
	Profile string `json:"profile"`
	Enabled bool   `json:"enabled"`
}

type quotaSelectionStore struct {
	Version    int                                 `json:"version"`
	Selections map[string]quotaSelectionPreference `json:"selections"`
}

type quotaSelectionState struct {
	quotaSelectionPreference
	SelectionRef       string
	Generation         uint64
	DefinitionRevision string
	AuthIdentity       string
}

type quotaSelectionView struct {
	Provider     string `json:"provider"`
	Profile      string `json:"profile"`
	Enabled      bool   `json:"enabled"`
	SelectionRef string `json:"selection_ref"`
	Generation   uint64 `json:"generation"`
	Source       string `json:"source"`
}

type quotaSelectionsResult struct {
	Selections          []quotaSelectionView `json:"selections"`
	PollIntervalSeconds int                  `json:"poll_interval_seconds"`
}

type selectedQuotaHome struct {
	provider string
	profile  string
	home     string
	source   string
	enabled  bool
	ref      string
	gen      uint64
}

func (s *Service) initializeQuotaSelections() error {
	preferences, err := s.readQuotaSelectionPreferences()
	if err != nil {
		return err
	}
	s.quotaSelections = make(map[string]*quotaSelectionState, 2)
	for _, provider := range []string{"claude", "codex"} {
		preference, ok := preferences[provider]
		if !ok {
			preference = quotaSelectionPreference{Profile: "default", Enabled: true}
		}
		ref, err := newSelectionReference()
		if err != nil {
			return errors.New("quota selection reference could not be created")
		}
		s.quotaSelections[provider] = &quotaSelectionState{
			quotaSelectionPreference: preference,
			SelectionRef:             ref,
			Generation:               1,
		}
	}
	return nil
}

func (s *Service) readQuotaSelectionPreferences() (map[string]quotaSelectionPreference, error) {
	preferences := map[string]quotaSelectionPreference{
		"claude": {Profile: "default", Enabled: true},
		"codex":  {Profile: "default", Enabled: true},
	}
	path := filepath.Join(s.appSupportRoot, quotaSelectionStoreName)
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return preferences, nil
	}
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() > maxQuotaSelectionBytes || before.Mode().Perm() != 0o600 {
		return nil, errors.New("quota selection storage is unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("quota selection storage could not be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 || !os.SameFile(before, opened) || opened.Size() > maxQuotaSelectionBytes {
		return nil, errors.New("quota selection storage changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxQuotaSelectionBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxQuotaSelectionBytes {
		return nil, errors.New("quota selection storage is invalid")
	}
	after, fileErr := file.Stat()
	currentPath, pathErr := os.Lstat(path)
	if fileErr != nil || pathErr != nil || currentPath.Mode()&os.ModeSymlink != 0 || !currentPath.Mode().IsRegular() || currentPath.Mode().Perm() != 0o600 ||
		!os.SameFile(opened, after) || !os.SameFile(opened, currentPath) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) ||
		after.Size() != currentPath.Size() || !after.ModTime().Equal(currentPath.ModTime()) {
		return nil, errors.New("quota selection storage changed")
	}
	if !lifecycleJSONHasUniqueKeys(data) {
		return nil, errors.New("quota selection storage is ambiguous")
	}
	var stored quotaSelectionStore
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return nil, errors.New("quota selection storage is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || stored.Version != 1 || len(stored.Selections) != 2 {
		return nil, errors.New("quota selection storage is invalid")
	}
	for _, provider := range []string{"claude", "codex"} {
		preference, ok := stored.Selections[provider]
		if !ok || (preference.Profile != "default" && profile.ValidateName(preference.Profile) != nil) {
			return nil, errors.New("quota selection storage is invalid")
		}
		preferences[provider] = preference
	}
	return preferences, nil
}

func (s *Service) saveQuotaSelectionPreferencesLocked(preferences map[string]quotaSelectionPreference) error {
	stored := quotaSelectionStore{Version: 1, Selections: make(map[string]quotaSelectionPreference, 2)}
	for _, provider := range []string{"claude", "codex"} {
		preference, ok := preferences[provider]
		if !ok || (preference.Profile != "default" && profile.ValidateName(preference.Profile) != nil) {
			return errors.New("invalid quota selection preference")
		}
		stored.Selections[provider] = preference
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return errors.New("quota selection storage could not be encoded")
	}
	path := filepath.Join(s.appSupportRoot, quotaSelectionStoreName)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return errors.New("quota selection storage is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("quota selection storage could not be checked")
	}
	temporary, err := os.CreateTemp(s.appSupportRoot, ".quota-selections-*")
	if err != nil {
		return errors.New("quota selection storage could not be created")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("quota selection storage permissions could not be set")
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("quota selection storage could not be written")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("quota selection storage could not be synchronized")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("quota selection storage could not be closed")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return errors.New("quota selection storage could not be committed")
	}
	directory, err := os.Open(s.appSupportRoot)
	if err != nil {
		return errors.New("quota selection storage directory could not be opened")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("quota selection storage directory could not be synchronized")
	}
	return nil
}

func newSelectionReference() (string, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func (s *Service) quotaSelectionRead() (any, *ErrorObject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quotaSelectionsResultLocked()
}

func (s *Service) quotaSelectionsResultLocked() (any, *ErrorObject) {
	result := quotaSelectionsResult{Selections: make([]quotaSelectionView, 0, 2), PollIntervalSeconds: quotaPollInterval}
	for _, provider := range []string{"claude", "codex"} {
		state := s.quotaSelections[provider]
		if state == nil {
			return nil, &ErrorObject{Code: "selection_unavailable", Message: "provider selection is unavailable"}
		}
		if s.registry != nil {
			if err := s.refreshSharedSelectionLocked(provider); err != nil {
				return nil, err
			}
		}
		result.Selections = append(result.Selections, quotaSelectionView{
			Provider: provider, Profile: state.Profile, Enabled: state.Enabled,
			SelectionRef: state.SelectionRef, Generation: state.Generation,
			Source: s.quotaSource(provider),
		})
	}
	return result, nil
}

func (s *Service) quotaSelectionSet(raw json.RawMessage) (any, *ErrorObject) {
	var fields map[string]json.RawMessage
	if err := decodePayload(raw, &fields); err != nil || len(fields) != 3 {
		return nil, payloadError()
	}
	provider, providerOK := requiredString(fields, "provider")
	profileName, profileOK := requiredString(fields, "profile")
	var enabled bool
	enabledOK := json.Unmarshal(fields["enabled"], &enabled) == nil
	if !providerOK || !profileOK || !enabledOK || (provider != "claude" && provider != "codex") {
		return nil, payloadError()
	}
	if profileName != "default" && profile.ValidateName(profileName) != nil {
		return nil, &ErrorObject{Code: "invalid_profile_name", Message: "profile name is invalid"}
	}

	s.mu.Lock()
	current := s.quotaSelections[provider]
	if current == nil {
		s.mu.Unlock()
		return nil, &ErrorObject{Code: "selection_unavailable", Message: "provider selection is unavailable"}
	}
	if current.Profile != profileName {
		if _, err := s.resolveQuotaSelectionHomeLocked(provider, profileName); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	changed := current.Profile != profileName || current.Enabled != enabled
	if changed {
		if current.Generation == math.MaxUint64 {
			s.mu.Unlock()
			return nil, &ErrorObject{Code: "selection_unavailable", Message: "provider selection generation is unavailable"}
		}
		ref, err := newSelectionReference()
		if err != nil {
			s.mu.Unlock()
			return nil, &ErrorObject{Code: "selection_unavailable", Message: "provider selection reference could not be created"}
		}
		preferences := s.quotaSelectionPreferencesLocked()
		preferences[provider] = quotaSelectionPreference{Profile: profileName, Enabled: enabled}
		if err := s.saveQuotaSelectionPreferencesLocked(preferences); err != nil {
			s.mu.Unlock()
			return nil, &ErrorObject{Code: "storage_error", Message: "provider selection could not be saved"}
		}
		s.clearQuotaCache(provider)
		s.cancelQuotaProviderFlight(provider)
		s.quotaSelections[provider] = &quotaSelectionState{
			quotaSelectionPreference: quotaSelectionPreference{Profile: profileName, Enabled: enabled},
			SelectionRef:             ref, Generation: current.Generation + 1,
		}
	}
	result, err := s.quotaSelectionsResultLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) quotaSelectionPreferencesLocked() map[string]quotaSelectionPreference {
	preferences := make(map[string]quotaSelectionPreference, 2)
	for _, provider := range []string{"claude", "codex"} {
		state := s.quotaSelections[provider]
		if state == nil {
			preferences[provider] = quotaSelectionPreference{Profile: "default", Enabled: true}
			continue
		}
		preferences[provider] = state.quotaSelectionPreference
	}
	return preferences
}

func (s *Service) resolveQuotaSelectionHomeLocked(provider, profileName string) (string, *ErrorObject) {
	home := s.claudeHome
	if provider == "codex" {
		home = s.codexHome
	}
	if s.registry != nil {
		if conflicts, err := s.registry.MigrateDesktop(s.appSupportRoot); err != nil {
			return "", sharedProfileError(err)
		} else {
			for _, conflict := range conflicts {
				if conflict.Name == profileName {
					return "", &ErrorObject{Code: "migration_conflict", Message: "select the CLI or desktop folders first"}
				}
			}
		}
		entry, err := s.registry.Load(profileName)
		if err != nil {
			return "", &ErrorObject{Code: "selection_unavailable", Message: "selected profile is unavailable"}
		}
		if provider == "claude" {
			if entry.Profile.ClaudeConfigDir != "" {
				home = entry.Profile.ClaudeConfigDir
			}
		} else if entry.Profile.CodexHome != "" {
			home = entry.Profile.CodexHome
		}
	} else if profileName != "default" {
		profilesDir, exists, err := s.profilesDirExists()
		if err != nil {
			return "", &ErrorObject{Code: "unsafe_storage", Message: "profile storage is not a safe app-owned directory"}
		}
		if !exists {
			return "", &ErrorObject{Code: "selection_unavailable", Message: "selected profile is unavailable"}
		}
		profileDir := filepath.Join(profilesDir, profileName)
		if err := s.validateStoredDir(profileDir, profilesDir); err != nil {
			return "", &ErrorObject{Code: "selection_unavailable", Message: "selected profile is unavailable"}
		}
		profilePath := filepath.Join(profileDir, "profile.json")
		if err := s.validateProfileFile(profilePath, profileDir); err != nil {
			return "", &ErrorObject{Code: "unsafe_storage", Message: "profile mapping is not a safe app-owned file"}
		}
		stored, err := profile.LoadFrom(profilePath)
		if err != nil {
			return "", &ErrorObject{Code: "selection_unavailable", Message: "selected profile is unavailable"}
		}
		if provider == "claude" {
			home = stored.ClaudeConfigDir
		} else if len(stored.Options.CodexDirs) > 0 {
			home = stored.Options.CodexDirs[0]
		} else {
			home = ""
		}
	}
	if home == "" {
		return "", nil
	}
	validated, err := s.validateHome(home)
	if err != nil {
		return "", &ErrorObject{Code: "selection_unavailable", Message: "selected provider home is unavailable"}
	}
	return validated, nil
}

func (s *Service) quotaSource(provider string) string {
	if !s.providerMode {
		return "fixture"
	}
	if provider == "claude" {
		return "claude_usage"
	}
	return "codex_app_server"
}

func (s *Service) snapshotQuotaSelection(provider, ref string, generation uint64) (selectedQuotaHome, *ErrorObject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.quotaSelections[provider]
	if state == nil || state.SelectionRef != ref || state.Generation != generation {
		return selectedQuotaHome{}, &ErrorObject{Code: "selection_mismatch", Message: "provider selection changed; read the current selection first"}
	}
	if s.registry != nil {
		if err := s.refreshSharedSelectionLocked(provider); err != nil {
			return selectedQuotaHome{}, err
		}
		if state.SelectionRef != ref || state.Generation != generation {
			return selectedQuotaHome{}, &ErrorObject{Code: "selection_mismatch", Message: "profile definition changed; read the current selection first"}
		}
	}
	home, err := s.resolveQuotaSelectionHomeLocked(provider, state.Profile)
	if err != nil {
		return selectedQuotaHome{}, err
	}
	return selectedQuotaHome{
		provider: provider, profile: state.Profile, home: home, source: s.quotaSource(provider),
		enabled: state.Enabled, ref: state.SelectionRef, gen: state.Generation,
	}, nil
}

func (s *Service) currentQuotaSelectionMatches(provider, ref string, generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.quotaSelections[provider]
	return state != nil && state.SelectionRef == ref && state.Generation == generation
}

// observeSelectedQuotaIdentity returns false after rotating the runtime identity.
func (s *Service) observeSelectedQuotaIdentity(selection selectedQuotaHome, identity string) bool {
	if identity == "" {
		return s.currentQuotaSelectionMatches(selection.provider, selection.ref, selection.gen)
	}
	s.mu.Lock()
	state := s.quotaSelections[selection.provider]
	if state == nil || state.SelectionRef != selection.ref || state.Generation != selection.gen {
		s.mu.Unlock()
		return false
	}
	if state.AuthIdentity == "" {
		state.AuthIdentity = identity
		s.mu.Unlock()
		return true
	}
	if state.AuthIdentity == identity {
		s.mu.Unlock()
		return true
	}
	if state.Generation == math.MaxUint64 {
		s.mu.Unlock()
		return false
	}
	ref, err := newSelectionReference()
	if err != nil {
		s.mu.Unlock()
		return false
	}
	s.clearQuotaCache(selection.provider)
	state.SelectionRef = ref
	state.Generation++
	state.AuthIdentity = identity
	s.mu.Unlock()
	return false
}

func (s *Service) resetQuotaSelectionsForProfileLocked(profileName string) ([]string, error) {
	providers := make([]string, 0, 2)
	preferences := s.quotaSelectionPreferencesLocked()
	newStates := make(map[string]*quotaSelectionState)
	for _, provider := range []string{"claude", "codex"} {
		state := s.quotaSelections[provider]
		if state == nil || state.Profile != profileName {
			continue
		}
		if state.Generation == math.MaxUint64 {
			return nil, errors.New("quota selection generation is unavailable")
		}
		ref, err := newSelectionReference()
		if err != nil {
			return nil, errors.New("quota selection reference could not be created")
		}
		preference := quotaSelectionPreference{Profile: "default", Enabled: state.Enabled}
		preferences[provider] = preference
		newStates[provider] = &quotaSelectionState{
			quotaSelectionPreference: preference, SelectionRef: ref, Generation: state.Generation + 1,
		}
		providers = append(providers, provider)
	}
	if len(providers) == 0 {
		return nil, nil
	}
	if err := s.saveQuotaSelectionPreferencesLocked(preferences); err != nil {
		return nil, err
	}
	for provider, state := range newStates {
		s.quotaSelections[provider] = state
	}
	return providers, nil
}

func selectionChangedError() *ErrorObject {
	return &ErrorObject{Code: "selection_changed", Message: "provider selection changed while quota was being read"}
}

func selectedQuotaResponse(result quotaResult, selection selectedQuotaHome) any {
	return struct {
		quotaResult
		SelectionRef string `json:"selection_ref"`
		Generation   uint64 `json:"generation"`
		Profile      string `json:"profile"`
		Enabled      bool   `json:"enabled"`
		Source       string `json:"source"`
	}{
		quotaResult: result, SelectionRef: selection.ref, Generation: selection.gen,
		Profile: selection.profile, Enabled: selection.enabled, Source: selection.source,
	}
}

func safeSelectionError(code string) *ErrorObject {
	message := "provider selection is unavailable"
	switch code {
	case "provider_disabled":
		message = "provider quota is disabled"
	case "request_cancelled":
		message = "provider quota refresh was cancelled"
	case "provider_not_configured":
		message = "provider home is not configured"
	case "authentication_required":
		message = "provider authorization is required"
	case "account_changed":
		message = "provider account changed; refresh required"
	case "rate_limited":
		message = "provider quota request was rate limited"
	case "invalid_provider_response":
		message = "provider returned an unsupported quota response"
	case "provider_unavailable":
		message = "provider quota is unavailable"
	}
	return &ErrorObject{Code: code, Message: message}
}
