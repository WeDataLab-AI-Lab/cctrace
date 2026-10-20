package configtxn

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/atomicfile"
)

func workspace(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func put(t *testing.T, p, s string) {
	t.Helper()
	if e := os.WriteFile(p, []byte(s), 0640); e != nil {
		t.Fatal(e)
	}
}
func content(t *testing.T, p, want string) {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil || string(b) != want {
		t.Fatalf("%s: %q %v, want %q", p, b, e, want)
	}
}
func TestApplyAndRollback(t *testing.T) {
	t.Parallel()
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			d := workspace(t)
			p := filepath.Join(d, "existing")
			n := filepath.Join(d, "new", "file")
			put(t, p, "old")
			original, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			err = Apply(filepath.Join(d, "journal"), []Change{{Path: p, Before: []byte("old"), BeforeExists: true, After: []byte("next"), Mode: 0600}, {Path: n, After: []byte("new"), Mode: 0600}}, func() error {
				content(t, p, "next")
				if fail {
					return errors.New("validation")
				}
				return nil
			})
			if fail {
				if err == nil {
					t.Fatal("expected failure")
				}
				content(t, p, "old")
				if _, e := os.Stat(n); !os.IsNotExist(e) {
					t.Fatalf("new file remains: %v", e)
				}
				i, _ := os.Stat(p)
				if i.Mode().Perm() != original.Mode().Perm() {
					t.Fatalf("mode %o", i.Mode())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				content(t, p, "next")
				content(t, n, "new")
			}
			if e := Recover(filepath.Join(d, "journal")); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestPreconditionsAndConcurrentEdit(t *testing.T) {
	t.Parallel()
	d := workspace(t)
	p := filepath.Join(d, "file")
	put(t, p, "old")
	j := filepath.Join(d, "journal")
	c := Change{Path: p, Before: []byte("wrong"), BeforeExists: true, After: []byte("next"), Mode: 0600}
	if Apply(j, []Change{c}, nil) == nil {
		t.Fatal("accepted stale snapshot")
	}
	content(t, p, "old")
	c.Before = []byte("old")
	if Apply(j, []Change{c, c}, nil) == nil {
		t.Fatal("accepted duplicate")
	}
	err := Apply(j, []Change{c}, func() error { put(t, p, "user edit"); return errors.New("restart") })
	if err == nil {
		t.Fatal("expected conflict")
	}
	content(t, p, "user edit")
	if Recover(j) == nil {
		t.Fatal("expected persistent recovery conflict")
	}
	content(t, p, "user edit")
}
func TestSymlinksRejected(t *testing.T) {
	t.Parallel()
	d := workspace(t)
	p := filepath.Join(d, "real")
	put(t, p, "old")
	link := filepath.Join(d, "link")
	if e := os.Symlink(p, link); e != nil {
		t.Fatal(e)
	}
	if Apply(filepath.Join(d, "journal"), []Change{{Path: link, BeforeExists: true, Before: []byte("old"), After: []byte("new"), Mode: 0600}}, nil) == nil {
		t.Fatal("accepted symlink")
	}
	content(t, p, "old")
}

func TestInterruptedAndFailedWrites(t *testing.T) {
	t.Parallel()
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "crash"}[crash], func(t *testing.T) {
			d := workspace(t)
			a := filepath.Join(d, "a")
			b := filepath.Join(d, "b")
			put(t, a, "old-a")
			put(t, b, "old-b")
			j := filepath.Join(d, "journal")
			changes := []Change{{Path: a, Before: []byte("old-a"), BeforeExists: true, After: []byte("new-a"), Mode: 0600}, {Path: b, Before: []byte("old-b"), BeforeExists: true, After: []byte("new-b"), Mode: 0600}}
			func() {
				defer func() {
					if r := recover(); r != nil && !crash {
						t.Fatalf("unexpected panic: %v", r)
					}
				}()
				err := apply(j, changes, nil, func(p string, data []byte, m os.FileMode) error {
					if p == b {
						if crash {
							panic("simulated power loss")
						}
						return errors.New("disk failure")
					}
					return atomicfile.Write(p, data, m)
				})
				if err == nil {
					t.Fatal("expected injected failure")
				}
			}()
			if err := Recover(j); err != nil {
				t.Fatal(err)
			}
			content(t, a, "old-a")
			content(t, b, "old-b")
			if err := Recover(j); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompletedJournalDoesNotRollback(t *testing.T) {
	t.Parallel()
	d := workspace(t)
	p := filepath.Join(d, "file")
	put(t, p, "new")
	j := filepath.Join(d, "journal")
	if e := os.Mkdir(j, 0700); e != nil {
		t.Fatal(e)
	}
	if e := save(j, journal{Complete: true, Entries: []entry{{Change: Change{Path: p, BeforeExists: true, Before: []byte("old"), After: []byte("new"), Mode: 0600}, OriginalMode: 0640}}}); e != nil {
		t.Fatal(e)
	}
	if e := Recover(j); e != nil {
		t.Fatal(e)
	}
	content(t, p, "new")
}

func TestRollbackWithSharedNewParent(t *testing.T) {
	t.Parallel()
	d := workspace(t)
	p := filepath.Join(d, "new", "file")
	j := filepath.Join(d, "new", "journal")
	if e := Apply(j, []Change{{Path: p, After: []byte("new"), Mode: 0600}}, func() error { return errors.New("probe") }); e == nil {
		t.Fatal("expected error")
	}
	if _, e := os.Stat(p); !os.IsNotExist(e) {
		t.Fatalf("file remains: %v", e)
	}
	if e := Recover(j); e != nil {
		t.Fatal(e)
	}
}
