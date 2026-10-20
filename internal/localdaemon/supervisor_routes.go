package localdaemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"time"
)

// planRefresh is how long a plan is trusted when nothing the supervisor can see
// has changed. A plan also depends on the folders on disk (a link retargeted, a
// folder renamed), which no input records.
const planRefresh = 30 * time.Second

// routing is the supervisor's view of who reads which source. The in-memory file
// is the authority: the daemon is the only writer, and it reads the file at
// start and after a write whose outcome it could not tell.
type routing struct {
	file RouteFile
	// valid is false when the file could not be used. Nobody then knows who owns
	// what, and the supervisor writes nothing until a claim rebuilds the file.
	valid     bool
	uncertain bool // the last write may or may not have happened
	failing   bool // the last write of a plan failed, and the log has said so
	planned   bool
	key       string // inputs of the last plan
	at        time.Time
	dirty     bool // something outside the inputs asks for a new plan
	said      map[string]bool
	// back maps the identity a profile has in the routes file to its job's ID.
	back map[string]string
	// names remembers what profiles were called, so a profile that has been
	// deleted can still be named in the log.
	names map[string]string
	// adopted are hand-overs whose new owner has taken its lease, by hand-over id,
	// naming the owner; the routes still carry them until the next write.
	adopted map[string]string
	// allocations is what the allocations file last said.
	allocations []byte
	// pruned is the revision of the file whose routes the lock files were last
	// matched to, once that has been done. known are the routes seen in the file
	// whose lock files may still be there; only theirs are ever removed.
	pruned     int
	prunedDone bool
	known      map[string]bool
	// unsaved: the file in memory has not been written yet, though a plan may find
	// nothing to change in it.
	unsaved bool
}

// say logs one routing diagnostic. A line carries names, counts and ids, never a
// path, an address or a token.
func (s *Supervisor) say(format string, args ...any) {
	if s.config.Logf != nil {
		s.config.Logf("[routes] "+format, args...)
	}
}

// initRoutes reads the routes file once, at start.
func (s *Supervisor) initRoutes() {
	rt := &s.routes
	f, err := ReadRoutes(s.config.Root)
	switch {
	case err == nil:
		rt.file, rt.valid = f, true
	case errors.Is(err, os.ErrNotExist):
		rt.file, rt.valid = s.migrateRoutes(), true
		rt.unsaved = len(rt.file.Routes) > 0
	default:
		s.markInvalid(err)
	}
}

// migrateRoutes is the routes file of a first start. With nothing to go by, it
// is empty and the first plan reserves in ID order. If the daemon took over from
// an earlier one, the owners it named in its snapshot are kept, once.
func (s *Supervisor) migrateRoutes() RouteFile {
	restored, blocked, changed := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range s.ids() {
		j := s.jobs[id]
		old, ok := s.previous[id]
		if !ok || j.gone {
			continue
		}
		rid := routeIdentity(id)
		if old.RouteSignature != routeSignature(j.spec) {
			changed[rid] = old.RouteOwner
			continue
		}
		restored[rid] = old.RouteOwner
		if old.ErrorCode == "duplicate_route" && !old.RouteOwner {
			blocked[rid] = true
			j.afterBlock = true
		}
	}
	if !slices.Contains(slices.Collect(maps.Values(restored)), true) && len(blocked) == 0 {
		return RouteFile{Version: routesVersion}
	}
	in := s.planInput()
	return MigrateRoutes(in.Profiles, restored, blocked, changed, in.Now, nil)
}

func (s *Supervisor) markInvalid(err error) {
	reason := "unreadable"
	var invalid *InvalidRoutesError
	if errors.As(err, &invalid) {
		reason = invalid.Reason
	}
	s.routes.valid, s.routes.uncertain, s.routes.dirty = false, false, true
	s.say("invalid reason=%s", reason)
}

// rereadRoutes settles an uncertain write by taking whatever file is there. It
// reports whether the supervisor may decide again.
func (s *Supervisor) rereadRoutes() bool {
	rt := &s.routes
	f, err := ReadRoutes(s.config.Root)
	switch {
	case err == nil:
		rt.file, rt.valid, rt.uncertain, rt.dirty = f, true, false, true
	case errors.Is(err, os.ErrNotExist):
		// The write did not happen; the copy in memory stands and is written again.
		rt.uncertain, rt.dirty = false, true
	default:
		s.markInvalid(err)
	}
	return !rt.uncertain
}

// routeDestination is the destination key of a route. Destinations are hashes;
// anything else is hashed so it can be kept in the file, and an empty one has no
// route at all.
func routeDestination(d string) string {
	if d == "" || isHex(d, 64) {
		return d
	}
	sum := sha256.Sum256([]byte(d))
	return hex.EncodeToString(sum[:])
}

// RouteIdentity is how a profile is written into the routes file, for a reader
// outside the daemon that has to recognise itself in it.
func RouteIdentity(id string) string { return routeIdentity(id) }

// routeIdentity is how a profile is written into the routes file, which takes
// only registry profile IDs ("default", "named:<name>"). The registry makes no
// other; anything else is mapped to a name of that shape so it can still be kept.
func routeIdentity(id string) string {
	if id != "" && validIdentity(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "named:z" + hex.EncodeToString(sum[:8])
}

// jobOf finds the job of a profile as the routes file names it.
func (s *Supervisor) jobOf(identity string) *job {
	if id, ok := s.routes.back[identity]; ok {
		return s.jobs[id]
	}
	return s.jobs[identity]
}

// planInput describes the profiles as the supervisor sees them now. A profile
// that is gone from the registry but still has a running job is not a profile,
// yet the lines its job holds still count as held.
func (s *Supervisor) planInput() PlanInput {
	in := PlanInput{Routes: s.routes.file, Valid: s.routes.valid, Held: map[string][]string{}, Persist: true, Now: time.Now().UTC()}
	s.routes.back = map[string]string{}
	for _, id := range s.ids() {
		j := s.jobs[id]
		rid := routeIdentity(id)
		s.routes.back[rid] = id
		s.routes.names[rid] = j.spec.Name
		if lines := j.heldLines(); len(lines) > 0 {
			in.Held[rid] = lines
		}
		if j.gone {
			continue
		}
		in.Profiles = append(in.Profiles, planProfileOf(j.spec, s.stops[id]))
	}
	return in
}

// planProfileOf is how the planner sees a profile.
func planProfileOf(sp Spec, stopped bool) PlanProfile {
	return PlanProfile{ID: routeIdentity(sp.ID), Order: sp.ID, Name: sp.Name, Destination: routeDestination(sp.Destination), Lines: sp.Sources, Labels: sp.SourceLabels,
		Enabled: sp.Enabled, Blocked: sp.BlockedReason != "", Stopped: stopped, Filters: sp.Filters, RuntimeDir: sp.RuntimeDir}
}

// planKey identifies everything a plan is decided from or applied by, so a
// refresh in which none of it changed can skip the plan. A job's revision and
// whether it only analyses locally do not change the plan, but applying it sets
// the job's status by them and records the revision for runs outside the daemon.
func (s *Supervisor) planKey(in PlanInput) string {
	h := sha256.New()
	fmt.Fprintf(h, "%v/%d/%d\n", in.Valid, in.Routes.Rev, len(in.Routes.Routes))
	for _, p := range in.Profiles {
		fmt.Fprintf(h, "%s|%s|%s|%s|%v|%v|%v|%q|%v|%q|%v|%v|%q\n", p.ID, p.Name, p.RuntimeDir, p.Destination, p.Enabled, p.Blocked, p.Stopped, p.Lines, p.Labels,
			p.Filters.RepoPrefixes, p.Filters.RedactPrompts, p.Filters.RedactTools, p.Filters.ExcludeAccounts)
	}
	for _, id := range s.ids() {
		fmt.Fprintf(h, "job %s %q %v\n", id, s.jobs[id].spec.Revision, s.jobs[id].spec.LocalAnalysis)
	}
	held := make([]string, 0, len(in.Held))
	for id := range in.Held {
		held = append(held, id)
	}
	sort.Strings(held)
	for _, id := range held {
		fmt.Fprintf(h, "held %s %q\n", id, in.Held[id])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// route decides who reads what, when anything it depends on has changed, and
// hands the decision to the jobs. A decision that could not be saved is not
// acted on: after a restart it could come out differently.
func (s *Supervisor) route() {
	for pass := 0; pass < 4 && s.routeOnce(); pass++ {
	}
}

// routeOnce is one look at the routes. It reports whether it wrote the file in
// finishing a hand-over, which calls for another.
func (s *Supervisor) routeOnce() bool {
	if !s.complete {
		return false
	}
	rt := &s.routes
	if rt.uncertain && !s.rereadRoutes() {
		return false
	}
	cleared := s.clearAdopted()
	in := s.planInput()
	key := s.planKey(in)
	if rt.planned && !rt.dirty && key == rt.key && time.Since(rt.at) < planRefresh {
		return cleared
	}
	rt.dirty = false
	before := rt.file
	res := Plan(in)
	if in.Valid && (res.Changed || rt.unsaved) {
		written, err := WriteRoutes(s.config.Root, res.Routes)
		switch {
		case err == nil:
			rt.file, rt.unsaved, rt.failing = written, false, false
			in.Routes = written
		case errors.Is(err, ErrRoutesWriteUncertain):
			rt.uncertain = true
			s.say("write-uncertain rev=%d", res.Routes.Rev+1)
			return cleared
		default:
			// Nothing is decided until the write goes through; the log says why
			// once, without a path.
			if !rt.failing {
				reason := "io"
				var invalid *InvalidRoutesError
				if errors.As(err, &invalid) {
					reason = invalid.Reason
				}
				s.say("write-failed rev=%d reason=%s", res.Routes.Rev+1, reason)
			}
			rt.dirty, rt.failing = true, true
			return cleared
		}
	}
	rt.key, rt.at, rt.planned = s.planKey(in), time.Now(), true
	s.applyPlan(before, res)
	s.pruneLineLocks()
	var handOver, remove []string
	for _, id := range res.Commits {
		if slices.Contains(res.Losers, id) {
			remove = append(remove, id)
		} else {
			handOver = append(handOver, id)
		}
	}
	published := s.publishHandoffs(handOver)
	removed := s.removeLosers(remove)
	return published || removed || cleared
}

// removeLosers removes the routes that lost a collision once nothing of their
// owner's reads them, on the same conditions a hand-over is published on: the
// owner's job has let go (Plan put them among Commits), no run of the owner's
// outside this daemon holds its profile, and the line's lock, held while the
// file is written, is free. Only then may the survivor's owner read the source.
// It reports whether it wrote the file.
func (s *Supervisor) removeLosers(ids []string) bool {
	rt := &s.routes
	if len(ids) == 0 || !rt.valid || rt.uncertain {
		return false
	}
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	next := RouteFile{Version: rt.file.Version, Rev: rt.file.Rev, Routes: make([]Route, 0, len(rt.file.Routes))}
	var removed []Route
	for _, r := range rt.file.Routes {
		if !slices.Contains(ids, r.ID) || r.State != RouteReleasing {
			next.Routes = append(next.Routes, r)
			continue
		}
		if !s.previousRunEnded(r.Owner) {
			rt.dirty = true
			next.Routes = append(next.Routes, r)
			continue
		}
		release, err := LockLines(context.Background(), s.config.Root, []string{r.ID}, 0)
		if err != nil {
			rt.dirty = true
			next.Routes = append(next.Routes, r)
			continue
		}
		releases = append(releases, release)
		removed = append(removed, r)
	}
	if len(removed) == 0 {
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
	for _, r := range removed {
		s.say("removed source=%s holder=%s cause=collision", r.ID, s.name(r.Owner))
	}
	return true
}

// applyPlan gives every job its share of a plan.
func (s *Supervisor) applyPlan(before RouteFile, res PlanResult) {
	for _, id := range s.ids() {
		j := s.jobs[id]
		if j.gone {
			j.alloc, j.plan = nil, JobPlan{}
			continue
		}
		s.applyJob(j, res.Jobs[routeIdentity(id)])
	}
	s.writeAllocations()
	s.logPlan(before, res)
}

// applyJob sets what a job may read and keeps its status true to it. Whether a
// profile is held back is a fact of the latest plan, never a memory of an earlier
// one: it is blocked exactly when it has sources to upload and may read none.
func (s *Supervisor) applyJob(j *job, jp JobPlan) {
	j.plan = jp
	j.status.RouteOwner = jp.RouteOwner
	next := Allocation{Allowed: slices.Clone(jp.Allowed), Routes: slices.Clone(jp.Routes), LineRoutes: maps.Clone(jp.LineRoutes), PeerPending: slices.Clone(jp.PeerPending), Handoffs: slices.Clone(jp.Handoffs), AfterBlock: j.afterBlock}
	j.alloc = &next

	eligible := j.spec.Enabled && j.spec.BlockedReason == ""
	switch {
	case eligible && jp.AllExcluded && !j.spec.LocalAnalysis:
		j.status.ErrorCode = "duplicate_route"
		if !j.active && !s.stops[j.spec.ID] {
			j.status.State = "blocked"
		}
	case j.status.ErrorCode == "duplicate_route":
		j.status.ErrorCode = ""
		if j.status.State == "blocked" {
			j.status.State = "unknown"
		}
		j.next = time.Time{}
	}

	// A generation reads the lines it was started with, and the queues of the
	// siblings it was told of. When the profile may read others, no longer some of
	// them, or other siblings' queues, the generation ends, its state is saved
	// and its lease released, and the next one starts with the new lines.
	if j.held != nil && (!sameLines(j.held.Allowed, next.Allowed) || !sameLines(j.held.PeerPending, next.PeerPending)) {
		if j.active {
			j.reset = true
			j.cancel()
			j.status.State = "stopping"
		} else {
			s.release(j)
		}
	}
}

func sameLines(a, b []string) bool {
	return slices.Equal(lineSet(a), lineSet(b))
}

func lineSet(lines []string) []string {
	out := slices.Clone(lines)
	slices.Sort(out)
	return slices.Compact(out)
}

// heldLines are the lines the job's lease, or its run, was made for.
func (j *job) heldLines() []string {
	if j.held == nil {
		return nil
	}
	return j.held.Allowed
}

// sharedSources is what the snapshot says about the sources a job does not read.
// The state of a holder is its profile's, except that a holder whose job is in
// error is blocked.
func (s *Supervisor) sharedSources(j *job) []SharedSource {
	if len(j.plan.Excluded) == 0 {
		return nil
	}
	out := slices.Clone(j.plan.Excluded)
	for i := range out {
		holder := s.jobOf(out[i].Holder)
		if holder != nil {
			out[i].Holder = holder.spec.ID
		}
		if out[i].HolderState == HolderActive && holder != nil && holder.status.State == "error" {
			out[i].HolderState = HolderBlocked
		}
	}
	return out
}

// unavailable says why a start, restart or collect request was refused.
func (s *Supervisor) unavailable(j *job) (reason string, holders []string, more int) {
	switch {
	case !j.spec.Enabled:
		return "disabled", nil, 0
	case j.spec.BlockedReason != "":
		return j.spec.BlockedReason, nil, 0
	}
	for _, x := range j.plan.Excluded {
		name := x.HolderName
		switch x.HolderState {
		case HolderRemoved:
			name = "(removed profile)"
		case HolderUnowned, HolderUnknown:
			name = "(unknown)"
		}
		if name != "" && !slices.Contains(holders, name) {
			holders = append(holders, name)
		}
	}
	if len(holders) > maxReplyHolders {
		holders, more = holders[:maxReplyHolders], len(holders)-maxReplyHolders
	}
	return "source_overlap", holders, more
}

// name is how a profile is called in the log.
func (s *Supervisor) name(identity string) string {
	id := identity
	if j := s.jobOf(identity); j != nil && j.spec.Name != "" {
		return j.spec.Name
	}
	if name := s.routes.names[identity]; name != "" {
		return name
	}
	if id == "" {
		return "-"
	}
	return strings.TrimPrefix(id, "named:")
}

func dash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// logPlan says what a plan changed. A line about a state is said once while the
// state lasts and again if it comes back; a line about an event is said when it
// happens. Names, counts and ids only.
func (s *Supervisor) logPlan(before RouteFile, res PlanResult) {
	if s.config.Logf == nil {
		return
	}
	known := map[string]Route{}
	for _, r := range before.Routes {
		known[r.ID] = r
	}
	for _, r := range res.Routes.Routes {
		old, ok := known[r.ID]
		switch {
		case !ok:
			s.say("reserved profile=%s kind=%s source=%s label=%s destination=%s", s.name(r.Owner), r.Kind, r.ID, dash(s.labelOf(r)), r.Destination[:8])
		case old.State == RouteActive && r.State == RouteReleasing:
			to, cause := "none", "source_removed"
			if r.To != "" {
				to = s.name(r.To)
			}
			if slices.Contains(res.Losers, r.ID) {
				cause = "collision"
			}
			s.say("releasing source=%s holder=%s to=%s cause=%s", r.ID, s.name(r.Owner), to, cause)
		}
	}
	now := map[string]bool{}
	var lines []string
	add := func(line string) {
		now[line] = true
		lines = append(lines, line)
	}
	for _, id := range s.ids() {
		j := s.jobs[id]
		if j.gone {
			continue
		}
		for _, x := range j.plan.Excluded {
			add(fmt.Sprintf("excluded profile=%s kind=%s source=%s label=%s reason=%s holder=%s holder_state=%s", j.spec.Name, x.Kind, dash(x.Source), dash(x.Label), x.Reason, dash(x.HolderName), x.HolderState))
		}
		if j.plan.AllExcluded && !j.spec.LocalAnalysis && j.spec.Enabled && j.spec.BlockedReason == "" {
			_, holders, _ := s.unavailable(j)
			add(fmt.Sprintf("fully-shared profile=%s sources=%d holders=%s", j.spec.Name, len(j.plan.Excluded), dash(strings.Join(holders, ","))))
		}
	}
	for _, r := range res.Routes.Routes {
		if owner := s.jobOf(r.Owner); r.Owner != "" && (owner == nil || owner.gone) {
			add(fmt.Sprintf("orphan source=%s former_holder=%s", r.ID, s.name(r.Owner)))
		}
	}
	for _, line := range lines {
		if !s.routes.said[line] {
			s.say("%s", line)
		}
	}
	s.routes.said = now
}

// labelOf finds how the owner of a route got its folder, for the log.
func (s *Supervisor) labelOf(r Route) string {
	j := s.jobOf(r.Owner)
	if j == nil {
		return ""
	}
	for _, line := range j.spec.Sources {
		if kind, folder, _ := strings.Cut(line, ":"); kind == r.Kind && canonicalSource(folder) == r.Path {
			return j.spec.SourceLabels[line]
		}
	}
	return ""
}

// pruneLineLocks removes the lock files of routes that are gone, once for each
// revision of the routes file. A file that cannot be read says nothing about
// which routes exist, so it removes nothing.
func (s *Supervisor) pruneLineLocks() {
	rt := &s.routes
	if !rt.valid || (rt.prunedDone && rt.pruned == rt.file.Rev) {
		return
	}
	live := map[string]bool{}
	for _, r := range rt.file.Routes {
		live[r.ID] = true
	}
	var gone []string
	for id := range rt.known {
		if !live[id] {
			gone = append(gone, id)
		}
	}
	slices.Sort(gone)
	held, err := PruneLineLocks(s.config.Root, gone)
	if err != nil {
		held = gone // try them all again
	}
	for _, id := range held {
		live[id] = true
	}
	rt.known = live
	if err == nil {
		rt.pruned, rt.prunedDone = rt.file.Rev, true
	}
}
