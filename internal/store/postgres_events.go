package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *PgStore) ListEvents(ctx context.Context, f EventFilter) ([]*OtelEvent, error) {
	where, args := buildEventWhere(f)
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	q := fmt.Sprintf(`SELECT ts, event_name, session_id, prompt_id,
		user_id, profile_email, login_email, user_name, user_team, org_id,
		model, cost_usd, input_tokens, output_tokens,
		cache_read_tokens, cache_create_tokens, duration_ms,
		tool_name, tool_decision, tool_success,
		speed, service_version, attrs
		FROM visible_events %s ORDER BY ts DESC, tiebreak DESC LIMIT %d OFFSET %d`,
		where, limit, f.Offset)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*OtelEvent, 0)
	for rows.Next() {
		e := &OtelEvent{}
		var attrsJSON []byte
		if err := rows.Scan(
			&e.Ts, &e.EventName, &e.SessionID, &e.PromptID,
			&e.UserID, &e.ProfileEmail, &e.LoginEmail, &e.UserName, &e.UserTeam, &e.OrgID,
			&e.Model, &e.CostUSD, &e.InputTokens, &e.OutputTokens,
			&e.CacheReadTokens, &e.CacheCreateTokens, &e.DurationMs,
			&e.ToolName, &e.ToolDecision, &e.ToolSuccess,
			&e.Speed, &e.ServiceVersion, &attrsJSON,
		); err != nil {
			return nil, err
		}
		if len(attrsJSON) > 0 {
			_ = json.Unmarshal(attrsJSON, &e.Attrs)
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *PgStore) CountEvents(ctx context.Context, f EventFilter) (int64, error) {
	where, args := buildEventWhere(f)
	q := fmt.Sprintf("SELECT COUNT(*) FROM visible_events %s", where)
	var count int64
	err := s.pool.QueryRow(ctx, q, args...).Scan(&count)
	return count, err
}

func buildEventWhere(f EventFilter) (string, []interface{}) {
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

	add("session_id", f.SessionID)
	if f.UserID != "" {
		n++
		conds = append(conds, fmt.Sprintf("user_id = $%d", n))
		args = append(args, f.UserID)
	} else {
		add("profile_email", f.ProfileEmail)
		add("login_email", f.LoginEmail)
	}
	if f.ProjectHash != "" {
		n++
		conds = append(conds, fmt.Sprintf("session_id IN (SELECT DISTINCT session_id FROM session_records WHERE project_hash = $%d)", n))
		args = append(args, f.ProjectHash)
	}
	add("user_team", f.UserTeam)
	add("event_name", f.EventName)
	if clause := appendAgentScope("agent", f.Agent, &n, &args); clause != "" {
		conds = append(conds, clause)
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
