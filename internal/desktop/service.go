package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/usage"
)

const quotaStaleAfter = 15 * time.Minute

type Config struct {
	HomeDiscoveryRoot     string
	homeDiscoveryRoot     string
	AppSupportRoot        string
	ProfileRoot           string
	FixtureRoot           string
	DaemonRoot            string
	ProviderMode          bool
	ClaudeHome            string
	CodexHome             string
	ProviderReader        ProviderQuotaReader
	accountIdentityReader func(context.Context, string, string) (string, error)
	accountReader         func(context.Context, string, string) (accountSample, error)
	Now                   func() time.Time
	daemonControlDeps     *daemonControlDependencies
}

type Service struct {
	accountStoreRevision      string
	providerConsentAfterWrite func(int) error
	recoverClaudeSession      func(context.Context, string, string) error
	recoveryFlights           map[string]*sessionRecoveryFlight
	recoveryRetry             map[string]time.Time
	homeDiscoveryRoot         string
	homeCandidates            map[string]profile.HomeCandidate
	homePlans                 map[string]homePlan
	consentPlans              map[string]consentPlan
	onboardingSnapshot        *onboardingSnapshot
	multiHomeProfiles         atomic.Bool
	accountsMu                sync.Mutex
	accounts                  *quotaAccountStore
	accountFlightMu           sync.Mutex
	quotaAccountHomes         bool // guarded by accountsMu; explicit local-UI opt-in
	accountFlights            map[string]*accountFlight
	accountSlots              chan struct{}
	accountRetry              map[string]time.Time
	accountIdentityReader     func(context.Context, string, string) (string, error)
	accountReader             func(context.Context, string, string) (accountSample, error)
	appSupportRoot            string
	registry                  *profile.Registry
	fixtureRoot               string
	daemonRoot                string
	daemonLockProbe           func(string) daemonLockState
	daemonControl             *daemonControlDependencies
	controlRootOK             bool
	controlMu                 sync.Mutex
	providerMode              bool
	claudeHome                string
	codexHome                 string
	providerReader            ProviderQuotaReader
	now                       func() time.Time
	mu                        sync.Mutex
	quotaMu                   sync.Mutex
	lastQuota                 map[string]*providerQuotaSnapshot
	codexAuthHash             map[string]string
	codexChangedAt            map[string]time.Time
	quotaSelections           map[string]*quotaSelectionState
	quotaFlightMu             sync.Mutex
	quotaFlights              map[string]*quotaReadFlight
}

func New(config Config) (*Service, error) {
	controlDeps := newDaemonControlDependencies(config.daemonControlDeps)
	defaultProfileRoot := ""
	if config.ProviderMode {
		defaultProfileRoot = controlDeps.profileDefaultDir()
	}
	daemonRoot := config.DaemonRoot
	if daemonRoot != "" {
		if !filepath.IsAbs(daemonRoot) {
			return nil, errors.New("daemon root must be an absolute path")
		}
		daemonRoot = filepath.Clean(daemonRoot)
	} else if config.ProviderMode {
		daemonRoot = defaultProfileRoot
		if !filepath.IsAbs(daemonRoot) {
			daemonRoot = ""
		}
	}
	if daemonRoot != "" && filepath.IsAbs(config.AppSupportRoot) {
		supportCompare := resolvePathForComparison(config.AppSupportRoot)
		daemonCompare := resolvePathForComparison(daemonRoot)
		if isWithin(supportCompare, daemonCompare) || isWithin(daemonCompare, supportCompare) {
			return nil, errors.New("app-support and daemon roots must be disjoint")
		}
	}
	if config.HomeDiscoveryRoot != "" {
		if config.ProviderMode || config.FixtureRoot == "" || config.ProfileRoot == "" || profile.CheckCodexHome(config.HomeDiscoveryRoot) != nil || pathsOverlap(config.HomeDiscoveryRoot, config.AppSupportRoot) || pathsOverlap(config.HomeDiscoveryRoot, config.ProfileRoot) || pathsOverlap(config.HomeDiscoveryRoot, config.FixtureRoot) {
			return nil, errors.New("fixture discovery root must be canonical, private and isolated")
		}
		config.homeDiscoveryRoot = config.HomeDiscoveryRoot
	}
	appSupportRoot, err := ensureAppSupportRoot(config.AppSupportRoot)
	if err != nil {
		return nil, err
	}
	fixtureRoot, err := resolveFixtureRoot(config.FixtureRoot)
	if err != nil {
		return nil, err
	}
	if fixtureRoot != "" && pathsOverlap(appSupportRoot, fixtureRoot) {
		return nil, errors.New("app-support and fixture roots must be disjoint")
	}
	if daemonRoot != "" && fixtureRoot != "" {
		return nil, errors.New("fixture and daemon roots cannot be combined")
	}
	if !config.ProviderMode && (config.ClaudeHome != "" || config.CodexHome != "") {
		return nil, errors.New("provider homes require explicit provider mode")
	}
	if config.ProviderMode && fixtureRoot != "" {
		return nil, errors.New("fixture and provider modes cannot be combined")
	}
	explicitRootNormal := config.DaemonRoot == "" ||
		filepath.IsAbs(config.DaemonRoot) && filepath.Clean(config.DaemonRoot) == config.DaemonRoot && config.DaemonRoot == defaultProfileRoot
	controlRootOK := config.ProviderMode && fixtureRoot == "" && explicitRootNormal &&
		filepath.IsAbs(defaultProfileRoot) && filepath.Clean(defaultProfileRoot) == defaultProfileRoot && daemonRoot == defaultProfileRoot
	now := config.Now
	if now == nil {
		now = time.Now
	}
	service := &Service{
		appSupportRoot:        appSupportRoot,
		homeDiscoveryRoot:     config.homeDiscoveryRoot,
		accountReader:         config.accountReader,
		accountIdentityReader: config.accountIdentityReader,
		fixtureRoot:           fixtureRoot,
		daemonRoot:            daemonRoot,
		daemonLockProbe:       probeDaemonLock,
		daemonControl:         controlDeps,
		controlRootOK:         controlRootOK,
		providerMode:          config.ProviderMode,
		now:                   now,
		lastQuota:             make(map[string]*providerQuotaSnapshot),
		codexAuthHash:         make(map[string]string),
		codexChangedAt:        make(map[string]time.Time),
		quotaFlights:          make(map[string]*quotaReadFlight),
	}
	if controlDeps.lockProbe != nil {
		service.daemonLockProbe = controlDeps.lockProbe
	}
	if config.ProviderMode {
		if config.ClaudeHome != "" {
			service.claudeHome, err = service.validateHome(config.ClaudeHome)
			if err != nil {
				return nil, errors.New("claude home must be an absolute existing directory outside app-support")
			}
		}
		if config.CodexHome != "" {
			service.codexHome, err = service.validateHome(config.CodexHome)
			if err != nil {
				return nil, errors.New("codex home must be an absolute existing directory outside app-support")
			}
		}
		service.providerReader = config.ProviderReader
		if service.providerReader == nil {
			service.providerReader = liveProviderQuotaReader{}
		}
	}
	registryRoot := config.ProfileRoot

	if registryRoot != "" {
		registryCompare := resolvePathForComparison(registryRoot)
		if !filepath.IsAbs(registryRoot) || isWithin(registryCompare, service.appSupportRoot) || isWithin(service.appSupportRoot, registryCompare) {
			return nil, errors.New("profile registry must be an absolute directory outside app-support")
		}
		service.registry = &profile.Registry{Root: filepath.Clean(registryRoot)}
	}
	if err := service.initializeQuotaSelections(); err != nil {
		return nil, errors.New("quota selection storage is unavailable")
	}
	return service, nil
}

func resolvePathForComparison(path string) string {
	current := filepath.Clean(path)
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func (s *Service) Handle(ctx context.Context, request Request) Response {
	if request.Version != ProtocolVersion {
		return errorResponse(validResponseID(request.ID), "unsupported_version", "protocol version is not supported")
	}
	if request.Kind != "request" {
		return errorResponse(validResponseID(request.ID), "invalid_kind", "request kind is not supported")
	}
	if !validRequestID(request.ID) {
		return errorResponse("", "invalid_id", "request id is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return errorResponse(request.ID, "request_cancelled", "request was cancelled by the helper supervisor")
	}

	var result any
	var err *ErrorObject
	switch request.Op {
	case "profiles.membership.preview":
		result, err = s.previewMembership(request.Payload)
	case "profiles.membership.apply":
		result, err = s.applyConsent(ctx, request.Payload, "membership")
	case "profiles.onboarding.recover":
		result, err = s.recoverHomeConsent(request.Payload)
	case "profiles.onboarding.discover":
		result, err = s.discoverOnboarding(request.Payload)
	case "profiles.onboarding.preview":
		result, err = s.previewOnboarding(request.Payload)
	case "profiles.onboarding.apply":
		result, err = s.applyConsent(ctx, request.Payload, "onboarding")
	case "profiles.transmission.preview":
		result, err = s.previewTransmission(request.Payload)
	case "profiles.transmission.apply":
		result, err = s.applyConsent(ctx, request.Payload, "transmission")
	case "profiles.homes.preview":
		result, err = s.previewHomes(request.Payload)
	case "profiles.homes.apply":
		result, err = s.applyHomes(ctx, request.Payload)
	case "update.trust.read":
		result, err = s.updateTrust(request.Payload)
	case "profiles.discover":
		if !emptyPayload(request.Payload) {
			err = payloadError()
		} else {
			result, err = s.discoverHomes()
		}
	case "profiles.discover.exclude":
		result, err = s.excludeDiscoveredHome(request.Payload)
	case "profiles.discover.register":
		s.mu.Lock()
		modern := s.multiHomeProfiles.Load()
		s.mu.Unlock()
		if modern {
			err = &ErrorObject{Code: "onboarding_required", Message: "New homes require the complete onboarding flow."}
		} else {
			result, err = s.registerDiscoveredHome(request.Payload)
		}
	case "quota.accounts.list":
		result, err = s.listQuotaAccounts(request.Payload)
	case "quota.account.add":
		result, err = s.addQuotaAccount(request.Payload)
	case "quota.account.set":
		result, err = s.setQuotaAccount(request.Payload)
	case "quota.account.exclusion":
		result, err = s.excludeQuotaAccount(ctx, request.Payload)
	case "quota.account.retention":
		result, err = s.retainQuotaAccount(request.Payload)
	case "quota.account.remove":
		result, err = s.removeQuotaAccount(request.Payload)
	case "quota.account.recover":
		result, err = s.recoverQuotaAccount(ctx, request.Payload)
	case "quota.account.read":
		result, err = s.readQuotaAccount(ctx, request.Payload)
	case "quota.account.cancel":
		result, err = s.cancelQuotaAccount(request.Payload)
	case "hello":
		quotaSelectionCapability := false
		daemonStatusCapability := false
		daemonDiagnosticsCapability := false
		daemonSharedSourcesCapability := false
		daemonControlRequested := false
		profileRegistryRequested := false
		quotaAccountsRequested := false
		quotaAccountHomesRequested := false
		profileDiscoveryRequested := false
		updateTrustRequested := false
		multiHomeRequested := false
		if !emptyPayload(request.Payload) {
			var fields map[string]json.RawMessage
			if decodePayload(request.Payload, &fields) != nil || len(fields) != 1 {
				err = payloadError()
				break
			}
			switch {
			case bytes.Equal(bytes.TrimSpace(fields["multi_home_profiles"]), []byte("true")):
				multiHomeRequested = true
			case bytes.Equal(bytes.TrimSpace(fields["update_trust"]), []byte("true")):
				updateTrustRequested = true
			case bytes.Equal(bytes.TrimSpace(fields["profile_discovery"]), []byte("true")):
				profileDiscoveryRequested = true
			case bytes.Equal(bytes.TrimSpace(fields["quota_account_homes"]), []byte("true")):
				quotaAccountHomesRequested = true
			case bytes.Equal(bytes.TrimSpace(fields["quota_accounts"]), []byte("true")):
				quotaAccountsRequested = true
			case bytes.Equal(bytes.TrimSpace(fields["quota_selection"]), []byte("true")):
				quotaSelectionCapability = true
			case bytes.Equal(bytes.TrimSpace(fields["daemon_diagnostics"]), []byte("true")):
				daemonDiagnosticsCapability = true
			case bytes.Equal(bytes.TrimSpace(fields["daemon_shared_sources"]), []byte("true")):
				daemonSharedSourcesCapability = true
			case bytes.Equal(bytes.TrimSpace(fields["daemon_status"]), []byte("true")):
				daemonStatusCapability = true
			case bytes.Equal(bytes.TrimSpace(fields["profile_registry"]), []byte("true")):
				profileRegistryRequested = true
			case bytes.Equal(bytes.TrimSpace(fields["daemon_control"]), []byte("true")):
				daemonControlRequested = true
			default:
				err = payloadError()
			}
			if err != nil {
				break
			}
		}
		if multiHomeRequested {
			s.mu.Lock()
			s.multiHomeProfiles.Store(true)
			s.mu.Unlock()
		}
		if quotaAccountHomesRequested {
			s.accountsMu.Lock()
			s.quotaAccountHomes = true
			s.accountsMu.Unlock()
		}
		var daemonControlCapability *bool
		if daemonControlRequested {
			available := s.daemonControlAvailable()
			daemonControlCapability = &available
		}
		result = helloResult{
			ProtocolVersion: ProtocolVersion,
			SelfUpdate:      false,
			Capabilities: helperCapabilities{
				MultiHomeProfiles:     multiHomeRequested && s.registry != nil,
				UpdateTrust:           updateTrustRequested && s.registry != nil,
				FixtureOnly:           !s.providerMode,
				Progress:              false,
				Cancellation:          false,
				QuotaSelection:        quotaSelectionCapability,
				QuotaAccounts:         quotaAccountsRequested,
				ClaudeSessionRecovery: quotaAccountsRequested && s.providerMode && usage.ClaudeSessionRecoverySupported(),
				QuotaAccountHomes:     quotaAccountHomesRequested,
				ProfileDiscovery:      profileDiscoveryRequested && s.registry != nil,
				DaemonStatus:          daemonStatusCapability,
				DaemonDiagnostics:     daemonDiagnosticsCapability,
				DaemonControl:         daemonControlCapability,
				ProfileRegistry:       profileRegistryRequested && s.registry != nil,
				DaemonSharedSources:   daemonSharedSourcesCapability,
			},
		}
	case "daemon.restart":
		result, err = s.restartDaemon(ctx, request.Payload)
	case "daemon.status.read":
		statusRequest, valid := parseDaemonStatusRequest(request.Payload)
		if !valid {
			err = payloadError()
			break
		}
		result, err = s.readDaemonStatus()
		if statusRequest.diagnostics && err == nil {
			result = s.withTransferDiagnostics(result)
		} else if value, ok := result.(daemonStatusResult); ok {
			for i := range value.Profiles {
				value.Profiles[i].TransferDiagnostics = nil
			}
			result = value
		}
		if !statusRequest.sharedSources {
			result = withoutSharedSources(result)
		}
	case "status":
		if !emptyPayload(request.Payload) {
			err = payloadError()
			break
		}
		mode := "fixture"
		if s.providerMode {
			mode = "provider"
		}
		result = statusResult{Mode: mode, Sync: syncStatus{State: "disabled"}}
	case "profiles.list":
		if !emptyPayload(request.Payload) {
			err = payloadError()
			break
		}
		result, err = s.listProfiles()
	case "profiles.add":
		s.mu.Lock()
		modern := s.multiHomeProfiles.Load()
		s.mu.Unlock()
		if modern {
			err = &ErrorObject{Code: "onboarding_required", Message: "New homes require the complete onboarding flow."}
		} else {
			result, err = s.addProfile(request.Payload)
		}
	case "profiles.update":
		result, err = s.updateSharedProfile(request.Payload)
	case "profiles.resolve":
		result, err = s.resolveSharedProfile(request.Payload)
	case "profiles.remove.preview":
		result, err = s.previewSharedProfileRemoval(request.Payload)
	case "profiles.remove":
		result, err = s.removeProfile(request.Payload)
	case "reset.preview", "reset.apply", "uninstall.preview", "uninstall.apply":
		result, err = s.handleLifecycle(request.Op, request.Payload)
	case "quota.read":
		result, err = s.readQuota(ctx, request.Payload)
	case "quota.selection.read":
		if !emptyPayload(request.Payload) {
			err = payloadError()
			break
		}
		result, err = s.quotaSelectionRead()
	case "quota.selection.set":
		result, err = s.quotaSelectionSet(request.Payload)
	case "quota.cancel":
		result, err = s.quotaCancel(request.Payload)
	case "update.check":
		if !emptyPayload(request.Payload) {
			err = payloadError()
			break
		}
		result = updateResult{Owner: "app", SelfUpdate: false, State: "delegated"}
	default:
		return errorResponse(request.ID, "unknown_operation", "operation is not supported")
	}
	if err != nil {
		return Response{Version: ProtocolVersion, Kind: "response", ID: request.ID, Error: err}
	}
	return Response{Version: ProtocolVersion, Kind: "response", ID: request.ID, Result: result}
}

type helloResult struct {
	ProtocolVersion int                `json:"protocol_version"`
	SelfUpdate      bool               `json:"self_update"`
	Capabilities    helperCapabilities `json:"capabilities"`
}

type helperCapabilities struct {
	MultiHomeProfiles     bool  `json:"multi_home_profiles,omitempty"`
	ClaudeSessionRecovery bool  `json:"claude_session_recovery,omitempty"`
	UpdateTrust           bool  `json:"update_trust,omitempty"`
	ProfileDiscovery      bool  `json:"profile_discovery,omitempty"`
	QuotaAccounts         bool  `json:"quota_accounts,omitempty"`
	QuotaAccountHomes     bool  `json:"quota_account_homes,omitempty"`
	ProfileRegistry       bool  `json:"profile_registry,omitempty"`
	FixtureOnly           bool  `json:"fixture_only"`
	Progress              bool  `json:"progress"`
	Cancellation          bool  `json:"cancellation"`
	QuotaSelection        bool  `json:"quota_selection,omitempty"`
	DaemonStatus          bool  `json:"daemon_status,omitempty"`
	DaemonDiagnostics     bool  `json:"daemon_diagnostics,omitempty"`
	DaemonControl         *bool `json:"daemon_control,omitempty"`
	DaemonSharedSources   bool  `json:"daemon_shared_sources,omitempty"`
}

type statusResult struct {
	Mode string     `json:"mode"`
	Sync syncStatus `json:"sync"`
}

type syncStatus struct {
	State string `json:"state"`
}

type updateResult struct {
	Owner      string `json:"owner"`
	SelfUpdate bool   `json:"self_update"`
	State      string `json:"state"`
}

type profileMapping struct {
	HomeCollection        *profile.HomeCollection   `json:"home_collection,omitempty"`
	HomesVersion          int                       `json:"homes_version,omitempty"`
	Homes                 *[]profile.HomeMembership `json:"homes,omitempty"`
	ID                    string                    `json:"id,omitempty"`
	Revision              string                    `json:"revision,omitempty"`
	CollectionState       string                    `json:"collection_state,omitempty"`
	ConfigurationComplete *bool                     `json:"configuration_complete,omitempty"`
	Removable             *bool                     `json:"removable,omitempty"`
	CodexDirs             []string                  `json:"codex_dirs,omitempty"`
	Name                  string                    `json:"name"`
	ClaudeHome            string                    `json:"claude_home"`
	CodexHome             string                    `json:"codex_home"`
}

type profileListResult struct {
	Profiles  []profileMapping            `json:"profiles"`
	Conflicts []profile.MigrationConflict `json:"conflicts,omitempty"`
}

type profileAddResult struct {
	Profile profileMapping `json:"profile"`
}

type profileRemoveResult struct {
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

func (s *Service) addProfile(raw json.RawMessage) (any, *ErrorObject) {
	if s.registry != nil {
		return s.addSharedProfile(raw)
	}
	var fields map[string]json.RawMessage
	if err := decodePayload(raw, &fields); err != nil || len(fields) != 3 {
		return nil, payloadError()
	}
	name, nameOK := requiredString(fields, "name")
	claudeHome, claudeOK := requiredString(fields, "claude_home")
	codexHome, codexOK := requiredString(fields, "codex_home")
	if !nameOK || !claudeOK || !codexOK {
		return nil, payloadError()
	}
	if err := profile.ValidateName(name); err != nil {
		return nil, &ErrorObject{Code: "invalid_profile_name", Message: "profile name is invalid"}
	}

	claudeHome, err := s.validateHome(claudeHome)
	if err != nil {
		return nil, &ErrorObject{Code: "invalid_home", Message: "mapped home must be an absolute existing directory outside app-support"}
	}
	codexHome, err = s.validateHome(codexHome)
	if err != nil {
		return nil, &ErrorObject{Code: "invalid_home", Message: "mapped home must be an absolute existing directory outside app-support"}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	profilesDir, err := s.profilesDir(true)
	if err != nil {
		return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile storage is not a safe app-owned directory"}
	}
	profileDir := filepath.Join(profilesDir, name)
	if info, err := os.Lstat(profileDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile storage is not a safe app-owned directory"}
		}
		return nil, &ErrorObject{Code: "profile_exists", Message: "profile already exists"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, &ErrorObject{Code: "storage_error", Message: "profile storage could not be checked"}
	}
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, &ErrorObject{Code: "profile_exists", Message: "profile already exists"}
		}
		return nil, &ErrorObject{Code: "storage_error", Message: "profile storage could not be created"}
	}
	if err := s.validateStoredDir(profileDir, profilesDir); err != nil {
		_ = os.Remove(profileDir)
		return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile storage is not a safe app-owned directory"}
	}

	stored := profile.NewDefault()
	stored.Options.SyncEnabled = false
	stored.Options.CodexSyncEnabled = false
	stored.ClaudeConfigDir = claudeHome
	if codexHome != "" {
		stored.Options.CodexDirs = []string{codexHome}
	}
	if err := profile.SaveTo(stored, filepath.Join(profileDir, "profile.json")); err != nil {
		_ = os.RemoveAll(profileDir)
		return nil, &ErrorObject{Code: "storage_error", Message: "profile mapping could not be saved"}
	}
	return profileAddResult{Profile: profileMapping{Name: name, ClaudeHome: claudeHome, CodexHome: codexHome}}, nil
}

func (s *Service) listProfiles() (any, *ErrorObject) {
	if s.registry != nil {
		return s.listSharedProfiles()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	profilesDir, exists, err := s.profilesDirExists()
	if err != nil {
		return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile storage is not a safe app-owned directory"}
	}
	profiles := make([]profileMapping, 0)
	if !exists {
		return profileListResult{Profiles: profiles}, nil
	}
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return nil, &ErrorObject{Code: "storage_error", Message: "profile storage could not be listed"}
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile storage contains an unsafe link"}
		}
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if err := profile.ValidateName(name); err != nil {
			return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile storage contains an invalid entry"}
		}
		profileDir := filepath.Join(profilesDir, name)
		if err := s.validateStoredDir(profileDir, profilesDir); err != nil {
			return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile storage contains an unsafe directory"}
		}
		profilePath := filepath.Join(profileDir, "profile.json")
		if err := s.validateProfileFile(profilePath, profileDir); err != nil {
			return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile mapping is not a safe app-owned file"}
		}
		stored, err := profile.LoadFrom(profilePath)
		if err != nil {
			return nil, &ErrorObject{Code: "storage_error", Message: "profile mapping could not be read"}
		}
		profiles = append(profiles, mappingFromProfile(name, stored))
	}
	return profileListResult{Profiles: profiles}, nil
}

func mappingFromProfile(name string, stored *profile.Profile) profileMapping {
	codexHome := ""
	if len(stored.Options.CodexDirs) > 0 {
		codexHome = stored.Options.CodexDirs[0]
	}
	return profileMapping{Name: name, ClaudeHome: stored.ClaudeConfigDir, CodexHome: codexHome}
}

func (s *Service) removeProfile(raw json.RawMessage) (any, *ErrorObject) {
	if s.registry != nil {
		return s.removeSharedProfile(raw)
	}
	var payload struct {
		Name string `json:"name"`
	}
	if err := decodePayload(raw, &payload); err != nil || payload.Name == "" {
		return nil, payloadError()
	}
	if err := profile.ValidateName(payload.Name); err != nil {
		return nil, &ErrorObject{Code: "invalid_profile_name", Message: "profile name is invalid"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	profilesDir, exists, err := s.profilesDirExists()
	if err != nil {
		return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile storage is not a safe app-owned directory"}
	}
	if !exists {
		return nil, &ErrorObject{Code: "profile_not_found", Message: "profile does not exist"}
	}
	profileDir := filepath.Join(profilesDir, payload.Name)
	if err := s.validateStoredDir(profileDir, profilesDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &ErrorObject{Code: "profile_not_found", Message: "profile does not exist"}
		}
		return nil, &ErrorObject{Code: "unsafe_storage", Message: "profile storage is not a safe app-owned directory"}
	}
	result, removeErr := s.removeOwnedProfile(payload.Name)
	if removeErr != nil {
		return result, removeErr
	}
	providers, err := s.resetQuotaSelectionsForProfileLocked(payload.Name)
	if err != nil {
		return result, &ErrorObject{Code: "storage_error", Message: "profile was removed but quota selection could not be reset"}
	}
	for _, provider := range providers {
		s.clearQuotaCache(provider)
		s.cancelQuotaProviderFlight(provider)
	}
	return result, nil
}

func (s *Service) handleLifecycle(op string, raw json.RawMessage) (any, *ErrorObject) {
	if !lifecycleJSONHasUniqueKeys(raw) {
		return nil, payloadError()
	}
	var fields map[string]json.RawMessage
	if decodePayload(raw, &fields) != nil {
		return nil, payloadError()
	}
	if op == "reset.preview" {
		if len(fields) != 0 {
			return nil, payloadError()
		}
		return s.ResetPreview()
	}
	if op == "uninstall.preview" {
		if !lifecycleHasExactJSONFields(fields, nil, []string{"erase_app_data"}) {
			return nil, payloadError()
		}
		erase := false
		if value, present := fields["erase_app_data"]; present {
			if !bytes.Equal(bytes.TrimSpace(value), []byte("true")) && !lifecycleJSONFalse(value) {
				return nil, payloadError()
			}
			erase = bytes.Equal(bytes.TrimSpace(value), []byte("true"))
		}
		return s.UninstallPreview(erase)
	}
	if !lifecycleHasExactJSONFields(fields, []string{"plan", "confirmed"}, nil) {
		return nil, payloadError()
	}
	confirmedRaw := bytes.TrimSpace(fields["confirmed"])
	if !bytes.Equal(confirmedRaw, []byte("true")) && !bytes.Equal(confirmedRaw, []byte("false")) {
		return nil, payloadError()
	}
	var planFields map[string]json.RawMessage
	if decodePayload(fields["plan"], &planFields) != nil || !lifecycleHasExactJSONFields(planFields, []string{"operation", "app_support_root", "changes", "retained", "plan_digest"}, []string{"erase_app_data"}) {
		return nil, payloadError()
	}
	for _, name := range []string{"operation", "app_support_root", "plan_digest"} {
		if _, ok := requiredString(planFields, name); !ok {
			return nil, payloadError()
		}
	}
	for _, name := range []string{"changes", "retained"} {
		value := bytes.TrimSpace(planFields[name])
		if len(value) == 0 || value[0] != '[' {
			return nil, payloadError()
		}
	}
	if value, present := planFields["erase_app_data"]; present && !bytes.Equal(bytes.TrimSpace(value), []byte("true")) && !lifecycleJSONFalse(value) {
		return nil, payloadError()
	}
	var plan LifecyclePlan
	if decodePayload(fields["plan"], &plan) != nil {
		return nil, payloadError()
	}
	var applied LifecycleApplyResult
	var err *ErrorObject
	confirmed := bytes.Equal(confirmedRaw, []byte("true"))
	if op == "reset.apply" {
		applied, err = s.ResetApply(plan, confirmed)
	} else {
		applied, err = s.UninstallApply(plan, confirmed)
	}
	if err != nil && len(applied.Removed) > 0 {
		err.PartialResult = &applied
	}
	return applied, err
}

func (s *Service) validateHome(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("home path is not absolute")
	}
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return "", errors.New("home path is not a directory")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || !filepath.IsAbs(resolved) {
		return "", errors.New("home path cannot be resolved")
	}
	resolved = filepath.Clean(resolved)
	if s.fixtureRoot != "" && s.homeDiscoveryRoot != "" && !isWithin(s.homeDiscoveryRoot, resolved) {
		return "", errors.New("home escaped explicit fixture discovery root")
	}
	if pathsOverlap(resolved, s.appSupportRoot) {
		return "", errors.New("home and app-support overlap")
	}
	return resolved, nil
}

func (s *Service) readQuota(ctx context.Context, raw json.RawMessage) (any, *ErrorObject) {
	var fields map[string]json.RawMessage
	if err := decodePayload(raw, &fields); err != nil {
		return nil, payloadError()
	}
	provider, providerOK := requiredString(fields, "provider")
	if !providerOK || (provider != "claude" && provider != "codex") {
		return nil, payloadError()
	}
	if len(fields) == 3 {
		ref, refOK := requiredString(fields, "selection_ref")
		var generation uint64
		generationOK := json.Unmarshal(fields["generation"], &generation) == nil && generation > 0
		if !refOK || ref == "" || !generationOK {
			return nil, payloadError()
		}
		return s.readSelectedQuota(ctx, provider, ref, generation)
	}
	if len(fields) != 1 {
		return nil, payloadError()
	}
	if s.providerMode {
		return s.readProviderQuota(ctx, provider), nil
	}
	return s.readFixtureQuota(provider), nil
}

func (s *Service) readFixtureQuota(provider string) quotaResult {
	if s.fixtureRoot == "" {
		return quotaUnavailable(provider, "fixture_unavailable", "quota fixture is not configured")
	}
	data, err := os.ReadFile(filepath.Join(s.fixtureRoot, "quota.json"))
	if err != nil {
		return quotaUnavailable(provider, "fixture_unavailable", "quota fixture is unavailable")
	}
	var fixture quotaFixture
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		return quotaUnavailable(provider, "invalid_fixture", "quota fixture is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return quotaUnavailable(provider, "invalid_fixture", "quota fixture is invalid")
	}
	if len(fixture.Claude) == 0 || len(fixture.Codex) == 0 {
		return quotaUnavailable(provider, "invalid_fixture", "quota fixture is invalid")
	}
	selectedJSON := fixture.Claude
	if provider == "codex" {
		selectedJSON = fixture.Codex
	}
	var selected quotaProviderFixture
	selectedDecoder := json.NewDecoder(bytes.NewReader(selectedJSON))
	selectedDecoder.DisallowUnknownFields()
	if err := selectedDecoder.Decode(&selected); err != nil {
		return quotaUnavailable(provider, "invalid_fixture", "quota fixture is invalid")
	}
	if !validQuotaFixture(&selected) {
		return quotaUnavailable(provider, "invalid_fixture", "quota fixture is invalid")
	}
	var trailingSelected any
	if err := selectedDecoder.Decode(&trailingSelected); !errors.Is(err, io.EOF) {
		return quotaUnavailable(provider, "invalid_fixture", "quota fixture is invalid")
	}
	return s.quotaResult(provider, &selected)
}

type quotaFixture struct {
	Claude json.RawMessage `json:"claude"`
	Codex  json.RawMessage `json:"codex"`
}

type quotaProviderFixture struct {
	State         string        `json:"state"`
	LastSuccessAt *string       `json:"last_success_at"`
	Windows       []quotaWindow `json:"windows"`
	Error         *ErrorObject  `json:"error"`
}

type quotaWindow struct {
	Name        string  `json:"name"`
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type quotaResult struct {
	Provider      string        `json:"provider"`
	State         string        `json:"state"`
	LastSuccessAt *string       `json:"last_success_at"`
	Windows       []quotaWindow `json:"windows"`
	Error         *ErrorObject  `json:"error"`
}

func validQuotaFixture(fixture *quotaProviderFixture) bool {
	if fixture.State != "fresh" && fixture.State != "stale" && fixture.State != "unavailable" {
		return false
	}
	if fixture.Error != nil {
		if fixture.Error.Code != "authentication_required" && fixture.Error.Code != "rate_limited" {
			return false
		}
	}
	if fixture.LastSuccessAt == nil {
		return len(fixture.Windows) == 0 && fixture.Error != nil
	}
	if len(fixture.Windows) == 0 {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, *fixture.LastSuccessAt); err != nil {
		return false
	}
	for _, window := range fixture.Windows {
		if (window.Name != "five_hour" && window.Name != "seven_day") ||
			math.IsNaN(window.Utilization) || math.IsInf(window.Utilization, 0) || window.Utilization < 0 || window.Utilization > 100 {
			return false
		}
		if _, err := time.Parse(time.RFC3339Nano, window.ResetsAt); err != nil {
			return false
		}
	}
	return true
}

func (s *Service) quotaResult(provider string, fixture *quotaProviderFixture) quotaResult {
	if fixture.LastSuccessAt == nil {
		return quotaUnavailable(provider, fixture.Error.Code, safeQuotaErrorMessage(fixture.Error.Code))
	}
	sampledAt, _ := time.Parse(time.RFC3339Nano, *fixture.LastSuccessAt)
	now := s.now().UTC()
	if sampledAt.After(now) {
		return quotaUnavailable(provider, "invalid_fixture", "quota fixture is invalid")
	}
	state := "fresh"
	if fixture.State != "fresh" || fixture.Error != nil || now.Sub(sampledAt) > quotaStaleAfter {
		state = "stale"
	}
	lastSuccessAt := sampledAt.UTC().Format(time.RFC3339Nano)
	var errObject *ErrorObject
	if fixture.Error != nil {
		errObject = &ErrorObject{Code: fixture.Error.Code, Message: safeQuotaErrorMessage(fixture.Error.Code)}
	} else if fixture.State == "unavailable" {
		errObject = &ErrorObject{Code: "provider_unavailable", Message: safeQuotaErrorMessage("provider_unavailable")}
	}
	windows := append([]quotaWindow{}, fixture.Windows...)
	return quotaResult{Provider: provider, State: state, LastSuccessAt: &lastSuccessAt, Windows: windows, Error: errObject}
}

func quotaUnavailable(provider, code, message string) quotaResult {
	return quotaResult{
		Provider: provider,
		State:    "unavailable",
		Windows:  []quotaWindow{},
		Error:    &ErrorObject{Code: code, Message: message},
	}
}

func safeQuotaErrorMessage(code string) string {
	switch code {
	case "authentication_required":
		return "provider authorization is required"
	case "rate_limited":
		return "provider quota request was rate limited"
	case "provider_unavailable":
		return "provider quota is unavailable"
	default:
		return "quota fixture is invalid"
	}
}

func emptyPayload(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if decodePayload(raw, &fields) != nil {
		return false
	}
	return len(fields) == 0
}

func requiredString(fields map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := fields[key]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func payloadError() *ErrorObject {
	return &ErrorObject{Code: "invalid_payload", Message: "operation payload is invalid"}
}

func validResponseID(id string) string {
	if validRequestID(id) {
		return id
	}
	return ""
}

func ensureAppSupportRoot(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("app-support must be an absolute path")
	}
	clean := filepath.Clean(path)
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return "", errors.New("app-support could not be created")
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("app-support must be a real directory")
	}
	if os.PathSeparator != '\\' && info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(clean, 0o700); err != nil {
			return "", errors.New("app-support permissions must be private")
		}
		info, err = os.Lstat(clean)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return "", errors.New("app-support permissions must be private")
		}
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || !filepath.IsAbs(resolved) {
		return "", errors.New("app-support could not be resolved")
	}
	return filepath.Clean(resolved), nil
}

func resolveFixtureRoot(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("fixture-root must be an absolute path")
	}
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("fixture-root must be an existing directory")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || !filepath.IsAbs(resolved) {
		return "", errors.New("fixture-root could not be resolved")
	}
	return filepath.Clean(resolved), nil
}

func (s *Service) profilesDir(create bool) (string, error) {
	profilesDir := filepath.Join(s.appSupportRoot, "profiles")
	info, err := os.Lstat(profilesDir)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.Mkdir(profilesDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, err = os.Lstat(profilesDir)
	}
	if errors.Is(err, os.ErrNotExist) && !create {
		return profilesDir, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("unsafe profiles directory")
	}
	if err := s.validateStoredDir(profilesDir, s.appSupportRoot); err != nil {
		return "", err
	}
	return profilesDir, nil
}

func (s *Service) profilesDirExists() (string, bool, error) {
	dir, err := s.profilesDir(false)
	if errors.Is(err, os.ErrNotExist) {
		return dir, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return dir, false, nil
	} else if err != nil {
		return "", false, err
	}
	return dir, true, nil
}

func (s *Service) validateStoredDir(path, parent string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("unsafe directory")
	}
	if os.PathSeparator != '\\' && info.Mode().Perm()&0o077 != 0 {
		return errors.New("directory permissions are not private")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !isWithin(parent, resolved) {
		return errors.New("directory escaped app-support")
	}
	return nil
}

func (s *Service) validateProfileFile(path, profileDir string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("unsafe profile file")
	}
	if os.PathSeparator != '\\' && info.Mode().Perm()&0o077 != 0 {
		return errors.New("profile file permissions are not private")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !isWithin(profileDir, resolved) {
		return errors.New("profile file escaped app-support")
	}
	return nil
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func pathsOverlap(a, b string) bool {
	if isWithin(a, b) || isWithin(b, a) {
		return true
	}
	// EvalSymlinks preserves case aliases on case-insensitive filesystems.
	// Compare physical ancestry as well; an unreadable ancestry is unsafe.
	return physicalAncestor(a, b) || physicalAncestor(b, a)
}

func physicalAncestor(ancestor, path string) bool {
	anchor, err := os.Stat(ancestor)
	if err != nil || !anchor.IsDir() {
		return true
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err != nil || !info.IsDir() || os.SameFile(anchor, info) {
			return true
		}
		if filepath.Dir(current) == current {
			return false
		}
	}
}
