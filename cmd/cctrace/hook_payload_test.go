package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/syncer"
)

// Claude Code hands a hook a small JSON object on stdin. A SessionStart's
// transcript_path is the session that is running right now -- the one file the
// first-run skip must not swallow, since the skip exists to avoid backfilling
// history from before the install. A SessionEnd's session_id and reason are what
// the pending session end records.
func TestParseHookPayload(t *testing.T) {
	// Built from t.TempDir so the "absolute" case is absolute on the platform the
	// test runs on. A hardcoded POSIX path is not absolute on Windows, and the
	// daemon reads paths produced by the same OS it runs on — pinning one syntax
	// would test the test, not the parser.
	abs := filepath.Join(t.TempDir(), "projects", "-work", "abc.jsonl")
	payload := func(v map[string]any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(b)
	}

	tests := []struct {
		name  string
		input string
		want  hookPayload
	}{
		{
			name: "SessionStart payload",
			input: payload(map[string]any{
				"session_id": "abc", "transcript_path": abs,
				"cwd": "/work", "hook_event_name": "SessionStart", "source": "startup",
			}),
			want: hookPayload{TranscriptPath: abs, SessionID: "abc", HookEventName: "SessionStart", CWD: "/work"},
		},
		{
			name: "SessionEnd payload",
			input: payload(map[string]any{
				"session_id": "abc", "transcript_path": abs, "cwd": "/work",
				"hook_event_name": "SessionEnd", "reason": "prompt_input_exit", "permission_mode": "default",
			}),
			want: hookPayload{TranscriptPath: abs, SessionID: "abc", HookEventName: "SessionEnd", Reason: "prompt_input_exit", CWD: "/work"},
		},
		// Everything below must yield the zero payload, or drop the bad part,
		// rather than an error. This runs on the hook path where nothing may fail
		// loudly: a daemon that refuses to start because stdin looked odd is a
		// worse outcome than one that starts without what the payload says.
		{name: "empty stdin", input: "", want: hookPayload{}},
		{name: "not json", input: "hello\n", want: hookPayload{}},
		{name: "json of another shape", input: `["SessionEnd","abc"]`, want: hookPayload{}},
		{name: "json without the fields", input: `{"tool_name":"Bash"}`, want: hookPayload{}},
		// A field of another type is missing alone; the rest is still read.
		{name: "field is not a string", input: `{"transcript_path":42,"session_id":"abc"}`, want: hookPayload{SessionID: "abc"}},
		{
			name: "reason and cwd of another type keep the transcript path",
			input: payload(map[string]any{
				"session_id": "abc", "transcript_path": abs, "hook_event_name": "SessionEnd",
				"reason": map[string]any{"code": 1}, "cwd": []string{"/work"},
			}),
			want: hookPayload{TranscriptPath: abs, SessionID: "abc", HookEventName: "SessionEnd"},
		},
		{name: "field is empty", input: `{"transcript_path":""}`, want: hookPayload{}},
		{
			name: "relative path is refused, the rest kept",
			input: payload(map[string]any{
				"transcript_path": filepath.Join("projects", "a.jsonl"),
				"session_id":      "abc", "hook_event_name": "SessionEnd", "reason": "clear",
			}),
			want: hookPayload{SessionID: "abc", HookEventName: "SessionEnd", Reason: "clear"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseHookPayload(strings.NewReader(tc.input)); got != tc.want {
				t.Errorf("parseHookPayload() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParseHookPayloadIgnoresOversizedInput: stdin on the hook path is whatever
// the caller attached. Reading it unbounded would let a stray pipe hang or
// balloon the process that is supposed to be starting a daemon.
func TestParseHookPayloadIgnoresOversizedInput(t *testing.T) {
	huge := `{"hook_event_name":"SessionEnd","session_id":"abc","transcript_path":"/a/` + strings.Repeat("x", 1<<20) + `.jsonl"}`
	if got := parseHookPayload(strings.NewReader(huge)); got != (hookPayload{}) {
		t.Errorf("oversized payload produced %+v, want the zero payload", got)
	}
}

// TestSeedCollectFileExemptsItFromFirstRunSkip is the point of the whole change:
// after seeding, the syncer sees a file it already knows and scans it from the
// start, while a sibling that was never seeded is still skipped to EOF.
func TestSeedCollectFileExemptsItFromFirstRunSkip(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	current := filepath.Join(dir, "current.jsonl")
	other := filepath.Join(dir, "other.jsonl")

	st := loadStateForTest(t, statePath)
	seedCollectFile(st, current)

	if !st.HasFile(current) {
		t.Error("the running session was not seeded; it will be skipped to EOF")
	}
	if got := st.GetOffset(current); got != 0 {
		t.Errorf("seeded offset = %d, want 0 — the session must be read from the start", got)
	}
	if st.HasFile(other) {
		t.Error("seeding must exempt one file, not disable the skip entirely")
	}
}

func loadStateForTest(t *testing.T, path string) *syncer.State {
	t.Helper()
	st, err := syncer.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	return st
}
