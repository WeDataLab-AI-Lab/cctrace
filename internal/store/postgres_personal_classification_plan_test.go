package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These tests share the package's truncating fixture store and deliberately do
// not run in parallel with its other DB tests.
func TestPersonalClassificationOnlyProbesOwnerScopeCandidates(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	personalSource(t, s, "owned-unsaved", "owner@example.com", "uid-owner")
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,uuid,record_type,user_id,profile_email,ts,raw)
		SELECT 'foreign-'||n,'foreign-'||n,'user','uid-other','other@example.com',now(),'{}'::jsonb
		FROM generate_series(1,32) n;
		UPDATE session_records SET login_email='selected-account@example.com'
		WHERE session_id IN ('owned-unsaved','foreign-2')`); err != nil {
		t.Fatal(err)
	}
	ids := []string{"owned-unsaved"}
	for n := 1; n <= 32; n++ {
		ids = append(ids, fmt.Sprintf("foreign-%d", n))
	}
	refreshOverviewRollupsForTest(t, s, ids)
	// A stale/forged metadata scope still cannot prove raw ownership. It may
	// survive the cheap scope filter, but the existing raw/source proof must veto it.
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_overview_rollups
		(scope_type,scope_value,session_id,start_time,end_time,has_sync)
		VALUES('user','uid-owner','foreign-1',now(),now(),true),
		('profile','owner@example.com','foreign-1',now(),now(),true)`); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []SessionOverviewFilter{
		{DashboardUserID: owner, PersonalUserID: "uid-owner"},
		{DashboardUserID: owner, PersonalEmail: "owner@example.com"},
	} {
		query, args := sessionOverviewRollupPersonalQuery(identity, true)
		var raw json.RawMessage
		if err := s.pool.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+query, args...).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plan []struct{ Plan map[string]interface{} }
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		var inspect func(map[string]interface{}, bool)
		proofFound := false
		inspect = func(node map[string]interface{}, candidate bool) {
			candidate = candidate || node["Subplan Name"] == "CTE personal_candidate"
			name, _ := node["Subplan Name"].(string)
			if candidate && strings.HasPrefix(name, "SubPlan ") {
				proofFound = true
				if loops := node["Actual Loops"].(float64); loops > 2 {
					t.Errorf("raw owner proof ran %.0f times for 2 owner-scope candidates; unrelated all-scope sessions must be eliminated first", loops)
				}
			}
			children, _ := node["Plans"].([]interface{})
			for _, child := range children {
				inspect(child.(map[string]interface{}), candidate)
			}
		}
		inspect(plan[0].Plan, false)
		if !proofFound {
			t.Fatal("raw ownership probe plan was not inspected")
		}
		counts, err := s.SessionOverviewClassificationCounts(ctx, identity)
		if err != nil || counts["unsorted"] != 1 {
			t.Fatalf("unsaved owned session or forged-scope veto changed: counts=%v error=%v", counts, err)
		}
		count, err := s.CountSessionOverviews(ctx, identity)
		if err != nil || count != 33 {
			t.Fatalf("all-scope count narrowed to personal ownership: count=%d error=%v", count, err)
		}
		identity.LoginEmail = "selected-account@example.com"
		counts, err = s.SessionOverviewClassificationCounts(ctx, identity)
		if err != nil || counts["unsorted"] != 1 {
			t.Fatalf("account scope lost the owned unsaved session: counts=%v error=%v", counts, err)
		}
		count, err = s.CountSessionOverviews(ctx, identity)
		if err != nil || count != 2 {
			t.Fatalf("account count narrowed to personal ownership: count=%d error=%v", count, err)
		}
	}
}

func TestPersonalClassificationSkipsSourceProofWithoutMatchingRecord(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	personalSource(t, s, "unrelated", "other@example.com", "uid-other")
	for _, identity := range []SessionOverviewFilter{
		{DashboardUserID: owner, PersonalUserID: "uid-owner"},
		{DashboardUserID: owner, PersonalEmail: "owner@example.com"},
	} {
		query, args := sessionOverviewRollupPersonalQuery(identity, true)
		var raw json.RawMessage
		if err := s.pool.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+query, args...).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plan []struct{ Plan map[string]interface{} }
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		var check func(map[string]interface{})
		basisFound := false
		check = func(node map[string]interface{}) {
			if node["Subplan Name"] == "CTE source_basis" {
				basisFound = true
				if node["Actual Rows"].(float64) != 0 {
					t.Errorf("unmatched caller scanned %v source-basis rows; raw ownership proof must narrow candidates first", node["Actual Rows"])
				}
			}
			children, _ := node["Plans"].([]interface{})
			for _, child := range children {
				check(child.(map[string]interface{}))
			}
		}
		check(plan[0].Plan)
		if !basisFound {
			t.Fatal("source-basis plan was not inspected")
		}
		counts, err := s.SessionOverviewClassificationCounts(ctx, identity)
		if err != nil {
			t.Fatal(err)
		}
		for class, count := range counts {
			if count != 0 {
				t.Errorf("unmatched caller %s count=%d, want zero", class, count)
			}
		}
	}
}

func TestPersonalClassificationRetainsAllSourceVetoes(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	for _, sid := range []string{"valid", "mixed-user", "mixed-email", "hidden-login", "hidden-account", "hidden-link", "hidden-project", "expired", "rebound", "excluded", "deleted"} {
		personalSource(t, s, sid, "owner@example.com", "uid-owner")
		class := "working"
		if _, err := s.PutPersonalSession(ctx, owner, sid, "", "uid-owner", &class, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	// Each veto is on a different source than the raw row that seeded ownership.
	// A shared-key claim cannot prove ownership, but still must be able to veto it.
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_snapshots(session_id,user_id,profile_email,login_email,billing_provider,account_id,project_hash,agent,observed_at,snapshot,identity_provenance) VALUES
		('mixed-user','uid-other','owner@example.com','','','','','claude',now(),'{}','shared_key_claim'),
		('mixed-email','uid-owner','other@example.com','','','','','claude',now(),'{}','personal_token'),
		('hidden-login','uid-owner','owner@example.com','hidden@example.com','','','','claude',now(),'{}','personal_token');
		INSERT INTO excluded_accounts(login_email) VALUES('hidden@example.com');
		INSERT INTO session_live_states(session_id,user_id,profile_email,billing_provider,account_id,agent,pid,proc_start,status,heartbeat_at,identity_provenance) VALUES
		('hidden-account','uid-owner','owner@example.com','fixture','hidden-account','claude',1,'fixture','idle',now(),'personal_token');
		INSERT INTO excluded_billing_accounts(billing_provider,account_id) VALUES('fixture','hidden-account');
		INSERT INTO session_observation_events(session_id,user_id,profile_email,billing_provider,account_id,project_hash,agent,event_id,kind,observed_at,identity_provenance) VALUES
		('hidden-link','uid-owner','owner@example.com','fixture','hidden-link','','claude','hidden-link','state_change',now(),'shared_key_claim'),
		('hidden-project','uid-owner','owner@example.com','','','hidden-project','claude','hidden-project','state_change',now(),'personal_token');
		INSERT INTO excluded_billing_links(billing_provider,account_id,login_email) VALUES('fixture','hidden-link','hidden@example.com');
		INSERT INTO blocked_projects(project_hash) VALUES('hidden-project');
		UPDATE session_personal_state SET expires_at='epoch' WHERE session_id='expired';
		UPDATE session_personal_state SET source_user_id='uid-other' WHERE session_id='rebound';
		INSERT INTO excluded_sessions(session_id) VALUES('excluded');
		INSERT INTO deleted_sessions(session_id) VALUES('deleted')`); err != nil {
		t.Fatal(err)
	}
	since, until := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	want := map[string]int{"working": 1, "unsorted": 0, "consulting": 0, "later": 0, "done": 0}
	for _, identity := range []SessionOverviewFilter{
		{DashboardUserID: owner, PersonalUserID: "uid-owner"},
		{DashboardUserID: owner, PersonalEmail: "owner@example.com"},
		// User ID mode must not fall back to a matching email.
		{DashboardUserID: owner, PersonalUserID: "uid-other", PersonalEmail: "owner@example.com"},
	} {
		for _, period := range []bool{false, true} {
			f := identity
			if period {
				f.Since, f.Until = &since, &until
			}
			counts, err := s.SessionOverviewClassificationCounts(ctx, f)
			expected := want
			if identity.PersonalUserID == "uid-other" {
				expected = map[string]int{"working": 0, "unsorted": 0, "consulting": 0, "later": 0, "done": 0}
			}
			if err != nil || !reflect.DeepEqual(counts, expected) {
				t.Fatalf("all-source veto period=%t identity=%+v counts=%v, want %v: %v", period, identity, counts, expected, err)
			}
		}
	}
}
