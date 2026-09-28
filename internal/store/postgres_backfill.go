package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// The backfill has separate literal query shapes for full-history and bounded
// passes. Besides avoiding a nullable OR predicate (which produces a poor generic
// plan), the bounded shape first discovers recently active sessions and then reads
// their complete retained timelines. Filtering timeline rows directly at since
// can erase an account switch just before the boundary and make the first retained
// account incorrectly reach backwards to older records in that session.
const backfillEdgesFullCTE = `
	WITH scoped AS (
		SELECT session_id, ts, login_email
		FROM otel_events
		WHERE login_email <> '' AND session_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = otel_events.session_id)
	), marked AS (
`

const backfillEdgesBoundedCTE = `
	WITH active_sessions AS (
		SELECT DISTINCT session_id
		FROM otel_events
		WHERE login_email <> '' AND session_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = otel_events.session_id)
		  AND ts >= $1::timestamptz
	), scoped AS (
		SELECT o.session_id, o.ts, o.login_email
		FROM otel_events o
		JOIN active_sessions a ON a.session_id = o.session_id
		WHERE o.login_email <> '' AND o.session_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = o.session_id)
	), marked AS (
`

// backfillEdgesCTESuffix is shared verbatim by both scopes, and by each scope's
// UPDATE and preview. Ordering breaks ts ties on login_email so simultaneous
// observations resolve deterministically.
const backfillEdgesCTESuffix = `
		SELECT session_id, ts, login_email,
		       LAG(login_email) OVER (PARTITION BY session_id ORDER BY ts, login_email) AS prev
		FROM scoped
	), edges AS (
		SELECT session_id, login_email, ts AS from_ts,
		       LEAD(ts) OVER (PARTITION BY session_id ORDER BY ts, login_email) AS to_ts,
		       ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY ts, login_email) AS rn
		FROM marked
		WHERE prev IS DISTINCT FROM login_email
	)`

func backfillEdgesFor(since time.Time) (string, []any) {
	if since.IsZero() {
		return backfillEdgesFullCTE + backfillEdgesCTESuffix, nil
	}
	return backfillEdgesBoundedCTE + backfillEdgesCTESuffix, []any{since}
}

// backfillMatch is the predicate pairing a record with the interval it falls in.
// The first interval of a session reaches back to the session start (rn = 1).
//
// It admits two kinds of row: never attributed, and attributed by the inference
// pass. The second is what makes login_email_source more than a label. An inferred
// value is a judgement about a session nobody observed; when OTEL for that very
// session finally arrives, an observation exists and must win, or the guess is
// permanent and the "a later, better rule can revisit the guesses" claim the
// column is justified by is never actually exercised anywhere. It is also the only
// thing that closes cmd/cctraced's tick-boundary race, where a session's records
// land on one tick and its otel_events on the next.
//
// 'otel' is never overwritten. Replacing one observation with another is not a
// correction, it is losing information: two observations disagreeing means the
// account switched mid-session, which the interval split already represents.
const backfillMatch = `
	  AND sr.session_id <> ''
	  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
	  AND (sr.login_email = '' OR sr.login_email_source = 'inferred')
	  AND (e.rn = 1 OR sr.ts >= e.from_ts)
	  AND sr.ts < COALESCE(e.to_ts, 'infinity'::timestamptz)`

// BackfillPreview reports what a backfill pass would change, without changing it.
type BackfillPreview struct {
	// EmptyRows is every unattributed session_records row, regardless of scope:
	// the size of the gap the backfill exists to close.
	EmptyRows int64 `json:"empty_rows"`
	// FillableRows is every row this pass would write: the empty ones it fills,
	// plus RejudgedRows below. It is the number the UPDATE returns.
	FillableRows int64 `json:"fillable_rows"`
	// RejudgedRows is how many of FillableRows already carry a value -- one the
	// inference pass guessed, which an OTEL observation on this session now
	// replaces. Counted apart because they are not a gap being closed but an
	// attribution being changed, and an operator reading FillableRows against
	// EmptyRows would otherwise see more rows written than there were empty ones.
	RejudgedRows int64 `json:"rejudged_rows"`
	// UnfillableRows is the remainder this pass leaves empty. For an unbounded
	// pass these are sessions with no OTEL at all -- they predate OTEL collection
	// and can never be filled, so a shortfall here is not a defect.
	UnfillableRows int64 `json:"unfillable_rows"`
	// FillableSessions is how many distinct sessions the pass would touch.
	FillableSessions int64 `json:"fillable_sessions"`
}

// PreviewBackfillSessionRecordLoginEmail measures the blast radius of
// BackfillSessionRecordLoginEmail for the same `since`, reading only.
//
// It runs the identical interval matching as the UPDATE, so the row count an
// operator approves is the row count the UPDATE produces.
func (s *PgStore) PreviewBackfillSessionRecordLoginEmail(ctx context.Context, since time.Time) (*BackfillPreview, error) {
	edges, args := backfillEdgesFor(since)
	q := edges + `
	SELECT
		(SELECT COUNT(*) FROM session_records
		   WHERE login_email = '' AND session_id <> ''),
		COUNT(*),
		COUNT(*) FILTER (WHERE sr.login_email <> ''),
		COUNT(DISTINCT sr.session_id)
	FROM session_records sr
	JOIN edges e ON sr.session_id = e.session_id` + backfillMatch

	p := &BackfillPreview{}
	if err := s.pool.QueryRow(ctx, q, args...).
		Scan(&p.EmptyRows, &p.FillableRows, &p.RejudgedRows, &p.FillableSessions); err != nil {
		return nil, err
	}
	// Only the empty half of FillableRows closes the gap EmptyRows measures; the
	// rejudged half was never part of it.
	p.UnfillableRows = p.EmptyRows - (p.FillableRows - p.RejudgedRows)
	return p, nil
}

// BackfillSessionRecordLoginEmail fills empty session_records.login_email from
// otel_events. otel_events carries the Anthropic login account (user.email)
// recorded at session run time, which the JSONL sync path never populates, so
// this is the only accurate source for retroactive attribution.
//
// Attribution is per record, not per session. session_id survives /login, so a
// session can hold rows from two accounts; collapsing it to one value (or
// skipping it) merges their tokens and picks the label arbitrarily, which is
// the mis-attribution #220 exists to remove. Instead the account's change
// points within a session become intervals, and each record is stamped with the
// interval its ts falls in.
//
// The first interval of a session reaches back to the session start. That is a
// deliberate, bounded relaxation of the "never guess before the first
// observation" rule that codexauth/CodexAccountAt enforces: there the
// observation log spans the whole process lifetime, so reaching backwards could
// stamp a months-old record, while here it cannot reach outside one session. A
// session with no OTEL at all still stays blank.
//
// since limits the pass to sessions with OTEL activity at or after the bound;
// the zero value means all retained history. For each active session the bounded
// pass still reads its complete retained timeline, so a switch before since is
// not erased and cannot make the post-switch account reach backwards. otel_events
// is itself under a 90-day retention policy while session_records is not.
//
// login_email is stored exactly as the client sent it and nothing lowercases it
// on ingest, so the value is copied verbatim -- the excluded_accounts comparison
// case-folds for the same reason.
//
// What it writes is an observation, not a guess, so it stamps
// login_email_source = 'otel'. The inference pass in postgres_backfill_inference.go
// reads that column to tell its own guesses apart from these facts.
//
// Overwrites nothing an observation put there: only empty values and inferred
// guesses are in reach (see backfillMatch). Excludes empty session_id, which would
// otherwise merge unrelated records into one bogus group, and returns the number
// of rows updated. Idempotent -- what it writes it stamps 'otel', which its own
// match then refuses.
func (s *PgStore) BackfillSessionRecordLoginEmail(ctx context.Context, since time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	updated, ids, err := backfillSessionRecordLoginEmailTx(ctx, tx, since)
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

func backfillSessionRecordLoginEmailTx(ctx context.Context, tx pgx.Tx, since time.Time) (int64, []string, error) {
	edges, args := backfillEdgesFor(since)
	q := edges + `
	UPDATE session_records sr
	SET login_email = e.login_email, login_email_source = 'otel'
	FROM edges e
	WHERE sr.session_id = e.session_id` + backfillMatch + `
	RETURNING sr.session_id`
	return updateLoginEmailRows(ctx, tx, q, args...)
}

func updateLoginEmailRows(ctx context.Context, tx pgx.Tx, query string, args ...any) (int64, []string, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	var ids []string
	var updated int64
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, nil, err
		}
		updated++
		if _, ok := seen[id]; !ok && id != "" {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return updated, ids, rows.Err()
}

func refreshLoginEmailDerivedRows(ctx context.Context, tx pgx.Tx, ids []string) error {
	if err := refreshSessionOverviewRollups(ctx, tx, ids); err != nil {
		return err
	}
	if err := refreshPluginInvocationFacts(ctx, tx, ids); err != nil {
		return err
	}
	return refreshTaskSegmentFacts(ctx, tx, ids)
}

// rebuildAllLoginEmailDerivedRows refreshes retained history after an explicit repair.
// Preserve the overview -> plugin -> task maintenance lock order used by scoped refreshes.
func rebuildAllLoginEmailDerivedRows(ctx context.Context, tx pgx.Tx) error {
	if err := rebuildAllSessionOverviewRollups(ctx, tx); err != nil {
		return err
	}
	if err := rebuildAllPluginInvocationFacts(ctx, tx); err != nil {
		return err
	}
	return rebuildAllTaskSegmentFacts(ctx, tx)
}
