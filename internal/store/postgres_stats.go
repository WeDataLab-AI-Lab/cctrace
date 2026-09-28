package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// addProjectHashFilter keeps the scalar project_hash wire shape compatible while
// allowing one canonical project identity to select every member hash. An
// explicitly empty member list means no rows; an absent list falls back to the
// scalar value, and with neither value present the query remains unfiltered.
func addProjectHashFilter(conds *[]string, args *[]interface{}, n *int, sessionExpr, agentExpr string, f EventFilter) {
	if f.ProjectHashesPresent && len(f.ProjectHashes) == 0 {
		// The latest-activity builder formats predicates with a table alias;
		// retain that placeholder while making the predicate unconditionally false.
		*conds = append(*conds, fmt.Sprintf("%s IS NULL AND FALSE", sessionExpr))
		return
	}
	if len(f.ProjectHashes) > 0 {
		*n = *n + 1
		*conds = append(*conds, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM session_records project_scope
			WHERE project_scope.session_id = %s
				AND COALESCE(NULLIF(project_scope.agent, ''), 'claude') = COALESCE(NULLIF(%s, ''), 'claude')
				AND project_scope.project_hash = ANY($%d)
		)`, sessionExpr, agentExpr, *n))
		*args = append(*args, f.ProjectHashes)
		return
	}
	if f.ProjectHash != "" {
		*n = *n + 1
		*conds = append(*conds, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM session_records project_scope
			WHERE project_scope.session_id = %s
				AND COALESCE(NULLIF(project_scope.agent, ''), 'claude') = COALESCE(NULLIF(%s, ''), 'claude')
				AND project_scope.project_hash = $%d
		)`, sessionExpr, agentExpr, *n))
		*args = append(*args, f.ProjectHash)
	}
}

// addLatestProjectHashFilter computes the selected sessions once before the five
// latest-activity arms. Re-running the session_records lookup for every candidate
// event defeats each arm's timestamp LIMIT 1 lookup on a busy installation.
func addLatestProjectHashFilter(conds *[]string, args *[]interface{}, n *int, f EventFilter) string {
	if f.ProjectHashesPresent && len(f.ProjectHashes) == 0 {
		*conds = append(*conds, "%s.session_id IS NULL AND FALSE")
		return ""
	}

	var predicate string
	if len(f.ProjectHashes) > 0 {
		*n++
		predicate = fmt.Sprintf("project_hash = ANY($%d)", *n)
		*args = append(*args, f.ProjectHashes)
	} else if f.ProjectHash != "" {
		*n++
		predicate = fmt.Sprintf("project_hash = $%d", *n)
		*args = append(*args, f.ProjectHash)
	} else {
		return ""
	}

	return `project_scope AS MATERIALIZED (
		SELECT DISTINCT session_id, COALESCE(NULLIF(agent, ''), 'claude') AS agent
		FROM session_records
		WHERE ` + predicate + `
	)`
}

func (s *PgStore) DailyStats(ctx context.Context, f EventFilter) ([]*DailyStat, error) {
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

	if f.Since != nil {
		n++
		conds = append(conds, fmt.Sprintf("ts >= $%d", n))
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		n++
		conds = append(conds, fmt.Sprintf("ts < $%d", n))
		args = append(args, *f.Until)
	}
	add("profile_email", f.ProfileEmail)
	add("user_id", f.UserID)
	add("user_team", f.UserTeam)
	if clause := appendAgentScope("agent", f.Agent, &n, &args); clause != "" {
		conds = append(conds, clause)
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	tz := f.Timezone
	if tz == "" {
		tz = "UTC"
	}

	q := fmt.Sprintf(`SELECT to_char(date_trunc('day', ts AT TIME ZONE '%s'), 'YYYY-MM-DD'),
		COALESCE(sum(cost_usd),0),
		COALESCE(sum(input_tokens),0),
		COALESCE(sum(output_tokens),0),
		count(*)
	FROM visible_events %s
	GROUP BY date_trunc('day', ts AT TIME ZONE '%s')
	ORDER BY date_trunc('day', ts AT TIME ZONE '%s') DESC`, tz, where, tz, tz)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*DailyStat, 0)
	for rows.Next() {
		d := &DailyStat{}
		if err := rows.Scan(&d.Date, &d.CostUSD, &d.InputTokens, &d.OutputTokens, &d.EventCount); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (s *PgStore) TimeSeriesStats(ctx context.Context, f EventFilter, granularity string) ([]*DailyStat, error) {
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

	if f.Since != nil {
		n++
		conds = append(conds, fmt.Sprintf("ts >= $%d", n))
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		n++
		conds = append(conds, fmt.Sprintf("ts < $%d", n))
		args = append(args, *f.Until)
	}
	add("profile_email", f.ProfileEmail)
	add("login_email", f.LoginEmail)
	add("user_id", f.UserID)
	addProjectHashFilter(&conds, &args, &n, "visible_events.session_id", "visible_events.agent", f)
	add("user_team", f.UserTeam)
	if clause := appendAgentScope("agent", f.Agent, &n, &args); clause != "" {
		conds = append(conds, clause)
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	tz := f.Timezone
	if tz == "" {
		tz = "UTC"
	}

	q := fmt.Sprintf(`SELECT to_char(date_trunc('%s', ts AT TIME ZONE '%s'), '%s'),
		COALESCE(sum(cost_usd),0),
		COALESCE(sum(input_tokens),0),
		COALESCE(sum(output_tokens),0),
		count(*)
	FROM visible_events %s
	GROUP BY date_trunc('%s', ts AT TIME ZONE '%s')
	ORDER BY date_trunc('%s', ts AT TIME ZONE '%s') ASC`, granularity, tz, dateFmt, where, granularity, tz, granularity, tz)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*DailyStat, 0)
	for rows.Next() {
		d := &DailyStat{}
		if err := rows.Scan(&d.Date, &d.CostUSD, &d.InputTokens, &d.OutputTokens, &d.EventCount); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// LatestActivityTs returns the most recent unified_events ts within the given
// filter scope (same filters as TimeSeriesStatsByModel, minus the time window),
// bounded to the last 365 days. The frontend maps the age of this ts to the finest
// chart granularity whose window contains it, replacing a 5-step sequential
// granularity escalation with a single probe. Returns ok=false when no rows match.
func (s *PgStore) LatestActivityTs(ctx context.Context, f EventFilter) (time.Time, bool, error) {
	q, args := buildLatestActivityQuery(f)
	var latest time.Time
	if err := s.pool.QueryRow(ctx, q, args...).Scan(&latest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return latest, true, nil
}

// buildLatestActivityQuery expands the same six physical arms as unified_events,
// but asks each source for only its newest matching row. The per-arm ORDER/LIMIT is
// intentional: unlike max(ts), it gives PostgreSQL and TimescaleDB an ordered lookup
// that can stop at the first qualifying row of each timestamp index.
func buildLatestActivityQuery(f EventFilter) (string, []interface{}) {
	var shared []string
	var args []interface{}
	n := 0
	add := func(col, val string) {
		if val == "" {
			return
		}
		n++
		shared = append(shared, fmt.Sprintf("%%s.%s = $%d", col, n))
		args = append(args, val)
	}
	add("profile_email", f.ProfileEmail)
	add("login_email", f.LoginEmail)
	add("user_id", f.UserID)
	projectScope := addLatestProjectHashFilter(&shared, &args, &n, f)
	if clause := appendAgentScope("%s.agent", f.Agent, &n, &args); clause != "" {
		shared = append(shared, clause)
	}
	switch f.ModelCategory {
	case "anthropic":
		shared = append(shared, "%s.billing_provider = 'anthropic'")
	case "codex":
		shared = append(shared, "%s.billing_provider = 'openai'")
	case "compatible":
		shared = append(shared, "%s.billing_provider NOT IN ('anthropic', 'openai')")
	}
	if f.Model != "" {
		n++
		shared = append(shared, fmt.Sprintf("%%s.model LIKE $%d", n))
		args = append(args, "%"+f.Model+"%")
	}

	teamArg := 0
	if f.UserTeam != "" {
		n++
		teamArg = n
		args = append(args, f.UserTeam)
	}

	arm := func(table, alias string, base []string, hasUserTeam bool) string {
		conds := []string{alias + ".ts >= now() - interval '365 days'"}
		conds = append(conds, base...)
		for _, predicate := range shared {
			conds = append(conds, fmt.Sprintf(predicate, alias))
		}
		if teamArg != 0 {
			if hasUserTeam {
				conds = append(conds, fmt.Sprintf("%s.user_team = $%d", alias, teamArg))
			} else {
				// Every non-OTEL unified_events arm projects user_team as ''.
				conds = append(conds, fmt.Sprintf("'' = $%d", teamArg))
			}
		}
		conds = append(conds, fmt.Sprintf(`NOT EXISTS (
			SELECT 1 FROM excluded_accounts x
			WHERE lower(x.login_email) = lower(%s.login_email)
		)`, alias))
		if projectScope == "" {
			return fmt.Sprintf("(SELECT %s.ts FROM %s %s WHERE %s ORDER BY %s.ts DESC LIMIT 1)",
				alias, table, alias, strings.Join(conds, " AND "), alias)
		}

		// Drive from the project scope, one indexed lookup per session, instead of
		// joining the scope onto a newest-first walk of the whole table.
		//
		// The difference is what the walk has to cross. `ORDER BY ts DESC LIMIT 1`
		// reads the table newest-first and stops at the first row the join keeps,
		// so its cost is not the project's size but the distance back to the
		// project's newest row -- every other project's rows in between are read
		// and discarded. On prod a project whose newest otel_events row was 24 days
		// old spent 35.6s of a 36.0s query in that one arm, while the same answer
		// came back in 1.8s when driven per session.
		//
		// The correlation is on session_id, which otel_events, session_records and
		// both imputed-cost tables already index with ts DESC beside it. No new
		// index is needed; the scope is ~1k sessions and each lookup is a descent.
		conds = append(conds, fmt.Sprintf("%s.session_id = project_filter.session_id", alias))
		conds = append(conds, fmt.Sprintf("COALESCE(NULLIF(%s.agent, ''), 'claude') = project_filter.agent", alias))
		return fmt.Sprintf(`(SELECT max(newest.ts) AS ts FROM project_scope project_filter
			CROSS JOIN LATERAL (
				SELECT %s.ts FROM %s %s WHERE %s ORDER BY %s.ts DESC LIMIT 1
			) newest)`, alias, table, alias, strings.Join(conds, " AND "), alias)
	}

	arms := []string{
		arm("otel_events", "o", nil, true),
		arm("codex_imputed_cost", "c", nil, false),
		arm("claude_imputed_cost", "i", nil, false),
		arm("session_records", "g", []string{"g.agent = 'gjc'", "g.record_type = 'assistant'"}, false),
		arm("session_records", "m", []string{"m.agent = 'omo'", "m.record_type = 'assistant'"}, false),
		// A handful of rows a week, so the view's own arm as a subquery costs nothing.
		arm("("+weeklyUsageArm+")", "w", nil, false),
	}
	prefix := ""
	if projectScope != "" {
		prefix = "WITH " + projectScope + "\n"
	}
	// A project-filtered arm aggregates, so an arm whose table holds nothing for
	// this project returns one NULL row where the unfiltered form returned no row
	// at all. That breaks the caller twice over: Postgres orders NULLs FIRST
	// under DESC so an empty arm outranks the real answer, and a project with no
	// activity anywhere returns a NULL row instead of the no-rows the caller
	// reads as "never active" (it scans straight into *time.Time).
	//
	// Dropping the NULLs restores both: the row count matches the unfiltered
	// form, and the ordering has nothing to trip over.
	return prefix + `SELECT ts FROM (` + strings.Join(arms, " UNION ALL ") + `) latest
		WHERE ts IS NOT NULL
		ORDER BY ts DESC
		LIMIT 1`, args
}
