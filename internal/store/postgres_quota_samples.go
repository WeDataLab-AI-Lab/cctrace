package store

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// InsertQuotaSamples appends readings and reports how many were new.
//
// Conflicts are dropped rather than updated. A reading is identified by which
// account, which window, and the instant it was taken, so a row that already
// exists is the same measurement arriving again — the five-minute response
// cache hands the same one back repeatedly, several profiles report the same
// account, and the Codex backfill walks files that may already be collected.
// Overwriting would let a later, worse-attributed copy replace a good one; the
// count of new rows is what tells the caller whether anything was learned.
//
// Rows with no account id are skipped. They cannot be keyed, and filling one in
// from context would be a guess — precisely the mis-attribution the account key
// exists to prevent. One unattributed row must not fail the batch around it.
func (s *PgStore) InsertQuotaSamples(ctx context.Context, samples []*QuotaSample) (int, error) {
	if len(samples) == 0 {
		return 0, nil
	}

	valid := make([]*QuotaSample, 0, len(samples))
	var sourceIDs []string
	for _, q := range samples {
		if q == nil || q.AccountID == "" || q.BillingProvider == "" || q.WindowKey == "" {
			continue
		}
		valid = append(valid, q)
		sourceIDs = append(sourceIDs, q.SourceSessionID)
	}
	if len(valid) == 0 {
		return 0, nil
	}

	// Take deletion locks before building the final batch so samples sourced from a
	// tombstoned session are dropped while account-level samples remain untouched.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := lockSessionOverviewSourceMutation(ctx, tx, false); err != nil {
		return 0, err
	}
	live, err := liveSessionIDs(ctx, tx, sourceIDs)
	if err != nil {
		return 0, err
	}
	keep := valid[:0]
	for _, q := range valid {
		if q.SourceSessionID != "" && !live[q.SourceSessionID] {
			continue
		}
		keep = append(keep, q)
	}
	inserted := 0
	for len(keep) > 0 {
		n := min(len(keep), quotaSampleInsertChunk)
		affected, err := insertQuotaSampleRows(ctx, tx, keep[:n])
		if err != nil {
			return 0, err
		}
		inserted += affected
		keep = keep[n:]
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return inserted, nil
}

// quotaSampleInsertChunk bounds the rows in one INSERT statement. Each row binds
// quotaSampleColumns parameters and the Postgres extended protocol stops at
// 65,535, so one statement holds at most 4,369 rows -- fewer than the 5,000 the
// ingest handler accepts. Chunking inside the transaction keeps the request
// all-or-nothing while letting it reach the size the handler promises.
const (
	quotaSampleColumns     = 15
	quotaSampleInsertChunk = 4000
)

func insertQuotaSampleRows(ctx context.Context, tx pgx.Tx, rows []*QuotaSample) (int, error) {
	var b strings.Builder
	args := make([]any, 0, len(rows)*quotaSampleColumns)
	b.WriteString(`INSERT INTO quota_samples
		(billing_provider, account_id, window_key, sampled_at, used_pct, resets_at,
		 window_minutes, severity, is_active, scope_label, plan, login_email,
		 profile_email, attribution, source_session_id) VALUES `)
	for i, q := range rows {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(placeholders(len(args), quotaSampleColumns))
		attribution := q.Attribution
		if attribution == "" {
			attribution = AttributionObserved
		}
		args = append(args, q.BillingProvider, q.AccountID, q.WindowKey, q.SampledAt,
			q.UsedPct, q.ResetsAt, q.WindowMinutes, q.Severity, q.IsActive, q.ScopeLabel,
			q.Plan, q.LoginEmail, q.ProfileEmail, attribution, q.SourceSessionID)
	}
	b.WriteString(` ON CONFLICT (billing_provider, account_id, window_key, sampled_at) DO NOTHING`)
	tag, err := tx.Exec(ctx, b.String(), args...)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// placeholders renders ($1,$2,...) for one row starting after offset args.
func placeholders(offset, n int) string {
	var b strings.Builder
	b.WriteByte('(')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(offset + i + 1))
	}
	b.WriteByte(')')
	return b.String()
}

// targetPointsPerSeries bounds how many points one account's line can carry.
//
// A chart is on the order of a thousand pixels wide, so a series denser than
// that draws points no one can see while costing the same to encode, ship and
// parse. It is a display bound, not a data-loss tradeoff.
const targetPointsPerSeries = 1000

// bucketSeconds returns the downsampling bucket width for a range, or 0 when the
// range should be returned raw.
//
// Zero for an unbounded range: with no from/to there is nothing to divide, and
// bucketing on a guess would change what an unfiltered read means. Zero also for
// any range short enough that one bucket would be under a second, where
// downsampling could only lose resolution without saving anything.
func bucketSeconds(from, to time.Time) int64 {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return 0
	}
	return int64(to.Sub(from).Seconds()) / targetPointsPerSeries
}

// ListQuotaSamples returns readings in time order.
//
// Time order is not cosmetic. Reporters have their own clocks, and within one
// window utilization rises monotonically until the reset — so an inversion in
// the output is a clock skew to be read as such, not a data conflict. Sorting
// on read is what makes that legible.
func (s *PgStore) ListQuotaSamples(ctx context.Context, f QuotaSampleFilter) ([]*QuotaSample, error) {
	var (
		where []string
		args  []any
	)
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, clause+strconv.Itoa(len(args)))
	}
	if !f.From.IsZero() {
		add("sampled_at >= $", f.From)
	}
	if !f.To.IsZero() {
		add("sampled_at <= $", f.To)
	}
	if f.BillingProvider != "" {
		add("billing_provider = $", f.BillingProvider)
	}
	// 5h and 7d are different time scales; a series mixing them names no state
	// at all, so the chart asks for one window at a time.
	if f.WindowMinutes > 0 {
		add("window_minutes = $", f.WindowMinutes)
	}

	// Excluded accounts are filtered at read time, the same way visible_session_records
	// and the cost queries do it, so exclusion stays reversible policy rather than
	// destructive maintenance. This table was added after that rule was established
	// and did not inherit it: an excluded account's readings were drawn on the chart,
	// and -- worse than being visible -- its subscription price stayed in the
	// denominator of the price-weighted average, so the headline percentage was wrong
	// and not merely cluttered.
	//
	// Matched on login_email because that is what excluded_accounts keys on: the
	// billing login. profile_email is a different fact -- which cctrace profile
	// reported the reading -- and one account carries many of them, so it is not an
	// alternative key here.
	//
	// The same exclusion by the billing keys an account can be known by. Codex rows
	// carry no login_email at all -- 0 of 1,002,433 in session_records -- so the
	// address key can never reach them, while account_id is on every row here
	// because it is part of the primary key.
	//
	// For backfilled Codex readings that account_id is inferred rather than
	// observed: the JSONL's rate_limits records carry no account identifier, so
	// the account was credited from the observation log of which one was logged in
	// at the time. The attribution rule credits a gap to the *next* account
	// precisely so that an excluded account is over-excluded rather than leaked.
	//
	// And the accounts an excluded address was seen with (#715): most of an
	// account's readings carry no address, so the address key reaches only the
	// few that do.
	where = append(where, excludedAccountPredicateSQL("quota_samples"))

	const cols = `billing_provider, account_id, window_key, sampled_at, used_pct, resets_at,
	              window_minutes, severity, is_active, scope_label, plan, login_email,
	              profile_email, attribution, source_session_id`
	const order = " ORDER BY sampled_at, billing_provider, account_id, window_key"

	q := "SELECT " + cols + " FROM quota_samples"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}

	// Wide ranges are downsampled in the database rather than shipped whole.
	//
	// This endpoint returned every raw reading in the range: 16 rows over 72
	// hours, but 109,212 over 26 weeks. Encoding that much JSON ran past the
	// write deadline ("json encode error: ... i/o timeout") and the client froze
	// parsing what did arrive, so the widest ranges did not render at all.
	//
	// One row per bucket per series, and the bucket keeps its LAST reading.
	// Utilization is a gauge that climbs until the window resets, so the closing
	// value is what the step line between two points actually asserts; a mean
	// would invent a level the account never reported, and the max would erase
	// every reset.
	if n := bucketSeconds(f.From, f.To); n > 0 {
		args = append(args, n)
		bucket := "to_timestamp(floor(extract(epoch FROM sampled_at) / $" +
			strconv.Itoa(len(args)) + "::float8) * $" + strconv.Itoa(len(args)) + "::float8)"
		series := "billing_provider, account_id, window_key"
		q = `WITH bucketed AS (
			SELECT ` + cols + `,
			  row_number() OVER (PARTITION BY ` + series + `, ` + bucket + `
			                     ORDER BY sampled_at DESC) AS rn,
			  -- A bucket holding any inferred reading is inferred. Reporting the
			  -- surviving row's own attribution would let a bucket that mixes both
			  -- pass as observed, which is the one claim this flag exists to keep
			  -- honest.
			  bool_or(attribution = 'inferred') OVER (PARTITION BY ` + series + `, ` + bucket + `) AS any_inferred
			FROM (` + q + `) filtered
		)
		SELECT billing_provider, account_id, window_key, sampled_at, used_pct, resets_at,
		       window_minutes, severity, is_active, scope_label, plan, login_email,
		       profile_email,
		       CASE WHEN any_inferred THEN 'inferred' ELSE attribution END,
		       source_session_id
		FROM bucketed WHERE rn = 1`
	}
	q += order

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*QuotaSample
	for rows.Next() {
		r := &QuotaSample{}
		if err := rows.Scan(&r.BillingProvider, &r.AccountID, &r.WindowKey, &r.SampledAt,
			&r.UsedPct, &r.ResetsAt, &r.WindowMinutes, &r.Severity, &r.IsActive,
			&r.ScopeLabel, &r.Plan, &r.LoginEmail, &r.ProfileEmail, &r.Attribution,
			&r.SourceSessionID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
