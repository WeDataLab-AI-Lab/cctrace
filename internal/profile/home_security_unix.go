//go:build !windows

package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func validateCodexHomeOwnerAndMode(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("codex_home must be owned by the current user")
	}
	if info.Mode().Perm()&0022 != 0 {
		return errors.New("codex_home must not be group- or world-writable")
	}
	if err := validateCodexHomeAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

func validateCodexHomeAncestors(parent string) error {
	for dir := filepath.Clean(parent); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return errors.New("codex_home parent path is unavailable")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("cannot verify codex_home parent ownership")
		}
		owner := int(stat.Uid)
		perm := info.Mode().Perm()
		sticky := info.Mode()&os.ModeSticky != 0
		if perm&0022 != 0 && !sticky {
			return errors.New("codex_home parent is writable by other users")
		}
		if owner != os.Getuid() && owner != 0 && perm&0200 != 0 {
			return errors.New("codex_home parent can be changed by another owner")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
	}
}

func DirectoryIdentity(path string) (string, error) {
	if err := CheckCodexHome(path); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ErrUnsafe
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
