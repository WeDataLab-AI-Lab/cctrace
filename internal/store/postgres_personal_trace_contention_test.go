package store

import (
	"context"
	"testing"
	"time"
)

func TestPersonalOverviewReadDoesNotWaitForTraceReader(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "personal-trace-read"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,user_id,profile_email,uuid,record_type,raw) VALUES(now(),$1,'uid-owner','owner@example.test','trace-reader-fixture','user','{}')`, id); err != nil {
		t.Fatal(err)
	}
	blocker, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx) //nolint:errcheck
	// The trace worker holds this deletion barrier while preparing a bounded chunk.
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,392))`, id); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err = s.PersonalSessionOverviewValues(deadline, 1, "owner@example.test", "uid-owner", []string{id}); err != nil {
		t.Fatalf("basic overview waited for another read-only trace: %v", err)
	}
}

func TestPersonalOverviewReaderStillBlocksDeletion(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	reader, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback(ctx) //nolint:errcheck
	const id = "personal-trace-delete-barrier"
	if err = lockSessionDeletionReaders(ctx, reader, []string{id, id}); err != nil {
		t.Fatal(err)
	}
	deleter, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer deleter.Rollback(ctx) //nolint:errcheck
	var acquired bool
	if err = deleter.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,392))`, id).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if acquired {
		t.Fatal("deletion passed an active overview read")
	}
	if err = reader.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = deleter.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,392))`, id).Scan(&acquired); err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("deletion stayed blocked after the overview read ended")
	}
}
