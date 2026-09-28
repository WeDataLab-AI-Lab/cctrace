package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexsyncer"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// writeCodexRollout creates <home>/sessions/<name> with one minimal Codex
// rollout line and returns its absolute path.
func writeCodexRollout(t *testing.T, home, name string) string {
	t.Helper()
	sessions := filepath.Join(home, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(sessions, name)
	line := `{"type":"session_meta","timestamp":"2026-04-23T11:30:10.000Z","payload":{"cwd":"/tmp/proj","model_provider":"openai"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

// Wiring lock: `cctrace sync` must scan the default home, CODEX_HOME and the
// profile's options.codex_dirs. Collapsing the call site back to a single home
// (e.g. []string{codexlog.DefaultCodexDir()}) has to fail here — the unit tests
// on ResolveScanDirs cannot see the call site.
func TestRunSyncCodexScansEveryConfiguredHome(t *testing.T) {
	useTempSyncHome(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	base := t.TempDir()
	envHome := t.TempDir()
	profileHome := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", envHome)

	baseFile := writeCodexRollout(t, base, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-00000000000a.jsonl")
	envFile := writeCodexRollout(t, envHome, "rollout-2026-04-23T12-00-00-bbbbbbbb-0000-0000-0000-00000000000b.jsonl")
	profileFile := writeCodexRollout(t, profileHome, "rollout-2026-04-23T13-00-00-cccccccc-0000-0000-0000-00000000000c.jsonl")

	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	p.Options.CodexSyncEnabled = true
	p.Options.CodexDirs = []string{profileHome}
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}

	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync: %v", err)
	}

	state, err := syncer.LoadState(codexsyncer.StatePathForProfile(""))
	if err != nil {
		t.Fatalf("load codex state: %v", err)
	}
	for _, want := range []string{baseFile, envFile, profileFile} {
		if !state.HasFile(want) {
			t.Fatalf("codex sync state has no entry for %s; state = %v", want, state.Files)
		}
	}
}

func TestResolveCodexScanDirsUnionsEnvAndProfile(t *testing.T) {
	base := t.TempDir()
	envHome := t.TempDir()
	profileHome := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", envHome)

	p := profile.NewDefault()
	p.Options.CodexDirs = []string{profileHome}

	var buf bytes.Buffer
	got := resolveCodexScanDirs(&buf, p)
	want := []string{base, envHome, profileHome}
	if len(got) != len(want) {
		t.Fatalf("resolveCodexScanDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolveCodexScanDirs = %v, want %v", got, want)
		}
	}
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning output: %q", buf.String())
	}
}

func TestResolveCodexScanDirsWarnsOnUnscannableDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", "")

	missing := filepath.Join(t.TempDir(), "gone")
	p := profile.NewDefault()
	p.Options.CodexDirs = []string{missing}

	var buf bytes.Buffer
	got := resolveCodexScanDirs(&buf, p)
	if len(got) != 1 || got[0] != base {
		t.Fatalf("resolveCodexScanDirs = %v, want [%s]", got, base)
	}
	if !strings.Contains(buf.String(), missing) {
		t.Fatalf("warning = %q, want it to name %s", buf.String(), missing)
	}
}

func TestPrintCodexDryRunListsHomesAndFileCounts(t *testing.T) {
	base := t.TempDir()
	extra := t.TempDir()
	t.Setenv("CODEX_CONFIG_DIR", base)
	t.Setenv("CODEX_HOME", "")
	writeCodexRollout(t, extra, "rollout-2026-04-23T13-00-00-cccccccc-0000-0000-0000-00000000000c.jsonl")

	p := profile.NewDefault()
	p.Options.CodexSyncEnabled = true
	p.Options.CodexDirs = []string{extra}

	var buf bytes.Buffer
	printCodexDryRun(&buf, p)
	out := buf.String()
	if !strings.Contains(out, base+" (0 session files)") {
		t.Fatalf("dry-run output = %q, want base home with 0 files", out)
	}
	if !strings.Contains(out, extra+" (1 session files)") {
		t.Fatalf("dry-run output = %q, want extra home with 1 file", out)
	}
}

func TestPrintCodexDryRunReportsDisabledCodexSync(t *testing.T) {
	t.Setenv("CODEX_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CCTRACE_CODEX_SYNC", "")

	p := profile.NewDefault()
	p.Options.CodexSyncEnabled = false
	p.Options.CodexDirs = []string{t.TempDir()}

	var buf bytes.Buffer
	printCodexDryRun(&buf, p)
	if !strings.Contains(buf.String(), "Codex sync is disabled") {
		t.Fatalf("dry-run output = %q, want disabled notice", buf.String())
	}
}
