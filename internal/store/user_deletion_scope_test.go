package store

import (
	"context"
	"testing"
)

// TestEverySessionScopedTableIsJudged does this diff for session columns; this
// one does it for the two keys DeleteUserData and MergeUsers take. A table that
// names an account by profile_email or user_id is either on both functions'
// lists, or named here with how it is reached instead, or with what it holds and
// that nothing reaches it. client_policy_reports and client_versions were outside
// both lists, and the privacy page said their rows stay, until someone compared
// the schema with the lists by hand.
//
// It reads those two column names and no others. A table that names an account
// under another spelling -- owner_profile_email, source_profile_email,
// login_email -- is outside this diff, and so are the paths that reach one.
func TestEveryIdentityScopedTableIsJudged(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	// Reached without being on the lists.
	reachedOtherwise := map[string]string{
		"session_overview_rollups":      "derived per session; both functions re-derive the rollups of the sessions they touch",
		"session_commit_claims":         "follows a session_snapshots row through a cascading foreign key, and both functions rebuild the claims of the sessions they touch",
		"session_commit_claim_coverage": "follows a session_snapshots row through a cascading foreign key",
		// Rebuilt from the profile's session_records on every exclusion change and on
		// the 30-minute RefreshExcludedBillingLinks tick, so a deleted account's
		// minutes go at the next tick, with its records -- not in the same
		// transaction.
		"excluded_codex_metric_minutes": "per-minute hiding decisions rebuilt from session_records",
	}
	// Reached by statements of their own on both functions, because both keep the
	// address as it arrived and an exact match misses it in another case.
	// DeleteUserData's are the userDeleteFoldedTables list, checked below;
	// MergeUsers's are mergeFoldedTables, which TestMergeUsers_removesSourceQuotaSnapshot
	// and TestMergeUsers_movesPrivacySettingsWithoutUserID run.
	handledApart := map[string]string{
		"privacy_settings": "the account's own settings that hide its sessions' AI records; merge moves the rows that have no user_id",
		"quota_snapshots":  "the account's latest rate-limit reading; merge deletes the source's row",
	}
	// No path reaches these yet, and the change that added this test left them out
	// of its scope. DeleteUserData leaves their rows, and MergeUsers leaves them
	// under the old address. That is a known gap, not a decision that the rows
	// stay; naming them keeps a new table from joining them unasked.
	notReached := map[string]string{
		// The session sweep deletes rows by source_session_id
		// (sessionDeletionTargets), but nothing deletes or moves them by account.
		"quota_samples": "rate-limit history, naming the profile that reported it",
		// Only the rollup rebuild deletes rows, by bucket (postgres_usage_rollup.go);
		// p1t-design §5.1 records the gap as older than that work.
		"usage_hourly_rollups": "the hourly usage aggregate, by account",
		// Staging of the one-time login email repair. Its session counterpart,
		// login_email_history_session_intervals, is in sessionDeletionTargets;
		// this one is reached by nothing.
		"login_email_history_user_intervals": "staging of the one-time login email repair, by user_id",
		// Rebuilt from quota_samples (excludedCodexMetricProfilesDesiredSQL), which
		// nothing deletes by account, so the address stays as long as they do.
		"excluded_codex_metric_profiles": "the addresses whose every Codex account is excluded",
	}

	deleted := map[string]bool{}
	for _, table := range userDeleteTables {
		deleted[table] = true
	}
	folded := map[string]bool{}
	for _, table := range userDeleteFoldedTables {
		folded[table] = true
	}
	merged := map[string]bool{}
	for _, table := range userMergeUpdateTables {
		merged[table] = true
	}
	for _, m := range userMergeUpsertTables {
		merged[m.table] = true
	}

	rows, err := s.pool.Query(ctx, `
		SELECT c.table_name,
		       bool_or(c.column_name = 'profile_email'), bool_or(c.column_name = 'user_id')
		FROM information_schema.columns c
		JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		  AND c.column_name IN ('profile_email', 'user_id')
		GROUP BY c.table_name
		ORDER BY c.table_name`)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	defer rows.Close()

	var unjudged []string
	seen := map[string]bool{}
	for rows.Next() {
		var table string
		var hasEmail, hasUserID bool
		if err := rows.Scan(&table, &hasEmail, &hasUserID); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[table] = true
		// Both functions take either key, so a listed table needs both columns.
		if (deleted[table] || merged[table] || folded[table]) && (!hasEmail || !hasUserID) {
			t.Errorf("%s is on a delete or merge list but lacks profile_email or user_id", table)
		}
		_, otherwise := reachedOtherwise[table]
		_, apart := handledApart[table]
		_, open := notReached[table]
		switch {
		case otherwise || apart || open:
		case deleted[table] && merged[table]:
		case deleted[table]:
			t.Errorf("%s is deleted by DeleteUserData but MergeUsers leaves it under the old address", table)
		case merged[table]:
			t.Errorf("%s is moved by MergeUsers but DeleteUserData leaves it", table)
		default:
			unjudged = append(unjudged, table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(unjudged) > 0 {
		t.Errorf("these tables carry profile_email or user_id and nobody has said whether a user delete or merge reaches them: %v\n"+
			"Add each to the DeleteUserData and MergeUsers lists, or name it above.", unjudged)
	}

	// The other direction: a name for a table that no longer has the column.
	for _, names := range []map[string]bool{deleted, merged, folded} {
		for table := range names {
			if !seen[table] {
				t.Errorf("a delete or merge list names %s, which has no profile_email or user_id column", table)
			}
		}
	}
	// The delete half of handledApart is a list, so this can check it. A table
	// taken off the list while its name stays above would read as judged.
	for table := range folded {
		if _, ok := handledApart[table]; !ok {
			t.Errorf("%s is on userDeleteFoldedTables but not named in handledApart", table)
		}
	}
	for table := range handledApart {
		if !folded[table] {
			t.Errorf("%s is named in handledApart but DeleteUserData does not delete it by userDeleteFoldedTables", table)
		}
	}
	for _, names := range []map[string]string{reachedOtherwise, handledApart, notReached} {
		for table := range names {
			if !seen[table] {
				t.Errorf("%s is named above but has no profile_email or user_id column", table)
			}
		}
	}
}
