package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"cctrace/internal/sessionfacts"
)

const sessionFactRowLimit = 200000
const sessionFactReadLimit = 60 * time.Second
const sessionFactStatementLimit = `SET LOCAL statement_timeout = '30s'`

type SessionFactRetentionState struct {
	LastRunFinished *time.Time
	OldestSourceTS  *time.Time
}

// SessionFactLoad is a single database snapshot plus the queue version it saw.
// Truncated is an identity/time-only fact; it must not be passed to Compute.
type SessionFactLoad struct {
	Input sessionfacts.Input
	// LoadedKeys is independent evidence of every source row read by the
	// batched worker. A nil slice selects the legacy full-input adapter.
	LoadedKeys           []SessionTimelineKey
	Retention            map[string]SessionFactRetentionState
	Generation           int64
	SourceAgent          string
	RemovalGeneration    int64
	Reasons              int32
	ScanFromID           *int64
	Truncated            *sessionfacts.Output
	CommitClaimsComplete bool
	CopyGroup            *sessionFactCopyGroup
	// TimelineWitness is the session's raw key set in the same snapshot as
	// Input; nil for a truncated fallback, which reads no witness.
	TimelineWitness *SessionTimelineState
}

// LoadSessionFactsInput reads the sources of one computation at one snapshot.
// Only a guard breach or a query timeout is eligible for the fresh fallback;
// malformed data and other SQL errors must be retried rather than hidden.
func (s *PgStore) LoadSessionFactsInput(ctx context.Context, sessionID, agent string) (SessionFactLoad, error) {
	return s.loadSessionFactsInput(ctx, sessionID, agent, sessionFactRowLimit)
}

func (s *PgStore) loadSessionFactsInput(parent context.Context, sessionID, agent string, rowLimit int64) (SessionFactLoad, error) {
	return s.loadSessionFactsInputWithBudgets(parent, sessionID, agent, rowLimit, sessionFactReadLimit, 30*time.Second)
}

type sessionFactReadOptions struct {
	fullBudget, fallbackBudget          time.Duration
	statementSQL, countSQL, fallbackSQL string
}

func (s *PgStore) loadSessionFactsInputWithBudgets(parent context.Context, sessionID, agent string, rowLimit int64, fullBudget, fallbackBudget time.Duration) (SessionFactLoad, error) {
	return s.loadSessionFactsInputWithOptions(parent, sessionID, agent, rowLimit, sessionFactReadOptions{
		fullBudget: fullBudget, fallbackBudget: fallbackBudget,
		statementSQL: sessionFactStatementLimit,
		countSQL:     `SELECT count(*) FROM session_records WHERE session_id = $1`,
		fallbackSQL:  sessionFactFallbackSQL,
	})
}

func (s *PgStore) loadSessionFactsInputWithOptions(parent context.Context, sessionID, agent string, rowLimit int64, options sessionFactReadOptions) (SessionFactLoad, error) {
	ctx, cancel := context.WithTimeout(parent, options.fullBudget)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err == nil {
		if _, err = tx.Exec(ctx, options.statementSQL); err == nil {
			var n int64
			err = tx.QueryRow(ctx, options.countSQL, sessionID).Scan(&n)
			if err == nil && n <= rowLimit {
				var load SessionFactLoad
				load.Input.Agent = agent
				err = loadSessionFactSources(ctx, tx, sessionID, &load)
				if err == nil {
					err = tx.Commit(ctx)
				}
				if err == nil {
					return load, nil
				}
			} else if err == nil {
				err = errSessionFactGuard
			}
		}
		_ = tx.Rollback(context.Background())
	}
	if !sessionFactFallbackEligible(err, ctx.Err()) {
		return SessionFactLoad{}, fmt.Errorf("load session facts: %w", err)
	}
	if parent.Err() != nil {
		return SessionFactLoad{}, fmt.Errorf("session fact read canceled: %w", parent.Err())
	}
	fallbackCtx, fallbackCancel := context.WithTimeout(parent, options.fallbackBudget)
	defer fallbackCancel()
	fallback, fallbackErr := s.loadSessionFactFallbackWithSQL(fallbackCtx, sessionID, agent, options.fallbackSQL)
	if fallbackErr != nil {
		return SessionFactLoad{}, fmt.Errorf("session fact fallback: %w", fallbackErr)
	}
	return fallback, nil
}

var errSessionFactGuard = errors.New("session fact source row cap exceeded")

func sessionFactFallbackEligible(err, readErr error) bool {
	return errors.Is(err, errSessionFactGuard) || sessionFactTimeout(err) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(readErr, context.DeadlineExceeded)
}

func sessionFactTimeout(err error) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "57014"
}

func loadSessionFactRetention(ctx context.Context, tx pgx.Tx, load *SessionFactLoad) error {
	rows, err := tx.Query(ctx, `SELECT table_name, last_run_finished, oldest_source_ts
 FROM session_overview_retention_state WHERE table_name IN ('session_records','otel_events')`)
	if err != nil {
		return fmt.Errorf("retention state: %w", err)
	}
	defer rows.Close()
	load.Retention = make(map[string]SessionFactRetentionState)
	for rows.Next() {
		var name string
		var state SessionFactRetentionState
		if err := rows.Scan(&name, &state.LastRunFinished, &state.OldestSourceTS); err != nil {
			return fmt.Errorf("scan retention state: %w", err)
		}
		load.Retention[name] = state
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("retention state: %w", err)
	}
	return nil
}

func loadSessionFactSources(ctx context.Context, tx pgx.Tx, id string, load *SessionFactLoad) error {
	in := &load.Input
	if err := loadSessionFactRetention(ctx, tx, load); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&in.ReadAt); err != nil {
		return fmt.Errorf("snapshot time: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT generation, removal_generation, reasons, scan_from_id FROM session_fact_dirty WHERE session_id = $1`, id).
		Scan(&load.Generation, &load.RemovalGeneration, &load.Reasons, &load.ScanFromID); err != nil {
		return fmt.Errorf("dirty generation: %w", err)
	}
	if err := tx.QueryRow(ctx, sessionFactAgentSQL, id).Scan(&load.SourceAgent); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("source agent: %w", err)
	}
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name = 'session_fact_copies_v1'),
 EXISTS(SELECT 1 FROM otel_events WHERE session_id = $1),
 (SELECT min(ts) FROM otel_events)`, id).Scan(&in.CopyGate, &in.HasOTEL, &oldest); err != nil {
		return fmt.Errorf("source gates: %w", err)
	}
	if oldest != nil {
		in.OTELOldest = *oldest
	}
	if err := loadSessionFactClaude(ctx, tx, id, in); err != nil {
		return err
	}
	if in.Agent == "claude" {
		if err := loadSessionFactCommitResults(ctx, tx, id, in); err != nil {
			return fmt.Errorf("tool result text: %w", err)
		}
	}
	if err := loadSessionFactCodex(ctx, tx, id, in); err != nil {
		return err
	}
	if in.Agent == "codex" {
		if err := loadSessionFactCodexToolResults(ctx, tx, id, in); err != nil {
			return fmt.Errorf("codex tool results: %w", err)
		}
	}
	if err := loadSessionFactHeads(ctx, tx, id, in); err != nil {
		return err
	}
	if err := loadSessionFactOTEL(ctx, tx, id, in); err != nil {
		return err
	}
	if err := loadSessionFactSnapshots(ctx, tx, id, in); err != nil {
		return err
	}
	if err := loadSessionFactCommitClaims(ctx, tx, id, load); err != nil {
		return fmt.Errorf("commit claim checks: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT srec_id FROM session_record_copies WHERE session_id = $1 ORDER BY srec_id`, id)
	if err != nil {
		return fmt.Errorf("copy marks: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("copy marks: %w", err)
	}
	in.CopiedIDs = ids
	load.CopyGroup, err = loadSessionFactCopyGroup(ctx, tx, id, load.ScanFromID)
	if err != nil {
		return fmt.Errorf("copy classification source: %w", err)
	}
	if load.CopyGroup.Skipped {
		in.CopyState, in.CopySource = "skipped_large_group", "session_records"
	} else {
		in.CopyState, in.CopySource = "observed", "session_records"
	}
	witness, err := readSessionTimelineState(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("timeline witness: %w", err)
	}
	load.TimelineWitness = &witness
	in.TranscriptIO, err = loadSessionTranscriptIO(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("transcript measurement: %w", err)
	}
	return nil
}

func loadSessionFactCommitClaims(ctx context.Context, tx pgx.Tx, id string, load *SessionFactLoad) error {
	complete, claims, err := sessionFactCommitClaimChecks(ctx, tx, id, load.Input.Snapshots)
	load.CommitClaimsComplete = complete
	load.Input.CommitClaims = claims
	return err
}

func sessionFactCommitClaimChecks(ctx context.Context, tx pgx.Tx, id string, snapshots []sessionfacts.Snapshot) (bool, []sessionfacts.CommitClaim, error) {
	complete, err := sessionCommitClaimsComplete(ctx, tx)
	if err != nil || !complete {
		return complete, nil, err
	}
	type key struct{ repository, hash string }
	keys := make(map[key]bool)
	var claims []sessionfacts.CommitClaim
	for _, snapshot := range snapshots {
		if snapshot.Commits == nil {
			continue
		}
		for _, item := range snapshot.Commits.Items {
			if item.Tree < 0 || item.Tree >= len(snapshot.Commits.Trees) || !sessionfacts.ValidSessionCommitHash(item.Hash) {
				continue
			}
			repository := snapshot.Commits.Trees[item.Tree].Repository
			if repository != "" {
				keys[key{repository: repository, hash: item.Hash}] = true
			}
		}
	}
	for claim := range keys {
		var conflicting bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_commit_claims c
			WHERE c.repository=$1 AND c.commit_hash=$2 AND c.session_id<>$3
			AND NOT EXISTS(SELECT 1 FROM deleted_sessions d WHERE d.session_id=c.session_id))`,
			claim.repository, claim.hash, id).Scan(&conflicting); err != nil {
			return false, nil, err
		}
		claims = append(claims, sessionfacts.CommitClaim{Repository: claim.repository, Hash: claim.hash, Conflicting: conflicting})
	}
	return true, claims, nil
}

func loadSessionFactClaude(ctx context.Context, tx pgx.Tx, id string, in *sessionfacts.Input) error {
	rows, err := tx.Query(ctx, sessionFactClaudeSlimSQL, id)
	if err != nil {
		return fmt.Errorf("claude slim: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanSessionFactClaudeRow(rows)
		if err != nil {
			return err
		}
		in.Rows = append(in.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("claude slim: %w", err)
	}
	return nil
}

func textOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func loadSessionFactCodex(ctx context.Context, tx pgx.Tx, id string, in *sessionfacts.Input) error {
	rows, err := tx.Query(ctx, sessionFactCodexSlimSQL, id)
	if err != nil {
		return fmt.Errorf("codex slim: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanSessionFactCodexRow(rows)
		if err != nil {
			return err
		}
		in.CodexRows = append(in.CodexRows, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("codex slim: %w", err)
	}
	return nil
}

func loadSessionFactHeads(ctx context.Context, tx pgx.Tx, id string, in *sessionfacts.Input) error {
	query := sessionFactClaudeHeadsSQL
	if in.Agent == "codex" {
		query = sessionFactCodexHeadsSQL
	}
	rows, err := tx.Query(ctx, query, id)
	if err != nil {
		return fmt.Errorf("request heads: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h sessionfacts.Head
		if err = rows.Scan(&h.ID, &h.UUID, &h.TS, &h.IsSidechain, &h.AgentID, &h.SourceFile, &h.Model,
			&h.InputTokens, &h.OutputTokens, &h.CacheReadTokens, &h.CacheCreateTokens,
			&h.Imputed, &h.CostUSD, &h.HasRate); err != nil {
			return fmt.Errorf("scan request heads: %w", err)
		}
		in.Heads = append(in.Heads, h)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("request heads: %w", err)
	}
	return nil
}

func loadSessionFactOTEL(ctx context.Context, tx pgx.Tx, id string, in *sessionfacts.Input) error {
	rows, err := tx.Query(ctx, sessionFactVisibleOTELSQL, id)
	if err != nil {
		return fmt.Errorf("visible OTEL: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var o sessionfacts.OTELRow
		if err = rows.Scan(&o.TS, &o.PromptID, &o.CostUSD, &o.UserID, &o.ProfileEmail, &o.LoginEmail, &o.BillingProvider); err != nil {
			return fmt.Errorf("scan OTEL: %w", err)
		}
		in.OTEL = append(in.OTEL, o)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("visible OTEL: %w", err)
	}
	return nil
}

func loadSessionFactSnapshots(ctx context.Context, tx pgx.Tx, id string, in *sessionfacts.Input) error {
	rows, err := tx.Query(ctx, sessionFactSnapshotSQL, id)
	if err != nil {
		return fmt.Errorf("snapshots: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s sessionfacts.Snapshot
		var data []byte
		if err = rows.Scan(&s.UserID, &s.ProfileEmail, &s.LoginEmail, &s.BillingProvider, &s.AccountID, &s.ObservedAt, &data); err != nil {
			return fmt.Errorf("scan snapshot: %w", err)
		}
		var document struct {
			AITitle     *string `json:"ai_title"`
			CustomTitle *string `json:"custom_title"`
			AgentName   *string `json:"agent_name"`
			CostStates  []struct {
				StartTimeMs         int64    `json:"start_time_ms"`
				TotalCostUSD        float64  `json:"total_cost_usd"`
				LinesAdded          int64    `json:"lines_added"`
				LinesRemoved        int64    `json:"lines_removed"`
				Models              []string `json:"models"`
				HasUnknownModelCost bool     `json:"has_unknown_model_cost"`
			} `json:"cost_states"`
			PRLinks []struct {
				URL         string    `json:"url"`
				Number      int64     `json:"number"`
				Repository  string    `json:"repository"`
				FirstSeenAt time.Time `json:"first_seen_at"`
			} `json:"pr_links"`
			Commits    json.RawMessage `json:"commits"`
			CodexTurns []struct {
				TurnID          string    `json:"turn_id"`
				RootTurnID      string    `json:"root_turn_id"`
				StartedAt       time.Time `json:"started_at"`
				StartedAtSource string    `json:"started_at_source"`
				EndedAt         time.Time `json:"ended_at"`
				TimestampsExact bool      `json:"timestamps_exact"`
				End             string    `json:"end"`
				ContextWindow   *int      `json:"model_context_window"`
			} `json:"codex_turns"`
			ContinuedIn []string `json:"continued_in"`
			Truncated   []string `json:"truncated"`
		}
		if err = json.Unmarshal(data, &document); err != nil {
			return fmt.Errorf("decode snapshot: %w", err)
		}
		s.AITitle, s.CustomTitle, s.AgentName = document.AITitle, document.CustomTitle, document.AgentName
		s.ContinuedIn, s.Truncated = document.ContinuedIn, document.Truncated
		if len(document.Commits) > 0 {
			var commits sessionfacts.SnapshotCommits
			if string(document.Commits) == "null" || json.Unmarshal(document.Commits, &commits) != nil {
				s.Commits = &sessionfacts.SnapshotCommits{Malformed: true}
			} else {
				s.Commits = &commits
			}
		}
		for _, c := range document.CostStates {
			s.CostStates = append(s.CostStates, sessionfacts.SnapshotCostState{StartTimeMs: c.StartTimeMs, TotalCostUSD: c.TotalCostUSD, LinesAdded: c.LinesAdded, LinesRemoved: c.LinesRemoved, Models: c.Models, HasUnknownModelCost: c.HasUnknownModelCost})
		}
		for _, p := range document.PRLinks {
			s.PRLinks = append(s.PRLinks, sessionfacts.SnapshotPRLink{URL: p.URL, Number: p.Number, Repository: p.Repository, FirstSeenAt: p.FirstSeenAt})
		}
		for _, t := range document.CodexTurns {
			startedSource, endedSource := t.StartedAtSource, ""
			if t.TimestampsExact {
				startedSource, endedSource = "line", "line"
			}
			s.CodexTurns = append(s.CodexTurns, sessionfacts.SnapshotCodexTurn{TurnID: t.TurnID, RootTurnID: t.RootTurnID,
				StartedAt: t.StartedAt, StartedAtSource: startedSource, EndedAt: t.EndedAt, EndedAtSource: endedSource,
				TimestampsExact: t.TimestampsExact, End: t.End, ContextWindow: t.ContextWindow})
		}
		in.Snapshots = append(in.Snapshots, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("snapshots: %w", err)
	}
	return nil
}

// The fallback aggregates only visibility-qualified contributors, without loading
// a potentially huge transcript or inventing turns, signals or copy judgements.
var sessionFactFallbackSQL = `WITH codex_prefix AS (
 SELECT max(sr.ts) AS ended FROM visible_session_records sr
 JOIN session_record_copies c ON c.srec_id = sr.id AND c.session_id = sr.session_id
 WHERE sr.session_id = $1 AND sr.agent = 'codex' AND sr.record_type = 'usage'
 AND EXISTS (SELECT 1 FROM schema_backfills WHERE name = 'session_fact_copies_v1')
), contributors AS (
 SELECT sr.user_id, sr.profile_email, sr.login_email, sr.billing_provider, sr.account_id,
 sr.ts, 'record'::text AS kind, sr.record_type, sr.raw->>'subtype' AS subtype,
 (NOT (EXISTS (SELECT 1 FROM schema_backfills WHERE name = 'session_fact_copies_v1')
 AND EXISTS (SELECT 1 FROM session_record_copies c WHERE c.srec_id = sr.id))
 AND NOT (COALESCE(sr.agent, '') = 'codex' AND COALESCE(sr.ts <= (SELECT ended FROM codex_prefix), false))) AS active
 FROM visible_session_records sr WHERE sr.session_id = $1
 UNION ALL
 SELECT e.user_id, e.profile_email, e.login_email, e.billing_provider, ''::text,
 e.ts, 'otel'::text, ''::text, ''::text, true
 FROM visible_events e WHERE e.session_id = $1 AND e.event_name = 'api_request'
 AND NOT EXISTS (SELECT 1 FROM excluded_sessions x WHERE x.session_id = e.session_id)
 UNION ALL
 SELECT ss.user_id, ss.profile_email, ss.login_email, ss.billing_provider, ss.account_id,
 ss.observed_at, 'snapshot'::text, ''::text, ''::text, false
 FROM session_snapshots ss WHERE ` + sessionFactSnapshotVisibleSQL + `
)
SELECT COALESCE(array_agg(DISTINCT user_id), '{}'), COALESCE(array_agg(DISTINCT profile_email), '{}'),
 COALESCE(array_agg(DISTINCT login_email), '{}'),
 min(ts) FILTER (WHERE kind = 'record' AND ts > 'epoch'),
 min(ts) FILTER (WHERE kind <> 'snapshot' AND ts > 'epoch' AND active
  AND NOT (record_type = 'system' AND COALESCE(subtype, '') = 'away_summary')),
 max(ts) FILTER (WHERE kind <> 'snapshot' AND ts > 'epoch' AND active
  AND NOT (record_type = 'system' AND COALESCE(subtype, '') = 'away_summary')),
 COALESCE((SELECT jsonb_agg(DISTINCT jsonb_build_object(
  'login_email', login_email, 'billing_provider', billing_provider, 'account_id', account_id))
  FROM contributors), '[]'::jsonb),
 COALESCE(bool_or(kind = 'record'), false)
 FROM contributors`

func (s *PgStore) loadSessionFactFallbackWithSQL(ctx context.Context, id, agent, query string) (SessionFactLoad, error) {
	var load SessionFactLoad
	load.Input.Agent = agent
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return load, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, sessionFactStatementLimit); err != nil {
		return load, err
	}
	if err = tx.QueryRow(ctx, `SELECT generation, removal_generation, reasons, scan_from_id FROM session_fact_dirty WHERE session_id = $1`, id).Scan(&load.Generation, &load.RemovalGeneration, &load.Reasons, &load.ScanFromID); err != nil {
		return load, err
	}
	if err = tx.QueryRow(ctx, sessionFactAgentSQL, id).Scan(&load.SourceAgent); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return load, fmt.Errorf("fallback source agent: %w", err)
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name = 'session_fact_copies_v1')`).Scan(&load.Input.CopyGate); err != nil {
		return load, fmt.Errorf("fallback copy gate: %w", err)
	}
	// Publishing still validates the backfill marker for a truncated result.
	// Read it in this snapshot even though commit details are omitted, otherwise
	// a completed backfill makes every fallback look stale and retry forever.
	if load.CommitClaimsComplete, err = sessionCommitClaimsComplete(ctx, tx); err != nil {
		return load, fmt.Errorf("fallback commit claims gate: %w", err)
	}
	load.Input.CopyState, load.Input.CopySource = "undeterminable", "truncated_session_fallback"
	if err = loadSessionFactRetention(ctx, tx, &load); err != nil {
		return load, err
	}
	out := sessionfacts.Output{Session: sessionfacts.Session{Agent: agent, Coverage: "truncated", RulesVersion: sessionfacts.RulesVersion,
		Detail: sessionfacts.SessionDetail{Copy: &sessionfacts.CopyStateDetail{State: "undeterminable", Source: "truncated_session_fallback"}}}}
	var users, profiles, logins []string
	var accountJSON []byte
	if err = tx.QueryRow(ctx, query, id).Scan(&users, &profiles, &logins,
		&out.Session.FirstRecordAt, &out.Session.FirstActivityAt, &out.Session.LastActivityAt,
		&accountJSON, &out.Session.Transcript); err != nil {
		return load, err
	}
	sort.Strings(users)
	sort.Strings(profiles)
	sort.Strings(logins)
	out.Session.UserIDs, out.Session.ProfileEmails, out.Session.LoginEmails = users, profiles, logins
	if err := json.Unmarshal(accountJSON, &out.Session.AccountKeys); err != nil {
		return load, fmt.Errorf("decode fallback account keys: %w", err)
	}
	sort.Slice(out.Session.AccountKeys, func(i, j int) bool {
		a, b := out.Session.AccountKeys[i], out.Session.AccountKeys[j]
		if a.LoginEmail != b.LoginEmail {
			return a.LoginEmail < b.LoginEmail
		}
		if a.BillingProvider != b.BillingProvider {
			return a.BillingProvider < b.BillingProvider
		}
		return a.AccountID < b.AccountID
	})
	if len(users) == 1 && users[0] != "" {
		out.Session.OwnerUserID = &users[0]
	}
	if len(profiles) == 1 && profiles[0] != "" {
		out.Session.OwnerProfileEmail = &profiles[0]
	}
	out.Hash = sessionfacts.HashOutput(out)
	if err = tx.Commit(ctx); err != nil {
		return load, err
	}
	load.Truncated = &out
	return load, nil
}
