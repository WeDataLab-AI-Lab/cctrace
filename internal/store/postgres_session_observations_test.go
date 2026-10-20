package store

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"
)

var obsBase = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

func ingestObservations(t *testing.T, s *PgStore, b SessionObservationBatch) int {
	t.Helper()
	res, err := s.IngestSessionObservations(context.Background(), b)
	if err != nil {
		t.Fatalf("IngestSessionObservations: %v", err)
	}
	return res.Stored
}

func countWhere(t *testing.T, s *PgStore, table, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func obsSnapshot(sessionID, profile string, at time.Time, v string) *SessionSnapshot {
	return &SessionSnapshot{SessionID: sessionID, UserID: "u-1", ProfileEmail: profile, Agent: "claude",
		ObservedAt: at, Snapshot: json.RawMessage(`{"v":"` + v + `"}`)}
}

func obsLiveState(sessionID, profile string, at time.Time, status string) *SessionLiveState {
	return &SessionLiveState{SessionID: sessionID, UserID: "u-1", ProfileEmail: profile, Agent: "claude",
		PID: 4242, ProcStart: "start-1", Status: status, HeartbeatAt: at}
}

// obsEvent is an event as a client sends one: session_end is the only kind the
// API takes from a client.
func obsEvent(sessionID, profile, eventID string, at time.Time, payload string) *SessionObservationEvent {
	return &SessionObservationEvent{SessionID: sessionID, UserID: "u-1", ProfileEmail: profile, Agent: "claude",
		EventID: eventID, Kind: "session_end", ObservedAt: at, Payload: json.RawMessage(payload)}
}

// A snapshot is the client's whole view at observed_at, so the newest one is the
// answer and an older one arriving later -- a retry of a request whose response
// was lost -- must not replace it.
func TestIngestSessionObservations_snapshotKeepsNewest(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	read := func() (string, time.Time) {
		t.Helper()
		var v string
		var at time.Time
		if err := s.pool.QueryRow(ctx, `SELECT snapshot->>'v', observed_at FROM session_snapshots
			WHERE session_id = 's1' AND profile_email = 'p@example.com'`).Scan(&v, &at); err != nil {
			t.Fatalf("read snapshot: %v", err)
		}
		return v, at
	}

	if n := ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		obsSnapshot("s1", "p@example.com", obsBase.Add(2*time.Minute), "newer")}}); n != 1 {
		t.Fatalf("first snapshot stored %d, want 1", n)
	}
	if n := ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		obsSnapshot("s1", "p@example.com", obsBase.Add(time.Minute), "older")}}); n != 0 {
		t.Errorf("older retry stored %d, want 0", n)
	}
	if v, at := read(); v != "newer" || !at.Equal(obsBase.Add(2*time.Minute)) {
		t.Errorf("after an older retry the row is %q at %s, want the newer snapshot", v, at)
	}
	if n := ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		obsSnapshot("s1", "p@example.com", obsBase.Add(3*time.Minute), "newest")}}); n != 1 {
		t.Errorf("newer snapshot stored %d, want 1", n)
	}
	if v, _ := read(); v != "newest" {
		t.Errorf("row is %q, want the newest snapshot", v)
	}

	// The same key twice in one batch, newer first: the second is compared against
	// the first rather than failing the statement or overwriting it.
	if n := ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		obsSnapshot("s1", "p@example.com", obsBase.Add(5*time.Minute), "five"),
		obsSnapshot("s1", "p@example.com", obsBase.Add(4*time.Minute), "four"),
	}}); n != 1 {
		t.Errorf("batch with a duplicate key stored %d, want 1", n)
	}
	if v, _ := read(); v != "five" {
		t.Errorf("row is %q, want the newer of the two in the batch", v)
	}

	// Another identity observing the same session is its own row.
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		obsSnapshot("s1", "other@example.com", obsBase, "other")}})
	if n := countWhere(t, s, "session_snapshots", "session_id = 's1'"); n != 2 {
		t.Errorf("session_snapshots holds %d rows for s1, want one per identity (2)", n)
	}
}

func TestIngestSessionObservations_liveStateKeepsNewestHeartbeat(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	read := func() (string, time.Time, *time.Time) {
		t.Helper()
		var status string
		var hb time.Time
		var changed *time.Time
		if err := s.pool.QueryRow(ctx, `SELECT status, heartbeat_at, status_changed_at FROM session_live_states
			WHERE session_id = 's1' AND proc_start = 'start-1'`).Scan(&status, &hb, &changed); err != nil {
			t.Fatalf("read live state: %v", err)
		}
		return status, hb, changed
	}

	busy := obsLiveState("s1", "p@example.com", obsBase.Add(2*time.Minute), "busy")
	changedAt := obsBase.Add(90 * time.Second)
	busy.StatusChangedAt = &changedAt
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{busy}}); n != 1 {
		t.Fatalf("first live state stored %d, want 1", n)
	}
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{
		obsLiveState("s1", "p@example.com", obsBase.Add(time.Minute), "idle")}}); n != 0 {
		t.Errorf("older heartbeat stored %d, want 0", n)
	}
	if status, hb, changed := read(); status != "busy" || !hb.Equal(obsBase.Add(2*time.Minute)) ||
		changed == nil || !changed.Equal(changedAt) {
		t.Errorf("after an older retry the row is %s at %s (changed %v), want the newer report", status, hb, changed)
	}
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{
		obsLiveState("s1", "p@example.com", obsBase.Add(3*time.Minute), "waiting")}}); n != 1 {
		t.Errorf("newer heartbeat stored %d, want 1", n)
	}
	if status, _, changed := read(); status != "waiting" || changed != nil {
		t.Errorf("row is %s (changed %v), want the newer report, status_changed_at included", status, changed)
	}

	// A second process for the same session -- resumed in another terminal -- is
	// its own row, not an overwrite of the first.
	second := obsLiveState("s1", "p@example.com", obsBase, "idle")
	second.ProcStart = "start-2"
	ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{second}})
	if n := countWhere(t, s, "session_live_states", "session_id = 's1'"); n != 2 {
		t.Errorf("session_live_states holds %d rows for s1, want one per process (2)", n)
	}
}

// An equal time wins: the comparison is >=, not >. Clients send RFC3339Nano and
// the columns keep microseconds, so an equal time is in practice the same report
// sent again, and the later arrival taking the row leaves it as it was.
func TestIngestSessionObservations_equalTimeLaterArrivalWins(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	ingestObservations(t, s, SessionObservationBatch{
		Snapshots:  []*SessionSnapshot{obsSnapshot("s1", "p@example.com", obsBase, "first")},
		LiveStates: []*SessionLiveState{obsLiveState("s1", "p@example.com", obsBase, "busy")},
	})
	if n := ingestObservations(t, s, SessionObservationBatch{
		Snapshots:  []*SessionSnapshot{obsSnapshot("s1", "p@example.com", obsBase, "second")},
		LiveStates: []*SessionLiveState{obsLiveState("s1", "p@example.com", obsBase, "idle")},
	}); n != 2 {
		t.Errorf("reports at the stored time stored %d, want 2", n)
	}
	if n := countWhere(t, s, "session_snapshots", "snapshot->>'v' = 'second'"); n != 1 {
		t.Error("snapshot: the later arrival at the same time should hold the row")
	}
	if n := countWhere(t, s, "session_live_states", "status = 'idle'"); n != 1 {
		t.Error("live state: the later arrival at the same time should hold the row")
	}
}

// Times are stored and compared as the client sent them, ahead of the server's
// clock included: the API skips what is more than five minutes ahead, and what it
// lets through is kept as sent. Cut to now(), every report from a fast clock
// would be dated by its arrival, and an older retry landing after a newer report
// would take the row.
func TestIngestSessionObservations_storesClientTimesAsSent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	// "Ahead" is ahead of the database's clock, which is what a cut to now() would
	// use. It need not agree with this process's: a container's can run seconds
	// apart from the host's.
	var dbNow time.Time
	if err := s.pool.QueryRow(context.Background(), `SELECT now()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}

	// A second ahead is stored as sent, in every time column. The columns keep
	// microseconds.
	ahead := dbNow.Add(time.Second).Truncate(time.Microsecond)
	busy := obsLiveState("s1", "p@example.com", ahead, "busy")
	busy.StatusChangedAt = &ahead
	ingestObservations(t, s, SessionObservationBatch{
		Snapshots:  []*SessionSnapshot{obsSnapshot("s1", "p@example.com", ahead, "ahead")},
		LiveStates: []*SessionLiveState{busy},
		Events:     []*SessionObservationEvent{obsEvent("s1", "p@example.com", "e1", ahead, `{}`)},
	})
	for _, c := range []struct{ table, column, where string }{
		{"session_snapshots", "observed_at", "session_id = 's1'"},
		{"session_live_states", "heartbeat_at", "session_id = 's1'"},
		{"session_live_states", "status_changed_at", "session_id = 's1'"},
		{"session_observation_events", "observed_at", "event_id = 'e1'"},
		// The history row the live state derived.
		{"session_observation_events", "observed_at", "kind = 'state_change'"},
	} {
		if n := countWhere(t, s, c.table, c.where+" AND "+c.column+" = $1", ahead); n != 1 {
			t.Errorf("%s.%s where %s: %d row(s) at the time sent, want 1", c.table, c.column, c.where, n)
		}
	}

	// A clock two minutes fast reports a wait; then the retry of an earlier report
	// from it -- idle, a minute older, still after the server's now() -- lands.
	// Compared as sent, the newer report keeps the row, and the retry, which
	// changed nothing, is no history.
	newer := dbNow.Add(2 * time.Minute)
	older := newer.Add(-time.Minute)
	waiting := obsLiveState("s2", "p@example.com", newer, "waiting")
	waiting.StatusChangedAt = &newer
	ingestObservations(t, s, SessionObservationBatch{
		Snapshots:  []*SessionSnapshot{obsSnapshot("s2", "p@example.com", newer, "newer")},
		LiveStates: []*SessionLiveState{waiting},
	})
	if n := ingestObservations(t, s, SessionObservationBatch{
		Snapshots:  []*SessionSnapshot{obsSnapshot("s2", "p@example.com", older, "older")},
		LiveStates: []*SessionLiveState{obsLiveState("s2", "p@example.com", older, "idle")},
	}); n != 0 {
		t.Errorf("the older retry stored %d, want 0", n)
	}
	if n := countWhere(t, s, "session_snapshots", "session_id = 's2' AND snapshot->>'v' = 'newer'"); n != 1 {
		t.Error("snapshot: the older retry replaced the newer report")
	}
	if n := countWhere(t, s, "session_live_states", "session_id = 's2' AND status = 'waiting'"); n != 1 {
		t.Error("live state: the older retry replaced the newer report")
	}
	if n := countWhere(t, s, "session_observation_events", "session_id = 's2'"); n != 1 {
		t.Errorf("history rows for s2 = %d, want only the wait's: the retry changed nothing", n)
	}
}

// jsonb refuses the NUL escape, and one value carrying it fails the whole batch.
// The store swaps it for the U+FFFD escape: escape text, not the character, which
// is what keeps a literal "\u0000" in the text valid -- on the wire `\\u0000`, it
// would otherwise end as a backslash before a raw character, an escape jsonb
// refuses all the same.
func TestIngestSessionObservations_replacesNULEscape(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	withSnapshot := func(sessionID, body string) *SessionSnapshot {
		sn := obsSnapshot(sessionID, "p@example.com", obsBase, "")
		sn.Snapshot = json.RawMessage(body)
		return sn
	}
	if n := ingestObservations(t, s, SessionObservationBatch{
		Snapshots: []*SessionSnapshot{
			withSnapshot("real", `{"t":"a\u0000b"}`),
			withSnapshot("literal", `{"t":"a\\u0000b"}`),
		},
		Events: []*SessionObservationEvent{
			obsEvent("s1", "p@example.com", "real", obsBase, `{"t":"a\u0000b"}`),
			obsEvent("s1", "p@example.com", "literal", obsBase, `{"t":"a\\u0000b"}`),
		},
	}); n != 4 {
		t.Fatalf("stored %d, want all 4", n)
	}

	replacement := "a\xef\xbf\xbdb"      // the NUL itself, now U+FFFD
	literal := "a" + `\` + "uFFFD" + "b" // the text that spelled it, now spelling U+FFFD
	for _, c := range []struct{ query, id, want string }{
		{`SELECT snapshot->>'t' FROM session_snapshots WHERE session_id = $1`, "real", replacement},
		{`SELECT snapshot->>'t' FROM session_snapshots WHERE session_id = $1`, "literal", literal},
		{`SELECT payload->>'t' FROM session_observation_events WHERE event_id = $1`, "real", replacement},
		{`SELECT payload->>'t' FROM session_observation_events WHERE event_id = $1`, "literal", literal},
	} {
		var got string
		if err := s.pool.QueryRow(ctx, c.query, c.id).Scan(&got); err != nil {
			t.Fatalf("%s %s: %v", c.query, c.id, err)
		}
		if got != c.want {
			t.Errorf("%s (%s) = %q, want %q", c.query, c.id, got, c.want)
		}
	}
}

// A live state that is new or changes status leaves a history row, written by the
// server in the same transaction; a heartbeat that changes nothing does not, and
// neither does an out-of-order report that the newer row refused.
func TestIngestSessionObservations_derivesStateHistory(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	type entry struct {
		id, kind, from, to, waiting string
		at                          time.Time
	}
	history := func(profile string) []entry {
		t.Helper()
		rows, err := s.pool.Query(ctx, `SELECT event_id, kind, COALESCE(payload->>'from', '<null>'),
				payload->>'to', payload->>'waiting_kind', observed_at
			FROM session_observation_events WHERE session_id = 's1' AND profile_email = $1
			ORDER BY observed_at`, profile)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []entry
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.id, &e.kind, &e.from, &e.to, &e.waiting, &e.at); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
		return out
	}

	// A process seen for the first time is a change from nothing, dated by its
	// heartbeat when the file gave no time of its own.
	ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{
		obsLiveState("s1", "p@example.com", obsBase, "busy")}})
	h := history("p@example.com")
	if len(h) != 1 || h[0].kind != "state_change" || h[0].from != "<null>" || h[0].to != "busy" || !h[0].at.Equal(obsBase) {
		t.Fatalf("after the first state: %+v, want one state_change from null to busy at %s", h, obsBase)
	}
	if !strings.HasPrefix(h[0].id, "state-") {
		t.Errorf("event_id %q, want the server's derived id", h[0].id)
	}

	// A newer heartbeat with the same status is not history.
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{
		obsLiveState("s1", "p@example.com", obsBase.Add(time.Minute), "busy")}}); n != 1 {
		t.Fatalf("unchanged heartbeat stored %d, want 1", n)
	}
	if h := history("p@example.com"); len(h) != 1 {
		t.Errorf("an unchanged heartbeat wrote history: %+v", h)
	}

	// A change, dated by the time the state file gives.
	changedAt := obsBase.Add(90 * time.Second)
	waiting := obsLiveState("s1", "p@example.com", obsBase.Add(2*time.Minute), "waiting")
	waiting.WaitingKind = "permission"
	waiting.StatusChangedAt = &changedAt
	ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{waiting}})
	h = history("p@example.com")
	if len(h) != 2 || h[1].kind != "state_change" || h[1].from != "busy" || h[1].to != "waiting" ||
		h[1].waiting != "permission" || !h[1].at.Equal(changedAt) {
		t.Fatalf("after a change: %+v, want a second entry busy -> waiting (permission) at %s", h, changedAt)
	}

	// An older report of another status loses to the stored row, so it changed
	// nothing and is no history.
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{
		obsLiveState("s1", "p@example.com", obsBase.Add(30*time.Second), "idle")}}); n != 0 {
		t.Fatalf("out-of-order report stored %d, want 0", n)
	}
	if h := history("p@example.com"); len(h) != 2 {
		t.Errorf("an out-of-order report wrote history: %+v", h)
	}

	// A process gone is its own kind.
	ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{
		obsLiveState("s1", "p@example.com", obsBase.Add(3*time.Minute), "gone")}})
	h = history("p@example.com")
	if len(h) != 3 || h[2].kind != "gone" || h[2].from != "waiting" || h[2].to != "gone" {
		t.Fatalf("after the process went: %+v, want a gone entry from waiting", h)
	}

	// The id is the change's own: the same change reported under another identity
	// derives the same one, so a resend can never record a change twice.
	ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{
		obsLiveState("s1", "q@example.com", obsBase, "busy")}})
	if other := history("q@example.com"); len(other) != 1 || other[0].id != history("p@example.com")[0].id {
		t.Errorf("the same first state derived %+v, want the id %q", other, history("p@example.com")[0].id)
	}
}

// historyOf lists the derived history of session s1 under p@example.com as
// "from>to/waiting_kind@time", oldest first; from is "-" for a first state.
func historyOf(t *testing.T, s *PgStore) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT COALESCE(payload->>'from', '-'), payload->>'to',
			payload->>'waiting_kind', observed_at
		FROM session_observation_events
		WHERE session_id = 's1' AND profile_email = 'p@example.com' AND kind <> 'session_end'
		ORDER BY observed_at, received_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var from, to, waiting string
		var at time.Time
		if err := rows.Scan(&from, &to, &waiting, &at); err != nil {
			t.Fatal(err)
		}
		out = append(out, from+">"+to+"/"+waiting+"@"+at.Sub(obsBase).String())
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Changes dated by one time are still separate rows when they left different
// states or entered different waits. Every report here carries the same
// heartbeat and no status time -- an equal time wins, so each takes the row.
// busy is entered twice at that time, once as the first state and once from
// idle, and a wait changes kind twice.
func TestIngestSessionObservations_changesAtOneTimeAreSeparateRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	for _, state := range [][2]string{{"busy", ""}, {"idle", ""}, {"busy", ""},
		{"waiting", "permission"}, {"waiting", "dialog"}, {"waiting", "input"}} {
		ls := obsLiveState("s1", "p@example.com", obsBase, state[0])
		ls.WaitingKind = state[1]
		ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{ls}})
	}
	want := []string{"->busy/@0s", "busy>idle/@0s", "idle>busy/@0s",
		"busy>waiting/permission@0s", "waiting>waiting/dialog@0s", "waiting>waiting/input@0s"}
	if got := historyOf(t, s); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("history =\n  %v\nwant\n  %v", got, want)
	}
}

// While a process waits, a change in what it waits for is a change of state:
// a permission prompt and then a dialog are two waits. It is dated by
// status_changed_at when the report moved it, and otherwise by the heartbeat --
// an unmoved status_changed_at is when the wait began, not when its kind
// changed. Outside a wait the kind is not history.
func TestIngestSessionObservations_waitingKindChangeIsHistory(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	report := func(status, kind string, changed, heartbeat time.Duration) {
		t.Helper()
		ls := obsLiveState("s1", "p@example.com", obsBase.Add(heartbeat), status)
		ls.WaitingKind = kind
		at := obsBase.Add(changed)
		ls.StatusChangedAt = &at
		if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{ls}}); n != 1 {
			t.Fatalf("report %s/%s at %s stored %d, want 1", status, kind, heartbeat, n)
		}
	}

	report("waiting", "permission", 0, 0)
	// The kind changed and status_changed_at did not: dated by the heartbeat.
	report("waiting", "dialog", 0, time.Minute)
	// The same wait reported again is not history.
	report("waiting", "dialog", 0, 2*time.Minute)
	// The kind changed and status_changed_at moved with it: dated by that.
	report("waiting", "permission", 150*time.Second, 3*time.Minute)
	// Back to a dialog: the same change as before, at another time, so its own row.
	report("waiting", "dialog", 150*time.Second, 4*time.Minute)
	report("idle", "", 5*time.Minute, 5*time.Minute)
	// A kind outside a wait is not a change of state.
	report("idle", "other", 5*time.Minute, 6*time.Minute)

	want := []string{
		"->waiting/permission@0s",
		"waiting>waiting/dialog@1m0s",
		"waiting>waiting/permission@2m30s",
		"waiting>waiting/dialog@4m0s",
		"waiting>idle/@5m0s",
	}
	if got := historyOf(t, s); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("history =\n  %v\nwant\n  %v", got, want)
	}
}

// The columns a read will need to apply an exclusion made after the row landed
// are stored on all three session tables, the derived history included.
func TestIngestSessionObservations_storesExclusionColumns(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	sn := obsSnapshot("s1", "p@example.com", obsBase, "x")
	ls := obsLiveState("s1", "p@example.com", obsBase, "busy")
	ev := obsEvent("s1", "p@example.com", "e1", obsBase, `{}`)
	sn.LoginEmail, ls.LoginEmail, ev.LoginEmail = "login@example.com", "login@example.com", "login@example.com"
	sn.BillingProvider, ls.BillingProvider, ev.BillingProvider = "openai", "openai", "openai"
	sn.AccountID, ls.AccountID, ev.AccountID = "acct-1", "acct-1", "acct-1"
	ingestObservations(t, s, SessionObservationBatch{
		Snapshots: []*SessionSnapshot{sn}, LiveStates: []*SessionLiveState{ls}, Events: []*SessionObservationEvent{ev}})

	for table, want := range map[string]int{
		"session_snapshots":   1,
		"session_live_states": 1,
		// The client's event and the history row the live state derived.
		"session_observation_events": 2,
	} {
		if n := countWhere(t, s, table,
			"login_email = 'login@example.com' AND billing_provider = 'openai' AND account_id = 'acct-1'"); n != want {
			t.Errorf("%s: %d row(s) carry the exclusion columns, want %d", table, n, want)
		}
	}
}

// A failed batch rolled back, so nothing it had counted is stored, and the result
// says so rather than reporting the rows written before the failure.
func TestIngestSessionObservations_errorReturnsEmptyResult(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	bad := obsSnapshot("s2", "p@example.com", obsBase, "")
	bad.Snapshot = json.RawMessage(`{"t":`) // jsonb refuses it, after the row before it was written
	res, err := s.IngestSessionObservations(context.Background(), SessionObservationBatch{
		Snapshots: []*SessionSnapshot{obsSnapshot("s1", "p@example.com", obsBase, "good"), bad}})
	if err == nil {
		t.Fatal("a batch holding invalid JSON was accepted")
	}
	if res != (SessionObservationResult{}) {
		t.Errorf("result = %+v, want empty: the transaction rolled back", res)
	}
	if n := countWhere(t, s, "session_snapshots", "true"); n != 0 {
		t.Errorf("%d snapshot(s) stored by a failed batch", n)
	}
}

// Retries reuse the event_id, so a resend is a no-op and the first write stands.
func TestIngestSessionObservations_eventsAreIdempotent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if n := ingestObservations(t, s, SessionObservationBatch{Events: []*SessionObservationEvent{
		obsEvent("s1", "p@example.com", "e1", obsBase, `{"to":"idle"}`)}}); n != 1 {
		t.Fatalf("first event stored %d, want 1", n)
	}
	if n := ingestObservations(t, s, SessionObservationBatch{Events: []*SessionObservationEvent{
		obsEvent("s1", "p@example.com", "e1", obsBase.Add(time.Minute), `{"to":"busy"}`)}}); n != 0 {
		t.Errorf("retried event stored %d, want 0", n)
	}
	var to string
	if err := s.pool.QueryRow(ctx, `SELECT payload->>'to' FROM session_observation_events
		WHERE session_id = 's1' AND event_id = 'e1'`).Scan(&to); err != nil {
		t.Fatal(err)
	}
	if to != "idle" {
		t.Errorf("payload = %q, want the first write's", to)
	}

	// No payload is stored as the empty object, not as NULL.
	ingestObservations(t, s, SessionObservationBatch{Events: []*SessionObservationEvent{
		{SessionID: "s1", UserID: "u-1", ProfileEmail: "p@example.com", EventID: "e2", Kind: "session_end", ObservedAt: obsBase}}})
	if n := countWhere(t, s, "session_observation_events", "event_id = 'e2' AND payload = '{}'::jsonb"); n != 1 {
		t.Errorf("event without payload: %d rows with {}, want 1", n)
	}
	if n := countWhere(t, s, "session_observation_events", "session_id = 's1'"); n != 2 {
		t.Errorf("session_observation_events holds %d rows, want 2", n)
	}
}

// heartbeat_at and last_stored_at only move forward; client_version follows the
// latest request that sent one.
func TestCollectorHeartbeat_onlyMovesForward(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	hb := func(at time.Time, version string) SessionObservationBatch {
		return SessionObservationBatch{Heartbeat: &CollectorHeartbeat{UserID: "u-1", ProfileEmail: "p@example.com",
			HeartbeatAt: at, ClientVersion: version}}
	}
	read := func() (*time.Time, *time.Time, string) {
		t.Helper()
		var heartbeat, stored *time.Time
		var version string
		if err := s.pool.QueryRow(ctx, `SELECT heartbeat_at, last_stored_at, client_version FROM collector_heartbeats
			WHERE user_id = 'u-1' AND profile_email = 'p@example.com'`).Scan(&heartbeat, &stored, &version); err != nil {
			t.Fatalf("read heartbeat: %v", err)
		}
		return heartbeat, stored, version
	}

	ingestObservations(t, s, hb(obsBase.Add(2*time.Minute), "v0.7.59"))
	ingestObservations(t, s, hb(obsBase.Add(time.Minute), "v0.7.60"))
	if heartbeat, _, version := read(); heartbeat == nil || !heartbeat.Equal(obsBase.Add(2*time.Minute)) || version != "v0.7.60" {
		t.Errorf("after an older heartbeat: heartbeat_at %v, version %q; want the newer time kept and the version updated",
			heartbeat, version)
	}
	ingestObservations(t, s, hb(obsBase.Add(3*time.Minute), ""))
	if heartbeat, _, version := read(); !heartbeat.Equal(obsBase.Add(3*time.Minute)) || version != "v0.7.60" {
		t.Errorf("after a heartbeat without a version: heartbeat_at %v, version %q; want it moved and the version kept",
			heartbeat, version)
	}

	if err := s.TouchCollectorStored(ctx, "u-1", "p@example.com", obsBase.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchCollectorStored(ctx, "u-1", "p@example.com", obsBase.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if heartbeat, stored, _ := read(); stored == nil || !stored.Equal(obsBase.Add(10*time.Minute)) ||
		!heartbeat.Equal(obsBase.Add(3*time.Minute)) {
		t.Errorf("last_stored_at %v, heartbeat_at %v; want the later store kept and the heartbeat untouched", stored, heartbeat)
	}

	// A sync can land before the collector's first heartbeat; the row starts there.
	if err := s.TouchCollectorStored(ctx, "u-2", "q@example.com", obsBase); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, s, "collector_heartbeats",
		"user_id = 'u-2' AND heartbeat_at IS NULL AND last_stored_at = $1", obsBase); n != 1 {
		t.Errorf("touch for a new identity: %d matching rows, want 1", n)
	}
}

// DeleteUserData and MergeUsers take the source lock exclusively before they find
// an identity's sessions, and every writer takes it shared. A writer that did not
// could land a row between their discovery and their commit: written back after a
// privacy delete, or left under the old address by a merge. Both observation
// writers -- the ingest and the sync's stored mark -- wait for it.
func TestSessionObservations_writersWaitForExclusiveSourceLock(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// What DeleteUserData and MergeUsers hold while they run.
	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx) //nolint:errcheck // released below; this covers a failed test
	if err := lockSessionOverviewSourceMutation(ctx, holder, true); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 2)
	go func() {
		_, err := s.IngestSessionObservations(ctx, SessionObservationBatch{
			Snapshots: []*SessionSnapshot{obsSnapshot("s1", "p@example.com", obsBase, "x")}})
		done <- err
	}()
	go func() { done <- s.TouchCollectorStored(ctx, "u-1", "p@example.com", obsBase) }()

	// Both are queued on that lock, not merely slow to start: the source lock is the
	// two-key advisory lock (hashtext('session_overview_source'), 0).
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND NOT granted AND objsubid = 2
			  AND classid = hashtext('session_overview_source')::oid AND objid = 0`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d writer(s) waiting on the source lock after 10s, want 2", waiting)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("a writer returned (%v) while the exclusive source lock was held", err)
	default:
	}

	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("writer after the lock was released: %v", err)
		}
	}
	if n := countWhere(t, s, "session_snapshots", "session_id = 's1'"); n != 1 {
		t.Errorf("snapshot rows = %d after the lock was released, want 1", n)
	}
	if n := countWhere(t, s, "collector_heartbeats", "last_stored_at IS NOT NULL"); n != 1 {
		t.Errorf("stored marks = %d after the lock was released, want 1", n)
	}
}

// The API refuses deleted sessions from its cache; the store is the check that
// holds through commit. And a session deleted after it was observed loses those
// rows to the sweep, like every other per-session table.
func TestSessionObservations_tombstoneRefusedAndSwept(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	observe := func(sessionID string, at time.Time) SessionObservationBatch {
		return SessionObservationBatch{
			Snapshots:  []*SessionSnapshot{obsSnapshot(sessionID, "p@example.com", at, "x")},
			LiveStates: []*SessionLiveState{obsLiveState(sessionID, "p@example.com", at, "busy")},
			Events:     []*SessionObservationEvent{obsEvent(sessionID, "p@example.com", "e-"+at.Format(time.RFC3339), at, `{}`)},
		}
	}
	ingestObservations(t, s, observe("doomed", obsBase))
	ingestObservations(t, s, observe("kept", obsBase))

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "privacy", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if n := ingestObservations(t, s, observe("doomed", obsBase.Add(time.Minute))); n != 0 {
		t.Errorf("observations of a tombstoned session stored %d rows, want 0", n)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	for table, kept := range map[string]int{
		"session_snapshots":   1,
		"session_live_states": 1,
		// The client's event and the history row the live state derived.
		"session_observation_events": 2,
	} {
		if n := countWhere(t, s, table, "session_id = 'doomed'"); n != 0 {
			t.Errorf("%s still holds %d row(s) for the deleted session", table, n)
		}
		if n := countWhere(t, s, table, "session_id = 'kept'"); n != kept {
			t.Errorf("%s lost an unrelated session's row (kept = %d, want %d)", table, n, kept)
		}
	}
}

// observeInProject ingests one of each session-scoped observation under a
// project, and nothing else: no session_records, as for a session the collector
// saw before its first record synced.
func observeInProject(t *testing.T, s *PgStore, sessionID, projectHash string) {
	t.Helper()
	sn := obsSnapshot(sessionID, "p@example.com", obsBase, "x")
	ls := obsLiveState(sessionID, "p@example.com", obsBase, "busy")
	ev := obsEvent(sessionID, "p@example.com", "e1", obsBase, `{}`)
	sn.ProjectHash, ls.ProjectHash, ev.ProjectHash = projectHash, projectHash, projectHash
	ingestObservations(t, s, SessionObservationBatch{
		Snapshots: []*SessionSnapshot{sn}, LiveStates: []*SessionLiveState{ls}, Events: []*SessionObservationEvent{ev}})
}

func observationRowsFor(t *testing.T, s *PgStore, sessionID string) int {
	t.Helper()
	n := 0
	for _, table := range []string{"session_snapshots", "session_live_states", "session_observation_events"} {
		n += countWhere(t, s, table, "session_id = $1", sessionID)
	}
	return n
}

// A session known only through observations belongs to its project as much as a
// synced one: counting, deleting and blocking the project reach it, and the sweep
// then takes its rows.
func TestDeleteProject_reachesObservationOnlySessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	observeInProject(t, s, "obs-a", "-ph-a")
	observeInProject(t, s, "obs-other", "-ph-other")

	if n, err := s.ProjectSessionCount(ctx, "-ph-a", ""); err != nil || n != 1 {
		t.Fatalf("ProjectSessionCount = (%d, %v), want 1", n, err)
	}
	res, err := s.DeleteProject(ctx, []string{"-ph-a"}, "admin@example.com", "privacy", true)
	if err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if res.MarkedSessions != 1 || !res.ProjectBlocked {
		t.Fatalf("DeleteProject = %+v, want the observation-only session marked and the project blocked", res)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	if n := observationRowsFor(t, s, "obs-a"); n != 0 {
		t.Errorf("%d observation row(s) of the deleted project's session survived the sweep", n)
	}
	if n := observationRowsFor(t, s, "obs-other"); n == 0 {
		t.Error("another project's session lost its rows")
	}
}

// A block lands while an observation is in flight past ingest's refusal -- the
// store judges tombstones, not blocks. The periodic purge is what catches that,
// and it has to find a session that has observations and nothing else.
func TestPurgeBlockedProjects_sweepsObservationOnlySessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.BlockProject(ctx, "-ph-b", "", "admin@example.com", "privacy"); err != nil {
		t.Fatal(err)
	}
	observeInProject(t, s, "obs-raced", "-ph-b")
	observeInProject(t, s, "obs-other", "-ph-other")

	if n, err := s.PurgeBlockedProjects(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeBlockedProjects = (%d, %v), want the observation-only session tombstoned", n, err)
	}
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}
	if n := observationRowsFor(t, s, "obs-raced"); n != 0 {
		t.Errorf("%d observation row(s) of the blocked project's session survived", n)
	}
	if n := observationRowsFor(t, s, "obs-other"); n == 0 {
		t.Error("another project's session lost its rows")
	}
	if n, err := s.PurgeBlockedProjects(ctx); err != nil || n != 0 {
		t.Errorf("second purge = (%d, %v), want nothing left to do", n, err)
	}
}

// Deleting one session with "block the project" and "and its siblings" needs the
// session's project. One never synced has it only on its observation rows.
func TestDeleteSession_resolvesProjectFromObservations(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	observeInProject(t, s, "obs-c", "-ph-c")
	observeInProject(t, s, "obs-c-sibling", "-ph-c")

	res, err := s.DeleteSession(ctx, "obs-c", "admin@example.com", "privacy", true, true)
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if res.ProjectHash != "-ph-c" || !res.ProjectBlocked || res.PurgedSessions != 1 || res.MarkedSessions != 2 {
		t.Errorf("DeleteSession = %+v, want project -ph-c resolved, blocked, and its sibling purged", res)
	}
}

// The history has a window of its own and is pruned by it whatever else is set.
// The latest-value tables are not history, and without a session retention
// nothing prunes them.
func TestPruneSessionObservations_historyByItsOwnWindow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	forceRetention(t, s, "session_records", nil)
	ctx := context.Background()
	now := time.Now()
	old, recent := now.Add(-40*24*time.Hour), now.Add(-10*24*time.Hour)

	ingestObservations(t, s, SessionObservationBatch{
		Snapshots:  []*SessionSnapshot{obsSnapshot("s1", "p@example.com", old, "x")},
		LiveStates: []*SessionLiveState{obsLiveState("s1", "p@example.com", old, "idle")},
		Events: []*SessionObservationEvent{
			obsEvent("s1", "p@example.com", "old", old, `{}`),
			obsEvent("s1", "p@example.com", "recent", recent, `{}`),
		},
	})

	n, err := s.PruneSessionObservations(ctx, now.Add(-SessionObservationEventRetention))
	if err != nil {
		t.Fatalf("PruneSessionObservations: %v", err)
	}
	// The client's old event and the history row its old live state derived.
	if n != 2 {
		t.Errorf("pruned %d, want 2", n)
	}
	if got := countWhere(t, s, "session_observation_events", "event_id = 'recent'"); got != 1 {
		t.Errorf("the event inside retention was pruned")
	}
	if got := countWhere(t, s, "session_observation_events", "observed_at < $1", now.Add(-SessionObservationEventRetention)); got != 0 {
		t.Errorf("%d event(s) past retention survived", got)
	}
	if got := countWhere(t, s, "session_snapshots", "session_id = 's1'") + countWhere(t, s, "session_live_states", "session_id = 's1'"); got != 2 {
		t.Errorf("snapshot and live state rows = %d, want both kept: no session retention is set", got)
	}
}

// Snapshots, live states and their history hold titles, names and what each
// session was doing when, which is what the conversation says, so while
// session_records has a retention they last no longer than it. History keeps the
// shorter of that and its own window. The window is read from the
// session_records policy itself, so lifting the policy stops the prune too.
func TestPruneSessionObservations_followSessionRetention(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	// The policy is global state in the shared container; restore the baseline.
	t.Cleanup(func() { forceRetention(t, s, "session_records", nil) })
	forceRetention(t, s, "session_records", ptrInt(7))
	now := time.Now()
	stale, fresh := now.Add(-10*24*time.Hour), now.Add(-24*time.Hour)

	ingestObservations(t, s, SessionObservationBatch{
		Snapshots: []*SessionSnapshot{
			obsSnapshot("stale", "p@example.com", stale, "x"), obsSnapshot("fresh", "p@example.com", fresh, "x")},
		LiveStates: []*SessionLiveState{
			obsLiveState("stale", "p@example.com", stale, "idle"), obsLiveState("fresh", "p@example.com", fresh, "idle")},
		Events: []*SessionObservationEvent{
			obsEvent("stale", "p@example.com", "end", stale, `{}`), obsEvent("fresh", "p@example.com", "end", fresh, `{}`)},
	})
	n, err := s.PruneSessionObservations(ctx, now.Add(-SessionObservationEventRetention))
	if err != nil {
		t.Fatalf("PruneSessionObservations: %v", err)
	}
	// Past the 7-day window: the snapshot, the live state, the history row it
	// derived and the client's event.
	if n != 4 {
		t.Errorf("pruned %d, want 4", n)
	}
	for table, kept := range map[string]int{
		"session_snapshots":   1,
		"session_live_states": 1,
		// The derived history row and the client's event.
		"session_observation_events": 2,
	} {
		if got := countWhere(t, s, table, "session_id = 'stale'"); got != 0 {
			t.Errorf("%s kept %d row(s) past the 7-day session retention", table, got)
		}
		if got := countWhere(t, s, table, "session_id = 'fresh'"); got != kept {
			t.Errorf("%s holds %d row(s) inside the session retention, want %d", table, got, kept)
		}
	}

	// A session retention longer than history's own window leaves history to its
	// own: an event of 40 days goes, one of 20 stays, and so does a snapshot of 40.
	forceRetention(t, s, "session_records", ptrInt(60))
	truncateTables(t, s)
	ingestObservations(t, s, SessionObservationBatch{
		Snapshots: []*SessionSnapshot{obsSnapshot("long", "p@example.com", now.Add(-40*24*time.Hour), "x")},
		Events: []*SessionObservationEvent{
			obsEvent("long", "p@example.com", "40d", now.Add(-40*24*time.Hour), `{}`),
			obsEvent("long", "p@example.com", "20d", now.Add(-20*24*time.Hour), `{}`)},
	})
	if n, err := s.PruneSessionObservations(ctx, now.Add(-SessionObservationEventRetention)); err != nil || n != 1 {
		t.Errorf("prune under a 60-day session retention = (%d, %v), want the 40-day event alone", n, err)
	}
	if got := countWhere(t, s, "session_observation_events", "event_id = '20d'"); got != 1 {
		t.Error("the 20-day event was pruned, inside both windows")
	}
	if got := countWhere(t, s, "session_snapshots", "session_id = 'long'"); got != 1 {
		t.Error("the 40-day snapshot was pruned, inside the 60-day session retention")
	}

	forceRetention(t, s, "session_records", nil)
	ingestObservations(t, s, SessionObservationBatch{Snapshots: []*SessionSnapshot{
		obsSnapshot("stale-again", "p@example.com", stale, "x")}})
	if n, err := s.PruneSessionObservations(ctx, now.Add(-SessionObservationEventRetention)); err != nil || n != 0 {
		t.Errorf("prune with the retention lifted = (%d, %v), want nothing", n, err)
	}
}

// A project's sessions are looked up by project_hash in every session-scoped
// observation table -- a project delete, its count, and the blocked-project
// purge on every maintenance tick -- so each has an index on it rather than a
// scan of the whole table.
func TestSessionObservationTables_indexProjectHash(t *testing.T) {
	s := acquireTestStore(t)
	for _, table := range []string{"session_snapshots", "session_live_states", "session_observation_events"} {
		var n int
		if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_indexes
			WHERE schemaname = current_schema() AND tablename = $1 AND indexdef LIKE '%(project_hash)'`,
			table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s has %d index(es) on project_hash, want 1", table, n)
		}
	}
}

func seedObservationsFor(t *testing.T, s *PgStore, userID, profile, sessionID string, at time.Time) {
	t.Helper()
	sn := obsSnapshot(sessionID, profile, at, profile)
	ls := obsLiveState(sessionID, profile, at, "busy")
	ev := obsEvent(sessionID, profile, "e1", at, `{}`)
	sn.UserID, ls.UserID, ev.UserID = userID, userID, userID
	ingestObservations(t, s, SessionObservationBatch{
		Snapshots:  []*SessionSnapshot{sn},
		LiveStates: []*SessionLiveState{ls},
		Events:     []*SessionObservationEvent{ev},
		Heartbeat:  &CollectorHeartbeat{UserID: userID, ProfileEmail: profile, HeartbeatAt: at},
	})
}

var observationTables = []string{"session_snapshots", "session_live_states", "session_observation_events", "collector_heartbeats"}

// observationCounts counts the rows of each observation table that match where.
func observationCounts(t *testing.T, s *PgStore, where string) map[string]int {
	t.Helper()
	out := make(map[string]int, len(observationTables))
	for _, table := range observationTables {
		out[table] = countWhere(t, s, table, where)
	}
	return out
}

func TestDeleteUserData_removesSessionObservations(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	// Sessions known only through observations -- no session_records, no OTEL.
	seedObservationsFor(t, s, "u-gone", "gone@example.com", "obs-only-gone", obsBase)
	seedObservationsFor(t, s, "u-kept", "kept@example.com", "obs-only-kept", obsBase)
	// A stale rollup row for each. DeleteUserData re-derives the rollups of the
	// sessions it found, and with no source rows that removes the stale row -- so
	// whether it is gone says whether the session was found.
	for _, id := range []string{"obs-only-gone", "obs-only-kept"} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups (scope_type, scope_value, session_id, start_time, end_time)
			VALUES ('all', '', $1, $2, $2)`, id, obsBase); err != nil {
			t.Fatal(err)
		}
	}
	kept := observationCounts(t, s, "profile_email = 'kept@example.com'")
	for table, n := range kept {
		if n == 0 {
			t.Fatalf("%s holds no row of the other profile, so its survival below proves nothing", table)
		}
	}

	if err := s.DeleteUserData(ctx, "gone@example.com", ""); err != nil {
		t.Fatalf("DeleteUserData: %v", err)
	}
	for table, n := range observationCounts(t, s, "profile_email = 'gone@example.com'") {
		if n != 0 {
			t.Errorf("%s still holds %d row(s) of the deleted profile", table, n)
		}
	}
	if got := observationCounts(t, s, "profile_email = 'kept@example.com'"); !maps.Equal(got, kept) {
		t.Errorf("another profile's rows changed: %v, want %v", got, kept)
	}
	if n := rollupRowsForSession(t, s, "obs-only-gone"); n != 0 {
		t.Errorf("a session with only observations was not found by DeleteUserData (stale rollup rows = %d)", n)
	}
	if n := rollupRowsForSession(t, s, "obs-only-kept"); n != 1 {
		t.Errorf("another profile's session was touched (rollup rows = %d), so the probe above proves nothing", n)
	}

	// By user_id, the function's other key.
	if err := s.DeleteUserData(ctx, "", "u-kept"); err != nil {
		t.Fatalf("DeleteUserData by user_id: %v", err)
	}
	for table, n := range observationCounts(t, s, "user_id = 'u-kept'") {
		if n != 0 {
			t.Errorf("%s still holds %d row(s) of the deleted user_id", table, n)
		}
	}
}

// MergeUsers re-derives the rollups of the sessions it moves, so it has to find
// them as DeleteUserData does: a session known only through observations is one.
func TestMergeUsers_findsObservationOnlySessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedObservationsFor(t, s, "u-1", "old@example.com", "obs-only", obsBase)
	// The same probe as DeleteUserData's test: a stale rollup row goes only if the
	// merge found the session and refreshed it.
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups (scope_type, scope_value, session_id, start_time, end_time)
		VALUES ('all', '', 'obs-only', $1, $1)`, obsBase); err != nil {
		t.Fatal(err)
	}
	if err := s.MergeUsers(ctx, "old@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("MergeUsers: %v", err)
	}
	if n := rollupRowsForSession(t, s, "obs-only"); n != 0 {
		t.Errorf("a session with only observations was not found by MergeUsers (stale rollup rows = %d)", n)
	}
}

// MergeUsers moved profile_email with an UPDATE, which the observation tables'
// primary keys refuse wherever the target already holds the same row. The merge
// has to succeed in both cases, keep the newer observation, and leave nothing
// under the old address.
func TestMergeUsers_mergesSessionObservations(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	none := map[string]int{"session_snapshots": 0, "session_live_states": 0, "session_observation_events": 0, "collector_heartbeats": 0}

	t.Run("target holds nothing", func(t *testing.T) {
		truncateTables(t, s)
		seedObservationsFor(t, s, "u-1", "old@example.com", "s1", obsBase)
		moved := observationCounts(t, s, "profile_email = 'old@example.com'")
		if err := s.MergeUsers(ctx, "old@example.com", "", "new@example.com"); err != nil {
			t.Fatalf("MergeUsers: %v", err)
		}
		if got := observationCounts(t, s, "profile_email = 'old@example.com'"); !maps.Equal(got, none) {
			t.Errorf("rows left under the old address: %v", got)
		}
		if got := observationCounts(t, s, "profile_email = 'new@example.com'"); !maps.Equal(got, moved) {
			t.Errorf("rows under the new address: %v, want what the old one held, %v", got, moved)
		}
		if n := countWhere(t, s, "session_snapshots", "profile_email = 'new@example.com' AND snapshot->>'v' = 'old@example.com'"); n != 1 {
			t.Error("the moved snapshot lost its content")
		}
	})

	t.Run("target holds the same rows", func(t *testing.T) {
		truncateTables(t, s)
		// The old address saw the snapshot last; the new one saw the process last.
		ingestObservations(t, s, SessionObservationBatch{
			Snapshots:  []*SessionSnapshot{obsSnapshot("s1", "old@example.com", obsBase.Add(2*time.Minute), "old")},
			LiveStates: []*SessionLiveState{obsLiveState("s1", "old@example.com", obsBase, "busy")},
			Events:     []*SessionObservationEvent{obsEvent("s1", "old@example.com", "e1", obsBase, `{"from":"old"}`)},
			Heartbeat:  &CollectorHeartbeat{UserID: "u-1", ProfileEmail: "old@example.com", HeartbeatAt: obsBase, ClientVersion: "v-old"},
		})
		ingestObservations(t, s, SessionObservationBatch{
			Snapshots:  []*SessionSnapshot{obsSnapshot("s1", "new@example.com", obsBase, "new")},
			LiveStates: []*SessionLiveState{obsLiveState("s1", "new@example.com", obsBase.Add(2*time.Minute), "idle")},
			Events:     []*SessionObservationEvent{obsEvent("s1", "new@example.com", "e1", obsBase, `{"from":"new"}`)},
			Heartbeat:  &CollectorHeartbeat{UserID: "u-1", ProfileEmail: "new@example.com", HeartbeatAt: obsBase.Add(time.Minute), ClientVersion: "v-new"},
		})
		if err := s.TouchCollectorStored(ctx, "u-1", "old@example.com", obsBase.Add(5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := s.TouchCollectorStored(ctx, "u-1", "new@example.com", obsBase); err != nil {
			t.Fatal(err)
		}

		if err := s.MergeUsers(ctx, "old@example.com", "", "new@example.com"); err != nil {
			t.Fatalf("MergeUsers: %v", err)
		}
		if got := observationCounts(t, s, "profile_email = 'old@example.com'"); !maps.Equal(got, none) {
			t.Errorf("rows left under the old address: %v", got)
		}
		want := map[string]int{"session_snapshots": 1, "session_live_states": 1, "collector_heartbeats": 1,
			// e1 once, and each address's history of its own first state, whose
			// ids differ because the states do.
			"session_observation_events": 3}
		if got := observationCounts(t, s, "profile_email = 'new@example.com'"); !maps.Equal(got, want) {
			t.Errorf("rows under the new address: %v, want %v", got, want)
		}
		if n := countWhere(t, s, "session_snapshots", "snapshot->>'v' = 'old' AND observed_at = $1", obsBase.Add(2*time.Minute)); n != 1 {
			t.Error("snapshot: the old address's newer one should have replaced the target's")
		}
		if n := countWhere(t, s, "session_live_states", "status = 'idle' AND heartbeat_at = $1", obsBase.Add(2*time.Minute)); n != 1 {
			t.Error("live state: the target's newer one should have been kept")
		}
		var heartbeat, stored time.Time
		var version string
		if err := s.pool.QueryRow(ctx, `SELECT heartbeat_at, last_stored_at, client_version FROM collector_heartbeats
			WHERE profile_email = 'new@example.com'`).Scan(&heartbeat, &stored, &version); err != nil {
			t.Fatal(err)
		}
		if !heartbeat.Equal(obsBase.Add(time.Minute)) || !stored.Equal(obsBase.Add(5*time.Minute)) || version != "v-new" {
			t.Errorf("heartbeat %s, stored %s, version %q; want the later of each and the newer heartbeat's version",
				heartbeat, stored, version)
		}
	})

	// The user_id form only claims rows that have no profile yet, as the UPDATE
	// it sits beside does.
	t.Run("by user_id", func(t *testing.T) {
		truncateTables(t, s)
		seedObservationsFor(t, s, "u-orphan", "", "s1", obsBase)
		seedObservationsFor(t, s, "u-orphan", "elsewhere@example.com", "s2", obsBase)
		orphaned := observationCounts(t, s, "user_id = 'u-orphan' AND profile_email = ''")
		elsewhere := observationCounts(t, s, "profile_email = 'elsewhere@example.com'")
		if err := s.MergeUsers(ctx, "", "u-orphan", "new@example.com"); err != nil {
			t.Fatalf("MergeUsers: %v", err)
		}
		if got := observationCounts(t, s, "user_id = 'u-orphan' AND profile_email = ''"); !maps.Equal(got, none) {
			t.Errorf("profile-less rows left: %v", got)
		}
		if got := observationCounts(t, s, "user_id = 'u-orphan' AND profile_email = 'new@example.com'"); !maps.Equal(got, orphaned) {
			t.Errorf("rows under the new address: %v, want the profile-less ones, %v", got, orphaned)
		}
		if got := observationCounts(t, s, "profile_email = 'elsewhere@example.com'"); !maps.Equal(got, elsewhere) {
			t.Errorf("rows that already had a profile moved: %v, want %v", got, elsewhere)
		}
	})

	// Merging an address into itself upserts every row onto itself; the delete
	// that follows must not then take them.
	t.Run("into the same address", func(t *testing.T) {
		truncateTables(t, s)
		seedObservationsFor(t, s, "u-1", "same@example.com", "s1", obsBase)
		before := observationCounts(t, s, "profile_email = 'same@example.com'")
		if err := s.MergeUsers(ctx, "same@example.com", "", "same@example.com"); err != nil {
			t.Fatalf("MergeUsers: %v", err)
		}
		if got := observationCounts(t, s, "profile_email = 'same@example.com'"); !maps.Equal(got, before) {
			t.Errorf("rows after merging an address into itself: %v, want %v", got, before)
		}
	})
}
