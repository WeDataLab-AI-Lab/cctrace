package store

import (
	"context"
	"strings"
	"testing"
)

// userIndexNames are the three indexes the user_email -> profile_email rename
// left behind a DROP/CREATE pair for. On prod they cover 73 chunk indexes and
// 556MB, so rebuilding them on every boot is not free (#707).
var userIndexNames = []string{"idx_events_user", "idx_metrics_user", "idx_srec_user"}

// indexFiles maps every index (the hypertable parent and each chunk) to its
// relfilenode, keyed by index name so a dropped-and-recreated index is compared
// against its predecessor rather than looking like a different object. A
// rebuilt index gets a new relfilenode; an untouched one keeps its own.
func indexFiles(t *testing.T, s *PgStore) map[string]uint32 {
	t.Helper()
	ctx := context.Background()
	files := map[string]uint32{}
	for _, name := range userIndexNames {
		rows, err := s.pool.Query(ctx,
			`SELECT c.relname, c.relfilenode
			   FROM pg_class c
			  WHERE c.relkind = 'i' AND c.relname LIKE '%' || $1`, name)
		if err != nil {
			t.Fatalf("read %s relfilenodes: %v", name, err)
		}
		for rows.Next() {
			var relname string
			var node uint32
			if err := rows.Scan(&relname, &node); err != nil {
				rows.Close()
				t.Fatalf("scan %s relfilenode: %v", name, err)
			}
			files[relname] = node
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s relfilenodes: %v", name, err)
		}
	}
	if len(files) == 0 {
		t.Fatalf("none of %v exist after migration", userIndexNames)
	}
	return files
}

// A boot must not rebuild indexes it already has. Migrate has no applied-history
// table, so every statement runs on every boot: a DROP INDEX left in the list
// costs a full rebuild each time the server starts.
func TestMigrateDoesNotRebuildProfileEmailIndexes(t *testing.T) {
	s := acquireTestStore(t)
	before := indexFiles(t, s)

	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	after := indexFiles(t, s)
	for key, node := range before {
		got, ok := after[key]
		if !ok {
			t.Fatalf("%s disappeared across a second migrate", key)
		}
		if got != node {
			t.Fatalf("%s was rebuilt by a second migrate (relfilenode %d -> %d)", key, node, got)
		}
	}
}

// The indexes still have to exist, on the renamed column.
func TestMigrateKeepsProfileEmailIndexDefinitions(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	for _, name := range userIndexNames {
		var def string
		if err := s.pool.QueryRow(ctx,
			`SELECT indexdef FROM pg_indexes WHERE indexname = $1`, name).Scan(&def); err != nil {
			t.Fatalf("read %s definition: %v", name, err)
		}
		if !strings.Contains(def, "profile_email") || !strings.Contains(def, "ts DESC") {
			t.Fatalf("%s is not on (profile_email, ts DESC): %s", name, def)
		}
	}
}

// Why the DROP/CREATE pair was unnecessary: an index references its columns by
// attnum, so renaming a column carries the index with it. Recorded as a test
// because the whole removal rests on this behavior.
func TestRenameColumnCarriesIndex(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx, `CREATE TABLE rename_probe (user_email TEXT, ts TIMESTAMPTZ)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(context.Background(), `DROP TABLE IF EXISTS rename_probe`); err != nil {
			t.Logf("drop probe table: %v", err)
		}
	})
	if _, err := s.pool.Exec(ctx, `CREATE INDEX idx_rename_probe ON rename_probe (user_email, ts DESC)`); err != nil {
		t.Fatalf("create probe index: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `ALTER TABLE rename_probe RENAME COLUMN user_email TO profile_email`); err != nil {
		t.Fatalf("rename probe column: %v", err)
	}

	var def string
	if err := s.pool.QueryRow(ctx,
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_rename_probe'`).Scan(&def); err != nil {
		t.Fatalf("read probe index definition: %v", err)
	}
	if !strings.Contains(def, "profile_email") {
		t.Fatalf("rename did not carry the index: %s", def)
	}
}

// What the pre-rename path actually does. IF NOT EXISTS does not skip early: it
// resolves the columns first, so a CREATE naming a column that does not exist
// raises undefined_column even when an index of that name is already there.
//
// The consequence is that the user_email -> profile_email rename block
// (migrations.go:179-193) is unreachable. A database still on user_email dies
// at migrations.go:89, which names profile_email and runs long before the
// rename. Removing the DROP/CREATE pair that followed the rename therefore
// cannot regress an upgrade path -- nothing upgrades through there any more.
func TestCreateIndexIfNotExistsResolvesColumnsBeforeSkipping(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx, `CREATE TABLE legacy_probe (user_email TEXT, ts TIMESTAMPTZ)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	t.Cleanup(func() {
		if _, err := s.pool.Exec(context.Background(), `DROP TABLE IF EXISTS legacy_probe`); err != nil {
			t.Logf("drop probe table: %v", err)
		}
	})
	if _, err := s.pool.Exec(ctx, `CREATE INDEX idx_legacy_probe ON legacy_probe (user_email, ts DESC)`); err != nil {
		t.Fatalf("create probe index: %v", err)
	}

	// profile_email does not exist on this table yet.
	_, err := s.pool.Exec(ctx,
		`CREATE INDEX IF NOT EXISTS idx_legacy_probe ON legacy_probe (profile_email, ts DESC)`)
	if err == nil {
		t.Fatal("CREATE INDEX IF NOT EXISTS skipped before resolving columns; the rename block may be reachable after all")
	}
	if !strings.Contains(err.Error(), "profile_email") {
		t.Fatalf("failed for an unexpected reason: %v", err)
	}
}
