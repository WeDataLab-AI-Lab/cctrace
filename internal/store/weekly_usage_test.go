package store

import (
	"context"
	"math"
	"testing"
	"time"
)

// seedWeeklyRuns writes AI report runs straight into ai_report_runs so each test
// controls status, token reporting and the two timestamps exactly.
func seedWeeklyRuns(t *testing.T, s *PgStore, now time.Time) {
	t.Helper()
	ctx := context.Background()
	u, err := s.CreateDashboardUser(ctx, &DashboardUser{Email: t.Name() + "@example.com", PasswordHash: "test", Role: "user"})
	if err != nil {
		t.Fatalf("CreateDashboardUser: %v", err)
	}
	insert := `INSERT INTO ai_report_runs
		(dashboard_user_id, week, tz, since, until, status, runtime, model, auth_mode,
		 tokens_reported, input_tokens, cached_input_tokens, output_tokens, started_at, finished_at)
		VALUES ($1, '2026-W37', 'UTC', $2, $2, $3, 'codex-app-server', $4, 'chatgpt', $5, $6, $7, $8, $9, $10)`
	type run struct {
		status              string
		model               string
		reported            bool
		in, cached, out     any
		startedAt, finished any
	}
	runs := []run{
		// Completed: ts is started_at, the one timestamp a run never rewrites.
		{"completed", "gpt-5.6-terra", true, 22164, 12800, 507, now.Add(-2 * time.Minute), now.Add(-time.Minute)},
		// Failed after spending tokens: counted, still at started_at.
		{"failed", "gpt-5.6-terra", true, 1000, 0, 100, now.Add(-3 * time.Minute), now.Add(-2 * time.Minute)},
		// No finished_at: same ts.
		{"canceled", "gpt-5.6-terra", true, 500, 200, 50, now.Add(-4 * time.Minute), nil},
		// Tokens never reported: not usage, must not appear.
		{"running", "gpt-5.6-terra", false, nil, nil, nil, now, nil},
	}
	for _, r := range runs {
		if _, err := s.pool.Exec(ctx, insert, u.ID, now, r.status, r.model, r.reported, r.in, r.cached, r.out, r.startedAt, r.finished); err != nil {
			t.Fatalf("insert ai_report_runs(%s): %v", r.status, err)
		}
	}
}

func TestPgStore_WeeklyUsage_UnifiedEventsArm(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	seedWeeklyRuns(t, s, now)

	rows, err := s.pool.Query(ctx, `SELECT ts, event_name, session_id, prompt_id, user_id, profile_email,
		login_email, user_name, agent, billing_provider, model, cost_usd,
		input_tokens, output_tokens, cache_read_tokens, cache_create_tokens, account_id
		FROM unified_events WHERE agent = 'weekly' ORDER BY ts DESC`)
	if err != nil {
		t.Fatalf("query unified_events: %v", err)
	}
	defer rows.Close()
	type got struct {
		ts                                                   time.Time
		event, session, prompt, userID, profile, login, name string
		agent, billing, model, account                       string
		cost                                                 *float64
		in, out, cacheRead, cacheCreate                      int
	}
	var all []got
	for rows.Next() {
		var g got
		if err := rows.Scan(&g.ts, &g.event, &g.session, &g.prompt, &g.userID, &g.profile, &g.login, &g.name,
			&g.agent, &g.billing, &g.model, &g.cost, &g.in, &g.out, &g.cacheRead, &g.cacheCreate, &g.account); err != nil {
			t.Fatalf("scan: %v", err)
		}
		all = append(all, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("weekly rows = %d, want 3 (reported runs only)", len(all))
	}

	wantTs := []time.Time{now.Add(-2 * time.Minute), now.Add(-3 * time.Minute), now.Add(-4 * time.Minute)}
	for i, g := range all {
		if !g.ts.Equal(wantTs[i]) {
			t.Errorf("row %d ts = %v, want %v", i, g.ts, wantTs[i])
		}
		if g.event != "weekly_usage" || g.agent != "weekly" || g.billing != "openai" || g.model != "gpt-5.6-terra" {
			t.Errorf("row %d identity = %q/%q/%q/%q", i, g.event, g.agent, g.billing, g.model)
		}
		if g.session != "" || g.prompt != "" || g.userID != "" || g.profile != "" || g.login != "" || g.account != "" {
			t.Errorf("row %d carries a person or session: %+v", i, g)
		}
		if g.name != "weekly" {
			t.Errorf("row %d user_name = %q, want weekly", i, g.name)
		}
	}
	// Codex semantics: cached input is a subset of input, so input is stored net of it.
	first := all[0]
	if first.in != 22164-12800 || first.cacheRead != 12800 || first.out != 507 || first.cacheCreate != 0 {
		t.Errorf("token mapping = in %d cache_read %d out %d cache_create %d", first.in, first.cacheRead, first.out, first.cacheCreate)
	}

	// Cost is the codex imputation: the same tokens through the codex arm price the same.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: now.Add(-2 * time.Minute), SessionID: "weekly-parity-codex", UUID: "weekly-parity-codex-1", RecordType: "usage",
		ProfileEmail: "codex@ex.com", UserID: "u-codex", Model: "gpt-5.6-terra",
		InputTokens: ptrInt(22164), CacheReadTokens: ptrInt(12800), OutputTokens: ptrInt(507),
		Agent: "codex", BillingProvider: "openai",
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)
	var codexCost float64
	if err := s.pool.QueryRow(ctx, `SELECT cost_usd FROM unified_events WHERE session_id = 'weekly-parity-codex'`).Scan(&codexCost); err != nil {
		t.Fatalf("codex cost: %v", err)
	}
	if codexCost <= 0 {
		t.Fatalf("codex cost = %v, want a priced row (seeded gpt-5.6-terra rate)", codexCost)
	}
	if first.cost == nil || math.Abs(*first.cost-codexCost) > 1e-9 {
		t.Errorf("weekly cost = %v, want codex imputation %v", first.cost, codexCost)
	}
}

func TestPgStore_WeeklyUsage_AgentScope(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	seedWeeklyRuns(t, s, now)
	usageRaw := []byte(`{"message":{"usage":{"cost":{"total":2}}}}`)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{
		Ts: now, SessionID: "weekly-scope-omo", UUID: "weekly-scope-omo-1", RecordType: "assistant",
		ProfileEmail: "omo@ex.com", UserID: "u-omo", Model: "gpt-5.5",
		InputTokens: ptrInt(20), OutputTokens: ptrInt(6),
		Agent: "omo", BillingProvider: "openai", Raw: usageRaw,
	}, {
		// Weekly runs on codex, but the codex scope is people's Codex CLI usage.
		Ts: now, SessionID: "weekly-scope-codex", UUID: "weekly-scope-codex-1", RecordType: "usage",
		ProfileEmail: "codex@ex.com", UserID: "u-codex", Model: "gpt-5.6-terra",
		InputTokens: ptrInt(30), OutputTokens: ptrInt(4),
		Agent: "codex", BillingProvider: "openai",
	}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	refreshCodexImputed(t, s)

	since := now.Add(-time.Hour)
	until := now.Add(time.Minute)
	cases := []struct {
		agent string
		want  int64
	}{{"weekly", 3}, {"other", 1}, {"codex", 1}, {"", 5}}
	for _, c := range cases {
		f := EventFilter{Since: &since, Until: &until, Agent: c.agent}
		count, err := s.CountEvents(ctx, f)
		if err != nil {
			t.Fatalf("CountEvents(%q): %v", c.agent, err)
		}
		if count != c.want {
			t.Errorf("CountEvents(%q) = %d, want %d", c.agent, count, c.want)
		}
	}
	// truncateTables clears the rollup marker, so the first pass reads raw events;
	// the second builds and marks the rollup so the same scopes are read from it.
	for _, source := range []string{"raw", "rollup"} {
		if source == "rollup" {
			markUsageRollupsBuilt(t, s)
		}
		for _, c := range cases {
			f := EventFilter{Since: &since, Until: &until, Agent: c.agent}
			series, err := s.TimeSeriesStats(ctx, f, "day")
			if err != nil {
				t.Fatalf("%s TimeSeriesStats(%q): %v", source, c.agent, err)
			}
			var events int64
			for _, d := range series {
				events += d.EventCount
			}
			if events != c.want {
				t.Errorf("%s TimeSeriesStats(%q) event count = %d, want %d", source, c.agent, events, c.want)
			}
		}
	}

	if _, ok, err := s.LatestActivityTs(ctx, EventFilter{Agent: "weekly"}); err != nil {
		t.Fatalf("LatestActivityTs(weekly): %v", err)
	} else if !ok {
		t.Error("LatestActivityTs(weekly) found no activity, want the report runs")
	}
}

// Weekly usage belongs to no person, so per-user groupings show it as one line of
// its own instead of folding it into the blank-user bucket.
func TestPgStore_WeeklyUsage_OneUserLine(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	// The rollup path filters whole hour buckets by their start (bucket >= since),
	// so a run seeded minutes before now falls out of range whenever now is in the
	// first few minutes of an hour: its bucket starts before now-1h. Pin every row to
	// the middle of the previous hour so the result does not depend on the wall clock.
	now := time.Now().UTC().Truncate(time.Hour).Add(-30 * time.Minute)
	seedWeeklyRuns(t, s, now)
	if err := s.InsertEvents(ctx, []*OtelEvent{{
		Ts: now, EventName: "api_request", SessionID: "weekly-user-claude",
		ProfileEmail: "", UserID: "", Model: "claude-sonnet-5",
		CostUSD: ptrFloat(1), InputTokens: ptrInt(10), OutputTokens: ptrInt(5),
		Agent: "claude", BillingProvider: "anthropic",
	}}); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	since := now.Add(-time.Hour)
	until := now.Add(time.Minute)

	for _, source := range []string{"raw", "rollup"} {
		if source == "rollup" {
			markUsageRollupsBuilt(t, s)
		}
		byUser, err := s.TimeSeriesStatsByUser(ctx, EventFilter{Since: &since, Until: &until}, "day")
		if err != nil {
			t.Fatalf("%s TimeSeriesStatsByUser: %v", source, err)
		}
		events := map[string]int64{}
		for _, u := range byUser {
			events[u.UserID] += u.EventCount
		}
		if events["weekly"] != 3 || events[""] != 1 {
			t.Errorf("%s TimeSeriesStatsByUser events by user = %v, want weekly:3 and blank:1", source, events)
		}
	}

	costRows, err := s.CostByUser(ctx, since, until, "", "", "")
	if err != nil {
		t.Fatalf("CostByUser: %v", err)
	}
	var weeklyRequests int64
	for _, c := range costRows {
		if c.UserID == "weekly" {
			weeklyRequests += c.RequestCount
			if c.Agent != "weekly" {
				t.Errorf("CostByUser weekly row agent = %q", c.Agent)
			}
		}
	}
	if weeklyRequests != 3 {
		t.Errorf("CostByUser weekly requests = %d, want 3", weeklyRequests)
	}

	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, u := range users {
		if u.UserID == "weekly" {
			t.Error("ListUsers lists weekly as a person")
		}
	}
}

// markUsageRollupsBuilt builds the rollup and sets the marker trend reads gate on,
// which truncateTables clears.
func markUsageRollupsBuilt(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("RefreshUsageHourlyRollups: %v", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO schema_backfills (name) VALUES ($1) ON CONFLICT DO NOTHING`,
		usageHourlyRollupBackfill); err != nil {
		t.Fatalf("mark rollup ready: %v", err)
	}
	if !s.usageRollupsReady(ctx) {
		t.Fatal("rollup marker set but trend reads would not use it")
	}
}

func TestPgStore_WeeklyUsage_NotASession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	seedWeeklyRuns(t, s, time.Now().UTC())

	ids, err := s.SessionList(ctx, "", 50, 0)
	if err != nil {
		t.Fatalf("SessionList: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("SessionList = %v, want none", ids)
	}
	for _, agent := range []string{"", "weekly"} {
		n, err := s.CountSessionOverviews(ctx, SessionOverviewFilter{Agent: agent})
		if err != nil {
			t.Fatalf("CountSessionOverviews(%q): %v", agent, err)
		}
		if n != 0 {
			t.Errorf("CountSessionOverviews(%q) = %d, want 0", agent, n)
		}
		list, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{Agent: agent, Limit: 50})
		if err != nil {
			t.Fatalf("ListSessionOverviews(%q): %v", agent, err)
		}
		if len(list) != 0 {
			t.Errorf("ListSessionOverviews(%q) = %d rows, want 0", agent, len(list))
		}
	}
}

func TestPgStore_WeeklyUsage_HourlyRollups(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	seedWeeklyRuns(t, s, now)

	weeklyRollupEvents := func() int64 {
		t.Helper()
		var n int64
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(event_count),0) FROM usage_hourly_rollups WHERE agent = 'weekly'`).Scan(&n); err != nil {
			t.Fatalf("read rollups: %v", err)
		}
		return n
	}

	from := now.Add(-UsageRollupWindow)
	if err := s.RefreshUsageHourlyRollups(ctx, &from); err != nil {
		t.Fatalf("incremental refresh: %v", err)
	}
	if got := weeklyRollupEvents(); got != 3 {
		t.Errorf("incremental rollup weekly events = %d, want 3", got)
	}

	if err := s.BackfillUsageHourlyRollups(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := weeklyRollupEvents(); got != 3 {
		t.Errorf("backfill rollup weekly events = %d, want 3", got)
	}
	// A fresh build already holds every weekly row, so it settles the weekly marker too.
	if !markerSet(t, s, usageHourlyRollupWeeklyBackfill) {
		t.Error("fresh backfill left the weekly marker unset")
	}
}

func markerSet(t *testing.T, s *PgStore, name string) bool {
	t.Helper()
	var done bool
	if err := s.pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, name).Scan(&done); err != nil {
		t.Fatalf("read marker %s: %v", name, err)
	}
	return done
}

// An installation that built its rollups before the weekly arm existed has no
// weekly rows older than the refresh window. The weekly backfill adds exactly
// those, without the full rebuild that would send every trend read back to the
// raw view while it ran.
func TestPgStore_WeeklyUsage_BackfillOnBuiltRollups(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	seedWeeklyRuns(t, s, now.Add(-5*24*time.Hour))
	if _, err := s.pool.Exec(ctx, `INSERT INTO ai_report_runs
		(dashboard_user_id, week, tz, since, until, status, runtime, model, auth_mode,
		 tokens_reported, input_tokens, cached_input_tokens, output_tokens, started_at)
		SELECT dashboard_user_id, '2026-W38', 'UTC', $1, $1, 'completed', 'codex-app-server', 'gpt-5.6-terra', 'chatgpt',
		 true, 10, 0, 1, $1 FROM ai_report_runs LIMIT 1`, now.Add(-time.Hour)); err != nil {
		t.Fatalf("insert recent run: %v", err)
	}

	// The state of a v2 installation: built, marked, no weekly rows. The sentinel has
	// no source row, so a full rebuild would delete it.
	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("full refresh: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM usage_hourly_rollups WHERE agent = 'weekly'`); err != nil {
		t.Fatalf("drop weekly rollups: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO usage_hourly_rollups (bucket, model, agent, event_count)
		VALUES (date_trunc('hour', $1::timestamptz), 'sentinel', 'claude', 7)`, now.Add(-10*24*time.Hour)); err != nil {
		t.Fatalf("insert sentinel: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills (name) VALUES ($1)`, usageHourlyRollupBackfill); err != nil {
		t.Fatalf("mark rollups built: %v", err)
	}
	from := now.Add(-UsageRollupWindow)
	if err := s.RefreshUsageHourlyRollups(ctx, &from); err != nil {
		t.Fatalf("tail refresh: %v", err)
	}

	weeklyEvents := func() int64 {
		t.Helper()
		var n int64
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(event_count),0) FROM usage_hourly_rollups WHERE agent = 'weekly'`).Scan(&n); err != nil {
			t.Fatalf("read rollups: %v", err)
		}
		return n
	}
	if got := weeklyEvents(); got != 1 {
		t.Fatalf("before backfill weekly events = %d, want 1 (the tail refresh's recent run)", got)
	}

	for i := 0; i < 2; i++ {
		if err := s.BackfillUsageHourlyRollups(ctx); err != nil {
			t.Fatalf("backfill %d: %v", i, err)
		}
		if got := weeklyEvents(); got != 4 {
			t.Errorf("backfill %d weekly events = %d, want 4", i, got)
		}
	}
	var sentinel int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM usage_hourly_rollups WHERE model = 'sentinel'`).Scan(&sentinel); err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if sentinel != 1 {
		t.Error("weekly backfill rebuilt the whole aggregate")
	}
	if !markerSet(t, s, usageHourlyRollupWeeklyBackfill) {
		t.Error("weekly marker unset after backfill")
	}

	// Re-running the weekly pass without its marker must not double the rows.
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, usageHourlyRollupWeeklyBackfill); err != nil {
		t.Fatalf("clear weekly marker: %v", err)
	}
	if err := s.BackfillUsageHourlyRollups(ctx); err != nil {
		t.Fatalf("re-run backfill: %v", err)
	}
	if got := weeklyEvents(); got != 4 {
		t.Errorf("re-run weekly events = %d, want 4", got)
	}
}

// A run can report tokens while it is still running, and boot recovery stamps
// finished_at on it later. If that moved the row's ts, a run already rolled up at
// its start would leave that copy behind once it is outside the refresh window
// and enter the aggregate a second time at the new ts.
func TestPgStore_WeeklyUsage_LateFinishKeepsItsBucket(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Now().UTC()
	u, err := s.CreateDashboardUser(ctx, &DashboardUser{Email: t.Name() + "@example.com", PasswordHash: "test", Role: "user"})
	if err != nil {
		t.Fatalf("CreateDashboardUser: %v", err)
	}
	var runID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO ai_report_runs
		(dashboard_user_id, week, tz, since, until, status, runtime, model, auth_mode,
		 tokens_reported, input_tokens, cached_input_tokens, output_tokens, started_at)
		VALUES ($1, '2026-W37', 'UTC', $2, $2, 'running', 'codex-app-server', 'gpt-5.6-terra', 'chatgpt',
		 true, 1000, 0, 100, $2) RETURNING id`, u.ID, now.Add(-3*UsageRollupWindow)).Scan(&runID); err != nil {
		t.Fatalf("insert running run: %v", err)
	}
	if err := s.RefreshUsageHourlyRollups(ctx, nil); err != nil {
		t.Fatalf("full refresh: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE ai_report_runs SET status = 'failed', finished_at = now() WHERE id = $1`, runID); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	from := now.Add(-UsageRollupWindow)
	if err := s.RefreshUsageHourlyRollups(ctx, &from); err != nil {
		t.Fatalf("tail refresh: %v", err)
	}

	var events int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(event_count),0) FROM usage_hourly_rollups WHERE agent = 'weekly'`).Scan(&events); err != nil {
		t.Fatalf("read rollups: %v", err)
	}
	if events != 1 {
		t.Errorf("weekly rollup events = %d, want 1 (the run counted once)", events)
	}
}

// Weekly runs are priced by the provider their runtime bills: an OpenAI API run
// at codex_model_rates like Codex, a Claude API run at the Claude price table
// the offline Claude imputation reads, weighted the same way.
func TestPgStore_WeeklyUsage_CostByRuntime(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	u, err := s.CreateDashboardUser(ctx, &DashboardUser{Email: t.Name() + "@example.com", PasswordHash: "test", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO ai_report_runs
		(dashboard_user_id, week, tz, since, until, status, runtime, model, auth_mode,
		 tokens_reported, input_tokens, cached_input_tokens, output_tokens, started_at, finished_at)
		VALUES ($1, '2026-W37', 'UTC', $2, $2, $3, $4, $5, 'api_key', true, 30000, 20000, 1000, $6, $6)`
	for i, r := range []struct{ status, runtime, model string }{
		{"completed", "openai-api", "gpt-5.6-terra"},
		{"canceled", "claude-api", "claude-sonnet-5"},
		{"completed", "codex-app-server", "gpt-5.6-terra"},
	} {
		if _, err := s.pool.Exec(ctx, insert, u.ID, now, r.status, r.runtime, r.model, now.Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("insert %s: %v", r.runtime, err)
		}
	}

	type row struct {
		billing                     string
		cost                        *float64
		in, out, cacheRead, cacheWr int
	}
	got := map[string]row{}
	rows, err := s.pool.Query(ctx, `SELECT r.runtime, e.billing_provider, e.cost_usd, e.input_tokens, e.output_tokens, e.cache_read_tokens, e.cache_create_tokens
		FROM unified_events e JOIN ai_report_runs r ON e.tiebreak = 'w:' || r.id WHERE e.agent = 'weekly'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var rt string
		var g row
		if err := rows.Scan(&rt, &g.billing, &g.cost, &g.in, &g.out, &g.cacheRead, &g.cacheWr); err != nil {
			t.Fatal(err)
		}
		got[rt] = g
	}
	rows.Close()
	if len(got) != 3 {
		t.Fatalf("rows = %+v", got)
	}
	// The token contract does not change with the runtime.
	for rt, g := range got {
		if g.in != 10000 || g.cacheRead != 20000 || g.out != 1000 || g.cacheWr != 0 {
			t.Errorf("%s tokens = %+v", rt, g)
		}
	}

	var openaiWant, claudeWant float64
	if err := s.pool.QueryRow(ctx, `SELECT codex_rate_cost_usd('gpt-5.6-terra', $1::date, 30000, 20000, 1000)`, now).Scan(&openaiWant); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT claude_weighted_tokens('claude-sonnet-5', 10000, 1000, 20000, 0)
		* (SELECT input_rate FROM claude_model_rates WHERE 'claude-sonnet-5' LIKE model_prefix || '%' AND effective_from <= $1::date
		   ORDER BY length(model_prefix) DESC, effective_from DESC LIMIT 1) / 1e6`, now).Scan(&claudeWant); err != nil {
		t.Fatal(err)
	}
	if openaiWant <= 0 || claudeWant <= 0 {
		t.Fatalf("reference costs = %v %v, want seeded rates", openaiWant, claudeWant)
	}
	for rt, want := range map[string]struct {
		billing string
		cost    float64
	}{
		"openai-api":       {"openai", openaiWant},
		"codex-app-server": {"openai", openaiWant},
		"claude-api":       {"anthropic", claudeWant},
	} {
		g := got[rt]
		if g.billing != want.billing || g.cost == nil || math.Abs(*g.cost-want.cost) > 1e-9 {
			t.Errorf("%s = billing %q cost %v, want %q %v", rt, g.billing, g.cost, want.billing, want.cost)
		}
	}
}
