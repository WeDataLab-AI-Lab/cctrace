package localdaemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOverlappingSourcesReturnsOnlyTheOverlappingEntriesOfTheFirstSpec(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "codex")
	nested := filepath.Join(shared, "sub")
	own := filepath.Join(root, "home-a")
	for _, dir := range []string{shared, nested, own} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	a := Spec{Destination: "dest", Sources: []string{"claude:" + own, "codex:" + shared, "gjc:" + shared}}
	b := Spec{Destination: "dest", Sources: []string{"claude:" + filepath.Join(root, "home-b"), "codex:" + nested}}

	got := OverlappingSources(a, b)
	if want := []string{"codex:" + shared}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overlap = %v, want %v (kind must match, either side may be the ancestor)", got, want)
	}
	// The relation is symmetric as a yes/no, but each side reports its own entries.
	if back := OverlappingSources(b, a); !reflect.DeepEqual(back, []string{"codex:" + nested}) {
		t.Fatalf("reverse overlap = %v", back)
	}
}

func TestOverlappingSourcesNeverCrossesDestinations(t *testing.T) {
	root := t.TempDir()
	a := Spec{Destination: "one", Sources: []string{"codex:" + root}}
	b := Spec{Destination: "two", Sources: []string{"codex:" + root}}
	if got := OverlappingSources(a, b); got != nil {
		t.Fatalf("different destinations are an intentional fan-out, got %v", got)
	}
	if got := OverlappingSources(Spec{Sources: a.Sources}, Spec{Sources: a.Sources}); got != nil {
		t.Fatalf("an empty destination never overlaps, got %v", got)
	}
	if routesOverlap(a, b) || !routesOverlap(a, Spec{Destination: "one", Sources: a.Sources}) {
		t.Fatal("routesOverlap must keep agreeing with OverlappingSources")
	}
}
