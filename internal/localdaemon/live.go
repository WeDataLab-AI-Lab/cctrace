package localdaemon

import (
	"errors"
	"time"
)

// SnapshotFreshness is how old a snapshot may be and still describe the daemon
// as it is now. The desktop applies the same bound to the same file.
const SnapshotFreshness = 120 * time.Second

// SnapshotLive reports whether snapshot is what the daemon that rt describes
// published, and recent enough to trust: the same instance and process, not
// stamped in the future, no older than SnapshotFreshness. The file outlives the
// daemon that wrote it, so a snapshot alone never proves a daemon is running.
func SnapshotLive(rt Runtime, snapshot Snapshot, now time.Time) bool {
	return rt.InstanceID != "" && rt.PID > 0 &&
		snapshot.InstanceID == rt.InstanceID && snapshot.PID == rt.PID &&
		!snapshot.ObservedAt.After(now) && now.Sub(snapshot.ObservedAt) <= SnapshotFreshness
}

// LiveSnapshot returns the snapshot of the daemon running under root, and false
// when none is running, its files cannot be read, or the snapshot is not that
// daemon's current one (SnapshotLive).
func LiveSnapshot(root string) (Snapshot, bool) {
	if running, err := Running(root); err != nil || !running {
		return Snapshot{}, false
	}
	rt, snapshot, err := readStable(func() (Runtime, Snapshot, error) {
		rt, err := ReadRuntime(root)
		if err != nil {
			return Runtime{}, Snapshot{}, err
		}
		snapshot, err := ReadSnapshot(root)
		return rt, snapshot, err
	})
	if err != nil || !SnapshotLive(rt, snapshot, time.Now()) {
		return Snapshot{}, false
	}
	return snapshot, true
}

// readStable runs read, and once more if it lost a race with the daemon
// rewriting its files (ErrMetadataChanged). Any other failure is final.
func readStable(read func() (Runtime, Snapshot, error)) (Runtime, Snapshot, error) {
	rt, snapshot, err := read()
	if errors.Is(err, ErrMetadataChanged) {
		rt, snapshot, err = read()
	}
	return rt, snapshot, err
}
