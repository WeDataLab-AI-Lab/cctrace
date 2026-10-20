package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

// The package shares a truncating container, so these tests run serially.
func lifecycleStore(t *testing.T) *PgStore {
	t.Helper()
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const predicate = `name LIKE 'session_facts_r%' OR name IN ('session_fact_copies_v1','session_commit_claims_v1','request_set_v1')`
	rows, err := s.pool.Query(ctx, `SELECT name FROM schema_backfills WHERE `+predicate)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	clear := func() {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE `+predicate); err != nil {
			t.Fatal(err)
		}
	}
	clear()
	t.Cleanup(func() {
		clear()
		if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) SELECT unnest($1::text[]) ON CONFLICT DO NOTHING`, previous); err != nil {
			t.Fatal(err)
		}
	})
	return s
}

func TestSessionFactDueQueueProcessesEveryReason(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	for i, reason := range dirtyReasons {
		id := string(rune('a' + i))
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,reasons,priority,first_dirty_at)
			VALUES($1,$2,1,now()+make_interval(secs=>$3))`, id, reason.reason, i); err != nil {
			t.Fatal(err)
		}
	}
	for i := range dirtyReasons {
		want := string(rune('a' + i))
		id, generation, err := s.pickDueSessionFact(ctx)
		if err != nil || id != want {
			t.Fatalf("due reason %d: got %q/%d, %v; want %q", i, id, generation, err, want)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty WHERE session_id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionAnalysisPollingReadsQueueAndNone(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	const id = "read-without-demand"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,agent,start_time,end_time,has_sync)
		VALUES('all','',$1,'claude',now(),now(),true)`, id); err != nil {
		t.Fatal(err)
	}
	read := func(want string, pending bool) {
		t.Helper()
		for _, repoOnly := range []bool{false, true} {
			response, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{RepoStatusOnly: repoOnly})
			if err != nil {
				t.Fatal(err)
			}
			key := "state"
			if repoOnly {
				key = "analysis_state"
			}
			if response[key] != want || !repoOnly && response["analysis_pending"] != pending {
				t.Fatalf("repo=%v state/pending=%v/%v; want %s/%v", repoOnly, response[key], response["analysis_pending"], want, pending)
			}
		}
	}
	read("building", false)
	if n := countWhere(t, s, "session_fact_dirty", "session_id=$1", id); n != 0 {
		t.Fatal("GET enqueued missing facts")
	}
	_, done := sessionFactBuildMarkers()
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1)`, done); err != nil {
		t.Fatal(err)
	}
	read("none", false)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,reasons,priority,attempts,retry_at,not_before)
		VALUES($1,4,1,12,now()+interval '1 hour',now()+interval '2 hours')`, id); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := s.pool.QueryRow(ctx, `SELECT row_to_json(d)::text FROM session_fact_dirty d WHERE session_id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	read("building", true)
	read("building", true)
	if err := s.pool.QueryRow(ctx, `SELECT row_to_json(d)::text FROM session_fact_dirty d WHERE session_id=$1`, id).Scan(&after); err != nil || before != after {
		t.Fatalf("poll changed dirty metadata: before=%s after=%s err=%v", before, after, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_overview_rollups SET agent='gjc' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	read("unsupported", true)
	if _, err := s.pool.Exec(ctx, `INSERT INTO deleted_sessions(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	for _, repoOnly := range []bool{false, true} {
		if _, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{RepoStatusOnly: repoOnly}); err != ErrSessionAnalysisNotFound {
			t.Fatalf("tombstone lost precedence for repo=%v: %v", repoOnly, err)
		}
	}
}

func TestSessionOverviewMissingFactsStateRespectsPrivacyAndQueue(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	uid := personalFixture(t, s, "owner@example.com", "owner")
	for _, id := range []string{"no-analysis", "failed-analysis", "mixed-analysis"} {
		personalSource(t, s, id, "owner@example.com", "owner")
	}
	personalSource(t, s, "mixed-analysis", "foreign@example.com", "foreign")
	_, done := sessionFactBuildMarkers()
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1)`, done); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,reasons,attempts,retry_at)
		VALUES('failed-analysis',4,11,now()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []bool{false, true} {
		rows, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{
			UserID: "owner", PersonalUserID: "owner", DashboardUserID: uid,
			AnalysisOwner: owner, IncludeAnalysis: true, IncludeSessionRecords: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		states := map[string]interface{}{}
		for _, row := range rows {
			states[row.SessionID] = row.Analysis["state"]
		}
		if states["no-analysis"] != "none" || states["failed-analysis"] != "building" {
			t.Fatalf("owner=%v missing fact states=%v", owner, states)
		}
		if owner && states["mixed-analysis"] != "withheld" {
			t.Fatalf("privacy guard lost precedence: %v", states)
		}
	}
}

func TestSessionFactCopyGateRequeuesOriginalsAndDelaysCompletion(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	seedTimelineClaude(t, s, "gate-original")
	if run, err := s.processSessionFact(ctx, "gate-original", dirtyGeneration(t, s, "gate-original")); err != nil || run.Outcome != "written" {
		t.Fatalf("seed publish=%+v, %v", run, err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES('gate-unpublished','owner','profile','claude',now(),'{}')`); err != nil {
		t.Fatal(err)
	}
	if err := s.enqueueSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty;
		INSERT INTO schema_backfills(name) VALUES('request_set_v1'),('session_commit_claims_v1') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := s.pool.QueryRow(ctx, `SELECT epoch FROM session_timeline_policy_epoch`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := s.SessionFactStatus(ctx)
	if err != nil || !status.CopyGate || status.Ready || status.Build.State != "building" || status.Build.Remaining < 2 {
		t.Fatalf("gate opened with unfinished second wave: %+v %v", status, err)
	}
	for _, id := range []string{"gate-original", "gate-unpublished"} {
		if n := countWhere(t, s, "session_fact_dirty", "session_id=$1 AND reasons & 1024<>0", id); n != 1 {
			t.Fatalf("gate transition omitted %s", id)
		}
	}
	var nextEpoch int64
	if err := s.pool.QueryRow(ctx, `SELECT epoch FROM session_timeline_policy_epoch`).Scan(&nextEpoch); err != nil || nextEpoch != epoch+1 {
		t.Fatalf("policy epoch %d -> %d, %v", epoch, nextEpoch, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty`); err != nil {
		t.Fatal(err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = s.SessionFactStatus(ctx)
	if err != nil || !status.Ready || status.Build.State != "done" {
		t.Fatalf("second wave completion %+v, %v", status, err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, s, "session_fact_dirty", "TRUE"); n != 0 {
		t.Fatal("already open gate requeued the population")
	}
}

func TestSessionFactWorkerAutomaticallyBackfillsAndBuilds(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES('request_set_v1') ON CONFLICT DO NOTHING;
		INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES('automatic-build','owner','profile','claude',now(),'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES('automatic-build')`); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, "automatic-build", "claude")
	if err != nil {
		t.Fatal(err)
	}
	legacy := sessionfacts.Compute(load.Input)
	legacy.Session.Coverage, legacy.Session.RulesVersion = "truncated", 5
	legacy.Hash = sessionfacts.HashOutput(legacy)
	if outcome, err := s.publishSessionFact(ctx, "automatic-build", load.Generation, load, legacy, 1); err != nil || outcome != "written" {
		t.Fatalf("legacy publish %s, %v", outcome, err)
	}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var completed int
	s.runSessionFactLocked(workerCtx, conn, func(time.Duration) <-chan time.Time {
		cancel() // an idle boundary is enough; no wall-clock sleeps
		ch := make(chan time.Time)
		close(ch)
		return ch
	}, func(run SessionFactRun, err error) {
		if err != nil {
			t.Errorf("worker run %+v: %v", run, err)
		}
		if run.Outcome == "written" || run.Outcome == "unchanged" {
			completed++
		}
	})
	status, err := s.SessionFactStatus(ctx)
	if err != nil || !status.Ready || status.WaitingForCommitClaims || completed < 2 {
		t.Fatalf("automatic initial/second wave completions=%d status=%+v err=%v", completed, status, err)
	}
	if n := countWhere(t, s, "session_analysis_facts", "session_id='automatic-build' AND coverage='full' AND rules_version=6"); n != 1 {
		t.Fatal("automatic versioned build did not recover legacy truncated facts without a GET")
	}
}

func TestSessionFactNewVersionDoesNotRepeatOpenGateTransition(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES('session_fact_copies_v1'),('session_facts_r5');
		INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES('new-version-open-gate','owner','profile','claude',now(),'{}')`); err != nil {
		t.Fatal(err)
	}
	if err := s.enqueueSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty`); err != nil {
		t.Fatal(err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := s.pool.QueryRow(ctx, `SELECT epoch FROM session_timeline_policy_epoch`).Scan(&epoch); err != nil || epoch != 0 {
		t.Fatalf("already-open gate transitioned again: epoch=%d err=%v", epoch, err)
	}
	_, done := sessionFactBuildMarkers()
	if countWhere(t, s, "schema_backfills", "name=$1", done) != 1 || countWhere(t, s, "session_fact_dirty", "TRUE") != 0 {
		t.Fatal("new version repeated the gate transition instead of completing its own build")
	}
}

func TestSessionFactBusyQueueStillFinishesBuild(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	enqueued, done := sessionFactBuildMarkers()
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name)
		VALUES($1),('request_set_v1'),('session_commit_claims_v1'),('session_fact_copies_v1') ON CONFLICT DO NOTHING`, enqueued); err != nil {
		t.Fatal(err)
	}
	queue := func() {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,reasons) VALUES('ordinary-busy-queue',4)`); err != nil {
			t.Fatal(err)
		}
	}
	queue()
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := 0
	s.runSessionFactLocked(workerCtx, conn, func(time.Duration) <-chan time.Time {
		t.Error("worker reached idle before finishing under a busy queue")
		cancel()
		ch := make(chan time.Time)
		close(ch)
		return ch
	}, func(_ SessionFactRun, err error) {
		if err != nil {
			t.Fatal(err)
		}
		completed++
		if completed == 2 {
			if countWhere(t, s, "schema_backfills", "name=$1", done) != 1 {
				t.Error("busy queue postponed build completion until idle")
			}
			cancel()
			return
		}
		queue()
	})
}
