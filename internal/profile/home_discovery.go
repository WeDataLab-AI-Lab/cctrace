package profile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// HomeCandidate is a provider execution home, not an additional session scan root.
type HomeCandidate struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Home     string `json:"home"`
	Excluded bool   `json:"excluded"`
	stamp    os.FileInfo
}

// SameDirectory keeps a selected candidate bound to its discovered directory.
func (candidate HomeCandidate) SameDirectory(other HomeCandidate) bool {
	return candidate.stamp != nil && other.stamp != nil && os.SameFile(candidate.stamp, other.stamp)
}

type HomeDiscoveryError struct {
	ProfileID string `json:"profile_id"`
	Code      string `json:"code"`
}

type HomeDiscovery struct {
	Complete   bool                 `json:"complete"`
	Candidates []HomeCandidate      `json:"candidates"`
	Errors     []HomeDiscoveryError `json:"errors"`
}

// RegisterHomeCandidate registers only the selected execution home. Collection
// requires a later explicit configuration step, independent of discovery.
func (r Registry) RegisterHomeCandidate(candidate HomeCandidate, collectionEnabled bool) (entry Entry, err error) {
	entries, e := r.List()
	if e != nil {
		return Entry{}, e
	}
	for _, existing := range entries {
		if existing.Profile.HomesVersion != 0 {
			return Entry{}, ErrUnsafe
		}
	}

	if collectionEnabled || candidate.stamp == nil || ValidateName(candidate.Name) != nil ||
		(candidate.Provider != "claude" && candidate.Provider != "codex") || CheckCodexHome(candidate.Home) != nil {
		return Entry{}, ErrUnsafe
	}
	registryPath, err := resolveRemovalPath(r.Root)
	if err != nil || removalWithin(candidate.Home, registryPath) || removalWithin(registryPath, candidate.Home) {
		return Entry{}, ErrUnsafe
	}
	err = withProfileLock(r.Root, func() error {
		current, err := os.Lstat(candidate.Home)
		if err != nil || !os.SameFile(candidate.stamp, current) || !providerHomeMarker(candidate.Home, candidate.Provider) {
			return ErrConflict
		}
		found, err := r.DiscoverHomes(filepath.Dir(candidate.Home))
		if err != nil {
			return err
		}
		available := false
		for _, next := range found.Candidates {
			if next.Provider == candidate.Provider && next.Home == candidate.Home && next.Name == candidate.Name {
				available = !next.Excluded
				break
			}
		}
		if !available {
			return ErrConflict
		}
		path, _ := r.Path(candidate.Name)
		if _, err := os.Lstat(filepath.Dir(path)); err == nil {
			return ErrExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		p := NewDefault()
		p.ID, p.RegistrationOnly = ProfileID(candidate.Name), true
		p.Options.SyncEnabled, p.Options.CodexSyncEnabled = false, false
		p.Options.CodexSyncDeclined = true
		p.Options.GjcSyncEnabled, p.Options.OmoSyncEnabled, p.Options.LocalAnalysisEnabled = false, false, false
		if candidate.Provider == "claude" {
			p.ClaudeConfigDir = candidate.Home
		} else {
			p.CodexHome = candidate.Home
		}
		if err := saveToUnlocked(p, path); err != nil {
			return err
		}
		entry, err = r.Load(candidate.Name)
		return err
	})
	return
}

func (r Registry) DiscoverHomes(home string) (HomeDiscovery, error) {
	result := HomeDiscovery{Complete: true, Candidates: []HomeCandidate{}, Errors: []HomeDiscoveryError{}}
	home, err := NormalizeCodexHome(home)
	if err != nil {
		return result, ErrUnsafe
	}
	exclusions, err := r.homeExclusions()
	if err != nil {
		return result, err
	}
	known, names := map[string]bool{}, map[string]bool{"default": true}
	addIssue := func(id, code string) {
		result.Complete = false
		result.Errors = append(result.Errors, HomeDiscoveryError{ProfileID: id, Code: code})
	}
	load := func(name string) {
		names[name] = true
		path, _ := r.Path(name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Size() > 1<<20 {
			addIssue(ProfileID(name), "invalid_configuration")
			return
		}
		entry, err := r.Load(name)
		if err != nil {
			addIssue(ProfileID(name), "invalid_configuration")
			return
		}
		if entry.Profile.HomesVersion != 0 || entry.Profile.Homes != nil {
			for _, h := range entry.Profile.Homes {
				known[h.Provider+"\x00"+h.Home] = true
			}
			return
		}
		for provider, stored := range map[string]string{"claude": entry.Profile.ClaudeConfigDir, "codex": entry.Profile.CodexHome} {
			if stored == "" {
				if entry.Profile.RegistrationOnly {
					continue
				}
				stored = filepath.Join(home, "."+provider)
			}
			known[provider+"\x00"+filepath.Clean(stored)] = true
			if canonical, err := NormalizeCodexHome(stored); err == nil {
				known[provider+"\x00"+canonical] = true
			}
		}
	}
	load("default")
	profilesDir := filepath.Join(r.Root, "profiles")
	if _, err := os.Lstat(profilesDir); err == nil {
		if err := r.safePath(profilesDir); err != nil {
			addIssue("unknown", "unsafe_storage")
		} else if entries, err := discoveryEntries(profilesDir, 128); err != nil {
			addIssue("unknown", "resource_limit")
		} else {
			for _, entry := range entries {
				if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || ValidateName(entry.Name()) != nil {
					addIssue("unknown", "invalid_configuration")
					continue
				}
				load(entry.Name())
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		addIssue("unknown", "unsafe_storage")
	}
	entries, err := discoveryEntries(home, 4096)
	if err != nil {
		return result, ErrUnsafe
	}
	for _, entry := range entries {
		provider := ""
		for _, candidate := range []string{"claude", "codex"} {
			if entry.Name() == "."+candidate || strings.HasPrefix(entry.Name(), "."+candidate+"-") {
				provider = candidate
				break
			}
		}
		if provider == "" || entry.Name() == ".claude-mem" || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(home, entry.Name())
		if known[provider+"\x00"+path] || len(path) > 4096 || strings.ContainsFunc(path, func(r rune) bool { return r < 32 || r == 127 }) || CheckCodexHome(path) != nil || !providerHomeMarker(path, provider) {
			continue
		}
		stamp, err := os.Lstat(path)
		if err != nil || !stamp.IsDir() || stamp.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if len(result.Candidates) == 128 {
			addIssue("unknown", "resource_limit")
			break
		}
		name := strings.TrimPrefix(entry.Name(), ".")
		if ValidateName(name) != nil {
			name = provider + "-home"
		}
		base := name
		for suffix := 2; names[name]; suffix++ {
			ending := "-" + strconv.Itoa(suffix)
			if len(base)+len(ending) > 63 {
				base = provider + "-home"
			}
			name = base + ending
		}
		names[name] = true
		result.Candidates = append(result.Candidates, HomeCandidate{Provider: provider, Name: name, Home: path, Excluded: exclusions[provider+"\x00"+path], stamp: stamp})
	}
	return result, nil
}

func discoveryEntries(path string, limit int) ([]os.DirEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > limit {
		return nil, ErrUnsafe
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func providerHomeMarker(home, provider string) bool {
	markers := []string{"settings.json", ".claude.json", ".credentials.json", "projects"}
	if provider == "codex" {
		markers = []string{"config.toml", "auth.json", "sessions"}
	}
	for _, marker := range markers {
		info, err := os.Lstat(filepath.Join(home, marker))
		if err == nil && info.Mode()&os.ModeSymlink == 0 &&
			(info.Mode().IsRegular() && marker != "projects" && marker != "sessions" ||
				info.IsDir() && (marker == "projects" || marker == "sessions")) {
			return true
		}
	}
	return false
}
