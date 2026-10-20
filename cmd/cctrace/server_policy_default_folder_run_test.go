package main

import (
	"context"
	"testing"

	"cctrace/internal/store"
)

// HOME, the signing key, the runtime seams and the sync locks are process-wide,
// so none of these tests run in parallel.

// unmovedSiblingRun is the case the v0.7.64 fleet met: the default profile and
// "home" share the default Codex folder, and the policy does not name "home", which
// therefore does not move.
func unmovedSiblingRun(t *testing.T) *sharedRunFixture {
	t.Helper()
	f := newSharedRunFixture(t)
	f.planned(t, f.a)
	f.pol.Targets = f.pol.Targets[:1]
	f.serve(t, f.pol)
	return f
}

// wantOKReport checks what the run just ended would report to the server.
func wantOKReport(t *testing.T, reason string) {
	t.Helper()
	report, _, err := buildPolicyReport("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.CheckResult != store.ClientPolicyCheckOK || report.CheckReason != reason {
		t.Fatalf("report %q %q; want ok with reason %q", report.CheckResult, report.CheckReason, reason)
	}
}

// The profile's own block is the policy's to move whoever else shares the file:
// the whole run carries it out, and nothing is left to note.
func TestServerPolicyRunMovesOwnBlockInDefaultFolderSharedWithUnmovedSibling(t *testing.T) {
	f := unmovedSiblingRun(t)

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	state := f.state(t, "")
	if got := f.read(t); got != f.newBlock(t, "", sharedRunTokenA) {
		t.Fatalf("shared config not moved:\n%s", got)
	}
	if state.Status != "applied" || state.Sequence != f.pol.Sequence || state.Reason != "" || len(state.SharedWrites) != 0 {
		t.Fatalf("state %+v; want applied, no note, nothing left to write", state)
	}
	wantOKReport(t, "")
}

// The sibling's block is the sibling's to move, and it does not: the file stays as
// it is, the profile has moved, and the run says so -- on every later run too,
// since the operator reads it from the latest report.
func TestServerPolicyRunKeepsUnmovedSiblingsBlockInDefaultFolder(t *testing.T) {
	f := unmovedSiblingRun(t)
	sibling := f.block(t, f.home.Server.Endpoint, sharedRunTokenHome, "")
	f.writeConfig(t, sibling)
	note := "default Codex config kept: " + f.config

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	state := f.state(t, "")
	if got := f.read(t); got != sibling {
		t.Fatalf("overwrote the sibling's block:\n%s", got)
	}
	if state.Status != "applied" || state.Sequence != f.pol.Sequence || state.Reason != note || len(state.SharedWrites) != 0 {
		t.Fatalf("state %+v; want applied with the note and nothing left to write", state)
	}
	wantOKReport(t, note)

	history := len(state.History)
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if again := f.state(t, ""); again.Reason != note || len(again.History) != history {
		t.Fatalf("second run changed the state: %+v", again)
	}
	wantOKReport(t, note)
}
