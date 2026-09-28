package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResetRemovesAllArtifacts(t *testing.T) {
	home, bin := setupHome(t)

	shellProfile := filepath.Join(home, ".zshrc")
	os.WriteFile(shellProfile, []byte("# existing config\n"), 0644)

	ts := startAuthServer(t, "Test User", "test@example.com", "eng")
	input := initAnswers(ts.URL, ts.URL, "testuser", "testpass", "y", "n")
	initOut, err := runCctrace(t, bin, home, input, "init")
	if err != nil {
		t.Fatalf("init failed: %v\noutput: %s", err, initOut)
	}

	cctraceDir := filepath.Join(home, ".cctrace")
	if _, err := os.Stat(filepath.Join(cctraceDir, "profile.json")); err != nil {
		t.Fatal("profile.json should exist after init")
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatal("settings.json should exist after init")
	}

	out, err := runCctrace(t, bin, home, "", "reset", "--force")
	if err != nil {
		t.Fatalf("reset failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(cctraceDir, "profile.json")); !os.IsNotExist(err) {
		t.Error("profile.json should be removed after reset")
	}

	data, _ := os.ReadFile(shellProfile)
	if strings.Contains(string(data), "cctrace") {
		t.Error("shell profile should not contain cctrace marker")
	}
}

func TestResetIdempotent(t *testing.T) {
	home, bin := setupHome(t)

	out1, err := runCctrace(t, bin, home, "", "reset", "--force")
	if err != nil {
		t.Fatalf("first reset failed: %v\n%s", err, out1)
	}

	out2, err := runCctrace(t, bin, home, "", "reset", "--force")
	if err != nil {
		t.Fatalf("second reset failed: %v\n%s", err, out2)
	}

	if !strings.Contains(out2, "already absent") {
		t.Errorf("expected 'already absent' messages on second reset:\n%s", out2)
	}
}
