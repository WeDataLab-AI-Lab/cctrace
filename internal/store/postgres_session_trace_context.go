package store

import (
	"context"
	"encoding/json"
)

const traceContextRecordKey = `md5(jsonb_build_array(r.uuid,r.id,r.record_type,r.profile_email)::text)`

func traceContextKeySelect(canonical bool) string {
	columns := `r.ts,r.uuid,r.id,r.record_type,r.profile_email,` + traceContextRecordKey + ` AS record_key`
	var query string
	if canonical {
		// The mapping stamp has already fenced this exact published membership.
		// Its turn/time index pages original keys without scanning conversation raw.
		query = `SELECT ` + columns + ` FROM session_timeline_members m JOIN visible_session_records r
   ON r.session_id=m.session_id AND r.ts=m.source_ts AND r.uuid=m.source_uuid AND r.id=m.source_record_id
   AND r.record_type=m.source_record_type AND r.profile_email=m.source_profile_email
   WHERE m.session_id=$4 AND m.turn_key=$3 AND $1::text<>'' AND $2::text=''`
	} else {
		query = `WITH selected_sources AS (
   SELECT DISTINCT source FROM session_trace_events e CROSS JOIN LATERAL jsonb_array_elements(e.source_key) source
   WHERE e.snapshot_id=$1 AND (($2::text<>'' AND e.event_id=$2) OR ($3::text<>'' AND e.turn_key=$3))
  ) SELECT DISTINCT ` + columns + ` FROM selected_sources s JOIN visible_session_records r ON r.session_id=$4
   AND r.ts=(s.source->>'TS')::timestamptz AND COALESCE(r.uuid,'')=s.source->>'UUID' AND r.id=(s.source->>'ID')::bigint
   AND r.record_type=s.source->>'RecordType' AND COALESCE(r.source_file,'')=s.source->>'SourceFile'
   AND COALESCE(r.agent,'claude')=s.source->>'Agent' AND COALESCE(r.agent_id,'')=s.source->>'AgentID' AND COALESCE(r.branch,'')=s.source->>'Branch'
   AND r.profile_email IS NOT DISTINCT FROM ((s.source->>'SourceScope')::jsonb)->>'profile_email'
   AND r.user_id IS NOT DISTINCT FROM ((s.source->>'SourceScope')::jsonb)->>'user_id'
   AND r.billing_provider IS NOT DISTINCT FROM ((s.source->>'SourceScope')::jsonb)->>'billing_provider'
   AND r.account_id IS NOT DISTINCT FROM ((s.source->>'SourceScope')::jsonb)->>'account_id' WHERE TRUE`
	}
	return query + ` AND ` + ConversationProjectionPredicate() + `
  AND ($7::text='' OR r.user_id=$7) AND ($8::text='' OR r.profile_email=$8) AND ($9::text='' OR r.login_email=$9)`
}

func (s *PgStore) TraceContext(parent context.Context, id string, a SessionAnalysisAccess, q TraceContextRequest) (TraceContextResponse, error) {
	if (q.EventID == "") == (q.TurnKey == "") {
		return TraceContextResponse{}, ErrTraceInvalid
	}
	ctx, cancel := traceBudget(parent)
	defer cancel()
	tx, cleanup, err := s.turnContextTx(ctx, id)
	if err != nil {
		return TraceContextResponse{}, err
	}
	defer cleanup()
	v, err := loadTraceSnapshot(ctx, tx, id, a, q.SnapshotID)
	if err != nil {
		return TraceContextResponse{}, err
	}
	out := TraceContextResponse{SessionID: id, SnapshotID: v.ID, Records: []*SessionRecord{}}
	if q.EventID != "" {
		out.EventID = &q.EventID
	}
	selection := q.EventID + "/" + q.TurnKey
	var after traceCursor
	if q.Cursor != "" {
		after, err = decodeTraceCursor(q.Cursor, v.ID, traceScope(a), selection)
		if err != nil {
			return out, err
		}
		if after.Generation != v.After.ResumeGeneration {
			return out, ErrTraceSnapshot
		}
	}
	if v.State != "ready" {
		return out, tx.Commit(ctx)
	}
	canonical := q.TurnKey != "" && v.MappingStamp != ""
	var selected bool
	if canonical {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_timeline_members WHERE session_id=$1 AND turn_key=$2)`, id, q.TurnKey).Scan(&selected)
	} else {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_trace_events WHERE snapshot_id=$1 AND (($2::text<>'' AND event_id=$2) OR ($3::text<>'' AND turn_key=$3)))`, v.ID, q.EventID, q.TurnKey).Scan(&selected)
	}
	if err != nil {
		return out, err
	}
	if !selected {
		return out, ErrSessionAnalysisNotFound
	}
	args := []any{v.ID, q.EventID, q.TurnKey, id, after.Key, after.TS, a.UserID, a.ProfileEmail, a.LoginEmail}
	keys := traceContextKeySelect(canonical)
	if q.Cursor != "" {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(`+keys+` AND r.ts=$6::timestamptz AND `+traceContextRecordKey+`=$5::text)`, args...).Scan(&exists)
		if err != nil {
			return out, err
		}
		if !exists {
			return out, ErrTraceInvalid
		}
	}
	// Raw is constructed only after metadata key pagination. Neither DISTINCT nor
	// sorting sees the conversation body, and the 101st key is metadata only.
	rows, err := tx.Query(ctx, `WITH selected_keys AS MATERIALIZED (`+keys+`
  AND ($5::text='' OR (r.ts,`+traceContextRecordKey+`)>($6::timestamptz,$5)) ORDER BY r.ts,record_key LIMIT 101
 ) SELECT to_jsonb(r),k.ts,k.record_key,(SELECT count(*)>100 FROM selected_keys)
 FROM selected_keys k JOIN visible_session_records r ON r.session_id=$4 AND r.ts=k.ts AND r.uuid=k.uuid AND r.id=k.id
 AND r.record_type=k.record_type AND r.profile_email=k.profile_email
 AND ($7::text='' OR r.user_id=$7) AND ($8::text='' OR r.profile_email=$8) AND ($9::text='' OR r.login_email=$9) ORDER BY k.ts,k.record_key LIMIT 100`, args...)
	if err != nil {
		return out, err
	}
	var last traceCursor
	var more bool
	for rows.Next() {
		var raw []byte
		var c traceCursor
		if err = rows.Scan(&raw, &c.TS, &c.Key, &more); err != nil {
			rows.Close()
			return out, err
		}
		var record SessionRecord
		if err = json.Unmarshal(raw, &record); err != nil {
			rows.Close()
			return out, err
		}
		out.Records = append(out.Records, &record)
		last = c
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	if more {
		last.Snapshot = v.ID
		last.Scope = traceScope(a)
		last.Selection = selection
		last.Generation = v.After.ResumeGeneration
		cursor := encodeTraceCursor(last)
		out.NextCursor = &cursor
	}
	return out, tx.Commit(ctx)
}

var _ TraceStore = (*PgStore)(nil)
