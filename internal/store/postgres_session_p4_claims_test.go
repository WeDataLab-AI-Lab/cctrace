package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

const p4ClaimHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func p4ClaimSnapshot(t *testing.T, at time.Time) json.RawMessage {
	t.Helper()
	doc := map[string]any{"commits": map[string]any{
		"trees": []map[string]any{{"repository": "github.com/acme/repo", "branch": "main"}},
		"items": []map[string]any{{"hash": p4ClaimHash, "kind": "commit", "call": "commit-call", "transcript": "session.jsonl",
			"proof": "output_hash", "at": at.Unix(), "tree": 0, "parents": 1, "subject": "Fix #12"}},
	}}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func seedP4ClaimSession(t *testing.T, s *PgStore, id, owner string, base time.Time) {
	t.Helper()
	ctx := context.Background()
	records := []*SessionRecord{
		{Ts: base.Add(time.Second), SessionID: id, RecordType: "user", Agent: "claude", UUID: "u", SourceFile: "session.jsonl", UserID: owner, ProfileEmail: owner + "@example.com", LoginEmail: owner + "@example.com", BillingProvider: "anthropic", ProjectHash: "proj", RepositoryID: "github.com/acme/repo", Raw: json.RawMessage(`{"cctraceCollect":2,"promptId":"p","origin":{"kind":"human"},"message":{"content":"fix"}}`)},
		{Ts: base.Add(2 * time.Second), SessionID: id, RecordType: "assistant", Agent: "claude", UUID: "a", ParentUUID: "u", SourceFile: "session.jsonl", UserID: owner, ProfileEmail: owner + "@example.com", LoginEmail: owner + "@example.com", BillingProvider: "anthropic", ProjectHash: "proj", RepositoryID: "github.com/acme/repo", Raw: json.RawMessage(`{"cctraceCollect":2,"message":{"content":[{"type":"tool_use","id":"commit-call","name":"Bash","input":{"command":"git commit -m 'Fix #12'"}}]}}`)},
		{Ts: base.Add(4 * time.Second), SessionID: id, RecordType: "user", Agent: "claude", UUID: "r", ParentUUID: "a", SourceFile: "session.jsonl", UserID: owner, ProfileEmail: owner + "@example.com", LoginEmail: owner + "@example.com", BillingProvider: "anthropic", ProjectHash: "proj", RepositoryID: "github.com/acme/repo", Raw: json.RawMessage(`{"cctraceCollect":2,"message":{"content":[{"type":"tool_result","tool_use_id":"commit-call","content":"[main aaaaaaa] Fix #12"}]}}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	snapshot := &SessionSnapshot{SessionID: id, UserID: owner, ProfileEmail: owner + "@example.com", LoginEmail: owner + "@example.com",
		Agent: "claude", BillingProvider: "anthropic", ProjectHash: "proj", CollectVersion: 2, IdentityProvenance: "personal_token",
		Snapshot: p4ClaimSnapshot(t, base.Add(4*time.Second)), ObservedAt: base.Add(time.Minute)}
	if _, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{snapshot}}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionP4_ClaimsBackfillConflictFenceAndDeletionRecovery(t *testing.T) {
	s := claimBackfillStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_commit_claims; DELETE FROM session_commit_claim_coverage`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"session_commit_claims", "session_commit_claim_coverage", "session_task_facts", "session_commit_facts",
			"session_analysis_facts", "session_turn_facts", "session_activity_segments", "session_timeline_members", "session_timeline_provenance",
			"session_timeline_revisions", "session_fact_dirty", "session_snapshots", "session_records", "deleted_sessions", "excluded_sessions"} {
			if _, err := s.pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE session_id=ANY($1::text[])`, []string{"p4-claim-a", "p4-claim-b"}); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	seedP4ClaimSession(t, s, "p4-claim-a", "owner-a", base)
	drainClaimBackfill(t, s)
	if !claimBackfillDone(t, s) {
		t.Fatal("snapshot claim backfill did not finish")
	}
	genA := dirtyGeneration(t, s, "p4-claim-a")
	run, err := s.processSessionFact(ctx, "p4-claim-a", genA)
	if err != nil || run.Outcome != "written" {
		t.Fatalf("first publish %+v, %v", run, err)
	}
	var source, hash string
	if err := s.pool.QueryRow(ctx, `SELECT source,hash FROM session_commit_facts WHERE session_id='p4-claim-a'`).Scan(&source, &hash); err != nil || source != "git" || hash != p4ClaimHash {
		t.Fatalf("uncontested commit source/hash=%q/%q err=%v", source, hash, err)
	}

	seedP4ClaimSession(t, s, "p4-claim-b", "owner-b", base.Add(time.Hour))
	peerGeneration := dirtyGeneration(t, s, "p4-claim-a")
	if peerGeneration == genA {
		t.Fatal("new cross-session claim did not dirty the original session")
	}
	run, err = s.processSessionFact(ctx, "p4-claim-a", peerGeneration)
	if err != nil || run.Outcome != "written" {
		t.Fatalf("conflicted publish %+v, %v", run, err)
	}
	var state string
	var total *int
	if err := s.pool.QueryRow(ctx, `SELECT commits_state,commit_count FROM session_analysis_facts WHERE session_id='p4-claim-a'`).Scan(&state, &total); err != nil || state != "undeterminable" || total != nil {
		t.Fatalf("conflict state/count=%q/%v err=%v", state, total, err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT source,hash FROM session_commit_facts WHERE session_id='p4-claim-a'`).Scan(&source, &hash); err != nil || source != "call" || hash != "" {
		t.Fatalf("conflicted commit leaked full claim: %q/%q err=%v", source, hash, err)
	}

	if _, err := s.DeleteSession(ctx, "p4-claim-b", "admin", "remove duplicate claim", false, false); err != nil {
		t.Fatal(err)
	}
	restoredGeneration := dirtyGeneration(t, s, "p4-claim-a")
	if restoredGeneration == peerGeneration {
		t.Fatal("deleting the competing claimant did not dirty the original session")
	}
	run, err = s.processSessionFact(ctx, "p4-claim-a", restoredGeneration)
	if err != nil || run.Outcome != "written" {
		t.Fatalf("restored publish %+v, %v", run, err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT commits_state,commit_count FROM session_analysis_facts WHERE session_id='p4-claim-a'`).Scan(&state, &total); err != nil || state != "observed" || total == nil || *total != 1 {
		t.Fatalf("restored state/count=%q/%v err=%v", state, total, err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT source,hash FROM session_commit_facts WHERE session_id='p4-claim-a'`).Scan(&source, &hash); err != nil || source != "git" || hash != p4ClaimHash {
		t.Fatalf("restored commit source/hash=%q/%q err=%v", source, hash, err)
	}
}
