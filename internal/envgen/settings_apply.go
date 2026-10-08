package envgen

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"cctrace/internal/atomicfile"

	"github.com/gofrs/flock"

	"cctrace/internal/profile"
)

// warnOut receives the notes an apply prints about values it chose not to
// touch. A variable so tests can read them.
var warnOut io.Writer = os.Stderr

// ClaudeSettingsPath returns the path to the Claude settings.json for the given profile.
// Uses ClaudeConfigDir if set, otherwise defaults to ~/.claude.
func ClaudeSettingsPath(p *profile.Profile) (string, error) {
	claudeDir := p.ClaudeConfigDir
	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not determine home directory: %w", err)
		}
		claudeDir = filepath.Join(home, ".claude")
	}
	return filepath.Join(claudeDir, "settings.json"), nil
}

// ApplyToClaudeSettings merges OTEL env vars into the Claude settings.json
// for the given profile, preserving all existing non-OTEL keys.
// A runtime guard verifies no unrelated keys were modified before writing.
func ApplyToClaudeSettings(p *profile.Profile) error {
	settingsPath, err := ClaudeSettingsPath(p)
	if err != nil {
		return err
	}
	return withSettingsLock(settingsPath, func() error {
		return applyToClaudeSettingsLocked(settingsPath, p)
	})
}

// withSettingsLock serializes a read-modify-write of settings.json against the
// other cctrace processes that write it — most often a `cctrace config`/`init`
// invocation running while a sync daemon self-heals the same file.
//
// The lock lives beside settings.json, so the directory must exist before it
// can be taken: locking first failed with ENOENT on a machine where ~/.claude
// had not been created yet, and the sync-startup self-heal was skipped every
// run because of it.
//
// This bounds our own writers only. Every write already goes through an atomic
// replace, so an unserialized writer loses an update rather than corrupting the
// file, and an external editor of settings.json is still outside this bound.
func withSettingsLock(settingsPath string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return fmt.Errorf("could not create .claude directory: %w", err)
	}
	fl := flock.New(settingsPath + ".lock")
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("lock settings: %w", err)
	}
	defer fl.Unlock() //nolint:errcheck
	return fn()
}

// applyNodeExtraCACerts writes the CA file Claude Code's http/protobuf exporter
// trusts to NODE_EXTRA_CA_CERTS, and removes it when the CA stops applying
// (cleared, or the endpoint is no longer https). caFile is claudeCAFile(p).
//
// It touches only a value cctrace wrote, recorded in the stamp. This variable
// is commonly set already for a company proxy, so a value with no record is
// the user's: with no CA it is left exactly as it is, and with a CA that
// differs it is still left as it is, with a note naming both. Keeping it is the
// safer of the two choices -- overwriting would lose a value cctrace has no
// copy of, while keeping it loses nothing and the note says how to hand the
// key over. A value the user edits after cctrace wrote it no longer matches the
// record and becomes theirs again the same way.
func applyNodeExtraCACerts(settings, envSection map[string]interface{}, caFile string) {
	recorded := recordedNodeExtraCACerts(settings)
	raw, has := envSection[envNodeExtraCACerts]
	current, _ := raw.(string)
	owned := has && recorded != "" && current == recorded
	switch {
	case caFile == "":
		if owned {
			delete(envSection, envNodeExtraCACerts)
		}
		setRecordedNodeExtraCACerts(settings, "")
	case has && !owned && current != caFile:
		fmt.Fprintf(warnOut, "  [!] %s is already set to %v, which cctrace did not write; left as is.\n", envNodeExtraCACerts, raw)
		fmt.Fprintf(warnOut, "      Claude Code telemetry needs server.ca_cert_file (%s) there. Put both CAs in one\n", caFile)
		fmt.Fprintf(warnOut, "      PEM file and use it for both, or remove the variable to let cctrace manage it.\n")
		setRecordedNodeExtraCACerts(settings, "")
	default:
		envSection[envNodeExtraCACerts] = caFile
		setRecordedNodeExtraCACerts(settings, caFile)
	}
}

func applyToClaudeSettingsLocked(settingsPath string, p *profile.Profile) error {
	// Read existing settings or start fresh. A read error other than
	// "not exist", or an existing file that is not valid JSON, must abort —
	// otherwise an automated rewrite (self-heal) could clobber a
	// momentarily-corrupt or concurrently-written settings.json with an
	// empty document.
	var settings map[string]interface{}
	data, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read settings: %w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("existing settings.json is not valid JSON; refusing to overwrite: %w", err)
		}
	}
	if settings == nil {
		settings = make(map[string]interface{})
	}

	// Snapshot full state before modification
	snapshot := takeSnapshot(settings)

	// Get or create env section.
	envSection, ok := settings["env"].(map[string]interface{})
	if !ok {
		envSection = make(map[string]interface{})
	}

	// Merge OTEL vars.
	for k, v := range BuildEnvMap(p) {
		envSection[k] = v
	}
	applyNodeExtraCACerts(settings, envSection, claudeCAFile(p))
	settings["env"] = envSection

	// Add sync hooks if sync is enabled
	if p.Options.SyncEnabled {
		addSyncHook(settings, p)
	}

	// Record the self-heal stamp (content hash) so sync can detect when an
	// upgraded binary would generate different settings. See
	// EnsureClaudeSettingsCurrent.
	setManagedStamp(settings, managedSettingsHash(p), "")

	// Runtime guard: verify only cctrace-managed keys were changed (after ALL modifications)
	if err := snapshot.verify(settings); err != nil {
		return err
	}

	// The parent directory was created while taking the settings lock.

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal settings: %w", err)
	}

	// Final validation: re-parse and check for structural issues before writing
	if err := validateSettingsJSON(out); err != nil {
		return fmt.Errorf("settings guard (final): %w", err)
	}

	return writeFileAtomic(settingsPath, append(out, '\n'), 0600)
}

// EnsureClaudeSettingsCurrent rewrites settings.json only when cctrace's
// generated content would differ from the stamp stored there (absent stamp =
// stale). Returns changed=true if a rewrite happened. Safe to call on every
// sync startup: it self-heals settings after a binary upgrade changes the
// generated hooks/env. A settings-scoped flock serializes concurrent writers
// (multiple Claude Code windows).
func EnsureClaudeSettingsCurrent(p *profile.Profile) (bool, error) {
	settingsPath, err := ClaudeSettingsPath(p)
	if err != nil {
		return false, err
	}

	changed := false
	err = withSettingsLock(settingsPath, func() error {
		var innerErr error
		changed, innerErr = ensureClaudeSettingsCurrentLocked(settingsPath, p)
		return innerErr
	})
	return changed, err
}

func ensureClaudeSettingsCurrentLocked(settingsPath string, p *profile.Profile) (bool, error) {
	want := managedSettingsHash(p)

	data, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read settings: %w", err)
	}
	if err == nil && len(data) == 0 {
		// File exists but is empty — possibly truncated mid-write by another
		// tool. Don't clobber it with a fresh document; a later sync retries.
		return false, nil
	}
	if len(data) > 0 {
		var settings map[string]interface{}
		if err := json.Unmarshal(data, &settings); err != nil {
			return false, fmt.Errorf("settings.json is not valid JSON: %w", err)
		}
		if storedManagedHash(settings) == want {
			return false, nil // already current
		}
	}

	if err := applyToClaudeSettingsLocked(settingsPath, p); err != nil {
		return false, err
	}
	return true, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	return atomicfile.Write(path, data, perm)
}

// RemoveFromClaudeSettings removes all OTEL keys written by BuildEnvMap from
// the Claude settings.json. Uses profile.ClaudeConfigDir if set, otherwise ~/.claude.
func RemoveFromClaudeSettings(p *profile.Profile) error {
	settingsPath, err := ClaudeSettingsPath(p)
	if err != nil {
		return err
	}
	// Nothing to remove, and taking the lock would create the very directory
	// this command exists to clean up. A settings.json appearing between this
	// check and a later run is handled then; reset is idempotent.
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		return nil
	}
	return withSettingsLock(settingsPath, func() error {
		return removeFromClaudeSettingsLocked(settingsPath)
	})
}

func removeFromClaudeSettingsLocked(settingsPath string) error {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to do
		}
		return fmt.Errorf("could not read settings.json: %w", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("could not parse settings.json: %w", err)
	}

	// Snapshot full state before modification
	snapshot := takeSnapshot(settings)

	if envSection, ok := settings["env"].(map[string]interface{}); ok {
		for k := range otelKeySet {
			delete(envSection, k)
		}
		// NODE_EXTRA_CA_CERTS only when it is still the value cctrace wrote.
		applyNodeExtraCACerts(settings, envSection, "")
		settings["env"] = envSection
	}

	// Remove sync hook
	removeSyncHook(settings)
	// Remove cctrace self-heal stamp
	removeManagedStamp(settings)

	// Runtime guard: verify only cctrace-managed keys were changed
	if err := snapshot.verify(settings); err != nil {
		return err
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal settings: %w", err)
	}

	// Final validation: re-parse and check for structural issues before writing
	if err := validateSettingsJSON(out); err != nil {
		return fmt.Errorf("settings guard (final): %w", err)
	}

	return writeFileAtomic(settingsPath, append(out, '\n'), 0600)
}

// validateSettingsJSON re-parses marshaled JSON and checks for structural issues
// that Claude Code would reject (e.g., null values in hooks arrays).
func validateSettingsJSON(data []byte) error {
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("re-parse failed: %w", err)
	}

	// Check hooks structure: no null values allowed
	hooks, ok := parsed["hooks"].(map[string]interface{})
	if !ok {
		return nil // no hooks section is fine
	}

	for event, v := range hooks {
		matchers, ok := v.([]interface{})
		if !ok {
			return fmt.Errorf("hooks.%s is not an array", event)
		}
		for i, m := range matchers {
			matcher, ok := m.(map[string]interface{})
			if !ok {
				return fmt.Errorf("hooks.%s[%d] is not an object", event, i)
			}
			hooksList, exists := matcher["hooks"]
			if !exists {
				return fmt.Errorf("hooks.%s[%d].hooks is missing", event, i)
			}
			if hooksList == nil {
				return fmt.Errorf("hooks.%s[%d].hooks is null", event, i)
			}
			if _, ok := hooksList.([]interface{}); !ok {
				return fmt.Errorf("hooks.%s[%d].hooks is not an array", event, i)
			}
		}
	}

	return nil
}
