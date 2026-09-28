package store

import (
	"context"
	"testing"
	"time"
)

// A project whose sessions have all been deleted must leave the picker.
//
// Metadata rows carry a project_hash under an empty session_id, and tombstones are
// keyed on session_id, so those rows outlive every delete. ListProjects used to ask
// only for a project_hash, so such a project stayed selectable forever -- choosing
// it opened a header reading "0 sessions" over an empty list, because the session
// list counts only rows with a real session id.
func TestListProjects_dropsProjectsLeftWithOnlyMetadataRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, hash := range []string{"proj-live", "proj-emptied"} {
		if err := s.UpsertProject(ctx, "claude", hash, hash, "", "", "", "", now); err != nil {
			t.Fatalf("upsert %s: %v", hash, err)
		}
	}

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "sess-live", RecordType: "user", Agent: "claude", ProjectHash: "proj-live"},
		{Ts: now, SessionID: "sess-doomed", RecordType: "user", Agent: "claude", ProjectHash: "proj-emptied"},
		// The metadata row: a real project_hash under no session at all.
		{Ts: now, SessionID: "", RecordType: "summary", Agent: "claude", ProjectHash: "proj-emptied"},
	}); err != nil {
		t.Fatalf("seed session records: %v", err)
	}

	if _, err := s.DeleteSession(ctx, "sess-doomed", "tester", "", false, false); err != nil {
		t.Fatalf("delete sess-doomed: %v", err)
	}

	projects, err := s.ListProjects(ctx, ProjectFilter{})
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range projects {
		seen[p.ProjectHash] = true
	}
	if !seen["proj-live"] {
		t.Error("proj-live still has a session and must stay in the list")
	}
	if seen["proj-emptied"] {
		t.Error("proj-emptied has no sessions left; only its metadata row remains, so it must not be listed")
	}
}

// DeleteProject is the picker's entry point: it has no session to start from, and
// the project most in need of removal is the one whose sessions are already gone.
func TestDeleteProject_tombstonesEverySessionAndCanBlock(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.UpsertProject(ctx, "claude", "proj-doomed", "doomed", "", "", "", "", now); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "sess-a", RecordType: "user", Agent: "claude", ProjectHash: "proj-doomed"},
		{Ts: now, SessionID: "sess-b", RecordType: "user", Agent: "claude", ProjectHash: "proj-doomed"},
		{Ts: now, SessionID: "sess-other", RecordType: "user", Agent: "claude", ProjectHash: "proj-kept"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := s.DeleteProject(ctx, []string{"proj-doomed"}, "tester", "picker", true)
	if err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if res.MarkedSessions != 2 {
		t.Errorf("marked %d sessions, want 2", res.MarkedSessions)
	}
	if !res.ProjectBlocked {
		t.Error("block was requested and must be reported")
	}

	blocked, err := s.ListBlockedProjects(ctx)
	if err != nil {
		t.Fatalf("list blocked: %v", err)
	}
	var found bool
	for _, b := range blocked {
		if b.ProjectHash == "proj-doomed" {
			found = true
		}
	}
	if !found {
		t.Error("proj-doomed must appear in the blocked list")
	}

	projects, err := s.ListProjects(ctx, ProjectFilter{})
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	for _, p := range projects {
		if p.ProjectHash == "proj-doomed" {
			t.Error("every session was tombstoned; the project must leave the list")
		}
	}
}

// An empty hash is not an empty filter.
func TestDeleteProject_rejectsEmptyHash(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	if _, err := s.DeleteProject(ctx, []string{""}, "tester", "", false); err != ErrEmptyProjectHash {
		t.Errorf("err = %v, want ErrEmptyProjectHash", err)
	}
}
