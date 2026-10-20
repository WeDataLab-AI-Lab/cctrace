package syncer

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"

	"cctrace/internal/gitctx"
	"cctrace/internal/sessionlog"
)

// A Claude session's snapshot counts the files the session edited and how many
// of them git still reports uncommitted (docs/planning/session-detail-analysis/
// plan.md §4.1(4)). The edited paths are collected while sync reads the
// session's main and subagent transcripts and are kept on the main transcript's
// FileState; they never leave the machine. The counts are taken on the
// observation flush, from `git status` in each work tree the paths lie in, and
// go out in the snapshot.
//
// Nothing that can block on a filesystem runs in the sync pass: it keeps paths
// as the transcript wrote them, cleaned. Resolving them, finding their work
// trees and asking git all run on the flush, in goroutines it waits for only
// within sessionGitCountBudget (startGitJob): a hung network mount costs a
// goroutine, not the watch loop. A session git gives no answer for keeps its
// counts and is asked again after a backoff (retryBackoff).
//
// A Codex session has none: Codex edits through apply_patch, whose paths are
// inside the patch text rather than in a field of their own.

// Bounds on a session's git state.
const (
	// maxEditedPaths is how many edited paths one session keeps. Past it a path
	// is left out, and the counts say so (truncated).
	maxEditedPaths = 2000
	// sessionGitWindow is how long after its last activity a session's edited
	// files are counted again. A commit made after the session ended lowers the
	// count, which is the point; past the window the paths are dropped and the
	// counts stay as last taken.
	sessionGitWindow = 7 * 24 * time.Hour
	// gitCountWorkers is how many sessions, and how many work trees, one flush
	// asks about at a time.
	gitCountWorkers = 4
)

// sessionGitCountBudget bounds the counting one flush does before it sends, so
// a slow or hung git or filesystem leaves it the time to send what it has: half
// of the 10 seconds a run about to exit gives its flush, and at most that long
// a hold on the watch loop. Sessions not counted in time stay due for the next
// flush. Var, not const, so tests can shrink it.
var sessionGitCountBudget = 5 * time.Second

// statFn and evalSymlinksFn are the filesystem calls counting makes,
// indirected so a test can stall one as a hung network mount would.
var (
	statFn         = os.Stat
	evalSymlinksFn = filepath.EvalSymlinks
)

// retryBackoff is how long a session waits to be counted again after n counts
// in a row that ended without counts: five minutes, doubling up to metaTTL. A
// mount that hangs, or a git that does not answer, then costs the session one
// try an hour, not one every flush for a week.
func retryBackoff(n int) time.Duration {
	d := 5 * time.Minute
	for ; n > 1 && d < metaTTL; n-- {
		d *= 2
	}
	return min(d, metaTTL)
}

// SessionGitState is what the client keeps between passes to count a Claude
// session's uncommitted edited files (FileState.SessionGit, on the main
// transcript's entry).
type SessionGitState struct {
	// Edited holds the paths the session's transcripts edited: absolute and
	// cleaned, otherwise as written, so a filesystem that keeps both Unicode
	// forms of a name apart still finds them. They are compared in NFC, and
	// their symlinks resolved, when they are counted. Sorted.
	Edited []string `json:"edited,omitempty"`
	// Truncated says a path was left out of Edited at maxEditedPaths.
	Truncated bool `json:"truncated,omitempty"`
	// ActiveAt is the newest record time sync has read from the session's
	// transcripts, never later than when it was read.
	ActiveAt time.Time `json:"active_at,omitzero"`
	// CheckedAt is when the counts were last taken, or the session settled
	// without them: its snapshot goes under an excluded account. Pending says a
	// pass has read an edit since, so the next flush counts again without
	// waiting for metaTTL.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Pending   bool      `json:"pending,omitempty"`
	// TriedAt is when a count last ended without counts -- git gave no answer, a
	// job did not finish in time, or the snapshot could not be built -- and
	// NoAnswers how many counts in a row ended so, which sets the backoff
	// (retryBackoff). Both clear once counts are taken. Past its window a session
	// never counted is dropped once its backoff has reached its cap.
	TriedAt   time.Time `json:"tried_at,omitzero"`
	NoAnswers int       `json:"no_answers,omitempty"`
	// Partial says the set began again after its paths were dropped a week after
	// the session, and lacks the ones dropped (see SnapshotGit.Partial).
	Partial bool `json:"partial,omitempty"`
}

// SnapshotGit is what a Claude session's edited files came to: counts, never
// paths.
type SnapshotGit struct {
	// ChangedAt is when these counts were first observed: when a count last came
	// to different numbers. A count that comes to the same numbers leaves it and
	// does not send the snapshot again -- derived work redoes a session for every
	// snapshot it receives.
	ChangedAt time.Time `json:"changed_at"`
	// EditedFiles is how many files the session edited, up to maxEditedPaths;
	// with options.collect_repository_prefixes set, only those in repositories it
	// collects.
	EditedFiles int `json:"edited_files"`
	// UncommittedEditedFiles is how many of them `git status` reports: changed,
	// staged, deleted or untracked, a renamed one under its new path.
	UncommittedEditedFiles int `json:"uncommitted_edited_files"`
	// Repositories is how many work trees they lie in.
	Repositories int `json:"repositories"`
	// Unresolved is how many git could not place in its status: in no work tree,
	// inside a .git directory, or in a work tree whose status git answered with a
	// failure -- one whose .git git cannot use among them.
	Unresolved int `json:"unresolved"`
	// Partial says the counts miss edits the client never read: a transcript of
	// the session read from past its start, edits read while the session had no
	// main transcript to keep them, or paths dropped a week after the session
	// before it went on. A partial count's 0 says nothing, and N says at least N.
	Partial bool `json:"partial,omitempty"`
}

// add puts path in the set, unless the set is at its cap.
func (g *SessionGitState) add(path string) {
	i, found := slices.BinarySearch(g.Edited, path)
	switch {
	case found:
	case len(g.Edited) >= maxEditedPaths:
		g.Truncated = true
	default:
		g.Edited = slices.Insert(g.Edited, i, path)
	}
}

// editedPaths returns the paths the edit tools of the records' assistant lines
// name: Edit, Write and MultiEdit by input.file_path, NotebookEdit by
// input.notebook_path. A path that is not absolute is not kept; the rest are
// kept cleaned. Nothing here touches the filesystem. A block in a shape this
// client does not expect adds nothing.
func editedPaths(records []*sessionlog.Record) []string {
	var paths []string
	for _, r := range records {
		if r.Type != "assistant" || !bytes.Contains(r.Message, []byte(`"tool_use"`)) {
			continue
		}
		var msg struct {
			Content []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(r.Message, &msg) != nil {
			continue
		}
		for _, raw := range msg.Content {
			var block struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Input struct {
					FilePath     string `json:"file_path"`
					NotebookPath string `json:"notebook_path"`
				} `json:"input"`
			}
			if json.Unmarshal(raw, &block) != nil || block.Type != "tool_use" {
				continue
			}
			var path string
			switch block.Name {
			case "Edit", "Write", "MultiEdit":
				path = block.Input.FilePath
			case "NotebookEdit":
				path = block.Input.NotebookPath
			}
			if !filepath.IsAbs(path) {
				continue
			}
			paths = append(paths, filepath.Clean(path))
		}
	}
	return paths
}

// sessionTranscript is the main transcript a transcript's edits count for: a
// main transcript itself, and for a subagent's
// <project>/<sessionId>/subagents/<agent>.jsonl, <project>/<sessionId>.jsonl.
// It is "" for the older <project>/subagents/ layout, which names no session.
func (s *Syncer) sessionTranscript(path string) string {
	if isMainTranscript(path) {
		return path
	}
	projects := filepath.Join(normalizeHome(s.claudeDir), "projects")
	rel, err := filepath.Rel(projects, path)
	if err != nil {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 4 || parts[2] != "subagents" {
		return ""
	}
	return filepath.Join(projects, parts[0], parts[1]+".jsonl")
}

// noteSessionEdits adds what the records an advance of the offset collected,
// of a session's main or subagent transcript, edited to the session's set
// (SessionGitState), and moves the session's activity time. It is called where
// the offset moves (collectAdvanced), so a set never takes an edit from a line
// read twice, nor from one the allowlist kept back; offset is where the
// advanced bytes begin.
//
// The first advance that reaches here records, on the transcript's own FileState,
// where its edits began to be collected (EditsFrom). A transcript whose session
// has no main transcript in this state -- one that is not on disk -- keeps
// none: its edits are not collected, and its missing EditsFrom says so when the
// session is counted (editsPartial).
func (s *Syncer) noteSessionEdits(filePath string, offset int64, records []*sessionlog.Record) {
	main := s.sessionTranscript(filePath)
	if main == "" {
		return
	}
	mfs := s.state.Files[main]
	if mfs == nil {
		return
	}
	if fs := s.state.Files[filePath]; fs != nil && fs.EditsFrom == nil {
		from := offset
		fs.EditsFrom = &from
	}
	edited := editedPaths(records)
	gs := mfs.SessionGit
	if gs == nil {
		if len(edited) == 0 {
			return
		}
		gs = &SessionGitState{}
		mfs.SessionGit = gs
		// Counted before and dropped a week after the session: the set begins
		// again without the paths it had.
		if st := mfs.SessionSnapshot; st != nil && st.Snapshot.Git != nil {
			gs.Partial = true
		}
	}
	now := nowFn()
	for _, r := range records {
		at := r.Timestamp
		if at.After(now) {
			at = now
		}
		gs.ActiveAt = later(gs.ActiveAt, at)
	}
	if gs.ActiveAt.IsZero() {
		gs.ActiveAt = now
	}
	if len(edited) > 0 {
		gs.Pending = true
		for _, path := range edited {
			gs.add(path)
		}
	}
}

// backfillSessionEdits fills a session's set during re-enrichment from the part
// sync collected of one of its transcripts, so a session active within
// sessionGitWindow is counted from what it edited before this client read its
// lines, not only after. The transcript's EditsFrom moves back to the start of
// that part, and a session filled so from the start of every transcript is not
// partial. A session not active in the window is left as it is. It reports
// whether the state changed.
func (s *Syncer) backfillSessionEdits(filePath string, records []*sessionlog.Record) bool {
	fs := s.state.Files[filePath]
	main := s.sessionTranscript(filePath)
	if fs == nil || main == "" || s.state.Files[main] == nil {
		return false
	}
	mfs := s.state.Files[main]
	start, end := collectedStart(fs), fs.Offset
	now := nowFn()
	var part []*sessionlog.Record
	var newest time.Time
	for _, r := range records {
		if r.Offset < start || r.Offset >= end {
			continue
		}
		part = append(part, r)
		at := r.Timestamp
		if at.After(now) {
			at = now
		}
		newest = later(newest, at)
	}
	active := newest
	if mfs.SessionGit != nil {
		active = later(active, mfs.SessionGit.ActiveAt)
	}
	if active.IsZero() || now.Sub(active) > sessionGitWindow {
		return false
	}
	edited := editedPaths(part)
	gs := mfs.SessionGit
	if gs == nil && len(edited) > 0 {
		gs = &SessionGitState{}
		mfs.SessionGit = gs
	}
	if gs != nil {
		gs.ActiveAt = later(gs.ActiveAt, newest)
		for _, path := range edited {
			gs.add(path)
		}
		gs.Pending = gs.Pending || len(edited) > 0
	}
	from := start
	fs.EditsFrom = &from
	// Filled from the start of every transcript of the session, the set lacks
	// none of the paths it was dropped without.
	if gs != nil && gs.Partial {
		whole := true
		for path, tfs := range s.state.Files {
			if tfs != nil && s.sessionTranscript(path) == main && editsPartial(tfs) {
				whole = false
				break
			}
		}
		gs.Partial = !whole
	}
	return true
}

// editsPartial reports whether a transcript holds edits its session's set never
// took: its edits began to be collected past its start -- none at all, when
// EditsFrom is unset and the transcript has lines -- or its collected part does
// not begin at its start.
func editsPartial(fs *FileState) bool {
	from := fs.Offset
	if fs.EditsFrom != nil {
		from = *fs.EditsFrom
	}
	return from > 0 || collectedStart(fs) > 0
}

// dueSession is a session one flush counts.
type dueSession struct {
	path    string
	fs      *FileState
	gs      *SessionGitState
	partial bool
}

// at is the time the session's snapshot is attributed at once it carries
// counts: its newest record (putGitCounts).
func (d *dueSession) at() time.Time {
	if st := d.fs.SessionSnapshot; st != nil {
		return later(st.NewestRecordAt, d.gs.ActiveAt)
	}
	return d.gs.ActiveAt
}

// countSessionEdits takes the counts of this home's sessions that hold edited
// paths and are due: those a pass has read an edit for since they were last
// taken, and the rest metaTTL after that, while the session was active within
// sessionGitWindow. A session inactive for longer, once counted, drops its
// paths and keeps the counts as last taken; one first read already that old is
// counted once. New counts go into the main transcript's snapshot, where the
// flush sends them. It reports whether the state changed.
//
// git is not asked about a session whose snapshot would go under an excluded
// account, which is settled as if counted, nor about any while the exclusion
// query fails. It answers through internal/gitctx, every call under its
// timeout, in jobs this flush waits for within sessionGitCountBudget. When git
// gives no answer (gitctx.NoAnswer), a job does not finish in time, or the
// snapshot the counts go into cannot be built, the session keeps its previous
// counts and revision, and is asked again after retryBackoff. A session the
// budget ran out before reaching is due on the next flush. A failure git
// answers with makes the paths it concerns unresolved.
func (s *Syncer) countSessionEdits(ctx context.Context, now time.Time) bool {
	due, changed := s.dueSessions(now)
	due, settled := s.countable(ctx, due, now)
	changed = changed || settled
	if len(due) == 0 {
		return changed
	}
	s.markPartial(due)

	jobCtx, cancel := context.WithTimeout(ctx, sessionGitCountBudget)
	defer cancel()
	// Finding where paths lie gets half the budget at most, so one that hangs
	// leaves the rest for asking the work trees the others were found in.
	locateCtx, cancelLocate := context.WithTimeout(jobCtx, sessionGitCountBudget/2)
	located := s.locate(locateCtx, now, due)
	cancelLocate()
	inspected := s.inspect(jobCtx, now, located)
	for _, d := range due {
		loc, reached := located[d.path]
		if !reached {
			continue
		}
		var counts SnapshotGit
		answered := !loc.noAnswer
		if answered {
			counts, reached, answered = s.countsOf(loc, inspected)
			if !reached {
				continue
			}
		}
		changed = true
		settled := false
		if answered {
			counts.Partial = d.partial
			counts.ChangedAt = now.UTC()
			settled = s.putGitCounts(ctx, d.fs, d.path, counts, d.gs, now)
		}
		if !settled {
			d.gs.TriedAt, d.gs.NoAnswers = now, d.gs.NoAnswers+1
			continue
		}
		d.gs.Pending, d.gs.CheckedAt = false, now
		d.gs.TriedAt, d.gs.NoAnswers = time.Time{}, 0
	}
	return changed
}

// dueSessions lists, in path order, the own home's sessions to count now, and
// drops the paths of those inactive past sessionGitWindow that have been
// counted at least once, or never counted with their backoff run up to its cap:
// tried again and again, with nothing back. A session backing off is neither
// due nor dropped until its backoff has passed, so one first read past its
// window gets its tries. It reports whether it dropped any.
func (s *Syncer) dueSessions(now time.Time) ([]*dueSession, bool) {
	root := filepath.Join(normalizeHome(s.claudeDir), "projects") + string(filepath.Separator)
	var paths []string
	for path, fs := range s.state.Files {
		if fs != nil && fs.SessionGit != nil && strings.HasPrefix(path, root) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var due []*dueSession
	dropped := false
	for _, path := range paths {
		fs := s.state.Files[path]
		gs := fs.SessionGit
		switch {
		case gs.NoAnswers > 0 && now.Sub(gs.TriedAt) < retryBackoff(gs.NoAnswers):
			continue
		case now.Sub(gs.ActiveAt) > sessionGitWindow && (!gs.CheckedAt.IsZero() || retryBackoff(gs.NoAnswers) >= metaTTL):
			fs.SessionGit = nil
			dropped = true
			continue
		case !gs.Pending && !gs.CheckedAt.IsZero() && now.Sub(gs.CheckedAt) < metaTTL:
			continue
		}
		due = append(due, &dueSession{path: path, fs: fs, gs: gs})
	}
	return due, dropped
}

// countable keeps the due sessions git may be asked about: not one whose
// snapshot would go under an excluded account, which is consumed unsent
// anyway. Such a session is settled as if counted -- judged again metaTTL
// later, its paths dropped once its window has passed -- and countable reports
// whether it settled any. While the exclusion query fails, none is kept; they
// stay due.
func (s *Syncer) countable(ctx context.Context, due []*dueSession, now time.Time) ([]*dueSession, bool) {
	if len(due) == 0 {
		return nil, false
	}
	home := normalizeHome(s.claudeDir)
	refs := make([]AccountRef, len(due))
	var accounts []AccountRef
	for i, d := range due {
		if id := s.state.ClaudeAccountAt(home, d.at()); id != "" {
			refs[i] = AccountRef{BillingProvider: "anthropic", AccountID: id}
			accounts = append(accounts, refs[i])
		}
	}
	if s.accounts.resolve(ctx, accounts) != nil {
		return nil, false
	}
	kept := due[:0]
	settled := false
	for i, d := range due {
		if a := refs[i]; a.AccountID != "" && (s.client.excludedLocally(a) || s.accounts.excluded(a)) {
			d.gs.Pending, d.gs.CheckedAt = false, now
			settled = true
			continue
		}
		kept = append(kept, d)
	}
	return kept, settled
}

// markPartial marks each due session whose set misses edits: one dropped and
// begun again (SessionGitState.Partial), or a transcript of it, main or
// subagent, that holds edits the set never took (editsPartial).
func (s *Syncer) markPartial(due []*dueSession) {
	byMain := make(map[string]*dueSession, len(due))
	for _, d := range due {
		d.partial = d.gs.Partial || editsPartial(d.fs)
		byMain[d.path] = d
	}
	for path, fs := range s.state.Files {
		if fs == nil || isMainTranscript(path) {
			continue
		}
		if d := byMain[s.sessionTranscript(path)]; d != nil && editsPartial(fs) {
			d.partial = true
		}
	}
}

// putGitCounts puts a session's counts into its main transcript's snapshot,
// moving the revision when they change what the snapshot says: counts that come
// to the numbers it holds keep their ChangedAt. A main transcript with no
// snapshot yet -- one whose subagents alone have had passes since snapshots
// existed -- first has its collected part read, as a pass would read it
// (advanceSessionSnapshot), where the rebuild may read it (mayRebuild); one
// whose part holds no metadata line gets a snapshot of its own for the counts,
// and so does one whose part may not be read, with its titles left out
// (snapshotToCarry). One with nothing collected gets none, whatever its
// subagents edited: one sync never collected -- nothing of it lies past
// collectedStart -- or one kept back whole (keptBackWhole), such as a main
// transcript options.collect_repository_prefixes leaves out entirely.
//
// It reports whether the session is settled: its counts are in the snapshot,
// or it has none to put them in. It is not when the collected part could not
// be read; the session then stays due, and the next flush tries again.
//
// The snapshot is attributed at the session's newest record (ActiveAt), as one
// built from its lines is. Attributed at the time the counts changed, it would
// go under whichever account is signed in when they are taken again, an
// excluded account's session under another account among them.
func (s *Syncer) putGitCounts(ctx context.Context, fs *FileState, path string, counts SnapshotGit, gs *SessionGitState, now time.Time) bool {
	projectHash := sessionlog.ProjectHash(s.claudeDir, path)
	if fs.SessionSnapshot == nil {
		if collectedStart(fs) >= fs.Offset || keptBackWhole(fs) {
			return true
		}
		if !fs.NoSessionSnapshot {
			s.advanceSessionSnapshot(ctx, fs, path, fs.Offset, projectHash, nil)
		}
	}
	st := fs.SessionSnapshot
	if st == nil {
		if !fs.NoSessionSnapshot && !fs.RebuildDeclined {
			return false // the collected part could not be read, which is logged
		}
		st = &SessionSnapshotState{Snapshot: snapshotToCarry(fs), ProjectHash: projectHash}
		fs.SessionSnapshot = st
		fs.NoSessionSnapshot = false
	}
	st.NewestRecordAt = later(st.NewestRecordAt, gs.ActiveAt)
	if old := st.Snapshot.Git; old != nil {
		same := counts
		same.ChangedAt = old.ChangedAt
		if same == *old {
			counts = *old
		}
	}
	next := st.Snapshot.clone()
	next.Git = &counts
	if gs.Truncated {
		next.markTruncated("git")
	}
	next.fit()
	if !sameSnapshot(st.Snapshot, next) {
		st.Revision++
		st.ObservedAt = now.UTC()
	}
	st.Snapshot = next
	return true
}

// ---- jobs: what counting asks the filesystem and git, off the flush's goroutine ----

// gitJobs is the work counting runs in goroutines of its own, the way
// projectrule.RunBounded runs a rule scan. A flush waits for a job only within
// sessionGitCountBudget, and a job it stops waiting for is abandoned: Go cannot
// stop a goroutine parked in a syscall, and a hung network mount parks it in
// one for as long as the mount stays hung. So that abandoned jobs cannot pile
// up, a job is not started while one under the same key -- the same session's
// paths, the same work tree -- is still running. A job only computes: the
// state, and the caches, change on the flush's goroutine, from the results it
// receives.
type gitJobs struct {
	mu      sync.Mutex
	running map[string]bool
}

// startGitJob runs compute in a goroutine of its own unless a job under key is
// still running, and reports whether it started one. The result goes to
// deliver once the key is free again, so whoever receives it can start the next
// job under the key at once.
func startGitJob[T any](j *gitJobs, key string, compute func() T, deliver func(T)) bool {
	j.mu.Lock()
	if j.running[key] {
		j.mu.Unlock()
		return false
	}
	if j.running == nil {
		j.running = map[string]bool{}
	}
	j.running[key] = true
	j.mu.Unlock()
	go func() {
		v := compute()
		j.mu.Lock()
		delete(j.running, key)
		j.mu.Unlock()
		deliver(v)
	}()
	return true
}

// gitAnswer is what was found about a directory, and when: the work tree it
// lies in (rootCache), the top level git names for a work tree
// (Syncer.gitTops), or a work tree's repository (Syncer.gitRepos). Each is kept
// for metaTTL.
type gitAnswer struct {
	value string
	at    time.Time
}

// locateResult is where a session's edited paths lie: each distinct path once,
// after its symlinks are resolved. noAnswer says the session's job did not come
// back in time, or one from an earlier flush is still running, and nothing of
// the session is known.
type locateResult struct {
	paths    []locatedPath
	noAnswer bool
}

// locatedPath is one edited path: the work tree it lies in, "" for none, and
// the path relative to it -- in NFC, slash-separated -- "" when git cannot
// place it there.
type locatedPath struct {
	root string
	rel  string
}

// rootCache is the work tree each directory lies in, as findWorkTree found it,
// for metaTTL (prune, at the start of each count). Jobs fill it as they go,
// under its lock, so a search the budget cut off is taken up on the next try
// where it stopped, not begun again.
type rootCache struct {
	mu sync.Mutex
	m  map[string]gitAnswer
}

func (c *rootCache) get(dir string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.m[dir]
	return a.value, ok
}

// put records root for each of dirs, as found at now, except where the cache
// holds a newer answer: a job an earlier flush gave up on can finish long
// after a later one.
func (c *rootCache) put(dirs []string, root string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]gitAnswer{}
	}
	for _, dir := range dirs {
		if a, ok := c.m[dir]; ok && a.at.After(now) {
			continue
		}
		c.m[dir] = gitAnswer{value: root, at: now}
	}
}

// prune drops the entries metaTTL old.
func (c *rootCache) prune(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for dir, a := range c.m {
		if now.Sub(a.at) >= metaTTL {
			delete(c.m, dir)
		}
	}
}

// locate finds where the due sessions' edited paths lie, a job per session,
// gitCountWorkers at a time, and returns what came back before ctx ended. A
// session whose job did not come back in time, or whose job from an earlier
// flush is still running, has no answer; one the budget ran out before reaching
// is not in the result.
func (s *Syncer) locate(ctx context.Context, now time.Time, due []*dueSession) map[string]locateResult {
	s.gitRoots.prune(now)
	type result struct {
		path string
		loc  locateResult
	}
	// Buffered for every job, so one abandoned never blocks on its send.
	results := make(chan result, len(due))
	out := map[string]locateResult{}
	started := map[string]bool{}
	next, running := 0, 0
	for {
		for running < gitCountWorkers && next < len(due) && ctx.Err() == nil {
			d := due[next]
			next++
			edited := slices.Clone(d.gs.Edited)
			if startGitJob(&s.gitJobs, "locate\x00"+d.path,
				func() locateResult { return locateEdits(ctx, edited, now, &s.gitRoots) },
				func(loc locateResult) { results <- result{d.path, loc} }) {
				running++
				started[d.path] = true
			} else {
				out[d.path] = locateResult{noAnswer: true}
			}
		}
		if running == 0 {
			return out
		}
		select {
		case r := <-results:
			running--
			out[r.path] = r.loc
		case <-ctx.Done():
			for path := range started {
				if _, ok := out[path]; !ok {
					out[path] = locateResult{noAnswer: true}
				}
			}
			return out
		}
	}
}

// locateEdits is a locate job: it resolves each edited path's symlinks, and
// finds the work tree its directory lies in (findWorkTree) and its path in it.
// Paths, roots and the rels between them compare in NFC, so two spellings of
// one name are one path; the filesystem and git are given them as written. A
// path inside a .git directory is no file of the work tree, which git status
// never reports: it is unresolved.
func locateEdits(ctx context.Context, edited []string, now time.Time, cache *rootCache) locateResult {
	var res locateResult
	seen := map[string]bool{}
	gitDir := ".git" + string(filepath.Separator)
	for _, path := range edited {
		resolved := resolvedPath(path)
		key := norm.NFC.String(resolved)
		if seen[key] {
			continue
		}
		seen[key] = true
		root, ok := findWorkTree(ctx, filepath.Dir(resolved), now, cache)
		if !ok {
			return locateResult{noAnswer: true}
		}
		lp := locatedPath{root: root}
		if root != "" {
			if rel, err := filepath.Rel(norm.NFC.String(root), key); err == nil && rel != ".git" && !strings.HasPrefix(rel, gitDir) {
				lp.rel = filepath.ToSlash(rel)
			}
		}
		res.paths = append(res.paths, lp)
	}
	return res
}

// findWorkTree returns the work tree dir lies in: the nearest directory, dir
// itself or one above it, that holds a .git -- a directory, or the file a
// linked worktree or a submodule has -- or "" when none does. A directory that
// does not exist, removed since its file was edited, is passed like any other.
// It finds it with a stat for each directory it passes, not a git process for
// each: a session edits files in up to hundreds of directories, and on a slow
// machine a process for each did not fit in the budget. Every directory it
// passes goes into cache with the answer once it has one, and a directory the
// cache holds ends the search. It reports false when ctx ended first.
//
// For an ordinary repository, a linked worktree and a submodule it names the
// work tree git rev-parse --show-toplevel names. Where git names another, the
// paths are counted as git sees them all the same, as inspect checks each
// work tree found here against rev-parse, once per metaTTL, and asks status
// where git says: a .git directory git does not take for a repository -- an
// empty one inside another work tree, whose status git gives for the outer
// one -- core.worktree pointing the repository at a work tree elsewhere, and
// GIT_DIR or GIT_WORK_TREE in this process's environment. A directory whose
// .git git cannot use at all -- no repository, an owner safe.directory
// refuses, a linked worktree whose gitdir is gone -- git answers about with a
// failure, and its paths count as in no work tree. One difference stays: it
// searches past GIT_CEILING_DIRECTORIES and mount points, where git asked from
// a file's own directory would stop, so it places files git would not.
func findWorkTree(ctx context.Context, dir string, now time.Time, cache *rootCache) (string, bool) {
	var passed []string
	root := ""
	for d := dir; ; {
		if r, ok := cache.get(d); ok {
			root = r
			break
		}
		if ctx.Err() != nil {
			return "", false
		}
		passed = append(passed, d)
		if _, err := statFn(filepath.Join(d, ".git")); err == nil {
			root = d
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	cache.put(passed, root, now)
	return root, true
}

// resolvedPath is path with the symlinks of its longest existing part
// resolved: the whole of it when the file exists. A Write line is read before
// its file exists while its permission prompt waits, and a file can be removed
// after its line is read; their directories still lie in the work tree they
// did.
func resolvedPath(path string) string {
	rest := ""
	for p := path; ; {
		if real, err := evalSymlinksFn(p); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// rootResult is what an inspect job found for one work tree findWorkTree named
// (its root). top is the top level git names for it, asked of git when checked
// is set; noTree says git answered that it is no work tree git can use.
// repositoryID is its repository, when the job had to ask git for it
// (lookedUp); a work tree options.collect_repository_prefixes leaves out has no
// status asked. uncommitted holds the paths, relative to the root, that git
// status reports, and unplaced those that lie outside top.
type rootResult struct {
	top          string
	checked      bool
	noTree       bool
	repositoryID string
	lookedUp     bool
	uncommitted  map[string]bool
	unplaced     map[string]bool
	failed       bool
	noAnswer     bool
}

// inspect asks each work tree the located sessions' paths lie in for the status
// of those paths, a job per work tree, gitCountWorkers at a time, and returns
// what came back before ctx ended. A work tree whose job did not come back in
// time, or whose job from an earlier flush is still running, has no answer; one
// the budget ran out before reaching is not in the result.
//
// The top level git names for each work tree is kept for metaTTL (gitTops),
// as git answered it. A work tree the allowlist leaves out is not asked about.
// It is judged by the rule collects applies to a session's directory, on a
// cache of its own (gitRepos, collectsRoot): the repository the record sync's
// cache holds for a directory decides which records are collected, and an
// answer counting got from a git that timed out must not decide that for an
// hour.
func (s *Syncer) inspect(ctx context.Context, now time.Time, located map[string]locateResult) map[string]rootResult {
	rels := map[string][]string{}
	for _, loc := range located {
		if loc.noAnswer {
			continue
		}
		for _, p := range loc.paths {
			if p.root == "" {
				continue
			}
			if p.rel != "" && !slices.Contains(rels[p.root], p.rel) {
				rels[p.root] = append(rels[p.root], p.rel)
			} else if _, ok := rels[p.root]; !ok {
				rels[p.root] = nil
			}
		}
	}
	roots := make([]string, 0, len(rels))
	for root := range rels {
		roots = append(roots, root)
	}
	sort.Strings(roots)

	prefixes := s.collectPrefixes
	for _, cache := range []map[string]gitAnswer{s.gitRepos, s.gitTops} {
		for root, r := range cache {
			if now.Sub(r.at) >= metaTTL {
				delete(cache, root)
			}
		}
	}
	out := map[string]rootResult{}
	type result struct {
		root string
		res  rootResult
	}
	results := make(chan result, len(roots))
	started := map[string]bool{}
	next, running := 0, 0
	for {
		for running < gitCountWorkers && next < len(roots) && ctx.Err() == nil {
			root := roots[next]
			next++
			top, checked := s.gitTops[root]
			if checked && top.value == "" {
				out[root] = rootResult{noTree: true}
				continue
			}
			lookUp := false
			if len(prefixes) > 0 {
				if allowed, known := s.collectsRoot(root); !known {
					lookUp = true
				} else if !allowed {
					out[root] = rootResult{}
					continue
				}
			}
			rootRels := rels[root]
			if startGitJob(&s.gitJobs, "root\x00"+root,
				func() rootResult { return inspectRoot(ctx, root, rootRels, top.value, lookUp, prefixes) },
				func(res rootResult) { results <- result{root, res} }) {
				running++
				started[root] = true
			} else {
				out[root] = rootResult{noAnswer: true}
			}
		}
		if running == 0 {
			return out
		}
		select {
		case r := <-results:
			running--
			if r.res.checked {
				if s.gitTops == nil {
					s.gitTops = map[string]gitAnswer{}
				}
				s.gitTops[r.root] = gitAnswer{value: r.res.top, at: now}
			}
			if r.res.lookedUp {
				if s.gitRepos == nil {
					s.gitRepos = map[string]gitAnswer{}
				}
				s.gitRepos[r.root] = gitAnswer{value: r.res.repositoryID, at: now}
			}
			out[r.root] = r.res
		case <-ctx.Done():
			for root := range started {
				if _, ok := out[root]; !ok {
					out[root] = rootResult{noAnswer: true}
				}
			}
			return out
		}
	}
}

// inspectRoot is an inspect job for the work tree findWorkTree named root,
// whose rels are relative to it. Unless top is known from the cache, it asks
// git for the work tree's top level, once: git may name another than root, and
// status is then asked there, of the paths taken relative to it, and its
// answer taken back to the rels. It asks for the repository when the allowlist
// needs it and the cache had none, and, when the allowlist lets the work tree
// be collected, for the status of rels.
func inspectRoot(ctx context.Context, root string, rels []string, top string, lookUp bool, prefixes []string) rootResult {
	var res rootResult
	if top == "" {
		t, err := gitctx.WorkTreeRoot(ctx, root)
		if err != nil {
			return rootResult{noAnswer: true}
		}
		top, res.checked = t, true
		if top == "" {
			res.noTree = true
			return res
		}
	}
	res.top = top
	if lookUp {
		id, err := gitctx.RepositoryID(ctx, top)
		if err != nil {
			return rootResult{noAnswer: true}
		}
		res.repositoryID, res.lookedUp = id, true
		if !gitctx.AllowsRepository(id, prefixes) {
			return res
		}
	}
	// A path outside git's work tree, or the work tree itself -- a pathspec of
	// "." would ask about all of it -- is no file git can place.
	base, topNFC := norm.NFC.String(root), norm.NFC.String(top)
	asked := make(map[string]string, len(rels))
	for _, rel := range rels {
		fromTop, err := filepath.Rel(topNFC, filepath.Join(base, filepath.FromSlash(rel)))
		if err != nil || fromTop == "." || fromTop == ".." || strings.HasPrefix(fromTop, ".."+string(filepath.Separator)) {
			if res.unplaced == nil {
				res.unplaced = map[string]bool{}
			}
			res.unplaced[rel] = true
			continue
		}
		asked[filepath.ToSlash(fromTop)] = rel
	}
	if len(asked) == 0 {
		return res
	}
	paths, err := gitctx.UncommittedPaths(ctx, top, pathspecsOf(slices.Sorted(maps.Keys(asked))))
	switch {
	case err == nil:
		res.uncommitted = make(map[string]bool, len(paths))
		for path := range paths {
			if rel, ok := asked[norm.NFC.String(path)]; ok {
				res.uncommitted[rel] = true
			}
		}
	case gitctx.NoAnswer(err):
		res.noAnswer = true
	default:
		res.failed = true
	}
	return res
}

// pathspecsOf is rels as git status is asked about them: each in NFC, and in
// NFD too where that differs, so a name written either way on disk is found.
// macOS gives git NFC back; elsewhere a name is the bytes it was written as.
func pathspecsOf(rels []string) []string {
	specs := make([]string, 0, len(rels))
	for _, rel := range rels {
		specs = append(specs, rel)
		if d := norm.NFD.String(rel); d != rel {
			specs = append(specs, d)
		}
	}
	return specs
}

// collectsRoot judges a work tree by the rule collects applies to a working
// directory, from counting's own cache of repositories (gitRepos) alone, which
// inspect keeps to metaTTL. known is false when that cache has no entry for it.
func (s *Syncer) collectsRoot(root string) (allowed, known bool) {
	r, ok := s.gitRepos[root]
	if !ok {
		return false, false
	}
	return gitctx.AllowsRepository(r.value, s.collectPrefixes), true
}

// countsOf takes a located session's counts from the work trees' statuses.
// reached is false when a work tree it needs was not reached in this flush's
// budget, answered false when git gave no answer about one; either way there
// are no counts. With options.collect_repository_prefixes set, a path in a
// repository it leaves out, or in none, is not counted at all.
func (s *Syncer) countsOf(loc locateResult, inspected map[string]rootResult) (counts SnapshotGit, reached, answered bool) {
	reached = true
	for _, p := range loc.paths {
		if p.root == "" {
			continue
		}
		r, ok := inspected[p.root]
		if ok && r.noAnswer {
			return SnapshotGit{}, true, false
		}
		reached = reached && ok
	}
	if !reached {
		return SnapshotGit{}, false, true
	}
	listed := len(s.collectPrefixes) > 0
	repos := map[string]bool{}
	for _, p := range loc.paths {
		r := inspected[p.root]
		if p.root == "" || r.noTree {
			if !listed {
				counts.EditedFiles++
				counts.Unresolved++
			}
			continue
		}
		if listed {
			if allowed, known := s.collectsRoot(p.root); !known {
				return SnapshotGit{}, false, true
			} else if !allowed {
				continue
			}
		}
		counts.EditedFiles++
		repos[cmp.Or(r.top, p.root)] = true
		switch {
		case p.rel == "" || r.failed || r.unplaced[p.rel]:
			counts.Unresolved++
		case r.uncommitted[p.rel]:
			counts.UncommittedEditedFiles++
		}
	}
	counts.Repositories = len(repos)
	return counts, true, true
}
