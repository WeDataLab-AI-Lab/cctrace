package codexsyncer

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/syncer"
)

func TestExplicitCodexHomeIsRedactedFromLogs(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	state := &syncer.State{}
	s := New([]string{home}, "", "", state, nil, nil)
	s.SetRedactedHomes(home)
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	if _, err := s.findAllFiles(); err != nil {
		t.Fatal(err)
	}
	s.observeAccount()
	if strings.Contains(output.String(), home) || strings.Contains(output.String(), "auth.json") {
		t.Fatalf("explicit Codex home path reached logs: %s", output.String())
	}
	if !strings.Contains(output.String(), "[explicit Codex home]") || !strings.Contains(output.String(), "account metadata could not be read") {
		t.Fatalf("redacted diagnostic context missing: %s", output.String())
	}
}

func TestExplicitCodexHomeRedactionFollowsCanonicalAliases(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink creation is not available on this platform test configuration")
	}
	home := t.TempDir()
	alias := filepath.Join(t.TempDir(), "codex-link")
	if err := os.Symlink(home, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	file := filepath.Join(home, "sessions", "fixture.jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &CodexSyncer{}
	s.SetRedactedHomes(home)
	if got := s.displayPath(filepath.Join(alias, "sessions", "fixture.jsonl")); got != "[explicit Codex home]" {
		t.Fatalf("displayPath(alias) = %q", got)
	}
}
