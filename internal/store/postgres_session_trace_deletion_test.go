package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func traceDeletionReader(t *testing.T, s *PgStore, kind string, owner bool) (string, func(context.Context) (int, error)) {
	t.Helper()
	id, access := seedTurnContext(t, s)
	access.User = owner
	ctx := context.Background()
	// Other serial trace fixtures can leave live building demands behind.
	if _, err := s.pool.Exec(ctx, `TRUNCATE session_trace_snapshots CASCADE`); err != nil {
		t.Fatal(err)
	}
	demand, err := s.TraceDemand(ctx, id, access, TraceDemandRequest{Mode: "overview"})
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := s.RunTraceChunk(ctx); !worked || err != nil {
		t.Fatalf("prepare trace: worked=%v err=%v", worked, err)
	}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	events := TraceEventsRequest{SnapshotID: demand.SnapshotID, From: &from, To: &to}
	page, err := s.TraceEvents(ctx, id, access, events)
	if err != nil || len(page.Events) == 0 {
		t.Fatalf("prepare events: count=%d err=%v", len(page.Events), err)
	}
	return id, func(ctx context.Context) (int, error) {
		switch kind {
		case "overview":
			v, err := s.TraceOverview(ctx, id, access, demand.SnapshotID)
			return int(v.EventsCount), err
		case "events":
			v, err := s.TraceEvents(ctx, id, access, events)
			return len(v.Events), err
		default:
			v, err := s.TraceContext(ctx, id, access, TraceContextRequest{SnapshotID: demand.SnapshotID, EventID: page.Events[0].EventID})
			return len(v.Records), err
		}
	}
}

func TestTraceReadDeletionBeforeSnapshot(t *testing.T) {
	// Serial: the shared container's trace worker and deletion fixture are mutable.
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, role := range []string{"owner", "admin"} {
		for _, kind := range []string{"overview", "events", "context"} {
			t.Run(role+"/"+kind, func(t *testing.T) {
				id, read := traceDeletionReader(t, s, kind, role == "owner")
				if count, err := read(ctx); err != nil || count == 0 {
					t.Fatalf("initial read: count=%d err=%v", count, err)
				}
				deletion, err := s.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer deletion.Rollback(ctx) //nolint:errcheck
				if err = lockSessionDeletionTargets(ctx, deletion, []string{id}); err != nil {
					t.Fatal(err)
				}
				if _, err = deletion.Exec(ctx, `INSERT INTO deleted_sessions(session_id) VALUES($1)`, id); err != nil {
					t.Fatal(err)
				}
				type result struct {
					count int
					err   error
				}
				done := make(chan result, 1)
				go func() {
					count, err := read(ctx)
					done <- result{count, err}
				}()
				// Commit only after PostgreSQL confirms this reader waits for deletion;
				// a goroutine-start signal does not establish its snapshot ordering.
				wait, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
				defer cancel()
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					var blocked bool
					err = s.pool.QueryRow(wait, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
					 WHERE wait_event='advisory' AND $1::int=ANY(pg_blocking_pids(pid)))`, deletion.Conn().PgConn().PID()).Scan(&blocked)
					if err != nil {
						t.Fatal(err)
					}
					if blocked {
						break
					}
					select {
					case <-wait.Done():
						t.Fatal("reader never waited for deletion")
					case <-ticker.C:
					}
				}
				if err = deletion.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case got := <-done:
					if !errors.Is(got.err, ErrSessionAnalysisNotFound) || got.count != 0 {
						t.Fatalf("post-delete read: count=%d err=%v; want no content and not found", got.count, got.err)
					}
				case <-wait.Done():
					t.Fatal("reader did not finish after deletion")
				}
				assertTraceDeletionBarrierReleased(t, s, id)
			})
		}
	}
}

func TestTraceReadCancellationReleasesDeletionBarrier(t *testing.T) {
	// Serial: each reader uses the shared container and trace query pool.
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, kind := range []string{"overview", "events", "context"} {
		t.Run(kind, func(t *testing.T) {
			id, read := traceDeletionReader(t, s, kind, true)
			deletion, err := s.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer deletion.Rollback(ctx) //nolint:errcheck
			if err = lockSessionDeletionTargets(ctx, deletion, []string{id}); err != nil {
				t.Fatal(err)
			}
			short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			_, err = read(short)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lock wait ignored reader budget: %v", err)
			}
			if err = deletion.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if count, err := read(ctx); err != nil || count == 0 {
				t.Fatalf("read after cancellation: count=%d err=%v", count, err)
			}
			assertTraceDeletionBarrierReleased(t, s, id)
		})
	}
}

func assertTraceDeletionBarrierReleased(t *testing.T, s *PgStore, id string) {
	t.Helper()
	ctx := context.Background()
	check, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Rollback(ctx) //nolint:errcheck
	var locked bool
	if err = check.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,392))`, id).Scan(&locked); err != nil || !locked {
		t.Fatalf("reader leaked deletion barrier: locked=%v err=%v", locked, err)
	}
}
