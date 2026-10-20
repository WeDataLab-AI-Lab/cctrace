package localdaemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRouteCaseAliases(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "SharedSource")
	child := filepath.Join(source, "projects")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "sharedsource")
	original, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	aliased, err := os.Stat(alias)
	if err != nil || !os.SameFile(original, aliased) {
		t.Skip("fixture filesystem has case-sensitive directory names")
	}
	a := Spec{Destination: "same", Sources: []string{"claude:" + source}}
	for _, path := range []string{alias, filepath.Join(alias, "projects")} {
		b := Spec{Destination: "same", Sources: []string{"claude:" + path}}
		if !routesOverlap(a, b) || !routesOverlap(b, a) {
			t.Errorf("same physical source route accepted twice: %s and %s", source, path)
		}
		b.Destination = "different"
		if routesOverlap(a, b) {
			t.Error("intentional destination fanout was blocked")
		}
	}
}
