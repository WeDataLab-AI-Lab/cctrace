package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

const sessionFactToolResultsSQL = `WITH calls AS (
	SELECT * FROM unnest($2::text[],$3::text[]) AS c(source_file,call_id)
)
	SELECT r.id,r.source_file,e.b->>'tool_use_id',
	left(COALESCE(CASE jsonb_typeof(e.b->'content')
		WHEN 'string' THEN e.b->>'content'
		WHEN 'array' THEN (SELECT string_agg(x->>'text',E'\n') FROM jsonb_array_elements(e.b->'content') x
			WHERE x->>'type' IN ('text','input_text','output_text'))
	END,''),65536),
	octet_length(COALESCE(CASE jsonb_typeof(e.b->'content')
		WHEN 'string' THEN e.b->>'content'
		WHEN 'array' THEN (SELECT string_agg(x->>'text',E'\n') FROM jsonb_array_elements(e.b->'content') x
			WHERE x->>'type' IN ('text','input_text','output_text'))
	END,''))>65536
FROM visible_session_records r
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'message'->'content')='array'
	THEN r.raw->'message'->'content' ELSE '[]'::jsonb END) AS e(b)
JOIN calls c ON c.source_file=r.source_file AND c.call_id=e.b->>'tool_use_id'
WHERE r.session_id=$1 AND r.record_type='user' AND e.b->>'type'='tool_result'
ORDER BY r.ts,r.uuid COLLATE "C",r.id
LIMIT 1000`

type sessionFactToolCallKey struct{ file, id string }

func addSessionFactClaudeToolCalls(row sessionfacts.ClaudeRow, keys map[sessionFactToolCallKey]bool) {
	if row.RecordType != "assistant" {
		return
	}
	var blocks []struct {
		ID    string          `json:"id"`
		Name  string          `json:"n"`
		Type  string          `json:"t"`
		Input json.RawMessage `json:"in"`
	}
	if json.Unmarshal(row.Blocks, &blocks) != nil {
		return
	}
	for _, block := range blocks {
		if block.Type != "tool_use" || block.Name != "Bash" || block.ID == "" {
			continue
		}
		var input map[string]json.RawMessage
		if json.Unmarshal(block.Input, &input) != nil {
			continue
		}
		var command string
		if json.Unmarshal(input["gc"], &command) == nil && command != "" {
			keys[sessionFactToolCallKey{row.SourceFile, block.ID}] = true
		}
	}
}

func addSessionFactCodexToolCalls(row sessionfacts.CodexRow, keys map[sessionFactToolCallKey]bool) {
	if row.RecordType == "tool_call" && sessionFactCodexShellTool(row.ToolName) && row.SourceFile != "" && row.ToolCallID != "" {
		keys[sessionFactToolCallKey{row.SourceFile, row.ToolCallID}] = true
	}
}

func loadSessionFactCommitResults(ctx context.Context, tx pgx.Tx, id string, in *sessionfacts.Input) error {
	keys := make(map[sessionFactToolCallKey]bool)
	for _, row := range in.Rows {
		addSessionFactClaudeToolCalls(row, keys)
	}
	return scanSessionFactSelectedResults(ctx, tx, id, "claude", keys, func(result sessionfacts.CommitResult) error {
		in.CommitResults = append(in.CommitResults, result)
		return nil
	})
}

const sessionFactCodexToolResultsSQL = `WITH calls AS (
	SELECT * FROM unnest($2::text[],$3::text[]) AS c(source_file,call_id)
)
SELECT r.id,r.source_file,r.tool_call_id,
	left(COALESCE(CASE jsonb_typeof(r.raw->'payload'->'output')
		WHEN 'string' THEN r.raw->'payload'->>'output'
		WHEN 'array' THEN (SELECT string_agg(x->>'text',E'\n') FROM jsonb_array_elements(r.raw->'payload'->'output') x
			WHERE x->>'type' IN ('text','input_text','output_text'))
	END,''),65536),
	octet_length(COALESCE(CASE jsonb_typeof(r.raw->'payload'->'output')
		WHEN 'string' THEN r.raw->'payload'->>'output'
		WHEN 'array' THEN (SELECT string_agg(x->>'text',E'\n') FROM jsonb_array_elements(r.raw->'payload'->'output') x
			WHERE x->>'type' IN ('text','input_text','output_text'))
	END,''))>65536,
	COALESCE(r.raw->'payload'->>'status','')
FROM visible_session_records r
JOIN calls c ON c.source_file=r.source_file AND c.call_id=r.tool_call_id
WHERE r.session_id=$1 AND r.agent='codex' AND r.record_type='tool_output'
ORDER BY r.ts,r.uuid COLLATE "C",r.id
LIMIT 1000`

func loadSessionFactCodexToolResults(ctx context.Context, tx pgx.Tx, id string, in *sessionfacts.Input) error {
	keys := make(map[sessionFactToolCallKey]bool)
	for _, row := range in.CodexRows {
		addSessionFactCodexToolCalls(row, keys)
	}
	return scanSessionFactSelectedResults(ctx, tx, id, "codex", keys, func(result sessionfacts.CommitResult) error {
		in.CommitResults = append(in.CommitResults, result)
		return nil
	})
}

func scanSessionFactSelectedResults(ctx context.Context, tx pgx.Tx, id, agent string, keys map[sessionFactToolCallKey]bool, visit func(sessionfacts.CommitResult) error) error {
	if len(keys) == 0 {
		return nil
	}
	ordered := make([]sessionFactToolCallKey, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].file != ordered[j].file {
			return ordered[i].file < ordered[j].file
		}
		return ordered[i].id < ordered[j].id
	})
	files, calls := make([]string, len(ordered)), make([]string, len(ordered))
	for i, key := range ordered {
		files[i], calls[i] = key.file, key.id
	}
	query := sessionFactToolResultsSQL
	if agent == "codex" {
		query = sessionFactCodexToolResultsSQL
	}
	return scanSessionFactCursor(ctx, tx, query, []any{id, files, calls}, sessionFactBatchRows, func(row pgx.Row) error {
		var result sessionfacts.CommitResult
		targets := []any{&result.SrecID, &result.SourceFile, &result.Call, &result.Text, &result.Truncated}
		if agent == "codex" {
			targets = append(targets, &result.Status)
		}
		if err := row.Scan(targets...); err != nil {
			return err
		}
		return visit(result)
	})
}

func sessionFactCodexShellTool(name string) bool {
	switch strings.ToLower(name) {
	case "exec", "exec_command", "shell", "shell_command":
		return true
	default:
		return false
	}
}
