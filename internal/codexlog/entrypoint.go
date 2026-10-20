package codexlog

import "strings"

// EntrypointFromOriginator maps Codex's originator onto the entrypoint vocabulary the
// session classifier reads. Only the human TUI becomes "cli"; every other launcher —
// codex exec, the TS SDK, or a delegating harness such as Claude Code — keeps its own
// name, so an unrecognised originator is never mistaken for a human turn. An empty
// originator (pre-originator rollouts) stays empty and falls to the legacy path.
//
// subagentFork overrides all of it. A rollout the model forked off a session inherits
// that session's originator verbatim — measured on a real run, three subagent rollouts
// all carried {"originator":"codex-tui","thread_source":"subagent"} — so originator
// cannot distinguish "a person typed this" from "the model spawned this". Without the
// override every subagent thread lands on "cli" and joins the sessions a human had,
// which is the opposite of what the classifier exists to do. The override also beats
// the empty case: a rollout we know an agent spawned must not inherit the benefit of
// the doubt reserved for files predating the originator field.
func EntrypointFromOriginator(originator string, subagentFork bool) string {
	if subagentFork {
		return "codex-subagent"
	}
	if originator == "" {
		return ""
	}
	if originator == "codex-tui" {
		return "cli"
	}
	return strings.ReplaceAll(strings.ToLower(originator), " ", "-")
}
