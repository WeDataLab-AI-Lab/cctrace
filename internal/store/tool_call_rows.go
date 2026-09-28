package store

// toolCallRowsSQL is every tool call both agents report, one row per event or
// metric datapoint, for a FROM clause. Claude reports each call as a tool_result
// event; Codex sends none, and reports calls as the codex.tool.call metric -- a
// delta count carrying tool and success, with no session (#698). Tool names stay
// as each agent reports them. uses, successes and fails are counts to SUM.
//
// Callers filter on ts, tool_name, user_id, profile_email and login_email outside;
// the planner pushes those into both arms.
const toolCallRowsSQL = `(
	SELECT ts, tool_name, user_id, profile_email, login_email,
		1 AS uses,
		CASE WHEN tool_success = true THEN 1 ELSE 0 END AS successes,
		CASE WHEN tool_success = false THEN 1 ELSE 0 END AS fails
	FROM visible_events
	WHERE event_name = 'tool_result' AND tool_name <> ''
	UNION ALL
	SELECT ts, dimensions->>'tool', user_id, profile_email, login_email,
		COALESCE(value_int, 0),
		CASE WHEN dimensions->>'success' = 'true' THEN COALESCE(value_int, 0) ELSE 0 END,
		CASE WHEN dimensions->>'success' = 'false' THEN COALESCE(value_int, 0) ELSE 0 END
	FROM visible_metrics
	WHERE metric_name = 'codex.tool.call' AND COALESCE(dimensions->>'tool', '') <> ''
) tool_calls`
