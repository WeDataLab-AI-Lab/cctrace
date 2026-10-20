package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The shared database is reset for this test, so it cannot run in parallel.
func TestSessionOverviewAnalysisPagesBeforeSourceIdentity(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ids := []string{"lazy-newest", "lazy-middle", "lazy-oldest"}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,agent,record_type,user_id,profile_email)
 SELECT id,'claude','user','owner','profile' FROM unnest($1::text[]) id`, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups
 (scope_type,scope_value,session_id,agent,start_time,end_time,cost_usd,has_sync)
 SELECT 'user','owner',id,'claude',now()-n*interval '1 hour',now()-n*interval '1 hour',4-n,true
 FROM unnest($1::text[]) WITH ORDINALITY AS u(id,n)`, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id)
 SELECT id FROM unnest($1::text[]) id`, ids); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		load, err := s.LoadSessionFactsInput(ctx, id, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if outcome, err := publishSessionP4ForTest(ctx, s, id, load, 0); err != nil || outcome != "written" {
			t.Fatalf("publish fixture=%s err=%v", outcome, err)
		}
	}
	for _, sort := range []string{"activity", "start", "cost"} {
		t.Run(sort, func(t *testing.T) {
			f := SessionOverviewFilter{IncludeAnalysis: true, AnalysisOwner: true, DashboardUserID: 1,
				UserID: "owner", PersonalUserID: "owner", AnalysisSort: sort, Limit: 1, Offset: 1}
			base, args := sessionOverviewRollupListQuery(f)
			query, args := sessionOverviewAnalysisQuery(base, args, f, false, false)
			// Instrument identity reads only: evaluating a source outside the
			// selected page fails, regardless of wall-clock speed or query plan.
			// Runtime arithmetic prevents constant folding of the sentinel.
			const source = "FROM visible_session_records r"
			if strings.Count(query, source) != 1 {
				t.Fatal("identity source instrumentation must cover exactly one source")
			}
			query = strings.Replace(query, source, `FROM (
 SELECT session_id,profile_email,CASE WHEN session_id<>'lazy-middle'
 THEN (1/(length(session_id)-length(session_id)))::text ELSE user_id END AS user_id
 FROM visible_session_records) r`, 1)
			rows, err := s.pool.Query(ctx, query, args...)
			if err != nil {
				t.Fatal(err)
			}
			got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (string, error) {
				values, err := r.Values()
				if err != nil {
					return "", err
				}
				return values[0].(string), nil
			})
			if err != nil || len(got) != 1 || got[0] != "lazy-middle" {
				t.Fatalf("page must read only its identity sources: rows=%v err=%v", got, err)
			}
		})
	}
}
