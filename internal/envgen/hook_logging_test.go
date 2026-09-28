package envgen

import (
	"strings"
	"testing"

	"cctrace/internal/profile"
)

// The hook-spawned daemon parent is the first thing to attempt a self-update on
// every session, and its stdout and stderr go to a stream Claude Code discards.
// Without --log-to-file its diagnostics reach nobody: #458 needed exactly this
// parent's update failure and it was not in sync.log, because diagf only routes
// at the log when the flag put it there.
//
// The flag is the signal the routing is designed around -- see the comment on
// daemonLogInstalled: divert when the process's own streams go nowhere, not
// when stdout happens not to be a terminal.
func TestSyncCommandsRouteHookOutputAtTheLog(t *testing.T) {
	p := &profile.Profile{ClaudeConfigDir: "/tmp/claude-home"}
	start, end := syncCommands(p)

	for name, cmd := range map[string]string{"SessionStart": start, "SessionEnd": end} {
		if !strings.Contains(cmd, "--log-to-file") {
			t.Fatalf("%s hook does not route its output at sync.log; a failure there reaches nobody:\n%s", name, cmd)
		}
	}
}
