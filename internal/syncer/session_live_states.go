package syncer

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cctrace/internal/gitctx"
	"cctrace/internal/projecthash"
	"cctrace/internal/sessionlog"
)

// Claude Code keeps one file per running process in <claudeDir>/sessions/,
// <pid>.json, saying which session the process runs and what it is doing
// (docs/planning/session-detail-analysis/plan.md §4.1(3)). It is an
// undocumented internal file, so one in a shape this client does not know is
// skipped rather than guessed at. The <pid>.<hex>.key files beside them are
// never opened.

// sessionFileStatuses are the statuses process files have been seen to hold.
var sessionFileStatuses = map[string]bool{"busy": true, "idle": true, "waiting": true, "shell": true}

// waitingKinds maps a waiting process's waitingFor to the server's waiting_kind.
var waitingKinds = map[string]string{
	"input needed":      "input",
	"permission prompt": "permission",
	"dialog open":       "dialog",
	"sandbox request":   "sandbox",
	"worker request":    "worker",
	"goal proposal":     "goal",
}

// waitingKind is the waiting_kind for a waitingFor: "" when the file has none,
// "other" for a value this client does not know.
func waitingKind(waitingFor string) string {
	if waitingFor == "" {
		return ""
	}
	if kind, ok := waitingKinds[waitingFor]; ok {
		return kind
	}
	return "other"
}

// maxSessionFileBytes is as much of a process file as is read, Claude Code's own
// limit: a file past it is cut short, does not parse, and is skipped.
const maxSessionFileBytes = 256 << 10

// sessionFile is what the client reads from a process file.
type sessionFile struct {
	PID       int64  `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	ProcStart string `json:"procStart"`
	// ProcStartFt is the Windows start token Claude Code writes in place of
	// procStart.
	ProcStartFt string `json:"procStartFt"`
	PIDDomain   string `json:"pidDomain"`
	// Kind is interactive, bg, daemon or daemon-worker; Spare marks a bg process
	// started ahead of need, not yet given a session.
	Kind       string `json:"kind"`
	Spare      bool   `json:"spare"`
	Name       string `json:"name"`
	NameSource string `json:"nameSource"`
	Status     string `json:"status"`
	WaitingFor string `json:"waitingFor"`
	// StatusUpdatedAt and UpdatedAt are in epoch milliseconds: the last status
	// change, and the last write of any kind -- a switch to another session among
	// them, which leaves statusUpdatedAt as it was.
	StatusUpdatedAt float64 `json:"statusUpdatedAt"`
	UpdatedAt       float64 `json:"updatedAt"`
}

// epochMillis is a file's epoch-milliseconds time, zero when it has none.
func epochMillis(ms float64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms)).UTC()
}

// procStartLayout is how a process file writes procStart: the process's start
// time in UTC, in asctime layout ("Sun Oct  4 15:26:48 2026") -- Claude Code
// takes it from `LC_ALL=C TZ=UTC ps -o lstart=`.
const procStartLayout = "Mon Jan _2 15:04:05 2006"

// procStartTolerance is how far a running process's start time may lie from a
// file's procStart and still be the process that wrote it: procStart is in whole
// seconds, and Linux dates a process by clock ticks from a boot time in whole
// seconds.
const procStartTolerance = 2 * time.Second

// processStartInfo is what processStart can tell about the process with a pid.
type processStartInfo struct {
	exists bool
	// at is when the process started, by the wall clock.
	at time.Time
	// id stays the same while the process lives, and another process later given
	// its pid has another.
	id int64
	// fixed says at was recorded when the process started, so no later step of
	// the wall clock moves it.
	fixed bool
}

// liveness is what this client can tell about the process that wrote a process
// file.
type liveness int

const (
	// livenessUnknown is a process this client cannot check, or cannot check
	// for sure: the file's status is sent as it is, and never gone.
	livenessUnknown liveness = iota
	livenessAlive
	livenessGone
)

// liveness checks the process key names. Never alive when it has ended, and
// never gone while it runs: what cannot be told for sure is livenessUnknown.
//
// Only a file of this client's own pid domain (localPIDDomain) is checked, and
// only one whose procStart parses. The process is gone when no process has its
// pid. The first time it is found with a start within procStartTolerance of
// procStart it is alive, and its start id is kept: from then on the id alone
// decides, alive while it is the same and gone once the pid belongs to another
// process, whatever the wall clock does meanwhile.
//
// A start that does not match the first time is a pid given to another process
// -- or, on Linux, a step of the wall clock since the process started, which
// moves the start time Linux gives. darwin records the start time at the start,
// so there it is another process and gone. On Linux it is gone only when the
// process now with the pid plainly runs another program (runsAnotherProgram), a
// sign no clock moves; else it cannot be told apart from a step and is
// livenessUnknown. A step after the file's last write would move any comparison
// of wall-clock times alike (the file's updatedAt included), so none of them is
// taken as proof of an end.
func (m *liveStateMemory) liveness(key liveStateKey, pidDomain string) liveness {
	if pidDomain == "" || pidDomain != localPIDDomain() || key.pid <= 0 || key.pid > math.MaxInt32 {
		return livenessUnknown
	}
	want, err := time.ParseInLocation(procStartLayout, key.procStart, time.UTC)
	if err != nil {
		return livenessUnknown
	}
	p, err := processStart(int(key.pid))
	switch {
	case err != nil:
		return livenessUnknown
	case !p.exists:
		return livenessGone
	}
	if id, verified := m.starts[key]; verified {
		if p.id == id {
			return livenessAlive
		}
		return livenessGone
	}
	if d := p.at.Sub(want); d >= -procStartTolerance && d <= procStartTolerance {
		m.starts[key] = p.id
		return livenessAlive
	}
	if p.fixed || runsAnotherProgram(int(key.pid)) {
		return livenessGone
	}
	return livenessUnknown
}

// liveStateKey names one process's run of a session, as the server keys a live
// state: a pid alone is reused once its process exits.
type liveStateKey struct {
	sessionID string
	pid       int64
	procStart string
}

// liveStateMemory is what finding a process gone takes from one flush to the
// next, in memory only: a heartbeat needs no persistence, and gone is said once
// per process of this collector.
type liveStateMemory struct {
	// seen is each process last read alive, or with liveness unknown, and the
	// file it was read from.
	seen map[liveStateKey]seenLiveState
	// gone holds the gone states found and not yet delivered.
	gone map[liveStateKey]goneLiveState
	// done marks a process whose gone has been delivered, or consumed by an
	// exclusion, or whose state the server refused alone.
	done map[liveStateKey]doneLiveState
	// starts holds the start id of each process verified alive (liveness).
	starts map[liveStateKey]int64
	// files holds, for each process file, the process last read from it and
	// when: a file that reads as another one than last time is a switch.
	files map[string]fileSighting
}

type seenLiveState struct {
	file string
	item SessionLiveStateItem
	// floor is the earliest status_changed_at the process may be sent with,
	// set when it was taken up again (liveStates).
	floor time.Time
}

type doneLiveState struct {
	// final is true for a process verified gone or refused, false for one whose
	// file disappeared or moved to another session: that one can come back, and
	// when a file reads as it again it is taken up afresh.
	final bool
	// at is when its gone was found.
	at time.Time
}

type fileSighting struct {
	key liveStateKey
	at  time.Time
}

type goneLiveState struct {
	item SessionLiveStateItem
	// final is true for a process verified gone; false when its file
	// disappeared or moved to another session.
	final bool
}

// liveStates reads this home's process files and returns the live states a
// flush sends: every process alive, or whose liveness cannot be told here, with
// the status its file gives, and every gone state not yet delivered. A session
// in ending -- its session end goes in the same flush -- is not reported alive.
//
// A process is gone when liveness verifies it, or when a file seen earlier by
// this process has disappeared from the directory or holds another session now.
// Each is sent once, dated by when it was found, as both its heartbeat and its
// status change (the server's contract), with the name last read. A file present
// but unreadable this time is not taken for gone: it may be one Claude Code is
// rewriting. A process that disappeared or moved away and is read again -- a
// session switched back to -- is taken up afresh; one verified gone is not.
//
// A process taken up again, or read from a file that last held another one --
// a switch, which Claude Code makes without touching statusUpdatedAt -- is
// dated by the later of statusUpdatedAt and the file's updatedAt, and at least
// after its gone could have been found (the last read of the file as another
// process, or its own gone): else the server, which dates a status change by
// status_changed_at, would file "gone to busy" before the gone it follows, and
// a second switch back would repeat the first one's event and be dropped. The
// date holds while the process stays, until statusUpdatedAt passes it.
func (s *Syncer) liveStates(now time.Time, ending map[string]bool) []pendingObservation {
	mem := &s.obs.live
	if mem.seen == nil {
		mem.seen = map[liveStateKey]seenLiveState{}
		mem.gone = map[liveStateKey]goneLiveState{}
		mem.done = map[liveStateKey]doneLiveState{}
		mem.starts = map[liveStateKey]int64{}
		mem.files = map[string]fileSighting{}
	}
	dir := filepath.Join(normalizeHome(s.claudeDir), "sessions")
	entries, err := os.ReadDir(dir)
	// A missing directory is normal -- no process has run -- and lists no file.
	// Any other failure lists nothing that can be trusted to be complete.
	listed := err == nil || errors.Is(err, fs.ErrNotExist)
	if !listed {
		s.logOnce("sessions dir", "[syncer] live states: %v; nothing is taken for gone until the directory can be read", err)
	}

	var out []pendingObservation
	present := map[string]bool{}
	readAs := map[string]liveStateKey{}
	readKeys := map[liveStateKey]bool{}
	for _, e := range entries {
		// Only a regular <pid>.json: the <pid>.<hex>.key files beside them are
		// never opened.
		if !strings.HasSuffix(e.Name(), ".json") || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		present[path] = true
		f, ok := s.readSessionFile(path)
		if !ok {
			continue
		}
		key := liveStateKey{sessionID: f.SessionID, pid: f.PID, procStart: f.ProcStart}
		readAs[path] = key
		readKeys[key] = true
		last, sighted := mem.files[path]
		mem.files[path] = fileSighting{key: key, at: now}
		retaken := sighted && last.key != key
		var bound time.Time
		if retaken {
			bound = last.at
		}
		if d, found := mem.done[key]; found {
			if d.final {
				continue
			}
			delete(mem.done, key)
			retaken, bound = true, later(bound, d.at)
		}
		if g, found := mem.gone[key]; found {
			if g.final {
				continue
			}
			delete(mem.gone, key)
			retaken, bound = true, later(bound, g.item.HeartbeatAt)
		}
		item, ok := s.liveStateItem(f, now)
		if !ok {
			continue
		}
		if mem.liveness(key, f.PIDDomain) == livenessGone {
			delete(mem.seen, key)
			mem.gone[key] = goneLiveState{item: s.goneLiveState(item, now), final: true}
			continue
		}
		floor := mem.seen[key].floor
		if retaken {
			floor = later(epochMillis(f.StatusUpdatedAt), epochMillis(f.UpdatedAt))
			// Never later than now: a file dated ahead (a damaged updatedAt, a clock
			// that has since stepped back) would otherwise pin a floor the server
			// skips as too far ahead, for as long as the key lives.
			if !floor.After(bound) || floor.After(now) {
				floor = now.UTC()
			}
		}
		if !floor.IsZero() && (item.StatusChangedAt == nil || floor.After(*item.StatusChangedAt)) {
			at := floor
			item.StatusChangedAt = &at
		}
		mem.seen[key] = seenLiveState{file: path, item: item, floor: floor}
		if ending[f.SessionID] {
			continue
		}
		out = append(out, pendingObservation{liveState: &item, size: encodedSize(item), settle: func(refused bool) {
			if refused {
				mem.done[key] = doneLiveState{final: true, at: now}
				delete(mem.seen, key) // nor its gone, which the server would refuse alike
			}
		}})
	}
	if listed {
		for key, seen := range mem.seen {
			if k, read := readAs[seen.file]; !present[seen.file] || (read && k != key) {
				delete(mem.seen, key)
				mem.gone[key] = goneLiveState{item: s.goneLiveState(seen.item, now)}
			}
		}
		// Forget what no file says any more, so the memory stays the size of
		// the directory.
		for key := range mem.done {
			if !readKeys[key] {
				delete(mem.done, key)
			}
		}
		for key := range mem.starts {
			if _, seen := mem.seen[key]; !seen && !readKeys[key] {
				delete(mem.starts, key)
			}
		}
		for path := range mem.files {
			if !present[path] {
				delete(mem.files, path)
			}
		}
	}

	keys := make([]liveStateKey, 0, len(mem.gone))
	for key := range mem.gone {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].sessionID != keys[j].sessionID {
			return keys[i].sessionID < keys[j].sessionID
		}
		return keys[i].pid < keys[j].pid
	})
	for _, key := range keys {
		g := mem.gone[key]
		out = append(out, pendingObservation{liveState: &g.item, size: encodedSize(g.item), settle: func(bool) {
			mem.done[key] = doneLiveState{final: g.final, at: g.item.HeartbeatAt}
			delete(mem.gone, key)
		}})
	}
	return out
}

// readSessionFile reads one process file. One that cannot be read, does not
// parse, is not a session's -- Claude Code's daemon and its workers, a spare --
// lacks what a live state is keyed and described by, or holds a status this
// client does not know is skipped, each kind of problem logged once.
func (s *Syncer) readSessionFile(path string) (sessionFile, bool) {
	var f sessionFile
	file, err := os.Open(path)
	if err != nil {
		// Gone between the listing and the read: its process has ended.
		if !errors.Is(err, fs.ErrNotExist) {
			s.logOnce("session file unreadable", "[syncer] live states: %v; process files that cannot be read are skipped", err)
		}
		return f, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSessionFileBytes))
	file.Close()
	if err != nil {
		s.logOnce("session file unreadable", "[syncer] live states: %v; process files that cannot be read are skipped", err)
		return f, false
	}
	if err := json.Unmarshal(data, &f); err != nil {
		s.logOnce("session file malformed", "[syncer] live states: %s does not parse (%v); such process files are skipped", path, err)
		return f, false
	}
	switch {
	case f.Kind == "daemon" || f.Kind == "daemon-worker" || f.Spare:
		what := f.Kind
		if f.Spare {
			what = "spare"
		}
		s.logOnce("session file kind", "[syncer] live states: %s is Claude Code's %s process, not a session's; such process files are skipped", path, what)
		return f, false
	case f.ProcStart == "" && (f.ProcStartFt != "" || strings.HasPrefix(f.PIDDomain, "win32:")):
		s.logOnce("session file windows", "[syncer] live states: %s has no procStart: Claude Code on Windows writes procStartFt in its place, so no live state is sent from Windows", path)
		return f, false
	case f.SessionID == "" || f.PID <= 0 || f.ProcStart == "" || f.Status == "":
		s.logOnce("session file incomplete", "[syncer] live states: %s lacks sessionId, pid, procStart or status; such process files are skipped", path)
		return f, false
	case !sessionFileStatuses[f.Status]:
		s.logOnce("session file status", "[syncer] live states: %s holds status %q, which this client does not know; such process files are skipped", path, cutAtRune(f.Status, 64))
		return f, false
	}
	return f, true
}

// liveStateItem is the live state a process file describes as of now, or false
// when it is not sent: neither the session's transcript nor its directory names
// a project -- the server requires one --, a field is past the server's cap, or
// the repository is one options.collect_repository_prefixes leaves out, which
// sends nothing of it. None of that settles the process: the next flush asks
// again, and a project named by a working directory too long for the server
// waits for the session's transcript to name it.
func (s *Syncer) liveStateItem(f sessionFile, now time.Time) (SessionLiveStateItem, bool) {
	hash := s.transcriptProjectHash(f.SessionID)
	if hash == "" {
		hash = projecthash.FromPath(f.CWD)
	}
	if hash == "" {
		s.logOnce("session file project", "[syncer] live states: session %s has neither a transcript nor a working directory to name its project; its state is not sent", f.SessionID)
		return SessionLiveStateItem{}, false
	}
	for _, c := range []struct {
		name, value string
		max         int
	}{
		{"session_id", f.SessionID, maxObservationKeyBytes},
		{"proc_start", f.ProcStart, maxObservationKeyBytes},
		{"project_hash", hash, maxObservationKeyBytes},
		{"name_source", f.NameSource, maxObservationTagBytes},
	} {
		if len(c.value) > c.max {
			s.logOnce("live state cap "+c.name, "[syncer] live states: a %s of %d bytes is past the server's %d; such states are not sent", c.name, len(c.value), c.max)
			return SessionLiveStateItem{}, false
		}
	}
	if allowed, _ := s.collects(f.CWD); !allowed {
		return SessionLiveStateItem{}, false
	}
	item := SessionLiveStateItem{
		SessionID:       f.SessionID,
		Agent:           "claude",
		ProjectHash:     hash,
		BillingProvider: "anthropic",
		AccountID:       s.state.ClaudeAccountAt(normalizeHome(s.claudeDir), now),
		PID:             f.PID,
		ProcStart:       f.ProcStart,
		Status:          f.Status,
		StatusSource:    "claude_process_file",
		WaitingKind:     waitingKind(f.WaitingFor),
		Name:            f.Name,
		NameSource:      f.NameSource,
		HeartbeatAt:     now.UTC(),
	}
	if at := epochMillis(f.StatusUpdatedAt); !at.IsZero() {
		item.StatusChangedAt = &at
	}
	return item, true
}

// goneLiveState is the gone state of a process last described by last, found
// gone at now: dated by now as both its heartbeat and its status change, and
// attributed to the account of now.
func (s *Syncer) goneLiveState(last SessionLiveStateItem, now time.Time) SessionLiveStateItem {
	at := now.UTC()
	last.Status = "gone"
	last.StatusSource = "process_liveness"
	last.WaitingKind = ""
	last.StatusChangedAt = &at
	last.HeartbeatAt = at
	last.AccountID = s.state.ClaudeAccountAt(normalizeHome(s.claudeDir), now)
	return last
}

// transcriptProjectHash is the project directory of a session's main transcript
// under this home, as the session's records carry it (sessionlog.ProjectHash),
// or "" when there is no such file.
func (s *Syncer) transcriptProjectHash(sessionID string) string {
	// The id goes into a pattern; one that could match more than a file name is
	// not a session id.
	if sessionID == "" || strings.ContainsAny(sessionID, `*?[\/`) {
		return ""
	}
	home := normalizeHome(s.claudeDir)
	matches, _ := filepath.Glob(filepath.Join(home, "projects", "*", sessionID+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	return sessionlog.ProjectHash(home, matches[0])
}

// collects reports whether options.collect_repository_prefixes lets the
// repository at cwd be collected, as syncFile decides for records, and whether
// its repository is known at all: a cwd that names none cannot be judged, and
// neither can one whose lookup settled nothing -- git gave no answer, or the
// repository there is in question (projectMeta.hold). Such a lookup carries a
// fallback identity, or one that may not judge yet, and is not a verdict.
func (s *Syncer) collects(cwd string) (allowed, known bool) {
	if len(s.collectPrefixes) == 0 {
		return true, true
	}
	if cwd == "" {
		return false, false
	}
	m := s.resolveProjectMeta(cwd)
	if m.hold != "" {
		return false, false
	}
	return gitctx.AllowsRepository(m.repositoryID, s.collectPrefixes), m.repositoryID != ""
}
