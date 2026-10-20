package localdaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cctrace/internal/atomicfile"
)

func (r *routed) revision(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.profs[id]; p != nil && p.present {
		return fmt.Sprint(p.revision)
	}
	return ""
}

// claimFor is the request a CLI would send for a line of target: what it sees now.
func (r *routed) claimFor(target, line string) ClaimRequest {
	r.t.Helper()
	route, ok := r.routeOf(line)
	if !ok {
		r.t.Fatalf("no route for %s", line)
	}
	return ClaimRequest{Profile: target, Source: route.ID, Holder: route.Owner, RouteRev: route.Rev, TargetRev: r.revision(target), HolderRev: r.revision(route.Owner)}
}

func (r *routed) claim(req ClaimRequest) error {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return Claim(ctx, r.root, req)
}

func codeOf(err error) string {
	var control *ControlError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &control):
		return control.Code
	}
	return "other: " + err.Error()
}

func TestClaimMovesASourceToTheProfileThatAsksForIt(t *testing.T) {
	t.Parallel()
	r, shared, mine := handoffWorld(t)
	r.start()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })

	if err := r.claim(r.claimFor("named:work", shared)); err != nil {
		t.Fatal(err)
	}
	moved := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.State == RouteActive })
	if moved.From != "default" && moved.From != "" {
		t.Fatalf("route %+v", moved)
	}
	work := r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	if work.ErrorCode != "" || !work.RouteOwner {
		t.Fatalf("work: %+v", work)
	}
	var seen bool
	for _, a := range r.allocations("named:work") {
		for _, h := range a.Handoffs {
			seen = seen || (h.Line == shared && h.From == "default")
		}
	}
	if !seen {
		t.Fatalf("work was not told the source came from default: %+v", r.allocations("named:work"))
	}
	r.waitRoute(shared, func(rt Route) bool { return rt.Handoff == "" && rt.From == "" })
	def := r.waitJob("default", func(j JobStatus) bool { return len(j.SharedSources) == 1 })
	if def.ErrorCode != "" || def.SharedSources[0].Holder != "named:work" {
		t.Fatalf("default: %+v", def)
	}
	if last := r.allocations("default"); !slices.Equal(last[len(last)-1].Allowed, []string{mine}) {
		t.Fatalf("default keeps what is its own: %+v", last)
	}
	r.noOverlap()
	if rt, _ := r.routeOf(shared); rt.Rev < 3 {
		t.Fatalf("each step advances the route's revision: %+v", rt)
	}
}

func TestClaimRefusesWhatHasChangedSinceTheCallerLooked(t *testing.T) {
	t.Parallel()
	r, shared, _ := handoffWorld(t)
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	good := r.claimFor("named:work", shared)

	tests := []struct {
		name string
		mod  func(q *ClaimRequest)
		want string
	}{
		{"no such profile", func(q *ClaimRequest) { q.Profile = "named:nobody" }, "profile_unavailable"},
		{"no such route", func(q *ClaimRequest) { q.Source = "0000ffff" }, "route_unknown"},
		{"the route moved on", func(q *ClaimRequest) { q.RouteRev++ }, "route_changed"},
		{"someone else holds it now", func(q *ClaimRequest) { q.Holder = "named:other" }, "route_changed"},
		{"the taker was edited", func(q *ClaimRequest) { q.TargetRev = "99" }, "profile_changed"},
		{"the holder was edited", func(q *ClaimRequest) { q.HolderRev = "99" }, "profile_changed"},
		{"the holder already holds it", func(q *ClaimRequest) { q.Profile = "default"; q.TargetRev = r.revision("default") }, "invalid_source"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := good
			tc.mod(&q)
			if got := codeOf(r.claim(q)); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
			if rt, _ := r.routeOf(shared); rt.Owner != "default" || rt.State != RouteActive {
				t.Fatalf("a refused claim changed the route: %+v", rt)
			}
		})
	}
}

func TestClaimNeedsATargetThatCanCollect(t *testing.T) {
	t.Parallel()
	r, shared, _ := handoffWorld(t)
	r.add("named:off", "one", shared).disabled = true
	r.add("named:paused", "one", shared)
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	r.waitJob("named:off", func(j JobStatus) bool { return j.State == "disabled" })
	if err := r.command("stop", "named:paused"); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"named:off", "named:paused"} {
		q := r.claimFor(target, shared)
		if got := codeOf(r.claim(q)); got != "profile_unavailable" {
			t.Fatalf("%s: code = %q", target, got)
		}
	}
	var control *ControlError
	if err := r.claim(r.claimFor("named:paused", shared)); !errors.As(err, &control) || control.Reason != "stopped" {
		t.Fatalf("reason = %+v", control)
	}
}

// Only what is said about a profile is capped; a source past the cap is still
// one it is held back from, and can be claimed.
func TestClaimReachesASourcePastTheWarningCap(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	var lines []string
	for i := range maxSharedSources + 8 {
		lines = append(lines, r.env.line("claude", fmt.Sprintf("many/%d", i)))
	}
	r.add("default", "one", lines...)
	r.add("named:work", "one", lines...)
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	last := lines[len(lines)-1]
	if err := r.claim(r.claimFor("named:work", last)); err != nil {
		t.Fatalf("the %dth source: %v", len(lines), err)
	}
	r.waitRoute(last, func(rt Route) bool { return rt.State == RouteReleasing || rt.Owner == "named:work" })
}

func TestClaimIsRefusedWhileAHandOverIsUnderway(t *testing.T) {
	t.Parallel()
	r, shared, _ := handoffWorld(t)
	r.add("named:third", "one", shared)
	r.start()
	held := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "default" })
	r.waitJob("named:third", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	// A run of the owner's that keeps the line stalls the hand-over in its first phase.
	oneShot, err := LockLines(context.Background(), r.root, []string{held.ID}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(oneShot)

	if err := r.claim(r.claimFor("named:work", shared)); err != nil {
		t.Fatal(err)
	}
	r.waitRoute(shared, func(rt Route) bool { return rt.State == RouteReleasing && rt.To == "named:work" })
	if got := codeOf(r.claim(r.claimFor("named:third", shared))); got != "handoff_pending" {
		t.Fatalf("a claim on a route being released: %q", got)
	}
	oneShot()
	// The new owner has the line but has not yet adopted the previous cursor.
	r.profs["named:work"].failAcquire = 1000
	pending := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.Handoff != "" })
	q := r.claimFor("named:third", shared)
	if got := codeOf(r.claim(q)); got != "handoff_pending" {
		t.Fatalf("a claim before the new owner adopted the cursor: %q (%+v)", got, pending)
	}
	if rt, _ := r.routeOf(shared); rt.Owner != "named:work" || rt.From != pending.From || rt.Handoff != pending.Handoff {
		t.Fatalf("the refusal changed the route: %+v", rt)
	}
}

func TestClaimOfASourceNobodyReadsIsImmediate(t *testing.T) {
	t.Parallel()
	t.Run("a tombstone", func(t *testing.T) {
		r, shared, _ := handoffWorld(t)
		r.edit("default", func(p *rprofile) { p.filters = Filters{RepoPrefixes: []string{"host/org/"}} })
		r.start()
		r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
		r.edit("default", dropLine(shared))
		r.waitRoute(shared, func(rt Route) bool { return rt.State == RouteUnowned })

		if err := r.claim(r.claimFor("named:work", shared)); err != nil {
			t.Fatal(err)
		}
		got := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.State == RouteActive })
		if got.From != "default" && got.From != "" {
			t.Fatalf("a tombstone remembers its last holder: %+v", got)
		}
		r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 && j.ErrorCode == "" })
		r.noOverlap()
	})
	t.Run("a deleted owner's source", func(t *testing.T) {
		r, shared, _ := handoffWorld(t)
		r.start()
		r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
		r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
		q := func() ClaimRequest { return r.claimFor("named:work", shared) }
		r.mu.Lock()
		r.profs["default"].present = false
		r.mu.Unlock()
		r.waitJob("named:work", func(j JobStatus) bool {
			return len(j.SharedSources) == 1 && j.SharedSources[0].HolderState == HolderRemoved
		})
		// Removed describes registry membership, not Run/lease termination.
		// This immediate-claim case requires the reader to have actually exited
		// and its cached job to have been pruned, not merely cancellation sent.
		eventually(t, func() bool {
			if _, found := r.job("default"); found {
				return false
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.live[shared] == 0 && slices.Contains(r.events, "release default")
		})
		req := q()
		if req.HolderRev != "" {
			t.Fatalf("removed registry profile has revision %q", req.HolderRev)
		}
		if err := r.claim(req); err != nil {
			t.Fatal(err)
		}
		got := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.State == RouteActive })
		if got.From != "default" && got.From != "" {
			t.Fatalf("an orphan hands its cursor from its last owner: %+v", got)
		}
		r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 && j.ErrorCode == "" })
		r.noOverlap()
	})
}

// Registry removal is visible before a cancelled Run returns. Drive that
// boundary explicitly rather than racing the refresh loop against goroutine exit.
func TestClaimDeletedHolderUsesRegistryRevisionWhileRunDrains(t *testing.T) {
	t.Parallel()
	r, shared, _ := handoffWorld(t)
	entered, cancelled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s := New(r.config())
	load := s.config.Load
	s.config.Load = func() ([]Spec, error) {
		specs, err := load()
		for i := range specs {
			acquire := specs[i].Acquire
			specs[i].Acquire = func(ctx context.Context, a Allocation) (func(), error) {
				unlock, err := LockLines(ctx, r.root, a.Routes, 0)
				if err != nil {
					return nil, err
				}
				release, err := acquire(ctx, a)
				if err != nil {
					unlock()
					return nil, err
				}
				return func() { release(); unlock() }, nil
			}
			if specs[i].ID == "default" {
				specs[i].Run = func(ctx context.Context, _ func(Progress)) error {
					close(entered)
					<-ctx.Done()
					close(cancelled)
					<-finish
					return ctx.Err()
				}
			}
		}
		return specs, err
	}
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("lifecycle notification did not arrive")
		}
	}
	event := func() event {
		t.Helper()
		select {
		case e := <-s.events:
			return e
		case <-time.After(time.Second):
			t.Fatal("job event did not arrive")
			return event{}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	var finishOnce sync.Once
	finishRun := func() { finishOnce.Do(func() { close(finish) }) }
	t.Cleanup(func() {
		cancel()
		finishRun()
		s.stopAll()
		for s.activeCount() > 0 {
			s.handleEvent(event(), true)
		}
	})
	if err := s.begin(); err != nil {
		t.Fatal(err)
	}
	s.tick(ctx)
	await(entered) // The actual lease and line lock were acquired before Run.
	r.mu.Lock()
	r.profs["default"].present = false
	r.mu.Unlock()
	s.tick(ctx)
	await(cancelled) // Cancellation is observed, but Run cannot exit yet.
	holder, target := s.jobs["default"], s.jobs["named:work"]
	if !holder.gone || !holder.active || holder.held == nil || target.active {
		t.Fatalf("not at the draining boundary: holder=%+v target=%+v", holder, target)
	}
	sharedStatus := s.sharedSources(target)
	if len(sharedStatus) != 1 || sharedStatus[0].HolderState != HolderRemoved {
		t.Fatalf("caller must see the registry deletion: %+v", sharedStatus)
	}
	route, _ := r.routeOf(shared)
	req := commandRequest{ProfileID: "named:work", Source: route.ID, Holder: "default", RouteRev: route.Rev, TargetRev: target.spec.Revision}
	stale := req
	stale.HolderRev = "99"
	if reply := s.claim(stale); reply.Error != "profile_changed" {
		t.Fatalf("deleted registry revision must reject the old holder revision: %+v", reply)
	}
	if after, _ := r.routeOf(shared); !reflect.DeepEqual(after, route) {
		t.Fatalf("stale claim changed route: before=%+v after=%+v", route, after)
	}
	if reply := s.claim(req); reply.Error != "" {
		t.Fatalf("removed holder's registry revision is empty while its old run drains: %+v", reply)
	}
	s.route()
	pending, _ := r.routeOf(shared)
	if pending.State != RouteReleasing || pending.Owner != "default" || pending.To != "named:work" || target.active {
		t.Fatalf("claim must wait for the actual reader: %+v", pending)
	}
	stale.RouteRev, stale.HolderRev = pending.Rev, holder.spec.Revision
	if reply := s.claim(stale); reply.Error != "profile_changed" {
		t.Fatalf("the retained job revision is not a current registry revision: %+v", reply)
	}
	if after, _ := r.routeOf(shared); !reflect.DeepEqual(after, pending) {
		t.Fatalf("stale retained revision changed pending handoff: %+v", after)
	}
	if unlock, err := LockLines(ctx, r.root, []string{route.ID}, 0); err == nil {
		unlock()
		t.Fatal("deleted holder lost its line lock before Run exited")
	} else if !errors.Is(err, ErrLineBusy) {
		t.Fatal(err)
	}
	finishRun()
	for holder.active {
		s.handleEvent(event(), false)
	}
	if holder.held != nil || holder.release != nil {
		t.Fatal("exit did not release the deleted holder's lease")
	}
	s.tick(ctx)
	moved, _ := r.routeOf(shared)
	if moved.State != RouteActive || moved.Owner != "named:work" || moved.From != "default" || moved.Handoff == "" {
		t.Fatalf("cursor handoff must follow actual release: %+v", moved)
	}
	if !target.active || target.held == nil || len(target.held.Handoffs) != 1 || target.held.Handoffs[0].From != "default" {
		t.Fatalf("target did not receive the former holder's cursor: %+v", target.held)
	}
	for target.active {
		s.handleEvent(event(), false)
	}
	r.noOverlap()
}

// A claim that changes who reads what is acted on in the tick that answers it.
func TestClaimStartsTheProfileInTheSameTick(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:work", "one", shared)
	s := New(r.config())
	if err := s.begin(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.tick(ctx)
	if !s.jobs["default"].active || s.jobs["named:work"].active {
		t.Fatalf("setup: default %v work %v", s.jobs["default"].active, s.jobs["named:work"].active)
	}
	// The owner leaves the registry; its source is an orphan.
	r.mu.Lock()
	r.profs["default"].present = false
	r.mu.Unlock()
	for s.jobs["default"].active {
		s.handleEvent(<-s.events, false)
	}
	s.tick(ctx)
	req := r.claimFor("named:work", shared)
	req.HolderRev = ""
	dir := filepath.Join(r.root, "daemon-control")
	path := filepath.Join(dir, "claim.json")
	if err := writeJSON(path, commandRequest{InstanceID: s.runtime.InstanceID, Action: "claim", ProfileID: req.Profile, Source: req.Source, Holder: req.Holder, RouteRev: req.RouteRev, TargetRev: req.TargetRev, HolderRev: req.HolderRev}); err != nil {
		t.Fatal(err)
	}
	s.tick(ctx)
	var reply commandReply
	if err := ReadJSON(path+".ack", &reply); err != nil || reply.Error != "" {
		t.Fatalf("ack = %+v, %v", reply, err)
	}
	if !s.jobs["named:work"].active {
		t.Fatal("the claim was answered but the profile waited for the next tick")
	}
	if alloc := s.jobs["named:work"].held; alloc == nil || !slices.Equal(alloc.Allowed, []string{shared}) || len(alloc.Handoffs) != 1 {
		t.Fatalf("it started with the old allocation: %+v", alloc)
	}
	cancel()
	s.stopAll()
	for s.activeCount() > 0 {
		s.handleEvent(<-s.events, true)
	}
}

func TestClaimRebuildsAnUnreadableRoutesFile(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared, mine, theirs := r.env.line("codex", "shared"), r.env.line("claude", "mine"), r.env.line("claude", "theirs")
	r.add("default", "one", mine, shared)
	r.add("named:work", "one", theirs, shared)
	path := filepath.Join(r.root, "daemon-routes.json")
	damaged := []byte(`{"version":1,"rev":4,"routes":[{"id":"zz`)
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })

	// There are no ids to name: the caller names the line.
	req := ClaimRequest{Profile: "named:work", Source: shared, TargetRev: r.revision("named:work")}
	if err := r.claim(req); err != nil {
		t.Fatal(err)
	}
	got := r.waitRoute(shared, func(rt Route) bool { return rt.Owner == "named:work" && rt.State == RouteActive })
	f, err := ReadRoutes(r.root)
	if err != nil {
		t.Fatalf("the daemon wrote a file it cannot read: %v", err)
	}
	if got.Handoff == "" && got.From != "" {
		t.Fatalf("route %+v", got)
	}
	owners := map[string]string{}
	for _, rt := range f.Routes {
		owners[rt.Kind+":"+filepath.Base(rt.Path)] = rt.Owner
	}
	if owners["claude:mine"] != "default" || owners["claude:theirs"] != "named:work" || owners["codex:shared"] != "named:work" {
		t.Fatalf("owners = %v", owners)
	}
	matches, _ := filepath.Glob(path + ".invalid-*")
	if len(matches) != 1 {
		t.Fatalf("the damaged file is kept as evidence: %v", matches)
	}
	if kept, _ := os.ReadFile(matches[0]); string(kept) != string(damaged) {
		t.Fatalf("evidence changed: %q", kept)
	}
	r.waitJob("default", func(j JobStatus) bool {
		return len(j.SharedSources) == 1 && j.SharedSources[0].HolderState == HolderActive
	})
	for _, l := range r.logLines() {
		if strings.Contains(l, r.root) {
			t.Fatalf("a log line names a path: %s", l)
		}
	}
	// Once the file is usable again, sources are named by id.
	other := ClaimRequest{Profile: "named:work", Source: "codex:/not/a/line", TargetRev: r.revision("named:work")}
	if code := codeOf(r.claim(other)); code != "route_unknown" {
		t.Fatalf("code = %q", code)
	}
}

// A daemon that dies while it mends an unusable routes file must not come back
// to what looks like a first start: that would pick the owners of the contested
// sources on its own. The disk it finds is the one it left when it died, here
// taken at the moment the new file was to be written.
func TestClaimRebuildingCutShortIsNotAFirstStart(t *testing.T) {
	r := newRouted(t)
	shared, mine, theirs := r.env.line("codex", "shared"), r.env.line("claude", "mine"), r.env.line("claude", "theirs")
	r.add("default", "one", mine, shared)
	r.add("named:work", "one", theirs, shared)
	path := filepath.Join(r.root, "daemon-routes.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"rev":4,"routes":[{"id":"zz`), 0600); err != nil {
		t.Fatal(err)
	}
	disk := func() map[string][]byte {
		files := map[string][]byte{}
		matches, _ := filepath.Glob(path + "*")
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil {
				t.Error(err)
			}
			files[m] = data
		}
		return files
	}
	var died map[string][]byte
	var once sync.Once
	writeSeam(t, func(p string, data []byte, perm os.FileMode) error {
		var err error
		once.Do(func() { died, err = disk(), errors.New("the daemon died here") })
		if err != nil {
			return err
		}
		return atomicfile.Write(p, data, perm)
	})
	stop := r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	if err := r.claim(ClaimRequest{Profile: "named:work", Source: shared, TargetRev: r.revision("named:work")}); err == nil {
		t.Fatal("the claim went through a write that did not happen")
	}
	stop()
	first := r.snapshot().InstanceID
	for m := range disk() {
		if err := os.Remove(m); err != nil {
			t.Fatal(err)
		}
	}
	for m, data := range died {
		if err := os.WriteFile(m, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	leases := len(r.allocations("default"))
	r.start()
	eventually(t, func() bool { snap := r.snapshot(); return snap.InstanceID != "" && snap.InstanceID != first })
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	for _, a := range r.allocations("default")[leases:] {
		if slices.Contains(a.Allowed, shared) {
			t.Fatalf("after the restart default reads the contested source: %+v", a)
		}
	}
	if f, err := ReadRoutes(r.root); err == nil {
		t.Fatalf("the restart made owners up: %+v", f.Routes)
	}
}
