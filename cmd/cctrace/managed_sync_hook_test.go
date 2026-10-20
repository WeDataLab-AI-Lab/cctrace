package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestManagedStartPreservesHookTranscript(t *testing.T) {
	useTempSyncHome(t)
	resetSyncControlHooks(t)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	withHookStdin(t, map[string]any{"hook_event_name": "SessionStart", "transcript_path": transcript})
	var spawned []string
	spawnSyncProcessFn = func(args []string, name string) (int, error) {
		spawned = append([]string(nil), args...)
		return 123, nil
	}
	waitForSyncStartAckFn = func(string, time.Duration) (*syncStartAck, error) {
		return &syncStartAck{Status: syncStartAckStarted}, nil
	}
	if err := runManagedStart("", time.Second, "", "", "", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(spawned); i++ {
		if spawned[i] == "--collect-file" && spawned[i+1] == transcript {
			return
		}
	}
	t.Fatalf("hook transcript missing from supervisor args: %v", spawned)
}
