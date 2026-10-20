package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/samefolder"
)

// sharedPair is two profiles to one destination that both collect the one Codex
// folder. "holder" sorts before "work", so with nothing reserved it is the one
// that reads.
type sharedPair struct {
	shared       string
	holder, work profile.Entry
}

func newSharedPair(t *testing.T) sharedPair {
	t.Helper()
	home := useTempSyncHome(t)
	shared := filepath.Join(home, "shared-codex")
	if err := os.MkdirAll(filepath.Join(shared, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	same := func(p *profile.Profile) {
		p.User = profile.UserInfo{ID: "user-1", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
	}
	holder, _ := codexProfile(t, "holder", shared, same)
	work, _ := codexProfile(t, "work", shared, same)
	return sharedPair{shared: shared, holder: holder, work: work}
}

// publishDaemon leaves what a running daemon leaves: its lock for the rest of the
// test, its runtime, and the allocations and routes it is given.
func publishDaemon(t *testing.T, instance string, jobs map[string]localdaemon.JobAllocation, routes *localdaemon.RouteFile) {
	t.Helper()
	root := profile.DefaultDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(root, "daemon.lock"))
	if ok, err := lock.TryLock(); err != nil || !ok {
		t.Fatalf("fixture daemon lock: %v %v", ok, err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	writeJSONFile0600(t, filepath.Join(root, "daemon-runtime.json"), localdaemon.Runtime{PID: os.Getpid(), InstanceID: "inst-gate", StartedAt: time.Now().UTC()})
	writeJSONFile0600(t, filepath.Join(root, "daemon-allocations.json"), localdaemon.AllocationsFile{InstanceID: instance, Jobs: jobs})
	if routes != nil {
		if _, err := localdaemon.WriteRoutes(root, *routes); err != nil {
			t.Fatal(err)
		}
	}
}

func gateRoute(id, owner, folder, destination string) localdaemon.Route {
	return localdaemon.Route{ID: id, Rev: 1, Kind: "codex", Path: folder, Destination: destination, Owner: owner, State: localdaemon.RouteActive, Since: time.Now().UTC().Add(-time.Hour)}
}

func destinationOf(entry profile.Entry) string {
	return profileJobSpec(entry, daemonOverrides{}).Destination
}

func applyOnce(t *testing.T, g *lineGate, kind string, dirs ...string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return g.apply(ctx, kind, dirs)
}

func TestLineGateFollowsTheRunningDaemonsAllocations(t *testing.T) {
	w := newSharedPair(t)
	publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{
		"named:holder": {Revision: w.holder.Revision, Allowed: []string{"codex:" + w.shared}},
		"named:work":   {Revision: w.work.Revision, Allowed: []string{}, Excluded: []localdaemon.ExcludedLine{{Line: "codex:" + w.shared, Route: "4c7e19ab"}}},
	}, nil)

	work := newLineGate("work", false)
	if got := applyOnce(t, work, "codex", w.shared); len(got) != 0 || !work.allHeld() {
		t.Fatalf("work read %v, allHeld %v", got, work.allHeld())
	}
	holder := newLineGate("holder", false)
	if got := applyOnce(t, holder, "codex", w.shared); !slices.Equal(got, []string{w.shared}) || holder.allHeld() {
		t.Fatalf("holder read %v, allHeld %v", got, holder.allHeld())
	}
	// A folder no profile's decision has is nobody's: a run given it by a flag or
	// the environment reads it, under the lock of that source, so that two such runs
	// do not read it at once.
	other := filepath.Join(filepath.Dir(w.shared), "elsewhere")
	if got := applyOnce(t, work, "codex", other); !slices.Equal(got, []string{other}) {
		t.Fatalf("a folder nobody collects = %v", got)
	}
	assertLineBusy(t, localdaemon.SourceLockID("codex:"+other, destinationOf(w.work)))
	work.close()
	holder.close()
}

// assertLineBusy fails the test unless the line lock id is held.
func assertLineBusy(t *testing.T, id string) {
	t.Helper()
	release, err := localdaemon.LockLines(context.Background(), profile.DefaultDir(), []string{id}, 0)
	if errors.Is(err, localdaemon.ErrLineBusy) {
		return
	}
	if err == nil {
		release()
	}
	t.Fatalf("line lock %q is not held: %v", id, err)
}

// requireLineLocksReleased fails the test if a line lock under the profile root
// is still held when it ends: a run gives its lines up before it returns
// (lineGate.close), and Windows cannot remove a lock file that is still open,
// so the test's directory would not go either. It runs before the cleanups
// registered earlier, the temp home's removal among them.
func requireLineLocksReleased(t *testing.T) {
	root := profile.DefaultDir()
	t.Cleanup(func() {
		entries, err := os.ReadDir(filepath.Join(root, "route-locks"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("line locks: %v", err)
		}
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".lock")
			if !ok {
				continue
			}
			release, err := localdaemon.LockLines(context.Background(), root, []string{id}, 0)
			if err != nil {
				t.Errorf("line lock %s is still held when the test ends: %v", id, err)
				continue
			}
			release()
		}
	})
}

// A line no route covers yet is read under the lock of its source: the lock the
// daemon's job takes once the daemon reserves the line, for whichever profile.
// Profile "holder" sorts first, so the daemon gives it the line "work" is reading.
func TestLineGateAndTheDaemonThatReservesTheLineLaterTakeOneLock(t *testing.T) {
	home := useTempSyncHome(t)
	resetSyncControlHooks(t)
	shared := filepath.Join(home, "shared-codex")
	if err := os.MkdirAll(filepath.Join(shared, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	same := func(p *profile.Profile) {
		p.User = profile.UserInfo{ID: "user-1", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
	}
	work, _ := codexProfile(t, "work", shared, same)
	g := newLineGate("work", false)
	if got := applyOnce(t, g, "codex", shared); !slices.Equal(got, []string{shared}) {
		t.Fatalf("work alone read %v", got)
	}
	lock := localdaemon.SourceLockID("codex:"+shared, destinationOf(work))
	assertLineBusy(t, lock)

	codexProfile(t, "holder", shared, same)
	root := profile.DefaultDir()
	var mu sync.Mutex
	var tries []error
	startSupervisor(t, localdaemon.New(localdaemon.Config{Root: root, Refresh: 5 * time.Millisecond, Load: func() ([]localdaemon.Spec, error) {
		entries, err := (profile.Registry{Root: root}).List()
		if err != nil {
			return nil, err
		}
		var specs []localdaemon.Spec
		for _, entry := range entries {
			spec := profileJobSpec(entry, daemonOverrides{})
			id := spec.ID
			spec.Quiesce = nil
			spec.Acquire = func(ctx context.Context, alloc localdaemon.Allocation) (func(), error) {
				release, err := localdaemon.LockLines(ctx, root, alloc.Routes, 0)
				if id == "named:holder" {
					mu.Lock()
					if !slices.Contains(alloc.Routes, lock) {
						err = fmt.Errorf("the daemon named the route %v, not %s", alloc.Routes, lock)
					}
					tries = append(tries, err)
					mu.Unlock()
				}
				return release, err
			}
			spec.Run = func(_ context.Context, report func(localdaemon.Progress)) error {
				report(localdaemon.Progress{State: "healthy"})
				return nil
			}
			specs = append(specs, spec)
		}
		return specs, nil
	}}))
	tried := func(ok func(error) bool) bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.ContainsFunc(tries, ok)
	}
	managedEventually(t, func() bool { return tried(func(err error) bool { return err != nil }) })
	if tried(func(err error) bool { return err == nil || !errors.Is(err, localdaemon.ErrLineBusy) }) {
		t.Fatalf("the holder's job read the line while work's run still did: %v", tries)
	}
	g.close()
	managedEventually(t, func() bool { return tried(func(err error) bool { return err == nil }) })
}

// Two profiles that may not collect (here: only registered) both have a line no
// route covers: each run reads it under the source's lock, never both at once.
func TestLineGateTwoRunsOfAnUnreservedLineDoNotReadItAtOnce(t *testing.T) {
	home := useTempSyncHome(t)
	shared := filepath.Join(home, "shared-codex")
	if err := os.MkdirAll(filepath.Join(shared, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	idle := func(p *profile.Profile) {
		p.User = profile.UserInfo{ID: "user-1", Name: "one", Email: "one@fixture.invalid", Team: "fixture"}
		p.RegistrationOnly = true
	}
	codexProfile(t, "holder", shared, idle)
	codexProfile(t, "work", shared, idle)
	prev := oneShotLineWait
	oneShotLineWait = 30 * time.Millisecond
	t.Cleanup(func() { oneShotLineWait = prev })

	first := newLineGate("holder", false)
	if got := applyOnce(t, first, "codex", shared); !slices.Equal(got, []string{shared}) {
		t.Fatalf("holder read %v", got)
	}
	if got := applyOnce(t, newLineGate("work", false), "codex", shared); len(got) != 0 {
		t.Fatalf("work read %v while holder's run did", got)
	}
	first.close()
	second := newLineGate("work", false)
	defer second.close()
	if got := applyOnce(t, second, "codex", shared); !slices.Equal(got, []string{shared}) {
		t.Fatalf("work read %v once holder's run was done", got)
	}
}

// A folder the decision gives another profile, and not this one, may not be read
// by this one under a flag or the environment: nothing says it is not shared.
func TestLineGateLeavesAFolderTheDecisionGivesOnlyToAnotherProfile(t *testing.T) {
	t.Run("the running daemon's record", func(t *testing.T) {
		w := newSharedPair(t)
		theirs := filepath.Join(filepath.Dir(w.shared), "theirs")
		if err := os.MkdirAll(filepath.Join(theirs, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{
			"named:holder": {Revision: w.holder.Revision, Allowed: []string{"codex:" + w.shared, "codex:" + theirs}},
			"named:work":   {Revision: w.work.Revision, Allowed: []string{"codex:" + w.shared}},
		}, nil)
		if got := applyOnce(t, newLineGate("work", false), "codex", theirs, filepath.Join(theirs, "sessions", "2026")); len(got) != 0 {
			t.Fatalf("work read %v, the holder's folders", got)
		}
	})
	t.Run("the plan with no daemon", func(t *testing.T) {
		w := newSharedPair(t)
		theirs := filepath.Join(filepath.Dir(w.shared), "theirs")
		if err := os.MkdirAll(filepath.Join(theirs, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		codexProfile(t, "third", theirs, func(p *profile.Profile) {
			p.User = w.holder.Profile.User
		})
		if got := applyOnce(t, newLineGate("work", false), "codex", theirs); len(got) != 0 {
			t.Fatalf("work read %v, the third profile's folder", got)
		}
	})
}

// Overlap is between profiles that send to one destination, as Plan has it: a
// folder only a profile of another destination collects is no source this one
// shares, and a run given it by a flag or the environment (CODEX_HOME the hook's
// child inherits) reads it.
func TestLineGateReadsAFolderOnlyAnotherDestinationCollects(t *testing.T) {
	setup := func(t *testing.T) (work, other profile.Entry, theirs string) {
		home := useTempSyncHome(t)
		mine := filepath.Join(home, "mine-codex")
		theirs = filepath.Join(home, "theirs-codex")
		for _, d := range []string{mine, theirs} {
			if err := os.MkdirAll(filepath.Join(d, "sessions"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		work, _ = codexProfile(t, "work", mine)
		other, _ = codexProfile(t, "other", theirs)
		if destinationOf(work) == destinationOf(other) {
			t.Fatal("fixture: one destination")
		}
		return work, other, theirs
	}
	t.Run("the running daemon's record", func(t *testing.T) {
		work, other, theirs := setup(t)
		publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{
			"named:other": {Revision: other.Revision, Allowed: []string{"codex:" + theirs}},
			"named:work":  {Revision: work.Revision, Allowed: []string{}},
		}, nil)
		g := newLineGate("work", false)
		defer g.close()
		if got := applyOnce(t, g, "codex", theirs); !slices.Equal(got, []string{theirs}) {
			t.Fatalf("work read %v", got)
		}
	})
	t.Run("the plan with no daemon", func(t *testing.T) {
		_, _, theirs := setup(t)
		g := newLineGate("work", false)
		defer g.close()
		if got := applyOnce(t, g, "codex", theirs); !slices.Equal(got, []string{theirs}) {
			t.Fatalf("work read %v", got)
		}
	})
}

// With no usable routes file nobody knows who owns a shared line, so neither
// profile reads it, and a profile with nothing else is held back entirely.
func TestLineGateKeepsOffSharedLinesWhenTheRoutesFileCannotBeUsed(t *testing.T) {
	w := newSharedPair(t)
	if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "daemon-routes.json"), []byte(`{"version":2,"rev":1,"routes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"holder", "work"} {
		g := newLineGate(name, false)
		if got := applyOnce(t, g, "codex", w.shared); len(got) != 0 || !g.allHeld() {
			t.Fatalf("%s read %v, allHeld %v", name, got, g.allHeld())
		}
		g.close()
	}
}

// A folder is known by what it is, not by how it is spelled: the folder another
// profile holds is not read under another spelling, and the holder reads it under
// the line's lock whatever spelling it was given.
func TestLineGateKnowsAFolderByAnySpelling(t *testing.T) {
	w := newSharedPair(t)
	const id = "4c7e19ab"
	routes := &localdaemon.RouteFile{Version: 1, Rev: 2, Routes: []localdaemon.Route{gateRoute(id, "named:holder", w.shared, destinationOf(w.holder))}}
	publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{
		"named:holder": {Revision: w.holder.Revision, Allowed: []string{"codex:" + w.shared}, Routes: map[string][]string{"codex:" + w.shared: {id}}},
		"named:work":   {Revision: w.work.Revision, Allowed: []string{}, Excluded: []localdaemon.ExcludedLine{{Line: "codex:" + w.shared, Route: id}}},
	}, routes)
	sep := string(filepath.Separator)
	spellings := []string{w.shared + sep + ".", w.shared + sep}
	if link := filepath.Join(filepath.Dir(w.shared), "link-to-shared"); os.Symlink(w.shared, link) == nil {
		spellings = append(spellings, link)
	}
	if upper := filepath.Join(filepath.Dir(w.shared), strings.ToUpper(filepath.Base(w.shared))); samefolder.Same(w.shared, upper) {
		spellings = append(spellings, upper) // a disk that ignores case
	}
	root := profile.DefaultDir()
	for _, s := range spellings {
		if got := applyOnce(t, newLineGate("work", false), "codex", s); len(got) != 0 {
			t.Fatalf("work read %q, a spelling of the folder the holder owns", s)
		}
		holder := newLineGate("holder", false)
		if got := applyOnce(t, holder, "codex", s); !slices.Equal(got, []string{s}) {
			t.Fatalf("holder read %v for %q", got, s)
		}
		if release, err := localdaemon.LockLines(context.Background(), root, []string{id}, 0); !errors.Is(err, localdaemon.ErrLineBusy) {
			if err == nil {
				release()
			}
			t.Fatalf("holder read %q without the line's lock: %v", s, err)
		}
		holder.close()
	}
}

// What the daemon recorded is used only while that daemon runs and only for the
// profile as it still is; otherwise the registry decides, as with no daemon.
func TestLineGatePlansFromTheRegistryWhenTheDaemonsRecordIsNotCurrent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		instance string
		revision func(w sharedPair) string
	}{
		{"another run of the daemon wrote it", "an-earlier-daemon", func(w sharedPair) string { return w.work.Revision }},
		{"the profile has been edited since", "inst-gate", func(sharedPair) string { return "an-older-revision" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newSharedPair(t)
			publishDaemon(t, tc.instance, map[string]localdaemon.JobAllocation{
				"named:work": {Revision: tc.revision(w), Allowed: []string{"codex:" + w.shared}},
			}, nil)
			if got := applyOnce(t, newLineGate("work", false), "codex", w.shared); len(got) != 0 {
				t.Fatalf("work read %v from a record that is not current", got)
			}
		})
	}
}

func TestLineGateWithNoDaemonPlansFromTheRegistryAndWritesNothing(t *testing.T) {
	w := newSharedPair(t)
	holder := newLineGate("holder", false)
	defer holder.close()
	if got := applyOnce(t, holder, "codex", w.shared); !slices.Equal(got, []string{w.shared}) {
		t.Fatalf("holder read %v", got)
	}
	work := newLineGate("work", false)
	if got := applyOnce(t, work, "codex", w.shared); len(got) != 0 || !work.allHeld() {
		t.Fatalf("work read %v", got)
	}
	if _, err := os.Stat(filepath.Join(profile.DefaultDir(), "daemon-routes.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a run outside the daemon decided something for good: %v", err)
	}
	// A profile the registry does not know is nobody's to hold back.
	if g := newLineGate("nobody", false); g != nil {
		t.Fatalf("a gate for a profile that is not there: %+v", g)
	}
}

// The lines a run reads are locked for the run, and a line somebody else reads is
// left alone rather than waited for.
func TestLineGateLocksTheLinesItReadsAndLeavesBusyOnes(t *testing.T) {
	w := newSharedPair(t)
	const id = "4c7e19ab"
	routes := &localdaemon.RouteFile{Version: 1, Rev: 2, Routes: []localdaemon.Route{gateRoute(id, "named:holder", w.shared, destinationOf(w.holder))}}
	publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{
		"named:holder": {Revision: w.holder.Revision, Allowed: []string{"codex:" + w.shared}, Routes: map[string][]string{"codex:" + w.shared: {id}}},
	}, routes)
	prev := oneShotLineWait
	oneShotLineWait = 30 * time.Millisecond
	t.Cleanup(func() { oneShotLineWait = prev })

	root := profile.DefaultDir()
	elsewhere, err := localdaemon.LockLines(context.Background(), root, []string{id}, 0)
	if err != nil {
		t.Fatal(err)
	}
	busy := newLineGate("holder", false)
	if got := applyOnce(t, busy, "codex", w.shared); len(got) != 0 {
		t.Fatalf("read a line another run holds: %v", got)
	}
	elsewhere()

	g := newLineGate("holder", false)
	if got := applyOnce(t, g, "codex", w.shared); !slices.Equal(got, []string{w.shared}) {
		t.Fatalf("read %v", got)
	}
	if _, err := localdaemon.LockLines(context.Background(), root, []string{id}, 0); !errors.Is(err, localdaemon.ErrLineBusy) {
		t.Fatalf("a line being read is not locked: %v", err)
	}
	g.close()
	release, err := localdaemon.LockLines(context.Background(), root, []string{id}, 0)
	if err != nil {
		t.Fatalf("closing the gate left the line locked: %v", err)
	}
	release()

	// A gate that only looks (a dry run) takes no lock.
	look := newLineGate("holder", true)
	if got := applyOnce(t, look, "codex", w.shared); !slices.Equal(got, []string{w.shared}) {
		t.Fatalf("read %v", got)
	}
	release, err = localdaemon.LockLines(context.Background(), root, []string{id}, 0)
	if err != nil {
		t.Fatalf("a dry run took a lock: %v", err)
	}
	release()
}

// The owner can change between the daemon's decision and the lock; a line whose
// route is no longer the profile's is not read.
func TestLineGateLeavesALineWhoseRouteHasChangedHands(t *testing.T) {
	w := newSharedPair(t)
	const id = "4c7e19ab"
	routes := &localdaemon.RouteFile{Version: 1, Rev: 2, Routes: []localdaemon.Route{gateRoute(id, "named:work", w.shared, destinationOf(w.holder))}}
	publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{
		"named:holder": {Revision: w.holder.Revision, Allowed: []string{"codex:" + w.shared}, Routes: map[string][]string{"codex:" + w.shared: {id}}},
	}, routes)
	if got := applyOnce(t, newLineGate("holder", false), "codex", w.shared); len(got) != 0 {
		t.Fatalf("read a line whose route belongs to another profile: %v", got)
	}
}

// A line just handed to the profile is read first by the daemon's job, which takes
// over the cursor the last reader left: a run outside the daemon that read it
// before then would start from its own cursor and send what that reader had left
// unsent on purpose.
func TestLineGateLeavesALineWhoseCursorIsNotTakenOverYet(t *testing.T) {
	w := newSharedPair(t)
	const id = "4c7e19ab"
	r := gateRoute(id, "named:work", w.shared, destinationOf(w.work))
	r.From, r.Handoff = "named:holder", strings.Repeat("cd", 16)
	publishDaemon(t, "inst-gate", map[string]localdaemon.JobAllocation{
		"named:holder": {Revision: w.holder.Revision, Allowed: []string{}, Excluded: []localdaemon.ExcludedLine{{Line: "codex:" + w.shared, Route: id}}},
		"named:work":   {Revision: w.work.Revision, Allowed: []string{"codex:" + w.shared}, Routes: map[string][]string{"codex:" + w.shared: {id}}},
	}, &localdaemon.RouteFile{Version: 1, Rev: 1, Routes: []localdaemon.Route{r}})
	g := newLineGate("work", false)
	defer g.close()
	if got := applyOnce(t, g, "codex", w.shared); len(got) != 0 {
		t.Fatalf("work read %v before its daemon job took the cursor over", got)
	}
}

// With no profile list there is nobody to plan against; what the routes file
// gives the profile is the only thing that can be read, and the rest waits.
func TestLineGateWithAnUnreadableRegistryReadsOnlyWhatTheRoutesFileGivesTheProfile(t *testing.T) {
	w := newSharedPair(t)
	other := filepath.Join(filepath.Dir(w.shared), "unreserved")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	routes := &localdaemon.RouteFile{Version: 1, Rev: 2, Routes: []localdaemon.Route{gateRoute("4c7e19ab", "named:holder", w.shared, destinationOf(w.holder))}}
	if _, err := localdaemon.WriteRoutes(profile.DefaultDir(), *routes); err != nil {
		t.Fatal(err)
	}
	// A file where a profile directory belongs makes the list unsafe to read.
	if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "profiles", "stray"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (profile.Registry{Root: profile.DefaultDir()}).List(); err == nil {
		t.Fatal("fixture: the registry is still readable")
	}

	holder := newLineGate("holder", false)
	defer holder.close()
	if got := applyOnce(t, holder, "codex", w.shared, other); !slices.Equal(got, []string{w.shared}) {
		t.Fatalf("holder read %v; its own route yes, a folder nobody reserved no", got)
	}
	if got := applyOnce(t, newLineGate("work", false), "codex", w.shared); len(got) != 0 {
		t.Fatalf("work read %v from a route that is the holder's", got)
	}
}
