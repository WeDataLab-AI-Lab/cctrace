package omosyncer

import (
	"os"
	"path/filepath"
	"testing"
)

// omo is the agent that most needs a second scan root and was the last to get
// one. Its sessions already live under two trees, and missing one of them cost
// 56 records / 7.26M tokens / $10.13 reported as zero (#247, #250) -- the whole
// second root was invisible until someone went looking.
//
// codex and gjc both accept extra homes via options.codex_dirs / options.gjc_dirs.
// omo did not, so an operator whose sessions sat anywhere unusual had no way to
// say so (#280). This pins the multi-root behaviour that closes that gap.

const testProfileID = "someone"

func TestSyncScansEveryConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "primary")
	second := filepath.Join(root, "elsewhere")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(d, "sessions", "--proj--"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// One session file per root, so a syncer that reads only the first would
	// find exactly half and still look like it worked.
	writeTestSession(t, filepath.Join(first, "sessions", "--proj--", "a.jsonl"))
	writeTestSession(t, filepath.Join(second, "sessions", "--proj--", "b.jsonl"))

	s := New([]string{first, second}, testProfileID, "uid", newTestState(t), nil, nil)

	files, err := s.findAllFiles()
	if err != nil {
		t.Fatalf("findAllFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("found %d session file(s), want 2 (one per root): %v", len(files), files)
	}
}

// A root listed twice must not make the same session upload twice.
func TestSyncDeduplicatesRepeatedRoots(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sessions", "--proj--"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestSession(t, filepath.Join(root, "sessions", "--proj--", "a.jsonl"))

	s := New([]string{root, root}, testProfileID, "uid", newTestState(t), nil, nil)

	files, err := s.findAllFiles()
	if err != nil {
		t.Fatalf("findAllFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("found %d file(s), want 1 -- the same root was counted twice: %v", len(files), files)
	}
}

// One unreadable root must not stop the others. The configured dir may be on a
// disconnected volume; losing every other root because of it turns a partial
// gap into a total one.
func TestSyncSkipsBadRootAndKeepsGoing(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")
	if err := os.MkdirAll(filepath.Join(good, "sessions", "--proj--"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestSession(t, filepath.Join(good, "sessions", "--proj--", "a.jsonl"))

	s := New([]string{filepath.Join(root, "does-not-exist"), good}, testProfileID, "uid", newTestState(t), nil, nil)

	files, err := s.findAllFiles()
	if err != nil {
		t.Fatalf("findAllFiles returned an error instead of skipping the bad root: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("found %d file(s), want 1 from the good root: %v", len(files), files)
	}
}

func writeTestSession(t *testing.T, path string) {
	t.Helper()
	line := `{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":"2026-08-18T00:00:00Z"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}
