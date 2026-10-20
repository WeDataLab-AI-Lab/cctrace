package store

// Only normalized metadata is cached. Source content is fetched on selection.
var sessionTraceMigrations = []string{
	`CREATE INDEX IF NOT EXISTS idx_trace_source_scope_time ON session_records(session_id,profile_email,user_id,(COALESCE(account_id,'')),(COALESCE(agent,'claude')),(COALESCE(source_file,'')),ts,uuid,id)`,
	`CREATE INDEX IF NOT EXISTS idx_trace_tool_use_ids ON session_records USING gin ((jsonb_path_query_array(CASE WHEN jsonb_typeof(raw->'message'->'content')='array' THEN raw->'message'->'content' ELSE '[]'::jsonb END, '$[*] ? (@.type == "tool_use").id')))`,
	`CREATE INDEX IF NOT EXISTS idx_trace_tool_call_id ON session_records(session_id,tool_call_id) WHERE record_type='tool_call'`,
	`CREATE INDEX IF NOT EXISTS idx_trace_native_request_ref ON session_records(session_id,profile_email,user_id,source_file,(raw->>'id')) WHERE record_type='api-request'`,
	`CREATE INDEX IF NOT EXISTS idx_trace_native_blob_ref ON session_records(session_id,profile_email,user_id,source_file,(raw->>'hash')) WHERE record_type='api-request-blob'`,
	`CREATE INDEX IF NOT EXISTS idx_trace_native_shape_ref ON session_records(session_id,profile_email,user_id,source_file,(raw->>'shapeHash')) WHERE record_type='api-request-shape'`,
	`CREATE INDEX IF NOT EXISTS idx_trace_response_ref ON session_records(session_id,profile_email,user_id,source_file,(raw->>'requestRef')) WHERE raw ? 'requestRef'`,
	`CREATE INDEX IF NOT EXISTS idx_trace_transcript_refs ON session_records USING gin ((raw->'cctraceReferences') jsonb_path_ops) WHERE raw ? 'cctraceReferences'`,
	`CREATE TABLE IF NOT EXISTS session_trace_snapshots (
 snapshot_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, scope_key TEXT NOT NULL, access JSONB NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('overview','range')), range_from TIMESTAMPTZ, range_to TIMESTAMPTZ,
 source_revision BIGINT NOT NULL, policy_epoch BIGINT NOT NULL, rules_version INTEGER NOT NULL,mapping_stamp TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'building', coverage TEXT NOT NULL DEFAULT 'partial', reason TEXT,
 processed_rows BIGINT NOT NULL DEFAULT 0, after_key JSONB NOT NULL DEFAULT '{}',
 from_ts TIMESTAMPTZ,to_ts TIMESTAMPTZ,coverage_from TIMESTAMPTZ,coverage_to TIMESTAMPTZ, events_count BIGINT NOT NULL DEFAULT 0,
 bins JSONB NOT NULL DEFAULT '[]',created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE NULLS NOT DISTINCT(session_id,scope_key,mode,range_from,range_to,source_revision,policy_epoch,rules_version,mapping_stamp))`,
	`CREATE INDEX IF NOT EXISTS idx_trace_snapshot_lookup ON session_trace_snapshots(session_id,scope_key,created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS session_trace_leases (
 lease_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, scope_key TEXT NOT NULL,
 snapshot_id TEXT NOT NULL REFERENCES session_trace_snapshots(snapshot_id) ON DELETE CASCADE,
 expires_at TIMESTAMPTZ NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_trace_live_lease ON session_trace_leases(expires_at,snapshot_id)`,
	`CREATE TABLE IF NOT EXISTS session_trace_events (
 snapshot_id TEXT NOT NULL REFERENCES session_trace_snapshots(snapshot_id) ON DELETE CASCADE,
 event_id TEXT NOT NULL,kind TEXT NOT NULL,ts TIMESTAMPTZ NOT NULL,started_at TIMESTAMPTZ,ended_at TIMESTAMPTZ,
 turn_key TEXT,source_ref TEXT NOT NULL, event JSONB NOT NULL, metadata JSONB NOT NULL,
 correlation_key TEXT NOT NULL DEFAULT '',related_keys TEXT[] NOT NULL DEFAULT '{}',source_key JSONB NOT NULL,
 PRIMARY KEY(snapshot_id,event_id))`,
	`CREATE INDEX IF NOT EXISTS idx_trace_event_time ON session_trace_events(snapshot_id,ts,event_id)`,
	`CREATE INDEX IF NOT EXISTS idx_trace_event_span ON session_trace_events(snapshot_id,started_at,ended_at) WHERE started_at IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_trace_event_correlation ON session_trace_events(snapshot_id,correlation_key)`,
	`CREATE INDEX IF NOT EXISTS idx_trace_event_related ON session_trace_events USING gin (related_keys)`,
	`CREATE INDEX IF NOT EXISTS idx_trace_event_turn ON session_trace_events(snapshot_id,turn_key,ts,event_id)`,
}
