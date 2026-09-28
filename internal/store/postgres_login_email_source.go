package store

import (
	"context"
	"fmt"
	"log"
)

// loginEmailSourceBackfill identifies this one-time repair in schema_backfills.
const loginEmailSourceBackfill = "session_records_login_email_source_v1"

// BackfillLoginEmailSource stamps 'otel' on the rows that were already attributed
// before login_email_source existed.
//
// Without it the empty value carries two meanings: "nobody has ever attributed this row" and
// "attributed years ago by the session-scoped OTEL backfill". Only the first is a
// fact, and the inference pass (postgres_backfill_inference.go) is defined
// entirely in terms of it -- an inferred value has to be distinguishable from an
// observed one afterwards, or nothing can ever be re-judged when the inference
// rule changes.
//
// 'otel' is right for every one of them: the JSONL sync path does not write
// login_email at all (see the view comment in migrations.go), so every non-empty
// value present when the column was added was put there by
// BackfillSessionRecordLoginEmail, straight out of otel_events.
//
// Run it off the boot path. It rewrites every attributed row -- millions on
// production -- and BackfillCanonicalProjectHash already paid for that lesson:
// the schema migration blocks startup, so a data repair behind it turns a release
// into an outage.
//
// No rollup or fact refresh follows. login_email_source appears in no derived
// table (session_overview_rollups, plugin_invocation_facts and task_segment_facts
// copy login_email, not its provenance), and login_email is precisely the column
// this statement does not touch.
func (s *PgStore) BackfillLoginEmailSource(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	// Serialize concurrent cctraced instances so two of them cannot both read a
	// missing marker and both run the scan.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, loginEmailSourceBackfill); err != nil {
		return fmt.Errorf("lock login_email_source backfill: %w", err)
	}
	var done bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, loginEmailSourceBackfill).Scan(&done); err != nil {
		return fmt.Errorf("read login_email_source marker: %w", err)
	}
	if done {
		return tx.Commit(ctx)
	}

	// The login_email_source = '' guard keeps this idempotent if the marker is ever
	// cleared by hand, and stops the statement from rewriting rows an inference pass
	// has already stamped.
	tag, err := tx.Exec(ctx, `UPDATE session_records
		SET login_email_source = 'otel'
		WHERE login_email <> '' AND login_email_source = ''`)
	if err != nil {
		return fmt.Errorf("stamp login_email_source: %w", err)
	}
	// Marker and data commit together: a crash leaves both absent (safe to retry),
	// never a marker over a half-written table.
	if _, err := tx.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, loginEmailSourceBackfill); err != nil {
		return fmt.Errorf("record login_email_source marker: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		log.Printf("[store] login_email_source backfill: %d rows stamped otel", n)
	}
	return nil
}
