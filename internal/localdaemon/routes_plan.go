package localdaemon

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"cctrace/internal/samefolder"
)

// Shared reasons: how an excluded line overlaps the route that holds it.
const (
	ShareSame   = shapeSame
	ShareNested = shapeNested
)

// Holder states of a shared source, from the point of view of the profile that
// was left out of it.
const (
	HolderActive       = "active"
	HolderStopped      = "stopped"
	HolderDisabled     = "disabled"
	HolderBlocked      = "blocked"
	HolderTransferring = "transferring"
	HolderRemoved      = "removed"
	HolderUnowned      = "unowned"
	HolderUnknown      = "unknown"
)

// maxSharedSources caps the warnings carried for one profile. The decision is
// never capped, only what is said about it.
const maxSharedSources = 32

// PlanProfile is what Plan needs to know about one profile of the registry. A
// profile that has been deleted is simply not in the list.
type PlanProfile struct {
	ID, Name, Destination string
	// Order, when set, decides where the profile stands in the "by ID" order that
	// settles who reserves first and who takes over; it is ID otherwise. A caller
	// that names profiles to the file differently from how it orders them says so.
	Order string
	// RuntimeDir is the profile's directory, where its sync state and its queue of
	// session ends are.
	RuntimeDir string
	// Lines are the profile's Spec.Sources entries, "kind:folder".
	Lines []string
	// Labels says, per line, where the profile got it from: home, default, env,
	// configured or explicit.
	Labels                    map[string]string
	Enabled, Blocked, Stopped bool
	Filters                   Filters
}

func (p PlanProfile) order() string {
	if p.Order != "" {
		return p.Order
	}
	return p.ID
}

// eligible: the profile can collect now. Only an eligible profile makes new
// routes; the others keep the ones they have.
func (p PlanProfile) eligible() bool { return p.Enabled && !p.Blocked && !p.Stopped }

// PlanInput is everything a plan depends on.
type PlanInput struct {
	Profiles []PlanProfile
	// Routes is the file as read. Valid false says the file could not be used, and
	// Routes is then ignored: every line that overlaps another profile's is closed
	// because nobody knows who owns it.
	Routes RouteFile
	Valid  bool
	// Held is, per profile, the lines its running job reads now. Only a daemon
	// knows it.
	Held map[string][]string
	// Persist: the plan is the daemon's, and its Routes are to be saved. Otherwise
	// the plan is a read-only answer: a line nobody has reserved goes to the first
	// profile by ID in memory, and Routes comes back as it was given.
	Persist bool
	Now     time.Time
	// NewID makes route ids; nil draws random ones.
	NewID func() string
}

// SharedSource is a line a profile does not read because another profile's
// route holds it.
type SharedSource struct {
	Source      string `json:"source"` // route id; empty when the route is not stored
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Reason      string `json:"reason"` // ShareSame or ShareNested
	Holder      string `json:"holder,omitempty"`
	HolderName  string `json:"holder_name,omitempty"`
	HolderState string `json:"holder_state"`
}

// Handoff says a line was just given to its reader by another profile, whose
// cursor it takes over: From is that profile (empty if unknown) and ID names the
// transfer.
type Handoff struct{ Line, From, ID string }

// JobPlan is one profile's share of a plan.
type JobPlan struct {
	// Allowed are the lines to read, as they appear in PlanProfile.Lines.
	Allowed  []string
	Excluded []SharedSource
	// ExcludedLines are the lines behind Excluded, whole and uncapped, with the
	// route that holds each. They name folders, so they stay out of snapshots.
	ExcludedLines []ExcludedLine
	// Routes are the stored routes behind the lines in Allowed, by id: what a run
	// that reads them locks. A route that exists only in a read-only plan has no id.
	Routes []string
	// LineRoutes says which of those routes back each allowed line. A run outside
	// the daemon reads some lines and not others, and locks only theirs.
	LineRoutes map[string][]string
	// PeerPending are the RuntimeDirs of the profiles left out of this profile's
	// Claude folders: their session ends can only be sent by this profile.
	PeerPending []string
	Handoffs    []Handoff
	// RouteOwner: the profile holds at least one route and has no line left out.
	// It is true for an owner that is stopped or disabled, so an older build
	// reading the snapshot picks the same owner.
	RouteOwner bool
	// AllExcluded: the profile has lines to upload and may read none of them.
	AllExcluded bool
}

// PlanResult is the outcome of Plan.
type PlanResult struct {
	// Routes is the file to save. For a plan that is not persistent it is the file
	// that was given.
	Routes  RouteFile
	Changed bool
	// Commits are the releasing routes whose owner has let go of them.
	Commits []string
	// Losers are the routes that collide with an earlier one. Nobody reads one
	// while it stands; once it is among Commits it is removed, not handed over.
	Losers []string
	Jobs   map[string]JobPlan
}

// planLine is one source line of a profile, placed.
type planLine struct {
	text string // as in PlanProfile.Lines
	kind string
	path string   // canonical folder
	root []string // scan roots; nil when the line cannot be placed
}

// workRoute is a route while it is being planned. A route that exists only in
// memory (mem) is a decision for a read-only plan and has no id.
type workRoute struct {
	Route
	mem     bool
	roots   []string
	touched bool // Rev has been advanced in this plan
	dropped bool
	lost    bool // it collides with an earlier route, and goes once its owner has let go
}

func (r *workRoute) holds(destination string) bool {
	return r.Destination == destination || slices.Contains(r.Aliases, destination)
}

type planner struct {
	in      PlanInput
	view    *fsView
	profs   []PlanProfile // by ID
	byID    map[string]int
	lines   [][]planLine // parallel to profs
	routes  []*workRoute
	used    map[string]bool
	changed bool
}

// Plan decides which profile reads which source. It is a pure function of its
// input and the folders on disk, so the daemon, `status` and a one-shot run give
// the same answer.
//
// A line is a profile's source of one kind in one folder. Two lines overlap when
// the profiles send to the same destination and the folders their collectors
// scan overlap. The first profile to reserve a source owns it, and keeps it
// whatever the others do; every other profile's overlapping line is left out
// until the owner changes.
func Plan(in PlanInput) PlanResult {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	pl := newPlanner(in)
	if !in.Valid {
		return pl.unknownOwners()
	}
	pl.load()
	pl.settle()
	pl.reserve()
	return pl.result()
}

func newPlanner(in PlanInput) *planner {
	pl := &planner{in: in, view: newFSView(), byID: map[string]int{}, used: map[string]bool{}}
	pl.profs = slices.Clone(in.Profiles)
	slices.SortStableFunc(pl.profs, func(a, b PlanProfile) int { return strings.Compare(a.order(), b.order()) })
	for i := range pl.profs {
		pl.byID[pl.profs[i].ID] = i
		pl.lines = append(pl.lines, pl.place(&pl.profs[i]))
	}
	return pl
}

// place reads a profile's lines. A line of an unknown kind or without a folder
// has no scan roots: it cannot overlap anything and is left to the profile. The
// roots do not depend on the destination, so a profile whose destination is
// briefly unusable still has its lines and its routes keep their owner.
func (pl *planner) place(p *PlanProfile) []planLine {
	var out []planLine
	seen := map[string]bool{}
	for _, text := range p.Lines {
		if seen[text] {
			continue
		}
		seen[text] = true
		kind, folder, _ := strings.Cut(text, ":")
		l := planLine{text: text, kind: kind}
		if roots := pl.view.roots(kind, folder); roots != nil {
			l.path, l.root = pl.view.canonical(folder), roots
		}
		out = append(out, l)
	}
	return out
}

func (pl *planner) load() {
	for _, r := range pl.in.Routes.Routes {
		r.Aliases = slices.Clone(r.Aliases)
		pl.routes = append(pl.routes, &workRoute{Route: r, roots: pl.view.roots(r.Kind, r.Path)})
		pl.used[r.ID] = true
	}
}

// shape says how line l of profile p overlaps route r, "" if it does not: the
// route must be of the line's kind, hold against the profile's destination, and
// have a scan root that overlaps one of the line's.
func (pl *planner) shape(r *workRoute, p *PlanProfile, l planLine) string {
	if l.root == nil || r.Kind != l.kind || !r.holds(p.Destination) {
		return ""
	}
	return pl.view.shape(r.roots, l.root)
}

func (pl *planner) covered(p *PlanProfile, l planLine) bool {
	return slices.ContainsFunc(pl.routes, func(r *workRoute) bool { return pl.shape(r, p, l) != "" })
}

func (pl *planner) freshID() string {
	for {
		id := ""
		if pl.in.NewID != nil {
			id = pl.in.NewID()
		} else {
			id = randomID()[:8]
		}
		if !pl.used[id] {
			pl.used[id] = true
			return id
		}
	}
}

// touch records that a route has changed: its Rev advances once per plan, however
// many things changed in it.
func (pl *planner) touch(r *workRoute) {
	if !r.touched {
		r.Rev++
		r.touched = true
	}
	pl.changed = true
}

func (pl *planner) drop(r *workRoute) {
	r.dropped = true
	pl.changed = true
}

// hasLine: profile i has a line over the route, of the route's kind, whatever
// its destination.
func (pl *planner) hasLine(i int, r *workRoute) bool {
	return slices.ContainsFunc(pl.lines[i], func(l planLine) bool {
		return l.root != nil && l.kind == r.Kind && pl.view.shape(r.roots, l.root) != ""
	})
}

// settle brings the routes that already exist up to date with the profiles and
// the clock, before any new route is made. Nothing in it changes who owns a route
// except the owner dropping the source itself, or two routes having become one
// source, so no profile is made to give up a source by what another does, or by
// being stopped, disabled or deleted.
func (pl *planner) settle() {
	pl.dateNow()
	pl.followDestinations()
	pl.dropLosers()
	pl.tidyAliases()
	pl.release()
	pl.dropAbandoned()
	pl.routes = slices.DeleteFunc(pl.routes, func(r *workRoute) bool { return r.dropped })
}

// dateNow dates now a route dated further ahead than the reader accepts, which was
// made while the clock ran ahead and would make every write of the file refused.
// It stays after the routes made before it.
func (pl *planner) dateNow() {
	for _, r := range pl.routes {
		if r.Since.After(pl.in.Now.Add(maxSinceAhead)) {
			r.Since = pl.in.Now
			pl.touch(r)
		}
	}
}

// followDestinations: a policy can move a profile to another destination. A route
// follows its owner, and keeps the old destination as an alias so that a profile
// still sending there stays left out instead of reading in parallel. A route
// that is being handed over stays where it is.
func (pl *planner) followDestinations() {
	for _, r := range pl.routes {
		i, ok := pl.byID[r.Owner]
		if !ok || r.State != RouteActive {
			continue
		}
		p := &pl.profs[i]
		if !isHex(p.Destination, 64) || p.Destination == r.Destination || !pl.hasLine(i, r) {
			continue
		}
		r.Aliases = slices.DeleteFunc(r.Aliases, func(a string) bool { return a == p.Destination })
		if !slices.Contains(r.Aliases, r.Destination) {
			r.Aliases = append(r.Aliases, r.Destination)
		}
		r.Destination = p.Destination
		pl.touch(r)
	}
}

// collide: two routes cannot both stand, because they are of one kind, hold
// against a destination in common and have scan roots that overlap.
func (pl *planner) collide(a, b *workRoute) bool {
	return a.Kind == b.Kind && slices.ContainsFunc(a.classes(), b.holds) && pl.view.shape(a.roots, b.roots) != ""
}

// dropLosers resolves collisions between routes. A move causes one when a profile
// that moved first made its own route at the destination the owner then arrived
// at; the disk causes one when a folder is replaced by a link to another. The
// earlier route stays; the same time keeps the owner that sorts first. A profile
// whose route loses is left out of the survivor. A losing route that nobody owns
// goes at once. One that has an owner is released, so that nobody reads the
// source -- not even the survivor's owner, while the loser's owner may still be
// reading it -- and the supervisor removes it once that owner's runs are over,
// as it would hand it over.
func (pl *planner) dropLosers() {
	order := slices.Clone(pl.routes)
	slices.SortStableFunc(order, func(a, b *workRoute) int {
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		if c := strings.Compare(a.Owner, b.Owner); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	var kept []*workRoute
	for _, r := range order {
		switch {
		case !slices.ContainsFunc(kept, func(k *workRoute) bool { return pl.collide(r, k) }):
			kept = append(kept, r)
			// A route let go of for nobody, whose owner has the source again -- a
			// collision that passed before the owner had stopped reading, or a source
			// put back -- stays the owner's: handing it over would give it to nobody.
			// A cursor hand-over the release cut short is taken up again.
			if i, ok := pl.byID[r.Owner]; ok && r.State == RouteReleasing && r.To == "" && pl.hasLine(i, r) {
				r.State = RouteActive
				if r.From != "" {
					r.Handoff = randomID()
				}
				pl.touch(r)
			}
		case r.Owner == "":
			pl.drop(r)
		default:
			r.lost = true
			if r.State == RouteActive {
				r.State, r.To, r.Handoff = RouteReleasing, "", ""
				pl.touch(r)
			}
		}
	}
}

// tidyAliases drops an alias once no profile other than the owner sends to it
// with a line over the route, and keeps at most maxAliases, dropping the oldest.
func (pl *planner) tidyAliases() {
	for _, r := range pl.routes {
		if r.dropped || len(r.Aliases) == 0 {
			continue
		}
		var keep []string
		for _, a := range r.Aliases {
			if pl.someoneIsOn(r, a) {
				keep = append(keep, a)
			}
		}
		if len(keep) > maxAliases {
			keep = keep[len(keep)-maxAliases:]
		}
		if !slices.Equal(keep, r.Aliases) {
			r.Aliases = keep
			pl.touch(r)
		}
	}
}

func (pl *planner) someoneIsOn(r *workRoute, destination string) bool {
	for i := range pl.profs {
		if pl.profs[i].ID != r.Owner && pl.profs[i].Destination == destination && pl.hasLine(i, r) {
			return true
		}
	}
	return false
}

// release: an owner that no longer has any line over its route has given the
// source up (it turned the collector off or took the folder out of its
// settings). The route goes to the first profile that can take it without
// sending more than the owner did, or to nobody until someone claims it. A
// pending cursor handoff cannot outlive the owner's stake, but the profile that
// holds the cursor stays on record in From.
func (pl *planner) release() {
	for _, r := range pl.routes {
		i, ok := pl.byID[r.Owner]
		if r.dropped || r.State != RouteActive || !ok || pl.hasLine(i, r) {
			continue
		}
		r.State, r.To, r.Handoff = RouteReleasing, pl.successor(r, &pl.profs[i]), ""
		pl.touch(r)
	}
}

// successor is the first profile by ID that can collect, sends to the route's
// destination, has a line over the route and filters no more loosely than old.
func (pl *planner) successor(r *workRoute, old *PlanProfile) string {
	for j := range pl.profs {
		q := &pl.profs[j]
		if q.ID != old.ID && q.eligible() && q.Destination == r.Destination && noLooser(old.Filters, q.Filters) && pl.hasLine(j, r) {
			return q.ID
		}
	}
	return ""
}

// dropAbandoned removes a route that has no owner profile (deleted, or never one)
// once no profile has a line over it: it is holding nobody back. While some
// profile does, it stays and that profile stays out of the source.
func (pl *planner) dropAbandoned() {
	for _, r := range pl.routes {
		if _, owned := pl.byID[r.Owner]; r.dropped || r.State == RouteReleasing || owned {
			continue
		}
		if !pl.anyoneOver(r) {
			pl.drop(r)
		}
	}
}

func (pl *planner) anyoneOver(r *workRoute) bool {
	for i := range pl.profs {
		if slices.ContainsFunc(pl.lines[i], func(l planLine) bool { return pl.shape(r, &pl.profs[i], l) != "" }) {
			return true
		}
	}
	return false
}

// commits lists the releasing routes whose owner holds none of their lines now,
// so the supervisor may give them to the next owner.
func (pl *planner) commits() []string {
	var ids []string
	for _, r := range pl.routes {
		if !r.mem && r.State == RouteReleasing && !pl.holding(r) {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// holding: the owner's running job reads a line over the route.
func (pl *planner) holding(r *workRoute) bool {
	for _, text := range pl.in.Held[r.Owner] {
		kind, folder, _ := strings.Cut(text, ":")
		if kind == r.Kind && pl.view.shape(r.roots, pl.view.roots(kind, folder)) != "" {
			return true
		}
	}
	return false
}

// reserve gives every line of a profile that can collect, and that no route
// covers yet, a route owned by that profile. Profiles go by ID, so on a first
// run "default" owns whatever it shares.
func (pl *planner) reserve() {
	for i := range pl.profs {
		p := &pl.profs[i]
		if !p.eligible() || !isHex(p.Destination, 64) {
			continue
		}
		for _, l := range pl.lines[i] {
			if l.root == nil || pl.covered(p, l) {
				continue
			}
			r := &workRoute{Route: Route{Rev: 1, Kind: l.kind, Path: l.path, Destination: p.Destination, Owner: p.ID, State: RouteActive, Since: pl.in.Now}, roots: l.root}
			if pl.in.Persist {
				r.ID = pl.reserveID(l, p.Destination)
				pl.changed = true
			} else {
				r.mem = true
			}
			pl.routes = append(pl.routes, r)
		}
	}
}

// reserveID names a new route after its source (SourceLockID): a run outside the
// daemon that read the line before it was reserved holds the lock of that name,
// and the job that reads it now waits for that run. A name another route has
// already, or a plan given its names (NewID), draws one instead.
func (pl *planner) reserveID(l planLine, destination string) string {
	if pl.in.NewID == nil {
		if id := sourceID(l.kind, destination, l.root); !pl.used[id] {
			pl.used[id] = true
			return id
		}
	}
	return pl.freshID()
}

// sourceID names the route of a line of kind whose collector reads roots, sent to
// destination: the same in every plan, however the folder is spelled. The roots
// have their links resolved already; each part is then spelled as the disk lists
// it, which a disk that ignores case does not do on its own.
func sourceID(kind, destination string, roots []string) string {
	spelled := make([]string, len(roots))
	for i, root := range roots {
		spelled[i] = samefolder.Spelling(root)
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + destination + "\x00" + strings.Join(spelled, "\x00")))
	return hex.EncodeToString(sum[:4])
}

func (pl *planner) result() PlanResult {
	res := PlanResult{Jobs: pl.allocate()}
	if !pl.in.Persist {
		res.Routes = pl.in.Routes
		return res
	}
	file := RouteFile{Version: routesVersion, Rev: pl.in.Routes.Rev, Routes: make([]Route, 0, len(pl.routes))}
	for _, r := range pl.routes {
		file.Routes = append(file.Routes, r.Route)
	}
	res.Routes, res.Changed, res.Commits = file, pl.changed, pl.commits()
	for _, r := range pl.routes {
		if r.lost {
			res.Losers = append(res.Losers, r.ID)
		}
	}
	return res
}

// shut is a line left out by one route.
type shut struct {
	line  planLine
	route *workRoute
	shape string
}

// allocate says, for every profile, which of its lines it may read. A line is
// read only when every route that covers it is active and the profile's own.
func (pl *planner) allocate() map[string]JobPlan {
	owners := map[string]bool{}
	for _, r := range pl.routes {
		if r.Owner != "" {
			owners[r.Owner] = true
		}
	}
	jobs := make(map[string]JobPlan, len(pl.profs))
	shuts := make([][]shut, len(pl.profs))
	allExcluded := map[string]bool{}
	for i := range pl.profs {
		p := &pl.profs[i]
		var job JobPlan
		for _, l := range pl.lines[i] {
			var held []shut
			var handoffs []Handoff
			var owned []string
			for _, r := range pl.routes {
				shape := pl.shape(r, p, l)
				switch {
				case shape == "":
				case r.State == RouteActive && r.Owner == p.ID:
					if r.ID != "" {
						owned = append(owned, r.ID)
					}
					if r.Handoff != "" {
						handoffs = append(handoffs, Handoff{Line: l.text, From: r.From, ID: r.Handoff})
					}
				default:
					held = append(held, shut{l, r, shape})
				}
			}
			if len(held) == 0 {
				job.Allowed = append(job.Allowed, l.text)
				job.Handoffs = append(job.Handoffs, handoffs...)
				job.Routes = append(job.Routes, owned...)
				if len(owned) > 0 {
					if job.LineRoutes == nil {
						job.LineRoutes = map[string][]string{}
					}
					job.LineRoutes[l.text] = slices.Compact(slices.Sorted(slices.Values(owned)))
				}
			}
			for _, h := range held {
				job.ExcludedLines = append(job.ExcludedLines, ExcludedLine{Line: l.text, Route: h.route.ID})
			}
			shuts[i] = append(shuts[i], held...)
		}
		job.AllExcluded = len(pl.lines[i]) > 0 && len(job.Allowed) == 0
		job.RouteOwner = owners[p.ID] && len(shuts[i]) == 0
		slices.Sort(job.Routes)
		job.Routes = slices.Compact(job.Routes)
		allExcluded[p.ID] = job.AllExcluded
		jobs[p.ID] = job
	}
	siblings := map[string][]string{}
	for i := range pl.profs {
		for _, s := range shuts[i] {
			if s.route.Kind == "claude" && s.route.State == RouteActive && s.route.Owner != "" && pl.profs[i].RuntimeDir != "" {
				siblings[s.route.Owner] = append(siblings[s.route.Owner], pl.profs[i].RuntimeDir)
			}
		}
	}
	for i := range pl.profs {
		p := &pl.profs[i]
		job := jobs[p.ID]
		if dirs := siblings[p.ID]; len(dirs) > 0 {
			slices.Sort(dirs)
			job.PeerPending = slices.Compact(dirs)
		}
		for _, s := range shuts[i] {
			if len(job.Excluded) == maxSharedSources {
				break
			}
			job.Excluded = append(job.Excluded, pl.describe(p, s, allExcluded))
		}
		jobs[p.ID] = job
	}
	return jobs
}

func (pl *planner) describe(p *PlanProfile, s shut, allExcluded map[string]bool) SharedSource {
	out := SharedSource{Source: s.route.ID, Kind: s.line.kind, Label: p.Labels[s.line.text], Reason: s.shape, Holder: s.route.Owner, HolderState: HolderUnowned}
	if s.route.Owner == "" {
		return out
	}
	i, ok := pl.byID[s.route.Owner]
	if !ok {
		out.HolderState = HolderRemoved
		return out
	}
	h := &pl.profs[i]
	out.HolderName = h.Name
	switch {
	case s.route.State == RouteReleasing:
		out.HolderState = HolderTransferring
	case h.Stopped:
		out.HolderState = HolderStopped
	case !h.Enabled:
		out.HolderState = HolderDisabled
	case h.Blocked || allExcluded[h.ID]:
		out.HolderState = HolderBlocked
	default:
		out.HolderState = HolderActive
	}
	return out
}

// unknownOwners is the plan when the routes file cannot be used. Nobody knows
// who owned what, so no route is made and none is written; a line that overlaps
// a line of another profile with the same destination is closed, whatever the
// state of that profile, and a line nobody else has is read as usual. A closed
// line is in ExcludedLines like any other, with no route: a run outside the
// daemon stays off it, and `profile claim` names it by the line.
func (pl *planner) unknownOwners() PlanResult {
	type ref struct {
		profile int
		line    planLine
	}
	groups := map[string][]ref{}
	for i := range pl.profs {
		for _, l := range pl.lines[i] {
			if l.root != nil && isHex(pl.profs[i].Destination, 64) {
				key := pl.profs[i].Destination + "\x00" + l.kind
				groups[key] = append(groups[key], ref{i, l})
			}
		}
	}
	jobs := make(map[string]JobPlan, len(pl.profs))
	for i := range pl.profs {
		p := &pl.profs[i]
		var job JobPlan
		for _, l := range pl.lines[i] {
			shape := ""
			if l.root != nil && isHex(p.Destination, 64) {
				for _, other := range groups[p.Destination+"\x00"+l.kind] {
					if other.profile == i {
						continue
					}
					switch s := pl.view.shape(l.root, other.line.root); {
					case s == ShareNested:
						shape = ShareNested
					case s == ShareSame && shape == "":
						shape = ShareSame
					}
				}
			}
			if shape == "" {
				job.Allowed = append(job.Allowed, l.text)
				continue
			}
			job.ExcludedLines = append(job.ExcludedLines, ExcludedLine{Line: l.text})
			if len(job.Excluded) < maxSharedSources {
				job.Excluded = append(job.Excluded, SharedSource{Kind: l.kind, Label: p.Labels[l.text], Reason: shape, HolderState: HolderUnknown})
			}
		}
		job.AllExcluded = len(pl.lines[i]) > 0 && len(job.Allowed) == 0
		jobs[p.ID] = job
	}
	return PlanResult{Routes: pl.in.Routes, Jobs: jobs}
}
