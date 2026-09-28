package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// trendSource names where a bucketed trend query reads from and what its time
// column is called, so the callers differ by two strings rather than by two
// copies of the query.
type trendSource struct {
	table  string
	tsExpr string
	// countExpr is how many events a group holds. Raw rows are events, so it
	// counts them; rollup rows already carry the count and must sum it. Getting
	// this wrong is invisible in cost and tokens and wrong only in event_count,
	// which nothing on screen checks against another number.
	countExpr string
}

var (
	rawTrendSource    = trendSource{table: "visible_events", tsExpr: "ts", countExpr: "count(*)"}
	rollupTrendSource = trendSource{table: "usage_hourly_rollups", tsExpr: "bucket", countExpr: "COALESCE(sum(event_count),0)"}
)

// trendSourceFor picks the pre-aggregated table when it can answer the question
// exactly, and the raw view otherwise.
//
// Measured on a prod snapshot: the monthly by-model trend is 410ms from raw and
// 7.9ms from the rollup; the 26-week one, 476ms against 11.4ms.
//
// Three things disqualify the rollup, and each is a correctness limit rather
// than a performance one:
//
//   - Minute granularity. The rollup's finest bucket is an hour.
//   - A project filter. The raw path reaches events through session_records and
//     so matches a session on ANY hash it ever carried; the rollup stores the
//     newest one per session. Measured on prod, 337 sessions carry more than
//     one, so the two paths would disagree for those -- and the disagreement
//     would look like a project that simply spent less.
//   - The backfill not having run. A half-built aggregate does not fail; it
//     reports less usage than there was.
//   - A window that starts and ends inside one hour. Its only rows are a
//     fraction of one bucket, so the raw view answers it whole (see window).
func (s *PgStore) trendSourceFor(ctx context.Context, f EventFilter, granularity string) trendSource {
	if granularity == "minute" {
		return rawTrendSource
	}
	if f.ProjectHash != "" || len(f.ProjectHashes) > 0 || f.ProjectHashesPresent {
		return rawTrendSource
	}
	if f.Since != nil && f.Until != nil && ceilHour(*f.Since).After(f.Until.Truncate(time.Hour)) {
		return rawTrendSource
	}
	if !s.usageRollupsReady(ctx) {
		return rawTrendSource
	}
	return rollupTrendSource
}

// usageRollupsReady reports whether the one-shot backfill has finished.
func (s *PgStore) usageRollupsReady(ctx context.Context) bool {
	var ready bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`,
		usageHourlyRollupBackfill).Scan(&ready); err != nil {
		return false
	}
	return ready
}

// bucketExpr is the grouping key, identical in shape on both sources so the two
// paths produce the same labels for the same instants.
func (src trendSource) bucketExpr(granularity, tz string) string {
	return fmt.Sprintf("date_trunc('%s', %s AT TIME ZONE '%s')", granularity, src.tsExpr, tz)
}

// window adds the since/until bounds and returns what the query reads FROM.
//
// Rollup rows are whole hours, and the dashboard's `since` is now minus a window,
// so it is rarely on the hour. Comparing a bucket's start against it would drop
// the whole first hour and keep the whole last one (#768). Instead the rollup
// answers the whole hours inside the window and the raw view the partial hour at
// each end -- at most two hours of events, read by a ts range, so a long window
// still costs what the rollup costs. Edge rows take the rollup's shape (empty
// strings, not NULLs), so the outer query cannot tell the two apart.
func (src trendSource) window(since, until *time.Time, conds *[]string, args *[]interface{}, n *int) string {
	param := func(v time.Time) string {
		*n++
		*args = append(*args, v)
		return fmt.Sprintf("$%d", *n)
	}
	if src != rollupTrendSource {
		if since != nil {
			*conds = append(*conds, fmt.Sprintf("%s >= %s", src.tsExpr, param(*since)))
		}
		if until != nil {
			*conds = append(*conds, fmt.Sprintf("%s < %s", src.tsExpr, param(*until)))
		}
		return src.table
	}

	var whole, edges []string
	if since != nil {
		lo := ceilHour(*since)
		whole = append(whole, "bucket >= "+param(lo))
		if !lo.Equal(*since) {
			edges = append(edges, fmt.Sprintf("(ts >= %s AND ts < %s)", param(*since), param(lo)))
		}
	}
	if until != nil {
		hi := until.Truncate(time.Hour)
		whole = append(whole, "bucket < "+param(hi))
		if !hi.Equal(*until) {
			edges = append(edges, fmt.Sprintf("(ts >= %s AND ts < %s)", param(hi), param(*until)))
		}
	}
	if len(edges) == 0 {
		*conds = append(*conds, whole...)
		return src.table
	}
	// A derived table needs an alias; reusing the real name keeps any outer
	// reference qualified as usage_hourly_rollups pointing at the combined rows.
	return fmt.Sprintf(`(SELECT bucket, model, user_id, profile_email, login_email, user_team, agent,
			billing_provider, cost_usd, input_tokens, output_tokens, event_count
		FROM usage_hourly_rollups WHERE %s
		UNION ALL
		SELECT ts, COALESCE(model, ''), COALESCE(user_id, ''), COALESCE(profile_email, ''),
			COALESCE(login_email, ''), COALESCE(user_team, ''), COALESCE(agent, ''),
			COALESCE(billing_provider, ''), COALESCE(cost_usd, 0)::double precision,
			COALESCE(input_tokens, 0)::bigint, COALESCE(output_tokens, 0)::bigint, 1::bigint
		FROM visible_events WHERE %s) AS usage_hourly_rollups`,
		strings.Join(whole, " AND "), strings.Join(edges, " OR "))
}

// ceilHour is the first hour boundary at or after t.
func ceilHour(t time.Time) time.Time {
	if f := t.Truncate(time.Hour); !f.Equal(t) {
		return f.Add(time.Hour)
	}
	return t
}
