package store

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
	"cctrace/internal/sessionrecord"
)

func TestLoadSessionRecordSnapshotProjectsOnlySelectedSyntheticText(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	now := time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC)
	factHash := make([]byte, 32)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_analysis_facts
		(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,project_hash,repository_key,transcript,
		title_source,title_state,first_activity_at,last_activity_at,first_record_at,turn_boundaries,turns,requests,inputs,
		cost_source,detail,output_hash,source_generation,rules_version,computed_at)
		VALUES ('synthetic-session','claude','full',ARRAY['synthetic-user'],ARRAY['synthetic@example.test'],'synthetic-user','synthetic@example.test',
		'repo-hash','repo-key',true,'none','none',$1,$1,$1,'observed',1,1,1,'none',
		'{"edited_files":{"state":"observed","items":[]},"plans":[]}'::jsonb,$2,1,$3,$1)`, now, factHash, sessionfacts.RulesVersion); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	turnDetail, _ := json.Marshal(sessionfacts.TurnDetail{Inputs: []sessionfacts.TurnInput{{UUID: "synthetic-input", TS: now, Kind: "typed", N: 1}}})
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_turn_facts
		(session_id,turn_key,agent,ordinal,branch,started_at,ended_at,input_kind,is_request,status,preview_state,
		last_assistant_message_id,cost_source,context_source,detail,rules_version)
		VALUES ('synthetic-session','synthetic-turn','claude',1,'active',$1,$1,'typed',true,'ended','ok',
		'msg-final','none','unknown',$2, $3)`, now, turnDetail, sessionfacts.RulesVersion); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	for _, row := range []struct {
		ts                   time.Time
		typ, uuid, messageID string
		raw                  string
	}{
		{now, "user", "synthetic-input", "", `{"message":{"content":[{"type":"text","text":"Add the API endpoint"}]}}`},
		{now.Add(time.Second), "assistant", "", "msg-final", `{"message":{"content":[{"type":"text","text":"Added the API endpoint."},{"type":"tool_result","content":"DO_NOT_SEND_TOOL_OUTPUT"}]}}`},
	} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(ts,session_id,project_hash,record_type,profile_email,user_id,agent,uuid,message_id,raw)
			VALUES($1,'synthetic-session','repo-hash',$2,'synthetic@example.test','synthetic-user','claude',$3,NULLIF($4,''),$5::jsonb)`, row.ts, row.typ, row.uuid, row.messageID, row.raw); err != nil {
			t.Fatalf("seed %s row: %v", row.typ, err)
		}
	}
	var lastAssistantID, storedMessageID string
	if err := s.pool.QueryRow(ctx, `SELECT last_assistant_message_id FROM session_turn_facts WHERE session_id='synthetic-session'`).Scan(&lastAssistantID); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT message_id FROM session_records WHERE session_id='synthetic-session' AND record_type='assistant'`).Scan(&storedMessageID); err != nil {
		t.Fatal(err)
	}
	if lastAssistantID != storedMessageID {
		t.Fatalf("last assistant id %q != row message id %q", lastAssistantID, storedMessageID)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_ai_record_jobs(session_id,lane,claim,claimed_at) VALUES('synthetic-session','manual',42,$1)`, now); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	var dashboardUserID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO dashboard_users(email,password_hash,role,cctrace_user_id)
		VALUES('synthetic@example.test','test-only','user','synthetic-user') RETURNING id`).Scan(&dashboardUserID); err != nil {
		t.Fatalf("seed dashboard user: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_ai_record_consents(dashboard_user_id,runtime_key,disclosure_version)
		VALUES($1,'claude-api:api_key',$2)`, dashboardUserID, sessionrecord.RecordDisclosureVersion); err != nil {
		t.Fatalf("seed disclosure consent: %v", err)
	}
	cap := int64(64_000)
	if err := s.SetSessionAIRecordSettings(ctx, SessionAIRecordSettings{Enabled: true, Runtime: "claude-api", DailyTokenCap: &cap}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAIRecordRuntimeSettings(ctx, "claude-api", "synthetic-model", "", "test"); err != nil {
		t.Fatal(err)
	}
	job := sessionrecord.Job{SessionID: "synthetic-session", Claim: 42, OutputHash: factHash, Lane: "manual",
		RuntimeKey: "claude-api:api_key", DisclosureVersion: sessionrecord.RecordDisclosureVersion, Model: "synthetic-model",
		Owner: sessionrecord.Owner{DashboardUserID: dashboardUserID, UserID: "synthetic-user", ProfileEmail: "synthetic@example.test", UseUserIDAccessControl: true}}
	snapshot, err := s.LoadSessionRecordSnapshot(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	built, err := sessionrecord.BuildInput(snapshot.Input)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.TurnKeys) != 1 || built.TurnKeys["t1"] != "synthetic-turn" {
		t.Fatalf("turn mapping = %#v", built.TurnKeys)
	}
	if stringContains(built.JSON, "DO_NOT_SEND_TOOL_OUTPUT") || stringContains(built.JSON, "synthetic-user") {
		t.Fatalf("private identity or tool output escaped into reduced input: %s", built.JSON)
	}
	if !stringContains(built.JSON, "Add the API endpoint") || !stringContains(built.JSON, "Added the API endpoint") {
		t.Fatalf("selected synthetic request/final text missing (turnKey=%q final=%q): %s", snapshot.Input.Turns[0].Key, snapshot.Input.Turns[0].Final, built.JSON)
	}
	reservation, err := s.BeginSessionRecordTransmission(ctx, job, snapshot, job.RuntimeKey, sessionrecord.RecordDisclosureVersion, 64_000, "2026-10-09")
	if err != nil {
		t.Fatal(err)
	}
	reservation.ReleaseBody()
	if err := s.FinishSessionRecordTransmission(ctx, reservation, sessionrecord.CallUsage{Reported: true, InputTokens: 1000, OutputTokens: 100, InputBytes: 400, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginSessionRecordTransmission(ctx, job, snapshot, job.RuntimeKey, sessionrecord.RecordDisclosureVersion, 64_000, "2026-10-09"); err != sessionrecord.ErrRecordBudgetWaiting {
		t.Fatalf("second reservation error = %v, want daily budget refusal", err)
	}
	var usage, reservations int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_ai_record_usage WHERE day='2026-10-09' AND lane='manual' AND outcome='ok'`).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM session_ai_record_budget_reservations WHERE day='2026-10-09'`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if usage != 1 || reservations != 0 {
		t.Fatalf("usage rows=%d active reservations=%d, want 1 and 0", usage, reservations)
	}
	trueValue := true
	record := sessionrecord.PersistedRecord{SessionID: job.SessionID, Outcome: "ok", Agent: "claude", OwnerUserID: snapshot.OwnerUserID,
		OwnerProfileEmail: snapshot.OwnerProfileEmail, RepositoryKey: snapshot.RepositoryKey, AccountKeys: snapshot.AccountKeys,
		PromptVersion: sessionrecord.PromptVersion, Runtime: "claude-api", Model: "synthetic-model", FactsHash: snapshot.FactsHash,
		FactsRulesVersion: snapshot.FactsRulesVersion, BasisHash: snapshot.BasisHash, PrefixHash: snapshot.PrefixHash,
		InputHash: snapshot.InputHash, SourceInputHash: snapshot.SourceInputHash, CoversUntil: snapshot.CoversUntil,
		CoversTurnKey: snapshot.CoversTurnKey, CoveredRequests: snapshot.CoveredRequests, SourceFirstAt: snapshot.SourceFirstAt,
		GeneratedAt: now, UsageDay: "2026-10-09", Generations: 1, Language: "en", Headline: "Synthetic API work",
		Content: sessionrecord.RecordContent{Bullets: []sessionrecord.Bullet{}, Review: []sessionrecord.ReviewItem{},
			Work:    []sessionrecord.WorkItem{{ID: "1.1", Kind: "request", Text: "Add API", Done: &trueValue, TurnKey: "synthetic-turn", Children: []sessionrecord.WorkChild{}}},
			Skipped: []sessionrecord.SkippedTurn{}, NewTerms: sessionrecord.NewTerms{Component: []string{}, Screen: []string{}}, Gaps: []string{}},
		Kinds: []string{"implementation"}, Components: []string{"API"}, Screens: []string{"session"}, WorkTotal: 1, WorkDone: 1,
		Calls: 1, InputBytes: 400, TokensReported: true, InputTokens: ptrInt64(1000), CachedInputTokens: ptrInt64(0),
		OutputTokens: ptrInt64(100), SearchText: "Synthetic API work\nAdd API"}
	if err := s.PublishSessionRecord(ctx, job, snapshot, record); err != nil {
		t.Fatal(err)
	}
	var outcome, headline string
	var content []byte
	if err := s.pool.QueryRow(ctx, `SELECT outcome,headline,content FROM session_ai_records WHERE session_id=$1`, job.SessionID).Scan(&outcome, &headline, &content); err != nil {
		t.Fatal(err)
	}
	if outcome != "ok" || headline != record.Headline || len(content) == 0 {
		t.Fatalf("published row outcome=%q headline=%q content_bytes=%d", outcome, headline, len(content))
	}
	admin, err := s.SessionAIRecordAdminMetrics(ctx, job.RuntimeKey, job.DisclosureVersion, now, "UTC")
	if err != nil {
		t.Fatalf("read P5 admin aggregate: %v", err)
	}
	today, ok := admin["today"].(map[string]interface{})
	if !ok || today["used_tokens"] != int64(1100) || today["cap_tokens"] != int64(64000) {
		t.Fatalf("P5 admin today aggregate=%v", admin["today"])
	}
	analysis, err := s.ReadSessionAnalysis(ctx, job.SessionID, SessionAnalysisAccess{User: true, UserID: job.Owner.UserID,
		ProfileEmail: job.Owner.ProfileEmail, OwnerUserID: job.Owner.UserID, OwnerProfileEmail: job.Owner.ProfileEmail,
		DashboardUserID: dashboardUserID, RecordFeatureEnabled: true, RecordAdminEnabled: true,
		RecordRuntimeKey: job.RuntimeKey, RecordDisclosureVersion: job.DisclosureVersion, RecordRuntimeAvailable: true})
	if err != nil {
		t.Fatalf("read synthetic session analysis with record: %v", err)
	}
	apiRecord, ok := analysis["record"].(map[string]interface{})
	if !ok || apiRecord["state"] != "ready" || apiRecord["headline"] != record.Headline {
		t.Fatalf("analysis record response=%#v", analysis["record"])
	}
	judgement, ok := analysis["judgement"].(map[string]interface{})
	if !ok || judgement["bucket"] != nil {
		t.Fatalf("analysis judgement=%#v", analysis["judgement"])
	}
	checks, ok := judgement["checks"].([]map[string]interface{})
	if !ok || len(checks) != 5 || checks[4]["reason"] != "unvalidated_model" {
		t.Fatalf("analysis judgement checks=%#v", judgement["checks"])
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		VALUES('user','synthetic-user',$1,$2,$3,true,'claude') ON CONFLICT DO NOTHING`, job.SessionID, now.Add(-time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES($1) ON CONFLICT DO NOTHING`, sessionOverviewRollupBackfill); err != nil {
		t.Fatal(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	overview := &SessionOverview{SessionID: job.SessionID, Analysis: map[string]interface{}{}}
	if err := addSessionRecordOverview(ctx, tx, SessionOverviewFilter{IncludeSessionRecords: true, AnalysisOwner: true, PersonalUserID: job.Owner.UserID}, []*SessionOverview{overview}); err != nil {
		t.Fatal(err)
	}
	headlineValue, ok := overview.Analysis["headline"].(*string)
	if !ok || headlineValue == nil || *headlineValue != record.Headline || overview.Analysis["record_state"] != "ready" {
		t.Fatalf("overview P5 fields=%v", overview.Analysis)
	}
	rowJudgement, ok := overview.Analysis["judgement"].(map[string]interface{})
	if !ok || rowJudgement["bucket"] != nil {
		t.Fatalf("overview P5 judgement=%v", overview.Analysis["judgement"])
	}
	list, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{IncludeAnalysis: true, AnalysisOwner: true,
		IncludeSessionRecords: true, DashboardUserID: dashboardUserID, PersonalUserID: job.Owner.UserID,
		UserID: job.Owner.UserID, Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("read owner session overview with P5 record: rows=%d err=%v", len(list), err)
	}
	listHeadline, ok := list[0].Analysis["headline"].(*string)
	if !ok || listHeadline == nil || *listHeadline != "Synthetic API work" || list[0].Analysis["record_state"] != "ready" {
		t.Fatalf("overview API P5 analysis=%v", list[0].Analysis)
	}
	filtered, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{IncludeAnalysis: true, AnalysisOwner: true, IncludeSessionRecords: true,
		DashboardUserID: dashboardUserID, PersonalUserID: job.Owner.UserID, UserID: job.Owner.UserID, Limit: 10,
		AnalysisKind: "implementation", AnalysisComponent: "API", AnalysisScreen: "session"})
	if err != nil || len(filtered) != 1 {
		t.Fatalf("P5 owner tag filters rows=%d err=%v", len(filtered), err)
	}
	validatedBucket, err := s.ListSessionOverviews(ctx, SessionOverviewFilter{IncludeAnalysis: true, AnalysisOwner: true, IncludeSessionRecords: true,
		DashboardUserID: dashboardUserID, PersonalUserID: job.Owner.UserID, UserID: job.Owner.UserID, Limit: 10, AnalysisBucket: "done"})
	if err != nil || len(validatedBucket) != 0 {
		t.Fatalf("unvalidated work bucket returned rows=%d err=%v", len(validatedBucket), err)
	}
	calendarTx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	calendar := SessionCalendar{OwnerView: true, Sessions: []SessionCalendarSession{{SessionID: job.SessionID, Colors: map[string]interface{}{"status": nil, "components": nil, "screens": nil, "kinds": nil}}}}
	if err := addCalendarSessionRecords(ctx, calendarTx, &calendar, SessionCalendarFilter{SessionOverviewFilter: SessionOverviewFilter{UserID: job.Owner.UserID}}); err != nil {
		_ = calendarTx.Rollback(ctx)
		t.Fatal(err)
	}
	_ = calendarTx.Rollback(ctx)
	colors := calendar.Sessions[0].Colors
	if colors["status"] != "unknown" || !sameStrings(colors["components"].([]string), []string{"API"}) || !sameStrings(colors["screens"].([]string), []string{"session"}) {
		t.Fatalf("calendar P5 colors=%v", colors)
	}
}

func stringContains(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }

func ptrInt64(value int64) *int64 { return &value }
