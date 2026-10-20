package gjcsyncer

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"cctrace/internal/syncer"
)

// recordKeys lists what the server would deduplicate on: the type and the
// timestamp, to the microsecond, of every record sent.
func recordKeys(captured []map[string]interface{}) []string {
	var keys []string
	for _, batch := range captured {
		records, _ := batch["records"].([]interface{})
		for _, raw := range records {
			rec := raw.(map[string]interface{})
			keys = append(keys, fmt.Sprintf("%v|%v", rec["record_type"], rec["ts"]))
		}
	}
	slices.Sort(keys)
	return keys
}

// A session whose cursor is adopted goes on exactly as it would have under the
// profile that held it: only what was written since is sent, and records that
// share a timestamp with ones already sent get the keys the earlier pass left
// room for, not the ones it already used.
func TestGjcSyncer_AdoptedCursorContinuesWithTheSameRecordKeys(t *testing.T) {
	uuid := "019f0000-0000-7000-8000-0000000000f1"
	first := []string{
		headerLine(uuid, "/work/p"),
		userLine("m1", "", "2026-08-14T10:00:01.000Z", "go"),
		assistantWithToolCallsLine("m2", "m1", "2026-08-14T10:00:02.000Z", "anthropic", "claude-x"),
	}
	more := assistantWithToolCallsLine("m3", "m2", "2026-08-14T10:00:02.000Z", "anthropic", "claude-x")

	run := func(adopt bool) []string {
		srv, captured := newTestServer(t)
		client := syncer.NewClient(srv.URL, "", "")
		dir := t.TempDir()
		path := writeGjcFixture(t, dir, "p", uuid, first)
		prev := newTestState(t)
		if err := prev.Save(); err != nil { // not a first run
			t.Fatal(err)
		}
		holder := New([]string{dir}, "user@example.com", "uid-001", prev, client, nil)
		if _, err := holder.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		appendLines(t, path, more)
		*captured = nil
		next := holder
		if adopt {
			state := newTestState(t)
			state.AdoptSource(prev, syncer.Adoption{Path: func(p string) (string, bool) { return p, true }})
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}
			next = New([]string{dir}, "user@example.com", "uid-001", state, client, nil)
		}
		if _, err := next.SyncOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		return recordKeys(*captured)
	}
	control, adopted := run(false), run(true)
	if len(control) == 0 {
		t.Fatal("setup: the control pass sent nothing")
	}
	if !slices.Equal(control, adopted) {
		t.Fatalf("an adopted cursor must continue as the holder's would have:\n holder  %v\n adopter %v", control, adopted)
	}
}
