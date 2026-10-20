package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"cctrace/internal/sessionfacts"
)

func TestSessionGroupClustersUsesGroupHead(t *testing.T) {
	base := time.Now()
	groups := sessionGroupClusters([]*groupCandidate{
		{id: "second", endedAt: base.Add(-55 * time.Second)},
		{id: "third", endedAt: base.Add(-110 * time.Second)},
		{id: "first", endedAt: base},
	})
	if len(groups) != 2 || len(groups[0]) != 2 || groups[0][0].id != "first" || groups[1][0].id != "third" {
		t.Fatalf("transitive chain must split by group head: %+v", groups)
	}
	groups = sessionGroupClusters([]*groupCandidate{
		{id: "project-a", projectHash: "a", endedAt: base},
		{id: "project-b", projectHash: "b", endedAt: base.Add(-time.Second)},
	})
	if len(groups) != 2 {
		t.Fatalf("different projects must not share an end group: %+v", groups)
	}
}

func groupEndFixture(t *testing.T, s *PgStore, session string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	personalSourceAt(t, s, session, "owner@example.com", "uid-owner", at.Add(-time.Minute))
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_observation_events
		(session_id,user_id,profile_email,event_id,kind,observed_at,identity_provenance)
		VALUES($1,'uid-owner','owner@example.com','end','session_end',$2,'personal_token')`, session, at); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{session})
}

func groupAnalysisFactFixture(t *testing.T, s *PgStore, session, title string, hasPlan bool, at time.Time) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(), `INSERT INTO session_analysis_facts
		(session_id,agent,coverage,user_ids,profile_emails,owner_user_id,owner_profile_email,project_hash,repository_key,transcript,
		title,title_source,title_state,first_activity_at,last_activity_at,turn_boundaries,turns,requests,inputs,cost_source,
		has_plan_file,used_plan_mode,open_questions,last_turn_key,last_turn_status,detail,output_hash,source_generation,rules_version,computed_at)
		VALUES($1,'claude','full',ARRAY['uid-owner'],ARRAY['owner@example.com'],'uid-owner','owner@example.com','', '',true,
		$2,'ai','ok',$3,$3,'observed',1,1,1,'none',$4,false,0,'turn','ended',
		'{"signals":{"turn":{"state":"observed","value":0},"question":{"state":"observed","value":0},"background":{"state":"observed","value":0},"todo":{"state":"observed","value":0}}}'::jsonb,
		decode(repeat('00',32),'hex'),1,$5,$3)`, session, title, at, hasPlan, sessionfacts.RulesVersion)
	if err != nil {
		t.Fatalf("seed analysis fact for %s: %v", session, err)
	}
}

func TestSessionGroupsUseTheFilteredAnalysisPopulation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	for _, row := range []struct {
		id       string
		title    string
		plan     bool
		end      bool
		endAt    time.Time
		activity time.Time
	}{
		{id: "analysis-keep-a", title: "keep calendar A", plan: true, end: true, endAt: base, activity: base.Add(-time.Minute)},
		{id: "analysis-keep-b", title: "keep calendar B", plan: true, end: true, endAt: base.Add(-10 * time.Second), activity: base.Add(-time.Minute)},
		{id: "analysis-drop", title: "other project", plan: false, end: true, endAt: base, activity: base.Add(-time.Minute)},
		{id: "analysis-recent", title: "recent activity", plan: true, activity: time.Now().UTC().Add(-20 * time.Minute)},
	} {
		if row.end {
			groupEndFixture(t, s, row.id, row.endAt)
		} else {
			personalSourceAt(t, s, row.id, "owner@example.com", "uid-owner", row.activity)
		}
		groupAnalysisFactFixture(t, s, row.id, row.title, row.plan, row.activity)
		refreshOverviewRollupsForTest(t, s, []string{row.id})
	}
	since, until := base.Add(-2*time.Minute), time.Now().UTC().Add(time.Minute)
	f := SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner",
		Since: &since, Until: &until, IncludeAnalysis: true, AnalysisOwner: true, IncludeSessionRecords: true, AnalysisQuery: "keep",
		AnalysisPlan: "file", AnalysisSort: "cost"}
	got, err := s.GetSessionGroups(ctx, f)
	if err != nil || got.OverviewTotal != 2 || got.Counts.Ended != 2 || got.Counts.EndedGroups != 1 || len(got.Ended) != 1 || got.Ended[0].Total != 2 {
		t.Fatalf("q+plan group scope: %+v err=%v", got, err)
	}
	f.AnalysisBucket = "unknown"
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || got.OverviewTotal != 2 || got.Counts.Ended != 2 || got.Counts.EndedGroups != 1 {
		t.Fatalf("bucket-filtered group scope: %+v err=%v", got, err)
	}
	f.AnalysisBucket = "leftover"
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || got.OverviewTotal != 0 || got.Counts.Ended != 0 {
		t.Fatalf("empty bucket-filtered group scope: %+v err=%v", got, err)
	}
	f.AnalysisBucket, f.AnalysisQuery, f.AnalysisPlan, f.AnalysisCategory = "", "", "", "recent"
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || got.OverviewTotal != 1 || got.Counts.Ended != 0 || got.Counts.Running != 0 {
		t.Fatalf("category group scope: %+v err=%v", got, err)
	}
	f.AnalysisCategory = ""
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || got.OverviewTotal != 4 {
		t.Fatalf("unfiltered analysis group scope must retain base overview population: %+v err=%v", got, err)
	}
}

func TestSessionGroupDismissalsDurableEpochOwnerAndAtomicity(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	other := personalFixture(t, s, "other@example.com", "uid-other")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	for _, id := range []string{"one", "two", "solo", "live", "stale", "jsonl-only", "mixed"} {
		personalSourceAt(t, s, id, "owner@example.com", "uid-owner", base.Add(-2*time.Minute))
	}
	// The group candidates must use observed ends, not the last JSONL line.
	for _, row := range []struct {
		id string
		at time.Time
	}{{"one", base.Add(time.Minute)}, {"two", base.Add(30 * time.Second)}, {"solo", base.Add(-time.Minute)}, {"live", base.Add(time.Minute)}, {"stale", base.Add(time.Minute)}, {"mixed", base.Add(time.Minute)}} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO session_observation_events
			(session_id,user_id,profile_email,event_id,kind,observed_at,identity_provenance)
			VALUES($1,'uid-owner','owner@example.com',$2,'session_end',$3,'personal_token')`, row.id, "end-"+row.id, row.at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states
		(session_id,user_id,profile_email,pid,proc_start,status,heartbeat_at,identity_provenance)
		VALUES ('live','uid-owner','owner@example.com',1,'a','busy',now(),'personal_token'),
		('live','uid-owner','owner@example.com',2,'b','idle',now(),'personal_token'),
		('stale','uid-owner','owner@example.com',3,'c','busy',now()-interval '4 minutes','personal_token')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email)
		VALUES ('mixed','user','uid-other','other@example.com')`); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{"one", "two", "solo", "live", "stale", "jsonl-only", "mixed"})
	filter := SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner"}
	got, err := s.GetSessionGroups(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts.Running != 1 || got.Running[0].SessionID != "live" || got.Counts.Ended != 4 || len(got.Ended) != 1 || len(got.Ended[0].Items) != 3 {
		t.Fatalf("scoped groups: %+v", got)
	}
	items := []SessionGroupDismissal{{SessionID: "one", EndedAt: base.Add(time.Minute)}, {SessionID: "two", EndedAt: base.Add(30 * time.Second)}}
	if err = s.UpdateSessionGroupDismissals(ctx, filter, []SessionGroupDismissal{items[0], {SessionID: "other", EndedAt: items[1].EndedAt}}, true); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("atomic invalid member: %v", err)
	}
	if n := countWhere(t, s, "session_group_dismissals", "session_id='one'"); n != 0 {
		t.Fatalf("invalid batch saved %d rows", n)
	}
	if err = s.UpdateSessionGroupDismissals(ctx, filter, items, true); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSessionGroups(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	dismissed := make(map[string]bool)
	for _, item := range got.Ended[0].Items {
		dismissed[item.SessionID] = item.Dismissed
	}
	if !dismissed["one"] || !dismissed["two"] || dismissed["stale"] {
		t.Fatalf("durable GET: %+v", got)
	}
	filter.DashboardUserID = other
	got, err = s.GetSessionGroups(ctx, filter)
	if err != nil || got.Ended[0].Items[0].Dismissed {
		t.Fatalf("separate dashboard user: %+v, %v", got, err)
	}
	filter.DashboardUserID = owner
	if err = s.UpdateSessionGroupDismissals(ctx, filter, items, false); err != nil {
		t.Fatalf("undismiss: %v", err)
	}
	if n := countWhere(t, s, "session_group_dismissals", "session_id='one'"); n != 0 {
		t.Fatalf("delete left %d rows", n)
	}
}

func TestSessionGroupDismissalsMergeDeleteAndNewEpoch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	// Both epoch UPDATEs must stay inside the source row's UTC chunk.
	base := time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
	groupEndFixture(t, s, "a", base)
	groupEndFixture(t, s, "b", base.Add(-30*time.Second))
	f := SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner"}
	item := SessionGroupDismissal{SessionID: "a", EndedAt: base}
	if err := s.UpdateSessionGroupDismissals(ctx, f, []SessionGroupDismissal{item}, true); err != nil {
		t.Fatal(err)
	}
	newEnd := base.Add(2 * time.Minute)
	if _, err := s.pool.Exec(ctx, `UPDATE session_observation_events SET observed_at=$2 WHERE session_id=$1`, "a", newEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET ts=$2 WHERE session_id=$1`, "a", newEnd.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{"a"})
	newGroups, err := s.GetSessionGroups(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range newGroups.Ended {
		for _, member := range group.Items {
			if member.SessionID == "a" && member.Dismissed {
				t.Fatal("new end epoch inherited old dismissal")
			}
		}
	}
	if err := s.UpdateSessionGroupDismissals(ctx, f, []SessionGroupDismissal{item}, true); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("old epoch accepted after new end: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_observation_events SET observed_at=$2 WHERE session_id=$1`, "a", base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET ts=$2 WHERE session_id=$1`, "a", base.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{"a"})
	if err := s.MergeUsers(ctx, "owner@example.com", "", "new@example.com"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if n := countWhere(t, s, "session_group_dismissals", "session_id='a' AND owner_state='invalidated'"); n != 1 {
		t.Fatalf("merge must invalidate preference: %d", n)
	}
	f.UserID, f.PersonalUserID = "", ""
	f.ProfileEmail, f.PersonalEmail = "new@example.com", "new@example.com"
	if err := s.UpdateSessionGroupDismissals(ctx, f, []SessionGroupDismissal{item}, true); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("same invalidated epoch resurrected: %v", err)
	}
	if err := s.DeleteUserData(ctx, "owner@example.com", ""); err != nil {
		t.Fatalf("delete original key after merge: %v", err)
	}
	if n := countWhere(t, s, "session_group_dismissals", "session_id='a'"); n != 0 {
		t.Fatalf("delete original identity must reach invalidated preference: %d", n)
	}
}

func TestSessionGroupsRejectUnverifiedPositiveObservations(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	id := personalFixture(t, s, "owner@example.com", "uid-owner")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	groupEndFixture(t, s, "a", base)
	groupEndFixture(t, s, "b", base.Add(-30*time.Second))
	f := SessionOverviewFilter{DashboardUserID: id, UserID: "uid-owner", PersonalUserID: "uid-owner"}
	// A raw record proves ownership, not the provenance of subsequent shared-key observations.
	if _, err := s.pool.Exec(ctx, `UPDATE session_observation_events SET identity_provenance='unknown' WHERE session_id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states
		(session_id,user_id,profile_email,pid,proc_start,status,heartbeat_at,identity_provenance)
		VALUES ('a','uid-owner','owner@example.com',1,'forged','busy',now(),'unknown')`); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSessionGroups(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts.Running != 0 || got.Counts.Ended != 1 || len(got.Ended) != 0 {
		t.Fatalf("unverified matching observations established live/end: %+v", got)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_live_states SET identity_provenance='personal_token' WHERE session_id='a'`); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || got.Counts.Running != 1 || got.Running[0].SessionID != "a" {
		t.Fatalf("verified owner observation not live: %+v, %v", got, err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_live_states SET heartbeat_at=now()-interval '4 minutes' WHERE session_id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, `UPDATE session_observation_events SET identity_provenance='personal_token' WHERE session_id='a'`); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || got.Counts.Ended != 2 || len(got.Ended) != 1 {
		t.Fatalf("verified end absent: %+v, %v", got, err)
	}
}

func TestSessionGroupsMultiplePIDFreshnessAndNullStale(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	for _, id := range []string{"multi", "null-change", "stale"} {
		personalSourceAt(t, s, id, "owner@example.com", "uid-owner", time.Now().Add(-8*time.Hour))
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states
		(session_id,user_id,profile_email,pid,proc_start,status,status_changed_at,heartbeat_at,identity_provenance)
		VALUES ('multi','uid-owner','owner@example.com',1,'old','waiting',now()-interval '1 hour',now()-interval '2 minutes','personal_token'),
		('multi','uid-owner','owner@example.com',2,'new','idle',now(),now()-interval '1 minute','personal_token'),
		('null-change','uid-owner','owner@example.com',3,'null','busy',NULL,now(),'personal_token'),
		('stale','uid-owner','owner@example.com',4,'stale','waiting',now()-interval '7 hours',now(),'personal_token')`); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{"multi", "null-change", "stale"})
	got, err := s.GetSessionGroups(ctx, SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner"})
	if err != nil {
		t.Fatal(err)
	}
	categories := make(map[string]string)
	for _, row := range got.Running {
		categories[row.SessionID] = row.Category
	}
	if got.Counts.Running != 2 || categories["multi"] != "idle" || categories["null-change"] != "running" || categories["stale"] != "" {
		t.Fatalf("fresh latest PID and null timestamps: %+v", got)
	}
}

func TestSessionGroupsBoundPayloadWithoutClaimingWindowCountsAsTotal(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	base := time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records
		(session_id,record_type,user_id,profile_email,ts,uuid)
		SELECT 'many-'||g,'user','uid-owner','owner@example.com',$1::timestamptz-g*interval '1 second','many-'||g
		FROM generate_series(1,501) g`, base); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = fmt.Sprintf("many-%d", i+1)
	}
	refreshOverviewRollupsForTest(t, s, ids)
	f := SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner"}
	got, err := s.GetSessionGroups(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != "filtered_total" || got.OverviewTotal != 501 || got.Counts.Running != 0 || got.Counts.Ended != 0 {
		t.Fatalf("full filtered counts: %+v", got)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_live_states
		(session_id,user_id,profile_email,pid,proc_start,status,heartbeat_at,identity_provenance)
		VALUES ('many-501','uid-owner','owner@example.com',1,'last','busy',now(),'personal_token')`); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || got.Scope != "filtered_total" || got.Counts.Running != 1 {
		t.Fatalf("501st verified running must count after 500 inactive rows: %+v, %v", got, err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_live_states`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_observation_events
		(session_id,user_id,profile_email,event_id,kind,observed_at,identity_provenance)
		SELECT session_id,'uid-owner','owner@example.com','end','session_end',$1,'personal_token'
		FROM session_records`, base); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET ts=$1`, base.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || !got.Truncated || got.Counts.Ended != 501 || got.Counts.EndedGroups != 1 || len(got.Ended) != 1 || len(got.Ended[0].Items) != 500 {
		t.Fatalf("bounded payload with complete ended totals: %+v, %v", got, err)
	}
	visible := make(map[string]bool)
	for _, member := range got.Ended[0].Items {
		visible[member.SessionID] = true
	}
	var outside string
	for _, id := range ids {
		if !visible[id] {
			outside = id
			break
		}
	}
	if outside == "" {
		t.Fatal("fixture needs a member beyond the payload cap")
	}
	item := []SessionGroupDismissal{{SessionID: outside, EndedAt: base}}
	if err := s.UpdateSessionGroupDismissals(ctx, f, item, true); err != nil {
		t.Fatalf("dismiss outside GET cap: %v", err)
	}
	if err := s.UpdateSessionGroupDismissals(ctx, f, item, false); err != nil {
		t.Fatalf("restore outside GET cap: %v", err)
	}
	key := got.Ended[0].Key
	if got.Ended[0].Total != 501 {
		t.Fatalf("group total=%d", got.Ended[0].Total)
	}
	cursor := ""
	seen := make(map[string]bool)
	var all []SessionGroupDismissal
	firstCursor := ""
	for {
		page, err := s.GetSessionGroupMembers(ctx, f, key, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 501 || len(page.Items) > 100 {
			t.Fatalf("page: %+v", page)
		}
		for _, member := range page.Items {
			if seen[member.SessionID] {
				t.Fatal("duplicate member")
			}
			seen[member.SessionID] = true
			all = append(all, SessionGroupDismissal{SessionID: member.SessionID, EndedAt: member.EndedAt})
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if firstCursor == "" {
			firstCursor = cursor
		}
	}
	if len(all) != 501 {
		t.Fatalf("paged members=%d", len(all))
	}
	for offset := 0; offset < len(all); offset += 100 {
		end := min(offset+100, len(all))
		if err := s.UpdateSessionGroupDismissals(ctx, f, all[offset:end], true, key); err != nil {
			t.Fatal(err)
		}
	}
	if n := countWhere(t, s, "session_group_dismissals", "session_id LIKE 'many-%'"); n != 501 {
		t.Fatalf("all batch ACK saved %d", n)
	}
	if err := s.UpdateSessionGroupDismissals(ctx, f, all[:100], true, key); err != nil {
		t.Fatalf("retry ACK: %v", err)
	}
	page, err := s.GetSessionGroupMembers(ctx, f, key, firstCursor, 100)
	if err != nil || !page.Items[0].Dismissed {
		t.Fatalf("stable key after preferences: %+v %v", page, err)
	}
	if _, err := s.GetSessionGroupMembers(ctx, f, key, "broken", 100); !errors.Is(err, ErrPersonalInvalid) {
		t.Fatalf("cursor: %v", err)
	}
	if _, err := s.GetSessionGroupMembers(ctx, f, key, "", 101); !errors.Is(err, ErrPersonalInvalid) {
		t.Fatalf("limit: %v", err)
	}
	// Even a non-head member epoch change invalidates both paging and mutations.
	if _, err := s.pool.Exec(ctx, `UPDATE session_observation_events SET observed_at=observed_at-interval '1 second' WHERE session_id=$1`, all[250].SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionGroupMembers(ctx, f, key, firstCursor, 100); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("stale group page: %v", err)
	}
	if err := s.UpdateSessionGroupDismissals(ctx, f, all[:100], false, key); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("stale group mutation: %v", err)
	}
	if n := countWhere(t, s, "session_group_dismissals", "session_id LIKE 'many-%'"); n != 501 {
		t.Fatalf("stale request partially applied: %d", n)
	}
}

func TestSessionGroupDismissalsObservationRetentionAtomic(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	base := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Microsecond)
	groupEndFixture(t, s, "a", base)
	groupEndFixture(t, s, "b", base.Add(-30*time.Second))
	f := SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner"}
	if err := s.UpdateSessionGroupDismissals(ctx, f, []SessionGroupDismissal{{SessionID: "a", EndedAt: base}}, true); err != nil {
		t.Fatal(err)
	}
	// A surviving foreign source must not keep the original owner's preference.
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_observation_events
		(session_id,user_id,profile_email,event_id,kind,observed_at,identity_provenance)
		VALUES ('a','uid-other','other@example.com','later','session_end',now(),'personal_token')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneSessionObservations(ctx, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"session_group_dismissals", "session_group_dismissal_identity_keys"} {
		if n := countWhere(t, s, table, "session_id='a'"); n != 0 {
			t.Fatalf("retention committed with %s orphan: %d", table, n)
		}
	}
}

func TestSessionGroupsFilterCountsAndHeadAcrossBatch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	ids := make([]string, 502)
	for i := range ids {
		ids[i] = fmt.Sprintf("boundary-%03d", i)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email,ts,uuid,project_hash)
		SELECT id,'user','uid-owner','owner@example.com',$1::timestamptz-interval '5 minutes',id,'-project'
		FROM unnest($2::text[]) id`, base, ids); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_observation_events(session_id,user_id,profile_email,event_id,kind,observed_at,identity_provenance)
		SELECT id,'uid-owner','owner@example.com','end','session_end',
		CASE WHEN id='boundary-500' THEN $1::timestamptz-interval '55 seconds' WHEN id='boundary-501' THEN $1::timestamptz-interval '110 seconds' ELSE $1::timestamptz END,'personal_token'
		FROM unnest($2::text[]) id`, base, ids); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, ids)
	f := SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner", ProjectHashes: []string{"-project"}}
	got, err := s.GetSessionGroups(ctx, f)
	if err != nil || got.Counts.Ended != 502 || got.Counts.EndedGroups != 1 || !got.Truncated {
		t.Fatalf("head grouping across batch: %+v %v", got, err)
	}
	if err := s.UpdateSessionGroupDismissals(ctx, f, []SessionGroupDismissal{{SessionID: ids[501], EndedAt: base.Add(-110 * time.Second)}}, true); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("transitive singleton became eligible: %v", err)
	}
	since, until := base.Add(-6*time.Minute), base
	f.Since, f.Until = &since, &until
	f.Classification = "unsorted"
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || got.OverviewTotal != 502 || got.Counts.Ended != 502 || got.Counts.EndedGroups != 1 {
		t.Fatalf("period/classification filter parity: %+v %v", got, err)
	}
	f.ProjectHashes = []string{"-other"}
	got, err = s.GetSessionGroups(ctx, f)
	if err != nil || got.Counts.Ended != 0 || got.OverviewTotal != 0 {
		t.Fatalf("project filter: %+v %v", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err = s.GetSessionGroups(cancelled, f); err == nil || got != nil {
		t.Fatalf("cancelled read returned success: %+v %v", got, err)
	}
}

func TestSessionGroupSelectedAccountRemainsPersonallyOwned(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	for _, id := range []string{"selected-a", "selected-b", "different-a", "different-b"} {
		groupEndFixture(t, s, id, base)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET login_email=CASE WHEN session_id LIKE 'selected-%' THEN 'selected@example.com' ELSE 'different@example.com' END`); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{"selected-a", "selected-b", "different-a", "different-b"})
	f := SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner", LoginEmail: "selected@example.com"}
	got, err := s.GetSessionGroups(ctx, f)
	if err != nil || got.Counts.Ended != 2 || got.OverviewTotal != 2 || got.Ended[0].Total != 2 {
		t.Fatalf("selected account: %+v %v", got, err)
	}
	key := got.Ended[0].Key
	f.PersonalUserID = "uid-other"
	if _, err := s.GetSessionGroupMembers(ctx, f, key, "", 100); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("account filter granted ownership: %v", err)
	}
	f.PersonalUserID = "uid-owner"
	f.LoginEmail = "different@example.com"
	if _, err := s.GetSessionGroupMembers(ctx, f, key, "", 100); !errors.Is(err, ErrPersonalNotFound) {
		t.Fatalf("key bypassed selected account: %v", err)
	}
}

func TestSessionGroupsDeadlineIsError(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	blocker, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `LOCK TABLE session_overview_rollups IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	got, err := s.GetSessionGroups(deadline, SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner"})
	if err == nil || got != nil || !errors.Is(deadline.Err(), context.DeadlineExceeded) {
		t.Fatalf("deadline must be an error, not partial success: %+v %v", got, err)
	}
}

func TestSessionGroupDismissalsPruneOrphanAndDeleteSession(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	groupEndFixture(t, s, "a", base)
	groupEndFixture(t, s, "b", base.Add(-30*time.Second))
	f := SessionOverviewFilter{DashboardUserID: owner, UserID: "uid-owner", PersonalUserID: "uid-owner"}
	item := SessionGroupDismissal{SessionID: "a", EndedAt: base}
	if err := s.UpdateSessionGroupDismissals(ctx, f, []SessionGroupDismissal{item}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM session_observation_events WHERE session_id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrunePersonalState(ctx); err != nil {
		t.Fatalf("prune after observation retention: %v", err)
	}
	if n := countWhere(t, s, "session_group_dismissals", "session_id='a'"); n != 0 {
		t.Fatalf("orphan preference survives retention: %d", n)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_observation_events
		(session_id,user_id,profile_email,event_id,kind,observed_at,identity_provenance)
		VALUES('a','uid-owner','owner@example.com','again','session_end',$1,'personal_token')`, base); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSessionGroupDismissals(ctx, f, []SessionGroupDismissal{item}, true); err != nil {
		t.Fatalf("recreate preference: %v", err)
	}
	if _, err := s.DeleteSession(ctx, "a", "admin", "erase", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sweepOne(ctx, "a"); err != nil {
		t.Fatalf("session sweep: %v", err)
	}
	if n := countWhere(t, s, "session_group_dismissals", "session_id='a'"); n != 0 {
		t.Fatalf("session sweep kept private preference: %d", n)
	}
}
