package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	tc "github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"cctrace/internal/containertest"
	"cctrace/internal/insights"
	"cctrace/internal/store"
)

// The P1t transition on a real database (p1t-design §8.2): runRequestSetMigration
// over the store's own steps, each test on a fresh database of its own in one
// container the package shares. They run only under CCTRACE_REQUIRE_CONTAINERS=1
// -- make test-integration and make test -- where a container that will not start
// fails them. Anywhere else they skip before starting one, so make test-unit,
// which runs this package too, needs no Docker.

var (
	transitionPgOnce sync.Once
	transitionPgDSN  string
	transitionPgErr  error
	transitionDBs    atomic.Int64
)

// useColimaDockerHost points testcontainers at Colima when that is the runtime
// here, and its reaper at the socket inside the VM, as internal/store's
// ensureDockerHost does and says why.
func useColimaDockerHost() {
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
		return
	}
	if os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE") == "" {
		os.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", "/var/run/docker.sock")
	}
}

// freshTransitionStore is a migrated store on a database created for the calling
// test: a fresh install, as a deploy of the transition may meet one. The test's
// end closes it.
func freshTransitionStore(t *testing.T) *store.PgStore {
	t.Helper()
	if !containertest.Required() {
		t.Skipf("needs a postgres container: run with %s=1, as make test-integration does", containertest.RequireEnv)
	}
	transitionPgOnce.Do(func() {
		useColimaDockerHost()
		ctx := context.Background()
		ctr, err := pgmod.Run(ctx,
			"timescale/timescaledb:latest-pg16",
			pgmod.WithDatabase("transition"),
			pgmod.WithUsername("test"),
			pgmod.WithPassword("test"),
			// As internal/store's container: the module already runs postgres with
			// fsync=off, and these drop the rest of the per-commit durability work.
			// The container is thrown away, never restarted, and no test depends on
			// crash recovery.
			tc.WithCmdArgs("-c", "synchronous_commit=off", "-c", "full_page_writes=off"),
			tc.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			transitionPgErr = err
			return
		}
		transitionPgDSN, transitionPgErr = ctr.ConnectionString(ctx, "sslmode=disable")
	})
	if transitionPgErr != nil {
		containertest.SkipOrFail(t, "postgres container", transitionPgErr)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, transitionPgDSN)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx) //nolint:errcheck // nothing to do about a failed close
	name := fmt.Sprintf("transition_%d", transitionDBs.Add(1))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	dsn, err := url.Parse(transitionPgDSN)
	if err != nil {
		t.Fatalf("parse the DSN: %v", err)
	}
	dsn.Path = "/" + name
	pg, err := store.NewPgStore(ctx, dsn.String())
	if err != nil {
		t.Fatalf("NewPgStore: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	pg.SetTaskClassifier(insights.KeywordClassifier{})
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return pg
}

// seedTransitionHistory writes a small history of the shapes the transition
// recounts -- an offline Claude session whose typed turn runs a plugin command and
// whose response is written as three lines, priced by a weekly rate bucket; a
// session with OTEL; a Codex session -- and runs the boot repairs main runs ahead
// of the transition, which build what steps 4 to 8 recount.
func seedTransitionHistory(t *testing.T, pg *store.PgStore) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC) // a Monday: the rate bucket's week
	n := func(v int) *int { return &v }
	turn := func(session, uuid, command string) *store.SessionRecord {
		return &store.SessionRecord{Ts: at, SessionID: session, UUID: uuid, RecordType: "user", UserID: "uid-tr",
			ProfileEmail: "tr@example.com", LoginEmail: "tr@example.com", ProjectHash: "tr-project", CommandName: command,
			Raw: json.RawMessage(`{"message":{"role":"user","content":"please add a test for this"}}`)}
	}
	reply := func(session, uuid string, after time.Duration, out int) *store.SessionRecord {
		return &store.SessionRecord{Ts: at.Add(after), SessionID: session, UUID: uuid, RecordType: "assistant",
			UserID: "uid-tr", ProfileEmail: "tr@example.com", LoginEmail: "tr@example.com", ProjectHash: "tr-project",
			Model: "tr-model", InputTokens: n(10), OutputTokens: n(out), CacheReadTokens: n(1000),
			Raw: json.RawMessage(`{"message":{"id":"msg_` + session + `"}}`)}
	}
	if err := pg.InsertSessionRecords(ctx, []*store.SessionRecord{
		turn("tr-offline", "off-turn", "tr-review"),
		reply("tr-offline", "off-1", time.Second, 1),
		reply("tr-offline", "off-2", 2*time.Second, 300),
		reply("tr-offline", "off-3", 3*time.Second, 523),
		turn("tr-otel", "otel-turn", ""),
		reply("tr-otel", "otel-1", time.Minute, 40),
		{Ts: at.Add(2 * time.Minute), SessionID: "tr-codex", UUID: "codex-1", RecordType: "usage", Agent: "codex",
			BillingProvider: "openai", UserID: "uid-tr", ProfileEmail: "tr@example.com", Model: "codex-model",
			InputTokens: n(1000), OutputTokens: n(100), CacheReadTokens: n(400),
			Raw: json.RawMessage(`{"type":"token_usage_record","payload":{"usage":{"input_tokens":1}}}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	cost := 0.25
	if err := pg.InsertEvents(ctx, []*store.OtelEvent{{Ts: at.Add(time.Minute), EventName: "api_request",
		SessionID: "tr-otel", UserID: "uid-tr", ProfileEmail: "tr@example.com", LoginEmail: "tr@example.com",
		Model: "tr-model", CostUSD: &cost, InputTokens: n(500), OutputTokens: n(60), Agent: "claude",
		BillingProvider: "anthropic"}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if _, err := pg.Pool().Exec(ctx, `INSERT INTO model_rate_buckets (model, bucket_start, scale, sample_rows)
		VALUES ('tr-model', $1::date, $2, 20)`, at.Format(time.DateOnly), 1.0/(1<<20)); err != nil {
		t.Fatalf("seed the rate bucket: %v", err)
	}
	if err := pg.RefreshCodexImputedCost(ctx); err != nil {
		t.Fatalf("RefreshCodexImputedCost: %v", err)
	}
	for _, build := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"session overview rollups", pg.BackfillSessionOverviewRollups},
		{"usage hourly rollups", pg.BackfillUsageHourlyRollups},
		{"coverage measured minutes", pg.BackfillCoverageMeasuredMinutes},
		{"plugin invocation facts", pg.BackfillPluginInvocationFacts},
		{"task segment facts", pg.BackfillTaskSegmentFacts},
	} {
		if err := build.run(ctx); err != nil {
			t.Fatalf("build the %s: %v", build.name, err)
		}
	}
}

// p1tMarkers are the transition's steps in the order of p1t-design §4.1, by
// marker. Written out rather than read off the steps, so that a step left out or
// registered out of order shows.
var p1tMarkers = []string{
	"p1t_archive_v1", "session_records_message_id_v1", "claude_imputed_requests_v1", "session_overview_tokens_v2",
	"usage_hourly_rollups_requests_v1", "coverage_minutes_requests_v1", "plugin_invocation_facts_requests_v1",
	"task_segment_tokens_v2", "request_set_v1",
}

// doneLines are the completion lines of these steps in order, as runRequestSetMigration
// logs them on testClock.
func doneLines(markers ...string) []string {
	out := make([]string, 0, len(markers))
	for _, marker := range markers {
		out = append(out, "[request-set] "+marker+" done in 0s since this process first ran it")
	}
	return out
}

func checkTransitionStatus(t *testing.T, pg *store.PgStore, when string, want store.RequestSetMigrationStatus) {
	t.Helper()
	if got, err := pg.RequestSetMigrationStatus(context.Background()); err != nil || got != want {
		t.Errorf("%s: status %+v, %v; want %+v", when, got, err, want)
	}
}

// On a fresh database one run walks steps 1 to 9 in order, each doing its work and
// saying so once, with no wait between them, and the status goes from pending at
// step 1 to done. The next boot finds every marker and logs nothing.
func TestRequestSetMigrationWalksAFreshDatabase(t *testing.T) {
	pg := freshTransitionStore(t)
	seedTransitionHistory(t, pg)
	checkTransitionStatus(t, pg, "before the transition",
		store.RequestSetMigrationStatus{TokenRulePending: true, Step: p1tMarkers[0]})

	var logs []string
	clock := &testClock{}
	runRequestSetMigration(context.Background(), pg.RequestSetMigrationSteps(), clock.now, clock.after, collectLogs(&logs))
	if want := doneLines(p1tMarkers...); !slices.Equal(logs, want) {
		t.Errorf("the transition logged\n%s\nwant\n%s", strings.Join(logs, "\n"), strings.Join(want, "\n"))
	}
	if len(clock.waits) != 0 {
		t.Errorf("waits = %v, want none", clock.waits)
	}
	checkTransitionStatus(t, pg, "after the transition", store.RequestSetMigrationStatus{})

	logs = nil
	runRequestSetMigration(context.Background(), pg.RequestSetMigrationSteps(), clock.now, clock.after, collectLogs(&logs))
	if len(logs) != 0 {
		t.Errorf("the boot after the transition logged %q, want nothing", logs)
	}
}

// A step that fails stops the transition there, and the next process resumes at
// that step: the steps before it find their markers and say nothing, and it and
// every step after it do their work and say so once. Step 6 fails here on its
// marker write, after its recount, and the first process is stopped in the
// backoff that follows.
func TestRequestSetMigrationResumesAtTheStepThatFailed(t *testing.T) {
	pg := freshTransitionStore(t)
	seedTransitionHistory(t, pg)
	ctx := context.Background()
	const failing = 5 // step 6
	exec := func(stmt string) {
		t.Helper()
		if _, err := pg.Pool().Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	exec(`CREATE FUNCTION refuse_marker_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'marker write refused by the test: %', NEW.name; END $$`)
	exec(`CREATE TRIGGER refuse_marker_write BEFORE INSERT ON schema_backfills FOR EACH ROW
		WHEN (NEW.name = '` + p1tMarkers[failing] + `') EXECUTE FUNCTION refuse_marker_write()`)

	first, stop := context.WithCancel(ctx)
	defer stop()
	var logs []string
	clock := &testClock{}
	runRequestSetMigration(first, pg.RequestSetMigrationSteps(), clock.now, clock.after, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
		if strings.Contains(logs[len(logs)-1], " failed, ") {
			stop()
		}
	})
	if len(logs) != failing+1 || !slices.Equal(logs[:failing], doneLines(p1tMarkers[:failing]...)) ||
		!strings.HasPrefix(logs[failing], "[request-set] "+p1tMarkers[failing]+" failed, next try in 5s: ") ||
		!strings.Contains(logs[failing], "marker write refused by the test") {
		t.Fatalf("the first process logged\n%s\nwant steps 1 to 5 done, then step 6 failed", strings.Join(logs, "\n"))
	}
	checkTransitionStatus(t, pg, "after the first process",
		store.RequestSetMigrationStatus{TokenRulePending: true, Step: p1tMarkers[failing]})

	exec(`DROP TRIGGER refuse_marker_write ON schema_backfills`)
	logs = nil
	runRequestSetMigration(ctx, pg.RequestSetMigrationSteps(), clock.now, clock.after, collectLogs(&logs))
	if want := doneLines(p1tMarkers[failing:]...); !slices.Equal(logs, want) {
		t.Errorf("the next process logged\n%s\nwant\n%s", strings.Join(logs, "\n"), strings.Join(want, "\n"))
	}
	checkTransitionStatus(t, pg, "after the next process", store.RequestSetMigrationStatus{})
}
