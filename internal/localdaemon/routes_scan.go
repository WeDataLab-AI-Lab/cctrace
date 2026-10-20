package localdaemon

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Shapes of an overlap between two source lines.
const (
	// shapeSame: every scan root is the same physical folder, so the one that
	// reads it reads exactly what the other would have.
	shapeSame = "same"
	// shapeNested: scan roots nest. The collectors' globs fix a depth, so a file
	// under one line's root can be out of reach of the other's pattern; the
	// excluded line may have files nobody reads.
	shapeNested = "nested"
)

// scanSubdirs names, per kind, the folders under a source line's folder that its
// collector reads. Overlap is judged on these, not on the line's folder: two
// lines whose folders nest can still read disjoint files.
//
// The paths belong to internal/sessionlog, codexlog, gjclog and omolog, which
// this package does not import; TestScanRootsMatchCollectors pins this copy to
// what each collector finds.
var scanSubdirs = map[string][]string{
	"claude": {"projects"},
	"codex":  {"sessions"},
	"gjc":    {filepath.Join("agent", "sessions")},
	"omo":    {"sessions", filepath.Join("agent", "sessions")},
}

// ScanSubdirs returns, relative to a source line's folder, the folders a collector
// of kind reads; nil for an unknown kind.
func ScanSubdirs(kind string) []string {
	return slices.Clone(scanSubdirs[kind])
}

// ScanRoots returns the folders a collector of kind reads for the source line
// folder, as physical paths (symlinks resolved where they exist). It returns nil
// for an unknown kind.
func ScanRoots(kind, folder string) []string {
	return newFSView().roots(kind, folder)
}

// fsView answers the questions of one planning pass about folders on disk. It
// remembers every symlink resolution and stat: a pass asks about the same few
// folders and their ancestors for every pair of lines, and the same pass must
// not see the disk change under it.
type fsView struct {
	canon map[string]string
	stats map[string]os.FileInfo
	chain map[string][]os.FileInfo
}

func newFSView() *fsView {
	return &fsView{canon: map[string]string{}, stats: map[string]os.FileInfo{}, chain: map[string][]os.FileInfo{}}
}

func (v *fsView) canonical(path string) string {
	c, ok := v.canon[path]
	if !ok {
		c = canonicalSource(path)
		v.canon[path] = c
	}
	return c
}

// roots returns the scan roots of a line of kind at folder, nil for an unknown
// kind. A sub-root that is itself a link is followed, as the collector's glob
// does.
func (v *fsView) roots(kind, folder string) []string {
	subdirs := scanSubdirs[kind]
	if len(subdirs) == 0 || folder == "" {
		return nil
	}
	base := v.canonical(folder)
	roots := make([]string, len(subdirs))
	for i, sub := range subdirs {
		roots[i] = v.canonical(filepath.Join(base, sub))
	}
	return roots
}

// stat returns nil for a path that cannot be examined.
func (v *fsView) stat(path string) os.FileInfo {
	info, ok := v.stats[path]
	if !ok {
		info, _ = os.Stat(path)
		v.stats[path] = info
	}
	return info
}

// ancestors returns what can be examined of path and each of its parents.
func (v *fsView) ancestors(path string) []os.FileInfo {
	infos, ok := v.chain[path]
	if !ok {
		for current := path; ; current = filepath.Dir(current) {
			if info := v.stat(current); info != nil {
				infos = append(infos, info)
			}
			if filepath.Dir(current) == current {
				break
			}
		}
		v.chain[path] = infos
	}
	return infos
}

// within reports whether b is a, or inside it. macOS may keep different case
// spellings of one directory after symlink resolution, so besides lexical
// containment the physical ancestors of b are compared with a.
func (v *fsView) within(a, b string) bool {
	if lexicallyWithin(a, b) {
		return true
	}
	anchor := v.stat(a)
	if anchor == nil {
		return false
	}
	for _, info := range v.ancestors(b) {
		if os.SameFile(anchor, info) {
			return true
		}
	}
	return false
}

// lexicallyWithin: b is a, or inside it, for cleaned absolute paths. It is the
// containment filepath.Rel finds without cleaning and allocating on every pair.
func lexicallyWithin(a, b string) bool {
	if !strings.HasPrefix(b, a) {
		return false
	}
	return len(b) == len(a) || strings.HasSuffix(a, string(filepath.Separator)) || b[len(a)] == filepath.Separator
}

func (v *fsView) sameFolder(a, b string) bool {
	if a == b {
		return true
	}
	x, y := v.stat(a), v.stat(b)
	return x != nil && y != nil && os.SameFile(x, y)
}

// shape says how two lines of one kind overlap, from their scan roots: "" when
// no root of one is inside a root of the other, shapeSame when every root is the
// same folder, shapeNested otherwise. Both lines must be of the same kind, so
// their roots correspond one to one.
func (v *fsView) shape(a, b []string) string {
	if !v.overlap(a, b) {
		return ""
	}
	if len(a) != len(b) {
		return shapeNested
	}
	for i := range a {
		if !v.sameFolder(a[i], b[i]) {
			return shapeNested
		}
	}
	return shapeSame
}

func (v *fsView) overlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if v.within(x, y) || v.within(y, x) {
				return true
			}
		}
	}
	return false
}
