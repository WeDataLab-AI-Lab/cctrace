package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

const (
	maxDaemonProfiles   = 128
	maxDaemonNameScan   = 4096
	maxDaemonInputFile  = 4 << 20
	maxDaemonTotalInput = 16 << 20
	collectionStalled   = 15 * time.Minute
)

type daemonStatusResult struct {
	ObservedAt string                 `json:"observed_at"`
	Complete   bool                   `json:"complete"`
	Profiles   []daemonProfileStatus  `json:"profiles"`
	Daemon     *daemonOperationStatus `json:"daemon,omitempty"`
}

type daemonProfileStatus struct {
	TransferDiagnostics *syncer.TransferDiagnostics `json:"transfer_diagnostics,omitempty"`
	ID                  string                      `json:"id"`
	Name                string                      `json:"name"`
	SyncEnabled         *bool                       `json:"sync_enabled"`
	Daemon              daemonOperationStatus       `json:"daemon"`
	Collection          daemonCollectionStatus      `json:"collection"`
	ErrorCode           *string                     `json:"error_code"`
	Job                 *daemonJobStatus            `json:"job,omitempty"`
}

type daemonJobStatus struct {
	State       string  `json:"state"`
	Generation  int     `json:"generation"`
	Sent        int64   `json:"sent"`
	LastPassAt  *string `json:"last_pass_at"`
	NextRetryAt *string `json:"next_retry_at"`
	ErrorCode   *string `json:"error_code"`
	// SharedSources is sent only to a caller that asked for it (shared_sources.go).
	SharedSources []daemonSharedSource `json:"shared_sources,omitempty"`
}

type daemonOperationStatus struct {
	State     string  `json:"state"`
	Evidence  string  `json:"evidence"`
	Version   *string `json:"version"`
	StartedAt *string `json:"started_at"`
}

type daemonCollectionStatus struct {
	State              string                   `json:"state"`
	LastStateAdvanceAt *string                  `json:"last_state_advance_at"`
	Failure            *daemonFailureStatus     `json:"failure"`
	Counters           daemonCollectionCounters `json:"counters"`
}

type daemonFailureStatus struct {
	Class         string `json:"class"`
	FirstFailedAt string `json:"first_failed_at"`
	LastFailedAt  string `json:"last_failed_at"`
	FilesFailed   int    `json:"files_failed"`
}

type daemonCollectionCounters struct {
	ErrorCode             *string `json:"error_code,omitempty"`
	KnownFiles            *int64  `json:"known_files"`
	FirstSyncSkippedBytes *int64  `json:"first_sync_skipped_bytes"`
	BodyLimitBlockedFiles *int64  `json:"body_limit_blocked_files"`
}

type daemonProfilePath struct {
	name string
	id   string
	dir  string
}

type daemonReadBudget struct {
	remaining int64
	complete  bool
}

type daemonReadFailure struct {
	code string
}

type daemonProfileConfig struct {
	Options struct {
		SyncEnabled *bool `json:"sync_enabled"`
	} `json:"options"`
}

type daemonRuntimeFile struct {
	StartedAt time.Time `json:"started_at"`
	Version   string    `json:"version"`
}

type daemonStateFile struct {
	Files            map[string]daemonFileState `json:"files"`
	TransportFailure *daemonTransportFailure    `json:"transport_failure"`
}

type daemonFileState struct {
	LastSynced         time.Time `json:"last_synced"`
	SkippedAtFirstSync int64     `json:"skipped_at_first_sync"`
	BlockedByBodyLimit bool      `json:"blocked_by_body_limit"`
}

type daemonTransportFailure struct {
	Class         string    `json:"class"`
	FirstFailedAt time.Time `json:"first_failed_at"`
	LastFailedAt  time.Time `json:"last_failed_at"`
	FilesFailed   int       `json:"files_failed"`
}

func (s *Service) readDaemonStatus() (any, *ErrorObject) {
	if s.fixtureRoot != "" {
		return s.readDaemonStatusFixture()
	}
	if s.daemonRoot == "" {
		return nil, daemonStatusUnavailable()
	}
	return s.readLiveDaemonStatus(), nil
}

func daemonStatusUnavailable() *ErrorObject {
	return &ErrorObject{Code: "feature_unavailable", Message: "daemon status is not available in this helper mode"}
}

func (s *Service) readDaemonStatusFixture() (any, *ErrorObject) {
	path := filepath.Join(s.fixtureRoot, "daemon-status.json")
	data, disposition := readDaemonFile(path, maxDaemonInputFile)
	if disposition != nil && disposition.code == "missing" {
		return nil, daemonStatusUnavailable()
	}
	if disposition != nil || len(data) == 0 || !lifecycleJSONHasUniqueKeys(data) {
		return s.unknownDaemonStatus(), nil
	}
	var result daemonStatusResult
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return s.unknownDaemonStatus(), nil
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) || !validDaemonStatusFixture(result, s.now().UTC()) {
		return s.unknownDaemonStatus(), nil
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded)+256 > maxResponseBytes {
		return s.unknownDaemonStatus(), nil
	}
	return result, nil
}

func (s *Service) unknownDaemonStatus() daemonStatusResult {
	return daemonStatusResult{ObservedAt: s.now().UTC().Format(time.RFC3339Nano), Complete: false, Profiles: []daemonProfileStatus{}}
}

func validDaemonStatusFixture(result daemonStatusResult, now time.Time) bool {
	observedAt, observedAtErr := time.Parse(time.RFC3339Nano, result.ObservedAt)
	if observedAtErr != nil || !validStatusTime(result.ObservedAt, now, true) || len(result.Profiles) == 0 || len(result.Profiles) > maxDaemonProfiles || result.Profiles[0].ID != "default" {
		return false
	}
	seen := make(map[string]struct{}, len(result.Profiles))
	previousNamed := ""
	for _, p := range result.Profiles {
		if p.TransferDiagnostics != nil && !validTransferDiagnostics(*p.TransferDiagnostics, observedAt) {
			return false
		}
		if p.ID == "default" {
			if p.Name != "default" {
				return false
			}
		} else if strings.HasPrefix(p.ID, "named:") && validNamedStatusName(strings.TrimPrefix(p.ID, "named:")) {
			name := strings.TrimPrefix(p.ID, "named:")
			if p.Name != name || previousNamed != "" && name <= previousNamed {
				return false
			}
			previousNamed = name
		} else {
			return false
		}
		if _, exists := seen[p.ID]; exists {
			return false
		}
		seen[p.ID] = struct{}{}
		if !validDaemonState(p.Daemon.State) || !validDaemonEvidence(p.Daemon.Evidence) ||
			!validCollectionState(p.Collection.State) || p.ErrorCode != nil && !validSafeErrorCode(*p.ErrorCode) {
			return false
		}
		if (p.Daemon.State == "running" && p.Daemon.Evidence != "lock_held") ||
			(p.Daemon.State == "unknown" && p.Daemon.Evidence != "insufficient") ||
			(p.Daemon.State != "running" && p.Daemon.Evidence == "lock_held") {
			return false
		}
		if p.Collection.State == "disabled" && (p.SyncEnabled == nil || *p.SyncEnabled) ||
			(p.Collection.State == "failing" || p.Collection.State == "stalled") && p.Collection.Failure == nil {
			return false
		}
		if p.Collection.Counters.KnownFiles != nil && *p.Collection.Counters.KnownFiles < 0 ||
			p.Collection.Counters.FirstSyncSkippedBytes != nil && *p.Collection.Counters.FirstSyncSkippedBytes < 0 ||
			p.Collection.Counters.BodyLimitBlockedFiles != nil && *p.Collection.Counters.BodyLimitBlockedFiles < 0 {
			return false
		}
		if p.Collection.State == "blocked" {
			blocked := p.Collection.Counters.BodyLimitBlockedFiles
			known := p.Collection.Counters.KnownFiles
			if blocked != nil && *blocked == 0 || known != nil && *known == 0 ||
				blocked != nil && known != nil && *blocked > *known {
				return false
			}
		}
		if p.Daemon.Version != nil && !safeDaemonVersion(*p.Daemon.Version) {
			return false
		}
		if p.Daemon.StartedAt != nil && !validStatusTime(*p.Daemon.StartedAt, observedAt, true) {
			return false
		}
		if p.Collection.LastStateAdvanceAt != nil && !validStatusTime(*p.Collection.LastStateAdvanceAt, observedAt, true) {
			return false
		}
		if p.Collection.Failure != nil {
			f := p.Collection.Failure
			first, firstErr := time.Parse(time.RFC3339Nano, f.FirstFailedAt)
			last, lastErr := time.Parse(time.RFC3339Nano, f.LastFailedAt)
			if (f.Class != "transport" && f.Class != "server" && f.Class != "unknown") ||
				firstErr != nil || lastErr != nil || first.After(observedAt) || last.After(observedAt) ||
				f.FilesFailed < 0 || last.Before(first) {
				return false
			}
		}
	}
	return true
}

func validStatusTime(value string, now time.Time, notFuture bool) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	_, offset := parsed.Zone()
	return err == nil && offset == 0 && (!notFuture || !parsed.After(now))
}

func validDaemonState(value string) bool {
	switch value {
	case "running", "stopped", "unknown", "disabled":
		return true
	default:
		return false
	}
}

func validDaemonEvidence(value string) bool {
	switch value {
	case "lock_held", "lock_free", "insufficient":
		return true
	default:
		return false
	}
}

func validCollectionState(value string) bool {
	switch value {
	case "unknown", "failing", "stalled", "blocked", "disabled":
		return true
	default:
		return false
	}
}

func validSafeErrorCode(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func (s *Service) readLiveDaemonStatus() daemonStatusResult {
	if result, managed := s.readSupervisorStatus(); managed {
		return result
	}
	now := s.now().UTC()
	result := daemonStatusResult{ObservedAt: now.Format(time.RFC3339Nano), Complete: true, Profiles: make([]daemonProfileStatus, 0)}
	paths, listingComplete := s.listDaemonProfiles()
	if !listingComplete {
		result.Complete = false
	}
	if len(paths) > maxDaemonProfiles {
		paths = paths[:maxDaemonProfiles]
		result.Complete = false
	}
	budget := daemonReadBudget{remaining: maxDaemonTotalInput, complete: true}
	for _, path := range paths {
		profileStatus := s.readDaemonProfile(path, now, &budget)
		if profileStatus.ErrorCode != nil {
			result.Complete = false
		}
		result.Profiles = append(result.Profiles, profileStatus)
	}
	if !budget.complete {
		result.Complete = false
	}
	return result
}

func (s *Service) listDaemonProfiles() ([]daemonProfilePath, bool) {
	paths := []daemonProfilePath{{name: "default", id: "default", dir: s.daemonRoot}}
	rootInfo, err := os.Lstat(s.daemonRoot)
	if errors.Is(err, os.ErrNotExist) {
		return paths, true
	}
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return paths, false
	}
	profilesDir := filepath.Join(s.daemonRoot, "profiles")
	profilesInfo, err := os.Lstat(profilesDir)
	if errors.Is(err, os.ErrNotExist) {
		return paths, true
	}
	if err != nil || profilesInfo.Mode()&os.ModeSymlink != 0 || !profilesInfo.IsDir() {
		return paths, false
	}
	directory, err := os.Open(profilesDir)
	if err != nil {
		return paths, false
	}
	defer directory.Close()
	openedProfiles, err := directory.Stat()
	afterProfiles, pathErr := os.Lstat(profilesDir)
	if err != nil || pathErr != nil || afterProfiles.Mode()&os.ModeSymlink != 0 || !afterProfiles.IsDir() ||
		!os.SameFile(profilesInfo, openedProfiles) || !os.SameFile(profilesInfo, afterProfiles) {
		return paths, false
	}
	entries := make([]os.DirEntry, 0, maxDaemonNameScan+1)
	for len(entries) <= maxDaemonNameScan {
		remaining := maxDaemonNameScan + 1 - len(entries)
		if remaining > 512 {
			remaining = 512
		}
		batch, readErr := directory.ReadDir(remaining)
		entries = append(entries, batch...)
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return paths, false
			}
			break
		}
		if len(batch) == 0 {
			break
		}
	}
	complete := true
	if len(entries) > maxDaemonNameScan {
		entries = entries[:maxDaemonNameScan]
		complete = false
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(profilesDir, entry.Name())
		info, statErr := os.Lstat(entryPath)
		if errors.Is(statErr, os.ErrNotExist) {
			complete = false
			continue
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			complete = false
			continue
		}
		if !info.IsDir() {
			continue
		}
		if !validNamedStatusName(entry.Name()) {
			complete = false
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		paths = append(paths, daemonProfilePath{
			name: name,
			id:   "named:" + name,
			dir:  filepath.Join(profilesDir, name),
		})
	}
	return paths, complete
}

func (s *Service) readDaemonProfile(path daemonProfilePath, now time.Time, budget *daemonReadBudget) daemonProfileStatus {
	status := daemonProfileStatus{
		ID:   path.id,
		Name: path.name,
		Daemon: daemonOperationStatus{
			State:     "unknown",
			Evidence:  "insufficient",
			Version:   nil,
			StartedAt: nil,
		},
		Collection: daemonCollectionStatus{
			State: "unknown",
			Counters: daemonCollectionCounters{
				KnownFiles:            nil,
				FirstSyncSkippedBytes: nil,
				BodyLimitBlockedFiles: nil,
			},
		},
	}
	if !s.daemonProfileDirectorySafe(path) {
		status.setError("unsafe_path")
		budget.complete = false
		return status
	}
	configValue, configCode := s.readDaemonConfig(path.dir, budget)
	if configCode != "" {
		status.setError(configCode)
	} else {
		status.SyncEnabled = configValue
	}

	runtime, runtimePresent, runtimeCode := s.readDaemonRuntime(path.dir, budget)
	if runtimeCode != "" {
		status.setError(runtimeCode)
	}
	if runtime != nil {
		if safeDaemonVersion(runtime.Version) {
			version := runtime.Version
			status.Daemon.Version = &version
		} else if runtime.Version != "" {
			status.setError("invalid_runtime")
		}
		if !runtime.StartedAt.IsZero() && !runtime.StartedAt.After(now) {
			started := runtime.StartedAt.UTC().Format(time.RFC3339Nano)
			status.Daemon.StartedAt = &started
		} else if !runtime.StartedAt.IsZero() {
			status.setError("invalid_timestamp")
		}
	}
	pidPresent, pidCode := daemonTracePresent(filepath.Join(path.dir, "sync.pid"))
	if pidCode != "" {
		status.setError(pidCode)
	}
	probe := s.daemonLockProbe
	if probe == nil {
		probe = probeDaemonLock
	}
	lockState := probe(filepath.Join(path.dir, "sync.lock"))
	switch lockState {
	case daemonLockHeld:
		status.Daemon.State = "running"
		status.Daemon.Evidence = "lock_held"
	case daemonLockFree:
		status.Daemon.Evidence = "lock_free"
		if status.SyncEnabled != nil && !*status.SyncEnabled {
			status.Daemon.State = "disabled"
		} else {
			status.Daemon.State = "stopped"
		}
	case daemonLockMissing:
		if runtimePresent || pidPresent {
			status.Daemon.State = "unknown"
		} else {
			// A never-created lock with no runtime/PID trace is the wire-level
			// negative lock evidence accepted for a stopped profile.
			status.Daemon.Evidence = "lock_free"
			if status.SyncEnabled != nil && !*status.SyncEnabled {
				status.Daemon.State = "disabled"
			} else {
				status.Daemon.State = "stopped"
			}
		}
	default:
		status.Daemon.State = "unknown"
		status.setError("lock_unknown")
	}

	states, stateResult := s.readDaemonStates(path.dir, budget, now)
	if stateResult.errorCode != "" {
		status.setError(stateResult.errorCode)
	}
	status.Collection.LastStateAdvanceAt = stateResult.lastAdvance
	status.Collection.Failure = stateResult.failure
	status.Collection.Counters = stateResult.counters
	switch {
	case status.SyncEnabled != nil && !*status.SyncEnabled:
		status.Collection.State = "disabled"
	case states && stateResult.bodyLimitBlocked:
		status.Collection.State = "blocked"
	case states && stateResult.failure != nil:
		elapsed := stateResult.failure.LastFailedAtTime().Sub(stateResult.failure.FirstFailedAtTime())
		if elapsed >= collectionStalled {
			status.Collection.State = "stalled"
		} else {
			status.Collection.State = "failing"
		}
	default:
		status.Collection.State = "unknown"
	}
	if status.ErrorCode != nil {
		budget.complete = false
	}
	return status
}

func (s *Service) daemonProfileDirectorySafe(path daemonProfilePath) bool {
	if path.id == "default" {
		if daemonDirectorySafe(s.daemonRoot) {
			return true
		}
		_, err := os.Lstat(s.daemonRoot)
		return errors.Is(err, os.ErrNotExist)
	}
	return daemonDirectorySafe(s.daemonRoot) &&
		daemonDirectorySafe(filepath.Join(s.daemonRoot, "profiles")) &&
		daemonDirectorySafe(path.dir)
}

func (p *daemonProfileStatus) setError(code string) {
	if p.ErrorCode == nil {
		p.ErrorCode = &code
	} else if *p.ErrorCode != code {
		combined := "partial_read_error"
		p.ErrorCode = &combined
	}
}

func daemonDirectorySafe(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func validNamedStatusName(name string) bool {
	return name == "default" || profile.ValidateName(name) == nil
}

func safeDaemonVersion(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i, r := range value {
		alphaNumeric := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 && !alphaNumeric || i > 0 && !alphaNumeric && !strings.ContainsRune("._+-", r) {
			return false
		}
	}
	return true
}

func daemonTracePresent(path string) (bool, string) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, ""
	}
	if err != nil {
		return true, "read_unavailable"
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true, "unsafe_path"
	}
	return true, ""
}

func (s *Service) readDaemonConfig(dir string, budget *daemonReadBudget) (*bool, string) {
	data, code := s.readInput(filepath.Join(dir, "profile.json"), budget)
	if code != "" {
		if code == "missing" {
			return nil, "profile_missing"
		}
		return nil, code
	}
	if !lifecycleJSONHasUniqueKeys(data) {
		return nil, "invalid_profile"
	}
	var config daemonProfileConfig
	if err := json.Unmarshal(data, &config); err != nil || config.Options.SyncEnabled == nil {
		return nil, "invalid_profile"
	}
	var explicit profile.Profile
	if json.Unmarshal(data, &explicit) == nil && explicit.HomesVersion != 0 {
		if explicit.ValidateHomes() != nil {
			return nil, "invalid_profile"
		}
		enabled := explicit.CollectionEnabled("claude") || explicit.CollectionEnabled("codex")
		return &enabled, ""
	}
	return config.Options.SyncEnabled, ""
}

func (s *Service) readDaemonRuntime(dir string, budget *daemonReadBudget) (*daemonRuntimeFile, bool, string) {
	data, code := s.readInput(filepath.Join(dir, "sync-runtime.json"), budget)
	if code == "missing" {
		return nil, false, ""
	}
	if code != "" {
		return nil, true, code
	}
	if !lifecycleJSONHasUniqueKeys(data) {
		return nil, true, "invalid_runtime"
	}
	var runtime daemonRuntimeFile
	if err := json.Unmarshal(data, &runtime); err != nil {
		return nil, true, "invalid_runtime"
	}
	return &runtime, true, ""
}

func (s *Service) readDaemonStates(dir string, budget *daemonReadBudget, now time.Time) (bool, daemonStateSummary) {
	stateFiles := []string{"sync-state.json", "codex-sync-state.json", "gjc-sync-state.json", "omo-sync-state.json"}
	allPresent := true
	anyPresent := false
	var knownFiles, skippedBytes, blockedFiles int64
	var latest time.Time
	var failure *daemonTransportFailure
	bodyLimitBlocked := false
	errorCode := ""
	for _, name := range stateFiles {
		data, code := s.readInput(filepath.Join(dir, name), budget)
		if code == "missing" {
			allPresent = false
			continue
		}
		anyPresent = true
		if code != "" {
			allPresent = false
			errorCode = mergeDaemonError(errorCode, code)
			continue
		}
		if !lifecycleJSONHasUniqueKeys(data) {
			allPresent = false
			errorCode = mergeDaemonError(errorCode, "invalid_state")
			continue
		}
		var state daemonStateFile
		if err := json.Unmarshal(data, &state); err != nil || state.Files == nil {
			allPresent = false
			errorCode = mergeDaemonError(errorCode, "invalid_state")
			continue
		}
		valid := true
		for _, entry := range state.Files {
			if entry.SkippedAtFirstSync < 0 || entry.LastSynced.After(now) {
				valid = false
				continue
			}
			if knownFiles == math.MaxInt64 || entry.SkippedAtFirstSync > math.MaxInt64-skippedBytes {
				valid = false
				continue
			}
			knownFiles++
			skippedBytes += entry.SkippedAtFirstSync
			if entry.BlockedByBodyLimit {
				if blockedFiles == math.MaxInt64 {
					valid = false
				} else {
					blockedFiles++
					bodyLimitBlocked = true
				}
			}
			if !entry.LastSynced.IsZero() && entry.LastSynced.After(latest) {
				latest = entry.LastSynced
			}
		}
		if !valid {
			allPresent = false
			errorCode = mergeDaemonError(errorCode, "invalid_timestamp")
		}
		if state.TransportFailure != nil {
			candidate := state.TransportFailure
			if candidate.FilesFailed < 0 || candidate.FirstFailedAt.IsZero() || candidate.LastFailedAt.IsZero() ||
				candidate.FirstFailedAt.After(now) || candidate.LastFailedAt.After(now) || candidate.LastFailedAt.Before(candidate.FirstFailedAt) {
				allPresent = false
				errorCode = mergeDaemonError(errorCode, "invalid_timestamp")
			} else if failure == nil || candidate.LastFailedAt.Sub(candidate.FirstFailedAt) > failure.LastFailedAt.Sub(failure.FirstFailedAt) {
				copy := *candidate
				failure = &copy
			}
		}
	}
	summary := daemonStateSummary{failure: safeFailure(failure), bodyLimitBlocked: bodyLimitBlocked, errorCode: errorCode}
	if !latest.IsZero() {
		formatted := latest.UTC().Format(time.RFC3339Nano)
		summary.lastAdvance = &formatted
	}
	if allPresent && anyPresent {
		known := knownFiles
		skipped := skippedBytes
		blocked := blockedFiles
		summary.counters = daemonCollectionCounters{KnownFiles: &known, FirstSyncSkippedBytes: &skipped, BodyLimitBlockedFiles: &blocked}
	}
	return anyPresent, summary
}

type daemonStateSummary struct {
	counters         daemonCollectionCounters
	lastAdvance      *string
	failure          *daemonFailureStatus
	bodyLimitBlocked bool
	errorCode        string
}

func safeFailure(failure *daemonTransportFailure) *daemonFailureStatus {
	if failure == nil {
		return nil
	}
	class := failure.Class
	if class != "transport" && class != "server" {
		class = "unknown"
	}
	return &daemonFailureStatus{
		Class:         class,
		FirstFailedAt: failure.FirstFailedAt.UTC().Format(time.RFC3339Nano),
		LastFailedAt:  failure.LastFailedAt.UTC().Format(time.RFC3339Nano),
		FilesFailed:   failure.FilesFailed,
	}
}

func (f *daemonFailureStatus) FirstFailedAtTime() time.Time {
	value, _ := time.Parse(time.RFC3339Nano, f.FirstFailedAt)
	return value
}

func (f *daemonFailureStatus) LastFailedAtTime() time.Time {
	value, _ := time.Parse(time.RFC3339Nano, f.LastFailedAt)
	return value
}

func mergeDaemonError(current, next string) string {
	if current == "" || current == next {
		return next
	}
	return "partial_read_error"
}

func (s *Service) readInput(path string, budget *daemonReadBudget) ([]byte, string) {
	limit := int64(maxDaemonInputFile)
	if budget.remaining < limit {
		limit = budget.remaining
	}
	if limit < 0 {
		limit = 0
	}
	data, failure := readDaemonFile(path, limit)
	if failure != nil {
		if failure.code != "missing" {
			budget.complete = false
		}
		return nil, failure.code
	}
	budget.remaining -= int64(len(data))
	return data, ""
}

func readDaemonFile(path string, limit int64) ([]byte, *daemonReadFailure) {
	return readDaemonFileWithHook(path, limit, nil)
}

func readDaemonFileWithHook(path string, limit int64, openedHook func(*os.File)) ([]byte, *daemonReadFailure) {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &daemonReadFailure{code: "missing"}
	}
	if err != nil {
		return nil, &daemonReadFailure{code: "read_unavailable"}
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, &daemonReadFailure{code: "unsafe_path"}
	}
	if before.Size() > maxDaemonInputFile {
		return nil, &daemonReadFailure{code: "input_too_large"}
	}
	if before.Size() > limit {
		code := "input_too_large"
		if limit < maxDaemonInputFile {
			code = "input_limit_exceeded"
		}
		return nil, &daemonReadFailure{code: code}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, &daemonReadFailure{code: "read_unavailable"}
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, &daemonReadFailure{code: "snapshot_race"}
	}
	if openedHook != nil {
		openedHook(file)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, &daemonReadFailure{code: "read_unavailable"}
	}
	if int64(len(data)) > limit {
		code := "input_too_large"
		if limit < maxDaemonInputFile {
			code = "input_limit_exceeded"
		}
		return nil, &daemonReadFailure{code: code}
	}
	afterOpen, statErr := file.Stat()
	afterPath, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !afterPath.Mode().IsRegular() || afterPath.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(before, afterOpen) || !os.SameFile(before, afterPath) ||
		before.Size() != afterOpen.Size() || before.Size() != afterPath.Size() ||
		!before.ModTime().Equal(afterOpen.ModTime()) || !before.ModTime().Equal(afterPath.ModTime()) {
		return nil, &daemonReadFailure{code: "snapshot_race"}
	}
	return data, nil
}
