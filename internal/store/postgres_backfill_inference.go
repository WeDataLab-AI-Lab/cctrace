package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// sqlAgentKind is how every reader of session_records.agent resolves the column.
//
// The table was created with an empty-string default for agent and only later
// given the default 'claude', so the empty string is a real value that means
// Claude, and rows carrying it are still in production. A bare `agent <> 'codex'` happens to answer
// correctly for those while `agent = 'claude'` silently drops them, and neither
// says which is intended. postgres_plugin_usage_facts.go and the rest already fold
// the empty string in exactly this way; predicates written here do the same so a
// row is never classified two ways by two statements.
const sqlAgentKind = `COALESCE(NULLIF(sr.agent, ''), 'claude')`

// inferEdges turns each user's OTEL account changes into intervals on one
// timeline keyed by user_id.
//
// It is the same shape as the session-scoped backfill with the partition widened
// from session_id to user_id, and that widening is the whole point. The session-scoped
// pass can only fill a session that produced otel_events; a session that produced
// none has nothing to partition on and stays blank forever, which is how excluded
// accounts kept leaking into session_records views (#346). The user's timeline
// still covers those minutes even when the session itself is silent.
//
// The interval derivation is shared verbatim by the UPDATE and its preview: the
// row count an operator approves has to be the row count the statement produces.
//
// Ties on ts are broken by (excluded, login_email): two accounts stamped at the
// same instant resolve the same way on every run -- an answer that changes per run
// cannot be audited -- and the excluded one sorts last, so it owns the interval
// that starts at that instant. Plain alphabetical order made the same ambiguity
// resolve two opposite ways: with `bot@`(excluded) and `plain@` both observed at
// 10:00 a record at 10:00 landed on plain@ and stayed on screen, while `alice@`
// and `zbot@`(excluded) at 10:00 hid the record. Sorting on exclusion first makes
// both cases hide, which is the whole point of the conservative rule below.
//
// `runs` collapses consecutive observations of one account into a single row and
// keeps last_obs_ts, the last instant that account was actually seen inside the
// run. It is not decoration: the run's interval extends to the *next change
// point*, so a record sitting between two observations of the same account is
// inside the run yet has a definite account, while a record after the last
// observation and before the next change point has an ambiguous one. Only the
// second kind may be moved (see inferPickNext).
//
// Adjacent runs never carry the same account by construction, so pick_next needs
// no `next_login_email <> login_email` guard: a run boundary IS an account change.
//
// Full-history and bounded passes deliberately have separate literal query
// shapes. The bounded one discovers users active since the boundary, then reads
// each active user's complete retained timeline. Applying since directly to the
// timeline can discard a pre-boundary switch and either leave a record needlessly
// blank or infer from the wrong surviving interval.
const inferEdgesFullCTE = `
	WITH scoped AS (
		SELECT o.user_id, o.ts, o.login_email,
		       EXISTS (SELECT 1 FROM excluded_accounts x
		               WHERE lower(x.login_email) = lower(o.login_email)) AS excluded
		FROM otel_events o
		WHERE o.login_email <> '' AND o.user_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = o.session_id)
	), marked AS (
`

const inferEdgesBoundedCTE = `
	WITH active_users AS (
		SELECT DISTINCT user_id
		FROM otel_events
		WHERE login_email <> '' AND user_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = otel_events.session_id)
		  AND ts >= $1::timestamptz
	), scoped AS (
		SELECT o.user_id, o.ts, o.login_email,
		       EXISTS (SELECT 1 FROM excluded_accounts x
		               WHERE lower(x.login_email) = lower(o.login_email)) AS excluded
		FROM otel_events o
		JOIN active_users a ON a.user_id = o.user_id
		WHERE o.login_email <> '' AND o.user_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = o.session_id)
	), marked AS (
`

const inferEdgesCTESuffix = `
		SELECT user_id, ts, login_email, excluded,
		       LAG(login_email) OVER w AS prev
		FROM scoped
		WINDOW w AS (PARTITION BY user_id ORDER BY ts, excluded, login_email)
	), numbered AS (
		SELECT user_id, ts, login_email, excluded,
		       COUNT(*) FILTER (WHERE prev IS DISTINCT FROM login_email) OVER (
		           PARTITION BY user_id ORDER BY ts, excluded, login_email
		           ROWS UNBOUNDED PRECEDING) AS run_no
		FROM marked
	), runs AS (
		SELECT user_id, run_no, login_email, excluded,
		       MIN(ts) AS from_ts, MAX(ts) AS last_obs_ts
		FROM numbered
		GROUP BY user_id, run_no, login_email, excluded
	), intervals AS (
		SELECT user_id, login_email, from_ts, last_obs_ts,
		       LEAD(from_ts) OVER w AS to_ts,
		       LEAD(login_email) OVER w AS next_login_email,
		       (LEAD(excluded) OVER w AND NOT excluded) AS pick_next
		FROM runs
		WINDOW w AS (PARTITION BY user_id ORDER BY run_no)
	)`

func inferEdgesFor(since time.Time) (string, []any) {
	if since.IsZero() {
		return inferEdgesFullCTE + inferEdgesCTESuffix, nil
	}
	return inferEdgesBoundedCTE + inferEdgesCTESuffix, []any{since}
}

// inferPickNext is the conservative tiebreak, and it applies to one record at a
// time rather than to a whole interval.
//
// A record after this run's last observation and before the next account's first
// one is bracketed by two different accounts, and the data does not say which was
// in use. The two errors are not equally expensive: choosing the non-excluded side
// and being wrong leaves an excluded account on screen, which is the defect this
// exists to remove, while choosing the excluded side and being wrong hides a row
// that unexcluding the account brings back (and that an OTEL observation on the
// session now corrects outright -- see backfillMatch). So when exactly one side is
// excluded, the excluded side wins.
//
// `sr.ts > i.last_obs_ts` is what keeps that judgement inside the ambiguous
// window. Without it the whole run was flipped, including records sandwiched
// between two observations of the same account -- rows whose account is not in
// doubt at all. On production OTEL is dense, so one plain@ -> bot@(excluded)
// switch would have restamped every unattributed row of that user's entire plain@
// period, days or weeks of them, and made them all disappear.
const inferPickNext = `(i.pick_next AND sr.ts > i.last_obs_ts)`

// inferPickEmail is the account written for one record: the interval's own, unless
// the tiebreak above moves it to the next one.
const inferPickEmail = `CASE WHEN ` + inferPickNext + `
	          THEN i.next_login_email ELSE i.login_email END`

// inferMatch pairs an unattributed record with the interval its ts falls in.
//
// Unlike backfillMatch there is no rn = 1 reach-back. That relaxation is bounded
// by a session, so its worst case is a record minutes older than the first event
// of the session it already belongs to. A user's timeline has no such bound: OTEL
// collection started in 2026-05 and session_records goes back further, so reaching
// behind the first observation would label hundreds of thousands of rows on no
// evidence. syncer/state.go's ClaudeAccountAt refuses the same thing for the same
// reason.
//
// Forward the last interval is open-ended, which is the other half of that same
// contract: "the latest observation at or before ts". An account was seen in use
// and nothing has been seen replacing it -- evidence, unlike the silence before
// the first observation.
//
// Deliberately no forward cap. Any number would be arbitrary: otel_events'
// 90-day retention bounds what can be *scanned*, not how stale a last observation
// may be, and a user who simply stopped emitting OTEL has not thereby changed
// account. What makes the open end safe is that the guess is now correctable --
// backfillMatch admits login_email_source = 'inferred', so an OTEL observation
// arriving on that very session later overwrites it with the observed account.
// That also closes cmd/cctraced's tick-boundary race: the backfill -> infer
// ordering only holds within one pass, and a session whose records land on tick N
// and whose otel_events land on tick N+1 used to keep the tick-N guess forever.
//
// It is never corrected by a *later inference*, though: this match still refuses
// non-empty login_email, so a second pass will not restate its own guesses. That
// is on purpose -- the periodic pass runs every 48h against a sliding window, and
// letting it rewrite rows it already wrote would churn a hypertable for no new
// evidence in the overwhelmingly common case. The provenance column is what keeps
// a future, better rule free to select those rows and redo them in one shot.
//
// Intervals of one user are disjoint and half-open, so a record matches at most
// one row and UPDATE ... FROM has nothing to arbitrate.
//
// Codex records are refused outright. otel_events is a Claude-only table -- only
// the Anthropic exporter writes it, and production holds 2,335,512 rows all
// carrying agent = 'claude' -- while user_id identifies a person, not an agent. So
// a user's OTEL timeline is evidence about which Anthropic subscription they were
// on, and says nothing whatever about which OpenAI subscription their Codex
// sessions billed. Without this guard it was read as both: 344,034 codex rows were
// stamped user-a@example.com, an address quota_samples has never once observed on
// an OpenAI account (#524). #372 had built excluded_billing_accounts on the premise
// that codex rows carry no login_email at all, and #357 shipped this inference in
// the same release and broke that premise.
//
// The guard sits on the record rather than on the source in inferEdges* on
// purpose. If an OTEL exporter for Codex ever appears, restricting the source
// would quietly start attributing codex rows from it, whereas "a Codex record's
// account is whatever the quota mapping says" stays the right answer either way --
// the quota mapping is the only source that knows about OpenAI billing. Leaving
// inferEdges* untouched also keeps its two query shapes stable.
const inferMatch = `
	  AND sr.user_id <> ''
	  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
	  AND sr.login_email = ''
	  AND ` + sqlAgentKind + ` <> 'codex'
	  AND sr.ts >= i.from_ts
	  AND sr.ts < COALESCE(i.to_ts, 'infinity'::timestamptz)`

// LoginEmailInferencePreview reports what an inference pass would change.
type LoginEmailInferencePreview struct {
	// EmptyRows is every unattributed session_records row, the size of the gap.
	EmptyRows int64 `json:"empty_rows"`
	// FillableRows is how many of those this pass would fill.
	FillableRows int64 `json:"fillable_rows"`
	// UnfillableRows is the remainder. It is not a defect: a user with no OTEL at
	// all, and every record older than its user's first observation, belong here by
	// design.
	UnfillableRows int64 `json:"unfillable_rows"`
	// ExcludedTiebreakRows is how many of FillableRows are attributed to an
	// excluded account only because the conservative tiebreak chose it over the
	// interval's own account. These are the rows that will disappear from the
	// dashboard on a judgement call rather than on an observation, so they are
	// counted apart -- an operator approving this pass should see that number
	// before, not discover it after.
	ExcludedTiebreakRows int64 `json:"excluded_tiebreak_rows"`
	// FillableUsers is how many distinct users the pass would touch.
	FillableUsers int64 `json:"fillable_users"`
	// CodexRows is how many unattributed rows this pass refuses structurally rather
	// than for want of evidence: Codex records, which inferMatch will never fill
	// from a Claude-only OTEL timeline no matter how dense it is. They are part of
	// UnfillableRows, and without this number the jump in that total when the guard
	// landed would read as a regression instead of the correction it is.
	CodexRows int64 `json:"codex_rows"`
}

// PreviewInferSessionRecordLoginEmail measures the blast radius of
// InferSessionRecordLoginEmail for the same `since`, reading only. It runs the
// identical interval matching as the UPDATE.
func (s *PgStore) PreviewInferSessionRecordLoginEmail(ctx context.Context, since time.Time) (*LoginEmailInferencePreview, error) {
	edges, args := inferEdgesFor(since)
	q := edges + `
	SELECT
		(SELECT COUNT(*) FROM session_records WHERE login_email = ''),
		(SELECT COUNT(*) FROM session_records sr
		 WHERE sr.login_email = '' AND ` + sqlAgentKind + ` = 'codex'),
		COUNT(*),
		COUNT(*) FILTER (WHERE ` + inferPickNext + `),
		COUNT(DISTINCT sr.user_id)
	FROM session_records sr
	JOIN intervals i ON sr.user_id = i.user_id` + inferMatch

	p := &LoginEmailInferencePreview{}
	if err := s.pool.QueryRow(ctx, q, args...).
		Scan(&p.EmptyRows, &p.CodexRows, &p.FillableRows, &p.ExcludedTiebreakRows, &p.FillableUsers); err != nil {
		return nil, err
	}
	p.UnfillableRows = p.EmptyRows - p.FillableRows
	return p, nil
}

// InferSessionRecordLoginEmail fills session_records.login_email from the user's
// OTEL timeline for records the session-scoped backfill can never reach.
//
// BackfillSessionRecordLoginEmail partitions otel_events by session_id, so a
// session that emitted no OTEL at all has no interval to match against and keeps
// login_email empty forever. visible_session_records hides an account by comparing
// that column, and a blank matches no exclusion, so those rows stayed on screen no
// matter how many times an account was excluded (#346 -- on production this was
// two silent sessions holding 48 rows of an already-excluded account, wrapped on
// the user's own timeline by two observations of that very account).
//
// It writes login_email_source = 'inferred'. What this pass produces is a
// judgement from surrounding evidence, not something OTEL said about these rows,
// and the two must stay distinguishable: only then can a later, better rule
// revisit the guesses without touching the observations. The empty-login_email
// guard in inferMatch is what keeps an observation out of reach.
//
// since limits the pass to users with OTEL activity at or after the bound. The
// timeline for each active user still includes all retained observations, so a
// pre-boundary account switch cannot change or erase attribution merely because
// the source window was truncated.
//
// The three derived tables are refreshed for the same reason the session-scoped
// pass refreshes them: session_overview_rollups keys a whole scope on login_email
// (scope_type = 'login'), and plugin_invocation_facts and task_segment_facts copy
// it. Leaving them stale would move the leak one table along -- the session list
// would keep serving the pre-inference blank while session_records named the
// excluded account.
func (s *PgStore) InferSessionRecordLoginEmail(ctx context.Context, since time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	updated, ids, err := inferSessionRecordLoginEmailTx(ctx, tx, since)
	if err != nil {
		return 0, err
	}
	if err := refreshLoginEmailDerivedRows(ctx, tx, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return updated, nil
}

func inferSessionRecordLoginEmailTx(ctx context.Context, tx pgx.Tx, since time.Time) (int64, []string, error) {
	edges, args := inferEdgesFor(since)
	q := edges + `
	UPDATE session_records sr
	SET login_email = ` + inferPickEmail + `, login_email_source = 'inferred'
	FROM intervals i
	WHERE sr.user_id = i.user_id` + inferMatch + `
	RETURNING sr.session_id`
	return updateLoginEmailRows(ctx, tx, q, args...)
}
