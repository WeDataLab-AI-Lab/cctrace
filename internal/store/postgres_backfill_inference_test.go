package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// inferSeed builds one user's OTEL timeline and the unattributed records sitting
// on it. Every test here is a statement about which interval a record lands in,
// so the seeding is the only thing that varies.
type inferSeed struct {
	user   string
	events []inferEvent
	recs   []inferRec
}

type inferEvent struct {
	at    time.Time
	email string
}

type inferRec struct {
	uuid  string
	at    time.Time
	email string // pre-existing attribution; empty means unattributed
	// agent is the record's agent column. Empty means "let the writer decide",
	// which is 'claude' -- InsertSessionRecords normalizes it. A row that carries
	// the literal empty string in the database cannot be seeded through the writer
	// at all; forceEmptyAgent below is how a test reaches that state.
	agent string
}

func seedInference(t *testing.T, s *PgStore, seeds ...inferSeed) {
	t.Helper()
	ctx := context.Background()
	var events []*OtelEvent
	var recs []*SessionRecord
	for _, sd := range seeds {
		for _, e := range sd.events {
			events = append(events, &OtelEvent{
				Ts: e.at, EventName: "api_request",
				// The OTEL lands on its own session, never on the records' session:
				// the rows this pass repairs are exactly the ones whose session emitted
				// no telemetry, which is why the session-scoped backfill cannot see them.
				SessionID: sd.user + "-otel", UserID: sd.user, LoginEmail: e.email,
			})
		}
		for _, r := range sd.recs {
			recs = append(recs, &SessionRecord{
				Ts: r.at, SessionID: sd.user + "-quiet", RecordType: "user",
				UUID: r.uuid, UserID: sd.user, LoginEmail: r.email,
				Agent: r.agent,
				Raw:   json.RawMessage(`{}`),
			})
		}
	}
	if len(events) > 0 {
		if err := s.InsertEvents(ctx, events); err != nil {
			t.Fatalf("InsertEvents: %v", err)
		}
	}
	if len(recs) > 0 {
		if err := s.InsertSessionRecords(ctx, recs); err != nil {
			t.Fatalf("InsertSessionRecords: %v", err)
		}
	}
}

func excludeAccount(t *testing.T, s *PgStore, email string) {
	t.Helper()
	if _, err := s.ExcludeAccount(context.Background(), email, "test", "test"); err != nil {
		t.Fatalf("ExcludeAccount %s: %v", email, err)
	}
}

// The production case (#346, ocean_sense): two sessions that emitted no OTEL at
// all, so the session-scoped backfill can never reach them, wrapped on the user's
// own timeline by two observations of the same excluded account. Their 48 rows
// stayed visible in every session_records view because login_email was blank and a
// blank matches no exclusion.
//
// Mutation: key the timeline on session_id instead of user_id and q1 stays blank,
// because its session has no otel_events row to partition on -- which is the bug.
func TestInferFillsOtelLessSessionFromUserTimeline(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(43).Add(9*time.Hour + 46*time.Minute + 23*time.Second)

	seedInference(t, s, inferSeed{
		user: "u-ocean",
		events: []inferEvent{
			{base, "bot@example.com"},
			{base.Add(15 * time.Minute), "bot@example.com"},
		},
		recs: []inferRec{{uuid: "q1", at: base.Add(5 * time.Minute)}},
	})

	n, err := s.InferSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}
	if n != 1 {
		t.Fatalf("updated %d rows, want 1", n)
	}
	if email, src := loginEmailSourceOf(t, s, "q1"); email != "bot@example.com" || src != "inferred" {
		t.Fatalf("q1 = (%q, %q), want (bot@example.com, inferred)", email, src)
	}
}

// A record between two observations of different accounts is one of the two, and
// nothing in the data says which. Guessing the earlier one and being wrong leaves
// an excluded account on screen -- the failure this whole change exists to remove.
// Guessing the excluded one and being wrong hides a row that could have been
// shown, which is recoverable by unexcluding the account. The costs are not
// symmetric, so the tie goes to the excluded side.
//
// Mutation: drop the tiebreak (always take the interval's own account) and the
// "gap-to-excluded" case fills with plain@ instead of bot@, putting the excluded
// account back on screen.
func TestInferPrefersExcludedAccountAcrossBoundary(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(42).Add(10 * time.Hour)
	mid := base.Add(time.Hour)
	end := base.Add(2 * time.Hour)

	// Four users, one per boundary shape. Each sees plain -> other at `mid` with an
	// unattributed record in between.
	pair := func(user, first, second string) inferSeed {
		return inferSeed{
			user: user,
			events: []inferEvent{
				{base, first},
				{mid, second},
				{end, second},
			},
			recs: []inferRec{{uuid: user + "-r", at: base.Add(30 * time.Minute)}},
		}
	}
	seedInference(t, s,
		pair("u-next", "plain@example.com", "bot@example.com"),    // only the later account excluded
		pair("u-prev", "bot@example.com", "plain@example.com"),    // only the earlier account excluded
		pair("u-both", "bot@example.com", "bot2@example.com"),     // both excluded
		pair("u-none", "plain@example.com", "plain2@example.com"), // neither excluded
	)
	excludeAccount(t, s, "bot@example.com")
	excludeAccount(t, s, "bot2@example.com")

	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}

	want := map[string]string{
		// The record is bracketed by plain@ then bot@; bot@ is excluded, so it wins.
		"u-next-r": "bot@example.com",
		// Already the earlier account, so the ordinary rule and the conservative one
		// agree. Listed so a tiebreak that fires unconditionally shows up here.
		"u-prev-r": "bot@example.com",
		// Nothing to choose between: excluded either way, so keep the ordinary rule.
		"u-both-r": "bot@example.com",
		// Nothing to protect: keep the ordinary rule.
		"u-none-r": "plain@example.com",
	}
	for uuid, w := range want {
		if email, src := loginEmailSourceOf(t, s, uuid); email != w || src != "inferred" {
			t.Errorf("%s = (%q, %q), want (%q, inferred)", uuid, email, src, w)
		}
	}
}

// Before a user's first observation there is no evidence at all, only a hope that
// they never switched. codexauth/ClaudeAccountAt already answers this with a blank
// rather than a guess, and here the stakes are larger: OTEL collection started in
// 2026-05, so reaching backwards would label every record older than that.
//
// After the last observation the answer is the last one seen -- the same "latest
// observation at or before ts" contract ClaudeAccountAt implements. There is real
// evidence for it (an account was in use and nothing has been seen replacing it),
// which is exactly what the pre-first-observation case lacks.
//
// Mutation: let the first interval reach backwards (the rn = 1 relaxation the
// session-scoped backfill is allowed, because a session bounds it) and "before"
// fills instead of staying blank.
func TestInferLeavesRecordsBeforeFirstObservationBlank(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(41).Add(10 * time.Hour)

	seedInference(t, s, inferSeed{
		user:   "u-edge",
		events: []inferEvent{{base, "one@example.com"}},
		recs: []inferRec{
			{uuid: "before", at: base.Add(-time.Hour)},
			{uuid: "at", at: base},
			{uuid: "after", at: base.Add(30 * 24 * time.Hour)},
		},
	})

	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}

	if email, src := loginEmailSourceOf(t, s, "before"); email != "" || src != "" {
		t.Errorf("pre-first-observation record = (%q, %q), want unattributed", email, src)
	}
	if email, _ := loginEmailSourceOf(t, s, "at"); email != "one@example.com" {
		t.Errorf("record at the first observation = %q, want one@example.com", email)
	}
	if email, _ := loginEmailSourceOf(t, s, "after"); email != "one@example.com" {
		t.Errorf("post-last-observation record = %q, want one@example.com (last known account)", email)
	}
}

// An attributed row is either an OTEL observation or an earlier inference; either
// way this pass has nothing better to offer and must not restate it.
//
// Mutation: drop the empty-login_email guard from inferMatch and kept@ is
// overwritten with two@, turning a recorded fact into a guess.
func TestInferNeverOverwritesAnExistingAttribution(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(40).Add(10 * time.Hour)

	seedInference(t, s, inferSeed{
		user:   "u-kept",
		events: []inferEvent{{base, "two@example.com"}},
		recs:   []inferRec{{uuid: "kept", at: base.Add(time.Hour), email: "one@example.com"}},
	})

	n, err := s.InferSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}
	if n != 0 {
		t.Fatalf("updated %d rows, want 0", n)
	}
	if email, src := loginEmailSourceOf(t, s, "kept"); email != "one@example.com" || src != "" {
		t.Fatalf("kept = (%q, %q), want (one@example.com, '')", email, src)
	}
}

// The timeline is per user. Another user's observations are not evidence about
// this one -- two people on the same machine, or the same person under a second
// identity, would otherwise trade accounts.
//
// Mutation: remove user_id from the partition (or from the join) and quiet-r
// picks up loud@, an account its own user was never seen using.
func TestInferDoesNotCrossUsers(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(39).Add(10 * time.Hour)

	seedInference(t, s,
		inferSeed{
			user:   "u-loud",
			events: []inferEvent{{base, "loud@example.com"}},
		},
		inferSeed{
			user: "u-quiet",
			recs: []inferRec{{uuid: "quiet-r", at: base.Add(time.Hour)}},
		},
	)

	n, err := s.InferSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}
	if n != 0 {
		t.Fatalf("updated %d rows, want 0", n)
	}
	if email, src := loginEmailSourceOf(t, s, "quiet-r"); email != "" || src != "" {
		t.Fatalf("quiet-r = (%q, %q), want unattributed", email, src)
	}
}

// This is a bulk UPDATE on a multi-million row hypertable in production, so an
// operator approves a number before it runs. The number has to come from the same
// SQL the statement uses, or the thing approved is not the thing that happens.
//
// Mutation: write the preview's matching by hand (or count the excluded tiebreak
// with a second, differently-worded predicate) and the final equality check drifts.
func TestPreviewInferMatchesTheUpdate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(38).Add(10 * time.Hour)
	mid := base.Add(time.Hour)

	seedInference(t, s,
		inferSeed{
			user:   "u-plain",
			events: []inferEvent{{base, "one@example.com"}},
			recs: []inferRec{
				{uuid: "p1", at: base.Add(10 * time.Minute)},
				{uuid: "p0", at: base.Add(-10 * time.Minute)}, // before the first observation
			},
		},
		inferSeed{
			user:   "u-tie",
			events: []inferEvent{{base, "one@example.com"}, {mid, "bot@example.com"}},
			recs:   []inferRec{{uuid: "t1", at: base.Add(30 * time.Minute)}},
		},
		inferSeed{
			user: "u-dark",
			recs: []inferRec{{uuid: "d1", at: base}}, // no OTEL for this user at all
		},
	)
	excludeAccount(t, s, "bot@example.com")

	p, err := s.PreviewInferSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PreviewInferSessionRecordLoginEmail: %v", err)
	}
	if p.EmptyRows != 4 {
		t.Errorf("empty rows = %d, want 4", p.EmptyRows)
	}
	if p.FillableRows != 2 {
		t.Errorf("fillable rows = %d, want 2 (p1 and t1)", p.FillableRows)
	}
	if p.UnfillableRows != 2 {
		t.Errorf("unfillable rows = %d, want 2 (p0 and d1)", p.UnfillableRows)
	}
	if p.FillableUsers != 2 {
		t.Errorf("fillable users = %d, want 2", p.FillableUsers)
	}
	if p.ExcludedTiebreakRows != 1 {
		t.Errorf("excluded tiebreak rows = %d, want 1 (t1)", p.ExcludedTiebreakRows)
	}

	applied, err := s.InferSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}
	if applied != p.FillableRows {
		t.Fatalf("applied %d rows, preview said %d", applied, p.FillableRows)
	}
	if email, _ := loginEmailSourceOf(t, s, "t1"); email != "bot@example.com" {
		t.Fatalf("t1 = %q, want bot@example.com", email)
	}
}

// The periodic pass bounds the OTEL scan so it stops re-deriving the permanently
// unfillable tail. Narrowing the scan may leave rows blank that a full pass fills;
// it must never produce a different account for a row both passes fill, and the
// preview must describe the same scope.
//
// Mutation: ignore `since` and the bounded pass fills o1 too, which is the
// full-history rescan the bound exists to avoid.
func TestInferSinceBoundsTheScan(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	old := recentDay(60).Add(10 * time.Hour)
	recent := recentDay(10).Add(10 * time.Hour)

	seedInference(t, s,
		inferSeed{
			user:   "u-old",
			events: []inferEvent{{old, "one@example.com"}},
			recs:   []inferRec{{uuid: "o1", at: old.Add(time.Hour)}},
		},
		inferSeed{
			user:   "u-new",
			events: []inferEvent{{recent, "two@example.com"}},
			recs:   []inferRec{{uuid: "r1", at: recent.Add(time.Hour)}},
		},
	)

	since := recent.Add(-24 * time.Hour)
	p, err := s.PreviewInferSessionRecordLoginEmail(ctx, since)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if p.FillableRows != 1 {
		t.Fatalf("bounded fillable rows = %d, want 1", p.FillableRows)
	}
	applied, err := s.InferSessionRecordLoginEmail(ctx, since)
	if err != nil {
		t.Fatalf("bounded infer: %v", err)
	}
	if applied != p.FillableRows {
		t.Fatalf("applied %d rows, preview said %d", applied, p.FillableRows)
	}
	if email, _ := loginEmailSourceOf(t, s, "o1"); email != "" {
		t.Fatalf("o1 = %q, want '' (out of the bounded scan)", email)
	}

	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("full infer: %v", err)
	}
	if email, src := loginEmailSourceOf(t, s, "o1"); email != "one@example.com" || src != "inferred" {
		t.Fatalf("o1 after the full pass = (%q, %q), want (one@example.com, inferred)", email, src)
	}
}

// A bounded inference pass scopes work to users active since the boundary, but
// it must build those users' intervals from their complete retained timelines.
// Truncating the timeline at since would erase this switch and leave the earlier
// record blank; more generally, boundary truncation must not change attribution.
func TestInferBoundedPassKeepsPreSinceAccountSwitch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	since := recentDay(36).Add(10 * time.Hour)

	seedInference(t, s, inferSeed{
		user: "u-bounded-switch",
		events: []inferEvent{
			{since.Add(-2 * time.Hour), "one@example.com"},
			{since.Add(-time.Hour), "two@example.com"},
			// Keeps the user active without adding another account edge.
			{since.Add(time.Hour), "two@example.com"},
		},
		recs: []inferRec{{uuid: "bounded-before-switch", at: since.Add(-90 * time.Minute)}},
	})

	p, err := s.PreviewInferSessionRecordLoginEmail(ctx, since)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if p.FillableRows != 1 {
		t.Fatalf("preview fillable rows = %d, want 1", p.FillableRows)
	}
	if _, err := s.InferSessionRecordLoginEmail(ctx, since); err != nil {
		t.Fatalf("bounded infer: %v", err)
	}
	if email, source := loginEmailSourceOf(t, s, "bounded-before-switch"); email != "one@example.com" || source != "inferred" {
		t.Fatalf("pre-switch row = (%q, %q), want (one@example.com, inferred)", email, source)
	}
}

// The inferred rows have to reach the session list, which reads
// session_overview_rollups rather than session_records. A rollup left stale keeps
// serving the pre-inference login_email, so an excluded account stays on screen
// even though the underlying row now names it -- the original symptom, moved one
// table along.
//
// Mutation: drop refreshSessionOverviewRollups from the pass and the login-scoped
// rollup row keeps its old (empty) scope_value.
func TestInferRefreshesSessionOverviewRollups(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(36).Add(10 * time.Hour)

	seedInference(t, s, inferSeed{
		user:   "u-roll",
		events: []inferEvent{{base, "one@example.com"}},
		recs:   []inferRec{{uuid: "roll1", at: base.Add(time.Hour)}},
	})
	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}

	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM session_overview_rollups
		WHERE session_id = 'u-roll-quiet' AND scope_type = 'login' AND scope_value = 'one@example.com'`).
		Scan(&n); err != nil {
		t.Fatalf("query rollups: %v", err)
	}
	if n == 0 {
		t.Fatal("no login-scoped rollup row for the inferred account")
	}
}

// The conservative tiebreak is a statement about an ambiguous moment, not about a
// whole account. An interval runs from one account change to the next, so it
// swallows every consecutive observation of that account; a record sitting between
// two of them is bracketed by the same account on both sides and is not ambiguous
// at all. Only a record after the run's last observation, in the dark stretch
// before the next account appears, is.
//
// The distinction is not academic. Production OTEL is dense, so one
// plain@ -> bot@(excluded) switch would otherwise restamp every unattributed row
// of that user's whole plain@ period -- days or weeks of rows -- and make them all
// disappear from the dashboard.
//
// Mutation: drop `sr.ts > i.last_obs_ts` from inferPickNext and `inside` fills
// with second@, an account its own two bracketing observations rule out.
func TestInferTiebreakStopsAtTheLastObservation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(35).Add(10 * time.Hour)

	seedInference(t, s, inferSeed{
		user: "u-run",
		events: []inferEvent{
			{base, "first@example.com"},
			{base.Add(10 * time.Minute), "first@example.com"},
			{base.Add(20 * time.Minute), "second@example.com"},
		},
		recs: []inferRec{
			// Between two observations of first@: definite.
			{uuid: "inside", at: base.Add(5 * time.Minute)},
			// After first@ was last seen and before second@ appears: ambiguous.
			{uuid: "gap", at: base.Add(15 * time.Minute)},
		},
	})
	excludeAccount(t, s, "second@example.com")

	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}

	if email, _ := loginEmailSourceOf(t, s, "inside"); email != "first@example.com" {
		t.Errorf("record between two first@ observations = %q, want first@example.com", email)
	}
	if email, _ := loginEmailSourceOf(t, s, "gap"); email != "second@example.com" {
		t.Errorf("record in the ambiguous window = %q, want second@example.com", email)
	}
}

// Two accounts observed at the same instant are the same ambiguity whichever way
// their addresses sort, so they have to resolve the same way. Ordering ties
// alphabetically made that depend on the spelling: `bot@`(excluded) before
// `plain@` left the record on plain@ and on screen -- exactly the leak #346 exists
// to close -- while `zbot@`(excluded) after `alice@` hid it. Exclusion sorts last,
// so the excluded account owns the interval opening at that instant either way.
//
// Mutation: order on (ts, login_email) again and u-alpha's record comes back
// attributed to plain@, visible despite bot@ being excluded.
func TestInferPrefersExcludedAccountOnSimultaneousObservations(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(34).Add(10 * time.Hour)

	seedInference(t, s,
		inferSeed{
			user: "u-alpha", // the excluded account sorts first alphabetically
			events: []inferEvent{
				{base, "bot@example.com"},
				{base, "plain@example.com"},
			},
			recs: []inferRec{{uuid: "alpha-r", at: base}},
		},
		inferSeed{
			user: "u-omega", // and here it sorts last
			events: []inferEvent{
				{base, "alice@example.com"},
				{base, "zbot@example.com"},
			},
			recs: []inferRec{{uuid: "omega-r", at: base}},
		},
	)
	excludeAccount(t, s, "bot@example.com")
	excludeAccount(t, s, "zbot@example.com")

	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}

	if email, _ := loginEmailSourceOf(t, s, "alpha-r"); email != "bot@example.com" {
		t.Errorf("alpha-r = %q, want bot@example.com", email)
	}
	if email, _ := loginEmailSourceOf(t, s, "omega-r"); email != "zbot@example.com" {
		t.Errorf("omega-r = %q, want zbot@example.com", email)
	}
}

// login_email_source is justified by "a later, better rule can revisit the guesses
// without touching the observations", and this is the rule that does it. An
// inference writes an account onto a session nobody observed; when OTEL for that
// very session finally arrives there is no longer anything to guess about, and the
// guess must give way -- otherwise the overwrite is permanent, the column is a
// label with no consumer, and cmd/cctraced's tick-boundary race (records on tick N,
// telemetry on tick N+1) has no repair path at all.
//
// An 'otel' value stays put: replacing one observation with another loses
// information rather than adding any.
//
// Mutation: narrow backfillMatch back to the empty-login_email guard alone and
// `guessed` keeps wrong@/inferred even though its own session was observed on
// right@.
func TestBackfillRejudgesAnInferredGuessButNotAnObservation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(33).Add(10 * time.Hour)

	// The user's timeline says wrong@; the quiet session itself says nothing yet.
	seedInference(t, s, inferSeed{
		user:   "u-race",
		events: []inferEvent{{base, "wrong@example.com"}},
		recs: []inferRec{
			{uuid: "guessed", at: base.Add(time.Hour)},
			{uuid: "observed", at: base.Add(2 * time.Hour), email: "kept@example.com"},
		},
	})
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_records SET login_email_source = 'otel' WHERE uuid = 'observed'`); err != nil {
		t.Fatalf("stamp observed: %v", err)
	}
	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}
	if email, src := loginEmailSourceOf(t, s, "guessed"); email != "wrong@example.com" || src != "inferred" {
		t.Fatalf("guessed = (%q, %q), want (wrong@example.com, inferred)", email, src)
	}

	// The session's own telemetry arrives a tick later, naming a different account.
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: base.Add(30 * time.Minute), EventName: "api_request",
		SessionID: "u-race-quiet", UserID: "u-race", LoginEmail: "right@example.com",
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	p, err := s.PreviewBackfillSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if p.EmptyRows != 0 {
		t.Errorf("empty rows = %d, want 0 (inference left none)", p.EmptyRows)
	}
	if p.FillableRows != 1 || p.RejudgedRows != 1 {
		t.Errorf("fillable/rejudged = %d/%d, want 1/1", p.FillableRows, p.RejudgedRows)
	}
	// The gap is closed, so nothing is unfillable -- a rejudged row was never part
	// of it and must not be counted against it.
	if p.UnfillableRows != 0 {
		t.Errorf("unfillable rows = %d, want 0", p.UnfillableRows)
	}

	applied, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if applied != p.FillableRows {
		t.Fatalf("applied %d rows, preview said %d", applied, p.FillableRows)
	}
	if email, src := loginEmailSourceOf(t, s, "guessed"); email != "right@example.com" || src != "otel" {
		t.Errorf("guessed after the observation = (%q, %q), want (right@example.com, otel)", email, src)
	}
	if email, src := loginEmailSourceOf(t, s, "observed"); email != "kept@example.com" || src != "otel" {
		t.Errorf("observed = (%q, %q), want (kept@example.com, otel)", email, src)
	}

	// Still idempotent: what it wrote it stamped 'otel', which its own match refuses.
	again, err := s.BackfillSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if again != 0 {
		t.Fatalf("second backfill updated %d rows, want 0", again)
	}
}

// The unattributed count is the operator's list of sessions the product cannot
// place under any identity. Inference changes that population, and this pins the
// direction: an inferred account counts as an account, so the session leaves the
// list. It is not a silent change of meaning -- once inference has placed a
// session, exclusion and the account filter reach it and there is nothing an
// operator could still do about it there.
//
// Mutation: make has_account ignore inferred rows -- have its bool_or refuse any
// row whose login_email_source is 'inferred' -- and the count stays 1 after the
// pass, offering an entry nothing can act on.
func TestUnattributedCountDropsInferredSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(32).Add(10 * time.Hour)

	seedInference(t, s, inferSeed{
		user:   "u-list",
		events: []inferEvent{{base, "one@example.com"}},
		recs:   []inferRec{{uuid: "l1", at: base.Add(time.Hour)}},
	})

	unattr := SessionOverviewFilter{LoginEmail: "one@example.com", OnlyUnattributed: true}
	got, err := s.CountSessionOverviews(ctx, unattr)
	if err != nil {
		t.Fatalf("count before: %v", err)
	}
	if got != 1 {
		t.Fatalf("unattributed count before inference = %d, want 1", got)
	}

	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}

	got, err = s.CountSessionOverviews(ctx, unattr)
	if err != nil {
		t.Fatalf("count after: %v", err)
	}
	if got != 0 {
		t.Fatalf("unattributed count after inference = %d, want 0", got)
	}
}

// forceEmptyAgent writes the literal empty agent that legacy rows carry.
// InsertSessionRecords normalizes an unset agent to 'claude', so the state the
// production table is actually in -- rows written before the column had a default
// of 'claude' -- is unreachable through the writer and has to be set directly.
func forceEmptyAgent(t *testing.T, s *PgStore, uuid string) {
	t.Helper()
	tag, err := s.pool.Exec(context.Background(),
		`UPDATE session_records SET agent = '' WHERE uuid = $1`, uuid)
	if err != nil {
		t.Fatalf("force empty agent on %s: %v", uuid, err)
	}
	if tag.RowsAffected() == 0 {
		t.Fatalf("force empty agent on %s: no such record", uuid)
	}
}

// otel_events is a Claude-only source: only the Anthropic exporter writes it, so a
// user's OTEL timeline says nothing about which subscription their Codex sessions
// billed. user_id identifies a person, not an agent, so before this guard a Codex
// record simply inherited whatever Claude account its user was on at that instant
// -- on production 344,034 codex rows were stamped user-a@example.com, an address
// that has never once been observed on an OpenAI account (#524).
//
// Mutation: drop the agent line from inferMatch and the codex row is filled with
// the Claude account, which is the bug.
func TestInferSkipsCodexRecords(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(37).Add(10 * time.Hour)

	seedInference(t, s, inferSeed{
		user:   "u-mixed",
		events: []inferEvent{{base, "user-a@example.com"}},
		recs: []inferRec{
			{uuid: "mixed-claude", at: base.Add(10 * time.Minute)},
			{uuid: "mixed-codex", at: base.Add(10 * time.Minute), agent: "codex"},
		},
	})

	n, err := s.InferSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}
	if n != 1 {
		t.Fatalf("updated %d rows, want 1 (the claude row only)", n)
	}
	if email, src := loginEmailSourceOf(t, s, "mixed-claude"); email != "user-a@example.com" || src != "inferred" {
		t.Errorf("claude row = (%q, %q), want (user-a@example.com, inferred)", email, src)
	}
	if email, src := loginEmailSourceOf(t, s, "mixed-codex"); email != "" || src != "" {
		t.Errorf("codex row = (%q, %q), want unattributed", email, src)
	}
}

// The guard has to read the agent the way every other reader does. session_records
// was created with agent DEFAULT ” and only later given DEFAULT 'claude', so the
// empty string is a real, and Claude-meaning, value in the table --
// postgres_plugin_usage_facts.go and friends all fold it in with
// COALESCE(NULLIF(agent, ”), 'claude') for exactly this reason.
//
// Mutation: write the guard as `sr.agent = 'claude'` and the legacy row stays
// blank forever, silently losing the rows the inference pass exists to reach.
func TestInferTreatsEmptyAgentAsClaude(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(37).Add(11 * time.Hour)

	seedInference(t, s, inferSeed{
		user:   "u-legacy",
		events: []inferEvent{{base, "one@example.com"}},
		recs:   []inferRec{{uuid: "legacy", at: base.Add(10 * time.Minute)}},
	})
	forceEmptyAgent(t, s, "legacy")

	if _, err := s.InferSessionRecordLoginEmail(ctx, time.Time{}); err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}
	if email, src := loginEmailSourceOf(t, s, "legacy"); email != "one@example.com" || src != "inferred" {
		t.Fatalf("legacy row = (%q, %q), want (one@example.com, inferred)", email, src)
	}
}

// Excluding codex from the pass moves rows out of FillableRows and into
// UnfillableRows, and an operator reading a preview after the change would see
// that number jump with nothing to explain it. CodexRows is that explanation: the
// rows this pass now refuses structurally, not for want of evidence.
//
// Mutation: count CodexRows without the login_email = ” filter and an already
// attributed codex row inflates it past the unfillable remainder it is meant to
// account for.
func TestPreviewInferReportsCodexRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := recentDay(37).Add(12 * time.Hour)

	seedInference(t, s, inferSeed{
		user:   "u-preview",
		events: []inferEvent{{base, "one@example.com"}},
		recs: []inferRec{
			{uuid: "pv-claude", at: base.Add(10 * time.Minute)},
			{uuid: "pv-codex-a", at: base.Add(10 * time.Minute), agent: "codex"},
			{uuid: "pv-codex-b", at: base.Add(20 * time.Minute), agent: "codex"},
			// Already attributed, so it is not part of the gap this pass measures.
			{uuid: "pv-codex-done", at: base.Add(30 * time.Minute), agent: "codex", email: "obs@example.com"},
		},
	})

	p, err := s.PreviewInferSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("PreviewInferSessionRecordLoginEmail: %v", err)
	}
	if p.EmptyRows != 3 {
		t.Errorf("empty rows = %d, want 3", p.EmptyRows)
	}
	if p.FillableRows != 1 {
		t.Errorf("fillable rows = %d, want 1 (the claude row only)", p.FillableRows)
	}
	if p.UnfillableRows != 2 {
		t.Errorf("unfillable rows = %d, want 2", p.UnfillableRows)
	}
	if p.CodexRows != 2 {
		t.Errorf("codex rows = %d, want 2", p.CodexRows)
	}

	applied, err := s.InferSessionRecordLoginEmail(ctx, time.Time{})
	if err != nil {
		t.Fatalf("InferSessionRecordLoginEmail: %v", err)
	}
	if applied != p.FillableRows {
		t.Fatalf("applied %d rows, preview said %d", applied, p.FillableRows)
	}
}
