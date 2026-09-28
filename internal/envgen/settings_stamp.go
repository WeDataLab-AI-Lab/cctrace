package envgen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"cctrace/internal/profile"
)

// cctraceMetaKey is the top-level settings.json object holding cctrace's
// self-heal stamp. managedHashField is the content-hash subkey.
const (
	cctraceMetaKey   = "cctrace"
	managedHashField = "managed_settings_hash"
	binaryVersField  = "settings_binary_version"
)

// managedSettingsHash returns a stable hash of the settings content cctrace
// would generate for this profile (OTEL env + sync hooks). sync compares it
// against the stamp stored in settings.json to self-heal after an upgrade
// changes the generated output. See EnsureClaudeSettingsCurrent.
func managedSettingsHash(p *profile.Profile) string {
	h := sha256.New()

	env := BuildEnvMap(p)
	envKeys := make([]string, 0, len(env))
	for k := range env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		fmt.Fprintf(h, "env\x00%s\x00%s\n", k, env[k])
	}

	fmt.Fprintf(h, "sync_enabled\x00%t\n", p.Options.SyncEnabled)
	if p.Options.SyncEnabled {
		start, end := syncCommands(p)
		fmt.Fprintf(h, "hook\x00SessionStart\x00%s\n", start)
		fmt.Fprintf(h, "hook\x00SessionEnd\x00%s\x00async\x00true\x00timeout\x00%d\n", end, sessionEndHookTimeout)
	}

	return hex.EncodeToString(h.Sum(nil))
}

// setManagedStamp records cctrace's self-heal stamp (content hash + binary
// version) under the top-level "cctrace" key, preserving any unknown subkeys.
func setManagedStamp(settings map[string]interface{}, hash, version string) {
	meta, _ := settings[cctraceMetaKey].(map[string]interface{})
	if meta == nil {
		meta = make(map[string]interface{})
	}
	meta[managedHashField] = hash
	if version != "" {
		meta[binaryVersField] = version
	}
	settings[cctraceMetaKey] = meta
}

// storedManagedHash reads the previously recorded content hash, or "" if absent.
func storedManagedHash(settings map[string]interface{}) string {
	meta, _ := settings[cctraceMetaKey].(map[string]interface{})
	if meta == nil {
		return ""
	}
	s, _ := meta[managedHashField].(string)
	return s
}

// removeManagedStamp drops cctrace's stamp subkeys, removing the "cctrace"
// object entirely only when no unknown subkeys remain (preserves anything a
// user or other tool placed there).
func removeManagedStamp(settings map[string]interface{}) {
	meta, ok := settings[cctraceMetaKey].(map[string]interface{})
	if !ok {
		return
	}
	delete(meta, managedHashField)
	delete(meta, binaryVersField)
	if len(meta) == 0 {
		delete(settings, cctraceMetaKey)
	}
}
