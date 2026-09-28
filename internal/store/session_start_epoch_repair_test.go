package store

import (
	"context"
	"testing"
	"time"
)

func TestRepairEpochSessionOverviewStarts_rebuildsWrongRowsOnce(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schema_backfills WHERE name = $1`, sessionOverviewEpochStartRepair); err != nil {
		t.Fatalf("clear repair marker: %v", err)
	}
	ts := time.Date(2026, 9, 2, 11, 0, 0, 0, time.UTC)
	epoch := time.Unix(0, 0).UTC()
	email := "repair@example.test"

	if err := s.InsertSessionRecords(ctx, []*SessionRecord{
		{Ts: epoch, SessionID: "stale", ProfileEmail: email, RecordType: "last-prompt", UUID: "content-stale", Raw: []byte(`{}`)},
		{Ts: ts, SessionID: "stale", ProfileEmail: email, RecordType: "user", UUID: "s1", Raw: []byte(`{}`)},
		{Ts: ts, SessionID: "healthy", ProfileEmail: email, RecordType: "user", UUID: "h1", Raw: []byte(`{}`)},
	}); err != nil {
		t.Fatalf("InsertSessionRecords: %v", err)
	}

	// A mark only a rebuild would erase, on a session the repair must not reach. It is
	// deliberately a value the source does not support: asserting on a correct field
	// would pass whether the row was rebuilt or left alone, and the point here is that
	// the repair stays scoped instead of rebuilding the whole overview.
	if _, err := s.pool.Exec(ctx,
		`UPDATE session_overview_rollups SET model = 'untouched-marker' WHERE session_id = 'healthy'`); err != nil {
		t.Fatalf("mark healthy rollup: %v", err)
	}

	// The rows written before the fix are what the repair exists for, and no code path
	// produces them any more, so the test writes one the way production has it.
	corrupt := func() {
		if _, err := s.pool.Exec(ctx,
			`UPDATE session_overview_rollups SET start_time = 'epoch' WHERE session_id = 'stale'`); err != nil {
			t.Fatalf("corrupt rollup: %v", err)
		}
	}
	corrupt()
	if got := overviewFor(t, s, SessionOverviewFilter{ProfileEmail: email}, "stale"); got == nil || !got.StartTime.Equal(epoch) {
		t.Fatalf("fixture not stale: %+v", got)
	}

	if err := s.RepairEpochSessionOverviewStarts(ctx); err != nil {
		t.Fatalf("RepairEpochSessionOverviewStarts: %v", err)
	}
	if got := overviewFor(t, s, SessionOverviewFilter{ProfileEmail: email}, "stale"); got == nil || !got.StartTime.Equal(ts) {
		t.Fatalf("after repair: %+v, want start %s", got, ts)
	}
	var marked bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_backfills WHERE name = $1)`, sessionOverviewEpochStartRepair).Scan(&marked); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !marked {
		t.Fatal("repair did not mark itself done")
	}
	if got := overviewFor(t, s, SessionOverviewFilter{ProfileEmail: email}, "healthy"); got == nil || got.Model != "untouched-marker" {
		t.Fatalf("repair rebuilt a session it had no reason to touch: %+v", got)
	}

	// A one-time repair that runs again on every boot would rebuild the whole affected
	// set forever, so the marker has to stop it.
	corrupt()
	if err := s.RepairEpochSessionOverviewStarts(ctx); err != nil {
		t.Fatalf("second RepairEpochSessionOverviewStarts: %v", err)
	}
	if got := overviewFor(t, s, SessionOverviewFilter{ProfileEmail: email}, "stale"); got == nil || !got.StartTime.Equal(epoch) {
		t.Fatalf("repair repeated after its marker: %+v", got)
	}
}
