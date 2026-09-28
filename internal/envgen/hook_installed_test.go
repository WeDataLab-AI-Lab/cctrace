package envgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/profile"
)

// A watcher that gives up its lock during a stall depends on something starting
// a replacement, and the SessionStart hook is that something (#712). On an
// install without the hook -- a server, a hand-managed launchd unit -- nothing
// would, so the daemon has to know whether it has a successor before it exits.
func TestSyncStartHookInstalled(t *testing.T) {
	claudeDir := t.TempDir()
	p := profile.NewDefault()
	p.ClaudeConfigDir = claudeDir
	settingsPath := filepath.Join(claudeDir, "settings.json")

	write := func(t *testing.T, settings map[string]interface{}) {
		t.Helper()
		data, err := json.Marshal(settings)
		if err != nil {
			t.Fatalf("marshal settings: %v", err)
		}
		if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
			t.Fatalf("write settings: %v", err)
		}
	}

	if SyncStartHookInstalled(p) {
		t.Error("no settings.json at all was read as an installed hook")
	}

	write(t, map[string]interface{}{})
	if SyncStartHookInstalled(p) {
		t.Error("settings without hooks was read as an installed hook")
	}

	// A SessionStart hook that is not ours says nothing about a replacement
	// daemon.
	write(t, map[string]interface{}{"hooks": map[string]interface{}{
		"SessionStart": []interface{}{map[string]interface{}{
			"matcher": "",
			"hooks":   []interface{}{map[string]interface{}{"type": "command", "command": "echo hi"}},
		}},
	}})
	if SyncStartHookInstalled(p) {
		t.Error("someone else's SessionStart hook was read as ours")
	}

	// The SessionEnd hook runs `sync --daemon --once`, which finishes one pass
	// and exits. It is not a successor.
	write(t, map[string]interface{}{"hooks": map[string]interface{}{
		"SessionEnd": []interface{}{map[string]interface{}{
			"matcher": "",
			"hooks":   []interface{}{map[string]interface{}{"type": "command", "command": "/bin/cctrace sync --daemon --once --log-to-file"}},
		}},
	}})
	if SyncStartHookInstalled(p) {
		t.Error("the SessionEnd one-shot hook was read as a successor")
	}

	// What cctrace init actually writes.
	settings := map[string]interface{}{}
	addSyncHook(settings, p)
	write(t, settings)
	if !SyncStartHookInstalled(p) {
		t.Error("the hook cctrace installs was not recognised")
	}
}
