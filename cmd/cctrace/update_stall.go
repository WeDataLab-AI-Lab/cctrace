package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// updateStall records that this install keeps failing to move itself to a
// particular server version.
//
// Self-update failure is not transient. The shape production produced was an
// install directory owned by root, where the replacement file cannot be created
// at all; #623 describes a second one, a rename target a half-finished attempt
// had already deleted. Neither heals by being retried, and the watch child
// retried every five minutes anyway -- one install fetched the same artifact 867
// times in a week and never left the version it started on.
//
// Two things follow, and this record carries both. The retry is spaced out, so a
// dead loop costs a few requests a day instead of a few hundred. And the reason
// is kept where `cctrace status` reads it, because the only other record was one
// line per attempt in sync.log: a file that rotates, that nobody opens
// unprompted, and that held the answer for ten days while the version sat still.
type updateStall struct {
	// Version is the server version this install could not reach. A different
	// version is a different artifact and earns a fresh attempt, so Consecutive
	// only ever counts repeated failure against the same download.
	Version     string    `json:"version"`
	Consecutive int       `json:"consecutive"`
	FirstAt     time.Time `json:"first_at"`
	LastAt      time.Time `json:"last_at"`
	Reason      string    `json:"reason"`
}

// updateClock is the seam the backoff tests move time through.
var updateClock = time.Now

func updateStallFilePath(profileName string) string {
	return filepath.Join(syncProfileDir(profileName), "update-stall.json")
}

// readUpdateStall returns the recorded stall, or false when this install is not
// stalled. An unreadable or malformed file reads as "not stalled": the fallback
// is the old every-check behaviour, which is wasteful but never wrong.
func readUpdateStall(profileName string) (*updateStall, bool) {
	var st updateStall
	ok, err := readJSONFile(updateStallFilePath(profileName), &st)
	if err != nil || !ok || st.Version == "" {
		return nil, false
	}
	return &st, true
}

// noteUpdateFailure records one failed attempt against serverVersion, starting a
// new count whenever that differs from the version already recorded.
func noteUpdateFailure(profileName, serverVersion, reason string, now time.Time) {
	if serverVersion == "" {
		return
	}
	st, ok := readUpdateStall(profileName)
	if !ok || st.Version != serverVersion {
		st = &updateStall{Version: serverVersion, FirstAt: now}
	}
	st.Consecutive++
	st.LastAt = now
	st.Reason = reason
	_ = writeJSONFile(updateStallFilePath(profileName), st)
}

// clearUpdateStall forgets the record once this install is current again,
// whether because the update applied or because it is no longer behind.
func clearUpdateStall(profileName string) {
	_ = os.Remove(updateStallFilePath(profileName))
}

// updateRetryDelay is how long to wait before fetching the same artifact again
// after n consecutive failures.
//
// The ceiling is an hour rather than a day because the fix is on the user's side
// -- they move the binary, or correct the directory's ownership -- and an
// install that has just been repaired must not sit on the old version until
// tomorrow. An hour costs at most 24 attempts a day against the 288 it replaces.
func updateRetryDelay(consecutive int) time.Duration {
	switch {
	case consecutive <= 0:
		return 0
	case consecutive == 1:
		return 15 * time.Minute
	case consecutive == 2:
		return 30 * time.Minute
	default:
		return time.Hour
	}
}

// updateStallReady reports whether enough time has passed to try serverVersion
// again. A version this install has never failed on is always ready.
func updateStallReady(profileName, serverVersion string, now time.Time) bool {
	st, ok := readUpdateStall(profileName)
	if !ok || st.Version != serverVersion {
		return true
	}
	// A record stamped in the future would hold this install back until the
	// calendar caught up with it, and the one failure this path must never have
	// is a permanent one: a client that cannot update itself cannot be sent the
	// fix for whatever is wrong with it. A clock that jumped forward and back is
	// enough to write one, so anything not in the past counts as due.
	if st.LastAt.After(now) {
		return true
	}
	return !now.Before(st.LastAt.Add(updateRetryDelay(st.Consecutive)))
}

// updateStallNotice describes a stalled self-update for `cctrace status`, or ""
// when this install is keeping up. running is the version this binary reports.
//
// The reason is reproduced verbatim. Summarising it would discard the only part
// that is actionable: a permission error names the path to look at, and no
// phrasing invented here can do that.
//
// running is compared rather than trusted from the record, because a record can
// outlive the problem it describes: somebody replaces the binary by hand -- the
// fix #458 actually used -- and nothing on the update path runs again to clear
// it. A notice that survives its own cause is worse than none, since it is the
// first thing the next investigation reads.
func updateStallNotice(st *updateStall, running string) string {
	if st == nil || st.Version == "" {
		return ""
	}
	if !semverGT(st.Version, running) {
		return ""
	}
	return fmt.Sprintf("[!] cctrace cannot update itself to %s (failing since %s, %d attempts): %s",
		st.Version, st.FirstAt.Format("2006-01-02"), st.Consecutive, st.Reason)
}
