package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// clearLoginEmailSourceMarker drops the one-time marker so the repair runs, and
// puts it back afterwards.
//
// schema_backfills survives truncateTables on purpose (it is install state, not
// test data), so a test that consumed the marker and walked away would silently
// disarm this repair for every later test in the package.
func clearLoginEmailSourceMarker(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, loginEmailSourceBackfill); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`, loginEmailSourceBackfill); err != nil {
			t.Fatalf("restore marker: %v", err)
		}
	})
}

func loginEmailSourceOf(t *testing.T, s *PgStore, uuid string) (string, string) {
	t.Helper()
	var email, source string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT login_email, login_email_source FROM session_records WHERE uuid = $1`, uuid).
		Scan(&email, &source); err != nil {
		t.Fatalf("query %s: %v", uuid, err)
	}
	return email, source
}

// Rows attributed before login_email_source existed carry no provenance, so an
// empty source would mean both "never attributed" and "attributed by the OTEL
// backfill". The inference pass keys off empty meaning only the first.
//
// Mutation: drop the UPDATE and d1 keeps an empty source, which is the ambiguity
// this repair exists to remove.
func TestBackfillLoginEmailSourceStampsExistingRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailSourceMarker(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "done", RecordType: "user", UUID: "d1", LoginEmail: "one@example.com", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "blank", RecordType: "user", UUID: "b1", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "", RecordType: "user", UUID: "empty-session", LoginEmail: "one@example.com", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if err := s.BackfillLoginEmailSource(ctx); err != nil {
		t.Fatalf("BackfillLoginEmailSource: %v", err)
	}

	if _, src := loginEmailSourceOf(t, s, "d1"); src != "otel" {
		t.Errorf("attributed row source = %q, want otel", src)
	}
	if email, src := loginEmailSourceOf(t, s, "empty-session"); email != "one@example.com" || src != "otel" {
		t.Errorf("empty session attributed row = (%q, %q), want observed email and otel", email, src)
	}
	// An unattributed row must stay '': stamping it would claim OTEL observed an
	// account that it never did.
	if email, src := loginEmailSourceOf(t, s, "b1"); email != "" || src != "" {
		t.Errorf("unattributed row = (%q, %q), want ('', '')", email, src)
	}
}

// The repair scans every attributed row on a multi-million row hypertable. The
// marker is what keeps that off every subsequent boot.
//
// Mutation: remove the marker check and the second run stamps late1, which is the
// per-boot full scan this guard exists to prevent.
func TestBackfillLoginEmailSourceRunsOnce(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailSourceMarker(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

	if err := s.BackfillLoginEmailSource(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts, SessionID: "late", RecordType: "user", UUID: "late1", LoginEmail: "one@example.com", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.BackfillLoginEmailSource(ctx); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if _, src := loginEmailSourceOf(t, s, "late1"); src != "" {
		t.Errorf("second run stamped %q; the marker should have skipped the scan", src)
	}
}

func TestBackfillLoginEmailSourceClaimPeers(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailSourceMarker(t, s)
	ctx := context.Background()
	seedClaimSourcePeers(t, s, "subject", "peer", "gone")
	r := claimSourceRecord("subject", "attributed", time.Now().UTC())
	r.LoginEmail = "observed@example.com"
	blank := claimSourceRecord("blank", "blank", r.Ts)
	inferred := claimSourceRecord("inferred", "inferred", r.Ts)
	inferred.LoginEmail = "inferred@example.com"
	gone := claimSourceRecord("gone", "gone", r.Ts)
	gone.LoginEmail = "deleted@example.com"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{r, blank, inferred, gone}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET login_email_source='inferred' WHERE uuid='inferred'; INSERT INTO deleted_sessions(session_id) VALUES('gone')`); err != nil {
		t.Fatal(err)
	}
	clearClaimSourceDirty(t, s)
	if err := s.BackfillLoginEmailSource(ctx); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	assertClaimSourceDirty(t, s, "subject", 32|256, &zero)
	assertClaimSourceDirty(t, s, "peer", 256, &zero)
	for _, id := range []string{"blank", "inferred", "gone"} {
		if countWhere(t, s, "session_fact_dirty", "session_id=$1", id) != 0 {
			t.Fatalf("unexpected dirty %s", id)
		}
	}
	if email, source := loginEmailSourceOf(t, s, "attributed"); email != "observed@example.com" || source != "otel" {
		t.Fatalf("observed row=%q/%q", email, source)
	}
	if email, source := loginEmailSourceOf(t, s, "inferred"); email != "inferred@example.com" || source != "inferred" {
		t.Fatalf("inferred row=%q/%q", email, source)
	}
	if _, source := loginEmailSourceOf(t, s, "gone"); source != "" {
		t.Fatal("tombstone stamped")
	}
}

func TestBackfillLoginEmailSourceClaimRollback(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			s := acquireTestStore(t)
			truncateTables(t, s)
			clearLoginEmailSourceMarker(t, s)
			ctx := context.Background()
			seedClaimSourcePeers(t, s, "a-subject", "z-peer")
			r := claimSourceRecord("a-subject", "rollback", time.Now().UTC())
			r.LoginEmail = "observed@example.com"
			if err := s.InsertSessionRecords(ctx, []*SessionRecord{r}); err != nil {
				t.Fatal(err)
			}
			clearClaimSourceDirty(t, s)
			body := `RAISE EXCEPTION 'synthetic provenance dirty failure';`
			if cancelled {
				body = `PERFORM pg_sleep(2);`
			}
			if _, err := s.pool.Exec(ctx, `CREATE FUNCTION provenance_dirty_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.session_id='z-peer' THEN `+body+` END IF; RETURN NEW; END $$;
			CREATE TRIGGER provenance_dirty_fail BEFORE INSERT OR UPDATE ON session_fact_dirty FOR EACH ROW EXECUTE FUNCTION provenance_dirty_fail()`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := s.pool.Exec(ctx, `DROP TRIGGER IF EXISTS provenance_dirty_fail ON session_fact_dirty; DROP FUNCTION IF EXISTS provenance_dirty_fail()`); err != nil {
					t.Error(err)
				}
			})
			bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			if err := s.BackfillLoginEmailSource(bounded); err == nil {
				t.Fatal("failed repair accepted")
			}
			// Wait for rollback before checking visibility on another connection.
			barrier, err := s.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := barrier.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, loginEmailSourceBackfill); err != nil {
				t.Fatal(err)
			}
			_ = barrier.Rollback(ctx)
			if email, source := loginEmailSourceOf(t, s, "rollback"); email != "observed@example.com" || source != "" {
				t.Fatalf("failed repair persisted %q/%q", email, source)
			}
			if countWhere(t, s, "session_fact_dirty", "true") != 0 || countWhere(t, s, "schema_backfills", "name=$1", loginEmailSourceBackfill) != 0 {
				t.Fatal("failed repair persisted dirty/marker")
			}
		})
	}
}

func TestBackfillLoginEmailSourceFrozenSessionScope(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	clearLoginEmailSourceMarker(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seedClaimSourcePeers(t, s, "original", "peer")
	r := claimSourceRecord("original", "original", time.Now().UTC())
	r.LoginEmail = "observed@example.com"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{r}); err != nil {
		t.Fatal(err)
	}
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
	finished := make(chan error, 2)
	go func() { finished <- s.BackfillLoginEmailSource(ctx) }()
	// A blocked advisory lock proves the candidate SELECT has finished. The
	// late session must not leak into the later UPDATE's source-shared snapshot.
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("repair did not wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	late := claimSourceRecord("late", "late", r.Ts)
	late.LoginEmail = "late@example.com"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{late}); err != nil {
		t.Fatal(err)
	}
	// Re-evaluate the original candidate after the fence: another repair may
	// have already established a more specific provenance while we waited.
	if _, err := holder.Exec(ctx, `UPDATE session_records SET login_email_source='inferred' WHERE uuid='original'`); err != nil {
		t.Fatal(err)
	}
	go func() { finished <- s.BackfillLoginEmailSource(ctx) }()
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	if _, source := loginEmailSourceOf(t, s, "original"); source != "inferred" {
		t.Fatal("fenced condition was not re-evaluated")
	}
	if _, source := loginEmailSourceOf(t, s, "late"); source != "" {
		t.Fatal("late session escaped frozen repair scope")
	}
	if countWhere(t, s, "schema_backfills", "name=$1", loginEmailSourceBackfill) != 1 {
		t.Fatal("once marker missing")
	}
	zero := int64(0)
	assertClaimSourceDirty(t, s, "peer", 256, &zero)
	// RunsOnce intentionally leaves this late candidate to subsequent writers.
	if err := s.BackfillLoginEmailSource(ctx); err != nil {
		t.Fatal(err)
	}
	if _, source := loginEmailSourceOf(t, s, "late"); source != "" {
		t.Fatal("marker caused recurring repair")
	}
}

// The session-scoped backfill writes what OTEL observed, so it must say so. A
// value it left unstamped would be indistinguishable from an inferred one, and
// the inference pass would then be free to reconsider a directly observed fact.
//
// Mutation: drop login_email_source from its SET list and r1 comes back with an
// empty source, claiming the strongest attribution the system has is unattributed.
func TestBackfillSessionRecordLoginEmailStampsOtel(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := recentDay(49).Add(10 * time.Hour)

	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: ts, EventName: "api_request", SessionID: "has-otel", LoginEmail: "one@example.com"},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: ts.Add(time.Minute), SessionID: "has-otel", RecordType: "user", UUID: "r1", Raw: json.RawMessage(`{}`)},
		{Ts: ts, SessionID: "no-otel", RecordType: "user", UUID: "n1", Raw: json.RawMessage(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	if _, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("BackfillSessionRecordLoginEmail: %v", err)
	}

	if email, src := loginEmailSourceOf(t, s, "r1"); email != "one@example.com" || src != "otel" {
		t.Errorf("filled row = (%q, %q), want (one@example.com, otel)", email, src)
	}
	if _, src := loginEmailSourceOf(t, s, "n1"); src != "" {
		t.Errorf("untouched row source = %q, want ''", src)
	}
}
