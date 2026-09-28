package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// v2 rebuilds every fact once so rows written before billing_provider and
// account_id existed carry them (#717). The lock keeps the v1 key: it names the
// table's maintenance, not a backfill generation.
const (
	pluginInvocationFactsBackfill = "plugin_invocation_facts_v2"
	pluginInvocationFactsLockKey  = "plugin_invocation_facts_v1"
)

func lockPluginInvocationFacts(ctx context.Context, tx pgx.Tx, exclusive bool) error {
	q := `SELECT pg_advisory_xact_lock_shared(hashtext($1))`
	if exclusive {
		q = `SELECT pg_advisory_xact_lock(hashtext($1))`
	}
	if _, err := tx.Exec(ctx, q, pluginInvocationFactsLockKey); err != nil {
		return fmt.Errorf("lock plugin invocation facts: %w", err)
	}
	return nil
}

// refreshPluginInvocationFacts replaces all command facts for touched sessions.
// Recomputing the session, rather than appending the new records, is required because
// sync may deliver an earlier response or the next user boundary after a fact exists.
// Empty input, including nil, is a no-op. Full rebuilds use a separate entry point.
func refreshPluginInvocationFacts(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	if err := lockPluginInvocationFacts(ctx, tx, false); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(session_id, 947))
		FROM unnest($1::text[]) session_id ORDER BY session_id`, sessionIDs); err != nil {
		return fmt.Errorf("lock plugin fact sessions: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_invocation_facts
		WHERE session_id = ANY($1::text[])`, sessionIDs); err != nil {
		return fmt.Errorf("delete plugin invocation facts: %w", err)
	}
	if _, err := tx.Exec(ctx, pluginInvocationFactsInsertSQL, sessionIDs); err != nil {
		return fmt.Errorf("insert plugin invocation facts: %w", err)
	}
	return nil
}

// rebuildAllPluginInvocationFacts replaces every fact under the exclusive maintenance lock.
// Keep maintenance before per-session and relation locks, as in scoped refreshes.
func rebuildAllPluginInvocationFacts(ctx context.Context, tx pgx.Tx) error {
	if err := lockPluginInvocationFacts(ctx, tx, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_invocation_facts`); err != nil {
		return fmt.Errorf("delete plugin invocation facts: %w", err)
	}
	if _, err := tx.Exec(ctx, pluginInvocationFactsInsertSQL, nil); err != nil {
		return fmt.Errorf("insert plugin invocation facts: %w", err)
	}
	return nil
}

// BackfillPluginInvocationFacts performs the initial unbounded response-window scan.
// Its marker commits with the facts, making retries crash-safe and exactly once.
// One transaction, not session-cursor batches: the v2 rebuild measured 7.3 s cold /
// 4.8 s warm for DELETE+INSERT on the dev prod-copy (5.7M session records, 5,188
// facts) and 8.0 s for the read side on prod itself (7.2M records, 5,962 facts,
// read-only), 2026-09-21. Cost is the session_records scan, so it grows with the
// table; revisit batching once it nears the 10 s sync clients absorb by retrying.
func (s *PgStore) BackfillPluginInvocationFacts(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return err
	}
	if err := lockPluginInvocationFacts(ctx, tx, true); err != nil {
		return err
	}
	var done bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM schema_backfills WHERE name = $1)`, pluginInvocationFactsBackfill).Scan(&done); err != nil {
		return err
	}
	if done {
		return tx.Commit(ctx)
	}
	if err := rebuildAllPluginInvocationFacts(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, pluginInvocationFactsBackfill); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) pluginInvocationFactsReady(ctx context.Context) (bool, error) {
	var ready bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM schema_backfills WHERE name = $1)`, pluginInvocationFactsBackfill).Scan(&ready); err != nil {
		return false, fmt.Errorf("read plugin invocation fact marker: %w", err)
	}
	return ready, nil
}

// ReconcilePluginInvocationFactsForRetention removes facts whose command source was
// dropped by Timescale retention. Retention drops the oldest time chunks, so facts
// older than the surviving source minimum are a bounded candidate set; touched
// sessions are recomputed in case newer commands remain.
func (s *PgStore) ReconcilePluginInvocationFactsForRetention(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `SELECT min(ts) FROM session_records`).Scan(&oldest); err != nil {
		return 0, err
	}
	q := `SELECT DISTINCT session_id FROM plugin_invocation_facts`
	var args []interface{}
	if oldest != nil {
		q += ` WHERE command_ts < $1`
		args = append(args, *oldest)
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	var sessionIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		sessionIDs = append(sessionIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(sessionIDs) == 0 {
		return 0, tx.Commit(ctx)
	}
	if err := refreshPluginInvocationFacts(ctx, tx, sessionIDs); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(sessionIDs), nil
}

// MATERIALIZED is load-bearing, not decoration. Postgres inlines a CTE referenced
// once, and inlining pushes next_user_ts into the LEFT JOIN below as a SubPlan in the
// join filter -- so the scalar subquery runs per candidate row PAIR instead of once
// per command. Measured on the dev snapshot (4,700 commands, 3.7M session records):
// 12+ minutes and still running inlined, 2.9 seconds materialized. It matters because
// the backfill holds the exclusive advisory lock the ingest path waits on as a shared
// lock, so however long this takes is how long session record writes stall.
const pluginInvocationFactsInsertSQL = `
WITH commands AS MATERIALIZED (
	SELECT sr.id, sr.session_id, sr.ts, sr.record_type, sr.command_name,
		COALESCE(sr.command_source, '') AS command_source,
		sr.profile_email, COALESCE(sr.login_email, '') AS login_email,
		COALESCE(sr.user_id, '') AS user_id,
		COALESCE(sr.billing_provider, '') AS billing_provider,
		COALESCE(sr.account_id, '') AS account_id,
		COALESCE(NULLIF(sr.agent, ''), 'claude') AS agent,
		COALESCE(sr.project_hash, '') AS project_hash,
		COALESCE(sr.repository_id, '') AS repository_id,
		COALESCE(sr.repository_name, '') AS repository_name,
		COALESCE(sr.repo_subpath, '') AS repo_subpath,
		COALESCE(sr.commit_sha, '') AS commit_sha,
		COALESCE(sr.branch, '') AS branch,
		(SELECT MIN(u.ts) FROM session_records u
		 WHERE u.session_id = sr.session_id
		   AND u.record_type = 'user'
		   AND (COALESCE(NULLIF(sr.agent, ''), 'claude') = 'codex'
		     OR jsonb_typeof(u.raw->'message'->'content') = 'string'
		     OR (jsonb_typeof(u.raw->'message'->'content') = 'array' AND EXISTS (
		           SELECT 1 FROM jsonb_array_elements(u.raw->'message'->'content') e
		           WHERE e->>'type' IN ('text', 'input_text')
		         )))
		   AND u.ts > sr.ts) AS next_user_ts
	FROM session_records sr
	-- Two source values can ever be plugin usage, so anything else is not a fact
	-- worth storing. Which of these two ACTUALLY counts is decided at read time --
	-- see pluginUsageQuery. Deciding it here would freeze one release's builtin list
	-- into stored rows: this WHERE was copied from postgres_plugin_usage.go as it
	-- stood at v0.7.22, and #324 replaced that very line the next day, so the facts
	-- carried a rule that had already been superseded.
	WHERE sr.command_name <> ''
	  AND NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = sr.session_id)
	  AND (sr.command_source = '' OR sr.command_source = 'plugin')
	  AND ($1::text[] IS NULL OR sr.session_id = ANY($1))
)
INSERT INTO plugin_invocation_facts (
	source_record_id, session_id, command_ts, command_name, command_source,
	profile_email, login_email, user_id, agent, project_hash, repository_id,
	repository_name, repo_subpath, commit_sha, branch, input_tokens, output_tokens,
	billing_provider, account_id)
SELECT c.id, c.session_id, c.ts, c.command_name, c.command_source, c.profile_email,
	c.login_email, c.user_id, c.agent, c.project_hash, c.repository_id,
	c.repository_name, c.repo_subpath, c.commit_sha, c.branch,
	COALESCE(SUM(CASE WHEN c.agent = 'codex'
		THEN GREATEST(COALESCE(r.input_tokens, 0) - COALESCE(r.cache_read_tokens, 0), 0)
		ELSE COALESCE(r.input_tokens, 0) END), 0),
	COALESCE(SUM(COALESCE(r.output_tokens, 0)), 0),
	c.billing_provider, c.account_id
FROM commands c
LEFT JOIN session_records r ON r.session_id = c.session_id
	AND COALESCE(NULLIF(r.agent, ''), 'claude') = c.agent
	AND ((c.agent = 'codex' AND r.record_type = 'usage')
	  OR (c.agent <> 'codex' AND r.record_type = 'assistant'))
	AND ((c.record_type = 'assistant' AND r.ts >= c.ts)
	  OR (c.record_type <> 'assistant' AND r.ts > c.ts))
	AND (c.next_user_ts IS NULL OR r.ts < c.next_user_ts)
GROUP BY c.id, c.session_id, c.ts, c.command_name, c.command_source, c.profile_email,
	c.login_email, c.user_id, c.agent, c.project_hash, c.repository_id,
	c.repository_name, c.repo_subpath, c.commit_sha, c.branch,
	c.billing_provider, c.account_id`
