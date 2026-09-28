package store

import "fmt"

// typedTurnPredicateSQL is "this row is a turn a person typed", as SQL over a
// session_records row aliased to alias.
//
// A plain-string check alone is wrong twice. Codex writes payload.content as an
// array and leaves message.content NULL, so a string test drops every Codex turn
// (confirmed on prod: all 50,518 Codex user rows). And array-shaped Claude
// content is mostly tool_result, but not always -- 8,375 of 370,858 carry a real
// text/input_text block. insights.IsTypedTurn is the Go side of the same rule.
//
// The same predicate is spelled out in postgres_task_segment_facts.go,
// postgres_task_types.go, postgres_plugin_usage.go and
// postgres_plugin_usage_facts.go. Those four are left alone here: converging
// load-bearing queries belongs in its own change with its own tests, not in a
// display fix. This exists so the fix does not add a fifth uncommented copy.
func typedTurnPredicateSQL(alias string) string {
	return fmt.Sprintf(`(COALESCE(NULLIF(%[1]s.agent, ''), 'claude') = 'codex'
	  OR jsonb_typeof(%[1]s.raw->'message'->'content') = 'string'
	  OR (jsonb_typeof(%[1]s.raw->'message'->'content') = 'array' AND EXISTS (
	        SELECT 1 FROM jsonb_array_elements(%[1]s.raw->'message'->'content') e
	        WHERE e->>'type' IN ('text', 'input_text'))))`, alias)
}
