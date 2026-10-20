package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"cctrace/internal/sessionlog"
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

// hookPayload is what cctrace reads from a Claude Code hook's stdin payload.
type hookPayload struct {
	// TranscriptPath is the running session's transcript, absolute or "".
	TranscriptPath string
	SessionID      string
	HookEventName  string
	// Reason is why the session ended (SessionEnd only).
	Reason string
	CWD    string
}

// parseHookPayload reads a Claude Code hook payload. It returns the zero payload
// for anything it does not recognise, and never fails: this runs while starting
// the sync daemon, and a daemon that refuses to start because stdin looked odd
// would be a worse outcome than one that starts without what the payload says.
//
// Each field is read on its own, so one of an unexpected type is missing alone:
// a reason that is not a string still leaves the transcript path to collect.
func parseHookPayload(r io.Reader) hookPayload {
	data, err := io.ReadAll(io.LimitReader(r, maxHookPayload))
	if err != nil || len(data) == 0 {
		return hookPayload{}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return hookPayload{}
	}
	text := func(name string) string {
		var v string
		_ = json.Unmarshal(fields[name], &v)
		return v
	}
	payload := hookPayload{
		TranscriptPath: text("transcript_path"),
		SessionID:      text("session_id"),
		HookEventName:  text("hook_event_name"),
		Reason:         text("reason"),
		CWD:            text("cwd"),
	}
	// Absolute only. A relative path would be resolved against whatever directory
	// the daemon happens to start in, which is not the one the hook meant.
	if !filepath.IsAbs(payload.TranscriptPath) {
		payload.TranscriptPath = ""
	}
	return payload
}

// readHookPayload reads the hook payload from stdin when this process was
// started by a Claude Code hook, and returns the zero payload otherwise.
func readHookPayload() hookPayload {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return hookPayload{}
	}
	done := make(chan hookPayload, 1)
	go func() { done <- parseHookPayload(os.Stdin) }()
	select {
	case payload := <-done:
		return payload
	case <-time.After(hookStdinDeadline):
		return hookPayload{}
	}
}

// recordSessionEnd leaves the SessionEnd hook's report in the profile's pending
// directory, beside its sync-state.json, where that profile's flush picks it up
// (syncer.SessionEndDir). Its home is the one the transcript sits in
// (syncer.RecordSessionEnd); without a transcript, the one the --once child
// syncs, resolved the same way, so that home's Syncer is the one that sends it.
func recordSessionEnd(profileName, claudeDir string, hook hookPayload) {
	home := claudeDir
	if home == "" {
		if p, err := loadSyncProfile(profileName); err == nil {
			home = resolveClaudeDir(p, "")
		} else {
			home = sessionlog.DefaultClaudeDir()
		}
	}
	end := syncer.SessionEnd{
		SessionID:      hook.SessionID,
		Reason:         hook.Reason,
		EndedAt:        time.Now(),
		TranscriptPath: hook.TranscriptPath,
		CWD:            hook.CWD,
		ClaudeHome:     home,
	}
	if err := syncer.RecordSessionEnd(syncer.SessionEndDir(syncStatePathForProfile(profileName)), end); err != nil {
		diagf("[session-end] %v", err)
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
