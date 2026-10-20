package syncer

import (
	"os"
	"testing"
	"time"
)

// darwin records a process's start time when it forks, so a pid whose process
// started at another time than the file says has been given to another process:
// the file's writer is gone, verified, from the first look.
func TestDarwinPIDOfAnotherStartIsGone(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	start, err := time.ParseInLocation(procStartLayout, realProcStart(t), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{time.Hour, -time.Hour} {
		key := liveStateKey{sessionID: "s", pid: int64(os.Getpid()), procStart: start.Add(offset).Format(procStartLayout)}
		if got := newLiveStateMemory().liveness(key, claudePIDDomain(t)); got != livenessGone {
			t.Errorf("a start %s off: liveness %v, want gone", offset, got)
		}
	}

	reused := sessionFileFields(os.Getpid(), "sess-reused-pid")
	reused["pidDomain"], reused["procStart"] = claudePIDDomain(t), start.Add(time.Hour).Format(procStartLayout)
	writeSessionFile(t, claudeDir, reused)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	flushOnce(t, s)
	advance(time.Minute)
	flushOnce(t, s)
	states := liveStatesOf(srv.requests())
	if len(states) != 1 || states[0].Status != "gone" || states[0].StatusChangedAt == nil || !states[0].StatusChangedAt.Equal(states[0].HeartbeatAt) {
		t.Errorf("sent %+v, want one gone dated by when it was found", states)
	}
}
