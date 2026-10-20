package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cctrace/internal/sessionfacts"
)

type overviewSnapshotTrace struct {
	afterList  func()
	afterCount func()
	queries    []string
}

type overviewSnapshotTraceKey struct{}

func (tr *overviewSnapshotTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	tr.queries = append(tr.queries, data.SQL)
	return context.WithValue(ctx, overviewSnapshotTraceKey{}, data.SQL)
}

func (tr *overviewSnapshotTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	query, _ := ctx.Value(overviewSnapshotTraceKey{}).(string)
	if tr.afterList != nil && strings.Contains(query, "'state',j.analysis_state") {
		after := tr.afterList
		tr.afterList = nil
		after()
	}
	if tr.afterCount != nil && (strings.Contains(query, "overview_population") || strings.Contains(query, "SELECT count(*) FROM joined j")) {
		after := tr.afterCount
		tr.afterCount = nil
		after()
	}
}

func TestSessionOverviewAnalysisCountSnapshot(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, dated := range []bool{false, true} {
		truncateTables(t, s)
		id := "overview-count-snapshot"
		userID := personalFixture(t, s, "owner@example.com", "owner")
		personalSource(t, s, id, "owner@example.com", "owner")
		class := "working"
		if _, err := s.PutPersonalSession(ctx, userID, id, "", "owner", &class, nil, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent,has_account)
			VALUES('user','owner',$1,now()-interval '1 hour',now(),true,'claude',false) ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, sessionOverviewRollupBackfill); err != nil {
			t.Fatal(err)
		}
		trace := &overviewSnapshotTrace{afterCount: func() {
			if _, err := s.pool.Exec(ctx, `UPDATE session_personal_state SET classification='done' WHERE session_id=$1`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.pool.Exec(ctx, `UPDATE session_overview_rollups SET has_account=true WHERE session_id=$1`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := s.pool.Exec(ctx, `UPDATE session_records SET login_email='account@example.com' WHERE session_id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}}
		cfg := s.pool.Config()
		cfg.ConnConfig.Tracer = trace
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		reader := &PgStore{pool: pool}
		f := SessionOverviewFilter{UserID: "owner", PersonalUserID: "owner", DashboardUserID: userID, LoginEmail: "account@example.com", IncludeAnalysis: true}
		if dated {
			since := time.Now().Add(-time.Hour)
			f.Since = &since
		}
		count, buckets, unattributed, err := reader.SessionOverviewAnalysisCountWithClassification(ctx, f, true)
		pool.Close()
		if err != nil || count != 1 || buckets["working"] != 1 || buckets["done"] != 0 || unattributed != 1 || trace.afterCount != nil {
			t.Fatalf("count snapshot dated=%t count=%d buckets=%v unattributed=%d callbackPending=%t err=%v", dated, count, buckets, unattributed, trace.afterCount != nil, err)
		}
		count, buckets, unattributed, err = s.SessionOverviewAnalysisCountWithClassification(ctx, f, true)
		if err != nil || count != 1 || buckets["done"] != 1 || unattributed != 0 {
			t.Fatalf("new count frame dated=%t count=%d buckets=%v unattributed=%d err=%v", dated, count, buckets, unattributed, err)
		}
	}
}

func TestSessionOverviewAnalysis_P5_FiltersUseOnlyOwnerVisibleRecordRows(t *testing.T) {
	f := SessionOverviewFilter{IncludeAnalysis: true, AnalysisOwner: true, IncludeSessionRecords: true, DashboardUserID: 9,
		PersonalUserID: "synthetic-owner", AnalysisBucket: "done", AnalysisKind: "implementation", AnalysisComponent: "API", AnalysisScreen: "session"}
	query, args := sessionOverviewAnalysisQuery("SELECT 'synthetic-session'::text AS session_id", nil, f, false, false)
	for _, fragment := range []string{"git_snapshots", "git_reason", "git_count", "CASE WHEN j.analysis_state", "p5r.kinds", "p5r.components", "p5r.screens", "recordVisibilityPredicate"} {
		if fragment == "recordVisibilityPredicate" {
			if strings.Contains(query, fragment) {
				t.Fatalf("unexpanded visibility placeholder in query: %s", query)
			}
			continue
		}
		if !strings.Contains(query, fragment) {
			t.Errorf("P5 filter query missing %q: %s", fragment, query)
		}
	}
	joined := strings.Join([]string{query}, "")
	if !strings.Contains(joined, "p5f.owner_user_id") || len(args) < 5 {
		t.Fatalf("P5 tag filter has no owner scope: query=%s args=%v", query, args)
	}
}

func TestSessionOverviewAnalysisPersonalSnapshot(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, dated := range []bool{false, true} {
		truncateTables(t, s)
		userID := personalFixture(t, s, "owner@example.com", "owner")
		id := "overview-personal-snapshot"
		personalSource(t, s, id, "owner@example.com", "owner")
		class, note := "working", "before snapshot\nprivate second line"
		if _, err := s.PutPersonalSession(ctx, userID, id, "", "owner", &class, &note, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1) ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		load, err := s.LoadSessionFactsInput(ctx, id, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1); err != nil || outcome != "written" {
			t.Fatalf("publish=%s err=%v", outcome, err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
			VALUES('user','owner',$1,now()-interval '1 hour',now(),true,'claude') ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, sessionOverviewRollupBackfill); err != nil {
			t.Fatal(err)
		}
		trace := &overviewSnapshotTrace{afterList: func() {
			if _, err := s.pool.Exec(ctx, `UPDATE session_personal_state SET classification='done',note='after snapshot' WHERE session_id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}}
		cfg := s.pool.Config()
		cfg.ConnConfig.Tracer = trace
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		reader := &PgStore{pool: pool}
		f := SessionOverviewFilter{UserID: "owner", PersonalUserID: "owner", DashboardUserID: userID, AnalysisOwner: true, IncludeAnalysis: true}
		if dated {
			since := time.Now().Add(-time.Hour)
			f.Since = &since
		}
		rows, err := reader.ListSessionOverviews(ctx, f)
		pool.Close()
		if err != nil || len(rows) != 1 || rows[0].Classification == nil || *rows[0].Classification != class || rows[0].FirstLine == nil || *rows[0].FirstLine != "before snapshot" {
			t.Fatalf("personal snapshot dated=%t rows=%+v err=%v queries=%v", dated, rows, err, trace.queries)
		}
		queries := strings.Join(trace.queries, "\n")
		if !strings.Contains(queries, "begin isolation level repeatable read read only") || !strings.Contains(queries, "SET LOCAL statement_timeout = '5s'") || strings.Count(queries, "SELECT owned.session_id,owned.classification") != 1 {
			t.Fatalf("missing bounded RR/batch query: %s", queries)
		}
		values, err := s.PersonalSessionOverviewValues(ctx, userID, "", "owner", []string{id})
		if err != nil || values[id].Classification != "done" {
			t.Fatalf("concurrent update did not commit: values=%v err=%v", values, err)
		}
	}
}

func verifyOverviewActivityMatchesSharedHelper(t *testing.T, s *PgStore, ctx context.Context, f SessionOverviewFilter, ids []string, counts map[string]int) {
	t.Helper()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	base, args, err := s.sessionOverviewAnalysisPopulation(ctx, tx, f)
	if err != nil || base == "" {
		t.Fatalf("activity population base=%q err=%v", base, err)
	}
	query, args := sessionOverviewAnalysisQuery(base, args, f, false, true)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	frameCounts := make(map[string]int)
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		frameCounts[name] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	owner := &personalOwner{userID: f.PersonalUserID, email: f.PersonalEmail}
	shared := make(map[string]int)
	for _, id := range ids {
		input, err := loadSessionActivity(ctx, tx, id, owner)
		if err != nil {
			t.Fatal(err)
		}
		shared[evaluateSessionActivity(time.Now(), input).category]++
	}
	for _, name := range sessionAnalysisCategories {
		if frameCounts[name] != shared[name] || counts[name] != frameCounts[name] {
			t.Fatalf("shared category divergence category=%s overview=%v sameframe=%v shared=%v", name, counts, frameCounts, shared)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOverviewAnalysisCurrentSourceDrift(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := "overview-drift-owner"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'owner','owner@example.com','claude',now(),'{}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1)
	if err != nil || outcome != "written" {
		t.Fatalf("publish=%s err=%v", outcome, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET title='private owner title',title_state='ok',
		requests=19,has_plan_file=true,last_activity_at=now() WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		VALUES('user','owner',$1,now()-interval '1 hour',now(),true,'claude')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, sessionOverviewRollupBackfill); err != nil {
		t.Fatal(err)
	}
	f := SessionOverviewFilter{UserID: "owner", PersonalUserID: "owner", DashboardUserID: 1, AnalysisOwner: true, IncludeAnalysis: true}
	for _, source := range []string{"snapshot", "raw", "event", "empty_raw", "empty_snapshot", "empty_event"} {
		t.Run(source, func(t *testing.T) {
			var err error
			switch source {
			case "snapshot":
				_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
					VALUES($1,'other','other@example.com','claude',now(),'{}'::jsonb)`, id)
			case "raw":
				_, err = s.pool.Exec(ctx, `INSERT INTO session_records(session_id,user_id,profile_email,agent,record_type,ts,raw)
					VALUES($1,'other','other@example.com','claude','user',now(),'{}'::jsonb)`, id)
			case "event":
				_, err = s.pool.Exec(ctx, `INSERT INTO otel_events(session_id,user_id,profile_email,event_name,ts)
					VALUES($1,'other','other@example.com','api_request',now())`, id)
			case "empty_raw":
				_, err = s.pool.Exec(ctx, `INSERT INTO session_records(session_id,user_id,profile_email,agent,record_type,ts,raw)
					VALUES($1,'','other@example.com','claude','user',now(),'{}'::jsonb)`, id)
			case "empty_snapshot":
				_, err = s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
					VALUES($1,'','other@example.com','claude',now(),'{}'::jsonb)`, id)
			case "empty_event":
				_, err = s.pool.Exec(ctx, `INSERT INTO otel_events(session_id,user_id,profile_email,event_name,ts)
					VALUES($1,'','other@example.com','api_request',now())`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, dated := range []bool{false, true} {
				filter := f
				if dated {
					since := time.Now().Add(-time.Hour)
					filter.Since = &since
					if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,user_id,profile_email,agent,record_type,ts,raw)
						VALUES($1,'owner','owner@example.com','claude','user',now(),'{}'::jsonb)`, id); err != nil {
						t.Fatal(err)
					}
				}
				rows, err := s.ListSessionOverviews(ctx, filter)
				if err != nil || len(rows) != 1 || rows[0].Analysis["state"] != "withheld" || rows[0].Analysis["reason"] != "mixed_identity" {
					t.Fatalf("drift source=%s dated=%t rows=%v err=%v", source, dated, rows, err)
				}
				if len(rows[0].Analysis) != 2 {
					t.Fatalf("drift source=%s dated=%t leaked fields=%v", source, dated, rows[0].Analysis)
				}
			}
			for _, field := range []string{"plan", "title", "category"} {
				filter := f
				switch field {
				case "plan":
					filter.AnalysisPlan = "file"
				case "title":
					filter.AnalysisQuery = "private owner"
				case "category":
					filter.AnalysisCategory = "recent"
				}
				count, err := s.CountSessionOverviews(ctx, filter)
				if err != nil || count != 0 {
					t.Fatalf("drift filter=%+v count=%d err=%v", filter, count, err)
				}
			}
			counts, err := s.SessionOverviewCategoryCounts(ctx, f)
			if err != nil || counts["recent"] != 0 {
				t.Fatalf("drift category counts=%v err=%v", counts, err)
			}
		})
	}
}

func TestSessionOverviewAnalysisUIDOnly(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := "overview-uid-only"
	personalSource(t, s, id, "", "owner")
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1) ON CONFLICT DO NOTHING`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1)
	if err != nil || outcome != "written" {
		t.Fatalf("publish=%s err=%v", outcome, err)
	}
	// Persisted UID-only facts may omit the unavailable profile dimension entirely.
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET profile_emails='{}',owner_profile_email=NULL WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var profiles int
	if err := s.pool.QueryRow(ctx, `SELECT cardinality(profile_emails) FROM session_analysis_facts WHERE session_id=$1`, id).Scan(&profiles); err != nil || profiles != 0 {
		t.Fatalf("UID-only profiles=%d err=%v", profiles, err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		VALUES('user','owner',$1,now()-interval '1 hour',now(),true,'claude') ON CONFLICT DO NOTHING`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, sessionOverviewRollupBackfill); err != nil {
		t.Fatal(err)
	}
	for _, dated := range []bool{false, true} {
		f := SessionOverviewFilter{UserID: "owner", PersonalUserID: "owner", DashboardUserID: 1, AnalysisOwner: true, IncludeAnalysis: true}
		if dated {
			since := time.Now().Add(-time.Hour)
			f.Since = &since
		}
		rows, err := s.ListSessionOverviews(ctx, f)
		if err != nil || len(rows) != 1 || rows[0].Analysis["state"] != "ready" {
			t.Fatalf("UID-only dated=%t rows=%+v err=%v", dated, rows, err)
		}
		category, ok := rows[0].Analysis["category"].(string)
		if !ok || category == "" {
			t.Fatalf("UID-only category missing: %v", rows[0].Analysis)
		}
		f.AnalysisCategory = category
		count, err := s.CountSessionOverviews(ctx, f)
		if err != nil || count != 1 {
			t.Fatalf("UID-only dated=%t count=%d err=%v", dated, count, err)
		}
		counts, err := s.SessionOverviewCategoryCounts(ctx, f)
		if err != nil || counts[category] != 1 {
			t.Fatalf("UID-only dated=%t counts=%v err=%v", dated, counts, err)
		}
	}
}

func TestSessionOverviewAnalysisProfileOnly(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := "overview-profile-only"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'','owner@example.com','claude',now(),'{}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1)
	if err != nil || outcome != "written" {
		t.Fatalf("publish=%s err=%v", outcome, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET last_activity_at=now() WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		VALUES('profile','owner@example.com',$1,now()-interval '1 hour',now(),true,'claude')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, sessionOverviewRollupBackfill); err != nil {
		t.Fatal(err)
	}
	f := SessionOverviewFilter{ProfileEmail: "owner@example.com", PersonalEmail: "owner@example.com", DashboardUserID: 1, AnalysisOwner: true, IncludeAnalysis: true}
	for _, dated := range []bool{false, true} {
		if dated {
			since := time.Now().Add(-time.Hour)
			f.Since = &since
			if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,user_id,profile_email,agent,record_type,ts,raw)
				VALUES($1,'','owner@example.com','claude','user',now(),'{}'::jsonb)`, id); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := s.ListSessionOverviews(ctx, f)
		if err != nil || len(rows) != 1 || rows[0].Analysis["state"] != "ready" {
			t.Fatalf("profile-only dated=%t rows=%v err=%v", dated, rows, err)
		}
		counts, err := s.SessionOverviewCategoryCounts(ctx, f)
		if err != nil || counts["recent"] != 1 {
			t.Fatalf("profile-only category dated=%t counts=%v err=%v", dated, counts, err)
		}
	}
}

func TestSessionOverviewAnalysisRollupAndPeriod(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	ids := []string{"overview-analysis-a", "overview-analysis-b", "overview-analysis-c"}
	for _, id := range ids {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
			VALUES($1,'owner','owner@example.com','claude',now(),'{}'::jsonb)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
			t.Fatal(err)
		}
		load, err := s.LoadSessionFactsInput(ctx, id, "claude")
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1)
		if err != nil || outcome != "written" {
			t.Fatalf("publish %s: %s, %v", id, outcome, err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET requests=CASE session_id WHEN $1 THEN 9 WHEN $2 THEN 3 ELSE 1 END,
		title=CASE session_id WHEN $1 THEN 'Match alpha' WHEN $2 THEN 'Match beta' ELSE 'Other' END,
		title_state='ok', has_plan_file=session_id=$1, used_plan_mode=session_id=$2,
		last_activity_at=now() WHERE session_id=ANY($3)`, ids[0], ids[1], ids); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		SELECT 'user','owner',session_id,now() - interval '1 hour',now(),true,'claude'
		FROM session_analysis_facts WHERE session_id=ANY($1)`, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, sessionOverviewRollupBackfill); err != nil {
		t.Fatal(err)
	}
	f := SessionOverviewFilter{UserID: "owner", PersonalUserID: "owner", DashboardUserID: 1, AnalysisOwner: true,
		IncludeAnalysis: true, AnalysisSort: "requests", Limit: 1, AnalysisQuery: "Match", AnalysisPlan: "file"}
	for _, dated := range []bool{false, true} {
		if dated {
			since := time.Now().Add(-time.Hour)
			f.Since = &since
			// The period path uses visible events and records, not snapshots.
			for _, id := range ids {
				if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email,agent,ts,raw)
					VALUES($1,'user','owner','owner@example.com','claude',now(),'{}'::jsonb)`, id); err != nil {
					t.Fatal(err)
				}
			}
		}
		rows, err := s.ListSessionOverviews(ctx, f)
		if err != nil {
			t.Fatalf("dated=%v list: %v", dated, err)
		}
		if len(rows) != 1 || rows[0].SessionID != ids[0] || rows[0].Analysis["state"] != "ready" {
			t.Fatalf("dated=%v rows=%+v", dated, rows)
		}
		count, err := s.CountSessionOverviews(ctx, f)
		if err != nil || count != 1 {
			t.Fatalf("dated=%v count=%d err=%v", dated, count, err)
		}
		f.AnalysisPlan = ""
		count, err = s.CountSessionOverviews(ctx, f)
		if err != nil || count != 2 {
			t.Fatalf("dated=%v total beyond page=%d err=%v", dated, count, err)
		}
		f.AnalysisPlan = "file"
	}
	f.Since, f.AnalysisPlan, f.AnalysisQuery = nil, "", ""
	rows, err := s.ListSessionOverviews(ctx, f)
	if err != nil || len(rows) != 1 || rows[0].SessionID != ids[0] {
		t.Fatalf("requests sort before page: rows=%v err=%v", rows, err)
	}
	f.AnalysisCategory = "recent"
	count, err := s.CountSessionOverviews(ctx, f)
	if err != nil || count != 3 {
		t.Fatalf("category filter before page: count=%d err=%v", count, err)
	}
	f.AnalysisCategory = ""
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET rules_version=$1 WHERE session_id=$2`, sessionfacts.RulesVersion-1, ids[1]); err != nil {
		t.Fatal(err)
	}
	f.AnalysisSort, f.Limit = "requests", 3
	rows, err = s.ListSessionOverviews(ctx, f)
	if err != nil || len(rows) != 3 || rows[1].Analysis["state"] != "stale" {
		t.Fatalf("stale rules state rows=%v err=%v", rows, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET rules_version=$1 WHERE session_id=$2`, sessionfacts.RulesVersion, ids[1]); err != nil {
		t.Fatal(err)
	}
	f.Limit = 1
	f.Classification = "unsorted"
	for _, dated := range []bool{false, true} {
		if dated {
			since := time.Now().Add(-time.Hour)
			f.Since = &since
		}
		count, err = s.CountSessionOverviews(ctx, f)
		if err != nil || count != 3 {
			t.Fatalf("classification count dated=%v count=%d err=%v", dated, count, err)
		}
		rows, err = s.ListSessionOverviews(ctx, f)
		if err != nil || len(rows) != 1 || rows[0].SessionID != ids[0] {
			t.Fatalf("classification list dated=%v rows=%v err=%v", dated, rows, err)
		}
	}
	f.Since, f.Classification = nil, ""
	for _, dated := range []bool{false, true} {
		combined := f
		if dated {
			since := time.Now().Add(-time.Hour)
			combined.Since = &since
		}
		count, buckets, _, err := s.SessionOverviewAnalysisCountWithClassification(ctx, combined, false)
		if err != nil || count != 3 || buckets["unsorted"] != 3 {
			t.Fatalf("combined count dated=%t count=%d classes=%v err=%v", dated, count, buckets, err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_overview_rollups SET entrypoint='sdk-cli',has_enriched=true
		WHERE session_id=$1`, ids[2]); err != nil {
		t.Fatal(err)
	}
	f.Source = "interactive"
	counts, err := s.SessionOverviewCategoryCounts(ctx, f)
	if err != nil || counts["recent"] != 2 {
		t.Fatalf("interactive category excludes headless=%v, %v", counts, err)
	}
	f.Source = "all"
	counts, err = s.SessionOverviewCategoryCounts(ctx, f)
	if err != nil || counts["recent"] != 3 {
		t.Fatalf("source all category includes headless=%v, %v", counts, err)
	}
	f.Source = "headless"
	counts, err = s.SessionOverviewCategoryCounts(ctx, f)
	if err != nil || counts["recent"] != 1 {
		t.Fatalf("headless category population=%v, %v", counts, err)
	}
	f.Source = ""
	counts, err = s.SessionOverviewCategoryCounts(ctx, f)
	if err != nil || counts["recent"] != 3 {
		t.Fatalf("unpaged categories=%v, %v", counts, err)
	}
	f.AnalysisOwner = false
	counts, err = s.SessionOverviewCategoryCounts(ctx, f)
	if err != nil || counts["recent"] != 0 {
		t.Fatalf("nonowner categories=%v, %v", counts, err)
	}
	f.AnalysisOwner = true
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET last_activity_at=now()-interval '8 hours' WHERE session_id=ANY($1)`, ids); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		statusAt := "now()-interval '7 hours'"
		if i == 1 {
			statusAt = "NULL"
		}
		status := "busy"
		if i == 2 {
			status = "gone"
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states(session_id,user_id,profile_email,pid,status,status_changed_at,heartbeat_at,identity_provenance)
			VALUES($1,'owner','owner@example.com',$2,$3,`+statusAt+`,now()-interval '1 minute','personal_token')`, id, i+1, status); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_observation_events(session_id,user_id,profile_email,event_id,kind,observed_at,identity_provenance)
		VALUES($1,'owner','owner@example.com','end','gone',now()-interval '1 minute','personal_token')`, ids[2]); err != nil {
		t.Fatal(err)
	}
	counts, err = s.SessionOverviewCategoryCounts(ctx, f)
	if err != nil || counts["stale_live"] != 0 || counts["running"] != 2 || counts["recent"] != 1 {
		t.Fatalf("current raw activity supersedes old facts=%v, %v", counts, err)
	}
	verifyOverviewActivityMatchesSharedHelper(t, s, ctx, f, ids, counts)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states(session_id,user_id,profile_email,pid,status,status_changed_at,heartbeat_at,identity_provenance)
		VALUES($1,'owner','owner@example.com',40,'idle',now()-interval '7 hours',now(),'personal_token'),
		($2,'owner','owner@example.com',41,'idle',now()-interval '7 hours',now(),'unknown')`, ids[0], ids[2]); err != nil {
		t.Fatal(err)
	}
	counts, err = s.SessionOverviewCategoryCounts(ctx, f)
	if err != nil || counts["idle"] != 1 || counts["running"] != 1 || counts["recent"] != 1 || counts["stale_live"] != 0 {
		t.Fatalf("multi-pid latest verified live=%v, %v", counts, err)
	}
	verifyOverviewActivityMatchesSharedHelper(t, s, ctx, f, ids, counts)
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET user_ids=ARRAY['owner','other'], owner_user_id=NULL WHERE session_id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	f.AnalysisSort, f.Limit = "start", 10
	rows, err = s.ListSessionOverviews(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	var mixedState map[string]interface{}
	for _, row := range rows {
		if row.SessionID == ids[0] {
			mixedState = row.Analysis
		}
	}
	if mixedState["state"] != "withheld" || mixedState["reason"] != "mixed_identity" {
		t.Fatalf("mixed identity analysis=%v", mixedState)
	}
	f.AnalysisOwner = false
	rows, err = s.ListSessionOverviews(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if _, found := row.Analysis["category"]; found {
			t.Fatalf("nonowner category leaked: %v", row.Analysis)
		}
		if _, found := row.Analysis["signals"]; found {
			t.Fatalf("nonowner signals leaked: %v", row.Analysis)
		}
	}
	f.AnalysisOwner = true
	if _, err := s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES($1)`, ids[0]); err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListSessionOverviews(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.SessionID == ids[0] && row.Analysis["state"] == "withheld" {
			t.Fatalf("excluded facts must not disclose mixed identity: analysis=%v", row.Analysis)
		}
	}
}
