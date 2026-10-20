package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"cctrace/internal/atomicfile"
)

const homeExclusionsFile = "home-discovery-exclusions.json"
const homeExclusionsLimit = 1 << 20

func homeExclusionKey(candidate HomeCandidate) string {
	return candidate.Provider + "\x00" + candidate.Home
}

// homeExclusions never creates storage. Invalid metadata blocks discovery and
// mutations rather than silently treating the user's exclusions as empty.
func (r Registry) homeExclusions() (map[string]bool, error) {
	result := map[string]bool{}
	if _, err := os.Lstat(r.Root); errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err := r.safePath(r.Root); err != nil {
		return nil, ErrUnsafe
	}
	path := filepath.Join(r.Root, homeExclusionsFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > homeExclusionsLimit {
		return nil, ErrUnsafe
	}
	if err := r.safePath(path); err != nil {
		return nil, ErrUnsafe
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrUnsafe
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, ErrUnsafe
	}
	data, err := io.ReadAll(io.LimitReader(file, homeExclusionsLimit+1))
	if err != nil || len(data) > homeExclusionsLimit {
		return nil, ErrUnsafe
	}
	var stored map[string]json.RawMessage
	if json.Unmarshal(data, &stored) != nil || stored == nil {
		return nil, ErrUnsafe
	}
	for key, value := range stored {
		provider, home, ok := strings.Cut(key, "\x00")
		if !ok || (provider != "claude" && provider != "codex") || !filepath.IsAbs(home) || filepath.Clean(home) != home || strings.ContainsRune(home, 0) || !bytes.Equal(bytes.TrimSpace(value), []byte("true")) {
			return nil, ErrUnsafe
		}
		result[key] = true
	}
	return result, nil
}

// SetHomeExcluded records a local discovery choice, never a profile or provider
// account choice. Every writer shares the profile lock with registration.
func (r Registry) SetHomeExcluded(candidate HomeCandidate, excluded bool) error {
	if candidate.stamp == nil || CheckCodexHome(candidate.Home) != nil {
		return ErrUnsafe
	}
	registryPath, err := resolveRemovalPath(r.Root)
	if err != nil || removalWithin(candidate.Home, registryPath) || removalWithin(registryPath, candidate.Home) {
		return ErrUnsafe
	}
	return withProfileLock(r.Root, func() error {
		found, err := r.DiscoverHomes(filepath.Dir(candidate.Home))
		if err != nil {
			return err
		}
		available := false
		for _, next := range found.Candidates {
			if next.Provider == candidate.Provider && next.Name == candidate.Name && next.Home == candidate.Home && next.SameDirectory(candidate) {
				available = true
				break
			}
		}
		if !available {
			return ErrConflict
		}
		exclusions, err := r.homeExclusions()
		if err != nil {
			return err
		}
		key := homeExclusionKey(candidate)
		if excluded {
			exclusions[key] = true
		} else {
			delete(exclusions, key)
		}
		data, err := json.Marshal(exclusions)
		if err != nil {
			return err
		}
		if len(data) > homeExclusionsLimit {
			return ErrUnsafe
		}
		return atomicfile.Write(filepath.Join(r.Root, homeExclusionsFile), data, 0600)
	})
}
