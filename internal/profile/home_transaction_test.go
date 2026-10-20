package profile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHomeTransactionPreflightAndRecovery(t *testing.T) {
	t.Parallel()
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	p := NewDefault()
	p.ID = "default"
	path, _ := r.Path("default")
	if err := SaveTo(p, path); err != nil {
		t.Fatal(err)
	}
	entry, _ := r.Load("default")
	home, _ := NormalizeCodexHome(t.TempDir())
	changes := []HomeUpdate{{Name: "default", Revision: entry.Revision, Homes: []HomeMembership{{Provider: "claude", Home: home, Transmission: "exclude"}}}, {Name: "missing", Revision: "stale", Homes: []HomeMembership{}}}
	if _, err := r.ApplyHomeUpdates(changes, false); err == nil {
		t.Fatal("partial preflight accepted")
	}
	after, _ := r.Load("default")
	if after.Revision != entry.Revision {
		t.Fatal("preflight failure wrote first file")
	}
	changes = changes[:1]
	if _, err := r.ApplyHomeUpdates(changes, true); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(r.Root, ".homes-migration-complete.json")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyHomeUpdates(changes, true); err == nil {
		t.Fatal("stale migration replay accepted")
	}
}

func TestHomeTransactionRetirementAndInterruptedRecovery(t *testing.T) {
	t.Parallel()
	r := Registry{Root: filepath.Join(t.TempDir(), "registry")}
	old := NewDefault()
	old.ID = "named:legacy"
	old.Options.SyncEnabled = false
	path, _ := r.Path("legacy")
	if err := SaveTo(old, path); err != nil {
		t.Fatal(err)
	}
	cursor := filepath.Join(filepath.Dir(path), "sync-state.json")
	if err := os.WriteFile(cursor, []byte("synthetic-cursor-preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	entry, _ := r.Load("legacy")
	updates := []HomeUpdate{{Name: "legacy", Revision: entry.Revision, Homes: []HomeMembership{}, Retire: true}, {Name: "default", Homes: []HomeMembership{}}, {Name: "personal", Homes: []HomeMembership{}}}
	_, err := r.ApplyHomeUpdatesWithFinalizer(updates, true, func(string) error { return errors.New("synthetic interrupted finalize") })
	if err == nil {
		t.Fatal("interruption reported success")
	}
	if _, err = r.List(); err == nil {
		t.Fatal("interrupted transaction exposed runnable registry")
	}
	if err = r.RecoverHomeTransaction(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("original definition changed during recovery")
	}
	got, _ := os.ReadFile(cursor)
	if string(got) != "synthetic-cursor-preserved" {
		t.Fatal("retired cursor not recovered")
	}
	if _, err = r.ApplyHomeUpdates(updates, true); err != nil {
		t.Fatal(err)
	}
	entries, err := r.List()
	if err != nil || len(entries) != 2 || entries[0].Name != "default" || entries[1].Name != "personal" {
		t.Fatalf("final registry=%+v %v", entries, err)
	}
	retired, err := r.LoadRetired("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if data, e := os.ReadFile(filepath.Join(retired.RuntimeDir, "sync-state.json")); e != nil || string(data) != "synthetic-cursor-preserved" {
		t.Fatal("archived cursor unavailable for source handoff")
	}
	next := []HomeUpdate{}
	for _, entry := range entries {
		next = append(next, HomeUpdate{Name: entry.Name, Revision: entry.Revision, Homes: entry.Profile.Homes})
	}
	if _, err = r.ApplyHomeUpdates(next, true); err != nil {
		t.Fatal(err)
	}
	if _, err = r.LoadRetired("legacy"); err != nil {
		t.Fatal("later onboarding lost the previous retired-source cursor index", err)
	}

}
