package localdaemon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gofrs/flock"
)

// handoffWorld has default holding shared and work waiting for it, each with a
// line of its own besides.
func handoffWorld(t *testing.T) (r *routed, shared, mine string) {
	t.Helper()
	r = newRouted(t)
	shared, mine = r.env.line("codex", "shared"), r.env.line("claude", "mine")
	def := r.add("default", "one", mine, shared)
	def.blockRun = true
	r.add("named:work", "one", shared)
	return r, shared, mine
}

func dropLine(line string) func(p *rprofile) {
	return func(p *rprofile) {
		p.lines = slices.DeleteFunc(slices.Clone(p.lines), func(l string) bool { return l == line })
	}
}

func TestHandoffMovesASourceTheOwnerDroppedToAQualifyingProfile(t *testing.T) {
	t.Parallel()
	r, shared, mine := handoffWorld(t)
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	before := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "default" && rt.State == RouteActive })

	r.edit("default", dropLine(shared))

	after := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.State == RouteActive && rt.Handoff != "" })
	if after.ID != before.ID || after.From != "default" || after.To != "" {
		t.Fatalf("the hand-over must keep the reservation and say whose cursor it continues: %+v", after)
	}
	// The new owner reads the line, told it came from default, and the daemon then
	// clears the pending hand-over.
	work := r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	if work.ErrorCode != "" || work.State == "blocked" || !work.RouteOwner {
		t.Fatalf("work: %+v", work)
	}
	var got Allocation
	for _, a := range r.allocations("named:work") {
		if len(a.Handoffs) > 0 {
			got = a
		}
	}
	if len(got.Handoffs) != 1 || got.Handoffs[0] != (Handoff{Line: shared, From: "default", ID: after.Handoff}) || !slices.Equal(got.Allowed, []string{shared}) {
		t.Fatalf("work's allocation = %+v", got)
	}
	cleared := r.waitRoute(shared, func(rt Route) bool { return rt.Handoff == "" && rt.From == "" })
	if cleared.Owner != "named:work" || cleared.State != RouteActive {
		t.Fatalf("after adoption: %+v", cleared)
	}
	// default went on reading what it still has, and never at the same time as work.
	def := r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	if def.ErrorCode != "" {
		t.Fatalf("default: %+v", def)
	}
	if last := r.allocations("default"); !slices.Equal(last[len(last)-1].Allowed, []string{mine}) {
		t.Fatalf("default's last allocation = %+v", last)
	}
	r.noOverlap()
}

func TestHandoffLeavesASourceUnownedWhenNoProfileQualifies(t *testing.T) {
	t.Parallel()
	r, shared, _ := handoffWorld(t)
	r.edit("default", func(p *rprofile) { p.filters = Filters{RepoPrefixes: []string{"host/org/"}} })
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })

	r.edit("default", dropLine(shared))

	tomb := r.waitRoute(shared, func(rt Route) bool { return rt.State == RouteUnowned })
	if tomb.Owner != "" || tomb.From != "default" || tomb.Handoff != "" {
		t.Fatalf("a profile that would send more than the owner did must not get it: %+v", tomb)
	}
	r.waitLoads(10)
	if r.everAllowed("named:work", shared) {
		t.Fatal("work read a source nobody gave it")
	}
	// Loads count refreshes, not successful reads of an atomically replaced
	// snapshot. A concurrent replacement can make the single read return
	// ErrMetadataChanged, which job reports as absent. Await the published state.
	r.waitJob("named:work", func(work JobStatus) bool {
		return work.ErrorCode == "duplicate_route" && len(work.SharedSources) == 1 && work.SharedSources[0].HolderState == HolderUnowned
	})
}

func TestHandoffWaitsForALineLockHeldByAnotherRun(t *testing.T) {
	t.Parallel()
	r, shared, _ := handoffWorld(t)
	r.start()
	held := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "default" })
	// A one-shot run of the previous owner is reading the line.
	oneShot, err := LockLines(context.Background(), r.root, []string{held.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(oneShot)

	r.edit("default", dropLine(shared))
	r.waitRoute(shared, func(rt Route) bool { return rt.State == RouteReleasing })
	r.waitLoads(15)
	if rt, _ := r.routeOf(shared); rt.State != RouteReleasing || rt.Owner != "default" {
		t.Fatalf("the owner changed under a run that still reads the line: %+v", rt)
	}
	if r.everAllowed("named:work", shared) {
		t.Fatal("work started on a line another run was reading")
	}

	oneShot()
	r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" })
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	r.noOverlap()
}

func TestHandoffWaitsForTheOwnersProfileLock(t *testing.T) {
	t.Parallel()
	r, shared, mine := handoffWorld(t)
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "default" })
	// Stopped, default holds no lease. A run that does not know the line locks --
	// an older binary's hook child -- would hold only the profile's own lock.
	if err := r.command("stop", "default"); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(r.root, "rt", "default", "sync.lock"))
	if ok, err := lock.TryLock(); err != nil || !ok {
		t.Fatalf("fixture lock: %v %v", ok, err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })

	r.edit("default", func(p *rprofile) { p.lines = []string{mine} })
	r.waitRoute(shared, func(rt Route) bool { return rt.State == RouteReleasing })
	r.waitLoads(15)
	if rt, _ := r.routeOf(shared); rt.State != RouteReleasing {
		t.Fatalf("handed over while a run held the previous owner's profile lock: %+v", rt)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" })
}

func TestHandoffSurvivesARestartInEveryPhase(t *testing.T) {
	t.Parallel()
	r, shared, mine := handoffWorld(t)
	r.profs["named:work"].failAcquire = 1 // the new owner cannot take its lease at first
	stop := r.start()
	held := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "default" })
	oneShot, err := LockLines(context.Background(), r.root, []string{held.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(oneShot)
	r.edit("default", dropLine(shared))
	r.waitRoute(shared, func(rt Route) bool { return rt.State == RouteReleasing })

	// Phase one: restarted while the route is being released.
	stop()
	stop = r.start()
	r.waitLoads(5)
	if rt, _ := r.routeOf(shared); rt.State != RouteReleasing {
		t.Fatalf("restart lost the release: %+v", rt)
	}
	oneShot()
	// Phase two: the owner has changed but the new owner has not taken over.
	pending := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.Handoff != "" })
	stop()
	r.mu.Lock()
	r.profs["named:work"].failAcquire = 0
	r.mu.Unlock()
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	done := r.waitRoute(shared, func(rt Route) bool { return rt.Handoff == "" })
	if done.Owner != "named:work" || done.From != "" {
		t.Fatalf("after the restart: %+v", done)
	}
	var seen bool
	for _, a := range r.allocations("named:work") {
		for _, h := range a.Handoffs {
			seen = seen || (h.ID == pending.Handoff && h.From == "default")
		}
	}
	if !seen {
		t.Fatalf("the pending hand-over was not handed to the new owner's lease: %+v", r.allocations("named:work"))
	}
	if last := r.allocations("default"); !slices.Equal(last[len(last)-1].Allowed, []string{mine}) {
		t.Fatalf("default: %+v", last)
	}
	r.noOverlap()
}

// A lease that could not take the previous holder's cursor -- the folder failed a
// check at that moment -- leaves the hand-over on the route, and the next lease is
// given it again, rather than reading the folder later as if nobody had held it.
func TestHandoffStaysUntilALeaseTakesIt(t *testing.T) {
	t.Parallel()
	r, shared, _ := handoffWorld(t)
	r.profs["named:work"].declineHandoffs = true
	stop := r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	r.edit("default", dropLine(shared))
	pending := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.Handoff != "" })
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	r.waitLoads(3)
	if rt, _ := r.routeOf(shared); rt.Handoff != pending.Handoff || rt.From != pending.From {
		t.Fatalf("a hand-over the lease did not take was cleared: %+v, was %+v", rt, pending)
	}

	stop()
	r.mu.Lock()
	r.profs["named:work"].declineHandoffs = false
	leases := len(r.allocs["named:work"])
	r.mu.Unlock()
	r.start()
	r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.Handoff == "" && rt.From == "" })
	var offered bool
	for _, a := range r.allocations("named:work")[leases:] {
		offered = offered || slices.Contains(a.Handoffs, Handoff{Line: shared, From: pending.From, ID: pending.Handoff})
	}
	if !offered {
		t.Fatalf("the next lease was not given the hand-over: %+v", r.allocations("named:work"))
	}
}

func TestHandoffLogsEachStepOnce(t *testing.T) {
	t.Parallel()
	r, shared, _ := handoffWorld(t)
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	r.edit("default", dropLine(shared))
	r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.Handoff == "" && rt.From == "" })
	r.waitLoads(10)
	count := func(prefix string) int {
		n := 0
		for _, l := range r.logLines() {
			if strings.HasPrefix(l, prefix) {
				n++
			}
		}
		return n
	}
	if count("[routes] releasing source=") != 1 || count("[routes] handoff source=") != 1 {
		t.Fatalf("log = %q", r.logLines())
	}
	for _, l := range r.logLines() {
		if strings.HasPrefix(l, "[routes] handoff") && !strings.HasSuffix(l, " from=default to=work") {
			t.Fatalf("handoff line = %s", l)
		}
		if strings.Contains(l, r.root) {
			t.Fatalf("a log line names a path: %s", l)
		}
	}
}
