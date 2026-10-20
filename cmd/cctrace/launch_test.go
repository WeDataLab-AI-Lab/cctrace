package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

func TestProfileSelectionCLIUsesCASAndReportsUnavailable(t *testing.T) {
	setupTestHome(t)
	base, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	codexHome, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base.CodexHome = codexHome
	if err := profile.Save(base); err != nil {
		t.Fatal(err)
	}
	team := profile.NewDefault()
	team.CodexHome = base.CodexHome
	if err := profile.SaveNamed(team, "team"); err != nil {
		t.Fatal(err)
	}
	selectCmd := profileCmd()
	var out bytes.Buffer
	selectCmd.SetOut(&out)
	selectCmd.SetArgs([]string{"select", "team", "--agent", "codex", "--revision", "0"})
	if err := selectCmd.Execute(); err != nil {
		t.Fatalf("profile select: %v", err)
	}
	binDir := t.TempDir()
	writeFakeCodex(t, binDir, "codex-cli 0.162.0", "#!/bin/sh\nprintf '%s\\n' 'codex-cli 0.162.0'\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	selection, err := profile.ReadSelection(profile.SelectionPath())
	if err != nil || selection.Revision != 1 || selection.Codex != "team" {
		t.Fatalf("selection = %+v, %v", selection, err)
	}
	selectedCmd := profileCmd()
	selectedCmd.SetOut(&out)
	selectedCmd.SetArgs([]string{"selected", "--agent", "codex"})
	if err := selectedCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "team") {
		t.Fatalf("selected output = %q", out.String())
	}
}

func TestProfileSelectRejectsCodexProfileWithoutExplicitHome(t *testing.T) {
	setupTestHome(t)
	if err := profile.SaveNamed(profile.NewDefault(), "legacy-codex"); err != nil {
		t.Fatal(err)
	}
	cmd := profileCmd()
	cmd.SetArgs([]string{"select", "legacy-codex", "--agent", "codex"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "codex_home") {
		t.Fatalf("profile select without explicit CodexHome error = %v", err)
	}
	if _, err := os.Stat(profile.SelectionPath()); !os.IsNotExist(err) {
		t.Fatalf("failed profile selection changed selection file: stat error=%v", err)
	}
}

func TestLaunchAppliesSelectedCodexHomeToFakeAgent(t *testing.T) {
	setupTestHome(t)
	codexHome, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.CodexHome = codexHome
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.UpdateSelection(profile.SelectionPath(), "codex", "default", nil); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "argv-env.txt")
	t.Setenv("CCTRACE_TEST_LAUNCH_CAPTURE", capture)
	binDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '%s\\n' 'codex-cli 0.162.0'; exit 0; fi\nprintf '%s\\n' \"$CODEX_HOME\" \"$@\" > \"$CCTRACE_TEST_LAUNCH_CAPTURE\"\n"
	writeFakeCodex(t, binDir, "codex-cli 0.162.0", script)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := newRootCmd()
	cmd.SetArgs([]string{"launch", "--agent", "codex", "--", "--model", "fixture-model"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("launch: %v", err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || lines[0] != codexHome || lines[1] != "--model" || lines[2] != "fixture-model" {
		t.Fatalf("fake launch captured %q", data)
	}
}

func TestLaunchRequiresExplicitCodexHome(t *testing.T) {
	setupTestHome(t)
	cmd := newRootCmd()
	cmd.SetArgs([]string{"launch", "--agent", "codex"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "codex_home") {
		t.Fatalf("launch without explicit CodexHome error = %v", err)
	}
}

func TestLaunchRejectsUnverifiedCodexVersionBeforeLaunchingAgent(t *testing.T) {
	setupTestHome(t)
	codexHome, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.CodexHome = codexHome
	if err := profile.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.UpdateSelection(profile.SelectionPath(), "codex", "default", nil); err != nil {
		t.Fatal(err)
	}
	launched := filepath.Join(t.TempDir(), "agent-launched")
	binDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '%s\\n' 'codex-cli 9.9.9'; exit 0; fi\nprintf 'launched' > \"$CCTRACE_TEST_LAUNCH_MARKER\"\n"
	writeFakeCodex(t, binDir, "codex-cli 9.9.9", script)
	t.Setenv("CCTRACE_TEST_LAUNCH_MARKER", launched)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := newRootCmd()
	cmd.SetArgs([]string{"launch", "--agent", "codex"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "version is not verified") {
		t.Fatalf("unverified Codex version launch error = %v", err)
	}
	if _, err := os.Stat(launched); !os.IsNotExist(err) {
		t.Fatalf("unverified Codex CLI was launched: stat error=%v", err)
	}
}

func TestLaunchIOModeRequiresOptIn(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"launch", "--agent", "claude", "--io-mode", "pipes"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --observe-process-io") {
		t.Fatalf("--io-mode without explicit opt-in error = %v", err)
	}
}

func TestObservedPTYRejectsUnsupportedNonTerminalBeforeProfileRead(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"launch", "--agent", "claude", "--observe-process-io"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("observed PTY without terminal error = %v", err)
	}
}

func TestLaunchIORelayRequiresExplicitObservationOptIn(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"launch", "--agent", "claude", "--io-mode", "pipes"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --observe-process-io") {
		t.Fatalf("--io-mode without opt-in error = %v", err)
	}
}

func TestLaunchObservedPTYFailsBeforeAgentWhenTerminalIsUnavailable(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"launch", "--agent", "claude", "--observe-process-io"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("observed PTY without a terminal error = %v", err)
	}
}
