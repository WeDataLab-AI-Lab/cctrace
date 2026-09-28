package main

import (
	"strings"
	"testing"
)

// The checklist only prints after init authenticated, and the server refuses
// CLI authentication while the password is still temporary (403
// password_change_required). By then the password has already been changed, so
// a "change your temporary password" step names an order that cannot happen
// (#766). Both shapes are pinned: neither may carry it.
func TestNextStepsLinesHasNoPasswordStepAfterAuthentication(t *testing.T) {
	for _, syncEnabled := range []bool{true, false} {
		lines := strings.Join(nextStepsLines(syncEnabled), "\n")
		if strings.Contains(strings.ToLower(lines), "password") {
			t.Fatalf("sync=%v: checklist still asks for a password change:\n%s", syncEnabled, lines)
		}
	}
}

// The checklist is numbered, so the steps that remain must count up from 1
// without a gap in either shape.
func TestNextStepsLinesNumbersTheStepsItKeeps(t *testing.T) {
	withSync := strings.Join(nextStepsLines(true), "\n")
	if !strings.Contains(withSync, "1. Restart Claude Code") || !strings.Contains(withSync, "2. Session log sync") {
		t.Fatalf("sync enabled: steps are not numbered 1, 2:\n%s", withSync)
	}

	withoutSync := strings.Join(nextStepsLines(false), "\n")
	if !strings.Contains(withoutSync, "1. Restart Claude Code") || strings.Contains(withoutSync, "2.") {
		t.Fatalf("sync disabled: checklist is not the single restart step:\n%s", withoutSync)
	}
	if strings.Contains(withoutSync, "sync") {
		t.Fatalf("sync disabled: checklist still mentions sync:\n%s", withoutSync)
	}
}

// "automatically configured" described the settings.json write and was read as
// "it is running now". A Windows user finished init, saw an empty dashboard,
// and asked whether sync was not supposed to be automatic. It is -- from the
// next Claude Code session, which is the part the wording left out.
//
// The checklist has to answer both halves: when it starts on its own, and what
// to run to see something immediately.
func TestNextStepsLinesSaysWhenSyncStartsAndHowToStartItNow(t *testing.T) {
	lines := strings.Join(nextStepsLines(true), "\n")

	if !strings.Contains(lines, "next Claude Code session") {
		t.Fatalf("checklist does not say when sync starts on its own:\n%s", lines)
	}
	if !strings.Contains(lines, "cctrace sync") {
		t.Fatalf("checklist does not say how to sync now:\n%s", lines)
	}
	if strings.Contains(lines, "automatically configured") {
		t.Fatalf("checklist still claims sync is already active:\n%s", lines)
	}
}

// Sessions that predate the install are skipped on the first pass by design
// (see syncer.SyncOnce). Someone who installs after a day of work and finds
// none of it on the dashboard has no way to tell that apart from a broken
// install, so init says so before they go looking.
func TestNextStepsLinesWarnsThatEarlierSessionsAreNotBackfilled(t *testing.T) {
	lines := strings.Join(nextStepsLines(true), "\n")

	if !strings.Contains(lines, "before this install") {
		t.Fatalf("checklist does not mention the history it skips:\n%s", lines)
	}
}
