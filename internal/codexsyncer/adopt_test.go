package codexsyncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/syncer"
)

func TestCodexSyncer_AdoptedCursorSendsOnlyWhatWasWrittenSince(t *testing.T) {
	run := func(adopt bool) int {
		srv, _ := newTestServer(t)
		client := syncer.NewClient(srv.URL, "", "")
		dir := t.TempDir()
		sess := filepath.Join(dir, "sessions")
		if err := os.MkdirAll(sess, 0o755); err != nil {
			t.Fatal(err)
		}
		path := writeCodexFixture(t, sess, "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-0000000000f3.jsonl", []string{
			`{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}}`,
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
		fmt.Fprintln(f, `{"type":"response_item","timestamp":"2026-04-23T11:30:14.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}}`)
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
	if control == 0 || adopted != control {
		t.Fatalf("holder sent %d, adopter sent %d; both should send only the line written since", control, adopted)
	}
}
