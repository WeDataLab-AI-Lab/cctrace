package store

import (
	"context"
	"testing"

	"cctrace/internal/sessionfacts"
)

func TestSessionOverviewProcessIOUnknownStatusDoesNotCountAsRunning(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "overview-process-io-unknown"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'owner','owner@example.com','codex',now(),'{}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "codex")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, sessionfacts.Compute(load.Input), 1)
	if err != nil || outcome != "written" {
		t.Fatalf("publish=%s, %v", outcome, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET last_activity_at=now()-interval '1 hour' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		VALUES('user','owner',$1,now()-interval '1 hour',now(),true,'codex')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, sessionOverviewRollupBackfill); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states
		(session_id,user_id,profile_email,agent,project_hash,pid,proc_start,status,status_source,heartbeat_at,identity_provenance,
		 io_epoch,io_as_of,io_source,io_mode,input_bytes,input_lines,output_bytes,output_lines)
		VALUES($1,'owner','owner@example.com','codex','ph',91,'start','unknown','process_io_unknown',now(),'personal_token',
		 'epoch',now(),'process_stream','pty',0,0,0,0)`, id); err != nil {
		t.Fatal(err)
	}
	f := SessionOverviewFilter{UserID: "owner", PersonalUserID: "owner", DashboardUserID: 1, AnalysisOwner: true, IncludeAnalysis: true, Limit: 10}
	counts, err := s.SessionOverviewCategoryCounts(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if counts["running"] != 0 {
		t.Fatalf("unknown process state was counted as running: %v", counts)
	}
	f.AnalysisCategory = "running"
	rows, err := s.ListSessionOverviews(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("running filter returned process with unknown status: %+v", rows)
	}
}
