package syncer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadStateStrictTellsMissingFromCorruptFromValid(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	tests := []struct {
		name    string
		path    string
		want    StateOrigin
		isNew   bool
		wantLen int
	}{
		{"no file", filepath.Join(dir, "absent.json"), StateMissing, true, 0},
		{"damaged", write("damaged.json", `{"files":{`), StateCorrupt, true, 0},
		{"empty file", write("empty.json", ``), StateCorrupt, true, 0},
		{"valid and empty", write("valid-empty.json", `{}`), StateValid, false, 0},
		{"valid with an empty file map", write("valid-map.json", `{"files":{}}`), StateValid, false, 0},
		{"valid with a file", write("valid.json", `{"files":{"x":{"offset":3}}}`), StateValid, false, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, origin, err := LoadStateStrict(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if origin != tc.want || s.IsNew() != tc.isNew || len(s.Files) != tc.wantLen || s.Files == nil {
				t.Fatalf("origin %v new %v files %d (map %v)", origin, s.IsNew(), len(s.Files), s.Files)
			}
			// LoadState keeps treating the first two alike, as it always has.
			loose, err := LoadState(tc.path)
			if err != nil || loose.IsNew() != tc.isNew || len(loose.Files) != tc.wantLen {
				t.Fatalf("LoadState changed: new %v files %d err %v", loose.IsNew(), len(loose.Files), err)
			}
		})
	}
}

func TestLoadStateStrictFailsOnAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be is neither missing nor damaged JSON.
	if _, _, err := LoadStateStrict(dir); err == nil {
		t.Fatal("a path that cannot be read must be an error, not a state")
	}
}

func TestSourceMarksRoundTripAndAreForgottenPerFolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	s.MarkSource(a, SourceMark{Since: t0, Via: SourceAdopted, From: "default", Handoff: "h1"})
	s.MarkSource(b, SourceMark{Since: t0, Via: SourceInitial, AfterBlock: true})
	s.SetOffset("kept", 7)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := back.SourceMark(a); !ok || !reflect.DeepEqual(got, SourceMark{Since: t0, Via: SourceAdopted, From: "default", Handoff: "h1"}) {
		t.Fatalf("mark a = %+v %v", got, ok)
	}
	if got, ok := back.SourceMark(b); !ok || !got.AfterBlock || got.Via != SourceInitial {
		t.Fatalf("mark b = %+v %v", got, ok)
	}
	if _, ok := back.SourceMark(filepath.Join(a, "other")); ok {
		t.Fatal("a mark is for exactly one folder")
	}
	back.ForgetSourcesExcept([]string{a})
	if _, ok := back.SourceMark(b); ok {
		t.Fatal("the mark of a source this state no longer reads stays")
	}
	if _, ok := back.SourceMark(a); !ok {
		t.Fatal("the mark of a source it still reads was dropped")
	}
	if back.GetOffset("kept") != 7 {
		t.Fatal("forgetting a source's mark must leave the cursors, which a later hand-over back needs")
	}
}

func TestStateWithoutMarksWritesNoSourcesKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := LoadState(path)
	s.SetOffset("x", 1)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) == "" || containsKey(raw, "sources") {
		t.Fatalf("a state that has no marks must stay as old builds wrote it: %s", raw)
	}
}

func containsKey(raw []byte, key string) bool { return strings.Contains(string(raw), `"`+key+`"`) }
