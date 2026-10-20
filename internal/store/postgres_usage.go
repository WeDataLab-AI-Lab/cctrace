package store

import (
	"context"
	"fmt"
	"strings"
)

// UsageAggregates returns exact per-model token totals plus non-duplicated
// overall session and elapsed-time totals. OTEL tokens win for a session when
// present; synchronized session-record tokens are the fallback.
func (s *PgStore) UsageAggregates(ctx context.Context, f SessionOverviewFilter) (*UsageAggregate, error) {
	var eventConds, recordConds []string
	var args []interface{}
	n := 0
	addIdentity := func(eventColumn, recordColumn, value string) {
		if value == "" {
			return
		}
		n++
		eventConds = append(eventConds, fmt.Sprintf("e.%s = $%d", eventColumn, n))
		recordConds = append(recordConds, fmt.Sprintf("sr.%s = $%d", recordColumn, n))
		args = append(args, value)
	}
	if f.UserID != "" {
		addIdentity("user_id", "user_id", f.UserID)
	} else if f.ProfileEmail != "" {
		addIdentity("profile_email", "profile_email", f.ProfileEmail)
	} else if f.LoginEmail != "" {
		addIdentity("login_email", "login_email", f.LoginEmail)
	}
	if f.Since != nil {
		n++
		eventConds = append(eventConds, fmt.Sprintf("e.ts >= $%d", n))
		recordConds = append(recordConds, fmt.Sprintf("sr.ts >= $%d", n))
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		n++
		eventConds = append(eventConds, fmt.Sprintf("e.ts < $%d", n))
		recordConds = append(recordConds, fmt.Sprintf("sr.ts < $%d", n))
		args = append(args, *f.Until)
	}
	eventWhere := "WHERE e.session_id != ''"
	recordWhere := "sr.session_id != ''"
	if len(eventConds) > 0 {
		eventWhere += " AND " + strings.Join(eventConds, " AND ")
		recordWhere += " AND " + strings.Join(recordConds, " AND ")
	}
	var sessionFilters []string
	if len(f.ProjectHashes) > 0 {
		n++
		sessionFilters = append(sessionFilters, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM visible_session_records project_record
			WHERE project_record.session_id = session_scope.session_id
				AND project_record.project_hash = ANY($%d)
		)`, n))
		args = append(args, f.ProjectHashes)
	}
	sessionWhere := "WHERE (has_api_request OR has_sync)"
	if len(sessionFilters) > 0 {
		sessionWhere += " AND " + strings.Join(sessionFilters, " AND ")
	}

	// Codex visible event input is normalized to exclude cache reads. A cache-only
	// Codex visible row therefore still proves that the OTEL-side source is present;
	// Claude keeps the old input/output criterion so its session-record fallback
	// remains unchanged.
	//
	// The session-record fallback counts requests. Its arms of session_union are
	// attributedLinesUnionSQL's: every request line at 0, and a Codex usage row the
	// tokens of its codex_imputed_cost row, normalized like the event side, so
	// the union keeps its one parallel scan. Each request's tokens are
	// request_tokens, one row per request with its head line's session and model
	// (claudeRequestHeadsSQL), summed by model beside the union's rows: together
	// they are attributedRowsSQL's sum. recordWhere, the identity and period
	// filter, is the lineWhere of both.
	q := fmt.Sprintf(`WITH session_union AS (
		SELECT session_id, model, ts, 'otel'::text AS src, input_tokens, output_tokens,
			cache_read_tokens, COALESCE(agent, 'claude') AS agent, event_name
		FROM visible_events e %s
		UNION ALL
		%s
	), session_scope AS (
		SELECT session_id,
			MIN(ts) AS start_time, MAX(ts) AS end_time,
			bool_or(src = 'srec') AS has_sync,
			bool_or(event_name = 'api_request') AS has_api_request,
			COALESCE(SUM(input_tokens) FILTER (WHERE src = 'otel'), 0)
				+ COALESCE(SUM(output_tokens) FILTER (WHERE src = 'otel'), 0)
				+ COALESCE(SUM(cache_read_tokens) FILTER (WHERE src = 'otel' AND agent = 'codex'), 0) != 0 AS has_otel_tokens
		FROM session_union GROUP BY session_id
	), eligible_sessions AS (
		SELECT * FROM session_scope %s
	), request_tokens AS (
		-- Only a session without OTEL tokens counts its session records, so only
		-- its requests are folded. The array names sessions, a partition column of
		-- the fold, so each kept session's requests come out as they would over
		-- every session.
		SELECT h.session_id, COALESCE(h.model,'') AS model, h.input_tokens, h.output_tokens
		FROM (%s) h
	), model_totals AS (
		-- Each source's rows summed by model, then the two sums added: a request
		-- row is a session-record row of a Claude request, kept by the same rules.
		SELECT model, SUM(input_tokens)::bigint AS input_tokens, SUM(output_tokens)::bigint AS output_tokens
		FROM (
			SELECT COALESCE(NULLIF(u.model,''), 'unknown') AS model,
				COALESCE(SUM(u.input_tokens),0) AS input_tokens,
				COALESCE(SUM(u.output_tokens),0) AS output_tokens
			FROM session_union u
			JOIN eligible_sessions e USING (session_id)
			WHERE ((e.has_otel_tokens AND u.src = 'otel') OR (NOT e.has_otel_tokens AND u.src = 'srec'))
				AND (COALESCE(u.input_tokens,0) != 0 OR COALESCE(u.output_tokens,0) != 0
					OR (u.agent = 'codex' AND COALESCE(u.cache_read_tokens,0) != 0))
			GROUP BY 1
			UNION ALL
			SELECT COALESCE(NULLIF(r.model,''), 'unknown'),
				COALESCE(SUM(r.input_tokens),0), COALESCE(SUM(r.output_tokens),0)
			FROM request_tokens r
			JOIN eligible_sessions e USING (session_id)
			WHERE NOT e.has_otel_tokens
				AND (COALESCE(r.input_tokens,0) != 0 OR COALESCE(r.output_tokens,0) != 0)
			GROUP BY 1
		) m
		GROUP BY model
	), totals AS (
		SELECT COUNT(*)::bigint AS session_count,
			COALESCE((SELECT SUM(input_tokens) FROM model_totals),0)::bigint AS input_tokens,
			COALESCE((SELECT SUM(output_tokens) FROM model_totals),0)::bigint AS output_tokens,
			COALESCE(FLOOR(SUM(GREATEST(EXTRACT(EPOCH FROM (end_time - start_time)), 0)))::bigint, 0) AS work_time_seconds
		FROM eligible_sessions
	)
	SELECT t.session_count, t.input_tokens, t.output_tokens, t.work_time_seconds,
		COALESCE(m.model,''), COALESCE(m.input_tokens,0), COALESCE(m.output_tokens,0)
	FROM totals t LEFT JOIN model_totals m ON true
	ORDER BY m.model`, eventWhere,
		attributedLinesUnionSQL("visible_session_records", recordWhere, []string{"session_id", "model", "ts", "agent"},
			`session_id, COALESCE(model,''), ts, 'srec'::text, req_input_tokens, req_output_tokens,
			req_cache_read_tokens, COALESCE(agent, 'claude'), ''::text`, ""),
		sessionWhere,
		claudeRequestHeadsSQL("visible_session_records", recordWhere+
			" AND sr.session_id = ANY(ARRAY(SELECT session_id FROM eligible_sessions WHERE NOT has_otel_tokens))",
			[]string{"session_id", "model"}))
	rows, err := s.pool.Query(ctx, q, append([]any{plannedPerCall}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := &UsageAggregate{ByModel: make([]*ModelUsageAggregate, 0)}
	for rows.Next() {
		model := &ModelUsageAggregate{}
		if err := rows.Scan(
			&result.SessionCount, &result.InputTokens, &result.OutputTokens, &result.WorkTimeSeconds,
			&model.Model, &model.InputTokens, &model.OutputTokens,
		); err != nil {
			return nil, err
		}
		if model.Model != "" {
			result.ByModel = append(result.ByModel, model)
		}
	}
	return result, rows.Err()
}
