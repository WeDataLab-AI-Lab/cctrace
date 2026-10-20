// Package localdaemon owns one local collection process and independent profile jobs.
package localdaemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cctrace/internal/atomicfile"
	"cctrace/internal/filesec"

	"github.com/gofrs/flock"
)

var ErrAlreadyRunning = errors.New("local collection daemon already running")

// ErrPolicyLockBusy is what a job's Acquire reports when it waited for the policy
// lock -- held by a policy apply or by another profile starting -- and ran out of
// time, or found its profile changed by a policy while it waited. The job is
// retried, and its status says policy_lock_busy.
var ErrPolicyLockBusy = errors.New("policy lock wait exceeded; a policy apply or another profile start holds it")

// ErrMetadataChanged is ReadJSON's refusal when the file was replaced while it
// was being read. The daemon rewrites its snapshot every tick, so for a reader
// this is a lost race, not a damaged file.
var ErrMetadataChanged = errors.New("daemon metadata changed")

const maxInput = 1 << 20

type Runtime struct {
	PID        int       `json:"pid"`
	InstanceID string    `json:"instance_id"`
	StartedAt  time.Time `json:"started_at"`
	Version    string    `json:"version"`
}

type JobStatus struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	State          string    `json:"state"`
	Generation     int       `json:"generation"`
	Sent           int64     `json:"sent"`
	BlockedFiles   int64     `json:"blocked_files"`
	ErrorCode      string    `json:"error_code,omitempty"`
	LastPassAt     time.Time `json:"last_pass_at,omitempty"`
	NextRetryAt    time.Time `json:"next_retry_at,omitempty"`
	RouteSignature string    `json:"route_signature,omitempty"`
	RouteOwner     bool      `json:"route_owner,omitempty"`
	// SharedSources are the sources this profile does not read because another
	// profile's route holds them. They are a diagnostic: they leave ErrorCode and
	// the retry state alone.
	SharedSources []SharedSource `json:"shared_sources,omitempty"`
}

type Snapshot struct {
	Runtime
	ObservedAt time.Time   `json:"observed_at"`
	Complete   bool        `json:"complete"`
	Jobs       []JobStatus `json:"jobs"`
}

type commandRequest struct {
	InstanceID string `json:"instance_id"`
	Action     string `json:"action"`
	ProfileID  string `json:"profile_id,omitempty"`
	// claim: the source (a route id; a "kind:folder" line when the daemon has no
	// usable routes file), the profile the caller saw holding it, and the
	// revisions of the route and of the two profiles as the caller saw them.
	Source    string `json:"source,omitempty"`
	Holder    string `json:"holder,omitempty"`
	RouteRev  int    `json:"route_rev,omitempty"`
	TargetRev string `json:"target_rev,omitempty"`
	HolderRev string `json:"holder_rev,omitempty"`
}

type commandReply struct {
	Error string `json:"error,omitempty"`
	// Reason and Holders say why a profile_unavailable was refused and which
	// profiles hold the sources it overlaps; More counts holders past the cap.
	Reason  string   `json:"reason,omitempty"`
	Holders []string `json:"holders,omitempty"`
	More    int      `json:"holders_more,omitempty"`
}

// ControlError is the daemon's refusal of a control request.
type ControlError struct {
	Code, Reason string
	Holders      []string
	More         int
}

func (e *ControlError) Error() string {
	base := "daemon control: " + e.Code
	if e.Code != "profile_unavailable" {
		return base
	}
	switch e.Reason {
	case "source_overlap":
		return base + ": every collection source of this profile is already collected by another profile (held by: " + e.heldBy() + "). Free a source in that profile, or run: cctrace profile claim <profile> --all"
	case "disabled":
		return base + ": collection is disabled for this profile"
	case "migration_conflict":
		return base + ": profile migration conflict must be resolved first"
	}
	return base
}

func (e *ControlError) heldBy() string {
	if len(e.Holders) == 0 {
		return "(unknown)"
	}
	held := strings.Join(e.Holders, ", ")
	if e.More > 0 {
		held += fmt.Sprintf(", and %d more", e.More)
	}
	return held
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func writeJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0600)
}

// ReadJSON refuses symlinks and bounds every control/status read. It does not
// read profile credentials or provider-owned directories.
func ReadJSON(path string, value any) error {
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > maxInput {
		return errors.New("unsafe daemon metadata")
	}
	if err := rejectWritableByOthers(path, before, "unsafe daemon metadata"); err != nil {
		return err
	}
	f, err := openForRead(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return ErrMetadataChanged
	}
	b, err := io.ReadAll(io.LimitReader(f, maxInput+1))
	if err != nil {
		return err
	}
	if len(b) > maxInput {
		return errors.New("daemon metadata too large")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return ErrMetadataChanged
	}
	return json.Unmarshal(b, value)
}

// openForRead opens a daemon file. On Windows the daemon's replace of the file
// holds readers off for a moment, and the open waits that out (atomicfile.RetryBusy);
// elsewhere it is os.Open.
func openForRead(path string) (f *os.File, err error) {
	err = atomicfile.RetryBusy(func() (openErr error) { f, openErr = os.Open(path); return openErr })
	return f, err
}

func ReadSnapshot(root string) (Snapshot, error) {
	var s Snapshot
	err := ReadJSON(filepath.Join(root, "daemon-status.json"), &s)
	return s, err
}

func ReadRuntime(root string) (Runtime, error) {
	var r Runtime
	err := ReadJSON(filepath.Join(root, "daemon-runtime.json"), &r)
	return r, err
}

func safeDirectory(path string) error {
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe daemon directory")
	}
	return rejectWritableByOthers(path, i, "unsafe daemon directory")
}

// rejectWritableByOthers fails with unsafe when other users can change the file,
// and also when that cannot be told: the daemon trusts only what it can show
// nobody else wrote.
func rejectWritableByOthers(path string, info os.FileInfo, unsafe string) error {
	writable, err := filesec.WritableByOthers(path, info)
	return othersWriteVerdict(writable, err, unsafe)
}

// othersWriteVerdict maps filesec's answer to the error the daemon's callers see.
// A file that vanished since the Lstat that found it stays os.ErrNotExist, which
// callers branch on for a file that is not there yet. Every other failure to tell
// is as unsafe as a write grant.
func othersWriteVerdict(writable bool, err error, unsafe string) error {
	if errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil || writable {
		return errors.New(unsafe)
	}
	return nil
}

// Running probes the singleton lock, never a PID file. No process is signalled.
func Running(root string) (bool, error) {
	if err := safeDirectory(root); err != nil {
		return false, err
	}
	path := filepath.Join(root, "daemon.lock")
	if i, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	} else if !i.Mode().IsRegular() || i.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("unsafe daemon lock")
	}
	l := flock.New(path)
	ok, err := l.TryLock()
	if err != nil {
		return false, err
	}
	if ok {
		return false, l.Unlock()
	}
	return true, nil
}

func Command(ctx context.Context, root, action, id string) error {
	if action != "shutdown" && action != "stop" && action != "start" && action != "restart" && action != "collect" {
		return errors.New("invalid daemon action")
	}
	var settled func() (bool, error)
	switch action {
	case "shutdown":
		settled = func() (bool, error) {
			live, err := Running(root)
			return !live, err
		}
	case "stop":
		// The daemon acknowledges a stop before the job has ended; wait for it.
		settled = func() (bool, error) {
			snapshot, err := ReadSnapshot(root)
			if err != nil {
				return false, nil
			}
			for _, job := range snapshot.Jobs {
				if job.ID == id && job.State == "stopped" {
					return true, nil
				}
			}
			return false, nil
		}
	}
	return exchange(ctx, root, commandRequest{Action: action, ProfileID: id}, settled)
}

// exchange sends one control request to the daemon running under root and waits
// for its reply. A refusal comes back as a *ControlError. When settled is given,
// an acknowledgement is not the end: the request is done once settled says so.
func exchange(ctx context.Context, root string, req commandRequest, settled func() (bool, error)) error {
	running, err := Running(root)
	if err != nil {
		return err
	}
	if !running {
		return errors.New("local collection daemon is not running")
	}
	var rt Runtime
	for {
		rt, err = ReadRuntime(root)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		live, probeErr := Running(root)
		if probeErr != nil {
			return probeErr
		}
		if !live {
			return errors.New("daemon exited before publishing runtime")
		}
	}
	if rt.InstanceID == "" || rt.PID <= 0 {
		return errors.New("invalid daemon runtime")
	}
	dir := filepath.Join(root, "daemon-control")
	if err := safeDirectory(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, randomID()+".json")
	ack := path + ".ack"
	defer os.Remove(path)
	defer os.Remove(ack)
	req.InstanceID = rt.InstanceID
	if err := writeJSON(path, req); err != nil {
		return err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var reply commandReply
			if err := ReadJSON(ack, &reply); err == nil {
				if reply.Error != "" {
					return &ControlError{Code: reply.Error, Reason: reply.Reason, Holders: reply.Holders, More: reply.More}
				}
				if settled != nil {
					done, err := settled()
					if err != nil {
						return err
					}
					if !done {
						continue
					}
				}
				return nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
}

// ClaimRequest asks the daemon to give a source to a profile. The caller states
// what it saw -- who held the source and at which revisions -- so that the
// daemon refuses, instead of acting, if any of it has changed since.
type ClaimRequest struct {
	// Profile is the profile that takes the source.
	Profile string
	// Source is the route id. When the daemon cannot use its routes file it has no
	// ids, and Source is the profile's "kind:folder" line.
	Source string
	// Holder is the profile the caller saw holding the source, "" if nobody.
	Holder string
	// RouteRev is the route's revision. TargetRev and HolderRev are the profiles'
	// revisions as the registry has them.
	RouteRev             int
	TargetRev, HolderRev string
}

// Claim asks the running daemon to give a source to a profile. The daemon
// acknowledges once it has decided, not when the hand-over is done: the previous
// holder lets go of the source first. A refusal is a *ControlError whose Code
// says why: profile_unavailable, route_unknown, route_changed, profile_changed,
// handoff_pending, invalid_source, line_busy or state_write_failed. A daemon
// that does not know claim refuses it as invalid_action.
func Claim(ctx context.Context, root string, req ClaimRequest) error {
	return exchange(ctx, root, commandRequest{Action: "claim", ProfileID: req.Profile, Source: req.Source, Holder: req.Holder, RouteRev: req.RouteRev, TargetRev: req.TargetRev, HolderRev: req.HolderRev}, nil)
}
