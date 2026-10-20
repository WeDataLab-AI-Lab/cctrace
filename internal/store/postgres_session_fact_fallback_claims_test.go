package store

import (
	"context"
	"testing"
)

// These cases share the package's test database and backfill marker, so they
// must remain sequential. A zero row budget exercises the large-session guard
// without inserting hundreds of thousands of source rows.
func TestSessionFactFallbackCommitClaims(t *testing.T) {
	for _, complete := range []bool{true, false} {
		name := "backfill_incomplete"
		if complete {
			name = "backfill_complete"
		}
		t.Run(name, func(t *testing.T) {
			s := claimBackfillStore(t)
			ctx := context.Background()
			id := t.Name()
			if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,agent,record_type,user_id,profile_email)
 VALUES ($1,'claude','user','owner','profile')`, id); err != nil {
				t.Fatal(err)
			}
			if complete {
				if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ($1)`, sessionCommitClaimsBackfill); err != nil {
					t.Fatal(err)
				}
			}
			load, err := s.loadSessionFactsInput(ctx, id, "claude", 0)
			if err != nil {
				t.Fatal(err)
			}
			if load.Truncated == nil || load.CommitClaimsComplete != complete {
				t.Fatalf("fallback must retain backfill state: truncated=%v complete=%v want=%v", load.Truncated != nil, load.CommitClaimsComplete, complete)
			}
			if len(load.Input.CommitClaims) != 0 || len(load.Truncated.Turns) != 0 {
				t.Fatal("truncated fallback must not load detailed commit or turn facts")
			}
			outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, *load.Truncated, 0)
			if err != nil || outcome != "written" {
				t.Fatalf("fallback publish=(%q,%v), want written", outcome, err)
			}
			if countWhere(t, s, "session_fact_dirty", "session_id=$1", id) != 0 {
				t.Fatal("published fallback left a retry in the queue")
			}
		})
	}
	t.Run("backfill_changes_during_load", func(t *testing.T) {
		s := claimBackfillStore(t)
		ctx := context.Background()
		id := t.Name()
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES ($1)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,agent,record_type,user_id,profile_email)
 VALUES ($1,'claude','user','owner','profile')`, id); err != nil {
			t.Fatal(err)
		}
		load, err := s.loadSessionFactsInput(ctx, id, "claude", 0)
		if err != nil || load.Truncated == nil {
			t.Fatalf("fallback load: %v", err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ($1)`, sessionCommitClaimsBackfill); err != nil {
			t.Fatal(err)
		}
		outcome, err := s.publishSessionFact(ctx, id, load.Generation, load, *load.Truncated, 0)
		if err != nil || outcome != "commit_claims_source" {
			t.Fatalf("changed backfill publish=(%q,%v), want commit_claims_source", outcome, err)
		}
		if countWhere(t, s, "session_fact_dirty", "session_id=$1", id) != 1 {
			t.Fatal("source changed during load must remain queued")
		}
	})
}
