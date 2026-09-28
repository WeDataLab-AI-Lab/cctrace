package aireport

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

// defaultTZFixture is tickFixture on a service whose deployment names a default
// zone, the one a user with nothing saved and nothing recorded is read in.
func defaultTZFixture(t *testing.T, tz string) *fixture {
	t.Helper()
	f := tickFixture(t)
	rt := readyRuntime([]airuntime.FakeStep{
		{CallTool: "read_segment", Args: []byte(`{"segment_id":"10"}`)},
	}, goodOutput)
	svc := NewService(f.st, []airuntime.Runtime{rt}, Config{EnvRuntime: DefaultRuntimeKey, DefaultTZ: tz})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		svc.Shutdown(ctx)
	})
	f.svc = svc
	return f
}

// On a fresh deployment nobody has saved a zone or run a report. Reading the
// admin's "Monday 06:00" in UTC fired it at 15:00 in Seoul, where everyone
// using the deployment was; the deployment's default zone stands in instead.
func TestUserScheduleFallsBackToTheDefaultZone(t *testing.T) {
	f := defaultTZFixture(t, "Asia/Seoul")

	got, err := f.svc.UserSchedule(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("UserSchedule: %v", err)
	}
	if got.TZ != "Asia/Seoul" {
		t.Fatalf("TZ = %q, want the deployment's default", got.TZ)
	}
}

// The default is the last resort: a zone the user saved, and failing that the
// zone their last report was made in, still decide.
func TestUserScheduleDefaultZoneKeepsLowestPriority(t *testing.T) {
	ctx := context.Background()

	t.Run("saved zone", func(t *testing.T) {
		f := defaultTZFixture(t, "Asia/Seoul")
		if _, err := f.svc.SetUserSchedule(ctx, 1, nil, nil, nil, nil, "America/New_York"); err != nil {
			t.Fatal(err)
		}
		got, err := f.svc.UserSchedule(ctx, 1, "")
		if err != nil || got.TZ != "America/New_York" {
			t.Fatalf("TZ = %q, %v; want the saved zone", got.TZ, err)
		}
	})

	t.Run("last report's zone", func(t *testing.T) {
		f := defaultTZFixture(t, "Asia/Seoul")
		f.st.Reports[memKey(1, f.wk.ID)] = &store.AIReport{
			DashboardUserID: 1, Week: f.wk.ID, TZ: "America/New_York",
			Since: f.wk.Since, Until: f.wk.Until, GeneratedAt: f.wk.Since,
		}
		got, err := f.svc.UserSchedule(ctx, 1, "")
		if err != nil || got.TZ != "America/New_York" {
			t.Fatalf("TZ = %q, %v; want the recorded zone", got.TZ, err)
		}
	})
}

// Clearing one's own zone hands it back to the fallback, and the answer the
// screen gets must be the zone the ticker will then fire in -- not UTC.
func TestSetUserScheduleWithoutZoneAnswersTheDefault(t *testing.T) {
	f := defaultTZFixture(t, "Asia/Seoul")

	got, err := f.svc.SetUserSchedule(context.Background(), 1, nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("SetUserSchedule: %v", err)
	}
	if got.TZ != "Asia/Seoul" {
		t.Fatalf("TZ = %q, want the deployment's default", got.TZ)
	}
}

// The admin's screen says which zone its weekday and time are read in for
// everyone who has not chosen one.
func TestAutoScheduleShowsTheDefaultZone(t *testing.T) {
	f := defaultTZFixture(t, "Asia/Seoul")

	got, err := f.svc.AutoSchedule(context.Background())
	if err != nil {
		t.Fatalf("AutoSchedule: %v", err)
	}
	if got.TZ != "Asia/Seoul" {
		t.Fatalf("TZ = %q, want the deployment's default", got.TZ)
	}
}

// seoulFiring is just after Monday 06:00 in Seoul closing the fixture's week,
// which is Sunday 21:00 UTC -- before that week's firing in UTC.
func seoulFiring(t *testing.T, f *fixture) time.Time {
	t.Helper()
	wk, err := ParseISOWeek(f.wk.ID, "Asia/Seoul")
	if err != nil {
		t.Fatal(err)
	}
	return firingOf(t, ResolveSchedule(f.st.AutoSchedule, nil, "Asia/Seoul"), wk)
}

// The ticker reads the same default. At the Seoul firing the UTC reading still
// owes the week before -- which has nothing in it, so no run would start.
func TestScheduleTickFiresInTheDefaultZone(t *testing.T) {
	f := defaultTZFixture(t, "Asia/Seoul")
	f.st.AutoSchedule = &store.AIAutoSchedule{Enabled: boolPtr(true)}
	f.st.Candidates = []store.AIScheduleCandidate{candidate("")}
	now := seoulFiring(t, f)

	started, err := f.svc.ScheduleTick(context.Background(), now, scopeOf(f.sc))
	if err != nil {
		t.Fatalf("ScheduleTick: %v", err)
	}
	if started != 1 {
		t.Fatalf("started = %d at the Seoul firing, want 1", started)
	}
	run := onlyRun(t, f.st)
	if run.Week != f.wk.ID {
		t.Fatalf("run week = %q, want %q", run.Week, f.wk.ID)
	}
	waitRun(t, f.st, run.ID, store.AIRunCompleted)
}

// A candidate whose last report carries a zone keeps firing in it.
func TestScheduleTickRecordedZoneBeatsTheDefault(t *testing.T) {
	f := defaultTZFixture(t, "Asia/Seoul")
	f.st.AutoSchedule = &store.AIAutoSchedule{Enabled: boolPtr(true)}
	f.st.Candidates = []store.AIScheduleCandidate{candidate("UTC")}
	started, err := f.svc.ScheduleTick(context.Background(), seoulFiring(t, f), scopeOf(f.sc))
	if err != nil {
		t.Fatalf("ScheduleTick: %v", err)
	}
	if started != 0 {
		t.Fatalf("started = %d at the Seoul firing for a UTC user, want 0", started)
	}
}
