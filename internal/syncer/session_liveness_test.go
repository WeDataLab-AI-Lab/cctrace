//go:build darwin || linux

package syncer

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// unusedPID is past the largest pid darwin (99,999) or Linux (2^22) hands out,
// so no process has it.
const unusedPID = 1 << 30

// claudePIDDomain is the pidDomain Claude Code 2.1.289 writes for a process of
// this test's own pid space, built the way its bundle builds it (K7o): the
// platform alone on darwin; on Linux and WSL the platform, the trimmed
// /etc/machine-id and the /proc/self/ns/pid link, each empty when unreadable.
func claudePIDDomain(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		return "darwin"
	}
	machineID, _ := os.ReadFile("/etc/machine-id")
	namespace, _ := os.Readlink("/proc/self/ns/pid")
	return "linux:" + strings.TrimSpace(string(machineID)) + ":" + namespace
}

// realProcStart is the test process's start time as Claude Code writes procStart:
// `LC_ALL=C TZ=UTC ps -o lstart=`, read here the same way, independently of
// processStart. Only where there is no ps does it fall back to processStart.
func realProcStart(t *testing.T) string {
	t.Helper()
	pid := os.Getpid()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	if out, err := cmd.Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	p, err := processStart(pid)
	if err != nil || !p.exists {
		t.Fatalf("the test process's own start time: exists %v, err %v", p.exists, err)
	}
	t.Logf("no ps here; procStart from processStart")
	return p.at.UTC().Format(procStartLayout)
}

func newLiveStateMemory() *liveStateMemory {
	return &liveStateMemory{
		seen:   map[liveStateKey]seenLiveState{},
		gone:   map[liveStateKey]goneLiveState{},
		done:   map[liveStateKey]doneLiveState{},
		starts: map[liveStateKey]int64{},
		files:  map[string]fileSighting{},
	}
}

// This client computes the pid domain Claude Code writes, so a file of this
// machine's own processes is checked and one of another machine is not.
func TestLocalPIDDomainIsClaudeCodes(t *testing.T) {
	if got, want := localPIDDomain(), claudePIDDomain(t); got != want {
		t.Errorf("localPIDDomain() = %q, want Claude Code's %q", got, want)
	}
}

// The test process with its real procStart is alive; a pid no process has is
// gone. What this client cannot check is never judged: a file of another pid
// domain -- another machine, another pid namespace, an older Claude Code that
// wrote the bare platform on Linux -- or a procStart in another form.
func TestProcessLiveness(t *testing.T) {
	pid := int64(os.Getpid())
	real := realProcStart(t)
	start, err := time.ParseInLocation(procStartLayout, real, time.UTC)
	if err != nil {
		t.Fatalf("procStart %q does not parse: %v", real, err)
	}
	domain := claudePIDDomain(t)
	for _, tc := range []struct {
		name      string
		pid       int64
		procStart string
		domain    string
		want      liveness
	}{
		{"this process", pid, real, domain, livenessAlive},
		{"no such pid", unusedPID, real, domain, livenessGone},
		{"another machine's Linux domain", unusedPID, real, "linux:0123456789abcdef01234567:pid:[4026531836]", livenessUnknown},
		{"a bare platform name", unusedPID, real, "linux", livenessUnknown},
		{"no domain", unusedPID, real, "", livenessUnknown},
		{"a procStart of another form", pid, start.Format(time.RFC3339), domain, livenessUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := liveStateKey{sessionID: "s", pid: tc.pid, procStart: tc.procStart}
			if got := newLiveStateMemory().liveness(key, tc.domain); got != tc.want {
				t.Errorf("liveness = %v, want %v", got, tc.want)
			}
		})
	}
}

// Once verified, a process is judged by its start id alone: the same id is
// alive, another -- its pid given to a later process -- is gone.
func TestVerifiedProcessIsJudgedByItsStartID(t *testing.T) {
	mem := newLiveStateMemory()
	key := liveStateKey{sessionID: "s", pid: int64(os.Getpid()), procStart: realProcStart(t)}
	domain := claudePIDDomain(t)
	if got := mem.liveness(key, domain); got != livenessAlive {
		t.Fatalf("first check = %v, want alive", got)
	}
	if got := mem.liveness(key, domain); got != livenessAlive {
		t.Errorf("second check = %v, want alive by the kept start id", got)
	}
	mem.starts[key]++
	if got := mem.liveness(key, domain); got != livenessGone {
		t.Errorf("with another process's start id = %v, want gone", got)
	}
}

// A file left by a process that no longer runs -- its pid free -- is reported
// gone once, dated by when it was found: heartbeat_at and status_changed_at
// both, which is what the server dates the gone in its history by. The file
// staying put sends nothing more.
func TestCrashLeftoverIsGoneOnce(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	freed := sessionFileFields(unusedPID, "sess-freed-pid")
	freed["pidDomain"] = claudePIDDomain(t)
	writeSessionFile(t, claudeDir, freed)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	flushOnce(t, s)
	states := liveStatesOf(srv.requests())
	if len(states) != 1 {
		t.Fatalf("%d live states, want the one gone: %s", len(states), srv.bodies())
	}
	st := states[0]
	if st.Status != "gone" || st.WaitingKind != "" || st.StatusChangedAt == nil ||
		!st.StatusChangedAt.Equal(st.HeartbeatAt) || !st.HeartbeatAt.Equal(nowFn()) || st.Name != "LIVE-NAME" {
		t.Errorf("gone state %+v, want status gone dated by the flush, as heartbeat and change alike, keeping the name", st)
	}
	advance(time.Minute)
	flushOnce(t, s)
	if n := len(liveStatesOf(srv.requests())); n != 1 {
		t.Errorf("%d live states after the second flush, want no more", n-1)
	}
	// Once no file reads as the process any more, nothing of it is kept.
	if err := os.Remove(filepath.Join(claudeDir, "sessions", strconv.Itoa(unusedPID)+".json")); err != nil {
		t.Fatal(err)
	}
	flushOnce(t, s)
	if mem := s.obs.live; len(mem.done)+len(mem.gone)+len(mem.seen)+len(mem.starts)+len(mem.files) != 0 {
		t.Errorf("memory after the file went: %+v, want it empty", mem)
	}
}

// A process seen alive whose file then disappears is gone, once, with the name
// it was last seen with.
func TestDisappearedProcessFileIsGone(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	fields := sessionFileFields(os.Getpid(), "sess-ending")
	fields["pidDomain"], fields["procStart"], fields["status"] = claudePIDDomain(t), realProcStart(t), "busy"
	path := writeSessionFile(t, claudeDir, fields)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	flushOnce(t, s)
	if states := liveStatesOf(srv.requests()); len(states) != 1 || states[0].Status != "busy" {
		t.Fatalf("first flush: %+v, want the process busy", states)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	advance(time.Minute)
	flushOnce(t, s)
	states := liveStatesOf(srv.requests())
	if len(states) != 2 || states[1].Status != "gone" || states[1].Name != "LIVE-NAME" ||
		!states[1].HeartbeatAt.Equal(nowFn()) || states[1].StatusChangedAt == nil || !states[1].StatusChangedAt.Equal(nowFn()) {
		t.Fatalf("after the file went: %+v, want it gone, dated now, with its name", states)
	}
	advance(time.Minute)
	flushOnce(t, s)
	if n := len(liveStatesOf(srv.requests())); n != 2 {
		t.Errorf("%d live states after the gone was delivered, want none more", n-2)
	}
	if mem := s.obs.live; len(mem.done)+len(mem.gone)+len(mem.seen)+len(mem.starts)+len(mem.files) != 0 {
		t.Errorf("memory once no file reads as the process: %+v, want it empty", mem)
	}
}

// A process whose file moves to another session and back -- /resume of an
// earlier session in the same process -- is gone while away and alive again
// after: only a gone verified from the process itself is final.
//
// Claude Code switches without touching statusUpdatedAt; it rewrites updatedAt.
// A session switched to is dated by updatedAt, so the one taken up again is
// dated after the gone it follows and the server files the two in order -- and
// keeps that date while it stays. A file whose updatedAt says nothing newer is
// dated by when the switch was found.
func TestSessionSwitchedBackToIsAliveAgain(t *testing.T) {
	for _, tc := range []struct {
		name      string
		refreshed bool // Claude Code rewrites updatedAt on the switch
	}{{"updatedAt rewritten", true}, {"updatedAt unchanged", false}} {
		t.Run(tc.name, func(t *testing.T) {
			advance := fakeClock(t)
			srv := newObservationServer(t)
			claudeDir := t.TempDir()
			real, domain := realProcStart(t), claudePIDDomain(t)
			statusAt := nowFn().Add(-10 * time.Minute).Truncate(time.Millisecond)
			s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
			type sent struct {
				what           string
				changed, flush time.Time
			}
			var got []sent
			var writes, flushes []time.Time
			step := func(sid string) {
				t.Helper()
				f := sessionFileFields(os.Getpid(), sid)
				f["pidDomain"], f["procStart"], f["status"] = domain, real, "busy"
				f["statusUpdatedAt"], f["updatedAt"] = statusAt.UnixMilli(), statusAt.UnixMilli()
				if tc.refreshed {
					f["updatedAt"] = nowFn().UnixMilli()
				}
				writes = append(writes, nowFn())
				writeSessionFile(t, claudeDir, f)
				advance(10 * time.Second)
				flushes = append(flushes, nowFn())
				before := len(srv.requests())
				flushOnce(t, s)
				advance(50 * time.Second)
				for _, req := range srv.requests()[before:] {
					for _, st := range req.LiveStates {
						got = append(got, sent{st.SessionID + " " + st.Status, *st.StatusChangedAt, flushes[len(flushes)-1]})
					}
				}
			}
			step("sess-A")
			step("sess-B")
			step("sess-A")
			step("sess-A")
			var what []string
			for _, g := range got {
				what = append(what, g.what)
			}
			want := "sess-A busy|sess-B busy|sess-A gone|sess-A busy|sess-B gone|sess-A busy"
			if strings.Join(what, "|") != want {
				t.Fatalf("sent\n%s\nwant\n%s", strings.Join(what, "\n"), strings.ReplaceAll(want, "|", "\n"))
			}
			// When each switch shows: the write when updatedAt says so, else the
			// flush that found it.
			switchAt := func(i int) time.Time {
				if tc.refreshed {
					return writes[i]
				}
				return flushes[i]
			}
			for i, want := range []time.Time{
				statusAt,    // sess-A, first read: statusUpdatedAt
				switchAt(1), // sess-B, switched to
				flushes[1],  // sess-A gone, found then
				switchAt(2), // sess-A, switched back to: after its gone
				flushes[2],  // sess-B gone
				switchAt(2), // sess-A again: the same date while it stays
			} {
				if !got[i].changed.Equal(want) {
					t.Errorf("%s (send %d): status_changed_at %s, want %s", got[i].what, i, got[i].changed.Format(time.RFC3339), want.Format(time.RFC3339))
				}
			}
			if !got[3].changed.After(got[2].changed) {
				t.Errorf("sess-A came back dated %s, not after its gone at %s", got[3].changed, got[2].changed)
			}
		})
	}
}

// A switch whose file is dated ahead is dated by the flush that found it. A
// floor in the future would leave every report of the key skipped by the server
// as too far ahead, for as long as the key lives.
func TestSwitchDatedAheadIsDatedByTheFlush(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	real, domain := realProcStart(t), claudePIDDomain(t)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	write := func(sid string, updatedAt time.Time) {
		t.Helper()
		f := sessionFileFields(os.Getpid(), sid)
		f["pidDomain"], f["procStart"], f["status"] = domain, real, "busy"
		f["statusUpdatedAt"] = nowFn().Add(-10 * time.Minute).UnixMilli()
		f["updatedAt"] = updatedAt.UnixMilli()
		writeSessionFile(t, claudeDir, f)
	}
	write("sess-A", nowFn())
	flushOnce(t, s)
	advance(time.Minute)
	write("sess-B", nowFn().Add(365*24*time.Hour))
	flushAt := nowFn()
	before := len(srv.requests())
	flushOnce(t, s)
	var changed *time.Time
	for _, req := range srv.requests()[before:] {
		for _, st := range req.LiveStates {
			if st.SessionID == "sess-B" && st.Status == "busy" {
				changed = st.StatusChangedAt
			}
		}
	}
	if changed == nil {
		t.Fatal("sess-B was not sent")
	}
	if changed.After(flushAt) {
		t.Errorf("sess-B dated %s, after the flush at %s", changed.Format(time.RFC3339), flushAt.Format(time.RFC3339))
	}
}

// A gone that does not get through is not lost: it goes with the next flush.
func TestGoneIsResentUntilDelivered(t *testing.T) {
	srv := newObservationServer(t)
	failed := false
	srv.setAnswer(func(w http.ResponseWriter, req SessionObservationsRequest) bool {
		if failed || len(req.LiveStates) == 0 {
			return false
		}
		failed = true
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return true
	})
	claudeDir := t.TempDir()
	freed := sessionFileFields(unusedPID, "sess-freed-pid")
	freed["pidDomain"] = claudePIDDomain(t)
	writeSessionFile(t, claudeDir, freed)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	if err := s.FlushObservations(context.Background()); err == nil {
		t.Fatal("a 503 was not reported")
	}
	flushOnce(t, s)
	flushOnce(t, s)
	var gone int
	for _, st := range liveStatesOf(srv.requests()) {
		if st.Status == "gone" {
			gone++
		}
	}
	if gone != 2 {
		t.Errorf("gone sent %d times, want the refused attempt and the one retry", gone)
	}
}
