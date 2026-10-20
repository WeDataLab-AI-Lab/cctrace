package localdaemon

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// planEnv builds profiles whose source folders exist under one temp root.
type planEnv struct {
	t    *testing.T
	root string
	next int
}

func newPlanEnv(t *testing.T) *planEnv {
	t.Helper()
	return &planEnv{t: t, root: canonicalSource(t.TempDir())}
}

// folder creates a source folder under the env and returns its path.
func (e *planEnv) folder(name string) string {
	e.t.Helper()
	path := filepath.Join(e.root, name)
	if err := os.MkdirAll(path, 0700); err != nil {
		e.t.Fatal(err)
	}
	return path
}

// line is a Spec.Sources entry for a folder of the env.
func (e *planEnv) line(kind, name string) string {
	e.t.Helper()
	return kind + ":" + e.folder(name)
}

// profile is an enabled profile at the named destination.
func (e *planEnv) profile(id, dest string, lines ...string) PlanProfile {
	name := strings.TrimPrefix(id, "named:")
	return PlanProfile{ID: id, Name: name, Destination: testDest(dest), Lines: lines, Enabled: true}
}

// newID hands out 8-digit hex route ids in order, so tests can name them.
func (e *planEnv) newID() string {
	e.next++
	return fmt.Sprintf("%08x", e.next)
}

func (e *planEnv) in(profiles ...PlanProfile) PlanInput {
	return PlanInput{Profiles: profiles, Valid: true, Persist: true, Now: testNow, NewID: e.newID}
}

// plan runs Plan and checks what every result must satisfy: the file it hands
// back is one ReadRoutes would accept.
func (e *planEnv) plan(in PlanInput) PlanResult {
	e.t.Helper()
	res := Plan(in)
	if in.Persist && in.Valid {
		if err := ValidateRoutes(res.Routes, testNow); err != nil {
			e.t.Fatalf("Plan produced a file ValidateRoutes rejects: %v\n%+v", err, res.Routes)
		}
	}
	return res
}

func sharedSources(js JobPlan) []string {
	var out []string
	for _, s := range js.Excluded {
		out = append(out, fmt.Sprintf("%s/%s/%s/%s", s.Kind, s.Reason, s.Holder, s.HolderState))
	}
	return out
}

func TestPlanFirstReservationGoesToTheFirstProfileByID(t *testing.T) {
	e := newPlanEnv(t)
	shared, mine, theirs := e.line("codex", "shared"), e.line("claude", "mine"), e.line("claude", "theirs")
	res := e.plan(e.in(
		e.profile("named:work", "one", theirs, shared),
		e.profile("default", "one", mine, shared),
	))
	if !res.Changed || len(res.Routes.Routes) != 3 {
		t.Fatalf("three sources are three routes: changed=%v routes=%+v", res.Changed, res.Routes.Routes)
	}
	def, work := res.Jobs["default"], res.Jobs["named:work"]
	if !reflect.DeepEqual(def.Allowed, []string{mine, shared}) || len(def.Excluded) != 0 {
		t.Fatalf("default reads everything: %+v", def)
	}
	if !reflect.DeepEqual(work.Allowed, []string{theirs}) {
		t.Fatalf("the later profile keeps what is only its own: %+v", work.Allowed)
	}
	if len(work.Excluded) != 1 {
		t.Fatalf("exactly the shared line is left out: %+v", work.Excluded)
	}
	got := work.Excluded[0]
	var shareID string
	for _, r := range res.Routes.Routes {
		if r.Kind == "codex" {
			shareID = r.ID
			if r.Owner != "default" || r.State != RouteActive || r.Rev != 1 || !r.Since.Equal(testNow) || r.Path != filepath.Join(e.root, "shared") {
				t.Fatalf("route = %+v", r)
			}
		}
	}
	want := SharedSource{Source: shareID, Kind: "codex", Reason: ShareSame, Holder: "default", HolderName: "default", HolderState: HolderActive}
	if got != want {
		t.Fatalf("excluded = %+v, want %+v", got, want)
	}
	if def.AllExcluded || work.AllExcluded {
		t.Fatal("neither profile is fully excluded")
	}
}

func TestPlanOrdersProfilesByOrderWhenGiven(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	late, early := e.profile("default", "one", shared), e.profile("named:z1", "one", shared)
	late.Order, early.Order = "b", "a"
	res := e.plan(e.in(late, early))
	if len(res.Routes.Routes) != 1 || res.Routes.Routes[0].Owner != "named:z1" {
		t.Fatalf("the profile that orders first reserves first: %+v", res.Routes.Routes)
	}
}

func TestPlanNamesTheRoutesBehindTheLinesAProfileMayRead(t *testing.T) {
	e := newPlanEnv(t)
	shared, mine, theirs := e.line("codex", "shared"), e.line("claude", "mine"), e.line("claude", "theirs")
	nested := e.line("codex", "shared/sessions/2026")
	res := e.plan(e.in(e.profile("default", "one", nested, shared, mine), e.profile("named:work", "one", theirs, shared)))
	id := func(kind, folder string) string {
		for _, r := range res.Routes.Routes {
			if r.Kind == kind && r.Path == filepath.Join(e.root, folder) {
				return r.ID
			}
		}
		t.Fatalf("no route for %s %s", kind, folder)
		return ""
	}
	// default's two overlapping codex lines are covered by the one route it made.
	want := []string{id("claude", "mine"), id("codex", "shared/sessions/2026")}
	slices.Sort(want)
	if got := res.Jobs["default"].Routes; !slices.Equal(got, want) {
		t.Fatalf("default's routes = %v, want %v", got, want)
	}
	if got := res.Jobs["named:work"].Routes; !slices.Equal(got, []string{id("claude", "theirs")}) {
		t.Fatalf("a line that is left out names no route: %v", got)
	}
	read := e.in(e.profile("default", "one", nested, shared, mine), e.profile("named:work", "one", theirs, shared))
	read.Persist = false
	if got := Plan(read).Jobs["default"].Routes; len(got) != 0 {
		t.Fatalf("a route that is only a decision in memory has no id to lock: %v", got)
	}
}

func TestPlanSaysWhichStoredRoutesBackEachAllowedLine(t *testing.T) {
	e := newPlanEnv(t)
	shared, own := e.line("codex", "shared"), e.line("claude", "own")
	def, work := e.profile("default", "one", shared, own), e.profile("named:work", "one", shared)
	res := e.plan(e.in(def, work))
	got := res.Jobs["default"].LineRoutes
	if len(got) != 2 || len(got[shared]) != 1 || len(got[own]) != 1 || got[shared][0] == got[own][0] {
		t.Fatalf("default's lines are backed by %v", got)
	}
	if backing := res.Jobs["named:work"].LineRoutes; len(backing) != 0 {
		t.Fatalf("a line left out has a route behind it for this profile: %v", backing)
	}
	// What a run locks for these lines is the whole set, as it always was.
	if want := slices.Sorted(slices.Values(append(slices.Clone(got[shared]), got[own]...))); !slices.Equal(res.Jobs["default"].Routes, want) {
		t.Fatalf("routes %v, lines' routes %v", res.Jobs["default"].Routes, want)
	}
	// A route that only exists for a read-only plan has no id to lock.
	in := e.in(def, work)
	in.Persist = false
	if backing := Plan(in).Jobs["default"].LineRoutes; len(backing) != 0 {
		t.Fatalf("a read-only plan names routes that are not stored: %v", backing)
	}
}

func TestPlanNamesTheProfilesLeftOutOfAClaudeFolderForItsOwner(t *testing.T) {
	e := newPlanEnv(t)
	home, codex, own := e.line("claude", "home"), e.line("codex", "shared"), e.line("claude", "own")
	owner := e.profile("default", "one", home, codex)
	owner.RuntimeDir = "rt-default"
	work := e.profile("named:work", "one", own, home, codex)
	work.RuntimeDir = "rt-work"
	idle := e.profile("named:idle", "one", home)
	idle.RuntimeDir, idle.Stopped = "rt-idle", true
	apart := e.profile("named:apart", "two", home)
	apart.RuntimeDir = "rt-apart"
	res := e.plan(e.in(owner, work, idle, apart))
	if got := res.Jobs["default"].PeerPending; !slices.Equal(got, []string{"rt-idle", "rt-work"}) {
		t.Fatalf("default's siblings = %v: every profile left out of its Claude folder, whatever its state, and no other (not the codex overlap, not another destination)", got)
	}
	for _, id := range []string{"named:work", "named:idle", "named:apart"} {
		if got := res.Jobs[id].PeerPending; len(got) != 0 {
			t.Fatalf("%s reads no sibling's queue: %v", id, got)
		}
	}
}

func TestPlanNamesNoSiblingsForARouteNobodyHolds(t *testing.T) {
	e := newPlanEnv(t)
	home := e.line("claude", "home")
	h := newHistory(e)
	holder, other := e.profile("default", "one", home), e.profile("named:work", "one", home)
	holder.RuntimeDir, other.RuntimeDir = "rt-default", "rt-work"
	h.step(holder, other)
	// The holder is deleted: nobody reads the folder, so nobody reads its queues.
	other2 := other
	res := h.step(other2)
	if got := res.Jobs["named:work"].PeerPending; len(got) != 0 {
		t.Fatalf("siblings of an orphan route: %v", got)
	}
	// A route being handed over has no reader either.
	h2 := newHistory(e)
	h2.step(holder, other)
	r := h2.mustRoute(home)
	r.State, r.To = RouteReleasing, "named:work"
	h2.routes.Routes[0] = r
	res = h2.step(holder, other)
	if len(res.Jobs["default"].PeerPending) != 0 || len(res.Jobs["named:work"].PeerPending) != 0 {
		t.Fatalf("siblings of a route being handed over: %+v", res.Jobs)
	}
}

func TestPlanLabelsTheExcludedProfilesOwnLine(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	other := e.profile("named:work", "one", shared)
	other.Labels = map[string]string{shared: "explicit"}
	holder := e.profile("default", "one", shared)
	holder.Labels = map[string]string{shared: "default"}
	res := e.plan(e.in(holder, other))
	if got := res.Jobs["named:work"].Excluded[0].Label; got != "explicit" {
		t.Fatalf("label = %q, want the excluded profile's own label", got)
	}
}

func TestPlanRunningJobIsNotPushedOutByANewProfile(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	running := e.plan(e.in(e.profile("named:zeta", "one", shared)))
	// A fresh decision would give the source to "default", which sorts first. A
	// profile that turns up later must not take it from the one reading it.
	in := e.in(e.profile("default", "one", shared), e.profile("named:zeta", "one", shared))
	in.Routes = running.Routes
	res := e.plan(in)
	if res.Changed {
		t.Fatalf("nothing about the existing route changed: %+v", res.Routes)
	}
	if !reflect.DeepEqual(res.Jobs["named:zeta"].Allowed, []string{shared}) {
		t.Fatalf("the running profile keeps its source: %+v", res.Jobs["named:zeta"])
	}
	if late := res.Jobs["default"]; len(late.Allowed) != 0 || !late.AllExcluded {
		t.Fatalf("the late profile is left out: %+v", late)
	}
}

func TestPlanDifferentDestinationsKeepTheirFanOut(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	res := e.plan(e.in(e.profile("default", "one", shared), e.profile("named:work", "two", shared)))
	if len(res.Routes.Routes) != 2 {
		t.Fatalf("each destination reserves the source: %+v", res.Routes.Routes)
	}
	for id, js := range res.Jobs {
		if !reflect.DeepEqual(js.Allowed, []string{shared}) || len(js.Excluded) != 0 {
			t.Fatalf("%s: %+v", id, js)
		}
	}
}

func TestPlanJudgesOverlapOnScanRoots(t *testing.T) {
	e := newPlanEnv(t)
	tests := []struct {
		name     string
		kind     string
		a, b     string
		excluded string // "" = both read
	}{
		{"claude nested folders are disjoint", "claude", "h", "h/other", ""},
		{"claude nested roots", "claude", "h", "h/projects/q", "claude/nested/default/active"},
		{"codex same folder", "codex", "h", "h", "codex/same/default/active"},
		{"codex nested", "codex", "h", "h/sessions/2026", "codex/nested/default/active"},
		{"gjc disjoint", "gjc", "h", "h/agent", ""},
		{"omo shares one of its two roots", "omo", "h", "h/agent", "omo/nested/default/active"},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := e.line(tc.kind, fmt.Sprintf("%d/%s", i, tc.a)), e.line(tc.kind, fmt.Sprintf("%d/%s", i, tc.b))
			res := e.plan(e.in(e.profile("default", "one", a), e.profile("named:work", "one", b)))
			got := strings.Join(sharedSources(res.Jobs["named:work"]), ",")
			if got != tc.excluded {
				t.Fatalf("excluded = %q, want %q", got, tc.excluded)
			}
			if tc.excluded == "" && len(res.Routes.Routes) != 2 {
				t.Fatalf("disjoint sources are two routes: %+v", res.Routes.Routes)
			}
		})
	}
}

func TestPlanNeverCrossesKinds(t *testing.T) {
	e := newPlanEnv(t)
	folder := e.folder("h")
	res := e.plan(e.in(e.profile("default", "one", "codex:"+folder), e.profile("named:work", "one", "gjc:"+folder)))
	if len(res.Jobs["named:work"].Excluded) != 0 || len(res.Routes.Routes) != 2 {
		t.Fatalf("a different kind is a different source: %+v", res)
	}
}

func TestPlanOneProfilesOverlappingLinesShareOneRoute(t *testing.T) {
	e := newPlanEnv(t)
	outer, inner := e.line("codex", "h"), e.line("codex", "h/sessions/2026")
	res := e.plan(e.in(e.profile("default", "one", outer, inner), e.profile("named:work", "one", inner)))
	if len(res.Routes.Routes) != 1 {
		t.Fatalf("lines of one profile that overlap are one reservation: %+v", res.Routes.Routes)
	}
	if !reflect.DeepEqual(res.Jobs["default"].Allowed, []string{outer, inner}) {
		t.Fatalf("default reads both: %+v", res.Jobs["default"])
	}
	if !res.Jobs["named:work"].AllExcluded {
		t.Fatalf("work overlaps the one route: %+v", res.Jobs["named:work"])
	}
}

func TestPlanAllExcludedIsNotLocalAnalysisOnly(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	res := e.plan(e.in(e.profile("default", "one", shared), e.profile("named:work", "one", shared), e.profile("named:local", "one")))
	if !res.Jobs["named:work"].AllExcluded {
		t.Fatal("a profile whose every line is held elsewhere is fully excluded")
	}
	if local := res.Jobs["named:local"]; local.AllExcluded || len(local.Allowed) != 0 || len(local.Excluded) != 0 || local.RouteOwner {
		t.Fatalf("a profile with no upload lines is simply not collecting: %+v", local)
	}
}

func TestPlanHolderStates(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	stored := e.plan(e.in(e.profile("default", "one", shared))).Routes
	tests := []struct {
		name string
		mod  func(p *PlanProfile)
		want string
	}{
		{"collecting", func(*PlanProfile) {}, HolderActive},
		{"stopped", func(p *PlanProfile) { p.Stopped = true }, HolderStopped},
		{"disabled", func(p *PlanProfile) { p.Enabled = false }, HolderDisabled},
		{"blocked by a migration conflict", func(p *PlanProfile) { p.Blocked = true }, HolderBlocked},
		{"stopped wins over blocked", func(p *PlanProfile) { p.Stopped, p.Blocked = true, true }, HolderStopped},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			holder := e.profile("default", "one", shared)
			tc.mod(&holder)
			in := e.in(holder, e.profile("named:work", "one", shared))
			in.Routes = stored
			res := e.plan(in)
			if got := sharedSources(res.Jobs["named:work"]); !reflect.DeepEqual(got, []string{"codex/same/default/" + tc.want}) {
				t.Fatalf("excluded = %v, want holder state %s", got, tc.want)
			}
		})
	}
}

// A holder none of whose lines it may read is blocked, however it got there.
func TestPlanHolderThatIsItselfShutOutIsBlocked(t *testing.T) {
	e := newPlanEnv(t)
	outer, inner, rival := e.line("codex", "h"), e.line("codex", "h/sessions/2026"), e.line("codex", "h/sessions/2027")
	route := func(id, line, owner string) Route {
		return Route{ID: id, Rev: 1, Kind: "codex", Path: strings.TrimPrefix(line, "codex:"), Destination: testDest("one"), Owner: owner, State: RouteActive, Since: testNow}
	}
	// default's one line spans two routes; the other belongs to a third profile.
	in := e.in(e.profile("default", "one", outer), e.profile("named:rival", "one", rival), e.profile("named:work", "one", inner))
	in.Routes = RouteFile{Version: 1, Routes: []Route{route("00000b01", inner, "default"), route("00000b02", rival, "named:rival")}}
	res := e.plan(in)
	if !res.Jobs["default"].AllExcluded {
		t.Fatalf("default's line is partly held by the rival: %+v", res.Jobs["default"])
	}
	if got := sharedSources(res.Jobs["named:work"]); !reflect.DeepEqual(got, []string{"codex/same/default/" + HolderBlocked}) {
		t.Fatalf("excluded = %v", got)
	}
}

func TestPlanKeepsARemovedOwnersSourceClosed(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	routes := e.plan(e.in(e.profile("default", "one", shared))).Routes
	in := e.in(e.profile("named:work", "one", shared))
	in.Routes = routes
	res := e.plan(in)
	work := res.Jobs["named:work"]
	if len(work.Allowed) != 0 || len(work.Excluded) != 1 {
		t.Fatalf("a deleted owner must not hand its source to anyone: %+v", work)
	}
	if s := work.Excluded[0]; s.HolderState != HolderRemoved || s.Holder != "default" || s.HolderName != "" {
		t.Fatalf("excluded = %+v", s)
	}
	if res.Changed {
		t.Fatal("the orphan is left as it is while a profile still overlaps it")
	}
}

func TestPlanStoppedAndDisabledOwnersStayRouteOwners(t *testing.T) {
	e := newPlanEnv(t)
	shared, own := e.line("codex", "shared"), e.line("claude", "own")
	first := e.plan(e.in(e.profile("default", "one", shared, own), e.profile("named:work", "one", shared)))
	for name, mod := range map[string]func(*PlanProfile){
		"stopped":  func(p *PlanProfile) { p.Stopped = true },
		"disabled": func(p *PlanProfile) { p.Enabled = false },
		"blocked":  func(p *PlanProfile) { p.Blocked = true },
	} {
		t.Run(name, func(t *testing.T) {
			holder := e.profile("default", "one", shared, own)
			mod(&holder)
			in := e.in(holder, e.profile("named:work", "one", shared))
			in.Routes = first.Routes
			res := e.plan(in)
			if !res.Jobs["default"].RouteOwner {
				t.Fatal("an owner that is not running still owns: an older build must choose it again")
			}
			if res.Jobs["named:work"].RouteOwner {
				t.Fatal("a profile with a source left out is not a route owner")
			}
			if res.Changed {
				t.Fatalf("a stopped owner keeps its routes untouched: %+v", res.Routes)
			}
		})
	}
}

func TestPlanRouteOwnerNeedsEveryLine(t *testing.T) {
	e := newPlanEnv(t)
	shared, own := e.line("codex", "shared"), e.line("claude", "own")
	res := e.plan(e.in(e.profile("default", "one", shared), e.profile("named:work", "one", shared, own)))
	if !res.Jobs["default"].RouteOwner {
		t.Fatal("default holds all of its lines")
	}
	if res.Jobs["named:work"].RouteOwner {
		t.Fatal("work holds `own` but not `shared`")
	}
}

func TestPlanIneligibleProfilesReserveNothing(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	for name, mod := range map[string]func(*PlanProfile){
		"disabled": func(p *PlanProfile) { p.Enabled = false },
		"stopped":  func(p *PlanProfile) { p.Stopped = true },
		"blocked":  func(p *PlanProfile) { p.Blocked = true },
	} {
		t.Run(name, func(t *testing.T) {
			idle := e.profile("default", "one", shared)
			mod(&idle)
			res := e.plan(e.in(idle, e.profile("named:work", "one", shared)))
			if len(res.Routes.Routes) != 1 || res.Routes.Routes[0].Owner != "named:work" {
				t.Fatalf("only the profile that can collect reserves: %+v", res.Routes.Routes)
			}
			if got := res.Jobs["default"]; len(got.Excluded) != 1 || got.RouteOwner {
				t.Fatalf("the idle profile is told who holds the source: %+v", got)
			}
		})
	}
	t.Run("alone", func(t *testing.T) {
		idle := e.profile("default", "one", shared)
		idle.Enabled = false
		res := e.plan(e.in(idle))
		if res.Changed || len(res.Routes.Routes) != 0 || !reflect.DeepEqual(res.Jobs["default"].Allowed, []string{shared}) {
			t.Fatalf("nothing to reserve and nothing in the way: %+v", res)
		}
	})
}

func TestPlanUnroutableLinesAreLeftAlone(t *testing.T) {
	e := newPlanEnv(t)
	folder := e.folder("h")
	noDest := e.profile("default", "one", "codex:"+folder)
	noDest.Destination = ""
	malformed := e.profile("named:a", "one", "codex:"+folder)
	malformed.Destination = "dest"
	odd := e.profile("named:b", "one", "cursor:"+folder, "nocolon", "codex:", "codex:"+folder)
	res := e.plan(e.in(noDest, malformed, odd))
	if len(res.Routes.Routes) != 1 {
		t.Fatalf("only the routable line reserves: %+v", res.Routes.Routes)
	}
	for _, id := range []string{"default", "named:a"} {
		if js := res.Jobs[id]; len(js.Allowed) != 1 || len(js.Excluded) != 0 || js.RouteOwner {
			t.Fatalf("%s: %+v", id, js)
		}
	}
	if got := res.Jobs["named:b"].Allowed; !reflect.DeepEqual(got, odd.Lines) {
		t.Fatalf("lines the planner cannot place are not its business: %v", got)
	}
}

func TestPlanPersistFalseDecidesInMemory(t *testing.T) {
	e := newPlanEnv(t)
	shared, own := e.line("codex", "shared"), e.line("claude", "own")
	in := e.in(e.profile("named:work", "one", shared, own), e.profile("default", "one", shared))
	in.Persist = false
	res := e.plan(in)
	if res.Changed || len(res.Routes.Routes) != 0 || res.Commits != nil {
		t.Fatalf("a read-only plan changes nothing: %+v", res)
	}
	if !reflect.DeepEqual(res.Jobs["default"].Allowed, []string{shared}) {
		t.Fatalf("the first by ID reads the unreserved source: %+v", res.Jobs["default"])
	}
	work := res.Jobs["named:work"]
	if !reflect.DeepEqual(work.Allowed, []string{own}) || len(work.Excluded) != 1 || work.Excluded[0].Source != "" || work.Excluded[0].Holder != "default" {
		t.Fatalf("work is left out of the shared line, without a route id to name: %+v", work)
	}
	if !res.Jobs["default"].RouteOwner {
		t.Fatal("route_owner follows the same decision")
	}
}

func TestPlanPersistFalseFollowsExistingRoutes(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	stored := e.plan(e.in(e.profile("named:work", "one", shared))).Routes
	in := e.in(e.profile("default", "one", shared), e.profile("named:work", "one", shared))
	in.Routes, in.Persist = stored, false
	res := e.plan(in)
	if !res.Jobs["default"].AllExcluded || !reflect.DeepEqual(res.Jobs["named:work"].Allowed, []string{shared}) {
		t.Fatalf("a stored reservation beats the ID order: %+v", res.Jobs)
	}
	if got := res.Jobs["default"].Excluded[0].Source; got != stored.Routes[0].ID {
		t.Fatalf("the stored route is named by its id, got %q", got)
	}
	if !reflect.DeepEqual(res.Routes, stored) {
		t.Fatal("a read-only plan returns the file it was given")
	}
}

func TestPlanUnknownOwnerClosesOnlyOverlappingLines(t *testing.T) {
	e := newPlanEnv(t)
	shared, nested, innerOnly := e.line("codex", "shared"), e.line("codex", "tree"), e.line("codex", "tree/sessions/2026")
	own, other, fan := e.line("claude", "own"), e.line("claude", "theirs"), e.line("codex", "fan")
	in := e.in(
		e.profile("default", "one", shared, own, fan),
		e.profile("named:work", "one", shared, other, nested),
		e.profile("named:third", "one", innerOnly),
		e.profile("named:fanout", "two", fan),
	)
	in.Valid = false
	res := e.plan(in)
	if res.Changed || len(res.Routes.Routes) != 0 || res.Commits != nil {
		t.Fatalf("an unknown owner writes nothing: %+v", res)
	}
	def := res.Jobs["default"]
	if !reflect.DeepEqual(def.Allowed, []string{own, fan}) || len(def.Excluded) != 1 {
		t.Fatalf("default: %+v", def)
	}
	if got := def.Excluded[0]; got.Source != "" || got.Holder != "" || got.HolderName != "" || got.HolderState != HolderUnknown || got.Reason != ShareSame || got.Kind != "codex" {
		t.Fatalf("an unknown holder names nobody: %+v", got)
	}
	work := res.Jobs["named:work"]
	if !reflect.DeepEqual(work.Allowed, []string{other}) {
		t.Fatalf("work keeps the line nobody shares: %+v", work.Allowed)
	}
	if reasons := []string{work.Excluded[0].Reason, work.Excluded[1].Reason}; !reflect.DeepEqual(reasons, []string{ShareSame, ShareNested}) {
		t.Fatalf("work's reasons = %v", reasons)
	}
	if !res.Jobs["named:third"].AllExcluded {
		t.Fatal("third's only line sits inside work's tree")
	}
	if got := res.Jobs["named:fanout"].Allowed; !reflect.DeepEqual(got, []string{fan}) {
		t.Fatalf("another destination is not an overlap: %v", got)
	}
	for id, js := range res.Jobs {
		if js.RouteOwner {
			t.Fatalf("%s: no route is known, so none can be owned", id)
		}
	}
}

func TestPlanUnknownOwnerCountsIdleProfilesAsPossibleOwners(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	idle := e.profile("default", "one", shared)
	idle.Stopped = true
	in := e.in(idle, e.profile("named:work", "one", shared))
	in.Valid = false
	res := e.plan(in)
	if !res.Jobs["named:work"].AllExcluded {
		t.Fatal("the lost file may have said the stopped profile owns the source")
	}
}

// A run outside the daemon and `profile claim` act on ExcludedLines, not on the
// warnings: with no usable file every closed line is there, past the cap on
// warnings, and names no route because none is known.
func TestPlanUnknownOwnerNamesEveryClosedLine(t *testing.T) {
	e := newPlanEnv(t)
	own := e.line("claude", "own")
	var shared []string
	for i := range 40 {
		shared = append(shared, e.line("codex", fmt.Sprintf("shared/%d", i)))
	}
	in := e.in(e.profile("default", "one", append([]string{own}, shared...)...), e.profile("named:work", "one", shared...))
	in.Valid = false
	res := e.plan(in)
	for id, js := range res.Jobs {
		var lines []string
		for _, x := range js.ExcludedLines {
			if x.Route != "" {
				t.Fatalf("%s: a line closed for want of a file names route %q", id, x.Route)
			}
			lines = append(lines, x.Line)
		}
		if !reflect.DeepEqual(lines, shared) || len(js.Excluded) != maxSharedSources {
			t.Fatalf("%s: %d excluded lines of %d, %d warnings", id, len(lines), len(shared), len(js.Excluded))
		}
	}
	if def := res.Jobs["default"]; !reflect.DeepEqual(def.Allowed, []string{own}) || !res.Jobs["named:work"].AllExcluded {
		t.Fatalf("jobs = %+v", res.Jobs)
	}
}

func TestPlanUnknownOwnerIgnoresASingleProfilesOwnOverlap(t *testing.T) {
	e := newPlanEnv(t)
	outer, inner := e.line("codex", "h"), e.line("codex", "h/sessions/2026")
	in := e.in(e.profile("default", "one", outer, inner))
	in.Valid = false
	res := e.plan(in)
	if !reflect.DeepEqual(res.Jobs["default"].Allowed, []string{outer, inner}) {
		t.Fatalf("a profile does not contend with itself: %+v", res.Jobs["default"])
	}
}

func TestPlanReportsHandoffsOfAllowedLines(t *testing.T) {
	e := newPlanEnv(t)
	moved, kept := e.line("codex", "moved"), e.line("claude", "kept")
	owner := e.profile("named:work", "one", moved, kept)
	other := e.profile("default", "one", moved)
	handoff := testHandoff("h1")
	in := e.in(other, owner)
	in.Routes = RouteFile{Version: 1, Rev: 2, Routes: []Route{
		{ID: "00000a01", Rev: 4, Kind: "codex", Path: strings.TrimPrefix(moved, "codex:"), Destination: testDest("one"), Owner: "named:work", State: RouteActive, From: "default", Handoff: handoff, Since: testNow},
		{ID: "00000a02", Rev: 1, Kind: "claude", Path: strings.TrimPrefix(kept, "claude:"), Destination: testDest("one"), Owner: "named:work", State: RouteActive, Since: testNow},
	}}
	res := e.plan(in)
	want := []Handoff{{Line: moved, From: "default", ID: handoff}}
	if got := res.Jobs["named:work"].Handoffs; !reflect.DeepEqual(got, want) {
		t.Fatalf("handoffs = %+v, want %+v", got, want)
	}
	if got := res.Jobs["default"].Handoffs; len(got) != 0 {
		t.Fatalf("a profile that does not read the line has no handoff: %+v", got)
	}
}

func TestPlanIsStableOnceApplied(t *testing.T) {
	e := newPlanEnv(t)
	shared, own, nested := e.line("codex", "shared"), e.line("claude", "own"), e.line("codex", "shared/sessions/2026")
	profiles := []PlanProfile{
		e.profile("default", "one", shared, own),
		e.profile("named:work", "one", nested, own),
		e.profile("named:idle", "one", shared),
	}
	profiles[2].Stopped = true
	first := e.plan(e.in(profiles...))
	again := e.in(profiles...)
	again.Routes = first.Routes
	second := e.plan(again)
	if second.Changed {
		t.Fatalf("a settled plan changes nothing: %+v", second.Routes)
	}
	if !reflect.DeepEqual(first.Jobs, second.Jobs) || !reflect.DeepEqual(first.Routes, second.Routes) {
		t.Fatal("a settled plan answers the same")
	}
}

func TestPlanCapsExcludedEntriesPerProfile(t *testing.T) {
	e := newPlanEnv(t)
	var holderLines, workLines []string
	for i := range 40 {
		l := e.line("claude", fmt.Sprintf("many/%d", i))
		holderLines, workLines = append(holderLines, l), append(workLines, l)
	}
	res := e.plan(e.in(e.profile("default", "one", holderLines...), e.profile("named:work", "one", workLines...)))
	work := res.Jobs["named:work"]
	if len(work.Excluded) != 32 || !work.AllExcluded || len(work.Allowed) != 0 {
		t.Fatalf("the warning list is capped, the decision is not: %d excluded, allowed %d", len(work.Excluded), len(work.Allowed))
	}
}

func TestPlanDefaultIDsAreValidAndDistinct(t *testing.T) {
	e := newPlanEnv(t)
	var lines []string
	for i := range 20 {
		lines = append(lines, e.line("claude", fmt.Sprintf("r/%d", i)))
	}
	in := e.in(e.profile("default", "one", lines...))
	in.NewID = nil
	res := e.plan(in)
	seen := map[string]bool{}
	for _, r := range res.Routes.Routes {
		if seen[r.ID] {
			t.Fatalf("duplicate id %s", r.ID)
		}
		seen[r.ID] = true
	}
	if len(seen) != 20 {
		t.Fatalf("routes = %d", len(seen))
	}
}

func TestPlanStampsNewRoutesWithTheClockWhenNotGiven(t *testing.T) {
	e := newPlanEnv(t)
	in := e.in(e.profile("default", "one", e.line("claude", "own")))
	in.Now = time.Time{}
	before := time.Now()
	res := Plan(in)
	if err := ValidateRoutes(res.Routes, time.Now()); err != nil {
		t.Fatal(err)
	}
	if since := res.Routes.Routes[0].Since; since.Before(before) || since.After(time.Now()) {
		t.Fatalf("since = %s", since)
	}
}

func TestPlanDoesNotMutateItsInput(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	stored := e.plan(e.in(e.profile("default", "one", shared))).Routes
	stored.Routes[0].Aliases = []string{testDest("old")}
	snapshot := marshalFile(t, stored)
	in := e.in(e.profile("default", "one", shared), e.profile("named:work", "one", shared))
	in.Routes = stored
	e.plan(in)
	if marshalFile(t, stored) != snapshot {
		t.Fatal("Plan changed the file it was given")
	}
}

// R10: a planning pass over a large registry must stay cheap. The fleet of 128
// profiles all share the default Codex, gjc and omo folders and each has its
// own Claude folder, which is the shape of a machine with many profiles.
func TestPlanCostWithManyProfiles(t *testing.T) {
	e := newPlanEnv(t)
	shared := []string{e.line("codex", "defaults/codex"), e.line("gjc", "defaults/gjc"), e.line("omo", "defaults/omo")}
	var profiles []PlanProfile
	for i := range 128 {
		lines := append([]string{e.line("claude", fmt.Sprintf("claude/%03d", i))}, shared...)
		profiles = append(profiles, e.profile(fmt.Sprintf("named:p%03d", i), "one", lines...))
	}
	in := e.in(profiles...)
	start := time.Now()
	first := e.plan(in)
	cold := time.Since(start)
	if len(first.Routes.Routes) != 128+3 {
		t.Fatalf("routes = %d, want 131", len(first.Routes.Routes))
	}
	in.Routes = first.Routes
	start = time.Now()
	second := e.plan(in)
	warm := time.Since(start)
	if second.Changed {
		t.Fatal("the second pass must settle")
	}
	t.Logf("128 profiles: first plan %s, settled plan %s", cold, warm)
	if cold > time.Second || warm > time.Second {
		t.Fatalf("planning 128 profiles is too slow: first %s, settled %s", cold, warm)
	}
	if got := len(first.Jobs["named:p127"].Excluded); got != 3 {
		t.Fatalf("the last profile leaves out the three shared folders, got %d", got)
	}
}
