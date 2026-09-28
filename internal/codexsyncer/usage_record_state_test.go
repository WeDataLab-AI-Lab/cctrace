package codexsyncer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"cctrace/internal/syncer"
)

func usageEventLine(total, last int, timestamp string) string {
	return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":0},"last_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":0}}}}`, timestamp, total, last)
}

func tokenUsageRecordLine(threadTotal, usage int, timestamp string) string {
	return fmt.Sprintf(`{"type":"token_usage_record","timestamp":%q,"payload":{"usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":0},"thread_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":0}}}`, timestamp, usage, threadTotal)
}

// The scanner learns that a file carries source usage records by reading one, and
// an incremental pass only ever reads the new tail. So the fact has to be kept
// between passes: without it the flag is false again on the next scan, and the
// aggregation #543 added is decided by whichever slice of the file the pass
// happened to see rather than by the file.
//
// The state field and the scanner's use of it arrived in the same commit as the
// #544 boundary machine, so deferring that machine drops this too if nobody is
// looking. Nothing in the PR pinned this behaviour.
func TestCodexSyncer_UsageRecordFlagSurvivesAnIncrementalPass(t *testing.T) {
	srv, _ := newTestServer(t)
	state := newTestState(t)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	codexDir := t.TempDir()
	sessDir := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "rollout-2026-09-07T05-00-00-cccccccc-0000-0000-0000-000000000003.jsonl"
	path := writeCodexFixture(t, sessDir, name, []string{
		`{"type":"session_meta","timestamp":"2026-09-07T05:00:00.000Z","payload":{"cwd":"/tmp/codex-usage-state","model_provider":"openai"}}`,
		tokenUsageRecordLine(100, 100, "2026-09-07T05:00:01.000Z"),
	})

	cs := New([]string{codexDir}, "user@example.com", "uid-001", state, syncer.NewClient(srv.URL, "", ""), nil)
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("initial SyncOnce: %v", err)
	}

	fs := state.Files[path]
	if fs == nil {
		t.Fatalf("no state entry for %s", path)
	}
	if !fs.CodexHasTokenUsageRecord {
		t.Fatal("the pass that read a token_usage_record did not record it, so the next pass starts blind")
	}

	// A tail with no usage record at all: the flag must not be unlearned.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(f, usageEventLine(200, 100, "2026-09-07T05:00:02.000Z")); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.SyncOnce(context.Background()); err != nil {
		t.Fatalf("incremental SyncOnce: %v", err)
	}
	if fs := state.Files[path]; fs == nil || !fs.CodexHasTokenUsageRecord {
		t.Fatal("a tail without a usage record unlearned the fact that the file has them")
	}
}
