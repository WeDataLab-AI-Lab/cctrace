package api

import (
	"context"
	"testing"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/auth"
	"cctrace/internal/store"
)

// firingAfter is a moment just past the admin's default firing for wk, which is
// when a minute ticker would first ask.
func firingAfter(t *testing.T, wk aireport.Week, admin *store.AIAutoSchedule) time.Time {
	t.Helper()
	at, err := aireport.ResolveSchedule(admin, nil, wk.TZ).FireTime(wk)
	if err != nil {
		t.Fatal(err)
	}
	return at.Add(time.Minute)
}

// An automatic run must read exactly what the user's own run reads. The server
// owns that rule, so the tick goes through it rather than rebuilding a scope.
func TestRunAIReportScheduleStartsWithTheCallerScope(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	a.consent(t, aiCaller.ID)
	a.st.AddSegment("caller", store.AISegment{ID: 10, SessionID: "sess", StartTs: a.wk.Since.Add(time.Hour), Agent: "claude", ProjectName: "cctrace"})

	on := true
	admin := &store.AIAutoSchedule{Enabled: &on}
	a.st.AutoSchedule = admin
	a.st.Candidates = []store.AIScheduleCandidate{{
		DashboardUserID: aiCaller.ID, Email: aiCaller.Email,
		CctraceUserID: aiCaller.CctraceUserID, FallbackTZ: a.wk.TZ,
	}}

	started, err := a.srv.RunAIReportSchedule(context.Background(), firingAfter(t, a.wk, admin))
	if err != nil {
		t.Fatalf("RunAIReportSchedule: %v", err)
	}
	if started != 1 {
		t.Fatalf("started = %d, want 1", started)
	}

	var run *store.AIReportRun
	for _, r := range a.st.Runs {
		run = r
	}
	if run == nil {
		t.Fatal("no run was started")
	}
	if run.Week != a.wk.ID {
		t.Fatalf("run week = %q, want %q", run.Week, a.wk.ID)
	}
	if run.StartedBy != store.AIRunStartedBySchedule {
		t.Fatalf("StartedBy = %q, want %q", run.StartedBy, store.AIRunStartedBySchedule)
	}
	if run.ScopeUserID != aiCaller.CctraceUserID {
		t.Fatalf("scope user id = %q, want %q", run.ScopeUserID, aiCaller.CctraceUserID)
	}
}

// The scope the scheduler builds must match resolveUserAccessParams branch for
// branch. If it drifts, an automatic report quietly covers a different set of
// sessions than the one the user sees when they press the button.
func TestScheduleScopeMatchesRequestScope(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	tests := []struct {
		name         string
		userIDAccess bool
		user         *auth.DashboardUser
	}{
		{"scoped by profile email", false, &auth.DashboardUser{ID: 7, Email: "a@example.com", CctraceUserID: "u7"}},
		{"scoped by cctrace user id", true, &auth.DashboardUser{ID: 7, Email: "a@example.com", CctraceUserID: "u7"}},
		{"no cctrace id matches nothing", true, &auth.DashboardUser{ID: 7, Email: "a@example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.srv.useUserIDAccessControl = tt.userIDAccess
			profileEmail, _, userID := a.srv.resolveUserAccessParams(tt.user)

			got := a.srv.scheduleScope(store.AIScheduleCandidate{
				DashboardUserID: tt.user.ID, Email: tt.user.Email, CctraceUserID: tt.user.CctraceUserID,
			})
			want := aireport.Scope{DashboardUserID: tt.user.ID, ProfileEmail: profileEmail, UserID: userID}
			if got != want {
				t.Fatalf("scheduleScope = %+v, want %+v", got, want)
			}
		})
	}
}

// A deployment with no AI runtime still runs the ticker; it must do nothing
// rather than panic on a nil service.
func TestRunAIReportScheduleWithoutServiceIsNoop(t *testing.T) {
	a := newAITest(t, aiRuntime(), aiCaller)
	a.srv.aiReports = nil

	started, err := a.srv.RunAIReportSchedule(context.Background(), time.Now())
	if err != nil || started != 0 {
		t.Fatalf("started = %d, %v", started, err)
	}
}
