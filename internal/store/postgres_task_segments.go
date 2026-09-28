package store

import (
	"context"
	"strconv"
	"time"
)

// taskSegmentsLimit caps a single call so one heavy task_type cannot return an
// unbounded response; the modal it feeds shows a period's work at a glance, not
// a paginated archive.
const taskSegmentsLimit = 500

// TaskSegmentsByType lists the work segments in [since, until) that contain at
// least one turn classified as taskType. taskType is session_records.task_type
// (the keyword classifier's label), not task_segment_facts.activity_type --
// the latter is a behavior-derived label filled by a later stage and does not
// exist yet on most rows.
func (s *PgStore) TaskSegmentsByType(ctx context.Context, since, until time.Time, taskType, profileEmail, userID string) (*TaskSegmentPage, error) {
	// Counted over the period rather than the page, so the ceiling below cannot
	// make the week look smaller than it was.
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM task_segment_facts f
	WHERE f.start_ts >= $1 AND f.start_ts < $2
	  AND (($3 <> '' AND f.user_id = $3) OR ($4 <> '' AND f.profile_email = $4))
	  AND EXISTS (SELECT 1 FROM visible_session_records vsr WHERE vsr.id = f.boundary_record_id)
	  AND EXISTS (
	    SELECT 1 FROM session_records sr
	    WHERE sr.session_id = f.session_id AND sr.task_type = $5
	      AND sr.ts >= f.start_ts AND (f.end_ts IS NULL OR sr.ts < f.end_ts)
	  )`, since, until, userID, profileEmail, taskType).Scan(&total); err != nil {
		return nil, err
	}

	// Sessions whose whole record span fits inside a second: the times on those
	// rows are when the file was written, not when the work happened (#686).
	// Computed once as a set rather than per row -- the drill-down returns up to
	// 500 of them.
	rows, err := s.pool.Query(ctx, `WITH collapsed AS (
		SELECT session_id FROM visible_session_records
		WHERE ts >= $1 AND ts < $2
		GROUP BY session_id
		HAVING count(*) >= `+strconv.Itoa(collapsedTimelineMinRecords)+`
		   AND max(ts) - min(ts) < interval '`+collapsedTimelineMaxSpan+`')
	SELECT f.session_id, f.start_ts, f.end_ts,
		f.project_hash, COALESCE(NULLIF(MAX(p.project_name), ''), ''),
		f.tool_call_count, f.tool_fail_count, f.command_count, f.input_tokens, f.output_tokens,
		`+segmentToolEvidenceColumns+`,
		EXISTS (SELECT 1 FROM collapsed c WHERE c.session_id = f.session_id) AS timeline_collapsed
	FROM task_segment_facts f
	LEFT JOIN projects p ON p.agent = f.agent AND p.project_hash = f.project_hash
	WHERE f.start_ts >= $1 AND f.start_ts < $2
	  AND (($3 <> '' AND f.user_id = $3) OR ($4 <> '' AND f.profile_email = $4))
	  AND EXISTS (SELECT 1 FROM visible_session_records vsr WHERE vsr.id = f.boundary_record_id)
	  AND EXISTS (
	    SELECT 1 FROM session_records sr
	    WHERE sr.session_id = f.session_id AND sr.task_type = $5
	      AND sr.ts >= f.start_ts AND (f.end_ts IS NULL OR sr.ts < f.end_ts)
	  )
	GROUP BY f.boundary_record_id, f.session_id, f.start_ts, f.end_ts, f.project_hash,
		f.tool_call_count, f.tool_fail_count, f.command_count, f.input_tokens, f.output_tokens
	ORDER BY f.start_ts DESC
	LIMIT $6`, since, until, userID, profileEmail, taskType, taskSegmentsLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &TaskSegmentPage{Segments: []*TaskSegment{}, Total: total}
	for rows.Next() {
		v := &TaskSegment{}
		if err := rows.Scan(&v.SessionID, &v.StartTs, &v.EndTs, &v.ProjectHash, &v.ProjectName,
			&v.ToolCallCount, &v.ToolFailCount, &v.CommandCount, &v.InputTokens, &v.OutputTokens,
			&v.ToolEvidence, &v.ToolOutcomeEvidence, &v.TimelineCollapsed); err != nil {
			return nil, err
		}
		page.Segments = append(page.Segments, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	page.Truncated = int64(len(page.Segments)) < total
	return page, nil
}
