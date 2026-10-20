package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"cctrace/internal/atomicfile"
)

// HomeUpdate changes consent in place; a missing revision creates only an
// explicitly requested disabled registration, never a collecting profile.
type HomeUpdate struct {
	DirectoryIdentities map[string]string `json:"-"`
	LegacyClaudeHome    string            `json:"-"`
	DiscoveryHomes      []HomeMembership  `json:"-"`
	Retire              bool              `json:"retire,omitempty"`
	Name                string            `json:"name"`
	Revision            string            `json:"revision"`
	Homes               []HomeMembership  `json:"homes"`
	Definition          *Profile          `json:"-"`
	ExcludeAccounts     *[]string         `json:"-"`
}

type retiredHomeProfile struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Revision  string `json:"revision"`
}

type homeRecovery struct {
	RetiredProfiles  []retiredHomeProfile `json:"retired_profiles,omitempty"`
	DiscoveryChanged bool                 `json:"discovery_changed,omitempty"`
	DiscoveryBefore  string               `json:"discovery_before,omitempty"`
	DiscoveryAfter   string               `json:"discovery_after,omitempty"`
	Version          int                  `json:"version"`
	Directory        string               `json:"directory"`
	Files            []homeRecoveryFile   `json:"files"`
}
type homeRecoveryFile struct {
	Retire  bool   `json:"retire,omitempty"`
	Name    string `json:"name"`
	Existed bool   `json:"existed"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

func (r Registry) HomeTransactionPending() bool {
	_, err := os.Lstat(filepath.Join(r.Root, ".homes-transaction.json"))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// ApplyHomeUpdates preflights the entire CAS set before any definition changes.
// Durable originals and a pending barrier allow explicit recovery after process
// interruption. Readers must fail closed while that barrier exists.
func (r Registry) ApplyHomeUpdates(updates []HomeUpdate, migration bool) ([]Entry, error) {
	return r.ApplyHomeUpdatesWithFinalizer(updates, migration, nil)
}
func (r Registry) ApplyHomeUpdatesWithFinalizer(updates []HomeUpdate, migration bool, finalize func(string) error) (entries []Entry, err error) {
	return r.ApplyHomeUpdatesWithCensus(updates, migration, nil, finalize)
}
func (r Registry) ApplyHomeUpdatesWithCensus(updates []HomeUpdate, migration bool, census map[string]string, finalize func(string) error) (entries []Entry, err error) {
	err = withProfileLock(r.Root, func() error {
		if r.HomeTransactionPending() || len(updates) == 0 || len(updates) > 256 {
			return ErrUnsafe
		}
		if census != nil {
			current, e := r.List()
			if e != nil {
				return e
			}
			if len(current) != len(census) {
				return ErrConflict
			}
			for _, entry := range current {
				if census[entry.Name] != entry.Revision {
					return ErrConflict
				}
			}
		}
		originals := make([][]byte, len(updates))
		desired := make([][]byte, len(updates))
		seen := map[string]bool{}
		for i, u := range updates {
			if seen[u.Name] {
				return ErrUnsafe
			}
			seen[u.Name] = true
			path, e := r.Path(u.Name)
			if e != nil {
				return e
			}
			var p *Profile
			old, e := r.Load(u.Name)
			switch {
			case e == nil:
				if u.Revision == "" || old.Revision != u.Revision {
					return ErrConflict
				}
				p = old.Profile
				originals[i] = slices.Clone(p.sourceJSON)
			case errors.Is(e, os.ErrNotExist) && u.Revision == "":
				p = NewDefault()
				p.ID = ProfileID(u.Name)
				p.RegistrationOnly = true
				p.Options.SyncEnabled = false
				p.Options.CodexSyncEnabled = false
				p.Options.CodexSyncDeclined = true
			default:
				return e
			}
			if u.Retire && (u.Name == "default" || u.Name == "personal" || originals[i] == nil || len(u.Homes) > 0) {
				return ErrUnsafe
			}
			if u.Definition != nil {
				replacement := *u.Definition
				replacement.PreserveBaseline(p)
				p = &replacement
			}
			if u.ExcludeAccounts != nil {
				p.Options.ExcludeAccounts = slices.Clone(*u.ExcludeAccounts)
			}
			if u.LegacyClaudeHome != "" {
				p.ClaudeConfigDir = u.LegacyClaudeHome
			}
			p.HomesVersion = 1
			p.Homes = slices.Clone(u.Homes)
			p.FenceLegacyCollector()
			if e = p.ValidateHomes(); e != nil {
				return e
			}
			for _, h := range p.Homes {
				if expected, ok := u.DirectoryIdentities[h.Home]; ok {
					actual, e := DirectoryIdentity(h.Home)
					if e != nil || actual != expected {
						return ErrConflict
					}
				}
				if CheckCodexHome(h.Home) != nil || removalWithin(r.Root, h.Home) || removalWithin(h.Home, r.Root) {
					return ErrUnsafe
				}
			}
			if e = ensureProfileParent(path); e != nil {
				return e
			}
			desired[i], e = marshalPreservingUnknown(p)
			if e != nil {
				return e
			}
		}
		keyData, _ := json.Marshal(updates)
		desiredData, _ := json.Marshal(desired)
		key := revision(append(keyData, desiredData...))
		directory := filepath.Join(r.Root, "home-recovery", key)
		if e := os.MkdirAll(directory, 0700); e != nil {
			return e
		}
		receipt := homeRecovery{Version: 1, Directory: key, Files: []homeRecoveryFile{}}
		if migration {
			marker := filepath.Join(r.Root, ".homes-migration-complete.json")
			if info, e := os.Lstat(marker); e == nil {
				if info.Size() > 1<<20 {
					return ErrUnsafe
				}
				if e = r.safePath(marker); e != nil {
					return e
				}
				data, e := os.ReadFile(marker)
				if e != nil {
					return e
				}
				var prior homeRecovery
				if json.Unmarshal(data, &prior) != nil || prior.Version != 1 {
					return ErrUnsafe
				}
				seenRetired := map[string]bool{}
				for _, f := range prior.Files {
					if f.Retire {
						receipt.RetiredProfiles = append(receipt.RetiredProfiles, retiredHomeProfile{Name: f.Name, Directory: prior.Directory, Revision: f.Before})
						seenRetired[f.Name] = true
					}
				}
				for _, f := range prior.RetiredProfiles {
					if !seenRetired[f.Name] {
						receipt.RetiredProfiles = append(receipt.RetiredProfiles, f)
						seenRetired[f.Name] = true
					}
				}
				if len(receipt.RetiredProfiles) > 256 {
					return ErrUnsafe
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				return e
			}
		}
		for i, u := range updates {
			if originals[i] != nil {
				if e := atomicfile.Write(filepath.Join(directory, fmt.Sprintf("%d.json", i)), originals[i], 0600); e != nil {
					return e
				}
			}
			receipt.Files = append(receipt.Files, homeRecoveryFile{Retire: u.Retire, Name: u.Name, Existed: originals[i] != nil, Before: revision(originals[i]), After: revision(desired[i])})
		}
		var discoveryAfter []byte
		if migration {
			exclusions, e := r.homeExclusions()
			if e != nil {
				return e
			}
			discoveryPath := filepath.Join(r.Root, homeExclusionsFile)
			before, e := os.ReadFile(discoveryPath)
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
			changed := false
			for _, u := range updates {
				for _, h := range u.DiscoveryHomes {
					key := h.Provider + "\x00" + h.Home
					if exclusions[key] {
						delete(exclusions, key)
						changed = true
					}
				}
			}
			if changed {
				discoveryAfter, _ = json.Marshal(exclusions)
				receipt.DiscoveryChanged = true
				receipt.DiscoveryBefore = revision(before)
				receipt.DiscoveryAfter = revision(discoveryAfter)
				if e = atomicfile.Write(filepath.Join(directory, "discovery-original.json"), before, 0600); e != nil {
					return e
				}
			}
		}
		record, _ := json.Marshal(receipt)
		if e := atomicfile.Write(filepath.Join(directory, "manifest.json"), record, 0600); e != nil {
			return e
		}
		pending := filepath.Join(r.Root, ".homes-transaction.json")
		if e := atomicfile.Write(pending, record, 0600); e != nil {
			return e
		}
		for i, u := range updates {
			path, _ := r.Path(u.Name)
			current, e := os.ReadFile(path)
			if (originals[i] != nil && (e != nil || revision(current) != u.Revision)) || (originals[i] == nil && !errors.Is(e, os.ErrNotExist)) {
				return ErrConflict
			}
			if u.Retire {
				archive := filepath.Join(directory, "retired", u.Name)
				if e = os.MkdirAll(filepath.Dir(archive), 0700); e != nil {
					return e
				}
				if e = os.Rename(filepath.Dir(path), archive); e != nil {
					return e
				}
				continue
			}
			if e = atomicfile.Write(path, desired[i], 0600); e != nil {
				return e
			}
		}
		if receipt.DiscoveryChanged {
			path := filepath.Join(r.Root, homeExclusionsFile)
			current, e := os.ReadFile(path)
			if e != nil || revision(current) != receipt.DiscoveryBefore {
				return ErrConflict
			}
			if e = atomicfile.Write(path, discoveryAfter, 0600); e != nil {
				return e
			}
		}
		if finalize != nil {
			if e := finalize(directory); e != nil {
				return e
			}
		}
		if migration {
			if e := atomicfile.Write(filepath.Join(r.Root, ".homes-migration-complete.json"), record, 0600); e != nil {
				return e
			}
		}
		if e := os.Remove(pending); e != nil {
			return e
		}
		for _, u := range updates {
			if u.Retire {
				continue
			}
			entry, e := r.Load(u.Name)
			if e != nil {
				return e
			}
			entries = append(entries, entry)
		}
		return nil
	})
	return
}

// RecoverHomeTransaction restores only definitions still matching the recorded
// before/after hashes. Unrelated writes are never overwritten by recovery.
func (r Registry) RecoverHomeTransaction() error { return r.RecoverHomeTransactionWithFinalizer(nil) }
func (r Registry) RecoverHomeTransactionWithFinalizer(finalize func(string) error) error {
	return withProfileLock(r.Root, func() error {
		pending := filepath.Join(r.Root, ".homes-transaction.json")
		if e := r.safePath(pending); e != nil {
			return e
		}
		data, e := os.ReadFile(pending)
		if e != nil {
			return e
		}
		var record homeRecovery
		if json.Unmarshal(data, &record) != nil || record.Version != 1 || len(record.Directory) != 64 || len(record.Files) > 256 {
			return ErrUnsafe
		}
		for _, c := range record.Directory {
			if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
				return ErrUnsafe
			}
		}
		originals := make([][]byte, len(record.Files))
		for i, f := range record.Files {
			path, e := r.Path(f.Name)
			if e != nil {
				return e
			}
			current, e := os.ReadFile(path)
			if f.Retire && errors.Is(e, os.ErrNotExist) {
				archive := filepath.Join(r.Root, "home-recovery", record.Directory, "retired", f.Name)
				if e = r.safePath(filepath.Join(archive, "profile.json")); e != nil {
					return e
				}
				current, e = os.ReadFile(filepath.Join(archive, "profile.json"))
			}
			if e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
			if e == nil && revision(current) != f.Before && revision(current) != f.After {
				return ErrConflict
			}
			if f.Existed {
				backup := filepath.Join(r.Root, "home-recovery", record.Directory, fmt.Sprintf("%d.json", i))
				if e := r.safePath(backup); e != nil {
					return e
				}
				originals[i], e = os.ReadFile(backup)
				if e != nil || revision(originals[i]) != f.Before {
					return ErrUnsafe
				}
			}
		}
		if record.DiscoveryChanged {
			current, e := os.ReadFile(filepath.Join(r.Root, homeExclusionsFile))
			if e != nil {
				return e
			}
			if revision(current) != record.DiscoveryBefore && revision(current) != record.DiscoveryAfter {
				return ErrConflict
			}
		}
		if finalize != nil {
			if e := finalize(filepath.Join(r.Root, "home-recovery", record.Directory)); e != nil {
				return e
			}
		}
		for i, f := range record.Files {
			path, _ := r.Path(f.Name)
			if f.Retire {
				archive := filepath.Join(r.Root, "home-recovery", record.Directory, "retired", f.Name)
				if _, e := os.Stat(archive); e == nil {
					if _, e = os.Lstat(filepath.Dir(path)); !errors.Is(e, os.ErrNotExist) {
						return ErrConflict
					}
					if e = os.Rename(archive, filepath.Dir(path)); e != nil {
						return e
					}
				}
			}
			if f.Existed {
				if e := atomicfile.Write(path, originals[i], 0600); e != nil {
					return e
				}
			} else if e := os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
		}
		if record.DiscoveryChanged {
			before, e := os.ReadFile(filepath.Join(r.Root, "home-recovery", record.Directory, "discovery-original.json"))
			if e != nil || revision(before) != record.DiscoveryBefore {
				return ErrUnsafe
			}
			if e = atomicfile.Write(filepath.Join(r.Root, homeExclusionsFile), before, 0600); e != nil {
				return e
			}
		}
		return os.Remove(pending)
	})
}

// LoadRetired reads only a profile explicitly archived by the completed receipt.
// Its ID, definition and cursor paths remain available to source handoff logic.
func (r Registry) LoadRetired(name string) (Entry, error) {
	if ValidateName(name) != nil || r.HomeTransactionPending() {
		return Entry{}, ErrUnsafe
	}
	marker := filepath.Join(r.Root, ".homes-migration-complete.json")
	if err := r.safePath(marker); err != nil {
		return Entry{}, err
	}
	data, err := os.ReadFile(marker)
	if err != nil || len(data) > 1<<20 {
		return Entry{}, ErrUnsafe
	}
	var receipt homeRecovery
	if json.Unmarshal(data, &receipt) != nil || receipt.Version != 1 || len(receipt.Directory) != 64 {
		return Entry{}, ErrUnsafe
	}
	for _, c := range receipt.Directory {
		if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
			return Entry{}, ErrUnsafe
		}
	}
	candidates := []retiredHomeProfile{}
	for _, f := range receipt.Files {
		if f.Retire {
			candidates = append(candidates, retiredHomeProfile{Name: f.Name, Directory: receipt.Directory, Revision: f.Before})
		}
	}
	candidates = append(candidates, receipt.RetiredProfiles...)
	if len(candidates) > 256 {
		return Entry{}, ErrUnsafe
	}
	for _, f := range candidates {
		if f.Name != name {
			continue
		}
		if len(f.Directory) != 64 {
			return Entry{}, ErrUnsafe
		}
		for _, c := range f.Directory {
			if (c < 'a' || c > 'f') && (c < '0' || c > '9') {
				return Entry{}, ErrUnsafe
			}
		}
		path := filepath.Join(r.Root, "home-recovery", f.Directory, "retired", name, "profile.json")
		if err = r.safePath(path); err != nil {
			return Entry{}, err
		}
		p, err := LoadFrom(path)
		if err != nil {
			return Entry{}, err
		}
		if p.sourceRevision != f.Revision {
			return Entry{}, ErrConflict
		}
		return Entry{ID: ProfileID(name), Name: name, Revision: p.sourceRevision, RuntimeDir: filepath.Dir(path), Profile: p}, nil
	}
	return Entry{}, os.ErrNotExist
}
