package store

import (
	"context"
	"testing"
	"time"
)

func TestSessionRecordConsentAndOwnDeletionStayScoped(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	var ownerID, otherID int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO dashboard_users(email,password_hash,role,cctrace_user_id)
		VALUES('p5-owner@example.test','test','user','p5-owner') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `INSERT INTO dashboard_users(email,password_hash,role,cctrace_user_id)
		VALUES('p5-other@example.test','test','user','p5-other') RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, uid, email string }{
		{"p5-owner-session", "p5-owner", "p5-owner@example.test"},
		{"p5-other-session", "p5-other", "p5-other@example.test"},
	} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_analysis_facts
			(session_id,agent,coverage,owner_user_id,owner_profile_email,transcript,title,title_source,title_state,
			first_activity_at,last_activity_at,turn_boundaries,requests,cost_source,output_hash,source_generation,rules_version)
			VALUES($1,'claude','full',$2,$3,true,'Synthetic session','first_request','ok',now(),now(),'observed',1,'none',decode(repeat('01',32),'hex'),1,1)`, row.id, row.uid, row.email); err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_ai_records
			(session_id,outcome,agent,owner_user_id,owner_profile_email,prompt_version,facts_hash,facts_rules_version,basis_hash,
			generated_at,headline,content,search_text)
			SELECT $1,'ok','claude',$2,$3,1,output_hash,1,decode(repeat('02',32),'hex'),now()-interval '11 minutes','Synthetic summary',
			'{"bullets":[],"review":[],"work":[],"skipped":[],"new_terms":{"component":[],"screen":[]},"gaps":[]}'::jsonb,'Synthetic summary' FROM session_analysis_facts WHERE session_id=$1`, row.id, row.uid, row.email); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET repository_key='repo-key' WHERE session_id LIKE 'p5-%-session'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_ai_records SET repository_key='repo-key',components=ARRAY['API'] WHERE session_id='p5-owner-session'`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionRecordVocabulary(ctx, "repo-key", "component", []string{"ＡＰＩ", "backend"}, "admin"); err != nil {
		t.Fatal(err)
	}
	vocabulary, err := s.GetSessionRecordVocabulary(ctx, "repo-key")
	if err != nil || !vocabulary.ComponentsClosed || len(vocabulary.Components) != 2 || vocabulary.Components[0] != "API" {
		t.Fatalf("vocabulary=%+v err=%v", vocabulary, err)
	}
	if err := s.MergeSessionRecordVocabulary(ctx, "repo-key", "component", "API", "Server", "admin"); err != nil {
		t.Fatal(err)
	}
	var components []string
	if err := s.pool.QueryRow(ctx, `SELECT components FROM session_ai_records WHERE session_id='p5-owner-session'`).Scan(&components); err != nil {
		t.Fatal(err)
	}
	if len(components) != 1 || components[0] != "Server" {
		t.Fatalf("merged record components=%v", components)
	}
	if err := s.SetSessionRecordVocabularies(ctx, "repo-key", []string{"Server", "API"}, []string{"session"}, "admin"); err != nil {
		t.Fatal(err)
	}
	vocabulary, err = s.GetSessionRecordVocabulary(ctx, "repo-key")
	if err != nil || !vocabulary.ComponentsClosed || !vocabulary.ScreensClosed || len(vocabulary.Screens) != 1 || vocabulary.Screens[0] != "session" {
		t.Fatalf("combined vocabulary update=%+v err=%v", vocabulary, err)
	}
	if err := s.GrantSessionRecordConsent(ctx, ownerID, "claude-api:api_key", "disclosure-v1"); err != nil {
		t.Fatal(err)
	}
	cap := int64(64000)
	if err := s.SetSessionAIRecordSettings(ctx, SessionAIRecordSettings{Enabled: true, Runtime: "claude-api", DailyTokenCap: &cap}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestSessionRecord(ctx, "p5-owner-session", "p5-owner", "p5-owner@example.test", true, ownerID,
		"claude-api:api_key", "disclosure-v1", true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestSessionRecord(ctx, "p5-owner-session", "p5-owner", "p5-owner@example.test", true, ownerID,
		"claude-api:api_key", "disclosure-v1", true, time.Now().UTC()); err != ErrSessionRecordRateLimited {
		t.Fatalf("duplicate manual generation error=%v", err)
	}
	if err := s.RequestSessionRecordRange(ctx, ownerID, "p5-owner", "p5-owner@example.test", true,
		"claude-api:api_key", "disclosure-v1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rangeFrom, rangeTo := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	status, err := s.SessionRecordRangeStatus(ctx, ownerID, "p5-owner", "p5-owner@example.test", true,
		"claude-api:api_key", "disclosure-v1", rangeFrom, rangeTo, time.Now().UTC())
	if err != nil || status.State != "writing" || status.Total != 1 || status.Writing == 0 {
		t.Fatalf("range status=%+v err=%v", status, err)
	}
	granted, at, err := s.SessionRecordConsent(ctx, ownerID, "claude-api:api_key", "disclosure-v1")
	if err != nil || !granted || at == nil || time.Since(*at) > time.Minute {
		t.Fatalf("consent granted=%v at=%v err=%v", granted, at, err)
	}
	if err := s.RevokeSessionRecordConsent(ctx, ownerID, "claude-api:api_key"); err != nil {
		t.Fatal(err)
	}
	granted, at, err = s.SessionRecordConsent(ctx, ownerID, "claude-api:api_key", "disclosure-v1")
	if err != nil || granted || at != nil {
		t.Fatalf("revoked consent granted=%v at=%v err=%v", granted, at, err)
	}
	results, err := s.SearchMySessionRecords(ctx, "summary", "p5-owner", "p5-owner@example.test", true, true, 10)
	if err != nil || len(results) != 1 || results[0].SessionID != "p5-owner-session" || len(results[0].Matches) == 0 {
		t.Fatalf("owner search results=%+v err=%v", results, err)
	}
	results, err = s.SearchMySessionRecords(ctx, "summary", "p5-other", "p5-other@example.test", true, true, 10)
	if err != nil || len(results) != 1 || results[0].SessionID != "p5-other-session" {
		t.Fatalf("other owner search results=%+v err=%v", results, err)
	}
	deleted, err := s.DeleteOwnSessionAIRecords(ctx, "p5-owner", "p5-owner@example.test", true)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	var ownerRecords, otherRecords int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE session_id='p5-owner-session'),count(*) FILTER (WHERE session_id='p5-other-session') FROM session_ai_records`).Scan(&ownerRecords, &otherRecords); err != nil {
		t.Fatal(err)
	}
	if ownerRecords != 0 || otherRecords != 1 {
		t.Fatalf("record counts owner=%d other=%d", ownerRecords, otherRecords)
	}
}
