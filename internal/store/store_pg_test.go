package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"cctrace/internal/containertest"
	"cctrace/internal/insights"
)

func TestMain(m *testing.M) {
	ensureDockerHost()
	os.Exit(m.Run())
}

// inVMDockerSocket is the path Ryuk mounts to reach the Docker API from inside
// a container. It is deliberately NOT the DOCKER_HOST path: under Colima,
// DOCKER_HOST points at a socket on the macOS side
// (~/.colima/default/docker.sock) that cannot be bind-mounted into a container
// running in the VM ("operation not supported"). The daemon inside the VM
// exposes the same API at the standard path, so that is what Ryuk gets.
//
// Setting these to the same value is what previously made Ryuk fail to start,
// which is why the reaper used to be disabled and test containers accumulated.
const inVMDockerSocket = "/var/run/docker.sock"

// ensureDockerHost sets DOCKER_HOST for Colima if not already configured.
func ensureDockerHost() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	sock := filepath.Join(home, ".colima", "default", "docker.sock")
	if _, err := os.Stat(sock); err != nil {
		return
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		os.Setenv("DOCKER_HOST", "unix://"+sock)
	} else if host != "unix://"+sock {
		// Pointing somewhere else entirely (remote daemon, Docker Desktop):
		// leave the reaper mount alone, the default is right for that setup.
		return
	}
	// Set this even when DOCKER_HOST was already exported. The testing guide
	// tells developers to export it, and that path used to skip the override,
	// which put the un-mountable macOS socket back in front of Ryuk.
	if os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE") == "" {
		os.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", inVMDockerSocket)
	}
}

// The testing guide tells developers to export DOCKER_HOST. That path used to
// return before setting the reaper's mount path, which put the un-mountable
// macOS-side socket back in front of Ryuk — the container then failed to start
// and every DB test silently skipped while the package still reported ok.
func TestEnsureDockerHost_SetsReaperMountEvenWhenDockerHostPreset(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	sock := filepath.Join(home, ".colima", "default", "docker.sock")
	if _, err := os.Stat(sock); err != nil {
		t.Skip("colima socket not present; nothing to configure")
	}

	t.Setenv("DOCKER_HOST", "unix://"+sock)
	t.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "")

	ensureDockerHost()

	if got := os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE"); got != inVMDockerSocket {
		t.Errorf("reaper mount path = %q, want %q — Ryuk cannot mount the macOS-side socket", got, inVMDockerSocket)
	}
}

// An explicit override from the environment wins: a developer pointing the
// reaper somewhere deliberate must not be overwritten.
func TestEnsureDockerHost_KeepsExplicitReaperMount(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if _, err := os.Stat(filepath.Join(home, ".colima", "default", "docker.sock")); err != nil {
		t.Skip("colima socket not present; nothing to configure")
	}

	t.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/custom/docker.sock")
	ensureDockerHost()

	if got := os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE"); got != "/custom/docker.sock" {
		t.Errorf("explicit override was replaced with %q", got)
	}
}

// --- Shared container setup ---

var (
	pgOnce      sync.Once
	pgShared    *PgStore
	pgSharedDSN string
	pgInitErr   error
)

func acquireTestStore(t *testing.T) *PgStore {
	t.Helper()
	pgOnce.Do(func() {
		ctx := context.Background()
		ctr, err := pgmod.Run(ctx,
			"timescale/timescaledb:latest-pg16",
			pgmod.WithDatabase("testdb"),
			pgmod.WithUsername("test"),
			pgmod.WithPassword("test"),
			// The module already runs postgres with fsync=off; these drop the rest of
			// the per-commit durability work. The container is thrown away, never
			// restarted, and no test depends on crash recovery.
			tc.WithCmdArgs("-c", "synchronous_commit=off", "-c", "full_page_writes=off"),
			tc.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			pgInitErr = err
			return
		}
		dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			pgInitErr = err
			return
		}
		pgSharedDSN = dsn
		s, err := NewPgStore(ctx, dsn)
		if err != nil {
			pgInitErr = err
			return
		}
		s.SetTaskClassifier(insights.KeywordClassifier{})
		if err := s.Migrate(ctx); err != nil {
			pgInitErr = err
			return
		}
		if err := s.BackfillSessionOverviewRollups(ctx); err != nil {
			pgInitErr = err
			return
		}
		pgShared = s
	})
	if pgInitErr != nil {
		containertest.SkipOrFail(t, "postgres container", pgInitErr)
	}
	return pgShared
}

// truncateTables empties every table a test can leave rows in.
//
// It no longer issues TRUNCATE. The reset runs before almost every test (~490
// calls), and each TRUNCATE hands the table, its indexes and its TOAST a new
// relfilenode; measured on the migrated schema that cost ~300 ms per reset, over
// half the package's wall time. Every table here holds a handful of rows by the
// time the reset runs, so a DELETE of all of them in ONE transaction does the
// same job in a fraction of the time.
//
// What TRUNCATE ... CASCADE did, and how this keeps it:
//   - CASCADE also empties tables that reference these through a foreign key
//     (dashboard_api_tokens, the ai_report_* tables, ...). resetTargets walks
//     pg_constraint from the list below and deletes that same closure, so a table
//     added later with an FK to one of these is reached without editing the list.
//   - TRUNCATE does not fire row triggers or check FKs row by row. The DELETEs run
//     with session_replication_role = replica for the same effect: no cascade
//     ordering to get right, and since the whole FK closure is emptied in the one
//     transaction, nothing dangles at commit. That setting needs a superuser,
//     which the container's test role is; if it ever is not, the SET fails loudly.
//   - Plain TRUNCATE does not reset sequences, and neither does DELETE.
//   - The one difference: TRUNCATE on a hypertable dropped its chunks, DELETE
//     leaves them empty. No test counts chunks, and the retention test asserts
//     on rows (run_job still drops the old chunk it creates).
func truncateTables(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	targets := resetTargets(t, s)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("reset: begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// One round trip: without arguments pgx sends this as a simple query, which
	// runs every statement in order inside the open transaction.
	stmts := "SET LOCAL session_replication_role = replica;"
	for _, table := range targets {
		stmts += " DELETE FROM " + table + ";"
	}
	if _, err := tx.Exec(ctx, stmts); err != nil {
		t.Fatalf("reset: delete from %v: %v", targets, err)
	}
	// Their markers go with them. Emptying the table while leaving the marker
	// says "built, and there is nothing in it", which reads as a real zero.
	// schema_backfills itself is not truncated: the session-overview backfill runs
	// once per container in acquireTestStore and must not be re-triggered.
	if _, err := tx.Exec(ctx,
		`DELETE FROM schema_backfills WHERE name = ANY($1::text[])`,
		[]string{usageHourlyRollupBackfill, usageHourlyRollupWeeklyBackfill, coverageMinutesBackfill},
	); err != nil {
		t.Fatalf("clear aggregate backfill markers: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("reset: commit: %v", err)
	}
}

var (
	resetOnce        sync.Once
	resetTargetNames []string
	resetErr         error
)

// resetTargets is resetTables plus every table that reaches one of them through
// a foreign key, transitively -- the set TRUNCATE ... CASCADE would empty. It is
// read from the catalog once per container; the schema does not change after
// acquireTestStore migrates it.
func resetTargets(t *testing.T, s *PgStore) []string {
	t.Helper()
	resetOnce.Do(func() {
		rows, err := s.pool.Query(context.Background(), `
			WITH RECURSIVE reach(oid) AS (
				SELECT unnest($1::text[])::regclass::oid
				UNION
				SELECT c.conrelid FROM pg_constraint c JOIN reach r ON c.confrelid = r.oid
				 WHERE c.contype = 'f'
			)
			SELECT oid::regclass::text FROM reach`, resetTables)
		if err != nil {
			resetErr = err
			return
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				resetErr = err
				return
			}
			resetTargetNames = append(resetTargetNames, name)
		}
		resetErr = rows.Err()
	})
	if resetErr != nil {
		t.Fatalf("reset: read foreign-key closure: %v", resetErr)
	}
	return resetTargetNames
}

// resetTables are the tables truncateTables empties (with everything that
// references them).
var resetTables = []string{"project_rule_comments", "project_rule_versions", "project_rules", "otel_events", "otel_metrics", "session_records", "dashboard_users", "projects", "user_aliases", "privacy_settings", "quota_snapshots", "quota_samples", "client_versions", "retention_settings", "excluded_accounts", "excluded_billing_accounts", "excluded_billing_links", "excluded_codex_metric_profiles", "excluded_codex_metric_minutes", "flat_rate_models",
	"excluded_sessions", "excluded_sessions_refresh_state",
	// Deletion state. deletion_policy is truncated too: GetDeletionPolicy answers
	// a missing row with the seeded default, so an emptied table is the same as a
	// fresh install rather than a broken one.
	"deleted_sessions", "blocked_projects", "deletion_policy",
	// No row means no admin override, so an emptied table is a fresh install.
	"ai_settings", "ai_runtime_settings", "ai_provider_credentials",
	// The write-maintained aggregates and retention watermark. Derived, but a test
	// that leaves rows here hands the next one sessions it never seeded.
	"session_overview_rollups", "session_overview_retention_state", "plugin_invocation_facts",
	// The imputed-cost tables are derived, but they are also read by
	// unified_events, so rows left behind by one test surface as another test's
	// sessions. claude_imputed_cost had the same hole and never showed it --
	// no test called its refresh, so it was always empty anyway.
	"claude_imputed_cost", "codex_imputed_cost",
	// The time-bucketed aggregates and the markers that say they are built.
	// Both are read WITHOUT a refresh -- coverage falls back to raw events only
	// while the marker is absent -- so a test that leaves either behind hands
	// the next one another test's numbers under a marker claiming they are
	// current. That is order-dependent, and it fails in the direction that
	// looks like a real result.
	"usage_hourly_rollups", "coverage_measured_minutes",
	// A queued rebuild left behind would read as pending in the next test.
	"usage_rollup_rebuild_requests",
	// The derived rate table and its cursor. Left behind, its buckets price
	// the next test's offline rows and its cursor makes the refresh skip
	// that test's own events -- both fail as plausible-looking numbers.
	"model_rate_buckets", "model_rate_cursor",
	// Repair staging and the state rows they cascade from. The session sweep
	// reaches them now (#396), so rows one test leaves behind show up in the
	// next as "the cleanup reached an unrelated session" -- a failure that
	// points at the sweep and is really about the fixture.
	"login_email_history_session_intervals", "login_email_history_repair_state",
	"codex_login_email_repair_candidates", "codex_login_email_repair_state"}

// refreshCodexImputed materialises codex cost so unified_events can see it.
//
// Codex cost is no longer derived when the view is read -- it is computed into
// codex_imputed_cost and read from there (#245). Production runs this on a 10s
// tick; a test that inserts codex usage records and reads them back in the same
// breath has to stand in for that tick itself.
func refreshCodexImputed(t *testing.T, s *PgStore) {
	t.Helper()
	if err := s.RefreshCodexImputedCost(context.Background()); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
}

// --- Helper constructors ---

func ptrFloat(f float64) *float64 { return &f }
func ptrInt(i int) *int           { return &i }
func ptrBool(b bool) *bool        { return &b }

// --- InsertEvent / ListEvents ---

func TestPgStore_InsertAndListEvent(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	e := &OtelEvent{
		Ts:           time.Now().UTC().Truncate(time.Millisecond),
		EventName:    "api_request",
		SessionID:    "ses-001",
		PromptID:     "pmt-001",
		UserID:       "uid-001",
		ProfileEmail: "alice@example.com",
		UserTeam:     "eng",
		OrgID:        "org-001",
		Model:        "claude-3",
		CostUSD:      ptrFloat(0.05),
		InputTokens:  ptrInt(100),
		OutputTokens: ptrInt(50),
		Attrs:        map[string]interface{}{"extra": "data"},
	}

	if err := s.InsertEvent(ctx, e); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	events, err := s.ListEvents(ctx, EventFilter{SessionID: "ses-001"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	got := events[0]
	if got.EventName != "api_request" {
		t.Errorf("EventName: got %q, want %q", got.EventName, "api_request")
	}
	if got.SessionID != "ses-001" {
		t.Errorf("SessionID: got %q", got.SessionID)
	}
	if got.ProfileEmail != "alice@example.com" {
		t.Errorf("ProfileEmail: got %q", got.ProfileEmail)
	}
	if got.CostUSD == nil || *got.CostUSD != 0.05 {
		t.Errorf("CostUSD: got %v", got.CostUSD)
	}
	if got.InputTokens == nil || *got.InputTokens != 100 {
		t.Errorf("InputTokens: got %v", got.InputTokens)
	}
	if got.Attrs["extra"] != "data" {
		t.Errorf("Attrs: got %v", got.Attrs)
	}
}

func TestPgStore_InsertEvents_Batch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	events := []*OtelEvent{
		{Ts: now, EventName: "api_request", SessionID: "batch-ses", ProfileEmail: "a@b.com", Model: "claude-3", CostUSD: ptrFloat(0.01)},
		{Ts: now.Add(time.Second), EventName: "tool_result", SessionID: "batch-ses", ToolName: "Read", ToolSuccess: ptrBool(true)},
		{Ts: now.Add(2 * time.Second), EventName: "user_prompt", SessionID: "batch-ses"},
	}

	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	all, err := s.ListEvents(ctx, EventFilter{SessionID: "batch-ses", Limit: 10})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 events, got %d", len(all))
	}
}

func TestPgStore_UsageAggregatesByModelAndOwner(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	start := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if err := s.InsertEvents(ctx, []*OtelEvent{
		{Ts: start, EventName: "api_request", SessionID: "alice-1", UserID: "alice", Model: "sonnet", InputTokens: ptrInt(10), OutputTokens: ptrInt(4)},
		{Ts: start.Add(90 * time.Second), EventName: "api_request", SessionID: "alice-1", UserID: "alice", Model: "opus", InputTokens: ptrInt(6), OutputTokens: ptrInt(2)},
		{Ts: start.Add(time.Minute), EventName: "api_request", SessionID: "bob-1", UserID: "bob", Model: "opus", InputTokens: ptrInt(99), OutputTokens: ptrInt(99)},
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: start.Add(-time.Hour), SessionID: "alice-1", ProjectHash: "project-a", RecordType: "user",
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	since, until := start.Add(-time.Second), start.Add(2*time.Minute)
	got, err := s.UsageAggregates(ctx, SessionOverviewFilter{
		UserID: "alice", Since: &since, Until: &until, ProjectHashes: []string{"project-a"},
	})
	if err != nil {
		t.Fatalf("UsageAggregates: %v", err)
	}
	if got.SessionCount != 1 || got.InputTokens != 16 || got.OutputTokens != 6 || got.WorkTimeSeconds != 90 {
		t.Fatalf("totals = %+v", got)
	}
	if len(got.ByModel) != 2 {
		t.Fatalf("by model = %+v, want two models", got.ByModel)
	}
	if got.ByModel[0].Model != "opus" || got.ByModel[0].InputTokens != 6 || got.ByModel[0].OutputTokens != 2 {
		t.Fatalf("opus aggregate = %+v", got.ByModel[0])
	}
	if got.ByModel[1].Model != "sonnet" || got.ByModel[1].InputTokens != 10 || got.ByModel[1].OutputTokens != 4 {
		t.Fatalf("sonnet aggregate = %+v", got.ByModel[1])
	}
}

func TestPgStore_InsertEvents_Empty(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	// Should be a no-op with no error
	if err := s.InsertEvents(ctx, nil); err != nil {
		t.Errorf("InsertEvents(nil): %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{}); err != nil {
		t.Errorf("InsertEvents([]): %v", err)
	}
}

// --- CountEvents ---

func TestPgStore_CountEvents(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	events := []*OtelEvent{
		{Ts: now, EventName: "api_request", ProfileEmail: "count@test.com", UserTeam: "alpha"},
		{Ts: now.Add(time.Second), EventName: "api_request", ProfileEmail: "count@test.com", UserTeam: "alpha"},
		{Ts: now.Add(2 * time.Second), EventName: "tool_result", ProfileEmail: "other@test.com"},
	}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	n, err := s.CountEvents(ctx, EventFilter{ProfileEmail: "count@test.com"})
	if err != nil {
		t.Fatalf("CountEvents: %v", err)
	}
	if n != 2 {
		t.Errorf("CountEvents by user: got %d, want 2", n)
	}

	n, err = s.CountEvents(ctx, EventFilter{EventName: "tool_result"})
	if err != nil {
		t.Fatalf("CountEvents by event_name: %v", err)
	}
	if n != 1 {
		t.Errorf("CountEvents by name: got %d, want 1", n)
	}

	n, err = s.CountEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("CountEvents all: %v", err)
	}
	if n != 3 {
		t.Errorf("CountEvents all: got %d, want 3", n)
	}
}

// --- ListEvents filters ---

func TestPgStore_ListEvents_FilterByUserEmail(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", ProfileEmail: "alice@example.com"},
		{Ts: now.Add(time.Second), EventName: "api_request", ProfileEmail: "bob@example.com"},
	})

	events, err := s.ListEvents(ctx, EventFilter{ProfileEmail: "alice@example.com"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].ProfileEmail != "alice@example.com" {
		t.Errorf("expected 1 alice event, got %d", len(events))
	}
}

func TestPgStore_ListEvents_FilterByTeam(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", UserTeam: "alpha"},
		{Ts: now.Add(time.Second), EventName: "api_request", UserTeam: "beta"},
		{Ts: now.Add(2 * time.Second), EventName: "tool_result", UserTeam: "alpha"},
	})

	events, err := s.ListEvents(ctx, EventFilter{UserTeam: "alpha"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 alpha events, got %d", len(events))
	}
}

func TestPgStore_ListEvents_FilterByEventName(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request"},
		{Ts: now.Add(time.Second), EventName: "tool_result"},
	})

	events, err := s.ListEvents(ctx, EventFilter{EventName: "tool_result"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].EventName != "tool_result" {
		t.Errorf("expected 1 tool_result event, got %d", len(events))
	}
}

func TestPgStore_ListEvents_FilterBySince(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(-2 * time.Hour), EventName: "api_request"},
		{Ts: base.Add(time.Hour), EventName: "api_request"},
	})

	since := base
	events, err := s.ListEvents(ctx, EventFilter{Since: &since})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event since base, got %d", len(events))
	}
}

func TestPgStore_ListEvents_FilterByUntil(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: base.Add(-2 * time.Hour), EventName: "api_request"},
		{Ts: base.Add(time.Hour), EventName: "api_request"},
	})

	until := base
	events, err := s.ListEvents(ctx, EventFilter{Until: &until})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event until base, got %d", len(events))
	}
}

func TestPgStore_ListEvents_LimitAndOffset(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	events := make([]*OtelEvent, 5)
	for i := range events {
		events[i] = &OtelEvent{
			Ts:           now.Add(time.Duration(i) * time.Second),
			EventName:    "api_request",
			ProfileEmail: "page@test.com",
		}
	}
	_ = s.InsertEvents(ctx, events)

	page1, err := s.ListEvents(ctx, EventFilter{ProfileEmail: "page@test.com", Limit: 3, Offset: 0})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 3 {
		t.Errorf("page1: expected 3, got %d", len(page1))
	}

	page2, err := s.ListEvents(ctx, EventFilter{ProfileEmail: "page@test.com", Limit: 3, Offset: 3})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 {
		t.Errorf("page2: expected 2, got %d", len(page2))
	}
}

// --- InsertMetric / ListMetrics ---

func TestPgStore_InsertAndListMetric(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	m := &OtelMetric{
		Ts:           time.Now().UTC().Truncate(time.Millisecond),
		MetricName:   "token_count",
		SessionID:    "metric-ses",
		ProfileEmail: "metrics@test.com",
		UserTeam:     "platform",
		Model:        "claude-3",
		ValueDouble:  ptrFloat(1.5),
		Dimensions:   map[string]interface{}{"region": "us-east"},
	}

	if err := s.InsertMetric(ctx, m); err != nil {
		t.Fatalf("InsertMetric: %v", err)
	}

	metrics, err := s.ListMetrics(ctx, MetricFilter{MetricName: "token_count"})
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(metrics))
	}

	got := metrics[0]
	if got.MetricName != "token_count" {
		t.Errorf("MetricName: got %q", got.MetricName)
	}
	if got.ProfileEmail != "metrics@test.com" {
		t.Errorf("ProfileEmail: got %q", got.ProfileEmail)
	}
	if got.ValueDouble == nil || *got.ValueDouble != 1.5 {
		t.Errorf("ValueDouble: got %v", got.ValueDouble)
	}
	if got.Dimensions["region"] != "us-east" {
		t.Errorf("Dimensions: got %v", got.Dimensions)
	}
}

func TestPgStore_InsertMetrics_Batch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	vi := int64(42)
	metrics := []*OtelMetric{
		{Ts: now, MetricName: "latency_ms", ProfileEmail: "m@test.com", ValueDouble: ptrFloat(120.5)},
		{Ts: now.Add(time.Second), MetricName: "token_count", ProfileEmail: "m@test.com", ValueInt: &vi},
	}

	if err := s.InsertMetrics(ctx, metrics); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	all, err := s.ListMetrics(ctx, MetricFilter{ProfileEmail: "m@test.com", Limit: 10})
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 metrics, got %d", len(all))
	}
}

func TestPgStore_InsertMetrics_Empty(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()

	if err := s.InsertMetrics(ctx, nil); err != nil {
		t.Errorf("InsertMetrics(nil): %v", err)
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{}); err != nil {
		t.Errorf("InsertMetrics([]): %v", err)
	}
}

func TestPgStore_ListMetrics_FilterByTimeRange(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	_ = s.InsertMetrics(ctx, []*OtelMetric{
		{Ts: base.Add(-2 * time.Hour), MetricName: "cost", ProfileEmail: "tr@test.com"},
		{Ts: base.Add(time.Hour), MetricName: "cost", ProfileEmail: "tr@test.com"},
	})

	since := base
	metrics, err := s.ListMetrics(ctx, MetricFilter{Since: &since})
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Errorf("expected 1 metric after base, got %d", len(metrics))
	}
}

// --- CostByUser ---

func TestPgStore_CostByUser(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", ProfileEmail: "alice@ex.com", UserTeam: "eng", Model: "claude-3", CostUSD: ptrFloat(0.10), InputTokens: ptrInt(1000), OutputTokens: ptrInt(200)},
		{Ts: now.Add(time.Second), EventName: "api_request", ProfileEmail: "alice@ex.com", UserTeam: "eng", Model: "claude-3", CostUSD: ptrFloat(0.05), InputTokens: ptrInt(500), OutputTokens: ptrInt(100)},
		{Ts: now.Add(2 * time.Second), EventName: "api_request", ProfileEmail: "bob@ex.com", UserTeam: "eng", Model: "claude-3", CostUSD: ptrFloat(0.20), InputTokens: ptrInt(2000), OutputTokens: ptrInt(400)},
		// Different event type — should not count
		{Ts: now.Add(3 * time.Second), EventName: "tool_result", ProfileEmail: "alice@ex.com"},
	})

	since := now.Add(-time.Minute)
	until := now.Add(time.Minute)
	summaries, err := s.CostByUser(ctx, since, until, "", "", "")
	if err != nil {
		t.Fatalf("CostByUser: %v", err)
	}

	byEmail := make(map[string]*CostSummary)
	for _, c := range summaries {
		byEmail[c.ProfileEmail] = c
	}

	alice := byEmail["alice@ex.com"]
	if alice == nil {
		t.Fatal("alice not in cost summaries")
	}
	if alice.RequestCount != 2 {
		t.Errorf("alice RequestCount: got %d, want 2", alice.RequestCount)
	}
	// Floating-point: use approximate comparison
	if alice.TotalCost < 0.149 || alice.TotalCost > 0.151 {
		t.Errorf("alice TotalCost: got %f, want 0.15", alice.TotalCost)
	}

	bob := byEmail["bob@ex.com"]
	if bob == nil {
		t.Fatal("bob not in cost summaries")
	}
	if bob.RequestCount != 1 {
		t.Errorf("bob RequestCount: got %d, want 1", bob.RequestCount)
	}
}

// --- CostByTeam ---

func TestPgStore_CostByTeam(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", ProfileEmail: "a@ex.com", UserTeam: "alpha", Model: "claude-3", CostUSD: ptrFloat(0.10)},
		{Ts: now.Add(time.Second), EventName: "api_request", ProfileEmail: "b@ex.com", UserTeam: "alpha", Model: "claude-3", CostUSD: ptrFloat(0.20)},
		{Ts: now.Add(2 * time.Second), EventName: "api_request", ProfileEmail: "c@ex.com", UserTeam: "beta", Model: "claude-3", CostUSD: ptrFloat(0.05)},
	})

	since := now.Add(-time.Minute)
	until := now.Add(time.Minute)
	summaries, err := s.CostByTeam(ctx, since, until, "", "")
	if err != nil {
		t.Fatalf("CostByTeam: %v", err)
	}

	byTeam := make(map[string]*CostSummary)
	for _, c := range summaries {
		byTeam[c.UserTeam] = c
	}

	alpha := byTeam["alpha"]
	if alpha == nil {
		t.Fatal("alpha not in team summaries")
	}
	if alpha.RequestCount != 2 {
		t.Errorf("alpha RequestCount: got %d, want 2", alpha.RequestCount)
	}
	if alpha.TotalCost < 0.299 || alpha.TotalCost > 0.301 {
		t.Errorf("alpha TotalCost: got %f, want 0.30", alpha.TotalCost)
	}
}

// --- ToolUsage ---

func TestPgStore_ToolUsage(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "tool_result", ToolName: "Read", ToolSuccess: ptrBool(true)},
		{Ts: now.Add(time.Second), EventName: "tool_result", ToolName: "Read", ToolSuccess: ptrBool(false)},
		{Ts: now.Add(2 * time.Second), EventName: "tool_result", ToolName: "Bash", ToolSuccess: ptrBool(true)},
		// wrong event type — should not count
		{Ts: now.Add(3 * time.Second), EventName: "api_request", ToolName: "Read"},
	})

	since := now.Add(-time.Minute)
	until := now.Add(time.Minute)
	tools, err := s.ToolUsage(ctx, since, until, "", "", "")
	if err != nil {
		t.Fatalf("ToolUsage: %v", err)
	}

	byName := make(map[string]*ToolUsageSummary)
	for _, tu := range tools {
		byName[tu.ToolName] = tu
	}

	readTool := byName["Read"]
	if readTool == nil {
		t.Fatal("Read tool not in summary")
	}
	if readTool.UseCount != 2 {
		t.Errorf("Read UseCount: got %d, want 2", readTool.UseCount)
	}
	if readTool.SuccessCount != 1 {
		t.Errorf("Read SuccessCount: got %d, want 1", readTool.SuccessCount)
	}
	if readTool.FailCount != 1 {
		t.Errorf("Read FailCount: got %d, want 1", readTool.FailCount)
	}

	bashTool := byName["Bash"]
	if bashTool == nil {
		t.Fatal("Bash tool not in summary")
	}
	if bashTool.UseCount != 1 {
		t.Errorf("Bash UseCount: got %d, want 1", bashTool.UseCount)
	}
}

// --- PluginUsage ---

func TestPgStore_PluginUsage_CodexUsageTokens(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, "codex", "codex-proj", "repo", "https://github.com/org/repo.git", "", "", "", time.Time{}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "codex-ses", ProjectHash: "codex-proj", RecordType: "user", ProfileEmail: "codex@test.com", UserID: "uid-codex", CommandName: "commit", Agent: "codex", BillingProvider: "openai"},
		{Ts: now.Add(time.Second), SessionID: "codex-ses", ProjectHash: "codex-proj", RecordType: "usage", ProfileEmail: "codex@test.com", UserID: "uid-codex", Agent: "codex", BillingProvider: "openai", InputTokens: ptrInt(10), OutputTokens: ptrInt(2), CacheReadTokens: ptrInt(4)},
		{Ts: now.Add(2 * time.Second), SessionID: "codex-ses", RecordType: "assistant", ProfileEmail: "codex@test.com", UserID: "uid-codex", Agent: "codex", BillingProvider: "openai", InputTokens: ptrInt(100), OutputTokens: ptrInt(100)},
		{Ts: now.Add(3 * time.Second), SessionID: "codex-ses", RecordType: "user", ProfileEmail: "codex@test.com", UserID: "uid-codex", Agent: "codex", BillingProvider: "openai"},
		{Ts: now.Add(4 * time.Second), SessionID: "codex-ses", RecordType: "usage", ProfileEmail: "codex@test.com", UserID: "uid-codex", Agent: "codex", BillingProvider: "openai", InputTokens: ptrInt(50), OutputTokens: ptrInt(50)},
	})
	if err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	summaries, err := s.PluginUsage(ctx, now.Add(-time.Minute), now.Add(time.Minute), "", "", "", "codex")
	if err != nil {
		t.Fatalf("PluginUsage: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	got := summaries[0]
	if got.CommandName != "commit" || got.Agent != "codex" {
		t.Fatalf("summary = %s/%s, want commit/codex", got.CommandName, got.Agent)
	}
	if got.InvocationCount != 1 || got.TotalTokens != 8 || got.InputTokens != 6 || got.OutputTokens != 2 {
		t.Fatalf("counts = inv:%d total:%d input:%d output:%d, want 1/8/6/2", got.InvocationCount, got.TotalTokens, got.InputTokens, got.OutputTokens)
	}
	if got.RepositoryID != "github.com/org/repo" || got.RepositoryName != "repo" {
		t.Fatalf("repository = %q/%q, want github.com/org/repo/repo", got.RepositoryID, got.RepositoryName)
	}
	if !got.HasGit {
		t.Fatal("HasGit = false, want true")
	}
}

func TestPgStore_PluginUsage_ClaudeAttributionSkill(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, "claude", "claude-proj", "cctrace", "https://github.com/org/cctrace.git", "", "", "", time.Time{}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts:           now,
			SessionID:    "claude-ses",
			ProjectHash:  "claude-proj",
			RecordType:   "assistant",
			ProfileEmail: "claude@test.com",
			UserID:       "uid-claude",
			CommandName:  "playwright-cli",
			Agent:        "claude",
			InputTokens:  ptrInt(10),
			OutputTokens: ptrInt(5),
		},
		{
			Ts:           now.Add(time.Second),
			SessionID:    "claude-ses",
			ProjectHash:  "claude-proj",
			RecordType:   "user",
			ProfileEmail: "claude@test.com",
			UserID:       "uid-claude",
			Agent:        "claude",
			Raw:          json.RawMessage(`{"message":{"content":[{"type":"tool_result","content":"ok"}]}}`),
		},
		{
			Ts:           now.Add(2 * time.Second),
			SessionID:    "claude-ses",
			ProjectHash:  "claude-proj",
			RecordType:   "assistant",
			ProfileEmail: "claude@test.com",
			UserID:       "uid-claude",
			Agent:        "claude",
			InputTokens:  ptrInt(20),
			OutputTokens: ptrInt(7),
		},
		{
			Ts:           now.Add(3 * time.Second),
			SessionID:    "claude-ses",
			ProjectHash:  "claude-proj",
			RecordType:   "user",
			ProfileEmail: "claude@test.com",
			UserID:       "uid-claude",
			Agent:        "claude",
			Raw:          json.RawMessage(`{"message":{"content":"next human prompt"}}`),
		},
		{
			Ts:           now.Add(4 * time.Second),
			SessionID:    "claude-ses",
			ProjectHash:  "claude-proj",
			RecordType:   "assistant",
			ProfileEmail: "claude@test.com",
			UserID:       "uid-claude",
			Agent:        "claude",
			InputTokens:  ptrInt(100),
			OutputTokens: ptrInt(100),
		},
	})
	if err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	summaries, err := s.PluginUsage(ctx, now.Add(-time.Minute), now.Add(time.Minute), "claude@test.com", "", "", "claude")
	if err != nil {
		t.Fatalf("PluginUsage: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	got := summaries[0]
	if got.CommandName != "playwright-cli" || got.Agent != "claude" {
		t.Fatalf("summary = %s/%s, want playwright-cli/claude", got.CommandName, got.Agent)
	}
	if got.InvocationCount != 1 || got.TotalTokens != 42 || got.InputTokens != 30 || got.OutputTokens != 12 {
		t.Fatalf("counts = inv:%d total:%d input:%d output:%d, want 1/42/30/12", got.InvocationCount, got.TotalTokens, got.InputTokens, got.OutputTokens)
	}
}

// --- SkillUsage ---

func TestPgStore_SkillUsage(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	three := int64(3)
	one := int64(1)
	two := int64(2)
	if err := s.UpsertProject(ctx, "codex", "skill-proj", "cctrace", "https://github.com/org/cctrace.git", "", "", "", time.Time{}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	err := s.InsertMetrics(ctx, []*OtelMetric{
		{
			Ts:           now,
			MetricName:   "codex.skill.injected",
			ProfileEmail: "skill@test.com",
			Agent:        "codex",
			ValueInt:     &three,
			Dimensions:   map[string]interface{}{"skill": "imagegen", "status": "ok", "invoke_type": "implicit"},
		},
		{
			Ts:           now.Add(time.Second),
			MetricName:   "codex.skill.injected",
			ProfileEmail: "skill@test.com",
			Agent:        "codex",
			ValueInt:     &one,
			Dimensions:   map[string]interface{}{"skill": "imagegen", "status": "error", "invoke_type": "implicit"},
		},
		{
			Ts:           now.Add(2 * time.Second),
			MetricName:   "codex.skill.injected",
			SessionID:    "metric-ses",
			ProfileEmail: "skill@test.com",
			Agent:        "codex",
			ValueInt:     &two,
			Dimensions:   map[string]interface{}{"skill": "openai-docs", "status": "ok"},
		},
		{
			Ts:           now.Add(3 * time.Second),
			MetricName:   "other.metric",
			ProfileEmail: "skill@test.com",
			Agent:        "codex",
			ValueInt:     &one,
			Dimensions:   map[string]interface{}{"skill": "ignored", "status": "ok"},
		},
	})
	if err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	err = s.InsertSessionRecords(ctx, []*SessionRecord{
		{
			Ts:              now.Add(2500 * time.Millisecond),
			SessionID:       "metric-ses",
			ProjectHash:     "skill-proj",
			RecordType:      "user",
			ProfileEmail:    "skill@test.com",
			UserID:          "uid-skill",
			CommandName:     "openai-docs",
			Agent:           "codex",
			BillingProvider: "openai",
		},
		{
			Ts:              now.Add(4 * time.Second),
			SessionID:       "jsonl-ses",
			ProjectHash:     "skill-proj",
			RecordType:      "user",
			ProfileEmail:    "skill@test.com",
			UserID:          "uid-skill",
			CommandName:     "jsonl-only",
			Agent:           "codex",
			BillingProvider: "openai",
		},
	})
	if err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	since := now.Add(-time.Minute)
	until := now.Add(time.Minute)
	summaries, err := s.SkillUsage(ctx, since, until, "skill@test.com", "", "", "codex")
	if err != nil {
		t.Fatalf("SkillUsage: %v", err)
	}

	byKey := make(map[string]*SkillUsageSummary)
	for _, summary := range summaries {
		byKey[summary.SkillName+"/"+summary.InvokeType] = summary
	}

	imagegen := byKey["imagegen/implicit"]
	if imagegen == nil {
		t.Fatal("imagegen implicit summary not found")
	}
	if imagegen.SuccessCount != 3 || imagegen.FailCount != 1 || imagegen.TotalCount != 4 {
		t.Fatalf("imagegen counts = success:%d fail:%d total:%d, want 3/1/4", imagegen.SuccessCount, imagegen.FailCount, imagegen.TotalCount)
	}

	openaiDocs := byKey["openai-docs/explicit"]
	if openaiDocs == nil {
		t.Fatal("openai-docs explicit summary not found")
	}
	if openaiDocs.SuccessCount != 2 || openaiDocs.TotalCount != 2 {
		t.Fatalf("openai-docs counts = success:%d total:%d, want 2/2", openaiDocs.SuccessCount, openaiDocs.TotalCount)
	}

	jsonlOnly := byKey["jsonl-only/explicit"]
	if jsonlOnly == nil {
		t.Fatal("jsonl-only explicit summary not found")
	}
	if jsonlOnly.SuccessCount != 1 || jsonlOnly.FailCount != 0 || jsonlOnly.TotalCount != 1 {
		t.Fatalf("jsonl-only counts = success:%d fail:%d total:%d, want 1/0/1", jsonlOnly.SuccessCount, jsonlOnly.FailCount, jsonlOnly.TotalCount)
	}
	if jsonlOnly.ProjectHash != "skill-proj" || jsonlOnly.ProjectName != "cctrace" {
		t.Fatalf("jsonl-only project = %q/%q, want skill-proj/cctrace", jsonlOnly.ProjectHash, jsonlOnly.ProjectName)
	}
	if jsonlOnly.RepositoryID != "github.com/org/cctrace" || jsonlOnly.RepositoryName != "cctrace" {
		t.Fatalf("jsonl-only repository = %q/%q, want github.com/org/cctrace/cctrace", jsonlOnly.RepositoryID, jsonlOnly.RepositoryName)
	}
	if !jsonlOnly.HasGit {
		t.Fatal("jsonl-only HasGit = false, want true")
	}
}

// TestPgStore_PluginUsage_RepoSubpath verifies repo_subpath rides along with
// repository_id/repository_name into PluginUsage, so projectGroup on the plugins
// page can tell a monorepo subdirectory apart from the repo root — the same
// identity contract the sessions page already gets from ListProjects. See #257.
func TestPgStore_PluginUsage_RepoSubpath(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, "claude", "subpath-proj", "analyzer", "https://github.com/org/repo.git", "", "", "deliverable/analyzer/", time.Time{}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: now, SessionID: "subpath-ses", ProjectHash: "subpath-proj", RecordType: "assistant", ProfileEmail: "sub@test.com", UserID: "uid-sub", CommandName: "deploy", Agent: "claude", InputTokens: ptrInt(1), OutputTokens: ptrInt(1)},
	})
	if err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	summaries, err := s.PluginUsage(ctx, now.Add(-time.Minute), now.Add(time.Minute), "sub@test.com", "", "", "claude")
	if err != nil {
		t.Fatalf("PluginUsage: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if got := summaries[0].RepoSubpath; got != "deliverable/analyzer/" {
		t.Fatalf("RepoSubpath = %q, want %q", got, "deliverable/analyzer/")
	}
}

// TestPgStore_SkillUsage_RepoSubpath covers both SkillUsage sources: the
// metric_rows path (via session_ctx, joined per session_id) and the jsonl_rows
// path (reading sr.repo_subpath directly, same as repository_id).
func TestPgStore_SkillUsage_RepoSubpath(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	three := int64(3)
	if err := s.UpsertProject(ctx, "codex", "skill-subpath-proj", "cctrace", "https://github.com/org/cctrace.git", "", "", "", time.Time{}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		{Ts: now, MetricName: "codex.skill.injected", SessionID: "metric-subpath-ses", ProfileEmail: "sub-skill@test.com", Agent: "codex", ValueInt: &three, Dimensions: map[string]interface{}{"skill": "imagegen", "status": "ok", "invoke_type": "implicit"}},
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	err := s.InsertSessionRecords(ctx, []*SessionRecord{
		// Backs the metric_rows path via session_ctx: same session_id as the metric above.
		{Ts: now, SessionID: "metric-subpath-ses", ProjectHash: "skill-subpath-proj", RecordType: "user", ProfileEmail: "sub-skill@test.com", UserID: "uid-sub-skill", Agent: "codex", BillingProvider: "openai", RepoSubpath: "internal/store/"},
		// Backs the jsonl_rows path directly.
		{Ts: now.Add(time.Second), SessionID: "jsonl-subpath-ses", ProjectHash: "skill-subpath-proj", RecordType: "user", ProfileEmail: "sub-skill@test.com", UserID: "uid-sub-skill", CommandName: "jsonl-subpath-skill", Agent: "codex", BillingProvider: "openai", RepoSubpath: "internal/api/"},
	})
	if err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	summaries, err := s.SkillUsage(ctx, now.Add(-time.Minute), now.Add(time.Minute), "sub-skill@test.com", "", "", "codex")
	if err != nil {
		t.Fatalf("SkillUsage: %v", err)
	}
	byKey := make(map[string]*SkillUsageSummary)
	for _, summary := range summaries {
		byKey[summary.SkillName+"/"+summary.InvokeType] = summary
	}

	imagegen := byKey["imagegen/implicit"]
	if imagegen == nil {
		t.Fatal("imagegen implicit summary not found")
	}
	if imagegen.RepoSubpath != "internal/store/" {
		t.Fatalf("metric_rows RepoSubpath = %q, want %q", imagegen.RepoSubpath, "internal/store/")
	}

	jsonlSkill := byKey["jsonl-subpath-skill/explicit"]
	if jsonlSkill == nil {
		t.Fatal("jsonl-subpath-skill explicit summary not found")
	}
	if jsonlSkill.RepoSubpath != "internal/api/" {
		t.Fatalf("jsonl_rows RepoSubpath = %q, want %q", jsonlSkill.RepoSubpath, "internal/api/")
	}
}

// --- SessionList ---

func TestPgStore_SessionList(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", SessionID: "ses-A", ProfileEmail: "sess@test.com"},
		{Ts: now.Add(time.Second), EventName: "api_request", SessionID: "ses-A", ProfileEmail: "sess@test.com"},
		{Ts: now.Add(2 * time.Second), EventName: "api_request", SessionID: "ses-B", ProfileEmail: "sess@test.com"},
		{Ts: now.Add(3 * time.Second), EventName: "api_request", SessionID: "ses-C", ProfileEmail: "other@test.com"},
	})

	// All sessions
	all, err := s.SessionList(ctx, "", 50, 0)
	if err != nil {
		t.Fatalf("SessionList all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 sessions total, got %d", len(all))
	}

	// Filtered by user
	userSessions, err := s.SessionList(ctx, "sess@test.com", 50, 0)
	if err != nil {
		t.Fatalf("SessionList by user: %v", err)
	}
	if len(userSessions) != 2 {
		t.Errorf("expected 2 sessions for sess@test.com, got %d", len(userSessions))
	}

	// Limit
	limited, err := s.SessionList(ctx, "", 2, 0)
	if err != nil {
		t.Fatalf("SessionList limited: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("expected 2 sessions (limited), got %d", len(limited))
	}
}

func TestPgStore_SessionList_SkipsEmpty(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC()
	_ = s.InsertEvents(ctx, []*OtelEvent{
		{Ts: now, EventName: "api_request", SessionID: ""}, // no session_id
		{Ts: now.Add(time.Second), EventName: "api_request", SessionID: "real-ses"},
	})

	sessions, err := s.SessionList(ctx, "", 50, 0)
	if err != nil {
		t.Fatalf("SessionList: %v", err)
	}
	for _, sid := range sessions {
		if sid == "" {
			t.Error("SessionList should not return empty session_id")
		}
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 non-empty session, got %d", len(sessions))
	}
}

// --- Ping ---

func TestPgStore_Ping(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	if err := s.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

// --- QuotaSnapshot ---

func TestPgStore_QuotaSnapshot(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	snap := &QuotaSnapshot{
		ProfileEmail:      "alice@example.com",
		UserID:            "alice",
		FiveHourPct:       37.5,
		FiveHourResetsAt:  &now,
		SevenDayPct:       68.0,
		SevenDaySonnetPct: 26.0,
	}

	// Upsert then Get
	if err := s.UpsertQuotaSnapshot(ctx, snap); err != nil {
		t.Fatalf("UpsertQuotaSnapshot: %v", err)
	}
	got, err := s.GetQuotaSnapshot(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetQuotaSnapshot: %v", err)
	}
	if got.FiveHourPct != snap.FiveHourPct {
		t.Errorf("FiveHourPct: got %v, want %v", got.FiveHourPct, snap.FiveHourPct)
	}
	if got.SevenDayPct != snap.SevenDayPct {
		t.Errorf("SevenDayPct: got %v, want %v", got.SevenDayPct, snap.SevenDayPct)
	}

	// Upsert again with updated values — should replace
	snap.FiveHourPct = 55.0
	snap.SevenDaySonnetPct = 40.0
	if err := s.UpsertQuotaSnapshot(ctx, snap); err != nil {
		t.Fatalf("UpsertQuotaSnapshot (2nd): %v", err)
	}
	got, err = s.GetQuotaSnapshot(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetQuotaSnapshot (2nd): %v", err)
	}
	if got.FiveHourPct != 55.0 {
		t.Errorf("FiveHourPct after update: got %v, want 55.0", got.FiveHourPct)
	}
	if got.SevenDaySonnetPct != 40.0 {
		t.Errorf("SevenDaySonnetPct after update: got %v, want 40.0", got.SevenDaySonnetPct)
	}

	// List: add a second profile, expect 2 rows
	if err := s.UpsertQuotaSnapshot(ctx, &QuotaSnapshot{
		ProfileEmail: "bob@example.com",
		SevenDayPct:  10.0,
	}); err != nil {
		t.Fatalf("UpsertQuotaSnapshot (bob): %v", err)
	}
	list, err := s.ListQuotaSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListQuotaSnapshots: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("ListQuotaSnapshots: got %d rows, want 2", len(list))
	}
}
