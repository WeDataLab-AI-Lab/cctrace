package codexsyncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

func codexAssistantLine(ts, text string) string {
	return fmt.Sprintf(`{"type":"response_item","timestamp":%q,"payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}}`, ts, text)
}

// newBoundaryFixture returns a codex home whose state is already past its first
// run, so a fixture written into it is collected from byte zero instead of
// being skipped to EOF.
func newBoundaryFixture(t *testing.T) (codexDir, sessDir string, state *syncer.State) {
	t.Helper()
	codexDir = t.TempDir()
	sessDir = filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state = newTestState(t)
	state.SetOffset(filepath.Join(sessDir, "placeholder"), 0)
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}
	return codexDir, sessDir, state
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

// Codex sends every record with an empty uuid, so the server's dedup key is
// effectively four columns and records that share a timestamp collide.
// The conflict nudge exists to break that tie by nudging whole
// microseconds -- but it only ever saw one scan's worth of records, so a run of
// same-timestamp records split across two scans had its second half start
// counting from zero again and land exactly on the rows the first half already
// wrote. The server dropped them silently.
func TestCodexSyncer_SameTimestampRecordsSplitAcrossScansSurvive(t *testing.T) {
	srv, captured := newTestServer(t)
	codexDir, sessDir, state := newBoundaryFixture(t)
	client := syncer.NewClient(srv.URL, "", "")

	const ts = "2026-08-13T11:00:00.000Z"
	path := writeCodexFixture(t, sessDir, "rollout-2026-08-13T11-00-00-019ffb4b-0000-0000-0000-0000000000a1.jsonl", []string{
		codexAssistantLine(ts, "one"),
		codexAssistantLine(ts, "two"),
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}

	appendLines(t, path, codexAssistantLine(ts, "three"), codexAssistantLine(ts, "four"))
	if _, err := cs.SyncOnce(context.Background()); err != nil {
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

// The daemon restarts on every upgrade and every reboot, and a session that is
// still being written straddles the restart. Keeping the count in memory would
// fix the scan boundary and leave the restart boundary broken, so the ledger
// has to come back from the state file.
func TestCodexSyncer_SameTimestampRecordsSurviveADaemonRestart(t *testing.T) {
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
		t.Fatalf("save state: %v", err)
	}

	const ts = "2026-08-13T11:00:00.000Z"
	path := writeCodexFixture(t, sessDir, "rollout-2026-08-13T11-00-00-019ffb4b-0000-0000-0000-0000000000a3.jsonl", []string{
		codexAssistantLine(ts, "one"),
		codexAssistantLine(ts, "two"),
	})
	if _, err := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil).
		SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (before restart): %v", err)
	}

	restarted, err := syncer.LoadState(statePath)
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	appendLines(t, path, codexAssistantLine(ts, "three"), codexAssistantLine(ts, "four"))
	if _, err := New([]string{codexDir}, "user@example.com", "uid-001", restarted, client, nil).
		SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (after restart): %v", err)
	}

	stored, sent := serverStoredKeys(*captured)
	if sent != 4 {
		t.Fatalf("client sent %d records, want 4", sent)
	}
	if len(stored) != 4 {
		t.Fatalf("only %d of %d records survived a restart at the scan boundary; %d dropped silently\nkeys: %v",
			len(stored), sent, sent-len(stored), stored)
	}
}

// The nudge has to be reproducible, not merely unique: a full rescan (a new
// file entry, a retry, a restart with the offset behind) re-sends records the
// server already holds, and idempotency there is what makes
// ON CONFLICT DO NOTHING a recovery mechanism rather than a duplicate factory.
// So the keys an incremental split produces must be exactly the keys one
// from-zero scan of the same file produces.
func TestCodexSyncer_FullRescanReproducesTheIncrementalConflictKeys(t *testing.T) {
	srv, captured := newTestServer(t)
	codexDir, sessDir, state := newBoundaryFixture(t)
	client := syncer.NewClient(srv.URL, "", "")

	const ts = "2026-08-13T11:00:00.000Z"
	const name = "rollout-2026-08-13T11-00-00-019ffb4b-0000-0000-0000-0000000000a2.jsonl"
	path := writeCodexFixture(t, sessDir, name, []string{
		codexAssistantLine(ts, "one"),
		codexAssistantLine(ts, "two"),
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, client, nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 1): %v", err)
	}
	appendLines(t, path, codexAssistantLine(ts, "three"), codexAssistantLine(ts, "four"))
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (pass 2): %v", err)
	}
	incremental, _ := serverStoredKeys(*captured)

	*captured = nil
	rescanState := newTestState(t)
	rescanState.SetOffset(filepath.Join(sessDir, "placeholder"), 0)
	if err := rescanState.Save(); err != nil {
		t.Fatalf("save rescan state: %v", err)
	}
	rescan := New([]string{codexDir}, "user@example.com", "uid-001", rescanState, client, nil)
	if _, err := rescan.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce (rescan): %v", err)
	}
	full, _ := serverStoredKeys(*captured)

	if !reflect.DeepEqual(incremental, full) {
		t.Fatalf("a full rescan does not reproduce the incremental keys, so re-sending duplicates rows\nincremental: %v\nfull rescan: %v", incremental, full)
	}
}
