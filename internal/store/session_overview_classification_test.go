package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSessionOverviewClassificationPopulation(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	owner := personalFixture(t, s, "owner@example.com", "uid-owner")
	otherDashboard := personalFixture(t, s, "other@example.com", "uid-other")
	for _, sid := range []string{"working", "unsorted", "empty", "other"} {
		uid, email := "uid-owner", "owner@example.com"
		if sid == "other" {
			uid, email = "uid-other", "other@example.com"
		}
		personalSource(t, s, sid, email, uid)
	}
	class := "working"
	if _, err := s.PutPersonalSession(ctx, owner, "working", "", "uid-owner", &class, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutPersonalSession(ctx, otherDashboard, "other", "", "uid-other", &class, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET forked_from_session='working' WHERE session_id='unsorted'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET project_hash='project-alpha',agent='codex' WHERE session_id='working'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_records SET source_file='empty.jsonl',prompt_source='',entrypoint='sdk-cli' WHERE session_id='empty'`); err != nil {
		t.Fatal(err)
	}
	refreshOverviewRollupsForTest(t, s, []string{"unsorted", "working", "empty"})
	for _, sid := range []string{"expired", "rebound", "mixed"} {
		personalSource(t, s, sid, "owner@example.com", "uid-owner")
		if _, err := s.PutPersonalSession(ctx, owner, sid, "", "uid-owner", &class, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	// Container/host clock corrections can move backwards by seconds during this
	// test. Use an unambiguously expired instant so that fixture cannot become valid.
	if _, err := s.pool.Exec(ctx, `UPDATE session_personal_state SET expires_at='epoch'::timestamptz WHERE session_id='expired'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE session_personal_state SET source_user_id='uid-other' WHERE session_id='rebound'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO session_records(session_id,record_type,user_id,profile_email) VALUES ('mixed','user','uid-other','other@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestSessionObservations(ctx, SessionObservationBatch{Snapshots: []*SessionSnapshot{{
		SessionID: "observation-only", UserID: "uid-owner", ProfileEmail: "owner@example.com",
		IdentityProvenance: "personal_token", ObservedAt: time.Now(), Snapshot: json.RawMessage(`{}`),
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutPersonalSession(ctx, owner, "observation-only", "", "uid-owner", &class, nil, 0); err != nil {
		t.Fatal(err)
	}
	personalCounts, err := s.PersonalSessionCounts(ctx, owner, "", "uid-owner", PersonalCountFilter{})
	if err != nil || personalCounts.Counts["working"] != 2 {
		t.Fatalf("observation-only present in personal counts: %+v, %v", personalCounts, err)
	}
	filters := []SessionOverviewFilter{
		{UserID: "uid-owner", DashboardUserID: owner, PersonalUserID: "uid-owner", Limit: 1},
		{DashboardUserID: owner, PersonalUserID: "uid-owner", Limit: 1},
	}
	since := time.Now().Add(-time.Hour)
	until := time.Now().Add(time.Hour)
	for _, base := range filters {
		for _, period := range []bool{false, true} {
			f := base
			if period {
				f.Since, f.Until = &since, &until
			}
			all := f
			all.Limit = 100
			allRows, err := s.ListSessionOverviews(ctx, all)
			wantAll := 7
			if base.UserID != "" {
				wantAll = 6
			}
			if err != nil || len(allRows) != wantAll {
				t.Fatalf("overview population period=%t scope=%+v: %d rows, %v", period, base, len(allRows), err)
			}
			for _, row := range allRows {
				if row.SessionID == "observation-only" {
					t.Fatalf("observation-only entered overview list period=%t scope=%+v", period, base)
				}
			}
			allCount, err := s.CountSessionOverviews(ctx, all)
			if err != nil || allCount != wantAll {
				t.Fatalf("overview count period=%t scope=%+v: %d, %v", period, base, allCount, err)
			}
			buckets, err := s.SessionOverviewClassificationCounts(ctx, f)
			if err != nil || buckets["working"] != 1 || buckets["unsorted"] != 2 || buckets["done"] != 0 {
				t.Fatalf("buckets period=%t scope=%+v: %+v, %v", period, base, buckets, err)
			}
			f.Classification = "unsorted"
			count, err := s.CountSessionOverviews(ctx, f)
			if err != nil || count != 2 {
				t.Fatalf("unsorted count period=%t: %d, %v", period, count, err)
			}
			rows, err := s.ListSessionOverviews(ctx, f)
			if err != nil || len(rows) != 1 || rows[0].SessionID != "empty" {
				ids := make([]string, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.SessionID)
				}
				t.Fatalf("unsorted page period=%t: %v, %v", period, ids, err)
			}
			f.Offset = 1
			rows, err = s.ListSessionOverviews(ctx, f)
			if err != nil || len(rows) != 1 || rows[0].SessionID != "unsorted" {
				t.Fatalf("unsorted second page period=%t: %+v, %v", period, rows, err)
			}
			f.Classification = "working"
			f.Offset = 0
			count, err = s.CountSessionOverviews(ctx, f)
			if err != nil || count != 1 {
				t.Fatalf("working count period=%t: %d, %v", period, count, err)
			}
			assembled := f
			assembled.FoldLineage = true
			assembled.Classification = ""
			assembledBuckets, err := s.SessionOverviewClassificationCounts(ctx, assembled)
			if err != nil || assembledBuckets["working"] != 1 || assembledBuckets["unsorted"] != 1 {
				t.Fatalf("assembled buckets period=%t scope=%+v: %+v, %v", period, base, assembledBuckets, err)
			}
			assembled.Classification = "unsorted"
			assembledCount, err := s.CountSessionOverviews(ctx, assembled)
			if err != nil || assembledCount != 1 {
				t.Fatalf("assembled count period=%t scope=%+v: %d, %v", period, base, assembledCount, err)
			}
			for _, test := range []struct {
				name     string
				filter   SessionOverviewFilter
				working  int
				unsorted int
			}{
				{"project", SessionOverviewFilter{ProjectHashes: []string{"project-alpha"}}, 1, 0},
				{"codex", SessionOverviewFilter{Agent: "codex"}, 1, 0},
				{"headless", SessionOverviewFilter{Source: "headless"}, 0, 1},
				{"interactive", SessionOverviewFilter{Source: "interactive"}, 1, 1},
			} {
				scoped := f
				scoped.Classification = ""
				scoped.ProjectHashes, scoped.Agent, scoped.Source = test.filter.ProjectHashes, test.filter.Agent, test.filter.Source
				scopedBuckets, err := s.SessionOverviewClassificationCounts(ctx, scoped)
				if err != nil || scopedBuckets["working"] != test.working || scopedBuckets["unsorted"] != test.unsorted {
					t.Fatalf("%s buckets period=%t scope=%+v: %+v, %v", test.name, period, base, scopedBuckets, err)
				}
				for classification, want := range map[string]int{"working": test.working, "unsorted": test.unsorted} {
					scoped.Classification = classification
					got, err := s.CountSessionOverviews(ctx, scoped)
					if err != nil || got != want {
						t.Fatalf("%s %s count period=%t scope=%+v: %d, %v", test.name, classification, period, base, got, err)
					}
				}
			}
			f.Classification = "invalid"
			if _, err := s.CountSessionOverviews(ctx, f); !errors.Is(err, ErrPersonalInvalid) {
				t.Fatalf("invalid count classification: %v", err)
			}
			if _, err := s.ListSessionOverviews(ctx, f); !errors.Is(err, ErrPersonalInvalid) {
				t.Fatalf("invalid list classification: %v", err)
			}
		}
	}
}
