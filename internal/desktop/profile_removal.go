package desktop

import (
	"errors"
	"os"
	"path/filepath"

	"cctrace/internal/profile"
)

// removeOwnedProfile requires s.mu; unknown and shared entries are retained.
func (s *Service) removeOwnedProfile(name string) (any, *ErrorObject) {
	unsafe := func() (any, *ErrorObject) {
		return nil, lifecycleError("unsafe_storage", "profile storage requires manual repair")
	}
	root, err := openLifecycleRoot(s.appSupportRoot)
	if err != nil {
		return unsafe()
	}
	defer root.Close()
	profiles, err := openLifecycleDirectory(root, "profiles")
	if err != nil {
		return unsafe()
	}
	defer profiles.Close()
	directory, err := openLifecycleDirectory(profiles, name)
	if err != nil {
		return unsafe()
	}
	defer directory.Close()
	children, err := lifecycleReadDir(directory)
	if err != nil || len(children) != 1 || children[0].Name() != "profile.json" {
		return unsafe()
	}
	data, info, err := lifecycleReadFile(directory, "profile.json")
	var stored profile.Profile
	if err != nil || !lifecycleOwnedDesktopProfile(data, s.appSupportRoot, &stored) {
		return unsafe()
	}
	verifyLocation := func() bool {
		if verifyLifecycleRootPath(s.appSupportRoot, root) != nil {
			return false
		}
		current, err := openLifecyclePathDir(root, filepath.Join("profiles", name))
		if err != nil {
			return false
		}
		defer current.Close()
		currentInfo, e1 := current.Lstat(".")
		originalInfo, e2 := directory.Lstat(".")
		currentProfiles, e3 := openLifecycleDirectory(root, "profiles")
		if e3 != nil {
			return false
		}
		defer currentProfiles.Close()
		parentInfo, e4 := currentProfiles.Lstat(".")
		originalParent, e5 := profiles.Lstat(".")
		return e1 == nil && e2 == nil && e4 == nil && e5 == nil && os.SameFile(currentInfo, originalInfo) && os.SameFile(parentInfo, originalParent)
	}
	latest, latestInfo, err := lifecycleReadFile(directory, "profile.json")
	children, childrenErr := lifecycleReadDir(directory)
	if err != nil || childrenErr != nil || len(children) != 1 || children[0].Name() != "profile.json" || !os.SameFile(info, latestInfo) || lifecycleFileDigest(data) != lifecycleFileDigest(latest) || !verifyLocation() {
		return unsafe()
	}
	if err := directory.Remove("profile.json"); err != nil {
		return nil, lifecycleError("storage_error", "profile metadata could not be removed")
	}
	partial := LifecycleApplyResult{Operation: "profiles.remove", Digest: lifecycleFileDigest(data), Removed: []string{filepath.Join("profiles", name, "profile.json")}, Retained: []LifecycleRetained{}}
	failure := func() (any, *ErrorObject) {
		return nil, &ErrorObject{Code: "storage_error", Message: "profile metadata was removed but its directory requires manual repair", PartialResult: &partial}
	}
	remaining, err := lifecycleReadDir(directory)
	if err != nil || len(remaining) != 0 || !verifyLocation() {
		return failure()
	}
	if err := profiles.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return failure()
	}
	return profileRemoveResult{Name: name, Removed: true}, nil
}
