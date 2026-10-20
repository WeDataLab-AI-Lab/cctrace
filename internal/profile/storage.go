package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/gofrs/flock"
)

var ErrConflict = errors.New("profile changed; reload before editing")
var ErrExists = errors.New("profile already exists")
var ErrUnsafe = errors.New("profile storage requires manual repair")

func revision(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func profileRootForPath(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(filepath.Dir(dir)) == "profiles" {
		return filepath.Dir(filepath.Dir(dir))
	}
	return dir
}

// All definition writers lock the same stable parent, not the replaced file inode.
// Unix directory locks avoid adding lifecycle debris; Windows uses a private file.
func withProfileLock(root string, action func() error) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafe
	}
	lockPath, flag := profileLockLocation(root)
	lock := flock.New(lockPath, flock.SetFlag(flag), flock.SetPermissions(0600))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
	return action()
}

func marshalPreservingUnknown(p *Profile) ([]byte, error) {
	known, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var old, fresh map[string]json.RawMessage
	if len(p.sourceJSON) > 0 {
		if err := json.Unmarshal(p.sourceJSON, &old); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal(known, &fresh); err != nil {
		return nil, err
	}
	return json.MarshalIndent(mergeUnknown(old, fresh, reflect.TypeOf(*p)), "", "  ")
}

func mergeUnknown(old, fresh map[string]json.RawMessage, typ reflect.Type) map[string]json.RawMessage {
	if old == nil {
		old = make(map[string]json.RawMessage)
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		key := strings.Split(field.Tag.Get("json"), ",")[0]
		if key == "" || key == "-" {
			continue
		}
		value, ok := fresh[key]
		if !ok {
			delete(old, key)
			continue
		}
		if key == "user" || key == "server" || key == "options" {
			var original, current map[string]json.RawMessage
			_ = json.Unmarshal(old[key], &original)
			_ = json.Unmarshal(value, &current)
			value, _ = json.Marshal(mergeUnknown(original, current, field.Type))
		}
		old[key] = value
	}
	return old
}

// PreserveBaseline makes an intentional re-init subject to the same lost-update
// check as config edits, and retains fields newer than this binary understands.
func (p *Profile) PreserveBaseline(previous *Profile) {
	if previous == nil {
		return
	}
	p.sourcePath = previous.sourcePath
	p.sourceRevision = previous.sourceRevision
	p.sourceJSON = append([]byte(nil), previous.sourceJSON...)
	p.ID = previous.ID
}

// SourceMatches checks the exact definition bytes captured by LoadFrom/SaveTo.
func (p *Profile) SourceMatches(data []byte) bool {
	if p.sourceRevision == "" || revision(data) != p.sourceRevision {
		return false
	}
	var stored Profile
	if json.Unmarshal(data, &stored) != nil {
		return false
	}
	expected, err := json.Marshal(p)
	if err != nil {
		return false
	}
	actual, err := json.Marshal(&stored)
	return err == nil && string(expected) == string(actual)
}

func ensureProfileParent(path string) error {
	root := profileRootForPath(path)
	relative, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return err
	}
	current := root
	for _, part := range append([]string{""}, strings.Split(relative, string(os.PathSeparator))...) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0700); err != nil {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafe
		}
	}
	return nil
}

// LegacyDesktopRoot identifies only the old macOS mapping store for migration.
func LegacyDesktopRoot() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "cctrace")
}

// DefinitionRevision identifies the exact persisted consent used by a launch.
func (p *Profile) DefinitionRevision() string { return p.sourceRevision }

// WithDefinitionFence serializes a post-confirmation integration with definition
// writers and refuses stale drafts or a registry awaiting transaction recovery.
func (p *Profile) WithDefinitionFence(action func() error) error {
	if p.sourcePath == "" || p.sourceRevision == "" {
		return ErrConflict
	}
	root := profileRootForPath(p.sourcePath)
	return withProfileLock(root, func() error {
		registry := Registry{Root: root}
		if registry.HomeTransactionPending() {
			return ErrUnsafe
		}
		if err := registry.safePath(p.sourcePath); err != nil {
			return err
		}
		data, err := os.ReadFile(p.sourcePath)
		if err != nil {
			return err
		}
		if !p.SourceMatches(data) {
			return ErrConflict
		}
		return action()
	})
}
