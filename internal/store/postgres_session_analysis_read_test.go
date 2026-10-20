package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cctrace/internal/sessionfacts"
)

func TestSessionAnalysisReadIdentityVisibilityAndFallback(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	access := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"}
	id := "analysis-reader-owner"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent,input_tokens) VALUES ('user','owner',$1,now(),now(),true,'claude',42)`, id)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil || result["state"] != "building" {
		t.Fatalf("building=%v, %v", result, err)
	}
	if result["analysis_pending"] != false || result["pending_since"] != nil {
		t.Fatalf("GET changed the pending status without queued work: %v", result)
	}
	if _, ok := result["identities"]; ok {
		t.Fatal("building leaked identities")
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email,agent,ts,raw) VALUES ($1,'user','foreign','foreign@example.com','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadSessionAnalysis(ctx, id, access)
	if !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("foreign identity should hide rollup: %v", err)
	}
	admin := SessionAnalysisAccess{OwnerUserID: "admin"}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent) VALUES ('all','',$1,now(),now(),true,'claude')`, id)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.ReadSessionAnalysis(ctx, id, admin)
	if err != nil || result["state"] != "building" {
		t.Fatalf("admin building=%v, %v", result, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadSessionAnalysis(ctx, id, admin)
	if !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("excluded rollup: %v", err)
	}
}

func TestSessionAnalysisRepoStatusDoesNotDemandFullAnalysis(t *testing.T) {
	// Shared store truncation prevents running this integration test in parallel.
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "analysis-repo-status-lazy"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'owner','owner@example.com','claude',now(),
		'{"git":{"edited_files":3,"uncommitted_edited_files":2,"repositories":1,"unresolved":0}}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent)
		VALUES('user','owner',$1,now(),now(),true,'claude')`, id); err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner", RepoStatusOnly: true}
	for _, cached := range []bool{false, true} {
		if cached {
			if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
				t.Fatal(err)
			}
			load, err := s.LoadSessionFactsInput(ctx, id, "claude")
			if err != nil {
				t.Fatal(err)
			}
			if outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1); err != nil || outcome != "written" {
				t.Fatalf("publish=%s, err=%v", outcome, err)
			}
			if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET detail=detail-'transcript_io' WHERE session_id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}
		result, err := s.ReadSessionAnalysis(ctx, id, access)
		if err != nil {
			t.Fatal(err)
		}
		if cached {
			status := result["header"].(map[string]interface{})["repo_status"].(map[string]interface{})
			if status["uncommitted_edited_files"] != 2 || status["uncommitted_state"] != "observed" {
				t.Fatalf("repository status missing: %v", status)
			}
		}
		for _, key := range []string{"turns", "live", "record", "signals"} {
			if _, ok := result[key]; ok {
				t.Fatalf("repo status loaded unrelated analysis field %s", key)
			}
		}
		var queued bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_fact_dirty WHERE session_id=$1)`, id).Scan(&queued); err != nil || queued {
			t.Fatalf("repo status queued analysis: cached=%v queued=%v err=%v", cached, queued, err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email,agent,ts,raw)
		VALUES($1,'system','foreign','foreign@example.com','claude',now(),'{}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadSessionAnalysis(ctx, id, access); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("repo status bypassed current owner visibility: %v", err)
	}
}

func TestSessionAnalysisReadFullOwnerAdminAndTruncated(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := "analysis-full-owner"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot) VALUES($1,'owner','owner@example.com','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1)
	if err != nil || outcome != "written" {
		t.Fatalf("publish=%s, %v", outcome, err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"}
	result, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	if result["state"] != "ready" {
		t.Fatalf("state=%v", result["state"])
	}
	if _, ok := result["identities"]; ok {
		t.Fatal("owner received admin identities")
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email,agent,ts,raw) VALUES($1,'system','foreign','foreign@example.com','claude',now(),'{"subtype":"away_summary","content":"foreign recap"}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReadSessionAnalysis(ctx, id, access); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("foreign recap after fact publish should hide owner fact: %v", err)
	}
	if _, err = s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, ProfileEmail: "owner@example.com", OwnerProfileEmail: "owner@example.com"}); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("foreign recap should hide legacy owner fact: %v", err)
	}
	if _, err = s.pool.Exec(ctx, `DELETE FROM session_records WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"header", "signals", "outputs", "recaps", "turns", "abandoned", "inputs", "questions", "plans", "subagents", "truncated"} {
		if _, ok := result[key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) {
		t.Fatal("invalid JSON")
	}
	admin, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{OwnerUserID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if admin["owner_view"] != false {
		t.Fatalf("admin owner_view=%v", admin["owner_view"])
	}
	if _, ok := admin["signals"]; ok {
		t.Fatal("admin received owner signals")
	}
	if admin["header"].(map[string]interface{})["name"] != nil {
		t.Fatalf("non-owner admin received name: %v", admin["header"])
	}
	if _, ok := admin["identities"]; !ok {
		t.Fatal("admin missing identities")
	}
	if _, err = s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{UserID: "different"}); !errors.Is(err, ErrSessionAnalysisNotFound) {
		t.Fatalf("admin user filter bypass: %v", err)
	}
	if _, err = s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{UserID: "owner"}); err != nil {
		t.Fatalf("admin user filter rejected matching fact: %v", err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE session_snapshots SET snapshot=$2::jsonb WHERE session_id=$1`, id,
		`{"custom_title":"My session","cost_states":[{"lines_added":4,"lines_removed":2}],"pr_links":[{"url":"https://example.invalid/pr/7","number":7,"repository":"local","first_seen_at":"2026-10-01T00:00:00Z"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	withSnapshot, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	outputs := withSnapshot["outputs"].(map[string]interface{})
	if name := withSnapshot["header"].(map[string]interface{})["name"].(map[string]interface{}); name["text"] != "My session" || name["source"] != "custom_title" {
		t.Fatalf("snapshot name=%v", name)
	}
	if outputs["lines"].(map[string]interface{})["added"] != int64(4) || len(outputs["prs"].(map[string]interface{})["items"].([]interface{})) != 1 {
		t.Fatalf("snapshot outputs=%v", outputs)
	}
	if pr := outputs["prs"].(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{}); pr["turn_key"] != nil {
		t.Fatalf("PR outside turn interval should remain unattributed: %v", pr)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO projects(project_hash,project_name) VALUES('analysis-project','Verified project')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE session_analysis_facts SET project_hash='analysis-project' WHERE session_id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	withProject, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	if got := withProject["header"].(map[string]interface{})["project"].(map[string]interface{})["name"]; got != "Verified project" {
		t.Fatalf("project name=%v", got)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_sync,agent,input_tokens) VALUES('user','owner',$1,now(),now(),true,'claude',71)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE session_analysis_facts SET coverage='truncated' WHERE session_id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	truncated, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	if truncated["state"] != "truncated" {
		t.Fatalf("state=%v", truncated["state"])
	}
	if _, ok := truncated["turns"]; ok {
		t.Fatal("truncated response has turns")
	}
	tokens := truncated["header"].(map[string]interface{})["tokens"].(map[string]interface{})
	if tokens["input"] != int64(71) {
		t.Fatalf("truncated tokens=%v", tokens)
	}
}

func TestSessionAnalysisExposesOwner_P4_GitStatusWithoutClaimingRepoWideCounts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := "analysis-p4-git-status"
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email,agent,ts,branch,raw)
		VALUES($1,'user','owner','owner@example.test','claude',now(),'feature/p4','{}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'owner','owner@example.test','claude',now(),$2::jsonb)`, id,
		`{"git":{"changed_at":"2026-10-09T10:00:00Z","edited_files":4,"uncommitted_edited_files":2,"repositories":1,"unresolved":0,"partial":false}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	output := sessionfacts.Compute(load.Input)
	if _, err := s.publishSessionFact(ctx, id, load.Generation, load, output, 1); err != nil {
		t.Fatal(err)
	}
	owner, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	header := owner["header"].(map[string]interface{})
	if header["branch"] != "feature/p4" {
		t.Fatalf("branch=%v, want latest observed record branch", header["branch"])
	}
	gitStatus, ok := header["repo_status"].(map[string]interface{})
	if !ok || gitStatus["uncommitted_edited_files"] != 2 || gitStatus["uncommitted_state"] != "observed" || gitStatus["uncommitted_source"] != "session_git_snapshot" || gitStatus["uncommitted_scope"] != "session_edited_files" {
		t.Fatalf("owner P4 status=%v", header["repo_status"])
	}

	admin, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{OwnerUserID: "other-admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := admin["header"].(map[string]interface{})["repo_status"]; ok {
		t.Fatal("non-owner received private git status")
	}

	if _, err := s.pool.Exec(ctx, `UPDATE session_snapshots SET snapshot=$2::jsonb WHERE session_id=$1`, id,
		`{"git":{"changed_at":"2026-10-09T10:00:00Z","edited_files":4,"uncommitted_edited_files":0,"repositories":1,"unresolved":1,"partial":true},"truncated":["git"]}`); err != nil {
		t.Fatal(err)
	}
	partial, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	partialStatus := partial["header"].(map[string]interface{})["repo_status"].(map[string]interface{})
	if partialStatus["uncommitted_edited_files"] != nil || partialStatus["uncommitted_state"] != "undeterminable" {
		t.Fatalf("partial P4 status fabricated an exact count: %v", partialStatus)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_snapshots SET snapshot=$2::jsonb WHERE session_id=$1`, id,
		`{"git":{"edited_files":4,"repositories":1,"unresolved":0,"partial":false}}`); err != nil {
		t.Fatal(err)
	}
	missingCount, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	missingStatus := missingCount["header"].(map[string]interface{})["repo_status"].(map[string]interface{})
	if missingStatus["uncommitted_edited_files"] != nil || missingStatus["uncommitted_state"] != "undeterminable" || missingStatus["uncommitted_reason"] != "invalid_git_snapshot" {
		t.Fatalf("missing P4 count field became observed zero: %v", missingStatus)
	}
}

func TestSessionAnalysisRebuildsVersionOneFacts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "analysis-rules-version-rebuild"
	enqueued, done := sessionFactBuildMarkers()
	if enqueued != "session_facts_r6_w3_enqueued" || done != "session_facts_r6_w3" {
		t.Fatalf("rules 6 / witness 3 build markers: %q, %q", enqueued, done)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name IN ($1,$2)`, enqueued, done); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name IN ($1,$2)`, enqueued, done)
	})
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot) VALUES($1,'owner','owner@example.com','claude',now(),'{}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id); err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	output, timeline := computeSessionFactTimeline(load.Input)
	if outcome, err := s.publishSessionFactWithTimeline(ctx, id, load.Generation, load, output, timeline, 1); err != nil || outcome != "written" {
		t.Fatalf("initial publish=%s, %v", outcome, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_analysis_facts SET rules_version=1, output_hash=decode('00','hex') WHERE session_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"}
	old, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil || old["state"] != "stale" || old["rules_version"] != 1 {
		t.Fatalf("version 1 fact=%v, %v", old, err)
	}
	if err := s.enqueueSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	var queued bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name=$1) AND EXISTS(SELECT 1 FROM session_fact_dirty WHERE session_id=$2 AND reasons & 512 <> 0)`, enqueued, id).Scan(&queued); err != nil || !queued {
		t.Fatalf("version 3 rebuild not queued: %v, %v", queued, err)
	}
	load, err = s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	output, timeline = computeSessionFactTimeline(load.Input)
	if outcome, err := s.publishSessionFactWithTimeline(ctx, id, load.Generation, load, output, timeline, 1); err != nil || outcome != "written" {
		t.Fatalf("rebuild publish=%s, %v", outcome, err)
	}
	fresh, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil || fresh["state"] != "ready" || fresh["rules_version"] != sessionfacts.RulesVersion {
		t.Fatalf("version 3 fact=%v, %v", fresh, err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	// Opening the gate creates a second wave before this version is done.
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_fact_dirty WHERE reasons & 1024<>0`); err != nil {
		t.Fatal(err)
	}
	if err := s.finishSessionFactBuild(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_backfills WHERE name=$1)`, done).Scan(&queued); err != nil || !queued {
		t.Fatalf("version 3 build not marked done: %v, %v", queued, err)
	}
}

func TestSessionAnalysisTurnBranchLimits(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "analysis-branch-limits"
	_, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot) VALUES($1,'owner','owner@example.com','claude',now(),'{}'::jsonb)`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id)
	if err != nil {
		t.Fatal(err)
	}
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1)
	if err != nil || outcome != "written" {
		t.Fatalf("publish=%s, %v", outcome, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_turn_facts(session_id,turn_key,agent,ordinal,branch,started_at,ended_at,input_kind,is_request,status,preview_state,cost_source,context_source,rules_version)
 SELECT $1, 'turn-' || n, 'claude', n, CASE WHEN n <= 501 THEN 'abandoned' ELSE 'active' END,
 now(), now(), 'typed', true, 'ended', 'none', 'none', 'unknown', $2
 FROM generate_series(1,2502) n`, id, sessionfacts.RulesVersion)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE session_turn_facts SET detail=$2::jsonb WHERE session_id=$1 AND ordinal IN (501,2502)`, id,
		`{"inputs":[{"uuid":"extra","ts":"2026-10-01T00:00:00Z","kind":"typed","n":1}],"questions":[{"tool_use_id":"extra","srec_id":1}]}`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(result["abandoned"].([]interface{})); got != 500 {
		t.Fatalf("abandoned count=%d", got)
	}
	if got := len(result["turns"].([]interface{})); got != 2000 {
		t.Fatalf("active count=%d", got)
	}
	limits := result["truncated"].(map[string]bool)
	if !limits["abandoned"] || !limits["turns"] || !limits["inputs"] || !limits["questions"] {
		t.Fatalf("branch and nested limits=%v", limits)
	}
}

func TestSessionAnalysisLiveOwnerScope(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "analysis-live-owner"
	personalSource(t, s, id, "owner@example.com", "owner")
	load, err := s.LoadSessionFactsInput(ctx, id, "claude")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1)
	if err != nil || outcome != "written" {
		t.Fatalf("publish=%s, %v", outcome, err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO session_live_states
	(session_id,user_id,profile_email,pid,proc_start,status,waiting_kind,name,status_changed_at,heartbeat_at,identity_provenance)
	VALUES($1,'owner','owner@example.com',1,'a','waiting','permission','Owner editor',now(),now(),'personal_token'),
	($1,'owner','owner@example.com',2,'b','idle','','Unverified editor',now(),now(),'unknown')`, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO collector_heartbeats(user_id,profile_email,heartbeat_at,last_stored_at)
	VALUES('owner','owner@example.com',now(),now()-interval '1 minute')`)
	if err != nil {
		t.Fatal(err)
	}
	access := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner", OwnerProfileEmail: "owner@example.com"}
	owner, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	category := owner["header"].(map[string]interface{})["category"].(map[string]interface{})
	if category["key"] != "waiting" || category["waiting_kind"] != "permission" || category["as_of"] == "" {
		t.Fatalf("owner category=%v", category)
	}
	live := owner["live"].(map[string]interface{})
	instances := live["instances"].([]interface{})
	if len(instances) != 1 || instances[0].(map[string]interface{})["name"] != "Owner editor" {
		t.Fatalf("owner live=%v", live)
	}
	collector := live["collector"].(map[string]interface{})
	if collector["heartbeat_at"] == nil || collector["last_stored_at"] == nil {
		t.Fatalf("collector=%v", collector)
	}
	admin, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{OwnerUserID: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := admin["live"]; ok {
		t.Fatal("nonowner admin received live")
	}
	if _, ok := admin["header"].(map[string]interface{})["category"]; ok {
		t.Fatal("nonowner admin received category")
	}
	_, err = s.pool.Exec(ctx, `UPDATE session_live_states SET status='gone' WHERE session_id=$1 AND identity_provenance='personal_token'`, id)
	if err != nil {
		t.Fatal(err)
	}
	withoutLive, err := s.ReadSessionAnalysis(ctx, id, access)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(withoutLive["live"].(map[string]interface{})["instances"].([]interface{})); got != 0 {
		t.Fatalf("gone process leaked as live: %d", got)
	}
}

func TestSessionAnalysisCopyOriginalFilter(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const original, copied = "analysis-copy-source", "analysis-copy-target"
	for _, id := range []string{original, copied} {
		_, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot) VALUES($1,'owner','owner@example.com','claude',now(),'{}'::jsonb)`, id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.pool.Exec(ctx, `INSERT INTO session_fact_dirty(session_id) VALUES($1)`, id)
		if err != nil {
			t.Fatal(err)
		}
		load, err := s.LoadSessionFactsInput(ctx, id, "claude")
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := publishSessionP4ForTest(ctx, s, id, load, 1)
		if err != nil || outcome != "written" {
			t.Fatalf("publish %s=%s, %v", id, outcome, err)
		}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO schema_backfills(name) VALUES ('session_fact_copies_v1') ON CONFLICT DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM schema_backfills WHERE name='session_fact_copies_v1'`)
	})
	_, err = s.pool.Exec(ctx, `INSERT INTO session_record_copies(session_id,srec_id,original_session_id,record_type,ts) VALUES($1,0,$2,'user',now())`, copied, original)
	if err != nil {
		t.Fatal(err)
	}
	owner := SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"}
	result, err := s.ReadSessionAnalysis(ctx, copied, owner)
	if err != nil {
		t.Fatal(err)
	}
	copy := result["copies"].(map[string]interface{})
	if copy["original_visible"] != true || copy["original_session_id"] != original {
		t.Fatalf("owner copy=%v", copy)
	}
	_, err = s.pool.Exec(ctx, `UPDATE session_analysis_facts SET user_ids=ARRAY['other'] WHERE session_id=$1`, original)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.ReadSessionAnalysis(ctx, copied, SessionAnalysisAccess{UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	copy = result["copies"].(map[string]interface{})
	if copy["original_visible"] != true || copy["original_session_id"] != original {
		t.Fatalf("admin filter on selected copy hid independently visible original=%v", copy)
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO excluded_sessions(session_id) VALUES($1)`, original)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.ReadSessionAnalysis(ctx, copied, SessionAnalysisAccess{UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	copy = result["copies"].(map[string]interface{})
	if copy["original_visible"] != false || copy["original_session_id"] != nil {
		t.Fatalf("excluded original exposed=%v", copy)
	}
}

func TestSessionAnalysisTurnEmptyArrays(t *testing.T) {
	turn := sessionfacts.Turn{Detail: sessionfacts.TurnDetail{Questions: []sessionfacts.QuestionReference{{ToolUseID: "ask"}}}}
	encoded, err := json.Marshal(analysisTurn(turn))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Edited struct {
			Sample []string `json:"sample"`
		} `json:"edited"`
		QuestionHeaders []struct {
			Headers []string `json:"headers"`
		} `json:"question_headers"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Edited.Sample == nil || len(got.QuestionHeaders) != 1 || got.QuestionHeaders[0].Headers == nil {
		t.Fatalf("null array in turn JSON: %s", encoded)
	}
}

func TestSessionAnalysisBodyStatesAndBudget(t *testing.T) {
	marker := `[redacted by cctrace client]`
	q := sessionfacts.QuestionReference{ToolUseID: "ask", SrecID: 1, AnswerSrecID: 2, State: "observed", Status: "answered"}
	question := []byte(`{"message":{"content":[{"id":"ask","input":{"questions":[{"header":"Choice","question":"What?","options":[{"label":"Yes","description":"Agreed"}]}]}}]}}`)
	answer := []byte(`{"toolUseResult":{"answers":{"What?":"Yes"}}}`)
	budget, trunc := 512*1024, map[string]bool{}
	got := analysisQuestion(q, "turn", time.Time{}, map[int64]json.RawMessage{1: question, 2: answer}, &budget, trunc)
	if got["state"] != "observed" || budget >= 512*1024 {
		t.Fatalf("observed question=%v budget=%d", got, budget)
	}
	items := got["items"].([]interface{})
	selected := items[0].(map[string]interface{})["answer"].(map[string]interface{})["labels"].([]string)
	if len(selected) != 1 || selected[0] != "Yes" {
		t.Fatalf("answer labels=%v", selected)
	}
	for _, tc := range []struct {
		name string
		raw  map[int64]json.RawMessage
		want string
	}{
		{"missing", nil, "withheld"},
		{"whole redacted", map[int64]json.RawMessage{1: json.RawMessage(`{"redacted":"` + marker + `"}`)}, "redacted"},
		{"answer redacted", map[int64]json.RawMessage{1: question, 2: json.RawMessage(`{"redacted":"` + marker + `"}`)}, "redacted"},
		{"answer object redacted", map[int64]json.RawMessage{1: question, 2: json.RawMessage(`{"toolUseResult":{"answers":"` + marker + `"}}`)}, "redacted"},
		{"field redacted", map[int64]json.RawMessage{1: json.RawMessage(`{"message":{"content":[{"id":"ask","input":{"questions":[{"question":"` + marker + `"}]}}]}}`)}, "redacted"},
		{"input string redacted", map[int64]json.RawMessage{1: json.RawMessage(`{"message":{"content":[{"id":"ask","input":"` + marker + `"}]}}`)}, "redacted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget, trunc := 512*1024, map[string]bool{}
			got := analysisQuestion(q, "turn", time.Time{}, tc.raw, &budget, trunc)
			if got["state"] != tc.want || len(got["items"].([]interface{})) != 0 {
				t.Fatalf("question=%v", got)
			}
		})
	}
	largeQuestion := json.RawMessage(`{"message":{"content":[{"id":"ask","input":{"questions":[{"header":"A","question":"What?","options":[{"label":"` + strings.Repeat("x", 510*1024) + `"},{"label":"` + strings.Repeat("y", 4*1024) + `"}] }]}}]}}`)
	redactedAnswer := json.RawMessage(`{"toolUseResult":{"answers":"` + marker + `"}}`)
	fullPlan := strings.Repeat("p", 16*1024)
	shared := map[int64]json.RawMessage{
		1: largeQuestion, 2: redactedAnswer,
		3: json.RawMessage(`{"message":{"content":[{"id":"plan","input":{"plan":"` + fullPlan + `"}}]}}`),
	}
	budget, trunc = 512*1024, map[string]bool{}
	if got := analysisQuestion(q, "turn", time.Time{}, shared, &budget, trunc); got["state"] != "redacted" || len(got["items"].([]interface{})) != 0 || budget != 512*1024 || trunc["bodies"] {
		t.Fatalf("discarded question consumed body budget: state=%v budget=%d trunc=%v", got["state"], budget, trunc)
	}
	if got := analysisPlan(sessionfacts.PlanReference{ToolUseID: "plan", SrecID: 3}, shared, &budget, trunc); got["state"] != "observed" || got["body"] != fullPlan || budget != 512*1024-16*1024 || trunc["bodies"] {
		t.Fatalf("plan following redacted question was truncated: state=%v body=%v budget=%d trunc=%v", got["state"], got["body"], budget, trunc)
	}
	p := sessionfacts.PlanReference{ToolUseID: "plan", SrecID: 3}
	plans := map[int64]json.RawMessage{3: json.RawMessage(`{"message":{"content":[{"id":"plan","input":{"plan":"` + marker + `"}}]}}`)}
	budget, trunc = 512*1024, map[string]bool{}
	if got := analysisPlan(p, plans, &budget, trunc); got["state"] != "redacted" || got["body"] != nil {
		t.Fatalf("redacted plan=%v", got)
	}
	plans[3] = json.RawMessage(`{"message":{"content":[{"id":"plan","input":"` + marker + `"}]}}`)
	budget, trunc = 512*1024, map[string]bool{}
	if got := analysisPlan(p, plans, &budget, trunc); got["state"] != "redacted" || got["body"] != nil {
		t.Fatalf("redacted input string plan=%v", got)
	}
	p.AttachmentSrecID = 4
	plans[4] = json.RawMessage(`{"attachment":{"type":"plan_file_reference","planContent":"From file"}}`)
	budget, trunc = 512*1024, map[string]bool{}
	if got := analysisPlan(p, plans, &budget, trunc); got["state"] != "redacted" || got["body"] != nil {
		t.Fatalf("redacted tool plan must not fall back to attachment=%v", got)
	}
	delete(plans, 3)
	budget, trunc = 512*1024, map[string]bool{}
	if got := analysisPlan(p, plans, &budget, trunc); got["state"] != "observed" || got["body"] != "From file" {
		t.Fatalf("linked attachment plan=%v", got)
	}
	p.AttachmentSrecID = 0
	budget, trunc = 512*1024, map[string]bool{}
	if got := analysisPlan(p, plans, &budget, trunc); got["state"] != "withheld" || got["body"] != nil {
		t.Fatalf("unlinked attachment must stay withheld=%v", got)
	}
	plans[3] = json.RawMessage(`{"message":{"content":[{"id":"plan","input":{"plan":"` + strings.Repeat("x", 16*1024+1) + `"}}]}}`)
	budget, trunc = 512*1024, map[string]bool{}
	got = analysisPlan(p, plans, &budget, trunc)
	if len(got["body"].(string)) != 16*1024 || !trunc["bodies"] {
		t.Fatalf("plan cap=%v truncated=%v", got["state"], trunc)
	}
	budget, trunc = 1, map[string]bool{}
	if text := analysisSpend("large", &budget, trunc, 512*1024); text != "" || budget != 0 || !trunc["bodies"] {
		t.Fatalf("body cap text=%q budget=%d truncated=%v", text, budget, trunc)
	}
}

func questionSelectionFixture(t *testing.T, labels []string, recommended string, multiSelect bool) json.RawMessage {
	t.Helper()
	options := make([]map[string]interface{}, 0, len(labels))
	for _, label := range labels {
		options = append(options, map[string]interface{}{
			"label": label, "description": "설명 " + label, "recommended": label == recommended,
		})
	}
	raw, err := json.Marshal(map[string]interface{}{"message": map[string]interface{}{"content": []interface{}{
		map[string]interface{}{"id": "ask-multi", "input": map[string]interface{}{"questions": []interface{}{
			map[string]interface{}{"header": "Scope", "question": "Choose", "multiSelect": multiSelect, "options": options},
		}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func multiSelectQuestionFixture(t *testing.T, labels []string, recommended string) json.RawMessage {
	t.Helper()
	return questionSelectionFixture(t, labels, recommended, true)
}

func readQuestionAnswerFixture(t *testing.T, question, answer json.RawMessage) (map[string]interface{}, map[string]interface{}, map[string]interface{}) {
	t.Helper()
	q := sessionfacts.QuestionReference{ToolUseID: "ask-multi", SrecID: 1, AnswerSrecID: 2, State: "observed", Status: "answered"}
	raw := map[int64]json.RawMessage{1: question}
	if answer != nil {
		raw[q.AnswerSrecID] = answer
	}
	budget, trunc := 512*1024, map[string]bool{}
	got := analysisQuestion(q, "turn-1", time.Time{}, raw, &budget, trunc)
	if got["state"] != "observed" {
		t.Fatalf("question state=%v", got["state"])
	}
	items, ok := got["items"].([]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("question items=%v", got["items"])
	}
	item := items[0].(map[string]interface{})
	answerMap, _ := item["answer"].(map[string]interface{})
	return got, item, answerMap
}

func TestSessionAnalysisQuestionSingleChoiceRecommendedSuffix(t *testing.T) {
	tests := []struct {
		name         string
		optionLabels []string
		recommended  string
		selected     string
		wantLabels   []string
		wantFreeText interface{}
	}{
		{name: "unique recommended suffix", optionLabels: []string{"입력 선"}, recommended: "입력 선", selected: "입력 선 (Recommended)", wantLabels: []string{"입력 선"}},
		{name: "exact match takes priority", optionLabels: []string{"입력 선", "입력 선 (Recommended)"}, selected: "입력 선 (Recommended)", wantLabels: []string{"입력 선 (Recommended)"}},
		{name: "normalized duplicate is ambiguous", optionLabels: []string{"입력 선", "입력 선 (Recommended)"}, selected: "입력 선 (Recommended) ", wantFreeText: "입력 선 (Recommended) "},
		{name: "ordinary free response stays free text", optionLabels: []string{"입력 선"}, selected: "원하는 방식으로 정리해 주세요.", wantFreeText: "원하는 방식으로 정리해 주세요."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			question := questionSelectionFixture(t, tt.optionLabels, tt.recommended, false)
			answer, err := json.Marshal(map[string]interface{}{"toolUseResult": map[string]interface{}{
				"answers": map[string]string{"Choose": tt.selected},
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, _, got := readQuestionAnswerFixture(t, question, answer)
			labels, _ := got["labels"].([]string)
			if len(labels) != len(tt.wantLabels) {
				t.Fatalf("selected labels=%v, want %v", labels, tt.wantLabels)
			}
			for i := range labels {
				if labels[i] != tt.wantLabels[i] {
					t.Fatalf("selected labels=%v, want %v", labels, tt.wantLabels)
				}
			}
			if got["free_text"] != tt.wantFreeText {
				t.Fatalf("free_text=%v, want %v", got["free_text"], tt.wantFreeText)
			}
		})
	}
}

func TestSessionAnalysisQuestionMultiSelectRecommendedSuffixes(t *testing.T) {
	question := multiSelectQuestionFixture(t, []string{"API", "UI"}, "")
	answer, err := json.Marshal(map[string]interface{}{"toolUseResult": map[string]interface{}{
		"answers": map[string]string{"Choose": "API (Recommended), UI (Recommended)"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, got := readQuestionAnswerFixture(t, question, answer)
	labels, _ := got["labels"].([]string)
	if len(labels) != 2 || labels[0] != "API" || labels[1] != "UI" || got["free_text"] != nil {
		t.Fatalf("selected labels=%v free_text=%v", labels, got["free_text"])
	}
}

func TestSessionAnalysisQuestionMultiSelectPreservesLabelsAndNotes(t *testing.T) {
	question := multiSelectQuestionFixture(t, []string{"API", "UI"}, "UI")
	answer := json.RawMessage(`{"toolUseResult":{"answers":{"Choose":"API, UI"},"annotations":{"Choose":{"notes":"두 경로가 모두 필요합니다."}}}}`)
	_, item, got := readQuestionAnswerFixture(t, question, answer)
	if item["multi_select"] != true {
		t.Fatalf("multi_select=%v", item["multi_select"])
	}
	labels, _ := got["labels"].([]string)
	if len(labels) != 2 || labels[0] != "API" || labels[1] != "UI" {
		t.Fatalf("selected labels=%v", labels)
	}
	if got["free_text"] != "두 경로가 모두 필요합니다." {
		t.Fatalf("free_text=%v", got["free_text"])
	}
	options := item["options"].([]interface{})
	if options[1].(map[string]interface{})["recommended"] != true {
		t.Fatalf("recommended option marker=%v", options[1])
	}
}

func TestSessionAnalysisQuestionAmbiguousMultiSelectDoesNotGuess(t *testing.T) {
	question := multiSelectQuestionFixture(t, []string{"API", "UI", "API, UI"}, "")
	answer := json.RawMessage(`{"toolUseResult":{"answers":{"Choose":"API, UI"}}}`)
	_, _, got := readQuestionAnswerFixture(t, question, answer)
	labels, _ := got["labels"].([]string)
	if len(labels) != 0 || got["free_text"] != "API, UI" {
		t.Fatalf("ambiguous answer=%v", got)
	}
}

func TestSessionAnalysisQuestionCommaLabelRequiresUniqueExactMatch(t *testing.T) {
	question := multiSelectQuestionFixture(t, []string{"API, UI"}, "")
	answer := json.RawMessage(`{"toolUseResult":{"answers":{"Choose":"API, UI"}}}`)
	_, _, got := readQuestionAnswerFixture(t, question, answer)
	labels, _ := got["labels"].([]string)
	if len(labels) != 1 || labels[0] != "API, UI" || got["free_text"] != nil {
		t.Fatalf("exact comma label=%v", got)
	}
}

func TestSessionAnalysisQuestionUnknownMultiSelectDoesNotGuess(t *testing.T) {
	question := multiSelectQuestionFixture(t, []string{"API", "UI"}, "")
	answer := json.RawMessage(`{"toolUseResult":{"answers":{"Choose":"API, unknown"}}}`)
	_, _, got := readQuestionAnswerFixture(t, question, answer)
	labels, _ := got["labels"].([]string)
	if len(labels) != 0 || got["free_text"] != "API, unknown" {
		t.Fatalf("unknown answer=%v", got)
	}
}

func TestSessionAnalysisQuestionAnnotationsNotesPreserveFreeText(t *testing.T) {
	question := multiSelectQuestionFixture(t, []string{"API", "UI"}, "")
	answer := json.RawMessage(`{"toolUseResult":{"annotations":{"Choose":{"notes":"둘 다 말고 사이드바에 두세요."}}}}`)
	_, _, got := readQuestionAnswerFixture(t, question, answer)
	labels, _ := got["labels"].([]string)
	if len(labels) != 0 || got["free_text"] != "둘 다 말고 사이드바에 두세요." {
		t.Fatalf("annotation answer=%v", got)
	}
}

func TestSessionAnalysisQuestionRedactedNotesStayHidden(t *testing.T) {
	question := multiSelectQuestionFixture(t, []string{"API", "UI"}, "")
	answer := json.RawMessage(`{"toolUseResult":{"answers":{"Choose":"API"},"annotations":{"Choose":{"notes":"[redacted by cctrace client]"}}}}`)
	got, _, answerMap := readQuestionAnswerFixture(t, question, answer)
	if got["state"] != "observed" || answerMap["free_text"] != nil {
		t.Fatalf("redacted note leaked into answer=%v", answerMap)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "[redacted by cctrace client]") {
		t.Fatalf("redaction marker escaped in question=%s", encoded)
	}
}

func TestSessionAnalysisInputImageCountsUseExactSourceRows(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "analysis-input-image-witness"
	base := time.Date(2026, 10, 9, 4, 4, 40, 0, time.UTC)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
		VALUES($1,'owner','owner@example.test','claude',$2,'{}'::jsonb)`, id, base); err != nil {
		t.Fatal(err)
	}
	records := []*SessionRecord{
		{Ts: base, SessionID: id, RecordType: "user", Agent: "claude", UserID: "owner", ProfileEmail: "owner@example.test", UUID: "input-1", PromptSource: "typed",
			Raw: json.RawMessage(`{"cctraceCollect":2,"imagePasteCount":2,"origin":{"kind":"human"},"message":{"role":"user","content":"attach these images"}}`)},
		{Ts: base.Add(5 * time.Second), SessionID: id, RecordType: "assistant", Agent: "claude", UserID: "owner", ProfileEmail: "owner@example.test", UUID: "question-1", ParentUUID: "input-1",
			Raw: json.RawMessage(`{"cctraceCollect":2,"message":{"role":"assistant","content":[{"type":"tool_use","id":"ask-1","name":"AskUserQuestion","input":{"questions":[{"header":"Scope","question":"Which items?","options":[{"label":"API"},{"label":"UI"}]}]}}]}}`)},
		{Ts: base.Add(10 * time.Second), SessionID: id, RecordType: "user", Agent: "claude", UserID: "owner", ProfileEmail: "owner@example.test", UUID: "fixture-precise-full-8", ParentUUID: "question-1",
			Raw: json.RawMessage(`{"cctraceCollect":2,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"ask-1","content":"API, UI"}]},"toolUseResult":{"answers":{"Which items?":"API, UI"}}}`)},
		{Ts: base.Add(20 * time.Second), SessionID: id, RecordType: "assistant", Agent: "claude", UserID: "owner", ProfileEmail: "owner@example.test", UUID: "assistant-1", ParentUUID: "fixture-precise-full-8",
			Raw: json.RawMessage(`{"cctraceCollect":2,"message":{"role":"assistant","content":[{"type":"text","text":"Done."}]}}`)},
		{Ts: base.Add(30 * time.Second), SessionID: id, RecordType: "user", Agent: "claude", UserID: "owner", ProfileEmail: "owner@example.test", UUID: "input-3", ParentUUID: "assistant-1", PromptSource: "typed",
			Raw: json.RawMessage(`{"cctraceCollect":2,"origin":{"kind":"human"},"message":{"role":"user","content":"continue"}}`)},
	}
	if err := s.InsertSessionRecords(ctx, records); err != nil {
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
	got, err := s.ReadSessionAnalysis(ctx, id, SessionAnalysisAccess{User: true, UserID: "owner", OwnerUserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	inputs := got["inputs"].([]interface{})
	if len(inputs) != 3 {
		t.Fatalf("inputs=%v", inputs)
	}
	images := map[string]int{}
	for _, value := range inputs {
		input := value.(map[string]interface{})
		count, ok := input["images"].(int)
		if !ok {
			t.Fatalf("input %s has no exact image count: %v", input["uuid"], input["images"])
		}
		images[input["uuid"].(string)] = count
	}
	if images["input-1"] != 2 || images["fixture-precise-full-8"] != 0 || images["input-3"] != 0 {
		t.Fatalf("per-input images=%v, want input-1=2 answer=0 input-3=0", images)
	}
	turns := got["turns"].([]interface{})
	if len(turns) != 2 || turns[0].(map[string]interface{})["images"] != 2 || turns[1].(map[string]interface{})["images"] != 0 {
		t.Fatalf("turn aggregate images=%v, want 2 and 0", turns)
	}
}

func TestSessionAnalysisRecapContentStates(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	const id = "analysis-recap-content"
	read := func() map[string]interface{} {
		t.Helper()
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		budget, trunc := 512*1024, map[string]bool{}
		result, err := analysisRecaps(ctx, tx, id, SessionAnalysisAccess{User: true, UserID: "owner"}, sessionfacts.RecapHint{SystemRowsCollected: true, Turns: 2}, &budget, trunc)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email,ts,raw) VALUES
	($1,'system','owner','owner@example.com',now()-interval '2 minutes','{"subtype":"away_summary","cctraceWithheld":["content"]}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	if got := read(); got["state"] != "withheld" || got["absent_reason"] != "withheld" {
		t.Fatalf("withheld recap=%v", got)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email,ts,raw) VALUES
	($1,'system','owner','owner@example.com',now()-interval '90 seconds','{"subtype":"away_summary","content":"Same recap (disable recaps in /config)"}'::jsonb),
	($1,'system','owner','owner@example.com',now()-interval '60 seconds','{"subtype":"away_summary","content":"Same recap (disable recaps in /config)"}'::jsonb)`, id); err != nil {
		t.Fatal(err)
	}
	got := read()
	items := got["items"].([]interface{})
	if got["state"] != "observed" || len(items) != 1 || items[0].(map[string]interface{})["text"] != "Same recap" || items[0].(map[string]interface{})["repeats"] != 2 {
		t.Fatalf("repeated recaps=%v", got)
	}
}

func TestSessionAnalysisReadUnsupportedAndTimeout(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	_, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups(scope_type,scope_value,session_id,start_time,end_time,has_api_request,agent) VALUES('all','','unsupported',now(),now(),true,'gjc')`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ReadSessionAnalysis(ctx, "unsupported", SessionAnalysisAccess{})
	if err != nil || result["state"] != "unsupported" {
		t.Fatalf("unsupported=%v, %v", result, err)
	}
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	_, err = s.ReadSessionAnalysis(expired, "unsupported", SessionAnalysisAccess{})
	if err == nil {
		t.Fatal("expired context unexpectedly succeeded")
	}
}
