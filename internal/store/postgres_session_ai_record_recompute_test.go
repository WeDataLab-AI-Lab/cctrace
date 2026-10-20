package store

import (
	"context"
	"testing"

	"cctrace/internal/sessionrecord"
)

func TestSessionFactRecomputePreservesRecordAndClaimUntilSourceDeletion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "synthetic-record-recompute"
	seedTimelineClaude(t, s, id)
	if run, err := s.processSessionFact(ctx, id, dirtyGeneration(t, s, id)); err != nil || run.Outcome != "written" {
		t.Fatalf("initial publish=%+v, %v", run, err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_ai_records
		(session_id,outcome,agent,owner_user_id,owner_profile_email,prompt_version,facts_hash,facts_rules_version,basis_hash,
		generated_at,covers_until,headline,content,search_text)
		SELECT session_id,'ok',agent,owner_user_id,owner_profile_email,$2,output_hash,rules_version,decode(repeat('02',32),'hex'),
		now(),last_activity_at,'Synthetic saved summary',
		'{"bullets":[],"review":[],"work":[],"skipped":[],"new_terms":{"component":[],"screen":[]},"gaps":[]}'::jsonb,
		'Synthetic saved summary' FROM session_analysis_facts WHERE session_id=$1`, id, sessionrecord.PromptVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_ai_record_jobs(session_id,lane,claim,claimed_at)
		VALUES($1,'manual',43,now())`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,parent_uuid,model,raw)
		VALUES('2026-09-01 09:00:03+00',$1,'assistant','claude','owner','profile','a2','a1','claude-opus-5',
		'{"cctraceCollect":1,"message":{"content":[{"type":"text","text":"Synthetic later activity"}]}}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if run, err := s.processSessionFact(ctx, id, requeueTimeline(t, s, id)); err != nil || run.Outcome != "written" {
		t.Fatalf("changed publish=%+v, %v", run, err)
	}
	if n := countWhere(t, s, "session_ai_records", "session_id=$1", id); n != 1 {
		t.Fatalf("ordinary recompute removed saved record: count=%d", n)
	}
	var claim int64
	if err := s.pool.QueryRow(ctx, `SELECT claim FROM session_ai_record_jobs WHERE session_id=$1`, id).Scan(&claim); err != nil || claim != 43 {
		t.Fatalf("ordinary recompute removed or replaced claim: claim=%d err=%v", claim, err)
	}
	response, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner",
		RecordFeatureEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	record := response["record"].(map[string]interface{})
	if record["state"] != "stale" || record["reason"] != "new_activity" || record["headline"] != "Synthetic saved summary" {
		t.Fatalf("saved record after new activity=%v", record)
	}
	// Source removal must still erase both the saved content and its publish fence.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := deleteSessionFacts(ctx, tx, []string{id}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"session_ai_records", "session_ai_record_jobs"} {
		if n := countWhere(t, s, table, "session_id=$1", id); n != 0 {
			t.Errorf("source deletion retained %s: count=%d", table, n)
		}
	}
}
