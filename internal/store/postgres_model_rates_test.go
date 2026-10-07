package store

import (
	"context"
	"math"
	"testing"
	"time"
)

// weighted mirrors claude_weighted_tokens in Go, so a test can say what a scale
// should produce without restating the SQL. Keeping the two in step is the whole
// reason the SQL side is a function: a scale is dollars per weighted token, and
// if the fit and the application weight a row differently the number is noise.
func weighted(model string, in, out, cacheRead, cacheWrite int) float64 {
	read := 0.1
	if model == "claude-fable-5-1" || model == "claude-mythos-5-1" {
		read = 0.025
	}
	return float64(in) + 5*float64(out) + read*float64(cacheRead) + 1.25*float64(cacheWrite)
}

func billedEvent(session, model string, ts time.Time, in, out, cacheRead, cacheWrite int, cost float64) *OtelEvent {
	return &OtelEvent{
		Ts:                ts,
		EventName:         "claude_code.api_request",
		SessionID:         session,
		UserID:            "uid-rates",
		ProfileEmail:      "p@example.com",
		Model:             model,
		CostUSD:           &cost,
		InputTokens:       ptrInt(in),
		OutputTokens:      ptrInt(out),
		CacheReadTokens:   ptrInt(cacheRead),
		CacheCreateTokens: ptrInt(cacheWrite),
		Agent:             "claude",
		BillingProvider:   "anthropic",
	}
}

func offlineAssistant(session, model string, ts time.Time, in, out, cacheRead, cacheWrite int) *SessionRecord {
	return &SessionRecord{
		Ts:                ts,
		SessionID:         session,
		RecordType:        "assistant",
		ProfileEmail:      "p@example.com",
		UserID:            "uid-rates",
		Model:             model,
		InputTokens:       ptrInt(in),
		OutputTokens:      ptrInt(out),
		CacheReadTokens:   ptrInt(cacheRead),
		CacheCreateTokens: ptrInt(cacheWrite),
		Agent:             "claude",
		BillingProvider:   "anthropic",
	}
}

// seedBilledWeek writes enough billed events in one week for that week to clear
// the 20-row floor, at exactly the requested dollars-per-weighted-token.
func seedBilledWeek(t *testing.T, s *PgStore, model string, day time.Time, scale float64) {
	t.Helper()
	const in, out, cacheRead, cacheWrite = 1000, 200, 5000, 3000
	w := weighted(model, in, out, cacheRead, cacheWrite)
	var events []*OtelEvent
	for i := 0; i < 20; i++ {
		events = append(events, billedEvent(
			"online-"+model+"-"+day.Format("0102")+"-"+time.Duration(i).String(),
			model, day.Add(time.Duration(i)*time.Minute), in, out, cacheRead, cacheWrite, w*scale))
	}
	if err := s.InsertEvents(context.Background(), events); err != nil {
		t.Fatalf("seed billed week: %v", err)
	}
}

func imputedRow(t *testing.T, s *PgStore, session string) (cost float64, source string) {
	t.Helper()
	if err := s.pool.QueryRow(context.Background(),
		`SELECT cost_usd, rate_source FROM claude_imputed_cost WHERE session_id = $1`, session,
	).Scan(&cost, &source); err != nil {
		t.Fatalf("read imputed row for %s: %v", session, err)
	}
	return cost, source
}

func refreshRates(t *testing.T, s *PgStore) {
	t.Helper()
	ctx := context.Background()
	if err := s.RefreshModelRateBuckets(ctx); err != nil {
		t.Fatalf("RefreshModelRateBuckets: %v", err)
	}
	if err := s.RefreshClaudeImputedCost(ctx); err != nil {
		t.Fatalf("RefreshClaudeImputedCost: %v", err)
	}
}

// The failure #526 is about: one scale for a model's whole life means a price
// change rewrites history. Sonnet 5 really did drop from $3/MTok to $2 in
// August, and an offline row from July has to keep July's price.
func TestImputedCostUsesTheWeekTheRowFallsIn(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	const model = "claude-sonnet-5"
	july := recentMonday(10).Add(12 * time.Hour)
	august := recentMonday(5).Add(12 * time.Hour)
	seedBilledWeek(t, s, model, july, 3.0/1e6)
	seedBilledWeek(t, s, model, august, 2.0/1e6)

	if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{
		offlineAssistant("off-july", model, july.Add(2*time.Hour), 1000, 100, 2000, 500),
		offlineAssistant("off-august", model, august.Add(2*time.Hour), 1000, 100, 2000, 500),
	}); err != nil {
		t.Fatal(err)
	}
	refreshRates(t, s)

	w := weighted(model, 1000, 100, 2000, 500)
	for _, tc := range []struct {
		session string
		scale   float64
	}{
		{"off-july", 3.0 / 1e6},
		{"off-august", 2.0 / 1e6},
	} {
		cost, source := imputedRow(t, s, tc.session)
		if want := w * tc.scale; math.Abs(cost-want) > want*0.001 {
			t.Errorf("%s cost = %.6f, want %.6f -- the row was priced with another week's rate", tc.session, cost, want)
		}
		if source != "bucket" {
			t.Errorf("%s rate_source = %q, want \"bucket\"", tc.session, source)
		}
	}
}

// A dev-sync copies production into an existing database, so months of billed
// history arrive after that database already holds recent buckets. Ingestion
// order is why the cursor counts rows and not timestamps: a `ts >` watermark
// steps straight over data that is old by timestamp but new by arrival, and
// those weeks get no bucket at all -- every offline row in them then falls
// through to a neighbouring week or a price list.
func TestLateArrivingHistoryStillGetsItsOwnBucket(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	const model = "claude-opus-5"
	recent := recentMonday(2).Add(12 * time.Hour)
	seedBilledWeek(t, s, model, recent, 50.0/1e6)
	refreshRates(t, s)

	// Now the backfill lands: older by timestamp, newer by arrival.
	old := recentMonday(8).Add(12 * time.Hour)
	seedBilledWeek(t, s, model, old, 5.0/1e6)
	if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{
		offlineAssistant("off-old", model, old.Add(2*time.Hour), 1000, 100, 2000, 500),
	}); err != nil {
		t.Fatal(err)
	}
	refreshRates(t, s)

	cost, source := imputedRow(t, s, "off-old")
	want := weighted(model, 1000, 100, 2000, 500) * 5.0 / 1e6
	if math.Abs(cost-want) > want*0.001 {
		t.Errorf("cost = %.6f, want %.6f -- the backfilled week never got a bucket", cost, want)
	}
	if source != "bucket" {
		t.Errorf("rate_source = %q, want \"bucket\"", source)
	}
}

// A week already written keeps its number when a later week is added. The view
// this replaces refit all of history every 30 minutes, so spend that had already
// been reported moved underneath it.
func TestAnEarlierWeekIsUnaffectedByALaterOne(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	const model = "claude-opus-5"
	old := recentMonday(8).Add(12 * time.Hour)
	seedBilledWeek(t, s, model, old, 5.0/1e6)
	if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{
		offlineAssistant("off-old", model, old.Add(2*time.Hour), 1000, 100, 2000, 500),
	}); err != nil {
		t.Fatal(err)
	}
	refreshRates(t, s)
	before, _ := imputedRow(t, s, "off-old")

	seedBilledWeek(t, s, model, recentMonday(2).Add(12*time.Hour), 50.0/1e6)
	refreshRates(t, s)

	after, _ := imputedRow(t, s, "off-old")
	if math.Abs(after-before) > before*0.001 {
		t.Errorf("cost moved from %.6f to %.6f", before, after)
	}
}

// A month with no OTEL at all is the case the 90-day window handled worst: it
// silently used today's traffic to price it. The nearest observed week is the
// closest thing to an observation that exists, and saying so in rate_source is
// what lets a dashboard mark it.
func TestOfflineMonthWithNoBilledTrafficCarriesTheNearestWeek(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	const model = "claude-opus-4-6"
	june := recentMonday(8).Add(12 * time.Hour)
	seedBilledWeek(t, s, model, june, 5.0/1e6)
	if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{
		offlineAssistant("off-march", model, june.AddDate(0, 0, -73), 1000, 100, 2000, 500),
		offlineAssistant("off-july", model, june.AddDate(0, 0, 49), 1000, 100, 2000, 500),
	}); err != nil {
		t.Fatal(err)
	}
	refreshRates(t, s)

	want := weighted(model, 1000, 100, 2000, 500) * 5.0 / 1e6
	for _, tc := range []struct{ session, source string }{
		{"off-march", "carry_back"},
		{"off-july", "carry_forward"},
	} {
		cost, source := imputedRow(t, s, tc.session)
		if math.Abs(cost-want) > want*0.001 {
			t.Errorf("%s cost = %.6f, want %.6f", tc.session, cost, want)
		}
		if source != tc.source {
			t.Errorf("%s rate_source = %q, want %q", tc.session, source, tc.source)
		}
	}
}

// A model this deployment has never been billed for has nothing to fit against.
// The published price is the fallback -- and it has to be the published price
// for that date, or the fallback reintroduces the bug it is backing up.
func TestUnbilledModelFallsBackToThePublishedPrice(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	const model = "claude-sonnet-5"
	if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{
		offlineAssistant("off-before-cut", model, time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC), 1000, 100, 2000, 500),
		offlineAssistant("off-after-cut", model, time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC), 1000, 100, 2000, 500),
	}); err != nil {
		t.Fatal(err)
	}
	refreshRates(t, s)

	w := weighted(model, 1000, 100, 2000, 500)
	for _, tc := range []struct {
		session string
		rate    float64
	}{
		{"off-before-cut", 3.0},
		{"off-after-cut", 2.0},
	} {
		cost, source := imputedRow(t, s, tc.session)
		if want := w * tc.rate / 1e6; math.Abs(cost-want) > want*0.001 {
			t.Errorf("%s cost = %.6f, want %.6f at $%.0f/MTok", tc.session, cost, want, tc.rate)
		}
		if source != "official" {
			t.Errorf("%s rate_source = %q, want \"official\"", tc.session, source)
		}
	}
}

// Models run through Claude Code that Anthropic does not sell must not be priced
// as if it did. A guessed number is worse than the gap, because a gap is visible
// (#441) and a wrong price is not.
func TestNonAnthropicModelsGetNoImputedCost(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	if err := s.InsertSessionRecords(context.Background(), []*SessionRecord{
		offlineAssistant("off-kimi", "kimi-k2.5", time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC), 1000, 100, 2000, 500),
		offlineAssistant("off-qwen", "qwen3.5-plus", time.Date(2026, 4, 9, 9, 0, 0, 0, time.UTC), 1000, 100, 2000, 500),
	}); err != nil {
		t.Fatal(err)
	}
	refreshRates(t, s)

	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM claude_imputed_cost WHERE session_id IN ('off-kimi','off-qwen')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d non-Anthropic rows were priced; they must carry no cost at all", n)
	}
}

// The published footnote: cache reads on Fable 5.1 and Mythos 5.1 cost 0.025x
// base input, not the 0.1x every other model uses. Measured on production,
// billing them at 0.1x over-predicts Fable 5.1 spend by 1.5x. The fit and the
// application both have to know, or the scale silently absorbs the error and
// misplaces it across rows instead.
func TestFableCacheReadsUseTheDocumentedMultiplier(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)

	ctx := context.Background()
	var fable, opus float64
	if err := s.pool.QueryRow(ctx,
		`SELECT claude_weighted_tokens('claude-fable-5-1', 0, 0, 1000000, 0),
		        claude_weighted_tokens('claude-opus-5',    0, 0, 1000000, 0)`).Scan(&fable, &opus); err != nil {
		t.Fatal(err)
	}
	if fable != 25000 {
		t.Errorf("fable 5.1 weighted cache reads = %.0f, want 25000 (0.025x)", fable)
	}
	if opus != 100000 {
		t.Errorf("opus 5 weighted cache reads = %.0f, want 100000 (0.1x)", opus)
	}
	if fable == opus {
		t.Error("the footnote is not applied: both models weight cache reads the same")
	}
}
