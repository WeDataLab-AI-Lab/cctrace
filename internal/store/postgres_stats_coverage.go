package store

import (
	"context"
	"log"
	"strings"
	"time"
)

type CoverageGapFilter struct {
	Since, Until time.Time
	// LoginEmail restricts the population to one billing login. Empty = every
	// Claude account with observed readings.
	LoginEmail string
	// Granularity and Timezone ask for the per-bucket series on the same
	// buckets the trend chart draws. Empty Granularity = range total only.
	Granularity string
	Timezone    string
	// HeadlineSince and HeadlineUntil ask for a second, wider scope in the same
	// response. The chart needs both at once and they do not coincide: the
	// sentence widens to a trailing week when the plotted range is under a day,
	// while the overlay stays on the plotted range. Asking separately meant two
	// requests, each re-loading the same 28 days of fit data -- and the second
	// could not start until the first returned, because it was gated on the
	// first's eligibility. Zero means the two scopes are the same.
	HeadlineSince, HeadlineUntil time.Time
}

// wantsHeadlineScope reports whether a second, differently-ranged answer was
// asked for alongside the bucket one.
func (f CoverageGapFilter) wantsHeadlineScope() bool {
	return !f.HeadlineSince.IsZero() && !f.HeadlineUntil.IsZero() &&
		(!f.HeadlineSince.Equal(f.Since) || !f.HeadlineUntil.Equal(f.Until))
}

// CoverageGapStats loads both sides on the same population and hands them to
// computeCoverageGap.
//
// The population is Claude accounts (billing_provider = 'anthropic') keyed by
// login_email -- the identity every unified_events arm carries, where account_id
// is only filled for Codex -- with observed readings only, since an inferred
// reading was credited to an account across an observation gap and would put a
// guess under the fit. Both sides apply the same exclusions: visible_events
// already does, and the quota query repeats the two anti-joins the quota chart
// uses, so an excluded account leaves the numerator and the denominator together.
//
// Compatible-provider traffic ('other') burns no subscription and is dropped
// from the measured side; counting it would make the ratio exceed one.
//
// The measured side is cost_usd, and so is the fitted factor. It used to be a
// token total including cache reads and writes, on the reasoning that the
// window's burn is dominated by cache traffic and the factor should be fitted
// on the same total the meter reacts to. #539 measured that reasoning and it
// did not hold: the meter prices models apart and a token total does not, so
// the token-unit factor moved with the model mix. cost_usd already carries the
// pricing, including cache reads and writes, and fitted with a smaller
// out-of-sample spread in 8 of 9 cells. chart_tokens is still loaded, but only
// to place the unknown band on the chart's token axis.
// coverageSlowThreshold is when a request is worth a line in the log.
//
// Measured on prod before any of this was instrumented: the measured-usage query
// alone takes 966ms over its unconditional 28-day window, and the chart issues
// two of them in series. 250ms is under the fast cases (a 3-hour report range
// measures 22.5ms) and over the noise, so the log names the slow shape without
// narrating the healthy one.
const coverageSlowThreshold = 250 * time.Millisecond

func (s *PgStore) CoverageGapStats(ctx context.Context, f CoverageGapFilter) (*CoverageGap, error) {
	// Split three ways because the three move independently: the readings query
	// is bounded by how often the meter is polled, the measured query by the
	// range and the shape of visible_events, and the computation by how many
	// minutes were loaded. Without the split, an improvement in one is
	// indistinguishable from a regression in another.
	var readingsTook, measuredTook, computeTook time.Duration
	started := time.Now()
	defer func() {
		total := time.Since(started)
		if total < coverageSlowThreshold {
			return
		}
		log.Printf("[coverage] %v total (readings %v, measured %v, compute %v) range=%v..%v granularity=%q",
			total.Round(time.Millisecond), readingsTook.Round(time.Millisecond),
			measuredTook.Round(time.Millisecond), computeTook.Round(time.Millisecond),
			f.Since.Format(time.RFC3339), f.Until.Format(time.RFC3339), f.Granularity)
	}()

	email := strings.ToLower(strings.TrimSpace(f.LoginEmail))
	// One load covering both scopes. The fit window already reaches back 28 days,
	// so a headline scope that starts earlier than the bucket one is normally
	// already inside it -- but the bound is taken explicitly rather than assumed,
	// because "normally" is how a range silently loses its first interval.
	loadFrom, loadUntil := f.Since, f.Until
	if f.wantsHeadlineScope() {
		if f.HeadlineSince.Before(loadFrom) {
			loadFrom = f.HeadlineSince
		}
		if f.HeadlineUntil.After(loadUntil) {
			loadUntil = f.HeadlineUntil
		}
	}
	fitFrom := loadUntil.Add(-coverageFitWindow)
	if loadFrom.Before(fitFrom) {
		fitFrom = loadFrom
	}

	var timings coverageLoadTimings
	samples, measured, err := s.loadCoverageInputs(ctx, email, fitFrom, loadUntil, &timings)
	if err != nil {
		return nil, err
	}
	readingsTook, measuredTook = timings.readings, timings.measured

	var bucketing *coverageBucketing
	if f.Granularity != "" {
		loc, err := time.LoadLocation(f.Timezone)
		if err != nil || f.Timezone == "" {
			loc = time.UTC
		}
		bucketing = &coverageBucketing{granularity: f.Granularity, loc: loc}
	}
	gap := computeCoverageGapBuckets(samples, measured, f.Since, f.Until, bucketing)
	// The second scope is computed from the SAME loaded slices. That is the whole
	// point: computeCoverageGapBuckets is pure over its inputs, so the expensive
	// part is paid once no matter how many scopes are reported.
	if f.wantsHeadlineScope() {
		headline := computeCoverageGapBuckets(samples, measured, f.HeadlineSince, f.HeadlineUntil, nil)
		gap.Headline = headline
	}
	return gap, nil
}

// loadCoverageInputs reads both sides for one population and window.
//
// Extracted so the fit-drift replay can load once and re-derive k at many
// instants: reloading per replayed hour would cost 336 times what the experiment
// needs, and the experiment is what decides whether the fit may be cached.
// coverageLoadTimings splits the load so the caller can attribute a slow request
// to the side that caused it. Optional: the replay harness passes nil.
type coverageLoadTimings struct{ readings, measured time.Duration }

func (s *PgStore) loadCoverageInputs(ctx context.Context, email string, from, to time.Time, timings *coverageLoadTimings) ([]coverageSample, map[string][]coverageMinute, error) {
	started := time.Now()
	emailClause := ""
	args := []any{from, to}
	if email != "" {
		emailClause = " AND lower(login_email) = $3"
		args = append(args, email)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT lower(login_email), account_id, window_key, plan, sampled_at, used_pct, resets_at
		FROM quota_samples
		WHERE billing_provider = 'anthropic'
		  AND attribution = 'observed'
		  AND window_key IN ('session', 'weekly_all')
		  AND login_email <> ''
		  AND sampled_at >= $1 AND sampled_at <= $2`+emailClause+`
		  AND `+excludedAccountPredicateSQL("quota_samples")+`
		ORDER BY 1, 3, 5`, args...)
	if err != nil {
		return nil, nil, err
	}
	var samples []coverageSample
	for rows.Next() {
		var c coverageSample
		if err := rows.Scan(&c.LoginEmail, &c.AccountID, &c.WindowKey, &c.Plan, &c.SampledAt, &c.UsedPct, &c.ResetsAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		samples = append(samples, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// (timing is the caller's)

	if timings != nil {
		timings.readings = time.Since(started)
	}
	measuredStarted := time.Now()
	// The pre-aggregated table when it is built, raw events until then. The
	// aggregate is the same GROUP BY, done once on a schedule instead of on every
	// request: measured on a prod snapshot, 757ms of aggregation becomes an 8ms
	// range read.
	//
	// The two account-level exclusions are applied HERE rather than baked into the
	// table, so an account excluded today leaves the measured side at the same
	// instant it leaves the readings side. The table is small enough (14,366 rows
	// for 28 days) that the anti-joins cost nothing.
	measuredSQL := `
		SELECT login_email, minute, chart_tokens, cost_usd
		FROM coverage_measured_minutes
		WHERE minute >= $1 AND minute <= $2` + emailClause + `
		  AND NOT EXISTS (
		    SELECT 1 FROM excluded_accounts x
		    WHERE lower(x.login_email) = coverage_measured_minutes.login_email
		  )
		ORDER BY 1, 2`
	if !s.coverageMinutesReady(ctx) {
		measuredSQL = `
		SELECT lower(login_email), date_trunc('minute', ts),
			sum(COALESCE(input_tokens,0) + COALESCE(output_tokens,0))::float8,
			COALESCE(sum(cost_usd),0)::float8
		FROM visible_events
		WHERE billing_provider = 'anthropic'
		  AND login_email <> ''
		  AND ts >= $1 AND ts <= $2` + emailClause + `
		GROUP BY 1, 2
		ORDER BY 1, 2`
	}
	rows, err = s.pool.Query(ctx, measuredSQL, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	measured := map[string][]coverageMinute{}
	for rows.Next() {
		var e string
		var m coverageMinute
		if err := rows.Scan(&e, &m.At, &m.ChartTokens, &m.Cost); err != nil {
			return nil, nil, err
		}
		measured[e] = append(measured[e], m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	if timings != nil {
		timings.measured = time.Since(measuredStarted)
	}
	return samples, measured, nil
}
