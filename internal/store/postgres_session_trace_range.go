package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessiontrace"
)

// This expression inspects only the bounded time-index candidate set. It is
// intentionally not indexed globally: startup never preprocesses raw sessions.
const traceCallIDsSQL = `(jsonb_path_query_array(r.raw,'$.message.content[*] ? (@.type == "tool_use").id') || jsonb_path_query_array(r.raw,'$.message.content[*] ? (@.type == "tool_result").tool_use_id') ||
 CASE WHEN r.tool_call_id<>'' THEN jsonb_build_array(r.tool_call_id) WHEN r.raw->'payload' ? 'call_id' THEN jsonb_build_array(r.raw->'payload'->>'call_id') ELSE '[]'::jsonb END)`

func traceRangeSeed(ctx context.Context, tx pgx.Tx, v traceSnapshot) (complete, limited bool, err error) {
	var snapshot, state, coverage string
	err = tx.QueryRow(ctx, `SELECT snapshot_id,state,coverage FROM session_trace_snapshots WHERE session_id=$1 AND scope_key=$2 AND snapshot_id<>$3
 AND mapping_stamp=$7 AND mode='overview' AND source_revision=$4 AND policy_epoch=$5 AND rules_version=$6
 ORDER BY (state='ready' AND coverage='complete') DESC,created_at DESC LIMIT 1`, v.Session, v.Scope, v.ID, v.Revision, v.Epoch, TraceRulesVersion, v.MappingStamp).Scan(&snapshot, &state, &coverage)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	seedLimit := TraceChunkRows
	if v.After.ChunkLimit > 0 && v.After.ChunkLimit < seedLimit {
		seedLimit = v.After.ChunkLimit
	}
	rows, err := tx.Query(ctx, `SELECT metadata,started_at,event_id FROM session_trace_events WHERE snapshot_id=$1
 AND started_at<$3 AND (ended_at >= $2 OR ended_at IS NULL) ORDER BY started_at,event_id LIMIT $4`, snapshot, v.From, v.To, seedLimit)
	if err != nil {
		return false, false, err
	}
	events := []sessiontrace.Event{}
	var lastTS time.Time
	lastID := ""
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw, &lastTS, &lastID); err != nil {
			rows.Close()
			return false, false, err
		}
		part, err := sessiontrace.UnmarshalStorage(raw)
		if err != nil {
			rows.Close()
			return false, false, err
		}
		events = append(events, part...)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return false, false, err
	}
	if len(events) >= seedLimit {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_trace_events WHERE snapshot_id=$1 AND started_at<$3 AND (ended_at >= $2 OR ended_at IS NULL)
 AND (started_at,event_id)>($4,$5))`, snapshot, v.From, v.To, lastTS, lastID).Scan(&limited)
		if err != nil {
			return false, false, err
		}
	}
	if err = mergeTraceEvents(ctx, tx, v.ID, events); err != nil {
		return false, false, err
	}
	return state == "ready" && coverage == "complete" && !limited, limited, nil
}

// Each range boundary pass reads at most 1000 cached events and 2000 indexed
// source candidates. It never walks from the beginning of an overview. Unknown
// crossings remain partial when no complete current overview exists.
func traceRangeBoundary(ctx context.Context, tx pgx.Tx, v traceSnapshot) (after string, done bool, err error) {
	if v.After.SeedComplete {
		return v.After.BoundaryAfter, true, nil
	}
	rows, err := tx.Query(ctx, `SELECT event_id,metadata FROM session_trace_events WHERE snapshot_id=$1 AND event_id>$2 ORDER BY event_id LIMIT 1000`, v.ID, v.After.BoundaryAfter)
	if err != nil {
		return "", false, err
	}
	events := []sessiontrace.Event{}
	n := 0
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&after, &raw); err != nil {
			rows.Close()
			return after, false, err
		}
		part, err := sessiontrace.UnmarshalStorage(raw)
		if err != nil {
			rows.Close()
			return after, false, err
		}
		events = append(events, part...)
		n++
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return after, false, err
	}
	if n == 0 {
		return v.After.BoundaryAfter, true, nil
	}
	keys := map[string]bool{}
	calls := []string{}
	seen := map[string]bool{}
	for _, e := range events {
		for _, key := range sessiontrace.EventRelatedCorrelationKeys(e) {
			keys[key] = true
		}
		for _, id := range sessiontrace.EventCallIDs(e) {
			if id != "" && !seen[id] {
				calls = append(calls, id)
				seen[id] = true
			}
		}

	}
	if len(calls) == 0 {
		return after, n < 1000, nil
	}
	sourceRows, err := tx.Query(ctx, `WITH boundary_candidates AS MATERIALIZED (
 (SELECT r.* FROM visible_session_records r WHERE r.session_id=$1 AND `+traceSourcePredicate()+`
 AND ($4::text='' OR r.user_id=$4) AND ($5::text='' OR r.profile_email=$5) AND ($6::text='' OR r.login_email=$6)
 AND r.ts < $8 ORDER BY r.ts DESC,r.uuid DESC LIMIT 1000)
 UNION ALL
 (SELECT r.* FROM visible_session_records r WHERE r.session_id=$1 AND `+traceSourcePredicate()+`
 AND ($4::text='' OR r.user_id=$4) AND ($5::text='' OR r.profile_email=$5) AND ($6::text='' OR r.login_email=$6)
 AND r.ts >= $9 ORDER BY r.ts,r.uuid LIMIT 1000)
 ) SELECT `+strings.NewReplacer("$8", "$2", "$9", "$3").Replace(traceRecordColumns)+` FROM boundary_candidates r
 WHERE `+traceCallIDsSQL+` ?| $7::text[] ORDER BY r.ts,r.uuid COLLATE "C",r.id`, v.Session, v.Revision, v.Epoch, v.Access.UserID, v.Access.ProfileEmail, v.Access.LoginEmail, calls, v.From, v.To)
	if err != nil {
		return after, false, err
	}
	records, _, err := collectTraceRecords(sourceRows, v.Session, traceSourceKey{})
	if err != nil {
		return after, false, err
	}
	records, err = filterTraceCopies(ctx, tx, v.Session, records)
	if err != nil {
		return after, false, err
	}
	matching := []sessiontrace.Event{}
	for _, e := range sessiontrace.Normalize(records) {
		if keys[sessiontrace.EventCorrelationKey(e)] {
			matching = append(matching, e)
		}
	}
	if err = mergeTraceEvents(ctx, tx, v.ID, matching); err != nil {
		return after, false, err
	}
	return after, n < 1000, nil
}
