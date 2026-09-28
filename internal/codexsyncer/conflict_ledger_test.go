package codexsyncer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/syncer"
)

// storedCopies replays the captured batches through the server's dedup key and
// returns, per original source line, how many rows that line ended up holding.
//
// Row totals are the wrong axis here and they hide exactly this defect. When a
// record is dropped its key stays free, a later record's second copy fills it,
// and the total comes out right while one record is missing and another is
// there twice -- which is why every test in this file counts per record.
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
			copies[string(line)]++
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

func assertStored(t *testing.T, copies map[string]int, texts ...string) {
	t.Helper()
	for _, text := range texts {
		found := false
		for line := range copies {
			if strings.Contains(line, text) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("record %q reached no row at all", text)
		}
	}
}

// legacyState rewrites a saved state the way an already-deployed client's looks:
// offsets and metadata present, no ledger anywhere, because no cctrace that ever
// wrote that file knew about one.
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

func rescanFrom(t *testing.T, codexDir, sessDir string, client *syncer.Client) {
	t.Helper()
	state, err := syncer.LoadState(filepath.Join(t.TempDir(), "rescan.json"))
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(filepath.Join(sessDir, "placeholder"), 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil).
		SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (rescan): %v", err)
	}
}

// Every client already in the field has a state file with offsets in it and no
// ledger, because no version that wrote one knew a ledger existed. Reading that
// absence as "the prefix placed no records" builds the first post-upgrade ledger
// out of a suffix, and every scan after that counts from a number describing
// part of the file. The rows it produces sit on keys a from-zero scan would give
// to other records, so the next re-send stores them again.
//
// The upgrade must not do that. It may leave this file's same-timestamp loss
// exactly where it was -- that loss predates the ledger and is what the file
// would suffer anyway -- but it may not convert it into a duplicate.
func TestCodexSyncer_AnUpgradedStateWithoutALedgerDoesNotDuplicate(t *testing.T) {
	srv, captured := newTestServer(t)
	client := syncer.NewClient(srv.URL, "", "")

	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	state, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(filepath.Join(sessDir, "placeholder"), 0)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	const ts = "2026-08-13T11:00:00.000Z"
	path := writeCodexFixture(t, sessDir, "rollout-2026-08-13T11-00-00-019ffb4b-0000-0000-0000-0000000000a4.jsonl", []string{
		codexAssistantLine(ts, "one"),
		codexAssistantLine(ts, "two"),
	})
	if _, err := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil).
		SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pre-upgrade): %v", err)
	}

	upgraded := legacyState(t, statePath)
	appendLines(t, path, codexAssistantLine(ts, "three"), codexAssistantLine(ts, "four"))
	cs := New([]string{codexDir}, "user@example.com", "uid-001", upgraded, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (first pass after upgrade): %v", err)
	}
	appendLines(t, path, codexAssistantLine(ts, "five"))
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (second pass after upgrade): %v", err)
	}

	// The re-send is where a wrong ledger shows itself: until something reads
	// the file again the mis-keyed rows look fine.
	rescanFrom(t, codexDir, sessDir, client)
	assertNoDuplicates(t, storedCopies(*captured))
}

// Codex rewrites rollouts in place when it migrates them to paginated history,
// reserializing every line. The stored offset survives that -- it is just a
// number -- but the record it points at does not, so a ledger carried across the
// rewrite counts a prefix that no longer exists. Its rows land on free keys and
// look correct until the file is read again from the start.
//
// Nothing about the rewrite is visible to the size check: measured on 195
// duplicated local sessions, 132 of the 171 rewritten ones were the same size or
// larger.
func TestCodexSyncer_ALedgerIsDroppedWhenTheFileWasRewrittenUnderIt(t *testing.T) {
	srv, captured := newTestServer(t)
	codexDir, sessDir, state := newBoundaryFixture(t)
	client := syncer.NewClient(srv.URL, "", "")

	const ts = "2026-08-13T11:00:00.000Z"
	const name = "rollout-2026-08-13T11-00-00-019ffb4b-0000-0000-0000-0000000000a5.jsonl"
	padded := func(text, pad string) string {
		return fmt.Sprintf(`{"type":"response_item","timestamp":%q,"pad":%q,"payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}}`, ts, pad, text)
	}

	pad := strings.Repeat("x", 40)
	path := writeCodexFixture(t, sessDir, name, []string{
		padded("one", pad), padded("two", pad), padded("three", pad), padded("four", pad),
	})
	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (before rewrite): %v", err)
	}

	// Same records, shorter lines, then more of them: the old offset now falls
	// at a different record than the ledger was counted to.
	writeCodexFixture(t, sessDir, name, []string{
		padded("one", ""), padded("two", ""), padded("three", ""), padded("four", ""),
		padded("five", ""), padded("six", ""), padded("seven", ""),
	})
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (after rewrite): %v", err)
	}
	appendLines(t, path, padded("eight", ""))
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (after append): %v", err)
	}

	rescanFrom(t, codexDir, sessDir, client)
	copies := storedCopies(*captured)
	assertNoDuplicates(t, copies)
	assertStored(t, copies, "eight")
}
