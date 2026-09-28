package main

import (
	"strings"
	"testing"
)

// Re-running `cctrace init` over an existing profile offered a menu whose default
// was "Patch Codex integration only". A Claude-only user pressing enter got
// "Codex CLI not detected. Install Codex first" and nothing else -- and that user
// was often sent there by the troubleshooting table's advice for a stale hook
// path, so the documented cure died on the default.
func TestInitReRunOptions(t *testing.T) {
	t.Run("without codex the codex patch is not offered", func(t *testing.T) {
		opts := initReRunOptions(false)
		for _, o := range opts {
			if o.action == reRunPatchCodex {
				t.Fatalf("codex patch offered with no codex installed: %+v", opts)
			}
		}
	})

	t.Run("without codex the default re-applies hooks", func(t *testing.T) {
		opts := initReRunOptions(false)
		if len(opts) == 0 {
			t.Fatal("no options")
		}
		if opts[0].action != reRunApplyEnv {
			t.Errorf("default action = %v, want re-apply hooks/env", opts[0].action)
		}
	})

	t.Run("with codex the default stays the codex patch", func(t *testing.T) {
		opts := initReRunOptions(true)
		if len(opts) == 0 {
			t.Fatal("no options")
		}
		if opts[0].action != reRunPatchCodex {
			t.Errorf("default action = %v, want the codex patch", opts[0].action)
		}
	})

	t.Run("every menu always offers full re-setup and cancel", func(t *testing.T) {
		for _, codex := range []bool{false, true} {
			var full, cancel bool
			for _, o := range initReRunOptions(codex) {
				full = full || o.action == reRunFullSetup
				cancel = cancel || o.action == reRunCancel
			}
			if !full || !cancel {
				t.Errorf("codex=%v: full=%v cancel=%v; both are required", codex, full, cancel)
			}
		}
	})

	// The prompt used to hard-code [1/2/3]; with a conditional option that string
	// has to be derived, or it will offer a choice that does not exist.
	t.Run("prompt lists exactly the offered numbers", func(t *testing.T) {
		for _, codex := range []bool{false, true} {
			opts := initReRunOptions(codex)
			got := reRunPromptRange(opts)
			var want []string
			for i := range opts {
				want = append(want, string(rune('1'+i)))
			}
			if got != strings.Join(want, "/") {
				t.Errorf("codex=%v: prompt = %q, want %q", codex, got, strings.Join(want, "/"))
			}
		}
	})
}
