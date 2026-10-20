package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cctrace/internal/sessionfacts"
)

type calendarSnapshotTrace struct {
	afterCandidates func()
}

func (tr *calendarSnapshotTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, overviewSnapshotTraceKey{}, data.SQL)
}

func (tr *calendarSnapshotTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	query, _ := ctx.Value(overviewSnapshotTraceKey{}).(string)
	if tr.afterCandidates != nil && strings.HasPrefix(query, "WITH scoped AS (") {
		after := tr.afterCandidates
		tr.afterCandidates = nil
		after()
	}
}

func TestSessionCalendarSnapshotSurvivesConcurrentDeletion(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(time.Second)
	calendarFixture(t, s, "snapshot", "owner", "claude", "full", "observed", from, true)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,branch,started_at,ended_at,input_kind,is_request,status,preview_state,cost_source,context_source,rules_version,detail)
		VALUES('snapshot','t','claude',1,'active',$1,$1,'text',true,'ended','none','none','unknown',2,
		jsonb_build_object('inputs',jsonb_build_array(jsonb_build_object('ts',$2::text))))`, from, calendarTime(from)); err != nil {
		t.Fatal(err)
	}
	trace := &calendarSnapshotTrace{afterCandidates: func() {
		if _, err := s.pool.Exec(ctx, `INSERT INTO deleted_sessions(session_id) VALUES('snapshot')`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM session_turn_facts WHERE session_id='snapshot'`); err != nil {
			t.Fatal(err)
		}
	}}
	cfg := s.pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	f := SessionCalendarFilter{From: from, To: from.Add(time.Hour), Access: SessionAnalysisAccess{User: true, UserID: "owner"}}
	result, err := (&PgStore{pool: pool}).ReadSessionCalendar(ctx, f)
	if err != nil || len(result.Sessions) != 1 || len(result.Sessions[0].Inputs.Items) != 1 || trace.afterCandidates != nil {
		t.Fatalf("calendar frame torn: %+v err=%v", result, err)
	}
	result, err = s.ReadSessionCalendar(ctx, f)
	if err != nil || len(result.Sessions) != 0 {
		t.Fatalf("next frame missed deletion: %+v err=%v", result, err)
	}
}

// Shared fixture tables require a serial integration test. The two-second
// deadline is the read budget being verified, not a wait before an assertion.
func TestSessionCalendarLargeWeekKeepsAllSessionsWithinReadBudget(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := t.Context()
	from := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	for _, query := range []string{
		`INSERT INTO session_records(ts,session_id,record_type,uuid,user_id,profile_email,agent,raw)
		 SELECT $1::timestamptz+n*interval '1 millisecond','calendar-large-week','assistant','large-'||n,'owner','owner@example.com','claude','{"message":{"content":"small body"}}'::jsonb
		 FROM generate_series(1,200000) n`,
		`INSERT INTO session_records(ts,session_id,record_type,uuid,user_id,profile_email,agent,raw)
		 SELECT $1::timestamptz,'calendar-week-'||n,'user','small-'||n,'owner','owner@example.com','claude','{"message":{"content":"small request"}}'::jsonb
		 FROM generate_series(1,2000) n`,
		`INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		 SELECT 'user','owner',id,$1::timestamptz,$1::timestamptz+interval '1 hour',true,'claude'
		 FROM (SELECT 'calendar-week-'||n id FROM generate_series(1,2000) n UNION ALL SELECT 'calendar-large-week') sessions`,
		`INSERT INTO session_analysis_facts(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,
		 transcript,title,title_source,title_state,turn_boundaries,cost_source,output_hash,source_generation,rules_version,first_activity_at,last_activity_at)
		 SELECT id,'claude','full',ARRAY['owner'],ARRAY['owner@example.com'],'owner','owner@example.com',true,id,'first_request','ok','observed','none',decode('01','hex'),1,6,$1::timestamptz,$1::timestamptz+interval '1 hour'
		 FROM (SELECT 'calendar-week-'||n id FROM generate_series(1,2000) n UNION ALL SELECT 'calendar-large-week') sessions`,
		`INSERT INTO session_activity_segments(session_id,seg_start,seg_end,owner_user_id,owner_profile_email,agent,records,human_inputs,rules_version)
		 SELECT id,$1::timestamptz,$1::timestamptz+interval '1 hour','owner','owner@example.com','claude',1,1,6
		 FROM (SELECT 'calendar-week-'||n id FROM generate_series(1,2000) n UNION ALL SELECT 'calendar-large-week') sessions`,
	} {
		if _, err := s.pool.Exec(ctx, query, from); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `ANALYZE session_records; ANALYZE session_activity_segments; ANALYZE session_analysis_facts; ANALYZE session_overview_rollups`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	started := time.Now()
	result, err := s.ReadSessionCalendar(readCtx, SessionCalendarFilter{From: from, To: from.Add(2 * time.Hour),
		SessionOverviewFilter: SessionOverviewFilter{Source: "all", FoldLineage: true}, Access: SessionAnalysisAccess{User: true, UserID: "owner"}})
	if err != nil || len(result.Sessions) != 2001 {
		t.Fatalf("large week: sessions=%d want=2001 elapsed=%s err=%v", len(result.Sessions), time.Since(started), err)
	}
	for _, session := range result.Sessions {
		if len(session.Segments) != 1 {
			t.Fatalf("session %s lost its activity: %+v", session.SessionID, session.Segments)
		}
	}
}

func calendarFixture(t *testing.T, s *PgStore, id, uid, agent, coverage, boundaries string, from time.Time, segment bool) {
	t.Helper()
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot) VALUES($1,$2,$2||'@example.com',$3,$4,'{}')`,
		`INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent,model,cost_usd,input_tokens,project_hash)
		 VALUES('user',$2,$1,$4,$4::timestamptz+interval '1 hour',true,$3,'real-model',12.5,42,'real-project')`,
	} {
		if _, err := s.pool.Exec(ctx, query, id, uid, agent, from); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_analysis_facts(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,
		transcript,title,title_source,title_state,turn_boundaries,cost_source,output_hash,source_generation,rules_version,first_activity_at,last_activity_at)
		VALUES($1,$3,$5,ARRAY[$2],ARRAY[$2||'@example.com'],$2,$2||'@example.com',true,'real title','first_request','ok',$6,'none',decode('01','hex'),1,2,$4,$4::timestamptz+interval '1 hour')`, id, uid, agent, from, coverage, boundaries); err != nil {
		t.Fatal(err)
	}
	if segment {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_activity_segments(session_id,seg_start,seg_end,owner_user_id,owner_profile_email,agent,records,human_inputs,rules_version)
			VALUES($1,$3,$3::timestamptz+interval '1 hour',$2,$2||'@example.com',$4,3,2,2)`, id, uid, from, agent); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionCalendarAdminSelectedMixedSessions(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct{ id, agent, coverage string }{
		{"mixed-full", "claude", "full"}, {"mixed-truncated", "claude", "truncated"}, {"mixed-codex", "codex", "full"},
	} {
		calendarFixture(t, s, tc.id, "selected", tc.agent, tc.coverage, "observed", from, tc.id == "mixed-full")
		for _, query := range []string{
			`UPDATE session_analysis_facts SET user_ids=ARRAY['selected','other'],profile_emails=ARRAY['selected@example.com','other@example.com'],owner_user_id=NULL,owner_profile_email=NULL,requests=7 WHERE session_id=$1`,
			`UPDATE session_activity_segments SET owner_user_id=NULL,owner_profile_email=NULL WHERE session_id=$1`,
			`INSERT INTO session_records(session_id,user_id,profile_email,record_type) VALUES($1,'other','other@example.com','user')`,
			`INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
			 SELECT 'profile','selected@example.com',session_id,start_time,end_time,has_sync,agent FROM session_overview_rollups WHERE session_id=$1 AND scope_type='user' ON CONFLICT DO NOTHING`,
			`INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,branch,started_at,ended_at,input_kind,is_request,status,preview_state,cost_source,context_source,rules_version,detail)
			 SELECT session_id,'mixed-turn',agent,1,'active',first_activity_at,last_activity_at,'text',true,'ended','none','none','unknown',2,
			 jsonb_build_object('inputs',jsonb_build_array(jsonb_build_object('ts',first_activity_at))) FROM session_analysis_facts WHERE session_id=$1`,
		} {
			if _, err := s.pool.Exec(ctx, query, tc.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, legacy := range []bool{false, true} {
		f := SessionCalendarFilter{From: from, To: from.Add(2 * time.Hour), Access: SessionAnalysisAccess{OwnerUserID: "admin"}}
		if legacy {
			f.ProfileEmail = "selected@example.com"
			f.Access = SessionAnalysisAccess{OwnerProfileEmail: "admin@example.com"}
		} else {
			f.UserID = "selected"
		}
		for _, own := range []bool{false, true} {
			if own {
				f.Access.OwnerUserID, f.Access.OwnerProfileEmail = f.UserID, f.ProfileEmail
			}
			result, err := s.ReadSessionCalendar(ctx, f)
			if err != nil || result.OwnerView != own || len(result.Sessions) != 3 {
				t.Fatalf("legacy=%t own=%t mixed result=%+v err=%v", legacy, own, result, err)
			}
			for _, item := range result.Sessions {
				if item.Requests == nil || *item.Requests != 7 || item.Title["state"] != "ok" || item.Overview == nil {
					t.Fatalf("whole-session facts missing: %+v", item)
				}
				if _, ok := item.Colors["status"]; ok {
					t.Fatal("mixed session exposed owner status")
				}
				if item.SessionID == "mixed-full" && (len(item.Segments) != 1 || len(item.Inputs.Items) != 1) {
					t.Fatalf("mixed full activity/input missing: %+v", item)
				}
				if item.SessionID != "mixed-full" && len(item.Segments) != 0 {
					t.Fatal("mixed range-only fabricated activity")
				}
			}
		}
		f.Access.User, f.Access.UserID, f.Access.ProfileEmail = true, f.UserID, f.ProfileEmail
		result, err := s.ReadSessionCalendar(ctx, f)
		if err != nil || len(result.Sessions) != 0 {
			t.Fatalf("restricted mixed result=%+v err=%v", result, err)
		}
	}
}

func TestSessionCalendarStatusColorUsesTheSharedJudgementAndGitSnapshot(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	rows := []struct {
		id, turnStatus, turnState, questionState string
		question                                 int
		gitCount                                 *int
		live                                     bool
	}{
		{id: "calendar-midway", turnStatus: "interrupted", turnState: "observed", questionState: "observed"},
		{id: "calendar-leftover", turnStatus: "ended", turnState: "observed", questionState: "observed", question: 1},
		{id: "calendar-committed", turnStatus: "ended", turnState: "observed", questionState: "observed", gitCount: ptrInt(2)},
		{id: "calendar-unknown", turnState: "undeterminable", questionState: "undeterminable"},
		{id: "calendar-running", turnStatus: "open", turnState: "observed", questionState: "observed", live: true},
	}
	for i, row := range rows {
		at := from.Add(time.Duration(i) * time.Second)
		calendarFixture(t, s, row.id, "owner", "claude", "full", "observed", at, true)
		detail, _ := json.Marshal(map[string]interface{}{"signals": map[string]interface{}{
			"turn":       map[string]interface{}{"state": row.turnState},
			"question":   map[string]interface{}{"state": row.questionState, "value": row.question},
			"background": map[string]interface{}{"state": "observed", "value": 0},
		}})
		if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET last_turn_status=$2,detail=$3 WHERE session_id=$1`, row.id, row.turnStatus, detail); err != nil {
			t.Fatal(err)
		}
		if row.gitCount != nil {
			snapshot, _ := json.Marshal(map[string]interface{}{"git": map[string]interface{}{
				"changed_at": at.Add(-24 * time.Hour), "edited_files": 5, "uncommitted_edited_files": *row.gitCount,
				"repositories": 1, "unresolved": 0, "partial": false,
			}})
			if _, err := s.pool.Exec(ctx, `UPDATE session_snapshots SET snapshot=$2 WHERE session_id=$1`, row.id, snapshot); err != nil {
				t.Fatal(err)
			}
		}
		if row.live {
			if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states
				(session_id,user_id,profile_email,pid,proc_start,status,heartbeat_at,identity_provenance)
				VALUES($1,'owner','owner@example.com',1,'start','busy',now(),'personal_token')`, row.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	filter := SessionCalendarFilter{From: from.Add(-time.Minute), To: from.Add(2 * time.Hour), SessionOverviewFilter: SessionOverviewFilter{
		UserID: "owner", PersonalUserID: "owner", PersonalEmail: "owner@example.com", IncludeSessionRecords: true,
	}, Access: SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"}}
	result, err := s.ReadSessionCalendar(ctx, filter)
	if err != nil || len(result.Sessions) != len(rows) {
		t.Fatalf("calendar status result=%+v err=%v", result, err)
	}
	got := make(map[string]interface{}, len(result.Sessions))
	for _, row := range result.Sessions {
		got[row.SessionID] = row.Colors["status"]
	}
	want := map[string]interface{}{"calendar-midway": "midway", "calendar-leftover": "leftover", "calendar-committed": "leftover",
		"calendar-unknown": "unknown", "calendar-running": "running"}
	for id, color := range want {
		if got[id] != color {
			t.Fatalf("calendar status color %s=%v want %v (all=%v)", id, got[id], color, got)
		}
	}
}

func TestSessionCalendarRangesScopeClassificationAndPrecision(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	from := time.Now().UTC().Truncate(time.Second)
	userID := personalFixture(t, s, "owner@example.com", "owner")
	calendarFixture(t, s, "truncated", "owner", "claude", "truncated", "undeterminable", from, false)
	calendarFixture(t, s, "codex", "owner", "codex", "full", "observed", from, false)
	calendarFixture(t, s, "redacted", "owner", "claude", "full", "undeterminable", from, true)
	calendarFixture(t, s, "normal", "owner", "claude", "full", "observed", from, true)
	calendarFixture(t, s, "outside", "owner", "codex", "full", "observed", from.Add(-24*time.Hour), false)
	calendarFixture(t, s, "foreign", "other", "claude", "full", "observed", from, true)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,branch,started_at,ended_at,input_kind,is_request,status,preview_state,cost_source,context_source,rules_version,detail)
		VALUES('normal','old-turn','claude',1,'active',$1,$1,'text',true,'ended','none','none','unknown',2,
		jsonb_build_object('inputs',jsonb_build_array(jsonb_build_object('ts',$2::text),jsonb_build_object('ts',$3::text)),
		'extra_input_ts',jsonb_build_array($4::text)))`, from.Add(-time.Hour), calendarTime(from.Add(100*time.Millisecond)), calendarTime(from), calendarTime(from.Add(50*time.Millisecond))); err != nil {
		t.Fatal(err)
	}
	f := SessionCalendarFilter{From: from, To: from.Add(2 * time.Hour), Access: SessionAnalysisAccess{OwnerUserID: "owner", DashboardUserID: userID},
		SessionOverviewFilter: SessionOverviewFilter{DashboardUserID: userID, PersonalUserID: "owner"}}
	result, err := s.ReadSessionCalendar(ctx, f)
	if err != nil || !result.OwnerView || len(result.Sessions) != 4 {
		t.Fatalf("default own candidates=%+v err=%v", result, err)
	}
	for _, item := range result.Sessions {
		if item.Overview == nil || item.Overview.CostUSD != 12.5 || item.Overview.InputTokens != 42 || item.Overview.Model != "real-model" {
			t.Fatalf("real overview missing: %+v", item)
		}
		if item.SessionID == "truncated" || item.SessionID == "codex" {
			if len(item.Segments) != 0 || item.Range["start"] != calendarTime(from) {
				t.Fatalf("range-only fabricated activity: %+v", item)
			}
		}
		if item.SessionID == "redacted" && (item.Inputs.State != "undeterminable" || item.Segments[0].HumanInputs != nil) {
			t.Fatalf("unobserved inputs became zero: %+v", item)
		}
		if item.SessionID == "normal" {
			want := []string{calendarTime(from), calendarTime(from.Add(50 * time.Millisecond)), calendarTime(from.Add(100 * time.Millisecond))}
			if len(item.Inputs.Items) != len(want) {
				t.Fatalf("old turn inputs missing: %+v", item.Inputs)
			}
			for i := range want {
				if item.Inputs.Items[i].TS != want[i] {
					t.Fatalf("instant order=%+v want=%v", item.Inputs.Items, want)
				}
			}
		}
	}
	f.UserID = "owner"
	result, err = s.ReadSessionCalendar(ctx, f)
	if err != nil || !result.OwnerView {
		t.Fatalf("explicit own owner_view=%t err=%v", result.OwnerView, err)
	}
	f.UserID = "other"
	result, err = s.ReadSessionCalendar(ctx, f)
	if err != nil || result.OwnerView || len(result.Sessions) != 1 {
		t.Fatalf("selected person=%+v err=%v", result, err)
	}
	if _, ok := result.Sessions[0].Colors["status"]; ok {
		t.Fatal("other person's status exposed")
	}
	f.UserID = ""
	f.LoginEmail = "shared@example.com"
	result, err = s.ReadSessionCalendar(ctx, f)
	if err != nil || len(result.Sessions) != 0 {
		t.Fatalf("login account widened scope: %+v err=%v", result, err)
	}
	f.LoginEmail = ""
	personalSource(t, s, "normal", "owner@example.com", "owner")
	class := "working"
	if _, err := s.PutPersonalSession(ctx, userID, "normal", "", "owner", &class, nil, 0); err != nil {
		t.Fatal(err)
	}
	f.Classification = class
	result, err = s.ReadSessionCalendar(ctx, f)
	if err != nil || len(result.Sessions) != 1 || result.Sessions[0].SessionID != "normal" {
		t.Fatalf("personal classification=%+v err=%v", result, err)
	}
	for _, mutation := range []string{`UPDATE session_personal_state SET expires_at=now()-interval '1 second'`,
		`UPDATE session_personal_state SET expires_at=now()+interval '1 day',source_user_id='foreign'`} {
		if _, err := s.pool.Exec(ctx, mutation); err != nil {
			t.Fatal(err)
		}
		result, err = s.ReadSessionCalendar(ctx, f)
		if err != nil || len(result.Sessions) != 0 {
			t.Fatalf("private proof fence=%+v err=%v", result, err)
		}
	}
	f.Classification = "invalid"
	if _, err := s.ReadSessionCalendar(ctx, f); err != ErrPersonalInvalid {
		t.Fatalf("invalid classification=%v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		VALUES('profile','owner@example.com','normal',$1,$2,true,'claude') ON CONFLICT DO NOTHING`, from, from.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	legacy := SessionCalendarFilter{From: from, To: from.Add(2 * time.Hour), Access: SessionAnalysisAccess{User: true, ProfileEmail: "owner@example.com"}}
	result, err = s.ReadSessionCalendar(ctx, legacy)
	if err != nil || !result.OwnerView || len(result.Sessions) != 1 || result.Sessions[0].SessionID != "normal" {
		t.Fatalf("legacy caller scope=%+v err=%v", result, err)
	}
}

func TestSessionCalendarInvalidatedFactsAndSourceFamilies(t *testing.T) {
	s := acquireTestStore(t)
	ctx := context.Background()
	for _, cause := range []string{"foreign_event", "empty_event", "foreign_raw", "empty_snapshot", "retained_sources_gone", "excluded_account", "deleted", "retention", "copy_gate"} {
		t.Run(cause, func(t *testing.T) {
			truncateTables(t, s)
			from := time.Now().UTC().Truncate(time.Second)
			calendarFixture(t, s, "fenced", "owner", "claude", "full", "observed", from, true)
			queries := map[string]string{
				"foreign_event":         `INSERT INTO otel_events(session_id,user_id,event_name) VALUES('fenced','other','api_request')`,
				"empty_event":           `INSERT INTO otel_events(session_id,user_id,event_name) VALUES('fenced','','api_request')`,
				"foreign_raw":           `INSERT INTO session_records(session_id,user_id,record_type) VALUES('fenced','other','user')`,
				"empty_snapshot":        `UPDATE session_snapshots SET user_id='' WHERE session_id='fenced'`,
				"retained_sources_gone": `DELETE FROM session_snapshots WHERE session_id='fenced'`,
				"excluded_account":      `INSERT INTO excluded_accounts(login_email) VALUES('blocked@example.com')`,
				"deleted":               `INSERT INTO deleted_sessions(session_id) VALUES('fenced')`,
				"retention":             `INSERT INTO session_fact_dirty(session_id,generation,removal_generation,reasons) VALUES('fenced',2,2,128)`,
				"copy_gate":             `INSERT INTO schema_backfills(name) VALUES('session_fact_copies_v1') ON CONFLICT(name) DO UPDATE SET done_at=now()`,
			}
			if cause == "copy_gate" {
				t.Cleanup(func() {
					if _, err := s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`); err != nil {
						t.Errorf("clean up calendar copy gate: %v", err)
					}
				})
			}
			if _, err := s.pool.Exec(ctx, queries[cause]); err != nil {
				t.Fatal(err)
			}
			if cause == "excluded_account" {
				if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET account_keys='[{"login_email":"blocked@example.com"}]' WHERE session_id='fenced'`); err != nil {
					t.Fatal(err)
				}
			}
			cfg := s.pool.Config()
			trace := &overviewSnapshotTrace{}
			cfg.ConnConfig.Tracer = trace
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			result, err := (&PgStore{pool: pool}).ReadSessionCalendar(ctx, SessionCalendarFilter{From: from, To: from.Add(time.Hour), Access: SessionAnalysisAccess{User: true, UserID: "owner"}})
			pool.Close()
			if err != nil {
				t.Fatal(err)
			}
			if cause == "retention" || cause == "copy_gate" {
				if len(result.Sessions) != 1 || result.Sessions[0].Facts != "building" || len(result.Sessions[0].Segments) != 0 || result.Sessions[0].Inputs.State != "undeterminable" {
					t.Fatalf("invalidated facts leaked: %+v", result)
				}
				body, _ := json.Marshal(result.Sessions[0].Title)
				if strings.Contains(string(body), "real title") {
					t.Fatal("invalidated title leaked")
				}
			} else if len(result.Sessions) != 0 {
				t.Fatalf("source fence leaked: %+v", result)
			}
			queriesText := strings.Join(trace.queries, "\n")
			if !strings.Contains(queriesText, "begin isolation level repeatable read read only") || !strings.Contains(queriesText, "SET LOCAL statement_timeout = '5s'") {
				t.Fatalf("missing bounded snapshot: %s", queriesText)
			}
		})
	}
}

func TestSessionCalendarSegmentBoundaryIdentityAndInputs(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	from := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 15, 4, 0, 0, 0, time.UTC)
	id := "calendar-cross-week"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'owner','owner@example.com','claude',$2,'{}'::jsonb)`, id, from)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		VALUES('user','owner',$1,$2,$3,true,'claude')`, id, from.Add(-24*time.Hour), to.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_analysis_facts(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,
		transcript,title_source,title_state,turn_boundaries,cost_source,output_hash,source_generation,rules_version)
		VALUES($1,'claude','full',ARRAY['owner'],ARRAY['owner@example.com'],'owner','owner@example.com',true,'none','none','observed','none',decode('01','hex'),1,$2)`, id, sessionfacts.RulesVersion)
	if err != nil {
		t.Fatal(err)
	}
	for _, ts := range []time.Time{from.Add(-time.Nanosecond), from, to} {
		_, err = s.pool.Exec(ctx, `INSERT INTO session_activity_segments(session_id,seg_start,seg_end,owner_user_id,owner_profile_email,agent,records,human_inputs,rules_version)
			VALUES($1,$2,$2,'owner','owner@example.com','claude',1,1,$3)`, id, ts, sessionfacts.RulesVersion)
		if err != nil {
			t.Fatal(err)
		}
	}
	// A turn opened before the requested week can still contain a queued input inside it.
	_, err = s.pool.Exec(ctx, `INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,branch,started_at,ended_at,input_kind,is_request,
			status,preview_state,cost_source,context_source,rules_version,detail)
			VALUES($1,'turn-1','claude',1,'active',$2,$3,'text',true,'ended','none','none','unknown',$6,
			jsonb_build_object('extra_input_ts',jsonb_build_array($4::text,$5::text)))`, id, from.Add(-time.Hour), to,
		from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano), sessionfacts.RulesVersion)
	if err != nil {
		t.Fatal(err)
	}
	filter := SessionCalendarFilter{From: from, To: to, SessionOverviewFilter: SessionOverviewFilter{UserID: "owner"},
		Access: SessionAnalysisAccess{User: true, UserID: "owner"}}
	result, err := s.ReadSessionCalendar(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || len(result.Sessions[0].Segments) != 1 || len(result.Sessions[0].Inputs.Items) != 1 || result.Sessions[0].Inputs.Items[0].TS != calendarTime(from) {
		t.Fatalf("week boundary: %+v", result)
	}
	if result.Sessions[0].Facts != "ready" || result.Sessions[0].Range["start"] != calendarTime(from.Add(-24*time.Hour)) {
		t.Fatalf("fact and point range: %+v", result.Sessions[0])
	}
	if inputs := result.Sessions[0].Segments[0].HumanInputs; inputs == nil || *inputs != 1 {
		t.Fatalf("observed human inputs: %v", inputs)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET rules_version=$2 WHERE session_id=$1`, id, sessionfacts.RulesVersion-1); err != nil {
		t.Fatal(err)
	}
	result, err = s.ReadSessionCalendar(ctx, filter)
	if err != nil || len(result.Sessions) != 1 || result.Sessions[0].Facts != "stale" || len(result.Sessions[0].Inputs.Items) != 1 {
		t.Fatalf("older rules remain stale with observed inputs: %+v, %v", result, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email,agent,ts,raw)
		VALUES($1,'user','foreign','foreign@example.com','claude',$2,'{}'::jsonb)`, id, from)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.ReadSessionCalendar(ctx, filter)
	if err != nil || len(result.Sessions) != 0 {
		t.Fatalf("current mixed identity result=%+v err=%v", result, err)
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM session_records WHERE session_id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.ReadSessionCalendar(ctx, filter)
	if err != nil || len(result.Sessions) != 0 {
		t.Fatalf("excluded result=%+v err=%v", result, err)
	}
	if _, err = s.pool.Exec(ctx, `DELETE FROM excluded_sessions WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_snapshots SET user_id='foreign',profile_email='foreign@example.com' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	result, err = s.ReadSessionCalendar(ctx, filter)
	if err != nil || len(result.Sessions) != 0 {
		t.Fatalf("owner drift result=%+v err=%v", result, err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_snapshots SET user_id='owner',profile_email='owner@example.com' WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `INSERT INTO deleted_sessions(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	result, err = s.ReadSessionCalendar(ctx, filter)
	if err != nil || len(result.Sessions) != 0 {
		t.Fatalf("deleted result=%+v err=%v", result, err)
	}
}

func TestSessionCalendarInputCapAndAdminScope(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	from := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	to := from.Add(25 * time.Hour)
	id := "calendar-input-cap"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'owner','owner@example.com','claude',$2,'{}'::jsonb)`, id, from)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ typ, value string }{{"user", "owner"}, {"all", ""}} {
		_, err = s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
			VALUES($1,$2,$3,$4,$5,true,'claude')`, scope.typ, scope.value, id, from, to)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_analysis_facts(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,
		transcript,title_source,title_state,turn_boundaries,cost_source,output_hash,source_generation,rules_version)
		VALUES($1,'claude','full',ARRAY['owner'],ARRAY['owner@example.com'],'owner','owner@example.com',true,'none','none','observed','none',decode('01','hex'),1,2)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_activity_segments(session_id,seg_start,seg_end,owner_user_id,owner_profile_email,agent,records,human_inputs,rules_version)
		VALUES($1,$2,$3,'owner','owner@example.com','claude',1,2001,2)`, id, from, to)
	if err != nil {
		t.Fatal(err)
	}
	// Generate 2,001 actual per-turn timestamps without a large fixture in Go.
	_, err = s.pool.Exec(ctx, `INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,branch,started_at,ended_at,input_kind,is_request,
		status,preview_state,cost_source,context_source,rules_version,detail)
		SELECT $1,'turn-'||n,'claude',n,'active',$2::timestamptz+make_interval(secs=>n),$2::timestamptz+make_interval(secs=>n),'text',true,
		'ended','none','none','unknown',2,jsonb_build_object('inputs',jsonb_build_array(jsonb_build_object('ts',to_char($2::timestamptz+make_interval(secs=>n), 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))))
		FROM generate_series(1,2001) n`, id, from)
	if err != nil {
		t.Fatal(err)
	}
	filter := SessionCalendarFilter{From: from, To: to, SessionOverviewFilter: SessionOverviewFilter{UserID: "owner"},
		Access: SessionAnalysisAccess{User: true, UserID: "owner"}}
	result, err := s.ReadSessionCalendar(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || len(result.Sessions[0].Inputs.Items) != 2000 || !result.Sessions[0].Inputs.Truncated {
		t.Fatalf("2001st input: sessions=%d err=%v", len(result.Sessions), err)
	}
	filter.Access = SessionAnalysisAccess{OwnerUserID: "admin"}
	filter.SessionOverviewFilter = SessionOverviewFilter{UserID: "owner"}
	admin, err := s.ReadSessionCalendar(ctx, filter)
	if err != nil || admin.OwnerView || len(admin.Sessions) != 1 {
		t.Fatalf("admin result=%+v err=%v", admin, err)
	}
	if _, ok := admin.Sessions[0].Colors["status"]; ok {
		t.Fatal("admin sees owner status")
	}
	filter.UserID = "foreign"
	other, err := s.ReadSessionCalendar(ctx, filter)
	if err != nil || len(other.Sessions) != 0 {
		t.Fatalf("other account result=%+v err=%v", other, err)
	}
}
