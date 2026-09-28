package store

import (
	"math"
	"testing"
	"time"
)

// The pure computation is exercised without a database: every case below is a
// sequence of weekly-meter readings plus per-minute measured tokens for one
// account.

var covT0 = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// weeklySeries returns n readings `every` apart on the weekly meter, each
// burning stepPct, with costPerInterval measured dollars landing in one minute
// inside every interval. Dollars, not tokens: since #539 the factor is fitted
// on cost_usd, so that is what a fixture has to supply for an account to fit at
// all. ChartTokens is left at zero and set by the bucket cases that need the
// token axis.
func weeklySeries(n int, every time.Duration, stepPct, costPerInterval float64) ([]coverageSample, []coverageMinute) {
	var samples []coverageSample
	var minutes []coverageMinute
	resets := covT0.Add(7 * 24 * time.Hour)
	for i := 0; i <= n; i++ {
		at := covT0.Add(time.Duration(i) * every)
		samples = append(samples, coverageSample{
			LoginEmail: "a@x.test", AccountID: "acct-a", WindowKey: "weekly_all", Plan: "max",
			SampledAt: at, UsedPct: float64(i) * stepPct, ResetsAt: &resets,
		})
		if i > 0 && costPerInterval > 0 {
			minutes = append(minutes, coverageMinute{At: at.Add(-every / 2), Cost: costPerInterval})
		}
	}
	return samples, minutes
}

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v (±%v)", name, got, want, tol)
	}
}

func one(minutes []coverageMinute) map[string][]coverageMinute {
	return map[string][]coverageMinute{"a@x.test": minutes}
}

// Dense readings: 5 minutes apart, 0.1% each, so a fit segment spans 20 readings.
func TestCoverageGap_fullCoverageReadsAsOne(t *testing.T) {
	samples, minutes := weeklySeries(300, 5*time.Minute, 0.1, 100)
	since, until := covT0, covT0.Add(300*5*time.Minute)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	acct := gap.Accounts[0]
	if acct.WindowKey != "weekly_all" || acct.Unfitted != "" {
		t.Fatalf("account = %+v", acct)
	}
	approx(t, "k", acct.KUsdPerPct, 1000, 1e-9)
	approx(t, "implied", gap.ImpliedUSD, 30000, 1e-6)
	approx(t, "measured", gap.MeasuredUSD, 30000, 1e-6)
	approx(t, "ratio", gap.CoverageRatio, 1, 1e-9)
	approx(t, "sample coverage", gap.SampleCoverage, 1, 1e-9)
	if gap.KMethod != CoverageKMethod {
		t.Errorf("k_method = %q", gap.KMethod)
	}
}

// The reason the weekly meter was chosen: readings six hours apart bracket the
// burn between them exactly, so a daemon that is awake a few times a day is
// enough. Nothing about the fit or the ratio depends on the gap length.
func TestCoverageGap_sparseReadingsStillFit(t *testing.T) {
	samples, minutes := weeklySeries(20, 6*time.Hour, 3, 3000)
	since, until := covT0, covT0.Add(20*6*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "k", gap.Accounts[0].KUsdPerPct, 1000, 1e-9)
	approx(t, "ratio", gap.CoverageRatio, 1, 1e-9)
	approx(t, "sample coverage", gap.SampleCoverage, 1, 1e-9)
}

// Half the intervals burn quota with nothing measured -- the machine that did the
// work has no cctrace. k must come from the measured half alone, so those dark
// intervals show up as missing coverage rather than dragging k down to explain
// them away.
//
// After #539 the estimator is a delta-weighted mean, which would do exactly that
// dragging if the dark segments entered it. They do not: fitSegments drops a
// segment with nothing measured in it rather than contributing a zero, so k is
// still 1,000 and the ratio is still 0.5. The dim case, where a segment has SOME
// measured usage, is the one the mean now absorbs -- see
// TestCoverageGap_dimSegmentsAreAbsorbedIntoTheFactor.
//
// Mutation: drop the `if sumMeasured > 0` guard in fitSegments so dark segments
// are appended too -> k 500, ratio 1.
func TestCoverageGap_darkIntervalsLowerTheRatio(t *testing.T) {
	samples, minutes := weeklySeries(40, 3*time.Hour, 2, 2000)
	var seen []coverageMinute
	for i, m := range minutes {
		if i%2 == 0 {
			seen = append(seen, m)
		}
	}
	since, until := covT0, covT0.Add(40*3*time.Hour)

	gap := computeCoverageGap(samples, one(seen), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "k", gap.Accounts[0].KUsdPerPct, 1000, 1e-9)
	approx(t, "ratio", gap.CoverageRatio, 0.5, 1e-9)
}

// A window rollover between two readings. The burn before the rollover instant
// is unseen and that stretch leaves both sides; after it, the new reading is the
// burn exactly and that stretch stays on both sides.
func TestCoverageGap_resetIsCensoredUpToTheRollover(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	n := len(samples)
	rollover := samples[n-2].SampledAt.Add(30 * time.Minute)
	later := covT0.Add(14 * 24 * time.Hour)
	// Every earlier reading names the rollover; the last one names the next.
	for i := 0; i < n-1; i++ {
		samples[i].ResetsAt = &rollover
	}
	samples[n-1].UsedPct = 3
	samples[n-1].ResetsAt = &later
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	acct := gap.Accounts[0]
	approx(t, "censored seconds", acct.CensoredSeconds, 1800, 1e-9)
	// 29 whole intervals plus the post-rollover 3%.
	approx(t, "implied", gap.ImpliedUSD, (29*2+3)*1000, 1e-6)
	// The last interval's minute lands at the rollover instant, on the known side.
	approx(t, "measured", gap.MeasuredUSD, 30*2000, 1e-6)
	approx(t, "sample coverage", acct.SampleCoverage, 1-1800.0/(30*3600), 1e-9)
}

// Without a known rollover instant the whole interval is censored on both
// sides: measured tokens there would be compared against an unknown burn.
func TestCoverageGap_unknownRolloverCensorsWholeInterval(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	later := covT0.Add(14 * 24 * time.Hour)
	n := len(samples)
	samples[n-1].UsedPct = 3
	samples[n-1].ResetsAt = &later
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "censored seconds", gap.Accounts[0].CensoredSeconds, 3600, 1e-9)
	approx(t, "implied", gap.ImpliedUSD, 29*2*1000, 1e-6)
	approx(t, "measured", gap.MeasuredUSD, 29*2000, 1e-6)
}

// resets_at is recomputed by the API on every call and drifts by microseconds
// between readings of the same window. Seen on prod: 1,124 of 2,714 consecutive
// weekly readings "advanced" while the window reset once. That drift is not a
// rollover and must not censor the interval.
func TestCoverageGap_resetsAtJitterIsNotAReset(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	for i := range samples {
		jittered := samples[i].ResetsAt.Add(time.Duration(i) * 3 * time.Microsecond)
		samples[i].ResetsAt = &jittered
	}
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	if gap.Accounts[0].CensoredSeconds != 0 {
		t.Errorf("jitter was treated as a reset: censored %v s", gap.Accounts[0].CensoredSeconds)
	}
	approx(t, "implied", gap.ImpliedUSD, 60000, 1e-6)
}

// The newer API shape omits resets_at for some windows, so a falling percentage
// is the only reset signal there.
func TestCoverageGap_pctDropWithoutResetsAtIsAReset(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	for i := range samples {
		samples[i].ResetsAt = nil
	}
	samples[len(samples)-1].UsedPct = 3
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "implied", gap.ImpliedUSD, 29*2*1000, 1e-6)
	approx(t, "measured", gap.MeasuredUSD, 29*2000, 1e-6)
	approx(t, "censored seconds", gap.Accounts[0].CensoredSeconds, 3600, 1e-9)
}

// A small drop while resets_at stands still is clock skew between two reporting
// profiles, not a reset. It contributes nothing rather than a phantom window,
// and the recovery is measured from the high-water mark.
func TestCoverageGap_skewWithoutResetAdvanceIsZero(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	samples[15].UsedPct = samples[14].UsedPct - 0.5
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	if gap.Accounts[0].CensoredSeconds != 0 {
		t.Errorf("skew was treated as a reset: censored %v s", gap.Accounts[0].CensoredSeconds)
	}
	approx(t, "implied", gap.ImpliedUSD, 60000, 1e-6)
}

// A few segments far above the plateau -- the meter barely moved while many
// tokens were measured, as integer quantization can produce -- would drag the
// upper quantile away from the fully measured level. The top must agree with
// the plateau, or no factor is fitted.
func TestCoverageGap_topOutliersRefuseToFit(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	for i := 0; i < 3; i++ {
		minutes[i].Cost = 20000
	}
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if gap.Eligible {
		t.Fatal("eligible with three segments at ten times the plateau")
	}
	if gap.Accounts[0].Unfitted == "" || gap.Accounts[0].KUsdPerPct != 0 {
		t.Errorf("account = %+v, want a refusal with no k", gap.Accounts[0])
	}
}

// Half the segments ran at a tenth of the others: that is a blind spot, not an
// unstable factor. The upper segments agree, so the account still fits rather
// than being refused -- seen on prod, and refused there by the previous Q75/Q25
// rule.
//
// The EXPECTATION changed with #539. Under q90 this case fitted k on the lit
// half (1,000) and reported the dim half as 55% coverage. Under the
// delta-weighted mean the dim segments are averaged in, k is 33,000/60 = 550,
// and the ratio is exactly one: a blind spot that is present throughout the fit
// window is now absorbed into the factor instead of being reported. That is the
// deliberate trade behind the swap -- on prod it moved the seven-day coverage
// from 66.3% to 103.4%, within 3.4% of break-even -- and it is why the ratio now
// answers "how far does this range depart from the 28-day average" rather than
// "how much of the burn did we see".
//
// A segment with NOTHING measured is still dropped rather than averaged in at
// zero, so a wholly dark stretch keeps lowering the ratio. That is
// TestCoverageGap_darkIntervalsLowerTheRatio, which still reports 0.5.
//
// Mutation: fit.KUsdPerPct = quantile(ratios, 0.9) -> k 1000, ratio 0.55.
func TestCoverageGap_dimSegmentsAreAbsorbedIntoTheFactor(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	for i := range minutes {
		if i%2 == 0 {
			minutes[i].Cost = 200
		}
	}
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "k", gap.Accounts[0].KUsdPerPct, 550, 1e-9)
	approx(t, "measured", gap.MeasuredUSD, 33000, 1e-6)
	approx(t, "ratio", gap.CoverageRatio, 1, 1e-9)
}

func TestCoverageGap_tooFewSegmentsRefusesToFit(t *testing.T) {
	samples, minutes := weeklySeries(5, time.Hour, 2, 2000)
	since, until := covT0, covT0.Add(5*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if gap.Eligible {
		t.Fatal("eligible with 5 segments; the floor is 12")
	}
	if len(gap.Unfitted) != 1 || gap.Unfitted[0] != "a@x.test" || gap.Accounts[0].Unfitted == "" {
		t.Errorf("unfitted = %v, account = %+v", gap.Unfitted, gap.Accounts[0])
	}
}

// An interval that straddles the range boundary is attributed by time overlap.
func TestCoverageGap_partialIntervalIsProrated(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	since := covT0.Add(30 * time.Minute)
	until := covT0.Add(30*time.Hour - 30*time.Minute)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "implied", gap.ImpliedUSD, 29*2*1000, 1e-6)
}

// Measured tokens outside the span the readings cover are counted against
// nothing on the burn side, so they are left out and the span is reported.
func TestCoverageGap_measuredOutsideReadingsIsNotCounted(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	before := coverageMinute{At: covT0.Add(-2 * time.Hour), Cost: 999999}
	// Range starts two hours before the first reading.
	since, until := covT0.Add(-2*time.Hour), covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(append([]coverageMinute{before}, minutes...)), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "measured", gap.MeasuredUSD, 60000, 1e-6)
	approx(t, "ratio", gap.CoverageRatio, 1, 1e-9)
	approx(t, "sample coverage", gap.SampleCoverage, 30.0/32.0, 1e-9)
}

// Accounts with usage but no quota readings cannot enter either side of the
// ratio; they are named instead so the gap is never silently narrowed.
func TestCoverageGap_unquotedAccountIsNamedNotCounted(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	since, until := covT0, covT0.Add(30*time.Hour)
	measured := map[string][]coverageMinute{
		"a@x.test": minutes,
		"b@x.test": {{At: covT0.Add(time.Minute), Cost: 99999}},
	}

	gap := computeCoverageGap(samples, measured, since, until)

	approx(t, "measured", gap.MeasuredUSD, 60000, 1e-6)
	if len(gap.Unfitted) != 1 || gap.Unfitted[0] != "b@x.test" {
		t.Errorf("unfitted = %v", gap.Unfitted)
	}
}

// With only the five-hour meter reported it is used, but a gap longer than the
// window hides its resets: the percentage simply comes back lower, which the
// reset rule catches, and the pre-reset burn is censored rather than invented.
func TestCoverageGap_sessionMeterIsTheFallback(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	for i := range samples {
		samples[i].WindowKey = "session"
	}
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible || gap.Accounts[0].WindowKey != "session" {
		t.Fatalf("gap = %+v", gap)
	}
}

// --- bucketed unknown series (Step 2) ---

var covKST = time.FixedZone("KST", 9*3600)

func TestCoverageGap_bucketsAreZeroUnderFullCoverage(t *testing.T) {
	samples, minutes := weeklySeries(48, time.Hour, 2, 2000)
	for i := range minutes {
		minutes[i].ChartTokens = 1000
	}
	since, until := covT0, covT0.Add(48*time.Hour)

	gap := computeCoverageGapBuckets(samples, one(minutes), since, until, &coverageBucketing{granularity: "day", loc: time.UTC})

	if !gap.Eligible {
		t.Fatalf("not eligible: %s", gap.Reason)
	}
	if len(gap.Buckets) != 2 || gap.Buckets[0].Date != "2026-09-01" || gap.Buckets[1].Date != "2026-09-02" {
		t.Fatalf("buckets = %+v", gap.Buckets)
	}
	for _, b := range gap.Buckets {
		approx(t, b.Date+" implied", b.ImpliedUSD, 48000, 1e-6)
		approx(t, b.Date+" measured", b.MeasuredUSD, 48000, 1e-6)
		approx(t, b.Date+" unknown tokens", b.UnknownTokens, 0, 1e-9)
		approx(t, b.Date+" unknown cost", b.UnknownCostUSD, 0, 1e-9)
	}
}

// The second day is dark: the meter keeps moving, nothing is measured. The
// unknown lands on that day alone.
//
// Since #539 both sides are dollars, so the cost axis takes the gap unconverted
// -- the unknown cost IS implied minus measured, asserted here on every bucket.
// Only the token axis is a conversion, at the range-wide
// chart-tokens-per-dollar rate, because the dark day has no mix of its own to
// borrow: 24 lit minutes carried 500 chart tokens against $48,000 measured, so
// the rate is 0.25 and the dark day's $48,000 gap draws as 12,000 tokens.
//
// Mutation: cb.UnknownCostUSD = gap * chartPerUSD, the pre-#539 shape where the
// gap was a token count needing a rate -> $12,000 instead of $48,000.
func TestCoverageGap_bucketsPutTheGapWhereItIs(t *testing.T) {
	samples, minutes := weeklySeries(48, time.Hour, 2, 2000)
	var lit []coverageMinute
	for _, m := range minutes {
		if m.At.Before(covT0.Add(24 * time.Hour)) {
			m.ChartTokens = 500 // 500 chart tokens per 2,000 measured dollars
			lit = append(lit, m)
		}
	}
	since, until := covT0, covT0.Add(48*time.Hour)

	gap := computeCoverageGapBuckets(samples, one(lit), since, until, &coverageBucketing{granularity: "day", loc: time.UTC})

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	for _, b := range gap.Buckets {
		approx(t, b.Date+" unknown cost", b.UnknownCostUSD, b.ImpliedUSD-b.MeasuredUSD, 1e-9)
	}
	approx(t, "day1 unknown", gap.Buckets[0].UnknownTokens, 0, 1e-9)
	approx(t, "day1 unknown cost", gap.Buckets[0].UnknownCostUSD, 0, 1e-9)
	// $48,000 of burn unseen, at 500/2,000 chart tokens per measured dollar.
	approx(t, "day2 unknown tokens", gap.Buckets[1].UnknownTokens, 12000, 1e-6)
	approx(t, "day2 unknown cost", gap.Buckets[1].UnknownCostUSD, 48000, 1e-6)
}

// A partly dark bucket scales its own chart total by its own gap, so the
// unknown wears that bucket's mix rather than a range-wide average.
func TestCoverageGap_bucketsScaleByTheBucketsOwnShare(t *testing.T) {
	samples, minutes := weeklySeries(48, time.Hour, 2, 2000)
	var seen []coverageMinute
	for i, m := range minutes {
		m.ChartTokens = 1000
		if i%2 == 0 {
			seen = append(seen, m)
		}
	}
	since, until := covT0, covT0.Add(48*time.Hour)

	gap := computeCoverageGapBuckets(samples, one(seen), since, until, &coverageBucketing{granularity: "day", loc: time.UTC})

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	for _, b := range gap.Buckets {
		// implied 48,000 vs measured 24,000 -> gap equals what was measured, so
		// the unknown equals the bucket's chart total.
		approx(t, b.Date+" unknown", b.UnknownTokens, 12000, 1e-6)
	}
}

func TestCoverageGap_bucketKeysMatchThePostgresShape(t *testing.T) {
	at := time.Date(2026, 9, 2, 16, 47, 30, 0, time.UTC) // Wednesday; 01:47 KST on the 3rd
	cases := map[string]string{
		"minute": "2026-09-03T01:47:00",
		"hour":   "2026-09-03T01:00:00",
		"day":    "2026-09-03",
		"week":   "2026-08-31",
		"month":  "2026-09-01",
	}
	for g, want := range cases {
		if got := bucketKey(at, &coverageBucketing{granularity: g, loc: covKST}); got != want {
			t.Errorf("%s: key = %q, want %q", g, got, want)
		}
	}
}

// --- the central statistic (#539) ---

// coverageSteps builds one account's weekly readings from a list of per-segment
// (deltaPct, measured dollars) pairs. Reading i sits i hours after covT0 and the
// measured dollars for a segment land in one minute inside it, so each pair is
// exactly one fit segment as long as its delta is at least
// coverageMinFitDelta.
func coverageSteps(steps [][2]float64) ([]coverageSample, []coverageMinute) {
	resets := covT0.Add(7 * 24 * time.Hour)
	samples := []coverageSample{{
		LoginEmail: "a@x.test", AccountID: "acct-a", WindowKey: "weekly_all", Plan: "max",
		SampledAt: covT0, UsedPct: 0, ResetsAt: &resets,
	}}
	var minutes []coverageMinute
	pct := 0.0
	for i, s := range steps {
		at := covT0.Add(time.Duration(i+1) * time.Hour)
		pct += s[0]
		samples = append(samples, coverageSample{
			LoginEmail: "a@x.test", AccountID: "acct-a", WindowKey: "weekly_all", Plan: "max",
			SampledAt: at, UsedPct: pct, ResetsAt: &resets,
		})
		if s[1] > 0 {
			minutes = append(minutes, coverageMinute{At: at.Add(-30 * time.Minute), Cost: s[1]})
		}
	}
	return samples, minutes
}

// The estimator is the delta-weighted mean, not an upper quantile.
//
// Thirty segments move two points each and carry a measured total that rises
// steadily from 1,400 to 2,560, so the per-segment ratio runs 700..1280. Q90
// picks 1,220 -- near the top of that spread -- and implies 73,200 tokens
// against 59,400 measured, a coverage of 81%. The delta-weighted mean is
// sum(measured)/sum(delta) = 990, which implies exactly what was measured.
//
// That identity is the point and also the honest limit of the change: over the
// fit window the ratio is driven to one by construction, so what it reports is
// how far the reported range departs from the 28-day average, not an absolute
// blind spot. On prod (#539) the same swap moved the seven-day coverage from
// 66.3% to 103.4%, within 3.4% of break-even, against 127.5% for the median.
//
// Mutation: fit.KUsdPerPct = quantile(ratios, 0.9) instead of
// sumMeasured/sumDelta -> k 1220, ratio 0.81.
func TestCoverageGap_weightedMeanSitsAtBreakEven(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2000)
	for i := range minutes {
		minutes[i].Cost = 1400 + 40*float64(i)
	}
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "k", gap.Accounts[0].KUsdPerPct, 990, 1e-9)
	approx(t, "measured", gap.MeasuredUSD, 59400, 1e-6)
	approx(t, "implied", gap.ImpliedUSD, 59400, 1e-6)
	approx(t, "ratio", gap.CoverageRatio, 1, 1e-9)
}

// Weighting is by how far the meter moved, not by segment count.
//
// Twelve segments move two points at 500 per point; eight move eight points at
// 1,000 per point. A plain mean of the twenty ratios is 700 -- the numerous
// small segments win. The delta-weighted mean is 76,000/88 = 863.6, pulled
// toward the eight-point segments because they carry 64 of the 88 points.
//
// Mutation: sum(ratios)/len(ratios) instead of sumMeasured/sumDelta -> k 700.
func TestCoverageGap_largeDeltaSegmentsWeighMore(t *testing.T) {
	var steps [][2]float64
	for i := 0; i < 12; i++ {
		steps = append(steps, [2]float64{2, 1000})
	}
	for i := 0; i < 8; i++ {
		steps = append(steps, [2]float64{8, 8000})
	}
	samples, minutes := coverageSteps(steps)
	since, until := covT0, covT0.Add(20*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	k := gap.Accounts[0].KUsdPerPct
	approx(t, "k", k, 76000.0/88.0, 1e-9)
	if k <= 700 {
		t.Errorf("k = %v, want above the unweighted mean of 700", k)
	}
}

// --- the fit's unit (#539) ---

// The factor is fitted on dollars, and token counts do not enter it.
//
// Thirty segments move two points each and cost $2 each, so the factor is
// exactly $1 per point and the range balances. The chart tokens alternate
// between 4,000 and 1,000 per segment -- the model-mix swing that made the
// token-unit factor unstable on prod, where one stretch realized 4.4M tokens per
// point against 25.8M in the stretch beside it. The factor must not notice.
// Fitted on those chart tokens it would be 75,000/60 = 1,250 instead of 1.
//
// Mutation: segment() accumulating minutes[j].ChartTokens into measuredUSD
// instead of minutes[j].Cost -> k 1250.
func TestCoverageGap_factorIsPricedNotCounted(t *testing.T) {
	samples, minutes := weeklySeries(30, time.Hour, 2, 2)
	for i := range minutes {
		if i%2 == 0 {
			minutes[i].ChartTokens = 4000
		} else {
			minutes[i].ChartTokens = 1000
		}
	}
	since, until := covT0, covT0.Add(30*time.Hour)

	gap := computeCoverageGap(samples, one(minutes), since, until)

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "k", gap.Accounts[0].KUsdPerPct, 1, 1e-9)
	approx(t, "measured", gap.MeasuredUSD, 60, 1e-9)
	approx(t, "implied", gap.ImpliedUSD, 60, 1e-9)
	approx(t, "ratio", gap.CoverageRatio, 1, 1e-9)
}

// --- the spread cap (#528) ---

// idleThenBurst builds the prod shape from #528: a dense stretch that fixes the
// factor, then `idle` of readings on an unmoving meter, then one reading that
// jumps by jumpPct. The measured tokens for the jump land in the minute before
// the reading that reveals it.
func idleThenBurst(idle time.Duration, jumpPct, jumpCost float64) ([]coverageSample, []coverageMinute, time.Time) {
	samples, minutes := weeklySeries(300, 5*time.Minute, 0.1, 100)
	last := samples[len(samples)-1]
	resets := *last.ResetsAt
	flat := last.UsedPct
	for at := last.SampledAt.Add(5 * time.Minute); !at.After(last.SampledAt.Add(idle)); at = at.Add(5 * time.Minute) {
		samples = append(samples, coverageSample{
			LoginEmail: last.LoginEmail, AccountID: last.AccountID, WindowKey: last.WindowKey, Plan: last.Plan,
			SampledAt: at, UsedPct: flat, ResetsAt: &resets,
		})
	}
	jumpAt := last.SampledAt.Add(idle + 5*time.Minute)
	samples = append(samples, coverageSample{
		LoginEmail: last.LoginEmail, AccountID: last.AccountID, WindowKey: last.WindowKey, Plan: last.Plan,
		SampledAt: jumpAt, UsedPct: flat + jumpPct, ResetsAt: &resets,
	})
	minutes = append(minutes, coverageMinute{At: jumpAt.Add(-5 * time.Minute), Cost: jumpCost})
	return samples, minutes, last.SampledAt
}

// Nine idle hours followed by a two-point jump. The jump is real burn, but it
// cannot have happened before the meter stopped standing still for that long,
// so it is painted on the capped stretch behind the reading that revealed it
// and the idle hours stay empty. Prod drew an hourly band worth about $9.5/h
// across those hours instead.
//
// Mutation: drop the cap and the two points spread over all nine hours -- every
// idle bucket picks up implied burn and the two capped buckets lose it.
func TestCoverageGap_idleHoursGetNoImpliedBurn(t *testing.T) {
	samples, minutes, idleFrom := idleThenBurst(9*time.Hour, 2, 2000)
	since, until := idleFrom, idleFrom.Add(9*time.Hour+10*time.Minute)

	gap := computeCoverageGapBuckets(samples, one(minutes), since, until, &coverageBucketing{granularity: "hour", loc: time.UTC})

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "k", gap.Accounts[0].KUsdPerPct, 1000, 1e-9)
	approx(t, "implied", gap.ImpliedUSD, 2000, 1e-6)

	// The cap ends at the jump reading (10:05) and reaches back 30 minutes, so
	// 25 of its 30 minutes fall in the 09 bucket and 5 in the 10 bucket.
	want := map[string]float64{
		idleFrom.Add(8 * time.Hour).Format("2006-01-02T15:04:05"): 2000 * 25.0 / 30.0,
		idleFrom.Add(9 * time.Hour).Format("2006-01-02T15:04:05"): 2000 * 5.0 / 30.0,
	}
	for _, b := range gap.Buckets {
		approx(t, b.Date+" implied", b.ImpliedUSD, want[b.Date], 1e-6)
	}
	if len(gap.Buckets) == 0 {
		t.Fatal("no buckets")
	}
}

// The cap must not defeat what the spread is for. The percentage is an integer,
// so one five-minute reading shows a whole point that actually accumulated over
// the minutes behind it; a cap shorter than a few reading intervals would pile
// each point back into the single minute that reported it.
//
// Mutation: shorten the cap below one reading interval and the burn collapses
// into three minute buckets instead of fifteen.
func TestCoverageGap_shortIntervalsStillSpreadEvenly(t *testing.T) {
	samples, minutes := weeklySeries(300, 5*time.Minute, 0.1, 100)
	since := covT0.Add(24 * time.Hour)
	until := since.Add(15 * time.Minute)

	gap := computeCoverageGapBuckets(samples, one(minutes), since, until, &coverageBucketing{granularity: "minute", loc: time.UTC})

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	// Three readings land inside, each worth 0.1% at k=1000, spread over its own
	// five minutes: every one of the fifteen minutes carries the same share.
	if len(gap.Buckets) != 15 {
		t.Fatalf("buckets = %d, want 15", len(gap.Buckets))
	}
	for _, b := range gap.Buckets {
		approx(t, b.Date+" implied", b.ImpliedUSD, 20, 1e-6)
	}
}

// The reset branch spreads the new window's reading from the rollover instant,
// and that stretch is unbounded for the same reason: a window that rolls over
// while nobody is working leaves hours between the rollover and the reading
// that first shows the new burn. The cap applies there too.
//
// Mutation: drop the cap and the three points spread from 03:00 instead of
// 05:30, so the 03 and 04 buckets pick up implied burn they should not have.
func TestCoverageGap_resetSpreadIsAlsoCapped(t *testing.T) {
	samples, minutes := weeklySeries(300, 5*time.Minute, 0.1, 100)
	prev := samples[len(samples)-1]
	rollover := prev.SampledAt.Add(2 * time.Hour)
	later := covT0.Add(14 * 24 * time.Hour)
	samples[len(samples)-1].ResetsAt = &rollover
	samples = append(samples, coverageSample{
		LoginEmail: prev.LoginEmail, AccountID: prev.AccountID, WindowKey: prev.WindowKey, Plan: prev.Plan,
		SampledAt: prev.SampledAt.Add(5 * time.Hour), UsedPct: 3, ResetsAt: &later,
	})
	since, until := prev.SampledAt, prev.SampledAt.Add(5*time.Hour)

	gap := computeCoverageGapBuckets(samples, one(minutes), since, until, &coverageBucketing{granularity: "hour", loc: time.UTC})

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	approx(t, "censored seconds", gap.Accounts[0].CensoredSeconds, 2*3600, 1e-9)
	approx(t, "implied", gap.ImpliedUSD, 3000, 1e-6)
	// The whole 3% lands in the last hour before the reading, not from 03:00 on.
	wantHour := prev.SampledAt.Add(4 * time.Hour).Format("2006-01-02T15:04:05")
	for _, b := range gap.Buckets {
		want := 0.0
		if b.Date == wantHour {
			want = 3000
		}
		approx(t, b.Date+" implied", b.ImpliedUSD, want, 1e-6)
	}
}

// The account total and the bucket series are two views of the same spread, and
// the cap has to move both or the chart stops summing to the headline.
//
// Mutation: cap only the account total and the bucket sum stays at 2000 while
// the buckets themselves are drawn from the uncapped stretch.
func TestCoverageGap_bucketsSumToTheAccountTotal(t *testing.T) {
	samples, minutes, idleFrom := idleThenBurst(9*time.Hour, 2, 2000)
	since, until := idleFrom, idleFrom.Add(9*time.Hour+10*time.Minute)

	gap := computeCoverageGapBuckets(samples, one(minutes), since, until, &coverageBucketing{granularity: "hour", loc: time.UTC})

	if !gap.Eligible {
		t.Fatalf("not eligible: %s (%+v)", gap.Reason, gap.Accounts)
	}
	var sum float64
	for _, b := range gap.Buckets {
		sum += b.ImpliedUSD
	}
	approx(t, "bucket sum", sum, gap.Accounts[0].ImpliedUSD, 1e-6)
	approx(t, "account total", gap.Accounts[0].ImpliedUSD, gap.ImpliedUSD, 1e-9)
}
