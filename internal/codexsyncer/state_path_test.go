package codexsyncer

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStatePathForProfile(t *testing.T) {
	// Default (unnamed) profile uses the global state path, unchanged from the
	// pre-fix behavior for backward compatibility.
	if got := StatePathForProfile(""); got != DefaultCodexStatePath() {
		t.Fatalf("empty profile: got %q, want global %q", got, DefaultCodexStatePath())
	}

	// A named profile gets its own per-profile state file so concurrent syncs
	// from different profiles do not share offsets.
	got := StatePathForProfile("local-dev")
	if got == DefaultCodexStatePath() {
		t.Fatalf("named profile must not use the global path: %q", got)
	}
	if filepath.Base(got) != "codex-sync-state.json" {
		t.Fatalf("expected codex-sync-state.json filename, got %q", got)
	}
	if !strings.Contains(got, filepath.Join("profiles", "local-dev")) {
		t.Fatalf("expected per-profile dir in path, got %q", got)
	}

	// Distinct profiles must map to distinct state files.
	if StatePathForProfile("acct-a") == StatePathForProfile("acct-b") {
		t.Fatalf("distinct profiles must map to distinct state paths")
	}

	// An invalid/reserved name ("default" is reserved) falls back to the global
	// path rather than producing an invalid location.
	if got := StatePathForProfile("default"); got != DefaultCodexStatePath() {
		t.Fatalf("reserved name should fall back to global, got %q", got)
	}
}
