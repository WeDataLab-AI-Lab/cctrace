package gjcsyncer

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"cctrace/internal/syncer"
)

// serverStoredKeys models what internal/store actually does with a batch:
// INSERT ... ON CONFLICT (session_id, ts, record_type, profile_email, uuid)
// DO NOTHING. The first row to claim a key is stored; every later row with the
// same key is discarded without an error, so the only way a client can see the
// loss is by counting what it sent against what the key space can hold.
func serverStoredKeys(payloads []map[string]interface{}) (stored []string, sent int) {
	seen := map[string]bool{}
	for _, payload := range payloads {
		records, _ := payload["records"].([]interface{})
		for _, raw := range records {
			rec, _ := raw.(map[string]interface{})
			sent++
			key := fmt.Sprintf("%v|%v|%v|%v|%v",
				rec["session_id"], rec["ts"], rec["record_type"], rec["profile_email"], rec["uuid"])
			if seen[key] {
				continue
			}
			seen[key] = true
			stored = append(stored, key)
		}
	}
	return stored, sent
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	f.Close()
}

// gjc sends every record with an empty uuid, so the server's dedup key is
// effectively four columns and records that share a timestamp collide.
// The conflict nudge exists to break that tie by nudging whole
// microseconds -- but it only ever saw one scan's worth of records, so a run of
// same-timestamp records split across two scans had its second half start
// counting from zero again and land exactly on the rows the first half already
// wrote. The server dropped them silently.
func TestGjcSyncer_SameTimestampRecordsSplitAcrossScansSurvive(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}
	client := syncer.NewClient(srv.URL, "", "")

	const ts = "2026-08-14T10:00:02.000Z"
	gjcDir := t.TempDir()
	sessionUUID := "019f0000-0000-7000-8000-0000000000a1"
	path := writeGjcFixture(t, gjcDir, "abcde", sessionUUID, []string{
		headerLine(sessionUUID, "/Users/alice/project"),
		assistantLine("m1", "", ts, "anthropic", "claude-x", "one"),
		assistantLine("m2", "m1", ts, "anthropic", "claude-x", "two"),
	})

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}

	appendLines(t, path,
		assistantLine("m3", "m2", ts, "anthropic", "claude-x", "three"),
		assistantLine("m4", "m3", ts, "anthropic", "claude-x", "four"))
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}

	stored, sent := serverStoredKeys(*captured)
	if sent != 4 {
		t.Fatalf("client sent %d records, want 4", sent)
	}
	if len(stored) != 4 {
		t.Fatalf("only %d of %d records survived the server dedup key; %d dropped silently\nkeys: %v",
			len(stored), sent, sent-len(stored), stored)
	}
}

// The nudge has to be reproducible, not merely unique: a full rescan (a new
// file entry, a retry, a restart with the offset behind) re-sends records the
// server already holds, and idempotency there is what makes
// ON CONFLICT DO NOTHING a recovery mechanism rather than a duplicate factory.
// So the keys an incremental split produces must be exactly the keys one
// from-zero scan of the same file produces.
func TestGjcSyncer_FullRescanReproducesTheIncrementalConflictKeys(t *testing.T) {
	srv, captured := newTestServer(t)
	state := newTestState(t)
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}
	client := syncer.NewClient(srv.URL, "", "")

	const ts = "2026-08-14T10:00:02.000Z"
	gjcDir := t.TempDir()
	sessionUUID := "019f0000-0000-7000-8000-0000000000a2"
	path := writeGjcFixture(t, gjcDir, "abcde", sessionUUID, []string{
		headerLine(sessionUUID, "/Users/alice/project"),
		assistantLine("m1", "", ts, "anthropic", "claude-x", "one"),
		assistantLine("m2", "m1", ts, "anthropic", "claude-x", "two"),
	})

	gs := New([]string{gjcDir}, "user@example.com", "uid-001", state, client, nil)
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}
	appendLines(t, path,
		assistantLine("m3", "m2", ts, "anthropic", "claude-x", "three"),
		assistantLine("m4", "m3", ts, "anthropic", "claude-x", "four"))
	if _, err := gs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}
	incremental, _ := serverStoredKeys(*captured)

	*captured = nil
	rescanState := newTestState(t)
	if err := rescanState.Save(); err != nil {
		t.Fatalf("save rescan state: %v", err)
	}
	rescan := New([]string{gjcDir}, "user@example.com", "uid-001", rescanState, client, nil)
	if _, err := rescan.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (rescan): %v", err)
	}
	full, _ := serverStoredKeys(*captured)

	if !reflect.DeepEqual(incremental, full) {
		t.Fatalf("a full rescan does not reproduce the incremental keys, so re-sending duplicates rows\nincremental: %v\nfull rescan: %v", incremental, full)
	}
}
