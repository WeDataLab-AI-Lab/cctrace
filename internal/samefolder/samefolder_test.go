package samefolder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFolderIdentityIsNotSpelling(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "real")
	inner := filepath.Join(real, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	if !Same(real, alias) || !Same(real, real) || Same(real, inner) || Same(real, filepath.Join(root, "absent")) {
		t.Fatal("Same must follow the folder, not its name")
	}
	if !Same(filepath.Join(root, "absent"), filepath.Join(root, "absent")) {
		t.Fatal("one spelling of a folder that cannot be examined is the same as itself")
	}
	for _, tc := range []struct {
		path, want string
		ok         bool
	}{
		{alias, ".", true},
		{filepath.Join(alias, "inner", "f.jsonl"), filepath.Join("inner", "f.jsonl"), true},
		{filepath.Join(alias, "gone", "deeper", "f.jsonl"), filepath.Join("gone", "deeper", "f.jsonl"), true},
		{filepath.Join(root, "elsewhere", "f.jsonl"), "", false},
	} {
		got, ok := Relative(real, tc.path)
		if ok != tc.ok || got != tc.want {
			t.Errorf("Relative(real, %s) = %q %v, want %q %v", tc.path, got, ok, tc.want, tc.ok)
		}
	}
	if _, ok := Relative(filepath.Join(root, "absent"), real); ok {
		t.Fatal("a folder that cannot be examined contains nothing")
	}
}

func TestSpellingIsTheOneTheDiskLists(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "Mixed", "inner")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(real, "absent", "deeper")
	if got := Spelling(real); got != real {
		t.Fatalf("Spelling(%s) = %s", real, got)
	}
	if got := Spelling(absent); got != absent {
		t.Fatalf("a path below what exists is kept: %s", got)
	}
	other := filepath.Join(root, "MIXED", "INNER")
	if !Same(real, other) {
		t.Skip("the disk tells case apart")
	}
	if got := Spelling(other); got != real {
		t.Fatalf("Spelling(%s) = %s, want %s", other, got, real)
	}
	if got := Spelling(filepath.Join(other, "absent", "deeper")); got != absent {
		t.Fatalf("what exists is spelled as listed, the rest as given: %s", got)
	}
}
