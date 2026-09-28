package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func seedProjectIdentityRepairRows(t *testing.T, s *PgStore) {
	t.Helper()
	truncateTables(t, s)
	ctx := context.Background()
	rows := []ProjectIdentityRepairRow{
		{Agent: "codex", ProjectHash: "raw-shared-hash", ProjectName: "beta-worktree", GitRemoteURL: "https://git.example.test/team/beta.git", RepositoryID: "local:beta:0123456789abcdef", RepositoryName: "beta", RepoSubpath: ""},
		{Agent: "claude", ProjectHash: "raw-shared-hash", ProjectName: "alpha", GitRemoteURL: "https://git.example.test/team/alpha.git", RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha", RepoSubpath: "service/"},
	}
	for _, row := range rows {
		if _, err := s.pool.Exec(ctx, `INSERT INTO projects
			(agent, project_hash, project_name, git_remote_url, repository_id, repository_name, repo_subpath)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			row.Agent, row.ProjectHash, row.ProjectName, row.GitRemoteURL, row.RepositoryID, row.RepositoryName, row.RepoSubpath); err != nil {
			t.Fatalf("insert project %s/%s: %v", row.Agent, row.ProjectHash, err)
		}
	}
}

func TestProjectIdentityRepairSnapshotLoadsEveryProjectFieldInStableOrder(t *testing.T) {
	s := acquireTestStore(t)
	seedProjectIdentityRepairRows(t, s)
	ctx := context.Background()

	var got []ProjectIdentityRepairRow
	err := s.withProjectIdentityRepairReadOnlyTx(ctx, func(tx pgx.Tx) error {
		var err error
		got, err = loadProjectIdentityRepairSnapshot(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	want := []ProjectIdentityRepairRow{
		{Agent: "claude", ProjectHash: "raw-shared-hash", ProjectName: "alpha", GitRemoteURL: "https://git.example.test/team/alpha.git", RepositoryID: "git.example.test/team/alpha", RepositoryName: "alpha", RepoSubpath: "service/"},
		{Agent: "codex", ProjectHash: "raw-shared-hash", ProjectName: "beta-worktree", GitRemoteURL: "https://git.example.test/team/beta.git", RepositoryID: "local:beta:0123456789abcdef", RepositoryName: "beta", RepoSubpath: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot = %#v, want %#v", got, want)
	}
}

func TestProjectIdentityRepairHistoryLoadsExactScopeIdentityAggregates(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
		(ts, agent, project_hash, repository_id, repo_subpath)
		VALUES
		($1, ' claude ', ' same ', 'git.example.test/team/alpha', ''),
		($2, 'claude', 'same', 'git.example.test/team/alpha', 'web/'),
		($3, 'claude', 'same', 'git.example.test/team/alpha', 'web/')`,
		now, now.Add(time.Second), now.Add(2*time.Second)); err != nil {
		t.Fatalf("insert session identity history: %v", err)
	}

	var got []ProjectIdentityRepairHistoryRow
	err := s.withProjectIdentityRepairReadOnlyTx(ctx, func(tx pgx.Tx) error {
		var err error
		got, err = loadProjectIdentityRepairHistory(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatalf("load identity history: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("history row count = %d, want 2 exact scopes: %#v", len(got), got)
	}
	want := []ProjectIdentityRepairHistoryRow{
		{Agent: " claude ", ProjectHash: " same ", RepositoryID: "git.example.test/team/alpha", RepoSubpath: "", RecordCount: 1, FirstSeen: now, LastSeen: now},
		{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepoSubpath: "web/", RecordCount: 2, FirstSeen: now.Add(time.Second), LastSeen: now.Add(2 * time.Second)},
	}
	for index := range want {
		if got[index].Agent != want[index].Agent || got[index].ProjectHash != want[index].ProjectHash || got[index].RepositoryID != want[index].RepositoryID || got[index].RepoSubpath != want[index].RepoSubpath || got[index].RecordCount != want[index].RecordCount || !got[index].FirstSeen.Equal(want[index].FirstSeen) || !got[index].LastSeen.Equal(want[index].LastSeen) {
			t.Fatalf("history[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func TestProjectIdentityRepairHistorySnapshotIsOrderIndependent(t *testing.T) {
	rows := []ProjectIdentityRepairHistoryRow{
		{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepoSubpath: "", RecordCount: 2, FirstSeen: time.Unix(1, 0), LastSeen: time.Unix(2, 0)},
		{Agent: "claude", ProjectHash: "same", RepositoryID: "git.example.test/team/alpha", RepoSubpath: "web/", RecordCount: 3, FirstSeen: time.Unix(3, 0), LastSeen: time.Unix(4, 0)},
	}
	reversed := []ProjectIdentityRepairHistoryRow{rows[1], rows[0]}
	first, second := projectIdentityRepairHistorySnapshot(rows), projectIdentityRepairHistorySnapshot(reversed)
	if first != second {
		t.Fatalf("history checksum depends on order: first=%+v second=%+v", first, second)
	}
	if first.AggregateCount != 2 || first.RecordCount != 5 || first.Checksum == "" {
		t.Fatalf("history snapshot = %+v, want cardinality and checksum", first)
	}
}

func TestProjectIdentityRepairTransactionRejectsDML(t *testing.T) {
	s := acquireTestStore(t)
	seedProjectIdentityRepairRows(t, s)
	ctx := context.Background()

	err := s.withProjectIdentityRepairReadOnlyTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO projects (agent, project_hash) VALUES ('claude', 'must-not-write')`)
		return err
	})
	if err == nil {
		t.Fatal("DML succeeded in project identity repair transaction")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "25006" {
		t.Fatalf("DML error = %v, want SQLSTATE 25006 read_only_sql_transaction", err)
	}
}

func TestDryRunProjectIdentityRepairIsStableAndDoesNotChangeRows(t *testing.T) {
	s := acquireTestStore(t)
	seedProjectIdentityRepairRows(t, s)
	ctx := context.Background()

	before := projectRowsJSON(t, s)
	first, err := s.DryRunProjectIdentityRepair(ctx)
	if err != nil {
		t.Fatalf("first dry-run: %v", err)
	}
	second, err := s.DryRunProjectIdentityRepair(ctx)
	if err != nil {
		t.Fatalf("second dry-run: %v", err)
	}
	after := projectRowsJSON(t, s)

	if before != after {
		t.Fatalf("projects changed across dry-run\nbefore: %s\nafter:  %s", before, after)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated dry-run changed output\nfirst:  %#v\nsecond: %#v", first, second)
	}
	if !first.ReadOnlyVerified || !first.SnapshotConsistent || first.WriteQueries != 0 {
		t.Fatalf("dry-run safety evidence = %+v", first)
	}
	if first.SnapshotBefore != first.SnapshotAfter || first.SnapshotBefore.RowCount != 2 || first.SnapshotBefore.Checksum == "" {
		t.Fatalf("snapshot evidence = before:%+v after:%+v", first.SnapshotBefore, first.SnapshotAfter)
	}
	if len(first.Results) != 2 {
		t.Fatalf("result count = %d, want 2", len(first.Results))
	}
	for _, result := range first.Results {
		if result.ScopeFingerprint == "" {
			t.Fatalf("result has no sanitized scope fingerprint: %+v", result)
		}
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("encode dry-run report: %v", err)
	}
	for _, forbidden := range []string{"raw-shared-hash", "https://git.example.test/team/alpha.git", "https://git.example.test/team/beta.git", "local:beta:0123456789abcdef"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report exposes raw source value %q: %s", forbidden, encoded)
		}
	}
}

func TestProjectIdentityRepairSnapshotChecksumIsOrderIndependentForDuplicateScopes(t *testing.T) {
	rows := []ProjectIdentityRepairRow{
		{Agent: "claude", ProjectHash: "duplicate", ProjectName: "alpha", RepositoryID: "git.example.test/team/alpha"},
		{Agent: "claude", ProjectHash: "duplicate", ProjectName: "beta", RepositoryID: "git.example.test/team/beta"},
	}
	reversed := []ProjectIdentityRepairRow{rows[1], rows[0]}

	if first, second := projectIdentityRepairSnapshot(rows), projectIdentityRepairSnapshot(reversed); first != second {
		t.Fatalf("snapshot checksum depends on duplicate-scope row order: first=%+v second=%+v", first, second)
	}
}

func projectRowsJSON(t *testing.T, s *PgStore) string {
	t.Helper()
	var rows string
	if err := s.pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY agent, project_hash), '[]'::jsonb)::text FROM projects p`).Scan(&rows); err != nil {
		t.Fatalf("snapshot project rows: %v", err)
	}
	return rows
}

func seedProjectIdentityRepairApplyRows(t *testing.T, s *PgStore) {
	t.Helper()
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO projects
		(agent, project_hash, project_name, git_remote_url, repository_id, repository_name, repo_subpath)
		VALUES
		('claude', 'h-local-row', 'alpha-leaf', '', 'local:alpha-leaf:0123456789abcdef', 'alpha-leaf', 'web/'),
		('claude', 'h-url-form', 'beta', '', 'https://example.test/team/beta.git', 'beta', ''),
		('claude', 'h-name-drift', 'seaslug', '', 'example.test/team/gamma', 'seaslug', ''),
		('claude', 'h-canonical', 'delta', '', 'example.test/team/delta', 'delta', 'service/'),
		('codex', 'h-local-only', 'local-project', '', 'local:local-project:0123456789abcdef', 'local-project', ''),
		('claude', 'h-conflict', 'eps', 'https://example.test/team/eps.git', 'example.test/team/zeta', 'zeta', '')`); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	now := time.Now().UTC()
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
		(ts, agent, project_hash, repository_id, repo_subpath)
		VALUES
		($1, 'claude', 'h-local-row', 'example.test/team/alpha', ''),
		($2, 'claude', 'h-local-row', 'example.test/team/alpha', 'web/'),
		($3, 'claude', 'h-history-only', 'example.test/team/eta', '')`, now, now.Add(time.Second), now.Add(2*time.Second)); err != nil {
		t.Fatalf("seed session identity history: %v", err)
	}
}

type projectIdentityRepairStoredRow struct {
	repositoryID, repositoryName, repoSubpath string
}

func projectIdentityRepairStoredRows(t *testing.T, s *PgStore) map[string]projectIdentityRepairStoredRow {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT agent, project_hash, repository_id, repository_name, repo_subpath FROM projects`)
	if err != nil {
		t.Fatalf("read projects: %v", err)
	}
	defer rows.Close()
	got := make(map[string]projectIdentityRepairStoredRow)
	for rows.Next() {
		var agent, hash string
		var row projectIdentityRepairStoredRow
		if err := rows.Scan(&agent, &hash, &row.repositoryID, &row.repositoryName, &row.repoSubpath); err != nil {
			t.Fatalf("scan projects: %v", err)
		}
		got[agent+"/"+hash] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate projects: %v", err)
	}
	return got
}

func TestApplyProjectIdentityRepairWritesOnlyRecoverableRows(t *testing.T) {
	s := acquireTestStore(t)
	seedProjectIdentityRepairApplyRows(t, s)
	ctx := context.Background()
	historyBefore := sessionRecordIdentityJSON(t, s)

	report, err := s.ApplyProjectIdentityRepair(ctx)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	want := map[string]projectIdentityRepairStoredRow{
		// The split row #382 describes: a local fallback whose sessions carry the
		// remote. Its subpath is left as stored.
		"claude/h-local-row":  {"example.test/team/alpha", "alpha", "web/"},
		"claude/h-url-form":   {"example.test/team/beta", "beta", ""},
		"claude/h-name-drift": {"example.test/team/gamma", "gamma", ""},
		"claude/h-canonical":  {"example.test/team/delta", "delta", "service/"},
		"codex/h-local-only":  {"local:local-project:0123456789abcdef", "local-project", ""},
		"claude/h-conflict":   {"example.test/team/zeta", "zeta", ""},
	}
	if got := projectIdentityRepairStoredRows(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("projects after apply = %#v, want %#v", got, want)
	}
	if after := sessionRecordIdentityJSON(t, s); after != historyBefore {
		t.Fatalf("apply changed session_records\nbefore: %s\nafter:  %s", historyBefore, after)
	}
	if report.Mode != "apply" || report.ReadOnlyVerified || report.WriteQueries != 3 {
		t.Fatalf("apply report = mode %q read_only %v writes %d, want apply/false/3", report.Mode, report.ReadOnlyVerified, report.WriteQueries)
	}
	// The history-only scope is recoverable but has no projects row to write.
	if report.Counts.Recoverable != 4 || report.Counts.AlreadyCanonical != 1 || report.Counts.LocalOnly != 1 || report.Counts.ConflictingRemote != 1 {
		t.Fatalf("apply counts = %+v", report.Counts)
	}
	if report.SnapshotBefore == report.SnapshotAfter {
		t.Fatalf("snapshot after apply equals before: %+v", report.SnapshotAfter)
	}
}

func TestApplyProjectIdentityRepairIsIdempotent(t *testing.T) {
	s := acquireTestStore(t)
	seedProjectIdentityRepairApplyRows(t, s)
	ctx := context.Background()

	if _, err := s.ApplyProjectIdentityRepair(ctx); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	afterFirst := projectRowsJSON(t, s)
	second, err := s.ApplyProjectIdentityRepair(ctx)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if second.WriteQueries != 0 {
		t.Fatalf("second apply wrote %d rows, want 0", second.WriteQueries)
	}
	if second.SnapshotBefore != second.SnapshotAfter {
		t.Fatalf("second apply changed the snapshot: before %+v after %+v", second.SnapshotBefore, second.SnapshotAfter)
	}
	if afterSecond := projectRowsJSON(t, s); afterSecond != afterFirst {
		t.Fatalf("second apply changed projects\nfirst:  %s\nsecond: %s", afterFirst, afterSecond)
	}
}

func TestApplyProjectIdentityRepairRollsBackEveryRowOnFailure(t *testing.T) {
	s := acquireTestStore(t)
	seedProjectIdentityRepairApplyRows(t, s)
	ctx := context.Background()
	// Writes run in (agent, project_hash) order, so h-url-form is the last one:
	// h-local-row and h-name-drift are already updated inside the transaction
	// when it fails.
	if _, err := s.pool.Exec(ctx, `CREATE OR REPLACE FUNCTION project_identity_repair_test_fail() RETURNS trigger AS $$
		BEGIN
			IF NEW.project_hash = 'h-url-form' THEN RAISE EXCEPTION 'forced repair failure'; END IF;
			RETURN NEW;
		END $$ LANGUAGE plpgsql`); err != nil {
		t.Fatalf("create failing trigger function: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `CREATE TRIGGER project_identity_repair_test_fail BEFORE UPDATE ON projects
		FOR EACH ROW EXECUTE FUNCTION project_identity_repair_test_fail()`); err != nil {
		t.Fatalf("create failing trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS project_identity_repair_test_fail ON projects`)
		_, _ = s.pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS project_identity_repair_test_fail()`)
	})
	before := projectRowsJSON(t, s)

	if _, err := s.ApplyProjectIdentityRepair(ctx); err == nil || !strings.Contains(err.Error(), "forced repair failure") {
		t.Fatalf("apply error = %v, want the forced failure", err)
	}
	if after := projectRowsJSON(t, s); after != before {
		t.Fatalf("failed apply left partial writes\nbefore: %s\nafter:  %s", before, after)
	}
}

func sessionRecordIdentityJSON(t *testing.T, s *PgStore) string {
	t.Helper()
	var rows string
	if err := s.pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(jsonb_build_array(agent, project_hash, repository_id, repo_subpath) ORDER BY agent, project_hash, repository_id, repo_subpath), '[]'::jsonb)::text FROM session_records`).Scan(&rows); err != nil {
		t.Fatalf("snapshot session identity history: %v", err)
	}
	return rows
}
