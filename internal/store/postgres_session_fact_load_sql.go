package store

import (
	"fmt"
	"strings"
)

// The content projection is deliberately a bounded summary, never the raw
// transcript or a Write/Edit input body.
const sessionFactClaudeSlimSQL = `
SELECT sr.id, sr.ts, sr.record_type, sr.uuid, sr.parent_uuid, sr.is_sidechain, sr.agent_id,
 sr.source_file, sr.tool_use_id, sr.is_compact_summary, sr.is_meta, sr.prompt_source, sr.entrypoint,
 sr.command_name, sr.command_invoke, sr.model, sr.forked_from_session,
 sr.user_id, sr.profile_email, COALESCE(sr.login_email,''), sr.login_email_source,
 sr.billing_provider, sr.account_id, sr.project_hash, sr.repository_id, sr.repo_subpath, sr.branch,
 sr.task_type, COALESCE(sr.message_id,''), sr.raw->>'cwd',
 sr.raw->'cctraceCollect', sr.raw->'cctraceDropped', sr.raw->'cctraceWithheld',
 sr.raw->>'promptId', sr.raw->'origin'->>'kind', sr.raw->>'turnOrigin', sr.raw->'turnPosition',
 sr.raw->>'permissionMode', sr.raw->>'toolDenialKind', sr.raw->>'interruptedMessageId',
 sr.raw->'imagePasteCount', sr.raw->'toolUseResult', sr.raw->'isApiErrorMessage',
 sr.raw ? 'error', sr.raw->>'redacted', sr.raw->>'subtype', sr.raw->>'logicalParentUuid',
 sr.raw->'pendingBackgroundAgentCount', sr.raw->'pendingWorkflowCount', sr.raw->'attachment',
 jsonb_typeof(sr.raw->'message'->'content'),
 CASE WHEN sr.record_type = 'user' THEN left(COALESCE(
   CASE WHEN jsonb_typeof(sr.raw->'message'->'content') = 'string' THEN sr.raw->'message'->>'content' END,
   (SELECT b->>'text' FROM jsonb_array_elements(
     CASE WHEN jsonb_typeof(sr.raw->'message'->'content') = 'array'
       THEN sr.raw->'message'->'content' ELSE '[]'::jsonb END) b
    WHERE b->>'type' IN ('text','input_text') LIMIT 1)), 512) END,
 CASE WHEN jsonb_typeof(sr.raw->'message'->'content') = 'array' THEN (
  SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
   't', b->>'type', 'id', b->>'id', 'n', b->>'name', 'tu', b->>'tool_use_id', 'err', b->'is_error',
   'tk', CASE WHEN b->>'type'='tool_result' THEN substring(left(CASE jsonb_typeof(b->'content')
     WHEN 'string' THEN b->>'content'
     WHEN 'array' THEN (SELECT string_agg(x->>'text', E'\n') FROM jsonb_array_elements(b->'content') x WHERE x->>'type'='text')
   END,64) FROM '^Task #([0-9]+) created') END,
   'in', CASE b->>'name'
    WHEN 'Edit' THEN jsonb_build_object('fp', b->'input'->'file_path')
    WHEN 'Write' THEN jsonb_build_object('fp', b->'input'->'file_path')
    WHEN 'MultiEdit' THEN jsonb_build_object('fp', b->'input'->'file_path')
    WHEN 'NotebookEdit' THEN jsonb_build_object('fp', b->'input'->'notebook_path')
    WHEN 'Bash' THEN jsonb_build_object('bg', b->'input'->'run_in_background',
      'gc', CASE WHEN b->'input'->>'command' ~ '(^|[^[:alnum:]_.-])(git|gh)([[:space:]]|$)'
        AND b->'input'->>'command' ~ '(commit|cherry-pick|revert|merge|pull|gh[[:space:]]+(issue|pr|api)[[:space:]])'
        THEN left(b->'input'->>'command', 8192) END,
      'hid', jsonb_typeof(b->'input') = 'string')
    WHEN 'Agent' THEN jsonb_build_object('bg', b->'input'->'run_in_background')
    WHEN 'Task' THEN jsonb_build_object('bg', b->'input'->'run_in_background')
    WHEN 'AskUserQuestion' THEN jsonb_build_object('nq', jsonb_array_length(
      CASE WHEN jsonb_typeof(b->'input'->'questions') = 'array' THEN b->'input'->'questions' ELSE '[]'::jsonb END),
      'h', jsonb_path_query_array(b->'input', '$.questions[*].header'))
    WHEN 'ExitPlanMode' THEN jsonb_build_object('pf', b->'input'->'planFilePath')
    WHEN 'TaskUpdate' THEN jsonb_build_object('id', b->'input'->'taskId', 'st', b->'input'->'status',
      'ed', (b->'input') ?| ARRAY['subject','description','owner','addBlocks','addBlockedBy','metadata'],
      'sub', left(b->'input'->>'subject', 200), 'desc', left(b->'input'->>'description', 200),
      'hid', jsonb_typeof(b->'input') = 'string')
    WHEN 'TaskCreate' THEN jsonb_build_object('c', true, 'sub', left(b->'input'->>'subject', 200),
      'desc', left(b->'input'->>'description', 200), 'hid', jsonb_typeof(b->'input') = 'string')
    WHEN 'TodoWrite' THEN jsonb_build_object('sts', jsonb_path_query_array(b->'input', '$.todos[*].status'),
      'tds', jsonb_path_query_array(b->'input', '$.todos[*].content'), 'hid', jsonb_typeof(b->'input') = 'string')
   END)) ORDER BY ord)
  FROM jsonb_array_elements(sr.raw->'message'->'content') WITH ORDINALITY e(b, ord)) END
FROM visible_session_records sr
WHERE sr.session_id = $1 AND COALESCE(NULLIF(sr.agent,''),'claude') = 'claude'`

const sessionFactCodexSlimSQL = `
SELECT sr.id, sr.ts, sr.record_type, sr.model,
 COALESCE(sr.input_tokens,0)::bigint, COALESCE(sr.output_tokens,0)::bigint,
 COALESCE(sr.cache_read_tokens,0)::bigint, COALESCE(sr.cache_create_tokens,0)::bigint,
 sr.user_id, sr.profile_email, COALESCE(sr.login_email,''), sr.login_email_source,
 sr.billing_provider, sr.account_id, sr.project_hash, sr.repository_id, sr.branch, sr.entrypoint,
 sr.raw->>'type', COALESCE(sr.raw->'payload'->>'role', sr.raw #>> '{payload,message,role}', sr.raw->>'role'),
 sr.command_name, sr.raw->'payload'->>'type',
 COALESCE(sr.raw->'payload'->>'turn_id', sr.raw #>> '{metadata,retained_source,id,turn_id}'),
 sr.raw->'payload'->>'root_turn_id', sr.raw->'payload'->>'response_id',
 CASE WHEN sr.record_type = 'user' THEN left(COALESCE(
   CASE WHEN jsonb_typeof(sr.raw->'payload'->'content') = 'string' THEN sr.raw->'payload'->>'content' END,
   (SELECT b->>'text' FROM jsonb_array_elements(
      CASE WHEN jsonb_typeof(sr.raw->'payload'->'content') = 'array' THEN sr.raw->'payload'->'content' ELSE '[]'::jsonb END) b
    WHERE b->>'type' IN ('text','input_text') LIMIT 1),
   sr.raw #>> '{payload,message,content,0,text}', sr.raw->'payload'->>'text',
   CASE WHEN jsonb_typeof(sr.raw->'content') = 'string' THEN sr.raw->>'content' END,
   (SELECT b->>'text' FROM jsonb_array_elements(
      CASE WHEN jsonb_typeof(sr.raw->'content') = 'array' THEN sr.raw->'content' ELSE '[]'::jsonb END) b
    WHERE b->>'type' IN ('text','input_text') LIMIT 1)), 512) END,
 CASE WHEN COALESCE(sr.raw #>> '{payload,model_context_window}', sr.raw #>> '{payload,info,model_context_window}') ~ '^[0-9]{1,9}$'
  THEN COALESCE(sr.raw #>> '{payload,model_context_window}', sr.raw #>> '{payload,info,model_context_window}')::int END,
 sr.uuid, sr.source_file, sr.tool_name, sr.tool_call_id,
	CASE WHEN sr.record_type='tool_call' AND lower(sr.tool_name) IN ('exec','exec_command','shell','shell_command')
	 THEN left(COALESCE(sr.raw->'payload'->>'arguments',sr.raw->'payload'->>'input',''),16384) ELSE '' END,
	CASE WHEN sr.record_type='tool_call' AND lower(sr.tool_name) IN ('exec','exec_command','shell','shell_command')
	 AND octet_length(COALESCE(sr.raw->'payload'->>'arguments',sr.raw->'payload'->>'input',''))>16384 THEN true ELSE false END,
	''::text,
 COALESCE(sr.raw->'payload'->>'status',''),
 COALESCE(sr.raw->>'cwd',sr.raw->'payload'->>'workdir',sr.raw->'payload'->>'cwd',''),
 sr.codex_turn_id,sr.codex_root_turn_id,sr.codex_turn_metadata_known
FROM visible_session_records sr
WHERE sr.session_id = $1 AND sr.agent = 'codex'`

const sessionFactCopyWhereSQL = `NOT (EXISTS (SELECT 1 FROM schema_backfills WHERE name = 'session_fact_copies_v1')
 AND EXISTS (SELECT 1 FROM session_record_copies c WHERE c.srec_id = sr.id))`

var sessionFactClaudeHeadsSQL = fmt.Sprintf(`SELECT h.id, h.uuid, h.ts, h.is_sidechain, h.agent_id, h.source_file, h.model,
 COALESCE(h.input_tokens,0)::bigint, COALESCE(h.output_tokens,0)::bigint,
 COALESCE(h.cache_read_tokens,0)::bigint, COALESCE(h.cache_create_tokens,0)::bigint,
 (ic.srec_id IS NOT NULL), COALESCE(ic.cost_usd,0),
 (COALESCE(%s, %s, %s) IS NOT NULL)
FROM (%s) h
LEFT JOIN LATERAL (SELECT ic.srec_id, ic.cost_usd FROM claude_imputed_cost ic
 WHERE ic.srec_id = h.id AND ic.session_id = $1 ORDER BY ic.ts DESC LIMIT 1) ic ON TRUE`, strings.ReplaceAll(claudeRateAt, "sr.", "h."), strings.ReplaceAll(claudeRateBack, "sr.", "h."), strings.ReplaceAll(claudeRateOfficial, "sr.", "h."),
	claudeRequestHeadsSQL("visible_session_records", "sr.session_id = $1 AND "+sessionFactCopyWhereSQL,
		[]string{"id", "uuid", "ts", "is_sidechain", "agent_id", "source_file", "model"}))

var sessionFactCodexHeadsSQL = fmt.Sprintf(`SELECT sr.id, ''::text, sr.ts, false, ''::text, ''::text, sr.model,
 ic.input_tokens::bigint, ic.output_tokens::bigint, ic.cache_read_tokens::bigint,
 ic.cache_create_tokens::bigint, true, ic.cost_usd,
 (rate.model_prefix IS NOT NULL)
FROM (%s) sr
JOIN visible_session_records v ON v.id = sr.id AND v.session_id = sr.session_id AND v.ts = sr.ts
JOIN codex_imputed_cost ic ON ic.srec_id = sr.id AND ic.session_id = sr.session_id
LEFT JOIN LATERAL (SELECT model_prefix FROM codex_model_rates r
 WHERE lower(sr.model) LIKE r.model_prefix || '%%' AND r.effective_from <= sr.ts::date
 ORDER BY length(r.model_prefix) DESC, r.effective_from DESC LIMIT 1) rate ON TRUE
WHERE %s`, codexUsageRowsSQL("ARRAY[$1]::text[]", "NULL::bigint"), strings.ReplaceAll(sessionFactCopyWhereSQL, "sr.id", "v.id"))

var sessionFactSnapshotVisibleSQL = `ss.session_id = $1
 AND NOT EXISTS (SELECT 1 FROM excluded_sessions e WHERE e.session_id = ss.session_id)
 AND ` + excludedAccountPredicateSQL("ss")

var sessionFactSnapshotSQL = `SELECT ss.user_id, ss.profile_email, ss.login_email, ss.billing_provider,
 ss.account_id, ss.observed_at, ss.snapshot
FROM session_snapshots ss WHERE ` + sessionFactSnapshotVisibleSQL

const sessionFactVisibleOTELSQL = `SELECT e.ts, e.prompt_id, COALESCE(e.cost_usd,0), e.user_id,
 e.profile_email, e.login_email, e.billing_provider
FROM visible_events e WHERE e.session_id = $1 AND e.event_name = 'api_request'
 AND NOT EXISTS (SELECT 1 FROM excluded_sessions x WHERE x.session_id = e.session_id)`
