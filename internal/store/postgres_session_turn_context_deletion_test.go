package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTurnContextDeletionBeforeSnapshotAndCancellation(t *testing.T) {
	// Serial: exercises the shared fixture's advisory lock and tombstone tables.
	s := acquireTestStore(t)
	id, a := seedTurnContext(t, s)
	publishTurnContext(t, s, id)
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err = lockSessionDeletionTargets(ctx, tx, []string{id}); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deleted_sessions(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		v, err := s.TurnContext(ctx, id, "opener", a, TurnContextRequest{})
		if len(v.Records) > 0 {
			result <- errors.New("deleted content returned")
			return
		}
		result <- err
	}()
	// Observe the real waiter before committing deletion; a channel alone would
	// not establish whether the reader already took its database snapshot.
	waitCtx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err = s.pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event='advisory' AND query LIKE 'SELECT pg_advisory_lock_shared%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("reader never waited for deletion barrier")
		case <-ticker.C:
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrSessionAnalysisNotFound) {
			t.Fatalf("post-delete read=%v", err)
		}
	case <-waitCtx.Done():
		t.Fatal("reader did not complete after deletion")
	}
	// A short parent budget also covers lock acquisition, and the canceled
	// session lock must not leak into the pool or block a later deletion.
	t.Run("budget", func(t *testing.T) {
		otherID, otherAccess := seedTurnContext(t, s)
		deletion, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer deletion.Rollback(ctx) //nolint:errcheck
		if err = lockSessionDeletionTargets(ctx, deletion, []string{otherID}); err != nil {
			t.Fatal(err)
		}
		short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		_, err = s.TurnContext(short, otherID, "opener", otherAccess, TurnContextRequest{})
		cancel()
		if err == nil {
			t.Fatal("lock wait ignored parent cancellation")
		}
		if err = deletion.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		check, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		next, err := s.pool.Begin(check)
		if err != nil {
			t.Fatal(err)
		}
		defer next.Rollback(ctx) //nolint:errcheck
		if err = lockSessionDeletionTargets(check, next, []string{otherID}); err != nil {
			t.Fatalf("canceled read leaked session lock: %v", err)
		}
	})
}
