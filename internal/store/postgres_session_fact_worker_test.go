package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

// computeSessionFactTimeline computes the unchanged base facts with the
// timeline, commits and task actions under their shared output freshness.
func computeSessionFactTimeline(in sessionfacts.Input) (sessionfacts.Output, *sessionFactTimeline) {
	extended := sessionfacts.ComputeWithTimelineAndCommits(in)
	return extended.Output, &sessionFactTimeline{
		Facts: extended.Timeline, HasTimeline: true, Commits: extended.Commits, Tasks: extended.Tasks,
		Hash: extended.TimelineHash,
	}
}

func sessionFactOutputForTest(load SessionFactLoad) sessionfacts.Output {
	if load.Truncated != nil {
		return *load.Truncated
	}
	return sessionfacts.Compute(load.Input)
}

func TestSessionFactBuildQueueAndFailureStatus(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	enqueued, done := sessionFactBuildMarkers()
	_, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name IN ($1,$2,'session_fact_copies_v1')`, enqueued, done)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name IN ($1,$2,'session_fact_copies_v1')`, enqueued, done)
	})
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot) VALUES
		('b','owner','profile','claude',now(),'{}'::jsonb),
		('a','owner','profile','codex',now(),'{}'::jsonb)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_record_copies(srec_id,session_id,original_session_id,record_type,ts)
		VALUES(99001,'b','a','assistant',now())`); err != nil {
		t.Fatal(err)
	}
	if err := s.enqueueSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	var firstGeneration int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = 'a' AND reasons & 512 <> 0 AND priority = 1 AND scan_from_id = 0`).Scan(&firstGeneration); err != nil {
		t.Fatal(err)
	}
	if err := s.enqueueSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	var secondGeneration int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = 'a'`).Scan(&secondGeneration); err != nil || secondGeneration != firstGeneration {
		t.Fatalf("restart generation = %d, %v; want %d", secondGeneration, err, firstGeneration)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.SessionFactStatus(ctx)
	if err != nil || status.Ready || status.CopyGate || status.Build.Remaining != 2 || status.Build.State != "building" {
		t.Fatalf("build pending: %+v, %v", status, err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE session_fact_dirty SET attempts = 10, last_error = 'compute_or_publish' WHERE session_id = 'a'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id = 'b'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ('request_set_v1'),('session_commit_claims_v1') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.SessionFactStatus(ctx)
	if err != nil || status.Ready || !status.CopyGate || status.Build.State != "building" || status.Build.Failed != 1 || status.Build.Remaining != 1 {
		t.Fatalf("copy-gate second wave: %+v, %v", status, err)
	}
	var copyGateReason bool
	if err := s.pool.QueryRow(ctx, `SELECT (reasons & 1024) <> 0 FROM session_fact_dirty WHERE session_id='b'`).Scan(&copyGateReason); err != nil || !copyGateReason {
		t.Fatalf("copygate did not queue persisted copy markers: %v, %v", copyGateReason, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id='b'`); err != nil {
		t.Fatal(err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.SessionFactStatus(ctx)
	if err != nil || !status.Ready || status.Build.State != "done_with_failures" || status.Build.Failed != 1 || len(status.Build.FailedSessions) != 1 {
		t.Fatalf("build failed status: %+v, %v", status, err)
	}
}

func TestSessionFactPublishRejectsChangedCopyGeneration(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-new-generation"
	const peer = "fact-new-generation-copy-peer"
	ts := time.Date(2026, 10, 9, 1, 2, 3, 0, time.UTC)
	for _, row := range []struct {
		session, message string
	}{
		{id, "source request"},
		{peer, "copied request"},
	} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,message_id,raw)
			VALUES($1,$2,'assistant','claude','owner','profile','shared-copy-uuid',$3,
			jsonb_build_object('cctraceCollect',2,'message',jsonb_build_object('content',jsonb_build_array(jsonb_build_object('type','text','text',$4::text)))))`,
			ts, row.session, row.session+"-message", row.message); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,scan_from_id) VALUES ($1,0)`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if load.CopyGroup == nil || load.CopyGroup.Skipped {
		t.Fatalf("copy group = %+v", load.CopyGroup)
	}
	var peerLoaded bool
	var peerGenerationBefore int64
	for _, member := range load.CopyGroup.Members {
		if member == peer {
			peerLoaded = true
		}
	}
	for _, member := range load.CopyGroup.Generations {
		if member.SessionID == peer {
			peerGenerationBefore = member.Generation
		}
	}
	if !peerLoaded || peerGenerationBefore != 0 {
		t.Fatalf("copy group does not contain unqueued peer: members=%v generations=%+v", load.CopyGroup.Members, load.CopyGroup.Generations)
	}
	output := sessionFactOutputForTest(load)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, peer); err != nil {
		t.Fatal(err)
	}
	var peerGeneration int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = $1`, peer).Scan(&peerGeneration); err != nil {
		t.Fatal(err)
	}
	if peerGeneration == peerGenerationBefore {
		t.Fatalf("peer generation did not change: before=%d after=%d", peerGenerationBefore, peerGeneration)
	}
	var ownGeneration int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&ownGeneration); err != nil {
		t.Fatal(err)
	}
	if ownGeneration != load.Generation {
		t.Fatalf("the publisher's generation changed: loaded=%d current=%d", load.Generation, ownGeneration)
	}
	outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, output, 2)
	if err != nil || outcome != "copy_source" {
		t.Fatalf("publish = %q, %v", outcome, err)
	}
	if n := countWhere(t, s, "session_analysis_facts", "session_id=$1", id); n != 0 {
		t.Fatalf("old copy-group witness published %d facts", n)
	}
	var queuedGeneration int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&queuedGeneration); err != nil {
		t.Fatal(err)
	}
	if queuedGeneration != load.Generation {
		t.Fatalf("publisher generation moved: queued=%d loaded=%d", queuedGeneration, load.Generation)
	}
	fresh, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	var freshPeerGeneration int64
	for _, member := range fresh.CopyGroup.Generations {
		if member.SessionID == peer {
			freshPeerGeneration = member.Generation
		}
	}
	if freshPeerGeneration != peerGeneration {
		t.Fatalf("fresh copy-group peer generation=%d want=%d", freshPeerGeneration, peerGeneration)
	}
	if outcome, err = s.publishSessionFact(ctx, id, fresh.Generation, fresh, sessionFactOutputForTest(fresh), 1); err != nil || outcome != "written" {
		t.Fatalf("fresh witness publish = %q, %v", outcome, err)
	}
	var computedGeneration int64
	if err := s.pool.QueryRow(ctx, `SELECT source_generation FROM session_analysis_facts WHERE session_id = $1`, id).Scan(&computedGeneration); err != nil || computedGeneration != queuedGeneration {
		t.Fatalf("fresh publish generation=%d want=%d err=%v", computedGeneration, queuedGeneration, err)
	}
	requeueTimeline(t, s, id)
	stale, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	var loadedPeer bool
	for _, member := range stale.CopyGroup.Members {
		if member == peer {
			loadedPeer = true
		}
	}
	if !loadedPeer {
		t.Fatalf("stale publish has no peer in copy group: %+v", stale.CopyGroup)
	}
	staleOutput := sessionFactOutputForTest(stale)
	nextOwnGeneration := requeueTimeline(t, s, id)
	outcome, err = s.publishSessionFact(ctx, id, stale.Generation, stale, staleOutput, 1)
	if err != nil || outcome != "copy_source" {
		t.Fatalf("peer-group self-generation change publish = %q, %v", outcome, err)
	}
	var queuedOwnGeneration int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&queuedOwnGeneration); err != nil || queuedOwnGeneration != nextOwnGeneration {
		t.Fatalf("peer-group self-generation queue=%d want=%d err=%v", queuedOwnGeneration, nextOwnGeneration, err)
	}
	requeueTimeline(t, s, id)
	fresh, err = s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = s.publishSessionFact(ctx, id, fresh.Generation, fresh, sessionFactOutputForTest(fresh), 1)
	if err != nil || outcome != "unchanged" {
		t.Fatalf("same hash publish = %q, %v", outcome, err)
	}
	var count int64
	if err := s.pool.QueryRow(ctx, `SELECT recompute_count FROM session_analysis_facts WHERE session_id = $1`, id).Scan(&count); err != nil || count != 2 {
		t.Fatalf("recompute_count = %d, %v", count, err)
	}
}

func TestSessionFactPublishWaitsForSourceDeletion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-source-lock"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	holder, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock(hashtext('session_overview_source'), 0)`); err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	go func() {
		outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, sessionFactOutputForTest(load), 1)
		if err != nil {
			result <- err.Error()
		} else {
			result <- outcome
		}
	}()
	select {
	case got := <-result:
		t.Fatalf("published while source deletion held the lock: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := holder.Exec(ctx, `INSERT INTO deleted_sessions(session_id,deleted_by,reason) VALUES ($1,'admin','test')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('session_overview_source'), 0)`); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got != "tombstone" {
			t.Fatalf("publisher after deletion = %s, want tombstone", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publisher did not resume after source unlock")
	}
}

func TestSessionFactPublishWaitsForRetentionMutation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-retention-lock"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	holder, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock(hashtext('session_overview_retention'))`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.publishSessionFact(ctx, id, load.Generation, load, sessionFactOutputForTest(load), 1)
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("published while retention was mutating: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('session_overview_retention'))`); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publisher did not resume after retention unlock")
	}
}

func TestSessionFactPublishRejectsChangedIdentity(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-identity-change"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'other','profile','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, sessionFactOutputForTest(load), 1)
	if err != nil || outcome != "identity" {
		t.Fatalf("changed owner publish = %q, %v", outcome, err)
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_analysis_facts WHERE session_id = $1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("changed owner fact count = %d, %v", count, err)
	}
}

func TestSessionFactExcludedPublishDeletesOldFactsAndDirty(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-excluded-cleanup"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	out := sessionFactOutputForTest(load)
	if outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, out, 1); err != nil || outcome != "written" {
		t.Fatalf("first publish %q, %v", outcome, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, out, 1)
	if err != nil || outcome != "stale" {
		t.Fatalf("already processed dirty %q, %v", outcome, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	// A queue generation is part of the copy witness. Reload the complete
	// snapshot after requeueing; replacing only load.Generation leaves the old
	// copy-group generation and correctly fails the publish fence.
	load, err = s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = s.publishSessionFact(ctx, id, generation, load, out, 1)
	if err != nil || outcome != "excluded" {
		t.Fatalf("excluded after read publish %q, %v", outcome, err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_analysis_facts WHERE session_id = $1)
		+ (SELECT count(*) FROM session_fact_dirty WHERE session_id = $1)`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("excluded facts and dirty after cleanup %d, %v", n, err)
	}
}

func TestSessionFactPublishRejectsRetentionAndRemoval(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-retention"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_overview_retention_state(table_name,last_run_finished)
		VALUES ('session_records', now()) ON CONFLICT (table_name) DO UPDATE SET last_run_finished = now()`)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, sessionFactOutputForTest(load), 1)
	if err != nil || outcome != "retention" {
		t.Fatalf("retention publish = %q, %v", outcome, err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE session_fact_dirty SET generation = nextval('session_fact_generation'),
		removal_generation = nextval('session_fact_generation') WHERE session_id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = s.publishSessionFact(ctx, id, load.Generation, load, sessionFactOutputForTest(load), 1)
	if err != nil || outcome != "removal" {
		t.Fatalf("removal publish = %q, %v", outcome, err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_analysis_facts WHERE session_id = $1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stale fact count = %d, %v", n, err)
	}
}

func TestSessionFactDueQueueRespectsPriorityAndRetry(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id, priority, reasons, first_dirty_at, not_before, retry_at) VALUES
		('bulk',1,512,now()-interval '1 day',now(),NULL),
		('live-later',0,4,now()-interval '10 minutes',now(),NULL),
		('live-earlier',0,1,now()-interval '20 minutes',now(),NULL),
		('waiting',0,16,now()-interval '1 day',now()+interval '1 hour',NULL),
		('retrying',0,128,now()-interval '1 day',now(),now()+interval '1 hour')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"live-earlier", "live-later", "bulk"} {
		id, generation, err := s.pickDueSessionFact(ctx)
		if err != nil || id != want || generation <= 0 {
			t.Fatalf("due = %q generation %d, %v; want %q", id, generation, err, want)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.pickDueSessionFact(ctx); err != pgx.ErrNoRows {
		t.Fatalf("future due = %v, want no rows", err)
	}
}

func TestSessionFactPickerPreservesScheduleAndDrainsHistoricalDemand(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,reasons,priority,not_before) VALUES
		('historical-demand',262144,1,now()+interval '1 hour'),('ordinary-dirty',4,0,now())`); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := s.pool.QueryRow(ctx, `SELECT json_agg(d ORDER BY session_id)::text FROM session_fact_dirty d`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if id, _, err := s.pickDueSessionFact(ctx); err != nil || id != "ordinary-dirty" {
			t.Fatalf("ordinary due row=%s, %v", id, err)
		}
	}
	if err := s.pool.QueryRow(ctx, `SELECT json_agg(d ORDER BY session_id)::text FROM session_fact_dirty d`).Scan(&after); err != nil || before != after {
		t.Fatalf("picker changed queue metadata: before=%s after=%s err=%v", before, after, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id='ordinary-dirty'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.pickDueSessionFact(ctx); err != pgx.ErrNoRows {
		t.Fatalf("historical demand skipped its debounce: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_fact_dirty SET not_before=now() WHERE session_id='historical-demand'`); err != nil {
		t.Fatal(err)
	}
	if id, _, err := s.pickDueSessionFact(ctx); err != nil || id != "historical-demand" {
		t.Fatalf("historical due demand=%s, %v", id, err)
	}
}

func TestSessionAnalysisReadDoesNotEnqueueForeignSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "lazy-foreign-session"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot) VALUES($1,'owner','profile','claude',now(),'{}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "other", OwnerUserID: "other"}); err != ErrSessionAnalysisNotFound {
		t.Fatalf("foreign read error=%v", err)
	}
	var queued bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_fact_dirty WHERE session_id=$1)`, id).Scan(&queued); err != nil || queued {
		t.Fatalf("foreign session queued=%v, err=%v", queued, err)
	}
}

func TestSessionAnalysisReadReusesCurrentFacts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "lazy-current-cache"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot) VALUES($1,'owner','profile','claude',now(),'{}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	output, timeline := computeSessionFactTimeline(load.Input)
	if outcome, err := s.publishSessionFactWithTimeline(ctx, id, load.Generation, load, output, timeline, 1); err != nil || outcome != "written" {
		t.Fatalf("publish=%s, err=%v", outcome, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET coverage='truncated' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"})
		if err != nil {
			t.Fatal(err)
		}
		if result["state"] != "truncated" || result["analysis_pending"] != false {
			t.Fatalf("current truncated cache state/pending=%v/%v", result["state"], result["analysis_pending"])
		}
	}
	var queued bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_fact_dirty WHERE session_id=$1)`, id).Scan(&queued); err != nil || queued {
		t.Fatalf("current cached analysis was requeued=%v, err=%v", queued, err)
	}
}

func TestSessionAnalysisUnsupportedIsNotDemanded(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "lazy-unsupported"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,agent,start_time,end_time,has_sync)
		VALUES ('all','',$1,'gjc',now(),now(),true)`, id); err != nil {
		t.Fatal(err)
	}
	result, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{})
	if err != nil || result["state"] != "unsupported" || result["analysis_pending"] != false {
		t.Fatalf("unsupported response=%v err=%v", result, err)
	}
	var queued bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_fact_dirty WHERE session_id=$1)`, id).Scan(&queued); err != nil || queued {
		t.Fatalf("unsupported session queued=%v err=%v", queued, err)
	}
}

func TestSessionFactStatusReportsWaitingWithoutFakingGlobalReadiness(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	enqueued, done := sessionFactBuildMarkers()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name IN ($1,$2,'session_fact_copies_v1','request_set_v1','session_commit_claims_v1')`, enqueued, done); err != nil {
		t.Fatal(err)
	}
	status, err := s.SessionFactStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != "build" || status.Ready || status.Build.State != "waiting" || !status.WaitingForRequestSet || !status.WaitingForCommitClaims || status.CopyGate {
		t.Fatalf("waiting status falsely reports global readiness: %+v", status)
	}
	if err := s.enqueueSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.SessionFactStatus(ctx)
	if err != nil || status.Mode != "build" || status.Ready || status.Build.State != "building" {
		t.Fatalf("explicit build status=%+v err=%v", status, err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.SessionFactStatus(ctx)
	if err != nil || status.Ready || status.Build.State != "done" || !status.CopyGate || !status.WaitingForRequestSet || !status.WaitingForCommitClaims {
		t.Fatalf("missing global markers falsely report ready: %+v err=%v", status, err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ('request_set_v1') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	status, err = s.SessionFactStatus(ctx)
	if err != nil || status.Ready || status.WaitingForRequestSet || !status.WaitingForCommitClaims {
		t.Fatalf("missing commit claims marker falsely reports ready: %+v err=%v", status, err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ('session_commit_claims_v1') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	status, err = s.SessionFactStatus(ctx)
	if err != nil || !status.Ready || status.Build.State != "done" || !status.CopyGate || status.WaitingForRequestSet || status.WaitingForCommitClaims {
		t.Fatalf("explicit build completion status=%+v err=%v", status, err)
	}
}

func TestSessionFactIdentityMatchesVisibleAgentAndByteOrder(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-identity-order"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []struct{ user, profile string }{{"z", "z"}, {"ä", "ä"}} {
		_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
			VALUES ($1,$2,$3,'claude',now(),'{}'::jsonb)`, id, identity.user, identity.profile)
		if err != nil {
			t.Fatal(err)
		}
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, sessionFactOutputForTest(load), 1)
	if err != nil || outcome != "written" {
		t.Fatalf("byte order identity publish %q, %v", outcome, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO excluded_accounts(login_email) VALUES ('hidden@example.test')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,login_email,observed_at,snapshot)
		VALUES ($1,'excluded','excluded','claude','hidden@example.test',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err = s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = s.publishSessionFact(ctx, id, load.Generation, load, sessionFactOutputForTest(load), 1)
	if err != nil || outcome != "unchanged" {
		t.Fatalf("excluded snapshot identity publish %q, %v", outcome, err)
	}
}

func TestSessionFactMixedAgentIdentityUsesChosenSource(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-mixed-agent-identity"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []struct{ agent, user, profile string }{{"claude", "owner-a", "profile-a"}, {"codex", "owner-b", "profile-b"}} {
		_, err = s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,raw)
			VALUES (now(),$1,'user',$2,$3,$4,'{}'::jsonb)`, id, source.agent, source.user, source.profile)
		if err != nil {
			t.Fatal(err)
		}
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil || load.SourceAgent != "claude" {
		t.Fatalf("chosen agent %q, %v", load.SourceAgent, err)
	}
	output := sessionFactOutputForTest(load)
	if len(output.Session.UserIDs) != 1 || output.Session.UserIDs[0] != "owner-a" {
		t.Fatalf("unexpected selected source identity: %v", output.Session.UserIDs)
	}
	outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, output, 1)
	if err != nil || outcome != "written" {
		t.Fatalf("mixed-agent publish %q, %v", outcome, err)
	}
	var users []string
	if err := s.pool.QueryRow(ctx, `SELECT user_ids FROM session_analysis_facts WHERE session_id = $1`, id).Scan(&users); err != nil || !sameStrings(users, []string{"owner-a"}) {
		t.Fatalf("published selected identity %v, %v", users, err)
	}
}

func TestSessionFactFallbackMixedAgentsAndOpenCopyGate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-mixed-fallback-gate"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []struct{ agent, user, profile string }{{"claude", "owner-a", "profile-a"}, {"codex", "owner-b", "profile-b"}} {
		_, err = s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,raw)
			VALUES (now(),$1,'user',$2,$3,$4,'{}'::jsonb)`, id, source.agent, source.user, source.profile)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ('session_fact_copies_v1') ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name = 'session_fact_copies_v1'`)
	})
	load, err := s.loadSessionFactsInput(ctx, id, "claude", 0)
	if err != nil || load.Truncated == nil || !load.Input.CopyGate || load.SourceAgent != "claude" {
		t.Fatalf("fresh fallback gate=%v agent=%q truncated=%v err=%v", load.Input.CopyGate, load.SourceAgent, load.Truncated != nil, err)
	}
	if !sameStrings(load.Truncated.Session.UserIDs, []string{"owner-a", "owner-b"}) {
		t.Fatalf("fallback identity %v", load.Truncated.Session.UserIDs)
	}
	outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, *load.Truncated, 1)
	if err != nil || outcome != "written" {
		t.Fatalf("fallback mixed agent with gate publish %q, %v", outcome, err)
	}
	var users []string
	var coverage string
	if err := s.pool.QueryRow(ctx, `SELECT user_ids, coverage FROM session_analysis_facts WHERE session_id = $1`, id).Scan(&users, &coverage); err != nil || coverage != "truncated" || !sameStrings(users, []string{"owner-a", "owner-b"}) {
		t.Fatalf("fallback fact users=%v coverage=%q err=%v", users, coverage, err)
	}
}

func TestSessionFactComputeFailurePreservesFactsAndRetry(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	const id = "fact-compute-failure"
	seedTimelineClaude(t, s, id)
	if run, err := s.processSessionFact(ctx, id, dirtyGeneration(t, s, id)); err != nil || run.Outcome != "written" {
		t.Fatalf("seed run %+v, %v", run, err)
	}
	for _, cause := range []error{context.DeadlineExceeded, context.Canceled, errors.New("resource unavailable")} {
		generation := requeueTimeline(t, s, id)
		run, err := s.processSessionFactWithSnapshotCompute(ctx, id, generation,
			func(context.Context, string, string) (SessionFactLoad, sessionfacts.TimelineCommitOutput, error) {
				return SessionFactLoad{}, sessionfacts.TimelineCommitOutput{}, cause
			})
		if !errors.Is(err, cause) || run.Outcome != "" {
			t.Fatalf("failure published %+v, %v", run, err)
		}
		if n := countWhere(t, s, "session_analysis_facts", "session_id=$1 AND coverage='full'", id); n != 1 {
			t.Fatal("failed computation replaced the existing full facts")
		}
		if m, p := timelineCounts(t, s, id); m != 2 || p != 1 {
			t.Fatalf("failure cleared existing timeline: %d %d", m, p)
		}
		if attempts, err := s.failSessionFact(ctx, id, generation, cause); err != nil || attempts < 1 {
			t.Fatalf("retry=%d, %v", attempts, err)
		}
		if n := countWhere(t, s, "session_fact_dirty", "session_id=$1 AND retry_at>now()", id); n != 1 {
			t.Fatal("failed computation has no durable retry")
		}
	}
}

func TestSessionFactSourceAgentFencedAtSnapshotAndPublish(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-source-agent-race"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id, reasons) VALUES ($1,17)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil || load.SourceAgent != "claude" {
		t.Fatalf("initial agent %q, %v", load.SourceAgent, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,raw)
		VALUES (now(),$1,'user','codex','owner','profile','{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM session_snapshots WHERE session_id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.publishSessionFact(ctx, id, generation, load, sessionFactOutputForTest(load), 1)
	if err != nil || outcome != "source" {
		t.Fatalf("changed agent publish %q, %v", outcome, err)
	}
	fresh, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil || fresh.SourceAgent != "codex" || fresh.Generation != generation {
		t.Fatalf("new snapshot agent %q generation %d, %v", fresh.SourceAgent, fresh.Generation, err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dirty after agent race %d, %v", n, err)
	}
}

func TestSessionFactChangedSourceAgentLeavesDirtyForRetry(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-agent-read-race"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	// The first agent choice was claude; before the RR source read another
	// writer changes the visible source without bumping this picked generation.
	_, err = s.pool.Exec(ctx, `DELETE FROM session_snapshots WHERE session_id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','codex',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil || load.Generation != generation || load.SourceAgent != "codex" {
		t.Fatalf("RR agent %q generation %d, %v", load.SourceAgent, load.Generation, err)
	}
	if load.SourceAgent == "claude" {
		t.Fatal("stale agent unexpectedly matched")
	}
	if err := s.deferSessionFact(ctx, id, generation); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("source race dirty remains %d, %v", n, err)
	}
}

func TestSessionFactAbortDefersWithoutChangingDirtyMetadata(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id, priority, reasons, scan_from_id, attempts, last_error)
		VALUES ('aborted',0,262161,42,3,'timeout'), ('other',0,262144,NULL,0,'')`)
	if err != nil {
		t.Fatal(err)
	}
	id, generation, err := s.pickDueSessionFact(ctx)
	if err != nil || id != "aborted" {
		t.Fatalf("first pick %q, %v", id, err)
	}
	if err := s.deferSessionFact(ctx, id, generation); err != nil {
		t.Fatal(err)
	}
	id, _, err = s.pickDueSessionFact(ctx)
	if err != nil || id != "other" {
		t.Fatalf("next pick %q, %v, want other", id, err)
	}
	var attempts, reasons int
	var scan int64
	var lastError string
	var notBefore, retryAt time.Time
	if err := s.pool.QueryRow(ctx, `SELECT attempts, reasons, scan_from_id, last_error, not_before, retry_at
		FROM session_fact_dirty WHERE session_id = 'aborted'`).Scan(&attempts, &reasons, &scan, &lastError, &notBefore, &retryAt); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || reasons != 262161 || scan != 42 || lastError != "timeout" || !notBefore.After(time.Now()) || !retryAt.After(time.Now()) {
		t.Fatalf("abort mutated dirty metadata: attempts=%d reasons=%d scan=%d error=%s not_before=%s retry=%s", attempts, reasons, scan, lastError, notBefore, retryAt)
	}
}

func TestSessionFactEmptyPublishKeepsLiveSourceAndGeneration(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "fact-empty-race"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id, reasons, scan_from_id) VALUES ($1,17,42)`, id)
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES ($1,'owner','profile','codex',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := s.publishEmptySessionFact(ctx, id, generation)
	if err != nil || outcome != "source" {
		t.Fatalf("live source publish %q, %v", outcome, err)
	}
	var reasons int
	var scan int64
	if err := s.pool.QueryRow(ctx, `SELECT reasons, scan_from_id FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&reasons, &scan); err != nil || reasons != 17 || scan != 42 {
		t.Fatalf("dirty preserved: reasons=%d scan=%d err=%v", reasons, scan, err)
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM session_snapshots WHERE session_id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE session_fact_dirty SET generation = nextval('session_fact_generation') WHERE session_id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err = s.publishEmptySessionFact(ctx, id, generation)
	if err != nil || outcome != "stale" {
		t.Fatalf("stale generation publish %q, %v", outcome, err)
	}
}

func TestSessionFactFailureStoresOnlyStage(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ('failure')`)
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id = 'failure'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.failSessionFact(ctx, "failure", generation, context.DeadlineExceeded)
	if err != nil || attempts != 1 {
		t.Fatalf("failure count = %d, %v", attempts, err)
	}
	var stage string
	if err := s.pool.QueryRow(ctx, `SELECT last_error FROM session_fact_dirty WHERE session_id = 'failure'`).Scan(&stage); err != nil || !strings.EqualFold(stage, "timeout") {
		t.Fatalf("failure stage = %q, %v", stage, err)
	}
}
