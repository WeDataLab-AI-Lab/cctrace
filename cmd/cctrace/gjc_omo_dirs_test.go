package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/omosyncer"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// writeGjcSession creates <home>/agent/sessions/v2-<proj>/<name> with one
// minimal gjc session line and returns its absolute path.
func writeGjcSession(t *testing.T, home, proj, name string) string {
	t.Helper()
	dir := filepath.Join(home, "agent", "sessions", "v2-"+proj)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir gjc session dir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write gjc session: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

// writeOmoSession creates <home>/sessions/--proj--/<name> with one minimal
// omo session line and returns its absolute path.
func writeOmoSession(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "--proj--")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir omo session dir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write omo session: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

func TestResolveGjcScanDirsUnionsDefaultAndProfile(t *testing.T) {
	home := t.TempDir()
	extra := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".gjc"), 0o755); err != nil {
		t.Fatalf("mkdir default gjc home: %v", err)
	}

	p := profile.NewDefault()
	p.Options.GjcDirs = []string{extra}

	var buf bytes.Buffer
	got := resolveGjcScanDirs(&buf, p)
	want := []string{filepath.Join(home, ".gjc"), extra}
	if len(got) != len(want) {
		t.Fatalf("resolveGjcScanDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolveGjcScanDirs = %v, want %v", got, want)
		}
	}
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning output: %q", buf.String())
	}
}

func TestResolveGjcScanDirsWarnsOnUnscannableDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".gjc"), 0o755); err != nil {
		t.Fatalf("mkdir default gjc home: %v", err)
	}

	missing := filepath.Join(t.TempDir(), "gone")
	p := profile.NewDefault()
	p.Options.GjcDirs = []string{missing}

	var buf bytes.Buffer
	got := resolveGjcScanDirs(&buf, p)
	if len(got) != 1 || got[0] != filepath.Join(home, ".gjc") {
		t.Fatalf("resolveGjcScanDirs = %v, want [%s]", got, filepath.Join(home, ".gjc"))
	}
	if !strings.Contains(buf.String(), missing) {
		t.Fatalf("warning = %q, want it to name %s", buf.String(), missing)
	}
}

func TestPrintGjcDryRunListsHomesAndFileCounts(t *testing.T) {
	home := t.TempDir()
	extra := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".gjc"), 0o755); err != nil {
		t.Fatalf("mkdir default gjc home: %v", err)
	}
	writeGjcSession(t, extra, "proj", "session.jsonl")

	p := profile.NewDefault()
	p.Options.GjcSyncEnabled = true
	p.Options.GjcDirs = []string{extra}

	var buf bytes.Buffer
	printGjcDryRun(&buf, p)
	out := buf.String()
	if !strings.Contains(out, filepath.Join(home, ".gjc")+" (0 session files)") {
		t.Fatalf("dry-run output = %q, want default home with 0 files", out)
	}
	if !strings.Contains(out, extra+" (1 session files)") {
		t.Fatalf("dry-run output = %q, want extra home with 1 file", out)
	}
}

func TestPrintGjcDryRunReportsDisabledGjcSync(t *testing.T) {
	t.Setenv("CCTRACE_GJC_SYNC", "")

	p := profile.NewDefault()
	p.Options.GjcSyncEnabled = false
	p.Options.GjcDirs = []string{t.TempDir()}

	var buf bytes.Buffer
	printGjcDryRun(&buf, p)
	if !strings.Contains(buf.String(), "Gjc sync is disabled") {
		t.Fatalf("dry-run output = %q, want disabled notice", buf.String())
	}
}

func TestPrintOmoDryRunListsFileCount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeOmoSession(t, filepath.Join(home, ".omo"), "1970-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl")

	p := profile.NewDefault()
	p.Options.OmoSyncEnabled = true

	var buf bytes.Buffer
	printOmoDryRun(&buf, p)
	out := buf.String()
	if !strings.Contains(out, "(1 session files)") {
		t.Fatalf("dry-run output = %q, want 1 session file", out)
	}
}

func TestPrintOmoDryRunSkipsWhenDisabled(t *testing.T) {
	t.Setenv("CCTRACE_OMO_SYNC", "")

	p := profile.NewDefault()
	p.Options.OmoSyncEnabled = false

	var buf bytes.Buffer
	printOmoDryRun(&buf, p)
	if buf.Len() != 0 {
		t.Fatalf("dry-run output = %q, want no output when disabled", buf.String())
	}
}

func TestGjcStatePathForProfileUsesNamedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got := gjcStatePathForProfile("")
	want := filepath.Join(home, ".cctrace", "gjc-sync-state.json")
	if got != want {
		t.Fatalf("gjcStatePathForProfile(\"\") = %q, want %q", got, want)
	}

	dir, err := profile.NamedDir("work")
	if err != nil {
		t.Fatalf("profile.NamedDir: %v", err)
	}
	got = gjcStatePathForProfile("work")
	want = filepath.Join(dir, "gjc-sync-state.json")
	if got != want {
		t.Fatalf("gjcStatePathForProfile(\"work\") = %q, want %q", got, want)
	}
}

func TestOmoStatePathForProfileUsesNamedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got := omoStatePathForProfile("")
	want := omosyncer.DefaultStatePath()
	if got != want {
		t.Fatalf("omoStatePathForProfile(\"\") = %q, want %q", got, want)
	}

	dir, err := profile.NamedDir("work")
	if err != nil {
		t.Fatalf("profile.NamedDir: %v", err)
	}
	got = omoStatePathForProfile("work")
	want = filepath.Join(dir, "omo-sync-state.json")
	if got != want {
		t.Fatalf("omoStatePathForProfile(\"work\") = %q, want %q", got, want)
	}
}

// Wiring lock: `cctrace sync` must run the gjc and omo syncers when enabled
// and record their scanned files in per-tool state, just like the Codex leg.
func TestRunSyncGjcAndOmoEnabledScanFiles(t *testing.T) {
	home := useTempSyncHome(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")

	gjcFile := writeGjcSession(t, filepath.Join(home, ".gjc"), "proj", "session.jsonl")
	omoFile := writeOmoSession(t, filepath.Join(home, ".omo"), "1970-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000101.jsonl")

	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	p.Options.GjcSyncEnabled = true
	p.Options.OmoSyncEnabled = true
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}

	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync: %v", err)
	}

	gjcState, err := syncer.LoadState(gjcStatePathForProfile(""))
	if err != nil {
		t.Fatalf("load gjc state: %v", err)
	}
	if !gjcState.HasFile(gjcFile) {
		t.Fatalf("gjc sync state has no entry for %s; state = %v", gjcFile, gjcState.Files)
	}

	omoState, err := syncer.LoadState(omoStatePathForProfile(""))
	if err != nil {
		t.Fatalf("load omo state: %v", err)
	}
	if !omoState.HasFile(omoFile) {
		t.Fatalf("omo sync state has no entry for %s; state = %v", omoFile, omoState.Files)
	}
}

func TestRunSyncGjcEnabledHandlesStateLoadError(t *testing.T) {
	useTempSyncHome(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	p.Options.GjcSyncEnabled = true
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	if err := os.MkdirAll(gjcStatePathForProfile(""), 0700); err != nil {
		t.Fatalf("mkdir gjc state path as dir: %v", err)
	}

	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync with gjc state load error should not abort: %v", err)
	}
}

func TestRunSyncOmoEnabledHandlesStateLoadError(t *testing.T) {
	useTempSyncHome(t)
	claudeDir := filepath.Join(t.TempDir(), ".claude")
	p := profile.NewDefault()
	p.User.Email = "user@example.com"
	p.Server.Endpoint = "http://localhost:4317"
	p.ClaudeConfigDir = claudeDir
	p.Options.OmoSyncEnabled = true
	if err := profile.Save(p); err != nil {
		t.Fatalf("profile.Save: %v", err)
	}
	if err := os.MkdirAll(omoStatePathForProfile(""), 0700); err != nil {
		t.Fatalf("mkdir omo state path as dir: %v", err)
	}

	if err := runSync(false, claudeDir, false, false, false, false, time.Second, "", "", "", false, "", false); err != nil {
		t.Fatalf("runSync with omo state load error should not abort: %v", err)
	}
}
