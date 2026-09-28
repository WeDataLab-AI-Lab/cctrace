package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Every tool read shares the caller and week boundary, including ID lookups.
// Facts are not a visibility view: check their boundary record explicitly.
const aiSegmentWhere = `f.start_ts >= $1 AND f.start_ts < $2
 AND (($3 <> '' AND f.user_id=$3) OR ($4 <> '' AND f.profile_email=$4))
 AND EXISTS (SELECT 1 FROM visible_session_records vsr WHERE vsr.id=f.boundary_record_id)`
const aiSegmentColumns = `f.boundary_record_id,f.session_id,f.start_ts,f.end_ts,f.project_hash,
 COALESCE((SELECT MAX(p.project_name) FROM projects p WHERE p.agent=f.agent AND p.project_hash=f.project_hash),''),
 f.agent,f.typed_turn_count,f.tool_call_count,f.tool_fail_count,f.command_count,f.had_compact,
 ` + segmentToolEvidenceColumns

func scanAISegments(rows pgx.Rows) ([]AISegment, error) {
	defer rows.Close()
	result := []AISegment{}
	for rows.Next() {
		var v AISegment
		if err := rows.Scan(&v.ID, &v.SessionID, &v.StartTs, &v.EndTs, &v.ProjectHash, &v.ProjectName, &v.Agent, &v.TypedTurnCount, &v.ToolCallCount, &v.ToolFailCount, &v.CommandCount, &v.HadCompact, &v.ToolEvidence, &v.ToolOutcomeEvidence); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *PgStore) AIWeekSegments(ctx context.Context, sc AISegmentScope, f AISegmentFilter) ([]AISegment, error) {
	order := "f.start_ts"
	switch f.OrderBy {
	case AISegmentOrderTypedTurnCount:
		order = "f.typed_turn_count"
	case AISegmentOrderToolFailCount:
		order = "f.tool_fail_count"
	}
	limit := f.Limit
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+aiSegmentColumns+` FROM task_segment_facts f WHERE `+aiSegmentWhere+`
 AND ($5::boolean IS NULL OR f.had_compact=$5) AND f.tool_fail_count >= $6 AND f.typed_turn_count >= $7 AND ($8='' OR f.agent=$8)
 ORDER BY `+order+` DESC,f.boundary_record_id DESC LIMIT $9`, sc.Since, sc.Until, sc.UserID, sc.ProfileEmail, f.HadCompact, f.MinToolFail, f.MinTypedTurns, f.Agent, limit)
	if err != nil {
		return nil, err
	}
	return scanAISegments(rows)
}

func (s *PgStore) AISegmentsByIDs(ctx context.Context, sc AISegmentScope, ids []int64) (map[int64]AISegment, error) {
	result := map[int64]AISegment{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT `+aiSegmentColumns+` FROM task_segment_facts f WHERE `+aiSegmentWhere+` AND f.boundary_record_id=ANY($5::bigint[])`, sc.Since, sc.Until, sc.UserID, sc.ProfileEmail, ids)
	if err != nil {
		return nil, err
	}
	segments, err := scanAISegments(rows)
	if err != nil {
		return nil, err
	}
	for _, v := range segments {
		result[v.ID] = v
	}
	return result, nil
}

func (s *PgStore) AISegmentConversation(ctx context.Context, sc AISegmentScope, id int64, offset, limit int) ([]*SessionRecord, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, `SELECT sr.ts,sr.session_id,sr.uuid,sr.record_type,sr.agent,sr.user_id,sr.profile_email,sr.raw,sr.tool_use_id,sr.is_compact_summary,sr.is_meta
 FROM task_segment_facts f JOIN visible_session_records sr ON sr.session_id=f.session_id
 AND sr.ts>=f.start_ts AND (f.end_ts IS NULL OR sr.ts<f.end_ts)
 WHERE `+aiSegmentWhere+` AND f.boundary_record_id=$5
 AND (($3 <> '' AND sr.user_id=$3) OR ($4 <> '' AND sr.profile_email=$4))
 ORDER BY sr.ts,sr.uuid,sr.id LIMIT $6 OFFSET $7`, sc.Since, sc.Until, sc.UserID, sc.ProfileEmail, id, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*SessionRecord{}
	for rows.Next() {
		r := &SessionRecord{}
		if err = rows.Scan(&r.Ts, &r.SessionID, &r.UUID, &r.RecordType, &r.Agent, &r.UserID, &r.ProfileEmail, &r.Raw, &r.ToolUseID, &r.IsCompactSummary, &r.IsMeta); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *PgStore) AIWeekAggregate(ctx context.Context, sc AISegmentScope) (*AIWeekAggregate, error) {
	a := &AIWeekAggregate{}
	err := s.pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT f.session_id),COALESCE(sum(f.typed_turn_count),0),COALESCE(sum(f.tool_call_count),0),
 COALESCE(sum(f.tool_call_count) FILTER (WHERE `+segmentToolOutcomeEvidenceSQL+`),0),
 COALESCE(sum(f.tool_fail_count),0),COALESCE(sum(f.command_count),0),count(*) FILTER (WHERE f.had_compact)
 FROM task_segment_facts f WHERE `+aiSegmentWhere, sc.Since, sc.Until, sc.UserID, sc.ProfileEmail).Scan(&a.SegmentCount, &a.SessionCount, &a.TypedTurnCount, &a.ToolCallCount, &a.ToolOutcomeObservedCount, &a.ToolFailCount, &a.CommandCount, &a.CompactedSegmentCount)
	if err != nil {
		return nil, err
	}
	return a, nil
}
