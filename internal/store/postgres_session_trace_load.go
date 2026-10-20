package store

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"cctrace/internal/sessionfacts"

	"cctrace/internal/sessiontrace"

	"github.com/jackc/pgx/v5"
)

// Projection includes structural flags and exact keys only. No message text,
// thinking, tool input, command arguments, result content, or output crosses the
// DB boundary for trace calculation. Original array ordinal is retained.
const traceBlocksProjection = `COALESCE((SELECT jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
 'index',b.ordinality-1,'type',b.value->>'type','id',b.value->>'id','name',b.value->>'name',
 'tool_use_id',b.value->>'tool_use_id','is_error',CASE WHEN jsonb_typeof(b.value->'is_error')='boolean' THEN b.value->'is_error' END,
 'status',b.value->>'status','has_text',COALESCE(jsonb_typeof(b.value->'text')='string' AND btrim(b.value->>'text')<>'',false),'child_agent_id',b.value->>'child_agent_id')) ORDER BY b.ordinality)
 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(r.raw->'message'->'content')='array' THEN r.raw->'message'->'content'
 WHEN jsonb_typeof(r.raw->'payload'->'content')='array' THEN r.raw->'payload'->'content' ELSE '[]'::jsonb END) WITH ORDINALITY b(value,ordinality)), '[]'::jsonb)`

var traceRecordColumns = `r.id,r.ts,COALESCE(r.uuid,''),r.record_type,r.profile_email,COALESCE(r.agent,'claude'),COALESCE(r.source_file,''),COALESCE(r.agent_id,''),COALESCE(r.branch,''),
 jsonb_build_object('profile_email',r.profile_email,'user_id',r.user_id,'billing_provider',r.billing_provider,'account_id',r.account_id)::text,
 COALESCE(r.tool_use_id,''),COALESCE(r.codex_turn_id,''),COALESCE(r.codex_root_turn_id,''),
 COALESCE((SELECT m.turn_key FROM session_timeline_members m JOIN session_timeline_provenance p USING(session_id)
 WHERE m.session_id=r.session_id AND m.source_ts=r.ts AND m.source_uuid=r.uuid AND m.source_record_type=r.record_type
 AND m.source_profile_email=r.profile_email AND m.source_record_id=r.id AND p.source_revision=$8 AND p.policy_epoch=$9 AND p.tracking='tracked' AND p.timeline_state IN ('observed','undeterminable')
 AND octet_length(p.timeline_hash)=32 AND octet_length(p.fact_output_hash)=32 AND p.source_generation>0
 AND p.mapping_version=` + strconv.Itoa(sessionTimelineMappingVersion) + ` AND p.rules_version=` + strconv.Itoa(sessionfacts.RulesVersion) + ` LIMIT 1),''),
 COALESCE(r.raw->'message'->>'role',r.raw->'payload'->>'role',''),COALESCE(r.raw->'payload'->>'type',''),
 COALESCE(NULLIF(r.tool_name,''),r.raw->'payload'->>'name',''),COALESCE(NULLIF(r.tool_call_id,''),r.raw->'payload'->>'call_id',''),
 COALESCE(r.raw->'payload'->>'status',''),COALESCE((jsonb_typeof(r.raw->'message'->'content')='string' AND btrim(r.raw->'message'->>'content')<>'') OR (jsonb_typeof(r.raw->'payload'->'content')='string' AND btrim(r.raw->'payload'->>'content')<>''),false),
 (r.is_meta OR NOT (` + ConversationProjectionPredicate() + `)),r.is_compact_summary, COALESCE(r.raw->>'redacted','')<>'' OR COALESCE(r.raw->'cctraceWithheld','[]'::jsonb)<>'[]'::jsonb OR COALESCE(r.raw->'cctraceDropped','[]'::jsonb)<>'[]'::jsonb,
 COALESCE(r.raw->>'parent_tool_use_id',''),COALESCE(r.raw->>'parent_agent_id',''),COALESCE(r.raw->>'parent_source_file',''),
 COALESCE(r.raw->'toolUseResult'->>'agentId',r.raw->'toolUseResult'->>'agent_id',r.raw->>'child_agent_id',''),` + traceBlocksProjection

// Timestamp-less bookkeeping carries the epoch placeholder. Reuse the basic
// conversation exclusion policy before bounds and input pagination alike.
func traceSourcePredicate() string {
	return `r.ts > 'epoch'::timestamptz AND (` + ConversationProjectionPredicate() + `)`
}

func loadTraceRecords(ctx context.Context, tx pgx.Tx, v traceSnapshot, limit int) ([]sessiontrace.Record, traceSourceKey, error) {
	from, to := v.From, v.To
	after := v.After
	// authorizeTrace has already checked every source's exclusion/ownership in
	// this transaction. Stage metadata keys from that session before touching raw
	// or expanding the visibility anti-joins. Otherwise the planner can sort all
	// remaining rows and detoast their bodies before LIMIT (249848 rows/7.48s on
	// the large fixture). The bounded lateral lookup still enforces the view.
	// Full tuple ordering is required: id alone is not unique on the hypertable.
	rows, err := tx.Query(ctx, `WITH trace_keys AS MATERIALIZED (
 SELECT r.id AS source_id,r.ts AS source_ts,r.uuid AS source_uuid,r.record_type AS source_type,r.profile_email AS source_profile
 FROM session_records r WHERE r.session_id=$1 AND `+traceSourcePredicate()+`
 AND ($12::text='' OR r.user_id=$12) AND ($13::text='' OR r.profile_email=$13) AND ($14::text='' OR r.login_email=$14)
 AND ($2::timestamptz IS NULL OR r.ts >= $2) AND ($3::timestamptz IS NULL OR r.ts < $3)
 AND ($4::timestamptz IS NULL OR (r.ts,r.uuid COLLATE "C",r.id,r.record_type COLLATE "C",r.profile_email COLLATE "C") >
 ($4,$5::text COLLATE "C",$6::bigint,$10::text COLLATE "C",$11::text COLLATE "C"))
 ORDER BY r.ts,r.uuid COLLATE "C",r.id,r.record_type COLLATE "C",r.profile_email COLLATE "C" LIMIT $7
 ) SELECT `+traceRecordColumns+` FROM trace_keys k JOIN LATERAL (
 SELECT r.* FROM visible_session_records r WHERE r.session_id=$1 AND r.ts=k.source_ts AND r.uuid=k.source_uuid
 AND r.id=k.source_id AND r.record_type=k.source_type AND r.profile_email=k.source_profile LIMIT 1
 ) r ON true ORDER BY r.ts,r.uuid COLLATE "C",r.id,r.record_type COLLATE "C",r.profile_email COLLATE "C"`, v.Session, from, to,
		traceAfterTime(after), after.UUID, after.ID, limit, v.Revision, v.Epoch, after.Type, after.Profile, v.Access.UserID, v.Access.ProfileEmail, v.Access.LoginEmail)
	if err != nil {
		return nil, after, err
	}
	return collectTraceRecords(rows, v.Session, after)
}
func collectTraceRecords(rows pgx.Rows, session string, after traceSourceKey) ([]sessiontrace.Record, traceSourceKey, error) {
	defer rows.Close()
	records := make([]sessiontrace.Record, 0)
	var err error

	for rows.Next() {
		var r sessiontrace.Record
		var blocks []byte
		var profile string
		err = rows.Scan(&r.ID, &r.TS, &r.UUID, &r.RecordType, &profile, &r.Agent, &r.SourceFile, &r.AgentID, &r.Branch, &r.SourceScope,
			&r.ToolUseID, &r.CodexTurnID, &r.CodexRootTurnID, &r.TurnKey, &r.Role, &r.PayloadType, &r.ToolName, &r.ToolCallID, &r.ToolStatus, &r.HasText,
			&r.IsMeta, &r.IsCompactSummary, &r.Redacted, &r.ParentToolUseID, &r.ParentAgentID, &r.ParentSourceFile, &r.ChildAgentID, &blocks)
		if err != nil {
			return nil, after, err
		}
		r.SessionID = session
		if err = json.Unmarshal(blocks, &r.Blocks); err != nil {
			return nil, after, err
		}
		if len(r.Blocks) == 0 {
			r.Blocks = nil
		}
		records = append(records, r)
		after.TS, after.UUID, after.ID, after.Type, after.Profile = r.TS, r.UUID, r.ID, r.RecordType, profile
	}
	return records, after, rows.Err()
}
func traceAfterTime(k traceSourceKey) *time.Time {
	if k.TS.IsZero() {
		return nil
	}
	return &k.TS
}
