// Package configtxn applies a bounded set of configuration replacements. Callers
// must hold their policy and settings locks throughout Apply and Recover.
package configtxn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cctrace/internal/atomicfile"
)

// ErrDeferRecovery leaves staged files intact when a consumer cannot be stopped safely.
var ErrDeferRecovery = errors.New("transaction recovery deferred")

type Change struct {
	Path         string
	Before       []byte
	BeforeExists bool
	After        []byte
	Mode         os.FileMode
}
type entry struct {
	Change
	OriginalMode os.FileMode
}
type journal struct {
	Entries  []entry
	Dirs     []string
	Complete bool
}

// Apply durably stages backups and replacements before changing any target.
// A failed validation rolls back, but never overwrites a concurrent user edit.
func Apply(dir string, changes []Change, validate func() error) error {
	return apply(dir, changes, validate, atomicfile.Write)
}
func apply(dir string, changes []Change, validate func() error, write func(string, []byte, os.FileMode) error) error {
	if err := safePath(dir); err != nil {
		return err
	}
	if err := Recover(dir); err != nil {
		return err
	}
	j := journal{}
	seen := map[string]bool{}
	for _, c := range changes {
		if err := safePath(c.Path); err != nil {
			return err
		}
		if c.Path == dir || strings.HasPrefix(c.Path, dir+string(os.PathSeparator)) || seen[c.Path] {
			return fmt.Errorf("invalid transaction target %s", c.Path)
		}
		seen[c.Path] = true
		if err := matches(c.Path, c.Before, c.BeforeExists); err != nil {
			return err
		}
		e := entry{Change: c}
		if c.BeforeExists {
			i, err := os.Stat(c.Path)
			if err != nil {
				return err
			}
			e.OriginalMode = i.Mode().Perm()
		}
		j.Entries = append(j.Entries, e)
		for p := filepath.Dir(c.Path); ; p = filepath.Dir(p) {
			_, err := os.Lstat(p)
			if err == nil {
				break
			}
			if !os.IsNotExist(err) {
				return err
			}
			if p != dir && !strings.HasPrefix(dir, p+string(os.PathSeparator)) {
				j.Dirs = append(j.Dirs, p)
			}
		}
	}
	if len(j.Entries) == 0 {
		return nil
	}
	if err := mkdirDurable(dir); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	if err := save(dir, j); err != nil {
		return err
	}
	validated := false
	fail := func(err error) error {
		if validated {
			return errors.Join(ErrDeferRecovery, err)
		}
		if errors.Is(err, ErrDeferRecovery) {
			return err
		}
		return errors.Join(err, Recover(dir))
	}
	for _, e := range j.Entries {
		if err := safePath(e.Path); err != nil {
			return fail(err)
		}
		if err := matches(e.Path, e.Before, e.BeforeExists); err != nil {
			return fail(err)
		}
		if err := mkdirDurable(filepath.Dir(e.Path)); err != nil {
			return fail(err)
		}
		if err := write(e.Path, e.After, e.Mode.Perm()); err != nil {
			return fail(err)
		}
	}
	if validate != nil {
		if err := validate(); err != nil {
			return fail(err)
		}
	}
	validated = validate != nil
	for _, e := range j.Entries {
		if err := matches(e.Path, e.After, true); err != nil {
			return fail(err)
		}
	}
	j.Complete = true
	if err := save(dir, j); err != nil {
		return fail(err)
	}
	return clear(dir)
}

// Recover rolls back an interrupted transaction. The journal directory is private
// trusted state; callers must not accept a journal supplied by another principal.
func Recover(dir string) error {
	if err := safePath(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, "journal.json")
	if err := safePath(path); err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var j journal
	if err = json.Unmarshal(b, &j); err != nil {
		return err
	}
	// Validate the entire journal before touching a target.
	seen := map[string]bool{}
	allowedDirs := map[string]bool{}
	for _, e := range j.Entries {
		if err := safePath(e.Path); err != nil {
			return err
		}
		if e.Path == dir || strings.HasPrefix(e.Path, dir+string(os.PathSeparator)) || seen[e.Path] {
			return errors.New("invalid journal target")
		}
		seen[e.Path] = true
		for p := filepath.Dir(e.Path); p != filepath.Dir(p); p = filepath.Dir(p) {
			allowedDirs[p] = true
		}
	}
	for _, p := range j.Dirs {
		if err := safePath(p); err != nil {
			return err
		}
		if !allowedDirs[p] || p == dir || strings.HasPrefix(dir, p+string(os.PathSeparator)) {
			return errors.New("invalid journal directory")
		}
	}
	if j.Complete {
		return clear(dir)
	}
	var errs []error
	for i := len(j.Entries) - 1; i >= 0; i-- {
		e := j.Entries[i]
		if matches(e.Path, e.Before, e.BeforeExists) == nil {
			if e.BeforeExists {
				info, statErr := os.Stat(e.Path)
				if statErr != nil {
					errs = append(errs, statErr)
					continue
				}
				if info.Mode().Perm() != e.OriginalMode {
					// Recovery may have stopped after restoring bytes, before mode.
					if info.Mode().Perm() != e.OriginalMode&e.Mode.Perm() {
						errs = append(errs, fmt.Errorf("mode conflict: %s", e.Path))
						continue
					}
					if modeErr := restoreMode(e.Path, e.OriginalMode); modeErr != nil {
						errs = append(errs, modeErr)
					}
				}
			}
			continue
		}
		if err := matches(e.Path, e.After, true); err != nil {
			errs = append(errs, err)
			continue
		}
		if e.BeforeExists {
			err = atomicfile.Write(e.Path, e.Before, e.OriginalMode)
			// atomicfile intentionally narrows modes; rollback restores the original.
			if err == nil {
				err = restoreMode(e.Path, e.OriginalMode)
			}
		} else {
			err = os.Remove(e.Path)
			if err == nil {
				err = syncDir(filepath.Dir(e.Path))
			}
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	for _, p := range j.Dirs {
		if err := os.Remove(p); err == nil {
			if err := syncDir(filepath.Dir(p)); err != nil {
				return err
			}
		}
	}
	return clear(dir)
}
func safePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("path must be clean and absolute: %s", path)
	}
	for p := path; ; p = filepath.Dir(p) {
		i, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && i.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink path: %s", p)
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
func matches(path string, want []byte, exists bool) error {
	i, err := os.Lstat(path)
	if !exists && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", path, err)
	}
	if !exists || !i.Mode().IsRegular() {
		return fmt.Errorf("snapshot conflict: %s", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(b, want) {
		return fmt.Errorf("snapshot conflict: %s", path)
	}
	return nil
}
func save(dir string, j journal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, "journal.json"), b, 0600)
}
func clear(dir string) error {
	if err := os.Remove(filepath.Join(dir, "journal.json")); err != nil {
		return err
	}
	return syncDir(dir)
}

func mkdirDurable(dir string) error {
	var missing []string
	for p := dir; ; p = filepath.Dir(p) {
		_, err := os.Stat(p)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, p)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, p := range missing {
		if err := syncDir(p); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(p)); err != nil {
			return err
		}
	}
	return nil
}

func restoreMode(path string, mode os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	// Atomic replacement already synced the contents. No metadata operation is
	// needed when the restored file already has its original effective mode.
	if sameRestoreMode(info.Mode().Perm(), mode.Perm()) {
		return nil
	}
	return changeModeDurably(path, mode)
}
