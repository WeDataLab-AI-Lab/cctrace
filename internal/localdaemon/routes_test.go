package localdaemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/atomicfile"
)

var testNow = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

// testDest returns a destination hash for a name. Fixtures build their hashes
// the way profileJobSpec does instead of spelling them out.
func testDest(name string) string {
	sum := sha256.Sum256([]byte("destination\x00" + name))
	return hex.EncodeToString(sum[:])
}

func testHandoff(name string) string {
	sum := sha256.Sum256([]byte("handoff\x00" + name))
	return hex.EncodeToString(sum[:16])
}

// route builds a valid active route owned by "default" at a folder under dir.
func route(dir, id, kind, folder string) Route {
	return Route{ID: id, Rev: 1, Kind: kind, Path: filepath.Join(dir, folder), Destination: testDest("one"), Owner: "default", State: RouteActive, Since: testNow.Add(-time.Hour)}
}

func routeFile(routes ...Route) RouteFile {
	return RouteFile{Version: 1, Rev: 3, Routes: routes}
}

func invalidReason(t *testing.T, err error) string {
	t.Helper()
	var invalid *InvalidRoutesError
	if !errors.As(err, &invalid) {
		t.Fatalf("error %v is not an *InvalidRoutesError", err)
	}
	return invalid.Reason
}

func TestValidateRoutes(t *testing.T) {
	dir := canonicalSource(t.TempDir())
	base := func() RouteFile {
		return routeFile(route(dir, "3fa91c2d", "codex", "a"), route(dir, "9b01d7e4", "claude", "b"))
	}
	tests := []struct {
		name   string
		mutate func(*RouteFile)
		want   string // "" = valid
	}{
		{"valid", func(*RouteFile) {}, ""},
		{"valid with every optional field", func(f *RouteFile) {
			r := &f.Routes[0]
			r.Aliases = []string{testDest("a"), testDest("b"), testDest("c"), testDest("d")}
			r.Owner, r.From, r.Handoff = "named:work", "named:old_1", testHandoff("x")
		}, ""},
		{"valid empty file", func(f *RouteFile) { f.Routes = nil }, ""},
		{"valid released and unowned", func(f *RouteFile) {
			f.Routes[0].State, f.Routes[0].Owner, f.Routes[0].From = RouteUnowned, "", "default"
			f.Routes[1].State, f.Routes[1].To = RouteReleasing, "named:work"
		}, ""},
		{"version zero", func(f *RouteFile) { f.Version = 0 }, "version"},
		{"version from the future", func(f *RouteFile) { f.Version = 2 }, "version"},
		{"missing id", func(f *RouteFile) { f.Routes[0].ID = "" }, "missing_field"},
		{"missing kind", func(f *RouteFile) { f.Routes[0].Kind = "" }, "missing_field"},
		{"missing path", func(f *RouteFile) { f.Routes[0].Path = "" }, "missing_field"},
		{"missing destination", func(f *RouteFile) { f.Routes[0].Destination = "" }, "missing_field"},
		{"missing state", func(f *RouteFile) { f.Routes[0].State = "" }, "missing_field"},
		{"missing since", func(f *RouteFile) { f.Routes[0].Since = time.Time{} }, "missing_field"},
		{"id too short", func(f *RouteFile) { f.Routes[0].ID = "3fa91c2" }, "id"},
		{"id too long", func(f *RouteFile) { f.Routes[0].ID = "3fa91c2d0" }, "id"},
		{"id upper case", func(f *RouteFile) { f.Routes[0].ID = "3FA91C2D" }, "id"},
		{"id not hex", func(f *RouteFile) { f.Routes[0].ID = "3fa91c2g" }, "id"},
		{"id traversal", func(f *RouteFile) { f.Routes[0].ID = "../../x." }, "id"},
		{"duplicate id", func(f *RouteFile) { f.Routes[1].ID = f.Routes[0].ID }, "duplicate_id"},
		{"unknown kind", func(f *RouteFile) { f.Routes[0].Kind = "cursor" }, "kind"},
		{"relative path", func(f *RouteFile) { f.Routes[0].Path = "relative/folder" }, "path"},
		{"path not cleaned", func(f *RouteFile) { f.Routes[0].Path = dir + "/a/../a" }, "path"},
		{"path with trailing slash", func(f *RouteFile) { f.Routes[0].Path += "/" }, "path"},
		{"destination too short", func(f *RouteFile) { f.Routes[0].Destination = f.Routes[0].Destination[1:] }, "destination"},
		{"destination not hex", func(f *RouteFile) { f.Routes[0].Destination = strings.Repeat("g", 64) }, "destination"},
		{"destination upper case", func(f *RouteFile) { f.Routes[0].Destination = strings.ToUpper(f.Routes[0].Destination) }, "destination"},
		{"five aliases", func(f *RouteFile) {
			f.Routes[0].Aliases = []string{testDest("a"), testDest("b"), testDest("c"), testDest("d"), testDest("e")}
		}, "aliases"},
		{"alias not hex", func(f *RouteFile) { f.Routes[0].Aliases = []string{"nope"} }, "aliases"},
		{"owner is not a profile id", func(f *RouteFile) { f.Routes[0].Owner = "work" }, "identity"},
		{"owner with an empty name", func(f *RouteFile) { f.Routes[0].Owner = "named:" }, "identity"},
		{"owner naming the reserved name", func(f *RouteFile) { f.Routes[0].Owner = "named:default" }, "identity"},
		{"owner name with a space", func(f *RouteFile) { f.Routes[0].Owner = "named:a b" }, "identity"},
		{"owner name too long", func(f *RouteFile) { f.Routes[0].Owner = "named:a" + strings.Repeat("b", 63) }, "identity"},
		{"to is not a profile id", func(f *RouteFile) { f.Routes[0].State, f.Routes[0].To = RouteReleasing, "x" }, "identity"},
		{"from is not a profile id", func(f *RouteFile) { f.Routes[0].From = "named:../x" }, "identity"},
		{"unknown state", func(f *RouteFile) { f.Routes[0].State = "moving" }, "state"},
		{"releasing without an owner", func(f *RouteFile) { f.Routes[0].State, f.Routes[0].Owner = RouteReleasing, "" }, "state"},
		{"unowned with an owner", func(f *RouteFile) { f.Routes[0].State = RouteUnowned }, "state"},
		{"to on an active route", func(f *RouteFile) { f.Routes[0].To = "named:work" }, "state"},
		{"to on an unowned route", func(f *RouteFile) {
			f.Routes[0].State, f.Routes[0].Owner, f.Routes[0].To = RouteUnowned, "", "named:work"
		}, "state"},
		{"handoff not hex", func(f *RouteFile) { f.Routes[0].Handoff = "abc" }, "handoff"},
		{"handoff on a releasing route", func(f *RouteFile) { f.Routes[0].State, f.Routes[0].Handoff = RouteReleasing, testHandoff("x") }, "handoff"},
		{"handoff on an unowned route", func(f *RouteFile) {
			f.Routes[0].State, f.Routes[0].Owner, f.Routes[0].Handoff = RouteUnowned, "", testHandoff("x")
		}, "handoff"},
		{"since a day ahead is allowed for clock skew", func(f *RouteFile) { f.Routes[0].Since = testNow.Add(23 * time.Hour) }, ""},
		{"since more than a day ahead", func(f *RouteFile) { f.Routes[0].Since = testNow.Add(25 * time.Hour) }, "since"},
		{"too many routes", func(f *RouteFile) { f.Routes = make([]Route, maxRoutes+1) }, "size"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := base()
			tc.mutate(&f)
			err := ValidateRoutes(f, testNow)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted a file that should fail with %q", tc.want)
			}
			if got := invalidReason(t, err); got != tc.want {
				t.Fatalf("reason = %q, want %q (%v)", got, tc.want, err)
			}
		})
	}
}

func TestValidateRoutesConflicts(t *testing.T) {
	dir := canonicalSource(t.TempDir())
	for _, d := range []string{"h", "h/inner", "other"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name   string
		a, b   func(*Route)
		kinds  [2]string
		folder [2]string
		want   string
	}{
		{"same folder and destination", func(*Route) {}, func(*Route) {}, [2]string{"codex", "codex"}, [2]string{"h", "h"}, "conflict"},
		{"nested roots", func(*Route) {}, func(*Route) {}, [2]string{"codex", "codex"}, [2]string{"h", "h/sessions/2026"}, "conflict"},
		{"nested folders with disjoint roots are fine", func(*Route) {}, func(*Route) {}, [2]string{"claude", "claude"}, [2]string{"h", "h/inner"}, ""},
		{"unrelated folders", func(*Route) {}, func(*Route) {}, [2]string{"codex", "codex"}, [2]string{"h", "other"}, ""},
		{"other kind", func(*Route) {}, func(*Route) {}, [2]string{"codex", "gjc"}, [2]string{"h", "h"}, ""},
		{"other destination is a fan-out", func(*Route) {}, func(r *Route) { r.Destination = testDest("two") }, [2]string{"codex", "codex"}, [2]string{"h", "h"}, ""},
		{"alias meets a destination", func(r *Route) { r.Aliases = []string{testDest("two")} }, func(r *Route) { r.Destination = testDest("two") }, [2]string{"codex", "codex"}, [2]string{"h", "h"}, "conflict"},
		{"alias meets an alias", func(r *Route) { r.Aliases = []string{testDest("old")} }, func(r *Route) { r.Destination = testDest("two"); r.Aliases = []string{testDest("old")} }, [2]string{"codex", "codex"}, [2]string{"h", "h"}, "conflict"},
		{"aliases that differ", func(r *Route) { r.Aliases = []string{testDest("old")} }, func(r *Route) { r.Destination = testDest("two"); r.Aliases = []string{testDest("older")} }, [2]string{"codex", "codex"}, [2]string{"h", "h"}, ""},
		{"a releasing route stands until its owner stops", func(*Route) {}, func(r *Route) { r.State = RouteReleasing }, [2]string{"codex", "codex"}, [2]string{"h", "h"}, ""},
		{"an unowned route does not", func(*Route) {}, func(r *Route) { r.State, r.Owner = RouteUnowned, "" }, [2]string{"codex", "codex"}, [2]string{"h", "h"}, "conflict"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := route(dir, "00000001", tc.kinds[0], tc.folder[0]), route(dir, "00000002", tc.kinds[1], tc.folder[1])
			tc.a(&a)
			tc.b(&b)
			err := ValidateRoutes(routeFile(a, b), testNow)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || invalidReason(t, err) != tc.want {
				t.Fatalf("err = %v, want reason %q", err, tc.want)
			}
		})
	}
}

func TestValidateRoutesSeesCaseAliasesAsConflict(t *testing.T) {
	dir := canonicalSource(t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, "Home", "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	original, _ := os.Stat(filepath.Join(dir, "Home"))
	if aliased, err := os.Stat(filepath.Join(dir, "home")); err != nil || !os.SameFile(original, aliased) {
		t.Skip("fixture filesystem has case-sensitive directory names")
	}
	err := ValidateRoutes(routeFile(route(dir, "00000001", "codex", "Home"), route(dir, "00000002", "codex", "home")), testNow)
	if err == nil || invalidReason(t, err) != "conflict" {
		t.Fatalf("one physical folder under two spellings must conflict, got %v", err)
	}
}

// R10: the conflict check must not compare every pair of a large file.
func TestValidateRoutesGroupsByDestination(t *testing.T) {
	dir := canonicalSource(t.TempDir())
	var routes []Route
	for i := range 2000 {
		r := route(dir, fmt.Sprintf("%08x", i), "codex", fmt.Sprintf("f%d", i))
		r.Destination = testDest(fmt.Sprint("d", i))
		routes = append(routes, r)
	}
	start := time.Now()
	if err := ValidateRoutes(routeFile(routes...), testNow); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("validating 2000 routes with distinct destinations took %s", took)
	}
}

func marshalFile(t *testing.T, f RouteFile) string {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func realRoute(dir, id, folder string) Route {
	r := route(dir, id, "codex", folder)
	r.Since = time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	return r
}

func TestRoutesFileRoundTrip(t *testing.T) {
	root, dir := t.TempDir(), canonicalSource(t.TempDir())
	if _, err := ReadRoutes(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing file reads as not-exist, got %v", err)
	}
	first := RouteFile{Version: 1, Routes: []Route{realRoute(dir, "3fa91c2d", "a")}}
	written, err := WriteRoutes(root, first)
	if err != nil {
		t.Fatal(err)
	}
	if written.Rev != 1 || first.Rev != 0 {
		t.Fatalf("file rev must count writes from 0: written %d, input %d", written.Rev, first.Rev)
	}
	requirePrivate(t, filepath.Join(root, "daemon-routes.json"), 0600)
	got, err := ReadRoutes(root)
	if err != nil {
		t.Fatal(err)
	}
	if marshalFile(t, got) != marshalFile(t, written) {
		t.Fatalf("read %s, wrote %s", marshalFile(t, got), marshalFile(t, written))
	}
	second := got
	second.Routes = append(second.Routes, realRoute(dir, "9b01d7e4", "b"))
	if written, err = WriteRoutes(root, second); err != nil || written.Rev != 2 {
		t.Fatalf("second write: rev %d, err %v", written.Rev, err)
	}
	if got, err = ReadRoutes(root); err != nil || len(got.Routes) != 2 || got.Rev != 2 {
		t.Fatalf("read back %+v, %v", got, err)
	}
}

func TestWriteRoutesRefusesWhatReadWouldRefuse(t *testing.T) {
	root, dir := t.TempDir(), canonicalSource(t.TempDir())
	bad := RouteFile{Version: 1, Routes: []Route{realRoute(dir, "3fa91c2d", "a"), realRoute(dir, "3fa91c2d", "b")}}
	if _, err := WriteRoutes(root, bad); err == nil || invalidReason(t, err) != "duplicate_id" {
		t.Fatalf("an invalid file must not be written, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "daemon-routes.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused write left a file behind")
	}
	// ReadJSON refuses more than 1 MiB. A file the daemon cannot read back would
	// put it in unknown-owner mode, so the writer refuses it first.
	var routes []Route
	for i := range 2400 {
		r := realRoute(dir, fmt.Sprintf("%08x", i), fmt.Sprintf("f%d", i))
		r.Destination = testDest(fmt.Sprint("d", i))
		for _, a := range []string{"a", "b", "c", "d"} {
			r.Aliases = append(r.Aliases, testDest(fmt.Sprint(a, i)))
		}
		routes = append(routes, r)
	}
	if _, err := WriteRoutes(root, RouteFile{Version: 1, Routes: routes}); err == nil || invalidReason(t, err) != "size" {
		t.Fatalf("a file over the read limit must be refused as size, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "daemon-routes.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused write left a file behind")
	}
}

func TestReadRoutesReportsWhyAFileIsUnusable(t *testing.T) {
	dir := canonicalSource(t.TempDir())
	valid := marshalFile(t, RouteFile{Version: 1, Routes: []Route{realRoute(dir, "3fa91c2d", "a")}})
	tests := []struct {
		name  string
		write func(t *testing.T, path string)
		want  string
	}{
		{"not json", func(_ *testing.T, p string) { os.WriteFile(p, []byte("{not json"), 0600) }, "format"},
		{"wrong field type", func(_ *testing.T, p string) { os.WriteFile(p, []byte(`{"version":"1"}`), 0600) }, "format"},
		{"json null", func(_ *testing.T, p string) { os.WriteFile(p, []byte("null"), 0600) }, "version"},
		{"semantically invalid", func(_ *testing.T, p string) {
			os.WriteFile(p, []byte(strings.Replace(valid, "codex", "cursor", 1)), 0600)
		}, "kind"},
		{"group writable", func(t *testing.T, p string) { os.WriteFile(p, []byte(valid), 0600); letGroupWrite(t, p, 0660) }, "unreadable"},
		{"a symlink", func(_ *testing.T, p string) {
			os.WriteFile(p+".real", []byte(valid), 0600)
			os.Symlink(p+".real", p)
		}, "unreadable"},
		{"over the read limit", func(_ *testing.T, p string) { os.WriteFile(p, []byte(strings.Repeat(" ", maxInput+1)), 0600) }, "unreadable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.write(t, filepath.Join(root, "daemon-routes.json"))
			_, err := ReadRoutes(root)
			if err == nil {
				t.Fatal("an unusable file was accepted")
			}
			if errors.Is(err, os.ErrNotExist) {
				t.Fatalf("an unusable file must not read as missing: %v", err)
			}
			if got := invalidReason(t, err); got != tc.want {
				t.Fatalf("reason = %q, want %q (%v)", got, tc.want, err)
			}
			if strings.Contains(err.Error(), root) {
				t.Fatalf("the error names a path: %v", err)
			}
		})
	}
}

// writeSeam replaces the atomic writer for one test.
func writeSeam(t *testing.T, w func(path string, data []byte, perm os.FileMode) error) {
	t.Helper()
	old := atomicWriteRoutes
	atomicWriteRoutes = w
	t.Cleanup(func() { atomicWriteRoutes = old })
}

func TestWriteRoutesUncertainty(t *testing.T) {
	dir := canonicalSource(t.TempDir())
	prior := RouteFile{Version: 1, Routes: []Route{realRoute(dir, "3fa91c2d", "a")}}
	next := RouteFile{Version: 1, Routes: append(append([]Route{}, prior.Routes...), realRoute(dir, "9b01d7e4", "b"))}
	fsyncFailure := errors.New("sync directory failed")

	tests := []struct {
		name      string
		setup     func(root string) RouteFile // returns the file the caller holds
		write     func(path string, data []byte, perm os.FileMode) error
		wantApply bool // the new file is on disk and the caller may use it
		uncertain bool
	}{
		{
			name:      "replace succeeded, directory fsync failed",
			write:     func(p string, d []byte, m os.FileMode) error { atomicfile.Write(p, d, m); return fsyncFailure },
			wantApply: true,
		},
		{
			name:  "nothing was written",
			write: func(string, []byte, os.FileMode) error { return fsyncFailure },
		},
		{
			name: "someone else's newer file is on disk",
			write: func(p string, d []byte, m os.FileMode) error {
				other := prior
				other.Rev = 9
				b, _ := json.Marshal(other)
				atomicfile.Write(p, b, m)
				return fsyncFailure
			},
			uncertain: true,
		},
		{
			name:      "the file cannot be read back",
			write:     func(p string, d []byte, m os.FileMode) error { os.WriteFile(p, []byte("{"), 0600); return fsyncFailure },
			uncertain: true,
		},
		{
			name:      "the file vanished",
			write:     func(p string, d []byte, m os.FileMode) error { os.Remove(p); return fsyncFailure },
			uncertain: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			held, err := WriteRoutes(root, prior)
			if err != nil {
				t.Fatal(err)
			}
			held.Routes = next.Routes
			writeSeam(t, tc.write)
			got, err := WriteRoutes(root, held)
			switch {
			case tc.uncertain:
				if !errors.Is(err, ErrRoutesWriteUncertain) {
					t.Fatalf("err = %v, want ErrRoutesWriteUncertain", err)
				}
			case tc.wantApply:
				if err != nil || got.Rev != held.Rev+1 || len(got.Routes) != 2 {
					t.Fatalf("an applied write must succeed: %+v, %v", got, err)
				}
			default:
				if err == nil || errors.Is(err, ErrRoutesWriteUncertain) {
					t.Fatalf("a write that did not happen is a plain failure, got %v", err)
				}
				if !errors.Is(err, fsyncFailure) {
					t.Fatalf("the cause is lost: %v", err)
				}
				if got.Rev != held.Rev {
					t.Fatalf("the caller's file must come back unchanged, rev %d", got.Rev)
				}
			}
		})
	}
}

func TestWriteRoutesFirstWriteThatDidNotHappen(t *testing.T) {
	root, dir := t.TempDir(), canonicalSource(t.TempDir())
	failure := errors.New("disk full")
	writeSeam(t, func(string, []byte, os.FileMode) error { return failure })
	_, err := WriteRoutes(root, RouteFile{Version: 1, Routes: []Route{realRoute(dir, "3fa91c2d", "a")}})
	if !errors.Is(err, failure) || errors.Is(err, ErrRoutesWriteUncertain) {
		t.Fatalf("a missing file after a failed first write means it did not happen, got %v", err)
	}
}
