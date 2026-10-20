package localdaemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/gofrs/flock"
)

// publishHandoffs finishes the hand-over of the releasing routes Plan says are
// ready, that is those whose owner's job no longer holds the line. The owner is
// changed only while this process holds the line's lock and knows that no run of
// the previous owner is going on, so the new owner finds the previous owner's
// cursor final: nothing writes it after the owner is published.
//
// A route that is not ready yet is tried again on the next tick. It reports
// whether it wrote the file.
func (s *Supervisor) publishHandoffs(ready []string) bool {
	rt := &s.routes
	if len(ready) == 0 || !rt.valid || rt.uncertain {
		return false
	}
	next := RouteFile{Version: rt.file.Version, Rev: rt.file.Rev, Routes: slices.Clone(rt.file.Routes)}
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	type moved struct{ id, holder, from, to string }
	var published []moved
	for _, id := range ready {
		i := slices.IndexFunc(next.Routes, func(r Route) bool { return r.ID == id })
		if i < 0 || next.Routes[i].State != RouteReleasing {
			continue
		}
		r := next.Routes[i]
		if !s.previousRunEnded(r.Owner) {
			rt.dirty = true
			continue
		}
		release, err := LockLines(context.Background(), s.config.Root, []string{id}, 0)
		if err != nil {
			// A run of the previous owner that does not belong to this daemon still
			// reads the line, or the lock cannot be taken. Either way, not yet.
			rt.dirty = true
			continue
		}
		releases = append(releases, release)
		from := r.From
		if from == "" {
			from = r.Owner
		}
		published = append(published, moved{id, r.Owner, from, r.To})
		if r.To == "" {
			r.Owner, r.State, r.From, r.Handoff = "", RouteUnowned, from, ""
		} else {
			r.Owner, r.State, r.From, r.Handoff = r.To, RouteActive, from, randomID()
		}
		r.To = ""
		r.Rev++
		next.Routes[i] = r
	}
	if len(published) == 0 {
		return false
	}
	written, err := WriteRoutes(s.config.Root, next)
	switch {
	case err == nil:
	case errors.Is(err, ErrRoutesWriteUncertain):
		rt.uncertain = true
		s.say("write-uncertain rev=%d", next.Rev+1)
		return false
	default:
		rt.dirty = true
		return false
	}
	rt.file, rt.dirty = written, true
	for _, m := range published {
		if m.to == "" {
			s.say("released source=%s holder=%s cause=source_removed successor=none", m.id, s.name(m.holder))
		} else {
			s.say("handoff source=%s from=%s to=%s", m.id, s.name(m.from), s.name(m.to))
		}
	}
	return true
}

// previousRunEnded says that nothing of the previous owner's is reading its
// cursor: this daemon either holds the profile's lease for lines of its own, or
// holds nothing and the profile's lock is free to take. Whoever else holds that
// lock is a run that does not take line locks -- an older binary, started just
// before an update -- and the hand-over waits for it. The owner of a profile that
// has been deleted, or one that takes no lease, has nothing running to wait for.
func (s *Supervisor) previousRunEnded(owner string) bool {
	j := s.jobOf(owner)
	if j == nil || j.spec.Acquire == nil || j.held != nil {
		return true
	}
	lock := flock.New(filepath.Join(j.spec.RuntimeDir, "sync.lock"))
	locked, err := lock.TryLock()
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !locked {
		return false
	}
	_ = lock.Unlock()
	return true
}

// adopt records that a job has taken the lease of a generation made of hand-overs,
// and which of them it took: for those the profile has the previous owner's
// cursor, so the routes need not carry them any longer.
func (s *Supervisor) adopt(j *job, handoffs []Handoff) {
	if s.routes.adopted == nil {
		s.routes.adopted = map[string]string{}
	}
	for _, h := range handoffs {
		s.routes.adopted[h.ID] = routeIdentity(j.spec.ID)
	}
	s.routes.dirty = true
}

// clearAdopted removes from the routes the hand-overs that have been adopted. It
// reports whether it wrote the file.
func (s *Supervisor) clearAdopted() bool {
	rt := &s.routes
	if len(rt.adopted) == 0 || !rt.valid || rt.uncertain {
		return false
	}
	next := RouteFile{Version: rt.file.Version, Rev: rt.file.Rev, Routes: slices.Clone(rt.file.Routes)}
	var done []string
	for i, r := range next.Routes {
		if owner, ok := rt.adopted[r.Handoff]; ok && r.Handoff != "" && r.State == RouteActive && r.Owner == owner {
			done = append(done, r.Handoff)
			r.From, r.Handoff = "", ""
			r.Rev++
			next.Routes[i] = r
		}
	}
	for id := range rt.adopted {
		if !slices.ContainsFunc(rt.file.Routes, func(r Route) bool { return r.Handoff == id }) {
			delete(rt.adopted, id)
		}
	}
	if len(done) == 0 {
		return false
	}
	written, err := WriteRoutes(s.config.Root, next)
	switch {
	case err == nil:
	case errors.Is(err, ErrRoutesWriteUncertain):
		rt.uncertain = true
		s.say("write-uncertain rev=%d", next.Rev+1)
		return false
	default:
		rt.dirty = true
		return false
	}
	rt.file, rt.dirty = written, true
	for _, id := range done {
		delete(rt.adopted, id)
	}
	return true
}
