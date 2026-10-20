package store

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/sessionfacts"
)

func TestSessionFactCopyClassifierLoadsRawNeighborsAndPublishesRequestMarkers(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`)
	})
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"original", "copy"} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
			VALUES($1,'owner','owner@example.com','claude',$2,'{}'::jsonb)`, id, base); err != nil {
			t.Fatal(err)
		}
	}
	rows := []struct {
		session, uuid, message string
		sec                    int
	}{
		{"original", "shared-uuid", "request-1", 10},
		{"original", "original-only", "request-original", 9},
		{"copy", "shared-uuid", "request-1", 10},
		{"copy", "copy-only", "request-1", 11},
	}
	var copiedIDs []int64
	for _, row := range rows {
		var id int64
		err := s.pool.QueryRow(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,message_id,raw)
			VALUES($1,$2,'assistant','claude','owner','owner@example.com',$3,$4,
			'{"cctraceCollect":2,"message":{"content":[{"type":"text","text":"ok"}]}}'::jsonb) RETURNING id`,
			base.Add(time.Duration(row.sec)*time.Second), row.session, row.uuid, row.message).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		if row.session == "copy" {
			copiedIDs = append(copiedIDs, id)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,scan_from_id) VALUES('copy',0)`); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, "copy", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if load.CopyGroup == nil || load.CopyGroup.Skipped || len(load.CopyGroup.Members) != 2 || len(load.CopyGroup.Desired) != 2 {
		t.Fatalf("copy group = %+v", load.CopyGroup)
	}
	for i, id := range copiedIDs {
		marker := load.CopyGroup.Desired[i]
		if marker.SRecID != id || marker.SessionID != "copy" || marker.OriginalSessionID != "original" {
			t.Fatalf("marker[%d] = %+v", i, marker)
		}
	}
	outcome, err := s.publishSessionFact(ctx, "copy", load.Generation, load, sessionfacts.Compute(load.Input), 1)
	if err != nil || outcome != "written" {
		t.Fatalf("publish = %q, %v", outcome, err)
	}
	var markers int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_record_copies WHERE session_id='copy' AND original_session_id='original'`).Scan(&markers); err != nil || markers != 2 {
		t.Fatalf("persisted request-wide copy markers = %d, %v", markers, err)
	}
	var gate bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name='session_fact_copies_v1')`).Scan(&gate); err != nil || gate {
		t.Fatalf("copy gate opened before the build completed: %v, %v", gate, err)
	}
}

func TestSessionFactCopyClassifierIncludesExcludedRawSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`)
	})
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	for _, id := range []string{"a-visible-original", "z-excluded-copy"} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
			VALUES($1,'owner','owner@example.com','claude',$2,'{}'::jsonb)`, id, base); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		session, uuid string
		sec           int
	}{
		{"a-visible-original", "shared-raw-key", 10},
		{"z-excluded-copy", "shared-raw-key", 10},
	} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,record_type,agent,user_id,profile_email,uuid,raw)
			VALUES($1,$2,'assistant','claude','owner','owner@example.com',$3,'{}'::jsonb)`,
			base.Add(time.Duration(row.sec)*time.Second), row.session, row.uuid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES('z-excluded-copy')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,scan_from_id) VALUES('z-excluded-copy',0)`); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.pool.QueryRow(ctx, `SELECT generation FROM session_fact_dirty WHERE session_id='z-excluded-copy'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	outcome, err := s.processSessionFact(ctx, "z-excluded-copy", generation)
	if err != nil || outcome.Outcome != "copy_only" {
		t.Fatalf("excluded copy pass = %+v, %v", outcome, err)
	}
	var markers int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_record_copies
		WHERE session_id='z-excluded-copy' AND original_session_id='a-visible-original'`).Scan(&markers); err != nil || markers != 1 {
		t.Fatalf("raw-source copy marker count=%d, %v", markers, err)
	}
}

func TestSessionFactCopyGroupStopsAtTheMemberCap(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_record_copies(srec_id,session_id,original_session_id,record_type,ts)
		SELECT 100000+n, 'copy-0', 'copy-'||n::text, 'assistant', now()
		FROM generate_series(1,1001) AS g(n)`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	group, err := loadSessionFactCopyGroup(ctx, tx, "copy-0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !group.Skipped || len(group.Members) != sessionFactCopyMemberLimit+1 || len(group.Desired) != 0 {
		t.Fatalf("large copy group = %+v", group)
	}
}
