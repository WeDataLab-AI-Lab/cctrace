package store

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// These tests mutate the package's shared fixture tables and must run serially.
func TestSessionFactCopyGateRequeuesNewMarkers(t *testing.T) {
	for _, peer := range []bool{false, true} {
		name := "current"
		if peer {
			name = "peer"
		}
		t.Run(name, func(t *testing.T) {
			s := lifecycleStore(t)
			ctx := context.Background()
			if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES('session_fact_copies_v1')`); err != nil {
				t.Fatal(err)
			}
			base := recentDay(1).Add(time.Hour)
			insert := func(id string, tail bool) {
				t.Helper()
				if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
					(ts,session_id,agent,record_type,uuid,parent_uuid,user_id,profile_email,prompt_source,message_id,model,input_tokens,forked_from_session,raw)
					SELECT $1::timestamptz+make_interval(secs=>v.sec),$2,'claude',v.kind,v.uuid,v.parent,'owner','profile',
						CASE WHEN v.kind='user' THEN 'typed' ELSE '' END,v.message,'claude-haiku-4-5',v.tokens,
						CASE WHEN $2='copy' THEN 'original' ELSE '' END,v.raw::jsonb
					FROM (VALUES
						(0,'user','shared-u','','',0,'{"promptId":"shared","message":{"content":"shared work"}}'),
						(1,'assistant','shared-a','shared-u','shared-request',100,'{"message":{"content":[{"type":"text","text":"done"}]}}'),
						(3,'user','own-u','shared-a','',0,'{"promptId":"own","message":{"content":"new work"}}'),
						(4,'assistant','own-a','own-u','own-request',10,'{"message":{"content":[{"type":"text","text":"done"}]}}')
					) v(sec,kind,uuid,parent,message,tokens,raw) WHERE $3 OR v.sec<2`, base, id, tail); err != nil {
					t.Fatal(err)
				}
			}
			queue := func(id string) int64 {
				t.Helper()
				var generation int64
				if err := s.pool.QueryRow(ctx, `INSERT INTO session_fact_dirty(session_id,scan_from_id) VALUES($1,0) RETURNING generation`, id).Scan(&generation); err != nil {
					t.Fatal(err)
				}
				return generation
			}
			run := func(id string, generation int64, want string) {
				t.Helper()
				got, err := s.processSessionFact(ctx, id, generation)
				if err != nil || got.Outcome != want {
					t.Fatalf("process %s=%+v, err=%v; want %s", id, got, err, want)
				}
			}
			insert("copy", true)
			generation := queue("copy")
			var oldHash []byte
			if peer {
				run("copy", generation, "written")
				if err := s.pool.QueryRow(ctx, `SELECT output_hash FROM session_analysis_facts WHERE session_id='copy' AND api_requests=2 AND requests=2`).Scan(&oldHash); err != nil {
					t.Fatalf("uncopied baseline: %v", err)
				}
			}
			insert("original", false)
			if peer {
				run("original", queue("original"), "written")
				var currentHash []byte
				if err := s.pool.QueryRow(ctx, `SELECT output_hash FROM session_analysis_facts WHERE session_id='copy'`).Scan(&currentHash); err != nil || !bytes.Equal(oldHash, currentHash) {
					t.Fatalf("peer facts changed before recomputation: %v", err)
				}
			} else {
				run("copy", generation, "copy_requeued")
				for _, table := range []string{"session_analysis_facts", "session_turn_facts", "session_timeline_provenance", "session_timeline_members"} {
					if countWhere(t, s, table, "session_id='copy'") != 0 {
						t.Fatalf("copy transition published partial %s", table)
					}
				}
			}
			if countWhere(t, s, "session_record_copies", "session_id='copy' AND original_session_id='original'") != 2 {
				t.Fatal("new copied user/assistant markers missing")
			}
			var next int64
			var reasons int32
			if err := s.pool.QueryRow(ctx, `SELECT generation,reasons FROM session_fact_dirty WHERE session_id='copy'`).Scan(&next, &reasons); err != nil || next <= generation || reasons&dirtyReasons[dirtyPublishPeer].reason == 0 {
				t.Fatalf("new copy markers lost recomputation: generation=%d previous=%d reasons=%d err=%v", next, generation, reasons, err)
			}
			run("copy", next, "written")
			var requests, turns, apiRequests int
			var tokens, publishedGeneration int64
			var coverage string
			if err := s.pool.QueryRow(ctx, `SELECT requests,turns,api_requests,input_tokens,coverage,source_generation
				FROM session_analysis_facts WHERE session_id='copy'`).Scan(&requests, &turns, &apiRequests, &tokens, &coverage, &publishedGeneration); err != nil {
				t.Fatal(err)
			}
			if requests != 1 || turns != 1 || apiRequests != 1 || tokens != 10 || coverage != "full" || publishedGeneration != next {
				t.Fatalf("copied work remained in facts: requests=%d turns=%d api=%d tokens=%d coverage=%s generation=%d", requests, turns, apiRequests, tokens, coverage, publishedGeneration)
			}
			if countWhere(t, s, "session_fact_dirty", "session_id='copy'") != 0 || countWhere(t, s, "session_turn_facts", "session_id='copy'") != 1 || countWhere(t, s, "session_timeline_members", "session_id='copy' AND turn_key IS NOT NULL") != 2 {
				t.Fatal("final facts, timeline and queue did not finish together")
			}
			current, err := s.ReadSessionTimelineState(ctx, "copy")
			if err != nil {
				t.Fatal(err)
			}
			var storedWitness []byte
			if err := s.pool.QueryRow(ctx, `SELECT source_witness FROM session_timeline_provenance WHERE session_id='copy' AND source_generation=$1`, next).Scan(&storedWitness); err != nil || !bytes.Equal(storedWitness, current.Witness) {
				t.Fatalf("published timeline witness stale after copy transition: %v", err)
			}
		})
	}
}
