package localdaemon

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// publishOldSnapshot writes the snapshot an earlier daemon left behind, with the
// route signatures the profiles have now unless told otherwise.
func (r *routed) publishOldSnapshot(mutate func(id string, j *JobStatus)) {
	r.t.Helper()
	specs, _ := r.load()
	snap := Snapshot{Runtime: Runtime{PID: 1, InstanceID: "old", StartedAt: time.Now().UTC().Add(-time.Hour), Version: "old"}, ObservedAt: time.Now().UTC(), Complete: true}
	for _, spec := range specs {
		j := JobStatus{ID: spec.ID, Name: spec.Name, Enabled: spec.Enabled, State: "healthy", RouteSignature: routeSignature(spec)}
		mutate(spec.ID, &j)
		snap.Jobs = append(snap.Jobs, j)
	}
	slices.SortFunc(snap.Jobs, func(a, b JobStatus) int { return strings.Compare(a.ID, b.ID) })
	if err := writeJSON(filepath.Join(r.root, "daemon-status.json"), snap); err != nil {
		r.t.Fatal(err)
	}
}

// waitDaemon waits for the first snapshot of the daemon just started, not the
// one an earlier daemon left.
func (r *routed) waitDaemon() {
	r.t.Helper()
	eventually(r.t, func() bool {
		snap := r.snapshot()
		return snap.InstanceID != "" && snap.InstanceID != "old"
	})
}

// The daemon this one replaces kept the owner only as a flag in its snapshot.
// The flag is read once, to make the first routes file, so that an upgrade does
// not move a source to whoever sorts first.
func TestFirstStartKeepsTheOwnerAnEarlierSnapshotNamed(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:zeta", "one", shared)
	r.publishOldSnapshot(func(id string, j *JobStatus) {
		switch id {
		case "named:zeta":
			j.RouteOwner = true
		case "default":
			j.State, j.ErrorCode = "blocked", "duplicate_route"
		}
	})
	r.start()
	r.waitDaemon()
	zeta := r.waitJob("named:zeta", func(j JobStatus) bool { return j.Sent > 0 })
	def := r.waitJob("default", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	if !zeta.RouteOwner || def.RouteOwner {
		t.Fatalf("zeta %+v default %+v", zeta, def)
	}
	f, err := ReadRoutes(r.root)
	if err != nil || len(f.Routes) != 1 || f.Routes[0].Owner != "named:zeta" || f.Routes[0].State != RouteActive {
		t.Fatalf("routes %+v, %v", f, err)
	}
	if n := len(r.allocations("default")); n != 0 {
		t.Fatalf("the blocked profile took %d leases", n)
	}
}

func TestFirstStartGivesNobodyTheSourcesOfABlockedProfileWhoseHolderIsGone(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("named:work", "one", shared)
	r.publishOldSnapshot(func(id string, j *JobStatus) {
		j.State, j.ErrorCode = "blocked", "duplicate_route"
	})
	r.start()
	r.waitDaemon()
	work := r.waitJob("named:work", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	rt, ok := r.routeOf(shared)
	if !ok || rt.State != RouteUnowned || rt.Owner != "" {
		t.Fatalf("an unexplained block must not turn into a guess: %+v", rt)
	}
	if len(work.SharedSources) != 1 || work.SharedSources[0].HolderState != HolderUnowned {
		t.Fatalf("work: %+v", work)
	}
	r.waitLoads(5)
	if n := len(r.allocations("named:work")); n != 0 {
		t.Fatalf("work read a source nobody gave it: %d leases", n)
	}
}

func TestFirstStartIgnoresASnapshotWhoseSourcesHaveChanged(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	r.add("named:zeta", "one", shared)
	r.publishOldSnapshot(func(id string, j *JobStatus) {
		if id == "named:zeta" {
			j.RouteOwner = true
			j.RouteSignature = "from-other-sources"
		}
	})
	r.start()
	r.waitDaemon()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	r.waitJob("named:zeta", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
}

// The snapshot named zeta the owner, but zeta's sources have changed since, so its
// flag does not reserve them all. Default was held back, and zeta is the one owner
// the snapshot named that still has a line over default's: zeta keeps that line
// and default stays held back by it. Leaving the source to nobody would shut zeta
// out of what it was collecting, on no one's say.
func TestFirstStartKeepsTheLineANamedOwnerHeldWhenItsOtherSourcesChanged(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared, more := r.env.line("codex", "shared"), r.env.line("claude", "more")
	r.add("default", "one", shared)
	r.add("named:zeta", "one", shared, more)
	r.publishOldSnapshot(func(id string, j *JobStatus) {
		switch id {
		case "named:zeta":
			j.RouteOwner = true
			j.RouteSignature = "from-other-sources"
		case "default":
			j.State, j.ErrorCode = "blocked", "duplicate_route"
		}
	})
	r.start()
	r.waitDaemon()
	def := r.waitJob("default", func(j JobStatus) bool { return j.ErrorCode == "duplicate_route" })
	r.waitJob("named:zeta", func(j JobStatus) bool { return j.Sent > 0 })
	if rt, ok := r.routeOf(shared); !ok || rt.Owner != "named:zeta" || rt.State != RouteActive {
		t.Fatalf("the owner the snapshot named keeps the line it still has: %+v", rt)
	}
	if !r.everAllowed("named:zeta", shared) {
		t.Fatalf("zeta was shut out of the source it held: %+v", r.allocations("named:zeta"))
	}
	if len(def.SharedSources) != 1 || def.SharedSources[0].Holder != "named:zeta" || def.SharedSources[0].HolderState != HolderActive {
		t.Fatalf("default: %+v", def)
	}
}

func TestFirstStartTellsAProfileItWasBlockedBeforeItsStateIsMade(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared, mine := r.env.line("codex", "shared"), r.env.line("claude", "mine")
	r.add("default", "one", shared)
	r.add("named:work", "one", shared, mine)
	r.publishOldSnapshot(func(id string, j *JobStatus) {
		switch id {
		case "default":
			j.RouteOwner = true
		case "named:work":
			j.State, j.ErrorCode = "blocked", "duplicate_route"
		}
	})
	r.start()
	r.waitDaemon()
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	first := r.allocations("named:work")[0]
	if !first.AfterBlock || !slices.Equal(first.Allowed, []string{mine}) {
		t.Fatalf("the first lease after a block is marked: %+v", first)
	}
	// Only the first.
	r.edit("named:work", func(p *rprofile) { p.lines = append(slices.Clone(p.lines), r.env.line("claude", "more")) })
	eventually(t, func() bool { return len(r.allocations("named:work")) >= 2 })
	if r.allocations("named:work")[1].AfterBlock {
		t.Fatal("the mark outlived the lease it was for")
	}
}

func TestFirstStartWithAnUnreadableSnapshotSaysSoAndGoesOn(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared := r.env.line("codex", "shared")
	r.add("default", "one", shared)
	if err := os.WriteFile(filepath.Join(r.root, "daemon-status.json"), []byte(`{"jobs":[`), 0600); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.waitDaemon()
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })
	said := 0
	for _, l := range r.logLines() {
		if l == "[routes] snapshot-unreadable" {
			said++
		}
	}
	if said != 1 {
		t.Fatalf("log = %q", r.logLines())
	}
}
