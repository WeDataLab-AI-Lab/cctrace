package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSessionFactsResetRejectsAnActiveWorker(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, sessionFactWorkerLock); err != nil {
		t.Fatal(err)
	}
	_, err = s.ResetSessionFacts(ctx)
	if !errors.Is(err, ErrSessionFactsWorkerActive) {
		t.Fatalf("reset error = %v, want active worker refusal", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtext($1))`, sessionFactWorkerLock); err != nil {
		t.Fatal(err)
	}
}

func TestSessionFactsResetRebuildsDerivedDataThenClears_P1b_State(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "session-facts-reset"
	seedFacts(t, s, id, factsRow{})
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_record_copies(srec_id,session_id,original_session_id,record_type,ts)
		VALUES(99002,$1,'other','assistant',now())`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES
		('session_fact_copies_v1'),('session_facts_r5_enqueued'),('session_facts_r5') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_timeline_revisions(session_id,revision) VALUES($1,1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_timeline_members
		(session_id,source_ts,source_record_type,source_profile_email,source_uuid,source_record_id,turn_key)
		VALUES($1,$2,'user','','u',1,'turn')`, id, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_commit_facts(session_id,ordinal,subject_state,committed_at,kind,source,rules_version)
		VALUES($1,1,'none',now(),'commit','output_hash',4)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_task_facts(session_id,item_key,kind,display_id,title_state,first_ts,rules_version)
		VALUES($1,'task','task','1','none',now(),4)`, id); err != nil {
		t.Fatal(err)
	}

	report, err := s.ResetSessionFacts(ctx)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if !report.CopyGateClosed || report.AnalysisRows != 0 || report.TurnRows != 0 || report.ActivityRows != 0 ||
		report.CommitRows != 0 || report.TaskRows != 0 || report.CopyRows != 0 || report.TimelineRows != 0 ||
		report.DirtyRows != 0 || report.BuildMarkers != 0 {
		t.Fatalf("reset report = %+v", report)
	}
	var hourly, coverage, plugins int
	for table, target := range map[string]*int{
		"usage_hourly_rollups":      &hourly,
		"coverage_measured_minutes": &coverage,
		"plugin_invocation_facts":   &plugins,
	} {
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if hourly != 0 || coverage != 0 || plugins != 0 {
		t.Fatalf("derived rows after empty-source reset: hourly=%d coverage=%d plugins=%d", hourly, coverage, plugins)
	}
	repeated, err := s.ResetSessionFacts(ctx)
	if err != nil || !repeated.CopyGateClosed || repeated.AnalysisRows != 0 || repeated.TimelineRows != 0 || repeated.BuildMarkers != 0 {
		t.Fatalf("repeated reset = %+v, %v", repeated, err)
	}
}
