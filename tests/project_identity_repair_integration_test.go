package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cctrace/internal/containertest"
	"cctrace/internal/store"

	"github.com/jackc/pgx/v5"
	tc "github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestProjectIdentityRepairBinaryDryRunIsReadOnlyApplyIsIdempotentAndNeitherBootsServer(t *testing.T) {
	ensureSyncDockerHost()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const passwordMarker = "phase3-secret-marker"
	ctr, err := pgmod.Run(ctx,
		"timescale/timescaledb:latest-pg16",
		pgmod.WithDatabase("repairdb"),
		pgmod.WithUsername("repair_operator"),
		pgmod.WithPassword(passwordMarker),
		tc.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		containertest.SkipOrFail(t, "postgres container", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get postgres connection string: %v", err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect fixture database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	if _, err := conn.Exec(ctx, `CREATE TABLE projects (
		agent TEXT NOT NULL,
		project_hash TEXT NOT NULL,
		project_name TEXT NOT NULL,
		git_remote_url TEXT NOT NULL,
		repository_id TEXT NOT NULL,
		repository_name TEXT NOT NULL,
		repo_subpath TEXT NOT NULL,
		PRIMARY KEY (agent, project_hash)
	)`); err != nil {
		t.Fatalf("create projects fixture: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE session_records (
		id BIGSERIAL,
		ts TIMESTAMPTZ NOT NULL,
		agent TEXT NOT NULL,
		project_hash TEXT NOT NULL,
		repository_id TEXT NOT NULL,
		repo_subpath TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create session_records fixture: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO projects
		(agent, project_hash, project_name, git_remote_url, repository_id, repository_name, repo_subpath)
		VALUES
		('claude', 'raw-remote-hash', 'remote-project', 'https://git.example.test/team/remote.git', 'git.example.test/team/remote', 'remote', ''),
		('claude', 'raw-canonical-hash', 'canonical-project', 'https://git.example.test/team/canonical.git', 'git.example.test/team/canonical', 'canonical', 'service/'),
		('codex', 'raw-local-hash', 'local-project', '', 'local:local-project:0123456789abcdef', 'local-project', ''),
		('claude', 'raw-split-hash', 'split-leaf', '', 'local:split-leaf:0123456789abcdef', 'split-leaf', 'web/')`); err != nil {
		t.Fatalf("seed projects fixture: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO session_records
		(ts, agent, project_hash, repository_id, repo_subpath)
		VALUES
		(now() - interval '2 minutes', 'claude', 'raw-remote-hash', 'git.example.test/team/remote', ''),
		(now() - interval '1 minute', 'claude', 'raw-remote-hash', 'git.example.test/team/remote', 'web/'),
		(now(), 'claude', 'raw-canonical-hash', 'git.example.test/team/canonical', 'service/'),
		(now(), 'claude', 'raw-split-hash', 'git.example.test/team/split', '')`); err != nil {
		t.Fatalf("seed session identity history: %v", err)
	}
	before := projectIdentityRowsJSON(t, ctx, conn)
	beforeHistory := projectIdentityHistoryRowsJSON(t, ctx, conn)

	bin := filepath.Join(t.TempDir(), "cctraced")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", bin, "./cmd/cctraced")
	build.Dir = rootDir(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cctraced: %v\n%s", err, output)
	}

	fakeHome := t.TempDir()
	command := exec.CommandContext(ctx, bin, "project-identity-repair", "--dry-run")
	command.Env = projectIdentityRepairCommandEnv(dsn, fakeHome)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run cctraced project identity repair: %v\n%s", err, output)
	}

	var payload struct {
		BinaryVersion string                             `json:"binary_version"`
		Report        *store.ProjectIdentityRepairReport `json:"report"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		t.Fatalf("decode project identity repair output: %v\n%s", err, output)
	}
	if payload.Report == nil || !payload.Report.ReadOnlyVerified || !payload.Report.SnapshotConsistent {
		t.Fatalf("missing read-only snapshot evidence: %+v", payload.Report)
	}
	if payload.Report.SchemaVersion != 3 || payload.Report.SourceRelation != "projects" || payload.Report.HistorySourceRelation != "session_records" || payload.Report.SnapshotBefore.Checksum == "" || payload.Report.HistorySnapshotBefore.Checksum == "" {
		t.Fatalf("missing versioned source evidence: %+v", payload.Report)
	}
	if payload.Report.HistorySnapshotBefore != payload.Report.HistorySnapshotAfter || payload.Report.HistorySnapshotBefore.AggregateCount != 4 || payload.Report.HistorySnapshotBefore.RecordCount != 4 {
		t.Fatalf("missing history snapshot evidence: %+v", payload.Report)
	}
	if payload.Report.WriteQueries != 0 || payload.Report.Counts.Recoverable != 1 || payload.Report.Counts.AlreadyCanonical != 2 || payload.Report.Counts.LocalOnly != 1 {
		t.Fatalf("unexpected dry-run report: %+v", payload.Report)
	}
	if after := projectIdentityRowsJSON(t, ctx, conn); before != after {
		t.Fatalf("projects changed across binary dry-run\nbefore: %s\nafter:  %s", before, after)
	}
	if after := projectIdentityHistoryRowsJSON(t, ctx, conn); beforeHistory != after {
		t.Fatalf("session identity history changed across binary dry-run\nbefore: %s\nafter:  %s", beforeHistory, after)
	}

	var dashboardUsersExists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.dashboard_users') IS NOT NULL`).Scan(&dashboardUsersExists); err != nil {
		t.Fatalf("check migration side effects: %v", err)
	}
	if dashboardUsersExists {
		t.Fatal("dashboard_users exists: dry-run command ran server migrations")
	}
	if _, err := os.Stat(filepath.Join(fakeHome, ".cctrace", "cctraced.pid")); !os.IsNotExist(err) {
		t.Fatalf("server PID file created during dry-run: %v", err)
	}
	// The dry-run returns before the startup block, so it takes neither lock. A
	// lock file here would mean the command reached the point where a second
	// cctraced would be refused.
	for _, name := range []string{"cctraced.lock", "cctraced.control.lock"} {
		if _, err := os.Stat(filepath.Join(fakeHome, ".cctrace", name)); !os.IsNotExist(err) {
			t.Fatalf("%s created during dry-run: %v", name, err)
		}
	}
	for _, forbidden := range []string{passwordMarker, "postgres://", "raw-remote-hash", "raw-local-hash"} {
		if strings.Contains(string(output), forbidden) {
			t.Fatalf("dry-run output exposes %q: %s", forbidden, output)
		}
	}

	// --apply writes the one recoverable row, and only its identity columns.
	runApply := func() *store.ProjectIdentityRepairReport {
		t.Helper()
		command := exec.CommandContext(ctx, bin, "project-identity-repair", "--apply")
		command.Env = projectIdentityRepairCommandEnv(dsn, fakeHome)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("run cctraced project identity repair --apply: %v\n%s", err, output)
		}
		for _, forbidden := range []string{passwordMarker, "postgres://", "raw-split-hash"} {
			if strings.Contains(string(output), forbidden) {
				t.Fatalf("apply output exposes %q: %s", forbidden, output)
			}
		}
		var payload struct {
			Report *store.ProjectIdentityRepairReport `json:"report"`
		}
		if err := json.Unmarshal(output, &payload); err != nil || payload.Report == nil {
			t.Fatalf("decode apply output: %v\n%s", err, output)
		}
		return payload.Report
	}
	first := runApply()
	if first.Mode != "apply" || first.WriteQueries != 1 || first.Counts.Recoverable != 1 {
		t.Fatalf("first apply report: %+v", first)
	}
	var repositoryID, repositoryName, repoSubpath string
	if err := conn.QueryRow(ctx, `SELECT repository_id, repository_name, repo_subpath FROM projects
		WHERE agent = 'claude' AND project_hash = 'raw-split-hash'`).Scan(&repositoryID, &repositoryName, &repoSubpath); err != nil {
		t.Fatalf("read repaired row: %v", err)
	}
	if repositoryID != "git.example.test/team/split" || repositoryName != "split" || repoSubpath != "web/" {
		t.Fatalf("repaired row = (%q, %q, %q), want the remote identity with the subpath untouched", repositoryID, repositoryName, repoSubpath)
	}
	afterFirst := projectIdentityRowsJSON(t, ctx, conn)
	second := runApply()
	if second.WriteQueries != 0 || second.Counts.Recoverable != 0 || second.Counts.AlreadyCanonical != 3 {
		t.Fatalf("second apply report: %+v", second)
	}
	if afterSecond := projectIdentityRowsJSON(t, ctx, conn); afterSecond != afterFirst {
		t.Fatalf("second apply changed projects\nfirst:  %s\nsecond: %s", afterFirst, afterSecond)
	}
	if after := projectIdentityHistoryRowsJSON(t, ctx, conn); beforeHistory != after {
		t.Fatalf("session identity history changed across apply\nbefore: %s\nafter:  %s", beforeHistory, after)
	}
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.dashboard_users') IS NOT NULL`).Scan(&dashboardUsersExists); err != nil || dashboardUsersExists {
		t.Fatalf("apply ran server migrations: exists=%v err=%v", dashboardUsersExists, err)
	}
	for _, name := range []string{"cctraced.pid", "cctraced.lock", "cctraced.control.lock"} {
		if _, err := os.Stat(filepath.Join(fakeHome, ".cctrace", name)); !os.IsNotExist(err) {
			t.Fatalf("%s created during apply: %v", name, err)
		}
	}
}

func projectIdentityRowsJSON(t *testing.T, ctx context.Context, conn *pgx.Conn) string {
	t.Helper()
	var rows string
	if err := conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY agent, project_hash), '[]'::jsonb)::text FROM projects p`).Scan(&rows); err != nil {
		t.Fatalf("snapshot project rows: %v", err)
	}
	return rows
}

func projectIdentityHistoryRowsJSON(t *testing.T, ctx context.Context, conn *pgx.Conn) string {
	t.Helper()
	var rows string
	if err := conn.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY agent, project_hash, repository_id, repo_subpath, ts), '[]'::jsonb)::text FROM session_records s`).Scan(&rows); err != nil {
		t.Fatalf("snapshot session identity history: %v", err)
	}
	return rows
}

func projectIdentityRepairCommandEnv(dsn, fakeHome string) []string {
	blocked := map[string]struct{}{
		"CCTRACE_ALLOW_EMPTY_DASHBOARD": {},
		"DATABASE_URL":                  {},
		"HOME":                          {},
		"JWT_SECRET":                    {},
		"USERPROFILE":                   {},
	}
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, found := blocked[key]; !found {
			env = append(env, entry)
		}
	}
	return append(env,
		fmt.Sprintf("DATABASE_URL=%s", dsn),
		"HOME="+fakeHome,
		"USERPROFILE="+fakeHome,
	)
}
