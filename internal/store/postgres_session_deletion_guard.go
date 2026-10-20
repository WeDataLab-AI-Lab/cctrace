package store

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

// sessionDeletionTargets is the authoritative inventory of rows whose existence
// means a session tombstone has not been strictly swept. Keep the key expression
// here rather than duplicating table lists in the sweeper and verifier.
//
// # What belongs here
//
// A table belongs here when a row of it can be traced back to one session AND
// that row says something about the person who ran it -- what they typed, what
// they ran, what it cost them, when they were working. That is the contract a
// privacy delete makes: not "the source tables were cleaned" but "nothing derived
// from this session is left".
//
// Derived-ness is not an exemption. plugin_invocation_facts, task_segment_facts,
// the two imputed-cost tables and session_overview_rollups are all recomputed from
// sources that this sweep also empties, so leaving them would be "temporarily
// wrong" only in the sense that nothing would ever recompute them -- their input
// is gone. They carry per-session activity and cost in the meantime, which is the
// thing being deleted.
//
// The planning for #400 nearly left the imputed-cost tables and quota_samples out,
// on the reasoning that each has its own refresh cycle and would clear itself. It
// does not hold: a refresh driven by rows that no longer exist never revisits the
// row it already wrote. The contract won over the reasoning, and the list grew
// from five tables to nine.
//
// # What does not
//
// A table stays out when it holds no per-session row, or when its rows are about
// the deployment rather than the person -- schema_backfills, the various cursors
// and refresh_state markers, excluded_accounts, model_rate_buckets. Deleting a
// person's sessions must not reset a backfill or a rate table.
//
// # Adding one
//
// A new table with a session_id column is a decision, not a default. Ask whether a
// row of it describes the session's author. If yes it goes here, and both the
// sweeper and the verifier pick it up automatically -- they read this list, so
// nothing else needs editing. If no, say why in a comment beside its schema, so
// the next person does not have to re-derive the answer.
//
// The failure mode this guards against is silence: a table added without this
// question gets no entry, the sweep keeps reporting success, and the gap is only
// visible to somebody who compares the schema against this list by hand (#396).
type sessionDeletionTarget struct {
	table string
	key   string
}

var sessionDeletionTargets = []sessionDeletionTarget{
	{table: "otel_events", key: "session_id"},
	{table: "otel_metrics", key: "session_id"},
	{table: "session_records", key: "session_id"},
	{table: "plugin_invocation_facts", key: "session_id"},
	{table: "task_segment_facts", key: "session_id"},
	{table: "claude_imputed_cost", key: "session_id"},
	{table: "codex_imputed_cost", key: "session_id"},
	{table: "quota_samples", key: "source_session_id"},
	{table: "session_overview_rollups", key: "session_id"},
	// Repair staging. Transient -- both cascade away when their repair-state row is
	// deleted -- but while they exist they hold this session's login_email and
	// account_id, and a tombstone marked swept while they do says something untrue.
	// A repair cannot re-stage a swept session afterwards: it reads session_records,
	// which this same sweep empties first.
	//
	// Found by TestEverySessionScopedTableIsJudged on its first run, which is the
	// point of that test (#396).
	{table: "login_email_history_session_intervals", key: "session_id"},
	{table: "codex_login_email_repair_candidates", key: "session_id"},
	// Session observations. Raw rather than derived, and each one says what the
	// person was doing in that session and when: its title and name, whether it was
	// busy or waiting on them, when it ended. Ingest refuses a tombstoned session
	// under the same per-session lock, so a swept session cannot be observed back.
	//
	// collector_heartbeats is not here: it is keyed by identity and names no
	// session, so DeleteUserData is what removes it.
	{table: "session_commit_claim_coverage", key: "session_id"},
	{table: "session_commit_claims", key: "session_id"},
	{table: "session_snapshots", key: "session_id"},
	{table: "session_live_states", key: "session_id"},
	{table: "session_observation_events", key: "session_id"},
	{table: "session_personal_identity_keys", key: "session_id"},
	{table: "session_personal_state", key: "session_id"},
	{table: "session_group_dismissal_identity_keys", key: "session_id"},
	{table: "session_group_dismissals", key: "session_id"},
	// Session analysis (p1b-design §2). Derived, which is no exemption: the facts
	// say what the person asked, when and at what cost; a queue row says when the
	// session last changed; a copy marker says which of its lines another session
	// keeps.
	//
	// Each is swept by session_id, the session the row is about.
	// session_record_copies names a second session, original_session_id, and that
	// is not a sweep key: a marker pointing at a deleted original belongs to the
	// copy's session, and dropping it changes what that session counts, which only
	// a path that also queues the copy's session for recompute may do (p1b-design
	// §6.1).
	{table: "session_analysis_facts", key: "session_id"},
	{table: "session_turn_facts", key: "session_id"},
	{table: "session_activity_segments", key: "session_id"},
	{table: "session_commit_facts", key: "session_id"},
	{table: "session_task_facts", key: "session_id"},
	// P5 records are user-written derivatives of session text; jobs can contain
	// an active claim and therefore must be swept before their record row.
	{table: "session_ai_record_jobs", key: "session_id"},
	{table: "session_ai_records", key: "session_id"},
	// Timeline preparation. Membership names this session's raw rows, provenance
	// fingerprints them, and a revision row says the session existed and changed.
	{table: "session_timeline_members", key: "session_id"},
	{table: "session_timeline_provenance", key: "session_id"},
	{table: "session_trace_leases", key: "session_id"},
	{table: "session_trace_snapshots", key: "session_id"},
	{table: "session_timeline_revisions", key: "session_id"},
	{table: "session_fact_dirty", key: "session_id"},
	{table: "session_record_copies", key: "session_id"},
	// Repair staging. Transient -- both cascade away when their repair-state row is
	// deleted -- but while they exist they hold this session's login_email and
	// account_id, and a tombstone marked swept while they do says something untrue.
	// A repair cannot re-stage a swept session afterwards: it reads session_records,
	// which this same sweep empties first.
	//
	// Found by TestEverySessionScopedTableIsJudged on its first run, which is the
	// point of that test (#396).
	// Repair staging. Transient -- both cascade away when their repair-state row is
	// deleted -- but while they exist they hold this session's login_email and
	// account_id, and a tombstone marked swept while they do says something untrue.
	// A repair cannot re-stage a swept session afterwards: it reads session_records,
	// which this same sweep empties first.
	//
	// Found by TestEverySessionScopedTableIsJudged on its first run, which is the
	// point of that test (#396).
}

// beginStrictDeletion follows the same global -> source ordering as the history
// repair. Taking the source lock exclusively closes discovery races with all
// transactional writers. Any unfinished history snapshot is canceled wholesale:
// user intervals are account-level and cannot be safely cut by session, so the
// only privacy-safe resume is a fresh snapshot that excludes tombstones.
func beginStrictDeletion(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, loginEmailHistoryBackfill); err != nil {
		return fmt.Errorf("lock login_email history cancellation: %w", err)
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM login_email_history_repair_state WHERE name = $1`, loginEmailHistoryBackfill); err != nil {
		return fmt.Errorf("cancel login_email history staging: %w", err)
	}
	return nil
}

func uniqueSortedSessionIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// lockSessionDeletionTargets is shared by deletion, sweep, and every writer that
// can create a session-derived row. Sorted acquisition is required for batches:
// two writers containing the same sessions in opposite order must not deadlock.
func lockSessionDeletionTargets(ctx context.Context, tx pgx.Tx, ids []string) error {
	ids = uniqueSortedSessionIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(session_id, 392))
		FROM unnest($1::text[]) session_id ORDER BY session_id COLLATE "C"`, ids); err != nil {
		return fmt.Errorf("lock session deletion targets: %w", err)
	}
	return nil
}

// Read-only consumers share the deletion barrier, so a trace chunk cannot delay
// basic session metadata. Deletion and derived-row writers retain the exclusive
// barrier above and still wait until every reader has finished.
func lockSessionDeletionReaders(ctx context.Context, tx pgx.Tx, ids []string) error {
	ids = uniqueSortedSessionIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended(session_id, 392))
		FROM unnest($1::text[]) session_id ORDER BY session_id COLLATE "C"`, ids); err != nil {
		return fmt.Errorf("lock session deletion readers: %w", err)
	}
	return nil
}

// liveSessionIDs locks the requested session IDs and returns the subset that has
// no tombstone. The lock makes the check stable through commit: deletion takes the
// same lock before creating a tombstone.
func liveSessionIDs(ctx context.Context, tx pgx.Tx, ids []string) (map[string]bool, error) {
	ids = uniqueSortedSessionIDs(ids)
	if err := lockSessionDeletionTargets(ctx, tx, ids); err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return live, nil
	}
	rows, err := tx.Query(ctx, `SELECT i.session_id
		FROM unnest($1::text[]) i(session_id)
		WHERE NOT EXISTS (SELECT 1 FROM deleted_sessions d WHERE d.session_id = i.session_id)`, ids)
	if err != nil {
		return nil, fmt.Errorf("check deleted sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	return live, rows.Err()
}

func sessionIDsFromEvents(events []*OtelEvent) []string {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		if e != nil {
			ids = append(ids, e.SessionID)
		}
	}
	return ids
}

func sessionIDsFromMetrics(metrics []*OtelMetric) []string {
	ids := make([]string, 0, len(metrics))
	for _, m := range metrics {
		if m != nil {
			ids = append(ids, m.SessionID)
		}
	}
	return ids
}

func filterLiveEvents(events []*OtelEvent, live map[string]bool) []*OtelEvent {
	out := make([]*OtelEvent, 0, len(events))
	for _, e := range events {
		if e != nil && (e.SessionID == "" || live[e.SessionID]) {
			out = append(out, e)
		}
	}
	return out
}

func filterLiveMetrics(metrics []*OtelMetric, live map[string]bool) []*OtelMetric {
	out := make([]*OtelMetric, 0, len(metrics))
	for _, m := range metrics {
		if m != nil && (m.SessionID == "" || live[m.SessionID]) {
			out = append(out, m)
		}
	}
	return out
}

func filterLiveSessionRecords(records []*SessionRecord, live map[string]bool) []*SessionRecord {
	out := make([]*SessionRecord, 0, len(records))
	for _, r := range records {
		if r != nil && (r.SessionID == "" || live[r.SessionID]) {
			out = append(out, r)
		}
	}
	return out
}
