package store

import (
	"context"
	"fmt"
	"strings"
)

func (s *PgStore) TimeSeriesStatsByModel(ctx context.Context, f EventFilter, granularity string) ([]*ModelDailyStat, error) {
	allowed := map[string]bool{"minute": true, "hour": true, "day": true, "week": true, "month": true}
	if !allowed[granularity] {
		granularity = "day"
	}
	dateFmt := map[string]string{
		"minute": `YYYY-MM-DD"T"HH24:MI:SS`,
		"hour":   `YYYY-MM-DD"T"HH24:MI:SS`,
		"day":    `YYYY-MM-DD`,
		"week":   `YYYY-MM-DD`,
		"month":  `YYYY-MM-DD`,
	}[granularity]

	var conds []string
	var args []interface{}
	n := 0

	add := func(col, val string) {
		if val != "" {
			n++
			conds = append(conds, fmt.Sprintf("%s = $%d", col, n))
			args = append(args, val)
		}
	}

	src := s.trendSourceFor(ctx, f, granularity)
	from := src.window(f.Since, f.Until, &conds, &args, &n)
	add("profile_email", f.ProfileEmail)
	add("login_email", f.LoginEmail)
	add("user_id", f.UserID)
	// Only reachable on the raw source: a project filter sends trendSourceFor
	// back to visible_events.
	addProjectHashFilter(&conds, &args, &n, "visible_events.session_id", "visible_events.agent", f)
	add("user_team", f.UserTeam)
	if clause := appendAgentScope("agent", f.Agent, &n, &args); clause != "" {
		conds = append(conds, clause)
	}
	switch f.ModelCategory {
	case "anthropic":
		conds = append(conds, "billing_provider = 'anthropic'")
	case "codex":
		conds = append(conds, "billing_provider = 'openai'")
	case "compatible":
		conds = append(conds, "billing_provider NOT IN ('anthropic', 'openai')")
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	tz := f.Timezone
	if tz == "" {
		tz = "UTC"
	}

	bucket := src.bucketExpr(granularity, tz)
	q := fmt.Sprintf(`SELECT to_char(%s, '%s'),
		COALESCE(model, ''),
		COALESCE(sum(cost_usd),0),
		COALESCE(sum(input_tokens),0),
		COALESCE(sum(output_tokens),0),
		%s
	FROM %s %s
	GROUP BY %s, model
	ORDER BY %s ASC, model ASC`, bucket, dateFmt, src.countExpr, from, where, bucket, bucket)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*ModelDailyStat, 0)
	for rows.Next() {
		d := &ModelDailyStat{}
		if err := rows.Scan(&d.Date, &d.Model, &d.CostUSD, &d.InputTokens, &d.OutputTokens, &d.EventCount); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
