package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

const sessionOverviewRollupBackfill = "session_overview_rollups_v1"

func querySessionIDs(ctx context.Context, tx pgx.Tx, query string, args ...interface{}) ([]string, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// lockSessionOverviewSourceMutation serializes rare identity-wide admin mutations
// against all overview-relevant source writers. Ordinary writers take the shared form
// and remain concurrent; DeleteUserData/MergeUsers take the exclusive form before they
// discover affected sessions, so no unseen session can commit into the mutation window.
func lockSessionOverviewSourceMutation(ctx context.Context, tx pgx.Tx, exclusive bool) error {
	query := `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_source'), 0)`
	if exclusive {
		query = `SELECT pg_advisory_xact_lock(hashtext('session_overview_source'), 0)`
	}
	if _, err := tx.Exec(ctx, query); err != nil {
		return fmt.Errorf("lock session overview source mutation: %w", err)
	}
	return nil
}

func lockSessionOverviewMaintenance(ctx context.Context, tx pgx.Tx, exclusive bool) error {
	query := `SELECT pg_advisory_xact_lock_shared(hashtext('session_overview_rollups'), 0)`
	if exclusive {
		query = `SELECT pg_advisory_xact_lock(hashtext('session_overview_rollups'), 0)`
	}
	if _, err := tx.Exec(ctx, query); err != nil {
		return fmt.Errorf("lock session overview maintenance: %w", err)
	}
	return nil
}

// lockSessionOverviewRollups takes the shared maintenance lock before deterministic
// per-session locks. Full rebuilds take the exclusive form of the maintenance lock,
// so touched refreshes may run concurrently with each other but never race a rebuild.
func lockSessionOverviewRollups(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {
	if err := lockSessionOverviewMaintenance(ctx, tx, false); err != nil {
		return err
	}
	if len(sessionIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(session_id, 731))
		FROM unnest($1::text[]) session_id ORDER BY session_id`, sessionIDs); err != nil {
		return fmt.Errorf("lock session overview rollups: %w", err)
	}
	return nil
}

// refreshSessionOverviewRollups replaces every scope row for the supplied sessions.
// The caller must hold a transaction so source rows and their derived overview commit
// atomically. Empty input (including nil) is a no-op; full rebuilds use the separate
// rebuildAllSessionOverviewRollups entry point.
func refreshSessionOverviewRollups(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	if err := lockSessionOverviewRollups(ctx, tx, sessionIDs); err != nil {
		return err
	}
	return replaceSessionOverviewRollups(ctx, tx, sessionIDs)
}

// replaceSessionOverviewRollups is refreshSessionOverviewRollups without the
// locks, for a caller that already holds the maintenance lock exclusively. Every
// other writer takes that lock shared first, so the per-session locks would only
// fill the lock table: one per session, and an exclusion change can touch
// thousands of sessions.
func replaceSessionOverviewRollups(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, sessionOverviewRollupDeleteScopedSQL, sessionIDs); err != nil {
		return fmt.Errorf("delete session overview rollups: %w", err)
	}
	if _, err := tx.Exec(ctx, sessionOverviewRollupInsertScopedSQL, sessionIDs); err != nil {
		return fmt.Errorf("insert session overview rollups: %w", err)
	}
	return nil
}

// rebuildAllSessionOverviewRollups replaces the entire overview under the exclusive
// maintenance lock. Callers must acquire this lock before any relation locks.
func rebuildAllSessionOverviewRollups(ctx context.Context, tx pgx.Tx) error {
	if err := lockSessionOverviewMaintenance(ctx, tx, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sessionOverviewRollupDeleteFullSQL); err != nil {
		return fmt.Errorf("delete session overview rollups: %w", err)
	}
	if _, err := tx.Exec(ctx, sessionOverviewRollupInsertFullSQL); err != nil {
		return fmt.Errorf("insert session overview rollups: %w", err)
	}
	return nil
}

// BackfillSessionOverviewRollups builds the initial session overview aggregate after
// migration has recreated the visible_* views. It belongs off the listener startup path:
// rebuilding a large existing database can take minutes.
//
// The marker and data are committed together, so a crash either leaves both absent (safe
// to retry) or both present (the history scan is never repeated).
func (s *PgStore) BackfillSessionOverviewRollups(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, sessionOverviewRollupBackfill); err != nil {
		return err
	}
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, sessionOverviewRollupBackfill).Scan(&done); err != nil {
		return err
	}
	if done {
		return tx.Commit(ctx)
	}
	if err := rebuildAllSessionOverviewRollups(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, sessionOverviewRollupBackfill); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// sessionStartExpr and sessionEndExpr bound a session by the rows that carry a
// timestamp of their own. Claude state lines -- last-prompt, permission-mode,
// cost-state, ai-title and friends -- have none in the source, and the syncer stores
// them at the zero instant so that rereading the same bytes lands on the same row
// (see toStoreRecord). A row that never knew when it happened must not say when the
// session began: plain MIN(ts) took the zero instant and dated 979 production
// sessions to 1970-01-01 in the session list.
//
// The COALESCE fallback covers a session that has nothing else -- all of its rows are
// state lines. It then keeps the only timestamp it has. NULL is not an option: the
// column is NOT NULL in session_overview_rollups, start_time/end_time are non-nullable
// in the API contract, and the list orders on end_time, so a NULL would sort
// unpredictably instead of merely being wrong by a known amount.
//
// It is one rule with three call sites (this rollup, the live session list, the older
// session summaries), and nothing at runtime compares them, so they share the source.
func sessionStartExpr(col string) string {
	return "COALESCE(MIN(" + col + ") FILTER (WHERE " + col + " > 'epoch'), MIN(" + col + "))"
}

// sessionEndExpr is the same rule at the other end. MAX only reaches the zero instant
// when every row is a state line, which is exactly the fallback case.
func sessionEndExpr(col string) string {
	return "COALESCE(MAX(" + col + ") FILTER (WHERE " + col + " > 'epoch'), MAX(" + col + "))"
}

const (
	sessionOverviewRollupDeleteFullSQL   = `DELETE FROM session_overview_rollups`
	sessionOverviewRollupDeleteScopedSQL = `DELETE FROM session_overview_rollups WHERE session_id = ANY($1::text[])`
)

var (
	sessionOverviewRollupInsertFullSQL = sessionOverviewRollupInsertPrefixSQL +
		sessionOverviewRollupEventFullFilterSQL + sessionOverviewRollupBetweenSourcesSQL +
		sessionOverviewRollupRecordFullFilterSQL + sessionOverviewRollupInsertSuffixSQL
	sessionOverviewRollupInsertScopedSQL = sessionOverviewRollupInsertPrefixSQL +
		sessionOverviewRollupEventScopedFilterSQL + sessionOverviewRollupBetweenSourcesSQL +
		sessionOverviewRollupRecordScopedFilterSQL + sessionOverviewRollupInsertSuffixSQL
)

const sessionOverviewRollupInsertPrefixSQL = `
WITH session_union AS (
	SELECT sc.scope_type, sc.scope_value, e.session_id, e.profile_email, e.user_id,
		e.login_email, e.model, COALESCE(e.agent,'claude') AS agent, e.ts,
		'otel'::text AS src, ''::text AS source_file, ''::text AS prompt_source,
		''::text AS entrypoint, ''::text AS forked_from_session, ''::text AS project_hash,
		e.input_tokens, e.output_tokens, e.cost_usd::double precision AS cost_usd,
		e.event_name, ''::text AS cctrace_version, e.service_version AS claude_version,
		''::text AS account_id,
		-- otel_events has no provenance column and needs none: a row here IS the
		-- observation the srec arm's 'otel' stamp refers to, so it can never be
		-- 'inferred'.
		''::text AS login_email_source
	FROM visible_events e
	CROSS JOIN LATERAL (VALUES
		('all'::text, ''::text), ('user', COALESCE(e.user_id,'')),
		('profile', COALESCE(e.profile_email,'')), ('login', COALESCE(e.login_email,''))
	) sc(scope_type, scope_value)
`

const (
	sessionOverviewRollupEventFullFilterSQL   = `WHERE e.session_id <> ''`
	sessionOverviewRollupEventScopedFilterSQL = `WHERE e.session_id <> '' AND e.session_id = ANY($1::text[])`
)

const sessionOverviewRollupBetweenSourcesSQL = `
		AND (sc.scope_type = 'all' OR sc.scope_value <> '')
		-- The events arm has to refuse deleted sessions itself. Its sibling below reads
		-- visible_session_records, which anti-joins excluded_sessions; visible_events does
		-- not, so without this a deleted session keeps a rollup row derived from its own
		-- telemetry and walks straight back into the list.
		--
		-- It belongs here rather than in visible_events: that view feeds every cost and
		-- usage rollup in the product, and the anti-join measured 72ms -> 161ms on a
		-- seven-day aggregate there. This statement already scans what it scans.
		AND NOT EXISTS (SELECT 1 FROM excluded_sessions xs WHERE xs.session_id = e.session_id)
	UNION ALL
	SELECT sc.scope_type, sc.scope_value, sr.session_id, sr.profile_email,
		COALESCE(sr.user_id,''), COALESCE(sr.login_email,''), COALESCE(sr.model,''),
		COALESCE(sr.agent,'claude'), sr.ts, 'srec', COALESCE(sr.source_file,''),
		COALESCE(sr.prompt_source,''), COALESCE(sr.entrypoint,''),
		COALESCE(sr.forked_from_session,''), COALESCE(sr.project_hash,''),
		sr.input_tokens, sr.output_tokens, 0::double precision, ''::text,
		COALESCE(sr.cctrace_version,''), ''::text, COALESCE(sr.account_id,''),
		COALESCE(sr.login_email_source,'')
	FROM visible_session_records sr
	CROSS JOIN LATERAL (VALUES
		('all'::text, ''::text), ('user', COALESCE(sr.user_id,'')),
		('profile', COALESCE(sr.profile_email,'')), ('login', COALESCE(sr.login_email,''))
	) sc(scope_type, scope_value)
`

// loginEmailInferredExpr is the session-level roll-up of login_email_source: true
// when any record behind the session was attributed by a guess rather than an
// observation.
//
// It is a shared constant because two separate statements compute it -- the rollup
// table built here and the live union in postgres_session_records.go, which the
// session list falls back to whenever a date filter is set. They must agree on
// which provenance values count as a guess, and nothing at runtime compares them,
// so a value added to one and not the other leaves that read path reporting an
// inferred account as observed.
//
// 'quota-inferred' is in the set for the same reason 'inferred' is: at least one
// link in the evidence chain behind it was itself inferred. Plain 'quota' is not
// -- that account_id to login_email mapping was observed, so it is a measurement
// like 'otel' is (#524).
const loginEmailInferredExpr = `bool_or(s.login_email_source IN ('inferred','quota-inferred'))`

const (
	sessionOverviewRollupRecordFullFilterSQL   = `WHERE sr.session_id <> ''`
	sessionOverviewRollupRecordScopedFilterSQL = `WHERE sr.session_id <> '' AND sr.session_id = ANY($1::text[])`
)

var sessionOverviewRollupInsertSuffixSQL = `
		AND (sc.scope_type = 'all' OR sc.scope_value <> '')
),
acct AS (
	SELECT scope_type, scope_value, session_id,
		CASE WHEN agent = 'codex' THEN account_id ELSE login_email END AS account_key,
		SUM(COALESCE(input_tokens,0) + COALESCE(output_tokens,0)) AS toks,
		MAX(login_email) AS login_email
	FROM session_union
	WHERE COALESCE(NULLIF(CASE WHEN agent = 'codex' THEN account_id ELSE login_email END, ''), '') <> ''
	GROUP BY scope_type, scope_value, session_id, 4
),
top_acct AS (
	SELECT DISTINCT ON (scope_type, scope_value, session_id)
		scope_type, scope_value, session_id, login_email
	FROM acct ORDER BY scope_type, scope_value, session_id, toks DESC, account_key
),
acct_count AS (
	SELECT scope_type, scope_value, session_id, COUNT(*) AS account_count
	FROM acct GROUP BY scope_type, scope_value, session_id
)
INSERT INTO session_overview_rollups (
	scope_type, scope_value, session_id, profile_email, user_id, login_email,
	account_count, model, agent, start_time, end_time, input_tokens, output_tokens,
	cost_usd, event_count, has_sync, has_api_request, has_account, project_hash,
	entrypoint, has_enriched, has_genuine, has_fork, cctrace_version, claude_version,
	login_email_inferred)
SELECT s.scope_type, s.scope_value, s.session_id, MAX(s.profile_email),
	COALESCE(MAX(s.user_id), ''), COALESCE(MAX(ta.login_email), ''),
	COALESCE(MAX(ac.account_count), 0),
	COALESCE(MAX(CASE WHEN s.model <> '' THEN s.model END), ''),
	COALESCE(MAX(s.agent), 'claude'),
	` + sessionStartExpr("s.ts") + `, ` + sessionEndExpr("s.ts") + `,
	COALESCE(NULLIF(SUM(s.input_tokens) FILTER (WHERE s.src = 'otel'), 0),
		SUM(s.input_tokens) FILTER (WHERE s.src = 'srec'), 0),
	COALESCE(NULLIF(SUM(s.output_tokens) FILTER (WHERE s.src = 'otel'), 0),
		SUM(s.output_tokens) FILTER (WHERE s.src = 'srec'), 0),
	COALESCE(SUM(s.cost_usd) FILTER (WHERE s.src = 'otel'), 0), COUNT(*),
	bool_or(s.src = 'srec'), bool_or(s.event_name = 'api_request'),
	-- has_account, which drives OnlyUnattributed. An inferred account counts as an
	-- account here, on purpose: the unattributed list exists so an operator can find
	-- sessions the product cannot place under any identity, and once inference has
	-- placed one there is nothing left for them to do -- exclusion, cost and the
	-- account filter all reach it now, which is the whole point of #346. Keeping it
	-- listed would offer a session no action can change. The guess does not vanish
	-- silently either: login_email_inferred below marks it wherever it is shown.
	bool_or(s.login_email <> ''),
	COALESCE(MAX(s.project_hash) FILTER (WHERE s.project_hash <> ''), ''),
	COALESCE(MAX(s.entrypoint) FILTER (WHERE s.entrypoint <> ''), ''),
	bool_or(s.source_file <> ''),
	bool_or(s.prompt_source IN ('typed','paste','queued','suggestion_accepted')),
	bool_or(s.forked_from_session <> ''),
	COALESCE(MAX(s.cctrace_version) FILTER (WHERE s.cctrace_version <> ''), ''),
	COALESCE(MAX(s.claude_version) FILTER (WHERE s.claude_version <> ''), ''),
	-- Any inferred row makes the session's account inferred; see SessionOverview.
	` + loginEmailInferredExpr + `
FROM session_union s
LEFT JOIN top_acct ta USING (scope_type, scope_value, session_id)
LEFT JOIN acct_count ac USING (scope_type, scope_value, session_id)
GROUP BY s.scope_type, s.scope_value, s.session_id`

func sessionOverviewRollupScope(f SessionOverviewFilter, count bool) (string, string) {
	if f.UserID != "" {
		return "user", f.UserID
	}
	if f.ProfileEmail != "" {
		return "profile", f.ProfileEmail
	}
	if f.LoginEmail != "" && (!count || !f.OnlyUnattributed) {
		return "login", f.LoginEmail
	}
	return "all", ""
}

func appendRollupFilters(f SessionOverviewFilter, includeUnattributed bool, n *int, args *[]interface{}) []string {
	filters := []string{"(r.has_api_request OR r.has_sync)"}
	if f.FoldLineage {
		filters = append(filters, "NOT r.has_fork")
	}
	if includeUnattributed && f.OnlyUnattributed {
		filters = append(filters, "NOT r.has_account")
	}
	switch f.Source {
	case "interactive":
		filters = append(filters, strings.ReplaceAll(sessionInteractiveCol(), "agent", "r.agent"))
	case "headless":
		filters = append(filters, "NOT "+strings.ReplaceAll(sessionInteractiveCol(), "agent", "r.agent"))
	}
	if len(f.ProjectHashes) > 0 {
		*n++
		filters = append(filters, fmt.Sprintf("r.project_hash = ANY($%d)", *n))
		*args = append(*args, f.ProjectHashes)
	}
	if clause := appendAgentFilter(f.Agent, n, args); clause != "" {
		filters = append(filters, "r."+clause)
	}
	return filters
}

func sessionOverviewRollupCountQuery(f SessionOverviewFilter) (string, []interface{}) {
	scopeType, scopeValue := sessionOverviewRollupScope(f, true)
	args := []interface{}{scopeType, scopeValue}
	n := 2
	filters := appendRollupFilters(f, true, &n, &args)
	return `SELECT count(*) FROM session_overview_rollups r WHERE r.scope_type = $1 AND r.scope_value = $2 AND ` + strings.Join(filters, " AND "), args
}

func (s *PgStore) sessionOverviewRollupsReady(ctx context.Context) (bool, error) {
	var ready bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`,
		sessionOverviewRollupBackfill).Scan(&ready); err != nil {
		return false, fmt.Errorf("read session overview rollup marker: %w", err)
	}
	return ready, nil
}

func (s *PgStore) countSessionOverviewRollups(ctx context.Context, f SessionOverviewFilter) (int, error) {
	q, args := sessionOverviewRollupCountQuery(f)
	var count int
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func sessionOverviewRollupListQuery(f SessionOverviewFilter) (string, []interface{}) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	scopeType, scopeValue := sessionOverviewRollupScope(f, false)
	args := []interface{}{scopeType, scopeValue}
	n := 2
	filters := appendRollupFilters(f, false, &n, &args)
	n++
	limitArg := n
	args = append(args, limit)
	n++
	offsetArg := n
	args = append(args, offset)
	q := fmt.Sprintf(`SELECT r.session_id, r.profile_email, r.user_id, r.login_email,
		r.account_count, r.model, r.agent, r.start_time, r.end_time, r.input_tokens,
		r.output_tokens, r.cost_usd, r.event_count, r.has_sync, r.project_hash,
		COALESCE(p.project_name, ''), r.entrypoint, r.has_enriched,
		r.cctrace_version, r.claude_version, r.login_email_inferred
	FROM session_overview_rollups r
	LEFT JOIN projects p ON p.project_hash = r.project_hash
		AND p.agent = COALESCE(NULLIF(r.agent, ''), 'claude')
	WHERE r.scope_type = $1 AND r.scope_value = $2 AND %s
	ORDER BY r.end_time DESC, r.session_id DESC LIMIT $%d OFFSET $%d`,
		strings.Join(filters, " AND "), limitArg, offsetArg)
	return q, args
}

func (s *PgStore) listSessionOverviewRollups(ctx context.Context, f SessionOverviewFilter) ([]*SessionOverview, error) {
	q, args := sessionOverviewRollupListQuery(f)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*SessionOverview, 0)
	for rows.Next() {
		o := &SessionOverview{}
		if err := rows.Scan(&o.SessionID, &o.ProfileEmail, &o.UserID, &o.LoginEmail,
			&o.AccountCount, &o.Model, &o.Agent, &o.StartTime, &o.EndTime,
			&o.InputTokens, &o.OutputTokens, &o.CostUSD, &o.EventCount, &o.HasSync,
			&o.ProjectHash, &o.ProjectName, &o.Entrypoint, &o.HasEnriched,
			&o.CctraceVersion, &o.ClaudeVersion, &o.LoginEmailInferred); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
