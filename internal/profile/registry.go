package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cctrace/internal/atomicfile"
)

// Registry shares the existing CLI definitions. Names are immutable identities;
// UI preferences and provider credentials remain outside this registry API.
type Registry struct{ Root string }

type Entry struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Revision              string   `json:"revision"`
	CollectionState       string   `json:"collection_state"`
	ConfigurationComplete bool     `json:"configuration_complete"`
	RuntimeDir            string   `json:"-"`
	Profile               *Profile `json:"-"`
}

type MigrationConflict struct {
	Name              string `json:"name"`
	CLIRevision       string `json:"cli_revision"`
	DesktopRevision   string `json:"desktop_revision"`
	CLIClaudeHome     string `json:"cli_claude_home"`
	CLICodexHome      string `json:"cli_codex_home"`
	DesktopClaudeHome string `json:"desktop_claude_home"`
	DesktopCodexHome  string `json:"desktop_codex_home"`
}

func (r Registry) Path(name string) (string, error) {
	if name == "default" {
		return filepath.Join(r.Root, "profile.json"), nil
	}
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(r.Root, "profiles", name, "profile.json"), nil
}
func ProfileID(name string) string {
	if name == "default" {
		return name
	}
	return "named:" + name
}
func (r Registry) Load(name string) (Entry, error) {
	path, err := r.Path(name)
	if err != nil {
		return Entry{}, err
	}
	if err := r.safePath(path); err != nil {
		return Entry{}, err
	}
	p, err := LoadFrom(path)
	if err != nil {
		return Entry{}, err
	}
	if p.ValidateHomes() != nil {
		return Entry{}, ErrUnsafe
	}
	if p.ID != "" && p.ID != ProfileID(name) {
		return Entry{}, ErrUnsafe
	}
	complete := p.Options.LocalAnalysisEnabled ||
		(p.User.ID != "" || p.User.Email != "") && (p.Server.SyncEndpoint != "" || p.Server.Endpoint != "")
	state := "disabled"
	if p.RegistrationOnly || !complete {
		state = "registered"
	} else if p.CollectionEnabled("claude") || p.CollectionEnabled("codex") || p.Options.GjcSyncEnabled || p.Options.OmoSyncEnabled || p.Options.LocalAnalysisEnabled {
		state = "enabled"
	}
	return Entry{ID: ProfileID(name), Name: name, Revision: p.sourceRevision, CollectionState: state, ConfigurationComplete: complete, RuntimeDir: filepath.Dir(path), Profile: p}, nil
}
func (r Registry) List() ([]Entry, error) {
	if r.HomeTransactionPending() {
		return nil, ErrUnsafe
	}
	entries := make([]Entry, 0)
	if _, err := os.Lstat(r.Root); errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if _, err := os.Lstat(filepath.Join(r.Root, "profile.json")); err == nil {
		entry, err := r.Load("default")
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	dirs, err := os.ReadDir(filepath.Join(r.Root, "profiles"))
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if !dir.IsDir() || dir.Type()&os.ModeSymlink != 0 {
			return nil, ErrUnsafe
		}
		path, err := r.Path(dir.Name())
		if err != nil {
			return nil, ErrUnsafe
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		entry, err := r.Load(dir.Name())
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name == "default" {
			return true
		}
		if entries[j].Name == "default" {
			return false
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}
func (r Registry) Enabled() ([]Entry, error) {
	entries, err := r.List()
	if err != nil {
		return nil, err
	}
	enabled := make([]Entry, 0)
	for _, entry := range entries {
		if entry.CollectionState == "enabled" {
			enabled = append(enabled, entry)
		}
	}
	return enabled, nil
}
func (r Registry) RegisterMapping(name, claudeHome, codexHome string) (entry Entry, err error) {
	entries, e := r.List()
	if e != nil {
		return Entry{}, e
	}
	for _, existing := range entries {
		if existing.Profile.HomesVersion != 0 {
			return Entry{}, ErrUnsafe
		}
	}

	if err = ValidateName(name); err != nil {
		return
	}
	err = withProfileLock(r.Root, func() error {
		path, _ := r.Path(name)
		if _, err := os.Lstat(filepath.Dir(path)); err == nil {
			return ErrExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		p := NewDefault()
		p.ID = ProfileID(name)
		p.RegistrationOnly = true
		p.Options.SyncEnabled = false
		p.Options.CodexSyncEnabled = false
		p.ClaudeConfigDir = claudeHome
		p.CodexHome = codexHome
		if err := saveToUnlocked(p, path); err != nil {
			return err
		}
		entry, err = r.Load(name)
		return err
	})
	return
}
func (r Registry) UpdateMapping(name, expected, claudeHome, codexHome string) (entry Entry, err error) {
	err = withProfileLock(r.Root, func() error {
		existing, err := r.Load(name)
		if err != nil {
			return err
		}
		if expected == "" || existing.Revision != expected {
			return ErrConflict
		}
		p := existing.Profile
		if p.HomesVersion != 0 || p.Homes != nil {
			return ErrUnsafe
		}
		p.ID = existing.ID
		p.ClaudeConfigDir = claudeHome
		p.CodexHome = codexHome
		p.UpdatedAt = time.Now().UTC()
		path, _ := r.Path(name)
		if err := saveToUnlocked(p, path); err != nil {
			return err
		}
		entry, err = r.Load(name)
		return err
	})
	return
}

// RemoveMapping never removes runtime/cursor directories. CLI's explicit
// profile remove remains the owner of its historical recursive metadata cleanup.
func (r Registry) RemoveMapping(name, expected string) error {
	if name == "default" {
		return ErrUnsafe
	}
	return withProfileLock(r.Root, func() error {
		entry, err := r.Load(name)
		if err != nil {
			return err
		}
		if expected == "" || entry.Revision != expected {
			return ErrConflict
		}
		if !r.CanRemoveMapping(entry) {
			return ErrUnsafe
		}
		path, _ := r.Path(name)
		root, err := os.OpenRoot(r.Root)
		if err != nil {
			return err
		}
		defer root.Close()
		relative, err := filepath.Rel(r.Root, path)
		if err != nil {
			return err
		}
		if err := root.Remove(relative); err != nil {
			return err
		}
		return root.Remove(filepath.Dir(relative))
	})
}
func (r Registry) safePath(path string) error {
	relative, err := filepath.Rel(r.Root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return ErrUnsafe
	}
	current := r.Root
	for _, part := range append([]string{""}, strings.Split(relative, string(os.PathSeparator))...) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return ErrUnsafe
		}
		if os.PathSeparator != '\\' && info.Mode().Perm()&0077 != 0 {
			return ErrUnsafe
		}
	}
	return nil
}

// Migration receipts are hashes only; original legacy files are never removed.
// Each successful import records its source digest, so a later removal does not
// recreate the profile from an unchanged legacy record.
func (r Registry) migrationReceipts() (map[string]string, error) {
	receipts := map[string]string{}
	path := filepath.Join(r.Root, ".desktop-migrations.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return receipts, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.safePath(path); err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &receipts)
	return receipts, err
}
func (r Registry) writeMigrationReceipts(receipts map[string]string) error {
	data, err := json.MarshalIndent(receipts, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(r.Root, ".desktop-migrations.json"), data, 0600)
}
func (r Registry) MigrateDesktop(legacyRoot string) (conflicts []MigrationConflict, err error) {
	entries, e := r.List()
	if e != nil {
		return nil, e
	}
	for _, entry := range entries {
		if entry.Profile.HomesVersion != 0 {
			return []MigrationConflict{}, nil
		}
	}

	conflicts = make([]MigrationConflict, 0)
	if legacyRoot == "" {
		return
	}
	if filepath.Clean(legacyRoot) == filepath.Clean(r.Root) {
		return
	}
	// No registry is created by mere listing when no legacy data exists.
	if _, e := os.Lstat(filepath.Join(legacyRoot, "profiles")); errors.Is(e, os.ErrNotExist) {
		return
	}
	err = withProfileLock(r.Root, func() error {
		legacy := Registry{Root: legacyRoot}
		entries, err := legacy.List()
		if err != nil {
			return err
		}
		receipts, err := r.migrationReceipts()
		if err != nil {
			return err
		}
		changed := false
		for _, old := range entries {
			oldPath, _ := legacy.Path(old.Name)
			key := revision([]byte(oldPath))
			if receipts[key] == old.Revision {
				continue
			}
			current, e := r.Load(old.Name)
			if errors.Is(e, os.ErrNotExist) {
				p := old.Profile
				p.ID = ProfileID(old.Name)
				p.RegistrationOnly = true
				p.Options.SyncEnabled = false
				p.Options.CodexSyncEnabled = false
				if p.CodexHome == "" && len(p.Options.CodexDirs) > 0 {
					p.CodexHome = p.Options.CodexDirs[0]
				}
				path, _ := r.Path(old.Name)
				if err := saveToUnlocked(p, path); err != nil {
					return err
				}
			} else if e != nil {
				return e
			} else if current.Profile.ClaudeConfigDir != old.Profile.ClaudeConfigDir || current.Profile.CodexHome != old.Profile.legacyMappingCodexHome() {
				conflicts = append(conflicts, MigrationConflict{Name: old.Name, CLIRevision: current.Revision, DesktopRevision: old.Revision, CLIClaudeHome: current.Profile.ClaudeConfigDir, CLICodexHome: current.Profile.CodexHome, DesktopClaudeHome: old.Profile.ClaudeConfigDir, DesktopCodexHome: old.Profile.legacyMappingCodexHome()})
				continue
			}
			receipts[key] = old.Revision
			changed = true
		}
		if changed {
			return r.writeMigrationReceipts(receipts)
		}
		return nil
	})
	return
}

// legacyMappingCodexHome keeps legacy desktop's first scan root interpretation while
// giving explicit execution homes priority. It never changes the scan-root list.
func (p *Profile) legacyMappingCodexHome() string {
	if p.CodexHome != "" {
		return p.CodexHome
	}
	if len(p.Options.CodexDirs) > 0 {
		return p.Options.CodexDirs[0]
	}
	return ""
}
func (r Registry) ResolveMigration(legacyRoot, name, source, cliRevision, desktopRevision string) error {
	if source != "cli" && source != "desktop" {
		return fmt.Errorf("select cli or desktop")
	}
	return withProfileLock(r.Root, func() error {
		current, err := r.Load(name)
		if err != nil {
			return err
		}
		legacy := Registry{Root: legacyRoot}
		old, err := legacy.Load(name)
		if err != nil {
			return err
		}
		if current.Revision != cliRevision || old.Revision != desktopRevision {
			return ErrConflict
		}
		if source == "desktop" {
			// Preserve the complete CLI definition before an explicit folder selection.
			backup := filepath.Join(r.Root, "migration-originals", current.Name+"-"+current.Revision+".json")
			if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
				return err
			}
			if err := atomicfile.Write(backup, current.Profile.sourceJSON, 0600); err != nil {
				return err
			}
			p := current.Profile
			p.ID = current.ID
			p.ClaudeConfigDir = old.Profile.ClaudeConfigDir
			p.CodexHome = old.Profile.legacyMappingCodexHome()
			path, _ := r.Path(name)
			if err := saveToUnlocked(p, path); err != nil {
				return err
			}
		}
		receipts, err := r.migrationReceipts()
		if err != nil {
			return err
		}
		oldPath, _ := legacy.Path(name)
		receipts[revision([]byte(oldPath))] = old.Revision
		return r.writeMigrationReceipts(receipts)
	})
}

// CanRemoveMapping makes the same conservative predicate available to previews.
func (r Registry) CanRemoveMapping(entry Entry) bool {
	if entry.Name == "default" {
		return false
	}
	p := entry.Profile
	if !p.RegistrationOnly || p.CollectionEnabled("claude") || p.CollectionEnabled("codex") || p.Options.GjcSyncEnabled || p.Options.OmoSyncEnabled || p.Options.LocalAnalysisEnabled || p.User != (UserInfo{}) || p.Server != (ServerInfo{Protocol: "grpc"}) {
		return false
	}
	known, _ := json.Marshal(p)
	var raw, canonical map[string]json.RawMessage
	_ = json.Unmarshal(p.sourceJSON, &raw)
	_ = json.Unmarshal(known, &canonical)
	for key := range canonical {
		if _, ok := raw[key]; !ok {
			return false
		}
	}
	if len(raw) != len(canonical) {
		return false
	}
	for _, key := range []string{"user", "server", "options"} {
		var a, b map[string]json.RawMessage
		_ = json.Unmarshal(raw[key], &a)
		_ = json.Unmarshal(canonical[key], &b)
		for key := range b {
			if _, ok := a[key]; !ok {
				return false
			}
		}
		if len(a) != len(b) {
			return false
		}
	}
	children, err := os.ReadDir(entry.RuntimeDir)
	if err != nil || len(children) != 1 || children[0].Name() != "profile.json" {
		return false
	}
	return true
}
