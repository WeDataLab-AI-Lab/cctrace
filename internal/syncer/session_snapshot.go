package syncer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"cctrace/internal/jsonlscan"
	"cctrace/internal/sessionlog"
)

// sessionSnapshotSchema is written into every snapshot, so derived code can tell
// this shape from a later one.
const sessionSnapshotSchema = 1

// Caps on one snapshot. The server stores a snapshot whole and refuses one over
// 64 KiB, and the client keeps one in sync-state.json for every active transcript.
const (
	// A title or name is read as one line; past 1 KiB it is text, not a title.
	maxSnapshotTitleBytes = 1 << 10
	// A PR URL, a repository or a model id is an identifier; past 512 B it is not one.
	maxSnapshotFieldBytes = 512
	// The models one run used; 32 is past any real mix and bounds one entry.
	maxSnapshotModels = 32
	// One entry per run of the session (startTime); 50 runs bound a long-resumed session.
	maxSnapshotCostStates = 50
	// The PRs one session opened; 100 bounds a runaway loop well under the server's cap.
	maxSnapshotPRLinks = 100
	// The sessions this one continued in; 20 bounds a runaway chain.
	maxSnapshotContinuedIn = 20
	// The turns of a Codex session, newest kept. Local rollouts hold 2 at the
	// median and 52 at p99; 3 of 3,051 hold more than 150.
	maxSnapshotCodexTurns = 150
	// Encoded size kept under the server's 64 KiB, with room for redaction markers.
	maxSnapshotBytes = 60 << 10
)

// SessionSnapshot is what a main transcript's metadata lines say about its
// session (docs/planning/session-detail-analysis/plan.md §4.1(2)): its titles, the
// cost each run of it ended at, the PRs it opened, the sessions it continued in.
// Those lines carry neither a uuid nor a timestamp, so session_records keeps them
// without their payload (sessionlog.Record holds it only in RawLine). This is
// where the payload goes instead, built from typed fields -- never by echoing a
// line -- and sent as the snapshot of a session-observations item.
//
// The client reads a file in order, so it knows the latest value of each field.
// A title is the last line's value, an empty one included. cost_states holds one
// entry per startTime, the last line for it winning. pr_links and continued_in
// keep the first sighting of each.
//
// The titles are pointers because a snapshot can leave them out, which is not the
// same as having none. A default re-enrichment backfill leaves them out -- they
// are content, which it does not send (see backfillSessionSnapshot) -- and their
// keys are then absent. A snapshot built from new lines always carries them, ""
// when the session has none. An absent list is an empty one.
type SessionSnapshot struct {
	Schema      int                 `json:"schema"`
	AITitle     *string             `json:"ai_title,omitempty"`
	CustomTitle *string             `json:"custom_title,omitempty"`
	AgentName   *string             `json:"agent_name,omitempty"`
	CostStates  []SnapshotCostState `json:"cost_states,omitempty"`
	PRLinks     []SnapshotPRLink    `json:"pr_links,omitempty"`
	ContinuedIn []string            `json:"continued_in,omitempty"`
	// CodexTurns is a Codex session's turns as its rollout's lifecycle lines
	// describe them (SnapshotCodexTurn), oldest start first. Absent on Claude
	// snapshots.
	CodexTurns []SnapshotCodexTurn `json:"codex_turns,omitempty"`
	// Git counts the files a Claude session edited and how many of them are
	// still uncommitted (SnapshotGit). Absent until they are first counted, and
	// on Codex snapshots.
	Git     *SnapshotGit     `json:"git,omitempty"`
	Commits *SnapshotCommits `json:"commits,omitempty"`
	// Truncated names the lists above that reached their cap, so derived code can
	// say "incomplete" rather than read a cut list as the whole of it. "git"
	// says the counts cover the edited paths kept up to their cap.
	Truncated []string `json:"truncated,omitempty"`
}

// SnapshotCostState is where one run of a session (one startTime) last stood.
// Models are the modelUsage keys, sorted; the per-model numbers stay local.
type SnapshotCostState struct {
	StartTimeMs         int64    `json:"start_time_ms"`
	TotalCostUSD        float64  `json:"total_cost_usd"`
	LinesAdded          int64    `json:"lines_added"`
	LinesRemoved        int64    `json:"lines_removed"`
	Models              []string `json:"models"`
	HasUnknownModelCost bool     `json:"has_unknown_model_cost"`
}

// SnapshotPRLink is a PR the session linked, as first seen.
type SnapshotPRLink struct {
	URL         string    `json:"url"`
	Number      int64     `json:"number"`
	Repository  string    `json:"repository"`
	FirstSeenAt time.Time `json:"first_seen_at,omitzero"`
}

// snapshotLineTypes are the line types a snapshot is built from. All 56,272 such
// lines on a local machine sat in main transcripts, none in subagents/ files, and
// each one's sessionId was its file's name.
var snapshotLineTypes = map[string]bool{
	"ai-title": true, "custom-title": true, "agent-name": true,
	"cost-state": true, "pr-link": true, "continued-in": true,
}

// truncatableLists is the order Truncated keeps, so two snapshots that hit the
// same caps in a different order still encode alike.
var truncatableLists = []string{"cost_states", "pr_links", "continued_in", "codex_turns", "git", "commits"}

// snapshotLine is every field of the six line types that a snapshot keeps, so a
// line is decoded once whatever its type. A field in a shape it does not expect
// fails the line, which then adds nothing: a format change shows up as missing
// data rather than as wrong data.
type snapshotLine struct {
	Type                 string                     `json:"type"`
	Timestamp            time.Time                  `json:"timestamp"`
	AITitle              string                     `json:"aiTitle"`
	CustomTitle          string                     `json:"customTitle"`
	AgentName            string                     `json:"agentName"`
	StartTime            float64                    `json:"startTime"`
	TotalCostUSD         float64                    `json:"totalCostUSD"`
	TotalLinesAdded      float64                    `json:"totalLinesAdded"`
	TotalLinesRemoved    float64                    `json:"totalLinesRemoved"`
	ModelUsage           map[string]json.RawMessage `json:"modelUsage"`
	HasUnknownModelCost  bool                       `json:"hasUnknownModelCost"`
	PRNumber             int64                      `json:"prNumber"`
	PRURL                string                     `json:"prUrl"`
	PRRepository         string                     `json:"prRepository"`
	ContinuedInSessionID string                     `json:"continuedInSessionId"`
}

// newSessionSnapshot is the snapshot of a session whose lines have said nothing
// yet. It carries the titles, as "".
func newSessionSnapshot() SessionSnapshot {
	return SessionSnapshot{Schema: sessionSnapshotSchema, AITitle: new(string), CustomTitle: new(string), AgentName: new(string)}
}

// apply folds one metadata line into the snapshot, in file order, and returns
// the line's timestamp (zero for the types that carry none).
func (s *SessionSnapshot) apply(raw []byte) time.Time {
	var l snapshotLine
	if json.Unmarshal(raw, &l) != nil {
		return time.Time{}
	}
	switch l.Type {
	case "ai-title":
		s.AITitle = snapshotTitle(l.AITitle)
	case "custom-title":
		s.CustomTitle = snapshotTitle(l.CustomTitle)
	case "agent-name":
		s.AgentName = snapshotTitle(l.AgentName)
	case "cost-state":
		models := make([]string, 0, len(l.ModelUsage))
		for model := range l.ModelUsage {
			models = append(models, cutAtRune(model, maxSnapshotFieldBytes))
		}
		sort.Strings(models)
		// Two ids that differ only past the cut are one id once cut.
		models = slices.Compact(models)
		if len(models) > maxSnapshotModels {
			models = models[:maxSnapshotModels]
		}
		s.addCostState(SnapshotCostState{
			StartTimeMs:         int64(l.StartTime),
			TotalCostUSD:        l.TotalCostUSD,
			LinesAdded:          int64(l.TotalLinesAdded),
			LinesRemoved:        int64(l.TotalLinesRemoved),
			Models:              models,
			HasUnknownModelCost: l.HasUnknownModelCost,
		})
	case "pr-link":
		s.addPRLink(SnapshotPRLink{
			URL:         cutAtRune(l.PRURL, maxSnapshotFieldBytes),
			Number:      l.PRNumber,
			Repository:  cutAtRune(l.PRRepository, maxSnapshotFieldBytes),
			FirstSeenAt: l.Timestamp,
		})
	case "continued-in":
		s.addContinuedIn(l.ContinuedInSessionID)
	default:
		return time.Time{}
	}
	return l.Timestamp
}

// snapshotTitle cuts a title or name to maxSnapshotTitleBytes.
func snapshotTitle(title string) *string {
	title = cutAtRune(title, maxSnapshotTitleBytes)
	return &title
}

// cutAtRune cuts s to at most limit bytes on a rune boundary, so what is kept is
// still valid UTF-8.
func cutAtRune(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// addCostState keeps one entry per startTime, sorted by it: the last line for a
// startTime replaces the entry, and past the cap the oldest run goes.
func (s *SessionSnapshot) addCostState(c SnapshotCostState) {
	i := sort.Search(len(s.CostStates), func(i int) bool { return s.CostStates[i].StartTimeMs >= c.StartTimeMs })
	if i < len(s.CostStates) && s.CostStates[i].StartTimeMs == c.StartTimeMs {
		s.CostStates[i] = c
		return
	}
	s.CostStates = slices.Insert(s.CostStates, i, c)
	if len(s.CostStates) > maxSnapshotCostStates {
		s.CostStates = slices.Delete(s.CostStates, 0, len(s.CostStates)-maxSnapshotCostStates)
		s.markTruncated("cost_states")
	}
}

// addPRLink keeps the first sighting of each PR URL. A line without one names
// no PR.
func (s *SessionSnapshot) addPRLink(p SnapshotPRLink) {
	if p.URL == "" || slices.ContainsFunc(s.PRLinks, func(q SnapshotPRLink) bool { return q.URL == p.URL }) {
		return
	}
	if len(s.PRLinks) >= maxSnapshotPRLinks {
		s.markTruncated("pr_links")
		return
	}
	s.PRLinks = append(s.PRLinks, p)
}

// addContinuedIn keeps each session this one continued in, first sighting first.
func (s *SessionSnapshot) addContinuedIn(sessionID string) {
	if sessionID == "" || slices.Contains(s.ContinuedIn, sessionID) {
		return
	}
	if len(s.ContinuedIn) >= maxSnapshotContinuedIn {
		s.markTruncated("continued_in")
		return
	}
	s.ContinuedIn = append(s.ContinuedIn, sessionID)
}

func (s *SessionSnapshot) markTruncated(list string) {
	if slices.Contains(s.Truncated, list) {
		return
	}
	s.Truncated = append(s.Truncated, list)
	slices.SortFunc(s.Truncated, func(a, b string) int {
		return slices.Index(truncatableLists, a) - slices.Index(truncatableLists, b)
	})
}

// carriesTitles reports whether the snapshot holds its titles, possibly empty,
// rather than leaving them out.
func (s *SessionSnapshot) carriesTitles() bool {
	return s.AITitle != nil && s.CustomTitle != nil && s.AgentName != nil
}

// empty reports whether the snapshot says nothing about its session.
func (s *SessionSnapshot) empty() bool {
	for _, title := range []*string{s.AITitle, s.CustomTitle, s.AgentName} {
		if title != nil && *title != "" {
			return false
		}
	}
	return len(s.CostStates) == 0 && len(s.PRLinks) == 0 && len(s.ContinuedIn) == 0 &&
		len(s.CodexTurns) == 0 && s.Git == nil && s.Commits == nil
}

// merge folds another snapshot of the same session into s, its lists as if
// their lines came after s's. Its titles are taken only where s carries none:
// titles s already carries came from sync reading the file in order, and o
// cannot know better.
func (s *SessionSnapshot) merge(o SessionSnapshot) {
	s.Schema = sessionSnapshotSchema
	if !s.carriesTitles() && o.carriesTitles() {
		s.AITitle, s.CustomTitle, s.AgentName = o.AITitle, o.CustomTitle, o.AgentName
	}
	for _, c := range o.CostStates {
		s.addCostState(c)
	}
	for _, p := range o.PRLinks {
		s.addPRLink(p)
	}
	for _, id := range o.ContinuedIn {
		s.addContinuedIn(id)
	}
	s.addCodexTurns(o.CodexTurns)
	s.mergeCommits(o.Commits)
	for _, list := range o.Truncated {
		s.markTruncated(list)
	}
}

// clone copies the lists, so changing the copy cannot reach the snapshot it came
// from. The titles, each entry's Models, each turn's numbers and Git are
// shared: they are replaced, never written through.
func (s SessionSnapshot) clone() SessionSnapshot {
	s.CostStates = slices.Clone(s.CostStates)
	s.PRLinks = slices.Clone(s.PRLinks)
	s.ContinuedIn = slices.Clone(s.ContinuedIn)
	s.CodexTurns = slices.Clone(s.CodexTurns)
	s.Truncated = slices.Clone(s.Truncated)
	s.Commits = s.Commits.clone()
	return s
}

// fit keeps the encoded snapshot within maxSnapshotBytes, which the per-field
// caps alone do not: a hundred PR links at their cap come to twice that. The
// oldest runs in cost_states go first, then the last PR links and continuations
// -- the ones the caps would also have cut -- then the oldest Codex turns, and
// each list cut is named in truncated. json.Marshal escapes more than the
// request encoder does, so its count is an upper bound on what the server
// measures.
func (s *SessionSnapshot) fit() {
	for {
		b, err := json.Marshal(s)
		if err != nil || len(b) <= maxSnapshotBytes {
			return
		}
		switch {
		case s.Commits != nil && len(s.Commits.Items) > 0:
			s.Commits.Items = s.Commits.Items[1:]
			s.Commits.compactTrees()
			s.markTruncated("commits")
		case s.Commits != nil && len(s.Commits.Unresolved) > 0:
			s.Commits.Unresolved = s.Commits.Unresolved[1:]
			s.markTruncated("commits")
		case len(s.CostStates) > 0:
			s.CostStates = slices.Delete(s.CostStates, 0, 1)
			s.markTruncated("cost_states")
		case len(s.PRLinks) > 0:
			s.PRLinks = s.PRLinks[:len(s.PRLinks)-1]
			s.markTruncated("pr_links")
		case len(s.ContinuedIn) > 0:
			s.ContinuedIn = s.ContinuedIn[:len(s.ContinuedIn)-1]
			s.markTruncated("continued_in")
		case len(s.CodexTurns) > 0:
			s.CodexTurns = slices.Delete(s.CodexTurns, 0, 1)
			s.markTruncated("codex_turns")
		default:
			return
		}
	}
}

// TitleFlags holds one flag for each title of a snapshot.
type TitleFlags struct {
	AITitle     bool `json:"ai_title,omitempty"`
	CustomTitle bool `json:"custom_title,omitempty"`
	AgentName   bool `json:"agent_name,omitempty"`
}

// noteRead records, for each title whose value next changes from prev, whether
// redact_user_prompts was on when that value was read. A title whose value did
// not change keeps the flag it had: a repeat read under another policy is not a
// new reading of anything.
func (f *TitleFlags) noteRead(prev, next SessionSnapshot, redacting bool) {
	if !sameTitle(prev.AITitle, next.AITitle) {
		f.AITitle = redacting
	}
	if !sameTitle(prev.CustomTitle, next.CustomTitle) {
		f.CustomTitle = redacting
	}
	if !sameTitle(prev.AgentName, next.AgentName) {
		f.AgentName = redacting
	}
}

func sameTitle(a, b *string) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// sameSnapshot reports whether two snapshots encode alike: whether sending one
// in place of the other would tell the server anything.
func sameSnapshot(a, b SessionSnapshot) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ab, bb)
}

// SessionSnapshotState is a main transcript's snapshot as the client keeps it
// between passes (FileState.SessionSnapshot), with what sending it takes.
type SessionSnapshotState struct {
	Snapshot SessionSnapshot `json:"snapshot"`
	// Revision counts the snapshot's effective changes; SentRevision is the last
	// one the server accepted, or exclusion consumed. Dirty while it is behind.
	Revision     int64 `json:"revision,omitempty"`
	SentRevision int64 `json:"sent_revision,omitempty"`
	// ProjectHash is the sessionlog.ProjectHash the file's records carry.
	ProjectHash string `json:"project_hash,omitempty"`
	// NewestRecordAt is the newest record timestamp seen in the file. The
	// metadata lines carry none, so the snapshot's account is read at this time
	// (ClaudeAccountAt); read at their own, it would always come out blank.
	NewestRecordAt time.Time `json:"newest_record_at,omitzero"`
	// ObservedAt is when the snapshot last changed: the item's observed_at, which
	// the server's newer-wins upsert compares.
	ObservedAt time.Time `json:"observed_at,omitzero"`
	// TitlesReadRedacted marks each title whose current value was read while
	// redact_user_prompts was on. The sender hides such a title whatever the
	// option says when it goes out, so turning the option off does not publish
	// what was read under it. A value read later goes as the option then says.
	TitlesReadRedacted TitleFlags `json:"titles_read_redacted,omitzero"`
}

func (st *SessionSnapshotState) dirty() bool { return st.Revision > st.SentRevision }

// isMainTranscript reports whether path is a session's own transcript rather
// than a subagent's (FindJSONLFiles' subagents/ layouts). Only main transcripts
// carry the metadata lines, so only they get a snapshot.
func isMainTranscript(path string) bool {
	return !strings.Contains(filepath.ToSlash(path), "/subagents/")
}

// collectedStart is where the part of a file sync collected begins: the end of
// the first-sync skip or of a shrunk file's remnant, whichever is later. Bytes in
// front of it were never collected, and nothing built from collected lines reads
// them -- re-enrichment, the snapshot rebuild and its backfill alike.
func collectedStart(fs *FileState) int64 {
	if fs == nil {
		return 0
	}
	return max(fs.SkippedAtFirstSync, fs.CollectedFrom)
}

// keptBackForGood reports whether some of a file's lines were deliberately not
// collected: a record the offset passed unsent under a repository allowlist,
// because its repository was excluded or may have been (ExcludedSeen), or
// because its hold ran out (HoldExpired). This is the one place that lists
// these marks. Neither is ever cleared, and they keep the file from being read
// again whatever the settings later become -- the allowlist removed, or widened
// to the repository that was excluded -- because what lies between
// collectedStart and the offset says nothing about which of its lines were
// sent: only the advance that passed them knew.
func keptBackForGood(fs *FileState) bool {
	return fs != nil && (fs.ExcludedSeen || fs.HoldExpired != nil)
}

// mayRebuild reports whether the one-time snapshot rebuild may read the part
// of a main transcript sync collected (advanceSessionSnapshot), in a pass or
// on the observation flush. It may only without an allowlist, and only for a
// file nothing was kept back of for good (keptBackForGood) and whose rebuild
// was not declined before (FileState.RebuildDeclined).
//
// Under an allowlist it never may. A file an earlier cctrace consumed under one
// left no mark of the lines it consumed unsent, so this version cannot tell
// them from the lines it sent, and a rebuild has no lookup to judge them by:
// lookups are made before judgment, not in an advance or the flush. Only
// re-enrichment, which looks every cwd of the file up (reenrichAllowed), can
// vouch for such a part.
func (s *Syncer) mayRebuild(fs *FileState) bool {
	return len(s.collectPrefixes) == 0 && !keptBackForGood(fs) && !fs.RebuildDeclined
}

// mayBackfill reports whether re-enrichment may fill the snapshot, the edited
// paths and the git calls from a file it got to (reenrichFile): under an
// allowlist reenrichAllowed has passed it by then. It may unless something of
// the file was kept back for good, with an allowlist or without one. Develop's
// re-enrichment of the stored records is not this: it only updates rows that
// exist, and creates nothing for a line that was never sent.
func mayBackfill(fs *FileState) bool {
	return !keptBackForGood(fs)
}

// heldBackForNow reports whether something about a file's pending tail is not
// settled yet: a run held now (Held), a last cwd that could not be established
// (CWDUnknown), a taint above the offset. Unlike the marks keptBackForGood
// lists, each of these may yet clear.
func (s *Syncer) heldBackForNow(path string, fs *FileState) bool {
	if fs.CWDUnknown || len(fs.Held) > 0 {
		return true
	}
	for _, t := range s.state.Taints[path] {
		if t.Until > fs.Offset {
			return true
		}
	}
	return false
}

// keptBackWhole reports whether, as far as its marks tell, nothing of a main
// transcript that has no snapshot was collected: something of it was kept back
// for good (keptBackForGood) and nothing says an advance collected any of it --
// neither the mark that its lines hold no metadata line nor a declined
// rebuild, which only follow collected lines. Its session gets no snapshot
// then, whatever its subagents did: no line of its main transcript went out.
func keptBackWhole(fs *FileState) bool {
	return keptBackForGood(fs) && !fs.NoSessionSnapshot && !fs.RebuildDeclined
}

// errRebuildDeclined is what a rebuild callback returns when it may not read
// the collected part (mayRebuild): advanceSnapshot then makes the snapshot
// from the advance's own lines, quietly, and leaves its titles unknown.
var errRebuildDeclined = errors.New("session snapshot rebuild declined")

// snapshotToCarry is the snapshot a session's git counts or commit calls go
// into when its main transcript has none: one carrying its titles, as "", when
// its collected part was read and held no metadata line (NoSessionSnapshot),
// and one leaving them out -- unknown, not empty -- when the part was not read
// (RebuildDeclined). "Not read" is not "read and found nothing": a snapshot
// that carries "" titles keeps them, and a later re-enrichment with content
// could not fill the real ones in (merge).
func snapshotToCarry(fs *FileState) SessionSnapshot {
	if fs.RebuildDeclined {
		return SessionSnapshot{Schema: sessionSnapshotSchema}
	}
	return newSessionSnapshot()
}

// later returns the later of two times.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// typeMarker opens the type member of a line as Claude Code writes it.
var typeMarker = []byte(`"type":"`)

// hasSnapshotMarker reports whether line holds `"type":"<name>"` for one of
// snapshotLineTypes, without decoding it. A transcript is mostly conversation,
// and a rebuild runs inside a sync pass, so only lines that pass this are
// decoded. A marker quoted inside a string is escaped and does not match.
func hasSnapshotMarker(line []byte) bool {
	rest := line
	for {
		i := bytes.Index(rest, typeMarker)
		if i < 0 {
			return false
		}
		rest = rest[i+len(typeMarker):]
		if end := bytes.IndexByte(rest, '"'); end >= 0 && snapshotLineTypes[string(rest[:end])] {
			return true
		}
	}
}

// rebuildSessionSnapshot builds a snapshot from bytes [from, to) of a main
// transcript: the part sync collected before the file had a snapshot. It never
// reads in front of from, and it decodes only the lines hasSnapshotMarker passes.
// It returns the snapshot, carrying its titles, and the newest timestamp among
// the lines it decoded.
func rebuildSessionSnapshot(ctx context.Context, path string, from, to int64) (SessionSnapshot, time.Time, error) {
	snap := newSessionSnapshot()
	var newest time.Time
	f, err := os.Open(path)
	if err != nil {
		return snap, newest, err
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return snap, newest, err
	}
	// A line running past to was not collected. The limit makes it the trailing
	// partial line, which ReadLine reports incomplete.
	reader := bufio.NewReaderSize(io.LimitReader(f, to-from), 64*1024)
	for {
		line, _, complete, err := jsonlscan.ReadLine(ctx, reader)
		if errors.Is(err, jsonlscan.ErrLineTooLong) {
			continue
		}
		if err != nil && err != io.EOF {
			return snap, newest, err
		}
		if complete && hasSnapshotMarker(line) {
			newest = later(newest, snap.apply(line))
		}
		if err == io.EOF {
			return snap, newest, nil
		}
	}
}

// advanceSessionSnapshot folds the records an advance of the offset collected
// into a main transcript's snapshot. It is called where the offset moves, just
// before the save (collectAdvanced), so the snapshot and the offset are stored
// together. offset is where the advanced bytes begin, and records are the
// collected ones among them: under an allowlist, not the ones kept back.
//
// A transcript that sync has already collected part of, and whose snapshot is
// neither carrying titles nor known to be empty -- collected before snapshots
// existed, or given one without titles by a default backfill -- is first rebuilt
// from that collected part. Once: from then on the snapshot carries titles, ""
// when that part had none, or the transcript is marked as having nothing
// (FileState.NoSessionSnapshot). The rebuild runs only for a transcript
// receiving new lines, so the redaction policy in force is the one the person
// chose for it.
//
// A part the rebuild may not read (mayRebuild) is not read. The snapshot is
// then what the advances build, its titles left out until something that may
// read the part fills them in (a re-enrichment with content), and the
// transcript is not marked as holding nothing (NoSessionSnapshot): it was not
// looked at. Under an allowlist the decline is recorded (RebuildDeclined), so
// that no later pass reads the part once the allowlist is gone.
//
// Only an effective change moves the revision. ai-title lines repeat constantly
// with the same value, and a repeat must not make the snapshot dirty.
func (s *Syncer) advanceSessionSnapshot(ctx context.Context, fs *FileState, filePath string, offset int64, projectHash string, records []*sessionlog.Record) {
	if fs == nil || !isMainTranscript(filePath) {
		return
	}
	advanceSnapshot(ctx, fs, filePath, offset, projectHash, s.redactingPrompts(),
		func(from, to int64) (SessionSnapshot, time.Time, error) {
			if !s.mayRebuild(fs) {
				if len(s.collectPrefixes) > 0 {
					fs.RebuildDeclined = true
				}
				return SessionSnapshot{}, time.Time{}, errRebuildDeclined
			}
			return rebuildSessionSnapshot(ctx, filePath, from, to)
		},
		func(next *SessionSnapshot) time.Time {
			var newest time.Time
			for _, r := range records {
				newest = later(newest, r.Timestamp)
				if snapshotLineTypes[r.Type] {
					next.apply(r.RawLine)
				}
			}
			return newest
		})
}

// advanceSnapshot is what advancing a snapshot takes, for a Claude transcript
// and a Codex rollout (AdvanceCodexSnapshot) alike: rebuild reads the collected
// part [from, to) when the snapshot needs that, and fold applies the pass's own
// lines and returns their newest timestamp. redacting is the prompts policy the
// titles are read under.
//
// rebuild may decline (errRebuildDeclined) instead of reading: the snapshot is
// then the fold alone, its titles as the state had them -- left out when it
// had none -- and a fold that adds nothing leaves the file without a snapshot
// and without the mark that its lines hold none. A Codex rollout's rebuild
// never declines.
func advanceSnapshot(ctx context.Context, fs *FileState, path string, offset int64, projectHash string, redacting bool,
	rebuild func(from, to int64) (SessionSnapshot, time.Time, error), fold func(next *SessionSnapshot) time.Time) {
	st := fs.SessionSnapshot
	// A new snapshot is compared with a session that has said nothing, so a
	// transcript with no metadata lines is never sent.
	prev := newSessionSnapshot()
	var next SessionSnapshot
	var newest time.Time
	switch {
	case st != nil:
		prev, next, newest = st.Snapshot, st.Snapshot.clone(), st.NewestRecordAt
	case fs.NoSessionSnapshot:
		// Read before and found empty: there is nothing to rebuild.
		next = newSessionSnapshot()
	}
	declined := false
	if !next.carriesTitles() {
		rebuilt := newSessionSnapshot()
		if start := collectedStart(fs); offset > start {
			var at time.Time
			var err error
			rebuilt, at, err = rebuild(start, offset)
			switch {
			case errors.Is(err, errRebuildDeclined):
				declined = true
			case err != nil:
				// Left as it was: the next pass with new lines rebuilds from the
				// same start, this pass's lines included.
				if ctx.Err() == nil {
					log.Printf("[syncer] %s: rebuild session snapshot: %v", path, err)
				}
				return
			default:
				newest = later(newest, at)
			}
		}
		if declined {
			next.Schema = sessionSnapshotSchema
		} else {
			next.merge(rebuilt)
		}
	}
	newest = later(newest, fold(&next))
	next.fit()

	if st == nil {
		if next.empty() {
			// A flag, not an empty SessionSnapshotState: that would add some 300
			// bytes per transcript to a file rewritten on every Save. The flag is
			// also what keeps the rebuild to once. A part that was not read is
			// not known to hold nothing, and gets no flag.
			if !declined {
				fs.NoSessionSnapshot = true
			}
			return
		}
		st = &SessionSnapshotState{}
		fs.SessionSnapshot = st
		fs.NoSessionSnapshot = false
	}
	st.ProjectHash = projectHash
	st.NewestRecordAt = newest
	st.TitlesReadRedacted.noteRead(prev, next, redacting)
	if !sameSnapshot(prev, next) {
		st.Revision++
		st.ObservedAt = nowFn().UTC()
	}
	st.Snapshot = next
}

// redactingPrompts reports whether redact_user_prompts is on for this Syncer's
// uploads.
func (s *Syncer) redactingPrompts() bool {
	return s.client != nil && s.client.redact.UserPrompts
}

// backfillSessionSnapshot rebuilds a main transcript's snapshot during
// re-enrichment from the records sync collected (Offset in
// [collectedStart, the stored offset)), so a historical session gets its outcome
// metadata without waiting for new lines. It reports whether the state changed,
// and whether the snapshot did (one more for SnapshotsBackfilled).
//
// Titles and names are content, which a default re-enrichment does not send
// (sessionlog.Record.WithholdContent): without includeContent they are left out,
// and with it they are carried, redacted at the sender under the policy in force
// then. The server overwrites a snapshot whole, so one without titles must never
// replace one that has them. It becomes the state only for a session that has
// none -- one never sent -- and is otherwise merged into the state, whose titles
// merge keeps.
//
// Under an allowlist a file gets here only once reenrichAllowed has looked
// every cwd of it up, which is what a declined rebuild could not do: the file
// is vouched for, and its decline (RebuildDeclined) is forgotten. Without one
// nothing vouches for the part, and the decline stands.
func (s *Syncer) backfillSessionSnapshot(fs *FileState, filePath, projectHash string, records []*sessionlog.Record, includeContent bool) (stateChanged, snapshotChanged bool) {
	if fs == nil || !isMainTranscript(filePath) {
		return false, false
	}
	start, end := collectedStart(fs), fs.Offset
	built := newSessionSnapshot()
	var newest time.Time
	for _, r := range records {
		if r.Offset < start || r.Offset >= end {
			continue
		}
		newest = later(newest, r.Timestamp)
		if snapshotLineTypes[r.Type] {
			built.apply(r.RawLine)
		}
	}
	if !includeContent {
		built.AITitle, built.CustomTitle, built.AgentName = nil, nil, nil
	}
	stateChanged, snapshotChanged = backfillSnapshot(fs, projectHash, built, newest, s.redactingPrompts())
	if fs.RebuildDeclined && len(s.collectPrefixes) > 0 {
		fs.RebuildDeclined = false
		stateChanged = true
	}
	return stateChanged, snapshotChanged
}

// backfillSnapshot puts built -- a snapshot re-enrichment made from the part
// sync collected, of a Claude transcript or a Codex rollout
// (BackfillCodexSnapshot) -- into the file's state: as the state when the file
// has none, merged into it otherwise. newest is the newest timestamp built was
// made from, and redacting the prompts policy its titles were read under. It
// reports whether the state changed, and whether the snapshot did.
func backfillSnapshot(fs *FileState, projectHash string, built SessionSnapshot, newest time.Time, redacting bool) (stateChanged, snapshotChanged bool) {
	built.fit()

	st := fs.SessionSnapshot
	if st == nil {
		// A collected part that says nothing gets no state entry: there would be
		// nothing to send, and sync-state.json is rewritten whole on every Save.
		if built.empty() {
			return false, false
		}
		st = &SessionSnapshotState{
			Snapshot:       built,
			Revision:       1,
			ProjectHash:    projectHash,
			NewestRecordAt: newest,
			ObservedAt:     nowFn().UTC(),
		}
		st.TitlesReadRedacted.noteRead(SessionSnapshot{}, built, redacting)
		fs.SessionSnapshot = st
		fs.NoSessionSnapshot = false
		return true, true
	}
	next := st.Snapshot.clone()
	next.merge(built)
	next.fit()
	if !sameSnapshot(st.Snapshot, next) {
		st.TitlesReadRedacted.noteRead(st.Snapshot, next, redacting)
		st.Snapshot = next
		st.Revision++
		st.ObservedAt = nowFn().UTC()
		stateChanged, snapshotChanged = true, true
	}
	if newest.After(st.NewestRecordAt) {
		st.NewestRecordAt = newest
		stateChanged = true
	}
	if st.ProjectHash != projectHash {
		st.ProjectHash = projectHash
		stateChanged = true
	}
	return stateChanged, snapshotChanged
}
