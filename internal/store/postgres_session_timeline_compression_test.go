package store

import (
	"context"
	"testing"
	"time"
)

// Compression and fixture resets mutate shared chunks, so this test is serial.
func TestSessionTimelineCompressedEventWitness(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		name := "rowstore"
		if compressed {
			name = "compressed"
		}
		t.Run(name, func(t *testing.T) {
			s := acquireTestStore(t)
			truncateTables(t, s)
			ctx := context.Background()
			const id = "compressed-event-witness"
			seedTimelineClaude(t, s, id)
			if _, err := s.pool.Exec(ctx, `INSERT INTO otel_events(ts,session_id,event_name,user_id,profile_email,agent,prompt_id,cost_usd,attrs)
				VALUES($1,$2,'api_request','owner','profile','claude','p1',0.5,'{}'),
				($1+interval '1 second',$2,'tool_result','owner','profile','claude','',NULL,'{"result":"before"}')`, recentDay(40).Add(time.Hour), id); err != nil {
				t.Fatal(err)
			}
			before := timelineState(t, s, id)
			if compressed {
				compressTimelineEventChunk(t, s, id)
			}
			load, _, err := s.computeSessionFactSnapshot(ctx, id, "claude")
			if err != nil {
				t.Fatalf("%s worker snapshot: %v", name, err)
			}
			if load.TimelineWitness == nil || !sameSessionTimelineState(before, *load.TimelineWitness) {
				t.Fatal("lossless compression changed the source witness")
			}
			runFresh := func() {
				t.Helper()
				run, err := s.processSessionFact(ctx, id, dirtyGeneration(t, s, id))
				if err != nil || run.Outcome != "written" && run.Outcome != "unchanged" || !timelineValid(t, s, id) {
					t.Fatalf("fresh %s publish: %+v err=%v", name, run, err)
				}
			}
			runFresh()
			for _, change := range []struct{ name, sql string }{
				{"duplicate id insert", `INSERT INTO otel_events(id,ts,session_id,event_name,user_id,profile_email,agent,attrs)
					SELECT id,ts+interval '1 microsecond',session_id,event_name,user_id,profile_email,agent,'{"result":"duplicate"}'
					FROM otel_events WHERE session_id=$1 AND event_name='tool_result'`},
				{"same-key payload", `UPDATE otel_events SET attrs='{"result":"after"}' WHERE session_id=$1 AND event_name='tool_result'`},
				{"non-api identity", `UPDATE otel_events SET user_id='other' WHERE session_id=$1 AND event_name='tool_result'`},
				{"api cost", `UPDATE otel_events SET cost_usd=0.75 WHERE session_id=$1 AND event_name='api_request'`},
				{"api prompt", `UPDATE otel_events SET prompt_id='different' WHERE session_id=$1 AND event_name='api_request'`},
				{"delete", `DELETE FROM otel_events WHERE session_id=$1 AND event_name='tool_result'`},
				{"chunk drop", `SELECT drop_chunks('otel_events',older_than=>now()-interval '20 days')`},
			} {
				t.Run(change.name, func(t *testing.T) {
					generation := requeueTimeline(t, s, id)
					load, out, err := s.computeSessionFactSnapshot(ctx, id, "claude")
					if err != nil {
						t.Fatal(err)
					}
					stored := timelineDump(t, s, id)
					var facts string
					if err := s.pool.QueryRow(ctx, `SELECT concat_ws(':',encode(output_hash,'hex'),recompute_count,source_generation)
						FROM session_analysis_facts WHERE session_id=$1`, id).Scan(&facts); err != nil {
						t.Fatal(err)
					}
					args := []any{id}
					if change.name == "chunk drop" {
						args = nil
					}
					if tag, err := s.pool.Exec(ctx, change.sql, args...); err != nil || tag.RowsAffected() == 0 {
						t.Fatalf("source mutation: %v", err)
					}
					current := timelineState(t, s, id)
					if current.Revision != load.TimelineWitness.Revision || current.PolicyEpoch != load.TimelineWitness.PolicyEpoch || sameSessionTimelineState(current, *load.TimelineWitness) {
						t.Fatal("unqueued source mutation escaped the content witness")
					}
					timeline := &sessionFactTimeline{Facts: out.Timeline, HasTimeline: true, Commits: out.Commits, Tasks: out.Tasks, Hash: out.TimelineHash}
					if outcome, err := s.publishSessionFactWithTimeline(ctx, id, generation, load, out.Output, timeline, 1); err != nil || outcome != "timeline_source" {
						t.Fatalf("stale publish=%s err=%v", outcome, err)
					}
					var after string
					if err := s.pool.QueryRow(ctx, `SELECT concat_ws(':',encode(output_hash,'hex'),recompute_count,source_generation)
						FROM session_analysis_facts WHERE session_id=$1`, id).Scan(&after); err != nil || after != facts || timelineDump(t, s, id) != stored || dirtyGeneration(t, s, id) != generation {
						t.Fatalf("stale publish changed facts, timeline or queue: %v", err)
					}
					runFresh()
				})
			}
		})
	}
}

func compressTimelineEventChunk(t *testing.T, s *PgStore, id string) {
	t.Helper()
	ctx := context.Background()
	var chunk string
	if err := s.pool.QueryRow(ctx, `SELECT tableoid::regclass::text FROM otel_events WHERE session_id=$1 LIMIT 1`, id).Scan(&chunk); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, chunk).Scan(&exists); err != nil {
			t.Errorf("find remaining compressed chunk: %v", err)
			return
		}
		if !exists { // The chunk-drop regression deliberately removed it.
			return
		}
		if _, err := s.pool.Exec(ctx, `SELECT decompress_chunk($1::regclass,true)`, chunk); err != nil {
			t.Errorf("restore rowstore chunk: %v", err)
		}
	})
	if _, err := s.pool.Exec(ctx, `SELECT compress_chunk($1::regclass)`, chunk); err != nil {
		t.Fatal(err)
	}
	var compressed bool
	if err := s.pool.QueryRow(ctx, `SELECT is_compressed FROM timescaledb_information.chunks
		WHERE format('%I.%I',chunk_schema,chunk_name)=$1`, chunk).Scan(&compressed); err != nil || !compressed {
		t.Fatalf("chunk not actually compressed: %v", err)
	}
}
