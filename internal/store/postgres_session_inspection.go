package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Matches sessionview's legacy h: anchor byte for byte, including the raw jsonb
// representation and RFC3339Nano UTC timestamp. PostgreSQL bytea concatenation
// permits NUL separators without introducing NUL into a text value.
const sessionRecordAnchorSQL = `CASE WHEN COALESCE(uuid,'')<>'' THEN uuid ELSE 'h:'||left(encode(sha256(
 convert_to(session_id,'UTF8')||decode('00','hex')||
 convert_to(to_char(ts AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS')||
 CASE WHEN to_char(ts AT TIME ZONE 'UTC','US')='000000' THEN '' ELSE '.'||rtrim(to_char(ts AT TIME ZONE 'UTC','US'),'0') END||'Z','UTF8')||decode('00','hex')||
 convert_to(record_type,'UTF8')||decode('00','hex')||convert_to(raw::text,'UTF8')),'hex'),16) END`

// Inspectable context is taken from the selected session's collected records,
// never inferred from the repository's current files or rule-version inventory.
const sessionInspectionContextSQL = `(record_type IN ('context','developer') OR raw->>'role'='developer' OR raw->'attachment'->>'type'='instructions'
 OR left(COALESCE(raw->'attachment'->>'type',''),5)='hook_'
 OR raw->'attachment'->>'type'='async_hook_response'
 OR raw->'payload'->>'role'='developer' OR raw->'message'->>'role'='developer'
 OR raw->'payload'->>'type'='developer_instructions'
 OR (record_type='user' AND (COALESCE(raw->'message'->>'content','') LIKE '%# AGENTS.md instructions%'
 OR COALESCE(raw->'payload'->>'content','') LIKE '%# AGENTS.md instructions%')))`

// codexInjectedMessageSQL matches a user-role line Codex writes itself, such as
// <environment_context>: the same rule as sessionfacts.codexHuman, where a
// leading "<" is not a person typing unless it is a command or a <skill> call.
const codexInjectedMessageSQL = `(ltrim(COALESCE(CASE WHEN jsonb_typeof(raw->'payload'->'content')='string' THEN raw->'payload'->>'content' END,
 raw #>> '{payload,content,0,text}',''),E' \t\r\n') LIKE '<%' AND COALESCE(command_name,'')=''
 AND ltrim(COALESCE(raw #>> '{payload,content,0,text}',raw->'payload'->>'content',''),E' \t\r\n') NOT LIKE '<skill%')`

const latestSessionRequestSQL = `(record_type='user' AND NOT COALESCE(is_sidechain,false) AND NOT COALESCE(is_meta,false)
 AND NOT (COALESCE(agent,'')='codex' AND ` + codexInjectedMessageSQL + `)
 AND (COALESCE(prompt_source,'') IN ('typed','paste','queued','suggestion_accepted')
 OR (COALESCE(prompt_source,'')='' AND NOT COALESCE(raw->'message'->'content' @> '[{"type":"tool_result"}]'::jsonb,false)
 AND COALESCE(raw->'payload'->>'role','user')='user'
 AND COALESCE(raw->'message'->>'content','') NOT LIKE '%# AGENTS.md instructions%'
 AND COALESCE(raw->'payload'->>'content','') NOT LIKE '%# AGENTS.md instructions%')))`

func appendSessionInspectionFilters(filter SessionRecordFilter, conds []string, args []interface{}) ([]string, []interface{}) {
	if filter.ExactAnchor && filter.Anchor != "" {
		args = append(args, filter.Anchor)
		conds = append(conds, fmt.Sprintf("%s=$%d", sessionRecordAnchorSQL, len(args)))
		if filter.SourceFile != "" {
			args = append(args, filter.SourceFile)
			conds = append(conds, fmt.Sprintf("source_file=$%d", len(args)))
		}
	}
	if filter.ContextOnly {
		conds = append(conds, sessionInspectionContextSQL)
	}
	if filter.RelatedToolID != "" {
		args = append(args, filter.RelatedToolID)
		p := fmt.Sprintf("$%d", len(args))
		conds = append(conds, `(tool_call_id=`+p+` OR tool_use_id=`+p+` OR raw->'attachment'->>'toolUseID'=`+p+`
 OR raw->'payload'->>'call_id'=`+p+` OR EXISTS (SELECT 1 FROM jsonb_array_elements(
 CASE WHEN jsonb_typeof(raw->'message'->'content')='array' THEN raw->'message'->'content' ELSE '[]'::jsonb END) b
 WHERE b->>'id'=`+p+` OR b->>'tool_use_id'=`+p+`))`)
	}
	return conds, args
}

type sessionRecordPosition struct {
	Timestamp time.Time
	UUID      string
	ID        int64
}
type sessionRecordStart struct {
	Offset   int
	Position *sessionRecordPosition
}

// Resolves only a bounded page's start. Counting uses metadata except for an
// explicitly requested legacy hash; it does not build analysis or load a session.
func (s *PgStore) sessionRecordsStart(ctx context.Context, filter SessionRecordFilter, conds []string, args []interface{}) (sessionRecordStart, error) {
	offset := max(filter.Offset, 0)
	if filter.ExactAnchor {
		if filter.StartOffset != nil {
			*filter.StartOffset = offset
		}
		return sessionRecordStart{Offset: offset}, nil
	}
	if filter.Anchor == "" && filter.TurnKey == "" && filter.AnchorTime == nil && filter.Start != "latest-request" {
		if filter.StartOffset != nil {
			*filter.StartOffset = offset
		}
		return sessionRecordStart{Offset: offset}, nil
	}
	where := strings.Join(conds, " AND ")
	if where == "" {
		where = "TRUE"
	}
	var anchor string
	if filter.Anchor != "" {
		args = append(args, filter.Anchor)
		anchor = fmt.Sprintf("%s=$%d", sessionRecordAnchorSQL, len(args))
		if filter.SourceFile != "" {
			args = append(args, filter.SourceFile)
			anchor += fmt.Sprintf(" AND source_file=$%d", len(args))
		}
	} else if filter.TurnKey != "" {
		args = append(args, filter.SessionID, filter.TurnKey)
		sid, key := len(args)-1, len(args)
		turn := fmt.Sprintf("SELECT opener_uuid,started_at FROM session_turn_facts WHERE session_id=$%d AND turn_key=$%d", sid, key)
		anchor = `EXISTS(SELECT 1 FROM (` + turn + `) f WHERE (f.opener_uuid<>'' AND uuid=f.opener_uuid) OR (f.opener_uuid='' AND ts=f.started_at))`
	} else if filter.AnchorTime != nil {
		args = append(args, *filter.AnchorTime)
		anchor = fmt.Sprintf("ts=$%d", len(args))
	} else {
		anchor = latestSessionRequestSQL
	}
	// Resolve the target once. Its count preserves existing offset paging; the
	// body query uses this exact key instead of discarding all earlier raw rows.
	targetOrder := "ts DESC,uuid DESC,id DESC"
	if (filter.AnchorTime != nil || filter.TurnKey != "") && filter.Anchor == "" {
		targetOrder = "ts ASC,uuid ASC,id ASC"
	}
	targetQuery := `SELECT ts,uuid,id FROM visible_session_records WHERE ` + where + ` AND (` + anchor + `) ORDER BY ` + targetOrder + ` LIMIT 1`
	prefix := `WITH target AS (` + targetQuery + `)`
	if filter.Direction == "previous-request" || filter.Direction == "next-request" {
		operator, order := "<", "DESC"
		if filter.Direction == "next-request" {
			operator, order = ">", "ASC"
		}
		prefix = `WITH anchor AS (` + targetQuery + `), origin AS (SELECT r.ts,r.uuid,r.id FROM visible_session_records r,anchor a WHERE ` + where + ` AND ` + latestSessionRequestSQL + `
 AND (r.ts,r.uuid,r.id)<=(a.ts,a.uuid,a.id) ORDER BY r.ts DESC,r.uuid DESC,r.id DESC LIMIT 1), target AS (SELECT r.ts,r.uuid,r.id FROM visible_session_records r,origin o WHERE ` + where + ` AND ` + latestSessionRequestSQL + `
 AND (r.ts,r.uuid,r.id)` + operator + `(o.ts,o.uuid,o.id) ORDER BY r.ts ` + order + `,r.uuid ` + order + `,r.id ` + order + ` LIMIT 1)`
	}
	q := prefix + `
 SELECT CASE WHEN EXISTS(SELECT 1 FROM target) THEN
 (SELECT count(*) FROM visible_session_records r,target t WHERE ` + where + ` AND (r.ts,r.uuid,r.id)<(t.ts,t.uuid,t.id)) ELSE -1 END`
	// Predicates refer to unqualified columns; the target join would make ts/uuid
	// ambiguous only in predicates that refer to those fields (currently none).
	var timestamp *time.Time
	var uuid *string
	var id *int64
	var originFound bool
	destinations := []any{&offset}
	if filter.Direction != "" {
		q += `,EXISTS(SELECT 1 FROM origin)`
		destinations = append(destinations, &originFound)
	}
	q += `,(SELECT ts FROM target),(SELECT uuid FROM target),(SELECT id FROM target)`
	destinations = append(destinations, &timestamp, &uuid, &id)
	if err := s.pool.QueryRow(ctx, q, args...).Scan(destinations...); err != nil {
		return sessionRecordStart{}, err
	}
	if filter.OriginFound != nil {
		*filter.OriginFound = originFound
	}
	if offset < 0 && filter.Anchor == "" && filter.TurnKey == "" && filter.AnchorTime == nil && filter.Direction == "" {
		offset = 0
	}
	if filter.StartOffset != nil {
		*filter.StartOffset = offset
	}
	start := sessionRecordStart{Offset: offset}
	if timestamp != nil && uuid != nil && id != nil {
		start.Position = &sessionRecordPosition{Timestamp: *timestamp, UUID: *uuid, ID: *id}
	}
	return start, nil
}

// Keep offset metadata for compatible paging, but read large-session bodies only
// after the already resolved key. No discarded raw rows participate in the sort.
func sessionRecordPageStart(start sessionRecordStart, args []interface{}) (string, []interface{}, int) {
	if start.Position == nil {
		return "", args, start.Offset
	}
	n := len(args) + 1
	args = append(args, start.Position.Timestamp, start.Position.UUID, start.Position.ID)
	return fmt.Sprintf("(ts,uuid,id)>=($%d,$%d,$%d)", n, n+1, n+2), args, 0
}
