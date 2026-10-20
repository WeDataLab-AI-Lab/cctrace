package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func runLoginEmailClaimPass(ctx context.Context, s *PgStore, infer bool, since time.Time) (int64, error) {
	if infer {
		return s.InferSessionRecordLoginEmail(ctx, since)
	}
	return s.BackfillSessionRecordLoginEmail(ctx, since)
}

func seedLoginEmailClaimEvent(t *testing.T, s *PgStore, id string, at time.Time) {
	t.Helper()
	if err := s.InsertEvents(context.Background(), []*OtelEvent{{Ts: at, EventName: "api_request", SessionID: id, UserID: "claim-user", LoginEmail: "observed@example.com"}}); err != nil {
		t.Fatal(err)
	}
}

func TestLoginEmailClaimPeersAndScope(t *testing.T) {
	for _, infer := range []bool{false, true} {
		for _, bounded := range []bool{false, true} {
			t.Run(fmt.Sprintf("infer=%v/bounded=%v", infer, bounded), func(t *testing.T) {
				s := acquireTestStore(t)
				truncateTables(t, s)
				ctx := context.Background()
				at := time.Now().UTC().Truncate(time.Microsecond)
				seedClaimSourcePeers(t, s, "subject", "peer", "gone")
				var records []*SessionRecord
				for _, id := range []string{"subject", "fixed", "gone", "codex", ""} {
					r := claimSourceRecord(id, "row-"+id, at)
					r.UserID = "claim-user"
					if id == "fixed" {
						r.LoginEmail = "fixed@example.com"
					}
					if id == "codex" {
						r.Agent = "codex"
					}
					records = append(records, r)
					seedLoginEmailClaimEvent(t, s, id, at.Add(-time.Minute))
				}
				if err := s.InsertSessionRecords(ctx, records); err != nil {
					t.Fatal(err)
				}
				if _, err := s.pool.Exec(ctx, `UPDATE session_records SET login_email_source='otel' WHERE session_id='fixed'; INSERT INTO deleted_sessions(session_id) VALUES('gone')`); err != nil {
					t.Fatal(err)
				}
				if !infer {
					if _, err := s.pool.Exec(ctx, `UPDATE session_records SET login_email='guess@example.com',login_email_source='inferred' WHERE session_id='subject'`); err != nil {
						t.Fatal(err)
					}
				}
				clearClaimSourceDirty(t, s)
				var since time.Time
				if bounded {
					since = at.Add(-2 * time.Minute)
				}
				n, err := runLoginEmailClaimPass(ctx, s, infer, since)
				if err != nil || n != 2 {
					t.Fatalf("updated=%d err=%v, want 2", n, err)
				}
				source := "otel"
				if infer {
					source = "inferred"
				}
				if email, got := loginEmailSourceOf(t, s, "row-subject"); email != "observed@example.com" || got != source {
					t.Fatalf("subject=%q/%q", email, got)
				}
				if email, got := loginEmailSourceOf(t, s, "row-fixed"); email != "fixed@example.com" || got != "otel" {
					t.Fatalf("observation overwritten=%q/%q", email, got)
				}
				for _, id := range []string{"gone", "codex", ""} {
					want := id == "codex" && !infer || id == "" && infer
					email, got := loginEmailSourceOf(t, s, "row-"+id)
					if want && (email != "observed@example.com" || got != source) || !want && (email != "" || got != "") {
						t.Fatalf("scope %q=%q/%q, eligible=%v", id, email, got, want)
					}
				}
				zero := int64(0)
				assertClaimSourceDirty(t, s, "subject", 32|256, &zero)
				assertClaimSourceDirty(t, s, "peer", 256, &zero)
				for _, id := range []string{"fixed", "gone", ""} {
					if countWhere(t, s, "session_fact_dirty", "session_id=$1", id) != 0 {
						t.Fatalf("unexpected dirty %q", id)
					}
				}
				if n, err := runLoginEmailClaimPass(ctx, s, infer, since); err != nil || n != 0 {
					t.Fatalf("repeat updated=%d err=%v", n, err)
				}
			})
		}
	}
}

func loginEmailClaimDerivedState(t *testing.T, s *PgStore) string {
	t.Helper()
	var state string
	if err := s.pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(row ORDER BY row::text), '[]'::jsonb)::text FROM (
		SELECT jsonb_build_array('overview',to_jsonb(r)) AS row FROM session_overview_rollups r
		UNION ALL SELECT jsonb_build_array('plugin',to_jsonb(r)) FROM plugin_invocation_facts r
		UNION ALL SELECT jsonb_build_array('task',to_jsonb(r)) FROM task_segment_facts r
	) rows`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestLoginEmailClaimRollback(t *testing.T) {
	for _, infer := range []bool{false, true} {
		for _, cancelled := range []bool{false, true} {
			t.Run(fmt.Sprintf("infer=%v/cancel=%v", infer, cancelled), func(t *testing.T) {
				s := acquireTestStore(t)
				truncateTables(t, s)
				ctx := context.Background()
				seedClaimSourcePeers(t, s, "a-subject", "z-peer")
				r := claimSourceRecord("a-subject", "rollback", time.Now().UTC())
				r.UserID = "claim-user"
				if err := s.InsertSessionRecords(ctx, []*SessionRecord{r}); err != nil {
					t.Fatal(err)
				}
				seedLoginEmailClaimEvent(t, s, r.SessionID, r.Ts.Add(-time.Minute))
				clearClaimSourceDirty(t, s)
				before := loginEmailClaimDerivedState(t, s)
				body := `RAISE EXCEPTION 'synthetic login email dirty failure';`
				if cancelled {
					body = `PERFORM pg_sleep(2);`
				}
				if _, err := s.pool.Exec(ctx, `CREATE FUNCTION login_email_claim_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.session_id='z-peer' THEN `+body+` END IF; RETURN NEW; END $$;
				CREATE TRIGGER login_email_claim_fail BEFORE INSERT OR UPDATE ON session_fact_dirty FOR EACH ROW EXECUTE FUNCTION login_email_claim_fail()`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS login_email_claim_fail ON session_fact_dirty; DROP FUNCTION IF EXISTS login_email_claim_fail()`); err != nil {
						t.Error(err)
					}
				})
				bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
				if _, err := runLoginEmailClaimPass(bounded, s, infer, time.Time{}); err == nil {
					t.Fatal("failed pass accepted")
				}
				barrier, err := s.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer barrier.Rollback(ctx)
				if err := lockSessionDeletionTargets(ctx, barrier, []string{r.SessionID}); err != nil {
					t.Fatal(err)
				}
				if email, source := loginEmailSourceOf(t, s, r.UUID); email != "" || source != "" {
					t.Fatalf("rollback persisted=%q/%q", email, source)
				}
				if countWhere(t, s, "session_fact_dirty", "true") != 0 || loginEmailClaimDerivedState(t, s) != before {
					t.Fatal("rollback persisted dirty or derived changes")
				}
			})
		}
	}
}

func waitLoginEmailClaimBlocked(t *testing.T, s *PgStore, ctx context.Context, pid, count int) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked >= count {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}

func TestLoginEmailClaimFrozenScope(t *testing.T) {
	for _, infer := range []bool{false, true} {
		t.Run(fmt.Sprint(infer), func(t *testing.T) {
			s := acquireTestStore(t)
			truncateTables(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			seedClaimSourcePeers(t, s, "original", "peer")
			r := claimSourceRecord("original", "original", time.Now().UTC())
			r.UserID = "claim-user"
			if err := s.InsertSessionRecords(ctx, []*SessionRecord{r}); err != nil {
				t.Fatal(err)
			}
			seedLoginEmailClaimEvent(t, s, "original", r.Ts.Add(-time.Minute))
			seedLoginEmailClaimEvent(t, s, "late", r.Ts.Add(-time.Minute))
			clearClaimSourceDirty(t, s)
			holder, err := s.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(ctx)
			if err := lockSessionOverviewSourceMutation(ctx, holder, false); err != nil {
				t.Fatal(err)
			}
			if err := lockSessionDeletionTargets(ctx, holder, []string{"original"}); err != nil {
				t.Fatal(err)
			}
			var pid int
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			type result struct {
				n   int64
				err error
			}
			finished := make(chan result, 1)
			go func() {
				n, err := runLoginEmailClaimPass(ctx, s, infer, time.Time{})
				finished <- result{n, err}
			}()
			waitLoginEmailClaimBlocked(t, s, ctx, pid, 1)
			late := claimSourceRecord("late", "late", r.Ts)
			late.UserID = r.UserID
			if err := s.InsertSessionRecords(ctx, []*SessionRecord{late}); err != nil {
				t.Fatal(err)
			}
			if _, err := holder.Exec(ctx, `UPDATE session_records SET login_email='fixed@example.com',login_email_source='otel' WHERE uuid='original'`); err != nil {
				t.Fatal(err)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			got := <-finished
			if got.err != nil || got.n != 0 {
				t.Fatalf("frozen pass updated=%d err=%v", got.n, got.err)
			}
			if email, source := loginEmailSourceOf(t, s, "original"); email != "fixed@example.com" || source != "otel" {
				t.Fatalf("predicate not rechecked=%q/%q", email, source)
			}
			if email, source := loginEmailSourceOf(t, s, "late"); email != "" || source != "" {
				t.Fatal("late session escaped frozen scope")
			}
			zero := int64(0)
			assertClaimSourceDirty(t, s, "peer", 256, &zero)
			if n, err := runLoginEmailClaimPass(ctx, s, infer, time.Time{}); err != nil || n != 1 {
				t.Fatalf("next pass updated=%d err=%v", n, err)
			}
			if email, _ := loginEmailSourceOf(t, s, "late"); email != "observed@example.com" {
				t.Fatal("next periodic pass did not pick up late session")
			}
		})
	}
}

func TestLoginEmailClaimConcurrentReverseCandidates(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seedClaimSourcePeers(t, s, "a-subject", "z-subject", "peer")
	at := time.Now().UTC()
	for _, id := range []string{"z-subject", "a-subject"} {
		r := claimSourceRecord(id, id, at)
		r.UserID = "claim-user"
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{r}); err != nil {
			t.Fatal(err)
		}
		seedLoginEmailClaimEvent(t, s, id, at.Add(-time.Minute))
	}
	clearClaimSourceDirty(t, s)
	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if err := lockSessionDeletionTargets(ctx, holder, []string{"a-subject"}); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 2)
	for _, infer := range []bool{true, false} {
		go func(infer bool) {
			_, err := runLoginEmailClaimPass(ctx, s, infer, time.Time{})
			finished <- err
		}(infer)
	}
	waitLoginEmailClaimBlocked(t, s, ctx, pid, 2)
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	zero := int64(0)
	for _, id := range []string{"a-subject", "z-subject"} {
		if email, source := loginEmailSourceOf(t, s, id); email != "observed@example.com" || source != "otel" {
			t.Fatalf("concurrent result=%q/%q", email, source)
		}
		assertClaimSourceDirty(t, s, id, 32|256, &zero)
	}
	assertClaimSourceDirty(t, s, "peer", 256, &zero)
}
