package main

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/profile"
)

func TestHomeDecisionInputRequiresExplicitChoicesAndFinalSummary(t *testing.T) {
	t.Parallel()
	homes := []profile.HomeMembership{{Provider: "claude", Home: "/synthetic/claude", Transmission: "undecided"}}
	for _, input := range []string{"", "allow\n", "allow\nyes\nno\n", "undecided\nyes\nyes\n"} {
		ir := &inputReader{reader: bufio.NewReader(strings.NewReader(input))}
		if _, err := promptHomeDecisions(ir, io.Discard, homes); err == nil {
			t.Fatalf("incomplete consent accepted: %q", input)
		}
	}
	ir := &inputReader{reader: bufio.NewReader(strings.NewReader("exclude\nyes\nyes\n"))}
	got, err := promptHomeDecisions(ir, io.Discard, homes)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Transmission != "exclude" || !got[0].QuotaEnabled {
		t.Fatal("separate choices lost")
	}
}

// HOME is process-wide, so this legacy CLI fixture is deliberately serial.
func TestLegacyProfileAddRequiresOnboardingWithoutProviderWrites(t *testing.T) {
	home := setupTestHome(t)
	provider := filepath.Join(home, ".claude-unapproved")
	if err := os.Mkdir(provider, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(profile.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runProfileCmd(t, "add", "unapproved", "--home", provider); err == nil || !strings.Contains(err.Error(), "onboard") {
		t.Fatalf("legacy clone bypassed mandatory onboarding: %v", err)
	}
	if names, _ := profile.ListNamed(); len(names) != 0 {
		t.Fatal("unapproved profile created")
	}
	if _, err = os.Stat(filepath.Join(provider, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("unapproved home received provider hooks/exporter")
	}
	after, _ := os.ReadFile(profile.DefaultPath())
	if string(after) != string(before) {
		t.Fatal("legacy default mutated")
	}
}
