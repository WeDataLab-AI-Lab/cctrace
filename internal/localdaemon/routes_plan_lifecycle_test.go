package localdaemon

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// history plays a plan forward: each step plans over the file the last one
// produced, as the daemon does, and a step that is not followed by silence is a
// plan that does not settle.
type history struct {
	e      *planEnv
	routes RouteFile
	held   map[string][]string
	steps  int
}

func newHistory(e *planEnv) *history { return &history{e: e} }

func (h *history) step(profiles ...PlanProfile) PlanResult {
	h.e.t.Helper()
	h.steps++
	in := h.e.in(profiles...)
	in.Routes, in.Held, in.Now = h.routes, h.held, testNow.Add(time.Duration(h.steps)*time.Minute)
	res := h.e.plan(in)
	h.routes = res.Routes
	again := h.e.in(profiles...)
	again.Routes, again.Held, again.Now = h.routes, h.held, in.Now
	if next := h.e.plan(again); next.Changed {
		h.e.t.Fatalf("the plan does not settle: a second pass over its own file changed it\nfirst:  %+v\nsecond: %+v", res.Routes.Routes, next.Routes.Routes)
	}
	return res
}

// removeLosers plays the supervisor's part for the routes that lost a collision:
// once their owner holds nothing of them, they are removed.
func (h *history) removeLosers(res PlanResult) {
	h.e.t.Helper()
	h.routes.Routes = slices.DeleteFunc(slices.Clone(h.routes.Routes), func(r Route) bool {
		return slices.Contains(res.Losers, r.ID) && slices.Contains(res.Commits, r.ID)
	})
}

// routeAt finds the route of a line folder.
func (h *history) routeAt(line string) (Route, bool) {
	kind, folder, _ := strings.Cut(line, ":")
	for _, r := range h.routes.Routes {
		if r.Kind == kind && r.Path == folder {
			return r, true
		}
	}
	return Route{}, false
}

func (h *history) mustRoute(line string) Route {
	h.e.t.Helper()
	r, ok := h.routeAt(line)
	if !ok {
		h.e.t.Fatalf("no route for %s in %+v", line, h.routes.Routes)
	}
	return r
}

func (e *planEnv) withFilters(p PlanProfile, f Filters) PlanProfile {
	p.Filters = f
	return p
}

func TestPlanReleasesASourceItsOwnerNoLongerHas(t *testing.T) {
	strict := Filters{RepoPrefixes: []string{"host/org/x"}}
	scoped := Filters{RepoPrefixes: []string{"host/org/"}}
	tests := []struct {
		name  string
		owner Filters
		who   func(e *planEnv, shared string) []PlanProfile // candidates besides the owner
		to    string
	}{
		{"the first qualifying candidate by ID", scoped, func(e *planEnv, s string) []PlanProfile {
			return []PlanProfile{e.withFilters(e.profile("named:a", "one", s), Filters{}), e.withFilters(e.profile("named:b", "one", s), strict), e.withFilters(e.profile("named:c", "one", s), strict)}
		}, "named:b"},
		{"only a looser candidate leaves it unowned", scoped, func(e *planEnv, s string) []PlanProfile {
			return []PlanProfile{e.withFilters(e.profile("named:a", "one", s), Filters{})}
		}, ""},
		{"an owner that collects everything admits any candidate", Filters{}, func(e *planEnv, s string) []PlanProfile {
			return []PlanProfile{e.withFilters(e.profile("named:a", "one", s), Filters{})}
		}, "named:a"},
		{"a stopped candidate does not qualify", scoped, func(e *planEnv, s string) []PlanProfile {
			stopped := e.withFilters(e.profile("named:a", "one", s), strict)
			stopped.Stopped = true
			return []PlanProfile{stopped, e.withFilters(e.profile("named:b", "one", s), strict)}
		}, "named:b"},
		{"a disabled candidate does not qualify", scoped, func(e *planEnv, s string) []PlanProfile {
			off := e.withFilters(e.profile("named:a", "one", s), strict)
			off.Enabled = false
			return []PlanProfile{off}
		}, ""},
		{"a candidate on another destination is not a candidate", scoped, func(e *planEnv, s string) []PlanProfile {
			return []PlanProfile{e.withFilters(e.profile("named:a", "two", s), strict)}
		}, ""},
		{"a profile without the source is not a candidate", scoped, func(e *planEnv, s string) []PlanProfile {
			return []PlanProfile{e.withFilters(e.profile("named:a", "one", e.line("claude", "elsewhere")), strict), e.withFilters(e.profile("named:b", "one", s), strict)}
		}, "named:b"},
		{"a candidate whose prefix drops the owner's slash does not qualify", Filters{RepoPrefixes: []string{"github.com/org/"}}, func(e *planEnv, s string) []PlanProfile {
			return []PlanProfile{e.withFilters(e.profile("named:a", "one", s), Filters{RepoPrefixes: []string{"github.com/org"}})}
		}, ""},
		{"a candidate that redacts less than the owner does not qualify", Filters{RedactPrompts: true}, func(e *planEnv, s string) []PlanProfile {
			return []PlanProfile{e.profile("named:a", "one", s), e.withFilters(e.profile("named:b", "one", s), Filters{RedactPrompts: true})}
		}, "named:b"},
		{"a candidate that excludes fewer accounts does not qualify", Filters{ExcludeAccounts: []string{"a1"}}, func(e *planEnv, s string) []PlanProfile {
			return []PlanProfile{e.profile("named:a", "one", s), e.withFilters(e.profile("named:b", "one", s), Filters{ExcludeAccounts: []string{"a1", "a2"}})}
		}, "named:b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newPlanEnv(t)
			h := newHistory(e)
			shared, other := e.line("codex", "shared"), e.line("claude", "other")
			def := e.withFilters(e.profile("default", "one", shared), tc.owner)
			cands := tc.who(e, shared)
			h.step(append([]PlanProfile{def}, cands...)...)
			before := h.mustRoute(shared)
			if before.Owner != "default" || before.State != RouteActive {
				t.Fatalf("setup: %+v", before)
			}
			def.Lines = []string{other}
			res := h.step(append([]PlanProfile{def}, cands...)...)
			got := h.mustRoute(shared)
			if got.State != RouteReleasing || got.To != tc.to || got.Owner != "default" || got.Rev != before.Rev+1 || got.Handoff != "" {
				t.Fatalf("route after the owner dropped the line: %+v", got)
			}
			if !res.Changed {
				t.Fatal("releasing is a change to save")
			}
			for _, c := range cands {
				for _, s := range res.Jobs[c.ID].Excluded {
					if s.Source == got.ID && s.HolderState != HolderTransferring {
						t.Fatalf("%s sees the holder as %s, want transferring", c.ID, s.HolderState)
					}
				}
			}
			if !reflect.DeepEqual(res.Commits, []string{got.ID}) {
				t.Fatalf("an owner that holds nothing has let go: commits %v", res.Commits)
			}
		})
	}
}

func TestPlanKeepsASourceWhileTheOwnerStillHasAnOverlappingLine(t *testing.T) {
	e := newPlanEnv(t)
	h := newHistory(e)
	outer, inner := e.line("codex", "h"), e.line("codex", "h/sessions/2026")
	other := e.profile("named:work", "one", outer)
	h.step(e.profile("default", "one", outer), other)
	res := h.step(e.profile("default", "one", inner), other)
	if r := h.mustRoute(outer); r.State != RouteActive || r.Owner != "default" || res.Changed {
		t.Fatalf("a line inside the route's scan root still reads it: %+v", r)
	}
	// Turning the whole kind off is what lets go of it.
	h.step(e.profile("default", "one", e.line("claude", "mine")), other)
	if r := h.mustRoute(outer); r.State != RouteReleasing {
		t.Fatalf("with no codex line left the route is released: %+v", r)
	}
}

func TestPlanOwnerWithAnUnusableDestinationKeepsItsSources(t *testing.T) {
	e := newPlanEnv(t)
	h := newHistory(e)
	shared := e.line("codex", "shared")
	other := e.profile("named:work", "one", shared)
	h.step(e.profile("default", "one", shared), other)
	broken := e.profile("default", "one", shared)
	broken.Destination = ""
	h.step(broken, other)
	if r := h.mustRoute(shared); r.State != RouteActive || r.Owner != "default" || r.Destination != testDest("one") {
		t.Fatalf("a profile whose destination is briefly unusable must not lose its source: %+v", r)
	}
}

func TestPlanReleaseClearsAPendingHandoffButKeepsWhereTheCursorWas(t *testing.T) {
	e := newPlanEnv(t)
	shared, other := e.line("codex", "shared"), e.line("claude", "other")
	in := e.in(e.profile("named:work", "one", other))
	in.Routes = RouteFile{Version: 1, Routes: []Route{{ID: "00000c01", Rev: 5, Kind: "codex", Path: strings.TrimPrefix(shared, "codex:"), Destination: testDest("one"), Owner: "named:work", State: RouteActive, From: "default", Handoff: testHandoff("p"), Since: testNow}}}
	res := e.plan(in)
	got := res.Routes.Routes[0]
	if got.State != RouteReleasing || got.Handoff != "" || got.From != "default" || got.Rev != 6 {
		t.Fatalf("the cursor still sits with the previous owner: %+v", got)
	}
}

func TestPlanCommitsOnlyOnceTheOwnerHoldsNothingOfTheRoute(t *testing.T) {
	e := newPlanEnv(t)
	shared, claude := e.line("codex", "shared"), e.line("claude", "mine")
	def := e.profile("default", "one", shared)
	h := newHistory(e)
	h.step(def, e.profile("named:work", "one", shared))
	// A claim: the owner keeps its line, the route is released.
	r := h.mustRoute(shared)
	r.State, r.To = RouteReleasing, "named:work"
	h.routes.Routes[0] = r
	idle := func(held map[string][]string, persist bool) PlanResult {
		in := e.in(def, e.profile("named:work", "one", shared))
		in.Routes, in.Held, in.Persist = h.routes, held, persist
		return e.plan(in)
	}
	tests := []struct {
		name    string
		held    map[string][]string
		persist bool
		want    []string
	}{
		{"the owner's job still reads the line", map[string][]string{"default": {shared}}, true, nil},
		{"it reads a line inside the route", map[string][]string{"default": {e.line("codex", "shared/sessions/2026")}}, true, nil},
		{"it reads other things only", map[string][]string{"default": {claude}}, true, []string{r.ID}},
		{"it is not running", nil, true, []string{r.ID}},
		{"another profile's held lines do not count", map[string][]string{"named:work": {shared}}, true, []string{r.ID}},
		{"a read-only plan commits nothing", nil, false, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := idle(tc.held, tc.persist)
			if !reflect.DeepEqual(res.Commits, tc.want) {
				t.Fatalf("commits = %v, want %v", res.Commits, tc.want)
			}
			if res.Changed {
				t.Fatal("releasing is settled; committing is the supervisor's step")
			}
		})
	}
	// While it is released nobody reads the line, the owner included.
	res := idle(map[string][]string{"default": {shared}}, true)
	for _, id := range []string{"default", "named:work"} {
		if len(res.Jobs[id].Allowed) != 0 {
			t.Fatalf("%s reads a line that is being handed over", id)
		}
	}
	if s := res.Jobs["default"].Excluded; len(s) != 1 || s[0].HolderState != HolderTransferring || s[0].Holder != "default" {
		t.Fatalf("the outgoing owner is told the line is moving: %+v", s)
	}
	if res.Jobs["default"].RouteOwner {
		t.Fatal("a profile whose line is moving is not a route owner")
	}
}

func TestPlanCommitsAReleasingRouteWhoseOwnerWasDeleted(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	h := newHistory(e)
	h.step(e.profile("default", "one", shared), e.profile("named:work", "one", shared))
	r := h.mustRoute(shared)
	r.State, r.To = RouteReleasing, "named:work"
	h.routes.Routes[0] = r
	res := h.step(e.profile("named:work", "one", shared))
	if !reflect.DeepEqual(res.Commits, []string{r.ID}) {
		t.Fatalf("a deleted owner runs nothing, so the hand-over can go on: %v", res.Commits)
	}
}

func TestPlanFollowsADestinationMovedByPolicyInEitherOrder(t *testing.T) {
	setup := func(t *testing.T) (*planEnv, *history, string, string) {
		e := newPlanEnv(t)
		h := newHistory(e)
		shared := e.line("codex", "shared")
		own := e.line("claude", "own")
		h.step(e.profile("default", "one", shared, own), e.profile("named:work", "one", shared))
		return e, h, shared, own
	}
	t.Run("the owner moves first", func(t *testing.T) {
		e, h, shared, own := setup(t)
		before := h.mustRoute(shared)
		res := h.step(e.profile("default", "two", shared, own), e.profile("named:work", "one", shared))
		r := h.mustRoute(shared)
		if r.Destination != testDest("two") || !reflect.DeepEqual(r.Aliases, []string{testDest("one")}) || r.Owner != "default" || r.ID != before.ID || r.Rev != before.Rev+1 {
			t.Fatalf("the old destination must be kept as an alias: %+v", r)
		}
		if w := res.Jobs["named:work"]; len(w.Allowed) != 0 || len(w.Excluded) != 1 || w.Excluded[0].HolderState != HolderActive {
			t.Fatalf("the profile still on the old destination stays left out, not sending in parallel: %+v", w)
		}
		// Then the other follows.
		res = h.step(e.profile("default", "two", shared, own), e.profile("named:work", "two", shared))
		r = h.mustRoute(shared)
		if len(r.Aliases) != 0 || r.Owner != "default" || r.Destination != testDest("two") {
			t.Fatalf("once nobody is on the old destination the alias goes: %+v", r)
		}
		if w := res.Jobs["named:work"]; len(w.Allowed) != 0 || len(w.Excluded) != 1 {
			t.Fatalf("work is still left out under the new destination: %+v", w)
		}
	})
	t.Run("the other profile moves first", func(t *testing.T) {
		e, h, shared, own := setup(t)
		res := h.step(e.profile("default", "one", shared, own), e.profile("named:work", "two", shared))
		if w := res.Jobs["named:work"]; !reflect.DeepEqual(w.Allowed, []string{shared}) || len(w.Excluded) != 0 {
			t.Fatalf("a different destination is a separate fan-out, as it has always been: %+v", w)
		}
		if len(h.routes.Routes) != 3 {
			t.Fatalf("work reserves the source under its own destination: %+v", h.routes.Routes)
		}
		first := h.mustRoute(shared) // the owner's, found first in the file
		// Then the owner follows: the earlier reservation wins and the later one goes,
		// once work no longer reads it. Until then nobody reads the source there.
		res = h.step(e.profile("default", "two", shared, own), e.profile("named:work", "two", shared))
		kept := h.mustRoute(shared)
		if kept.ID != first.ID || kept.Owner != "default" || kept.Destination != testDest("two") || len(kept.Aliases) != 0 {
			t.Fatalf("the earlier reservation must win: %+v (was %+v)", kept, first)
		}
		if len(h.routes.Routes) != 3 || len(res.Losers) != 1 || !slices.Equal(res.Commits, res.Losers) {
			t.Fatalf("the later reservation is let go of, to be removed: routes %+v losers %v commits %v", h.routes.Routes, res.Losers, res.Commits)
		}
		if d, w := res.Jobs["default"], res.Jobs["named:work"]; slices.Contains(d.Allowed, shared) || len(w.Allowed) != 0 {
			t.Fatalf("while the later reservation stands, nobody reads the source: default %+v work %+v", d, w)
		}
		h.removeLosers(res)
		res = h.step(e.profile("default", "two", shared, own), e.profile("named:work", "two", shared))
		if len(h.routes.Routes) != 2 || !slices.Contains(res.Jobs["default"].Allowed, shared) {
			t.Fatalf("once it is removed the owner reads the source: %+v %+v", h.routes.Routes, res.Jobs["default"])
		}
		if w := res.Jobs["named:work"]; len(w.Allowed) != 0 || len(w.Excluded) != 1 || w.Excluded[0].HolderState != HolderActive {
			t.Fatalf("work is left out again, the owner did not change: %+v", w)
		}
	})
	t.Run("the earlier reservation wins even when its owner sorts later", func(t *testing.T) {
		e := newPlanEnv(t)
		h := newHistory(e)
		shared := e.line("codex", "shared")
		h.step(e.profile("named:work", "one", shared))
		h.step(e.profile("named:work", "one", shared), e.profile("default", "two", shared))
		if len(h.routes.Routes) != 2 {
			t.Fatalf("two destinations, two reservations: %+v", h.routes.Routes)
		}
		res := h.step(e.profile("named:work", "two", shared), e.profile("default", "two", shared))
		h.removeLosers(res)
		res = h.step(e.profile("named:work", "two", shared), e.profile("default", "two", shared))
		if len(h.routes.Routes) != 1 || h.routes.Routes[0].Owner != "named:work" {
			t.Fatalf("work reserved first and keeps it although default sorts first: %+v", h.routes.Routes)
		}
		if d := res.Jobs["default"]; !d.AllExcluded {
			t.Fatalf("default is left out of the source it started reading in parallel: %+v", d)
		}
	})
	t.Run("the owner moves back", func(t *testing.T) {
		e, h, shared, own := setup(t)
		h.step(e.profile("default", "two", shared, own), e.profile("named:work", "one", shared))
		h.step(e.profile("default", "one", shared, own), e.profile("named:work", "one", shared))
		if r := h.mustRoute(shared); r.Destination != testDest("one") || len(r.Aliases) != 0 {
			t.Fatalf("a destination the owner is back on is not an alias: %+v", r)
		}
	})
	t.Run("a releasing route does not follow its owner", func(t *testing.T) {
		e, h, shared, own := setup(t)
		r := h.mustRoute(shared)
		r.State, r.To = RouteReleasing, "named:work"
		h.routes.Routes[0] = r
		h.step(e.profile("default", "two", shared, own), e.profile("named:work", "one", shared))
		if got := h.mustRoute(shared); got.Destination != testDest("one") {
			t.Fatalf("the owner is leaving this route: %+v", got)
		}
	})
}

func TestPlanSameSinceKeepsTheSmallerOwnerWhenRoutesCollide(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	rt := func(id, owner, dest string) Route {
		return Route{ID: id, Rev: 1, Kind: "codex", Path: strings.TrimPrefix(shared, "codex:"), Destination: testDest(dest), Owner: owner, State: RouteActive, Since: testNow}
	}
	in := e.in(e.profile("default", "two", shared), e.profile("named:work", "two", shared))
	in.Routes = RouteFile{Version: 1, Routes: []Route{rt("00000d01", "named:work", "two"), rt("00000d02", "default", "one")}}
	res := e.plan(in)
	if !slices.Equal(res.Losers, []string{"00000d01"}) || res.Routes.Routes[0].State != RouteReleasing || res.Routes.Routes[1].State != RouteActive {
		t.Fatalf("with equal times the owner that sorts first keeps it: %v %+v", res.Losers, res.Routes.Routes)
	}
}

// A folder replaced by a link to another makes two routes one source. The plan
// does not leave both profiles shut out of it for good: the earlier route stays,
// and the other is let go of, and removed once its owner has stopped reading it.
func TestPlanResolvesRoutesTheDiskMadeOne(t *testing.T) {
	e := newPlanEnv(t)
	h := newHistory(e)
	a, b := e.line("codex", "codex-a"), e.line("codex", "codex-b")
	for _, line := range []string{a, b} {
		if err := os.MkdirAll(filepath.Join(strings.TrimPrefix(line, "codex:"), "sessions"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	profiles := func() []PlanProfile {
		return []PlanProfile{e.profile("default", "one", a), e.profile("named:work", "one", b)}
	}
	h.step(profiles()...)
	theirs := h.mustRoute(b)
	if err := os.RemoveAll(strings.TrimPrefix(b, "codex:")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(strings.TrimPrefix(a, "codex:"), strings.TrimPrefix(b, "codex:")); err != nil {
		t.Skip("symlinks unavailable")
	}
	h.held = map[string][]string{"default": {a}, "named:work": {b}}
	res := h.step(profiles()...)
	if !res.Changed || !slices.Equal(res.Losers, []string{theirs.ID}) || len(res.Commits) != 0 {
		t.Fatalf("the later route loses, and waits for its owner: changed=%v losers=%v commits=%v", res.Changed, res.Losers, res.Commits)
	}
	if got := h.mustRoute(b); got.State != RouteReleasing || got.Owner != "named:work" {
		t.Fatalf("the losing route is let go of: %+v", got)
	}
	for _, id := range []string{"default", "named:work"} {
		if len(res.Jobs[id].Allowed) != 0 {
			t.Fatalf("%s reads the folder while the other may still read it: %+v", id, res.Jobs[id])
		}
	}
	h.held = map[string][]string{}
	res = h.step(profiles()...)
	if !slices.Equal(res.Commits, []string{theirs.ID}) {
		t.Fatalf("once its owner holds nothing of it, the losing route can go: %v", res.Commits)
	}
	h.removeLosers(res)
	res = h.step(profiles()...)
	if len(h.routes.Routes) != 1 || !slices.Equal(res.Jobs["default"].Allowed, []string{a}) {
		t.Fatalf("the earlier route is read again: %+v %+v", h.routes.Routes, res.Jobs["default"])
	}
	if w := res.Jobs["named:work"]; !w.AllExcluded || w.Excluded[0].Holder != "default" || w.Excluded[0].HolderState != HolderActive {
		t.Fatalf("the other profile is left out of the folder it shares: %+v", w)
	}
}

// A route made while the clock ran more than a day ahead is dated in the future
// once the clock is put right, and a file with such a date is refused. The plan
// dates it now, so the daemon can still write its file and read it at its next
// start.
// A collision that passes before the losing route's owner has let go of it -- the
// link put back, a policy that went and came back -- leaves the route with its
// owner, who still has the source. Handing it over would give it to nobody. A
// cursor hand-over the collision interrupted is taken up again.
func TestPlanGivesALosingRouteBackWhenTheCollisionPasses(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%v", pending), func(t *testing.T) {
			e := newPlanEnv(t)
			h := newHistory(e)
			a, b := e.line("codex", "codex-a"), e.line("codex", "codex-b")
			fa, fb := strings.TrimPrefix(a, "codex:"), strings.TrimPrefix(b, "codex:")
			for _, f := range []string{fa, fb} {
				if err := os.MkdirAll(filepath.Join(f, "sessions"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			profiles := func() []PlanProfile {
				return []PlanProfile{e.profile("default", "one", a), e.profile("named:work", "one", b)}
			}
			h.step(profiles()...)
			theirs := h.mustRoute(b)
			if pending {
				for i := range h.routes.Routes {
					if h.routes.Routes[i].ID == theirs.ID {
						h.routes.Routes[i].From, h.routes.Routes[i].Handoff = "named:old", testHandoff("h1")
					}
				}
			}
			sessions := filepath.Join(fb, "sessions")
			if err := os.Remove(sessions); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(fa, "sessions"), sessions); err != nil {
				t.Skip("symlinks unavailable")
			}
			h.held = map[string][]string{"default": {a}, "named:work": {b}}
			if res := h.step(profiles()...); !slices.Equal(res.Losers, []string{theirs.ID}) {
				t.Fatalf("setup: losers %v", res.Losers)
			}
			// The link is put back before work's job has let go of the line.
			if err := os.Remove(sessions); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(sessions, 0700); err != nil {
				t.Fatal(err)
			}
			h.held = map[string][]string{"default": {a}}
			res := h.step(profiles()...)
			got := h.mustRoute(b)
			if got.State != RouteActive || got.Owner != "named:work" || slices.Contains(res.Commits, theirs.ID) || len(res.Losers) != 0 {
				t.Fatalf("the route goes back to its owner, not on to nobody: %+v commits=%v losers=%v", got, res.Commits, res.Losers)
			}
			if w := res.Jobs["named:work"]; !slices.Equal(w.Allowed, []string{b}) || len(w.Excluded) != 0 {
				t.Fatalf("work reads its own folder again: %+v", w)
			}
			if pending && (got.From != "named:old" || got.Handoff == "" || len(res.Jobs["named:work"].Handoffs) != 1) {
				t.Fatalf("the cursor work was to take over is still to be taken over: %+v %+v", got, res.Jobs["named:work"].Handoffs)
			}
			if !pending && (got.From != "" || got.Handoff != "") {
				t.Fatalf("nothing is to be taken over: %+v", got)
			}
		})
	}
}

func TestPlanBringsAFutureSinceBackToNow(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	in := e.in(e.profile("default", "one", shared), e.profile("named:work", "one", shared))
	in.Routes = RouteFile{Version: 1, Rev: 3, Routes: []Route{{ID: "00000a01", Rev: 2, Kind: "codex", Path: strings.TrimPrefix(shared, "codex:"), Destination: testDest("one"), Owner: "named:work", State: RouteActive, Since: testNow.Add(48 * time.Hour)}}}
	res := e.plan(in)
	r := res.Routes.Routes[0]
	if !res.Changed || !r.Since.Equal(testNow) || r.Rev != 3 || r.Owner != "named:work" {
		t.Fatalf("the route keeps its owner and is dated now: changed=%v %+v", res.Changed, r)
	}
	in.Routes.Routes[0].Since = testNow.Add(time.Hour)
	if res := e.plan(in); res.Changed {
		t.Fatalf("a date the reader accepts is left alone: %+v", res.Routes.Routes)
	}
}

func TestPlanAliasesAreBoundedAndOnlyKeptWhileSomeoneNeedsThem(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	rt := Route{ID: "00000e01", Rev: 1, Kind: "codex", Path: strings.TrimPrefix(shared, "codex:"), Destination: testDest("d0"), Aliases: []string{testDest("a1"), testDest("a2"), testDest("a3"), testDest("a4")}, Owner: "default", State: RouteActive, Since: testNow}
	var users []PlanProfile
	for i := 1; i <= 4; i++ {
		users = append(users, e.profile(fmt.Sprintf("named:u%d", i), fmt.Sprintf("a%d", i), shared))
	}
	run := func(owner PlanProfile, others ...PlanProfile) Route {
		in := e.in(append([]PlanProfile{owner}, others...)...)
		in.Routes = RouteFile{Version: 1, Routes: []Route{rt}}
		res := e.plan(in)
		return res.Routes.Routes[0]
	}
	t.Run("an alias nobody is on goes first", func(t *testing.T) {
		got := run(e.profile("default", "d5", shared), users[1:]...)
		if got.Destination != testDest("d5") || !reflect.DeepEqual(got.Aliases, []string{testDest("a2"), testDest("a3"), testDest("a4")}) {
			t.Fatalf("d0 and a1 are unused and must be dropped: %+v", got)
		}
	})
	t.Run("when every alias is in use the oldest goes", func(t *testing.T) {
		in := e.in(append([]PlanProfile{e.profile("default", "d5", shared), e.profile("named:u0", "d0", shared)}, users...)...)
		in.Routes = RouteFile{Version: 1, Routes: []Route{rt}}
		got := e.plan(in).Routes.Routes[0]
		if len(got.Aliases) != maxAliases || got.Aliases[0] != testDest("a2") || got.Aliases[3] != testDest("d0") {
			t.Fatalf("aliases = %v", got.Aliases)
		}
	})
}

func TestPlanRemovesRoutesNobodyOverlapsAnymore(t *testing.T) {
	e := newPlanEnv(t)
	shared, other := e.line("codex", "shared"), e.line("claude", "other")
	tests := []struct {
		name  string
		route func(r *Route)
		who   []PlanProfile // the profiles left, besides default's other line
		keep  bool
	}{
		{"orphan nobody overlaps", func(*Route) {}, nil, false},
		{"orphan a profile still overlaps", func(*Route) {}, []PlanProfile{e.profile("named:work", "one", shared)}, true},
		{"orphan a profile on another destination overlaps", func(*Route) {}, []PlanProfile{e.profile("named:work", "two", shared)}, false},
		{"orphan whose alias a profile is on", func(r *Route) { r.Aliases = []string{testDest("two")} }, []PlanProfile{e.profile("named:work", "two", shared)}, true},
		{"tombstone nobody overlaps", func(r *Route) { r.Owner, r.State, r.From = "", RouteUnowned, "default" }, nil, false},
		{"tombstone a profile still overlaps", func(r *Route) { r.Owner, r.State, r.From = "", RouteUnowned, "default" }, []PlanProfile{e.profile("named:work", "one", shared)}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := Route{ID: "00000f01", Rev: 2, Kind: "codex", Path: strings.TrimPrefix(shared, "codex:"), Destination: testDest("one"), Owner: "named:gone", State: RouteActive, Since: testNow}
			tc.route(&rt)
			in := e.in(append([]PlanProfile{e.profile("default", "one", other)}, tc.who...)...)
			in.Routes = RouteFile{Version: 1, Routes: []Route{rt}}
			res := e.plan(in)
			if kept := len(res.Routes.Routes) > 0 && res.Routes.Routes[0].ID == rt.ID; kept != tc.keep {
				t.Fatalf("kept = %v, want %v: %+v", kept, tc.keep, res.Routes.Routes)
			}
		})
	}
}

func TestPlanReadOnlyPlanAnswersAsTheDaemonWouldWithoutSavingIt(t *testing.T) {
	e := newPlanEnv(t)
	shared, other := e.line("codex", "shared"), e.line("claude", "other")
	h := newHistory(e)
	h.step(e.profile("default", "one", shared), e.profile("named:work", "one", shared))
	in := e.in(e.profile("default", "one", other), e.profile("named:work", "one", shared))
	in.Routes, in.Persist = h.routes, false
	res := e.plan(in)
	if !reflect.DeepEqual(res.Routes, h.routes) || res.Changed {
		t.Fatal("a read-only plan hands back the file it was given even when it would release a route")
	}
	if w := res.Jobs["named:work"]; len(w.Allowed) != 0 || len(w.Excluded) != 1 || w.Excluded[0].HolderState != HolderTransferring {
		t.Fatalf("it still answers as the daemon would: %+v", w)
	}
}

// applyCommits plays the supervisor's part of a hand-over for the routes Plan
// says are ready: the next owner (if any) takes the route with its cursor
// pending. It keeps what Plan promised about the file.
func applyCommits(f RouteFile, ids []string, handoff func() string) RouteFile {
	out := RouteFile{Version: f.Version, Rev: f.Rev, Routes: append([]Route(nil), f.Routes...)}
	for _, id := range ids {
		for i, r := range out.Routes {
			if r.ID != id {
				continue
			}
			from := r.From
			if from == "" {
				from = r.Owner
			}
			if r.To == "" {
				r.Owner, r.State, r.From, r.Handoff = "", RouteUnowned, from, ""
			} else {
				r.Owner, r.State, r.From, r.Handoff = r.To, RouteActive, from, handoff()
			}
			r.To = ""
			r.Rev++
			out.Routes[i] = r
		}
	}
	return out
}

// Whatever the profiles do -- add and drop sources, move to another destination,
// stop, get disabled, vanish and come back -- a plan keeps promises the rest of
// the design rests on: the file it writes is one the reader accepts, a plan over
// its own file changes nothing, no two profiles that can collect read
// overlapping lines for one destination, and a route's owner is changed only by
// the supervisor's commit, never by Plan.
func TestPlanKeepsItsPromisesOverRandomHistories(t *testing.T) {
	ids := []string{"default", "named:a", "named:b", "named:c"}
	universe := []string{
		"codex:hx", "codex:hx/sessions/y1", "codex:hy",
		"claude:c1", "claude:c2", "claude:c1/other", "claude:c1/projects/q",
		"gjc:g1", "omo:o1", "omo:o1/agent",
	}
	for seed := range 3 {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := newTestRand(seed)
			e := newPlanEnv(t)
			h := newHistory(e)
			type state struct {
				present, stopped, disabled bool
				dest                       string
				lines                      map[string]bool
			}
			world := map[string]*state{}
			for _, id := range ids {
				world[id] = &state{present: true, dest: "d1", lines: map[string]bool{}}
			}
			for _, l := range universe[:3] {
				world["default"].lines[l] = true
			}
			var log []string
			handoffs := 0
			for step := range 80 {
				id := ids[rng.Intn(len(ids))]
				w := world[id]
				switch op := rng.Intn(7); op {
				case 0, 1, 2:
					l := universe[rng.Intn(len(universe))]
					w.lines[l] = !w.lines[l]
					log = append(log, fmt.Sprintf("%s toggles %s", id, l))
				case 3:
					w.dest = []string{"d1", "d2"}[rng.Intn(2)]
					log = append(log, fmt.Sprintf("%s moves to %s", id, w.dest))
				case 4:
					w.stopped = !w.stopped
					log = append(log, fmt.Sprintf("%s stopped=%v", id, w.stopped))
				case 5:
					w.disabled = !w.disabled
					log = append(log, fmt.Sprintf("%s disabled=%v", id, w.disabled))
				case 6:
					w.present = !w.present
					log = append(log, fmt.Sprintf("%s present=%v", id, w.present))
				}
				var profiles []PlanProfile
				for _, pid := range ids {
					s := world[pid]
					if !s.present {
						continue
					}
					var lines []string
					for _, l := range universe {
						if s.lines[l] {
							kind, name, _ := strings.Cut(l, ":")
							lines = append(lines, e.line(kind, name))
						}
					}
					p := e.profile(pid, s.dest, lines...)
					p.Stopped, p.Enabled = s.stopped, !s.disabled
					profiles = append(profiles, p)
				}
				before := h.routes
				res := h.step(profiles...)
				fail := func(format string, args ...any) {
					t.Helper()
					t.Fatalf("step %d after %v: %s", step, log[max(0, len(log)-6):], fmt.Sprintf(format, args...))
				}
				// Ownership is never changed by Plan itself.
				prev := map[string]Route{}
				for _, r := range before.Routes {
					prev[r.ID] = r
				}
				for _, r := range res.Routes.Routes {
					if old, ok := prev[r.ID]; ok && (old.Owner != r.Owner || old.Kind != r.Kind || old.Path != r.Path || old.Since != r.Since) {
						fail("Plan changed route %s identity: %+v -> %+v", r.ID, old, r)
					}
				}
				// No two profiles that can collect read overlapping lines for one destination.
				view := newFSView()
				for _, a := range profiles {
					for _, b := range profiles {
						if a.ID >= b.ID || a.Destination != b.Destination || !a.eligible() || !b.eligible() {
							continue
						}
						for _, la := range res.Jobs[a.ID].Allowed {
							for _, lb := range res.Jobs[b.ID].Allowed {
								ka, fa, _ := strings.Cut(la, ":")
								kb, fb, _ := strings.Cut(lb, ":")
								if ka == kb && view.shape(view.roots(ka, fa), view.roots(kb, fb)) != "" {
									fail("%s and %s both read overlapping %s and %s", a.ID, b.ID, la, lb)
								}
							}
						}
					}
				}
				// The daemon's next plan sees what these jobs hold, and commits what is ready.
				h.held = map[string][]string{}
				for pid, job := range res.Jobs {
					h.held[pid] = job.Allowed
				}
				h.removeLosers(res)
				commits := slices.DeleteFunc(slices.Clone(res.Commits), func(id string) bool { return slices.Contains(res.Losers, id) })
				if len(commits) > 0 {
					h.routes = applyCommits(h.routes, commits, func() string { handoffs++; return testHandoff(fmt.Sprint("r", handoffs)) })
					if err := ValidateRoutes(h.routes, testNow.Add(time.Hour)); err != nil {
						fail("a commit produced an invalid file: %v", err)
					}
				}
				// A new owner finishes taking over (the supervisor clears the handoff).
				if step%3 == 0 {
					for i := range h.routes.Routes {
						h.routes.Routes[i].From, h.routes.Routes[i].Handoff = "", ""
					}
				}
			}
		})
	}
}

func newTestRand(seed int) *rand.Rand { return rand.New(rand.NewSource(int64(seed) + 1)) }
