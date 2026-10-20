package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// messageIDLine is a Claude assistant line of session sid.
func messageIDLine(sid, uuid string, ts time.Time, raw string) *SessionRecord {
	return &SessionRecord{Ts: ts, SessionID: sid, UUID: uuid, RecordType: "assistant",
		ProfileEmail: "user@example.com", Agent: "claude", Raw: json.RawMessage(raw)}
}

// forgetMessageIDs puts a session's rows in the state of a row written before the
// column existed, which is what the backfill is for.
func forgetMessageIDs(t *testing.T, s *PgStore, sessionID string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`UPDATE session_records SET message_id = NULL WHERE session_id = $1`, sessionID); err != nil {
		t.Fatalf("forget message_id: %v", err)
	}
}

// resetMessageIDBackfill removes the marker and the cursor, so the next call
// starts a walk of its own, and removes them again when the test ends.
func resetMessageIDBackfill(t *testing.T, s *PgStore) {
	t.Helper()
	clear := func() {
		ctx := context.Background()
		if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, sessionRecordMessageIDBackfill); err != nil {
			t.Fatalf("clear message_id marker: %v", err)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM session_records_message_id_cursor`); err != nil {
			t.Fatalf("clear message_id cursor: %v", err)
		}
	}
	clear()
	t.Cleanup(clear)
}

func messageIDBackfillMarked(t *testing.T, s *PgStore) bool {
	t.Helper()
	var marked bool
	if err := s.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, sessionRecordMessageIDBackfill).Scan(&marked); err != nil {
		t.Fatalf("read message_id marker: %v", err)
	}
	return marked
}

// messageIDOf answers a row's message_id by uuid, nil for NULL.
func messageIDOf(t *testing.T, s *PgStore, uuid string) *string {
	t.Helper()
	var id *string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT message_id FROM session_records WHERE uuid = $1`, uuid).Scan(&id); err != nil {
		t.Fatalf("read message_id of %s: %v", uuid, err)
	}
	return id
}

// xminOf is the transaction that wrote the row's current version. Any UPDATE,
// even one writing the value the row already holds, replaces it.
func xminOf(t *testing.T, s *PgStore, uuid string) string {
	t.Helper()
	var xmin string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT xmin::text FROM session_records WHERE uuid = $1`, uuid).Scan(&xmin); err != nil {
		t.Fatalf("read xmin of %s: %v", uuid, err)
	}
	return xmin
}

type messageIDCursorRow struct {
	start, end, next time.Time
}

func readMessageIDCursor(t *testing.T, s *PgStore) (messageIDCursorRow, bool) {
	t.Helper()
	var c messageIDCursorRow
	err := s.pool.QueryRow(context.Background(), `SELECT range_start, range_end, next_window_end
		FROM session_records_message_id_cursor`).Scan(&c.start, &c.end, &c.next)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false
	}
	if err != nil {
		t.Fatalf("read message_id cursor: %v", err)
	}
	return c, true
}

func idString(id *string) string {
	if id == nil {
		return "NULL"
	}
	return fmt.Sprintf("%q", *id)
}

func ptrString(s string) *string { return &s }

// The insert fills the column from the values the row is stored with: the record
// type after codexRecordType, the agent after its default, the raw after
// sanitizeJSONNUL. Only a Claude assistant line gets a value.
func TestInsertSessionRecordsFillsMessageID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ts := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	line := func(sid, uuid, agent, recordType, raw string, sec int) *SessionRecord {
		return &SessionRecord{Ts: ts.Add(time.Duration(sec) * time.Second), SessionID: sid, UUID: uuid,
			RecordType: recordType, ProfileEmail: "user@example.com", Agent: agent, Raw: json.RawMessage(raw)}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		line("mid-claude", "with-id", "claude", "assistant", `{"message":{"id":"msg_01A","role":"assistant"}}`, 0),
		line("mid-claude", "no-id", "claude", "assistant", `{"message":{"role":"assistant","content":[]}}`, 1),
		// No agent: stored as claude, and the id follows what is stored.
		line("mid-claude", "default-agent", "", "assistant", `{"message":{"id":"msg_01B"}}`, 2),
		// The NUL escape jsonb refuses. Read from anything but the sanitised raw,
		// the cast would fail the whole batch.
		line("mid-claude", "nul-id", "claude", "assistant", `{"message":{"id":"msg_\u0000C"}}`, 3),
		// Each of these carries a message.id too, so a NULL below is the record
		// type or the agent speaking, not a missing id.
		line("mid-claude", "claude-user", "claude", "user", `{"message":{"id":"msg_user","role":"user","content":"hi"}}`, 4),
		line("mid-codex", "codex-usage", "codex", "usage", `{"type":"event_msg","message":{"id":"msg_codex"}}`, 5),
		line("mid-gjc", "gjc-assistant", "gjc", "assistant", `{"message":{"id":"msg_gjc","usage":{}}}`, 6),
		line("mid-omo", "omo-assistant", "omo", "assistant", `{"message":{"id":"msg_omo"}}`, 7),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	want := map[string]*string{
		"with-id":       ptrString("msg_01A"),
		"no-id":         ptrString(""),
		"default-agent": ptrString("msg_01B"),
		"nul-id":        ptrString("msg_�C"),
		"claude-user":   nil,
		"codex-usage":   nil,
		"gjc-assistant": nil,
		"omo-assistant": nil,
	}
	for uuid, w := range want {
		if got := messageIDOf(t, s, uuid); idString(got) != idString(w) {
			t.Errorf("%s: message_id = %s, want %s", uuid, idString(got), idString(w))
		}
	}

	// The backfill writes the same expression over the stored columns. Every row
	// the insert wrote has to agree with it, or a filled row would depend on which
	// of the two filled it.
	var rows, disagree int
	if err := s.pool.QueryRow(ctx, `SELECT count(*),
			count(*) FILTER (WHERE message_id IS DISTINCT FROM (`+
		fmt.Sprintf(sessionRecordMessageIDSQL, "record_type", "agent", "raw")+`))
		FROM session_records WHERE session_id LIKE 'mid-%'`).Scan(&rows, &disagree); err != nil {
		t.Fatalf("compare with the shared expression: %v", err)
	}
	if rows != len(want) || disagree != 0 {
		t.Errorf("%d rows, %d disagreeing with sessionRecordMessageIDSQL over the stored columns; want %d and 0",
			rows, disagree, len(want))
	}
}

// The boot that adds the column has to hand it to visible_session_records, a
// SELECT * view, on that same boot. Every boot after it must lock nothing:
// Migrate runs on every boot, and an ADD COLUMN on session_records takes ACCESS
// EXCLUSIVE on the hypertable and every chunk even when IF NOT EXISTS makes it a
// no-op.
func TestMessageIDColumnMigration(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// A database from before the column, the way Migrate itself would find one:
	// the SELECT * views have to go before the column can.
	for _, stmt := range append(append([]string{}, dropDependentViews...),
		`ALTER TABLE session_records DROP COLUMN IF EXISTS message_id`) {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// Whatever fails below, the next test gets the column and the views back.
	t.Cleanup(func() { _ = s.Migrate(ctx) })
	// The boot that adds it, and the one after.
	for boot := 1; boot <= 2; boot++ {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate (boot %d): %v", boot, err)
		}
		var columns int
		var dataType, nullable string
		if err := s.pool.QueryRow(ctx, `SELECT count(*), COALESCE(max(data_type), ''), COALESCE(max(is_nullable), '')
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'session_records' AND column_name = 'message_id'`).
			Scan(&columns, &dataType, &nullable); err != nil {
			t.Fatalf("read message_id column: %v", err)
		}
		if columns != 1 || dataType != "text" || nullable != "YES" {
			t.Fatalf("boot %d: message_id has %d column(s), type %q, nullable %q; want one nullable text column",
				boot, columns, dataType, nullable)
		}
		var inView bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'visible_session_records'
			  AND column_name = 'message_id')`).Scan(&inView); err != nil {
			t.Fatalf("read visible_session_records columns: %v", err)
		}
		if !inView {
			t.Fatalf("boot %d: visible_session_records has no message_id -- the column was added after the view was recreated", boot)
		}
	}

	if !slices.Contains(migrations, sessionRecordsMessageIDColumnDDL) ||
		slices.Contains(dependentViews, sessionRecordsMessageIDColumnDDL) {
		t.Fatal("sessionRecordsMessageIDColumnDDL is not in the migrations group, ahead of the views it has to reach")
	}

	// exclusiveLocksHeld runs stmt in a transaction it keeps open and counts, from
	// a second connection, the ACCESS EXCLUSIVE locks that transaction holds.
	exclusiveLocksHeld := func(stmt string) int {
		t.Helper()
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer conn.Release()
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck // the statement must not stay applied
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '10s'`); err != nil {
			t.Fatalf("set lock_timeout: %v", err)
		}
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatalf("read pid: %v", err)
		}
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("run %s: %v", firstLine(stmt), err)
		}
		var held int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE pid = $1 AND mode = 'AccessExclusiveLock' AND granted`, pid).Scan(&held); err != nil {
			t.Fatalf("read pg_locks: %v", err)
		}
		return held
	}

	// The probe has to be able to see what it is looking for: a bare ADD COLUMN IF
	// NOT EXISTS, with nothing to add, still takes the lock.
	if held := exclusiveLocksHeld(`ALTER TABLE session_records ADD COLUMN IF NOT EXISTS message_id TEXT`); held == 0 {
		t.Fatal("a no-op ADD COLUMN IF NOT EXISTS showed no ACCESS EXCLUSIVE lock; the probe cannot see one")
	}
	if held := exclusiveLocksHeld(sessionRecordsMessageIDColumnDDL); held != 0 {
		t.Errorf("the message_id migration holds %d ACCESS EXCLUSIVE lock(s) on a database that already has the column", held)
	}
}

// One window per call, newest first, and the marker with the last window and not
// before it. Rows that are not Claude assistant lines, and a line with no ts of
// its own, are never the walk's.
//
// The rows sit at fractions of messageIDBackfillWindow, so that whatever its
// length they fall in four consecutive windows, none of them empty, and each call
// fills the next. (An empty window costs a call too; see
// TestMessageIDBackfillSpendsACallOnAnEmptyWindow.) At 24 hours the walk's rows
// are 0, 5, 30, 50 and 73 hours back.
func TestMessageIDBackfillWalksNewestWindowFirst(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rangeStart := hi.Add(-messageIDBackfillWindow * 73 / 24)
	const sid = "mid-walk"
	records := []*SessionRecord{
		messageIDLine(sid, "w1-max", hi, `{"message":{"id":"msg_w1"}}`),
		messageIDLine(sid, "w1-no-id", hi.Add(-messageIDBackfillWindow*5/24), `{"message":{"role":"assistant"}}`),
		messageIDLine(sid, "w2", hi.Add(-messageIDBackfillWindow*5/4), `{"message":{"id":"msg_w2"}}`),
		messageIDLine(sid, "w3", hi.Add(-messageIDBackfillWindow*25/12), `{"message":{"id":"msg_w3"}}`),
		// The range start, alone in a window cut off below at it.
		messageIDLine(sid, "w4-min", rangeStart, `{"message":{"id":"msg_w4"}}`),
		// Inside the range, and none of them the walk's.
		{Ts: hi.Add(-messageIDBackfillWindow / 12), SessionID: sid, UUID: "user", RecordType: "user", ProfileEmail: "user@example.com",
			Agent: "claude", Raw: json.RawMessage(`{"message":{"id":"msg_user","role":"user","content":"hi"}}`)},
		{Ts: hi.Add(-messageIDBackfillWindow / 8), SessionID: "mid-walk-codex", UUID: "codex", RecordType: "usage", ProfileEmail: "user@example.com",
			Agent: "codex", Raw: json.RawMessage(`{"message":{"id":"msg_codex"}}`)},
		{Ts: hi.Add(-messageIDBackfillWindow / 6), SessionID: "mid-walk-gjc", UUID: "gjc", RecordType: "assistant", ProfileEmail: "user@example.com",
			Agent: "gjc", Raw: json.RawMessage(`{"message":{"id":"msg_gjc"}}`)},
		// A line stored at the zero instant. Counted in the range it would make
		// the walk start in 1970.
		messageIDLine(sid, "epoch", time.Unix(0, 0).UTC(), `{"message":{"id":"msg_epoch"}}`),
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	for _, id := range []string{sid, "mid-walk-codex", "mid-walk-gjc"} {
		forgetMessageIDs(t, s, id)
	}

	windows := [][]string{{"w1-max", "w1-no-id"}, {"w2"}, {"w3"}, {"w4-min"}}
	want := map[string]string{"w1-max": "msg_w1", "w1-no-id": "", "w2": "msg_w2", "w3": "msg_w3", "w4-min": "msg_w4"}
	var walk messageIDWalk
	for i := range windows {
		finished, err := s.sessionRecordMessageIDStep(ctx, &walk)
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		last := i == len(windows)-1
		if finished != last {
			t.Fatalf("call %d: finished = %v, want %v", i+1, finished, last)
		}
		if marked := messageIDBackfillMarked(t, s); marked != last {
			t.Fatalf("call %d: marker present = %v, want %v -- it commits with the last window", i+1, marked, last)
		}
		for j, window := range windows {
			for _, uuid := range window {
				got := messageIDOf(t, s, uuid)
				if j <= i {
					if got == nil || *got != want[uuid] {
						t.Errorf("call %d: %s = %s, want %q", i+1, uuid, idString(got), want[uuid])
					}
				} else if got != nil {
					t.Errorf("call %d: %s = %s, want NULL until window %d", i+1, uuid, idString(got), j+1)
				}
			}
		}
		if i == 0 {
			c, ok := readMessageIDCursor(t, s)
			if !ok || !c.start.Equal(rangeStart) || !c.end.Equal(hi) || !c.next.Equal(hi.Add(-messageIDBackfillWindow)) {
				t.Fatalf("cursor after the first call = %+v (present %v), want range [%s, %s] and next window ending %s",
					c, ok, rangeStart, hi, hi.Add(-messageIDBackfillWindow))
			}
		}
	}
	for _, uuid := range []string{"user", "codex", "gjc", "epoch"} {
		if got := messageIDOf(t, s, uuid); got != nil {
			t.Errorf("%s = %s, want NULL: the walk is Claude assistant lines with a real ts", uuid, idString(got))
		}
	}
}

// The range is the one the first call recorded. A row that arrives afterwards is
// the insert path's, and a row already holding a value is never rewritten.
func TestMessageIDBackfillKeepsItsRecordedRange(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const sid = "mid-range"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "max", hi, `{"message":{"id":"msg_max"}}`),
		messageIDLine(sid, "kept", hi.Add(-messageIDBackfillWindow*5/12), `{"message":{"id":"msg_raw"}}`),
		messageIDLine(sid, "min", hi.Add(-messageIDBackfillWindow*5/4), `{"message":{"id":"msg_min"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)
	// A value the expression would not give, so a rewrite cannot hide.
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET message_id = 'set-before' WHERE uuid = 'kept'`); err != nil {
		t.Fatalf("seed kept: %v", err)
	}
	keptXmin := xminOf(t, s, "kept")

	var walk messageIDWalk
	if finished, err := s.sessionRecordMessageIDStep(ctx, &walk); err != nil || finished {
		t.Fatalf("first call: finished %v, err %v; want one window done and one to go", finished, err)
	}

	// After the range is recorded: one row past its end and one late into the
	// window just walked, both through the insert path, which fills them.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "after-range", hi.Add(2*time.Hour), `{"message":{"id":"msg_after"}}`),
		messageIDLine(sid, "late-walked", hi.Add(-messageIDBackfillWindow/24), `{"message":{"id":"msg_late"}}`),
		messageIDLine(sid, "beyond", hi.Add(3*time.Hour), `{"message":{"id":"msg_beyond"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords after the range: %v", err)
	}
	for uuid, w := range map[string]string{"after-range": "msg_after", "late-walked": "msg_late"} {
		if got := messageIDOf(t, s, uuid); got == nil || *got != w {
			t.Fatalf("%s = %s, want %q from the insert path", uuid, idString(got), w)
		}
	}
	afterXmin, lateXmin := xminOf(t, s, "after-range"), xminOf(t, s, "late-walked")
	// And one past the end left NULL, the way only a row the walk must not reach
	// would be: had the walk re-read max(ts), it would fill it.
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET message_id = NULL WHERE uuid = 'beyond'`); err != nil {
		t.Fatalf("clear beyond: %v", err)
	}

	if _, err := s.BackfillSessionRecordMessageIDs(ctx); err != nil {
		t.Fatalf("BackfillSessionRecordMessageIDs: %v", err)
	}
	if !messageIDBackfillMarked(t, s) {
		t.Fatal("marker missing after the walk returned")
	}
	if got := messageIDOf(t, s, "min"); got == nil || *got != "msg_min" {
		t.Errorf("min = %s, want \"msg_min\"", idString(got))
	}
	if got := messageIDOf(t, s, "beyond"); got != nil {
		t.Errorf("beyond = %s, want NULL: it is past the range the first call recorded", idString(got))
	}
	if c, ok := readMessageIDCursor(t, s); !ok || !c.end.Equal(hi) {
		t.Errorf("cursor range end = %s (present %v), want %s", c.end, ok, hi)
	}
	for uuid, before := range map[string]string{"kept": keptXmin, "after-range": afterXmin, "late-walked": lateXmin} {
		if now := xminOf(t, s, uuid); now != before {
			t.Errorf("%s was rewritten by the walk (xmin %s -> %s)", uuid, before, now)
		}
	}
	if got := messageIDOf(t, s, "kept"); got == nil || *got != "set-before" {
		t.Errorf("kept = %s, want \"set-before\"", idString(got))
	}
}

// Once the marker exists a call does nothing, even with the cursor gone. Taking
// the marker away as well -- the rollback runbook removes both -- starts a new
// walk over a newly recorded range, which fills what an older binary left NULL.
func TestMessageIDBackfillRestartsOnlyWithoutMarkerAndCursor(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const sid = "mid-restart"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "max", hi, `{"message":{"id":"msg_max"}}`),
		messageIDLine(sid, "min", hi.Add(-messageIDBackfillWindow*5/4), `{"message":{"id":"msg_min"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)
	if _, err := s.BackfillSessionRecordMessageIDs(ctx); err != nil {
		t.Fatalf("first walk: %v", err)
	}
	if !messageIDBackfillMarked(t, s) {
		t.Fatal("marker missing after the first walk")
	}

	// What a rollback leaves: a row the old binary inserted past the first walk's
	// range, and the rows inside that range NULL again.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "old-binary", hi.Add(5*time.Hour), `{"message":{"id":"msg_old_binary"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_records_message_id_cursor`); err != nil {
		t.Fatalf("delete cursor: %v", err)
	}
	var walk messageIDWalk
	for i := 0; i < 2; i++ {
		finished, err := s.sessionRecordMessageIDStep(ctx, &walk)
		if err != nil || !finished {
			t.Fatalf("call %d after the marker: finished %v, err %v; want a finished no-op", i+1, finished, err)
		}
	}
	for _, uuid := range []string{"max", "min", "old-binary"} {
		if got := messageIDOf(t, s, uuid); got != nil {
			t.Errorf("after the marker: %s = %s, want NULL -- the call must do nothing", uuid, idString(got))
		}
	}
	if _, ok := readMessageIDCursor(t, s); ok {
		t.Error("a call after the marker recorded a new range")
	}

	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, sessionRecordMessageIDBackfill); err != nil {
		t.Fatalf("delete marker: %v", err)
	}
	if _, err := s.BackfillSessionRecordMessageIDs(ctx); err != nil {
		t.Fatalf("second walk: %v", err)
	}
	for uuid, w := range map[string]string{"max": "msg_max", "min": "msg_min", "old-binary": "msg_old_binary"} {
		if got := messageIDOf(t, s, uuid); got == nil || *got != w {
			t.Errorf("after the restart: %s = %s, want %q", uuid, idString(got), w)
		}
	}
	if c, ok := readMessageIDCursor(t, s); !ok || !c.end.Equal(hi.Add(5*time.Hour)) {
		t.Errorf("restarted range end = %s (present %v), want %s", c.end, ok, hi.Add(5*time.Hour))
	}
	if !messageIDBackfillMarked(t, s) {
		t.Error("marker missing after the second walk")
	}
}

// A row another writer holds -- here an uncommitted UPDATE, as sync reenrich or a
// login_email pass leaves one -- fails its batch at once with 55P03 rather than
// after messageIDBackfillLockTimeout: the backfill takes the batch's rows NOWAIT
// and never waits on a row. Nothing of the batch is written (a window this small
// is one batch), the cursor stays in front of the window, and once the writer is
// done the same call fills it.
func TestMessageIDBackfillYieldsToARowWriter(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const sid = "mid-yield"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "w1", hi, `{"message":{"id":"msg_w1"}}`),
		messageIDLine(sid, "w2-free", hi.Add(-messageIDBackfillWindow*5/4), `{"message":{"id":"msg_w2_free"}}`),
		messageIDLine(sid, "w2-held", hi.Add(-messageIDBackfillWindow*31/24), `{"message":{"id":"msg_w2_held"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)
	var walk messageIDWalk
	if finished, err := s.sessionRecordMessageIDStep(ctx, &walk); err != nil || finished {
		t.Fatalf("first call: finished %v, err %v", finished, err)
	}

	writer, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin writer: %v", err)
	}
	defer writer.Rollback(ctx) //nolint:errcheck // committed below
	if _, err := writer.Exec(ctx, `UPDATE session_records SET entrypoint = entrypoint WHERE uuid = 'w2-held'`); err != nil {
		t.Fatalf("writer UPDATE: %v", err)
	}
	before := map[string]string{"w2-free": xminOf(t, s, "w2-free"), "w2-held": xminOf(t, s, "w2-held")}

	// Bounded past the lock timeout, so a step that waits on the row fails here
	// instead of hanging the package.
	waitCtx, cancel := context.WithTimeout(ctx, messageIDBackfillLockTimeout+7*time.Second)
	defer cancel()
	started := time.Now()
	finished, err := s.sessionRecordMessageIDStep(waitCtx, &walk)
	elapsed := time.Since(started)
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		t.Fatalf("the call filled a window whose row another writer holds (finished %v)", finished)
	case !errors.As(err, &pgErr) || pgErr.Code != "55P03" || !isContendedLockError(err):
		t.Fatalf("held row failed the window with %v, want 55P03 that isContendedLockError recognises", err)
	}
	if elapsed >= messageIDBackfillLockTimeout/2 {
		t.Errorf("the window failed after %s, want well under the %s lock_timeout: the backfill waited on the row", elapsed, messageIDBackfillLockTimeout)
	}
	for uuid, xmin := range before {
		if now := xminOf(t, s, uuid); now != xmin {
			t.Errorf("%s was written by the failed window (xmin %s -> %s)", uuid, xmin, now)
		}
		if got := messageIDOf(t, s, uuid); got != nil {
			t.Errorf("%s = %s after the failed window, want NULL", uuid, idString(got))
		}
	}
	if c, ok := readMessageIDCursor(t, s); !ok || !c.next.Equal(hi.Add(-messageIDBackfillWindow)) {
		t.Fatalf("cursor after the failed window = %+v (present %v), want it still in front of that window", c, ok)
	}
	if messageIDBackfillMarked(t, s) {
		t.Fatal("marker written by a window that failed")
	}

	if err := writer.Commit(ctx); err != nil {
		t.Fatalf("writer commit: %v", err)
	}
	if finished, err := s.sessionRecordMessageIDStep(ctx, &walk); err != nil || !finished {
		t.Fatalf("retry: finished %v, err %v; want the same window filled and the walk done", finished, err)
	}
	for uuid, w := range map[string]string{"w2-free": "msg_w2_free", "w2-held": "msg_w2_held"} {
		if got := messageIDOf(t, s, uuid); got == nil || *got != w {
			t.Errorf("%s after the retry = %s, want %q", uuid, idString(got), w)
		}
	}
}

// The locks a batch still waits on are bounded by messageIDBackfillLockTimeout.
// An identity-wide admin change holds the source lock exclusively; the batch
// rolls back after the timeout with an error isContendedLockError recognises,
// and once the change is done the same call fills it.
func TestMessageIDBackfillLockTimeoutBoundsTheSourceLockWait(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const sid = "mid-lock"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "w1", hi, `{"message":{"id":"msg_w1"}}`),
		messageIDLine(sid, "w2", hi.Add(-messageIDBackfillWindow*5/4), `{"message":{"id":"msg_w2"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)
	var walk messageIDWalk
	if finished, err := s.sessionRecordMessageIDStep(ctx, &walk); err != nil || finished {
		t.Fatalf("first call: finished %v, err %v", finished, err)
	}

	admin, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin admin: %v", err)
	}
	defer admin.Rollback(ctx) //nolint:errcheck // released below
	if err := lockSessionOverviewSourceMutation(ctx, admin, true); err != nil {
		t.Fatalf("hold the source lock: %v", err)
	}

	// Bounded well past the lock timeout, so a step that waits forever fails here
	// instead of hanging the package.
	waitCtx, cancel := context.WithTimeout(ctx, messageIDBackfillLockTimeout+7*time.Second)
	defer cancel()
	finished, err := s.sessionRecordMessageIDStep(waitCtx, &walk)
	switch {
	case err == nil:
		t.Fatalf("the call filled a window while an admin change held the source lock (finished %v)", finished)
	case !isContendedLockError(err) || !isLockTimeout(err):
		t.Fatalf("held source lock failed the window with %v, want a lock timeout isContendedLockError recognises", err)
	}
	if c, ok := readMessageIDCursor(t, s); !ok || !c.next.Equal(hi.Add(-messageIDBackfillWindow)) {
		t.Fatalf("cursor after the failed window = %+v (present %v), want it still in front of that window", c, ok)
	}
	if got := messageIDOf(t, s, "w2"); got != nil || messageIDBackfillMarked(t, s) {
		t.Fatalf("after the failed window: w2 = %s, marker %v; want NULL and no marker", idString(got), messageIDBackfillMarked(t, s))
	}

	if err := admin.Rollback(ctx); err != nil {
		t.Fatalf("release the source lock: %v", err)
	}
	if finished, err := s.sessionRecordMessageIDStep(ctx, &walk); err != nil || !finished {
		t.Fatalf("retry: finished %v, err %v; want the same window filled and the walk done", finished, err)
	}
	if got := messageIDOf(t, s, "w2"); got == nil || *got != "msg_w2" {
		t.Errorf("w2 after the retry = %s, want \"msg_w2\"", idString(got))
	}
}

// A range that is an exact number of windows puts rows on the edges: one at the
// range end, one where two windows meet, one at the range start. A window is
// open below and closed above, and the walk ends only once a window has gone
// below the range start, so each edge row lands in exactly one window.
func TestMessageIDBackfillFillsWindowBoundaries(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const sid = "mid-edges"
	want := map[string]string{"range-end": "msg_end", "boundary": "msg_boundary", "range-start": "msg_start"}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "range-end", hi, `{"message":{"id":"msg_end"}}`),
		messageIDLine(sid, "boundary", hi.Add(-messageIDBackfillWindow), `{"message":{"id":"msg_boundary"}}`),
		messageIDLine(sid, "range-start", hi.Add(-2*messageIDBackfillWindow), `{"message":{"id":"msg_start"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)

	var walk messageIDWalk
	for call := 1; ; call++ {
		finished, err := s.sessionRecordMessageIDStep(ctx, &walk)
		if err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		if finished {
			break
		}
		if call > 10 {
			t.Fatal("the walk did not finish in 10 calls")
		}
	}
	for uuid, w := range want {
		if got := messageIDOf(t, s, uuid); got == nil || *got != w {
			t.Errorf("%s = %s, want %q", uuid, idString(got), w)
		}
	}
	// Two full windows and the one that reaches the range start.
	if walk.windows != 3 || walk.filled != 3 {
		t.Errorf("walked %d window(s) filling %d row(s), want 3 and 3", walk.windows, walk.filled)
	}
	if !messageIDBackfillMarked(t, s) {
		t.Error("marker missing after the walk finished")
	}
}

// The cursor stays on a window whose batch found the cap, so a batch has to fill
// every row it locked: a row left NULL is picked again by the next call, and the
// walk would commit the same batch for ever without an error. Nothing in the
// schema leaves a locked row unfilled, so a trigger that drops one row's update
// stands in for whatever would. The call fails and commits nothing, and the walk
// goes on once the row can be written.
func TestMessageIDBackfillFailsABatchThatLeftALockedRowUnfilled(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const sid = "mid-unfilled"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "kept", hi, `{"message":{"id":"msg_kept"}}`),
		messageIDLine(sid, "dropped", hi.Add(-time.Minute), `{"message":{"id":"msg_dropped"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)

	removed := false
	removeTrigger := func() {
		if removed {
			return
		}
		removed = true
		for _, q := range []string{
			`DROP TRIGGER IF EXISTS mid_drop_update ON session_records`,
			`DROP FUNCTION IF EXISTS mid_drop_update()`,
		} {
			if _, err := s.pool.Exec(context.Background(), q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	t.Cleanup(removeTrigger)
	for _, q := range []string{
		`CREATE FUNCTION mid_drop_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			IF NEW.uuid = 'dropped' THEN RETURN NULL; END IF;
			RETURN NEW;
		END $$`,
		`CREATE TRIGGER mid_drop_update BEFORE UPDATE ON session_records
			FOR EACH ROW EXECUTE FUNCTION mid_drop_update()`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("install the trigger: %v", err)
		}
	}

	var walk messageIDWalk
	finished, err := s.sessionRecordMessageIDStep(ctx, &walk)
	if err == nil || finished {
		t.Fatalf("a batch that left a locked row unfilled: finished=%v err=%v, want an error", finished, err)
	}
	if !strings.Contains(err.Error(), "locked 2 row(s), filled 1") {
		t.Errorf("error = %v, want it to say 2 rows were locked and 1 filled", err)
	}
	// The whole batch rolled back, with the range the same call had recorded.
	if got := messageIDOf(t, s, "kept"); got != nil {
		t.Errorf("kept = %s after the failed batch, want NULL", idString(got))
	}
	if _, ok := readMessageIDCursor(t, s); ok {
		t.Error("the failed first call left a cursor")
	}
	if messageIDBackfillMarked(t, s) || walk.batches != 0 || walk.filled != 0 {
		t.Errorf("after the failed batch: marked=%v batches=%d filled=%d, want none",
			messageIDBackfillMarked(t, s), walk.batches, walk.filled)
	}

	removeTrigger()
	finished, err = s.sessionRecordMessageIDStep(ctx, &walk)
	if err != nil || !finished {
		t.Fatalf("the retry: finished=%v err=%v, want the walk finished", finished, err)
	}
	for uuid, w := range map[string]string{"kept": "msg_kept", "dropped": "msg_dropped"} {
		if got := messageIDOf(t, s, uuid); got == nil || *got != w {
			t.Errorf("%s = %s, want %q", uuid, idString(got), w)
		}
	}
}

// A window with nothing to fill costs a call all the same: the cursor moves one
// window at a time and does not jump to the next row. Two rows two and a half
// windows apart leave the window between them empty, and that call commits,
// moves the cursor and does not finish the walk.
func TestMessageIDBackfillSpendsACallOnAnEmptyWindow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const sid = "mid-gap"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "newest", hi, `{"message":{"id":"msg_newest"}}`),
		messageIDLine(sid, "oldest", hi.Add(-messageIDBackfillWindow*5/2), `{"message":{"id":"msg_oldest"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)

	var walk messageIDWalk
	for i, want := range []struct {
		finished bool
		filled   int64 // rows filled by the walk so far
		next     time.Time
	}{
		{false, 1, hi.Add(-messageIDBackfillWindow)},
		{false, 1, hi.Add(-2 * messageIDBackfillWindow)},
		{true, 2, hi.Add(-3 * messageIDBackfillWindow)},
	} {
		finished, err := s.sessionRecordMessageIDStep(ctx, &walk)
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		c, ok := readMessageIDCursor(t, s)
		if finished != want.finished || walk.windows != i+1 || walk.filled != want.filled || !ok || !c.next.Equal(want.next) {
			t.Fatalf("call %d: finished %v, %d window(s) walked filling %d row(s), cursor %+v (present %v); want %v, %d, %d and the next window ending %s",
				i+1, finished, walk.windows, walk.filled, c, ok, want.finished, i+1, want.filled, want.next)
		}
		if marked := messageIDBackfillMarked(t, s); marked != want.finished {
			t.Fatalf("call %d: marker present = %v, want %v", i+1, marked, want.finished)
		}
	}
	for uuid, w := range map[string]string{"newest": "msg_newest", "oldest": "msg_oldest"} {
		if got := messageIDOf(t, s, uuid); got == nil || *got != w {
			t.Errorf("%s = %s, want %q", uuid, idString(got), w)
		}
	}
}

// The range ends: no Claude assistant line with a real ts at all, and a range
// that is a single instant.
func TestMessageIDBackfillRangeEdges(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: at, SessionID: "mid-edge-user", UUID: "user", RecordType: "user", ProfileEmail: "user@example.com",
			Agent: "claude", Raw: json.RawMessage(`{"message":{"role":"user","content":"hi"}}`)},
		messageIDLine("mid-edge-epoch", "epoch", time.Unix(0, 0).UTC(), `{"message":{"id":"msg_epoch"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, "mid-edge-epoch")
	var walk messageIDWalk
	finished, err := s.sessionRecordMessageIDStep(ctx, &walk)
	if err != nil || !finished || !messageIDBackfillMarked(t, s) {
		t.Fatalf("empty range: finished %v, err %v, marker %v; want done in one call", finished, err, messageIDBackfillMarked(t, s))
	}
	if _, ok := readMessageIDCursor(t, s); ok {
		t.Error("empty range recorded a cursor")
	}

	resetMessageIDBackfill(t, s)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine("mid-edge-single", "single", at, `{"message":{"id":"msg_single"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, "mid-edge-single")
	finished, err = s.sessionRecordMessageIDStep(ctx, &walk)
	if err != nil || !finished || !messageIDBackfillMarked(t, s) {
		t.Fatalf("min = max: finished %v, err %v, marker %v; want done in one call", finished, err, messageIDBackfillMarked(t, s))
	}
	if got := messageIDOf(t, s, "single"); got == nil || *got != "msg_single" {
		t.Errorf("single = %s, want \"msg_single\"", idString(got))
	}
}

// A call reports whether it committed anything: false only for a walk already
// done when it began. A range with no line at all, whose call commits nothing
// but the marker, a history shorter than a window, and a call left with only the
// last window to commit all report it. (A call stopped by an error reports the
// batches it committed before; see TestMessageIDBackfillAHeldRowFailsOnlyItsBatch
// and TestRequestSetMigrationResumesAtTheFailedStep.)
func TestMessageIDBackfillReportsWhetherItCommitted(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	backfill := func(when string, want bool) {
		t.Helper()
		if progressed, err := s.BackfillSessionRecordMessageIDs(ctx); err != nil || progressed != want {
			t.Fatalf("%s: progressed %v, err %v; want %v and no error", when, progressed, err, want)
		}
	}

	resetMessageIDBackfill(t, s)
	backfill("empty range", true)
	backfill("empty range, marked", false)

	resetMessageIDBackfill(t, s)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine("mid-progress-one", "one-a", hi, `{"message":{"id":"msg_one_a"}}`),
		messageIDLine("mid-progress-one", "one-b", hi.Add(-messageIDBackfillWindow/8), `{"message":{"id":"msg_one_b"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, "mid-progress-one")
	backfill("one-window walk", true)
	backfill("one-window walk, marked", false)

	// A row more than a window back makes it two windows; the first is walked
	// before the call, which then has only the last one to commit, with the marker.
	resetMessageIDBackfill(t, s)
	forgetMessageIDs(t, s, "mid-progress-one")
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine("mid-progress-two", "two-old", hi.Add(-messageIDBackfillWindow*5/4), `{"message":{"id":"msg_two_old"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, "mid-progress-two")
	var walk messageIDWalk
	if finished, err := s.sessionRecordMessageIDStep(ctx, &walk); err != nil || finished {
		t.Fatalf("first window: finished %v, err %v; want one window done and one to go", finished, err)
	}
	backfill("last window only", true)
	if got := messageIDOf(t, s, "two-old"); got == nil || *got != "msg_two_old" {
		t.Errorf("two-old = %s, want \"msg_two_old\"", idString(got))
	}
	backfill("two-window walk, marked", false)
}

// messageIDTimes are n instants from `from` back, a step apart, 4,000 steps to a
// window: the longest run the row-cap tests write, 2,007 rows, stays inside the
// window it is meant for whatever that window's length.
func messageIDTimes(from time.Time, n int) []time.Time {
	step := (messageIDBackfillWindow / 4000).Truncate(time.Microsecond)
	ats := make([]time.Time, n)
	for i := range ats {
		ats[i] = from.Add(-time.Duration(i) * step)
	}
	return ats
}

// messageIDRowsAt writes a Claude assistant line of session sid at each of ats --
// uuid prefix-index, and msg_ and its uuid for an id unless noID says the line has
// none -- and puts the rows in the state of rows written before the column. It
// returns the uuids in the order of ats and records in want the message_id the
// backfill has to give each: the id, or the empty string for a line with none.
func messageIDRowsAt(t *testing.T, s *PgStore, sid, prefix string, ats []time.Time, noID func(i int) bool,
	want map[string]string) []string {
	t.Helper()
	uuids := make([]string, len(ats))
	records := make([]*SessionRecord, len(ats))
	for i, at := range ats {
		uuid := fmt.Sprintf("%s-%04d", prefix, i)
		raw, id := `{"message":{"role":"assistant"}}`, ""
		if noID == nil || !noID(i) {
			id = "msg_" + uuid
			raw = fmt.Sprintf(`{"message":{"id":%q}}`, id)
		}
		uuids[i], want[uuid] = uuid, id
		records[i] = messageIDLine(sid, uuid, at, raw)
	}
	if err := s.InsertSessionRecords(context.Background(), records); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)
	return uuids
}

// messageIDState is a row's message_id, nil for NULL, and the transaction that
// wrote its current version.
type messageIDState struct {
	id   *string
	xmin string
}

func messageIDStates(t *testing.T, s *PgStore, sid string) map[string]messageIDState {
	t.Helper()
	rows, err := s.pool.Query(context.Background(),
		`SELECT uuid, message_id, xmin::text FROM session_records WHERE session_id = $1`, sid)
	if err != nil {
		t.Fatalf("read the rows of %s: %v", sid, err)
	}
	defer rows.Close()
	states := map[string]messageIDState{}
	for rows.Next() {
		var uuid string
		var st messageIDState
		if err := rows.Scan(&uuid, &st.id, &st.xmin); err != nil {
			t.Fatalf("read a row of %s: %v", sid, err)
		}
		states[uuid] = st
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the rows of %s: %v", sid, err)
	}
	return states
}

// checkMessageIDPrefix reads the rows of session sid and checks that exactly the
// first n of order -- the order the walk is to take them in, newest first -- hold
// their message_id, each the value in want, and that none has been written again
// since the call that filled it: seen keeps the xmin each row had when it was
// first seen filled. It reports a handful of the rows that differ, not all.
func checkMessageIDPrefix(t *testing.T, s *PgStore, when, sid string, order []string, want map[string]string,
	n int, seen map[string]string) {
	t.Helper()
	got := messageIDStates(t, s, sid)
	var problems []string
	for j, uuid := range order {
		st, ok := got[uuid]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s is gone", uuid))
		case j >= n && st.id != nil:
			problems = append(problems, fmt.Sprintf("%s (newest-first row %d) = %s, want NULL", uuid, j+1, idString(st.id)))
		case j < n && (st.id == nil || *st.id != want[uuid]):
			problems = append(problems, fmt.Sprintf("%s (newest-first row %d) = %s, want %q", uuid, j+1, idString(st.id), want[uuid]))
		case j < n:
			if first, ok := seen[uuid]; !ok {
				seen[uuid] = st.xmin
			} else if first != st.xmin {
				problems = append(problems, fmt.Sprintf("%s was written again (xmin %s -> %s)", uuid, first, st.xmin))
			}
		}
	}
	if len(problems) > 0 {
		t.Fatalf("%s: %d of %d row(s) are not as expected -- the newest %d filled, the rest NULL -- e.g. %s",
			when, len(problems), len(order), n, strings.Join(problems[:min(len(problems), 3)], "; "))
	}
}

// A window with more rows than the cap takes several calls, each locking and
// filling the newest rows it has left, and the cursor stays on the window until a
// call finds fewer rows than the cap: only that call moves it, by one window, and
// only the last window's last call commits the marker. A window of n rows takes
// n / cap + 1 calls (integer division), so rows that are an exact multiple of the
// cap cost one more call that finds nothing.
//
// Window one holds two caps and 7 rows: 3 calls, of the cap, the cap and 7 rows.
// Window two, the last, holds exactly the cap: 2 calls, the cap and then none,
// and the second moves the cursor and finishes the walk. That is 5 calls, and
// the table below asserts each: the rows filled after it, where the cursor
// stands, the marker, and the walk's counts.
//
// Every seventh line has no id and is filled with the empty string. No row is
// written twice, the empty ones included, which is also why the walk ends.
func TestMessageIDBackfillFillsAWindowInBatchesOfAtMostTheRowCap(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	var logged bytes.Buffer
	prevLog := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(prevLog) })

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rowCap := messageIDBackfillBatchRows
	const sid = "mid-cap"
	want := map[string]string{}
	noID := func(i int) bool { return i%7 == 3 }
	one := messageIDRowsAt(t, s, sid, "w1", messageIDTimes(hi, 2*rowCap+7), noID, want)
	two := messageIDRowsAt(t, s, sid, "w2", messageIDTimes(hi.Add(-messageIDBackfillWindow*5/4), rowCap), noID, want)
	order := slices.Concat(one, two) // the order the walk takes them in

	steps := []struct {
		filled   int       // rows filled so far: the newest of order
		windows  int       // windows walked to their end so far
		next     time.Time // where the cursor stands
		finished bool
	}{
		{rowCap, 0, hi, false},
		{2 * rowCap, 0, hi, false},
		{len(one), 1, hi.Add(-messageIDBackfillWindow), false},
		{len(one) + rowCap, 1, hi.Add(-messageIDBackfillWindow), false},
		{len(order), 2, hi.Add(-2 * messageIDBackfillWindow), true},
	}
	var walk messageIDWalk
	seen := map[string]string{}
	for i, w := range steps {
		call := fmt.Sprintf("call %d of %d", i+1, len(steps))
		finished, err := s.sessionRecordMessageIDStep(ctx, &walk)
		if err != nil {
			t.Fatalf("%s: %v", call, err)
		}
		if finished != w.finished {
			t.Fatalf("%s: finished = %v, want %v", call, finished, w.finished)
		}
		if c, ok := readMessageIDCursor(t, s); !ok || !c.next.Equal(w.next) {
			t.Fatalf("%s: cursor's next window end = %s (present %v), want %s", call, c.next, ok, w.next)
		}
		if marked := messageIDBackfillMarked(t, s); marked != w.finished {
			t.Fatalf("%s: marker present = %v, want %v -- it commits with the last batch", call, marked, w.finished)
		}
		checkMessageIDPrefix(t, s, call, sid, order, want, w.filled, seen)
		if walk.batches != i+1 || walk.windows != w.windows || walk.filled != int64(w.filled) {
			t.Fatalf("%s: walk counts %d batch(es), %d window(s), %d row(s); want %d, %d and %d",
				call, walk.batches, walk.windows, walk.filled, i+1, w.windows, w.filled)
		}
	}

	// The line logged at the end says the same, and no batch it reports is over the cap.
	log.SetOutput(prevLog)
	summary := regexp.MustCompile(fmt.Sprintf(
		`walked %d window\(s\) in %d batch\(es\) and filled %d row\(s\), slowest batch \S+ for (\d+) row\(s\)`,
		steps[len(steps)-1].windows, len(steps), len(order))).FindStringSubmatch(logged.String())
	if summary == nil {
		t.Fatalf("the walk's summary line is not in the log:\n%s", logged.String())
	}
	if rows, _ := strconv.Atoi(summary[1]); rows > rowCap {
		t.Errorf("the summary reports a slowest batch of %d row(s), over the cap of %d", rows, rowCap)
	}
}

// A row another writer holds fails the batch that would take it, and only that
// one. The window holds the cap and 5 rows; a writer holds the oldest, which is in
// the second batch. The call commits the first batch -- so it reports progress --
// and fails on the second with 55P03, at once and not after the lock timeout; the
// rows of the first stay filled, those of the second are not written, and the
// cursor stays on the window. A call that starts at the second batch while the
// row is held commits nothing and reports none. Once the writer is done the retry
// takes the 5 rows that are left and nothing before them, and finishes the walk.
func TestMessageIDBackfillAHeldRowFailsOnlyItsBatch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	// A walk that never reaches its end fails on this deadline instead of holding
	// the package until its timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rowCap := messageIDBackfillBatchRows
	const sid = "mid-held-batch"
	want := map[string]string{}
	order := messageIDRowsAt(t, s, sid, "r", messageIDTimes(hi, rowCap+5), nil, want)
	held := order[len(order)-1]

	walked := func() (windows, batches int, filled int64) {
		s.messageIDWalk.mu.Lock()
		defer s.messageIDWalk.mu.Unlock()
		return s.messageIDWalk.windows, s.messageIDWalk.batches, s.messageIDWalk.filled
	}
	windows0, batches0, filled0 := walked()
	expectWalked := func(when string, windows, batches int, filled int64) {
		t.Helper()
		w, b, f := walked()
		if w-windows0 != windows || b-batches0 != batches || f-filled0 != filled {
			t.Fatalf("%s: the walk counted %d window(s), %d batch(es), %d row(s); want %d, %d and %d",
				when, w-windows0, b-batches0, f-filled0, windows, batches, filled)
		}
	}

	writer, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin writer: %v", err)
	}
	defer writer.Rollback(ctx) //nolint:errcheck // committed below
	if _, err := writer.Exec(ctx, `UPDATE session_records SET entrypoint = entrypoint WHERE uuid = $1`, held); err != nil {
		t.Fatalf("writer UPDATE: %v", err)
	}
	before := messageIDStates(t, s, sid)

	progressed, err := s.BackfillSessionRecordMessageIDs(ctx)
	if !progressed || !isLockTimeout(err) || !isContendedLockError(err) {
		t.Fatalf("with %s held: progressed %v, err %v; want true -- the first batch committed -- and 55P03 from the second",
			held, progressed, err)
	}
	seen := map[string]string{}
	checkMessageIDPrefix(t, s, "after the first call", sid, order, want, rowCap, seen)
	after := messageIDStates(t, s, sid)
	for _, uuid := range order[rowCap:] {
		if after[uuid].xmin != before[uuid].xmin {
			t.Errorf("%s was written by the failed batch (xmin %s -> %s)", uuid, before[uuid].xmin, after[uuid].xmin)
		}
	}
	if c, ok := readMessageIDCursor(t, s); !ok || !c.next.Equal(hi) {
		t.Fatalf("cursor's next window end after the failed batch = %s (present %v), want %s: still on the window", c.next, ok, hi)
	}
	if messageIDBackfillMarked(t, s) {
		t.Fatal("marker written by a walk that failed")
	}
	expectWalked("after the first call", 0, 1, int64(rowCap))

	started := time.Now()
	progressed, err = s.BackfillSessionRecordMessageIDs(ctx)
	elapsed := time.Since(started)
	if progressed || !isLockTimeout(err) {
		t.Fatalf("starting at the held batch: progressed %v, err %v; want false and 55P03", progressed, err)
	}
	if elapsed >= messageIDBackfillLockTimeout/2 {
		t.Errorf("the batch failed after %s, want well under the %s lock_timeout: the backfill waited on the row",
			elapsed, messageIDBackfillLockTimeout)
	}
	expectWalked("after the second call", 0, 1, int64(rowCap))

	if err := writer.Commit(ctx); err != nil {
		t.Fatalf("writer commit: %v", err)
	}
	if progressed, err := s.BackfillSessionRecordMessageIDs(ctx); err != nil || !progressed {
		t.Fatalf("retry: progressed %v, err %v; want the rest of the window filled and the walk done", progressed, err)
	}
	checkMessageIDPrefix(t, s, "after the retry", sid, order, want, len(order), seen)
	if c, ok := readMessageIDCursor(t, s); !ok || !c.next.Equal(hi.Add(-messageIDBackfillWindow)) {
		t.Errorf("cursor's next window end after the retry = %s (present %v), want %s: one window on", c.next, ok, hi.Add(-messageIDBackfillWindow))
	}
	if !messageIDBackfillMarked(t, s) {
		t.Error("marker missing after the retry finished the walk")
	}
	expectWalked("after the retry", 1, 2, int64(len(order)))
}

// The rows of a batch are the newest of the window, and rows with the same ts go
// by id, the higher first: ts DESC, id DESC is a total order, so a batch is the
// same rows whatever plan reads them. The window holds the cap and 3 rows, and
// the cap-th row shares its ts with the one after it, so the first batch ends
// between the two: it takes the one with the higher id, and the other opens the
// second.
func TestMessageIDBackfillBatchTakesTheNewestRowsAndBreaksATieByID(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	rowCap := messageIDBackfillBatchRows
	const sid = "mid-order"
	want := map[string]string{}
	ats := messageIDTimes(hi, rowCap+3)
	ats[rowCap] = ats[rowCap-1]
	inserted := messageIDRowsAt(t, s, sid, "r", ats, nil, want)

	idOf := func(uuid string) int64 {
		t.Helper()
		var id int64
		if err := s.pool.QueryRow(ctx, `SELECT id FROM session_records WHERE uuid = $1`, uuid).Scan(&id); err != nil {
			t.Fatalf("read the id of %s: %v", uuid, err)
		}
		return id
	}
	higher, lower := inserted[rowCap], inserted[rowCap-1] // the one inserted second has the higher id
	if idOf(higher) < idOf(lower) {
		t.Fatalf("fixture: %s was inserted after %s but has the lower id (%d against %d)", higher, lower, idOf(higher), idOf(lower))
	}
	order := slices.Concat(inserted[:rowCap-1], []string{higher, lower}, inserted[rowCap+1:])

	var walk messageIDWalk
	seen := map[string]string{}
	for i, filled := range []int{rowCap, len(order)} {
		finished, err := s.sessionRecordMessageIDStep(ctx, &walk)
		if err != nil || finished != (i == 1) {
			t.Fatalf("call %d: finished %v, err %v; want finished %v", i+1, finished, err, i == 1)
		}
		checkMessageIDPrefix(t, s, fmt.Sprintf("call %d", i+1), sid, order, want, filled, seen)
	}
}

// A window can lie across two chunks of the hypertable -- with 7-day chunks about
// one 6-hour window in 28 does -- and a batch then reads, orders and locks rows
// of both in one statement. The window here ends half a window past a chunk
// boundary and holds the cap and 7 rows, 600 of them in the newer chunk: the
// first batch takes those and goes on into the older chunk, newest first across
// the boundary, and the second takes the 7 the older chunk has left.
func TestMessageIDBackfillBatchReadsAWindowAcrossTwoChunks(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetMessageIDBackfill(t, s)
	ctx := context.Background()

	// 2026-09-10 00:00 UTC starts a chunk: session_records has the default 7-day
	// chunks, aligned to the Unix epoch. The fixture check below fails the test
	// should the rows not land in two chunks.
	boundary := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	hi := boundary.Add(messageIDBackfillWindow / 2)
	rowCap := messageIDBackfillBatchRows
	const sid = "mid-two-chunks"
	want := map[string]string{}
	newerAts := messageIDTimes(hi, 600)
	olderAts := messageIDTimes(boundary.Add(-time.Second), rowCap+7-len(newerAts))
	if !newerAts[len(newerAts)-1].After(boundary) || !olderAts[len(olderAts)-1].After(hi.Add(-messageIDBackfillWindow)) {
		t.Fatalf("fixture: the rows do not fit the window on both sides of the boundary (oldest newer %s, oldest older %s)",
			newerAts[len(newerAts)-1], olderAts[len(olderAts)-1])
	}
	newer := messageIDRowsAt(t, s, sid, "n", newerAts, nil, want)
	older := messageIDRowsAt(t, s, sid, "o", olderAts, nil, want)
	order := slices.Concat(newer, older)

	var chunks, inNewer int
	if err := s.pool.QueryRow(ctx, `SELECT count(DISTINCT tableoid), count(*) FILTER (WHERE ts >= $2)
		FROM session_records WHERE session_id = $1`, sid, boundary).Scan(&chunks, &inNewer); err != nil {
		t.Fatalf("read the chunks of the fixture: %v", err)
	}
	if chunks != 2 || inNewer != len(newer) {
		t.Fatalf("fixture: the rows are in %d chunk(s), %d of them from the boundary on; want 2 chunks and %d", chunks, inNewer, len(newer))
	}

	var walk messageIDWalk
	seen := map[string]string{}
	for i, filled := range []int{rowCap, len(order)} {
		finished, err := s.sessionRecordMessageIDStep(ctx, &walk)
		if err != nil || finished != (i == 1) {
			t.Fatalf("call %d: finished %v, err %v; want finished %v", i+1, finished, err, i == 1)
		}
		checkMessageIDPrefix(t, s, fmt.Sprintf("call %d", i+1), sid, order, want, filled, seen)
	}
	if walk.batches != 2 || walk.windows != 1 || walk.filled != int64(len(order)) {
		t.Errorf("walked %d window(s) in %d batch(es) filling %d row(s), want 1, 2 and %d",
			walk.windows, walk.batches, walk.filled, len(order))
	}
}
