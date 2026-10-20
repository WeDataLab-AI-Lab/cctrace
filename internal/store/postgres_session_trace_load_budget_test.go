package store

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type traceQueryRecorder struct {
	sync.Mutex
	queries []string
}

func (r *traceQueryRecorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.Lock()
	r.queries = append(r.queries, d.SQL)
	r.Unlock()
	return ctx
}
func (*traceQueryRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestTraceRangeDemandSkipsFullSessionBounds(t *testing.T) {
	// The shared container's source fixture is serial, including its query tracer.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	id := "trace-range-bounds-test"
	base := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,uuid,record_type,user_id,profile_email,agent,raw)
 VALUES($1,$2,'point','user','owner','owner@example.test','claude','{"message":{"content":"synthetic"}}')`, base, id); err != nil {
		t.Fatal(err)
	}
	recorder := &traceQueryRecorder{}
	cfg := s.pool.Config().Copy()
	cfg.ConnConfig.Tracer = recorder
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	traced := &PgStore{pool: pool}
	t.Cleanup(func() {
		if traced.tracePool != nil {
			traced.tracePool.Close()
		}
		pool.Close()
	})
	a := SessionAnalysisAccess{UserID: "owner", DashboardUserID: 105}
	from, to := base.Add(-time.Minute), base.Add(time.Minute)
	d, err := traced.TraceDemand(ctx, id, a, TraceDemandRequest{Mode: "range", From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	v, err := traced.TraceOverview(ctx, id, a, d.SnapshotID)
	if err != nil || v.From == nil || v.To == nil || !v.From.Equal(from) || !v.To.Equal(to) {
		t.Fatalf("range bounds changed: %v", err)
	}
	recorder.Lock()
	defer recorder.Unlock()
	for _, q := range recorder.queries {
		if strings.Contains(q, "ORDER BY ts DESC,uuid DESC LIMIT 1") {
			t.Fatal("range demand read full session bounds before using selected range")
		}
	}
}

func TestTraceMetadataKeyPagingPreservesFullTuple(t *testing.T) {
	// Serial fixture lease protects explicit duplicate timestamp/id source keys.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := "trace-full-key-load-test"
	base := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(id,ts,session_id,uuid,record_type,user_id,profile_email,agent,raw)
 SELECT 1+g/4,$1,$2,'same-'||(g/4)::text,CASE WHEN g%4<2 THEN 'user' ELSE 'assistant' END,'owner',
 CASE WHEN g%2=0 THEN 'A@example.test' ELSE 'a@example.test' END,'claude','{"message":{"content":"synthetic"}}'::jsonb
 FROM generate_series(0,1499) g`, base, id); err != nil {
		t.Fatal(err)
	}
	tx, err := s.traceTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	a := SessionAnalysisAccess{UserID: "owner", DashboardUserID: 106}
	if err = authorizeTrace(ctx, tx, id, a); err != nil {
		t.Fatal(err)
	}
	v := traceSnapshot{Session: id, Access: a}
	seen := make(map[traceSourceKey]bool)
	// 499 deliberately splits the four type/profile keys sharing ts/uuid/id.
	for pass := 0; pass < 5; pass++ {
		records, after, err := loadTraceRecords(ctx, tx, v, 499)
		if err != nil {
			t.Fatal(err)
		}
		expected := 499
		switch pass {
		case 3:
			expected = 3
		case 4:
			expected = 0
		}
		if len(records) != expected {
			t.Fatalf("page %d rows=%d want=%d", pass, len(records), expected)
		}
		for _, r := range records {
			key := traceSourceKey{TS: r.TS, UUID: r.UUID, ID: r.ID, Type: r.RecordType, Profile: r.SourceScope}
			if seen[key] {
				t.Fatal("full source tuple repeated across metadata pages")
			}
			seen[key] = true
		}
		v.After = after
	}
	if len(seen) != 1500 {
		t.Fatalf("key pages skipped sources: %d", len(seen))
	}
}
