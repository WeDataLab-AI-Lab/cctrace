package localdaemon

import (
	"path/filepath"
	"testing"
)

// When the routes file cannot be used, a new one is built in which every source
// that only one profile wants is that profile's, and every source that several
// want is nobody's until somebody claims it.
func TestRecoverRoutesLeavesContestedSourcesUnowned(t *testing.T) {
	e := newPlanEnv(t)
	shared, mine, theirs := e.line("codex", "shared"), e.line("claude", "mine"), e.line("claude", "theirs")
	nested := e.line("codex", "shared/sessions/2026")
	idle := e.profile("named:idle", "one", nested)
	idle.Stopped = true
	profiles := []PlanProfile{
		e.profile("default", "one", mine, shared),
		e.profile("named:work", "one", theirs, shared),
		idle,
		e.profile("named:apart", "two", shared),
	}
	f := RecoverRoutes(profiles, testNow, e.newID)
	if err := ValidateRoutes(f, testNow); err != nil {
		t.Fatalf("the recovered file must be usable: %v", err)
	}
	find := func(kind, folder, dest string) Route {
		t.Helper()
		for _, r := range f.Routes {
			if r.Kind == kind && r.Path == filepath.Join(e.root, folder) && r.Destination == testDest(dest) {
				return r
			}
		}
		t.Fatalf("no %s route for %s at %s in %+v", kind, folder, dest, f.Routes)
		return Route{}
	}
	if r := find("claude", "mine", "one"); r.Owner != "default" || r.State != RouteActive {
		t.Fatalf("a source only default wants is default's: %+v", r)
	}
	if r := find("claude", "theirs", "one"); r.Owner != "named:work" || r.State != RouteActive {
		t.Fatalf("a source only work wants is work's: %+v", r)
	}
	if r := find("codex", "shared", "one"); r.Owner != "" || r.State != RouteUnowned || r.From != "" || r.Handoff != "" || r.To != "" {
		t.Fatalf("a source several profiles want, one of them idle, is nobody's: %+v", r)
	}
	if r := find("codex", "shared", "two"); r.Owner != "named:apart" || r.State != RouteActive {
		t.Fatalf("another destination is not contested: %+v", r)
	}
	if len(f.Routes) != 4 || f.Version != routesVersion {
		t.Fatalf("routes %+v", f)
	}
}

// A source that only idle profiles share is contested all the same: when one of
// them can collect again, nobody knows which held it or with whose filters.
func TestRecoverRoutesKeepsASourceIdleProfilesShareUnowned(t *testing.T) {
	e := newPlanEnv(t)
	shared, only := e.line("codex", "shared"), e.line("claude", "only")
	stopped, disabled, alone := e.profile("default", "one", shared), e.profile("named:work", "one", shared), e.profile("named:alone", "one", only)
	stopped.Stopped, disabled.Enabled, alone.Stopped = true, false, true
	f := RecoverRoutes([]PlanProfile{stopped, disabled, alone, e.profile("named:busy", "one", e.line("claude", "busy"))}, testNow, e.newID)
	if err := ValidateRoutes(f, testNow); err != nil {
		t.Fatalf("the recovered file must be usable: %v", err)
	}
	var contested, idle []Route
	for _, r := range f.Routes {
		switch r.Path {
		case filepath.Join(e.root, "shared"):
			contested = append(contested, r)
		case filepath.Join(e.root, "only"):
			idle = append(idle, r)
		}
	}
	if len(contested) != 1 || contested[0].Owner != "" || contested[0].State != RouteUnowned {
		t.Fatalf("a source two idle profiles share is nobody's: %+v", f.Routes)
	}
	if len(idle) != 0 {
		t.Fatalf("a source only one idle profile has is reserved when it can collect, as on a first start: %+v", idle)
	}
	stopped.Stopped = false
	in := e.in(stopped, disabled)
	in.Routes = f
	if res := e.plan(in); !res.Jobs["default"].AllExcluded || res.Jobs["default"].Excluded[0].HolderState != HolderUnowned {
		t.Fatalf("the profile that can collect again waits for a claim: %+v", res.Jobs["default"])
	}
}

// An owner the snapshot named whose sources changed keeps a line over a blocked
// profile's only when it is the one such owner: of two, which held the line
// cannot be told, and the line is nobody's.
func TestMigrateRoutesGivesABlockedLineOnlyToTheOneChangedOwnerOverIt(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	profiles := []PlanProfile{e.profile("default", "one", shared), e.profile("named:a", "one", shared), e.profile("named:b", "one", shared)}
	blocked := map[string]bool{"default": true}
	f := MigrateRoutes(profiles, nil, blocked, map[string]bool{"named:b": true}, testNow, e.newID)
	if err := ValidateRoutes(f, testNow); err != nil || len(f.Routes) != 1 || f.Routes[0].Owner != "named:b" || f.Routes[0].State != RouteActive {
		t.Fatalf("the one changed owner keeps the line: %+v %v", f.Routes, err)
	}
	f = MigrateRoutes(profiles, nil, blocked, map[string]bool{"named:a": true, "named:b": true}, testNow, e.newID)
	if err := ValidateRoutes(f, testNow); err != nil || len(f.Routes) != 1 || f.Routes[0].Owner != "" || f.Routes[0].State != RouteUnowned {
		t.Fatalf("of two, neither is guessed: %+v %v", f.Routes, err)
	}
}

// The routes a first start makes from an old snapshot are named after their
// source, as the routes Plan makes are: a run outside the daemon that read the
// source before it was reserved holds the lock of that name, and is waited for.
func TestMigrateRoutesNamesTheRoutesItMakesAfterTheirSource(t *testing.T) {
	e := newPlanEnv(t)
	shared, own := e.line("codex", "shared"), e.line("claude", "own")
	profiles := []PlanProfile{e.profile("default", "one", shared), e.profile("named:zeta", "one", shared), e.profile("named:solo", "one", own)}
	f := MigrateRoutes(profiles, nil, map[string]bool{"default": true, "named:solo": true}, map[string]bool{"named:zeta": true}, testNow, nil)
	if err := ValidateRoutes(f, testNow); err != nil || len(f.Routes) != 2 {
		t.Fatalf("routes %+v, %v", f.Routes, err)
	}
	for _, r := range f.Routes {
		if want := SourceLockID(r.Kind+":"+r.Path, r.Destination); r.ID != want {
			t.Fatalf("route %+v is not named after its source %s", r, want)
		}
	}
}
