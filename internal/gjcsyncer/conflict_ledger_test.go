package gjcsyncer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/syncer"
)

// storedCopies replays the captured batches through the server's dedup key and
// returns, per original record, how many rows that record ended up holding. A
// gjc source line can yield an assistant record and several tool_call siblings,
// so the identity is the line plus what separates those siblings from one
// another, not the line alone.
//
// Row totals are the wrong axis and they hide exactly this defect: when a record
// is dropped its key stays free, a later record's second copy fills it, and the
// total comes out right while one record is missing and another is there twice.
func storedCopies(payloads []map[string]interface{}) map[string]int {
	seen := map[string]bool{}
	copies := map[string]int{}
	for _, payload := range payloads {
		records, _ := payload["records"].([]interface{})
		for _, raw := range records {
			rec, _ := raw.(map[string]interface{})
			key := fmt.Sprintf("%v|%v|%v|%v|%v",
				rec["session_id"], rec["ts"], rec["record_type"], rec["profile_email"], rec["uuid"])
			if seen[key] {
				continue
			}
			seen[key] = true
			line, _ := json.Marshal(rec["raw"])
			copies[fmt.Sprintf("%s|%v|%v", line, rec["record_type"], rec["tool_use_id"])]++
		}
	}
	return copies
}

func assertNoDuplicates(t *testing.T, copies map[string]int) {
	t.Helper()
	for line, n := range copies {
		if n > 1 {
			t.Errorf("one source record occupies %d rows, so re-sending the file duplicates instead of deduplicating\n  %s", n, line)
		}
	}
}

// Every client already in the field has a state file with offsets in it and no
// ledger, because no version that wrote one knew a ledger existed. Reading that
// absence as "the prefix placed no records" builds the first post-upgrade ledger
// out of a suffix, and the rows it then produces sit on keys a from-zero scan
// would give to other records -- so the next re-send stores them again. The
// upgrade may leave this file's existing loss alone; it may not turn that loss
// into a duplicate.
func TestGjcSyncer_AnUpgradedStateWithoutALedgerDoesNotDuplicate(t *testing.T) {
	srv, captured := newTestServer(t)
	client := syncer.NewClient(srv.URL, "", "")

	statePath := filepath.Join(t.TempDir(), "state.json")
	state, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	const ts = "2026-08-14T10:00:02.000Z"
	gjcDir := t.TempDir()
	sessionUUID := "019f0000-0000-7000-8000-0000000000b1"
	path := writeGjcFixture(t, gjcDir, "abcde", sessionUUID, []string{
		headerLine(sessionUUID, "/Users/alice/project"),
		assistantLine("m1", "", ts, "anthropic", "claude-x", "one"),
		assistantLine("m2", "m1", ts, "anthropic", "claude-x", "two"),
	})
	if _, err := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil).
		SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pre-upgrade): %v", err)
	}

	upgraded := legacyState(t, statePath)
	appendLines(t, path,
		assistantLine("m3", "m2", ts, "anthropic", "claude-x", "three"),
		assistantLine("m4", "m3", ts, "anthropic", "claude-x", "four"))
	gs := New([]string{gjcDir}, "user@example.com", "uid-001", upgraded, client, nil)
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (first pass after upgrade): %v", err)
	}
	appendLines(t, path, assistantLine("m5", "m4", ts, "anthropic", "claude-x", "five"))
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (second pass after upgrade): %v", err)
	}

	// The re-send is where a wrong ledger shows itself: until something reads
	// the file again the mis-keyed rows look fine.
	rescan, err := syncer.LoadState(filepath.Join(t.TempDir(), "rescan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rescan.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := New([]string{gjcDir}, "user@example.com", "uid-001", rescan, client, nil).
		SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (rescan): %v", err)
	}
	assertNoDuplicates(t, storedCopies(*captured))
}

// legacyState rewrites a saved state the way an already-deployed client's looks:
// offsets and metadata present, no ledger anywhere.
func legacyState(t *testing.T, statePath string) *syncer.State {
	t.Helper()
	b, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	files, _ := doc["files"].(map[string]interface{})
	for _, v := range files {
		f, _ := v.(map[string]interface{})
		delete(f, "conflict_tail")
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
