package main

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/samefolder"
)

// oneShotLineWait is how long a run outside the daemon waits for the lock of a
// line another run is reading, before it leaves that line for a later run.
var oneShotLineWait = 2 * time.Second

// lineGate decides which source lines a run outside the daemon may read. Such a
// run -- a one-shot sync, the SessionEnd hook's child, a re-enrichment -- holds
// the profile's lock and nothing else keeps it off a source another profile
// owns, so it follows the daemon's decision and locks each line it reads, as the
// daemon's jobs do.
//
// The decision is the running daemon's own record of what the job reads when
// there is one that is current, else the plan the daemon would make from the
// routes file as it stands, which this run does not change. When the profiles
// cannot be read at all there is nothing to plan with, and a line is read only if
// the routes file gives it to this profile.
type lineGate struct {
	root, label, owner string
	readOnly           bool
	allowed            []string                   // the lines the profile may read
	held               []localdaemon.ExcludedLine // the lines it may not, and the routes that hold them
	lineRoutes         map[string][]string        // allowed line -> the routes behind it
	decided            []string                   // the lines of the profiles the decision covers that send where this one does
	fully              bool                       // the profile has lines and may read none
	// restricted: no plan, only the routes file.
	restricted  bool
	destination string // the profile's own, as its spec has it
	routes      localdaemon.RouteFile
	releases    []func()
}

// newLineGate reads the decision for the profile. It is nil, and gates nothing,
// for a profile the registry has no entry for. readOnly gates a run that only
// looks (a dry run): it takes no locks.
func newLineGate(profileName string, readOnly bool) *lineGate {
	root := profile.DefaultDir()
	resolved, id := managedProfile(profileName)
	registry := profile.Registry{Root: root}
	g := &lineGate{root: root, label: resolved, owner: localdaemon.RouteIdentity(id), readOnly: readOnly}
	entries, err := registry.List()
	if err != nil {
		// Absence is not evidence: nothing here says another profile does not hold
		// the lines, so only what the routes file gives this profile is read.
		g.restricted = true
		if entry, err := registry.Load(resolved); err == nil {
			g.destination = profileJobSpec(entry, daemonOverrides{}).Destination
		}
		if routes, err := localdaemon.ReadRoutes(root); err == nil {
			g.routes = routes
		}
		return g
	}
	i := slices.IndexFunc(entries, func(e profile.Entry) bool { return e.ID == id })
	if i < 0 {
		return nil
	}
	specs := make([]localdaemon.Spec, 0, len(entries))
	for _, entry := range entries {
		specs = append(specs, profileJobSpec(entry, daemonOverrides{}))
	}
	g.destination = specs[i].Destination
	// Lines overlap only between profiles that send to one destination, as Plan
	// judges them; a profile of another destination shares nothing with this one.
	sameDestination := map[string]bool{}
	for _, spec := range specs {
		sameDestination[localdaemon.RouteIdentity(spec.ID)] = spec.Destination == g.destination
	}
	if alloc, ok := runningDaemonsAllocation(root, id, entries[i].Revision); ok {
		job := alloc.Jobs[id]
		g.adopt(job.Allowed, job.Excluded, job.Routes)
		for other, j := range alloc.Jobs {
			if sameDestination[localdaemon.RouteIdentity(other)] {
				g.decide(j.Allowed, j.Excluded)
			}
		}
		return g
	}
	res := localdaemon.PlanOffline(root, specs)
	if plan, ok := res.Job(id); ok {
		g.adopt(plan.Allowed, plan.ExcludedLines, plan.LineRoutes)
	}
	for other, j := range res.Jobs {
		if sameDestination[other] {
			g.decide(j.Allowed, j.ExcludedLines)
		}
	}
	return g
}

// runningDaemonsAllocation is the daemon's record of what its jobs read, when a
// daemon is running, the record is its own, and it has the profile as it is now:
// the record is then what the profile's job does, whatever settings the daemon
// was started with.
func runningDaemonsAllocation(root, id, revision string) (localdaemon.AllocationsFile, bool) {
	if running, err := localdaemon.Running(root); err != nil || !running {
		return localdaemon.AllocationsFile{}, false
	}
	rt, err := localdaemon.ReadRuntime(root)
	if err != nil {
		return localdaemon.AllocationsFile{}, false
	}
	alloc, err := localdaemon.ReadAllocations(root)
	if err != nil || alloc.InstanceID != rt.InstanceID {
		return localdaemon.AllocationsFile{}, false
	}
	job, ok := alloc.Jobs[id]
	return alloc, ok && job.Revision == revision
}

func (g *lineGate) adopt(allowed []string, excluded []localdaemon.ExcludedLine, routes map[string][]string) {
	g.allowed, g.held, g.lineRoutes = allowed, excluded, routes
	g.fully = len(allowed) == 0 && len(excluded) > 0
}

// decide records the lines of a profile the decision covers that sends where
// this one does.
func (g *lineGate) decide(allowed []string, excluded []localdaemon.ExcludedLine) {
	g.decided = append(g.decided, allowed...)
	for _, x := range excluded {
		g.decided = append(g.decided, x.Line)
	}
}

// allHeld says whether the profile has sources and is allowed to read none.
func (g *lineGate) allHeld() bool { return g != nil && g.fully }

// apply keeps the folders of kind that the run may read, locking each as it goes.
// A folder the decision does not give the profile is read only if it gives it to
// no profile that sends where this one does: one given by a flag or the
// environment that is such a profile's line may be shared, and nothing here says
// it is not.
func (g *lineGate) apply(ctx context.Context, kind string, dirs []string) []string {
	if g == nil {
		return dirs
	}
	var out []string
	for _, dir := range dirs {
		if g.admit(ctx, kind, dir) {
			out = append(out, dir)
		}
	}
	return out
}

// admit finds the folder among the decision's lines by what it is, not by how it
// is spelled: a folder that overlaps a line the profile is held back from is not
// read, one that is an allowed line is read under that line's locks, and one that
// is neither is read only if it overlaps no line of a profile that sends where
// this one does.
//
// A line no route covers -- nobody has reserved it, or the profile may not
// collect now and makes no route -- is locked by the name its route will have
// (SourceLockID), so a daemon job that reserves it later waits for this run, and
// so does another run of it.
func (g *lineGate) admit(ctx context.Context, kind, dir string) bool {
	line := kind + ":" + dir
	for _, x := range g.held {
		if x.Line == line || localdaemon.SourceShape(x.Line, line) != "" {
			g.leave(kind, x.Route, "another profile holds it")
			return false
		}
	}
	locks := map[string]bool{} // line lock id -> no route has it yet
	unreserved := func(l string) {
		if id := localdaemon.SourceLockID(l, g.destination); id != "" {
			locks[id] = true
		}
	}
	allowed := false
	for _, a := range g.allowed {
		if a != line && localdaemon.SourceShape(a, line) != localdaemon.ShareSame {
			continue
		}
		allowed = true
		for _, id := range g.lineRoutes[a] {
			locks[id] = false
		}
		if len(g.lineRoutes[a]) == 0 {
			unreserved(a)
		}
	}
	switch {
	case g.restricted:
		id, ok := g.ownRoute(kind, dir)
		if !ok {
			g.leave(kind, "", "no route gives it to this profile")
			return false
		}
		locks = map[string]bool{id: false}
	case allowed:
	case slices.ContainsFunc(g.decided, func(d string) bool { return d == line || localdaemon.SourceShape(d, line) != "" }):
		g.leave(kind, "", "the daemon's decision does not give it to this profile")
		return false
	default:
		unreserved(line)
	}
	if g.readOnly || len(locks) == 0 {
		return true
	}
	ids := slices.Sorted(maps.Keys(locks))
	release, err := localdaemon.LockLines(ctx, g.root, ids, oneShotLineWait)
	if err != nil {
		reason := "it could not be locked"
		if errors.Is(err, localdaemon.ErrLineBusy) {
			reason = "another run is reading it"
		}
		g.leave(kind, ids[0], reason)
		return false
	}
	for _, id := range ids {
		var err error
		if locks[id] {
			err = localdaemon.ConfirmUnreserved(g.root, id, g.owner)
		} else {
			_, err = localdaemon.ConfirmRoute(g.root, id, g.owner)
		}
		if err != nil {
			release()
			reason := "its route could not be confirmed"
			switch {
			case errors.Is(err, localdaemon.ErrRouteChanged):
				reason = "its route is no longer this profile's"
			case errors.Is(err, localdaemon.ErrHandoffPending):
				reason = "it was just handed to this profile, and the daemon reads it first"
			}
			g.leave(kind, id, reason)
			return false
		}
	}
	g.releases = append(g.releases, release)
	return true
}

// ownRoute finds the route that gives the folder to this profile: an active one,
// of the profile, of its destination or one it had before a policy moved it.
func (g *lineGate) ownRoute(kind, dir string) (string, bool) {
	for _, r := range g.routes.Routes {
		if r.Kind == kind && r.Owner == g.owner && r.State == localdaemon.RouteActive && g.destination != "" &&
			(r.Destination == g.destination || slices.Contains(r.Aliases, g.destination)) && samefolder.Same(r.Path, dir) {
			return r.ID, true
		}
	}
	return "", false
}

// leave says, without a folder, that a source was not read.
func (g *lineGate) leave(kind, route, reason string) {
	if route == "" {
		route = "-"
	}
	diagf("profile=%s [routes] kind=%s source=%s was not read: %s", g.label, kind, route, reason)
}

// close gives up the locks of the lines the run read.
func (g *lineGate) close() {
	if g == nil {
		return
	}
	for i := len(g.releases) - 1; i >= 0; i-- {
		g.releases[i]()
	}
	g.releases = nil
}
