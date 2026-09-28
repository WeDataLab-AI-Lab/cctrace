package aireport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/airuntime"
	"cctrace/internal/store"
)

func tickFixture(t *testing.T) *fixture {
	t.Helper()
	rt := readyRuntime([]airuntime.FakeStep{
		{CallTool: "read_segment", Args: json.RawMessage(`{"segment_id":"10"}`)},
	}, goodOutput)
	return newFixture(t, rt)
}

// The server hands the scheduler the same scope a request would, so an
// automatic run reads exactly the segments the user's own run reads.
func scopeOf(sc Scope) func(store.AIScheduleCandidate) Scope {
	return func(store.AIScheduleCandidate) Scope { return sc }
}

func onlyRun(t *testing.T, st *MemStore) *store.AIReportRun {
	t.Helper()
	var found *store.AIReportRun
	for _, r := range st.Runs {
		if found != nil {
			t.Fatalf("more than one run: %+v and %+v", found, r)
		}
		found = r
	}
	if found == nil {
		t.Fatal("no run was started")
	}
	return found
}

func candidate(tz string) store.AIScheduleCandidate {
	return store.AIScheduleCandidate{
		DashboardUserID: 1, Email: "me@example.test", CctraceUserID: "me", FallbackTZ: tz,
	}
}

// firingOf is a moment just after the schedule fires for wk, so the tick is
// asked the same question a real minute-ticker would ask right then.
func firingOf(t *testing.T, sch ResolvedSchedule, wk Week) time.Time {
	t.Helper()
	at, err := sch.FireTime(wk)
	if err != nil {
		t.Fatal(err)
	}
	return at.Add(time.Minute)
}

// A firing covers the week that has ended, and the run it starts is marked as
// the scheduler's own so a later tick can tell it apart from a manual one.
func TestScheduleTickStartsTheWeekThatEnded(t *testing.T) {
	f := tickFixture(t)
	f.st.AutoSchedule = &store.AIAutoSchedule{Enabled: boolPtr(true)}
	f.st.Candidates = []store.AIScheduleCandidate{candidate(f.wk.TZ)}
	now := firingOf(t, ResolveSchedule(f.st.AutoSchedule, nil, f.wk.TZ), f.wk)

	started, err := f.svc.ScheduleTick(context.Background(), now, scopeOf(f.sc))
	if err != nil {
		t.Fatalf("ScheduleTick: %v", err)
	}
	if started != 1 {
		t.Fatalf("started = %d, want 1", started)
	}

	run := onlyRun(t, f.st)
	if run.Week != f.wk.ID {
		t.Fatalf("run week = %q, want %q", run.Week, f.wk.ID)
	}
	if run.StartedBy != store.AIRunStartedBySchedule {
		t.Fatalf("StartedBy = %q, want %q", run.StartedBy, store.AIRunStartedBySchedule)
	}
	waitRun(t, f.st, run.ID, store.AIRunCompleted)
}

// The ticker runs every minute; the week's firing must produce one run, not one
// per tick. The gate is the marker in the store, so it survives a restart.
func TestScheduleTickFiresOncePerWeek(t *testing.T) {
	f := tickFixture(t)
	f.st.AutoSchedule = &store.AIAutoSchedule{Enabled: boolPtr(true)}
	f.st.Candidates = []store.AIScheduleCandidate{candidate(f.wk.TZ)}
	now := firingOf(t, ResolveSchedule(f.st.AutoSchedule, nil, f.wk.TZ), f.wk)
	ctx := context.Background()

	started, err := f.svc.ScheduleTick(ctx, now, scopeOf(f.sc))
	if err != nil || started != 1 {
		t.Fatalf("first tick = %d, %v", started, err)
	}
	// Let it finish, so the second tick is refused by the marker rather than by
	// the one-running-per-user rule.
	waitRun(t, f.st, onlyRun(t, f.st).ID, store.AIRunCompleted)

	started, err = f.svc.ScheduleTick(ctx, now.Add(time.Minute), scopeOf(f.sc))
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if started != 0 {
		t.Fatalf("second tick started %d runs", started)
	}
	onlyRun(t, f.st) // still exactly one
}

// Off is off: an admin turns automatic runs on, and a user can turn their own
// back off without the admin's default reaching them.
func TestScheduleTickRespectsTheSwitch(t *testing.T) {
	tests := []struct {
		name  string
		admin *store.AIAutoSchedule
		user  *store.AIUserSchedule
	}{
		{"no admin word leaves it off", nil, nil},
		{"the admin turned it off", &store.AIAutoSchedule{Enabled: boolPtr(false)}, nil},
		{"the user turned their own off",
			&store.AIAutoSchedule{Enabled: boolPtr(true)},
			&store.AIUserSchedule{DashboardUserID: 1, Enabled: boolPtr(false)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tickFixture(t)
			f.st.AutoSchedule = tt.admin
			c := candidate(f.wk.TZ)
			c.Schedule = tt.user
			f.st.Candidates = []store.AIScheduleCandidate{c}
			now := firingOf(t, ResolveSchedule(&store.AIAutoSchedule{Enabled: boolPtr(true)}, nil, f.wk.TZ), f.wk)

			started, err := f.svc.ScheduleTick(context.Background(), now, scopeOf(f.sc))
			if err != nil {
				t.Fatalf("ScheduleTick: %v", err)
			}
			if started != 0 {
				t.Fatalf("started = %d, want 0", started)
			}
			if len(f.st.Runs) != 0 {
				t.Fatalf("runs = %d, want none", len(f.st.Runs))
			}
		})
	}
}

// A user who moved their own time runs at that time, not at the admin's. At
// the admin's firing this user is owed an earlier week, which has nothing in
// it; an empty week is skipped rather than turned into a weekly failed run.
func TestScheduleTickFollowsTheUsersOwnTime(t *testing.T) {
	f := tickFixture(t)
	f.st.AutoSchedule = &store.AIAutoSchedule{Enabled: boolPtr(true)}
	// The admin fires Monday 06:00; this user moved it to Friday 09:00.
	user := &store.AIUserSchedule{DashboardUserID: 1, Weekday: intPtr(int(time.Friday)), Hour: intPtr(9)}
	c := candidate(f.wk.TZ)
	c.Schedule = user
	f.st.Candidates = []store.AIScheduleCandidate{c}
	ctx := context.Background()

	adminFiring := firingOf(t, ResolveSchedule(f.st.AutoSchedule, nil, f.wk.TZ), f.wk)
	started, err := f.svc.ScheduleTick(ctx, adminFiring, scopeOf(f.sc))
	if err != nil {
		t.Fatalf("ScheduleTick at the admin's firing: %v", err)
	}
	if started != 0 || len(f.st.Runs) != 0 {
		t.Fatalf("started = %d, runs = %d at the admin's firing", started, len(f.st.Runs))
	}

	// Their own firing for that week: now the week they are owed is the one with
	// segments in it.
	ownFiring := firingOf(t, ResolveSchedule(f.st.AutoSchedule, user, f.wk.TZ), f.wk)
	started, err = f.svc.ScheduleTick(ctx, ownFiring, scopeOf(f.sc))
	if err != nil {
		t.Fatalf("ScheduleTick at the user's firing: %v", err)
	}
	if started != 1 {
		t.Fatalf("started = %d at the user's own firing, want 1", started)
	}
	if run := onlyRun(t, f.st); run.Week != f.wk.ID {
		t.Fatalf("run week = %q, want %q", run.Week, f.wk.ID)
	}
}
