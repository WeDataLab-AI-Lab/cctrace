package store

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	QuotaUsageStaleAfter          = 30 * time.Minute
	MaxQuotaUsageRange            = 90 * 24 * time.Hour
	MaxQuotaComparisonRows        = 100000
	MaxQuotaComparisonPoints      = 1000
	MaxQuotaComparisonReplyPoints = 1500
)

func quotaWindowMinutesSQL(alias string) string {
	return `(CASE WHEN ` + alias + `.window_minutes IS NOT NULL THEN ` + alias + `.window_minutes
		WHEN ` + alias + `.window_key ~ '^[0-9]{1,5}$' THEN ` + alias + `.window_key::integer ELSE NULL END)`
}

func (s *PgStore) ListQuotaUsage(ctx context.Context, dashboardUserID int64, from, to, now time.Time) ([]*QuotaUsageAccount, error) {
	if dashboardUserID <= 0 || from.IsZero() || to.IsZero() || !to.After(from) || to.Sub(from) > MaxQuotaUsageRange {
		return nil, ErrQuotaBindingInvalid
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	bindings, err := s.ListQuotaAccountBindings(ctx, dashboardUserID)
	if err != nil {
		return nil, err
	}

	const minutesExpr = `CASE WHEN q.window_minutes IS NOT NULL THEN q.window_minutes
		WHEN q.window_key ~ '^[0-9]{1,5}$' THEN q.window_key::integer ELSE NULL END`
	query := `SELECT DISTINCT ON (q.billing_provider,q.account_id,(` + minutesExpr + `),q.window_key)
		q.billing_provider,q.account_id,q.window_key,q.sampled_at,q.used_pct,q.resets_at,(` + minutesExpr + `) AS window_minutes,
		q.plan,q.scope_label,q.attribution
	FROM quota_owner_samples q
	JOIN quota_account_bindings b ON b.dashboard_user_id=q.dashboard_user_id
		AND b.billing_provider=q.billing_provider AND b.account_id=q.account_id
	WHERE q.dashboard_user_id=$1 AND b.status='approved' AND q.binding_verified_at=b.verified_at
		AND q.sampled_at >= b.verified_at
		AND q.sampled_at >= $2 AND q.sampled_at <= $3
		AND (` + minutesExpr + `) IN (300,10080)
		AND NOT (q.billing_provider='openai' AND (q.window_key LIKE 'codex_bengalfox:%' OR lower(q.scope_label) LIKE '%spark%'))
		AND ` + quotaOwnerExclusionClause("q") + `
	ORDER BY q.billing_provider,q.account_id,(` + minutesExpr + `),q.window_key,q.sampled_at DESC`
	rows, err := s.pool.Query(ctx, query, dashboardUserID, from, to)
	if err != nil {
		return nil, err
	}
	samplesByAccount := make(map[quotaOwnerAccountKey][]*QuotaOwnerSample)
	for rows.Next() {
		var sample QuotaOwnerSample
		var resetsAt pgtype.Timestamptz
		var windowMinutes int
		if err := rows.Scan(&sample.BillingProvider, &sample.AccountID, &sample.WindowKey, &sample.SampledAt, &sample.UsedPct,
			&resetsAt, &windowMinutes, &sample.Plan, &sample.ScopeLabel, &sample.Attribution); err != nil {
			rows.Close()
			return nil, err
		}
		if resetsAt.Valid {
			value := resetsAt.Time
			sample.ResetsAt = &value
		}
		sample.WindowMinutes = &windowMinutes
		key := quotaOwnerAccountKey{provider: sample.BillingProvider, accountID: sample.AccountID}
		samplesByAccount[key] = append(samplesByAccount[key], &sample)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	result := make([]*QuotaUsageAccount, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Status == "revoked" {
			continue
		}
		account := &QuotaUsageAccount{
			BillingProvider: binding.BillingProvider,
			AccountID:       binding.AccountID,
			Label:           quotaAccountLabel(binding.BillingProvider, binding.AccountID),
			BindingStatus:   binding.Status,
			Windows: QuotaUsageWindows{
				FiveHour: unknownQuotaUsageWindow("no_sample"),
				SevenDay: unknownQuotaUsageWindow("no_sample"),
			},
		}
		if binding.Status != "approved" || binding.VerifiedAt == nil {
			account.Windows.FiveHour = unknownQuotaUsageWindow("ownership_unverified")
			account.Windows.SevenDay = unknownQuotaUsageWindow("ownership_unverified")
			result = append(result, account)
			continue
		}
		excluded, err := quotaOwnerAccountExcluded(ctx, s.pool, binding.BillingProvider, binding.AccountID)
		if err != nil {
			return nil, err
		}
		if excluded {
			account.Windows.FiveHour = unknownQuotaUsageWindow("excluded")
			account.Windows.SevenDay = unknownQuotaUsageWindow("excluded")
			result = append(result, account)
			continue
		}
		samples := samplesByAccount[quotaOwnerAccountKey{provider: binding.BillingProvider, accountID: binding.AccountID}]
		fiveSample := currentQuotaOwnerSample(binding.BillingProvider, 300, samples, now)
		weekSample := currentQuotaOwnerSample(binding.BillingProvider, 10080, samples, now)
		account.Windows.FiveHour = quotaUsageWindow(fiveSample, now)
		account.Windows.SevenDay = quotaUsageWindow(weekSample, now)
		if weekSample != nil && (fiveSample == nil || weekSample.SampledAt.After(fiveSample.SampledAt)) {
			account.Plan = weekSample.Plan
		} else if fiveSample != nil {
			account.Plan = fiveSample.Plan
		}
		result = append(result, account)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].BillingProvider == result[j].BillingProvider {
			return result[i].AccountID < result[j].AccountID
		}
		return result[i].BillingProvider < result[j].BillingProvider
	})
	return result, nil
}

func unknownQuotaUsageWindow(reason string) QuotaUsageWindow {
	return QuotaUsageWindow{Attribution: "unknown", State: "unmeasured", Source: "unknown", Reason: reason}
}

func quotaAccountLabel(provider, accountID string) string {
	name := provider
	switch provider {
	case "anthropic":
		name = "Claude"
	case "openai":
		name = "Codex"
	}
	if len(accountID) > 8 {
		accountID = accountID[len(accountID)-8:]
	}
	return name + " · …" + accountID
}

func quotaWindowMinutes(sample *QuotaOwnerSample) int {
	if sample.WindowMinutes != nil && *sample.WindowMinutes > 0 {
		return *sample.WindowMinutes
	}
	n, _ := strconv.Atoi(sample.WindowKey)
	return n
}

func isGeneralQuotaWindow(provider string, sample *QuotaOwnerSample) bool {
	if strings.Contains(sample.WindowKey, ":") {
		return false
	}
	if provider == "anthropic" && sample.WindowKey == "weekly_scoped" {
		return false
	}
	return true
}

func currentQuotaOwnerSample(provider string, windowMinutes int, samples []*QuotaOwnerSample, now time.Time) *QuotaOwnerSample {
	var candidates []*QuotaOwnerSample
	for _, sample := range samples {
		if quotaWindowMinutes(sample) == windowMinutes {
			candidates = append(candidates, sample)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	general := make([]*QuotaOwnerSample, 0, len(candidates))
	for _, sample := range candidates {
		if isGeneralQuotaWindow(provider, sample) {
			general = append(general, sample)
		}
	}
	if len(general) > 0 {
		candidates = general
		preferred := ""
		if provider == "anthropic" && windowMinutes == 300 {
			preferred = "session"
		} else if provider == "anthropic" && windowMinutes == 10080 {
			preferred = "weekly_all"
		} else if provider == "openai" {
			preferred = strconv.Itoa(windowMinutes)
		}
		if preferred != "" {
			for _, sample := range candidates {
				if sample.WindowKey == preferred {
					candidates = []*QuotaOwnerSample{sample}
					break
				}
			}
		}
	}
	var usable []*QuotaOwnerSample
	for _, sample := range candidates {
		if sample.ResetsAt == nil || sample.ResetsAt.After(now) {
			usable = append(usable, sample)
		}
	}
	if len(usable) == 0 {
		// Keep the latest expired reading long enough for quotaUsageWindow to
		// report that reset occurred but has not yet been observed. Returning nil
		// here made that state indistinguishable from never having sampled it.
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].SampledAt.After(candidates[j].SampledAt) })
		return candidates[0]
	}
	if len(general) > 0 {
		sort.Slice(usable, func(i, j int) bool { return usable[i].SampledAt.After(usable[j].SampledAt) })
		return usable[0]
	}
	var best *QuotaOwnerSample
	for _, sample := range usable {
		if now.Sub(sample.SampledAt) > QuotaUsageStaleAfter {
			continue
		}
		if best == nil || sample.UsedPct > best.UsedPct || sample.UsedPct == best.UsedPct && sample.SampledAt.After(best.SampledAt) {
			best = sample
		}
	}
	if best != nil {
		return best
	}
	sort.Slice(usable, func(i, j int) bool { return usable[i].SampledAt.After(usable[j].SampledAt) })
	return usable[0]
}

func quotaUsageWindow(sample *QuotaOwnerSample, now time.Time) QuotaUsageWindow {
	if sample == nil {
		return unknownQuotaUsageWindow("no_current_window_sample")
	}
	if sample.ResetsAt != nil && !sample.ResetsAt.After(now) {
		return unknownQuotaUsageWindow("reset_not_yet_observed")
	}
	value := sample.UsedPct
	resetsAt := sample.ResetsAt
	sampledAt := sample.SampledAt
	state := "measured"
	if now.Sub(sample.SampledAt) > QuotaUsageStaleAfter {
		state = "stale"
	}
	return QuotaUsageWindow{
		UsedPct:     &value,
		ResetsAt:    resetsAt,
		SampledAt:   &sampledAt,
		Attribution: sample.Attribution,
		State:       state,
		Source:      "client_reported",
	}
}

func (s *PgStore) CompareQuotaUsageWindows(ctx context.Context, dashboardUserID int64, billingProvider, accountID string, windowMinutes, limitWindows int, now time.Time) (*QuotaWindowComparison, error) {
	if dashboardUserID <= 0 || !validQuotaAccount(billingProvider, accountID) || windowMinutes != 300 && windowMinutes != 10080 {
		return nil, ErrQuotaBindingInvalid
	}
	maxWindows := 36
	if windowMinutes == 10080 {
		maxWindows = 17
	}
	if limitWindows == 0 {
		limitWindows = maxWindows
	}
	if limitWindows < 1 || limitWindows > maxWindows {
		return nil, ErrQuotaBindingInvalid
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var epoch pgtype.Timestamptz
	err := s.pool.QueryRow(ctx, `SELECT verified_at FROM quota_account_bindings
		WHERE dashboard_user_id=$1 AND billing_provider=$2 AND account_id=$3 AND status='approved'`,
		dashboardUserID, billingProvider, accountID).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !epoch.Valid {
		return nil, ErrQuotaBindingNotFound
	}
	if err != nil {
		return nil, err
	}
	excluded, err := quotaOwnerAccountExcluded(ctx, s.pool, billingProvider, accountID)
	if err != nil {
		return nil, err
	}
	if excluded {
		return nil, ErrQuotaAccountExcluded
	}
	minutesExpr := quotaWindowMinutesSQL("q")
	lookback := time.Duration((limitWindows+2)*windowMinutes) * time.Minute
	rows, err := s.pool.Query(ctx, `SELECT q.window_key,q.sampled_at,q.used_pct,q.resets_at,(`+minutesExpr+`) AS window_minutes,q.attribution
		FROM quota_owner_samples q
		JOIN quota_account_bindings b ON b.dashboard_user_id=q.dashboard_user_id
			AND b.billing_provider=q.billing_provider AND b.account_id=q.account_id
		WHERE q.dashboard_user_id=$1 AND q.billing_provider=$2 AND q.account_id=$3
			AND b.status='approved' AND b.verified_at=$4 AND q.binding_verified_at=b.verified_at
			AND q.sampled_at >= b.verified_at
		AND (`+minutesExpr+`)=$5 AND q.sampled_at >= $6 AND q.sampled_at <= $7
		AND NOT (q.billing_provider='openai' AND (q.window_key LIKE 'codex_bengalfox:%' OR lower(q.scope_label) LIKE '%spark%'))
		AND `+quotaOwnerExclusionClause("q")+`
		ORDER BY q.sampled_at ASC LIMIT $8`, dashboardUserID, billingProvider, accountID, epoch.Time,
		windowMinutes, now.Add(-lookback), now, MaxQuotaComparisonRows+1)
	if err != nil {
		return nil, err
	}
	samples := make([]*QuotaOwnerSample, 0)
	truncatedRows := false
	for rows.Next() {
		if len(samples) == MaxQuotaComparisonRows {
			truncatedRows = true
			break
		}
		sample := &QuotaOwnerSample{BillingProvider: billingProvider, AccountID: accountID}
		var resetsAt pgtype.Timestamptz
		var minutes int
		if err := rows.Scan(&sample.WindowKey, &sample.SampledAt, &sample.UsedPct, &resetsAt, &minutes, &sample.Attribution); err != nil {
			rows.Close()
			return nil, err
		}
		if resetsAt.Valid {
			value := resetsAt.Time
			sample.ResetsAt = &value
		}
		sample.WindowMinutes = &minutes
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	comparison := buildQuotaWindowComparison(billingProvider, accountID, windowMinutes, limitWindows, now, samples)
	comparison.Truncated = comparison.Truncated || truncatedRows
	if len(samples) == 0 {
		comparison.Reason = "window_boundary_unobserved"
	}
	return comparison, nil
}

func buildQuotaWindowComparison(provider, accountID string, windowMinutes, limitWindows int, now time.Time, samples []*QuotaOwnerSample) *QuotaWindowComparison {
	return buildQuotaWindowComparisonFromSamples(provider, accountID, windowMinutes, limitWindows, now, samples)
}

func finiteQuotaPct(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func linearPercentile(sortedValues []float64, percentile float64) *float64 {
	if len(sortedValues) == 0 {
		return nil
	}
	position := percentile * float64(len(sortedValues)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	value := sortedValues[lower]
	if upper != lower {
		value += (position - float64(lower)) * (sortedValues[upper] - sortedValues[lower])
	}
	return &value
}

func downsampleQuotaWindowPoints(points []QuotaWindowPoint, limit int) ([]QuotaWindowPoint, bool) {
	if len(points) <= limit || limit <= 0 {
		return points, false
	}
	if limit == 1 {
		return []QuotaWindowPoint{points[len(points)-1]}, true
	}
	first, last := points[0], points[len(points)-1]
	buckets := limit - 2
	lastInBucket := make([]int, buckets)
	for i := range lastInBucket {
		lastInBucket[i] = -1
	}
	for i := 1; i < len(points)-1; i++ {
		bucket := min(buckets-1, int(math.Floor(points[i].ElapsedFraction*float64(buckets))))
		lastInBucket[bucket] = i
	}
	out := make([]QuotaWindowPoint, 0, limit)
	out = append(out, first)
	for _, index := range lastInBucket {
		if index > 0 {
			out = append(out, points[index])
		}
	}
	out = append(out, last)
	return out, true
}

type quotaComparisonWindowGroup struct {
	start   time.Time
	resets  time.Time
	key     string
	samples []*QuotaOwnerSample
}

func buildQuotaWindowComparisonFromSamples(provider, accountID string, windowMinutes, limitWindows int, now time.Time, samples []*QuotaOwnerSample) *QuotaWindowComparison {
	result := &QuotaWindowComparison{
		BillingProvider: provider,
		AccountID:       accountID,
		WindowMinutes:   windowMinutes,
		History:         []QuotaComparisonWindow{},
		Quantiles:       []QuotaQuantilePoint{},
		Source:          "client_reported",
	}
	if len(samples) == 0 {
		result.Reason = "no_observations"
		return result
	}
	key := chooseQuotaComparisonKey(provider, windowMinutes, samples, now)
	if key == "" {
		result.Reason = "window_boundary_unobserved"
		return result
	}
	duration := time.Duration(windowMinutes) * time.Minute
	groupsByStart := make(map[int64]*quotaComparisonWindowGroup)
	for _, sample := range samples {
		if sample.WindowKey != key || quotaWindowMinutes(sample) != windowMinutes || sample.ResetsAt == nil {
			continue
		}
		start := sample.ResetsAt.Add(-duration)
		x := sample.SampledAt.Sub(start).Seconds() / duration.Seconds()
		if !finiteQuotaPct(sample.UsedPct) || x < 0 || x > 1 || !sample.SampledAt.Before(*sample.ResetsAt) {
			result.Truncated = true
			continue
		}
		mapKey := start.UnixNano()
		group := groupsByStart[mapKey]
		if group == nil {
			group = &quotaComparisonWindowGroup{start: start, resets: *sample.ResetsAt, key: key}
			groupsByStart[mapKey] = group
		}
		group.samples = append(group.samples, sample)
	}
	if len(groupsByStart) == 0 {
		result.Reason = "window_boundary_unobserved"
		return result
	}
	groups := make([]*quotaComparisonWindowGroup, 0, len(groupsByStart))
	for _, group := range groupsByStart {
		sort.Slice(group.samples, func(i, j int) bool { return group.samples[i].SampledAt.Before(group.samples[j].SampledAt) })
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].start.Before(groups[j].start) })

	var current *quotaComparisonWindowGroup
	var histories []*quotaComparisonWindowGroup
	for _, group := range groups {
		if !group.start.After(now) && group.resets.After(now) {
			if current == nil || group.start.After(current.start) {
				current = group
			}
		} else if !group.resets.After(now) && group.start.Before(now) {
			histories = append(histories, group)
		}
	}
	if len(histories) > limitWindows {
		result.Truncated = true
		histories = histories[len(histories)-limitWindows:]
	}
	buildPoints := func(group *quotaComparisonWindowGroup, maxPoints int) []QuotaWindowPoint {
		points := make([]QuotaWindowPoint, 0, len(group.samples))
		for _, sample := range group.samples {
			x := sample.SampledAt.Sub(group.start).Seconds() / duration.Seconds()
			points = append(points, QuotaWindowPoint{
				ElapsedFraction: x,
				UsedPct:         sample.UsedPct,
				SampledAt:       sample.SampledAt,
				Attribution:     sample.Attribution,
			})
		}
		points, downsampled := downsampleQuotaWindowPoints(points, maxPoints)
		if downsampled {
			result.Truncated = true
		}
		return points
	}
	const quantilePoints = 1001
	const freshness = 30 * time.Minute
	quantileWindows := make([]QuotaComparisonWindow, 0, len(histories))
	for _, group := range histories {
		points, downsampled := downsampleQuotaWindowPoints(buildPoints(group, MaxQuotaComparisonPoints), MaxQuotaComparisonPoints)
		if downsampled {
			result.Truncated = true
		}
		quantileWindows = append(quantileWindows, QuotaComparisonWindow{
			WindowStart: group.start,
			ResetsAt:    group.resets,
			WindowKey:   group.key,
			Points:      points,
		})
	}
	result.HistoryCount = len(quantileWindows)
	if current != nil {
		points := buildPoints(current, MaxQuotaComparisonPoints)
		result.Current = &QuotaComparisonWindow{
			WindowStart: current.start,
			ResetsAt:    current.resets,
			WindowKey:   current.key,
			Points:      points,
		}
	}
	if len(quantileWindows) == 0 {
		if current == nil {
			result.Reason = "current_window_unobserved"
		}
		return result
	}

	quantiles := make([]QuotaQuantilePoint, 0, quantilePoints)
	for i := 0; i < quantilePoints; i++ {
		x := float64(i) / float64(quantilePoints-1)
		values := make([]float64, 0, len(quantileWindows))
		for _, window := range quantileWindows {
			if value := quotaValueAt(window.Points, x, duration, freshness); value != nil {
				values = append(values, *value)
			}
		}
		sort.Float64s(values)
		point := QuotaQuantilePoint{ElapsedFraction: x, N: len(values)}
		point.P50 = linearPercentile(values, 0.5)
		if len(values) >= 4 {
			point.P25 = linearPercentile(values, 0.25)
			point.P75 = linearPercentile(values, 0.75)
		}
		quantiles = append(quantiles, point)
	}
	result.Quantiles = quantiles

	// Keep the complete quantile grid and share the remaining response budget
	// evenly across current/history traces.
	traceCount := len(quantileWindows)
	if current != nil {
		traceCount++
	}
	traceBudget := MaxQuotaComparisonReplyPoints - len(quantiles)
	pointsPerTrace := 0
	if traceCount > 0 && traceBudget > 0 {
		pointsPerTrace = max(2, traceBudget/traceCount)
	}
	for i := range quantileWindows {
		points, downsampled := downsampleQuotaWindowPoints(quantileWindows[i].Points, pointsPerTrace)
		if downsampled {
			result.Truncated = true
		}
		quantileWindows[i].Points = points
	}
	if result.Current != nil {
		points, downsampled := downsampleQuotaWindowPoints(result.Current.Points, pointsPerTrace)
		if downsampled {
			result.Truncated = true
		}
		result.Current.Points = points
	}
	result.History = quantileWindows
	return result
}

func chooseQuotaComparisonKey(provider string, windowMinutes int, samples []*QuotaOwnerSample, now time.Time) string {
	keys := make(map[string][]*QuotaOwnerSample)
	for _, sample := range samples {
		if sample.ResetsAt == nil || quotaWindowMinutes(sample) != windowMinutes ||
			provider == "openai" && (strings.HasPrefix(sample.WindowKey, "codex_bengalfox:") || strings.Contains(strings.ToLower(sample.ScopeLabel), "spark")) {
			continue
		}
		keys[sample.WindowKey] = append(keys[sample.WindowKey], sample)
	}
	if len(keys) == 0 {
		return ""
	}
	preferred := ""
	if provider == "openai" {
		preferred = strconv.Itoa(windowMinutes)
	} else if provider == "anthropic" && windowMinutes == 300 {
		preferred = "session"
	} else if provider == "anthropic" && windowMinutes == 10080 {
		preferred = "weekly_all"
	}
	if preferred != "" && len(keys[preferred]) > 0 {
		return preferred
	}
	var general []string
	for key := range keys {
		candidate := &QuotaOwnerSample{WindowKey: key}
		if isGeneralQuotaWindow(provider, candidate) {
			general = append(general, key)
		}
	}
	if len(general) > 0 {
		return mostRecentQuotaKey(keys, general)
	}
	var selected string
	var selectedPct float64 = -1
	var selectedAt time.Time
	for key, points := range keys {
		for _, sample := range points {
			if sample.SampledAt.After(now) {
				continue
			}
			start := sample.ResetsAt.Add(-time.Duration(windowMinutes) * time.Minute)
			if start.After(now) || !sample.ResetsAt.After(now) {
				continue
			}
			if selected == "" || sample.UsedPct > selectedPct || sample.UsedPct == selectedPct && sample.SampledAt.After(selectedAt) {
				selected, selectedPct, selectedAt = key, sample.UsedPct, sample.SampledAt
			}
		}
	}
	if selected != "" {
		return selected
	}
	return mostRecentQuotaKey(keys, nil)
}

func mostRecentQuotaKey(keys map[string][]*QuotaOwnerSample, candidates []string) string {
	if candidates == nil {
		for key := range keys {
			candidates = append(candidates, key)
		}
	}
	sort.Strings(candidates)
	selected := ""
	var latest time.Time
	for _, key := range candidates {
		for _, sample := range keys[key] {
			if selected == "" || sample.SampledAt.After(latest) {
				selected, latest = key, sample.SampledAt
			}
		}
	}
	return selected
}

func quotaValueAt(points []QuotaWindowPoint, elapsed float64, duration, freshness time.Duration) *float64 {
	var latest *QuotaWindowPoint
	for i := range points {
		if points[i].ElapsedFraction > elapsed {
			break
		}
		latest = &points[i]
	}
	if latest == nil || (elapsed-latest.ElapsedFraction)*duration.Seconds() > freshness.Seconds() {
		return nil
	}
	value := latest.UsedPct
	return &value
}
