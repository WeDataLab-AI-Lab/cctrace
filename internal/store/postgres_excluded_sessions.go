package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// RecomputeExcludedSessions fully rebuilds excluded_sessions from the current
// excluded_accounts set, and the whole session overview with it. It is for callers
// that cannot say what changed; an exclusion change goes through
// applyExclusionChange, which names it and so rebuilds only what it reaches.
//
// A full rebuild rather than an incremental delete/insert is required for
// correctness on removal: a session two accounts both touched must stay hidden
// after only one of them is un-excluded, which a DELETE keyed on the removed
// account's own session_ids would get wrong. Exclusion changes are rare enough that
// a full otel_events scan on every change is an acceptable cost.
func (s *PgStore) RecomputeExcludedSessions(ctx context.Context) error {
	return s.applyExclusionChange(ctx, nil, nil, nil, true)
}

// recomputeExcludedSessionsTx rebuilds everything visibility-derived that lives
// under the session-overview maintenance lock, and returns the billing accounts
// whose link to an excluded address it added or removed -- the caller queues a
// usage rebuild for those (see applyExclusionChange).
//
// emails and accounts are what the caller changed. Unless full is set, only the
// overview rows of sessions they, the changed links, or the change in
// excluded_sessions reach are rebuilt: the full rebuild rewrote every session in
// history and held the POST that triggered it open for over ten minutes on
// production data (#715).
func (s *PgStore) recomputeExcludedSessionsTx(ctx context.Context, tx pgx.Tx, emails []string, accounts []BillingAccountRef, full bool) ([]BillingAccountRef, error) {
	// Lock before touching excluded_sessions. Every overview refresh takes the
	// shared maintenance lock before reading this table; reversing those two locks
	// creates a classic two-transaction deadlock.
	if err := lockSessionOverviewMaintenance(ctx, tx, true); err != nil {
		return nil, err
	}
	// DELETE, not TRUNCATE. This transaction can run for minutes on production data
	// (a full overview rebuild below is in it), and TRUNCATE's AccessExclusive lock
	// blocked every dashboard read of the visible_* views for all of that time
	// (#715: five minutes after a deploy). A DELETE lets readers keep the previous
	// set until this commits.
	before, err := querySessionIDs(ctx, tx, `DELETE FROM excluded_sessions RETURNING session_id`)
	if err != nil {
		return nil, err
	}
	// The billing accounts the excluded addresses reach: every view and rollup
	// rebuilt from here reads them (#715).
	linked, err := syncExcludedBillingLinksTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	// Read after the links above: a profile is fully excluded through them too.
	if err := syncExcludedCodexMetricProfilesTx(ctx, tx); err != nil {
		return nil, err
	}
	if err := syncExcludedCodexMetricMinutesTx(ctx, tx); err != nil {
		return nil, err
	}
	// The rebuild empties the table first, so anything in this table that is not derived from
	// excluded_accounts has to be re-supplied here or it is lost. Deleted sessions
	// are exactly that: DeleteSession writes them here so the read path hides them
	// immediately, and a recompute that forgot them would make deleted sessions
	// reappear in every list until the sweep caught up.
	// The table was emptied above, so what this returns is the whole new set.
	after, err := querySessionIDs(ctx, tx, `
		INSERT INTO excluded_sessions (session_id)
		SELECT DISTINCT o.session_id FROM otel_events o
		JOIN excluded_accounts x ON lower(x.login_email) = lower(o.login_email)
		WHERE o.session_id <> ''
		UNION
		SELECT session_id FROM deleted_sessions
		ON CONFLICT (session_id) DO NOTHING
		RETURNING session_id`)
	if err != nil {
		return nil, err
	}

	// Advance the incremental refresh watermark to the newest otel_events row this
	// rebuild already covered, so the next periodic tick only adds what arrives after
	// it. Keyed on id, not ts — see the watermark table's migration comment for why
	// (delayed/replayed WAL rows keep their original ts, which an id can't fake).
	var maxID int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM otel_events`).Scan(&maxID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO excluded_sessions_refresh_state (id, last_event_id) VALUES (true, $1)
		ON CONFLICT (id) DO UPDATE SET last_event_id = EXCLUDED.last_event_id`, maxID); err != nil {
		return nil, err
	}
	// Account exclusion changes visibility rather than source rows. Rebuild in this
	// transaction so the overview fast path changes atomically with excluded_sessions.
	if full {
		if err := rebuildAllSessionOverviewRollups(ctx, tx); err != nil {
			return nil, err
		}
		return linked, nil
	}
	// A session's visibility can only have changed if it entered or left
	// excluded_sessions, or has a row under a changed address or billing account.
	// This transaction holds the maintenance lock exclusively, so the rows are
	// replaced without the per-session locks a concurrent refresh would take.
	affected, err := sessionsOfIdentitiesTx(ctx, tx, emails, append(append([]BillingAccountRef(nil), accounts...), linked...))
	if err != nil {
		return nil, err
	}
	affected = append(affected, symmetricDifference(before, after)...)
	if err := replaceSessionOverviewRollups(ctx, tx, affected); err != nil {
		return nil, err
	}
	return linked, nil
}

// sessionsOfIdentitiesTx returns the sessions with a row, in any source the
// session overview reads, under one of the addresses (case-folded) or billing
// accounts. The base tables are read rather than unified_events: its codex arm
// carries a window function nothing pushes a predicate through.
func sessionsOfIdentitiesTx(ctx context.Context, tx pgx.Tx, emails []string, accounts []BillingAccountRef) ([]string, error) {
	if len(emails) == 0 && len(accounts) == 0 {
		return nil, nil
	}
	lowered := make([]string, 0, len(emails))
	for _, e := range emails {
		lowered = append(lowered, normalizeLoginEmail(e))
	}
	providers := make([]string, 0, len(accounts))
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		providers = append(providers, a.BillingProvider)
		ids = append(ids, a.AccountID)
	}
	return querySessionIDs(ctx, tx, `
		WITH acct AS (SELECT * FROM unnest($2::text[], $3::text[]) a(billing_provider, account_id))
		SELECT session_id FROM otel_events WHERE lower(login_email) = ANY($1)
		UNION
		SELECT session_id FROM claude_imputed_cost WHERE lower(login_email) = ANY($1)
		UNION
		SELECT session_id FROM codex_imputed_cost
		WHERE lower(login_email) = ANY($1) OR (billing_provider, account_id) IN (SELECT * FROM acct)
		UNION
		SELECT session_id FROM session_records
		WHERE lower(login_email) = ANY($1) OR (billing_provider, account_id) IN (SELECT * FROM acct)`,
		lowered, providers, ids)
}

// symmetricDifference returns the ids in exactly one of a and b.
func symmetricDifference(a, b []string) []string {
	inA := make(map[string]bool, len(a))
	for _, id := range a {
		inA[id] = true
	}
	var out []string
	for _, id := range b {
		if inA[id] {
			delete(inA, id)
		} else {
			out = append(out, id)
		}
	}
	for id := range inA {
		out = append(out, id)
	}
	return out
}

// RefreshExcludedSessionsIncremental adds session_ids seen in otel_events since the
// last refresh watermark to excluded_sessions. Intended for the periodic refresh loop
// in cmd/cctraced (same cadence as the login_email backfill / imputed-cost refresh):
// otel_events ingest is a bulk CopyFrom with no per-row hook, so this is what closes
// the gap for a session that starts under an already-excluded account between two
// runs of RecomputeExcludedSessions. This is the staleness window documented on the
// visible_session_records view.
//
// The watermark is otel_events.id, not ts. otel_events ingest can replay through the
// disk WAL buffer (internal/buffer/diskspiller.go) after downtime, and a replayed row
// keeps its original event ts — which can already be behind a ts-based watermark, so
// a "ts > watermark" query would never see it and an excluded account's session would
// leak permanently. id is assigned at insert time (BIGSERIAL), so a replayed row
// always gets a fresh id above the watermark regardless of its ts.
func (s *PgStore) RefreshExcludedSessionsIncremental(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var lastID int64
	err = tx.QueryRow(ctx, `SELECT last_event_id FROM excluded_sessions_refresh_state WHERE id`).Scan(&lastID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		lastID = 0 // no watermark yet: treat as "everything is new"
	}

	var maxID *int64
	if err := tx.QueryRow(ctx,
		`SELECT max(id) FROM otel_events WHERE id > $1`, lastID).Scan(&maxID); err != nil {
		return err
	}
	if maxID == nil {
		return tx.Commit(ctx) // nothing new since the last refresh
	}

	// Match the universal order: maintenance advisory lock, then any relation lock
	// on excluded_sessions. Full rebuilds take the exclusive form in the same order.
	if err := lockSessionOverviewMaintenance(ctx, tx, false); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO excluded_sessions (session_id)
		SELECT DISTINCT o.session_id FROM otel_events o
		JOIN excluded_accounts x ON lower(x.login_email) = lower(o.login_email)
		WHERE o.id > $1 AND o.session_id <> ''
		ON CONFLICT DO NOTHING
		RETURNING session_id`, lastID)
	if err != nil {
		return err
	}
	var newlyExcluded []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			rows.Close()
			return err
		}
		newlyExcluded = append(newlyExcluded, sessionID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO excluded_sessions_refresh_state (id, last_event_id) VALUES (true, $1)
		ON CONFLICT (id) DO UPDATE SET last_event_id = EXCLUDED.last_event_id`, *maxID); err != nil {
		return err
	}
	if err := refreshSessionOverviewRollups(ctx, tx, newlyExcluded); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
