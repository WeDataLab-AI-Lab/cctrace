package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	metricLimitDefault = 100
	// metricLimitMax bounds a caller-supplied page size, as the other paged
	// reads in this package do. otel_metrics is a hypertable and the model
	// filter runs as a LIKE pattern that no index can serve, so without a cap
	// one request can scan every chunk within the retention window.
	metricLimitMax = 1000
)

func clampMetricLimit(limit int) int {
	if limit <= 0 {
		return metricLimitDefault
	}
	if limit > metricLimitMax {
		return metricLimitMax
	}
	return limit
}

func (s *PgStore) ListMetrics(ctx context.Context, f MetricFilter) ([]*OtelMetric, error) {
	where, args := buildMetricWhere(f)
	limit := clampMetricLimit(f.Limit)

	q := fmt.Sprintf(`SELECT ts, metric_name, session_id,
			user_id, profile_email, login_email, user_team, model,
			value_double, value_int, agent, billing_provider, dimensions
			FROM visible_metrics %s ORDER BY ts DESC, id DESC LIMIT %d OFFSET %d`,
		where, limit, f.Offset)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*OtelMetric, 0)
	for rows.Next() {
		m := &OtelMetric{}
		var dimJSON []byte
		if err := rows.Scan(
			&m.Ts, &m.MetricName, &m.SessionID,
			&m.UserID, &m.ProfileEmail, &m.LoginEmail, &m.UserTeam, &m.Model,
			&m.ValueDouble, &m.ValueInt, &m.Agent, &m.BillingProvider, &dimJSON,
		); err != nil {
			return nil, err
		}
		if len(dimJSON) > 0 {
			_ = json.Unmarshal(dimJSON, &m.Dimensions)
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

// escapeLike neutralizes LIKE wildcards in user input so the pattern matches the
// characters typed. Used with an explicit ESCAPE '\' clause.
var likeEscaper = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)

func escapeLike(s string) string { return likeEscaper.Replace(s) }

func buildMetricWhere(f MetricFilter) (string, []interface{}) {
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

	add("metric_name", f.MetricName)
	add("user_id", f.UserID)
	add("profile_email", f.ProfileEmail)
	add("login_email", f.LoginEmail)
	add("user_team", f.UserTeam)
	if clause := appendAgentScope("agent", f.Agent, &n, &args); clause != "" {
		conds = append(conds, clause)
	}

	// Contains match, so the same model= value means the same thing here and on
	// the /api/stats/* endpoints. Equality does not work: the dashboard's model
	// dropdown persists values with the "claude-" prefix stripped
	// ("sonnet-4-6"), which would match none of the stored "claude-sonnet-4-6"
	// rows and report that as an empty result rather than an error.
	//
	// Known cost, inherited from the stats endpoints: a shorter model name also
	// selects the longer ones built on it, so model=gpt-5.4 returns gpt-5.4-mini
	// rows even though the dropdown lists them separately. Narrowing that would
	// have to change the stats endpoints too, which would move numbers already
	// on the dashboard. Documented in docs/design/design-api-endpoints.md.
	//
	// The value is escaped so a "_" or "%" in it matches itself. The stats
	// endpoints do not escape, so the two differ only for input containing LIKE
	// wildcards -- not for any value the dropdown can produce.
	if f.Model != "" {
		n++
		conds = append(conds, fmt.Sprintf(`model LIKE $%d ESCAPE '\'`, n))
		args = append(args, "%"+escapeLike(f.Model)+"%")
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

	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}
