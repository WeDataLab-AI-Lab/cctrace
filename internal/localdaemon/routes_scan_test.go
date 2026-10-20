package localdaemon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cctrace/internal/codexlog"
	"cctrace/internal/gjclog"
	"cctrace/internal/omolog"
	"cctrace/internal/sessionlog"
)

// mkdirs creates dirs under a canonical temp root and returns that root.
func mkdirs(t *testing.T, dirs ...string) string {
	t.Helper()
	root := canonicalSource(t.TempDir())
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestScanRootsPerKind(t *testing.T) {
	home := mkdirs(t, "h")
	folder := filepath.Join(home, "h")
	tests := []struct {
		kind string
		want []string
	}{
		{"claude", []string{filepath.Join(folder, "projects")}},
		{"codex", []string{filepath.Join(folder, "sessions")}},
		{"gjc", []string{filepath.Join(folder, "agent", "sessions")}},
		{"omo", []string{filepath.Join(folder, "sessions"), filepath.Join(folder, "agent", "sessions")}},
		{"unknown", nil},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			if got := ScanRoots(tc.kind, folder); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ScanRoots(%s) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestScanRootsFollowSymlinks(t *testing.T) {
	home := mkdirs(t, "real/sessions")
	if err := os.Symlink(filepath.Join(home, "real"), filepath.Join(home, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	want := []string{filepath.Join(home, "real", "sessions")}
	if got := ScanRoots("codex", filepath.Join(home, "link")); !reflect.DeepEqual(got, want) {
		t.Fatalf("a linked folder must be judged where it really is: %v, want %v", got, want)
	}
	// The sub-root itself may be the link: the collector's glob follows it.
	other := mkdirs(t, "elsewhere", "folder")
	if err := os.Symlink(filepath.Join(other, "elsewhere"), filepath.Join(other, "folder", "sessions")); err != nil {
		t.Skip("symlinks unavailable")
	}
	want = []string{filepath.Join(other, "elsewhere")}
	if got := ScanRoots("codex", filepath.Join(other, "folder")); !reflect.DeepEqual(got, want) {
		t.Fatalf("a linked sub-root must be judged where it really is: %v, want %v", got, want)
	}
}

// The roots are mirrored from the collectors, which this package does not
// import. This pins the mirror to what each collector really finds: every file
// it returns lies under a declared root, every declared root holds one, and a
// file outside the roots is invisible to it.
func TestScanRootsMatchCollectors(t *testing.T) {
	tests := []struct {
		kind    string
		visible []string // relative to the folder; the collector must find these
		hidden  []string // the collector must not find these
		find    func(string) []string
	}{
		{
			kind:    "claude",
			visible: []string{"projects/p1/a.jsonl", "projects/p1/subagents/b.jsonl", "projects/p1/s/subagents/c.jsonl"},
			hidden:  []string{"a.jsonl", "sessions/p1/a.jsonl", "other/projects/p1/a.jsonl"},
			find: func(dir string) []string {
				files, err := sessionlog.FindJSONLFiles(dir)
				if err != nil {
					t.Fatal(err)
				}
				return files
			},
		},
		{
			kind:    "codex",
			visible: []string{"sessions/rollout-x.jsonl", "sessions/2026/10/15/rollout-y.jsonl"},
			hidden:  []string{"rollout-x.jsonl", "projects/p1/rollout-x.jsonl", "sessions-extra/rollout-x.jsonl"},
			find: func(dir string) []string {
				files, err := codexlog.FindJSONLFiles(dir)
				if err != nil {
					t.Fatal(err)
				}
				return files
			},
		},
		{
			kind:    "gjc",
			visible: []string{"agent/sessions/v2-abc/a.jsonl", "agent/sessions/v2-abc/art/b.jsonl"},
			hidden:  []string{"sessions/v2-abc/a.jsonl", "agent/v2-abc/a.jsonl"},
			find: func(dir string) []string {
				main, err := gjclog.FindSessionFiles(dir)
				if err != nil {
					t.Fatal(err)
				}
				sub, err := gjclog.FindSubagentFiles(dir)
				if err != nil {
					t.Fatal(err)
				}
				return append(main, sub...)
			},
		},
		{
			kind:    "omo",
			visible: []string{"sessions/--x--/a.jsonl", "agent/sessions/--y--/b.jsonl"},
			hidden:  []string{"--x--/a.jsonl", "agent/--y--/b.jsonl"},
			find: func(dir string) []string {
				files, err := omolog.FindSessionFiles(dir)
				if err != nil {
					t.Fatal(err)
				}
				return files
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			folder := mkdirs(t)
			for _, rel := range append(append([]string{}, tc.visible...), tc.hidden...) {
				touch(t, filepath.Join(folder, filepath.FromSlash(rel)))
			}
			roots := ScanRoots(tc.kind, folder)
			found := tc.find(folder)
			if len(found) != len(tc.visible) {
				t.Fatalf("collector found %d files, fixture expects %d: %v", len(found), len(tc.visible), found)
			}
			inside := make([]int, len(roots))
			for _, file := range found {
				ok := false
				for i, root := range roots {
					if strings.HasPrefix(file, root+string(filepath.Separator)) {
						inside[i]++
						ok = true
					}
				}
				if !ok {
					t.Errorf("collector reads %s outside the declared roots %v", file, roots)
				}
			}
			for i, n := range inside {
				if n == 0 {
					t.Errorf("declared root %s holds nothing the collector reads", roots[i])
				}
			}
		})
	}
}

func TestOverlapShape(t *testing.T) {
	tests := []struct {
		name string
		kind string
		a, b string // line folders, relative to the temp root
		want string
	}{
		{"same folder", "codex", "h", "h", shapeSame},
		{"unrelated folders", "codex", "h", "g", ""},
		{"claude nested roots lose files", "claude", "h", "h/projects/q", shapeNested},
		{"claude nested folders with disjoint roots do not overlap", "claude", "h", "h/other", ""},
		{"codex folder inside the other's sessions", "codex", "h", "h/sessions/2026", shapeNested},
		{"gjc nested folders with disjoint roots do not overlap", "gjc", "h", "h/agent", ""},
		{"omo same folder reads both roots alike", "omo", "h", "h", shapeSame},
		{"omo one root shared is nested, not same", "omo", "h", "h/agent", shapeNested},
		{"omo disjoint", "omo", "h", "h/zzz", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := mkdirs(t, tc.a, tc.b)
			v := newFSView()
			a, b := v.roots(tc.kind, filepath.Join(root, tc.a)), v.roots(tc.kind, filepath.Join(root, tc.b))
			if got := v.shape(a, b); got != tc.want {
				t.Fatalf("shape(a, b) = %q, want %q (a=%v b=%v)", got, tc.want, a, b)
			}
			if got := v.shape(b, a); got != tc.want {
				t.Fatalf("shape must be symmetric: shape(b, a) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOverlapShapeSeesThroughSpellings(t *testing.T) {
	root := mkdirs(t, "Real/sessions", "Real/projects")
	if err := os.Symlink(filepath.Join(root, "Real"), filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable")
	}
	v := newFSView()
	a := v.roots("codex", filepath.Join(root, "Real"))
	b := v.roots("codex", filepath.Join(root, "link"))
	if got := v.shape(a, b); got != shapeSame {
		t.Fatalf("a symlink to the same folder is the same source, got %q", got)
	}
	alias := filepath.Join(root, "real")
	original, err := os.Stat(filepath.Join(root, "Real"))
	if err != nil {
		t.Fatal(err)
	}
	if aliased, err := os.Stat(alias); err != nil || !os.SameFile(original, aliased) {
		t.Skip("fixture filesystem has case-sensitive directory names")
	}
	if got := v.shape(a, v.roots("codex", alias)); got != shapeSame {
		t.Fatalf("a case alias of the same folder is the same source, got %q", got)
	}
	// An alias of an ancestor is still an ancestor.
	if got := v.shape(v.roots("claude", filepath.Join(root, "Real")), v.roots("claude", filepath.Join(alias, "projects", "q"))); got != shapeNested {
		t.Fatalf("a case alias of an ancestor must still nest, got %q", got)
	}
}

// lexicallyWithin replaces filepath.Rel on the hot path of every pair of lines;
// it must keep Rel's answer for the cleaned absolute paths it is given.
func TestLexicallyWithinAgreesWithRel(t *testing.T) {
	sep := string(filepath.Separator)
	paths := []string{sep, sep + "a", sep + "a" + sep + "b", sep + "ab", sep + "a" + sep + "b" + sep + "c", sep + "b"}
	for _, a := range paths {
		for _, b := range paths {
			rel, err := filepath.Rel(a, b)
			want := err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+sep)
			if got := lexicallyWithin(a, b); got != want {
				t.Errorf("lexicallyWithin(%q, %q) = %v, Rel says %v (%q)", a, b, got, want, rel)
			}
		}
	}
}

// A run that takes over a source needs to know which of a previous holder's files
// the new folder's collector would read; the answer is the scan roots, relative.
func TestScanSubdirsAreTheScanRootsBelowTheFolder(t *testing.T) {
	folder := mkdirs(t, "f")
	for _, kind := range []string{"claude", "codex", "gjc", "omo"} {
		var want []string
		for _, root := range ScanRoots(kind, folder) {
			rel, err := filepath.Rel(folder, root)
			if err != nil {
				t.Fatal(err)
			}
			want = append(want, rel)
		}
		if got := ScanSubdirs(kind); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ScanSubdirs = %v, want %v", kind, got, want)
		}
	}
	if got := ScanSubdirs("unknown"); got != nil {
		t.Fatalf("unknown kind: %v", got)
	}
	// A copy: callers cannot change what the packages judge overlap by.
	ScanSubdirs("claude")[0] = "changed"
	if ScanSubdirs("claude")[0] != "projects" {
		t.Fatal("ScanSubdirs hands out the package's own slice")
	}
}
