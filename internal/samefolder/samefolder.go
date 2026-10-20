// Package samefolder compares folders by what they are, not by how they are
// spelled: a link, another case of the same name on a case-insensitive disk.
package samefolder

import (
	"os"
	"path/filepath"
	"strings"
)

// Same reports whether a and b are one folder. Two spellings that cannot be
// examined are the same only if they are the same string.
func Same(a, b string) bool {
	if a == b {
		return true
	}
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// Relative says where path lies below folder: the nearest ancestor of path that
// is folder (by Same) is where the relative path starts. It is "." for folder
// itself. The path need not exist, only some ancestor of it.
func Relative(folder, path string) (string, bool) {
	anchor, err := os.Stat(folder)
	if err != nil {
		return "", false
	}
	for current := path; ; current = filepath.Dir(current) {
		if info, err := os.Stat(current); err == nil && os.SameFile(anchor, info) {
			rel, err := filepath.Rel(current, path)
			return rel, err == nil
		}
		if filepath.Dir(current) == current {
			return "", false
		}
	}
}

// Spelling returns the absolute path with each part that exists spelled as its
// parent folder lists it, so that two spellings of one folder on a disk that
// ignores case, or that keeps one form of a name and opens another, come out as
// one string. A part that cannot be examined, and all below it, is kept as given.
func Spelling(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	path = filepath.Clean(path)
	sep := string(filepath.Separator)
	vol := filepath.VolumeName(path)
	out, rest := vol+sep, strings.Trim(path[len(vol):], sep)
	if rest == "" {
		return out
	}
	parts := strings.Split(rest, sep)
	for i, name := range parts {
		listed, ok := listedName(out, name)
		if !ok {
			return filepath.Join(append([]string{out}, parts[i:]...)...)
		}
		out = filepath.Join(out, listed)
	}
	return out
}

// listedName is how folder dir lists its entry name: name itself, or the entry
// that is the same file. ok is false when name cannot be examined.
func listedName(dir, name string) (string, bool) {
	want, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return name, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return name, true
	}
	for _, e := range entries {
		if e.Name() == name {
			return name, true
		}
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if info, err := e.Info(); err == nil && os.SameFile(want, info) {
			return e.Name(), true
		}
	}
	return name, true
}
