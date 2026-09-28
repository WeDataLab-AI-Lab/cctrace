package envgen

import (
	"encoding/json"
	"os"
	"strings"

	"cctrace/internal/profile"
)

// SyncStartHookInstalled reports whether this Claude home has the SessionStart
// hook that starts a watch daemon.
//
// A stalled watcher releases its lock and exits so a fresh process can take over
// (#712), and that only recovers collection if something starts one. The hook is
// what does, on every session start. An install that does not have it -- a
// server, a hand-written launchd unit, a home where the hook was removed -- has
// no successor, so the watcher stays instead: a daemon that keeps retrying is
// better than no daemon at all.
//
// The SessionEnd hook is deliberately not accepted. It runs with --once, which
// completes a single pass and exits, so it cannot take over the watch.
//
// Anything unreadable reports false. The cost of guessing wrong in that
// direction is a watcher that keeps trying, which is the behaviour this whole
// path is an exception to.
func SyncStartHookInstalled(p *profile.Profile) bool {
	settingsPath, err := ClaudeSettingsPath(p)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil || len(data) == 0 {
		return false
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false
	}
	hooks, _ := settings["hooks"].(map[string]interface{})
	matchers, _ := hooks["SessionStart"].([]interface{})
	for _, m := range matchers {
		matcher, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		entries, _ := matcher["hooks"].([]interface{})
		for _, e := range entries {
			entry, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			command, _ := entry["command"].(string)
			if strings.Contains(command, "sync --daemon") && !strings.Contains(command, "--once") {
				return true
			}
		}
	}
	return false
}
