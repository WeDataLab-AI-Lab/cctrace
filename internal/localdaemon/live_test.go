package localdaemon

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSnapshotLive(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	rt := Runtime{PID: 41, InstanceID: "instance-a"}
	published := func(mutate func(*Snapshot)) Snapshot {
		s := Snapshot{Runtime: rt, ObservedAt: now.Add(-3 * time.Second)}
		mutate(&s)
		return s
	}
	cases := []struct {
		name     string
		rt       Runtime
		snapshot Snapshot
		want     bool
	}{
		{"published a moment ago by this daemon", rt, published(func(*Snapshot) {}), true},
		{"exactly at the freshness bound", rt, published(func(s *Snapshot) { s.ObservedAt = now.Add(-SnapshotFreshness) }), true},
		{"a second past the bound", rt, published(func(s *Snapshot) { s.ObservedAt = now.Add(-SnapshotFreshness - time.Second) }), false},
		{"never observed", rt, published(func(s *Snapshot) { s.ObservedAt = time.Time{} }), false},
		{"stamped in the future", rt, published(func(s *Snapshot) { s.ObservedAt = now.Add(time.Second) }), false},
		{"left by another instance", rt, published(func(s *Snapshot) { s.InstanceID = "instance-b" }), false},
		{"left by another process", rt, published(func(s *Snapshot) { s.PID = 42 }), false},
		{"runtime file names no instance", Runtime{PID: 41}, published(func(s *Snapshot) { s.InstanceID = "" }), false},
		{"runtime file names no process", Runtime{InstanceID: "instance-a"}, published(func(s *Snapshot) { s.PID = 0 }), false},
	}
	for _, tc := range cases {
		if got := SnapshotLive(tc.rt, tc.snapshot, now); got != tc.want {
			t.Errorf("%s: live = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The daemon rewrites its snapshot every tick, so a reader sometimes finds the
// file replaced between the checks ReadJSON makes. That is a lost race, not a
// reason to say there is no daemon: it is read once more, and no more than once.
func TestReadStableRetriesALostRaceOnce(t *testing.T) {
	want := Snapshot{ObservedAt: time.Unix(1, 0)}
	calls := 0
	rt, got, err := readStable(func() (Runtime, Snapshot, error) {
		calls++
		if calls == 1 {
			return Runtime{}, Snapshot{}, ErrMetadataChanged
		}
		return Runtime{PID: 7}, want, nil
	})
	if err != nil || calls != 2 || rt.PID != 7 || !got.ObservedAt.Equal(want.ObservedAt) {
		t.Fatalf("calls=%d err=%v rt=%+v", calls, err, rt)
	}

	calls = 0
	if _, _, err := readStable(func() (Runtime, Snapshot, error) {
		calls++
		return Runtime{}, Snapshot{}, ErrMetadataChanged
	}); !errors.Is(err, ErrMetadataChanged) || calls != 2 {
		t.Fatalf("a second lost race must be reported: calls=%d err=%v", calls, err)
	}

	calls = 0
	other := errors.New("unsafe daemon metadata")
	if _, _, err := readStable(func() (Runtime, Snapshot, error) { calls++; return Runtime{}, Snapshot{}, other }); err != other || calls != 1 {
		t.Fatalf("any other failure is final: calls=%d err=%v", calls, err)
	}
}

func TestLiveSnapshotFollowsTheRunningDaemon(t *testing.T) {
	t.Parallel()
	f := newBlockFixture(t, "one", "two")
	if _, live := LiveSnapshot(f.root); live {
		t.Fatal("a snapshot was reported live before any daemon ran")
	}
	stop := f.start()
	eventually(t, func() bool {
		return f.probeWhenReady(func() bool {
			snapshot, live := LiveSnapshot(f.root)
			return live && len(snapshot.Jobs) == 2
		})
	})
	stop()
	if _, live := LiveSnapshot(f.root); live {
		t.Fatal("the file a stopped daemon leaves behind was reported live")
	}
}

func TestBlockFixtureKeepsLiveProbeBehindReadiness(t *testing.T) {
	t.Parallel()
	f := newBlockFixture(t, "one", "two")
	loadGate := make(chan struct{})
	var releaseOnce, loadOnce sync.Once
	releaseLoad := func() { releaseOnce.Do(func() { close(loadGate) }) }
	defer releaseLoad()
	f.spec = func(spec Spec) Spec {
		loadOnce.Do(func() { <-loadGate })
		return spec
	}
	f.start()
	calls := 0
	probe := func() bool {
		calls++
		snapshot, live := LiveSnapshot(f.root)
		return live && len(snapshot.Jobs) == 2
	}
	if f.probeWhenReady(probe) || calls != 0 {
		t.Fatal("live lock probe ran while initial publication was gated")
	}
	releaseLoad()
	// Startup and observation share the existing eventually deadline; there is
	// no separate readiness wait or additional timeout budget.
	eventually(t, func() bool { return f.probeWhenReady(probe) })
	if calls == 0 {
		t.Fatal("ready fixture never ran its live probe")
	}
}
