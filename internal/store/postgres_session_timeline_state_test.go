package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// truncateTables does not reach the timeline preparation tables, so each test
// clears the sessions it names.
func timelineCleanup(t *testing.T, s *PgStore, ids ...string) {
	t.Helper()
	clear := func() {
		for _, table := range []string{"session_timeline_members", "session_timeline_provenance", "session_timeline_revisions"} {
			if _, err := s.pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE session_id=ANY($1::text[])`, ids); err != nil {
				t.Fatalf("clear %s: %v", table, err)
			}
		}
	}
	clear()
	t.Cleanup(clear)
}

func timelineSeed(t *testing.T, s *PgStore, id string, n int) []SessionTimelineMember {
	t.Helper()
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,profile_email,uuid,raw) SELECT '2026-01-01'::timestamptz+g*interval '1 microsecond',$1,'user','p',g::text,'{}' FROM generate_series(1,$2::int) g`, id, n)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.pool.Query(ctx, `SELECT ts,record_type,profile_email,uuid,id FROM session_records WHERE session_id=$1 ORDER BY ts`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []SessionTimelineMember
	for rows.Next() {
		var m SessionTimelineMember
		if err = rows.Scan(&m.Timestamp, &m.RecordType, &m.ProfileEmail, &m.UUID, &m.RecordID); err != nil {
			t.Fatal(err)
		}
		m.TurnKey = "turn"
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func timelineState(t *testing.T, s *PgStore, id string) SessionTimelineState {
	t.Helper()
	v, err := s.ReadSessionTimelineState(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func timelineValid(t *testing.T, s *PgStore, id string) bool {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	state, err := readSessionTimelineState(ctx, tx, id)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := sessionTimelineMappingValid(ctx, tx, state, 1)
	if err != nil {
		t.Fatal(err)
	}
	return valid
}

// timelineMembers returns the stored placed rows in source order.
func timelineMembers(t *testing.T, s *PgStore, id string) []SessionTimelineMember {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT source_ts,source_record_type,source_profile_email,source_uuid,source_record_id,turn_key
		FROM session_timeline_members WHERE session_id=$1 AND turn_key IS NOT NULL ORDER BY source_ts,source_uuid COLLATE "C"`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []SessionTimelineMember
	for rows.Next() {
		var m SessionTimelineMember
		if err = rows.Scan(&m.Timestamp, &m.RecordType, &m.ProfileEmail, &m.UUID, &m.RecordID, &m.TurnKey); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTimelineMembers(a, b []SessionTimelineMember) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Timestamp.Equal(b[i].Timestamp) || a[i].RecordType != b[i].RecordType || a[i].ProfileEmail != b[i].ProfileEmail ||
			a[i].UUID != b[i].UUID || a[i].RecordID != b[i].RecordID || a[i].TurnKey != b[i].TurnKey {
			return false
		}
	}
	return true
}

func TestSessionTimelineMappingReplace(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "timeline", "timeline-new")
	ctx := context.Background()
	m := timelineSeed(t, s, "timeline", 3)
	state := timelineState(t, s, "timeline")
	if state.Tracking != SessionTimelineTracked || state.Revision != 0 || state.Rows != 3 || len(state.Witness) != 32 {
		t.Fatalf("state %+v", state)
	}
	if err := s.ReplaceSessionTimelineMapping(ctx, state, 1, m); err != nil {
		t.Fatal(err)
	}
	if timelineValid(t, s, "timeline") {
		t.Fatal("a prepared mapping counted as a published one")
	}
	var tracking string
	if err := s.pool.QueryRow(ctx, `SELECT tracking FROM session_timeline_provenance WHERE session_id='timeline'`).Scan(&tracking); err != nil || tracking != "tracked" {
		t.Fatalf("stored tracking=%q err=%v", tracking, err)
	}

	// Input order does not change what is stored.
	reversed := []SessionTimelineMember{m[2], m[1], m[0]}
	reversed[2].TurnKey = "changed-same-source"
	if err := s.ReplaceSessionTimelineMapping(ctx, state, 1, reversed); err != nil {
		t.Fatal(err)
	}
	want := append([]SessionTimelineMember(nil), m...)
	want[0].TurnKey = "changed-same-source"
	if got := timelineMembers(t, s, "timeline"); !sameTimelineMembers(got, want) {
		t.Fatalf("stored %+v want %+v", got, want)
	}

	// Each rejection leaves the previous mapping and provenance intact.
	stored := timelineDump(t, s, "timeline")
	reject := func(name string, expected SessionTimelineState, members []SessionTimelineMember, changed bool) {
		t.Helper()
		err := s.ReplaceSessionTimelineMapping(ctx, expected, 1, members)
		if err == nil || errors.Is(err, ErrSessionTimelineChanged) != changed {
			t.Fatalf("%s: err=%v changed=%v", name, err, changed)
		}
		if got := timelineMembers(t, s, "timeline"); !sameTimelineMembers(got, want) {
			t.Fatalf("%s: rollback lost mapping: %+v", name, got)
		}
		if got := timelineDump(t, s, "timeline"); got != stored {
			t.Fatalf("%s: rollback changed what was stored:\n%s", name, got)
		}
	}
	idOnly := append([]SessionTimelineMember(nil), m...)
	idOnly[0].UUID = m[1].UUID // a real id paired with another row's tuple
	reject("tuple/id mismatch", state, idOnly, true)
	wrongID := append([]SessionTimelineMember(nil), m...)
	wrongID[0].RecordID = m[1].RecordID // a real tuple paired with another row's id
	reject("id mismatch", state, wrongID, true)
	reject("duplicate tuple", state, []SessionTimelineMember{m[0], m[0]}, false)
	fine := append([]SessionTimelineMember(nil), m...)
	fine[0].Timestamp = fine[0].Timestamp.Add(time.Nanosecond)
	reject("sub-microsecond", state, fine, false)
	empty := append([]SessionTimelineMember(nil), m...)
	empty[0].TurnKey = ""
	reject("empty turn", state, empty, false)
	forged := state
	forged.Witness = make([]byte, 32)
	reject("forged witness", forged, m, true)
	untracked := state
	untracked.Tracking = SessionTimelineUntracked
	reject("unsupported tracking", untracked, m, false)

	// A key change is visible to the witness of a published mapping.
	if err := publishTimeline(t, s, state, observedTimeline(timelineKeys(m), m)); err != nil {
		t.Fatal(err)
	}
	if !timelineValid(t, s, "timeline") {
		t.Fatal("a published mapping of the current source is not valid")
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,profile_email,uuid,raw) VALUES('2026-01-02','timeline','assistant','p','late','{}')`); err != nil {
		t.Fatal(err)
	}
	if timelineValid(t, s, "timeline") {
		t.Fatal("new source row kept mapping valid")
	}
	if err := s.ReplaceSessionTimelineMapping(ctx, state, 1, m); !errors.Is(err, ErrSessionTimelineChanged) {
		t.Fatalf("stale publish=%v", err)
	}
	state = timelineState(t, s, "timeline")
	if err := s.ReplaceSessionTimelineMapping(ctx, state, 1, m); err != nil {
		t.Fatal(err)
	}

	// The shared witness detects a raw-only rewrite even when storage keys stay
	// the same, and it detects a policy visibility change for the session.
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET raw='{"redacted":true}' WHERE session_id='timeline'`); err != nil {
		t.Fatal(err)
	}
	rawRewrite := timelineState(t, s, "timeline")
	if sameSessionTimelineState(rawRewrite, state) {
		t.Fatal("raw-only rewrite kept source witness")
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES('timeline')`); err != nil {
		t.Fatal(err)
	}
	if got := timelineState(t, s, "timeline"); sameSessionTimelineState(got, rawRewrite) || got.Tracking != SessionTimelineTracked {
		t.Fatalf("policy visibility change was not witnessed: %+v", got)
	}

	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET session_id='timeline-new' WHERE session_id='timeline'`); err != nil {
		t.Fatal(err)
	}
	if got := timelineState(t, s, "timeline"); got.Rows != 0 || timelineValid(t, s, "timeline") {
		t.Fatalf("moved rows still witnessed: %+v", got)
	}
	if got := timelineState(t, s, "timeline-new"); got.Rows != 4 {
		t.Fatalf("new session rows=%d", got.Rows)
	}
}

func TestSessionTimelineSourceWitnessCoversSnapshotsCopiesEventsAndCosts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "timeline-all-sources")
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`)
	})
	members := timelineSeed(t, s, "timeline-all-sources", 1)
	cleanup := func() {
		for _, table := range []string{"session_snapshots", "session_record_copies", "otel_events", "claude_imputed_cost", "session_records"} {
			if _, err := s.pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE session_id=$1`, "timeline-all-sources"); err != nil {
				t.Errorf("clear %s: %v", table, err)
			}
		}
	}
	t.Cleanup(cleanup)
	state := timelineState(t, s, "timeline-all-sources")
	assertMoved := func(stage string) {
		t.Helper()
		next := timelineState(t, s, "timeline-all-sources")
		if sameSessionTimelineState(next, state) {
			t.Fatalf("%s source update kept the witness", stage)
		}
		state = next
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES('timeline-all-sources','owner','p','claude',now(),'{}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	assertMoved("snapshot")
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_record_copies(srec_id,session_id,original_session_id,record_type,ts)
		VALUES($1,'timeline-all-sources','original','assistant',now())`, members[0].RecordID); err != nil {
		t.Fatal(err)
	}
	unchanged := timelineState(t, s, "timeline-all-sources")
	if !sameSessionTimelineState(unchanged, state) {
		t.Fatal("copy marker changed the witness while the copy gate was closed")
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES('session_fact_copies_v1') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`)
	})
	assertMoved("copy gate")
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_record_copies(srec_id,session_id,original_session_id,record_type,ts)
		VALUES($1,'timeline-all-sources','original','assistant',now())`, members[0].RecordID+100); err != nil {
		t.Fatal(err)
	}
	assertMoved("copy marker while gate is open")
	if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events(ts,event_name,session_id,user_id,profile_email,prompt_id,cost_usd)
		VALUES(now(),'api_request','timeline-all-sources','owner','p','prompt',0.5)`); err != nil {
		t.Fatal(err)
	}
	assertMoved("OTEL")
	if _, err := s.pool.Exec(ctx, `INSERT INTO claude_imputed_cost(srec_id,session_id,ts,user_id,profile_email,model,cost_usd,input_tokens)
		VALUES($1,'timeline-all-sources',now(),'owner','p','model',0.25,10)`, members[0].RecordID); err != nil {
		t.Fatal(err)
	}
	assertMoved("imputed cost")
}

func TestSessionTimelineSourceWitnessTracksAccountPolicyWriter(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "timeline-account-policy")
	ctx := context.Background()
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: time.Now().UTC(), SessionID: "timeline-account-policy", RecordType: "user", Agent: "claude",
		UUID: "u", UserID: "owner", ProfileEmail: "p", LoginEmail: "excluded@example.com", Raw: json.RawMessage(`{"message":{"content":"x"}}`),
	}}); err != nil {
		t.Fatal(err)
	}
	before := timelineState(t, s, "timeline-account-policy")
	if _, err := s.ExcludeAccount(ctx, "excluded@example.com", "test", "admin"); err != nil {
		t.Fatal(err)
	}
	after := timelineState(t, s, "timeline-account-policy")
	if sameSessionTimelineState(before, after) || after.PolicyEpoch <= before.PolicyEpoch || after.Revision <= before.Revision {
		t.Fatalf("account exclusion did not advance source/policy witness: before=%+v after=%+v", before, after)
	}
}

func TestSessionTimelineWitnessIsWholeOrderedKeySet(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "witness-a", "witness-b")
	ctx := context.Background()
	insert := `INSERT INTO session_records(id,ts,session_id,record_type,profile_email,uuid,raw) VALUES($1,'2026-01-01',$2,$3,'p',$4,'{}')`
	// The same unique tuples arrive in opposite order in two sessions.
	for _, r := range [][]any{{101, "witness-a", "user", "u1"}, {102, "witness-a", "assistant", "u2"}} {
		if _, err := s.pool.Exec(ctx, insert, r...); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range [][]any{{102, "witness-b", "assistant", "u2"}, {101, "witness-b", "user", "u1"}} {
		if _, err := s.pool.Exec(ctx, insert, r...); err != nil {
			t.Fatal(err)
		}
	}
	a, keysA, err := readTimelineKeysNow(s, "witness-a")
	if err != nil {
		t.Fatal(err)
	}
	b, keysB, err := readTimelineKeysNow(s, "witness-b")
	if err != nil {
		t.Fatal(err)
	}
	if a.Rows != 2 || b.Rows != 2 {
		t.Fatalf("rows %d %d", a.Rows, b.Rows)
	}
	if len(keysA) != len(keysB) {
		t.Fatalf("ordered keys differ in length: %d %d", len(keysA), len(keysB))
	}
	for i := range keysA {
		if !keysA[i].Timestamp.Equal(keysB[i].Timestamp) || keysA[i].UUID != keysB[i].UUID || keysA[i].RecordID != keysB[i].RecordID || keysA[i].RecordType != keysB[i].RecordType || keysA[i].ProfileEmail != keysB[i].ProfileEmail {
			t.Fatalf("insertion order changed key %d: %+v / %+v", i, keysA[i], keysB[i])
		}
	}
	if got := timelineState(t, s, "witness-a"); !sameSessionTimelineState(got, a) {
		t.Fatal("re-reading the same source changed the witness")
	}
	// The id alone changes the witness even when the unique tuple does not.
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET id=103 WHERE session_id='witness-a' AND uuid='u1'`); err != nil {
		t.Fatal(err)
	}
	if got := timelineState(t, s, "witness-a"); sameSessionTimelineState(got, a) {
		t.Fatal("id change kept the witness")
	}
}

func TestSessionTimelineChunkWitness(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "chunk-timeline")
	ctx := context.Background()
	m := timelineSeed(t, s, "chunk-timeline", 1)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,profile_email,uuid,raw) VALUES('2020-01-01','chunk-timeline','user','p','old','{}')`); err != nil {
		t.Fatal(err)
	}
	state := timelineState(t, s, "chunk-timeline")
	// Both rows loaded: the old one is unplaced, the new one placed.
	_, all, err := readTimelineKeysNow(s, "chunk-timeline")
	if err != nil {
		t.Fatal(err)
	}
	if err := publishTimeline(t, s, state, observedTimeline(all, m)); err != nil {
		t.Fatal(err)
	}
	if !timelineValid(t, s, "chunk-timeline") {
		t.Fatal("published mapping invalid before the drop")
	}
	if _, err := s.pool.Exec(ctx, `SELECT drop_chunks('session_records',older_than=>'2021-01-01'::timestamptz)`); err != nil {
		t.Fatal(err)
	}
	after := timelineState(t, s, "chunk-timeline")
	if after.Rows != 1 || after.Revision != state.Revision || sameSessionTimelineState(after, state) {
		t.Fatal("chunk drop witness contract failed")
	}
	if timelineValid(t, s, "chunk-timeline") {
		t.Fatal("chunk drop did not invalidate mapping")
	}
}

// timelineStore is a store of its own over the test database: fresh
// connections at the server's defaults, whatever session settings earlier
// tests left on the shared pool's connections.
func timelineStore(t *testing.T) *PgStore {
	t.Helper()
	acquireTestStore(t)
	ts, err := NewPgStore(context.Background(), pgSharedDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ts.Close() })
	return ts
}

// The witness read is bounded and all-or-nothing: a blocked read fails within
// its own five seconds, a caller's earlier deadline or the connection's lock
// timeout, whichever comes first, and returns no partial state. Each limit is
// checked by the error it gives, not by the time alone.
func TestSessionTimelineStateTimeout(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "timeout-timeline")
	ctx := context.Background()
	timelineSeed(t, s, "timeout-timeline", 2)
	ts := timelineStore(t)
	var setting string
	if err := ts.pool.QueryRow(ctx, `SELECT current_setting('lock_timeout')`).Scan(&setting); err != nil || setting != "0" {
		t.Fatalf("a fresh connection has lock_timeout=%q err=%v, want the server default 0", setting, err)
	}
	blocker, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, `LOCK TABLE session_records IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	read := func(ctx context.Context) (SessionTimelineState, time.Duration, error) {
		start := time.Now()
		got, err := ts.ReadSessionTimelineState(ctx, "timeout-timeline")
		if got.SessionID != "" || got.Rows != 0 || got.Witness != nil {
			t.Fatalf("partial state on error: %+v", got)
		}
		return got, time.Since(start), err
	}
	code := func(err error) string {
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			return pe.Code
		}
		return ""
	}

	// Its own budget: the five-second context or the five-second statement
	// timeout beside it, never a lock timeout.
	if _, elapsed, err := read(ctx); (!errors.Is(err, context.DeadlineExceeded) && code(err) != "57014") || elapsed < 4500*time.Millisecond || elapsed > 6*time.Second {
		t.Fatalf("own budget: err=%v after %s, want the five-second deadline", err, elapsed)
	}

	// A caller's earlier deadline ends it first.
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	_, elapsed, err := read(short)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || elapsed < 250*time.Millisecond || elapsed > 1500*time.Millisecond {
		t.Fatalf("caller deadline: err=%v after %s, want the caller's 300ms", err, elapsed)
	}

	// A connection's own lock timeout ends it with that error, before the
	// read's own budget. The error tells it from that budget (a deadline or
	// 57014 fails here); the wall bound is the own-budget case's, since the
	// one-second wait comes after round trips a loaded run can stretch.
	bounded, cancelBounded := context.WithTimeout(ctx, 10*time.Second)
	defer cancelBounded()
	withOneSetConnection(bounded, ts.pool, `SET lock_timeout = '1s'`, `RESET lock_timeout`, t.Fatalf, t.Errorf, func() {
		_, elapsed, err = read(ctx)
		if code(err) != "55P03" || elapsed > 6*time.Second {
			t.Fatalf("lock timeout: err=%v after %s, want 55P03 within the read's own bound", err, elapsed)
		}
	})

	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSessionTimelineState(ctx, ""); err == nil {
		t.Fatal("empty session accepted")
	}
}

// withOneSetConnection holds every connection of pool, gives one of them the
// session setting set and returns it, so use's next acquire is that one; then
// it takes it back, applies reset and returns them all. Every connection is
// owned by the deferred cleanup from the moment it is acquired, so a fail or a
// use that stops the goroutine (t.Fatalf) still has the set connection taken
// back and reset -- the only idle one while the others are held -- and every
// connection returned, once each. A connection reset could not undo is closed
// rather than reused, and reported. ctx bounds every acquire and statement of
// the run; the cleanup has a short deadline of its own, since the run may have
// stopped because ctx ended.
func withOneSetConnection(ctx context.Context, pool *pgxpool.Pool, set, reset string, fail, report func(format string, args ...any), use func()) {
	held := make([]*pgxpool.Conn, 0, pool.Config().MaxConns)
	var lent, restored bool
	restore := func(ctx context.Context, c *pgxpool.Conn) error {
		_, err := c.Exec(ctx, reset)
		if err != nil {
			_ = c.Conn().Close(ctx)
		}
		return err
	}
	defer func() {
		if lent && !restored {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if c, err := pool.Acquire(cleanup); err != nil {
				report("take back the set connection: %v", err)
			} else {
				if err := restore(cleanup, c); err != nil {
					report("%s: %v", reset, err)
				}
				c.Release()
			}
		}
		for _, c := range held {
			if c != nil {
				c.Release()
			}
		}
	}()
	for i := int32(0); i < pool.Config().MaxConns; i++ {
		c, err := pool.Acquire(ctx)
		if err != nil {
			fail("acquire connection %d: %v", i, err)
			return
		}
		held = append(held, c)
	}
	if _, err := held[0].Exec(ctx, set); err != nil {
		fail("%s: %v", set, err)
		return
	}
	held[0].Release()
	held[0], lent = nil, true
	use()
	c, err := pool.Acquire(ctx)
	if err != nil {
		fail("take back the set connection: %v", err)
		return
	}
	held[0], restored = c, true
	if err := restore(ctx, c); err != nil {
		fail("%s: %v", reset, err)
	}
}

// withOneSetConnection returns every connection, and leaves none set, however
// it stops: a failing setting or reset, or a use that stops its goroutine as
// t.Fatalf does -- after a real lock-timeout read, as the timeout test's
// assertion would. The pool then closes.
func TestWithOneSetConnectionReturnsEveryConnection(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "cleanup-timeline")
	timelineSeed(t, s, "cleanup-timeline", 1)
	ts := timelineStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	blocker, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, `LOCK TABLE session_records IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	read := func() {
		if _, err := ts.ReadSessionTimelineState(ctx, "cleanup-timeline"); err == nil {
			t.Error("blocked read succeeded")
		}
	}
	for _, c := range []struct {
		name, set, reset string
		use              func()
		stop, failed     bool
		reported         bool
	}{
		{"setting fails", `SET no_such_setting = 1`, `RESET lock_timeout`, nil, false, true, false},
		{"reset fails", `SET lock_timeout = '1s'`, `RESET no_such_setting`, nil, false, true, false},
		{"the read's assertion fails", `SET lock_timeout = '1s'`, `RESET lock_timeout`, read, true, false, false},
		{"use stops and its reset fails", `SET lock_timeout = '1s'`, `RESET no_such_setting`, read, true, false, true},
		{"clean", `SET lock_timeout = '1s'`, `RESET lock_timeout`, read, false, false, false},
		{"the run's context ends, then use stops", `SET lock_timeout = '1s'`, `RESET lock_timeout`, nil, true, false, false},
	} {
		var failed, reported bool
		run, cancelRun := context.WithCancel(ctx)
		if c.use == nil && c.stop {
			c.use = cancelRun
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			withOneSetConnection(run, ts.pool, c.set, c.reset,
				func(string, ...any) { failed = true; runtime.Goexit() },
				func(string, ...any) { reported = true },
				func() {
					if c.use != nil {
						c.use()
					}
					if c.stop {
						runtime.Goexit()
					}
				})
		}()
		<-done
		cancelRun()
		if failed != c.failed || reported != c.reported {
			t.Errorf("%s: failed=%v reported=%v, want %v %v", c.name, failed, reported, c.failed, c.reported)
		}
		// A closed connection leaves the pool's count once its asynchronous
		// destroy runs; wait for that, boundedly, not for anything else.
		deadline := time.Now().Add(2 * time.Second)
		for ts.pool.Stat().AcquiredConns() != 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := ts.pool.Stat().AcquiredConns(); n != 0 {
			t.Fatalf("%s: %d connections still acquired", c.name, n)
		}
		// No connection the pool hands out carries the setting.
		func() {
			conns := make([]*pgxpool.Conn, 0, ts.pool.Config().MaxConns)
			defer func() {
				for _, conn := range conns {
					conn.Release()
				}
			}()
			for i := int32(0); i < ts.pool.Config().MaxConns; i++ {
				conn, err := ts.pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				conns = append(conns, conn)
				var setting string
				if err := conn.QueryRow(ctx, `SHOW lock_timeout`).Scan(&setting); err != nil || setting != "0" {
					t.Errorf("%s: connection %d has lock_timeout=%q err=%v", c.name, i, setting, err)
				}
			}
		}()
	}
	closed := make(chan struct{})
	go func() {
		_ = ts.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the pool did not close: a connection was never returned")
	}
}

// The replacement takes its locks before it reads. A source writer that holds
// the session lock and commits while the replacement waits must be seen.
func TestSessionTimelineReplaceWaitsForSessionWriter(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "wait-timeline")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	m := timelineSeed(t, s, "wait-timeline", 2)
	state := timelineState(t, s, "wait-timeline")

	writer, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.Background())
	if err = lockSessionOverviewSourceMutation(ctx, writer, false); err != nil {
		t.Fatal(err)
	}
	if err = lockSessionDeletionTargets(ctx, writer, []string{"wait-timeline"}); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,profile_email,uuid,raw) VALUES('2026-01-02','wait-timeline','user','p','late','{}')`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.ReplaceSessionTimelineMapping(ctx, state, 1, m) }()
	for {
		var waiting int
		if err = s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("replacement did not wait for the session writer: %v", err)
		case <-ctx.Done():
			t.Fatal("replacement never waited")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrSessionTimelineChanged) {
		t.Fatalf("replacement after concurrent commit=%v", err)
	}
	if got := timelineMembers(t, s, "wait-timeline"); len(got) != 0 {
		t.Fatalf("stale mapping stored: %+v", got)
	}
}

// Fact invalidation drops membership and provenance with the facts but keeps the
// revision row (ABA). Strict deletion removes all three and the tombstone
// refuses any later replacement.
func TestSessionTimelineDeletionLifecycle(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "life-timeline")
	ctx := context.Background()
	m := timelineSeed(t, s, "life-timeline", 2)
	state := timelineState(t, s, "life-timeline")
	if err := s.ReplaceSessionTimelineMapping(ctx, state, 1, m); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_timeline_revisions(session_id) VALUES('life-timeline')`); err != nil {
		t.Fatal(err)
	}
	count := func(table string) int {
		t.Helper()
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE session_id='life-timeline'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = deleteSessionFacts(ctx, tx, []string{"life-timeline"}); err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if count("session_timeline_members") != 0 || count("session_timeline_provenance") != 0 || count("session_timeline_revisions") != 1 {
		t.Fatal("fact invalidation did not drop mapping or dropped the revision")
	}
	if err = s.ReplaceSessionTimelineMapping(ctx, state, 1, m); err != nil {
		t.Fatal(err)
	}

	if _, err = s.DeleteSession(ctx, "life-timeline", "admin@example.com", "strict", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"session_timeline_members", "session_timeline_provenance", "session_timeline_revisions"} {
		if n := count(table); n != 0 {
			t.Fatalf("%s kept %d rows after strict deletion", table, n)
		}
	}
	if err = s.ReplaceSessionTimelineMapping(ctx, state, 1, m); !errors.Is(err, ErrSessionTimelineChanged) {
		t.Fatalf("tombstoned replacement=%v", err)
	}
	if count("session_timeline_members") != 0 || count("session_timeline_provenance") != 0 {
		t.Fatal("tombstoned replacement wrote rows")
	}
}

// Preparation must not install source or policy tracking on existing writers.
func TestSessionTimelineInstallsNoTriggers(t *testing.T) {
	s := acquireTestStore(t)
	var n int
	if err := s.pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal AND tgname LIKE 'timeline%')
		+ (SELECT count(*) FROM pg_proc WHERE proname LIKE 'session_timeline%')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("timeline triggers or functions installed: %d", n)
	}
}

// The schema before rows could be unplaced, as 7b08a078 created it.
var oldSessionTimelineSchema = []string{
	`CREATE TABLE session_timeline_members (
		session_id TEXT NOT NULL,
		source_ts TIMESTAMPTZ NOT NULL,
		source_record_type TEXT NOT NULL,
		source_profile_email TEXT NOT NULL,
		source_uuid TEXT NOT NULL,
		source_record_id BIGINT NOT NULL,
		turn_key TEXT NOT NULL,
		PRIMARY KEY(session_id,source_ts,source_record_type,source_profile_email,source_uuid)
	)`,
	`CREATE INDEX idx_session_timeline_members_turn ON session_timeline_members
		(session_id,turn_key,source_ts,source_uuid,source_record_id,source_record_type,source_profile_email)`,
	`CREATE TABLE session_timeline_provenance (
		session_id TEXT PRIMARY KEY,
		tracking TEXT NOT NULL CHECK(tracking='untracked'),
		source_revision BIGINT NOT NULL,
		policy_epoch BIGINT NOT NULL,
		source_witness BYTEA NOT NULL CHECK(octet_length(source_witness)=32),
		source_rows BIGINT NOT NULL,
		mapping_version INTEGER NOT NULL CHECK(mapping_version>0)
	)`,
}

// upgradeOldTimelineSchema builds the old schema in a schema of its own inside
// tx, runs seed there, then runs the timeline migration twice.
func upgradeOldTimelineSchema(ctx context.Context, tx pgx.Tx, seed ...string) error {
	// The timeline migrations extend pre-existing account and AI settings tables;
	// this isolated schema supplies their original, minimal forms.
	stmts := []string{
		`CREATE SCHEMA timeline_upgrade`, `SET LOCAL search_path TO timeline_upgrade`,
		`CREATE TABLE dashboard_users (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE ai_settings (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE ai_runtime_settings (runtime_key TEXT PRIMARY KEY)`,
	}
	stmts = append(stmts, oldSessionTimelineSchema...)
	stmts = append(stmts, seed...)
	stmts = append(stmts, sessionTimelineMigrations...)
	stmts = append(stmts, sessionTimelineMigrations...)
	for _, q := range stmts {
		if _, err := tx.Exec(ctx, q); err != nil {
			return fmt.Errorf("%.60s: %w", q, err)
		}
	}
	return nil
}

func checkViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23514"
}

// Rows the preparation helper stored survive the migration, run twice: placed
// rows stay placed, provenance becomes "prepared" and is not promoted. A row
// the new constraints reject fails the migration instead of being hidden.
func TestSessionTimelineMigrationUpgradesOldRows(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = upgradeOldTimelineSchema(ctx, tx,
		`INSERT INTO session_timeline_members VALUES('old','2026-01-01','user','p','u1',7,'turn-a')`,
		`INSERT INTO session_timeline_provenance VALUES('old','untracked',0,0,decode(repeat('00',32),'hex'),1,1)`,
	); err != nil {
		t.Fatal(err)
	}
	var turn, roles, state string
	var reason *string
	var hashed bool
	if err = tx.QueryRow(ctx, `SELECT turn_key, roles::text, unmapped_reason FROM session_timeline_members WHERE session_id='old'`).Scan(&turn, &roles, &reason); err != nil {
		t.Fatal(err)
	}
	if turn != "turn-a" || roles != "{}" || reason != nil {
		t.Fatalf("old member became %q %s %v", turn, roles, reason)
	}
	if err = tx.QueryRow(ctx, `SELECT timeline_state, timeline_hash IS NOT NULL OR fact_output_hash IS NOT NULL OR source_generation IS NOT NULL
		FROM session_timeline_provenance WHERE session_id='old'`).Scan(&state, &hashed); err != nil {
		t.Fatal(err)
	}
	if state != "prepared" || hashed {
		t.Fatalf("old provenance became %s hashed=%v", state, hashed)
	}
	var constraints, oldIndex, newIndex int
	if err = tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM pg_constraint WHERE conname IN ('session_timeline_members_result','session_timeline_provenance_result')
			AND connamespace='timeline_upgrade'::regnamespace),
		(SELECT count(*) FROM pg_class WHERE relname='idx_session_timeline_members_turn' AND relnamespace='timeline_upgrade'::regnamespace),
		(SELECT count(*) FROM pg_class WHERE relname='idx_session_timeline_members_mapped' AND relnamespace='timeline_upgrade'::regnamespace)`).Scan(&constraints, &oldIndex, &newIndex); err != nil {
		t.Fatal(err)
	}
	if constraints != 2 || oldIndex != 0 || newIndex != 1 {
		t.Fatalf("constraints=%d old index=%d new index=%d", constraints, oldIndex, newIndex)
	}
	// The upgraded schema holds the new rules too.
	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sp.Exec(ctx, `UPDATE session_timeline_provenance SET timeline_state='observed' WHERE session_id='old'`); !checkViolation(err) {
		t.Fatalf("prepared provenance promoted without its hashes: %v", err)
	}
	_ = sp.Rollback(ctx)
	// The schema name is taken until this transaction ends.
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	bad, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Rollback(ctx)
	if err = upgradeOldTimelineSchema(ctx, bad, `INSERT INTO session_timeline_members VALUES('old','2026-01-01','user','p','u1',7,'')`); !checkViolation(err) {
		t.Fatalf("an empty old turn key passed the migration: %v", err)
	}
}

func TestSessionTimelineConstraintsRejectUnsoundRows(t *testing.T) {
	s := acquireTestStore(t)
	timelineCleanup(t, s, "constraint-timeline")
	ctx := context.Background()
	member := `INSERT INTO session_timeline_members(session_id,source_ts,source_record_type,source_profile_email,source_uuid,source_record_id,turn_key,roles,unmapped_reason)
		VALUES('constraint-timeline','2026-01-01','user','p',$1,1,%s,%s,%s)`
	for i, c := range []struct {
		turn, roles, reason string
		ok                  bool
	}{
		{"'t'", "'{opener}'", "NULL", true},
		{"NULL", "'{}'", "'ts_fallback'", true},
		{"'t'", "'{}'", "'ts_fallback'", false},
		{"NULL", "'{}'", "NULL", false},
		{"''", "'{}'", "NULL", false},
		{"NULL", "'{}'", "''", false},
		{"NULL", "'{opener}'", "'ts_fallback'", false},
		{"'t'", "'{\"\"}'", "NULL", false},
		{"'t'", "ARRAY[NULL]::text[]", "NULL", false},
	} {
		_, err := s.pool.Exec(ctx, fmt.Sprintf(member, c.turn, c.roles, c.reason), fmt.Sprintf("u%d", i))
		if c.ok != (err == nil) || (!c.ok && !checkViolation(err)) {
			t.Errorf("member turn=%s roles=%s reason=%s: err=%v", c.turn, c.roles, c.reason, err)
		}
	}
	provenance := `INSERT INTO session_timeline_provenance(session_id,tracking,source_revision,policy_epoch,source_witness,source_rows,mapping_version,
		timeline_state,timeline_reason,timeline_hash,fact_output_hash,source_generation,rules_version)
		VALUES('constraint-timeline','untracked',0,0,decode(repeat('00',32),'hex'),1,1,%s)`
	h := "decode(repeat('11',32),'hex')"
	for _, c := range []struct {
		values string
		ok     bool
	}{
		{"'prepared','',NULL,NULL,NULL,NULL", true},
		{"'observed',''," + h + "," + h + ",1,2", true},
		{"'undeterminable','redacted'," + h + "," + h + ",1,2", true},
		{"'truncated','',NULL," + h + ",1,2", true},
		{"'observed','',NULL," + h + ",1,2", false},
		{"'observed',''," + h + ",decode(repeat('11',31),'hex'),1,2", false},
		{"'observed',''," + h + "," + h + ",NULL,2", false},
		{"'observed',''," + h + "," + h + ",1,0", false},
		{"'observed',''," + h + "," + h + ",1,NULL", false},
		{"'undeterminable',''," + h + "," + h + ",1,2", false},
		{"'truncated','',NULL,NULL,1,2", false},
		{"'complete',''," + h + "," + h + ",1,2", false},
		// The state's own columns, as validSessionTimelinePublish checks them.
		{"'prepared',''," + h + ",NULL,NULL,NULL", false},
		{"'prepared','',NULL," + h + ",NULL,NULL", false},
		{"'prepared','',NULL,NULL,1,NULL", false},
		{"'prepared','',NULL,NULL,NULL,2", false},
		{"'prepared','redacted',NULL,NULL,NULL,NULL", false},
		{"'observed','redacted'," + h + "," + h + ",1,2", false},
		{"'truncated',''," + h + "," + h + ",1,2", false},
		{"'observed',''," + h + "," + h + ",0,2", false},
		{"'truncated','',NULL," + h + ",-1,2", false},
	} {
		_, err := s.pool.Exec(ctx, fmt.Sprintf(provenance, c.values))
		if c.ok != (err == nil) || (!c.ok && !checkViolation(err)) {
			t.Errorf("provenance %s: err=%v", c.values, err)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM session_timeline_provenance WHERE session_id='constraint-timeline'`); err != nil {
			t.Fatal(err)
		}
	}
}

func timelineKeys(m []SessionTimelineMember) []SessionTimelineKey {
	var out []SessionTimelineKey
	for _, x := range m {
		out = append(out, SessionTimelineKey{Timestamp: x.Timestamp, RecordType: x.RecordType, ProfileEmail: x.ProfileEmail, UUID: x.UUID, RecordID: x.RecordID})
	}
	return out
}

// publishTimeline is what the publisher will do around replaceSessionTimelineTx:
// its locks, the tombstone check, and commit or rollback whole.
func publishTimeline(t *testing.T, s *PgStore, expected SessionTimelineState, p SessionTimelinePublish) error {
	t.Helper()
	ctx := context.Background()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = lockSessionFactPublish(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = lockSessionDeletionTargets(ctx, tx, []string{expected.SessionID}); err != nil {
		t.Fatal(err)
	}
	if err = replaceSessionTimelineTx(ctx, tx, expected, p); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// timelineDump is every stored row and the provenance, for exact comparison.
func timelineDump(t *testing.T, s *PgStore, id string) string {
	t.Helper()
	ctx := context.Background()
	rows, err := s.pool.Query(ctx, `SELECT source_uuid, source_record_id, COALESCE(turn_key,'-'), roles::text, COALESCE(unmapped_reason,'-')
		FROM session_timeline_members WHERE session_id=$1 ORDER BY source_ts, source_uuid COLLATE "C"`, id)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for rows.Next() {
		var uuid, turn, roles, reason string
		var rid int64
		if err = rows.Scan(&uuid, &rid, &turn, &roles, &reason); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s/%d %s %s %s\n", uuid, rid, turn, roles, reason)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	var prov string
	err = s.pool.QueryRow(ctx, `SELECT concat_ws(' ', timeline_state, timeline_reason, COALESCE(encode(timeline_hash,'hex'),'-'),
		COALESCE(encode(fact_output_hash,'hex'),'-'), COALESCE(source_generation::text,'-'), COALESCE(rules_version::text,'-'), source_rows, mapping_version)
		FROM session_timeline_provenance WHERE session_id=$1`, id).Scan(&prov)
	if errors.Is(err, pgx.ErrNoRows) {
		prov = "no provenance"
	} else if err != nil {
		t.Fatal(err)
	}
	return b.String() + prov
}

func TestSessionTimelineReplaceTxStoresEveryRow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "full-timeline")
	keys := timelineKeys(timelineSeed(t, s, "full-timeline", 4))
	state := timelineState(t, s, "full-timeline")
	h1, h2 := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	base := func() SessionTimelinePublish {
		return SessionTimelinePublish{State: "observed", LoadedKeys: append([]SessionTimelineKey(nil), keys[:3]...), TimelineHash: h1, FactOutputHash: h2,
			SourceGeneration: 5, RulesVersion: 2, MappingVersion: 1,
			Rows: []SessionTimelineRow{
				{SessionTimelineKey: keys[0], TurnKey: "t1", Roles: []string{"opener", "input"}},
				{SessionTimelineKey: keys[1], TurnKey: "t1", Roles: []string{"assistant_final"}},
				{SessionTimelineKey: keys[2], Unmapped: "ts_fallback"},
			}}
	}
	if err := publishTimeline(t, s, state, base()); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("1/%d t1 {opener,input} -\n2/%d t1 {assistant_final} -\n3/%d - {} ts_fallback\n4/%d - {} not_loaded\n"+
		"observed  %x %x 5 2 4 1", keys[0].RecordID, keys[1].RecordID, keys[2].RecordID, keys[3].RecordID, h1, h2)
	if got := timelineDump(t, s, "full-timeline"); got != want {
		t.Fatalf("stored\n%s\nwant\n%s", got, want)
	}

	// Order does not matter.
	reordered := base()
	reordered.LoadedKeys = []SessionTimelineKey{keys[2], keys[0], keys[1]}
	reordered.Rows = []SessionTimelineRow{reordered.Rows[2], reordered.Rows[0], reordered.Rows[1]}
	if err := publishTimeline(t, s, state, reordered); err != nil {
		t.Fatal(err)
	}
	if got := timelineDump(t, s, "full-timeline"); got != want {
		t.Fatalf("reordered input stored\n%s", got)
	}

	ghost := keys[0]
	ghost.UUID = "ghost"
	otherID := keys[0]
	otherID.RecordID = keys[1].RecordID
	// why is the error a rejection must give, ErrSessionTimelineChanged's text
	// when the source does not have the row.
	reject := func(name, why string, edit func(*SessionTimelinePublish)) {
		t.Helper()
		p := base()
		edit(&p)
		err := publishTimeline(t, s, state, p)
		if err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: err=%v, want %q", name, err, why)
		}
		if got := timelineDump(t, s, "full-timeline"); got != want {
			t.Errorf("%s changed what was stored:\n%s", name, got)
		}
	}
	reject("a row the loader did not read", "loader did not read", func(p *SessionTimelinePublish) {
		p.Rows = append(p.Rows, SessionTimelineRow{SessionTimelineKey: keys[3], TurnKey: "t1"})
	})
	reject("a loaded row the computation dropped", "do not cover the loaded rows", func(p *SessionTimelinePublish) { p.Rows = p.Rows[:2] })
	reject("a loaded key twice", "duplicate loaded", func(p *SessionTimelinePublish) { p.LoadedKeys = append(p.LoadedKeys, keys[0]) })
	reject("a row twice", "duplicate timeline source tuple", func(p *SessionTimelinePublish) { p.Rows[2] = p.Rows[0] })
	reject("a row claiming not_loaded", "neither placed nor unplaced", func(p *SessionTimelinePublish) { p.Rows[2].Unmapped = "not_loaded" })
	reject("an unplaced row with roles", "neither placed nor unplaced", func(p *SessionTimelinePublish) { p.Rows[2].Roles = []string{"input"} })
	reject("a row both placed and unplaced", "neither placed nor unplaced", func(p *SessionTimelinePublish) { p.Rows[0].Unmapped = "ts_fallback" })
	reject("a row neither placed nor unplaced", "neither placed nor unplaced", func(p *SessionTimelinePublish) { p.Rows[2].Unmapped = "" })
	reject("an empty role", "empty timeline role", func(p *SessionTimelinePublish) { p.Rows[0].Roles = []string{""} })
	reject("a short hash", "invalid timeline provenance", func(p *SessionTimelinePublish) { p.FactOutputHash = h2[:31] })
	reject("no generation", "invalid timeline provenance", func(p *SessionTimelinePublish) { p.SourceGeneration = 0 })
	reject("undeterminable without a reason", "invalid timeline provenance", func(p *SessionTimelinePublish) { p.State = "undeterminable" })
	reject("observed with a reason", "invalid timeline provenance", func(p *SessionTimelinePublish) { p.Reason = "redacted" })
	reject("an unknown state", "invalid timeline state", func(p *SessionTimelinePublish) { p.State = "complete" })
	reject("a tuple no raw row has", ErrSessionTimelineChanged.Error(), func(p *SessionTimelinePublish) {
		p.LoadedKeys[0], p.Rows[0].SessionTimelineKey = ghost, ghost
	})
	reject("a tuple with another row's id", ErrSessionTimelineChanged.Error(), func(p *SessionTimelinePublish) {
		p.LoadedKeys[0], p.Rows[0].SessionTimelineKey = otherID, otherID
	})

	// A rollback after the replacement, as a failing fact write forces, keeps
	// the previous timeline and provenance.
	ctx := context.Background()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	if err = lockSessionFactPublish(ctx, tx); err == nil {
		err = lockSessionDeletionTargets(ctx, tx, []string{"full-timeline"})
	}
	changed := base()
	changed.Rows[0].TurnKey = "t9"
	changed.TimelineHash = bytes.Repeat([]byte{9}, 32)
	if err == nil {
		err = replaceSessionTimelineTx(ctx, tx, state, changed)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT 1/0`); err == nil {
		t.Fatal("the failing statement succeeded")
	}
	_ = tx.Rollback(ctx)
	if got := timelineDump(t, s, "full-timeline"); got != want {
		t.Fatalf("rollback kept part of the replacement:\n%s", got)
	}

	// Facts unchanged, mapping changed: the timeline is replaced on its own.
	if err = publishTimeline(t, s, state, changed); err != nil {
		t.Fatal(err)
	}
	if got := timelineDump(t, s, "full-timeline"); !strings.HasPrefix(got, fmt.Sprintf("1/%d t9 ", keys[0].RecordID)) {
		t.Fatalf("mapping not replaced:\n%s", got)
	}

	// A truncated session stores no rows, only its provenance.
	truncated := SessionTimelinePublish{State: "truncated", FactOutputHash: h2, SourceGeneration: 6, RulesVersion: 2, MappingVersion: 1}
	if err = publishTimeline(t, s, state, truncated); err != nil {
		t.Fatal(err)
	}
	if got := timelineDump(t, s, "full-timeline"); got != fmt.Sprintf("truncated  - %x 6 2 4 1", h2) {
		t.Fatalf("truncated stored\n%s", got)
	}
	truncated.LoadedKeys = keys[:1]
	truncated.Rows = []SessionTimelineRow{{SessionTimelineKey: keys[0], TurnKey: "t1"}}
	if err = publishTimeline(t, s, state, truncated); err == nil {
		t.Fatal("a truncated timeline stored rows")
	}

	// The source moved since the state was read.
	if err = publishTimeline(t, s, state, base()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,profile_email,uuid,raw) VALUES('2026-01-02','full-timeline','user','p','late','{}')`); err != nil {
		t.Fatal(err)
	}
	if err = publishTimeline(t, s, state, base()); !errors.Is(err, ErrSessionTimelineChanged) {
		t.Fatalf("stale witness: %v", err)
	}
	if got := timelineDump(t, s, "full-timeline"); got != want {
		t.Fatalf("stale witness changed what was stored:\n%s", got)
	}

	// Fact invalidation takes every row, placed or not, and the provenance.
	tx, err = s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = deleteSessionFacts(ctx, tx, []string{"full-timeline"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := timelineDump(t, s, "full-timeline"); got != "no provenance" {
		t.Fatalf("fact invalidation left\n%s", got)
	}
}

// The public helper stores a prepared mapping: the rows it names, the rest not
// loaded, and no publish provenance.
func TestSessionTimelinePublicHelperStoresPrepared(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "prepared-timeline")
	m := timelineSeed(t, s, "prepared-timeline", 2)
	state := timelineState(t, s, "prepared-timeline")
	if err := s.ReplaceSessionTimelineMapping(context.Background(), state, 1, m[:1]); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("1/%d turn {} -\n2/%d - {} not_loaded\nprepared  - - - - 2 1", m[0].RecordID, m[1].RecordID)
	if got := timelineDump(t, s, "prepared-timeline"); got != want {
		t.Fatalf("stored\n%s\nwant\n%s", got, want)
	}
}

// observedTimeline publishes members as placed rows, every loaded key that no
// member names as unplaced.
func observedTimeline(loaded []SessionTimelineKey, members []SessionTimelineMember) SessionTimelinePublish {
	p := SessionTimelinePublish{State: "observed", LoadedKeys: loaded, TimelineHash: bytes.Repeat([]byte{3}, 32),
		FactOutputHash: bytes.Repeat([]byte{4}, 32), SourceGeneration: 1, RulesVersion: 2, MappingVersion: 1}
	placed := map[sessionTimelineTuple]string{}
	for _, m := range members {
		placed[sessionTimelineTuple{m.Timestamp.UnixMicro(), m.RecordType, m.ProfileEmail, m.UUID}] = m.TurnKey
	}
	for _, k := range loaded {
		r := SessionTimelineRow{SessionTimelineKey: k, TurnKey: placed[k.tuple()]}
		if r.TurnKey == "" {
			r.Unmapped = "no_turn"
		}
		p.Rows = append(p.Rows, r)
	}
	return p
}

func readTimelineKeysNow(s *PgStore, id string) (SessionTimelineState, []SessionTimelineKey, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SessionTimelineState{}, nil, err
	}
	defer tx.Rollback(ctx)
	return readSessionTimelineKeys(ctx, tx, id)
}

// Only a published result of the current source at this version is valid:
// never a prepared one, from the public helper or from before the published
// states existed.
func TestSessionTimelineValidityByState(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "valid-timeline")
	ctx := context.Background()
	m := timelineSeed(t, s, "valid-timeline", 2)
	keys := timelineKeys(m)
	state := timelineState(t, s, "valid-timeline")
	versionValid := func(version int) bool {
		t.Helper()
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		current, err := readSessionTimelineState(ctx, tx, "valid-timeline")
		if err != nil {
			t.Fatal(err)
		}
		valid, err := sessionTimelineMappingValid(ctx, tx, current, version)
		if err != nil {
			t.Fatal(err)
		}
		return valid
	}

	if err := s.ReplaceSessionTimelineMapping(ctx, state, 1, m); err != nil {
		t.Fatal(err)
	}
	if versionValid(1) {
		t.Fatal("the public helper's prepared mapping is valid")
	}
	// A provenance row as the migration leaves an older one: prepared, no hashes.
	if _, err := s.pool.Exec(ctx, `UPDATE session_timeline_provenance SET timeline_state='prepared', timeline_reason='',
		timeline_hash=NULL, fact_output_hash=NULL, source_generation=NULL, rules_version=NULL WHERE session_id='valid-timeline'`); err != nil {
		t.Fatal(err)
	}
	if versionValid(1) {
		t.Fatal("an upgraded prepared row is valid")
	}

	observed := observedTimeline(keys, m)
	undeterminable := observedTimeline(keys, nil)
	undeterminable.State, undeterminable.Reason = "undeterminable", "pre_lineage"
	truncated := SessionTimelinePublish{State: "truncated", FactOutputHash: bytes.Repeat([]byte{4}, 32), SourceGeneration: 1, RulesVersion: 2, MappingVersion: 1}
	for name, p := range map[string]SessionTimelinePublish{"observed": observed, "undeterminable": undeterminable, "truncated": truncated} {
		if err := publishTimeline(t, s, state, p); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !versionValid(1) {
			t.Errorf("%s publish is not valid", name)
		}
		if versionValid(2) {
			t.Errorf("%s publish is valid at another mapping version", name)
		}
	}

	// The table constraint and the validity check are separate defences. With
	// the constraint lifted inside a transaction that is rolled back, the rows
	// it would refuse are still not valid.
	if err := publishTimeline(t, s, state, observed); err != nil {
		t.Fatal(err)
	}
	for _, set := range []string{
		`timeline_state='prepared'`,
		`timeline_reason='redacted'`,
		`timeline_state='truncated'`,
		`source_generation=0`,
		`rules_version=0`,
		`fact_output_hash=decode(repeat('11',31),'hex')`,
		`timeline_state='undeterminable'`,
		`timeline_state='complete'`,
	} {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `ALTER TABLE session_timeline_provenance DROP CONSTRAINT session_timeline_provenance_result`); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE session_timeline_provenance SET `+set+` WHERE session_id='valid-timeline'`); err != nil {
			t.Fatal(err)
		}
		current, err := readSessionTimelineState(ctx, tx, "valid-timeline")
		if err != nil {
			t.Fatal(err)
		}
		if valid, err := sessionTimelineMappingValid(ctx, tx, current, 1); err != nil || valid {
			t.Errorf("without the constraint, %s: valid=%v err=%v", set, valid, err)
		}
		if err = tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !versionValid(1) {
		t.Fatal("the rolled-back edits touched the stored provenance")
	}

	// A prepared mapping after a published one is not valid either.
	if err := s.ReplaceSessionTimelineMapping(ctx, state, 1, m); err != nil {
		t.Fatal(err)
	}
	if versionValid(1) {
		t.Fatal("a prepared mapping replacing a published one is valid")
	}
	if err := publishTimeline(t, s, state, observed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,profile_email,uuid,raw) VALUES('2026-01-03','valid-timeline','user','p','late','{}')`); err != nil {
		t.Fatal(err)
	}
	if versionValid(1) {
		t.Fatal("a published mapping of an older source is valid")
	}
}

// Only the same publish of the same source skips the row writes: any field of
// the stored provenance that differs rewrites them.
func TestSessionTimelineReplaceSkipsOnlyTheSamePublish(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	timelineCleanup(t, s, "skip-timeline")
	ctx := context.Background()
	m := timelineSeed(t, s, "skip-timeline", 2)
	state := timelineState(t, s, "skip-timeline")
	xmin := func() string {
		t.Helper()
		var x string
		if err := s.pool.QueryRow(ctx, `SELECT string_agg(xmin::text, ',' ORDER BY source_uuid) FROM session_timeline_members WHERE session_id='skip-timeline'`).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	base := observedTimeline(timelineKeys(m), m)
	if err := publishTimeline(t, s, state, base); err != nil {
		t.Fatal(err)
	}
	before := xmin()
	same := base
	same.SourceGeneration = 7
	if err := publishTimeline(t, s, state, same); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT source_generation FROM session_timeline_provenance WHERE session_id='skip-timeline'`).Scan(&generation); err != nil || generation != 7 {
		t.Fatalf("generation %d err=%v, want 7", generation, err)
	}
	if got := xmin(); got != before {
		t.Fatalf("the same publish rewrote the rows: %s then %s", before, got)
	}
	for name, edit := range map[string]func(*SessionTimelinePublish){
		"timeline hash":   func(p *SessionTimelinePublish) { p.TimelineHash = bytes.Repeat([]byte{8}, 32) },
		"fact hash":       func(p *SessionTimelinePublish) { p.FactOutputHash = bytes.Repeat([]byte{8}, 32) },
		"rules version":   func(p *SessionTimelinePublish) { p.RulesVersion = 3 },
		"mapping version": func(p *SessionTimelinePublish) { p.MappingVersion = 2 },
		"state":           func(p *SessionTimelinePublish) { p.State, p.Reason = "undeterminable", "pre_lineage" },
	} {
		p := base
		edit(&p)
		before := xmin()
		if err := publishTimeline(t, s, state, p); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := xmin(); got == before {
			t.Errorf("%s changed but the rows were not written", name)
		}
		if err := publishTimeline(t, s, state, base); err != nil {
			t.Fatal(err)
		}
	}
	// A prepared mapping of the same source is never the same as a publish.
	if err := s.ReplaceSessionTimelineMapping(ctx, state, 1, m); err != nil {
		t.Fatal(err)
	}
	before = xmin()
	if err := publishTimeline(t, s, state, base); err != nil {
		t.Fatal(err)
	}
	if got := xmin(); got == before {
		t.Error("a publish over a prepared mapping skipped the rows")
	}
}
