package omosyncer

import (
	"context"
	"fmt"
	"os"
	"testing"

	"cctrace/internal/syncer"
)

func TestOmoSyncer_AdoptedCursorSendsOnlyWhatWasWrittenSince(t *testing.T) {
	run := func(adopt bool) int {
		srv, _ := newTestServer(t)
		client := syncer.NewClient(srv.URL, "", "")
		dir := t.TempDir()
		path := writeOmoFixture(t, dir, "p", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-0000000000f2.jsonl", []string{
			sessionHeader("/work/p", "adopted"), userMessage("m1", "", "2026-01-01T00:00:01.000Z", "first"),
		})
		prev := newTestState(t)
		if err := prev.Save(); err != nil {
			t.Fatal(err)
		}
		holder := New([]string{dir}, "user@example.com", "uid-001", prev, client, nil)
		if _, err := holder.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(f, userMessage("m2", "m1", "2026-01-01T00:00:02.000Z", "second"))
		f.Close()
		next := holder
		if adopt {
			state := newTestState(t)
			state.AdoptSource(prev, syncer.Adoption{Path: func(p string) (string, bool) { return p, true }})
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}
			next = New([]string{dir}, "user@example.com", "uid-001", state, client, nil)
		}
		n, err := next.SyncOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	control, adopted := run(false), run(true)
	if control != 1 || adopted != control {
		t.Fatalf("holder sent %d, adopter sent %d; both should send only the line written since", control, adopted)
	}
}
