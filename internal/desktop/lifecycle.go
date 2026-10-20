package desktop

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"cctrace/internal/profile"
)

const (
	lifecycleReset           = "reset"
	lifecycleUninstall       = "uninstall"
	lifecycleRemoveFile      = "remove_file"
	lifecycleRemoveDir       = "remove_empty_directory"
	maxLifecycleProfileBytes = 1 << 20
	maxLifecycleJSONDepth    = 32
	maxLifecycleDirEntries   = 1024
	maxLifecyclePlanEntries  = 1024
)

var errLifecycleScanLimit = errors.New("app-support scan limit exceeded")

// LifecycleChange is one exact removal shown in a lifecycle preview.
type LifecycleChange struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	SHA256 string `json:"sha256,omitempty"`
}

// LifecycleRetained explains app-support entries that cleanup leaves in place.
type LifecycleRetained struct {
	Path        string `json:"path"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

// LifecyclePlan is a reviewable, digest-bound snapshot of one app-support action.
type LifecyclePlan struct {
	Operation      string              `json:"operation"`
	AppSupportRoot string              `json:"app_support_root"`
	EraseAppData   bool                `json:"erase_app_data,omitempty"`
	Changes        []LifecycleChange   `json:"changes"`
	Retained       []LifecycleRetained `json:"retained"`
	Digest         string              `json:"plan_digest"`
}

// LifecycleApplyResult reports paths removed by an explicitly confirmed plan.
type LifecycleApplyResult struct {
	Operation string              `json:"operation"`
	Digest    string              `json:"plan_digest"`
	Removed   []string            `json:"removed"`
	Retained  []LifecycleRetained `json:"retained"`
}

type lifecyclePreparedChange struct {
	change    LifecycleChange
	parent    *os.Root
	parentRel string
	name      string
	children  []string
	info      os.FileInfo
	dir       *os.Root
}

// ResetPreview previews removal of positively owned Desktop profile records.
func (s *Service) ResetPreview() (LifecyclePlan, *ErrorObject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifecyclePreview(lifecycleReset, false)
}

// ResetApply applies the same current reset plan after explicit confirmation.
func (s *Service) ResetApply(plan LifecyclePlan, confirmed bool) (LifecycleApplyResult, *ErrorObject) {
	return s.lifecycleApply(lifecycleReset, plan, confirmed)
}

// UninstallPreview previews app-support cleanup; app data is preserved by default.
func (s *Service) UninstallPreview(eraseAppData bool) (LifecyclePlan, *ErrorObject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lifecyclePreview(lifecycleUninstall, eraseAppData)
}

// UninstallApply applies the same current uninstall plan after explicit confirmation.
func (s *Service) UninstallApply(plan LifecyclePlan, confirmed bool) (LifecycleApplyResult, *ErrorObject) {
	return s.lifecycleApply(lifecycleUninstall, plan, confirmed)
}

func (s *Service) lifecyclePreview(operation string, eraseAppData bool) (LifecyclePlan, *ErrorObject) {
	root, err := openLifecycleRoot(s.appSupportRoot)
	if err != nil {
		return LifecyclePlan{}, lifecycleError("unsafe_storage", "app-support root is not a safe directory")
	}
	defer root.Close()
	return s.buildLifecyclePlan(root, operation, eraseAppData)
}

func (s *Service) lifecycleApply(operation string, plan LifecyclePlan, confirmed bool) (LifecycleApplyResult, *ErrorObject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !confirmed {
		return LifecycleApplyResult{}, lifecycleError("confirmation_required", "the current lifecycle plan must be explicitly confirmed")
	}
	if plan.Operation != operation || plan.AppSupportRoot != s.appSupportRoot || plan.Digest == "" || lifecycleDigest(plan) != plan.Digest {
		return LifecycleApplyResult{}, lifecycleError("invalid_plan", "the lifecycle plan is invalid")
	}
	if operation == lifecycleReset && plan.EraseAppData {
		return LifecycleApplyResult{}, lifecycleError("invalid_plan", "the reset plan is invalid")
	}
	root, err := openLifecycleRoot(s.appSupportRoot)
	if err != nil {
		return LifecycleApplyResult{}, lifecycleError("unsafe_storage", "app-support root is not a safe directory")
	}
	defer root.Close()
	current, lifecycleErr := s.buildLifecyclePlan(root, operation, plan.EraseAppData)
	if lifecycleErr != nil {
		return LifecycleApplyResult{}, lifecycleErr
	}
	if current.Digest != plan.Digest {
		return LifecycleApplyResult{}, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
	}
	prepared, lifecycleErr := prepareLifecycleChanges(root, current)
	if lifecycleErr != nil {
		return LifecycleApplyResult{}, lifecycleErr
	}
	defer closePreparedLifecycleChanges(prepared)
	if err := verifyLifecycleRootPath(s.appSupportRoot, root); err != nil {
		return LifecycleApplyResult{}, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
	}
	result := LifecycleApplyResult{
		Operation: operation,
		Digest:    current.Digest,
		Removed:   []string{},
		Retained:  append([]LifecycleRetained{}, current.Retained...),
	}
	for _, item := range prepared {
		if err := verifyLifecycleRootPath(s.appSupportRoot, root); err != nil {
			return result, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
		}
		parent, err := openLifecyclePathDir(root, item.parentRel)
		if err != nil {
			return result, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
		}
		parentInfo, parentInfoErr := parent.Lstat(".")
		preparedParentInfo, preparedParentErr := item.parent.Lstat(".")
		_ = parent.Close()
		if parentInfoErr != nil || preparedParentErr != nil || !os.SameFile(parentInfo, preparedParentInfo) {
			return result, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
		}
		if item.change.Action == lifecycleRemoveFile {
			entries, entriesErr := lifecycleReadDir(item.parent)
			if entriesErr != nil || !sameLifecycleNames(lifecycleEntryNames(entries), item.children) {
				return result, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
			}
			data, info, err := lifecycleReadFile(item.parent, item.name)
			if err != nil || !os.SameFile(info, item.info) || lifecycleFileDigest(data) != item.change.SHA256 {
				return result, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
			}
			if err := item.parent.Remove(item.name); err != nil {
				return result, lifecycleError("storage_error", "owned app metadata could not be removed")
			}
		} else {
			if item.dir == nil {
				return result, lifecycleError("invalid_plan", "the lifecycle plan is invalid")
			}
			currentDir, err := openLifecycleDirectory(item.parent, item.name)
			if err != nil {
				return result, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
			}
			currentDirInfo, currentDirInfoErr := currentDir.Lstat(".")
			preparedDirInfo, preparedDirErr := item.dir.Lstat(".")
			entries, entriesErr := lifecycleReadDir(currentDir)
			_ = currentDir.Close()
			if currentDirInfoErr != nil || preparedDirErr != nil || !os.SameFile(currentDirInfo, preparedDirInfo) || entriesErr != nil || len(entries) != 0 {
				return result, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
			}
			if err := item.parent.Remove(item.name); err != nil {
				return result, lifecycleError("storage_error", "empty owned profile directory could not be removed")
			}
		}
		result.Removed = append(result.Removed, item.change.Path)
	}
	return result, nil
}

func (s *Service) buildLifecyclePlan(root *os.Root, operation string, eraseAppData bool) (LifecyclePlan, *ErrorObject) {
	plan := LifecyclePlan{
		Operation:      operation,
		AppSupportRoot: s.appSupportRoot,
		EraseAppData:   operation == lifecycleUninstall && eraseAppData,
		Changes:        []LifecycleChange{},
		Retained:       []LifecycleRetained{},
	}
	remainingEntries := maxLifecyclePlanEntries
	rootEntries, err := lifecycleReadPlanDir(root, &remainingEntries)
	if err != nil {
		return LifecyclePlan{}, lifecycleError("storage_error", "app-support contents could not be listed")
	}
	profilesPresent := false
	for _, entry := range rootEntries {
		if entry.Name() == "profiles" {
			profilesPresent = true
			continue
		}
		plan.Retained = append(plan.Retained, LifecycleRetained{
			Path: filepath.Clean(entry.Name()), Disposition: "manual-repair", Reason: "unattributed app-support entry",
		})
	}
	if profilesPresent {
		profilesRoot, err := openLifecycleDirectory(root, "profiles")
		if err != nil {
			plan.Retained = append(plan.Retained, LifecycleRetained{Path: "profiles", Disposition: "manual-repair", Reason: "profile storage is not a safe app-owned directory"})
		} else {
			profilesEntries, err := lifecycleReadPlanDir(profilesRoot, &remainingEntries)
			if err != nil {
				_ = profilesRoot.Close()
				if errors.Is(err, errLifecycleScanLimit) {
					return LifecyclePlan{}, lifecycleError("storage_error", "app-support scan exceeds the entry limit")
				}
				plan.Retained = append(plan.Retained, LifecycleRetained{Path: "profiles", Disposition: "manual-repair", Reason: "profile storage could not be listed"})
				profilesRoot = nil
			}
			if profilesRoot == nil {
				// The unreadable directory is retained; there are no safe child entries to inspect.
			} else {
				for _, entry := range profilesEntries {
					name := entry.Name()
					profilePath := filepath.Join("profiles", name, "profile.json")
					if profile.ValidateName(name) != nil {
						plan.Retained = append(plan.Retained, LifecycleRetained{Path: filepath.Join("profiles", name), Disposition: "manual-repair", Reason: "profile name is not recognized"})
						continue
					}
					profileRoot, err := openLifecycleDirectory(profilesRoot, name)
					if err != nil {
						plan.Retained = append(plan.Retained, LifecycleRetained{Path: filepath.Join("profiles", name), Disposition: "manual-repair", Reason: "profile directory is not safely attributable"})
						continue
					}
					children, err := lifecycleReadPlanDir(profileRoot, &remainingEntries)
					if err != nil {
						_ = profileRoot.Close()
						if errors.Is(err, errLifecycleScanLimit) {
							_ = profilesRoot.Close()
							return LifecyclePlan{}, lifecycleError("storage_error", "app-support scan exceeds the entry limit")
						}
						plan.Retained = append(plan.Retained, LifecycleRetained{Path: filepath.Join("profiles", name), Disposition: "manual-repair", Reason: "profile directory could not be inspected"})
						continue
					}
					data, _, err := lifecycleReadFile(profileRoot, "profile.json")
					if err != nil {
						_ = profileRoot.Close()
						plan.Retained = append(plan.Retained, LifecycleRetained{Path: profilePath, Disposition: "manual-repair", Reason: "profile metadata is not a safe app-owned file"})
						continue
					}
					var stored profile.Profile
					if !lifecycleOwnedDesktopProfile(data, s.appSupportRoot, &stored) {
						_ = profileRoot.Close()
						plan.Retained = append(plan.Retained, LifecycleRetained{Path: profilePath, Disposition: "manual-repair", Reason: "profile metadata is not a recognized Desktop mapping"})
						continue
					}
					for _, child := range children {
						if child.Name() != "profile.json" {
							plan.Retained = append(plan.Retained, LifecycleRetained{Path: filepath.Join("profiles", name, child.Name()), Disposition: "manual-repair", Reason: "unattributed profile entry"})
						}
					}
					if !hasLifecycleName(children, "profile.json") {
						_ = profileRoot.Close()
						plan.Retained = append(plan.Retained, LifecycleRetained{Path: profilePath, Disposition: "manual-repair", Reason: "profile metadata file is missing"})
						continue
					}
					if operation == lifecycleUninstall && !eraseAppData {
						plan.Retained = append(plan.Retained, LifecycleRetained{Path: profilePath, Disposition: "preserved", Reason: "erase_app_data was not requested"})
						_ = profileRoot.Close()
						continue
					}
					plan.Changes = append(plan.Changes, LifecycleChange{Path: profilePath, Action: lifecycleRemoveFile, SHA256: lifecycleFileDigest(data)})
					if len(children) == 1 && children[0].Name() == "profile.json" {
						plan.Changes = append(plan.Changes, LifecycleChange{Path: filepath.Join("profiles", name), Action: lifecycleRemoveDir})
					}
					_ = profileRoot.Close()
				}
				_ = profilesRoot.Close()
			}
		}
	}
	sort.Slice(plan.Changes, func(i, j int) bool {
		if plan.Changes[i].Action != plan.Changes[j].Action {
			return plan.Changes[i].Action == lifecycleRemoveFile
		}
		return plan.Changes[i].Path < plan.Changes[j].Path
	})
	sort.Slice(plan.Retained, func(i, j int) bool {
		if plan.Retained[i].Path != plan.Retained[j].Path {
			return plan.Retained[i].Path < plan.Retained[j].Path
		}
		if plan.Retained[i].Disposition != plan.Retained[j].Disposition {
			return plan.Retained[i].Disposition < plan.Retained[j].Disposition
		}
		return plan.Retained[i].Reason < plan.Retained[j].Reason
	})
	plan.Digest = lifecycleDigest(plan)
	return plan, nil
}

func prepareLifecycleChanges(root *os.Root, plan LifecyclePlan) ([]lifecyclePreparedChange, *ErrorObject) {
	prepared := make([]lifecyclePreparedChange, 0, len(plan.Changes))
	closeAll := func() { closePreparedLifecycleChanges(prepared) }
	for _, change := range plan.Changes {
		if !validLifecycleChangePath(change) {
			closeAll()
			return nil, lifecycleError("invalid_plan", "the lifecycle plan is invalid")
		}
		parentRel := filepath.Dir(change.Path)
		parent, err := openLifecyclePathDir(root, parentRel)
		if err != nil {
			closeAll()
			return nil, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
		}
		item := lifecyclePreparedChange{change: change, parent: parent, parentRel: parentRel, name: filepath.Base(change.Path)}
		switch change.Action {
		case lifecycleRemoveFile:
			entries, err := lifecycleReadDir(parent)
			if err != nil || !hasLifecycleName(entries, item.name) {
				_ = parent.Close()
				closeAll()
				return nil, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
			}
			data, info, err := lifecycleReadFile(parent, item.name)
			if err != nil || lifecycleFileDigest(data) != change.SHA256 {
				_ = parent.Close()
				closeAll()
				return nil, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
			}
			item.children = lifecycleEntryNames(entries)
			item.info = info
		case lifecycleRemoveDir:
			item.dir, err = openLifecycleDirectory(parent, item.name)
			if err != nil {
				_ = parent.Close()
				closeAll()
				return nil, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
			}
			entries, err := lifecycleReadDir(item.dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "profile.json" {
				_ = parent.Close()
				_ = item.dir.Close()
				closeAll()
				return nil, lifecycleError("plan_changed", "app-support changed after the preview; create a new preview")
			}
		default:
			_ = parent.Close()
			closeAll()
			return nil, lifecycleError("invalid_plan", "the lifecycle plan is invalid")
		}
		prepared = append(prepared, item)
	}
	return prepared, nil
}

func closePreparedLifecycleChanges(changes []lifecyclePreparedChange) {
	for _, change := range changes {
		if change.parent != nil {
			_ = change.parent.Close()
		}
		if change.dir != nil {
			_ = change.dir.Close()
		}
	}
}

func lifecycleEntryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	sort.Strings(names)
	return names
}

func sameLifecycleNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func hasLifecycleName(entries []os.DirEntry, want string) bool {
	for _, entry := range entries {
		if entry.Name() == want {
			return true
		}
	}
	return false
}

func openLifecycleRoot(path string) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() || privateDirectoryRequired(before) {
		return nil, errors.New("unsafe app-support root")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, errors.New("app-support root changed")
	}
	return root, nil
}

func openLifecycleDirectory(parent *os.Root, name string) (*os.Root, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return nil, errors.New("invalid app-support directory")
	}
	before, err := parent.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() || privateDirectoryRequired(before) {
		return nil, errors.New("unsafe app-support directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Lstat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = child.Close()
		return nil, errors.New("app-support directory changed")
	}
	return child, nil
}

func openLifecyclePathDir(root *os.Root, relative string) (*os.Root, error) {
	if relative == "." {
		return root.OpenRoot(".")
	}
	if !validLifecycleRelativePath(relative) {
		return nil, errors.New("invalid app-support path")
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(relative, string(os.PathSeparator)) {
		next, err := openLifecycleDirectory(current, part)
		_ = current.Close()
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func lifecycleReadDir(root *os.Root) ([]os.DirEntry, error) {
	return lifecycleReadDirLimit(root, maxLifecycleDirEntries)
}

func lifecycleReadPlanDir(root *os.Root, remaining *int) ([]os.DirEntry, error) {
	if remaining == nil || *remaining < 0 {
		return nil, errLifecycleScanLimit
	}
	limit := maxLifecycleDirEntries
	if *remaining < limit {
		limit = *remaining
	}
	entries, err := lifecycleReadDirLimit(root, limit)
	if err != nil {
		return nil, err
	}
	*remaining -= len(entries)
	return entries, nil
}

func lifecycleReadDirLimit(root *os.Root, limit int) ([]os.DirEntry, error) {
	before, err := root.Lstat(".")
	if err != nil || !before.IsDir() {
		return nil, errors.New("unsafe app-support directory")
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("app-support directory changed")
	}
	entries, err := dir.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	after, err := root.Lstat(".")
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("app-support directory changed")
	}
	if len(entries) > limit {
		return nil, errLifecycleScanLimit
	}
	return entries, nil
}

func lifecycleReadFile(root *os.Root, name string) ([]byte, os.FileInfo, error) {
	before, err := root.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || privateFileRequired(before) {
		return nil, nil, errors.New("unsafe app-support file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, nil, errors.New("app-support file changed")
	}
	if opened.Size() > maxLifecycleProfileBytes {
		return nil, nil, errors.New("app-support file exceeds the size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxLifecycleProfileBytes)+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxLifecycleProfileBytes {
		return nil, nil, errors.New("app-support file exceeds the size limit")
	}
	after, err := root.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) {
		return nil, nil, errors.New("app-support file changed")
	}
	return data, before, nil
}

func verifyLifecycleRootPath(path string, root *os.Root) error {
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() {
		return errors.New("app-support root changed")
	}
	rootInfo, err := root.Lstat(".")
	if err != nil || !os.SameFile(pathInfo, rootInfo) {
		return errors.New("app-support root changed")
	}
	return nil
}

func lifecycleMappedHomeAmbiguous(stored *profile.Profile, appSupportRoot string) bool {
	resolvedRoot, err := filepath.EvalSymlinks(appSupportRoot)
	if err != nil || !filepath.IsAbs(resolvedRoot) {
		return true
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	homes := []string{stored.ClaudeConfigDir}
	homes = append(homes, stored.Options.CodexDirs...)
	homes = append(homes, stored.Options.GjcDirs...)
	homes = append(homes, stored.Options.OmoDirs...)
	for _, home := range homes {
		if home == "" {
			continue
		}
		if !filepath.IsAbs(home) {
			return true
		}
		resolved, err := filepath.EvalSymlinks(filepath.Clean(home))
		if err != nil || !filepath.IsAbs(resolved) || pathsOverlap(filepath.Clean(resolved), resolvedRoot) {
			return true
		}
	}
	return false
}

func lifecycleOwnedDesktopProfile(data []byte, appSupportRoot string, stored *profile.Profile) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(trimmed) || !lifecycleJSONHasUniqueKeys(trimmed) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(stored); err != nil {
		return false
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil || !lifecycleHasExactJSONFields(fields, []string{"version", "user", "server", "options", "created_at", "updated_at"}, []string{"claude_config_dir"}) {
		return false
	}
	var userFields, serverFields, optionFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["user"], &userFields); err != nil || !lifecycleHasExactJSONFields(userFields, []string{"id", "name", "email", "team"}, nil) {
		return false
	}
	for _, name := range []string{"id", "name", "email", "team"} {
		if !bytes.Equal(bytes.TrimSpace(userFields[name]), []byte(`""`)) {
			return false
		}
	}
	if err := json.Unmarshal(fields["server"], &serverFields); err != nil || !lifecycleHasExactJSONFields(serverFields, []string{"protocol"}, nil) {
		return false
	}
	if !bytes.Equal(bytes.TrimSpace(serverFields["protocol"]), []byte(`"grpc"`)) {
		return false
	}
	if err := json.Unmarshal(fields["options"], &optionFields); err != nil || !lifecycleHasExactJSONFields(optionFields, []string{"sync_enabled", "metrics_export_interval", "logs_export_interval", "codex_sync_enabled", "gjc_sync_enabled", "omo_sync_enabled"}, []string{"codex_dirs"}) {
		return false
	}
	if _, ok := fields["claude_config_dir"]; ok && stored.ClaudeConfigDir == "" {
		return false
	}
	if _, ok := optionFields["codex_dirs"]; ok && len(stored.Options.CodexDirs) != 1 {
		return false
	}
	if _, ok := optionFields["codex_dirs"]; !ok && len(stored.Options.CodexDirs) != 0 {
		return false
	}
	if len(stored.Options.CodexDirs) == 1 && stored.Options.CodexDirs[0] == "" {
		return false
	}
	for _, name := range []string{"sync_enabled", "codex_sync_enabled", "gjc_sync_enabled", "omo_sync_enabled"} {
		if !lifecycleJSONFalse(optionFields[name]) {
			return false
		}
	}
	if stored.Version != 1 || stored.User != (profile.UserInfo{}) || stored.Server != (profile.ServerInfo{Protocol: "grpc"}) ||
		stored.CreatedAt.IsZero() || !stored.CreatedAt.Equal(stored.UpdatedAt) ||
		stored.Options.SyncEnabled || stored.Options.RedactUserPrompts || stored.Options.RedactToolDetails ||
		stored.Options.MetricsExportInterval != 60000 || stored.Options.LogsExportInterval != 5000 ||
		stored.Options.CodexSyncEnabled || stored.Options.CodexSyncDeclined || stored.Options.GjcSyncEnabled || stored.Options.OmoSyncEnabled ||
		len(stored.Options.CodexDirs) > 1 || len(stored.Options.GjcDirs) != 0 || len(stored.Options.OmoDirs) != 0 ||
		len(stored.Options.CollectRepositoryPrefixes) != 0 || len(stored.Options.ExcludeAccounts) != 0 {
		return false
	}
	return !lifecycleMappedHomeAmbiguous(stored, appSupportRoot)
}

func lifecycleHasExactJSONFields(fields map[string]json.RawMessage, required, optional []string) bool {
	if fields == nil {
		return false
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = struct{}{}
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return false
		}
	}
	return true
}

func lifecycleJSONFalse(value json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(value), []byte("false"))
}

func lifecycleJSONHasUniqueKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := lifecycleJSONValue(decoder, 0); err != nil {
		return false
	}
	var extra json.RawMessage
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

func lifecycleJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxLifecycleJSONDepth {
		return errors.New("profile metadata is too deeply nested")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("profile metadata object key is invalid")
			}
			if _, exists := seen[key]; exists {
				return errors.New("profile metadata contains a duplicate key")
			}
			seen[key] = struct{}{}
			if err := lifecycleJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("profile metadata object is incomplete")
		}
	case '[':
		for decoder.More() {
			if err := lifecycleJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("profile metadata array is incomplete")
		}
	default:
		return errors.New("profile metadata delimiter is invalid")
	}
	return nil
}

func validLifecycleChangePath(change LifecycleChange) bool {
	if !validLifecycleRelativePath(change.Path) {
		return false
	}
	parts := strings.Split(change.Path, string(os.PathSeparator))
	if len(parts) == 3 && parts[0] == "profiles" && parts[2] == "profile.json" {
		return profile.ValidateName(parts[1]) == nil && change.Action == lifecycleRemoveFile && change.SHA256 != ""
	}
	if len(parts) == 2 && parts[0] == "profiles" {
		return profile.ValidateName(parts[1]) == nil && change.Action == lifecycleRemoveDir && change.SHA256 == ""
	}
	return false
}

func validLifecycleRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" || filepath.Clean(path) != path {
		return false
	}
	return path == "profiles" || strings.HasPrefix(path, "profiles"+string(os.PathSeparator))
}

func privateDirectoryRequired(info os.FileInfo) bool {
	return os.PathSeparator != '\\' && info.Mode().Perm()&0o077 != 0
}

func privateFileRequired(info os.FileInfo) bool {
	return os.PathSeparator != '\\' && info.Mode().Perm()&0o077 != 0
}

func lifecycleFileDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func lifecycleDigest(plan LifecyclePlan) string {
	plan.Digest = ""
	data, _ := json.Marshal(plan)
	return lifecycleFileDigest(data)
}

func lifecycleError(code, message string) *ErrorObject {
	return &ErrorObject{Code: code, Message: message}
}
