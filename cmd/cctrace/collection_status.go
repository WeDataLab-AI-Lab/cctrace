package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"cctrace/internal/syncer"
)

// collectionStallAfter is how long sending has to keep failing before `cctrace
// status` calls it a stall.
//
// It is a duration and not a number of passes because a pass has no fixed
// length: the installed SessionStart hook polls once a second, the flag defaults
// to thirty, and a failing pass then backs off to as much as five minutes. 15
// minutes clears every ordinary interruption -- a Wi-Fi roam, a VPN toggle, a
// server redeploy, a laptop waking up -- and still leaves a person 15 minutes of
// warning before the daemon releases its lock. Var, not const, so tests can
// shrink it.
var collectionStallAfter = 15 * time.Minute

// collectionStatusIndent aligns continuation lines under the value column of the
// `수집 상태:` line, the same way daemonRestartNotice aligns its second line.
const collectionStatusIndent = "                       "

// collectionStatusValue renders what the daemon is actually getting through, for
// the `수집 상태:` line.
//
// This exists because the line above it answers a different question. `서버
// 도달:` is the status process reaching the server itself, and for two days it
// said healthy -- correctly -- while the daemon on the same machine failed every
// single send (#712). One line per subject is what keeps the true answer to one
// question from standing in for the other.
//
// Elapsed time comes from the record's own two timestamps rather than from the
// clock, so this stays a pure function and the wording can be fixed by a test.
// The record's LastFailedAt is refreshed on an interval while a stall continues,
// which is why it is a usable "as of" and not a stale first sighting.
//
// It stays quiet when there is no record (the ordinary case), when the failure
// is younger than collectionStallAfter, and when the record's timestamps run
// backwards. A false alarm on a status line is worse than silence -- the same
// principle as daemonRestartNotice. It does NOT stay quiet when no daemon is
// running: the daemon exits on purpose after a long stall, and the fact that
// nothing has been collected since outlives it. Only the remedy changes.
func collectionStatusValue(rec *syncer.TransportFailure, daemonAlive bool) string {
	// A record without a first failure is a partial or hand-edited one; from the
	// zero time its "duration" would be two thousand years.
	if rec == nil || rec.FirstFailedAt.IsZero() {
		return "[OK] 전송 실패 기록이 없습니다"
	}
	failing := rec.LastFailedAt.Sub(rec.FirstFailedAt)
	if failing < collectionStallAfter {
		return "[OK] 전송 실패 기록이 없습니다"
	}

	lines := make([]string, 0, 4)
	switch {
	case rec.Class == syncer.TransportFailureClassServer:
		lines = append(lines, fmt.Sprintf("[!] 서버가 %s째 요청 과다(429)로 전송을 거부하고 있습니다 (파일 %d개 실패)",
			koreanDuration(failing), rec.FilesFailed))
	case daemonAlive:
		lines = append(lines, fmt.Sprintf("[!] 데몬이 %s째 전송에 성공하지 못했습니다 (파일 %d개 실패)",
			koreanDuration(failing), rec.FilesFailed))
	default:
		lines = append(lines, fmt.Sprintf("[!] 전송에 성공하지 못한 채 %s이 지났고 데몬은 실행 중이 아닙니다 (파일 %d개 실패)",
			koreanDuration(failing), rec.FilesFailed))
	}

	if !rec.LastSuccessAt.IsZero() {
		lines = append(lines, "마지막 성공: "+rec.LastSuccessAt.Format("2006-01-02 15:04"))
	}
	if rec.LastError != "" {
		// Reproduced verbatim, like updateStallNotice's reason: the address and
		// the errno are the part an investigation starts from, and no phrasing
		// invented here can carry them.
		lines = append(lines, "최근 오류: "+rec.LastError)
	}

	switch {
	case rec.Class == syncer.TransportFailureClassServer:
		// Restarting throws away the backoff that is honouring Retry-After, which
		// is the self-perpetuating retry this path exists to avoid.
		lines = append(lines, "재시작으로는 회복되지 않습니다 -- 서버의 요청 제한이 풀리기를 기다립니다. 오프셋은 그대로이므로 데이터는 사라지지 않습니다")
	case daemonAlive:
		lines = append(lines,
			"이 프로세스는 서버에 닿는데 데몬은 닿지 못한다면 데몬만 막혀 있습니다 --",
			"cctrace sync --stop 뒤 cctrace sync --daemon 으로 다시 띄우면 회복됩니다")
	default:
		lines = append(lines, "cctrace sync --daemon 으로 다시 띄우거나, Claude Code 세션을 새로 열면 자동으로 뜹니다")
	}

	return strings.Join(lines, "\n"+collectionStatusIndent)
}

// koreanDuration renders a stretch of failure the way the rest of this command
// talks to a person: days and hours once it is long, minutes while it is short.
// Seconds are never shown -- nothing here is reported until 15 minutes in.
func koreanDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	if minutes < 60 {
		return fmt.Sprintf("%d분", minutes)
	}
	hours := minutes / 60
	minutes %= 60
	if hours < 24 {
		if minutes == 0 {
			return fmt.Sprintf("%d시간", hours)
		}
		return fmt.Sprintf("%d시간 %d분", hours, minutes)
	}
	days := hours / 24
	hours %= 24
	if hours == 0 {
		return fmt.Sprintf("%d일", days)
	}
	return fmt.Sprintf("%d일 %d시간", days, hours)
}

// longerStall keeps whichever of two records describes the longer failing
// stretch.
//
// Every agent keeps its own state file and status reads all four, so more than
// one can carry a record. Only the Claude syncer writes one today; if another
// starts, the stall that has lasted longest is the one a person needs to see
// first.
func longerStall(a, b *syncer.TransportFailure) *syncer.TransportFailure {
	if b == nil {
		return a
	}
	if a == nil {
		return b
	}
	if b.LastFailedAt.Sub(b.FirstFailedAt) > a.LastFailedAt.Sub(a.FirstFailedAt) {
		return b
	}
	return a
}

// daemonHoldsSyncLock reports whether a watch daemon is running, by the same
// probe daemonRestartNotice uses: the lock is the truth, because signalling a
// pid supports only os.Kill on Windows.
//
// An unreadable lock is read as "a daemon is running". The two wordings differ
// only in the remedy they name, and the running-daemon remedy -- stop, then
// start -- does no harm when nothing is running.
func daemonHoldsSyncLock(profileName string) bool {
	free, err := syncLockFreeFn(profileName)
	if err != nil {
		return true
	}
	return !free
}

// passStalled reports a pass that attempted sends and got nothing through.
//
// Both halves matter. Failures with records accepted in the same pass mean one
// file is in trouble, not the path to the server, and treating that as a stall
// would back off collection over a single held session. No failures at all is an
// idle machine, which is the ordinary case.
func passStalled(stats syncer.PassStats) bool {
	return stats.FilesFailed > 0 && stats.Sent == 0
}

// stalledPassLine is what the watch loop prints when nothing got through.
//
// The elapsed stretch is what makes this line survive the log deduper: the
// previous line reported records-since-startup, which does not change while
// nothing is being sent, so the repeats were correctly folded away and the fold
// read as quiet (#712).
func stalledPassLine(stats syncer.PassStats, stalledFor, retryIn time.Duration) string {
	files := "files"
	if stats.FilesFailed == 1 {
		files = "file"
	}
	// Rounded to the minute once it is minutes, to the second while it is
	// seconds: a first failing pass reporting "no success for 0s" reads like a
	// rounding artifact, which is what it was.
	unit := time.Minute
	if stalledFor < time.Minute {
		unit = time.Second
	}
	line := fmt.Sprintf("[sync] pass: 0 sent, %d %s failed, no success for %s",
		stats.FilesFailed, files, stalledFor.Round(unit))
	if stats.LastErr != nil {
		line += " -- " + stats.LastErr.Error()
	}
	return line + fmt.Sprintf("; retry in %s", retryIn)
}

// noteSatelliteResult adds a satellite syncer's records, reporting a failure
// instead of dropping it, and returns what to add.
//
// Watch mode used to discard these with `if err == nil`, so Codex, gjc or omo
// collection could fail on every pass with nothing in the log -- while the
// initial pass in the same function reported the identical error.
func noteSatelliteResult(label string, records int, err error) int {
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			diagf("[%s] %v", label, err)
		}
		return 0
	}
	return records
}

// syncStallExitAfter is how long a watcher keeps trying before it stops holding
// the single-instance lock.
//
// It is measured from this process's own last successful send, so a watcher can
// only ever quit over a stall it witnessed itself. 30 minutes is twice the
// threshold at which status starts reporting, which is the point: a person gets
// 15 minutes to see the stall before the process that recorded it goes away.
// Var, not const, so tests can shrink it.
var syncStallExitAfter = 30 * time.Minute

// releaseLockForStall decides whether a stalled watcher should exit.
//
// Only a transport-class stall qualifies. #712 was recovered by killing the
// daemon -- a new process worked immediately against the same server from the
// same machine -- and the client had no way to reach that outcome, because the
// watcher holds the lock until it exits and every daemon the hook spawned for
// two days found the lock taken and left. Exiting hands the next hook-spawned
// process a free lock; it does not respawn anything itself, so a genuine outage
// costs one restart per session start rather than a restart loop.
//
// A server refusal is excluded: 413 needs a bigger limit on the server and 429
// needs waiting, and a new process would discard the backoff that is honouring
// the Retry-After.
//
// hasSuccessor is the SessionStart hook. Without it nobody starts the
// replacement, and exiting would trade a daemon that keeps retrying -- which
// recovers by itself when a long outage ends -- for no daemon at all.
//
// The trade this makes, on purpose: an outage longer than the threshold inside
// one long Claude session now stops collection until the next session starts,
// where the old daemon would have resumed by itself when the network came back.
// Nothing is lost -- offsets stay where they were -- but the Codex, gjc and omo
// syncers and quota polling ride in this process and pause with it. They share
// its client and its endpoint, so during a real transport stall they were not
// getting through either. A machine that uses only Codex never gets here: the
// stall is judged on Claude sessions, and with none to send there is no stall.
func releaseLockForStall(rec *syncer.TransportFailure, stalledFor time.Duration, hasSuccessor bool) bool {
	if !hasSuccessor {
		return false
	}
	if rec == nil || rec.Class != syncer.TransportFailureClassTransport {
		return false
	}
	return stalledFor >= syncStallExitAfter
}
