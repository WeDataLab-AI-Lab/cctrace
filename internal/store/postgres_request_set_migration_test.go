package store

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// resetRequestSetMigration puts the transition back at its start -- no step
// marker, no completion marker, no message_id, overview or task segment cursor,
// no archive, no claude_imputed_cost index -- and does it again when the test
// ends, so no other test inherits a transition this one moved.
func resetRequestSetMigration(t *testing.T, s *PgStore) {
	t.Helper()
	clear := func() {
		ctx := context.Background()
		if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = ANY($1::text[])`, []string{
			usageHourlyRollupArchive, sessionRecordMessageIDBackfill, requestSetMigrationDone, claudeImputedRequestsBackfill,
			sessionOverviewTokensRecount, usageHourlyRollupsRecount, coverageMinutesRecount, pluginInvocationFactsRecount,
			taskSegmentTokensRecount,
		}); err != nil {
			t.Fatalf("clear transition markers: %v", err)
		}
		for _, table := range []string{"session_records_message_id_cursor", "usage_hourly_rollups_archive_p1t",
			"session_overview_tokens_cursor", "task_segment_tokens_cursor"} {
			if _, err := s.pool.Exec(ctx, `DELETE FROM `+table); err != nil {
				t.Fatalf("clear %s: %v", table, err)
			}
		}
		if _, err := s.pool.Exec(ctx, `DROP INDEX IF EXISTS uniq_claude_imputed_srec`); err != nil {
			t.Fatalf("drop the claude_imputed_cost index: %v", err)
		}
	}
	clear()
	t.Cleanup(clear)
}

func backfillMarkedNow(t *testing.T, s *PgStore, name string) bool {
	t.Helper()
	var marked bool
	if err := s.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, name).Scan(&marked); err != nil {
		t.Fatalf("read marker %s: %v", name, err)
	}
	return marked
}

// setMarker writes marker name into schema_backfills, or deletes it.
func setMarker(t *testing.T, s *PgStore, name string, there bool) {
	t.Helper()
	stmt := `DELETE FROM schema_backfills WHERE name = $1`
	if there {
		stmt = `INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`
	}
	if _, err := s.pool.Exec(context.Background(), stmt, name); err != nil {
		t.Fatalf("set marker %s to %v: %v", name, there, err)
	}
}

// keepMarker puts marker name back as it is now, there or not, when the test
// ends, so that a test that writes or deletes a marker other tests share hands
// them the state it found.
func keepMarker(t *testing.T, s *PgStore, name string) {
	t.Helper()
	there := backfillMarkedNow(t, s, name)
	t.Cleanup(func() { setMarker(t, s, name, there) })
}

// refuseMarkerWrite makes every write of marker name into schema_backfills fail,
// and returns what allows it again; the test's end allows it too.
func refuseMarkerWrite(t *testing.T, s *PgStore, name string) func() {
	t.Helper()
	ctx := context.Background()
	exec := func(stmt string) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", firstLine(stmt), err)
		}
	}
	allow := func() {
		exec(`DROP TRIGGER IF EXISTS refuse_marker_write ON schema_backfills`)
		exec(`DROP FUNCTION IF EXISTS refuse_marker_write()`)
	}
	exec(`CREATE FUNCTION refuse_marker_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'marker write refused by the test: %', NEW.name; END $$`)
	exec(`CREATE TRIGGER refuse_marker_write BEFORE INSERT ON schema_backfills FOR EACH ROW
		WHEN (NEW.name = '` + name + `') EXECUTE FUNCTION refuse_marker_write()`)
	t.Cleanup(allow)
	return allow
}

// archiveKey is the unit the archive keeps (p1t-design §5.1, D7).
type archiveKey struct {
	bucket                        time.Time
	agent, billingProvider, model string
}

type archiveSums struct {
	cost                                          float64
	input, output, cacheRead, cacheCreate, events int64
}

type archivedRow struct {
	sums       archiveSums
	archivedAt time.Time
}

// rollupRow is one usage_hourly_rollups row: an archive key, the identity and
// project columns the archive sums away, and its values.
type rollupRow struct {
	key                                 archiveKey
	user, profile, login, team, project string
	sums                                archiveSums
}

// archiveFixture spreads keys over several identities and projects, so a key
// the archive keeps is the sum of more than one rollup row. Every cost is a sum
// of powers of two, so Postgres and Go add them up to the same float whatever
// the order.
func archiveFixture() []rollupRow {
	h0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	h1 := h0.Add(time.Hour)
	claudeA := archiveKey{h0, "claude", "anthropic", "model-a"}
	return []rollupRow{
		{claudeA, "u1", "a@example.com", "a@example.com", "team-a", "p1", archiveSums{1.5, 100, 10, 1000, 50, 3}},
		{claudeA, "u2", "b@example.com", "b@example.com", "team-b", "p2", archiveSums{2.25, 200, 20, 2000, 60, 4}},
		{claudeA, "u1", "a@example.com", "a@example.com", "team-a", "p2", archiveSums{0.5, 30, 3, 300, 5, 1}},
		{archiveKey{h0, "claude", "anthropic", "model-b"}, "u1", "a@example.com", "a@example.com", "team-a", "p1",
			archiveSums{4.75, 40, 4, 400, 7, 2}},
		// No billing provider, login or project: still its own key.
		{archiveKey{h0, "claude", "", "model-a"}, "u3", "c@example.com", "", "", "", archiveSums{0.25, 5, 1, 0, 0, 1}},
		{archiveKey{h1, "claude", "anthropic", "model-a"}, "u1", "a@example.com", "a@example.com", "team-a", "p1",
			archiveSums{3, 300, 30, 3000, 90, 5}},
		{archiveKey{h1, "claude", "anthropic", "model-a"}, "u3", "c@example.com", "c@example.com", "", "p3",
			archiveSums{1, 10, 1, 100, 9, 1}},
		{archiveKey{h0, "codex", "openai", "model-c"}, "u4", "d@example.com", "d@example.com", "team-a", "p4",
			archiveSums{6.5, 600, 60, 0, 0, 7}},
		{archiveKey{h0, "codex", "openai", "model-c"}, "u2", "b@example.com", "b@example.com", "team-b", "p4",
			archiveSums{0.75, 70, 7, 0, 0, 2}},
		{archiveKey{h1, "weekly", "", ""}, "u1", "a@example.com", "", "", "", archiveSums{0.125, 12, 2, 0, 0, 1}},
	}
}

func insertRollupRows(t *testing.T, s *PgStore, rows []rollupRow) {
	t.Helper()
	for _, r := range rows {
		if _, err := s.pool.Exec(context.Background(), `INSERT INTO usage_hourly_rollups (
				bucket, model, user_id, profile_email, login_email, user_team, agent, billing_provider, project_hash,
				cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, event_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			r.key.bucket, r.key.model, r.user, r.profile, r.login, r.team, r.key.agent, r.key.billingProvider, r.project,
			r.sums.cost, r.sums.input, r.sums.output, r.sums.cacheRead, r.sums.cacheCreate, r.sums.events); err != nil {
			t.Fatalf("insert rollup row %+v: %v", r, err)
		}
	}
}

func readArchive(t *testing.T, s *PgStore) map[archiveKey]archivedRow {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT bucket, agent, billing_provider, model,
			cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, event_count, archived_at
		FROM usage_hourly_rollups_archive_p1t`)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	archive := map[archiveKey]archivedRow{}
	for rows.Next() {
		var k archiveKey
		var r archivedRow
		if err := rows.Scan(&k.bucket, &k.agent, &k.billingProvider, &k.model, &r.sums.cost, &r.sums.input,
			&r.sums.output, &r.sums.cacheRead, &r.sums.cacheCreate, &r.sums.events, &r.archivedAt); err != nil {
			t.Fatalf("scan archive: %v", err)
		}
		k.bucket = k.bucket.UTC()
		archive[k] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read archive: %v", err)
	}
	return archive
}

func sameArchive(a, b map[archiveKey]archivedRow) bool {
	if len(a) != len(b) {
		return false
	}
	for k, r := range a {
		other, ok := b[k]
		if !ok || other.sums != r.sums || !other.archivedAt.Equal(r.archivedAt) {
			return false
		}
	}
	return true
}

// The archive keeps one row per (bucket, agent, billing_provider, model), each
// the sum of every rollup row under that key whoever and whichever project it
// was for, all stamped with one time from the database's clock: the
// transaction that took it.
func TestArchiveUsageHourlyRollupsSumsEachKey(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	fixture := archiveFixture()
	insertRollupRows(t, s, fixture)

	want := map[archiveKey]archiveSums{}
	for _, r := range fixture {
		sum := want[r.key]
		sum.cost += r.sums.cost
		sum.input += r.sums.input
		sum.output += r.sums.output
		sum.cacheRead += r.sums.cacheRead
		sum.cacheCreate += r.sums.cacheCreate
		sum.events += r.sums.events
		want[r.key] = sum
	}

	dbNow := func() time.Time {
		t.Helper()
		var at time.Time
		if err := s.pool.QueryRow(ctx, `SELECT now()`).Scan(&at); err != nil {
			t.Fatalf("read the database clock: %v", err)
		}
		return at
	}
	before := dbNow()
	if archived, err := s.ArchiveUsageHourlyRollups(ctx); err != nil || !archived {
		t.Fatalf("ArchiveUsageHourlyRollups: archived %v, err %v; want the archive taken", archived, err)
	}
	after := dbNow()

	got := readArchive(t, s)
	if len(got) != len(want) {
		t.Errorf("archive has %d keys, want %d: %+v", len(got), len(want), got)
	}
	stamps := map[int64]time.Time{}
	for k, w := range want {
		r, ok := got[k]
		switch {
		case !ok:
			t.Errorf("%+v: missing from the archive", k)
		case r.sums != w:
			t.Errorf("%+v: archived %+v, want %+v", k, r.sums, w)
		default:
			stamps[r.archivedAt.UnixMicro()] = r.archivedAt
		}
	}
	if len(stamps) != 1 {
		t.Errorf("archived_at takes %d values, want one: %v", len(stamps), stamps)
	}
	for _, at := range stamps {
		if at.Before(before) || at.After(after) {
			t.Errorf("archived_at %s, want between %s and %s on the database clock", at, before, after)
		}
	}
	if !backfillMarkedNow(t, s, usageHourlyRollupArchive) {
		t.Error("archive marker missing after the archive")
	}
}

// The rows and the marker commit together: a failure between them -- here the
// marker write, refused by a trigger after the rows are in -- leaves neither,
// and the next call archives from scratch.
func TestArchiveUsageHourlyRollupsCommitsRowsWithItsMarker(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	insertRollupRows(t, s, archiveFixture())
	allowMarker := refuseMarkerWrite(t, s, usageHourlyRollupArchive)

	archived, err := s.ArchiveUsageHourlyRollups(ctx)
	if err == nil || !strings.Contains(err.Error(), "marker write refused by the test") || archived {
		t.Fatalf("archive with the marker refused: archived %v, err %v; want the refusal and nothing committed", archived, err)
	}
	if n := len(readArchive(t, s)); n != 0 || backfillMarkedNow(t, s, usageHourlyRollupArchive) {
		t.Fatalf("after the failed marker write: %d archive row(s), marker %v; want neither",
			n, backfillMarkedNow(t, s, usageHourlyRollupArchive))
	}

	allowMarker()
	if archived, err := s.ArchiveUsageHourlyRollups(ctx); err != nil || !archived {
		t.Fatalf("archive again: archived %v, err %v; want the archive taken", archived, err)
	}
	if n := len(readArchive(t, s)); n == 0 || !backfillMarkedNow(t, s, usageHourlyRollupArchive) {
		t.Errorf("after the retry: %d archive row(s), marker %v; want the archive and its marker",
			n, backfillMarkedNow(t, s, usageHourlyRollupArchive))
	}
}

// The first archive is the "before", taken whole, once. A second call does
// nothing and says so, though the rollups have changed and gained an hour since.
// With the marker deleted by hand, a call adds nothing to the archive -- not
// that hour either -- and only writes the marker again.
func TestArchiveUsageHourlyRollupsKeepsTheFirstArchive(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	insertRollupRows(t, s, archiveFixture())

	if archived, err := s.ArchiveUsageHourlyRollups(ctx); err != nil || !archived {
		t.Fatalf("first call: archived %v, err %v; want the archive taken", archived, err)
	}
	first := readArchive(t, s)
	if len(first) == 0 {
		t.Fatal("the first archive is empty")
	}
	changeRollups := func() {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `UPDATE usage_hourly_rollups SET cost_usd = cost_usd + 1,
			input_tokens = input_tokens + 1, output_tokens = output_tokens + 1, cache_read_tokens = cache_read_tokens + 1,
			cache_create_tokens = cache_create_tokens + 1, event_count = event_count + 1`); err != nil {
			t.Fatalf("change rollups: %v", err)
		}
	}

	changeRollups()
	insertRollupRows(t, s, []rollupRow{{archiveKey{time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "claude", "anthropic", "model-a"},
		"u1", "a@example.com", "a@example.com", "team-a", "p1", archiveSums{8, 80, 8, 800, 8, 8}}})
	if archived, err := s.ArchiveUsageHourlyRollups(ctx); err != nil || archived {
		t.Fatalf("second call: archived %v, err %v; want nothing done", archived, err)
	}
	if got := readArchive(t, s); !sameArchive(got, first) {
		t.Errorf("a second call changed the archive:\n got %+v\nwant %+v", got, first)
	}

	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, usageHourlyRollupArchive); err != nil {
		t.Fatalf("delete marker: %v", err)
	}
	changeRollups()
	if archived, err := s.ArchiveUsageHourlyRollups(ctx); err != nil || !archived {
		t.Fatalf("call with the marker deleted: archived %v, err %v; want the marker written again", archived, err)
	}
	if got := readArchive(t, s); !sameArchive(got, first) {
		t.Errorf("a call with the marker deleted changed the archive:\n got %+v\nwant %+v", got, first)
	}
	if !backfillMarkedNow(t, s, usageHourlyRollupArchive) {
		t.Error("marker missing after the call that found it deleted")
	}
}

// The archive is a "before" only of rollups that were built. With the rollup
// backfill not done -- its boot repair failed, say -- step 1 builds the rollups
// first and archives what it built.
func TestRequestSetMigrationArchivesBuiltRollups(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()

	base := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	event := func(at time.Duration, user, model string, input, output int, cost float64) *OtelEvent {
		return &OtelEvent{Ts: base.Add(at), EventName: "api_request", SessionID: "sess-archive-build", UserID: user,
			LoginEmail: user + "@example.com", Model: model, Agent: "claude", BillingProvider: "anthropic",
			InputTokens: ptrInt(input), OutputTokens: ptrInt(output), CostUSD: ptrFloat(cost)}
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{
		event(5*time.Minute, "u1", "model-a", 100, 10, 1.5),
		event(40*time.Minute, "u2", "model-a", 50, 5, 0.25),
		event(70*time.Minute, "u1", "model-b", 30, 3, 0.5),
	}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if backfillMarkedNow(t, s, usageHourlyRollupBackfill) {
		t.Fatal("the rollups count as built before step 1: the test cannot see step 1 build them")
	}

	if progressed, err := s.RequestSetMigrationSteps()[0].Run(ctx); err != nil || !progressed {
		t.Fatalf("step 1: progressed %v, err %v; want the archive taken", progressed, err)
	}
	if !backfillMarkedNow(t, s, usageHourlyRollupBackfill) {
		t.Error("step 1 archived without building the rollups")
	}
	want := map[archiveKey]archiveSums{
		{base, "claude", "anthropic", "model-a"}:                {1.75, 150, 15, 0, 0, 2},
		{base.Add(time.Hour), "claude", "anthropic", "model-b"}: {0.5, 30, 3, 0, 0, 1},
	}
	got := readArchive(t, s)
	if len(got) != len(want) {
		t.Errorf("archive has %d keys, want %d: %+v", len(got), len(want), got)
	}
	for k, w := range want {
		if r, ok := got[k]; !ok || r.sums != w {
			t.Errorf("%+v: archived %+v (present %v), want %+v", k, r.sums, ok, w)
		}
	}
}

// The build is part of step 1, and so is its failure: with the rollup marker's
// write refused, step 1 fails naming the build, having committed nothing, and
// leaves neither archive rows nor its marker. Once the build can finish, the
// next try archives.
func TestRequestSetMigrationArchiveStepFailsWithTheBuild(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: time.Date(2026, 3, 4, 10, 5, 0, 0, time.UTC), EventName: "api_request",
		SessionID: "sess-archive-build-fails", UserID: "u1", Model: "model-a", Agent: "claude", BillingProvider: "anthropic",
		InputTokens: ptrInt(100), OutputTokens: ptrInt(10), CostUSD: ptrFloat(1.5)}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	allowBuild := refuseMarkerWrite(t, s, usageHourlyRollupBackfill)

	step := s.RequestSetMigrationSteps()[0]
	progressed, err := step.Run(ctx)
	if err == nil || progressed || !strings.Contains(err.Error(), "build usage hourly rollups before the archive") ||
		!strings.Contains(err.Error(), "marker write refused by the test") {
		t.Fatalf("step 1 with the build failing: progressed %v, err %v; want the build's error, named, and no progress", progressed, err)
	}
	if n := len(readArchive(t, s)); n != 0 || backfillMarkedNow(t, s, usageHourlyRollupArchive) {
		t.Fatalf("after the failed build: %d archive row(s), archive marker %v; want neither",
			n, backfillMarkedNow(t, s, usageHourlyRollupArchive))
	}

	allowBuild()
	if progressed, err := step.Run(ctx); err != nil || !progressed {
		t.Fatalf("step 1 once the build can finish: progressed %v, err %v; want the archive taken", progressed, err)
	}
	if n := len(readArchive(t, s)); n != 1 {
		t.Errorf("archive has %d key(s) after the retry, want the one the build made", n)
	}
}

// Once the archive is taken, step 1 builds nothing either: with its marker
// there and the rollup marker gone, it returns at once, done, and the rollups
// stay unbuilt.
func TestRequestSetMigrationArchiveStepReadsItsMarkerFirst(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	if err := s.InsertEvents(ctx, []*OtelEvent{{Ts: time.Date(2026, 3, 4, 10, 5, 0, 0, time.UTC), EventName: "api_request",
		SessionID: "sess-archive-marked", UserID: "u1", Model: "model-a", Agent: "claude", BillingProvider: "anthropic",
		InputTokens: ptrInt(100), OutputTokens: ptrInt(10), CostUSD: ptrFloat(1.5)}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, usageHourlyRollupArchive); err != nil {
		t.Fatalf("insert archive marker: %v", err)
	}

	if progressed, err := s.RequestSetMigrationSteps()[0].Run(ctx); err != nil || progressed {
		t.Fatalf("step 1 with its marker there: progressed %v, err %v; want (false, nil)", progressed, err)
	}
	var rollups int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM usage_hourly_rollups`).Scan(&rollups); err != nil {
		t.Fatalf("count rollups: %v", err)
	}
	if rollups != 0 || backfillMarkedNow(t, s, usageHourlyRollupBackfill) {
		t.Errorf("step 1 built the rollups with its own marker there: %d rollup row(s), rollup marker %v",
			rollups, backfillMarkedNow(t, s, usageHourlyRollupBackfill))
	}
}

// token_rule_pending holds until the completion marker exists, whatever the
// steps have done; step is the first registered step without its marker.
func TestRequestSetMigrationStatusFollowsTheMarkers(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()

	check := func(when string, pending bool, step string) {
		t.Helper()
		got, err := s.RequestSetMigrationStatus(ctx)
		if err != nil {
			t.Fatalf("%s: RequestSetMigrationStatus: %v", when, err)
		}
		if got.TokenRulePending != pending || got.Step != step {
			t.Errorf("%s: status %+v, want pending %v at step %q", when, got, pending, step)
		}
	}
	steps := s.RequestSetMigrationSteps()
	check("before any step", true, usageHourlyRollupArchive)
	if _, err := steps[0].Run(ctx); err != nil {
		t.Fatalf("step 1: %v", err)
	}
	check("after step 1", true, sessionRecordMessageIDBackfill)
	if _, err := steps[1].Run(ctx); err != nil {
		t.Fatalf("step 2: %v", err)
	}
	check("after step 2", true, claudeImputedRequestsBackfill)
	if _, err := steps[2].Run(ctx); err != nil {
		t.Fatalf("step 3: %v", err)
	}
	check("after step 3", true, sessionOverviewTokensRecount)
	if _, err := steps[3].Run(ctx); err != nil {
		t.Fatalf("step 4: %v", err)
	}
	check("after step 4", true, usageHourlyRollupsRecount)
	if _, err := steps[4].Run(ctx); err != nil {
		t.Fatalf("step 5: %v", err)
	}
	check("after step 5", true, coverageMinutesRecount)
	if _, err := steps[5].Run(ctx); err != nil {
		t.Fatalf("step 6: %v", err)
	}
	check("after step 6", true, pluginInvocationFactsRecount)
	if _, err := steps[6].Run(ctx); err != nil {
		t.Fatalf("step 7: %v", err)
	}
	check("after step 7", true, taskSegmentTokensRecount)
	if _, err := steps[7].Run(ctx); err != nil {
		t.Fatalf("step 8: %v", err)
	}
	check("after step 8", true, requestSetMigrationDone)
	if _, err := steps[8].Run(ctx); err != nil {
		t.Fatalf("step 9: %v", err)
	}
	check("after step 9", false, "")
}

// p1t-design §8.2: a step that fails stops the transition there, and a new
// process resumes at that step -- the steps before it find their markers and
// redo nothing, and a batch step goes on from its cursor. Along the way, the
// message_id walk reports progress for a call that committed a window before a
// held row stopped it, and none for a call stopped at its first window.
func TestRequestSetMigrationResumesAtTheFailedStep(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	// Built rollups, as the boot repair leaves them, holding the fixture rows.
	if err := s.BackfillUsageHourlyRollups(ctx); err != nil {
		t.Fatalf("BackfillUsageHourlyRollups: %v", err)
	}
	insertRollupRows(t, s, archiveFixture())

	// Three windows of the walk, newest first, a row in each (at 24 hours, 0, 30
	// and 55 hours back).
	hi := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const sid = "p1t-restart"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		messageIDLine(sid, "w1", hi, `{"message":{"id":"msg_w1"}}`),
		messageIDLine(sid, "w2", hi.Add(-messageIDBackfillWindow*5/4), `{"message":{"id":"msg_w2"}}`),
		messageIDLine(sid, "w3", hi.Add(-messageIDBackfillWindow*55/24), `{"message":{"id":"msg_w3"}}`),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	forgetMessageIDs(t, s, sid)

	// boot runs the steps in order, as a new process does, up to the first that
	// fails, and answers what each step it ran reported.
	boot := func() ([]bool, error) {
		var progressed []bool
		for _, step := range s.RequestSetMigrationSteps() {
			p, err := step.Run(ctx)
			progressed = append(progressed, p)
			if err != nil {
				return progressed, err
			}
		}
		return progressed, nil
	}
	// holdRow leaves a row written and uncommitted, as sync reenrich or a
	// login_email pass would.
	holdRow := func(uuid string) pgx.Tx {
		t.Helper()
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin writer: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE session_records SET entrypoint = entrypoint WHERE uuid = $1`, uuid); err != nil {
			t.Fatalf("hold %s: %v", uuid, err)
		}
		return tx
	}
	walked := func() int {
		s.messageIDWalk.mu.Lock()
		defer s.messageIDWalk.mu.Unlock()
		return s.messageIDWalk.windows
	}

	// A writer holds the newest window's row: step 1 archives, and step 2 fails
	// on its first window, having committed nothing.
	writer := holdRow("w1")
	progressed, err := boot()
	if !isLockTimeout(err) || !slices.Equal(progressed, []bool{true, false}) {
		t.Fatalf("first boot: steps reported %v with %v; want [true false] and 55P03 from step 2", progressed, err)
	}
	if err := writer.Rollback(ctx); err != nil {
		t.Fatalf("release w1: %v", err)
	}
	archive := readArchive(t, s)
	if len(archive) == 0 || !backfillMarkedNow(t, s, usageHourlyRollupArchive) {
		t.Fatalf("step 1 did not complete before step 2 ran: %d archive row(s)", len(archive))
	}
	if status, err := s.RequestSetMigrationStatus(ctx); err != nil || status.Step != sessionRecordMessageIDBackfill {
		t.Fatalf("status after the first boot: %+v, %v; want step 2", status, err)
	}

	// An hour the archive does not have: an archive taken again would add it.
	insertRollupRows(t, s, []rollupRow{{archiveKey{time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), "claude", "anthropic", "model-a"},
		"u1", "a@example.com", "a@example.com", "team-a", "p1", archiveSums{8, 80, 8, 800, 8, 8}}})

	// Now a writer holds the second window's row: step 1 finds its marker, and
	// the walk commits the first window and stops at the second.
	writer = holdRow("w2")
	progressed, err = boot()
	if !isLockTimeout(err) || !slices.Equal(progressed, []bool{false, true}) {
		t.Fatalf("second boot: steps reported %v with %v; want [false true] and 55P03 from step 2", progressed, err)
	}
	if err := writer.Rollback(ctx); err != nil {
		t.Fatalf("release w2: %v", err)
	}
	if got := messageIDOf(t, s, "w1"); got == nil || *got != "msg_w1" {
		t.Fatalf("w1 = %s after the second boot, want \"msg_w1\"", idString(got))
	}
	for _, uuid := range []string{"w2", "w3"} {
		if got := messageIDOf(t, s, uuid); got != nil {
			t.Fatalf("%s = %s after the second boot, want NULL", uuid, idString(got))
		}
	}
	cursor, ok := readMessageIDCursor(t, s)
	if !ok || !cursor.next.Equal(hi.Add(-messageIDBackfillWindow)) {
		t.Fatalf("cursor after the second boot = %+v (present %v), want the next window ending %s", cursor, ok, hi.Add(-messageIDBackfillWindow))
	}

	// The restart that gets through walks the two windows the cursor has left,
	// then runs steps 3 to 9. w1, set back to NULL here, is in the window already
	// walked: a walk that started over would fill it again.
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET message_id = NULL WHERE uuid = 'w1'`); err != nil {
		t.Fatalf("clear w1: %v", err)
	}
	windowsBefore := walked()
	progressed, err = boot()
	if err != nil || !slices.Equal(progressed, []bool{false, true, true, true, true, true, true, true, true}) {
		t.Fatalf("third boot: steps reported %v with %v; want [false true true true true true true true true] and no error",
			progressed, err)
	}
	if n := walked() - windowsBefore; n != 2 {
		t.Errorf("the third boot walked %d window(s), want the 2 the cursor had left", n)
	}
	if got := messageIDOf(t, s, "w1"); got != nil {
		t.Errorf("w1 = %s after the third boot, want NULL: the walk went back over a window the cursor had passed", idString(got))
	}
	for uuid, want := range map[string]string{"w2": "msg_w2", "w3": "msg_w3"} {
		if got := messageIDOf(t, s, uuid); got == nil || *got != want {
			t.Errorf("%s = %s after the third boot, want %q", uuid, idString(got), want)
		}
	}
	if c, ok := readMessageIDCursor(t, s); !ok || !c.start.Equal(cursor.start) || !c.end.Equal(cursor.end) {
		t.Errorf("cursor range after the third boot = [%s, %s] (present %v), want the recorded [%s, %s]",
			c.start, c.end, ok, cursor.start, cursor.end)
	}
	if got := readArchive(t, s); !sameArchive(got, archive) {
		t.Errorf("the archive was taken again on a restart:\n got %+v\nwant %+v", got, archive)
	}
	if status, err := s.RequestSetMigrationStatus(ctx); err != nil || status.Step != "" || status.TokenRulePending {
		t.Errorf("status after the third boot: %+v, %v; want every step done and the token rule no longer pending", status, err)
	}
}

// The marker the Claude imputed refresh waits on is read as it stands.
func TestClaudeImputedRequestsReady(t *testing.T) {
	s := acquireTestStore(t)
	resetRequestSetMigration(t, s)
	ctx := context.Background()

	if ready, err := s.ClaudeImputedRequestsReady(ctx); err != nil || ready {
		t.Fatalf("without the marker: ready %v, err %v; want false", ready, err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, claudeImputedRequestsBackfill); err != nil {
		t.Fatalf("insert marker: %v", err)
	}
	if ready, err := s.ClaudeImputedRequestsReady(ctx); err != nil || !ready {
		t.Errorf("with the marker: ready %v, err %v; want true", ready, err)
	}
}

// claudeImputedRead reads claude_imputed_cost for rsFixture.rows.
const claudeImputedRead = `SELECT srec_id, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens
	FROM claude_imputed_cost`

// claudeImputedWant is a row per priced request at its head line, each token
// field the request's MAX or 0 (p1t-design §6.1), from the fixture's lines.
func claudeImputedWant(f *rsFixture) map[int64]rsTokens {
	heads, _ := f.expect(rsAll, nil)
	want := map[int64]rsTokens{}
	for id, v := range heads {
		if f.lineOf(id).model == rpRated {
			want[id] = rsTok(rsVal(v[0]), rsVal(v[1]), rsVal(v[2]), rsVal(v[3]))
		}
	}
	return want
}

func claudeImputedIndexed(t *testing.T, s *PgStore) bool {
	t.Helper()
	var indexed bool
	if err := s.pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = 'uniq_claude_imputed_srec')`).Scan(&indexed); err != nil {
		t.Fatalf("read pg_indexes: %v", err)
	}
	return indexed
}

// Step 3 commits the rewrite, the overview rows of the sessions it touches, the
// index and the marker together. With the marker write refused, after all the
// rest, the table is left as the line rule wrote it -- a row per line -- the
// overview as stale as it was, no index, and the call reports no progress. The
// next call commits all of it, a row per request and the overview recounted,
// and the one after finds the marker and does nothing.
func TestRequestSetMigration_Step3_CommitsRowsIndexAndMarkerTogether(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	f := insertRequestSet(t, s, rpOffline()...)
	rpRate(t, s)
	if _, err := s.pool.Exec(ctx, `INSERT INTO claude_imputed_cost
			(srec_id, session_id, ts, model, cost_usd, input_tokens, output_tokens, cache_read_tokens,
			 cache_create_tokens, agent)
		SELECT id, session_id, ts, model, 1, COALESCE(input_tokens,0), COALESCE(output_tokens,0),
			COALESCE(cache_read_tokens,0), COALESCE(cache_create_tokens,0), agent
		FROM session_records WHERE model = $1`, rpRated); err != nil {
		t.Fatalf("write the line rule's rows: %v", err)
	}
	lineRows := f.rows(t, s, claudeImputedRead)
	sessions := []string{"rp-rated", "rp-unrated"}
	staleOverview(t, s, sessions)
	stale := map[string]map[[2]string][2]int64{}
	for _, session := range sessions {
		if stale[session] = overviewScopes(t, s, session); len(stale[session]) == 0 {
			t.Fatalf("fixture: %s has no overview rows", session)
		}
	}
	step := s.RequestSetMigrationSteps()[2]
	if step.Marker != claudeImputedRequestsBackfill {
		t.Fatalf("step 3 is %s, want %s", step.Marker, claudeImputedRequestsBackfill)
	}

	allowMarker := refuseMarkerWrite(t, s, claudeImputedRequestsBackfill)
	if progressed, err := step.Run(ctx); err == nil || progressed {
		t.Fatalf("with the marker refused: progressed %v, err %v; want an error and no progress", progressed, err)
	}
	f.compare(t, "claude_imputed_cost after the failed step", f.rows(t, s, claudeImputedRead), lineRows)
	for _, session := range sessions {
		if got := overviewScopes(t, s, session); !maps.Equal(got, stale[session]) {
			t.Errorf("overview of %s after the failed step = %v, want it as stale as before: %v", session, got, stale[session])
		}
	}
	if claudeImputedIndexed(t, s) || backfillMarkedNow(t, s, claudeImputedRequestsBackfill) {
		t.Fatalf("the failed step left index %v, marker %v; want neither",
			claudeImputedIndexed(t, s), backfillMarkedNow(t, s, claudeImputedRequestsBackfill))
	}

	allowMarker()
	if progressed, err := step.Run(ctx); err != nil || !progressed {
		t.Fatalf("step 3: progressed %v, err %v; want progress", progressed, err)
	}
	f.compare(t, "claude_imputed_cost after step 3", f.rows(t, s, claudeImputedRead), claudeImputedWant(f))
	// Recounted by request: rp-rated from its imputed rows, rp-unrated, unpriced,
	// from its session records. Both come to the requests' tokens in every scope.
	for _, session := range sessions {
		if got, want := overviewScopes(t, s, session), f.scopeTotals(session, nil); !maps.Equal(got, want) {
			t.Errorf("overview of %s after step 3 = %v, want %v", session, got, want)
		}
	}
	if !claudeImputedIndexed(t, s) || !backfillMarkedNow(t, s, claudeImputedRequestsBackfill) {
		t.Fatalf("step 3 left index %v, marker %v; want both",
			claudeImputedIndexed(t, s), backfillMarkedNow(t, s, claudeImputedRequestsBackfill))
	}
	if progressed, err := step.Run(ctx); err != nil || progressed {
		t.Errorf("step 3 again: progressed %v, err %v; want done, no progress", progressed, err)
	}
}

// Once step 3's marker is there, cmd/cctraced runs the boot and 30-minute
// refreshes again (claudeImputedRefresher reads ClaudeImputedRequestsReady), and
// a refresh then prices a session that arrived after step 3 by request too,
// under the index step 3 built.
func TestClaudeImputedRefreshRunsByRequestAfter_Step3(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	first := rpOffline()
	insertRequestSet(t, s, first...)
	rpRate(t, s)
	if ready, err := s.ClaudeImputedRequestsReady(ctx); err != nil || ready {
		t.Fatalf("before step 3: ready %v, err %v; want false", ready, err)
	}
	if _, err := s.RequestSetMigrationSteps()[2].Run(ctx); err != nil {
		t.Fatalf("step 3: %v", err)
	}
	if ready, err := s.ClaudeImputedRequestsReady(ctx); err != nil || !ready {
		t.Fatalf("after step 3: ready %v, err %v; want true", ready, err)
	}

	late := []rsLine{
		{tag: "late-1", session: "rp-late", uuid: "late-1", at: time.Minute, msgID: "msg_late", model: rpRated,
			in: ptrInt(9), out: ptrInt(1), cr: ptrInt(70)},
		{tag: "late-2", session: "rp-late", uuid: "late-2", at: time.Minute + time.Second, msgID: "msg_late",
			model: rpRated, in: ptrInt(9), out: ptrInt(300), cr: ptrInt(70)},
	}
	f := insertRequestSet(t, s, late...)
	f.lines = append(first, late...)
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatalf("refresh after step 3: %v", err)
	}
	f.compare(t, "claude_imputed_cost after the refresh", f.rows(t, s, claudeImputedRead), claudeImputedWant(f))
}

// Two refreshes at once leave every request priced once. A writer holds one row
// of the table, so the first refresh to reach it waits with the table half
// rewritten; the second waits on the writers' lock instead of deleting the rows
// it can see and inserting beside the first one's. No index here, so a request
// priced twice shows as a duplicate srec_id rather than as one refresh failing.
func TestClaudeImputedRefreshesAtOnceLeaveNoDuplicate(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	f := insertRequestSet(t, s, rpOffline()...)
	rpRate(t, s)
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatalf("RefreshClaudeImputedCost: %v", err)
	}
	// Only changed rows are rewritten. Make this request require repricing so
	// the first writer waits on its row and the second on the writers' fence.
	if _, err := s.pool.Exec(ctx, `UPDATE claude_imputed_cost SET cost_usd=cost_usd+1 WHERE srec_id=$1`, f.id("rep-1")); err != nil {
		t.Fatal(err)
	}

	holder := holdClaudeImputedRow(t, s, f.id("rep-1"))
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- s.RefreshClaudeImputedCost(ctx) }()
	}
	awaitLockWaiters(t, s, 2)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release the row: %v", err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Errorf("refresh: %v", err)
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT srec_id, count(*) FROM claude_imputed_cost GROUP BY srec_id HAVING count(*) > 1`)
	if err != nil {
		t.Fatalf("read duplicates: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		t.Errorf("request %s priced %d times", f.tags[id], n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read duplicates: %v", err)
	}
}

// holdClaudeImputedRow locks one claude_imputed_cost row in a transaction left
// open, as a writer would, so a refresh that reaches the row waits there with
// the table half rewritten. The test's end releases it if the test did not.
func holdClaudeImputedRow(t *testing.T, s *PgStore, srecID int64) pgx.Tx {
	t.Helper()
	ctx := context.Background()
	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if _, err := holder.Exec(ctx, `SELECT 1 FROM claude_imputed_cost WHERE srec_id = $1 FOR UPDATE`, srecID); err != nil {
		t.Fatalf("hold a row: %v", err)
	}
	return holder
}

// awaitLockWaiters waits until n client backends are waiting on a lock, and
// fails the test after 10 seconds.
func awaitLockWaiters(t *testing.T, s *PgStore, n int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		var waiting int
		if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND backend_type = 'client backend'
			  AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waiting == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d backend(s) waiting on a lock after 10s, want %d", waiting, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Two first calls of step 3 at once rewrite the table once. The first is held
// up at a row a writer holds; the second, waiting on the writers' lock, reads
// the marker only once it has the lock, so it finds the first one's, does
// nothing and reports no progress.
func TestRequestSetMigration_Step3_CalledTwiceAtOnceRewritesOnce(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	f := insertRequestSet(t, s, rpOffline()...)
	rpRate(t, s)
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatalf("RefreshClaudeImputedCost: %v", err)
	}
	// Exercise a real rewrite; an unchanged refresh intentionally leaves rows
	// alone and therefore has no reason to wait on the held request.
	if _, err := s.pool.Exec(ctx, `UPDATE claude_imputed_cost SET cost_usd=cost_usd+1 WHERE srec_id=$1`, f.id("rep-1")); err != nil {
		t.Fatal(err)
	}

	holder := holdClaudeImputedRow(t, s, f.id("rep-1"))
	step := s.RequestSetMigrationSteps()[2]
	type outcome struct {
		progressed bool
		err        error
	}
	done := make(chan outcome, 2)
	for range 2 {
		go func() {
			progressed, err := step.Run(ctx)
			done <- outcome{progressed, err}
		}()
	}
	awaitLockWaiters(t, s, 2)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release the row: %v", err)
	}
	var progressed int
	for range 2 {
		o := <-done
		if o.err != nil {
			t.Errorf("step 3: %v", o.err)
		}
		if o.progressed {
			progressed++
		}
	}
	if progressed != 1 {
		t.Errorf("%d of the two calls reported progress, want 1: the second rewrote the table again", progressed)
	}
	f.compare(t, "claude_imputed_cost", f.rows(t, s, claudeImputedRead), claudeImputedWant(f))
	if !claudeImputedIndexed(t, s) || !backfillMarkedNow(t, s, claudeImputedRequestsBackfill) {
		t.Errorf("index %v, marker %v; want both", claudeImputedIndexed(t, s), backfillMarkedNow(t, s, claudeImputedRequestsBackfill))
	}
}

// overviewScopes reads a session's overview rows as (scope_type, scope_value)
// -> (input, output).
func overviewScopes(t *testing.T, s *PgStore, sessionID string) map[[2]string][2]int64 {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT scope_type, scope_value, input_tokens, output_tokens
		FROM session_overview_rollups WHERE session_id = $1`, sessionID)
	if err != nil {
		t.Fatalf("read the overview of %s: %v", sessionID, err)
	}
	defer rows.Close()
	out := map[[2]string][2]int64{}
	for rows.Next() {
		var scope [2]string
		var tokens [2]int64
		if err := rows.Scan(&scope[0], &scope[1], &tokens[0], &tokens[1]); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[scope] = tokens
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the overview of %s: %v", sessionID, err)
	}
	return out
}

// scopeTotals is what each scope of a Claude session's overview should hold,
// computed from the fixture's lines and the session's OTEL events: a scope is
// every row whose user, profile or login is its value (all of them for 'all'),
// and it reports its OTEL, field by field, unless that is 0, then its requests.
func (f *rsFixture) scopeTotals(session string, events []*OtelEvent) map[[2]string][2]int64 {
	scopesOf := func(user, profile, login string) [][2]string {
		out := [][2]string{{"all", ""}}
		for _, sc := range [][2]string{{"user", user}, {"profile", profile}, {"login", login}} {
			if sc[1] != "" {
				out = append(out, sc)
			}
		}
		return out
	}
	scopes := map[[2]string]bool{}
	for _, l := range f.lines {
		if l.session == session {
			for _, sc := range scopesOf(l.userID(), l.profileEmail(), l.login) {
				scopes[sc] = true
			}
		}
	}
	for _, e := range events {
		if e.SessionID == session {
			for _, sc := range scopesOf(e.UserID, e.ProfileEmail, e.LoginEmail) {
				scopes[sc] = true
			}
		}
	}
	want := map[[2]string][2]int64{}
	for sc := range scopes {
		in := func(user, profile, login string) bool { return slices.Contains(scopesOf(user, profile, login), sc) }
		requests := f.totals(func(l rsLine) bool {
			return l.session == session && in(l.userID(), l.profileEmail(), l.login)
		})[session]
		var otel [2]int64
		for _, e := range events {
			if e.SessionID == session && in(e.UserID, e.ProfileEmail, e.LoginEmail) {
				otel[0] += int64(*e.InputTokens)
				otel[1] += int64(*e.OutputTokens)
			}
		}
		if otel[0] == 0 {
			otel[0] = requests.in
		}
		if otel[1] == 0 {
			otel[1] = requests.out
		}
		want[sc] = otel
	}
	return want
}

// staleOverview overwrites the sessions' overview rows, every scope, with a
// value no recount produces.
func staleOverview(t *testing.T, s *PgStore, sessions []string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `UPDATE session_overview_rollups
		SET input_tokens = -1, output_tokens = -1 WHERE session_id = ANY($1::text[])`, sessions); err != nil {
		t.Fatalf("stale the overview: %v", err)
	}
}

// Step 4 recounts every Claude and Codex session of the overview, a batch per
// transaction in session_id order, every scope of a session at once. A failure
// in the second batch leaves the first committed and the cursor after it; the
// next call goes on from the cursor -- the first batch, set stale again here, is
// not walked twice -- and commits its marker with the last batch. One session of
// the second batch has OTEL under profile A and JSONL under profile B, so its
// profile B row is a scope whose tokens come from the session records; the gjc
// session is not the step's to recount and stays as it was.
func TestRequestSetMigration_Step4_ResumesAtItsCursorAndRecountsEveryScope(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	// A walk that never reaches its end -- one that does not advance -- fails on
	// this deadline instead of holding the package until its timeout.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	sessionAt := func(i int) string { return fmt.Sprintf("rp-ovt-%03d", i) }
	const claudeSessions = sessionOverviewTokensBatchSize + 50
	const abAt, failingAt = sessionOverviewTokensBatchSize + 20, sessionOverviewTokensBatchSize + 30
	ab, failing := sessionAt(abAt), sessionAt(failingAt)
	var lines []rsLine
	var walked []string // the step's sessions, in session_id order
	for i := range claudeSessions {
		id, profile := sessionAt(i), ""
		if id == ab {
			profile = "b@example.com"
		}
		lines = append(lines,
			rsLine{tag: id + "-1", session: id, uuid: id + "-1", msgID: "msg_" + id, profile: profile,
				login: "l@example.com", in: ptrInt(10 + i), out: ptrInt(1)},
			rsLine{tag: id + "-2", session: id, uuid: id + "-2", at: time.Second, msgID: "msg_" + id, profile: profile,
				login: "l@example.com", in: ptrInt(10 + i), out: ptrInt(523)})
		walked = append(walked, id)
	}
	lines = append(lines,
		rsLine{tag: "rp-ovt-cx", session: "rp-ovt-cx", uuid: "rp-ovt-cx", agent: "codex", recType: "usage",
			model: "codex-model", login: "l@example.com", in: ptrInt(100), out: ptrInt(10), cr: ptrInt(40)},
		rsLine{tag: "rp-ovt-gjc", session: "rp-ovt-gjc", uuid: "rp-ovt-gjc", agent: "gjc", msgID: "msg_gjc",
			login: "l@example.com", in: ptrInt(11), out: ptrInt(12)})
	walked = append(walked, "rp-ovt-cx")
	f := insertRequestSet(t, s, lines...)
	refreshCodexImputed(t, s)
	events := []*OtelEvent{rpOTELEvent(ab, "a@example.com", 0, 700, 30, 0.375)}
	if err := s.InsertEvents(ctx, events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	want := func(session string) map[[2]string][2]int64 {
		if session == "rp-ovt-cx" {
			// Its codex_imputed_cost row, input net of cache reads, in every scope.
			w := [2]int64{60, 10}
			return map[[2]string][2]int64{{"all", ""}: w, {"user", "uid-rs"}: w, {"profile", "rs@example.com"}: w,
				{"login", "l@example.com"}: w}
		}
		return f.scopeTotals(session, events)
	}
	if w := want(ab); w[[2]string{"profile", "b@example.com"}] == w[[2]string{"all", ""}] {
		t.Fatalf("fixture: %s reports the same in profile B as overall: %v", ab, w)
	}
	staleOverview(t, s, append(walked, "rp-ovt-gjc"))
	stale := func(session string) bool {
		for _, tokens := range overviewScopes(t, s, session) {
			if tokens != [2]int64{-1, -1} {
				return false
			}
		}
		return true
	}
	recounted := func(session string) bool {
		got, w := overviewScopes(t, s, session), want(session)
		if len(got) != len(w) {
			return false
		}
		for sc, tokens := range w {
			if got[sc] != tokens {
				return false
			}
		}
		return true
	}
	firstBatch, rest := walked[:sessionOverviewTokensBatchSize], walked[sessionOverviewTokensBatchSize:]

	exec := func(stmt string) {
		t.Helper()
		// Not ctx: allow runs again at cleanup, after ctx is cancelled.
		if _, err := s.pool.Exec(context.Background(), stmt); err != nil {
			t.Fatalf("%s: %v", firstLine(stmt), err)
		}
	}
	allow := func() {
		exec(`DROP TRIGGER IF EXISTS refuse_overview_insert ON session_overview_rollups`)
		exec(`DROP FUNCTION IF EXISTS refuse_overview_insert()`)
	}
	exec(`CREATE FUNCTION refuse_overview_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'overview insert refused by the test: %', NEW.session_id; END $$`)
	exec(`CREATE TRIGGER refuse_overview_insert BEFORE INSERT ON session_overview_rollups FOR EACH ROW
		WHEN (NEW.session_id = '` + failing + `') EXECUTE FUNCTION refuse_overview_insert()`)
	t.Cleanup(allow)

	step := s.RequestSetMigrationSteps()[3]
	if step.Marker != sessionOverviewTokensRecount {
		t.Fatalf("step 4 is %s, want %s", step.Marker, sessionOverviewTokensRecount)
	}
	if progressed, err := step.Run(ctx); err == nil || !strings.Contains(err.Error(), "refused by the test") || !progressed {
		t.Fatalf("with %s refused: progressed %v, err %v; want the first batch committed, then the refusal", failing, progressed, err)
	}
	var cursor string
	if err := s.pool.QueryRow(ctx, `SELECT last_session_id FROM session_overview_tokens_cursor`).Scan(&cursor); err != nil {
		t.Fatalf("read the cursor: %v", err)
	}
	if want := firstBatch[len(firstBatch)-1]; cursor != want {
		t.Fatalf("cursor after the failure = %q, want %q", cursor, want)
	}
	for _, session := range firstBatch {
		if !recounted(session) {
			t.Fatalf("%s after the first batch = %v, want %v", session, overviewScopes(t, s, session), want(session))
		}
	}
	for _, session := range rest {
		if !stale(session) {
			t.Fatalf("%s was recounted by a batch that failed: %v", session, overviewScopes(t, s, session))
		}
	}
	if backfillMarkedNow(t, s, sessionOverviewTokensRecount) {
		t.Fatal("the marker committed with the walk unfinished")
	}

	staleOverview(t, s, firstBatch)
	allow()
	if progressed, err := step.Run(ctx); err != nil || !progressed {
		t.Fatalf("step 4 resumed: progressed %v, err %v; want progress", progressed, err)
	}
	for _, session := range firstBatch {
		if !stale(session) {
			t.Fatalf("%s, behind the cursor, was recounted again: %v", session, overviewScopes(t, s, session))
		}
	}
	for _, session := range rest {
		if !recounted(session) {
			t.Errorf("%s = %v, want %v", session, overviewScopes(t, s, session), want(session))
		}
	}
	if !stale("rp-ovt-gjc") {
		t.Errorf("the gjc session was recounted: %v", overviewScopes(t, s, "rp-ovt-gjc"))
	}
	if !backfillMarkedNow(t, s, sessionOverviewTokensRecount) {
		t.Error("the walk is done and its marker is missing")
	}
	if progressed, err := step.Run(ctx); err != nil || progressed {
		t.Errorf("step 4 again: progressed %v, err %v; want done, no progress", progressed, err)
	}
}

// Without session_overview_rollups_v1 the overview was never built, and the boot
// repair builds it with this binary's rules: step 4 writes its marker and walks
// nothing -- the stale rows here stay stale -- and it does not build the
// overview itself.
func TestRequestSetMigration_Step4_OnlyMarksAnOverviewNeverBuilt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	insertRequestSet(t, s,
		rsLine{tag: "nb-1", session: "rp-never-built", uuid: "nb-1", msgID: "msg_nb", in: ptrInt(10), out: ptrInt(1)},
		rsLine{tag: "nb-2", session: "rp-never-built", uuid: "nb-2", at: time.Second, msgID: "msg_nb",
			in: ptrInt(10), out: ptrInt(523)})
	staleOverview(t, s, []string{"rp-never-built"})
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, sessionOverviewRollupBackfill); err != nil {
		t.Fatalf("delete the overview marker: %v", err)
	}
	t.Cleanup(func() {
		if err := s.BackfillSessionOverviewRollups(context.Background()); err != nil {
			t.Errorf("rebuild the overview: %v", err)
		}
	})

	if progressed, err := s.RequestSetMigrationSteps()[3].Run(ctx); err != nil || !progressed {
		t.Fatalf("step 4: progressed %v, err %v; want its marker committed", progressed, err)
	}
	if !backfillMarkedNow(t, s, sessionOverviewTokensRecount) {
		t.Error("step 4 left no marker")
	}
	if backfillMarkedNow(t, s, sessionOverviewRollupBackfill) {
		t.Error("step 4 built the overview")
	}
	for scope, tokens := range overviewScopes(t, s, "rp-never-built") {
		if tokens != [2]int64{-1, -1} {
			t.Errorf("step 4 recounted %v: %v", scope, tokens)
		}
	}
	var cursors int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_overview_tokens_cursor`).Scan(&cursors); err != nil {
		t.Fatalf("read the cursor: %v", err)
	}
	if cursors != 0 {
		t.Errorf("step 4 started a walk: %d cursor row(s)", cursors)
	}
}

// p1t-design §8.2, F5: the rollback runbook deletes step 4's marker and its
// cursor in one transaction, and the next call recounts from the first session
// again. The marker alone is not enough -- the cursor, left at the last session,
// has the next call find nothing after it and mark the walk done without
// recounting a session -- which is why the runbook deletes both.
func TestRequestSetMigration_Step4_StartsOverWithoutItsMarkerAndCursor(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	sessions := []string{"rp-f5-a", "rp-f5-b", "rp-f5-c"}
	var lines []rsLine
	for i, id := range sessions {
		lines = append(lines,
			rsLine{tag: id + "-1", session: id, uuid: id + "-1", msgID: "msg_" + id, in: ptrInt(10 + i), out: ptrInt(1)},
			rsLine{tag: id + "-2", session: id, uuid: id + "-2", at: time.Second, msgID: "msg_" + id,
				in: ptrInt(10 + i), out: ptrInt(523)})
	}
	f := insertRequestSet(t, s, lines...)
	step := s.RequestSetMigrationSteps()[3]
	if _, err := step.Run(ctx); err != nil || !backfillMarkedNow(t, s, sessionOverviewTokensRecount) {
		t.Fatalf("the first walk: err %v, marker %v; want it done", err, backfillMarkedNow(t, s, sessionOverviewTokensRecount))
	}
	run := func(when string) {
		t.Helper()
		if progressed, err := step.Run(ctx); err != nil || !progressed {
			t.Fatalf("%s: progressed %v, err %v; want the step run again", when, progressed, err)
		}
	}

	staleOverview(t, s, sessions)
	stale := map[string]map[[2]string][2]int64{}
	for _, session := range sessions {
		stale[session] = overviewScopes(t, s, session)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, sessionOverviewTokensRecount); err != nil {
		t.Fatalf("delete the marker: %v", err)
	}
	run("with the marker deleted")
	for _, session := range sessions {
		if got := overviewScopes(t, s, session); len(got) == 0 || !maps.Equal(got, stale[session]) {
			t.Errorf("with the cursor left, %s = %v, want it as stale as before: %v", session, got, stale[session])
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, stmt := range []string{
		`DELETE FROM schema_backfills WHERE name = '` + sessionOverviewTokensRecount + `'`,
		`DELETE FROM session_overview_tokens_cursor`,
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	run("with the marker and the cursor deleted")
	for _, session := range sessions {
		if got, want := overviewScopes(t, s, session), f.scopeTotals(session, nil); !maps.Equal(got, want) {
			t.Errorf("%s after the walk started over = %v, want %v", session, got, want)
		}
	}
	if !backfillMarkedNow(t, s, sessionOverviewTokensRecount) {
		t.Error("the walk started over and its marker is missing")
	}
}

// Step 7 rebuilds every plugin fact by request and commits its marker with them.
// With the marker write refused, the facts are left as they were -- stale here,
// as facts counted line by line would be -- and the call reports no progress.
// The next call rebuilds them to the requests' numbers, and the one after finds
// the marker and does nothing.
func TestRequestSetMigration_Step7_RebuildsThePluginFactsWithItsMarker(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	keepMarker(t, s, pluginInvocationFactsBackfill)
	setMarker(t, s, pluginInvocationFactsBackfill, true)
	ctx := context.Background()
	lines, codex, commands, _ := rsPluginFixture()
	f := insertRequestSet(t, s, lines...)
	want := f.commandTokens(codex, commands...)
	if _, err := s.pool.Exec(ctx, `UPDATE plugin_invocation_facts SET input_tokens = -1, output_tokens = -1`); err != nil {
		t.Fatalf("stale the facts: %v", err)
	}
	stale := map[string][2]int64{}
	for _, tag := range commands {
		stale[tag] = [2]int64{-1, -1}
	}
	step := s.RequestSetMigrationSteps()[6]
	if step.Marker != pluginInvocationFactsRecount {
		t.Fatalf("step 7 is %s, want %s", step.Marker, pluginInvocationFactsRecount)
	}

	allowMarker := refuseMarkerWrite(t, s, pluginInvocationFactsRecount)
	if progressed, err := step.Run(ctx); err == nil || progressed {
		t.Fatalf("with the marker refused: progressed %v, err %v; want an error and no progress", progressed, err)
	}
	if got := f.commandFactTokens(t, s); !maps.Equal(got, stale) || backfillMarkedNow(t, s, pluginInvocationFactsRecount) {
		t.Fatalf("after the failed step: facts %v, marker %v; want them as stale as before %v and no marker",
			got, backfillMarkedNow(t, s, pluginInvocationFactsRecount), stale)
	}

	allowMarker()
	if progressed, err := step.Run(ctx); err != nil || !progressed {
		t.Fatalf("step 7: progressed %v, err %v; want progress", progressed, err)
	}
	if got := f.commandFactTokens(t, s); !maps.Equal(got, want) || !backfillMarkedNow(t, s, pluginInvocationFactsRecount) {
		t.Errorf("after step 7: facts %v, marker %v; want %v and the marker",
			got, backfillMarkedNow(t, s, pluginInvocationFactsRecount), want)
	}
	if progressed, err := step.Run(ctx); err != nil || progressed {
		t.Errorf("step 7 again: progressed %v, err %v; want done, no progress", progressed, err)
	}
}

// Without plugin_invocation_facts_v2 the facts were never built, and the boot
// repair builds them with this binary's rules: step 7 writes its marker and
// rebuilds nothing -- the stale facts here stay stale -- and does not build the
// facts itself.
func TestRequestSetMigration_Step7_OnlyMarksFactsNeverBuilt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	keepMarker(t, s, pluginInvocationFactsBackfill)
	setMarker(t, s, pluginInvocationFactsBackfill, false)
	ctx := context.Background()
	lines, _, commands, _ := rsPluginFixture()
	f := insertRequestSet(t, s, lines...)
	if _, err := s.pool.Exec(ctx, `UPDATE plugin_invocation_facts SET input_tokens = -1, output_tokens = -1`); err != nil {
		t.Fatalf("stale the facts: %v", err)
	}

	if progressed, err := s.RequestSetMigrationSteps()[6].Run(ctx); err != nil || !progressed {
		t.Fatalf("step 7: progressed %v, err %v; want its marker committed", progressed, err)
	}
	if !backfillMarkedNow(t, s, pluginInvocationFactsRecount) || backfillMarkedNow(t, s, pluginInvocationFactsBackfill) {
		t.Errorf("step 7 left its marker %v and the facts' build marker %v; want only its own",
			backfillMarkedNow(t, s, pluginInvocationFactsRecount), backfillMarkedNow(t, s, pluginInvocationFactsBackfill))
	}
	got := f.commandFactTokens(t, s)
	for _, tag := range commands {
		if got[tag] != [2]int64{-1, -1} {
			t.Errorf("step 7 rebuilt %s: %v", tag, got[tag])
		}
	}
}

// segmentRowsBesideTokens reads every column of the sessions' task segments but
// the two token columns, by boundary_record_id.
func segmentRowsBesideTokens(t *testing.T, s *PgStore, sessions []string) map[int64]string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT boundary_record_id,
			(to_jsonb(f) - 'input_tokens' - 'output_tokens')::text
		FROM task_segment_facts f WHERE session_id = ANY($1::text[])`, sessions)
	if err != nil {
		t.Fatalf("read the task segments: %v", err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var row string
		if err := rows.Scan(&id, &row); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the task segments: %v", err)
	}
	return out
}

// fixtureTaskType is the task type the step 8 fixtures give their typed turns
// themselves, rather than leave to the classifier, so TaskSegmentsByType finds
// their segments.
const fixtureTaskType = "p1t-fixture"

// markTaskType gives the sessions' user rows fixtureTaskType.
func markTaskType(t *testing.T, s *PgStore, sessions ...string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `UPDATE session_records SET task_type = $1
		WHERE record_type = 'user' AND session_id = ANY($2::text[])`, fixtureTaskType, sessions); err != nil {
		t.Fatalf("mark the task type: %v", err)
	}
}

// tokensReady reads TaskSegment.TokensReady off every segment of the step 8
// fixture's period, and fails unless they all say the same.
func tokensReady(t *testing.T, s *PgStore, segments int) bool {
	t.Helper()
	page, err := s.TaskSegmentsByType(context.Background(), rsBase.Add(-time.Hour), rsBase.Add(time.Hour),
		fixtureTaskType, "", "uid-rs")
	if err != nil {
		t.Fatalf("TaskSegmentsByType: %v", err)
	}
	if len(page.Segments) != segments {
		t.Fatalf("TaskSegmentsByType returned %d segment(s), want %d", len(page.Segments), segments)
	}
	for _, seg := range page.Segments {
		if seg.TokensReady != page.Segments[0].TokensReady {
			t.Fatalf("tokens_ready differs between segments of one read")
		}
	}
	return page.Segments[0].TokensReady
}

// Step 8 recounts the token columns of every stored segment, a batch of
// sessions per transaction in session_id order, and nothing else: tool counts,
// command counts and activity types set here as an expired session's OTEL left
// them stay as they are. A failure in the second batch leaves the first
// committed and the cursor after it; the next call goes on from the cursor --
// the first batch, set stale again here, is not walked twice -- and commits the
// marker with the last batch. tokens_ready is false until that marker commits.
func TestRequestSetMigration_Step8_ResumesAtItsCursorAndUpdatesOnlyTheTokens(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	keepMarker(t, s, taskSegmentFactsBackfill)
	setMarker(t, s, taskSegmentFactsBackfill, true)
	// A walk that never reaches its end fails on this deadline instead of holding
	// the package until its timeout.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	exec := func(stmt string, args ...any) {
		t.Helper()
		// Not ctx: allow runs again at cleanup, after ctx is cancelled.
		if _, err := s.pool.Exec(context.Background(), stmt, args...); err != nil {
			t.Fatalf("%s: %v", firstLine(stmt), err)
		}
	}
	// task_segment_facts is not among the tables truncateTables empties, and the
	// walk takes every session it holds.
	exec(`DELETE FROM task_segment_facts`)

	sessionAt := func(i int) string { return fmt.Sprintf("sf-step8-%03d", i) }
	const sessions = taskSegmentTokensBatchSize + 50
	var lines []rsLine
	var walked, opens []string
	for i := range sessions {
		id := sessionAt(i)
		lines = append(lines, rsTurn(id+"-turn", id, "", 0),
			rsLine{tag: id + "-1", session: id, uuid: id + "-1", at: time.Second, msgID: "msg_" + id,
				in: ptrInt(10 + i), out: ptrInt(1)},
			rsLine{tag: id + "-2", session: id, uuid: id + "-2", at: 2 * time.Second, msgID: "msg_" + id,
				in: ptrInt(10 + i), out: ptrInt(523)})
		walked = append(walked, id)
		opens = append(opens, id+"-turn")
	}
	f := insertRequestSet(t, s, lines...)
	markTaskType(t, s, walked...)
	want := f.segmentTokens(nil, opens...)
	exec(`UPDATE task_segment_facts SET input_tokens = -1, output_tokens = -1,
		tool_call_count = 7, tool_fail_count = 2, command_count = 3, activity_type = 'author'`)
	besides := segmentRowsBesideTokens(t, s, walked)
	if len(besides) != sessions {
		t.Fatalf("fixture: %d segment(s), want one per session", len(besides))
	}
	firstBatch, rest := walked[:taskSegmentTokensBatchSize], walked[taskSegmentTokensBatchSize:]
	recounted := func(group []string) bool {
		got := f.segmentFactTokens(t, s, group...)
		for _, session := range group {
			if got[session+"-turn"] != want[session+"-turn"] {
				return false
			}
		}
		return true
	}
	stale := func(group []string) bool {
		for _, tokens := range f.segmentFactTokens(t, s, group...) {
			if tokens != [2]int64{-1, -1} {
				return false
			}
		}
		return true
	}
	if tokensReady(t, s, sessions) {
		t.Fatal("tokens_ready before step 8")
	}

	failing := sessionAt(taskSegmentTokensBatchSize + 20)
	allow := func() {
		exec(`DROP TRIGGER IF EXISTS refuse_segment_update ON task_segment_facts`)
		exec(`DROP FUNCTION IF EXISTS refuse_segment_update()`)
	}
	exec(`CREATE FUNCTION refuse_segment_update() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'segment update refused by the test: %', NEW.session_id; END $$`)
	exec(`CREATE TRIGGER refuse_segment_update BEFORE UPDATE ON task_segment_facts FOR EACH ROW
		WHEN (NEW.session_id = '` + failing + `') EXECUTE FUNCTION refuse_segment_update()`)
	t.Cleanup(allow)

	step := s.RequestSetMigrationSteps()[7]
	if step.Marker != taskSegmentTokensRecount {
		t.Fatalf("step 8 is %s, want %s", step.Marker, taskSegmentTokensRecount)
	}
	if progressed, err := step.Run(ctx); err == nil || !strings.Contains(err.Error(), "refused by the test") || !progressed {
		t.Fatalf("with %s refused: progressed %v, err %v; want the first batch committed, then the refusal", failing, progressed, err)
	}
	var cursor string
	if err := s.pool.QueryRow(ctx, `SELECT last_session_id FROM task_segment_tokens_cursor`).Scan(&cursor); err != nil {
		t.Fatalf("read the cursor: %v", err)
	}
	if want := firstBatch[len(firstBatch)-1]; cursor != want {
		t.Fatalf("cursor after the failure = %q, want %q", cursor, want)
	}
	if !recounted(firstBatch) || !stale(rest) {
		t.Fatalf("after the failure: first batch recounted %v, the rest stale %v; want both", recounted(firstBatch), stale(rest))
	}
	if backfillMarkedNow(t, s, taskSegmentTokensRecount) || tokensReady(t, s, sessions) {
		t.Fatal("the marker committed, or tokens_ready turned, with the walk unfinished")
	}

	staleSegmentTokens(t, s, firstBatch...)
	allow()
	if progressed, err := step.Run(ctx); err != nil || !progressed {
		t.Fatalf("step 8 resumed: progressed %v, err %v; want progress", progressed, err)
	}
	if !stale(firstBatch) {
		t.Error("the first batch, behind the cursor, was recounted again")
	}
	if !recounted(rest) {
		t.Errorf("the rest = %v, want %v", f.segmentFactTokens(t, s, rest...), want)
	}
	if got := segmentRowsBesideTokens(t, s, walked); !maps.Equal(got, besides) {
		t.Error("step 8 changed a column other than the tokens")
	}
	if !backfillMarkedNow(t, s, taskSegmentTokensRecount) || !tokensReady(t, s, sessions) {
		t.Errorf("the walk is done: marker %v, tokens_ready %v; want both",
			backfillMarkedNow(t, s, taskSegmentTokensRecount), tokensReady(t, s, sessions))
	}
	if progressed, err := step.Run(ctx); err != nil || progressed {
		t.Errorf("step 8 again: progressed %v, err %v; want done, no progress", progressed, err)
	}
}

// Without task_segment_facts_v1 the facts were never built, and the boot repair
// builds them with this binary's rules: step 8 writes its marker and walks
// nothing -- the stale tokens here stay stale, and no cursor row is made -- and
// does not build the facts itself. tokens_ready follows the marker.
func TestRequestSetMigration_Step8_OnlyMarksFactsNeverBuilt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	keepMarker(t, s, taskSegmentFactsBackfill)
	setMarker(t, s, taskSegmentFactsBackfill, false)
	ctx := context.Background()
	f := insertRequestSet(t, s,
		rsTurn("nb-turn", "sf-never-built", "", 0),
		rsLine{tag: "nb-1", session: "sf-never-built", uuid: "nb-1", at: time.Second, msgID: "msg_nb", in: ptrInt(10), out: ptrInt(1)},
		rsLine{tag: "nb-2", session: "sf-never-built", uuid: "nb-2", at: 2 * time.Second, msgID: "msg_nb",
			in: ptrInt(10), out: ptrInt(523)})
	markTaskType(t, s, "sf-never-built")
	staleSegmentTokens(t, s, "sf-never-built")
	if tokensReady(t, s, 1) {
		t.Fatal("tokens_ready before step 8")
	}

	if progressed, err := s.RequestSetMigrationSteps()[7].Run(ctx); err != nil || !progressed {
		t.Fatalf("step 8: progressed %v, err %v; want its marker committed", progressed, err)
	}
	if !backfillMarkedNow(t, s, taskSegmentTokensRecount) || backfillMarkedNow(t, s, taskSegmentFactsBackfill) {
		t.Errorf("step 8 left its marker %v and the facts' build marker %v; want only its own",
			backfillMarkedNow(t, s, taskSegmentTokensRecount), backfillMarkedNow(t, s, taskSegmentFactsBackfill))
	}
	if got := f.segmentFactTokens(t, s, "sf-never-built"); got["nb-turn"] != [2]int64{-1, -1} {
		t.Errorf("step 8 recounted: %v", got)
	}
	var cursors int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM task_segment_tokens_cursor`).Scan(&cursors); err != nil {
		t.Fatalf("read the cursor: %v", err)
	}
	if cursors != 0 {
		t.Errorf("step 8 started a walk: %d cursor row(s)", cursors)
	}
	if !tokensReady(t, s, 1) {
		t.Error("tokens_ready is false with the step's marker there")
	}
}

// Two first calls of step 7 at once rebuild the facts once. A sync holding the
// facts' shared lock, as refreshPluginInvocationFacts does, holds both at the
// exclusive one; the second reads the marker only once it has that lock, so it
// finds the first one's, does nothing and reports no progress.
func TestRequestSetMigration_Step7_CalledTwiceAtOnceRebuildsOnce(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	keepMarker(t, s, pluginInvocationFactsBackfill)
	setMarker(t, s, pluginInvocationFactsBackfill, true)
	ctx := context.Background()
	lines, codex, commands, _ := rsPluginFixture()
	f := insertRequestSet(t, s, lines...)
	want := f.commandTokens(codex, commands...)
	if _, err := s.pool.Exec(ctx, `UPDATE plugin_invocation_facts SET input_tokens = -1, output_tokens = -1`); err != nil {
		t.Fatalf("stale the facts: %v", err)
	}

	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if err := lockPluginInvocationFacts(ctx, holder, false); err != nil {
		t.Fatal(err)
	}
	step := s.RequestSetMigrationSteps()[6]
	type outcome struct {
		progressed bool
		err        error
	}
	done := make(chan outcome, 2)
	for range 2 {
		go func() {
			progressed, err := step.Run(ctx)
			done <- outcome{progressed, err}
		}()
	}
	awaitLockWaiters(t, s, 2)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release the lock: %v", err)
	}
	var progressed int
	for range 2 {
		o := <-done
		if o.err != nil {
			t.Errorf("step 7: %v", o.err)
		}
		if o.progressed {
			progressed++
		}
	}
	if progressed != 1 {
		t.Errorf("%d of the two calls reported progress, want 1: the second rebuilt the facts again", progressed)
	}
	if got := f.commandFactTokens(t, s); !maps.Equal(got, want) || !backfillMarkedNow(t, s, pluginInvocationFactsRecount) {
		t.Errorf("facts %v, marker %v; want %v and the marker", got, backfillMarkedNow(t, s, pluginInvocationFactsRecount), want)
	}
}

// Step 8 takes the locks refreshTaskSegmentFacts takes: a sync holding them --
// the facts' shared lock, then the session's own -- holds the step's batch until
// it commits, and the step then recounts the session.
func TestRequestSetMigration_Step8_WaitsForASessionsWriter(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	keepMarker(t, s, taskSegmentFactsBackfill)
	setMarker(t, s, taskSegmentFactsBackfill, true)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM task_segment_facts`); err != nil {
		t.Fatalf("clear task_segment_facts: %v", err)
	}
	const session = "sf-writer"
	f := insertRequestSet(t, s,
		rsTurn("w-turn", session, "", 0),
		rsLine{tag: "w-1", session: session, uuid: "w-1", at: time.Second, msgID: "msg_w", in: ptrInt(10), out: ptrInt(1)},
		rsLine{tag: "w-2", session: session, uuid: "w-2", at: 2 * time.Second, msgID: "msg_w", in: ptrInt(10), out: ptrInt(523)})
	want := f.segmentTokens(nil, "w-turn")
	staleSegmentTokens(t, s, session)

	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
	if err := lockTaskSegmentFacts(ctx, holder, false); err != nil {
		t.Fatal(err)
	}
	if err := lockTaskSegmentSessions(ctx, holder, []string{session}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.RequestSetMigrationSteps()[7].Run(ctx)
		done <- err
	}()
	awaitLockWaiters(t, s, 1)
	if got := f.segmentFactTokens(t, s, session); got["w-turn"] != [2]int64{-1, -1} || backfillMarkedNow(t, s, taskSegmentTokensRecount) {
		t.Fatalf("step 8 went past the writer: tokens %v, marker %v", got, backfillMarkedNow(t, s, taskSegmentTokensRecount))
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("release the locks: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("step 8: %v", err)
	}
	if got := f.segmentFactTokens(t, s, session); !maps.Equal(got, want) || !backfillMarkedNow(t, s, taskSegmentTokensRecount) {
		t.Errorf("after the writer: tokens %v, marker %v; want %v and the marker",
			got, backfillMarkedNow(t, s, taskSegmentTokensRecount), want)
	}
}

// visibleRow is a row of visible_events as the tests of steps 5 and 6 state it in
// Go: an OTEL event, or a claude_imputed_cost row.
type visibleRow struct {
	ts                                                         time.Time
	session, model, user, profile, login, team, agent, billing string
	cost                                                       float64
	in, out, cacheRead, cacheCreate                            int64
}

func eventRows(events ...*OtelEvent) []visibleRow {
	n := func(p *int) int64 {
		if p == nil {
			return 0
		}
		return int64(*p)
	}
	out := make([]visibleRow, 0, len(events))
	for _, e := range events {
		r := visibleRow{ts: e.Ts, session: e.SessionID, model: e.Model, user: e.UserID, profile: e.ProfileEmail,
			login: e.LoginEmail, team: e.UserTeam, agent: e.Agent, billing: e.BillingProvider,
			in: n(e.InputTokens), out: n(e.OutputTokens), cacheRead: n(e.CacheReadTokens), cacheCreate: n(e.CacheCreateTokens)}
		if e.CostUSD != nil {
			r.cost = *e.CostUSD
		}
		out = append(out, r)
	}
	return out
}

// imputedRows is claude_imputed_cost as step 3 writes it for the lines keep
// selects: a row per request whose head has rpRated, at its head line, each token
// field the request's MAX, priced at rpScale (p1t-design §6.1).
func (f *rsFixture) imputedRows(keep func(rsLine) bool) []visibleRow {
	heads, _ := f.expect(keep, nil)
	var out []visibleRow
	for id, v := range heads {
		head := f.lineOf(id)
		if head.model != rpRated {
			continue
		}
		in, o, cr, cw := rsVal(v[0]), rsVal(v[1]), rsVal(v[2]), rsVal(v[3])
		out = append(out, visibleRow{ts: rsBase.Add(head.at), session: head.session, model: head.model,
			user: head.userID(), profile: head.profileEmail(), login: head.login, agent: "claude", billing: "anthropic",
			cost: weighted(head.model, int(in), int(o), int(cr), int(cw)) * rpScale, in: in, out: o, cacheRead: cr, cacheCreate: cw})
	}
	return out
}

// lineRuleCost is what each line costs in the claude_imputed_cost rows
// writeLineRuleRows leaves.
const lineRuleCost = 0.0625

// writeLineRuleRows writes a session's claude_imputed_cost as an older binary left
// it, a row per assistant line priced lineRuleCost, and returns those rows.
func (f *rsFixture) writeLineRuleRows(t *testing.T, s *PgStore, session string) []visibleRow {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO claude_imputed_cost
			(srec_id, session_id, ts, user_id, profile_email, login_email, model, cost_usd,
			 input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, agent, billing_provider)
		SELECT id, session_id, ts, user_id, profile_email, login_email, model, $2,
			COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(cache_read_tokens, 0),
			COALESCE(cache_create_tokens, 0), agent, billing_provider
		FROM session_records WHERE session_id = $1 AND record_type = 'assistant'`, session, lineRuleCost); err != nil {
		t.Fatalf("write the line rule's rows of %s: %v", session, err)
	}
	var out []visibleRow
	for _, l := range f.lines {
		if l.session == session && l.recordType() == "assistant" {
			v := l.tokens()
			out = append(out, visibleRow{ts: rsBase.Add(l.at), session: session, model: l.model, user: l.userID(),
				profile: l.profileEmail(), login: l.login, agent: "claude", billing: "anthropic", cost: lineRuleCost,
				in: rsVal(v[0]), out: rsVal(v[1]), cacheRead: rsVal(v[2]), cacheCreate: rsVal(v[3])})
		}
	}
	return out
}

// rollupRowKey is a usage_hourly_rollups primary key.
type rollupRowKey struct {
	bucket                                                     time.Time
	model, user, profile, login, team, agent, billing, project string
}

// rollupsOf is usage_hourly_rollups recounted from rows: their sums by UTC hour
// and the rest of the key, each row's project the one its session's lines name.
func rollupsOf(projects map[string]string, rows ...visibleRow) map[rollupRowKey]archiveSums {
	out := map[rollupRowKey]archiveSums{}
	for _, r := range rows {
		k := rollupRowKey{bucket: r.ts.UTC().Truncate(time.Hour), model: r.model, user: r.user, profile: r.profile,
			login: r.login, team: r.team, agent: r.agent, billing: r.billing, project: projects[r.session]}
		sum := out[k]
		sum.cost += r.cost
		sum.input += r.in
		sum.output += r.out
		sum.cacheRead += r.cacheRead
		sum.cacheCreate += r.cacheCreate
		sum.events++
		out[k] = sum
	}
	return out
}

// archiveOf is what step 1 archives of rollups: their sums by archive key.
func archiveOf(rollups map[rollupRowKey]archiveSums) map[archiveKey]archiveSums {
	out := map[archiveKey]archiveSums{}
	for k, v := range rollups {
		ak := archiveKey{k.bucket, k.agent, k.billing, k.model}
		sum := out[ak]
		sum.cost += v.cost
		sum.input += v.input
		sum.output += v.output
		sum.cacheRead += v.cacheRead
		sum.cacheCreate += v.cacheCreate
		sum.events += v.events
		out[ak] = sum
	}
	return out
}

func readRollups(t *testing.T, s *PgStore) map[rollupRowKey]archiveSums {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT bucket, model, user_id, profile_email, login_email,
			user_team, agent, billing_provider, project_hash,
			cost_usd, input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, event_count
		FROM usage_hourly_rollups`)
	if err != nil {
		t.Fatalf("read rollups: %v", err)
	}
	defer rows.Close()
	out := map[rollupRowKey]archiveSums{}
	for rows.Next() {
		var k rollupRowKey
		var v archiveSums
		if err := rows.Scan(&k.bucket, &k.model, &k.user, &k.profile, &k.login, &k.team, &k.agent, &k.billing, &k.project,
			&v.cost, &v.input, &v.output, &v.cacheRead, &v.cacheCreate, &v.events); err != nil {
			t.Fatalf("scan rollups: %v", err)
		}
		k.bucket = k.bucket.UTC()
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read rollups: %v", err)
	}
	return out
}

func readArchiveSums(t *testing.T, s *PgStore) map[archiveKey]archiveSums {
	t.Helper()
	out := map[archiveKey]archiveSums{}
	for k, r := range readArchive(t, s) {
		out[k] = r.sums
	}
	return out
}

// coverageKey is a coverage_measured_minutes primary key.
type coverageKey struct {
	login  string
	minute time.Time
}

type coverageSums struct{ tokens, chartTokens, cost float64 }

// coverageOf is coverage_measured_minutes recounted from rows: the Anthropic rows
// with a login, summed by lowercased login and UTC minute.
func coverageOf(rows ...visibleRow) map[coverageKey]coverageSums {
	out := map[coverageKey]coverageSums{}
	for _, r := range rows {
		if r.billing != "anthropic" || r.login == "" {
			continue
		}
		k := coverageKey{strings.ToLower(r.login), r.ts.UTC().Truncate(time.Minute)}
		sum := out[k]
		sum.tokens += float64(r.in + r.out + r.cacheRead + r.cacheCreate)
		sum.chartTokens += float64(r.in + r.out)
		sum.cost += r.cost
		out[k] = sum
	}
	return out
}

func readCoverage(t *testing.T, s *PgStore) map[coverageKey]coverageSums {
	t.Helper()
	rows, err := s.pool.Query(context.Background(),
		`SELECT login_email, minute, tokens, chart_tokens, cost_usd FROM coverage_measured_minutes`)
	if err != nil {
		t.Fatalf("read coverage: %v", err)
	}
	defer rows.Close()
	out := map[coverageKey]coverageSums{}
	for rows.Next() {
		var k coverageKey
		var v coverageSums
		if err := rows.Scan(&k.login, &k.minute, &v.tokens, &v.chartTokens, &v.cost); err != nil {
			t.Fatalf("scan coverage: %v", err)
		}
		k.minute = k.minute.UTC()
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read coverage: %v", err)
	}
	return out
}

// sameSums compares two sums, the costs to float rounding: Go and Postgres add
// them in different orders.
func sameSums(a, b archiveSums) bool {
	c := a
	c.cost = b.cost
	return c == b && math.Abs(a.cost-b.cost) <= 1e-12
}

func sameCoverage(a, b coverageSums) bool {
	return a.tokens == b.tokens && a.chartTokens == b.chartTokens && math.Abs(a.cost-b.cost) <= 1e-12
}

// compareKeyed reports every key whose value differs between got and want, or
// that only one of them has.
func compareKeyed[K comparable, V any](t *testing.T, what string, got, want map[K]V, same func(a, b V) bool) {
	t.Helper()
	for k, w := range want {
		g, ok := got[k]
		switch {
		case !ok:
			t.Errorf("%s: %+v is missing, want %+v", what, k, w)
		case !same(g, w):
			t.Errorf("%s: %+v = %+v, want %+v", what, k, g, w)
		}
	}
	for k, g := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s: %+v = %+v is not expected", what, k, g)
		}
	}
}

// Class 다, OTEL fully expired (p1t-design §7, §8.1): the rollups hold hours OTEL
// measured, and retention has dropped that OTEL since. Hour by hour from rsBase:
// 0, a session of API requests only; 1, OTEL with a team and without one; 2, OTEL
// with no JSONL behind it; 3, measured and estimated together -- OTEL that
// remains, OTEL that expired, and a session that never had any, priced line by
// line as an older binary left it; 4, an account excluded when the rollups were
// built and let back in since; 5, OTEL received again after it expired, one of
// its two events. Step 1 archives the hours as they were. Once step 3 has priced
// the sessions left without OTEL by request, step 5 recounts every hour from
// visible_events: an expired hour becomes its requests' estimate, the hour with
// no JSONL leaves, the remaining and the re-received OTEL stay measured, and the
// lifted account's hour comes back as an estimate. With the marker write refused
// the rollups are left as they were.
func TestRequestSetMigration_Step5_RecountsHoursWhoseOTELExpired(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	at := func(hour int, d time.Duration) time.Duration { return time.Duration(hour)*time.Hour + d }
	line := func(tag, session, login string, when time.Duration, msgID string, in, out int) rsLine {
		return rsLine{tag: tag, session: session, uuid: tag, at: when, msgID: msgID, model: rpRated, login: login,
			project: "dx-project", in: ptrInt(in), out: ptrInt(out), cr: ptrInt(100)}
	}
	event := func(session, login, team string, when time.Duration, in, out int, cost float64) *OtelEvent {
		return &OtelEvent{Ts: rsBase.Add(when), EventName: "api_request", SessionID: session, UserID: "uid-rs",
			ProfileEmail: "rs@example.com", LoginEmail: login, UserTeam: team, Model: rpRated, CostUSD: &cost,
			InputTokens: ptrInt(in), OutputTokens: ptrInt(out), CacheReadTokens: ptrInt(100), Agent: "claude",
			BillingProvider: "anthropic"}
	}
	f := insertRequestSet(t, s,
		line("api-1", "dx-api", "api@example.com", at(0, 5*time.Minute), "msg_api", 10, 1),
		line("api-2", "dx-api", "api@example.com", at(0, 5*time.Minute+time.Second), "msg_api", 10, 300),
		line("api-3", "dx-api", "api@example.com", at(0, 20*time.Minute), "msg_api2", 30, 40),
		line("team-1", "dx-team", "team@example.com", at(1, 10*time.Minute), "msg_team", 20, 2),
		line("team-2", "dx-team", "team@example.com", at(1, 10*time.Minute+time.Second), "msg_team", 20, 200),
		line("kept-1", "dx-kept", "kept@example.com", at(3, 5*time.Minute), "msg_kept", 40, 4),
		line("kept-2", "dx-kept", "kept@example.com", at(3, 5*time.Minute+time.Second), "msg_kept", 40, 400),
		line("gone-1", "dx-gone", "gone@example.com", at(3, 10*time.Minute), "msg_gone", 50, 5),
		line("gone-2", "dx-gone", "gone@example.com", at(3, 10*time.Minute+time.Second), "msg_gone", 50, 500),
		line("off-1", "dx-offline", "off@example.com", at(3, 20*time.Minute), "msg_off", 60, 6),
		line("off-2", "dx-offline", "off@example.com", at(3, 20*time.Minute+time.Second), "msg_off", 60, 60),
		line("off-3", "dx-offline", "off@example.com", at(3, 20*time.Minute+2*time.Second), "msg_off", 60, 600),
		line("lift-1", "dx-lifted", "lifted@example.com", at(4, 5*time.Minute), "msg_lift", 70, 7),
		line("lift-2", "dx-lifted", "lifted@example.com", at(4, 5*time.Minute+time.Second), "msg_lift", 70, 700),
		line("re-1", "dx-reseen", "reseen@example.com", at(5, 5*time.Minute), "msg_re", 80, 8),
		line("re-2", "dx-reseen", "reseen@example.com", at(5, 5*time.Minute+time.Second), "msg_re", 80, 800),
	)
	rpRate(t, s)
	projects := map[string]string{}
	for _, l := range f.lines {
		projects[l.session] = l.project
	}
	kept := []*OtelEvent{
		event("dx-kept", "kept@example.com", "", at(3, 5*time.Minute), 1400, 440, 0.5),
		// A tool event of the same session: no model, tokens or cost, one more event.
		{Ts: rsBase.Add(at(3, 6*time.Minute)), EventName: "tool_result", SessionID: "dx-kept", UserID: "uid-rs",
			ProfileEmail: "rs@example.com", LoginEmail: "kept@example.com", ToolName: "Bash", Agent: "claude",
			BillingProvider: "anthropic"},
	}
	reseen := event("dx-reseen", "reseen@example.com", "", at(5, 5*time.Minute), 900, 90, 0.5)
	expiring := []*OtelEvent{
		event("dx-api", "api@example.com", "", at(0, 5*time.Minute), 1000, 350, 0.25),
		event("dx-api", "api@example.com", "", at(0, 20*time.Minute), 1030, 45, 0.125),
		event("dx-team", "team@example.com", "team-a", at(1, 10*time.Minute), 1020, 210, 0.75),
		event("dx-team", "team@example.com", "", at(1, 40*time.Minute), 30, 3, 0.0625),
		event("dx-nojsonl", "nojsonl@example.com", "", at(2, 15*time.Minute), 700, 70, 1),
		event("dx-gone", "gone@example.com", "", at(3, 10*time.Minute), 1050, 550, 2),
		event("dx-lifted", "lifted@example.com", "", at(4, 5*time.Minute), 1070, 770, 4),
		reseen,
		event("dx-reseen", "reseen@example.com", "", at(5, 30*time.Minute), 200, 20, 0.25),
	}
	if err := s.InsertEvents(ctx, append(slices.Clone(kept), expiring...)); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	lineRule := f.writeLineRuleRows(t, s, "dx-offline")
	if _, err := s.ExcludeAccount(ctx, "lifted@example.com", "test", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if err := s.BackfillUsageHourlyRollups(ctx); err != nil {
		t.Fatalf("BackfillUsageHourlyRollups: %v", err)
	}
	var visibleThen []*OtelEvent
	for _, e := range append(slices.Clone(kept), expiring...) {
		if e.LoginEmail != "lifted@example.com" {
			visibleThen = append(visibleThen, e)
		}
	}
	before := rollupsOf(projects, append(eventRows(visibleThen...), lineRule...)...)
	compareKeyed(t, "the rollups as built", readRollups(t, s), before, sameSums)

	// Retention drops the OTEL of every session but dx-kept, the account is let
	// back in, and one of dx-reseen's events arrives again.
	if _, err := s.pool.Exec(ctx, `DELETE FROM otel_events WHERE session_id <> 'dx-kept'`); err != nil {
		t.Fatalf("expire OTEL: %v", err)
	}
	if err := s.RemoveExcludedAccount(ctx, "lifted@example.com"); err != nil {
		t.Fatalf("RemoveExcludedAccount: %v", err)
	}
	if err := s.InsertEvents(ctx, []*OtelEvent{reseen}); err != nil {
		t.Fatalf("receive dx-reseen's event again: %v", err)
	}
	offline := func(l rsLine) bool { return l.session != "dx-kept" && l.session != "dx-reseen" }
	after := rollupsOf(projects, append(eventRows(append(slices.Clone(kept), reseen)...), f.imputedRows(offline)...)...)
	hours := func(rollups map[rollupRowKey]archiveSums) []int {
		var out []int
		for k := range rollups {
			if h := int(k.bucket.Sub(rsBase) / time.Hour); !slices.Contains(out, h) {
				out = append(out, h)
			}
		}
		slices.Sort(out)
		return out
	}
	if b, a := hours(before), hours(after); !slices.Equal(b, []int{0, 1, 2, 3, 5}) || !slices.Equal(a, []int{0, 1, 3, 4, 5}) {
		t.Fatalf("fixture: hours %v as built and %v recounted, want hour 2 only before and hour 4 only after", b, a)
	}

	steps := s.RequestSetMigrationSteps()
	for i, step := range steps[:4] {
		if _, err := step.Run(ctx); err != nil {
			t.Fatalf("step %d: %v", i+1, err)
		}
	}
	compareKeyed(t, "the archive", readArchiveSums(t, s), archiveOf(before), sameSums)
	compareKeyed(t, "the rollups after steps 1 to 4", readRollups(t, s), before, sameSums)

	step := steps[4]
	if step.Marker != usageHourlyRollupsRecount {
		t.Fatalf("step 5 is %s, want %s", step.Marker, usageHourlyRollupsRecount)
	}
	allowMarker := refuseMarkerWrite(t, s, usageHourlyRollupsRecount)
	if progressed, err := step.Run(ctx); err == nil || progressed {
		t.Fatalf("with the marker refused: progressed %v, err %v; want an error and no progress", progressed, err)
	}
	compareKeyed(t, "the rollups after the failed step 5", readRollups(t, s), before, sameSums)
	if backfillMarkedNow(t, s, usageHourlyRollupsRecount) {
		t.Fatal("the failed step left its marker")
	}

	allowMarker()
	if progressed, err := step.Run(ctx); err != nil || !progressed {
		t.Fatalf("step 5: progressed %v, err %v; want progress", progressed, err)
	}
	compareKeyed(t, "the rollups after step 5", readRollups(t, s), after, sameSums)
	compareKeyed(t, "the archive after step 5", readArchiveSums(t, s), archiveOf(before), sameSums)
	// List = trend (p1t-design §7, class 다): an expired session lists what the
	// rollups now hold under its login, its requests' estimate.
	overviews := listOverviews(t, s, SessionOverviewFilter{})
	summaries, err := s.ListSessionSummaries(ctx, "", "", 50)
	if err != nil {
		t.Fatalf("ListSessionSummaries: %v", err)
	}
	for session, login := range map[string]string{
		"dx-api": "api@example.com", "dx-gone": "gone@example.com", "dx-lifted": "lifted@example.com",
	} {
		var trend rpTotals
		for k, v := range readRollups(t, s) {
			if k.login == login {
				trend.in, trend.out, trend.cost = trend.in+v.input, trend.out+v.output, trend.cost+v.cost
			}
		}
		o, sum := overviewOf(t, overviews, session), summaryOf(t, summaries, session)
		rpCheck(t, "the list of "+session, o.InputTokens, o.OutputTokens, o.CostUSD, trend)
		rpCheck(t, "the summary of "+session, sum.InputTokens, sum.OutputTokens, 0, rpTotals{in: trend.in, out: trend.out})
	}
	if !backfillMarkedNow(t, s, usageHourlyRollupsRecount) {
		t.Error("step 5 left no marker")
	}
	if progressed, err := step.Run(ctx); err != nil || progressed {
		t.Errorf("step 5 again: progressed %v, err %v; want done, no progress", progressed, err)
	}
}

// Step 6 recounts coverage_measured_minutes from visible_events and commits its
// marker with it (p1t-design §6.5). The table was built while an offline
// session's claude_imputed_cost was a row per line and before an account was
// excluded; step 3 has since priced the session by request. With the marker write
// refused the table is left as built. The next call holds the session's requests
// -- a response written as three lines counts once -- the measured account's OTEL
// under its lowercased login, and nothing of the account excluded now, whose
// minutes were stored before its exclusion.
func TestRequestSetMigration_Step6_RecountsCoverageWithItsMarker(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	line := func(tag string, at time.Duration, msgID string, in, out int) rsLine {
		return rsLine{tag: tag, session: "cv-offline", uuid: tag, at: at, msgID: msgID, model: rpRated,
			login: "off@example.com", in: ptrInt(in), out: ptrInt(out), cr: ptrInt(1000)}
	}
	f := insertRequestSet(t, s,
		line("cv-1", time.Minute, "msg_cv", 10, 1),
		line("cv-2", time.Minute+time.Second, "msg_cv", 10, 300),
		line("cv-3", time.Minute+2*time.Second, "msg_cv", 10, 523),
		line("cv-4", 3*time.Minute, "msg_cv2", 7, 70),
	)
	rpRate(t, s)
	event := func(session, login string, in, out int, cost float64) *OtelEvent {
		return &OtelEvent{Ts: rsBase.Add(2 * time.Minute), EventName: "api_request", SessionID: session,
			UserID: "uid-rs", LoginEmail: login, Model: "rp-otel-model", CostUSD: &cost, InputTokens: ptrInt(in),
			OutputTokens: ptrInt(out), Agent: "claude", BillingProvider: "anthropic"}
	}
	measured := event("cv-measured", "Measured@Example.com", 100, 10, 0.5)
	excluded := event("cv-excluded", "excluded@example.com", 200, 20, 0.25)
	if err := s.InsertEvents(ctx, []*OtelEvent{measured, excluded}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	lineRule := f.writeLineRuleRows(t, s, "cv-offline")
	if err := s.BackfillCoverageMeasuredMinutes(ctx); err != nil {
		t.Fatalf("BackfillCoverageMeasuredMinutes: %v", err)
	}
	before := coverageOf(append(eventRows(measured, excluded), lineRule...)...)
	compareKeyed(t, "coverage as built", readCoverage(t, s), before, sameCoverage)

	if _, err := s.ExcludeAccount(ctx, "excluded@example.com", "test", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatalf("RefreshClaudeImputedCost: %v", err)
	}
	after := coverageOf(append(eventRows(measured), f.imputedRows(rsAll)...)...)
	offMinute := coverageKey{"off@example.com", rsBase.Add(time.Minute)}
	excludedMinute := coverageKey{"excluded@example.com", rsBase.Add(2 * time.Minute)}
	if _, ok := after[excludedMinute]; ok || before[offMinute].tokens == after[offMinute].tokens {
		t.Fatalf("fixture: the excluded minute recounted %v, the offline minute %v as built and %v recounted",
			ok, before[offMinute], after[offMinute])
	}
	compareKeyed(t, "coverage before step 6", readCoverage(t, s), before, sameCoverage)

	step := s.RequestSetMigrationSteps()[5]
	if step.Marker != coverageMinutesRecount {
		t.Fatalf("step 6 is %s, want %s", step.Marker, coverageMinutesRecount)
	}
	allowMarker := refuseMarkerWrite(t, s, coverageMinutesRecount)
	if progressed, err := step.Run(ctx); err == nil || progressed {
		t.Fatalf("with the marker refused: progressed %v, err %v; want an error and no progress", progressed, err)
	}
	compareKeyed(t, "coverage after the failed step 6", readCoverage(t, s), before, sameCoverage)
	if backfillMarkedNow(t, s, coverageMinutesRecount) {
		t.Fatal("the failed step left its marker")
	}

	allowMarker()
	if progressed, err := step.Run(ctx); err != nil || !progressed {
		t.Fatalf("step 6: progressed %v, err %v; want progress", progressed, err)
	}
	compareKeyed(t, "coverage after step 6", readCoverage(t, s), after, sameCoverage)
	if !backfillMarkedNow(t, s, coverageMinutesRecount) {
		t.Error("step 6 left no marker")
	}
	if progressed, err := step.Run(ctx); err != nil || progressed {
		t.Errorf("step 6 again: progressed %v, err %v; want done, no progress", progressed, err)
	}
}

// p1t-design §8.2: step 6 and the 60-second tail refresh of coverage at once,
// and step 5 and the rollups' tail refresh likewise. A transaction holds the
// last row of the table, as a writer would, so whichever of the two reaches it
// first waits there with the table half rewritten; the other waits on the
// table's writers' lock rather than delete the rows it can see and insert the
// keys the first one inserts too, which collides on the primary key or deadlocks
// with it. The tail refresh starts at that last hour or minute, so every earlier
// row, made stale here, comes right only from the step's recount. Both finish,
// and the table holds what running them one after the other leaves: a recount
// from visible_events.
func TestRequestSetMigrationRecountsAndTailRefreshesAtOnceLeaveNoCollision(t *testing.T) {
	var events []*OtelEvent
	for i := range 3 {
		events = append(events, &OtelEvent{Ts: rsBase.Add(time.Duration(i)*time.Hour + time.Minute),
			EventName: "api_request", SessionID: "tail", UserID: "uid-rs", LoginEmail: "tail@example.com",
			Model: "rp-otel-model", CostUSD: ptrFloat(0.5), InputTokens: ptrInt(100 * (i + 1)), OutputTokens: ptrInt(10),
			Agent: "claude", BillingProvider: "anthropic"})
	}
	last := events[len(events)-1].Ts
	for _, c := range []struct {
		step        int
		marker      string
		build       func(*PgStore, context.Context) error
		stale, hold string
		held        time.Time
		tail        func(*PgStore, context.Context, *time.Time) error
		check       func(t *testing.T, s *PgStore)
	}{
		{4, usageHourlyRollupsRecount, (*PgStore).BackfillUsageHourlyRollups,
			`UPDATE usage_hourly_rollups SET cost_usd = -1, input_tokens = -1, event_count = -1`,
			`SELECT 1 FROM usage_hourly_rollups WHERE bucket = $1 FOR UPDATE`, last.Truncate(time.Hour),
			(*PgStore).RefreshUsageHourlyRollups,
			func(t *testing.T, s *PgStore) {
				compareKeyed(t, "the rollups", readRollups(t, s), rollupsOf(nil, eventRows(events...)...), sameSums)
			}},
		{5, coverageMinutesRecount, (*PgStore).BackfillCoverageMeasuredMinutes,
			`UPDATE coverage_measured_minutes SET tokens = -1, chart_tokens = -1, cost_usd = -1`,
			`SELECT 1 FROM coverage_measured_minutes WHERE minute = $1 FOR UPDATE`, last,
			(*PgStore).RefreshCoverageMeasuredMinutes,
			func(t *testing.T, s *PgStore) {
				compareKeyed(t, "coverage", readCoverage(t, s), coverageOf(eventRows(events...)...), sameCoverage)
			}},
	} {
		t.Run(c.marker, func(t *testing.T) {
			s := acquireTestStore(t)
			truncateTables(t, s)
			resetRequestSetMigration(t, s)
			ctx := context.Background()
			if err := s.InsertEvents(ctx, events); err != nil {
				t.Fatalf("InsertEvents: %v", err)
			}
			if err := c.build(s, ctx); err != nil {
				t.Fatalf("build the table: %v", err)
			}
			if _, err := s.pool.Exec(ctx, c.stale); err != nil {
				t.Fatalf("stale the table: %v", err)
			}
			holder, err := s.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin the holder: %v", err)
			}
			t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
			if tag, err := holder.Exec(ctx, c.hold, c.held); err != nil || tag.RowsAffected() != 1 {
				t.Fatalf("hold a row: %d held, %v", tag.RowsAffected(), err)
			}

			step := s.RequestSetMigrationSteps()[c.step]
			if step.Marker != c.marker {
				t.Fatalf("step %d is %s, want %s", c.step+1, step.Marker, c.marker)
			}
			type outcome struct {
				who        string
				progressed bool
				err        error
			}
			done := make(chan outcome, 2)
			go func() {
				progressed, err := step.Run(ctx)
				done <- outcome{"the step", progressed, err}
			}()
			go func() {
				from := c.held
				done <- outcome{"the tail refresh", false, c.tail(s, ctx, &from)}
			}()
			awaitLockWaiters(t, s, 2)
			if err := holder.Rollback(ctx); err != nil {
				t.Fatalf("release the row: %v", err)
			}
			for range 2 {
				o := <-done
				if o.err != nil {
					t.Errorf("%s: %v", o.who, o.err)
				}
				if o.who == "the step" && !o.progressed && o.err == nil {
					t.Error("the step reported no progress")
				}
			}
			c.check(t, s)
			if !backfillMarkedNow(t, s, c.marker) {
				t.Error("the step left no marker")
			}
		})
	}
}

// The rollups' writers and coverage's take turns on locks of their own
// (p1t-design §6.5): a refresh of either table goes ahead while the other's
// writers' lock is held -- by step 5 or 6 through its recount, say.
func TestRollupAndCoverageWritersDoNotShareALock(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	for _, c := range []struct {
		held    string
		lock    func(context.Context, pgx.Tx) error
		refresh func(context.Context, *time.Time) error
	}{
		{"the rollups' writers' lock", lockUsageHourlyRollupWriters, s.RefreshCoverageMeasuredMinutes},
		{"the coverage writers' lock", lockCoverageMeasuredMinutesWriters, s.RefreshUsageHourlyRollups},
	} {
		holder, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin the holder: %v", err)
		}
		if err := c.lock(ctx, holder); err != nil {
			t.Fatal(err)
		}
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		from := time.Now().Add(-time.Hour)
		err = c.refresh(bounded, &from)
		cancel()
		if rerr := holder.Rollback(ctx); rerr != nil {
			t.Fatalf("release %s: %v", c.held, rerr)
		}
		if err != nil {
			t.Errorf("a refresh with %s held: %v; want it done without waiting", c.held, err)
		}
	}
}

// Two first calls of step 5, or of step 6, at once recount once. A writer of the
// table holds its writers' lock, as a tail refresh under way does, and so holds
// both calls; the second reads the marker only once it has the lock, so it finds
// the first one's, does nothing and reports no progress.
func TestRequestSetMigration_Steps5And6_CalledTwiceAtOnceRecountOnce(t *testing.T) {
	for _, c := range []struct {
		step   int
		marker string
		build  func(*PgStore, context.Context) error
		lock   func(context.Context, pgx.Tx) error
	}{
		{4, usageHourlyRollupsRecount, (*PgStore).BackfillUsageHourlyRollups, lockUsageHourlyRollupWriters},
		{5, coverageMinutesRecount, (*PgStore).BackfillCoverageMeasuredMinutes, lockCoverageMeasuredMinutesWriters},
	} {
		t.Run(c.marker, func(t *testing.T) {
			s := acquireTestStore(t)
			truncateTables(t, s)
			resetRequestSetMigration(t, s)
			ctx := context.Background()
			if err := c.build(s, ctx); err != nil {
				t.Fatalf("build the table: %v", err)
			}
			holder, err := s.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin the holder: %v", err)
			}
			t.Cleanup(func() { _ = holder.Rollback(context.Background()) })
			if err := c.lock(ctx, holder); err != nil {
				t.Fatal(err)
			}
			step := s.RequestSetMigrationSteps()[c.step]
			if step.Marker != c.marker {
				t.Fatalf("step %d is %s, want %s", c.step+1, step.Marker, c.marker)
			}
			type outcome struct {
				progressed bool
				err        error
			}
			done := make(chan outcome, 2)
			for range 2 {
				go func() {
					progressed, err := step.Run(ctx)
					done <- outcome{progressed, err}
				}()
			}
			awaitLockWaiters(t, s, 2)
			if err := holder.Rollback(ctx); err != nil {
				t.Fatalf("release the lock: %v", err)
			}
			var progressed int
			for range 2 {
				o := <-done
				if o.err != nil {
					t.Errorf("step %d: %v", c.step+1, o.err)
				}
				if o.progressed {
					progressed++
				}
			}
			if progressed != 1 {
				t.Errorf("%d of the two calls reported progress, want 1: the second recounted again", progressed)
			}
			if !backfillMarkedNow(t, s, c.marker) {
				t.Error("no marker")
			}
		})
	}
}

// Without usage_hourly_rollups_v2, or coverage_measured_minutes_v1, the table was
// never built, and the boot repair builds it with this binary's rules: steps 5
// and 6 write their markers and recount nothing -- the rows put in by hand stay,
// though no event is behind them -- and build nothing themselves.
func TestRequestSetMigration_Steps5And6_OnlyMarkTablesNeverBuilt(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	insertRollupRows(t, s, archiveFixture())
	rollups := readRollups(t, s)
	minute := rsBase.Add(time.Minute)
	if _, err := s.pool.Exec(ctx, `INSERT INTO coverage_measured_minutes (login_email, minute, tokens, chart_tokens, cost_usd)
		VALUES ('a@example.com', $1, 100, 10, 0.5), ('b@example.com', $1, 200, 20, 0.25)`, minute); err != nil {
		t.Fatalf("insert coverage rows: %v", err)
	}
	coverage := readCoverage(t, s)
	if len(rollups) == 0 || len(coverage) == 0 {
		t.Fatalf("fixture: %d rollup row(s), %d coverage row(s)", len(rollups), len(coverage))
	}
	for _, c := range []struct {
		step          int
		marker, built string
	}{{4, usageHourlyRollupsRecount, usageHourlyRollupBackfill}, {5, coverageMinutesRecount, coverageMinutesBackfill}} {
		if backfillMarkedNow(t, s, c.built) {
			t.Fatalf("%s is there before step %d", c.built, c.step+1)
		}
		step := s.RequestSetMigrationSteps()[c.step]
		if step.Marker != c.marker {
			t.Fatalf("step %d is %s, want %s", c.step+1, step.Marker, c.marker)
		}
		if progressed, err := step.Run(ctx); err != nil || !progressed {
			t.Fatalf("step %d: progressed %v, err %v; want its marker committed", c.step+1, progressed, err)
		}
		if !backfillMarkedNow(t, s, c.marker) || backfillMarkedNow(t, s, c.built) {
			t.Errorf("step %d left its marker %v and the build marker %v; want only its own",
				c.step+1, backfillMarkedNow(t, s, c.marker), backfillMarkedNow(t, s, c.built))
		}
	}
	compareKeyed(t, "the rollups", readRollups(t, s), rollups, sameSums)
	compareKeyed(t, "coverage", readCoverage(t, s), coverage, sameCoverage)
}

// Step 9 writes the completion marker and reports that it did; a call that finds
// it there reports no progress, so a boot after the transition says nothing of it.
func TestRequestSetMigration_Step9_WritesTheCompletionMarker(t *testing.T) {
	s := acquireTestStore(t)
	resetRequestSetMigration(t, s)
	ctx := context.Background()
	step := s.RequestSetMigrationSteps()[8]
	if step.Marker != requestSetMigrationDone {
		t.Fatalf("step 9 is %s, want %s", step.Marker, requestSetMigrationDone)
	}
	if progressed, err := step.Run(ctx); err != nil || !progressed || !backfillMarkedNow(t, s, requestSetMigrationDone) {
		t.Fatalf("step 9: progressed %v, err %v, marker %v; want the marker written",
			progressed, err, backfillMarkedNow(t, s, requestSetMigrationDone))
	}
	if progressed, err := step.Run(ctx); err != nil || progressed {
		t.Errorf("step 9 again: progressed %v, err %v; want done, no progress", progressed, err)
	}
}
