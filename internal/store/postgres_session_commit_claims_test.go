package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func claimSnapshot(id, profile, repository, hash string, at time.Time) *SessionSnapshot {
	return &SessionSnapshot{SessionID: id, UserID: "claim-user", ProfileEmail: profile, Agent: "claude", ObservedAt: at,
		Snapshot: json.RawMessage(fmt.Sprintf(`{"commits":{"trees":[{"repository":%q}],"items":[{"hash":%q,"tree":0,"proof":"output_hash","at":1759836153,"parents":1,"kind":"commit","call":"claim-call","transcript":"claim.jsonl","subject":"not indexed"}]}}`, repository, hash))}
}

func claimGeneration(t *testing.T, s *PgStore, id string) int64 {
	t.Helper()
	var n int64
	if err := s.pool.QueryRow(context.Background(), `SELECT generation FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSnapshotClaimsRequiresOutputHashProof(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	at := time.Now().UTC()
	h := strings.Repeat("a", 40)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("valid", "p", "repo", h, at)}})
	before := claimGeneration(t, s, "valid")
	for _, proof := range []string{"", "time_window"} {
		snapshot := claimSnapshot("invalid-"+proof, "p", "repo", h, at)
		replacement := ""
		if proof != "" {
			replacement = `"proof":"` + proof + `",`
		}
		snapshot.Snapshot = json.RawMessage(strings.Replace(string(snapshot.Snapshot), `"proof":"output_hash",`, replacement, 1))
		if len(snapshotClaimKeys(snapshot.Snapshot)) != 0 {
			t.Errorf("unverified proof %q produced claim keys", proof)
		}
		ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{snapshot}})
		if countWhere(t, s, "session_commit_claims", "session_id=$1", snapshot.SessionID) != 0 {
			t.Errorf("unverified proof %q indexed", proof)
		}
		if claimGeneration(t, s, "valid") != before {
			t.Errorf("unverified proof %q dirtied valid peer", proof)
		}
	}
	if countWhere(t, s, "session_commit_claims", "session_id='valid'") != 1 {
		t.Fatal("valid proof lost")
	}
}

func TestSnapshotClaimsWriter(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	h1, h2 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		claimSnapshot("peer-old", "p", "repo", h1, at), claimSnapshot("peer-new", "p", "repo", h2, at),
		claimSnapshot("self", "p", "repo", h1, at), claimSnapshot("self", "other", "other-repo", h1, at),
	}})
	old, next := claimGeneration(t, s, "peer-old"), claimGeneration(t, s, "peer-new")
	if got := ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("self", "p", "repo", h2, at.Add(-time.Second))}}); got != 0 {
		t.Fatalf("old stored=%d", got)
	}
	if claimGeneration(t, s, "peer-old") != old || claimGeneration(t, s, "peer-new") != next {
		t.Fatal("rejected snapshot dirtied peers")
	}
	if got := ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		claimSnapshot("self", "p", "repo", h1, at.Add(time.Second)),
		claimSnapshot("self", "p", "repo", h2, at.Add(time.Second)),
		claimSnapshot("self", "p", "repo", h1, at),
	}}); got != 2 {
		t.Fatalf("batch stored=%d", got)
	}
	if claimGeneration(t, s, "peer-old") <= old || claimGeneration(t, s, "peer-new") <= next {
		t.Fatal("old and new peers must both be dirtied")
	}
	if countWhere(t, s, "session_commit_claims", "session_id='self' AND profile_email='p' AND commit_hash=$1", h2) != 1 || countWhere(t, s, "session_commit_claims", "session_id='self'") != 2 {
		t.Fatal("winner or separate identity lost")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	complete, err := sessionCommitClaimsComplete(ctx, tx)
	if err := deleteSessionFacts(ctx, tx, []string{"self"}); err != nil {
		t.Fatal(err)
	}
	var claimsAfterFactInvalidation int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM session_commit_claims WHERE session_id='self'`).Scan(&claimsAfterFactInvalidation); err != nil {
		t.Fatal(err)
	}
	if claimsAfterFactInvalidation != 2 {
		t.Fatal("ordinary fact invalidation deleted source claims")
	}
	_ = tx.Rollback(ctx)
	if err != nil || complete {
		t.Fatalf("unbackfilled index complete=%v err=%v", complete, err)
	}
	if err := s.DeleteUserData(ctx, "p", ""); err != nil {
		t.Fatal(err)
	}
	if countWhere(t, s, "session_commit_claims", "session_id='self'") != 1 {
		t.Fatal("partial identity deletion removed survivor")
	}
}

func TestSnapshotClaimsRollbackAndRetry(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Now().UTC()
	h := strings.Repeat("a", 40)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("peer", "p", "repo", h, at)}})
	before := claimGeneration(t, s, "peer")
	_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{
		Snapshots: []*SessionSnapshot{claimSnapshot("rollback", "p", "repo", h, at)},
		Events:    []*SessionObservationEvent{{SessionID: "rollback", EventID: "bad", Kind: "session_end", ObservedAt: at, Payload: json.RawMessage(`{`)}},
	})
	if err == nil {
		t.Fatal("invalid event accepted")
	}
	if countWhere(t, s, "session_snapshots", "session_id='rollback'") != 0 || countWhere(t, s, "session_commit_claims", "session_id='rollback'") != 0 || claimGeneration(t, s, "peer") != before {
		t.Fatal("partial transaction survived")
	}
	// A sequence survives rollback: inject one serialization failure after a
	// snapshot write, then prove that the public boundary retries the whole TX.
	_, err = s.pool.Exec(ctx, `CREATE SEQUENCE claim_retry_test_seq;
		CREATE FUNCTION claim_retry_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.session_id='retry' AND nextval('claim_retry_test_seq')=1 THEN RAISE EXCEPTION 'retry test' USING ERRCODE='40001'; END IF;
		IF NEW.session_id='bounded' THEN PERFORM nextval('claim_retry_test_seq'); RAISE EXCEPTION 'bounded retry test' USING ERRCODE='40P01'; END IF;
		RETURN NEW; END $$;
		CREATE TRIGGER claim_retry_test AFTER INSERT ON session_commit_claims FOR EACH ROW EXECUTE FUNCTION claim_retry_test()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := s.pool.Exec(context.Background(), `DROP TRIGGER claim_retry_test ON session_commit_claims; DROP FUNCTION claim_retry_test(); DROP SEQUENCE claim_retry_test_seq`)
		if err != nil {
			t.Error(err)
		}
	})
	if got := ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("retry", "p", "repo", h, at)}}); got != 1 {
		t.Fatalf("retry stored=%d", got)
	}
	if countWhere(t, s, "session_commit_claims", "session_id='retry'") != 1 || claimGeneration(t, s, "peer") <= before {
		t.Fatal("retry did not atomically publish claims and dirty peer")
	}
	var attemptsBefore, attemptsAfter int64
	if err := s.pool.QueryRow(ctx, `SELECT last_value FROM claim_retry_test_seq`).Scan(&attemptsBefore); err != nil {
		t.Fatal(err)
	}
	result, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("bounded", "p", "repo", h, at)}})
	if err == nil || result.Stored != 0 {
		t.Fatalf("bounded retry returned partial success: %+v %v", result, err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT last_value FROM claim_retry_test_seq`).Scan(&attemptsAfter); err != nil {
		t.Fatal(err)
	}
	if attemptsAfter-attemptsBefore != 3 || countWhere(t, s, "session_snapshots", "session_id='bounded'") != 0 || countWhere(t, s, "session_commit_claims", "session_id='bounded'") != 0 {
		t.Fatal("bounded retry did not roll back all three attempts")
	}
}

func TestSnapshotClaimsCancelledIngest(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("cancelled", "p", "repo", strings.Repeat("a", 40), time.Now())}})
	if !errors.Is(err, context.Canceled) || result.Stored != 0 {
		t.Fatalf("cancelled ingest=(%+v,%v)", result, err)
	}
	for _, table := range []string{"session_snapshots", "session_commit_claims", "session_fact_dirty"} {
		if countWhere(t, s, table, "session_id='cancelled'") != 0 {
			t.Fatalf("cancelled ingest wrote %s", table)
		}
	}
}

func TestSnapshotClaimsMergeTombstoneAndSweep(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Now().UTC()
	h1, h2 := strings.Repeat("a", 40), strings.Repeat("b", 40)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		claimSnapshot("merged", "from", "repo", h1, at), claimSnapshot("merged", "to", "repo", h2, at.Add(time.Second)),
		claimSnapshot("peer", "p", "repo", h1, at), claimSnapshot("new-peer", "p", "repo", h2, at),
	}})
	before := claimGeneration(t, s, "peer")
	if err := s.MergeUsers(ctx, "from", "", "to"); err != nil {
		t.Fatal(err)
	}
	if countWhere(t, s, "session_commit_claims", "session_id='merged' AND profile_email='to' AND commit_hash=$1", h2) != 1 || countWhere(t, s, "session_commit_claims", "session_id='merged'") != 1 {
		t.Fatal("merge did not keep destination newer winner")
	}
	if claimGeneration(t, s, "peer") <= before {
		t.Fatal("merge lost old peer")
	}
	before = claimGeneration(t, s, "new-peer")
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = beginStrictDeletion(ctx, tx); err != nil {
		t.Fatal(err)
	}
	peers, err := tombstoneTx(ctx, tx, []string{"merged"}, "", "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = markSessionFactsDirty(ctx, tx, peers, nil, dirtyDeletionPeer); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if countWhere(t, s, "session_commit_claims", "session_id='merged'") != 0 || countWhere(t, s, "session_snapshots", "session_id='merged'") != 1 {
		t.Fatal("tombstone must remove claims before source sweep")
	}
	if claimGeneration(t, s, "new-peer") <= before {
		t.Fatal("tombstone did not dirty peer")
	}
	if _, err = s.sweepOne(ctx, "merged"); err != nil {
		t.Fatal(err)
	}
	if countWhere(t, s, "session_snapshots", "session_id='merged'") != 0 {
		t.Fatal("sweep left source")
	}
}

func TestSnapshotClaimsConcurrentBatches(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h1, h2 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	at := time.Now().UTC()
	for round := 0; round < 12; round++ {
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for worker := 0; worker < 2; worker++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				<-start
				ids := []string{"A", "B"}
				if worker == 1 {
					ids = []string{"B", "A"}
				}
				_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{
					claimSnapshot(ids[0], "p", "repo", h1, at.Add(time.Duration(round)*time.Second)),
					claimSnapshot(ids[1], "p", "repo", h2, at.Add(time.Duration(round)*time.Second)),
				}})
				errs <- err
			}(worker)
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	var mismatches int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_commit_claims c JOIN session_snapshots s USING(session_id,user_id,profile_email)
		WHERE c.commit_hash<>s.snapshot->'commits'->'items'->0->>'hash'`).Scan(&mismatches); err != nil {
		t.Fatal(err)
	}
	if mismatches != 0 || countWhere(t, s, "session_commit_claims", "true") != 2 {
		t.Fatal("concurrent source/index mismatch")
	}
}

func TestSnapshotClaimsPrune(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
	forceRetention(t, s, "session_records", ptrInt(7))
	at := time.Now().UTC()
	h := strings.Repeat("a", 40)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		claimSnapshot("partial", "old", "repo", h, at.Add(-10*24*time.Hour)),
		claimSnapshot("partial", "fresh", "other", h, at),
		claimSnapshot("peer", "p", "repo", h, at),
	}})
	before := claimGeneration(t, s, "peer")
	if n, err := s.PruneSessionObservations(ctx, at.Add(-SessionObservationEventRetention)); err != nil || n != 1 {
		t.Fatalf("prune=(%d,%v)", n, err)
	}
	if countWhere(t, s, "session_commit_claims", "session_id='partial' AND profile_email='fresh'") != 1 || countWhere(t, s, "session_commit_claims", "session_id='partial'") != 1 || claimGeneration(t, s, "peer") <= before {
		t.Fatal("prune lost surviving identity or old peer")
	}
}

func TestSnapshotClaimsConcurrentFirstClaimAndAdminSweep(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	at := time.Now().UTC()
	h := strings.Repeat("a", 40)
	var sequenceBefore int64
	if err := s.pool.QueryRow(ctx, `SELECT nextval('session_fact_generation')`).Scan(&sequenceBefore); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, id := range []string{"first-A", "first-B"} {
		go func(id string) {
			<-start
			_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot(id, "p", "repo", h, at)}})
			errs <- err
		}(id)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if countWhere(t, s, "session_commit_claims", "true") != 2 {
		t.Fatal("simultaneous first claims lost")
	}
	// The later claimant must observe the earlier one after the key fence and
	// increment its generation, rather than both reading an empty inverse index.
	var earliestDirty int64
	if err := s.pool.QueryRow(ctx, `SELECT min(generation) FROM session_fact_dirty`).Scan(&earliestDirty); err != nil {
		t.Fatal(err)
	}
	// ON CONFLICT consumes the INSERT default and the UPDATE nextval, so
	// sequence gaps are valid; neither row may retain the first generation.
	if earliestDirty <= sequenceBefore+1 {
		t.Fatalf("first claimant was not requeued: %d <= %d", earliestDirty, sequenceBefore+1)
	}
	if countWhere(t, s, "session_fact_dirty", "reasons & 256 <> 0") != 1 {
		t.Fatal("later claimant did not invalidate exactly the earlier peer")
	}
	if _, err := s.DeleteSession(ctx, "first-A", "test", "test", false, false); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	go func() { <-start; _, err := s.sweepOne(ctx, "first-A"); errs <- err }()
	go func() { <-start; errs <- s.DeleteUserData(ctx, "p", "") }()
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if countWhere(t, s, "session_commit_claims", "true") != 0 {
		t.Fatal("admin/sweep left claims")
	}
}

func TestSnapshotClaimFenceHashCollisions(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var keys []string
	if err := tx.QueryRow(ctx, `SELECT array_agg(k ORDER BY k COLLATE "C") FROM
		(SELECT g::text k, hashtext(g::text) h FROM generate_series(1,200000) g) candidates
		GROUP BY h HAVING count(*)>1 LIMIT 1`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	keys = append(keys, keys[0])
	if err := lockSnapshotClaimKeys(ctx, tx, 394, keys); err != nil {
		t.Fatal(err)
	}
	var locks int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND classid=394 AND granted`).Scan(&locks); err != nil {
		t.Fatal(err)
	}
	if locks != 1 {
		t.Fatalf("collision lock count=%d", locks)
	}
}

func TestSnapshotClaimKeysRejectMalformed(t *testing.T) {
	h := strings.Repeat("a", 40)
	for _, raw := range []string{`{`, `{"commits":{"items":[{"proof":"output_hash","hash":"` + h + `","tree":0}]}}`, `{"commits":{"trees":[{"repository":"repo"}],"items":[{"proof":"output_hash","hash":"abc","tree":0}]}}`, `{"commits":{"trees":[{"repository":"repo"}],"items":[{"proof":"output_hash","hash":"` + h + `"}]}}`} {
		if got := snapshotClaimKeys([]byte(raw)); len(got) != 0 {
			t.Fatalf("malformed accepted: %+v", got)
		}
	}
}
