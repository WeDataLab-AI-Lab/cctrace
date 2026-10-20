package localdaemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// claim answers a request to give a source to a profile. Whatever it decides, the
// answer says why; nothing is changed unless every check passes.
func (s *Supervisor) claim(req commandRequest) commandReply {
	rt := &s.routes
	target := s.jobs[req.ProfileID]
	if target == nil || target.gone {
		return commandReply{Error: "profile_unavailable"}
	}
	switch {
	case !target.spec.Enabled:
		return commandReply{Error: "profile_unavailable", Reason: "disabled"}
	case target.spec.BlockedReason != "":
		return commandReply{Error: "profile_unavailable", Reason: target.spec.BlockedReason}
	case s.stops[target.spec.ID]:
		return commandReply{Error: "profile_unavailable", Reason: "stopped"}
	}
	if rt.uncertain && !s.rereadRoutes() {
		return commandReply{Error: "state_write_failed"}
	}
	if !rt.valid {
		return s.claimByRebuilding(req, target)
	}
	i := slices.IndexFunc(rt.file.Routes, func(r Route) bool { return r.ID == req.Source })
	if i < 0 {
		return commandReply{Error: "route_unknown"}
	}
	r := rt.file.Routes[i]
	holder := s.jobOf(r.Owner)
	holderRev := ""
	// Revisions describe current registry profiles, not a deleted profile's
	// draining job. Keep that job below so its lease still gates the handoff.
	if holder != nil && !holder.gone {
		holderRev = holder.spec.Revision
	}
	switch {
	case r.Rev != req.RouteRev || r.Owner != holderIdentity(req.Holder):
		return commandReply{Error: "route_changed"}
	case target.spec.Revision != req.TargetRev || holderRev != req.HolderRev:
		return commandReply{Error: "profile_changed"}
	case r.State == RouteReleasing || (r.State == RouteActive && (r.Handoff != "" || r.From != "")):
		return commandReply{Error: "handoff_pending"}
	}
	rid := routeIdentity(target.spec.ID)
	if r.Owner == rid || !slices.ContainsFunc(target.plan.ExcludedLines, func(x ExcludedLine) bool { return x.Route == r.ID }) {
		return commandReply{Error: "invalid_source"}
	}

	next := RouteFile{Version: rt.file.Version, Rev: rt.file.Rev, Routes: slices.Clone(rt.file.Routes)}
	direct := r.State == RouteUnowned || holder == nil
	var release func()
	if direct {
		// Nobody is reading the source, so there is nothing to wait for: the owner
		// changes at once, once no run holds the line.
		if holder != nil && holder.held != nil {
			return commandReply{Error: "line_busy"}
		}
		var err error
		if release, err = LockLines(context.Background(), s.config.Root, []string{r.ID}, 0); err != nil {
			return commandReply{Error: "line_busy"}
		}
		defer release()
		from := r.From
		if r.State == RouteActive {
			from = r.Owner
		}
		r.Owner, r.State, r.From, r.Handoff, r.To = rid, RouteActive, from, randomID(), ""
	} else {
		r.State, r.To, r.Handoff = RouteReleasing, rid, ""
	}
	r.Rev++
	next.Routes[i] = r
	if !s.saveClaim(next) {
		return commandReply{Error: "state_write_failed"}
	}
	if direct {
		s.say("handoff source=%s from=%s to=%s", r.ID, s.name(r.From), s.name(rid))
	} else {
		s.say("releasing source=%s holder=%s to=%s cause=claim", r.ID, s.name(r.Owner), s.name(rid))
	}
	return commandReply{}
}

// holderIdentity is how the caller's holder is written in the routes file; the
// caller sends a profile ID, or nothing for a source nobody holds.
func holderIdentity(id string) string {
	if id == "" {
		return ""
	}
	return routeIdentity(id)
}

// saveClaim writes the routes a claim has produced, and makes them the daemon's.
func (s *Supervisor) saveClaim(next RouteFile) bool {
	rt := &s.routes
	written, err := WriteRoutes(s.config.Root, next)
	switch {
	case err == nil:
		rt.file, rt.valid, rt.dirty = written, true, true
		return true
	case errors.Is(err, ErrRoutesWriteUncertain):
		rt.uncertain = true
		s.say("write-uncertain rev=%d", next.Rev+1)
	}
	return false
}

// claimByRebuilding is the claim that mends a routes file that cannot be used.
// Nobody knows who owned what, so ids do not exist; the caller names the line.
// The damaged file is kept aside, and a new one is built in which each source
// only one profile has is that profile's and each contested source is nobody's,
// except the one claimed, which goes to the claimant. Choosing the owner of a
// contested source is the one thing this does not do on its own.
func (s *Supervisor) claimByRebuilding(req commandRequest, target *job) commandReply {
	rt := &s.routes
	if target.spec.Revision != req.TargetRev {
		return commandReply{Error: "profile_changed"}
	}
	if !slices.Contains(target.spec.Sources, req.Source) {
		return commandReply{Error: "route_unknown"}
	}
	in := s.planInput()
	file := RecoverRoutes(in.Profiles, in.Now, nil)
	rid := routeIdentity(target.spec.ID)
	i := routeCovering(in.Profiles, file, rid, req.Source)
	if i < 0 || file.Routes[i].State != RouteUnowned {
		return commandReply{Error: "invalid_source"}
	}
	r := file.Routes[i]
	r.Owner, r.State, r.From, r.Handoff, r.To = rid, RouteActive, "", randomID(), ""
	r.Rev++
	file.Routes[i] = r

	path := filepath.Join(s.config.Root, routesFileName)
	aside := path + ".invalid-" + strconv.FormatInt(time.Now().Unix(), 10)
	// The damaged file is kept aside under a second name and stays in place until
	// the new one replaces it, so a daemon that dies in between finds it again and
	// not a missing file, which is a first start. What is not a file that can be
	// linked is moved aside instead.
	moved := false
	switch info, err := os.Lstat(path); {
	case errors.Is(err, os.ErrNotExist):
	case err == nil && info.Mode().IsRegular() && os.Link(path, aside) == nil:
	default:
		err = os.Rename(path, aside)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return commandReply{Error: "state_write_failed"}
		}
		moved = err == nil
	}
	if !s.saveClaim(file) {
		if moved && !s.routes.uncertain {
			_ = os.Rename(aside, path)
		}
		return commandReply{Error: "state_write_failed"}
	}
	s.say("recovered rev=%d", rt.file.Rev)
	s.say("handoff source=%s from=- to=%s", r.ID, s.name(rid))
	return commandReply{}
}
