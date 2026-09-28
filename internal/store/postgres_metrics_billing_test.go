package store

import (
	"context"
	"testing"
	"time"
)

// A Codex metric carries the billing account the exporter header named (#715),
// so the visible_metrics view has something to match exclusions against.
func TestInsertMetricsStoresBillingAccount(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	one := int64(1)
	if err := s.InsertMetrics(ctx, []*OtelMetric{{Ts: time.Now().UTC(), MetricName: "codex.tool.call", Agent: "codex",
		BillingProvider: "openai", AccountID: "acct-1", ValueInt: &one}}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	var account string
	if err := s.pool.QueryRow(ctx, `SELECT account_id FROM otel_metrics`).Scan(&account); err != nil {
		t.Fatalf("read account_id: %v", err)
	}
	if account != "acct-1" {
		t.Fatalf("account_id = %q, want acct-1", account)
	}
}

// visibleMetricUsers returns, per user_id, how many metric rows visible_metrics shows.
func visibleMetricUsers(t *testing.T, s *PgStore) map[string]int {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT user_id, count(*) FROM visible_metrics GROUP BY 1`)
	if err != nil {
		t.Fatalf("read visible_metrics: %v", err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var user string
		var n int
		if err := rows.Scan(&user, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[user] = n
	}
	return got
}

func codexMetric(user, account string) *OtelMetric {
	one := int64(1)
	return &OtelMetric{Ts: time.Now().UTC(), MetricName: "codex.tool.call", UserID: user,
		ProfileEmail: user + "@example.test", LoginEmail: user + "@example.test",
		Agent: "codex", BillingProvider: "openai", AccountID: account, ValueInt: &one}
}

func codexQuotaSample(profile, account string) *QuotaSample {
	q := sampleAt(0, account, "300")
	q.BillingProvider = "openai"
	q.ProfileEmail = profile
	return q
}

// A metric that names its billing account is hidden exactly when a session
// record of that account would be: excluded by billing id, or linked to an
// excluded address (#715). Excluding the dashboard address the metric carries
// as login_email does not reach it -- for Codex that address is the cctrace
// login, not an account, and visible_session_records does not hide Codex
// sessions by it either.
func TestVisibleMetricsAppliesBillingExclusionsToCodexAccount(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	one := int64(1)
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		codexMetric("personal", "acct-personal"),
		codexMetric("team", "acct-team"),
		{Ts: time.Now().UTC(), MetricName: "claude_code.token.usage", UserID: "claude", LoginEmail: "team@example.test",
			Agent: "claude", BillingProvider: "anthropic", ValueInt: &one},
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-personal", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if got := visibleMetricUsers(t, s); got["personal"] != 0 || got["team"] != 1 {
		t.Fatalf("billing-excluded: visible = %v, want personal hidden and team shown", got)
	}
	if err := s.RemoveExcludedBillingAccount(ctx, "openai", "acct-personal"); err != nil {
		t.Fatalf("RemoveExcludedBillingAccount: %v", err)
	}
	if got := visibleMetricUsers(t, s); got["personal"] != 1 {
		t.Fatalf("after un-excluding: visible = %v, want personal back", got)
	}

	// Linked through an excluded address seen on the account's quota reading.
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{personalLinkSample("acct-personal", "owner@example.test")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, err := s.ExcludeAccount(ctx, "owner@example.test", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if got := visibleMetricUsers(t, s); got["personal"] != 0 {
		t.Fatalf("linked address excluded: visible = %v, want personal hidden", got)
	}

	// The team member's dashboard address: hides the Claude metric that names it
	// as its Anthropic login, not the Codex metric on a visible account.
	if _, err := s.ExcludeAccount(ctx, "team@example.test", "team", "admin"); err != nil {
		t.Fatalf("ExcludeAccount: %v", err)
	}
	if got := visibleMetricUsers(t, s); got["team"] != 1 || got["claude"] != 0 {
		t.Fatalf("dashboard address excluded: visible = %v, want team shown and claude hidden", got)
	}
}

// Rows from clients older than the account header carry no account. Until they
// age out, such a row is hidden when every Codex account its user was seen on is
// excluded -- and shown when any one is not, or none is known.
func TestVisibleMetricsHidesAccountlessCodexRowsOfFullyExcludedUsers(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.InsertMetrics(ctx, []*OtelMetric{codexMetric("solo", ""), codexMetric("multi", ""), codexMetric("unknown", "")}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{
		codexQuotaSample("solo@example.test", "acct-solo"),
		codexQuotaSample("multi@example.test", "acct-solo"),
		codexQuotaSample("multi@example.test", "acct-work"),
	}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}

	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-solo", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	got := visibleMetricUsers(t, s)
	if got["solo"] != 0 || got["multi"] != 1 || got["unknown"] != 1 {
		t.Fatalf("visible = %v, want solo hidden, multi and unknown shown", got)
	}

	if err := s.RemoveExcludedBillingAccount(ctx, "openai", "acct-solo"); err != nil {
		t.Fatalf("RemoveExcludedBillingAccount: %v", err)
	}
	if got := visibleMetricUsers(t, s); got["solo"] != 1 {
		t.Fatalf("after un-excluding: visible = %v, want solo back", got)
	}
}

// A reading that shows a fully excluded profile on a new, visible account must
// bring its account-less rows back without waiting for the next exclusion change.
func TestRefreshExcludedBillingLinksRevisitsCodexMetricProfiles(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if err := s.InsertMetrics(ctx, []*OtelMetric{codexMetric("solo", "")}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{codexQuotaSample("solo@example.test", "acct-solo")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-solo", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if got := visibleMetricUsers(t, s); got["solo"] != 0 {
		t.Fatalf("visible = %v, want solo hidden", got)
	}

	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{codexQuotaSample("solo@example.test", "acct-work")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	changed, err := s.RefreshExcludedBillingLinks(ctx)
	if err != nil {
		t.Fatalf("RefreshExcludedBillingLinks: %v", err)
	}
	if !changed || visibleMetricUsers(t, s)["solo"] != 1 {
		t.Fatalf("changed=%v visible=%v, want solo back", changed, visibleMetricUsers(t, s))
	}
	if changed, _ := s.RefreshExcludedBillingLinks(ctx); changed {
		t.Fatal("second refresh reported a change with nothing new")
	}
}

// The case that made the per-user rule too coarse (#715, prod 2026-09-21). One
// person runs two Codex homes: ~/.codex switched from a team account to an
// excluded work account, while ~/.codex-2 stayed on an excluded personal
// account the whole time and sends no metrics. An account-less metric is judged
// by the accounts the person's session records show around its minute: hidden
// only when every one of them is excluded. With no record nearby, the latest one
// within 12 hours decides; past that, the per-user rule does.
func TestVisibleMetricsJudgesAccountlessCodexRowsByMinute(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const profile = "person@example.test"
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	record := func(at time.Duration, uuid, account string) *SessionRecord {
		return &SessionRecord{Ts: base.Add(at), SessionID: "s-" + account, UUID: uuid, RecordType: "usage",
			UserID: "person", ProfileEmail: profile, Agent: "codex", BillingProvider: "openai", AccountID: account}
	}
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		record(0, "team-1", "acct-team"), // team + personal side by side: visible
		record(0, "personal-1", "acct-personal"),
		record(5*time.Minute, "team-2", "acct-team"),
		record(2*time.Hour, "work-1", "acct-work"), // work + personal: hidden
		record(2*time.Hour, "personal-2", "acct-personal"),
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{
		codexQuotaSample(profile, "acct-team"), codexQuotaSample(profile, "acct-personal"), codexQuotaSample(profile, "acct-work"),
	}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	one := int64(1)
	metric := func(name string, at time.Duration) *OtelMetric {
		return &OtelMetric{Ts: base.Add(at), MetricName: "codex.tool.call", UserID: name, ProfileEmail: profile,
			Agent: "codex", BillingProvider: "openai", ValueInt: &one}
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{
		metric("team-window", 30*time.Second),
		metric("team-lookback", 10*time.Minute),
		metric("work-window", 2*time.Hour+30*time.Second),
		metric("work-lookback", 2*time.Hour+10*time.Minute),
		metric("stale", 15*time.Hour), // 13h past the last record: per-user rule
	}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}

	for _, account := range []string{"acct-personal", "acct-work"} {
		if _, _, err := s.ExcludeBillingAccount(ctx, "openai", account, "personal", "admin"); err != nil {
			t.Fatalf("ExcludeBillingAccount %s: %v", account, err)
		}
	}
	got := visibleMetricUsers(t, s)
	want := map[string]int{"team-window": 1, "team-lookback": 1, "work-window": 0, "work-lookback": 0, "stale": 1}
	for name, n := range want {
		if got[name] != n {
			t.Errorf("%s visible %d, want %d (visible = %v)", name, got[name], n, got)
		}
	}

	// Reversible: un-excluding the work account brings its minutes back.
	if err := s.RemoveExcludedBillingAccount(ctx, "openai", "acct-work"); err != nil {
		t.Fatalf("RemoveExcludedBillingAccount: %v", err)
	}
	if got := visibleMetricUsers(t, s); got["work-window"] != 1 || got["work-lookback"] != 1 {
		t.Fatalf("after un-excluding: visible = %v, want the work minutes back", got)
	}

	// Records keep arriving after an exclusion. The periodic refresh judges their
	// minutes without waiting for the next exclusion change.
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{record(20*time.Hour, "personal-3", "acct-personal")}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if err := s.InsertMetrics(ctx, []*OtelMetric{metric("late", 20*time.Hour+30*time.Second)}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	if _, err := s.RefreshExcludedBillingLinks(ctx); err != nil {
		t.Fatalf("RefreshExcludedBillingLinks: %v", err)
	}
	if got := visibleMetricUsers(t, s); got["late"] != 0 {
		t.Fatalf("after refresh: visible = %v, want the new excluded-only minute hidden", got)
	}
}

// Profile addresses reach the three tables through different paths and are not
// case-normalized. A minute judged hidden from the session records must still
// hide a metric whose profile address differs only in case -- here the per-user
// fallback would show it, so only the minute decision can hide it.
func TestVisibleMetricsMinuteDecisionIgnoresProfileCase(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: base, SessionID: "s-personal", UUID: "personal-1",
		RecordType: "usage", UserID: "person", ProfileEmail: "Person@Example.test", Agent: "codex",
		BillingProvider: "openai", AccountID: "acct-personal"}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{
		codexQuotaSample("person@example.test", "acct-team"), codexQuotaSample("PERSON@example.test", "acct-personal"),
	}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	one := int64(1)
	if err := s.InsertMetrics(ctx, []*OtelMetric{{Ts: base.Add(30 * time.Second), MetricName: "codex.tool.call",
		UserID: "personal-window", ProfileEmail: "person@EXAMPLE.test", Agent: "codex", BillingProvider: "openai",
		ValueInt: &one}}); err != nil {
		t.Fatalf("InsertMetrics: %v", err)
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "openai", "acct-personal", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	if got := visibleMetricUsers(t, s); got["personal-window"] != 0 {
		t.Fatalf("visible = %v, want the excluded-only minute hidden across address case", got)
	}
}

// The minute table answers for account-less Codex metrics, so only a profile
// with an excluded OpenAI account needs minutes -- the same provider rule the
// per-user fallback uses. An excluded Anthropic account must not pull the
// profile in.
func TestExcludedCodexMetricMinutesIgnoreAnthropicExclusions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const profile = "person@example.test"
	if err := s.InsertSessionRecords(ctx, []*SessionRecord{{Ts: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC),
		SessionID: "s-codex", UUID: "codex-1", RecordType: "usage", UserID: "person", ProfileEmail: profile,
		Agent: "codex", BillingProvider: "openai", AccountID: "acct-codex"}}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}
	claude := sampleAt(0, "acct-claude", "300")
	claude.ProfileEmail = profile
	if _, err := s.InsertQuotaSamples(ctx, []*QuotaSample{claude, codexQuotaSample(profile, "acct-codex")}); err != nil {
		t.Fatalf("InsertQuotaSamples: %v", err)
	}
	if _, _, err := s.ExcludeBillingAccount(ctx, "anthropic", "acct-claude", "personal", "admin"); err != nil {
		t.Fatalf("ExcludeBillingAccount: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM excluded_codex_metric_minutes`).Scan(&n); err != nil {
		t.Fatalf("count minutes: %v", err)
	}
	if n != 0 {
		t.Fatalf("excluded_codex_metric_minutes has %d rows, want none for an Anthropic-only exclusion", n)
	}
}
