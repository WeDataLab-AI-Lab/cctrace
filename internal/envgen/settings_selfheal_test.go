package envgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Absent stamp must be treated as stale so existing users (installed before
// the stamp existed) self-heal on first run.
func TestEnsureClaudeSettingsCurrentRewritesWhenStampAbsent(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)
	p := testProfile(claudeDir)

	// Pre-existing settings with the OLD cctrace SessionEnd hook (no stamp).
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionEnd": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						map[string]interface{}{
							"command": "/old/cctrace sync --stop 2>/dev/null; /old/cctrace sync --daemon --once --claude-dir " + claudeDir,
							"type":    "command",
						},
					},
					"matcher": "",
				},
			},
		},
	})

	changed, err := EnsureClaudeSettingsCurrent(p)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !changed {
		t.Fatal("absent stamp must be treated as stale (changed=true)")
	}

	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	if storedManagedHash(s) == "" {
		t.Fatal("stamp not recorded after rewrite")
	}
	if storedManagedHash(s) != managedSettingsHash(p) {
		t.Fatal("recorded stamp does not match current hash")
	}
}

func TestEnsureClaudeSettingsCurrentSkipsWhenCurrent(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)
	p := testProfile(claudeDir)

	if _, err := EnsureClaudeSettingsCurrent(p); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	changed, err := EnsureClaudeSettingsCurrent(p)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if changed {
		t.Fatal("second call with matching hash must be a no-op (changed=false)")
	}
}

func TestEnsureClaudeSettingsCurrentRejectsInvalidJSON(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)
	path := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0644); err != nil {
		t.Fatalf("write invalid fixture: %v", err)
	}

	changed, err := EnsureClaudeSettingsCurrent(testProfile(claudeDir))
	if err == nil {
		t.Fatal("expected invalid settings.json to be rejected")
	}
	if changed {
		t.Fatal("invalid settings.json must not report changed=true")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{not-json" {
		t.Fatalf("invalid settings.json was overwritten: %q", string(data))
	}
}

func TestApplyToClaudeSettingsPreservesUnknownCctraceMetadata(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		cctraceMetaKey: map[string]interface{}{
			"operator_note": "keep",
		},
	})

	if err := ApplyToClaudeSettings(testProfile(claudeDir)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	meta := s[cctraceMetaKey].(map[string]interface{})
	if meta["operator_note"] != "keep" {
		t.Fatalf("unknown cctrace metadata was not preserved: %v", meta)
	}
	if meta[managedHashField] == "" {
		t.Fatalf("managed hash was not written: %v", meta)
	}
}

func TestApplyToClaudeSettingsRejectsInvalidJSON(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)
	path := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(path, []byte("{ this is not valid json"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := ApplyToClaudeSettings(testProfile(claudeDir)); err == nil {
		t.Fatal("expected error on invalid existing settings.json")
	}

	// The corrupt file must be left untouched, not clobbered with an empty doc.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "not valid json") {
		t.Fatalf("invalid settings.json was overwritten: %q", string(data))
	}
}

func TestRemoveFromClaudeSettingsRemovesStamp(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)

	// Seed a user key, then Apply (adds OTEL env + hooks + stamp).
	writeJSON(t, filepath.Join(claudeDir, "settings.json"), map[string]interface{}{
		"permissions": map[string]interface{}{"allow": []interface{}{"Bash(ls)"}},
	})
	p := testProfile(claudeDir)
	if err := ApplyToClaudeSettings(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if s := readJSON(t, filepath.Join(claudeDir, "settings.json")); s[cctraceMetaKey] == nil {
		t.Fatal("stamp not written by Apply")
	}

	if err := RemoveFromClaudeSettings(p); err != nil {
		t.Fatalf("remove: %v", err)
	}
	s := readJSON(t, filepath.Join(claudeDir, "settings.json"))
	if _, has := s[cctraceMetaKey]; has {
		t.Fatalf("cctrace stamp not removed: %v", s[cctraceMetaKey])
	}
	// User key preserved.
	if _, has := s["permissions"]; !has {
		t.Fatal("user permissions key lost on remove")
	}
}

// An existing 0-byte settings.json (possibly truncated mid-write by another
// tool) must not be clobbered by self-heal.
func TestEnsureClaudeSettingsCurrentSkipsEmptyFile(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	os.MkdirAll(claudeDir, 0755)
	path := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}

	changed, err := EnsureClaudeSettingsCurrent(testProfile(claudeDir))
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if changed {
		t.Fatal("empty settings.json must not be clobbered (changed=false)")
	}
	data, _ := os.ReadFile(path)
	if len(data) != 0 {
		t.Fatalf("empty file was rewritten: %q", string(data))
	}
}
