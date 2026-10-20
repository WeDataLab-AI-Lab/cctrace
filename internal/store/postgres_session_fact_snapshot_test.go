package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

// Shared fixture tables are reset, so this test must not run in parallel.
func TestSessionFactSnapshotMatchesFullComputation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "batched-fact-snapshot"
	ts := recentDay(1).Add(time.Hour)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id,reasons,scan_from_id) VALUES($1,1,0)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
		(ts,session_id,agent,record_type,uuid,parent_uuid,user_id,profile_email,prompt_source,raw)
		VALUES ($1,$2,'claude','user','u','','owner','profile','typed',
		'{"promptId":"p","message":{"content":"keep every turn"},"cctraceCollect":{"version":1}}'),
		($1+interval '1 second',$2,'claude','assistant','a','u','owner','profile','',
		'{"message":{"content":[{"type":"tool_use","id":"edit","name":"Write","input":{"file_path":"kept.go","content":"discard input body"}}]}}'),
		($1+interval '2 seconds',$2,'claude','user','r','a','owner','profile','tool_result',
		'{"message":{"content":[{"type":"tool_result","tool_use_id":"edit","content":"done"}]}}')`, ts, id); err != nil {
		t.Fatal(err)
	}
	full, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	want := sessionfacts.ComputeWithTimelineAndCommits(full.Input)
	load, got, err := s.computeSessionFactSnapshot(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(load.Input.Rows) != 0 || len(load.Input.CodexRows) != 0 || len(load.Input.CommitResults) != 0 {
		t.Fatal("snapshot computation retained source payload slices")
	}
	if len(load.LoadedKeys) != 3 || load.Truncated != nil || load.Generation != full.Generation {
		t.Fatalf("loaded=%d truncated=%v generation=%d want=%d", len(load.LoadedKeys), load.Truncated != nil, load.Generation, full.Generation)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batched computation differs from full computation\ngot=%+v\nwant=%+v", got, want)
	}
	timeline := &sessionFactTimeline{Facts: got.Timeline, HasTimeline: true, Commits: got.Commits, Tasks: got.Tasks, Hash: got.TimelineHash}
	if outcome, err := s.publishSessionFactWithTimeline(ctx, id, load.Generation, load, got.Output, timeline, 1); err != nil || outcome != "written" {
		t.Fatalf("publish=%s error=%v", outcome, err)
	}
	var members int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_timeline_members WHERE session_id=$1`, id).Scan(&members); err != nil || members != 3 {
		t.Fatalf("timeline members=%d error=%v", members, err)
	}
}

func TestSessionFactReadOnlyReplayDoesNotRequireOrCreateDirtyRow(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "read-only-replay"
	if _, _, err := s.computeSessionFactSnapshot(ctx, id, "claude"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("production worker accepted an unqueued session: %v", err)
	}
	if _, _, err := s.computeSessionFactReadSnapshot(ctx, id, "claude", false); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_fact_dirty WHERE session_id=$1`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("read-only replay changed queue: rows=%d error=%v", n, err)
	}
}
