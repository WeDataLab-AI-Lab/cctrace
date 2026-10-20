package syncer

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"cctrace/internal/codexlog"
	"cctrace/internal/sessionlog"
)

// A Codex session's snapshot carries its turns: when each started and ended, how
// it ended, and the model, effort and context window it ran with
// (docs/planning/session-detail-analysis/plan.md §4.1(6)). They come from the
// rollout's lifecycle lines (codexlog.TurnEvent) and travel in the machinery a
// Claude transcript's metadata does: kept on the rollout's FileState in the
// Codex syncer's state, rebuilt once from the part sync collected, backfilled by
// re-enrichment, and sent by the one flush of the process. Codex gets no
// session_records rows from them.
//
// None of it is text -- ids, times, numbers and the names Codex gives a model,
// an effort and a collaboration mode -- so no redaction applies to it
// (redactSnapshot). Codex writes no title lines, so a Codex snapshot's titles
// are "", the snapshot contract's "no such line".

// SnapshotCodexTurn is one Codex turn, merged by turn id from every lifecycle
// line that names it. Times are UTC. A field no line gave is left out, except
// end, which is "" while the turn is open.
type SnapshotCodexTurn struct {
	TurnID     string `json:"turn_id"`
	RootTurnID string `json:"root_turn_id,omitempty"`
	// StartedAt is task_started's started_at. A task_started without one is
	// dated by its own line's timestamp, and StartedAtSource is then "line". A
	// turn whose task_started was not read takes another lifecycle line's
	// started_at.
	StartedAt       time.Time `json:"started_at,omitzero"`
	StartedAtSource string    `json:"started_at_source,omitempty"`
	EndedAt         time.Time `json:"ended_at,omitzero"`
	EndedAtSource   string    `json:"-"`
	TimestampsExact bool      `json:"timestamps_exact,omitempty"`
	// End is "complete", "aborted", or "error" for a task_complete that carried
	// an error, and "" while the turn is open. CodexErrorInfo is that error's
	// codex_error_info, an enum such as context_window_exceeded.
	End                   string `json:"end"`
	AbortReason           string `json:"abort_reason,omitempty"`
	CodexErrorInfo        string `json:"codex_error_info,omitempty"`
	DurationMs            *int64 `json:"duration_ms,omitempty"`
	TimeToFirstTokenMs    *int64 `json:"time_to_first_token_ms,omitempty"`
	ModelContextWindow    *int64 `json:"model_context_window,omitempty"`
	CollaborationModeKind string `json:"collaboration_mode_kind,omitempty"`
	Model                 string `json:"model,omitempty"`
	Effort                string `json:"effort,omitempty"`
	// startRank is how far StartedAt is trusted, as the line it came from says;
	// a turn read back from the state ranks by its fields (rank).
	startRank int
}

// How far a turn's start is trusted: a task_started's own started_at over
// another lifecycle line's, and either over a line's timestamp.
const (
	startFromEvent = 1 + iota
	startFromTaskStartedPayload
	startFromTaskStarted
)

func (t SnapshotCodexTurn) rank() int {
	switch {
	case t.startRank > 0:
		return t.startRank
	case t.StartedAt.IsZero():
		return 0
	case t.StartedAtSource == "line":
		return startFromTaskStarted
	case t.TimestampsExact:
		return startFromTaskStarted
	case t.StartedAtSource == "payload":
		return startFromTaskStartedPayload
	case t.StartedAtSource == "event":
		return startFromEvent
	}
	return startFromEvent
}

// maxShortNameBytes bounds a name a snapshot carries for Codex: a model, an
// effort, a collaboration mode, an abort reason, an error kind.
const maxShortNameBytes = 64

// shortName is s when it is such a name -- letters, digits and -_.:/, at most
// maxShortNameBytes -- and "" otherwise. Codex writes every one of them that
// way (all but one of 13,943 local models); anything else is not a name, and
// a snapshot carries no free text.
func shortName(s string) string {
	if len(s) > maxShortNameBytes {
		return ""
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', strings.IndexByte("-_.:/", c) >= 0:
		default:
			return ""
		}
	}
	return s
}

// codexTurnOf is what one lifecycle line says about its turn. Turn ids are cut
// at maxSnapshotFieldBytes, as a snapshot's other identifiers are; names that
// are not short names are left out.
func codexTurnOf(e codexlog.TurnEvent) SnapshotCodexTurn {
	t := SnapshotCodexTurn{
		TurnID:                cutAtRune(e.TurnID, maxSnapshotFieldBytes),
		RootTurnID:            cutAtRune(e.RootTurnID, maxSnapshotFieldBytes),
		DurationMs:            e.DurationMs,
		TimeToFirstTokenMs:    e.TimeToFirstTokenMs,
		ModelContextWindow:    e.ModelContextWindow,
		CollaborationModeKind: shortName(e.CollaborationModeKind),
		Model:                 shortName(e.Model),
		Effort:                shortName(e.Effort),
	}
	switch {
	case e.Kind == "task_started" && !e.At.IsZero():
		t.StartedAt, t.TimestampsExact, t.startRank = e.At.UTC(), true, startFromTaskStarted
	case e.Kind == "task_started" && !e.StartedAt.IsZero():
		t.StartedAt, t.StartedAtSource, t.startRank = e.StartedAt, "payload", startFromTaskStartedPayload
	case !e.StartedAt.IsZero():
		t.StartedAt, t.StartedAtSource, t.startRank = e.StartedAt, "event", startFromEvent
	}
	switch e.Kind {
	case "task_complete":
		t.End = "complete"
		if !e.At.IsZero() {
			t.EndedAt, t.EndedAtSource = e.At.UTC(), "line"
		} else if !e.CompletedAt.IsZero() {
			t.EndedAt, t.EndedAtSource = e.CompletedAt, "payload"
		}
		if e.Error {
			t.End, t.CodexErrorInfo = "error", shortName(e.ErrorInfo)
		}
	case "turn_aborted":
		t.End, t.AbortReason = "aborted", shortName(e.Reason)
		if !e.At.IsZero() {
			t.EndedAt, t.EndedAtSource = e.At.UTC(), "line"
		} else if !e.CompletedAt.IsZero() {
			t.EndedAt, t.EndedAtSource = e.CompletedAt, "payload"
		}
	}
	return t
}

// update takes each field o gives: o comes from a later line, or from a later
// reading of the same ones. A start is taken when it is trusted further than
// the one held; an ending is taken whole.
func (t *SnapshotCodexTurn) update(o SnapshotCodexTurn) {
	if o.RootTurnID != "" {
		t.RootTurnID = o.RootTurnID
	}
	if r := o.rank(); r > t.rank() {
		t.StartedAt, t.StartedAtSource, t.TimestampsExact, t.startRank = o.StartedAt, o.StartedAtSource, o.TimestampsExact, r
	}
	if o.End != "" {
		t.End, t.EndedAt, t.EndedAtSource, t.AbortReason, t.CodexErrorInfo = o.End, o.EndedAt, o.EndedAtSource, o.AbortReason, o.CodexErrorInfo
		t.TimestampsExact = t.TimestampsExact && o.EndedAtSource == "line"
	}
	if o.DurationMs != nil {
		t.DurationMs = o.DurationMs
	}
	if o.TimeToFirstTokenMs != nil {
		t.TimeToFirstTokenMs = o.TimeToFirstTokenMs
	}
	if o.ModelContextWindow != nil {
		t.ModelContextWindow = o.ModelContextWindow
	}
	if o.CollaborationModeKind != "" {
		t.CollaborationModeKind = o.CollaborationModeKind
	}
	if o.Model != "" {
		t.Model = o.Model
	}
	if o.Effort != "" {
		t.Effort = o.Effort
	}
}

// addCodexTurns merges turns into CodexTurns by turn id, orders them by start
// and keeps the newest maxSnapshotCodexTurns, naming codex_turns in Truncated
// when it cuts. A turn started in one pass and ended in a later one is one
// entry. Once the list has been cut, a line about a turn it does not hold that
// gives no start -- the ending of a turn the cut took, read late -- is dropped:
// taken in, it would sort as the newest turn and push a real one out.
func (s *SessionSnapshot) addCodexTurns(turns []SnapshotCodexTurn) {
	if len(turns) == 0 {
		return
	}
	cut := slices.Contains(s.Truncated, "codex_turns")
	for _, t := range turns {
		switch i := slices.IndexFunc(s.CodexTurns, func(u SnapshotCodexTurn) bool { return u.TurnID == t.TurnID }); {
		case i >= 0:
			s.CodexTurns[i].update(t)
		case cut && t.StartedAt.IsZero():
		default:
			s.CodexTurns = append(s.CodexTurns, t)
		}
	}
	orderCodexTurns(s.CodexTurns)
	if over := len(s.CodexTurns) - maxSnapshotCodexTurns; over > 0 {
		s.CodexTurns = slices.Delete(s.CodexTurns, 0, over)
		s.markTruncated("codex_turns")
	}
}

// orderCodexTurns sorts turns by start, oldest first. A turn no line gave a
// start stays right behind the turn it was first seen after, so turns without
// one keep the order they were first seen in.
func orderCodexTurns(turns []SnapshotCodexTurn) {
	key := make(map[string]time.Time, len(turns))
	var last time.Time
	for _, t := range turns {
		if !t.StartedAt.IsZero() {
			last = t.StartedAt
		}
		key[t.TurnID] = last
	}
	slices.SortStableFunc(turns, func(a, b SnapshotCodexTurn) int { return key[a.TurnID].Compare(key[b.TurnID]) })
}

// foldCodexTurns merges the turns of the events whose lines start in [from, to)
// and returns the newest line timestamp among them.
func (s *SessionSnapshot) foldCodexTurns(events []codexlog.TurnEvent, from, to int64) time.Time {
	var newest time.Time
	var turns []SnapshotCodexTurn
	for _, e := range events {
		if e.Offset < from || e.Offset >= to {
			continue
		}
		newest = later(newest, e.At)
		turns = append(turns, codexTurnOf(e))
	}
	s.addCodexTurns(turns)
	return newest
}

// AdvanceCodexSnapshot folds the turn lines a Codex sync pass read into the
// rollout's snapshot. The Codex syncer calls it where it commits the pass's
// offset, before the Save, so the snapshot and the offset are stored together,
// and only for a repository the allowlist lets it collect. offset is where the
// pass began and newOffset where it ends: a line past a newOffset held back is
// left for the pass that commits it. projectHash is the one the rollout's
// records carry.
//
// As for a Claude transcript (advanceSessionSnapshot), a rollout sync collected
// part of before it had a snapshot is first rebuilt from that part,
// [collectedStart, offset), once. The rebuild reads the file from its start, so
// a subagent fork's copied history is gated as the scan gates it, and keeps the
// lines of that part alone.
func AdvanceCodexSnapshot(ctx context.Context, fs *FileState, path string, offset, newOffset int64, projectHash string, turns []codexlog.TurnEvent) {
	if fs == nil {
		return
	}
	advanceSnapshot(ctx, fs, path, offset, projectHash, false,
		func(from, to int64) (SessionSnapshot, time.Time, error) {
			rebuilt := newSessionSnapshot()
			events, err := codexlog.ScanTurns(ctx, path, codexlog.SessionIDFromPath(path))
			if err != nil {
				return rebuilt, time.Time{}, err
			}
			newest := rebuilt.foldCodexTurns(events, from, to)
			return rebuilt, newest, nil
		},
		func(next *SessionSnapshot) time.Time { return next.foldCodexTurns(turns, offset, newOffset) })
}

// BackfillCodexSnapshot builds a Codex rollout's snapshot during re-enrichment
// from the turn lines in the part sync collected ([collectedStart, the stored
// offset)), so a historical session gets its turns without waiting for new
// lines. turns are a scan's from the start of the file. Unlike a Claude backfill
// it withholds nothing: a turn holds no content. It reports whether the state
// changed, and whether the snapshot did.
func BackfillCodexSnapshot(fs *FileState, projectHash string, turns []codexlog.TurnEvent) (stateChanged, snapshotChanged bool) {
	if fs == nil {
		return false, false
	}
	built := newSessionSnapshot()
	newest := built.foldCodexTurns(turns, collectedStart(fs), fs.Offset)
	return backfillSnapshot(fs, projectHash, built, newest, false)
}

// codexSnapshotIdle is how long a rollout's snapshot stays in the Codex state
// once it has been sent and the rollout has stopped growing.
const codexSnapshotIdle = 7 * 24 * time.Hour

// pruneCodexSnapshots drops from the Codex state the snapshots already sent of
// rollouts that have not grown for codexSnapshotIdle, and reports whether it
// dropped any. Up to 150 turns each, they would otherwise stay in a state file
// rewritten whole on every save, for every rollout ever synced. Should the
// rollout grow again, its snapshot is rebuilt from the part sync collected, as
// for one that never had one (advanceSnapshot), and sent again.
func (s *Syncer) pruneCodexSnapshots(now time.Time) bool {
	if s.codexState == nil {
		return false
	}
	pruned := false
	for _, fs := range s.codexState.Files {
		if fs == nil || fs.SessionSnapshot == nil || fs.SessionSnapshot.dirty() || now.Sub(fs.LastSynced) <= codexSnapshotIdle {
			continue
		}
		fs.SessionSnapshot = nil
		pruned = true
	}
	return pruned
}

// ServeCodexSnapshots has FlushObservations send, with this Claude home's
// observations, the dirty Codex snapshots state holds for rollouts under homes:
// the Codex syncer's state, and the Codex homes it reads. One flush per process
// sends every kind of item.
func (s *Syncer) ServeCodexSnapshots(state *State, homes []string) {
	s.codexState = state
	s.codexHomes = nil
	for _, home := range homes {
		s.codexHomes = append(s.codexHomes, normalizeHome(home))
	}
}

// LeaveClaudeHome says another profile reads this Syncer's Claude home. That
// profile sends the home's live states, session ends and snapshots, under its
// own filters, so FlushObservations then sends only the heartbeat and the Codex
// snapshots this Syncer serves (ServeCodexSnapshots), and leaves the home's
// state alone.
func (s *Syncer) LeaveClaudeHome() { s.claudeLeft = true }

// codexHomeOf is the served Codex home a rollout lives under -- the longest
// when homes nest, as the Codex syncer attributes the rollout's records
// (CodexSyncer.homeForFile) -- or "" when it lives under none.
func (s *Syncer) codexHomeOf(path string) string {
	best := ""
	for _, home := range s.codexHomes {
		if strings.HasPrefix(path, home+string(filepath.Separator)) && len(home) > len(best) {
			best = home
		}
	}
	return best
}

// dirtyCodexSnapshots lists, in path order, the dirty snapshots of the Codex
// rollouts under the homes this process serves (ServeCodexSnapshots). The homes
// share one state file and each logs in on its own, so a snapshot is
// attributed as its rollout's records are: provider openai, and the account
// observed for its own home, read at the time of its newest turn line. A
// rollout under no served home is left for the process that serves it, and one
// whose name carries no session id, or whose project is past the server's cap,
// is not sent.
func (s *Syncer) dirtyCodexSnapshots(f *observationFlush) []pendingObservation {
	if s.codexState == nil {
		return nil
	}
	var paths []string
	for path, fs := range s.codexState.Files {
		if fs != nil && fs.SessionSnapshot != nil && fs.SessionSnapshot.dirty() && s.codexHomeOf(path) != "" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	pending := make([]pendingObservation, 0, len(paths))
	for _, path := range paths {
		sessionID := codexlog.SessionIDFromPath(path)
		if sessionID == "" {
			continue
		}
		st := s.codexState.Files[path].SessionSnapshot
		// The project names the rollout's working directory, which no cap
		// bounds. Past the server's cap the item is refused whole, and a few
		// such refusals would park every flush, so it is not sent.
		if len(st.ProjectHash) > maxObservationKeyBytes {
			s.logOnce("codex snapshot cap", "[syncer] session observations: a Codex project of %d bytes is past the server's %d; such snapshots are not sent", len(st.ProjectHash), maxObservationKeyBytes)
			continue
		}
		item := SessionSnapshotItem{
			SessionID:          sessionID,
			Agent:              "codex",
			ProjectHash:        st.ProjectHash,
			BillingProvider:    "openai",
			AccountID:          s.codexState.CodexAccountAt(s.codexHomeOf(path), st.attributedAt()),
			ObservedAt:         st.ObservedAt,
			CollectVersion:     sessionlog.CollectVersion,
			Snapshot:           st.Snapshot,
			titlesReadRedacted: st.TitlesReadRedacted,
		}
		pending = append(pending, snapshotObservation(st, item, &f.codexMarked))
	}
	return pending
}
