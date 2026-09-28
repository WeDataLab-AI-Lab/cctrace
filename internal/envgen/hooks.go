package envgen

import (
	"fmt"
	"os"
	"path/filepath"

	"cctrace/internal/profile"
)

// sessionEndHookTimeout is the per-hook timeout (seconds) for the async
// SessionEnd hook. Shared by addSyncHook and managedSettingsHash so the hash
// changes whenever this value changes.
const sessionEndHookTimeout = 30

// syncCommands returns the SessionStart and SessionEnd commands for the given profile.
func syncCommands(p *profile.Profile) (startCmd, endCmd string) {
	claudeDir := p.ClaudeConfigDir
	if claudeDir == "" {
		home, _ := os.UserHomeDir()
		claudeDir = filepath.Join(home, ".claude")
	}
	// Same conversion the hook binary path gets, and for the same reason: the hook
	// is run by a bash-like shell. This was an inline copy of normalizeHookPath,
	// character for character. The copy is what let the test drift -- with no name
	// to call, it approximated the conversion and got it half right.
	claudeDir = normalizeHookPath(claudeDir)
	bin := shellQuote(hookBinaryPath())
	claudeDirArg := shellQuote(claudeDir)
	// --log-to-file because a hook's stdout and stderr go to a stream the caller
	// discards. Without it this parent's diagnostics reach nobody, and this
	// parent is the first thing to attempt a self-update on every session: #458
	// needed exactly its update failure and sync.log did not have it.
	//
	// The flag is the signal the output routing is built around -- see the
	// comment on daemonLogInstalled. It says "my own streams go nowhere", which
	// is true here and false for a foreground run with piped stdout.
	startCmd = fmt.Sprintf("%s sync --daemon --log-to-file --claude-dir %s --auto-profile --interval 1s", bin, claudeDirArg)
	endCmd = fmt.Sprintf("%s sync --daemon --once --log-to-file --claude-dir %s --auto-profile", bin, claudeDirArg)
	return
}

// addSyncHook adds cctrace sync hooks to both SessionStart and SessionEnd.
// SessionStart: starts the long-lived watch daemon (single-instance lock).
// SessionEnd: fires a best-effort one-shot sync. It is marked async so it never
// blocks session exit — SessionEnd's default hook timeout is only 1.5s, and the
// daemon is intentionally left running for other open sessions.
func addSyncHook(settings map[string]interface{}, p *profile.Profile) {
	startCmd, endCmd := syncCommands(p)
	upsertHookCommand(settings, hookCommandSpec{event: "SessionStart", command: startCmd})
	upsertHookCommand(settings, hookCommandSpec{event: "SessionEnd", command: endCmd, async: true, timeout: sessionEndHookTimeout})
}

// hookCommandSpec describes a single cctrace hook command and its optional
// execution modifiers (async / timeout) for a given Claude Code hook event.
type hookCommandSpec struct {
	event   string
	command string
	async   bool
	timeout int
}

// applyHookCommandSpec writes the command and optional async/timeout fields onto
// a hook entry map, so both the create and update paths stay consistent.
func applyHookCommandSpec(hm map[string]interface{}, spec hookCommandSpec) {
	hm["command"] = spec.command
	hm["type"] = "command"
	if spec.async {
		hm["async"] = true
	} else {
		delete(hm, "async")
	}
	if spec.timeout > 0 {
		hm["timeout"] = spec.timeout
	} else {
		delete(hm, "timeout")
	}
}

// upsertHookCommand adds or updates a cctrace sync command in the given hook event.
func upsertHookCommand(settings map[string]interface{}, spec hookCommandSpec) {
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		hooks = make(map[string]interface{})
	}

	hookEntry := map[string]interface{}{}
	applyHookCommandSpec(hookEntry, spec)

	eventHooks, _ := hooks[spec.event].([]interface{})
	if len(eventHooks) == 0 {
		hooks[spec.event] = []interface{}{
			map[string]interface{}{
				"hooks":   []interface{}{hookEntry},
				"matcher": "",
			},
		}
		settings["hooks"] = hooks
		return
	}

	// Find the default matcher (matcher: "")
	for _, m := range eventHooks {
		matcher, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		matcherVal, _ := matcher["matcher"].(string)
		if matcherVal != "" {
			continue
		}

		existingHooks, _ := matcher["hooks"].([]interface{})
		for _, h := range existingHooks {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if c, _ := hm["command"].(string); isCctraceHook(c) {
				// Migrate existing cctrace hooks: refresh command and apply
				// async/timeout so already-installed users get the new behavior.
				applyHookCommandSpec(hm, spec)
				settings["hooks"] = hooks
				return
			}
		}

		matcher["hooks"] = append(existingHooks, hookEntry)
		settings["hooks"] = hooks
		return
	}

	eventHooks = append(eventHooks, map[string]interface{}{
		"hooks":   []interface{}{hookEntry},
		"matcher": "",
	})
	hooks[spec.event] = eventHooks
	settings["hooks"] = hooks
}

// removeSyncHook removes cctrace sync commands from both SessionStart and SessionEnd.
// If a matcher's hooks become empty after removal, the matcher itself is removed.
// If an event has no matchers left, the event key is removed from hooks.
func removeSyncHook(settings map[string]interface{}) {
	hooks, _ := settings["hooks"].(map[string]interface{})
	if hooks == nil {
		return
	}
	for _, event := range []string{"SessionStart", "SessionEnd"} {
		eventHooks, _ := hooks[event].([]interface{})
		var survivingMatchers []interface{}
		for _, m := range eventHooks {
			matcher, ok := m.(map[string]interface{})
			if !ok {
				survivingMatchers = append(survivingMatchers, m)
				continue
			}
			existingHooks, _ := matcher["hooks"].([]interface{})
			filtered := make([]interface{}, 0)
			for _, h := range existingHooks {
				hm, ok := h.(map[string]interface{})
				if !ok {
					filtered = append(filtered, h)
					continue
				}
				if c, _ := hm["command"].(string); isCctraceHook(c) {
					continue
				}
				filtered = append(filtered, h)
			}
			// Keep matcher only if it still has hooks
			if len(filtered) > 0 {
				matcher["hooks"] = filtered
				survivingMatchers = append(survivingMatchers, matcher)
			}
		}
		if len(survivingMatchers) > 0 {
			hooks[event] = survivingMatchers
		} else {
			delete(hooks, event)
		}
	}
	settings["hooks"] = hooks
}
