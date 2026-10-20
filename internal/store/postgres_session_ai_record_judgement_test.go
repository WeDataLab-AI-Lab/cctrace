package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/sessionfacts"
)

func rowJudgementWith(committed interface{}, turn, question, background interface{}) map[string]interface{} {
	analysis := map[string]interface{}{"signals": map[string]interface{}{
		"turn": turn, "question": question, "background": background,
	}}
	if committed != nil {
		analysis["repo_status"] = committed
	}
	return rowSessionRecordJudgement(analysis, true)
}

func TestSessionJudgementBucketPriorityAndAllPassContract(t *testing.T) {
	for _, tc := range []struct {
		states []string
		want   string
	}{
		{[]string{"fail", "fail", "pass", "fail", "undeterminable"}, "ended_midway"},
		{[]string{"undeterminable", "pass", "fail", "pass", "undeterminable"}, "leftover"},
		{[]string{"pass", "pass", "pass", "pass", "pass"}, "done"},
		{[]string{"pass", "pass", "pass", "pass", "undeterminable"}, "unknown"},
	} {
		if got := sessionJudgementBucket(tc.states); got != tc.want {
			t.Fatalf("bucket(%v)=%s want %s", tc.states, got, tc.want)
		}
	}
}

func TestRowSessionRecordJudgementUsesObservedCommittedSnapshot(t *testing.T) {
	changedAt := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	zero := rowJudgementWith(map[string]interface{}{"uncommitted_edited_files": 0, "uncommitted_state": "observed",
		"uncommitted_source": "session_git_snapshot", "uncommitted_reason": nil, "changed_at": changedAt}, "ended", float64(0), float64(0))
	if zero["bucket"] != nil || zero["status"] != "unknown" {
		t.Fatalf("work_done is not validated, so an otherwise passing row remains unknown: %v", zero)
	}
	checks := zero["checks"].([]string)
	if checks[3] != "pass" || checks[4] != "undeterminable" || zero["committed_source"] != "session_snapshots.snapshot.git.uncommitted_edited_files" || zero["committed_as_of"] != changedAt {
		t.Fatalf("observed zero committed signal or provenance: %v", zero)
	}

	positive := rowJudgementWith(map[string]interface{}{"uncommitted_edited_files": 3, "uncommitted_state": "observed",
		"uncommitted_source": "session_git_snapshot", "uncommitted_reason": nil, "changed_at": changedAt}, "ended", float64(0), float64(0))
	if positive["bucket"] != "leftover" || positive["status"] != "leftover" {
		t.Fatalf("positive committed signal bucket: %v", positive)
	}
	positiveChecks := positive["checks"].([]string)
	if positiveChecks[3] != "fail" || positive["committed_count"] != int64(3) || positive["committed_as_of"] != changedAt {
		t.Fatalf("positive committed evidence: %v", positive)
	}
}

func TestRowSessionRecordJudgementKeepsUnknownAndTurnFailureBuckets(t *testing.T) {
	unknown := rowJudgementWith(map[string]interface{}{"uncommitted_edited_files": nil, "uncommitted_state": "undeterminable",
		"uncommitted_source": "unknown", "uncommitted_reason": "partial_snapshot"}, "ended", float64(0), float64(0))
	if unknown["bucket"] != nil || unknown["committed_reason"] != "partial_snapshot" {
		t.Fatalf("partial snapshot became a pass: %v", unknown)
	}
	turnFailure := rowJudgementWith(map[string]interface{}{"uncommitted_edited_files": 2, "uncommitted_state": "observed",
		"uncommitted_source": "session_git_snapshot", "uncommitted_reason": nil}, "interrupted", float64(1), float64(0))
	if turnFailure["bucket"] != "ended_midway" || turnFailure["status"] != "ended_midway" {
		t.Fatalf("turn failure must precede other failures: %v", turnFailure)
	}
}

func TestRowSessionRecordJudgementKeepsLiveStatusSeparateFromBucket(t *testing.T) {
	analysis := map[string]interface{}{"category": "running", "signals": map[string]interface{}{
		"turn": "ended", "question": float64(0), "background": float64(0),
	}, "repo_status": map[string]interface{}{"uncommitted_edited_files": 0, "uncommitted_state": "observed"}}
	judgement := rowSessionRecordJudgement(analysis, true)
	if judgement["status"] != "running" || judgement["bucket"] != nil {
		t.Fatalf("live status and incomplete work bucket should stay separate: %v", judgement)
	}
	analysis["category"] = "waiting"
	analysis["signals"].(map[string]interface{})["turn"] = "interrupted"
	judgement = rowSessionRecordJudgement(analysis, true)
	if judgement["status"] != "interrupted" || judgement["bucket"] != "ended_midway" {
		t.Fatalf("interrupted live status and observed bucket: %v", judgement)
	}
}

func TestRowSessionRecordJudgementDoesNotUseTruncated_P1b_Facts(t *testing.T) {
	analysis := map[string]interface{}{"state": "truncated", "signals": map[string]interface{}{
		"turn": "interrupted", "question": float64(1), "background": float64(0),
	}, "repo_status": map[string]interface{}{"uncommitted_edited_files": 3, "uncommitted_state": "observed"}}
	judgement := rowSessionRecordJudgement(analysis, true)
	checks := judgement["checks"].([]string)
	if judgement["bucket"] != nil || judgement["status"] != "unknown" || checks[0] != "undeterminable" || checks[3] != "undeterminable" {
		t.Fatalf("truncated source became evidence: %v", judgement)
	}
}

func TestSessionRecordJudgementUsesTheAnalysisGitSnapshotEvidence(t *testing.T) {
	facts := &sessionfacts.Session{LastTurnStatus: "ended", Detail: sessionfacts.SessionDetail{Signals: sessionfacts.Signals{
		Turn:       sessionfacts.Signal{State: "observed", Value: ptrInt(1)},
		Question:   sessionfacts.Signal{State: "observed", Value: ptrInt(0)},
		Background: sessionfacts.Signal{State: "observed", Value: ptrInt(0)},
	}}}
	changedAt := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	response := map[string]interface{}{"header": map[string]interface{}{"repo_status": map[string]interface{}{
		"uncommitted_edited_files": 0, "uncommitted_state": "observed", "uncommitted_source": "session_git_snapshot",
		"uncommitted_reason": nil, "changed_at": changedAt,
	}}}
	judgement := sessionRecordJudgement(facts, "none", "no_record", nil, response)
	checks := judgement["checks"].([]map[string]interface{})
	if checks[3]["state"] != "pass" || checks[3]["source"] != "session_snapshots.snapshot.git.uncommitted_edited_files" || checks[3]["as_of"] != changedAt {
		t.Fatalf("direct committed evidence=%v", checks[3])
	}
	if judgement["bucket"] != nil || judgement["status"] != "unknown" {
		t.Fatalf("unvalidated work_done must keep the bucket unknown: %v", judgement)
	}
	response["header"].(map[string]interface{})["repo_status"] = map[string]interface{}{
		"uncommitted_edited_files": 2, "uncommitted_state": "observed", "uncommitted_source": "session_git_snapshot",
		"uncommitted_reason": nil, "changed_at": changedAt,
	}
	judgement = sessionRecordJudgement(facts, "none", "no_record", nil, response)
	checks = judgement["checks"].([]map[string]interface{})
	if judgement["bucket"] != "leftover" || checks[3]["state"] != "fail" || checks[3]["count"] != int64(2) {
		t.Fatalf("positive committed evidence=%v judgement=%v", checks[3], judgement)
	}
}

func TestSessionOverviewJudgementBucketsAndFiltersUseTheSameSnapshotFacts(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.test", "judgement-owner")
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	type fixture struct {
		id, lastTurn, turnState, questionState, backgroundState string
		question, background                                    int
		gitCount                                                *int
		truncated                                               bool
	}
	positive, zero := 3, 0
	fixtures := []fixture{
		{id: "judgement-midway", lastTurn: "interrupted", turnState: "observed", questionState: "observed", backgroundState: "observed", question: 0, background: 0, gitCount: &zero},
		{id: "judgement-question", lastTurn: "ended", turnState: "observed", questionState: "observed", backgroundState: "observed", question: 1, background: 0, gitCount: &zero},
		{id: "judgement-committed", lastTurn: "ended", turnState: "observed", questionState: "observed", backgroundState: "observed", question: 0, background: 0, gitCount: &positive},
		{id: "judgement-unknown", turnState: "undeterminable", questionState: "undeterminable", backgroundState: "undeterminable"},
		{id: "judgement-zero", lastTurn: "ended", turnState: "observed", questionState: "observed", backgroundState: "observed", question: 0, background: 0, gitCount: &zero},
		{id: "judgement-truncated", lastTurn: "interrupted", turnState: "observed", questionState: "observed", backgroundState: "observed", question: 0, background: 0, gitCount: &positive, truncated: true},
	}
	for i, row := range fixtures {
		at := base.Add(time.Duration(i) * time.Second)
		record := &SessionRecord{Ts: at, SessionID: row.id, ProjectHash: "judgement-project", RecordType: "user",
			ProfileEmail: "owner@example.test", UserID: "judgement-owner", Agent: "claude", UUID: "prompt-" + row.id,
			Raw: json.RawMessage(`{"message":{"role":"user","content":"fixture"}}`)}
		if err := s.InsertSessionRecords(ctx, []*SessionRecord{record}); err != nil {
			t.Fatal(err)
		}
		detail, err := json.Marshal(map[string]interface{}{"signals": map[string]interface{}{
			"turn":       map[string]interface{}{"state": row.turnState},
			"question":   map[string]interface{}{"state": row.questionState, "value": row.question},
			"background": map[string]interface{}{"state": row.backgroundState, "value": row.background},
		}})
		if err != nil {
			t.Fatal(err)
		}
		coverage := "full"
		if row.truncated {
			coverage = "truncated"
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_analysis_facts
			(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,project_hash,repository_key,transcript,
			title,title_source,title_state,first_activity_at,last_activity_at,turn_boundaries,turns,requests,inputs,cost_source,
			last_turn_status,open_questions,detail,output_hash,source_generation,rules_version,computed_at)
			VALUES($1,'claude',$8,ARRAY['judgement-owner'],ARRAY['owner@example.test'],'judgement-owner','owner@example.test',
			'judgement-project','judgement-project',true,$2,'ai','ok',$3,$3,'observed',1,1,1,'none',$4,$5,$6,$7,1,$9,$3)`,
			row.id, row.id, at, row.lastTurn, nullableJudgementCount(row.questionState, row.question), detail, make([]byte, 32), coverage, sessionfacts.RulesVersion); err != nil {
			t.Fatalf("seed facts for %s: %v", row.id, err)
		}
		if row.gitCount != nil {
			gitSnapshot, _ := json.Marshal(map[string]interface{}{"git": map[string]interface{}{
				"changed_at": base.Add(-24 * time.Hour), "edited_files": 5, "uncommitted_edited_files": *row.gitCount,
				"repositories": 1, "unresolved": 0, "partial": false,
			}})
			if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,agent,observed_at,snapshot)
				VALUES($1,'judgement-owner','owner@example.test','claude',$2,$3)`, row.id, at, gitSnapshot); err != nil {
				t.Fatalf("seed snapshot for %s: %v", row.id, err)
			}
		}
	}
	since, until := base.Add(-time.Minute), base.Add(time.Minute)
	f := SessionOverviewFilter{UserID: "judgement-owner", PersonalUserID: "judgement-owner", PersonalEmail: "owner@example.test",
		DashboardUserID: owner, Since: &since, Until: &until, IncludeAnalysis: true, AnalysisOwner: true, IncludeSessionRecords: true, Limit: 100}
	counts, err := s.SessionOverviewJudgementBucketCounts(ctx, f)
	if err != nil || counts["done"] != 0 || counts["leftover"] != 2 || counts["ended_midway"] != 1 || counts["unknown"] != 3 {
		t.Fatalf("full judgment counts=%v err=%v", counts, err)
	}
	for bucket, want := range map[string][]string{
		"ended_midway": {"judgement-midway"},
		"leftover":     {"judgement-committed", "judgement-question"},
		"unknown":      {"judgement-truncated", "judgement-unknown", "judgement-zero"},
		"done":         {},
	} {
		filter := f
		filter.AnalysisBucket = bucket
		rows, err := s.ListSessionOverviews(ctx, filter)
		if err != nil || len(rows) != len(want) {
			t.Fatalf("bucket %s rows=%d err=%v", bucket, len(rows), err)
		}
		seen := map[string]bool{}
		for _, row := range rows {
			seen[row.SessionID] = true
		}
		for _, id := range want {
			if !seen[id] {
				t.Fatalf("bucket %s missing %s: %v", bucket, id, seen)
			}
		}
	}
}

func nullableJudgementCount(state string, count int) *int {
	if state != "observed" {
		return nil
	}
	return &count
}
