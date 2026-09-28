package main

import (
	"os"
	"strings"

	"cctrace/internal/codexlog"
)

// reRunAction is what the operator picked from the existing-profile menu.
type reRunAction int

const (
	reRunPatchCodex reRunAction = iota // rewrite the Codex OTEL block only
	reRunApplyEnv                      // re-register hooks/env for this binary (= `cctrace env apply`)
	reRunFullSetup                     // re-enter endpoint and password
	reRunCancel
)

type reRunOption struct {
	action reRunAction
	label  string
}

// codexInstalled reports whether a Codex CLI home exists to patch.
func codexInstalled() bool {
	_, err := os.Stat(codexlog.DefaultCodexDir())
	return err == nil
}

// initReRunOptions builds the menu for re-running init over an existing profile.
//
// The Codex patch is offered only when there is a Codex install to patch. It used
// to be listed unconditionally AND be the default, so a Claude-only user pressing
// enter reached "Codex CLI not detected" and stopped -- with no way from that menu
// to do the thing they most likely came for.
//
// That thing is re-applying hooks: the troubleshooting entry for a hook pointing
// at an old binary sends the user to `cctrace init`, so the menu has to be able to
// carry out the cure it is being used as.
func initReRunOptions(codexDetected bool) []reRunOption {
	var opts []reRunOption
	if codexDetected {
		opts = append(opts, reRunOption{reRunPatchCodex, "Patch Codex integration only (keep existing settings)"})
	}
	opts = append(opts,
		reRunOption{reRunApplyEnv, "Re-apply hooks and OTEL env for this binary (keep existing settings)"},
		reRunOption{reRunFullSetup, "Full re-setup (re-enter endpoint, password)"},
		reRunOption{reRunCancel, "Cancel"},
	)
	return opts
}

// reRunPromptRange renders the accepted numbers, derived from the options so the
// prompt cannot advertise a choice that is not on the menu.
func reRunPromptRange(opts []reRunOption) string {
	nums := make([]string, 0, len(opts))
	for i := range opts {
		nums = append(nums, string(rune('1'+i)))
	}
	return strings.Join(nums, "/")
}

// reRunChoice maps typed input to an action. Empty input takes the default (the
// first option); anything unrecognised returns false.
func reRunChoice(opts []reRunOption, input string) (reRunAction, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return opts[0].action, true
	}
	for i, o := range opts {
		if input == string(rune('1'+i)) {
			return o.action, true
		}
	}
	return reRunCancel, false
}
