package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/samefolder"
	"cctrace/internal/syncer"
)

// cursorKind is one kind of source of a job, with the state its folders are read
// through and how its syncer joins a folder.
type cursorKind struct {
	kind      string
	stateFile string
	state     *syncer.State
	dirs      []string
	// join skips what a folder holds at t0 that the state has not seen.
	join func(ctx context.Context, dir string, t0 time.Time) (int, error)
	// transcripts: the state holds Claude transcripts, whose snapshots are rebuilt
	// from what a cursor has passed.
	transcripts bool
}

// arrangeCursors settles, before the first pass, where reading each folder of a
// state begins. A folder the state reads already needs nothing. A folder it has
// not seen is marked, and:
//
//   - handed over by another profile whose state is readable: the profile goes on
//     from that state's cursor (adopted);
//   - handed over without one: it begins now, skipping what is on disk (joined);
//   - neither: the state's old rule stays. A state made now skips what is on disk
//     at the first pass; a folder added to an old state is read from its start.
//
// What a hand-over arranges is saved before this returns, since the supervisor
// takes the lease as proof that it is. Only the hand-overs of folders among
// c.dirs are taken (tookOver); one whose folder the job does not read this lease
// is left to a later one. Anything else waits for the pass's own
// save, except in a state that exists: there it is saved at once.
func (j *profileJob) arrangeCursors(ctx context.Context, alloc localdaemon.Allocation, c cursorKind) error {
	handoffs := map[string]localdaemon.Handoff{}
	for _, h := range alloc.Handoffs {
		if dir, ok := strings.CutPrefix(h.Line, c.kind+":"); ok {
			handoffs[dir] = h
		}
	}
	freshState := c.state.IsNew()
	now := time.Now()
	var unmarked []string
	handedOver, changed := false, false
	for _, dir := range c.dirs {
		mark, marked := c.state.SourceMark(dir)
		h, handed := handoffs[dir]
		switch {
		case handed && marked && mark.Handoff == h.ID && mark.Via != syncer.SourceJoining:
			// Done by an earlier lease; the daemon had not cleared it yet.
			j.tookOver = append(j.tookOver, h)
		case handed:
			if err := j.takeOver(ctx, c, dir, h, mark, marked, alloc.AfterBlock && freshState); err != nil {
				return err
			}
			j.tookOver = append(j.tookOver, h)
			handedOver, changed = true, true
		case !marked:
			unmarked = append(unmarked, dir)
		}
	}
	for _, dir := range unmarked {
		if handedOver && freshState {
			// Saving what the hand-over arranged ends the first-run window, which
			// would let this folder's history be collected. Skip it now instead.
			if _, err := c.join(ctx, dir, now); err != nil {
				return err
			}
		}
		c.state.MarkSource(dir, syncer.SourceMark{Since: now, Via: syncer.SourceInitial, AfterBlock: alloc.AfterBlock && freshState})
		changed = true
	}
	if len(c.state.Sources) > 0 {
		before := len(c.state.Sources)
		c.state.ForgetSourcesExcept(c.dirs)
		changed = changed || len(c.state.Sources) != before
	}
	if handedOver || (changed && !freshState) {
		return c.state.Save()
	}
	return nil
}

// takeOver brings one folder, handed over by h, into the state: the previous
// holder's cursor if it is readable, then a join for whatever the state still
// does not know. The join's start is recorded first, so a lease that is
// interrupted finishes it with the same start.
func (j *profileJob) takeOver(ctx context.Context, c cursorKind, dir string, h localdaemon.Handoff, mark syncer.SourceMark, marked, afterBlock bool) error {
	t0 := time.Now()
	from := ""
	if marked && mark.Via == syncer.SourceJoining && mark.Handoff == h.ID {
		t0, from = mark.Since, mark.From
	} else {
		var sourceHome []string
		if c.kind == "claude" {
			sourceHome = []string{dir}
		}
		prev, origin, declined := previousHolderState(h.From, c.stateFile, sourceHome...)
		if origin == syncer.StateValid {
			c.state.AdoptSource(prev, syncer.Adoption{Path: adoptionPath(c.kind, dir), Home: adoptionHome(dir), DeclineRebuild: declined && c.transcripts})
			from = h.From
		}
		c.state.MarkSource(dir, syncer.SourceMark{Since: t0, Via: syncer.SourceJoining, From: from, Handoff: h.ID, AfterBlock: afterBlock})
		if err := c.state.Save(); err != nil {
			return err
		}
	}
	if _, err := c.join(ctx, dir, t0); err != nil {
		return fmt.Errorf("join a source taken over from another profile: %w", err)
	}
	via := syncer.SourceJoined
	if from != "" {
		via = syncer.SourceAdopted
	}
	c.state.MarkSource(dir, syncer.SourceMark{Since: t0, Via: via, From: from, Handoff: h.ID, AfterBlock: afterBlock || (marked && mark.AfterBlock)})
	return nil
}

// previousHolderState reads the state the profile that held a source left, and
// says whether snapshots rebuilt from what it passed are to be declined: its
// filter may have kept lines back that nothing recorded, or is unknown because
// the profile is gone. A profile that cannot be found, or whose file is missing,
// damaged or unreadable, has no cursor to give.
func previousHolderState(from, stateFile string, claudeHome ...string) (state *syncer.State, origin syncer.StateOrigin, declineRebuild bool) {
	if from == "" {
		return nil, syncer.StateMissing, true
	}
	registry := profile.Registry{Root: profile.DefaultDir()}
	name := strings.TrimPrefix(from, "named:")
	entry, err := registry.Load(name)
	if err != nil {
		entry, err = registry.LoadRetired(name)
	}
	if err != nil || entry.Profile == nil {
		return nil, syncer.StateMissing, true
	}
	declineRebuild = len(entry.Profile.Options.CollectRepositoryPrefixes) > 0
	if len(claudeHome) > 0 {
		stateFile = claudeStateFile(entry.Profile, claudeHome[0])
	}
	state, origin, err = syncer.LoadStateStrict(filepath.Join(entry.RuntimeDir, stateFile))
	if len(claudeHome) == 0 && origin == syncer.StateMissing && strings.HasPrefix(stateFile, "claude-") {
		state, origin, err = syncer.LoadStateStrict(filepath.Join(entry.RuntimeDir, "sync-state.json"))
	}
	if err != nil {
		return nil, syncer.StateCorrupt, declineRebuild
	}
	return state, origin, declineRebuild
}

// adoptionPath maps a file of the previous holder's state to the same file under
// dir, when the new folder's collector reads it. The folders may be one folder
// under two spellings, or nested: only a file the collector of dir finds is
// taken, under dir's own spelling, so the keys are the ones its passes will use.
func adoptionPath(kind, dir string) func(string) (string, bool) {
	subdirs := localdaemon.ScanSubdirs(kind)
	below := map[string]string{} // directory of a previous key -> its path relative to dir
	return func(prev string) (string, bool) {
		parent, name := filepath.Split(prev)
		parent = filepath.Clean(parent)
		rel, seen := below[parent]
		if !seen {
			r, ok := samefolder.Relative(dir, parent)
			if ok {
				rel = r
			} else {
				rel = "\x00"
			}
			below[parent] = rel
		}
		if rel == "\x00" {
			return "", false
		}
		rel = filepath.Join(rel, name)
		if !slices.ContainsFunc(subdirs, func(sub string) bool { return rel == sub || strings.HasPrefix(rel, sub+string(filepath.Separator)) }) {
			return "", false
		}
		return filepath.Join(dir, rel), true
	}
}

// adoptionHome maps the home an account was observed for in the previous state
// to dir, if it is the same folder.
func adoptionHome(dir string) func(string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	return func(prev string) (string, bool) {
		if _, ok := samefolder.Relative(dir, prev); !ok {
			return "", false
		}
		if rel, _ := samefolder.Relative(dir, prev); rel != "." {
			return "", false
		}
		return abs, true
	}
}
