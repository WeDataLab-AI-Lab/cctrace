package syncer

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stepWallClock moves the wall clock by d as Linux shows it to processStart:
// /proc/stat btime is the wall clock now minus the time since boot, so a step of
// the clock moves it by the same amount, and every start time derived from it.
func stepWallClock(t *testing.T, d time.Duration) {
	t.Helper()
	previous := bootTimeFn
	bootTimeFn = func() (time.Time, error) {
		boot, err := bootTime()
		return boot.Add(d), err
	}
	t.Cleanup(func() { bootTimeFn = previous })
}

// startProcess runs path as a child that sleeps for a minute, with argv0 as its
// argv[0], and returns its pid; the child is ended with the test.
func startProcess(t *testing.T, path, argv0 string) int {
	t.Helper()
	cmd := &exec.Cmd{Path: path, Args: []string{argv0, "60"}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// startClaudeLike runs a process that looks like Claude Code's native build
// from /proc -- an executable at .../claude/versions/<version>, argv[0] claude --
// and returns its pid. It is sleep, copied there.
func startClaudeLike(t *testing.T) int {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(t.TempDir(), "claude", "versions", "2.1.289")
	if err := os.MkdirAll(filepath.Dir(native), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return startProcess(t, native, "claude")
}

// procStartOf is the procStart Claude Code writes for pid: `LC_ALL=C TZ=UTC ps -o
// lstart=`, read the same way.
func procStartOf(t *testing.T, pid int) string {
	t.Helper()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// A Claude Code process verified alive stays alive through a step of the wall
// clock: from then on it is judged by its start ticks, which no step moves. Seen
// for the first time after the step -- a collector restarted meanwhile -- the
// start it gives no longer matches procStart, and that cannot be told from a pid
// given to another program that looks like Claude Code: its liveness is
// unknown, never gone.
func TestLinuxWallClockStepNeverMakesALiveProcessGone(t *testing.T) {
	pid := startClaudeLike(t)
	key := liveStateKey{sessionID: "s", pid: int64(pid), procStart: procStartOf(t, pid)}
	domain := claudePIDDomain(t)
	verified := newLiveStateMemory()
	if got := verified.liveness(key, domain); got != livenessAlive {
		t.Fatalf("before the step: %v, want alive", got)
	}
	for _, step := range []time.Duration{8 * time.Hour, -time.Hour} {
		t.Run(step.String(), func(t *testing.T) {
			stepWallClock(t, step)
			if got := verified.liveness(key, domain); got != livenessAlive {
				t.Errorf("verified before the step: %v, want alive", got)
			}
			if got := newLiveStateMemory().liveness(key, domain); got != livenessUnknown {
				t.Errorf("first seen after the step: %v, want unknown", got)
			}
		})
	}
}

// The same through a flush: a collector that starts after a step reports the
// session with the status its file gives, flush after flush, and no gone.
func TestLinuxCollectorStartedAfterAStepSendsNoGone(t *testing.T) {
	advance := fakeClock(t)
	pid := startClaudeLike(t)
	procStart := procStartOf(t, pid)
	stepWallClock(t, 8*time.Hour)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	fields := sessionFileFields(pid, "sess-stepped")
	fields["pidDomain"], fields["procStart"], fields["status"] = claudePIDDomain(t), procStart, "idle"
	writeSessionFile(t, claudeDir, fields)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	for i := 0; i < 3; i++ {
		flushOnce(t, s)
		advance(time.Minute)
	}
	states := liveStatesOf(srv.requests())
	if len(states) != 3 {
		t.Fatalf("%d live states over three flushes, want 3", len(states))
	}
	for _, st := range states {
		if st.Status != "idle" {
			t.Errorf("sent %s, want the file's idle", st.Status)
		}
	}
}

// When a pid's first check does not match its file, a step of the wall clock and
// a pid given to another process look alike -- unless the process now with the
// pid plainly runs another program. Its executable and arguments are read from
// /proc, which no clock moves: a process naming no claude anywhere is another
// program, and the file's writer is gone. One that looks like Claude Code, or
// that cannot be read, stays unknown.
func TestLinuxPIDOfAnotherProgramIsGone(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	// A procStart an hour before the process started: the first check fails.
	mismatched := func(pid int) liveStateKey {
		t.Helper()
		p, err := processStart(pid)
		if err != nil || !p.exists {
			t.Fatalf("pid %d: exists %v, err %v", pid, p.exists, err)
		}
		return liveStateKey{sessionID: "s", pid: int64(pid), procStart: p.at.Add(-time.Hour).UTC().Format(procStartLayout)}
	}
	domain := claudePIDDomain(t)
	for _, tc := range []struct {
		name string
		pid  int
		want liveness
	}{
		{"another program", startProcess(t, sleep, "sleep"), livenessGone},
		{"a claude command", startProcess(t, sleep, "claude"), livenessUnknown},
		{"Claude Code's native build", startClaudeLike(t), livenessUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newLiveStateMemory().liveness(mismatched(tc.pid), domain); got != tc.want {
				t.Errorf("liveness = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		pid := startProcess(t, sleep, "sleep")
		previous := programOf
		programOf = func(int) (string, []string, error) { return "", nil, fs.ErrPermission }
		t.Cleanup(func() { programOf = previous })
		if got := newLiveStateMemory().liveness(mismatched(pid), domain); got != livenessUnknown {
			t.Errorf("liveness = %v, want unknown", got)
		}
	})
}
