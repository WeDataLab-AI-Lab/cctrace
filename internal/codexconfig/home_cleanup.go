package codexconfig

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"

	"cctrace/internal/profile"
)

func PlanOwnedOtelRemoval(data []byte, endpoint, token string) ([]byte, error) {
	section := otelSectionLines(string(data))
	if endpoint == "" || token == "" || len(section) == 0 || existingBearerToken(section) != token || !sendsTo(section, codexMetricsEndpoint(endpoint)) {
		return data, nil
	}
	if hasMultilineRootOtelKey(string(data)) {
		return nil, ErrMultilineRootOtelKey
	}
	return []byte(replaceOrAppendOtelSection(string(data), "")), nil
}
func ApplyHomeConfigCAS(home string, before, after []byte) error {
	if err := profile.CheckCodexHome(home); err != nil {
		return err
	}
	if bytes.Equal(before, after) {
		return nil
	}
	release, err := lockConfig(home, false)
	if err != nil {
		return err
	}
	if release == nil {
		return profile.ErrUnsafe
	}
	defer release()
	path := filepath.Join(home, "config.toml")
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (!info.Mode().IsRegular() || info.Size() > 1<<20) {
		return profile.ErrUnsafe
	}
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !bytes.Equal(current, before) {
		return profile.ErrConflict
	}
	if after == nil {
		if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return writeConfigFileAtomic(path, after, 0600)
}
func RemoveOwnedOtelBlock(home, endpoint, token string) (bool, error) {
	if err := profile.CheckCodexHome(home); err != nil {
		return false, err
	}
	path := filepath.Join(home, "config.toml")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return false, profile.ErrUnsafe
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	after, err := PlanOwnedOtelRemoval(before, endpoint, token)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(before, after), ApplyHomeConfigCAS(home, before, after)
}
