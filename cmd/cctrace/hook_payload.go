package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"cctrace/internal/syncer"

	"golang.org/x/term"
)

// maxHookPayload bounds how much of stdin the hook path will read. Claude Code's
// SessionStart payload is a few hundred bytes; anything approaching this is not it.
const maxHookPayload = 64 * 1024

// hookStdinDeadline stops a `cctrace sync --daemon` typed by hand from blocking on
// a pipe nobody is going to write to. The TTY check below catches an interactive
// terminal, but a redirect from a fifo or a still-open parent looks identical to a
// hook until something arrives.
const hookStdinDeadline = 200 * time.Millisecond

// parseHookTranscriptPath pulls transcript_path out of a Claude Code hook payload.
// It returns "" for anything it does not recognise, and never fails: this runs
// while starting the sync daemon, and a daemon that refuses to start because stdin
// looked odd would be a worse outcome than one that starts without the exemption.
func parseHookTranscriptPath(r io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(r, maxHookPayload))
	if err != nil || len(data) == 0 {
		return ""
	}
	var payload struct {
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	// Absolute only. A relative path would be resolved against whatever directory
	// the daemon happens to start in, which is not the one the hook meant.
	if payload.TranscriptPath == "" || !filepath.IsAbs(payload.TranscriptPath) {
		return ""
	}
	return payload.TranscriptPath
}

// hookTranscriptPath reads the current session's transcript path from stdin when
// this process was started by a Claude Code hook, and "" otherwise.
func hookTranscriptPath() string {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return ""
	}
	type result struct{ path string }
	done := make(chan result, 1)
	go func() { done <- result{parseHookTranscriptPath(os.Stdin)} }()
	select {
	case r := <-done:
		return r.path
	case <-time.After(hookStdinDeadline):
		return ""
	}
}

// seedCollectFile records a file at offset 0 so the first sync pass reads it whole
// instead of skipping to EOF.
//
// The skip is there to keep a fresh install from backfilling sessions that predate
// it, and the session the user is sitting in is not one of those. `cctrace init` is
// normally run from inside a live session, so without this exemption the very first
// session a new user has is the one that arrives truncated — and because the lost
// part carries their opening turn, it is also the one the dashboard files under
// headless.
//
// Seeding rather than special-casing the syncer: syncFile skips a file only when the
// state has never seen it, so a known file at offset 0 already means "read this from
// the start". No new branch, and nothing else about the pass changes.
func seedCollectFile(state *syncer.State, path string) {
	if path == "" || state == nil || state.HasFile(path) {
		return
	}
	state.SetOffset(path, 0)
}
