package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ConversationTimeline is the whole session's shape without any body: when each
// request was asked, when tools ran and when the assistant answered. The
// conversation axis draws it before the transcript pages above the reader are
// loaded, so earlier work is visible from the first paint.
type ConversationTimeline struct {
	Base time.Time `json:"base"`
	// Exact record timestamps, so a click can open the window at that request.
	Requests []time.Time `json:"requests"`
	// Milliseconds after Base.
	Replies []int64 `json:"replies"`
	// [start ms after Base, duration ms, flags]; flags bit 0 = aside, bit 1 = error.
	Spans [][3]int64 `json:"spans"`
}

type ConversationTimelineStore interface {
	ConversationTimeline(context.Context, SessionRecordFilter) (ConversationTimeline, error)
}

type timelineSpan struct {
	start, end time.Time
	aside, err bool
}

// Calls closer than this on one lane draw as one bar; the axis cannot separate them.
const timelineMergeGap = time.Second

// Human-gated calls measure a person deciding, not work; see timeline-scale.tsx.
var timelineGatedTools = []string{"ExitPlanMode", "AskUserQuestion"}

func (s *PgStore) ConversationTimeline(parent context.Context, filter SessionRecordFilter) (ConversationTimeline, error) {
	out := ConversationTimeline{Requests: []time.Time{}, Replies: []int64{}, Spans: [][3]int64{}}
	if filter.SessionID == "" {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	conds := []string{"session_id = $1", ConversationProjectionPredicate()}
	args := []interface{}{filter.SessionID}
	for _, v := range []struct{ column, value string }{{"user_id", filter.UserID}, {"profile_email", filter.ProfileEmail}, {"login_email", filter.LoginEmail}} {
		if v.value != "" {
			args = append(args, v.value)
			conds = append(conds, fmt.Sprintf("%s = $%d", v.column, len(args)))
		}
	}
	q := timelineSQL(strings.Join(conds, " AND "))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var spans []timelineSpan
	var replies []time.Time
	for rows.Next() {
		var kind int
		var span timelineSpan
		if err := rows.Scan(&kind, &span.start, &span.end, &span.aside, &span.err); err != nil {
			return out, err
		}
		switch kind {
		case 0:
			spans = append(spans, span)
		case 1:
			replies = append(replies, span.start)
		default:
			out.Requests = append(out.Requests, span.start)
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return buildConversationTimeline(out.Requests, replies, spans), nil
}

func buildConversationTimeline(requests, replies []time.Time, spans []timelineSpan) ConversationTimeline {
	out := ConversationTimeline{Requests: requests, Replies: []int64{}, Spans: [][3]int64{}}
	if out.Requests == nil {
		out.Requests = []time.Time{}
	}
	var base time.Time
	for _, at := range append(append([]time.Time{}, requests...), replies...) {
		if base.IsZero() || at.Before(base) {
			base = at
		}
	}
	for _, span := range spans {
		if base.IsZero() || span.start.Before(base) {
			base = span.start
		}
	}
	if base.IsZero() {
		return out
	}
	out.Base = base
	ms := func(at time.Time) int64 { return at.Sub(base).Milliseconds() }
	for _, at := range replies {
		// One mark per second is all the axis can show.
		if v := ms(at); len(out.Replies) == 0 || v-out.Replies[len(out.Replies)-1] >= 1000 {
			out.Replies = append(out.Replies, v)
		}
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start.Before(spans[j].start) })
	var merged []timelineSpan
	last := map[[2]bool]int{}
	for _, span := range spans {
		key := [2]bool{span.aside, span.err}
		if i, ok := last[key]; ok && !span.start.After(merged[i].end.Add(timelineMergeGap)) {
			if span.end.After(merged[i].end) {
				merged[i].end = span.end
			}
			continue
		}
		last[key] = len(merged)
		merged = append(merged, span)
	}
	for _, span := range merged {
		var flags int64
		if span.aside {
			flags |= 1
		}
		if span.err {
			flags |= 2
		}
		out.Spans = append(out.Spans, [3]int64{ms(span.start), span.end.Sub(span.start).Milliseconds(), flags})
	}
	return out
}

// timelineSQL reads only timestamps, flags and tool ids; raw is touched through
// jsonpath on the rows that can hold tool blocks or text.
func timelineSQL(where string) string {
	gated := "'" + strings.Join(timelineGatedTools, "','") + "'"
	return `WITH r AS MATERIALIZED (SELECT ts, record_type, agent, raw, tool_call_id, tool_name, command_name, is_sidechain, is_meta, prompt_source, COALESCE(is_sidechain,false) AS side
   FROM visible_session_records WHERE ` + where + ` AND (record_type IN ('assistant','user','tool_call','tool_output'))),
 starts AS (
   SELECT b->>'id' AS k, ts, side, b->>'name' AS name FROM r, jsonb_path_query(r.raw, '$.message.content[*] ? (@.type == "tool_use")') b WHERE record_type='assistant'
   UNION ALL SELECT tool_call_id, ts, side, tool_name FROM r WHERE record_type='tool_call' AND tool_call_id<>''),
 ends AS (
   SELECT b->>'tool_use_id' AS k, max(ts) AS ts, bool_or(COALESCE((b->>'is_error')::boolean,false)) AS err
     FROM r, jsonb_path_query(r.raw, '$.message.content[*] ? (@.type == "tool_result")') b WHERE record_type='user' GROUP BY 1
   UNION ALL SELECT tool_call_id, max(ts), false FROM r WHERE record_type='tool_output' AND tool_call_id<>'' GROUP BY 1)
 SELECT 0 AS kind, s.ts, CASE WHEN s.name IN (` + gated + `) OR e.ts IS NULL OR e.ts < s.ts THEN s.ts ELSE e.ts END, s.side, COALESCE(e.err,false)
   FROM starts s LEFT JOIN ends e ON e.k = s.k
 UNION ALL SELECT 1, ts, ts, false, false FROM r WHERE record_type='assistant' AND NOT side
   AND (agent NOT IN ('','claude') OR r.raw @? '$.message.content[*] ? (@.type == "text")')
 UNION ALL SELECT 2, ts, ts, false, false FROM r WHERE ` + latestSessionRequestSQL + `
 ORDER BY 2`
}
