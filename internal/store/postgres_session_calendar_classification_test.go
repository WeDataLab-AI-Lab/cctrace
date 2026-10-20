package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCalendarEmptyClassificationSkipsActivityAndOwnershipScans(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	userID := personalFixture(t, s, "owner@example.com", "owner")
	from := time.Now().UTC().Truncate(time.Second)
	calendarFixture(t, s, "unclassified", "owner", "claude", "full", "observed", from, true)
	personalSource(t, s, "unclassified", "owner@example.com", "owner")
	activityReads := 0
	cfg := s.pool.Config()
	cfg.ConnConfig.Tracer = &calendarSnapshotTrace{afterCandidates: func() { activityReads++ }}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	result, err := (&PgStore{pool: pool}).ReadSessionCalendar(ctx, SessionCalendarFilter{
		From: from, To: from.Add(time.Hour),
		Access:                SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: userID},
		SessionOverviewFilter: SessionOverviewFilter{DashboardUserID: userID, PersonalUserID: "owner", Classification: "working"},
	})
	if err != nil || len(result.Sessions) != 0 {
		t.Fatalf("empty classification: sessions=%d err=%v", len(result.Sessions), err)
	}
	if activityReads != 0 {
		t.Fatal("empty saved classification scanned activity and raw ownership sources")
	}
	result, err = s.ReadSessionCalendar(ctx, SessionCalendarFilter{
		From: from, To: from.Add(time.Hour),
		Access:                SessionAnalysisAccess{User: true, UserID: "owner", DashboardUserID: userID},
		SessionOverviewFilter: SessionOverviewFilter{DashboardUserID: userID, PersonalUserID: "owner", Classification: "unsorted"},
	})
	if err != nil || len(result.Sessions) != 1 || result.Sessions[0].SessionID != "unclassified" {
		t.Fatalf("unsorted must include sessions without a saved classification: sessions=%d err=%v", len(result.Sessions), err)
	}
}
