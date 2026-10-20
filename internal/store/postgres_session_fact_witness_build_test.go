package store

import (
	"context"
	"testing"
	"time"
)

// This lifecycle test resets shared tables and build markers, so it is serial.
func TestSessionFactWitnessUpgradeRebuildsOnceAcrossRestart(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name)
		VALUES('session_fact_copies_v1'),('session_commit_claims_v1'),('request_set_v1'),
		('session_facts_r6_enqueued'),('session_facts_r6')`); err != nil {
		t.Fatal(err)
	}
	const id = "old-witness-facts"
	seedTimelineClaude(t, s, id)
	if run, err := s.processSessionFact(ctx, id, dirtyGeneration(t, s, id)); err != nil || run.Outcome != "written" {
		t.Fatalf("publish r6 baseline: %+v, %v", run, err)
	}
	for _, sql := range []string{
		`UPDATE session_timeline_provenance SET source_witness=sha256('previous-format'::bytea) WHERE session_id=$1`,
		`INSERT INTO session_timeline_provenance(session_id,tracking,source_revision,policy_epoch,source_witness,source_rows,
			mapping_version,timeline_state,timeline_reason,timeline_hash,fact_output_hash,source_generation,rules_version)
		SELECT 'old-witness-provenance',tracking,source_revision,policy_epoch,
			source_witness,source_rows,mapping_version,timeline_state,timeline_reason,timeline_hash,fact_output_hash,source_generation,rules_version
		FROM session_timeline_provenance WHERE session_id=$1`,
	} {
		if _, err := s.pool.Exec(ctx, sql, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,agent,start_time,end_time,has_sync)
		VALUES('all','','old-witness-overview','claude',now(),now(),true)`); err != nil {
		t.Fatal(err)
	}
	if countWhere(t, s, "session_fact_dirty", "TRUE") != 0 || countWhere(t, s, "session_analysis_facts", "session_id=$1 AND coverage='full' AND rules_version=6", id) != 1 {
		t.Fatal("baseline must be completed r6 facts with no dirty work")
	}
	if err := s.enqueueSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	for _, queued := range []string{id, "old-witness-provenance", "old-witness-overview"} {
		if countWhere(t, s, "session_fact_dirty", "session_id=$1 AND reasons & 512<>0 AND scan_from_id=0", queued) != 1 {
			t.Fatalf("witness upgrade did not queue %s without a GET", queued)
		}
	}
	queueState := func() string {
		t.Helper()
		var state string
		if err := s.pool.QueryRow(ctx, `SELECT string_agg(row_to_json(d)::text,E'\n' ORDER BY session_id COLLATE "C") FROM session_fact_dirty d`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	beforeRestart := queueState()
	restarted := timelineStore(t)
	if err := restarted.enqueueSessionFactBuild(ctx); err != nil || queueState() != beforeRestart {
		t.Fatalf("restart repeated the witness build queue/generation bump: %v", err)
	}
	if err := restarted.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	if status, err := restarted.SessionFactStatus(ctx); err != nil || status.Ready || status.Build.Remaining != 3 {
		t.Fatalf("old r6 completion hid pending witness rebuild: %+v, %v", status, err)
	}
	conn, err := restarted.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	completed := 0
	work := func() {
		t.Helper()
		workerCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		restarted.runSessionFactLocked(workerCtx, conn, func(time.Duration) <-chan time.Time {
			cancel() // Stop at idle; no timer or GET initiates this rebuild.
			ch := make(chan time.Time)
			close(ch)
			return ch
		}, func(run SessionFactRun, err error) {
			if err != nil {
				t.Fatalf("automatic witness rebuild: %+v, %v", run, err)
			}
			completed++
		})
	}
	work()
	if completed != 3 || countWhere(t, s, "session_fact_dirty", "TRUE") != 0 || !timelineValid(t, s, id) {
		t.Fatalf("automatic witness rebuild incomplete: processed=%d", completed)
	}
	if status, err := restarted.SessionFactStatus(ctx); err != nil || !status.Ready || status.RulesVersion != 6 {
		t.Fatalf("witness rebuild readiness/rules: %+v, %v", status, err)
	}
	work() // A second worker startup must observe the durable current marker.
	if completed != 3 || countWhere(t, s, "session_fact_dirty", "TRUE") != 0 {
		t.Fatal("completed witness build repeated after another startup")
	}
}
