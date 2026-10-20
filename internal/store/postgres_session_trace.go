package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"cctrace/internal/sessionfacts"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type traceSnapshot struct {
	ID, Session, Scope, Mode, State, Coverage string
	MappingStamp                              string
	Access                                    SessionAnalysisAccess
	From, To, First, Last                     *time.Time
	Revision, Epoch, Rows, Events             int64
	Reason                                    *string
	After                                     traceSourceKey
	Bins                                      []TraceBin
	CoverageFrom, CoverageTo                  *time.Time
}
type traceSourceKey struct {
	TS                          time.Time
	UUID                        string
	ID                          int64
	Type, Profile               string
	SourceDone                  bool
	BoundaryAfter               string
	SeedComplete, SeedTruncated bool
	SourceIncomplete            bool
	ChunkLimit                  int
	ResumeGeneration            int64
}

const traceSnapshotColumns = `snapshot_id,session_id,scope_key,access,mode,range_from,range_to,source_revision,policy_epoch,state,coverage,reason,processed_rows,after_key,from_ts,to_ts,events_count,bins,coverage_from,coverage_to,mapping_stamp`

func scanTraceSnapshot(row pgx.Row) (v traceSnapshot, err error) {
	var access, after, bins []byte
	err = row.Scan(&v.ID, &v.Session, &v.Scope, &access, &v.Mode, &v.From, &v.To, &v.Revision, &v.Epoch, &v.State, &v.Coverage, &v.Reason, &v.Rows, &after, &v.First, &v.Last, &v.Events, &bins, &v.CoverageFrom, &v.CoverageTo, &v.MappingStamp)
	if err != nil {
		return
	}
	if err = json.Unmarshal(access, &v.Access); err != nil {
		return
	}
	if err = json.Unmarshal(after, &v.After); err != nil {
		return
	}
	err = json.Unmarshal(bins, &v.Bins)
	for _, p := range []*time.Time{v.From, v.To, v.First, v.Last, v.CoverageFrom, v.CoverageTo} {
		if p != nil {
			*p = p.UTC()
		}
	}
	for i := range v.Bins {
		v.Bins[i].From = v.Bins[i].From.UTC()
		v.Bins[i].To = v.Bins[i].To.UTC()
		v.Bins[i].FirstTS = v.Bins[i].FirstTS.UTC()
		v.Bins[i].LastTS = v.Bins[i].LastTS.UTC()
	}
	return
}
func (v traceSnapshot) coverage() TraceCoverage {
	return TraceCoverage{State: v.Coverage, From: v.CoverageFrom, To: v.CoverageTo, ProcessedRows: v.Rows, RowLimit: TraceOverviewRows, Reason: v.Reason}
}
func traceBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Second)
}
func (s *PgStore) traceQueryPool(ctx context.Context) (*pgxpool.Pool, error) {
	s.tracePoolOnce.Do(func() {
		cfg := s.pool.Config().Copy()
		cfg.MaxConns = 2
		cfg.MinConns = 0
		s.tracePool, s.tracePoolErr = pgxpool.NewWithConfig(ctx, cfg)
	})
	return s.tracePool, s.tracePoolErr
}
func (s *PgStore) TraceWake() <-chan struct{} {
	s.traceWakeOnce.Do(func() { s.traceWake = make(chan struct{}, 1) })
	return s.traceWake
}
func (s *PgStore) wakeTrace() {
	s.TraceWake()
	select {
	case s.traceWake <- struct{}{}:
	default:
	}
}
func (s *PgStore) traceTx(ctx context.Context) (pgx.Tx, error) {
	pool, err := s.traceQueryPool(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='1s'`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func traceRevision(ctx context.Context, tx pgx.Tx, id string) (revision, epoch int64, err error) {
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM session_timeline_revisions WHERE session_id=$1),0),epoch FROM session_timeline_policy_epoch WHERE only_row`, id).Scan(&revision, &epoch)
	return
}

// Ownership uses the existing session-scoped materialized identity basis. It
// deliberately never calls ReadSessionAnalysis (which can enqueue old facts).
func authorizeTrace(ctx context.Context, tx pgx.Tx, id string, a SessionAnalysisAccess) error {
	if id == "" {
		return ErrSessionAnalysisNotFound
	}
	// Trace reads and bounded cache writes may coexist with each other. The
	// original exclusive deletion barrier still waits for all of them to finish.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,392))`, id); err != nil {
		return err
	}
	var visible bool
	if a.User {
		uid, email := a.UserID, a.ProfileEmail
		if uid == "__no_access__" {
			return ErrSessionAnalysisNotFound
		}
		err := tx.QueryRow(ctx, personalVisibleSourceQuery(" AND session_id=$4")+`SELECT EXISTS(SELECT 1 FROM owned)`, a.DashboardUserID, uid, email, id).Scan(&visible)
		if err != nil {
			return err
		}
	} else {
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM visible_session_records r WHERE r.session_id=$1
   AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4))
   AND NOT EXISTS(SELECT 1 FROM deleted_sessions WHERE session_id=$1)
   AND NOT EXISTS(SELECT 1 FROM session_records r WHERE r.session_id=$1 AND
    (NOT (`+excludedAccountPredicateSQL("r")+`) OR EXISTS(SELECT 1 FROM blocked_projects b WHERE b.project_hash=r.project_hash)))`, id, a.UserID, a.ProfileEmail, a.LoginEmail).Scan(&visible)
		if err != nil {
			return err
		}
	}
	if !visible {
		return ErrSessionAnalysisNotFound
	}
	return nil
}
func traceMappingStamp(ctx context.Context, tx pgx.Tx, id string, rev, epoch int64) (stamp string, err error) {
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT encode(timeline_hash,'hex')||':'||timeline_state||':'||rules_version||':'||mapping_version
 FROM session_timeline_provenance WHERE session_id=$1 AND source_revision=$2 AND policy_epoch=$3
 AND mapping_version=$4 AND rules_version=$5 AND tracking='tracked' AND timeline_state IN ('observed','undeterminable')
 AND octet_length(timeline_hash)=32 AND octet_length(fact_output_hash)=32 AND source_generation>0),'')`, id, rev, epoch, sessionTimelineMappingVersion, sessionfacts.RulesVersion).Scan(&stamp)
	return
}
func traceSourceBounds(ctx context.Context, tx pgx.Tx, id string, a SessionAnalysisAccess) (first, last *time.Time, err error) {
	err = tx.QueryRow(ctx, `SELECT
 (SELECT ts FROM visible_session_records r WHERE r.session_id=$1 AND `+traceSourcePredicate()+` AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4) ORDER BY ts,uuid LIMIT 1),
 (SELECT ts FROM visible_session_records r WHERE r.session_id=$1 AND `+traceSourcePredicate()+` AND ($2::text='' OR r.user_id=$2) AND ($3::text='' OR r.profile_email=$3) AND ($4::text='' OR r.login_email=$4) ORDER BY ts DESC,uuid DESC LIMIT 1)`, id, a.UserID, a.ProfileEmail, a.LoginEmail).Scan(&first, &last)
	return
}
func (s *PgStore) TraceDemand(parent context.Context, id string, a SessionAnalysisAccess, d TraceDemandRequest) (TraceDemandResponse, error) {
	if !d.Valid() {
		return TraceDemandResponse{}, ErrTraceInvalid
	}
	ctx, cancel := traceBudget(parent)
	defer cancel()
	pool, err := s.traceQueryPool(ctx)
	if err != nil {
		return TraceDemandResponse{}, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return TraceDemandResponse{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err = authorizeTrace(ctx, tx, id, a); err != nil {
		return TraceDemandResponse{}, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='1s'`); err != nil {
		return TraceDemandResponse{}, err
	}
	scope := traceScope(a)
	// Same scope/mode/range heartbeats share a generation, while screen leases
	// remain independent. No global enqueue or generation reset on renewal.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "trace-demand/"+id+scope+d.Mode+traceSelection(d.From, d.To)); err != nil {
		return TraceDemandResponse{}, err
	}
	rev, epoch, err := traceRevision(ctx, tx, id)
	if err != nil {
		return TraceDemandResponse{}, err
	}
	stamp, err := traceMappingStamp(ctx, tx, id, rev, epoch)
	if err != nil {
		return TraceDemandResponse{}, err
	}
	var existingSnapshot string
	if d.LeaseID != "" {
		err = tx.QueryRow(ctx, `SELECT l.snapshot_id FROM session_trace_leases l JOIN session_trace_snapshots s USING(snapshot_id)
   WHERE l.lease_id=$1 AND l.session_id=$2 AND l.scope_key=$3 AND s.mode=$4
   AND s.range_from IS NOT DISTINCT FROM $5::timestamptz AND s.range_to IS NOT DISTINCT FROM $6::timestamptz`, d.LeaseID, id, scope, d.Mode, d.From, d.To).Scan(&existingSnapshot)
		if errors.Is(err, pgx.ErrNoRows) {
			return TraceDemandResponse{}, ErrTraceInvalid
		}
		if err != nil {
			return TraceDemandResponse{}, err
		}
	}
	v, err := scanTraceSnapshot(tx.QueryRow(ctx, `SELECT `+traceSnapshotColumns+` FROM session_trace_snapshots WHERE session_id=$1 AND scope_key=$2 AND mode=$3
 AND range_from IS NOT DISTINCT FROM $4::timestamptz AND range_to IS NOT DISTINCT FROM $5::timestamptz AND source_revision=$6 AND policy_epoch=$7 AND rules_version=$8 AND mapping_stamp=$9
 ORDER BY created_at DESC LIMIT 1`, id, scope, d.Mode, d.From, d.To, rev, epoch, TraceRulesVersion, stamp))
	if errors.Is(err, pgx.ErrNoRows) {
		access, _ := json.Marshal(a)
		first, last := d.From, d.To
		if d.Mode == "overview" {
			var boundsErr error
			first, last, boundsErr = traceSourceBounds(ctx, tx, id, a)
			if boundsErr != nil {
				return TraceDemandResponse{}, boundsErr
			}
		}
		v = traceSnapshot{ID: uuid.NewString(), Session: id, Scope: scope, Mode: d.Mode, From: d.From, To: d.To, Revision: rev, Epoch: epoch, State: "building", First: first, Last: last, MappingStamp: stamp}
		_, err = tx.Exec(ctx, `INSERT INTO session_trace_snapshots(snapshot_id,session_id,scope_key,access,mode,range_from,range_to,source_revision,policy_epoch,rules_version,from_ts,to_ts,mapping_stamp) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, v.ID, id, scope, access, d.Mode, d.From, d.To, rev, epoch, TraceRulesVersion, v.First, v.Last, stamp)
	}
	if err != nil {
		return TraceDemandResponse{}, err
	}
	// A budget stop keeps its normalized prefix. Only a fresh visit/retry may
	// resume; renewing another screen's old lease cannot create a retry loop.
	// Bump the paging witness before any further endpoint merge can occur.
	if d.LeaseID == "" && v.Reason != nil && *v.Reason == "database_budget" && (v.State == "ready" || v.State == "unavailable") {
		v.After.ChunkLimit = traceDefaultChunkRows
		v.After.ResumeGeneration++
		key, _ := json.Marshal(v.After)
		_, err = tx.Exec(ctx, `UPDATE session_trace_snapshots SET state='building',coverage='partial',reason=NULL,after_key=$2,updated_at=clock_timestamp() WHERE snapshot_id=$1`, v.ID, key)
		if err != nil {
			return TraceDemandResponse{}, err
		}
		v.State = "building"
	}
	lease := d.LeaseID
	if lease == "" {
		lease = uuid.NewString()
	}
	var expires time.Time
	err = tx.QueryRow(ctx, `INSERT INTO session_trace_leases(lease_id,session_id,scope_key,snapshot_id,expires_at) VALUES($1,$2,$3,$4,clock_timestamp()+interval '15 seconds')
 ON CONFLICT(lease_id) DO UPDATE SET snapshot_id=EXCLUDED.snapshot_id,expires_at=EXCLUDED.expires_at RETURNING expires_at`, lease, id, scope, v.ID).Scan(&expires)
	if err != nil {
		return TraceDemandResponse{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return TraceDemandResponse{}, err
	}
	s.wakeTrace()
	return TraceDemandResponse{SessionID: id, LeaseID: lease, ExpiresAt: expires.UTC(), SnapshotID: v.ID, State: v.State}, nil
}
func (s *PgStore) TraceRelease(parent context.Context, id string, a SessionAnalysisAccess, lease string) error {
	ctx, cancel := traceBudget(parent)
	defer cancel()
	pool, err := s.traceQueryPool(ctx)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `DELETE FROM session_trace_leases WHERE lease_id=$1 AND session_id=$2 AND scope_key=$3`, lease, id, traceScope(a))
	return err
}
func loadTraceSnapshot(ctx context.Context, tx pgx.Tx, id string, a SessionAnalysisAccess, snapshot string) (traceSnapshot, error) {
	if err := authorizeTrace(ctx, tx, id, a); err != nil {
		return traceSnapshot{}, err
	}
	rev, epoch, err := traceRevision(ctx, tx, id)
	if err != nil {
		return traceSnapshot{}, err
	}
	v, err := scanTraceSnapshot(tx.QueryRow(ctx, `SELECT `+traceSnapshotColumns+` FROM session_trace_snapshots WHERE session_id=$1 AND scope_key=$2
 AND ($3::text='' OR snapshot_id=$3) AND rules_version=$4 ORDER BY (mode='overview') DESC,created_at DESC LIMIT 1`, id, traceScope(a), snapshot, TraceRulesVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		if snapshot != "" {
			return v, ErrTraceSnapshot
		}
		return traceSnapshot{Session: id, Revision: rev, Epoch: epoch, State: "unavailable", Coverage: "unavailable", Bins: []TraceBin{}}, nil
	}
	if err != nil {
		return v, err
	}
	stamp, err := traceMappingStamp(ctx, tx, id, rev, epoch)
	if err != nil {
		return v, err
	}
	if v.Epoch != epoch || v.Revision != rev || v.MappingStamp != stamp {
		if snapshot != "" {
			return v, ErrTraceSnapshot
		}
		return traceSnapshot{Session: id, Revision: rev, Epoch: epoch, State: "stale", Coverage: "unavailable", Bins: []TraceBin{}}, nil
	}
	return v, nil
}
func (s *PgStore) TraceOverview(parent context.Context, id string, a SessionAnalysisAccess, snapshot string) (TraceOverviewResponse, error) {
	ctx, cancel := traceBudget(parent)
	defer cancel()
	// Share the canonical context reader's deletion barrier before taking a snapshot.
	tx, cleanup, err := s.turnContextTx(ctx, id)
	if err != nil {
		return TraceOverviewResponse{}, err
	}
	defer cleanup()
	v, err := loadTraceSnapshot(ctx, tx, id, a, snapshot)
	if err != nil {
		return TraceOverviewResponse{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return TraceOverviewResponse{}, err
	}
	if v.Bins == nil {
		v.Bins = []TraceBin{}
	}
	return TraceOverviewResponse{SessionID: id, SnapshotID: v.ID, SourceRevision: strconv.FormatInt(v.Revision, 10), State: v.State, Coverage: v.coverage(), From: v.First, To: v.Last, Bins: v.Bins, EventsCount: v.Events}, nil
}
func (s *PgStore) TraceEvents(parent context.Context, id string, a SessionAnalysisAccess, q TraceEventsRequest) (TraceEventsResponse, error) {
	if !validTraceRange(q.From, q.To) || q.Limit < 0 || q.Limit > 1000 {
		return TraceEventsResponse{}, ErrTraceInvalid
	}
	if q.Limit == 0 {
		q.Limit = 1000
	}
	ctx, cancel := traceBudget(parent)
	defer cancel()
	tx, cleanup, err := s.turnContextTx(ctx, id)
	if err != nil {
		return TraceEventsResponse{}, err
	}
	defer cleanup()
	v, err := loadTraceSnapshot(ctx, tx, id, a, q.SnapshotID)
	if err != nil {
		return TraceEventsResponse{}, err
	}
	out := TraceEventsResponse{SessionID: id, SnapshotID: v.ID, SourceRevision: strconv.FormatInt(v.Revision, 10), State: v.State, Coverage: v.coverage(), Events: []TraceEvent{}}
	var after traceCursor
	if q.Cursor != "" {
		after, err = decodeTraceCursor(q.Cursor, v.ID, traceScope(a), traceSelection(q.From, q.To))
		if err != nil {
			return out, err
		}
		if after.Generation != v.After.ResumeGeneration {
			return out, ErrTraceSnapshot
		}
	}
	if q.Cursor != "" {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_trace_events WHERE snapshot_id=$1 AND event_id=$2 AND ts=$3
  AND ((started_at IS NULL AND ts >= $4 AND ts < $5) OR (started_at < $5 AND (ended_at >= $4 OR ended_at IS NULL))))`, v.ID, after.Key, after.TS, q.From, q.To).Scan(&exists)
		if err != nil {
			return out, err
		}
		if !exists {
			return out, ErrTraceInvalid
		}
	}
	// Building snapshots are mutable. Publish cursor pages only after the final
	// chunk so readers never miss or duplicate an endpoint merged during paging.
	if v.State == "ready" {
		rows, err := tx.Query(ctx, `SELECT event FROM session_trace_events WHERE snapshot_id=$1 AND
 ((started_at IS NULL AND ts >= $2 AND ts < $3) OR (started_at < $3 AND (ended_at >= $2 OR ended_at IS NULL)))
 AND ($4::text='' OR (ts,event_id) > ($5::timestamptz,$4)) ORDER BY ts,event_id LIMIT $6`, v.ID, q.From, q.To, after.Key, after.TS, q.Limit+1)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var b []byte
			if err = rows.Scan(&b); err != nil {
				rows.Close()
				return out, err
			}
			var event TraceEvent
			if err = json.Unmarshal(b, &event); err != nil {
				rows.Close()
				return out, err
			}
			out.Events = append(out.Events, event)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return out, err
		}
		if len(out.Events) > q.Limit {
			last := out.Events[q.Limit-1]
			cursor := encodeTraceCursor(traceCursor{Snapshot: v.ID, Scope: traceScope(a), Selection: traceSelection(q.From, q.To), Generation: v.After.ResumeGeneration, TS: last.Timestamp, Key: last.EventID})
			out.NextCursor = &cursor
			out.Events = out.Events[:q.Limit]
		}
	}
	return out, tx.Commit(ctx)
}
