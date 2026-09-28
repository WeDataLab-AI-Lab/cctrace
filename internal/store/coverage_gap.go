package store

import (
	"math"
	"sort"
	"time"
)

// Coverage gap: how much of an account's subscription burn cctrace actually saw.
//
// The provider's usage API reports a window's utilization as a percentage and
// nothing else -- no token count, no session, no model. cctrace's own usage
// comes from OTEL/JSONL and covers only the machines a client runs on. The two
// are related by one unknown per account: how many measured DOLLARS one percent
// of the window costs. That factor is fitted here from stretches where
// collection is believed complete, and the ratio of measured to implied dollars
// over the requested range is the coverage.
//
// The unit is dollars rather than tokens because of #539. A token total treats
// a fable token and an opus token as the same claim on the window, and the
// meter plainly does not: measured on prod, one stretch whose fable share rose
// from 14% to 50% realized 4.4M tokens per point against 25.8M in the stretch
// beside it, the same account on the same day with no collection gap. Fitting
// on cost_usd, which already prices the models apart, gave the smaller
// out-of-sample spread in 8 of 9 account/window/threshold cells and roughly
// halved it. It is an improvement, not a fix: on the worst stretch measured, a
// cost-unit factor still under-predicts the meter's movement by 4.8x where a
// token-unit factor under-predicts it by 11.2x, and that residual 4.8x has no
// established cause. Candidates not tested: the long-context [1m] premium,
// per-request minimum billing, tool and web-search surcharges, and subagent
// traffic that never reaches OTEL. The 4.8x itself is deliberately not judged
// among them -- the quota sample behind it is nine days from two accounts, too
// thin to tell the candidates apart.
//
// Readings are sparse. They arrive only while a sync daemon is awake, so hours
// without one are normal. The seven-day meter tolerates that: it resets once a
// week, so two readings hours apart still bracket exactly the burn between
// them, and how that burn was spread inside the gap does not matter to a range
// total. The five-hour meter does not tolerate it -- a gap longer than the
// window hides at least one reset, and what was burned before it is gone --
// so it is only a fallback for accounts whose weekly meter was never reported.
//
// The design rejects a fixed factor on purpose. A previous attempt to estimate
// a coverage gap with one (docs/design/design-gjc-omo-usage-monitoring.md §7.2)
// measured the gap moving from -2.5% to -20% within half a day on one project.
// The factor is per account and plan, refitted over a trailing window, and the
// computation refuses rather than guesses when the fit is not stable.

const (
	// CoverageKMethod names the estimator in the response so a reader can tell
	// which rule produced the number.
	CoverageKMethod = "weighted-mean-usd/28d"
	// CoverageMinRange is the shortest range the ratio is answered for; below
	// it the integer meter has barely moved. Shorter chart ranges ask for the
	// trailing week instead.
	CoverageMinRange = 24 * time.Hour

	coverageFitWindow   = 28 * 24 * time.Hour
	coverageMinFitDelta = 2.0  // a fit segment accumulates readings until it has moved this much
	coverageMaxFitPct   = 95.0 // near the cap the meter stops moving
	coverageMinFitN     = 12
	// coverageMaxSpread bounds Q95/Q75 of the per-segment ratio. Only the top
	// of the distribution is judged: segments with unmeasured usage sit below
	// it by construction, and their spread is the coverage gap itself, not
	// evidence that one factor fails. Seen on prod: an account whose lower
	// segments ran at a tenth of its upper ones was exactly the account with
	// the largest blind spot, and a Q75/Q25 bound refused it for that reason.
	//
	// Decision (#539, 2026-09-21): don't reject the estimate -- raise this
	// bound and keep publishing the Q95/Q75 statistic rather than dropping it.
	// Measured on prod over the nine days of readings that exist, a bound of 3
	// to 5 refuses both accounts 100% of the time: nothing in that range
	// accepts either account, so it would not be discriminating a trustworthy
	// fit from an untrustworthy one, only refusing everyone. 8 and above
	// refuses neither, and there is no evidence in between to justify a value
	// closer to the old one. The estimate compensates by saying it can be
	// wrong -- the dashboard states the estimate can overstate the true gap by
	// more than 3x -- rather than by refusing to show a number at all.
	coverageMaxSpread = 8.0
	// coverageResetSlack is how far resets_at must move to count as a new
	// window. The API recomputes the instant on every call and it drifts by
	// microseconds between readings of the same window; a real rollover moves
	// it by the window length, hours at the least.
	coverageResetSlack = time.Minute
	// coverageMaxSpreadBack is how far behind a reading its increase may be
	// painted. The spread exists only to undo integer quantization -- the
	// percentage is a whole number, so one reading shows a whole point that
	// really accumulated over the minutes behind it -- and without a bound that
	// few-minute correction was being applied to stretches of hours. Measured on
	// prod (28 days, anthropic, observed): between two readings that both moved
	// while burning was already under way, the gap is 310s at the median, 389s at
	// p90 and 621s (session) / 1085s (weekly_all) at p99. Thirty minutes clears
	// the worst of that by better than 1.6x, so a point that genuinely took
	// several readings to accumulate is still spread rather than piled onto the
	// minute that reported it.
	//
	// The other side of the choice: half the percentage points the weekly meter
	// reported over those 28 days arrived after a gap longer than 30 minutes, the
	// longest of them 3.6 days. Capping keeps 23% of the painted time-area, which
	// is the idle pollution this removes -- an account that stopped for nine hours
	// and then burned two points was drawing a band across all nine.
	//
	// The range total is nearly unchanged by design: an interval that lies wholly
	// inside the reported range contributes k*deltaPct whatever its span, so only
	// intervals straddling a range edge move. Replayed over the last seven 24-hour
	// ranges the account-wide implied burn shifted by at most a few percent, in
	// both directions.
	coverageMaxSpreadBack = 30 * time.Minute
)

// coverageSample is one quota reading, already restricted by the loader to the
// provider, attribution and exclusion set the computation is defined on.
type coverageSample struct {
	LoginEmail string
	AccountID  string
	WindowKey  string
	Plan       string
	SampledAt  time.Time
	UsedPct    float64
	ResetsAt   *time.Time
}

// coverageMinute is the measured usage for one minute of one account, in the
// two units the trend chart draws. Cost carries the fit as well as the chart's
// cost mode; ChartTokens exists only to put the unknown band on the token axis.
//
// The total token count the meter reacts to (input + output + cache) used to be
// carried here as a third field and was what the factor was fitted on. #539
// removed it: nothing reads it now that the factor is priced.
type coverageMinute struct {
	At          time.Time
	ChartTokens float64 // input + output, the chart's token mode
	Cost        float64 // cost_usd, the fit's unit and the chart's cost mode
}

// CoverageBucket is one chart bucket's share of the unknown usage, in the two
// units the chart draws, so the segment can sit on either axis. The two sides
// the gap is taken between are dollars, the unit the factor is fitted in;
// UnknownTokens is that dollar gap converted for the token axis.
type CoverageBucket struct {
	Date           string  `json:"date"`
	ImpliedUSD     float64 `json:"implied_usd"`
	MeasuredUSD    float64 `json:"measured_usd"`
	UnknownTokens  float64 `json:"unknown_tokens"`
	UnknownCostUSD float64 `json:"unknown_cost_usd"`
}

// coverageBucketing describes how to cut the range into chart buckets. Nil
// means the caller wants the range total only.
type coverageBucketing struct {
	granularity string
	loc         *time.Location
}

// bucketAcc is one bucket's running totals. implied and measured are dollars --
// the gap is taken between them directly -- and chart is the input+output token
// total the token axis needs.
type bucketAcc struct {
	implied, measured, chart float64
}

type CoverageGapAccount struct {
	LoginEmail   string  `json:"login_email"`
	AccountID    string  `json:"account_id"`
	Plan         string  `json:"plan"`
	WindowKey    string  `json:"window_key"`
	KUsdPerPct   float64 `json:"k_usd_per_pct"`
	FitIntervals int     `json:"fit_intervals"`
	MeasuredUSD  float64 `json:"measured_usd"`
	ImpliedUSD   float64 `json:"implied_usd"`
	// SampleCoverage is the share of the range that lies between this account's
	// first and last reading -- the span both sides are computed on.
	SampleCoverage  float64 `json:"sample_coverage"`
	CensoredSeconds float64 `json:"censored_seconds"`
	// Unfitted names why this account contributes to neither side. Empty when it
	// does.
	Unfitted string `json:"unfitted,omitempty"`
}

type CoverageGap struct {
	Eligible         bool                 `json:"eligible"`
	Reason           string               `json:"reason,omitempty"`
	MeasuredUSD      float64              `json:"measured_usd"`
	ImpliedUSD       float64              `json:"implied_usd"`
	CoverageRatio    float64              `json:"coverage_ratio"`
	SampleCoverage   float64              `json:"sample_coverage"`
	CensoredFraction float64              `json:"censored_fraction"`
	Accounts         []CoverageGapAccount `json:"accounts"`
	// Headline is the same computation over a second, wider scope, present only
	// when one was asked for. The top level stays the bucket scope so a client
	// that does not know about this field sees exactly what it saw before.
	Headline *CoverageGap `json:"headline,omitempty"`
	Unfitted []string     `json:"unfitted"`
	KMethod  string       `json:"k_method"`
	// Buckets is present only when bucketing was asked for.
	Buckets []CoverageBucket `json:"buckets,omitempty"`
}

type coverageInterval struct {
	from, to time.Time
	reset    bool
	// boundary is where the window rolled over inside a reset interval: burn
	// before it is unknown, burn after it is toPct exactly. Equal to `to` when
	// the rollover instant is unknown, censoring the whole interval.
	boundary time.Time
	deltaPct float64
	fromPct  float64
	toPct    float64
	plan     string
	// measuredUSD is cost_usd summed over the minutes inside the interval: the
	// numerator the factor is fitted on.
	measuredUSD float64
}

// computeCoverageGap is the pure part. samples may hold several accounts and
// window kinds; measured is per-minute usage keyed by login_email. An
// email with measured usage but no readings is named as unfitted rather than
// counted on one side only.
func computeCoverageGap(samples []coverageSample, measured map[string][]coverageMinute, since, until time.Time) *CoverageGap {
	return computeCoverageGapBuckets(samples, measured, since, until, nil)
}

func computeCoverageGapBuckets(samples []coverageSample, measured map[string][]coverageMinute, since, until time.Time, b *coverageBucketing) *CoverageGap {
	out := &CoverageGap{KMethod: CoverageKMethod, Accounts: []CoverageGapAccount{}, Unfitted: []string{}}
	rangeSeconds := until.Sub(since).Seconds()
	if rangeSeconds <= 0 {
		out.Reason = "empty range"
		return out
	}

	byAccount := map[string][]coverageSample{}
	var order []string
	for _, s := range samples {
		if _, ok := byAccount[s.LoginEmail]; !ok {
			order = append(order, s.LoginEmail)
		}
		byAccount[s.LoginEmail] = append(byAccount[s.LoginEmail], s)
	}
	sort.Strings(order)

	var fitted int
	var coveredSeconds, censoredSeconds float64
	buckets := map[string]*bucketAcc{}
	var chartSum float64
	for _, email := range order {
		acct, perBucket := fitAccount(byAccount[email], measured[email], since, until, b)
		out.Accounts = append(out.Accounts, acct)
		if acct.Unfitted != "" {
			out.Unfitted = append(out.Unfitted, email)
			continue
		}
		fitted++
		out.MeasuredUSD += acct.MeasuredUSD
		out.ImpliedUSD += acct.ImpliedUSD
		coveredSeconds += acct.SampleCoverage * rangeSeconds
		censoredSeconds += acct.CensoredSeconds
		for key, v := range perBucket {
			acc := buckets[key]
			if acc == nil {
				acc = &bucketAcc{}
				buckets[key] = acc
			}
			acc.implied += v.implied
			acc.measured += v.measured
			acc.chart += v.chart
			chartSum += v.chart
		}
	}
	for email := range measured {
		if _, ok := byAccount[email]; ok {
			continue
		}
		out.Accounts = append(out.Accounts, CoverageGapAccount{LoginEmail: email, Unfitted: "no quota readings"})
		out.Unfitted = append(out.Unfitted, email)
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].LoginEmail < out.Accounts[j].LoginEmail })
	sort.Strings(out.Unfitted)

	if fitted == 0 {
		out.Reason = "no account with a stable fit"
		return out
	}
	out.SampleCoverage = coveredSeconds / (float64(fitted) * rangeSeconds)
	out.CensoredFraction = censoredSeconds / (float64(fitted) * rangeSeconds)
	if out.ImpliedUSD <= 0 {
		out.Reason = "no subscription burn in the range"
		return out
	}
	out.CoverageRatio = out.MeasuredUSD / out.ImpliedUSD
	out.Eligible = true
	if b != nil {
		// The range-wide chart-tokens-per-dollar rate, guarded because the
		// divisor is now cost rather than a token count: traffic that carries
		// tokens but no price would make it zero, and the token axis would
		// inherit an infinity instead of an empty band.
		var chartPerUSD float64
		if out.MeasuredUSD > 0 {
			chartPerUSD = chartSum / out.MeasuredUSD
		}
		out.Buckets = unknownBuckets(buckets, chartPerUSD)
	}
	return out
}

// unknownBuckets converts each bucket's gap into the chart's units.
//
// The gap is taken in dollars, so the cost axis needs no conversion at all --
// the unknown cost IS implied minus measured. The token axis does: where the
// bucket has measured usage, the unknown part is the same share of the bucket's
// own chart tokens that it is of the bucket's dollars, so the unseen usage
// wears that bucket's own model mix. Where nothing was measured, the
// range-wide chart-tokens-per-dollar rate stands in.
func unknownBuckets(buckets map[string]*bucketAcc, chartPerUSD float64) []CoverageBucket {
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]CoverageBucket, 0, len(keys))
	for _, k := range keys {
		acc := buckets[k]
		gap := math.Max(0, acc.implied-acc.measured)
		cb := CoverageBucket{Date: k, ImpliedUSD: acc.implied, MeasuredUSD: acc.measured, UnknownCostUSD: gap}
		if acc.measured > 0 {
			cb.UnknownTokens = acc.chart * (gap / acc.measured)
		} else {
			cb.UnknownTokens = gap * chartPerUSD
		}
		out = append(out, cb)
	}
	return out
}

// pickWindow chooses one meter per account: the weekly one, because it survives
// sparse readings (see the file comment), and the five-hour one only when the
// weekly was never reported. weekly_scoped is never used: it is the same seven
// days narrowed to one model, and would double count against weekly_all.
func pickWindow(samples []coverageSample) (string, []coverageSample) {
	byKey := map[string][]coverageSample{}
	for _, s := range samples {
		byKey[s.WindowKey] = append(byKey[s.WindowKey], s)
	}
	for _, key := range []string{"weekly_all", "session"} {
		if rows := byKey[key]; len(rows) >= 2 {
			sort.Slice(rows, func(i, j int) bool { return rows[i].SampledAt.Before(rows[j].SampledAt) })
			return key, rows
		}
	}
	return "", nil
}

// coverageFit is one account's fitted constant and the identity that goes with
// it -- everything derived from the 28-day window rather than from the reported
// range.
//
// Separated from the reporting pass because the two have different lifetimes:
// the fit is a 28-day mean that moves slowly, while the report is whatever range
// the reader is looking at. Keeping them in one function meant every request for
// a three-hour range paid for 28 days of fitting, and it left no way to measure
// how far the fit actually drifts -- which is the question that decides whether
// it may be reused at all.
type coverageFit struct {
	AccountID    string
	Plan         string
	WindowKey    string
	KUsdPerPct   float64
	FitIntervals int
	// Unfitted names why this account has no usable constant. Empty when it has one.
	Unfitted string
	// Rows is the chosen window's readings, already selected by pickWindow.
	Rows []coverageSample
	// Intervals is segment()'s output over those readings. Carried rather than
	// recomputed: it is a pure function of Rows and the measured minutes, and the
	// reporting pass needs exactly the same segmentation the fit was derived from.
	Intervals []coverageInterval
}

// fitAccountK derives the constant. `until` bounds the 28-day fit window; the
// reported range plays no part.
//
// minutes is sorted in place, which the caller relies on: the reporting pass
// walks it in order.
func fitAccountK(samples []coverageSample, minutes []coverageMinute, until time.Time) coverageFit {
	fit := coverageFit{}
	key, rows := pickWindow(samples)
	if key == "" {
		fit.Unfitted = "fewer than two readings of any usable window"
		return fit
	}
	last := rows[len(rows)-1]
	fit.AccountID, fit.Plan, fit.WindowKey, fit.Rows = last.AccountID, last.Plan, key, rows

	sort.Slice(minutes, func(i, j int) bool { return minutes[i].At.Before(minutes[j].At) })
	intervals := segment(rows, minutes)
	fit.Intervals = intervals

	segments := fitSegments(intervals, fit.Plan, until.Add(-coverageFitWindow), until)
	fit.FitIntervals = len(segments)
	if len(segments) < coverageMinFitN {
		fit.Unfitted = "fewer than 12 fully measured fit segments"
		return fit
	}
	ratios := make([]float64, 0, len(segments))
	var sumMeasured, sumDelta float64
	for _, seg := range segments {
		ratios = append(ratios, seg.measuredUSD/seg.deltaPct)
		sumMeasured += seg.measuredUSD
		sumDelta += seg.deltaPct
	}
	sort.Float64s(ratios)
	if q75 := quantile(ratios, 0.75); q75 <= 0 || quantile(ratios, 0.95)/q75 > coverageMaxSpread {
		fit.Unfitted = "fully measured segments disagree on dollars per percent"
		return fit
	}
	if sumDelta <= 0 {
		fit.Unfitted = "fit segments moved the meter nowhere"
		return fit
	}
	// The delta-weighted mean, not an upper quantile of the per-segment ratios.
	//
	// Q90 was chosen so that segments with unmeasured usage -- which sit BELOW
	// the true factor by construction -- could not drag the factor down and
	// explain the blind spot away. Measured on prod for #539 that reasoning cost
	// more than it bought: q90 landed at 1.77 times the break-even factor on the
	// busier of the two accounts, and the seven-day coverage it produced was
	// 66.3% when the same segments imply 103.4% under this mean. The median is
	// not the alternative -- it undershoots to 127.5%.
	//
	// This does NOT remove the $300 unknown spike #539 was filed for. Over the
	// thirty minutes that produced it, $158.67 was actually measured. q90 implies
	// $3,311.67 there, 20.9 times that; this mean implies $1,719.67, 10.8 times;
	// the break-even factor implies $1,818.24, 11.5 times. The per-minute peak
	// falls from $286 to $151 and stays a spike. It is a LOCAL disagreement
	// between the meter and what was measured, so no choice of a global central
	// statistic can remove it -- moving the centre only halves it.
	//
	// What is given up is stated plainly: sum(measured)/sum(delta) over the fit
	// window makes measured and implied equal over that window by construction,
	// so a blind spot that is STATIONARY is absorbed into the factor rather than
	// reported. What the ratio still answers is how far a reported range departs
	// from the 28-day average. A segment with nothing measured in it is still
	// dropped by fitSegments rather than averaged in at zero, so a wholly dark
	// stretch keeps lowering the ratio; a segment that is merely dim no longer
	// does.
	fit.KUsdPerPct = sumMeasured / sumDelta
	return fit
}

func fitAccount(samples []coverageSample, minutes []coverageMinute, since, until time.Time, b *coverageBucketing) (CoverageGapAccount, map[string]*bucketAcc) {
	return applyFit(fitAccountK(samples, minutes, until), samples, minutes, since, until, b)
}

// applyFit reports one account over [since, until) using an already-derived
// constant.
func applyFit(fit coverageFit, samples []coverageSample, minutes []coverageMinute, since, until time.Time, b *coverageBucketing) (CoverageGapAccount, map[string]*bucketAcc) {
	acct := CoverageGapAccount{
		LoginEmail:   samples[0].LoginEmail,
		AccountID:    fit.AccountID,
		Plan:         fit.Plan,
		WindowKey:    fit.WindowKey,
		KUsdPerPct:   fit.KUsdPerPct,
		FitIntervals: fit.FitIntervals,
		Unfitted:     fit.Unfitted,
	}
	if fit.Unfitted != "" {
		return acct, nil
	}
	rows := fit.Rows
	intervals := fit.Intervals
	last := rows[len(rows)-1]

	// Both sides are computed on the same span: from the first reading to the
	// last, clipped to the range, minus the censored stretches. Implied dollars
	// can only exist between readings, and measured dollars where the burn is
	// unknown -- outside that span, or before a rollover whose tail was never
	// read -- would be counted against nothing and inflate the ratio.
	spanFrom, spanTo := clip(rows[0].SampledAt, last.SampledAt, since, until)
	rangeSeconds := until.Sub(since).Seconds()
	if !spanTo.After(spanFrom) {
		acct.Unfitted = "no readings inside the range"
		return acct, nil
	}
	perBucket := map[string]*bucketAcc{}
	bucketOf := func(key string) *bucketAcc {
		acc := perBucket[key]
		if acc == nil {
			acc = &bucketAcc{}
			perBucket[key] = acc
		}
		return acc
	}
	// Burn is spread uniformly from the last instant the meter moved to the
	// reading that moved it. The percentage is an integer, so a five-minute
	// reading either shows a whole point or nothing; attributing that point to
	// its own five minutes would pile an hour's burn into one bucket.
	//
	// The stretch is bounded by coverageMaxSpreadBack. The meter standing still
	// for hours is evidence that nothing was burned in those hours, not evidence
	// that the next increase belongs to them, and both callers below hand this
	// function a `from` that can sit arbitrarily far back: the plain branch keeps
	// runFrom at the last reading that moved, and the reset branch starts at the
	// rollover instant, which a daemon running through a quiet night will not
	// follow with a moving reading for hours. Clipping inside spread rather than
	// at the call sites is what keeps the two consistent, and keeps the account
	// total and the bucket series -- computed here from the same `from` -- in
	// agreement.
	spread := func(from, to time.Time, deltaPct float64) {
		if earliest := to.Add(-coverageMaxSpreadBack); earliest.After(from) {
			from = earliest
		}
		overlap := overlapSeconds(from, to, since, until)
		if overlap <= 0 || !to.After(from) {
			return
		}
		usd := acct.KUsdPerPct * deltaPct * (overlap / to.Sub(from).Seconds())
		acct.ImpliedUSD += usd
		if b == nil {
			return
		}
		cFrom, cTo := clip(from, to, since, until)
		for start := bucketStart(cFrom, b); start.Before(cTo); start = nextBucket(start, b) {
			part := overlapSeconds(start, nextBucket(start, b), cFrom, cTo)
			bucketOf(bucketKey(start, b)).implied += usd * part / cTo.Sub(cFrom).Seconds()
		}
	}
	var censored [][2]time.Time
	runFrom := rows[0].SampledAt
	for _, iv := range intervals {
		if iv.reset {
			cFrom, cTo := clip(iv.from, iv.boundary, since, until)
			if cTo.After(cFrom) {
				acct.CensoredSeconds += cTo.Sub(cFrom).Seconds()
				censored = append(censored, [2]time.Time{cFrom, cTo})
			}
			spread(iv.boundary, iv.to, iv.deltaPct)
			runFrom = iv.to
			continue
		}
		if iv.deltaPct > 0 {
			spread(runFrom, iv.to, iv.deltaPct)
			runFrom = iv.to
		}
	}
	for _, m := range minutes {
		if m.At.Before(spanFrom) || !m.At.Before(spanTo) || inAny(m.At, censored) {
			continue
		}
		acct.MeasuredUSD += m.Cost
		if b != nil {
			acc := bucketOf(bucketKey(m.At, b))
			acc.measured += m.Cost
			acc.chart += m.ChartTokens
		}
	}
	acct.SampleCoverage = (spanTo.Sub(spanFrom).Seconds() - acct.CensoredSeconds) / rangeSeconds
	return acct, perBucket
}

// bucketStart, nextBucket and bucketKey reproduce the trend queries'
// date_trunc(granularity, ts AT TIME ZONE tz) and its to_char format, so the
// unknown series lands on the same x values the chart already has. A key that
// differed by one character would render as an all-zero band with no error.
func bucketStart(t time.Time, b *coverageBucketing) time.Time {
	l := t.In(b.loc)
	y, mo, d := l.Date()
	switch b.granularity {
	case "minute":
		return time.Date(y, mo, d, l.Hour(), l.Minute(), 0, 0, b.loc)
	case "hour":
		return time.Date(y, mo, d, l.Hour(), 0, 0, 0, b.loc)
	case "week":
		// date_trunc('week') is ISO: Monday.
		back := (int(l.Weekday()) + 6) % 7
		return time.Date(y, mo, d-back, 0, 0, 0, 0, b.loc)
	case "month":
		return time.Date(y, mo, 1, 0, 0, 0, 0, b.loc)
	default:
		return time.Date(y, mo, d, 0, 0, 0, 0, b.loc)
	}
}

func nextBucket(start time.Time, b *coverageBucketing) time.Time {
	switch b.granularity {
	case "minute":
		return start.Add(time.Minute)
	case "hour":
		return start.Add(time.Hour)
	case "week":
		return start.AddDate(0, 0, 7)
	case "month":
		return start.AddDate(0, 1, 0)
	default:
		return start.AddDate(0, 0, 1)
	}
}

func bucketKey(t time.Time, b *coverageBucketing) string {
	start := bucketStart(t, b)
	if b.granularity == "minute" || b.granularity == "hour" {
		return start.Format("2006-01-02T15:04:05")
	}
	return start.Format("2006-01-02")
}

func inAny(at time.Time, ranges [][2]time.Time) bool {
	for _, r := range ranges {
		if !at.Before(r[0]) && at.Before(r[1]) {
			return true
		}
	}
	return false
}

// coverageFitSegment is one accumulated fit segment: how far the meter moved
// and what was measured against that movement. The two are carried separately
// rather than reduced to a ratio because the estimator weighs segments by their
// movement, which a ratio has already divided away.
type coverageFitSegment struct {
	deltaPct    float64
	measuredUSD float64
}

// fitSegments walks the intervals inside the fit window and accumulates them
// into segments that have moved at least coverageMinFitDelta without a reset, a
// plan change or a reading near the cap. A segment's length in time is
// irrelevant -- that is what lets sparse readings fit at all -- but a segment
// with nothing measured inside it is a dark stretch, not evidence about the
// factor, and is skipped.
func fitSegments(intervals []coverageInterval, plan string, fitFrom, fitTo time.Time) []coverageFitSegment {
	var segments []coverageFitSegment
	var sumDelta, sumMeasured float64
	restart := func() { sumDelta, sumMeasured = 0, 0 }
	for _, iv := range intervals {
		if iv.to.Before(fitFrom) || iv.to.After(fitTo) {
			restart()
			continue
		}
		if iv.reset || iv.plan != plan || iv.fromPct >= coverageMaxFitPct || iv.toPct >= coverageMaxFitPct {
			restart()
			continue
		}
		sumDelta += iv.deltaPct
		sumMeasured += iv.measuredUSD
		if sumDelta >= coverageMinFitDelta {
			if sumMeasured > 0 {
				segments = append(segments, coverageFitSegment{deltaPct: sumDelta, measuredUSD: sumMeasured})
			}
			restart()
		}
	}
	return segments
}

// segment turns consecutive readings into intervals and marks the ones that
// cross a reset.
//
// A reset is signalled by resets_at advancing by more than the jitter it
// carries between readings (coverageResetSlack), or -- when either reading
// lacks resets_at, which the newer API shape omits for the five-hour window --
// by the percentage falling. A fall while resets_at stands still is clock skew between
// two reporting profiles, not a reset: it contributes nothing, and the next
// interval is measured from the segment's high-water mark so the dip is not
// counted twice on the way back up.
//
// The burn across a rollover is censored up to the rollover instant: the
// pre-reset tail is unseen, and assuming the window was exhausted would invent
// usage. After the instant -- the previous reading's resets_at, when it falls
// inside the interval -- the burn is the new reading exactly. Without a known
// instant the whole interval is censored.
func segment(rows []coverageSample, minutes []coverageMinute) []coverageInterval {
	var out []coverageInterval
	highWater := rows[0].UsedPct
	mi := 0
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		dt := cur.SampledAt.Sub(prev.SampledAt)
		if dt <= 0 {
			continue
		}
		iv := coverageInterval{from: prev.SampledAt, to: cur.SampledAt, fromPct: prev.UsedPct, toPct: cur.UsedPct, plan: cur.Plan}
		for mi < len(minutes) && !minutes[mi].At.After(prev.SampledAt) {
			mi++
		}
		for j := mi; j < len(minutes) && !minutes[j].At.After(cur.SampledAt); j++ {
			iv.measuredUSD += minutes[j].Cost
		}

		resetKnown := prev.ResetsAt != nil && cur.ResetsAt != nil
		reset := (resetKnown && cur.ResetsAt.Sub(*prev.ResetsAt) > coverageResetSlack) ||
			(!resetKnown && cur.UsedPct < prev.UsedPct-1e-9)
		if reset {
			iv.reset = true
			iv.deltaPct = cur.UsedPct
			iv.boundary = cur.SampledAt
			if prev.ResetsAt != nil && prev.ResetsAt.After(prev.SampledAt) && prev.ResetsAt.Before(cur.SampledAt) {
				iv.boundary = *prev.ResetsAt
			}
			highWater = cur.UsedPct
		} else {
			iv.deltaPct = math.Max(0, cur.UsedPct-highWater)
			highWater = math.Max(highWater, cur.UsedPct)
		}
		out = append(out, iv)
	}
	return out
}

func clip(aFrom, aTo, bFrom, bTo time.Time) (time.Time, time.Time) {
	if bFrom.After(aFrom) {
		aFrom = bFrom
	}
	if bTo.Before(aTo) {
		aTo = bTo
	}
	return aFrom, aTo
}

func overlapSeconds(aFrom, aTo, bFrom, bTo time.Time) float64 {
	from, to := clip(aFrom, aTo, bFrom, bTo)
	return math.Max(0, to.Sub(from).Seconds())
}

// quantile is nearest-rank over a sorted slice.
func quantile(sorted []float64, q float64) float64 {
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
