package envgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"cctrace/internal/profile"
)

// PlanOwnedHomeRemoval never treats a stale stamp as ownership. The current
// exporter endpoint and authorization must match a confirmed old configuration.
// Only unchanged managed values and recognizable cctrace hooks are removed.
func PlanOwnedHomeRemoval(data []byte, owners ...*profile.Profile) ([]byte, error) {
	if len(data) == 0 {
		return data, nil
	}
	var settings map[string]interface{}
	if json.Unmarshal(data, &settings) != nil || settings == nil {
		return nil, profile.ErrUnsafe
	}
	before, _ := json.Marshal(settings)
	env, _ := settings["env"].(map[string]interface{})
	for _, owner := range owners {
		expected := BuildEnvMap(owner)
		endpoint, token := expected["OTEL_EXPORTER_OTLP_ENDPOINT"], expected["OTEL_EXPORTER_OTLP_HEADERS"]
		if endpoint == "" || token == "" || env["OTEL_EXPORTER_OTLP_ENDPOINT"] != endpoint || env["OTEL_EXPORTER_OTLP_HEADERS"] != token {
			continue
		}
		for key, value := range expected {
			if env[key] == value {
				delete(env, key)
			}
		}
		caRecord := recordedNodeExtraCACerts(settings)
		removeManagedStamp(settings)
		if caRecord != "" {
			setRecordedNodeExtraCACerts(settings, caRecord)
		}
		break
	}
	removeSyncHook(settings)
	current, _ := json.Marshal(settings)
	if bytes.Equal(before, current) {
		return data, nil
	}
	after, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = validateSettingsJSON(after); err != nil {
		return nil, err
	}
	return after, nil
}

// ApplyHomeSettingsCAS shares the normal provider-config lock and compares the
// exact bytes reviewed by the user before replacing them. It never creates a
// provider directory or follows a replaced settings symlink.
func ApplyHomeSettingsCAS(home string, before, after []byte) error {
	if err := profile.CheckCodexHome(home); err != nil {
		return err
	}
	if bytes.Equal(before, after) {
		return nil
	}
	path := filepath.Join(home, "settings.json")
	return withSettingsLock(path, func() error {
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
		return writeFileAtomic(path, after, 0600)
	})
}
func RemoveManagedHomeSettings(home string, owners ...*profile.Profile) (bool, error) {
	if err := profile.CheckCodexHome(home); err != nil {
		return false, err
	}
	path := filepath.Join(home, "settings.json")
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
	after, err := PlanOwnedHomeRemoval(before, owners...)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(before, after), ApplyHomeSettingsCAS(home, before, after)
}

func applyExplicitClaudeSettings(p *profile.Profile) error {
	if err := p.ValidateHomes(); err != nil {
		return err
	}
	for _, h := range p.ProviderHomes("claude", "") {
		if err := profile.CheckCodexHome(h.Home); err != nil {
			return err
		}
		if h.Transmission != "allow" || !p.CollectionEnabled("claude") {
			if _, err := RemoveManagedHomeSettings(h.Home, p); err != nil {
				return err
			}
			continue
		}
		if h.Transmission != "allow" {
			continue
		}
		local := *p
		local.HomesVersion = 0
		local.Homes = nil
		local.HomeCollection = nil
		local.ClaudeConfigDir = h.Home
		local.Options.SyncEnabled = true
		if err := ApplyToClaudeSettings(&local); err != nil {
			return err
		}
	}
	return nil
}
