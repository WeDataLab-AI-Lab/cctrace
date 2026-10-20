package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func claimSourceRecord(id, uuid string, at time.Time) *SessionRecord {
	return &SessionRecord{SessionID: id, UUID: uuid, Ts: at, RecordType: "user", ProfileEmail: "p", Raw: json.RawMessage(`{"message":{"content":"synthetic"}}`)}
}

func seedClaimSourcePeers(t *testing.T, s *PgStore, ids ...string) {
	t.Helper()
	var snapshots []*SessionSnapshot
	for _, id := range ids {
		snapshots = append(snapshots, claimSnapshot(id, "p", "repo", strings.Repeat("a", 40), time.Now()))
	}
	ingestObservations(t, s, SessionObservationBatch{Snapshots: snapshots})
}

func clearClaimSourceDirty(t *testing.T, s *PgStore) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `DELETE FROM session_fact_dirty`); err != nil {
		t.Fatal(err)
	}
}

func assertClaimSourceDirty(t *testing.T, s *PgStore, id string, reason int, wantFrom *int64) {
	t.Helper()
	var reasons int
	var from *int64
	if err := s.pool.QueryRow(context.Background(), `SELECT reasons,scan_from_id FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&reasons, &from); err != nil {
		t.Fatal(err)
	}
	if reasons&reason != reason {
		t.Fatalf("%s reasons=%d want bits=%d", id, reasons, reason)
	}
	if (from == nil) != (wantFrom == nil) || from != nil && *from != *wantFrom {
		t.Fatalf("%s scan offset=%v want=%v", id, from, wantFrom)
	}
}

func TestClaimSourceInsertReenrichAndNoops(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedClaimSourcePeers(t, s, "subject", "peer")
	r := claimSourceRecord("subject", "u", time.Now().UTC().Truncate(time.Microsecond))
	zero := int64(0)
	for i := 0; i < 2; i++ {
		clearClaimSourceDirty(t, s)
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{r}); err != nil {
			t.Fatal(err)
		}
		assertClaimSourceDirty(t, s, "subject", 1|256, &zero)
		assertClaimSourceDirty(t, s, "peer", 256, &zero)
	}
	if countWhere(t, s, "session_records", "session_id='subject'") != 1 {
		t.Fatal("duplicate insert changed row count")
	}
	r.Raw = json.RawMessage(`{"toolUseResult":{"stdout":"synthetic commit result"},"cctraceCollect":1}`)
	r.ToolUseID = "new-call"
	r.AccountID = "observed-account"
	for i := 0; i < 2; i++ {
		clearClaimSourceDirty(t, s)
		if n, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{r}); err != nil || n != 1 {
			t.Fatalf("reenrich=%d %v", n, err)
		}
		assertClaimSourceDirty(t, s, "subject", 2|256, &zero)
		assertClaimSourceDirty(t, s, "peer", 256, &zero)
	}
	var result, call, account string
	if err := s.pool.QueryRow(ctx, `SELECT raw->'toolUseResult'->>'stdout',tool_use_id,account_id FROM session_records WHERE session_id='subject'`).Scan(&result, &call, &account); err != nil {
		t.Fatal(err)
	}
	if result != "synthetic commit result" || call != "new-call" || account != "observed-account" {
		t.Fatal("content/call/account enrichment missing")
	}
	if countWhere(t, s, "session_commit_claims", "repository='repo' AND commit_hash=$1", strings.Repeat("a", 40)) != 2 {
		t.Fatal("record writer changed claim keys")
	}
}

func TestClaimSourceInsertOffsetsAndTombstones(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	at := time.Now().UTC()
	seedClaimSourcePeers(t, s, "subject", "peer")
	if _, err := s.pool.Exec(ctx, `INSERT INTO deleted_sessions(session_id) VALUES('gone')`); err != nil {
		t.Fatal(err)
	}
	records := []*SessionRecord{claimSourceRecord("z", "z2", at), claimSourceRecord("subject", "s", at), claimSourceRecord("a", "a1", at), claimSourceRecord("z", "z1", at.Add(time.Second)), claimSourceRecord("gone", "g", at)}
	clearClaimSourceDirty(t, s)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "z"} {
		var from int64
		if err := s.pool.QueryRow(ctx, `SELECT min(id) FROM session_records WHERE session_id=$1`, id).Scan(&from); err != nil {
			t.Fatal(err)
		}
		assertClaimSourceDirty(t, s, id, 1, &from)
	}
	if countWhere(t, s, "session_records", "session_id='gone'") != 0 || countWhere(t, s, "session_fact_dirty", "session_id='gone'") != 0 {
		t.Fatal("tombstone was written or queued")
	}
	clearClaimSourceDirty(t, s)
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}
	assertClaimSourceDirty(t, s, "a", 1, nil)
	assertClaimSourceDirty(t, s, "z", 1, nil)
	if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{records[4]}); err != nil {
		t.Fatal(err)
	}
	if countWhere(t, s, "session_fact_dirty", "session_id='gone'") != 0 {
		t.Fatal("reenrich queued tombstone")
	}
}

func TestClaimSourceRollbackAndCancellation(t *testing.T) {
	for _, mode := range []string{"insert-fail", "reenrich-fail", "insert-cancel", "reenrich-cancel"} {
		t.Run(mode, func(t *testing.T) {
			s := acquireTestStore(t)
			truncateTables(t, s)
			ctx := context.Background()
			seedClaimSourcePeers(t, s, "a-subject", "z-peer")
			r := claimSourceRecord("a-subject", "u", time.Now().UTC().Truncate(time.Microsecond))
			reenrich := strings.HasPrefix(mode, "reenrich")
			if reenrich {
				if err := s.InsertSessionRecords(ctx, []*SessionRecord{r}); err != nil {
					t.Fatal(err)
				}
				r.ToolUseID = "must-rollback"
				r.Raw = json.RawMessage(`{"toolUseResult":{"stdout":"must rollback"}}`)
			}
			clearClaimSourceDirty(t, s)
			body := `RAISE EXCEPTION 'synthetic dirty failure';`
			if strings.HasSuffix(mode, "cancel") {
				body = `PERFORM pg_sleep(2);`
			}
			_, err := s.pool.Exec(ctx, `CREATE FUNCTION claim_source_dirty_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.session_id='z-peer' THEN `+body+` END IF; RETURN NEW; END $$;
    CREATE TRIGGER claim_source_dirty_failure BEFORE INSERT OR UPDATE ON session_fact_dirty FOR EACH ROW EXECUTE FUNCTION claim_source_dirty_failure()`)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS claim_source_dirty_failure ON session_fact_dirty; DROP FUNCTION IF EXISTS claim_source_dirty_failure()`); err != nil {
					t.Error(err)
				}
			})
			bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			if reenrich {
				_, err = s.ReenrichSessionRecords(bounded, []*SessionRecord{r})
			} else {
				err = s.InsertSessionRecords(bounded, []*SessionRecord{r})
			}
			if err == nil {
				t.Fatal("failure/cancellation accepted")
			}
			// The blocked peer is last: earlier source, derived and self-dirty changes
			// have already happened inside the transaction and must all roll back.
			barrier, err := s.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = lockSessionDeletionTargets(ctx, barrier, []string{"a-subject"}); err != nil {
				t.Fatal(err)
			}
			_ = barrier.Rollback(ctx)
			if countWhere(t, s, "session_fact_dirty", "true") != 0 {
				t.Fatal("failed transaction left dirty state")
			}
			if reenrich {
				if countWhere(t, s, "session_records", "tool_use_id='must-rollback' OR raw ? 'toolUseResult'") != 0 {
					t.Fatal("failed enrichment persisted")
				}
			} else {
				if countWhere(t, s, "session_records", "session_id='a-subject'") != 0 || countWhere(t, s, "session_overview_rollups", "session_id='a-subject'") != 0 {
					t.Fatal("failed insert persisted source/derived state")
				}
			}
		})
	}
}

func TestClaimSourceConcurrentFirstClaimAndDelete(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(map[bool]string{false: "first-claim", true: "delete"}[deleting], func(t *testing.T) {
			s := acquireTestStore(t)
			truncateTables(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			seedClaimSourcePeers(t, s, "subject")
			r := claimSourceRecord("subject", "u", time.Now().UTC())
			clearClaimSourceDirty(t, s)
			start := make(chan struct{})
			errs := make(chan error, 2)
			go func() { <-start; errs <- s.InsertSessionRecords(ctx, []*SessionRecord{r}) }()
			go func() {
				<-start
				if deleting {
					errs <- s.DeleteUserData(ctx, "p", "")
					return
				}
				_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{claimSnapshot("new-peer", "p", "repo", strings.Repeat("a", 40), time.Now())}})
				errs <- err
			}()
			close(start)
			for i := 0; i < 2; i++ {
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
			if !deleting {
				zero := int64(0)
				assertClaimSourceDirty(t, s, "subject", 1|256, &zero)
				if countWhere(t, s, "session_commit_claims", "true") != 2 {
					t.Fatal("first claimant lost")
				}
			} else if countWhere(t, s, "session_commit_claims", "true") != 0 {
				t.Fatal("delete left claims behind")
			}
		})
	}
}

func TestClaimSourceConcurrentReverseBatches(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	seedClaimSourcePeers(t, s, "a", "z", "peer")
	at := time.Now().UTC().Truncate(time.Microsecond)
	a, z := claimSourceRecord("a", "a", at), claimSourceRecord("z", "z", at)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{a, z}); err != nil {
		t.Fatal(err)
	}
	clearClaimSourceDirty(t, s)
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		for i := 0; i < 6; i++ {
			aa, zz := *a, *z
			if err := s.InsertSessionRecords(ctx, []*SessionRecord{&zz, &aa}); err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}()
	go func() {
		<-start
		for i := 0; i < 6; i++ {
			aa, zz := *a, *z
			aa.ToolUseID = "call-a"
			zz.ToolUseID = "call-z"
			if _, err := s.ReenrichSessionRecords(ctx, []*SessionRecord{&aa, &zz}); err != nil {
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
	zero := int64(0)
	for _, id := range []string{"a", "z"} {
		assertClaimSourceDirty(t, s, id, 1|2|256, &zero)
	}
	assertClaimSourceDirty(t, s, "peer", 256, &zero)
}
