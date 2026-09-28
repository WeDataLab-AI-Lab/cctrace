package store

import (
	"context"
	"fmt"
	"log"

	"cctrace/internal/projecthash"
)

// backfillName identifies this one-time repair in schema_backfills.
//
// The version suffix is load-bearing. A database that recorded an earlier version has
// values this pass would still change -- case folding and the project_name repair were
// both added after the first version -- and reusing the name would skip them forever.
// Widen what the repair does, bump the suffix.
const backfillName = "project_hash_canonical_v3"

// projectHashTables lists every table holding a project_hash. session_records is the
// large one; the others are small but must move with it or a join stops matching.
var projectHashTables = []string{"projects", "session_records", "plugin_invocation_facts", "project_rules"}

// BackfillCanonicalProjectHash rewrites stored project_hash values to the spelling
// Claude Code uses, once.
//
// Before this ran, the same directory could sit under several keys: the POSIX
// producers dropped the leading separator, and on Windows they left the raw path
// untouched because their conversion only knew about '/'. A project filter then
// matched one spelling and silently dropped the sessions recorded under the others
// (#303).
//
// The repair runs in Go rather than SQL so it uses the same projecthash.Repair the
// ingest path uses. Expressing the rule twice -- once here, once in a regex -- is how
// the two would drift, and a drift here is invisible: both spellings look plausible.
//
// It is safe to interrupt. Repair is idempotent and each statement matches on the old
// value, so a re-run resumes rather than double-applying; the marker is only written
// once everything has succeeded.
//
// Run it off the boot path. Repairing session_records rewrote 4.1 million rows and
// took nine minutes on a copy of production, and while it ran nothing was listening:
// the schema migration blocks startup, and putting a data repair behind that turns a
// release into an outage. Serving the old spellings for a few more minutes is what
// users already had; not serving at all is not.
func (s *PgStore) BackfillCanonicalProjectHash(ctx context.Context) error {
	var done bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, backfillName).Scan(&done); err != nil {
		return fmt.Errorf("read backfill marker: %w", err)
	}
	if done {
		return nil
	}

	for _, table := range projectHashTables {
		changed, err := s.repairTable(ctx, table)
		if err != nil {
			return fmt.Errorf("backfill %s: %w", table, err)
		}
		if changed > 0 {
			log.Printf("[store] project_hash backfill: %s, %d rows repaired", table, changed)
		}
	}

	named, err := s.repairProjectNames(ctx)
	if err != nil {
		return fmt.Errorf("backfill project names: %w", err)
	}
	if named > 0 {
		log.Printf("[store] project_hash backfill: %d project names shortened to a directory name", named)
	}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`, backfillName); err != nil {
		return fmt.Errorf("record backfill marker: %w", err)
	}
	return nil
}

// repairTable rewrites one table's hashes and reports how many rows moved.
func (s *PgStore) repairTable(ctx context.Context, table string) (int64, error) {
	rows, err := s.pool.Query(ctx,
		fmt.Sprintf(`SELECT DISTINCT project_hash FROM %s WHERE project_hash <> ''`, table))
	if err != nil {
		return 0, err
	}
	var stored []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return 0, err
		}
		stored = append(stored, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var total int64
	for _, old := range stored {
		repaired := projecthash.Repair(old)
		if repaired == old {
			continue
		}
		n, err := s.moveHash(ctx, table, old, repaired)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// moveHash points one stored spelling at its canonical form.
//
// projects is keyed by (agent, project_hash), so the canonical row may already
// exist -- that is the merge this change is for. Updating into it would violate the
// primary key, so the update is guarded and whatever it could not move is dropped:
// the surviving row describes the same directory.
func (s *PgStore) moveHash(ctx context.Context, table, old, repaired string) (int64, error) {
	if table != "projects" {
		tag, err := s.pool.Exec(ctx,
			fmt.Sprintf(`UPDATE %s SET project_hash = $1 WHERE project_hash = $2`, table), repaired, old)
		if err != nil {
			return 0, err
		}
		return tag.RowsAffected(), nil
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE projects p SET project_hash = $1
		WHERE p.project_hash = $2
		  AND NOT EXISTS (
			SELECT 1 FROM projects q WHERE q.agent = p.agent AND q.project_hash = $1
		  )`, repaired, old)
	if err != nil {
		return 0, err
	}
	moved := tag.RowsAffected()

	drop, err := s.pool.Exec(ctx, `DELETE FROM projects WHERE project_hash = $1`, old)
	if err != nil {
		return moved, err
	}
	return moved + drop.RowsAffected(), nil
}

// repairProjectNames replaces a stored name that is really a whole path with the last
// segment of it.
//
// A Windows client's name arrived as "C:\\work\\app\\.agents" because the name was
// split on '/' alone, so the column people read in a project list held a path. New
// syncs overwrite it, but only for projects someone still works in; without this the
// rest keep showing a path forever.
func (s *PgStore) repairProjectNames(ctx context.Context) (int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT project_name FROM projects WHERE project_name LIKE '%/%' OR project_name LIKE '%\\%'`)
	if err != nil {
		return 0, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return 0, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var total int64
	for _, name := range names {
		short := projecthash.NameFromPath(name)
		if short == "" || short == name {
			continue
		}
		tag, err := s.pool.Exec(ctx,
			`UPDATE projects SET project_name = $1 WHERE project_name = $2`, short, name)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}
