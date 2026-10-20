package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"cctrace/internal/atomicfile"
	"cctrace/internal/projecthash"
	"cctrace/internal/samefolder"
	"cctrace/internal/sessionlog"
)

// SessionEnd is a session the SessionEnd hook reported ended, kept as a file in
// the profile's pending directory until a flush delivers it
// (docs/planning/session-detail-analysis/plan.md §4.1(5)). The hook's parent
// writes it before anything that can wait on the sync lock: the --once child it
// starts gives up at once when a daemon holds the lock, and the daemon's next
// flush sends it instead.
type SessionEnd struct {
	SessionID string    `json:"session_id"`
	Reason    string    `json:"reason"`
	EndedAt   time.Time `json:"ended_at"`
	// TranscriptPath and CWD stay on this machine: the event carries the project
	// they name.
	TranscriptPath string `json:"transcript_path,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	// ClaudeHome is the normalised Claude home the session belongs to: the one
	// its transcript sits in (<home>/projects/<dir>/<file>.jsonl), else the one
	// the hook ran for. The homes of a profile share its pending directory, and
	// each session end is sent by its own home's Syncer, for the reason each home
	// sends only its own snapshots: the account it goes under, and whether that
	// account is excluded, are that home's.
	ClaudeHome string `json:"claude_home"`
}

// Bounds on a profile's pending session ends.
const (
	// The directory's file count. Past it the hook records no more until a
	// flush has delivered some or they have expired; nothing reads the
	// directory while sync is not running.
	maxPendingSessionEnds = 1000
	// How long one waits. One of a home no collector serves any more, or one the
	// server never accepts, would otherwise wait forever.
	pendingSessionEndTTL = 7 * 24 * time.Hour
	// A reason is a word or two of Claude Code's -- clear, logout,
	// prompt_input_exit -- and anything longer is not one.
	maxSessionEndReasonBytes = 64
)

// SessionEndDir is a profile's pending directory: beside the sync-state.json at
// statePath, so a profile's session ends live with the rest of its sync state.
func SessionEndDir(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "pending")
}

// RecordSessionEnd writes end into dir as <event id>.json, atomically, so a
// flush reading the directory meanwhile finds the whole file or none of it. It
// refuses once dir holds maxPendingSessionEnds files, counting what has not
// expired: the expired ones it deletes first, as a flush would, so a directory
// that filled while nothing flushed frees itself. Entries other than regular
// files are not this client's and are not counted.
func RecordSessionEnd(dir string, end SessionEnd) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	now, files := nowFn(), 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.Type().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) > pendingSessionEndTTL {
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil || errors.Is(err, fs.ErrNotExist) {
				continue
			}
		}
		files++
	}
	if files >= maxPendingSessionEnds {
		return fmt.Errorf("%s already holds %d files, the most it keeps; the end of session %s is not recorded", dir, files, end.SessionID)
	}
	end.EndedAt = end.EndedAt.UTC()
	// The transcript names its home outright, and wins over the hook's
	// --claude-dir, which is all there is without one.
	if home := claudeHomeOf(end.TranscriptPath); home != "" {
		end.ClaudeHome = home
	}
	end.ClaudeHome = normalizeHome(end.ClaudeHome)
	data, err := json.Marshal(end)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, sessionEndEventID(end.SessionID, end.EndedAt)+".json"), data, 0o600)
}

// claudeHomeOf is the Claude home of a main transcript path of the shape
// <home>/projects/<dir>/<file>.jsonl, and "" for any other path.
func claudeHomeOf(transcriptPath string) string {
	if !filepath.IsAbs(transcriptPath) || filepath.Ext(transcriptPath) != ".jsonl" {
		return ""
	}
	projects := filepath.Dir(filepath.Dir(transcriptPath))
	if filepath.Base(projects) != "projects" {
		return ""
	}
	return filepath.Dir(projects)
}

// sessionEndEventID is a session end's event_id: the first 32 hex digits of the
// sha256 of its session id, a NUL and its end time in RFC3339Nano. A resend
// carries the same id; a session that is resumed and ends again has two.
func sessionEndEventID(sessionID string, endedAt time.Time) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + endedAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])[:32]
}

// sessionEnds reads the profile's pending directory and returns this home's
// session ends as session_end events, oldest first. Every file older than
// pendingSessionEndTTL is deleted on the way, whichever home it is of; another
// home's are otherwise left for that home's Syncer.
//
// A session end in a repository options.collect_repository_prefixes leaves out
// is deleted unsent, as syncFile consumes such lines -- but only once the
// repository is known, as syncFile does too; until then it waits, and says so
// once (collects). One whose transcript lies outside
// this home's projects/, one that names no project, and one with a field past
// the server's cap are never sent: they wait to expire.
func (s *Syncer) sessionEnds(now time.Time) []pendingObservation {
	if s.state == nil || s.state.path == "" {
		return nil
	}
	home := normalizeHome(s.claudeDir)
	type pendingEnd struct {
		paths []string // the file, and any copy of the same end in another queue
		end   SessionEnd
	}
	var ends []pendingEnd
	byEvent := map[string]int{}
	sameHome := map[string]bool{}
	for _, dir := range s.pendingDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			// The profile's own queue not being there is as usual as a sibling's.
			if !errors.Is(err, fs.ErrNotExist) {
				s.logOnce("pending dir", "[syncer] session ends: %v", err)
			}
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			info, err := e.Info()
			if err != nil {
				continue // removed meanwhile
			}
			if now.Sub(info.ModTime()) > pendingSessionEndTTL {
				s.removeSessionEnd(path)
				continue
			}
			// Not <event id>.json: a write still in progress, or one that died.
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					s.logOnce("pending unreadable", "[syncer] session ends: %v", err)
				}
				continue
			}
			var end SessionEnd
			if json.Unmarshal(data, &end) != nil || end.SessionID == "" || end.EndedAt.IsZero() {
				s.logOnce("pending malformed", "[syncer] session ends: %s is not a session end this client wrote; it is left to expire", path)
				continue
			}
			// The home is the folder, not its spelling: a transcript reached through
			// a link, or under another case, still belongs to it.
			same, known := sameHome[end.ClaudeHome]
			if !known {
				same = end.ClaudeHome == home || samefolder.Same(end.ClaudeHome, home)
				sameHome[end.ClaudeHome] = same
			}
			if !same {
				continue
			}
			id := sessionEndEventID(end.SessionID, end.EndedAt)
			if at, dup := byEvent[id]; dup {
				ends[at].paths = append(ends[at].paths, path)
				continue
			}
			byEvent[id] = len(ends)
			ends = append(ends, pendingEnd{paths: []string{path}, end: end})
		}
	}
	sort.Slice(ends, func(i, j int) bool { return ends[i].end.EndedAt.Before(ends[j].end.EndedAt) })

	projects := filepath.Join(home, "projects") + string(filepath.Separator)
	var out []pendingObservation
	for _, pe := range ends {
		end := pe.end
		// The project is the transcript's directory, as the session's records
		// carry it, else the hook's working directory.
		var hash string
		transcript, inside := s.underProjects(home, projects, end.TranscriptPath)
		switch {
		case end.TranscriptPath == "":
			hash = projecthash.FromPath(end.CWD)
		case inside:
			hash = sessionlog.ProjectHash(home, transcript)
		default:
			s.logOnce("pending outside home", "[syncer] session ends: %s names a transcript outside %s; it is left to expire", pe.paths[0], projects)
			continue
		}
		if hash == "" {
			s.logOnce("pending project", "[syncer] session ends: %s names no project, by transcript or by directory; it is left to expire", pe.paths[0])
			continue
		}
		if len(end.SessionID) > maxObservationKeyBytes || len(hash) > maxObservationKeyBytes {
			s.logOnce("pending cap", "[syncer] session ends: %s has a session id or project past the server's %d bytes; it is left to expire", pe.paths[0], maxObservationKeyBytes)
			continue
		}
		if allowed, known := s.collects(end.CWD); !allowed {
			if known {
				for _, path := range pe.paths {
					s.removeSessionEnd(path)
				}
			} else {
				s.logOnce("pending undecided "+pe.paths[0], "[syncer] session ends: %s waits until the repository at %q can be judged against the allowlist; it is sent or consumed then, or left to expire", pe.paths[0], end.CWD)
			}
			continue
		}
		item := SessionObservationEventItem{
			SessionID:       end.SessionID,
			Agent:           "claude",
			ProjectHash:     hash,
			BillingProvider: "anthropic",
			AccountID:       s.state.ClaudeAccountAt(home, end.EndedAt),
			EventID:         sessionEndEventID(end.SessionID, end.EndedAt),
			Kind:            "session_end",
			ObservedAt:      end.EndedAt.UTC(),
			Payload:         SessionEndPayload{Reason: cutAtRune(end.Reason, maxSessionEndReasonBytes)},
		}
		paths := pe.paths
		out = append(out, pendingObservation{event: &item, size: encodedSize(item), settle: func(bool) {
			for _, path := range paths {
				s.removeSessionEnd(path)
			}
		}})
	}
	return out
}

// pendingDirs are the queues this home's session ends are read from: the
// profile's own, and those of the profiles left out of the same folder, which
// cannot send for themselves.
func (s *Syncer) pendingDirs() []string {
	dirs := []string{SessionEndDir(s.state.path)}
	for _, dir := range s.peerPending {
		if dir = filepath.Clean(dir); dir != "." && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// SetPeerPending names the pending directories of the other profiles whose
// session ends this Syncer sends as well: those of profiles that share this
// Claude folder and are left out of it, whose own Syncers do not read it. They
// are read, delivered and expired as the profile's own are.
func (s *Syncer) SetPeerPending(dirs []string) {
	s.peerPending = slices.Clone(dirs)
}

// underProjects maps a transcript path to the same file under this home, when it
// is a file of this home's projects/: the path as it is if it already lies there,
// otherwise by the folder it is in.
func (s *Syncer) underProjects(home, projects, transcript string) (string, bool) {
	if strings.HasPrefix(transcript, projects) {
		return transcript, true
	}
	if !filepath.IsAbs(transcript) {
		return "", false
	}
	rel, ok := samefolder.Relative(home, transcript)
	if !ok || !strings.HasPrefix(rel, "projects"+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(home, rel), true
}

func (s *Syncer) removeSessionEnd(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.logOnce("pending remove", "[syncer] session ends: %v", err)
	}
}
