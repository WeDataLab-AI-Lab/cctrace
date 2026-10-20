package localdaemon

import (
	"slices"
	"time"
)

// RecoverRoutes builds a routes file from nothing for profiles whose file could
// not be used. No one knows who owned what, so a source that only one profile
// has goes to that profile, as on a first start, and a source that more than one
// profile has -- whatever their state -- belongs to nobody. It stays that way
// until a profile claims it: choosing an owner here would pick who collects it,
// with whose filters, on no one's say.
func RecoverRoutes(profiles []PlanProfile, now time.Time, newID func() string) RouteFile {
	in := PlanInput{Profiles: profiles, Valid: true, Persist: true, Now: now, NewID: newID}
	in.Routes = Plan(in).Routes
	// A profile that cannot collect makes no route on a first start, so a source
	// only such profiles share would be left to whichever comes back first. It is
	// given a route here, to be nobody's below.
	idle := map[string]bool{}
	everyone := make([]PlanProfile, len(profiles))
	for i, p := range profiles {
		idle[p.ID] = !p.eligible()
		p.Enabled, p.Blocked, p.Stopped = true, false, false
		everyone[i] = p
	}
	in.Profiles = everyone
	file := Plan(in).Routes
	in.Profiles, in.Routes, in.Persist = profiles, file, false
	pl := newPlanner(in)
	pl.load()
	out := RouteFile{Version: file.Version, Rev: file.Rev, Routes: make([]Route, 0, len(file.Routes))}
	for i, r := range file.Routes {
		switch {
		case pl.contested(pl.routes[i]):
			r.Owner, r.State = "", RouteUnowned
			r.From, r.Handoff, r.To = "", "", ""
		case idle[r.Owner]:
			// Only this idle profile has it: it reserves it once it can collect.
			continue
		}
		out.Routes = append(out.Routes, r)
	}
	return out
}

// contested: a profile other than the route's owner has a line over it.
func (pl *planner) contested(r *workRoute) bool {
	for i := range pl.profs {
		if pl.profs[i].ID == r.Owner {
			continue
		}
		for _, l := range pl.lines[i] {
			if pl.shape(r, &pl.profs[i], l) != "" {
				return true
			}
		}
	}
	return false
}

// routeCovering returns the index of the route of f that covers the line of the
// profile, or -1.
func routeCovering(profiles []PlanProfile, f RouteFile, profile, line string) int {
	pl := newPlanner(PlanInput{Profiles: profiles, Routes: f, Valid: true})
	pl.load()
	pi, ok := pl.byID[profile]
	if !ok {
		return -1
	}
	for _, l := range pl.lines[pi] {
		if l.text != line {
			continue
		}
		for ri, r := range pl.routes {
			if pl.shape(r, &pl.profs[pi], l) != "" {
				return ri
			}
		}
	}
	return -1
}

// MigrateRoutes builds the first routes file from what an earlier daemon
// published, which kept the owner of a route only as a flag. restored are the
// profiles whose snapshot entry said they owned their sources and still has the
// sources it had; blocked are those it had held back as a duplicate; changed are
// those it said owned their sources that have other sources now.
//
// A restored profile reserves its sources, whatever its state now, so an upgrade
// does not hand them to whoever sorts first. A changed profile is not trusted
// with all its sources, but where it is the one such profile with a line over a
// line a blocked profile has, it keeps that line: it held it, and the profile it
// held back is held back by it still. A blocked profile whose sources no one
// reserves is left with all of them unowned: its holder is not to be found, and
// guessing one would pick the filters its data is collected under.
func MigrateRoutes(profiles []PlanProfile, restored, blocked, changed map[string]bool, now time.Time, newID func() string) RouteFile {
	asRestored := make([]PlanProfile, len(profiles))
	for i, p := range profiles {
		p.Enabled, p.Blocked, p.Stopped = restored[p.ID], false, !restored[p.ID]
		asRestored[i] = p
	}
	file := Plan(PlanInput{Profiles: asRestored, Valid: true, Persist: true, Now: now, NewID: newID}).Routes
	pl := newPlanner(PlanInput{Profiles: profiles, Routes: file, Valid: true, Persist: true, Now: now, NewID: newID})
	pl.load()
	over := func(j int, l planLine) []planLine {
		return slices.DeleteFunc(slices.Clone(pl.lines[j]), func(m planLine) bool {
			return m.root == nil || m.kind != l.kind || pl.view.shape(m.root, l.root) == ""
		})
	}
	for i := range pl.profs {
		p := &pl.profs[i]
		if !blocked[p.ID] || restored[p.ID] || !isHex(p.Destination, 64) {
			continue
		}
		for _, l := range pl.lines[i] {
			if l.root == nil || pl.covered(p, l) {
				continue
			}
			holder := -1
			for j := range pl.profs {
				if q := &pl.profs[j]; changed[q.ID] && j != i && q.Destination == p.Destination && len(over(j, l)) > 0 {
					if holder >= 0 {
						holder = -1 // more than one: nobody can say which
						break
					}
					holder = j
				}
			}
			if holder < 0 {
				continue
			}
			q := &pl.profs[holder]
			for _, m := range over(holder, l) {
				if !pl.covered(q, m) {
					pl.routes = append(pl.routes, &workRoute{Route: Route{ID: pl.reserveID(m, q.Destination), Rev: 1, Kind: m.kind, Path: m.path, Destination: q.Destination, Owner: q.ID, State: RouteActive, Since: now}, roots: m.root})
				}
			}
		}
		held := slices.ContainsFunc(pl.lines[i], func(l planLine) bool { return l.root != nil && pl.covered(p, l) })
		if held {
			continue
		}
		for _, l := range pl.lines[i] {
			if l.root == nil || pl.covered(p, l) {
				continue
			}
			pl.routes = append(pl.routes, &workRoute{Route: Route{ID: pl.reserveID(l, p.Destination), Rev: 1, Kind: l.kind, Path: l.path, Destination: p.Destination, State: RouteUnowned, Since: now}, roots: l.root})
		}
	}
	out := RouteFile{Version: routesVersion, Routes: make([]Route, 0, len(pl.routes))}
	for _, r := range pl.routes {
		out.Routes = append(out.Routes, r.Route)
	}
	return out
}
