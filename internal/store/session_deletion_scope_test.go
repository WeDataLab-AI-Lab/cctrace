package store

import (
	"context"
	"testing"
)

// The list and the schema have to be compared by somebody. Left to a person, the
// comparison does not happen: a table gets a session_id column, nobody asks
// whether a privacy delete should reach it, the sweep keeps reporting success, and
// the gap is visible only to whoever thinks to diff the two by hand. That is how
// this issue started, with three tables swept out of what turned out to be nine
// (#396).
//
// So the test does the diff. A new per-session table either joins the list or is
// named here with the reason it does not belong -- either way somebody answered
// the question.
func TestEverySessionScopedTableIsJudged(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	// Tables that carry a session column but describe the deployment rather than
	// the person who ran the session. Deleting somebody's sessions must not reset
	// a backfill marker or a rate table.
	notPersonal := map[string]string{
		"deleted_sessions":                 "the tombstone itself -- deleting it would lose the record that a delete happened",
		"excluded_sessions":                "an administrator's exclusion, not the session author's data",
		"excluded_sessions_refresh_state":  "a refresh marker for the deployment",
		"session_overview_retention_state": "a retention watermark for the deployment",
		"codex_imputed_cursor":             "an ingestion cursor for the deployment",
	}

	rows, err := s.pool.Query(ctx, `
		SELECT c.table_name, c.column_name
		FROM information_schema.columns c
		JOIN information_schema.tables t
		  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
		WHERE c.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		  AND c.column_name IN ('session_id', 'source_session_id')
		ORDER BY c.table_name`)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	defer rows.Close()

	swept := map[string]string{}
	for _, target := range sessionDeletionTargets {
		swept[target.table] = target.key
	}

	// Either spelling identifies a session; a table may carry both.
	sessionColumn := map[string]bool{"session_id": true, "source_session_id": true}

	var unjudged []string
	seen := map[string]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[table] = true
		if key, ok := swept[table]; ok {
			// quota_samples has both columns; the sweep keys on the source one
			// because the other names the account, not the session.
			if key != column && !sessionColumn[column] {
				t.Errorf("%s is swept by %q but its session column is %q", table, key, column)
			}
			continue
		}
		if _, ok := notPersonal[table]; ok {
			continue
		}
		unjudged = append(unjudged, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(unjudged) > 0 {
		t.Errorf("these tables carry a session column and nobody has said whether a privacy delete reaches them: %v\n"+
			"Add each to sessionDeletionTargets, or to notPersonal above with the reason it stays out.", unjudged)
	}

	// The other direction: a target naming a table that no longer exists would make
	// the sweep silently skip it while still reporting success.
	for table := range swept {
		if !seen[table] {
			t.Errorf("sessionDeletionTargets names %s, which has no session column in the schema", table)
		}
	}
}

// Being on the list and actually being emptied are different claims, and the
// second is the one a privacy delete makes. The repair staging tables were added
// to the list in #396; this runs the real sweep against seeded rows rather than
// trusting the list to be wired up.
func TestSweepEmptiesRepairStagingTables(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO login_email_history_repair_state (name, phase, max_record_id)
		VALUES ('t', 'source', 0) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	// No t.Skip here. A skip would make this test report success on exactly the
	// setup it exists to exercise, which is how the first version of it passed
	// while the sweep was not reaching these tables at all.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO codex_login_email_repair_state (name, phase, max_record_id)
		VALUES ('t', 'account', 0) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed codex repair state: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO login_email_history_session_intervals
			(repair_name, session_id, interval_no, login_email, from_ts, first_interval)
		VALUES ('t', 'doomed', 1, 'someone@example.com', now(), true),
		       ('t', 'kept',   1, 'other@example.com',   now(), true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO codex_login_email_repair_candidates (repair_name, id, session_id, account_id, any_inferred)
		VALUES ('t', 1, 'doomed', 'acc-1', false),
		       ('t', 2, 'kept',   'acc-2', false)`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteSession(ctx, "doomed", "admin@example.com", "privacy", false, false); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	// The delete leaves a tombstone; the sweep is what reclaims the rows.
	if _, err := s.SweepDeletedSessions(ctx, 25); err != nil {
		t.Fatalf("SweepDeletedSessions: %v", err)
	}

	for _, table := range []string{"login_email_history_session_intervals", "codex_login_email_repair_candidates"} {
		var doomed, kept int
		if err := s.pool.QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE session_id = 'doomed'),
			        count(*) FILTER (WHERE session_id = 'kept') FROM `+table).Scan(&doomed, &kept); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if doomed != 0 {
			t.Errorf("%s still holds %d row(s) for the deleted session", table, doomed)
		}
		// The sweep must be surgical: another session's staged repair is not this
		// person's data to delete.
		if kept != 1 {
			t.Errorf("%s lost an unrelated session's row (kept = %d)", table, kept)
		}
	}
}
