package localdaemon

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func offlineSpec(id, dest string, lines ...string) Spec {
	return Spec{ID: id, Name: id, Destination: testDest(dest), Sources: lines, Enabled: true}
}

// With no daemon there is nobody to decide, so the answer is the one the daemon
// would give from the file it left, and nothing is written.
func TestPlanOfflineFollowsTheRoutesFileAndWritesNothing(t *testing.T) {
	e := newPlanEnv(t)
	root := e.folder("daemon")
	shared := e.line("codex", "shared")
	specs := []Spec{offlineSpec("default", "one", shared), offlineSpec("named:work", "one", shared)}

	// No file yet: the first profile by ID would take it.
	res := PlanOffline(root, specs)
	if got, _ := res.Job("default"); !slices.Equal(got.Allowed, []string{shared}) {
		t.Fatalf("default = %+v", got)
	}
	work, _ := res.Job("named:work")
	if len(work.Allowed) != 0 || len(work.Excluded) != 1 || !work.AllExcluded || work.Excluded[0].Kind != "codex" {
		t.Fatalf("work = %+v", work)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("an offline plan wrote %d files", len(entries))
	}

	// A file the daemon left names an owner, and the owner is kept.
	owner := Route{ID: "0a0b0c0d", Rev: 1, Kind: "codex", Path: shared[len("codex:"):], Destination: testDest("one"), Owner: "named:work", State: RouteActive, Since: testNow}
	if _, err := WriteRoutes(root, RouteFile{Version: routesVersion, Rev: 1, Routes: []Route{owner}}); err != nil {
		t.Fatal(err)
	}
	res = PlanOffline(root, specs)
	if got, _ := res.Job("default"); len(got.Allowed) != 0 || len(got.Excluded) != 1 || got.Excluded[0].Source != "0a0b0c0d" || got.Excluded[0].Holder != "named:work" {
		t.Fatalf("default = %+v", got)
	}
	if got, _ := res.Job("named:work"); !slices.Equal(got.Allowed, []string{shared}) {
		t.Fatalf("work = %+v", got)
	}
}

// A routes file that cannot be used leaves every shared line closed, and a line
// no other profile shares is still the profile's.
func TestPlanOfflineClosesSharedLinesWhenTheRoutesFileCannotBeUsed(t *testing.T) {
	e := newPlanEnv(t)
	root := e.folder("daemon")
	shared, own := e.line("codex", "shared"), e.line("claude", "own")
	if err := os.WriteFile(filepath.Join(root, routesFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := PlanOffline(root, []Spec{offlineSpec("default", "one", shared, own), offlineSpec("named:work", "one", shared)})
	def, _ := res.Job("default")
	closed := []ExcludedLine{{Line: shared}}
	if !slices.Equal(def.Allowed, []string{own}) || len(def.Excluded) != 1 || def.Excluded[0].HolderState != HolderUnknown || !slices.Equal(def.ExcludedLines, closed) {
		t.Fatalf("default = %+v", def)
	}
	if work, _ := res.Job("named:work"); len(work.Allowed) != 0 || !work.AllExcluded || !slices.Equal(work.ExcludedLines, closed) {
		t.Fatalf("work = %+v", work)
	}
	if _, ok := res.Job("named:nobody"); ok {
		t.Fatal("a profile that was not planned has a plan")
	}
}

// A run outside the daemon finds its folders among the lines of a decision by
// what they are, the way Plan judges overlap, not by how they are spelled.
func TestSourceShapeKnowsAFolderByAnySpelling(t *testing.T) {
	e := newPlanEnv(t)
	h := e.folder("h")
	cases := []struct{ a, b, want string }{
		{"codex:" + h, "codex:" + h + string(filepath.Separator) + ".", ShareSame},
		{"codex:" + h, "codex:" + h + string(filepath.Separator), ShareSame},
		{"codex:" + h, "codex:" + filepath.Join(h, "sessions", "2026"), ShareNested},
		{"codex:" + h, "claude:" + h, ""},
		{"codex:" + h, "codex:" + e.folder("other"), ""},
		{"cursor:" + h, "cursor:" + h, ""},
	}
	if link := filepath.Join(e.root, "link"); os.Symlink(h, link) == nil {
		cases = append(cases, struct{ a, b, want string }{"codex:" + h, "codex:" + link, ShareSame})
	}
	for _, tc := range cases {
		if got := SourceShape(tc.a, tc.b); got != tc.want {
			t.Errorf("SourceShape(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

// A line nobody has reserved is locked by the id the daemon gives its route, so a
// run that reads it before the daemon reserves it and the job that reads it after
// take one lock, whichever profile the route goes to and however the folder is
// spelled.
func TestPlanNamesANewRouteAfterItsSource(t *testing.T) {
	e := newPlanEnv(t)
	shared := e.line("codex", "shared")
	alias := shared
	if link := filepath.Join(e.root, "link"); os.Symlink(strings.TrimPrefix(shared, "codex:"), link) == nil {
		alias = "codex:" + link
	}
	in := e.in(e.profile("default", "one", alias), e.profile("named:work", "one", shared))
	in.NewID = nil
	res := e.plan(in)
	want := SourceLockID(shared, testDest("one"))
	if len(res.Routes.Routes) != 1 || want == "" || res.Routes.Routes[0].ID != want {
		t.Fatalf("routes %+v, want one named %q", res.Routes.Routes, want)
	}
	if SourceLockID(alias, testDest("one")) != want || SourceLockID(shared, testDest("two")) == want {
		t.Fatal("the name is the folder's and the destination's, not the spelling's")
	}
	if SourceLockID("cursor:"+e.folder("x"), testDest("one")) != "" || SourceLockID(shared, "") != "" {
		t.Fatal("a line no route can be made for has no lock of its own")
	}
}

// On a disk that ignores case, two case spellings of one folder are one source
// and get one name: a run that read one spelling before the line was reserved
// holds the lock the daemon's job takes for the other.
func TestPlanNamesANewRouteAfterItsSourceInAnyCase(t *testing.T) {
	e := newPlanEnv(t)
	lower := e.line("codex", "shared")
	if err := os.MkdirAll(filepath.Join(strings.TrimPrefix(lower, "codex:"), "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	upper := "codex:" + filepath.Join(e.root, "SHARED")
	if SourceShape(lower, upper) != ShareSame {
		t.Skip("the disk tells case apart")
	}
	in := e.in(e.profile("default", "one", upper))
	in.NewID = nil
	res := e.plan(in)
	if want := SourceLockID(lower, testDest("one")); len(res.Routes.Routes) != 1 || res.Routes.Routes[0].ID != want {
		t.Fatalf("routes %+v for the upper-case spelling; a run of the lower-case one locks %q", res.Routes.Routes, want)
	}
}

// A run that locked a line by its source id may read it while no route goes by
// that id, or while the one that does is the profile's to read.
func TestConfirmUnreservedHoldsUntilTheLineIsSomeoneElses(t *testing.T) {
	root, dir := t.TempDir(), canonicalSource(t.TempDir())
	const id = "0000000a"
	if err := ConfirmUnreserved(root, id, "default"); err != nil {
		t.Fatalf("no file reserves nothing: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, routesFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmUnreserved(root, id, "default"); err != nil {
		t.Fatalf("a file that cannot be used reserves nothing: %v", err)
	}
	write := func(r Route) {
		t.Helper()
		if _, err := WriteRoutes(root, RouteFile{Version: 1, Routes: []Route{r}}); err != nil {
			t.Fatal(err)
		}
	}
	write(realRoute(dir, "0000000b", "b"))
	if err := ConfirmUnreserved(root, id, "default"); err != nil {
		t.Fatalf("another line's route: %v", err)
	}
	theirs := realRoute(dir, id, "a")
	theirs.Owner = "named:work"
	write(theirs)
	if err := ConfirmUnreserved(root, id, "default"); !errors.Is(err, ErrRouteChanged) {
		t.Fatalf("the daemon gave the line to another profile: %v", err)
	}
	mine := realRoute(dir, id, "a")
	write(mine)
	if err := ConfirmUnreserved(root, id, "default"); err != nil {
		t.Fatalf("the daemon gave the line to this profile: %v", err)
	}
	mine.From, mine.Handoff = "named:work", testHandoff("x")
	write(mine)
	if err := ConfirmUnreserved(root, id, "default"); !errors.Is(err, ErrHandoffPending) {
		t.Fatalf("a cursor still to be taken over: %v", err)
	}
}
