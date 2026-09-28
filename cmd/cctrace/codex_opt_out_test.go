package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

// #765: a disabled profile is either one that never met Codex sync (the
// upgrade case autoMigrateCodex exists for) or one whose owner said no. The
// second must stay off: no profile flip and no [otel] write, in the default
// home or in an extra one.
func TestAutoMigrateCodexRespectsADeclinedProfile(t *testing.T) {
	home := useTempSyncHome(t)
	codexDir := filepath.Join(home, ".codex")
	envHome := t.TempDir()
	t.Setenv("CODEX_HOME", envHome)
	legacy := "[otel]\nendpoint = \"http://localhost:4317\"\n"
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{codexDir, envHome} {
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(legacy), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	p := profile.NewDefault()
	p.Server.AuthToken = "tok"
	p.Options.CodexSyncDeclined = true
	autoMigrateCodex(p, "", "http://localhost:4317")

	if p.Options.CodexSyncEnabled {
		t.Fatal("a declined profile was enabled again")
	}
	for _, dir := range []string{codexDir, envHome} {
		data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != legacy {
			t.Fatalf("%s/config.toml was rewritten for a declined profile:\n%s", dir, data)
		}
	}
}

func TestConfigSetCodexSyncRecordsTheOptOut(t *testing.T) {
	p := profile.NewDefault()
	p.Options.CodexSyncEnabled = true

	if err := setProfileField(p, "options.codex_sync_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if p.Options.CodexSyncEnabled || !p.Options.CodexSyncDeclined {
		t.Fatalf("set false: enabled=%v declined=%v, want false/true", p.Options.CodexSyncEnabled, p.Options.CodexSyncDeclined)
	}

	if err := setProfileField(p, "options.codex_sync_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	if !p.Options.CodexSyncEnabled || p.Options.CodexSyncDeclined {
		t.Fatalf("set true: enabled=%v declined=%v, want true/false", p.Options.CodexSyncEnabled, p.Options.CodexSyncDeclined)
	}
}

func TestOfferCodexSyncRecordsADecline(t *testing.T) {
	home := useTempSyncHome(t)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}

	p := profile.NewDefault()
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.AuthToken = "tok"
	ir := &inputReader{reader: bufio.NewReader(strings.NewReader("n\n"))}
	captureOutput(t, func() { offerCodexSync(ir, p, "") })

	if p.Options.CodexSyncEnabled || !p.Options.CodexSyncDeclined {
		t.Fatalf("after n: enabled=%v declined=%v, want false/true", p.Options.CodexSyncEnabled, p.Options.CodexSyncDeclined)
	}
	saved, err := profile.Load()
	if err != nil {
		t.Fatalf("the decline was not saved: %v", err)
	}
	if !saved.Options.CodexSyncDeclined {
		t.Fatal("saved profile does not record the decline")
	}

	// Saying yes later (init again) clears it.
	ir = &inputReader{reader: bufio.NewReader(strings.NewReader("y\n"))}
	captureOutput(t, func() { offerCodexSync(ir, p, "") })
	if !p.Options.CodexSyncEnabled || p.Options.CodexSyncDeclined {
		t.Fatalf("after y: enabled=%v declined=%v, want true/false", p.Options.CodexSyncEnabled, p.Options.CodexSyncDeclined)
	}
}

func TestRunCodexPatchClearsTheOptOut(t *testing.T) {
	home := useTempSyncHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := profile.NewDefault()
	p.Server.Endpoint = "http://localhost:4317"
	p.Server.AuthToken = "tok"
	p.Options.CodexSyncDeclined = true

	captureOutput(t, func() {
		if err := runCodexPatch(p, false, ""); err != nil {
			t.Errorf("runCodexPatch: %v", err)
		}
	})
	if !p.Options.CodexSyncEnabled || p.Options.CodexSyncDeclined {
		t.Fatalf("enabled=%v declined=%v, want true/false", p.Options.CodexSyncEnabled, p.Options.CodexSyncDeclined)
	}
}
