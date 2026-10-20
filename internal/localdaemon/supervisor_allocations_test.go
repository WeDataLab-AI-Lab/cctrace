package localdaemon

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDaemonRecordsWhatEachJobActuallyUses(t *testing.T) {
	t.Parallel()
	r := newRouted(t)
	shared, mine, theirs := r.env.line("codex", "shared"), r.env.line("claude", "mine"), r.env.line("claude", "theirs")
	r.add("default", "one", mine, shared)
	r.add("named:work", "one", theirs, shared)
	r.start()
	r.waitJob("named:work", func(j JobStatus) bool { return j.Sent > 0 })
	r.waitJob("default", func(j JobStatus) bool { return j.Sent > 0 })

	var got AllocationsFile
	eventually(t, func() bool {
		f, err := ReadAllocations(r.root)
		got = f
		return err == nil && len(f.Jobs) == 2
	})
	rt, err := ReadRuntime(r.root)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := ReadRoutes(r.root)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstanceID != rt.InstanceID || got.RoutesRev != routes.Rev {
		t.Fatalf("the file says whose daemon wrote it and at which routes: %+v (daemon %s, routes rev %d)", got, rt.InstanceID, routes.Rev)
	}
	def, work := got.Jobs["default"], got.Jobs["named:work"]
	if !slices.Equal(lineSet(def.Allowed), lineSet([]string{mine, shared})) || len(def.Excluded) != 0 || def.Revision != "1" {
		t.Fatalf("default: %+v", def)
	}
	shareRoute, _ := r.routeOf(shared)
	mineRoute, _ := r.routeOf(mine)
	if !slices.Equal(def.Routes[shared], []string{shareRoute.ID}) || !slices.Equal(def.Routes[mine], []string{mineRoute.ID}) || len(work.Routes[shared]) != 0 {
		t.Fatalf("the routes behind each line, which a run outside the daemon locks: default %v, work %v", def.Routes, work.Routes)
	}
	if !slices.Equal(work.Allowed, []string{theirs}) || len(work.Excluded) != 1 || work.Excluded[0] != (ExcludedLine{Line: shared, Route: shareRoute.ID}) {
		t.Fatalf("work: %+v", work)
	}
	requirePrivate(t, filepath.Join(r.root, "daemon-allocations.json"), 0600)

	// It is written when something changes, not every tick.
	r.waitLoads(3)
	before, _ := os.Stat(filepath.Join(r.root, "daemon-allocations.json"))
	r.waitLoads(20)
	after, _ := os.Stat(filepath.Join(r.root, "daemon-allocations.json"))
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an idle daemon rewrote the allocations")
	}
	// ... and follows a hand-over.
	r.edit("default", dropLine(shared))
	eventually(t, func() bool {
		f, err := ReadAllocations(r.root)
		return err == nil && slices.Contains(f.Jobs["named:work"].Allowed, shared) && !slices.Contains(f.Jobs["default"].Allowed, shared)
	})
}

func TestReadAllocationsRefusesWhatIsNotTheDaemonsFile(t *testing.T) {
	root := t.TempDir()
	if _, err := ReadAllocations(root); !os.IsNotExist(err) {
		t.Fatalf("a missing file reads as not-exist: %v", err)
	}
	path := filepath.Join(root, "daemon-allocations.json")
	if err := os.WriteFile(path, []byte(`{"jobs":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAllocations(root); err == nil || os.IsNotExist(err) {
		t.Fatalf("err = %v", err)
	}
}
