package store

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"cctrace/internal/sessiontrace"

	"github.com/jackc/pgx/v5"
)

// Only the selected request's dependency chain is read. Recursive metadata is
// body-free; blob and transcript bodies are fetched later by exact reference.
func (l *nativeNodeLookup) primeRequests() error {
	r := l.scope.record
	query := `WITH RECURSIVE chain AS (
 SELECT r.id,r.ts,r.uuid,r.raw->>'id' AS request_id,r.raw->>'base' AS base_id,
 jsonb_build_object('base',r.raw->'base','keep',r.raw->'keep','tail',r.raw->'tail') AS recipe,ARRAY[r.id] AS visited,0 AS depth
 FROM visible_session_records r WHERE r.session_id=$1 AND r.id=$2 AND r.ts=$3
 AND r.profile_email=$4 AND r.user_id=$5 AND COALESCE(r.account_id,'')=$6 AND COALESCE(r.agent,'claude')=$7 AND COALESCE(r.source_file,'')=$8
 UNION ALL
 SELECT p.id,p.ts,p.uuid,p.raw->>'id',p.raw->>'base',jsonb_build_object('base',p.raw->'base','keep',p.raw->'keep','tail',p.raw->'tail'),c.visited||p.id,c.depth+1
 FROM chain c JOIN LATERAL (SELECT r.id,r.ts,r.uuid,r.raw FROM visible_session_records r
 WHERE r.session_id=$1 AND r.record_type='api-request' AND r.raw->>'id'=c.base_id
 AND r.profile_email=$4 AND r.user_id=$5 AND COALESCE(r.account_id,'')=$6 AND COALESCE(r.agent,'claude')=$7 AND COALESCE(r.source_file,'')=$8
 ORDER BY r.ts,r.id LIMIT 2) p ON NOT p.id=ANY(c.visited)
 WHERE c.depth<2048 AND jsonb_typeof(c.recipe->'keep')='number' AND c.recipe->'keep'>'0'::jsonb
 ) SELECT id,ts,uuid,request_id,recipe FROM chain LIMIT 2049`
	if r.AccountID == "" && !r.Ts.After(time.Unix(0, 0)) {
		query = strings.ReplaceAll(query, `COALESCE(r.account_id,'')=$6`, `$6::text=''`)
	} else {
		query = strings.ReplaceAll(query, `COALESCE(r.account_id,'')=$6`, `(COALESCE(r.account_id,'')=$6 OR (r.ts='epoch'::timestamptz AND COALESCE(r.account_id,'')=''))`)
	}
	rows, err := l.reader.tx.Query(l.reader.ctx, query, r.SessionID, l.scope.id, r.Ts, r.ProfileEmail, r.UserID, r.AccountID, r.Agent, r.SourceFile)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row traceNodeRow
		record := *r
		row.record = &record
		var raw []byte
		var id *string
		if err = rows.Scan(&row.id, &record.Ts, &record.UUID, &id, &raw); err != nil {
			return err
		}
		object, e := sessiontrace.DecodeObject(raw)
		if e != nil {
			return e
		}
		record.RecordType = "api-request"
		key := "request/" + row.key("request", -1).Ref
		if id != nil && *id != "" {
			key = "request/" + *id
		}
		if previous, ok := l.cache[key]; ok {
			if previous.raw == nil {
				continue
			}
			before, _ := sessiontrace.CanonicalJSON(previous.raw)
			after, _ := sessiontrace.CanonicalJSON(object)
			if string(before) != string(after) {
				l.cache[key] = nativeNodeReference{}
				continue
			}
		}
		l.cache[key] = nativeNodeReference{raw: object, ref: row.key("request", -1).Ref}
	}
	return rows.Err()
}

func referenceInt(value any, fallback int) int {
	switch v := value.(type) {
	case json.Number:
		n, e := strconv.Atoi(string(v))
		if e == nil {
			return n
		}
	case float64:
		return int(v)
	case int:
		return v
	}
	return fallback
}

func (l *nativeNodeLookup) transcriptMessage(row traceNodeRow, key string) (map[string]any, error) {
	message := nodeMessage(row)
	if hash, e := sessiontrace.MessageHash(message); e == nil && hash == key {
		return message, nil
	}
	refs, _ := row.raw["cctraceReferences"].(map[string]any)
	entries, _ := refs["messages"].([]any)
	for _, item := range entries {
		entry, _ := item.(map[string]any)
		if nodeString(entry, "hash") != key {
			continue
		}
		records, _ := entry["records"].([]any)
		if len(records) == 0 || len(records) > 100 {
			return nil, nil
		}
		var merged map[string]any
		var content []any
		var spans []nativeSourceSpan
		for _, item := range records {
			ref, _ := item.(map[string]any)
			uuid := nodeString(ref, "uuid")
			if uuid == "" {
				return nil, nil
			}
			matches, err := l.reader.nativeRows(l.scope, `r.uuid=$5 AND r.record_type IN ('user','assistant')`, []any{uuid}, 2)
			if err != nil {
				return nil, err
			}
			if len(matches) != 1 {
				return nil, nil
			}
			normal, err := sessiontrace.NormalizeMessage(nodeMessage(matches[0]))
			if err != nil {
				return nil, nil
			}
			blocks := normal["content"].([]any)
			start, end := referenceInt(ref["block_start"], 0), referenceInt(ref["block_end"], len(blocks))
			if start < 0 || end < start || end > len(blocks) {
				return nil, nil
			}
			if merged != nil && merged["role"] != normal["role"] {
				return nil, nil
			}
			merged = normal
			location := traceSourceLocation(matches[0], "message", "message.content")
			location.BlockStart = &start
			location.BlockEnd = &end
			l.addLocation(location)
			l.remember(matches[0])
			_, text := nodeMessage(matches[0])["content"].(string)
			spans = append(spans, nativeSourceSpan{location: location, start: len(content), end: len(content) + end - start, text: text})
			content = append(content, blocks[start:end]...)
		}
		merged["content"] = content
		hash, err := sessiontrace.MessageHash(merged)
		if err != nil {
			return nil, nil
		}
		if hash != key && traceAuditValue(content).State != "redacted" {
			return nil, nil
		}
		if l.spans == nil {
			l.spans = map[string][]nativeSourceSpan{}
		}
		l.spans[row.key("message", -1).Ref] = spans
		return merged, nil
	}
	return nil, nil
}

func (r traceNodeReader) previousResponse(out *TraceNodeDetail, row traceNodeRow) error {
	mid := nodeString(nodeMessage(row), "id")
	// Resolve the predecessor key descending before fetching its body.
	var sourceID int64
	var raw []byte
	err := r.tx.QueryRow(r.ctx, `WITH target AS MATERIALIZED (SELECT r.ts,r.id FROM visible_session_records r
 WHERE r.session_id=$1 AND r.record_type='assistant' AND r.agent='claude'
 AND r.profile_email=$2 AND r.user_id=$3 AND COALESCE(r.account_id,'')=$4 AND COALESCE(r.source_file,'')=$5
 AND (r.ts,r.id)<($6,$7) AND COALESCE(r.message_id,r.raw->'message'->>'id','')<>$8
 ORDER BY r.ts DESC,r.id DESC LIMIT 1)
 SELECT r.id,to_jsonb(r) FROM target t JOIN visible_session_records r ON r.session_id=$1 AND r.id=t.id AND r.ts=t.ts
 AND r.profile_email=$2 AND r.user_id=$3 AND COALESCE(r.account_id,'')=$4 AND COALESCE(r.source_file,'')=$5 LIMIT 1`, r.session, row.record.ProfileEmail, row.record.UserID, row.record.AccountID, row.record.SourceFile, row.record.Ts, row.id, mid).Scan(&sourceID, &raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	previous := traceNodeRow{id: sourceID, record: &SessionRecord{}}
	if err = json.Unmarshal(raw, previous.record); err != nil {
		return err
	}
	previous.raw, err = sessiontrace.DecodeObject(previous.record.Raw)
	if err != nil {
		return err
	}
	key, err := r.responseKey(previous)
	if err != nil {
		return err
	}
	usage := nodeMessage(previous)["usage"]
	if messageID := nodeString(nodeMessage(previous), "id"); messageID != "" {
		rows, e := r.tx.Query(r.ctx, `SELECT r.raw->'message'->'usage' FROM visible_session_records r WHERE r.session_id=$1
 AND r.profile_email=$2 AND r.user_id=$3 AND COALESCE(r.account_id,'')=$4 AND COALESCE(r.source_file,'')=$5 AND r.agent='claude'
 AND r.record_type='assistant' AND COALESCE(r.message_id,r.raw->'message'->>'id')=$6 ORDER BY r.ts,r.id LIMIT 101`, r.session, previous.record.ProfileEmail, previous.record.UserID, previous.record.AccountID, previous.record.SourceFile, messageID)
		if e != nil {
			return e
		}
		var snapshots []any
		for rows.Next() {
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				rows.Close()
				return e
			}
			if raw != nil {
				object, e := sessiontrace.DecodeObject(raw)
				if e == nil {
					snapshots = append(snapshots, object)
				}
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(snapshots) > 100 {
			out.Notices = append(out.Notices, "previous_usage_fragments_truncated")
			snapshots = snapshots[:100]
		}
		if len(snapshots) > 0 {
			usage = sessiontrace.MergeResponseUsage(snapshots)
		}
	}
	out.Usage["previous"] = map[string]any{"node_id": encodeNodeKey(key), "timestamp": previous.record.Ts, "values": usage, "scope": "response", "evidence": "previous_observed_response"}
	return nil
}
