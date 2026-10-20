package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *PgStore) SessionList(ctx context.Context, profileEmail string, limit, offset int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT DISTINCT session_id FROM visible_events
		WHERE session_id != '' AND ($1 = '' OR profile_email = $1)
		ORDER BY session_id DESC LIMIT $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, q, profileEmail, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]string, 0)
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		result = append(result, sid)
	}
	return result, rows.Err()
}

// sessionFactsOfIdentitySQL is DeleteUserData's lookup of the sessions whose
// session analysis facts name an identity, %s the array column the identity is
// kept in. Containment, not $1 = ANY(...): it is the form the GIN index on
// user_ids serves (TestDeleteUserDataFactsLookupUsesTheUserIDsIndex).
const sessionFactsOfIdentitySQL = `SELECT session_id FROM session_analysis_facts WHERE %s @> ARRAY[$1::text]`

// deleteQuotaOwnerBindingsForIdentity removes personal rate-limit evidence when
// the telemetry identity it was associated with is deleted or merged away. The
// binding is owned by the dashboard account, so it is removed only when that
// account's email/user_id matches the identity being changed; it is never
// transferred to the merge target.
func deleteQuotaOwnerBindingsForIdentity(ctx context.Context, tx pgx.Tx, column, value, preserveEmail string) error {
	if value == "" {
		return nil
	}
	var identityWhere string
	switch column {
	case "profile_email":
		identityWhere = `(
			lower(u.email)=lower($1) OR EXISTS (
				SELECT 1 FROM user_aliases a
				WHERE lower(a.from_profile_email)=lower($1) AND lower(a.to_profile_email)=lower(u.email)
			)
		)`
	case "user_id":
		identityWhere = `u.cctrace_user_id=$1`
	default:
		return fmt.Errorf("unsupported quota owner identity column %q", column)
	}
	_, err := tx.Exec(ctx, `DELETE FROM quota_account_bindings b USING dashboard_users u
		WHERE b.dashboard_user_id=u.id AND `+identityWhere+`
		AND ($2='' OR lower(u.email)<>lower($2))`, value, preserveEmail)
	return err
}

func (s *PgStore) DeleteUserData(ctx context.Context, profileEmail string, userID string) error {
	if profileEmail == "" && userID == "" {
		return fmt.Errorf("profileEmail or userID required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	column, value, identities := "user_id", userID, "user_ids"
	ownerColumn := "owner_user_id"
	if profileEmail != "" {
		column, value, identities = "profile_email", profileEmail, "profile_emails"
		ownerColumn = "owner_profile_email"
	}
	if err := deleteQuotaOwnerBindingsForIdentity(ctx, tx, column, value, ""); err != nil {
		return fmt.Errorf("delete quota owner data: %w", err)
	}
	// The observation tables are read too: a session can have been observed before
	// any of its records synced, and such a session is still this user's.
	//
	// So are the session analysis facts, by the identity arrays they were computed
	// with: they outlast the rows that named this user until they are computed
	// again -- an OTEL row past its retention only queues them -- and what they say
	// came from those rows. The user_ids lookup is served by its GIN index;
	// profile_emails has none, and the facts table, a row per session, is scanned
	// under the lock above.
	affected, err := querySessionIDs(ctx, tx, fmt.Sprintf(`
		SELECT session_id FROM otel_events WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_records WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM claude_imputed_cost WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM codex_imputed_cost WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_snapshots WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_live_states WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_observation_events WHERE %s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_personal_identity_keys WHERE source_%s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_group_dismissal_identity_keys WHERE source_%s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_ai_records WHERE %s = $1 AND session_id <> ''
		UNION `+sessionFactsOfIdentitySQL,
		column, column, column, column, column, column, column, column, column, ownerColumn, identities), value)
	if err != nil {
		return err
	}
	claimPeers, err := prepareSessionClaimMutation(ctx, tx, affected)
	if err != nil {
		return err
	}
	if err := lockSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	// Delete by the immutable verified identity key, not by the mutable source
	// columns. A personal-only session remains discoverable after raw retention.
	if _, err := tx.Exec(ctx, `DELETE FROM session_personal_state p
		USING session_personal_identity_keys k
		WHERE p.dashboard_user_id=k.dashboard_user_id AND p.session_id=k.session_id
		AND k.source_`+column+`=$1`, value); err != nil {
		return fmt.Errorf("delete personal state: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_group_dismissals d
		USING session_group_dismissal_identity_keys k
		WHERE d.dashboard_user_id=k.dashboard_user_id AND d.session_id=k.session_id
		AND d.grouping_version=k.grouping_version AND d.ended_at=k.ended_at
		AND k.source_`+column+`=$1`, value); err != nil {
		return fmt.Errorf("delete session group dismissals: %w", err)
	}
	// The deletion contract of session analysis (p1b-design §6.1), steps 1 and 2:
	// the copy markers naming these sessions, with the sessions on their other
	// side kept to queue at the end.
	neighbours, err := takeCopyMarkers(ctx, tx, affected)
	if err != nil {
		return err
	}
	if err := lockPluginInvocationFacts(ctx, tx, false); err != nil {
		return err
	}
	if err := lockTaskSegmentFacts(ctx, tx, false); err != nil {
		return err
	}
	for _, table := range userDeleteTables {
		q := fmt.Sprintf("DELETE FROM %s WHERE %s = $1", table, column)
		if _, err := tx.Exec(ctx, q, value); err != nil {
			return fmt.Errorf("delete from %s: %w", table, err)
		}
	}
	// These two are written with the address as it arrived, with no normalizing
	// -- a dashboard account's sign-in address, a client's reported one -- so an
	// exact match leaves the rows stored in another case.
	for _, table := range userDeleteFoldedTables {
		where := "user_id = $1"
		if column == "profile_email" {
			where = "lower(profile_email) = lower($1)"
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s", table, where), value); err != nil {
			return fmt.Errorf("delete from %s: %w", table, err)
		}
	}
	if err := refreshSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	if err := refreshPluginInvocationFacts(ctx, tx, affected); err != nil {
		return err
	}
	if err := refreshTaskSegmentFacts(ctx, tx, affected); err != nil {
		return err
	}
	// Steps 4 and 5: the facts of every affected session go, and they are queued
	// to be computed again from the rows other identities left.
	if err := deleteSessionFacts(ctx, tx, affected); err != nil {
		return err
	}
	if err := rebuildSessionClaims(ctx, tx, affected); err != nil {
		return err
	}
	if err := markSessionFactsDirtyInOrder(ctx, tx,
		dirtyGroup{ids: affected, reason: dirtyDeletionUser},
		dirtyGroup{ids: append(neighbours, claimPeers...), reason: dirtyDeletionPeer}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) MergeUsers(ctx context.Context, fromProfileEmail string, fromUserID string, toProfileEmail string) error {
	if toProfileEmail == "" {
		return fmt.Errorf("toProfileEmail required")
	}
	if fromProfileEmail == "" && fromUserID == "" {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, true); err != nil {
		return err
	}
	column, value, extra := "user_id", fromUserID, " AND profile_email = ''"
	ownerColumn := "owner_user_id"
	if fromProfileEmail != "" {
		column, value, extra = "profile_email", fromProfileEmail, ""
		ownerColumn = "owner_profile_email"
	}
	if fromProfileEmail != toProfileEmail {
		if err := deleteQuotaOwnerBindingsForIdentity(ctx, tx, column, value, toProfileEmail); err != nil {
			return fmt.Errorf("remove source quota owner data: %w", err)
		}
	}
	// The observation tables are read too, as DeleteUserData reads them: a session
	// observed before any of its records synced is still one this merge moves.
	affected, err := querySessionIDs(ctx, tx, fmt.Sprintf(`
		SELECT session_id FROM otel_events WHERE %[1]s = $1%[2]s AND session_id <> ''
		UNION SELECT session_id FROM session_records WHERE %[1]s = $1%[2]s AND session_id <> ''
		UNION SELECT session_id FROM claude_imputed_cost WHERE %[1]s = $1%[2]s AND session_id <> ''
		UNION SELECT session_id FROM codex_imputed_cost WHERE %[1]s = $1%[2]s AND session_id <> ''
		UNION SELECT session_id FROM session_snapshots WHERE %[1]s = $1%[2]s AND session_id <> ''
		UNION SELECT session_id FROM session_live_states WHERE %[1]s = $1%[2]s AND session_id <> ''
		UNION SELECT session_id FROM session_observation_events WHERE %[1]s = $1%[2]s AND session_id <> ''
		UNION SELECT session_id FROM session_personal_identity_keys WHERE source_%[1]s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_group_dismissal_identity_keys WHERE source_%[1]s = $1 AND session_id <> ''
		UNION SELECT session_id FROM session_ai_records WHERE %[3]s = $1 AND session_id <> ''`,
		column, extra, ownerColumn), value)
	if err != nil {
		return err
	}
	claimPeers, err := prepareSessionClaimMutation(ctx, tx, affected, toProfileEmail)
	if err != nil {
		return err
	}
	if err := lockSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	if err := lockPluginInvocationFacts(ctx, tx, false); err != nil {
		return err
	}
	// Source merge is not authorization to transfer a dashboard user's private
	// text. Preserve old and destination deletion keys, but hide the row.
	if _, err := tx.Exec(ctx, `UPDATE session_personal_state p SET owner_state='invalidated'
		FROM session_personal_identity_keys k
		WHERE p.dashboard_user_id=k.dashboard_user_id AND p.session_id=k.session_id
		AND k.source_`+column+`=$1`, value); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_personal_identity_keys
		(dashboard_user_id,session_id,source_user_id,source_profile_email)
		SELECT dashboard_user_id,session_id,source_user_id,$2
		FROM session_personal_identity_keys WHERE source_`+column+`=$1 ON CONFLICT DO NOTHING`, value, toProfileEmail); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE session_group_dismissals d SET owner_state='invalidated'
		FROM session_group_dismissal_identity_keys k
		WHERE d.dashboard_user_id=k.dashboard_user_id AND d.session_id=k.session_id
		AND d.grouping_version=k.grouping_version AND d.ended_at=k.ended_at
		AND k.source_`+column+`=$1`, value); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_group_dismissal_identity_keys
		(dashboard_user_id,session_id,grouping_version,ended_at,source_user_id,source_profile_email)
		SELECT dashboard_user_id,session_id,grouping_version,ended_at,source_user_id,$2
		FROM session_group_dismissal_identity_keys WHERE source_`+column+`=$1 ON CONFLICT DO NOTHING`, value, toProfileEmail); err != nil {
		return err
	}
	for _, table := range userMergeUpdateTables {
		q := fmt.Sprintf("UPDATE %s SET profile_email = $1 WHERE %s = $2%s", table, column, extra)
		if _, err := tx.Exec(ctx, q, toProfileEmail, value); err != nil {
			return fmt.Errorf("merge %s: %w", table, err)
		}
	}
	// The observation tables and client_versions cannot take that UPDATE.
	// profile_email is part of their primary key, so it fails wherever the target
	// address already holds the same row -- the same session observed under both
	// addresses, or both addresses' collector heartbeats or client versions. Their
	// rows are upserted onto the target instead, the two combined as ingest
	// combines a new report with the stored row, the newer one playing the new
	// report, and the old rows deleted after. "profile_email <> $1" keeps a merge
	// into the same address from deleting the rows it has just upserted onto
	// themselves.
	for _, m := range userMergeUpsertTables {
		q := fmt.Sprintf(`INSERT INTO %[1]s (profile_email, %[2]s)
			SELECT $1::text, %[2]s FROM %[1]s WHERE %[3]s = $2%[4]s
			ON CONFLICT (%[5]s) DO UPDATE SET %[6]s`, m.table, m.columns, column, extra, m.key, m.set)
		if _, err := tx.Exec(ctx, q, toProfileEmail, value); err != nil {
			return fmt.Errorf("merge %s: %w", m.table, err)
		}
		q = fmt.Sprintf("DELETE FROM %s WHERE %s = $2%s AND profile_email <> $1", m.table, column, extra)
		if _, err := tx.Exec(ctx, q, toProfileEmail, value); err != nil {
			return fmt.Errorf("merge %s: remove merged rows: %w", m.table, err)
		}
	}
	if err := mergeFoldedTables(ctx, tx, column, value, extra, toProfileEmail); err != nil {
		return err
	}
	if fromProfileEmail != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO user_aliases (from_profile_email, to_profile_email)
			VALUES (lower($1), lower($2)) ON CONFLICT (from_profile_email)
			DO UPDATE SET to_profile_email = EXCLUDED.to_profile_email`, fromProfileEmail, toProfileEmail); err != nil {
			return fmt.Errorf("upsert alias: %w", err)
		}
	}
	if err := refreshSessionOverviewRollups(ctx, tx, affected); err != nil {
		return err
	}
	if err := refreshPluginInvocationFacts(ctx, tx, affected); err != nil {
		return err
	}
	if err := refreshTaskSegmentFacts(ctx, tx, affected); err != nil {
		return err
	}
	// Steps 4 and 5 of the deletion contract of session analysis (p1b-design
	// §6.2). The rows only change hands, so no copy marker changes, but the facts
	// name the old identity, and a moved snapshot can replace the title they were
	// computed with: they go now, and the sessions are queued as a removal.
	if err := deleteSessionFacts(ctx, tx, affected); err != nil {
		return err
	}
	if err := rebuildSessionClaims(ctx, tx, affected); err != nil {
		return err
	}
	if err := markSessionFactsDirtyInOrder(ctx, tx,
		dirtyGroup{ids: affected, reason: dirtyMerge},
		dirtyGroup{ids: claimPeers, reason: dirtyDeletionPeer}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// mergeFoldedTables is MergeUsers's part for quota_snapshots and privacy_settings,
// the two tables DeleteUserData reaches through userDeleteFoldedTables. Both keep
// the address as it arrived, so the source is found by lower(profile_email), and
// neither can take the UPDATE of userMergeUpdateTables: profile_email is part of
// their unique key.
//
// quota_snapshots is the latest rate-limit reading, written again every few
// minutes, so the source's row is deleted, not moved. Moving it would put a second
// row beside the one the target's own reports fill, under whatever case the target
// was typed in (it is not normalized, handlers_users.go).
//
// privacy_settings are moved to the target only when they have no user_id. A
// setting with one is found by it as before, after the merge. A user_id merge has
// none to claim: they all name a profile. Where the target already has the same
// scope, its row stays. A moved row takes the cctrace user ID of the target's
// dashboard account, if it has one: the dashboard lists and removes an account's
// settings by that ID alone (ListPrivacySettings, RemovePrivacy).
//
// "profile_email <> $2" keeps a merge into the same address from deleting rows it
// would only move onto themselves.
func mergeFoldedTables(ctx context.Context, tx pgx.Tx, column, value, extra, toProfileEmail string) error {
	if column == "user_id" {
		if _, err := tx.Exec(ctx, "DELETE FROM quota_snapshots WHERE user_id = $1"+extra, value); err != nil {
			return fmt.Errorf("merge quota_snapshots: %w", err)
		}
		return nil
	}
	const source = `lower(profile_email) = lower($1) AND profile_email <> $2`
	if _, err := tx.Exec(ctx, `DELETE FROM quota_snapshots WHERE `+source, value, toProfileEmail); err != nil {
		return fmt.Errorf("merge quota_snapshots: %w", err)
	}
	// DISTINCT ON: two source rows that differ in address case only are one scope.
	if _, err := tx.Exec(ctx, `INSERT INTO privacy_settings (user_id, profile_email, scope_type, scope_value, created_at)
		SELECT DISTINCT ON (scope_type, scope_value)
			COALESCE((SELECT cctrace_user_id FROM dashboard_users
				WHERE lower(email) = lower($2::text) AND cctrace_user_id <> '' LIMIT 1), ''),
			$2::text, scope_type, scope_value, created_at
		FROM privacy_settings WHERE user_id = '' AND `+source+`
		ORDER BY scope_type, scope_value, created_at
		ON CONFLICT (profile_email, scope_type, scope_value) DO NOTHING`, value, toProfileEmail); err != nil {
		return fmt.Errorf("merge privacy_settings: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM privacy_settings WHERE user_id = '' AND `+source, value, toProfileEmail); err != nil {
		return fmt.Errorf("merge privacy_settings: remove merged rows: %w", err)
	}
	return nil
}

// userDeleteTables are the tables DeleteUserData deletes an identity's rows from,
// by profile_email or by user_id.
var userDeleteTables = []string{"otel_events", "otel_metrics", "session_records", "claude_imputed_cost", "codex_imputed_cost", "plugin_invocation_facts", "task_segment_facts",
	"session_snapshots", "session_live_states", "session_observation_events", "collector_heartbeats", "client_versions", "client_policy_reports"}

// userDeleteFoldedTables are the tables DeleteUserData deletes an identity's rows
// from by lower(profile_email) rather than by an exact match, or by user_id.
var userDeleteFoldedTables = []string{"quota_snapshots", "privacy_settings"}

// userMergeUpdateTables are the tables MergeUsers moves to the target address with
// an UPDATE of profile_email.
var userMergeUpdateTables = []string{"otel_events", "otel_metrics", "session_records", "claude_imputed_cost", "codex_imputed_cost", "plugin_invocation_facts", "task_segment_facts", "client_policy_reports"}

// userMergeUpsertTables are the tables MergeUsers upserts onto the target address
// instead, because profile_email is part of their primary key.
var userMergeUpsertTables = []struct{ table, key, columns, set string }{
	{
		table:   "session_snapshots",
		key:     "session_id, user_id, profile_email",
		columns: "session_id, user_id, login_email, agent, project_hash, billing_provider, account_id, collect_version, snapshot, observed_at, received_at, identity_provenance",
		set: `login_email = EXCLUDED.login_email, agent = EXCLUDED.agent, project_hash = EXCLUDED.project_hash,
			billing_provider = EXCLUDED.billing_provider, account_id = EXCLUDED.account_id,
			collect_version = EXCLUDED.collect_version, snapshot = EXCLUDED.snapshot,
			observed_at = EXCLUDED.observed_at, received_at = EXCLUDED.received_at, identity_provenance = EXCLUDED.identity_provenance
			WHERE EXCLUDED.observed_at >= session_snapshots.observed_at`,
	},
	{
		table:   "session_live_states",
		key:     "session_id, user_id, profile_email, pid, proc_start",
		columns: "session_id, user_id, login_email, agent, project_hash, billing_provider, account_id, pid, proc_start, status, waiting_kind, name, name_source, status_changed_at, heartbeat_at, received_at, identity_provenance",
		set: `login_email = EXCLUDED.login_email, agent = EXCLUDED.agent, project_hash = EXCLUDED.project_hash,
			billing_provider = EXCLUDED.billing_provider, account_id = EXCLUDED.account_id, status = EXCLUDED.status,
			waiting_kind = EXCLUDED.waiting_kind, name = EXCLUDED.name, name_source = EXCLUDED.name_source,
			status_changed_at = EXCLUDED.status_changed_at, heartbeat_at = EXCLUDED.heartbeat_at,
			received_at = EXCLUDED.received_at, identity_provenance = EXCLUDED.identity_provenance
			WHERE EXCLUDED.heartbeat_at >= session_live_states.heartbeat_at`,
	},
	{
		table:   "session_observation_events",
		key:     "session_id, user_id, profile_email, event_id",
		columns: "session_id, user_id, login_email, agent, project_hash, billing_provider, account_id, event_id, kind, observed_at, payload, received_at, identity_provenance",
		set: `login_email = EXCLUDED.login_email, agent = EXCLUDED.agent, project_hash = EXCLUDED.project_hash,
			billing_provider = EXCLUDED.billing_provider, account_id = EXCLUDED.account_id, kind = EXCLUDED.kind,
			observed_at = EXCLUDED.observed_at, payload = EXCLUDED.payload, received_at = EXCLUDED.received_at, identity_provenance = EXCLUDED.identity_provenance
			WHERE EXCLUDED.observed_at >= session_observation_events.observed_at`,
	},
	{
		// Both times only move forward. The version follows the newer heartbeat,
		// and an empty one never replaces a known one.
		table:   "collector_heartbeats",
		key:     "user_id, profile_email",
		columns: "user_id, heartbeat_at, last_stored_at, client_version",
		set: `heartbeat_at = GREATEST(collector_heartbeats.heartbeat_at, EXCLUDED.heartbeat_at),
			last_stored_at = GREATEST(collector_heartbeats.last_stored_at, EXCLUDED.last_stored_at),
			client_version = CASE
				WHEN EXCLUDED.client_version = '' THEN collector_heartbeats.client_version
				WHEN collector_heartbeats.client_version = '' THEN EXCLUDED.client_version
				WHEN EXCLUDED.heartbeat_at > collector_heartbeats.heartbeat_at THEN EXCLUDED.client_version
				ELSE collector_heartbeats.client_version END`,
	},
	{
		// Keyed by profile_email alone. The two rows are combined as
		// UpsertClientVersion combines a sync with the row it lands on, the newer
		// row (the merged-in one on a tie) in the place of the sync: an empty
		// version, OS or architecture does not erase a known one, update_reported
		// never goes back to false, and the update fields are the newer row's only
		// when it carried a report.
		table:   "client_versions",
		key:     "profile_email",
		columns: "user_id, client_version, client_os, client_arch, last_seen_at, update_reported, update_target_version, update_fail_count, update_first_failed_at, update_last_failed_at, update_fail_reason",
		set: `user_id = CASE WHEN EXCLUDED.last_seen_at >= client_versions.last_seen_at
				THEN EXCLUDED.user_id ELSE client_versions.user_id END,
			client_version = CASE WHEN EXCLUDED.last_seen_at >= client_versions.last_seen_at
				THEN COALESCE(NULLIF(EXCLUDED.client_version, ''), client_versions.client_version)
				ELSE COALESCE(NULLIF(client_versions.client_version, ''), EXCLUDED.client_version) END,
			client_os = CASE WHEN EXCLUDED.last_seen_at >= client_versions.last_seen_at
				THEN COALESCE(NULLIF(EXCLUDED.client_os, ''), client_versions.client_os)
				ELSE COALESCE(NULLIF(client_versions.client_os, ''), EXCLUDED.client_os) END,
			client_arch = CASE WHEN EXCLUDED.last_seen_at >= client_versions.last_seen_at
				THEN COALESCE(NULLIF(EXCLUDED.client_arch, ''), client_versions.client_arch)
				ELSE COALESCE(NULLIF(client_versions.client_arch, ''), EXCLUDED.client_arch) END,
			last_seen_at = GREATEST(client_versions.last_seen_at, EXCLUDED.last_seen_at),
			update_reported = client_versions.update_reported OR EXCLUDED.update_reported,
			update_target_version = CASE WHEN EXCLUDED.last_seen_at >= client_versions.last_seen_at
				THEN CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_target_version ELSE client_versions.update_target_version END
				ELSE CASE WHEN client_versions.update_reported THEN client_versions.update_target_version ELSE EXCLUDED.update_target_version END END,
			update_fail_count = CASE WHEN EXCLUDED.last_seen_at >= client_versions.last_seen_at
				THEN CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_fail_count ELSE client_versions.update_fail_count END
				ELSE CASE WHEN client_versions.update_reported THEN client_versions.update_fail_count ELSE EXCLUDED.update_fail_count END END,
			update_first_failed_at = CASE WHEN EXCLUDED.last_seen_at >= client_versions.last_seen_at
				THEN CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_first_failed_at ELSE client_versions.update_first_failed_at END
				ELSE CASE WHEN client_versions.update_reported THEN client_versions.update_first_failed_at ELSE EXCLUDED.update_first_failed_at END END,
			update_last_failed_at = CASE WHEN EXCLUDED.last_seen_at >= client_versions.last_seen_at
				THEN CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_last_failed_at ELSE client_versions.update_last_failed_at END
				ELSE CASE WHEN client_versions.update_reported THEN client_versions.update_last_failed_at ELSE EXCLUDED.update_last_failed_at END END,
			update_fail_reason = CASE WHEN EXCLUDED.last_seen_at >= client_versions.last_seen_at
				THEN CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_fail_reason ELSE client_versions.update_fail_reason END
				ELSE CASE WHEN client_versions.update_reported THEN client_versions.update_fail_reason ELSE EXCLUDED.update_fail_reason END END`,
	},
}

// CleanupOrphanSessionRecords deletes session_records that no event backs.
//
// It must read unified_events, never visible_events: an excluded account's
// events are hidden from the visible view, so filtering here would classify its
// sessions as orphans and delete them — turning a reversible exclusion into
// permanent data loss.
//
// It deletes session analysis facts and copy markers under the deletion contract
// (p1b-design §6.1), and with only the shared source lock it would meet the
// worker's publish, which holds that lock shared too. The retention lock keeps
// the two apart -- exclusive here, shared in the publish -- and is taken before
// the source lock, as the retention reconciliation takes it (p1b-design §5.4).
func (s *PgStore) CleanupOrphanSessionRecords(ctx context.Context) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('session_overview_retention'))`); err != nil {
		return 0, fmt.Errorf("lock session overview retention: %w", err)
	}
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `
		DELETE FROM session_records sr
		WHERE NOT EXISTS (
			SELECT 1 FROM unified_events e WHERE e.session_id = sr.session_id
		)
		RETURNING sr.session_id`)
	if err != nil {
		return 0, fmt.Errorf("cleanup orphan session records: %w", err)
	}
	seen := map[string]struct{}{}
	var affected []string
	var deleted int64
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		deleted++
		if _, ok := seen[id]; !ok && id != "" {
			seen[id] = struct{}{}
			affected = append(affected, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := refreshSessionOverviewRollups(ctx, tx, affected); err != nil {
		return 0, err
	}
	// Steps 1 and 2 of the deletion contract: the copy markers naming the sessions
	// that lost rows, with the sessions on their other side kept to queue.
	neighbours, err := takeCopyMarkers(ctx, tx, affected)
	if err != nil {
		return 0, err
	}
	if err := lockPluginInvocationFacts(ctx, tx, false); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_invocation_facts f WHERE NOT EXISTS (
		SELECT 1 FROM session_records sr WHERE sr.id = f.source_record_id)`); err != nil {
		return 0, err
	}
	if err := refreshPluginInvocationFacts(ctx, tx, affected); err != nil {
		return 0, err
	}
	if err := lockTaskSegmentFacts(ctx, tx, false); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_segment_facts f WHERE NOT EXISTS (
		SELECT 1 FROM session_records sr WHERE sr.id = f.boundary_record_id)`); err != nil {
		return 0, err
	}
	if err := refreshTaskSegmentFacts(ctx, tx, affected); err != nil {
		return 0, err
	}
	// Steps 4 and 5.
	if err := deleteSessionFacts(ctx, tx, affected); err != nil {
		return 0, err
	}
	if err := markSessionFactsDirtyInOrder(ctx, tx,
		dirtyGroup{ids: affected, reason: dirtyOrphan},
		dirtyGroup{ids: neighbours, reason: dirtyDeletionPeer}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return deleted, nil
}
