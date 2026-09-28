package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/syncer"
)

// Claude Code hands a SessionStart hook a small JSON object on stdin, and its
// transcript_path is the session that is running right now. That is the one file
// the first-run skip must not swallow: the skip exists to avoid backfilling
// history from before the install, and the session the user is sitting in is not
// history. Everything else on disk still gets skipped.
func TestParseHookTranscriptPath(t *testing.T) {
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
		want  string
	}{
		{
			name: "SessionStart payload",
			input: payload(map[string]any{
				"session_id": "abc", "transcript_path": abs,
				"cwd": "/work", "hook_event_name": "SessionStart", "source": "startup",
			}),
			want: abs,
		},
		// Everything below must yield "" rather than an error. This runs on the
		// hook path where nothing may fail loudly: a daemon that refuses to start
		// because stdin looked odd is a worse outcome than one that starts without
		// the exemption.
		{name: "empty stdin", input: "", want: ""},
		{name: "not json", input: "hello\n", want: ""},
		{name: "json without the field", input: `{"session_id":"abc"}`, want: ""},
		{name: "field is not a string", input: `{"transcript_path":42}`, want: ""},
		{name: "field is empty", input: `{"transcript_path":""}`, want: ""},
		{
			name:  "relative path is refused",
			input: payload(map[string]any{"transcript_path": filepath.Join("projects", "a.jsonl")}),
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseHookTranscriptPath(strings.NewReader(tc.input)); got != tc.want {
				t.Errorf("parseHookTranscriptPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseHookTranscriptPathIgnoresOversizedInput: stdin on the hook path is
// whatever the caller attached. Reading it unbounded would let a stray pipe hang
// or balloon the process that is supposed to be starting a daemon.
func TestParseHookTranscriptPathIgnoresOversizedInput(t *testing.T) {
	huge := `{"transcript_path":"/a/` + strings.Repeat("x", 1<<20) + `.jsonl"}`
	if got := parseHookTranscriptPath(strings.NewReader(huge)); got != "" {
		t.Errorf("oversized payload produced %q, want empty", got)
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
