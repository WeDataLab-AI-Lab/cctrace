package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cctrace/internal/sessiontrace"

	"github.com/jackc/pgx/v5"
)

// RunTraceChunk uses a separate connection, so a trace never occupies a slot in
// the basic request pool. The DB advisory lock enforces server-wide concurrency
// one even when several daemon instances run. It visits only active demands.
func (s *PgStore) RunTraceChunk(parent context.Context) (worked bool, resultErr error) {
	ctx, cancel := traceBudget(parent)
	defer cancel()
	activeSnapshot := ""
	activeLimit := traceDefaultChunkRows
	defer func() {
		if activeSnapshot != "" && traceChunkBudgetFailed(parent, ctx, resultErr) {
			if err := s.recoverTraceBudget(parent, activeSnapshot, activeLimit); err == nil {
				worked, resultErr = true, nil
			}
		}
	}()
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return false, err
	}
	defer conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='1s'`); err != nil {
		return false, err
	}
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('session-trace-worker',0))`).Scan(&locked); err != nil || !locked {
		if err == nil {
			return true, ErrTraceBusy
		}
		return false, err
	}
	v, err := claimTraceSnapshot(ctx, tx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	activeSnapshot = v.ID
	if v.After.ChunkLimit > 0 {
		activeLimit = v.After.ChunkLimit
	}
	if err = authorizeTrace(ctx, tx, v.Session, v.Access); err != nil {
		if !errors.Is(err, ErrSessionAnalysisNotFound) {
			return true, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM session_trace_snapshots WHERE snapshot_id=$1`, v.ID); err != nil {
			return true, err
		}
		return true, tx.Commit(ctx)
	}
	// Hold source/policy revisions only for this bounded chunk. An ingestion or
	// exclusion writer can never commit between the witness and publication.
	if _, err = tx.Exec(ctx, `SELECT revision FROM session_timeline_revisions WHERE session_id=$1 FOR SHARE`, v.Session); err != nil {
		return true, err
	}
	if _, err = tx.Exec(ctx, `SELECT epoch FROM session_timeline_policy_epoch WHERE only_row FOR SHARE`); err != nil {
		return true, err
	}
	rev, epoch, err := traceRevision(ctx, tx, v.Session)
	if err != nil {
		return true, err
	}
	stamp, err := traceMappingStamp(ctx, tx, v.Session, rev, epoch)
	if err != nil {
		return true, err
	}
	if rev != v.Revision || epoch != v.Epoch || v.MappingStamp != stamp {
		_, err = tx.Exec(ctx, `UPDATE session_trace_snapshots SET state='stale',coverage='unavailable',bins='[]',events_count=0,updated_at=now() WHERE snapshot_id=$1`, v.ID)
		if err != nil {
			return true, err
		}
		return true, tx.Commit(ctx)
	}
	after := v.After
	processed := v.Rows
	progressFrom, progressTo := v.CoverageFrom, v.CoverageTo
	state, coverage, reason := "building", "partial", (*string)(nil)
	if v.Mode == "range" && v.After.SourceDone {
		key, done, err := traceRangeBoundary(ctx, tx, v)
		if err != nil {
			if errors.Is(err, ErrTraceDependencyLimit) {
				return traceLimited(ctx, tx, v.ID, "dependency_limit")
			}
			return true, err
		}
		after.BoundaryAfter = key
		if done {
			state = "ready"
			coverage = "partial"
			r := "boundary_unknown"
			reason = &r
			if v.After.SeedComplete && !v.After.SeedTruncated {
				coverage = "complete"
				reason = nil
			}
			if v.After.SeedTruncated {
				coverage = "truncated"
				r := "boundary_limit"
				reason = &r
			}

		}
	} else {
		limit := activeLimit
		if remaining := int64(TraceOverviewRows) - v.Rows; remaining < int64(limit) {
			limit = int(remaining)
		}
		records, next, err := loadTraceRecords(ctx, tx, v, limit)
		if err != nil {
			return true, err
		}
		after = next
		kept, err := filterTraceCopies(ctx, tx, v.Session, records)
		if err != nil {
			return true, err
		}
		if err = mergeTraceEvents(ctx, tx, v.ID, sessiontrace.Normalize(kept)); err != nil {
			if errors.Is(err, ErrTraceDependencyLimit) {
				return traceLimited(ctx, tx, v.ID, "dependency_limit")
			}
			return true, err
		}
		processed += int64(len(records))
		for _, r := range records {
			if r.Redacted || ((r.RecordType == "user" || r.RecordType == "assistant") && !r.IsMeta && !r.IsCompactSummary && len(r.Blocks) == 0 && !r.HasText && r.PayloadType != "user_message" && r.PayloadType != "agent_message") {
				after.SourceIncomplete = true
			}
		}
		if len(records) > 0 {
			start, end := records[0].TS.UTC(), records[len(records)-1].TS.UTC()
			if progressFrom == nil {
				progressFrom = &start
			}
			progressTo = &end
		}
		sourceDone := len(records) < limit
		if processed >= TraceOverviewRows {
			next := v
			next.After = after
			more, _, err := loadTraceRecords(ctx, tx, next, 1)
			if err != nil {
				return true, err
			}
			state, coverage = "ready", "complete"
			sourceDone = true
			if len(more) > 0 {
				coverage = "truncated"
				r := "row_limit"
				reason = &r
			}
		} else if sourceDone {
			state, coverage = "ready", "complete"
		}
		if v.Mode == "range" && sourceDone && coverage != "truncated" {
			after.SourceDone = true
			state, coverage = "building", "partial"
			complete, limited, seedErr := traceRangeSeed(ctx, tx, v)
			after.SeedComplete, after.SeedTruncated = complete, limited
			if seedErr != nil {
				if errors.Is(seedErr, ErrTraceDependencyLimit) {
					return traceLimited(ctx, tx, v.ID, "dependency_limit")
				}
				err = seedErr
				return true, err
			}
		}
	}
	if after.SourceIncomplete && state == "ready" && coverage != "truncated" {
		coverage = "partial"
		r := "metadata_unavailable"
		reason = &r
	}
	if v.Mode == "overview" && state == "ready" && processed == 0 && v.First == nil {
		state, coverage = "unavailable", "unavailable"
		r := "no_trace_source"
		reason = &r
	}
	first, last := v.First, v.Last
	var eventFirst, eventLast *time.Time
	var count int64
	if err = tx.QueryRow(ctx, `SELECT min(least(ts,started_at)),max(greatest(ts,ended_at)),count(*) FROM session_trace_events WHERE snapshot_id=$1`, v.ID).Scan(&eventFirst, &eventLast, &count); err != nil {
		return true, err
	}
	bins, err := traceDensity(ctx, tx, v.ID, first, last)
	if err != nil {
		return true, err
	}
	b, _ := json.Marshal(bins)
	if bins == nil {
		b = []byte("[]")
	}
	key, _ := json.Marshal(after)
	result, err := tx.Exec(ctx, `UPDATE session_trace_snapshots SET after_key=$2,processed_rows=$3,state=$4,coverage=$5,reason=$6,
 from_ts=$7,to_ts=$8,events_count=$9,bins=$10,coverage_from=$11,coverage_to=$12,updated_at=clock_timestamp() WHERE snapshot_id=$1
 AND EXISTS(SELECT 1 FROM session_trace_leases l WHERE l.snapshot_id=$1 AND l.expires_at>clock_timestamp())`, v.ID, key, processed, state, coverage, reason, first, last, count, b, progressFrom, progressTo)
	if err != nil {
		return true, err
	}
	if result.RowsAffected() == 0 {
		return true, nil
	}
	return true, tx.Commit(ctx)
}

func traceChunkBudgetFailed(parent, chunk context.Context, resultErr error) bool {
	return resultErr != nil && parent.Err() == nil && (errors.Is(resultErr, context.DeadlineExceeded) || sessionFactTimeout(resultErr) || chunk.Err() == context.DeadlineExceeded)
}

func filterTraceCopies(ctx context.Context, tx pgx.Tx, id string, records []sessiontrace.Record) ([]sessiontrace.Record, error) {
	if len(records) == 0 {
		return records, nil
	}
	var gate bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1')`).Scan(&gate); err != nil {
		return nil, err
	}
	if !gate {
		return records, nil
	}
	ids := make([]int64, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.ID)
	}
	rows, err := tx.Query(ctx, `SELECT srec_id,ts,record_type FROM session_record_copies WHERE session_id=$1 AND srec_id=ANY($2)`, id, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	copied := map[string]bool{}
	key := func(id int64, ts time.Time, typ string) string { return fmt.Sprint(id, "/", ts.UnixMicro(), "/", typ) }
	for rows.Next() {
		var id int64
		var ts time.Time
		var typ string
		if err = rows.Scan(&id, &ts, &typ); err != nil {
			return nil, err
		}
		copied[key(id, ts, typ)] = true
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	kept := make([]sessiontrace.Record, 0, len(records))
	for _, r := range records {
		if !copied[key(r.ID, r.TS, r.RecordType)] {
			kept = append(kept, r)
		}
	}
	return kept, nil
}
func mergeTraceEvents(ctx context.Context, tx pgx.Tx, snapshot string, newEvents []sessiontrace.Event) error {
	if len(newEvents) == 0 {
		return nil
	}
	var err error
	keys := []string{}
	ownKeys := []string{}
	ids := []string{}
	seen := map[string]bool{}
	for _, e := range newEvents {
		for _, key := range sessiontrace.EventRelatedCorrelationKeys(e) {
			if key != "" && !seen[key] {
				keys = append(keys, key)
				seen[key] = true
			}
		}
		if key := sessiontrace.EventCorrelationKey(e); key != "" {
			ownKeys = append(ownKeys, key)
		}
		ids = append(ids, e.EventID)
	}
	old := []sessiontrace.Event{}
	oldIDs := map[string]bool{}
	// The incoming result may carry no parent metadata. Recover dependencies
	// from the stored original observations, then load their exact parents too.
	// Reverse lookup covers an explicit child waiting for a late parent start.
	for len(keys) > 0 || len(ids) > 0 {
		rows, err := tx.Query(ctx, `SELECT metadata FROM session_trace_events WHERE snapshot_id=$1
   AND (correlation_key=ANY($2) OR related_keys && $3::text[] OR event_id=ANY($4)) LIMIT 2000`, snapshot, keys, ownKeys, ids)
		if err != nil {
			return err
		}
		next := []string{}
		n := 0
		for rows.Next() {
			n++
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			events, err := sessiontrace.UnmarshalStorage(raw)
			if err != nil {
				rows.Close()
				return err
			}
			for _, event := range events {
				if oldIDs[event.EventID] {
					continue
				}
				oldIDs[event.EventID] = true
				old = append(old, event)
				for _, key := range sessiontrace.EventRelatedCorrelationKeys(event) {
					if key != "" && !seen[key] {
						seen[key] = true
						next = append(next, key)
					}
				}
			}
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(old) > 2000 {
			return ErrTraceDependencyLimit
		}
		if n == 2000 {
			known := make([]string, 0, len(oldIDs))
			for id := range oldIDs {
				known = append(known, id)
			}
			var more bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_trace_events WHERE snapshot_id=$1
  AND (correlation_key=ANY($2) OR related_keys && $3::text[] OR event_id=ANY($4)) AND NOT(event_id=ANY($5)))`, snapshot, keys, ownKeys, ids, known).Scan(&more)
			if err != nil {
				return err
			}
			if more {
				return ErrTraceDependencyLimit
			}
		}
		keys, ownKeys, ids = next, []string{}, []string{}
	}
	merged := sessiontrace.Merge(old, newEvents)
	obsolete := make([]string, 0, len(oldIDs))
	for id := range oldIDs {
		obsolete = append(obsolete, id)
	}
	if len(obsolete) > 0 {
		if _, err = tx.Exec(ctx, `DELETE FROM session_trace_events WHERE snapshot_id=$1 AND event_id=ANY($2)`, snapshot, obsolete); err != nil {
			return err
		}
	}
	batch := &pgx.Batch{}
	for _, e := range merged {
		wire, err := json.Marshal(e)
		if err != nil {
			return err
		}
		meta, err := sessiontrace.MarshalStorage([]sessiontrace.Event{e})
		if err != nil {
			return err
		}
		sources, _ := json.Marshal(sessiontrace.Sources(e))
		related := sessiontrace.EventRelatedCorrelationKeys(e)
		if related == nil {
			related = []string{}
		}
		batch.Queue(`INSERT INTO session_trace_events(snapshot_id,event_id,kind,ts,started_at,ended_at,turn_key,source_ref,event,metadata,correlation_key,related_keys,source_key)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(snapshot_id,event_id) DO UPDATE SET
  kind=excluded.kind,ts=excluded.ts,started_at=excluded.started_at,ended_at=excluded.ended_at,turn_key=excluded.turn_key,source_ref=excluded.source_ref,
  event=excluded.event,metadata=excluded.metadata,correlation_key=excluded.correlation_key,related_keys=excluded.related_keys,source_key=excluded.source_key`, snapshot, e.EventID, e.Kind, e.Timestamp, e.StartedAt, e.EndedAt, e.TurnKey, e.SourceRef, wire, meta, sessiontrace.EventCorrelationKey(e), related, sources)
	}
	return tx.SendBatch(ctx, batch).Close()
}
func traceDensity(ctx context.Context, tx pgx.Tx, snapshot string, from, to *time.Time) ([]TraceBin, error) {
	bins := []TraceBin{}
	if from == nil || to == nil {
		return bins, nil
	}
	rows, err := tx.Query(ctx, `SELECT kind,min(least(ts,started_at)),max(greatest(ts,ended_at)),min(ts),max(ts),count(*) FROM session_trace_events WHERE snapshot_id=$1
 GROUP BY kind,CASE WHEN $2::timestamptz=$3::timestamptz THEN 0 ELSE greatest(0,least(1023,floor(1024*extract(epoch FROM(ts-$2::timestamptz))/greatest(extract(epoch FROM($3::timestamptz-$2::timestamptz)),0.000001))))::integer END ORDER BY min(ts),kind`, snapshot, from, to)
	if err != nil {
		return bins, err
	}
	defer rows.Close()
	for rows.Next() {
		var b TraceBin
		if err = rows.Scan(&b.Kind, &b.From, &b.To, &b.FirstTS, &b.LastTS, &b.Count); err != nil {
			return bins, err
		}
		bins = append(bins, b)
	}
	return bins, rows.Err()
}

func traceLimited(ctx context.Context, tx pgx.Tx, snapshot, reason string) (bool, error) {
	_, err := tx.Exec(ctx, `UPDATE session_trace_snapshots SET state='unavailable',coverage='unavailable',reason=$2,bins='[]',events_count=0 WHERE snapshot_id=$1`, snapshot, reason)
	if err != nil {
		return true, err
	}
	return true, tx.Commit(ctx)
}

// A timed-out bounded pass retries a smaller chunk without losing generation,
// cursor, or progress. At one row, publish the saved partial cache with explicit
// truncation and stop automatic retries. A new visit can resume from this key.
// Parent cancellation and successful commits never invoke this recovery path.
func (s *PgStore) recoverTraceBudget(parent context.Context, snapshot string, limit int) error {
	ctx, cancel := traceBudget(parent)
	defer cancel()
	pool, err := s.traceQueryPool(ctx)
	if err != nil {
		return err
	}
	if limit <= 1 {
		_, err = pool.Exec(ctx, `UPDATE session_trace_snapshots SET state=CASE WHEN processed_rows>0 THEN 'ready' ELSE 'unavailable' END,
 coverage=CASE WHEN processed_rows>0 THEN 'truncated' ELSE 'unavailable' END,reason='database_budget',updated_at=clock_timestamp()
 WHERE snapshot_id=$1 AND state='building'`, snapshot)
		return err
	}
	_, err = pool.Exec(ctx, `UPDATE session_trace_snapshots SET after_key=jsonb_set(after_key,'{ChunkLimit}',to_jsonb($2::integer)),reason='chunk_budget'
 WHERE snapshot_id=$1 AND state='building'`, snapshot, limit/2)
	return err
}

func claimTraceSnapshot(ctx context.Context, tx pgx.Tx) (traceSnapshot, error) {
	return scanTraceSnapshot(tx.QueryRow(ctx, `SELECT `+traceSnapshotColumns+` FROM session_trace_snapshots s
 WHERE state='building' AND rules_version=$1 AND EXISTS(SELECT 1 FROM session_trace_leases l WHERE l.snapshot_id=s.snapshot_id AND l.expires_at>clock_timestamp())
 ORDER BY (mode='range') DESC,updated_at,snapshot_id FOR NO KEY UPDATE SKIP LOCKED LIMIT 1`, TraceRulesVersion))
}
