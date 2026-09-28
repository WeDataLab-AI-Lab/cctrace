package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrSymlinkUnsupported = errors.New("atomic write destination must not be a symlink")

func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	mode := perm
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrSymlinkUnsupported
		}
		if !info.Mode().IsRegular() {
			return errors.New("atomic write destination must be a regular file")
		}
		mode = info.Mode().Perm() & perm
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keepTemp := true
	defer func() {
		if keepTemp {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replace(tmpName, path); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	keepTemp = false
	return nil
}
