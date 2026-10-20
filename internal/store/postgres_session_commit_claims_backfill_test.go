package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func claimBackfillStore(t *testing.T) *PgStore {
	t.Helper()
	s := acquireTestStore(t)
	truncateTables(t, s)
	clear := func() {
		if _, err := s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name=$1`, sessionCommitClaimsBackfill); err != nil {
			t.Fatal(err)
		}
	}
	clear()
	t.Cleanup(clear)
	return s
}
func claimBackfillDone(t *testing.T, s *PgStore) bool {
	t.Helper()
	return countWhere(t, s, "schema_backfills", "name=$1", sessionCommitClaimsBackfill) == 1
}
func drainClaimBackfill(t *testing.T, s *PgStore) {
	t.Helper()
	for i := 0; i < 20; i++ {
		done, err := s.BackfillSessionCommitClaims(context.Background(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
	}
	t.Fatal("backfill did not finish")
}
func TestClaimBackfillCoverageResumeAndParity(t *testing.T) {
	s := claimBackfillStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	h := strings.Repeat("a", 40)
	empty := claimSnapshot("empty", "p", "repo", h, at)
	empty.Snapshot = json.RawMessage(`{}`)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{empty, claimSnapshot("positive", "p", "repo", h, at)}})
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_commit_claims`); err != nil {
		t.Fatal(err)
	}
	worked, done, err := s.backfillSessionCommitClaimsBatch(ctx, 1)
	if err != nil || !worked || done {
		t.Fatalf("batch=(%v,%v,%v)", worked, done, err)
	}
	if countWhere(t, s, "session_commit_claim_coverage", "session_id='empty'") != 1 || claimBackfillDone(t, s) {
		t.Fatal("zero claim coverage missing or premature marker")
	}
	var databaseHash, raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT sha256(convert_to(snapshot::text,'UTF8')),convert_to(snapshot::text,'UTF8') FROM session_snapshots WHERE session_id='empty'`).Scan(&databaseHash, &raw); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(raw)
	if !bytes.Equal(databaseHash, want[:]) {
		t.Fatal("built-in SHA256 differs")
	}
	// Restart from durable coverage, not a high-water cursor.
	worked, _, err = s.backfillSessionCommitClaimsBatch(ctx, 1)
	if err != nil || !worked {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_commit_claims SET commit_hash=$1 WHERE session_id='positive'`, strings.Repeat("b", 40)); err != nil {
		t.Fatal(err)
	}
	done, err = s.finalizeSessionCommitClaims(ctx)
	if err != nil || done || claimBackfillDone(t, s) {
		t.Fatalf("stale parity accepted: %v %v", done, err)
	}
	if countWhere(t, s, "session_commit_claim_coverage", "session_id='positive'") != 0 {
		t.Fatal("bad parity coverage not invalidated")
	}
	drainClaimBackfill(t, s)
	if countWhere(t, s, "session_commit_claims", "commit_hash=$1", h) != 1 {
		t.Fatal("stale index not repaired")
	}
}

func TestClaimBackfillCompletionRequeuesEveryPublishedWitness(t *testing.T) {
	s := lifecycleStore(t)
	ctx := context.Background()
	const id = "claims-gate-original"
	seedTimelineClaude(t, s, id)
	if run, err := s.processSessionFact(ctx, id, dirtyGeneration(t, s, id)); err != nil || run.Outcome != "written" {
		t.Fatalf("initial publish %+v %v", run, err)
	}
	enqueued, done := sessionFactBuildMarkers()
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1),($2),('session_fact_copies_v1')`, enqueued, done); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	if err := s.pool.QueryRow(ctx, `SELECT epoch FROM session_timeline_policy_epoch`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if complete, err := s.finalizeSessionCommitClaims(ctx); err != nil || !complete {
		t.Fatalf("claims finalization %v %v", complete, err)
	}
	if n := countWhere(t, s, "session_fact_dirty", "session_id=$1 AND reasons & 1024<>0", id); n != 1 {
		t.Fatal("claims completion omitted a published original with no snapshot or copy marker")
	}
	if n := countWhere(t, s, "schema_backfills", "name=$1", done); n != 0 {
		t.Fatal("claims transition kept a false build-complete marker")
	}
	var generation, nextEpoch int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT epoch FROM session_timeline_policy_epoch`).Scan(&nextEpoch); err != nil || nextEpoch != epoch+1 {
		t.Fatalf("claims epoch %d -> %d, %v", epoch, nextEpoch, err)
	}
	if complete, err := s.finalizeSessionCommitClaims(ctx); err != nil || !complete {
		t.Fatalf("repeat claims finalization %v %v", complete, err)
	}
	if queued := dirtyGeneration(t, s, id); queued != generation {
		t.Fatal("already-complete claims requeued the population")
	}
}

func TestClaimBackfillSourceChangeDeleteAndReappear(t *testing.T) {
	s := claimBackfillStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	h := strings.Repeat("a", 40)
	sn := claimSnapshot("changing", "p", "repo", h, at)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{sn}})
	if _, _, err := s.backfillSessionCommitClaimsBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	older := claimSnapshot("changing", "p", "repo", strings.Repeat("b", 40), at.Add(-time.Second))
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{older}})
	worked, _, err := s.backfillSessionCommitClaimsBatch(ctx, 1)
	if err != nil || worked {
		t.Fatalf("rejected old invalidated coverage: %v %v", worked, err)
	}
	changed := claimSnapshot("changing", "p", "repo", strings.Repeat("b", 40), at)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{changed}})
	done, err := s.finalizeSessionCommitClaims(ctx)
	if err != nil || done {
		t.Fatalf("same timestamp change accepted: %v %v", done, err)
	}
	if _, _, err := s.backfillSessionCommitClaimsBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	changed.ObservedAt = at.Add(time.Second)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{changed}})
	if worked, _, err := s.backfillSessionCommitClaimsBatch(ctx, 1); err != nil || !worked {
		t.Fatalf("timestamp-only change not rediscovered: %v %v", worked, err)
	}
	if err := s.DeleteUserData(ctx, "p", ""); err != nil {
		t.Fatal(err)
	}
	if countWhere(t, s, "session_commit_claim_coverage", "true") != 0 {
		t.Fatal("coverage did not cascade")
	}
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{sn}})
	if countWhere(t, s, "session_commit_claim_coverage", "true") != 0 {
		t.Fatal("reappearance reused coverage")
	}
	drainClaimBackfill(t, s)
}

func TestClaimBackfillRollbackCancellationAndEmpty(t *testing.T) {
	s := claimBackfillStore(t)
	ctx := context.Background()
	h := strings.Repeat("a", 40)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("rollback", "p", "repo", h, time.Now())}})
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_commit_claims;
 CREATE FUNCTION claim_coverage_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'coverage failure'; END $$;
 CREATE TRIGGER claim_coverage_fail BEFORE INSERT ON session_commit_claim_coverage FOR EACH ROW EXECUTE FUNCTION claim_coverage_fail()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS claim_coverage_fail ON session_commit_claim_coverage; DROP FUNCTION IF EXISTS claim_coverage_fail()`); err != nil {
			t.Error(err)
		}
	})
	before := claimGeneration(t, s, "rollback")
	_, err := s.BackfillSessionCommitClaims(ctx, 1)
	if err == nil {
		t.Fatal("injected failure accepted")
	}
	if countWhere(t, s, "session_commit_claims", "true") != 0 || countWhere(t, s, "session_commit_claim_coverage", "true") != 0 || claimGeneration(t, s, "rollback") != before || claimBackfillDone(t, s) {
		t.Fatal("failed batch left partial changes")
	}
	if _, err := s.pool.Exec(ctx, `DROP TRIGGER claim_coverage_fail ON session_commit_claim_coverage; DROP FUNCTION claim_coverage_fail()`); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if done, err := s.finalizeSessionCommitClaims(cancelled); err == nil || done || claimBackfillDone(t, s) {
		t.Fatal("cancelled finalization published marker")
	}
	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if err = lockSessionOverviewSourceMutation(ctx, holder, false); err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	done, err := s.finalizeSessionCommitClaims(bounded)
	if err == nil || done || time.Since(started) > time.Second || claimBackfillDone(t, s) {
		t.Fatalf("blocked finalize not bounded: %v %v", done, err)
	}
	_ = holder.Rollback(ctx)
	if err = s.DeleteUserData(ctx, "p", ""); err != nil {
		t.Fatal(err)
	}
	drainClaimBackfill(t, s)
}

func TestClaimBackfillConcurrentMutation(t *testing.T) {
	s := claimBackfillStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	at := time.Now().UTC()
	h := strings.Repeat("a", 40)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("race", "p", "repo", h, at)}})
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		for i := 0; i < 8; i++ {
			_, _, err := s.backfillSessionCommitClaimsBatch(ctx, 1)
			if err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	go func() {
		<-start
		for i := 0; i < 8; i++ {
			if err := s.DeleteUserData(ctx, "p", ""); err != nil {
				errs <- err
				return
			}
			_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("race", "p", "repo", h, at.Add(time.Duration(i)*time.Second))}})
			if err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	drainClaimBackfill(t, s)
	if countWhere(t, s, "session_commit_claim_coverage", "session_id='race'") != 1 || countWhere(t, s, "session_commit_claims", "session_id='race'") != 1 {
		t.Fatal("race lost current identity")
	}
}

func TestClaimBackfillMissingAndExtraClaims(t *testing.T) {
	for _, mode := range []string{"missing", "extra"} {
		t.Run(mode, func(t *testing.T) {
			s := claimBackfillStore(t)
			ctx := context.Background()
			h := strings.Repeat("a", 40)
			ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
				claimSnapshot("subject", "p", "repo", h, time.Now()),
				claimSnapshot("peer", "p", "repo", h, time.Now()),
			}})
			if _, _, err := s.backfillSessionCommitClaimsBatch(ctx, 2); err != nil {
				t.Fatal(err)
			}
			before := claimGeneration(t, s, "peer")
			var err error
			if mode == "missing" {
				_, err = s.pool.Exec(ctx, `DELETE FROM session_commit_claims WHERE session_id='subject'`)
			} else {
				_, err = s.pool.Exec(ctx, `INSERT INTO session_commit_claims SELECT session_id,user_id,profile_email,'extra',commit_hash FROM session_commit_claims WHERE session_id='subject'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if done, err := s.finalizeSessionCommitClaims(ctx); err != nil || done || claimBackfillDone(t, s) {
				t.Fatalf("parity=%v %v", done, err)
			}
			drainClaimBackfill(t, s)
			if countWhere(t, s, "session_commit_claims", "session_id='subject'") != 1 || claimGeneration(t, s, "peer") <= before {
				t.Fatal("repair did not restore exact claims and dirty peer")
			}
		})
	}
}

func TestClaimBackfillTombstoneAndIdentityCascade(t *testing.T) {
	s := claimBackfillStore(t)
	ctx := context.Background()
	h := strings.Repeat("a", 40)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		claimSnapshot("multi", "a", "repo", h, time.Now()),
		claimSnapshot("multi", "b", "repo", h, time.Now()),
	}})
	if _, _, err := s.backfillSessionCommitClaimsBatch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUserData(ctx, "a", ""); err != nil {
		t.Fatal(err)
	}
	if countWhere(t, s, "session_commit_claim_coverage", "profile_email='b'") != 1 || countWhere(t, s, "session_commit_claim_coverage", "profile_email='a'") != 0 {
		t.Fatal("identity cascade removed surviving identity")
	}
	// Synthetic pre-backfill corruption: tombstone with a stale indexed claim.
	if _, err := s.pool.Exec(ctx, `INSERT INTO deleted_sessions(session_id) VALUES('multi')`); err != nil {
		t.Fatal(err)
	}
	if done, err := s.finalizeSessionCommitClaims(ctx); err != nil || done {
		t.Fatalf("tombstone parity=%v %v", done, err)
	}
	drainClaimBackfill(t, s)
	if countWhere(t, s, "session_commit_claims", "true") != 0 || countWhere(t, s, "session_commit_claim_coverage", "true") != 0 {
		t.Fatal("tombstone claims or coverage survived repair")
	}
}

func TestClaimBackfillCancellationDuringBatch(t *testing.T) {
	s := claimBackfillStore(t)
	ctx := context.Background()
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("cancel", "p", "repo", strings.Repeat("a", 40), time.Now())}})
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_commit_claims;
		CREATE FUNCTION claim_coverage_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1); RETURN NEW; END $$;
		CREATE TRIGGER claim_coverage_wait BEFORE INSERT ON session_commit_claim_coverage FOR EACH ROW EXECUTE FUNCTION claim_coverage_wait()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS claim_coverage_wait ON session_commit_claim_coverage; DROP FUNCTION IF EXISTS claim_coverage_wait()`); err != nil {
			t.Error(err)
		}
	})
	before := claimGeneration(t, s, "cancel")
	bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if done, err := s.BackfillSessionCommitClaims(bounded, 1); err == nil || done {
		t.Fatalf("cancelled batch=%v %v", done, err)
	}
	if countWhere(t, s, "session_commit_claims", "true") != 0 || countWhere(t, s, "session_commit_claim_coverage", "true") != 0 || claimGeneration(t, s, "cancel") != before || claimBackfillDone(t, s) {
		t.Fatal("cancelled batch persisted partial changes")
	}
	if _, err := s.pool.Exec(ctx, `DROP TRIGGER claim_coverage_wait ON session_commit_claim_coverage; DROP FUNCTION claim_coverage_wait()`); err != nil {
		t.Fatal(err)
	}
	drainClaimBackfill(t, s)
}

func TestClaimBackfillFinalizationSyntheticCost(t *testing.T) {
	s := claimBackfillStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,snapshot,observed_at)
 SELECT 'cost-'||g,'u','p',CASE WHEN g%2=0 THEN '{}'::jsonb ELSE
		'{"commits":{"trees":[{"repository":"cost"}],"items":[{"tree":0,"hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","proof":"output_hash"}]}}'::jsonb END,
		now() FROM generate_series(1,2000) g`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := s.backfillSessionCommitClaimsBatch(ctx, 1000); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	done, err := s.finalizeSessionCommitClaims(ctx)
	elapsed := time.Since(started)
	t.Logf("final source-exclusive coverage/parity scan: 2000 snapshots, %s (budget %s)", elapsed, claimFinalizeBudget)
	if err != nil || !done || elapsed > claimFinalizeBudget {
		t.Fatalf("finalize=%v err=%v elapsed=%v", done, err, elapsed)
	}
}
