package syncer

import (
	"context"
	"log"
	"slices"
	"time"

	"cctrace/internal/gitctx"
	"cctrace/internal/sessionlog"
	"cctrace/internal/store"
)

// Reasons a run of records is held: not sent, and not passed either.
const (
	// HeldReasonGitUncertain: the lookup of the run's cwd did not hear git's
	// answer (timeout, broken gitfile, permission denied, dubious ownership).
	HeldReasonGitUncertain = "git-uncertain"
	// HeldReasonTransitionPending: the cwd holds another repository than the
	// one recorded, and the session files' unsent bytes could not be marked
	// yet (see Syncer.observeIdentity).
	HeldReasonTransitionPending = "transition-pending"
	// HeldReasonCWDUnknown: the run's records carry no cwd and the cwd the
	// consumed bytes ended in could not be found (see FileState.CWDUnknown).
	HeldReasonCWDUnknown = "cwd-unknown"
	// HeldReasonRepositoryLostGrace: a cwd that resolved to a repository now
	// certainly reports none, and repositoryLostGrace has not yet passed.
	HeldReasonRepositoryLostGrace = "repository-lost-grace"
)

// heldExpiry bounds a hold. A cwd whose run has been seen failing this long is
// taken as an unknown repository -- not allowed -- and its records are
// dropped, so one broken checkout cannot stall the file, and every allowed run
// behind it, forever. Var, not const, so tests can cross it in a few passes.
var heldExpiry = 24 * time.Hour

// tailVerdict is what happens to one record of a pending tail.
type tailVerdict int

const (
	// verdictSend: sent under its cwd's current identity.
	verdictSend tailVerdict = iota
	// verdictConsume: not sent, and the offset moves past it -- its
	// repository is, or may have been, outside the allowlist.
	verdictConsume
	// verdictHold: its repository could not be established. Neither it nor
	// anything behind it is consumed.
	verdictHold
	// verdictExpired: held for heldExpiry and now dropped: consumed, and
	// recorded as a loss when the offset passes it.
	verdictExpired
)

// syncJudgedTail sends a pending tail under a repository allowlist.
//
// Without an allowlist a tail is one request under its first cwd's identity.
// Here every record is judged by the repository of its own cwd, so a session
// that wandered into an excluded repository leaves those records behind, and
// what is sent goes out grouped by identity, each group under its own envelope
// (see sendJudged).
//
// The order matters. The tail is scanned first; then every cwd in it is looked
// up, including the ones behind a run that will hold; only then is anything
// judged. A lookup is where a replaced repository is noticed and the unsent
// bytes are marked (observeIdentity), and this file's own tail is among them:
// judging before looking would send the lines that made the syncer look.
func (s *Syncer) syncJudgedTail(ctx context.Context, filePath, projectHash string, records []*sessionlog.Record, skipped []sessionlog.SkippedSpan, offset, newOffset int64) (int, error) {
	cwds, ok := s.tailCWDs(filePath, offset, records, skipped)
	if !ok {
		// Nothing in the file says where it was written yet. Not consumed, and
		// not a hold: the next line with a cwd settles it.
		return 0, nil
	}
	metas := make(map[string]*projectMeta)
	for _, cwd := range cwds {
		if cwd != "" && metas[cwd] == nil {
			metas[cwd] = s.freshProjectMeta(cwd)
		}
	}

	// Every record is judged, the ones behind a hold too: each held cwd has
	// its own clock, and they all run while the file waits.
	taints := s.state.Taints[filePath]
	verdicts := make([]tailVerdict, len(records))
	held := make(map[string]string)
	for i, r := range records {
		var reason string
		if verdicts[i], reason = s.judgeRecord(cwds[i], r.Offset, metas[cwds[i]], taints); verdicts[i] == verdictHold {
			held[cwds[i]] = reason
		}
	}
	now := nowFn()
	expired := s.tickHolds(filePath, held, now)

	// The tail is consumed up to its first record still held and no further:
	// the offset is one position, so what lies behind a hold waits with it.
	end := len(records)
	for i := range records {
		if verdicts[i] != verdictHold {
			continue
		}
		if !expired[cwds[i]] {
			end = i
			break
		}
		verdicts[i] = verdictExpired
	}
	if end < len(records) {
		// Left alone for the interval: not scanned, its cwds not looked up. A
		// hung git costs commandTimeout per lookup and the watch loop polls
		// every second.
		if _, holding := s.heldRetryAt[filePath]; !holding {
			log.Printf("[syncer] %s: holding unsent records (%s); retrying every %s, dropped after %s", filePath, held[cwds[end]], lookupRetryInterval, heldExpiry)
		}
		s.heldRetryAt[filePath] = now.Add(lookupRetryInterval)
	} else {
		delete(s.heldRetryAt, filePath)
	}

	return s.sendJudged(ctx, filePath, projectHash, judgedTail{
		records: records, skipped: skipped, cwds: cwds, verdicts: verdicts, metas: metas,
		end: end, offset: offset, endOffset: offsetOf(records, end, newOffset), now: now,
	})
}

// tickHolds brings filePath's record of what is holding its tail up to date --
// held maps each cwd holding now to its reason -- and returns the cwds whose
// hold has been seen failing for heldExpiry.
//
// A cwd that keeps holding for the same reason keeps its clock, advanced by
// the time since its last retry, capped at two retry intervals: the daemon
// being stopped, or the machine asleep, is not time spent retrying. A cwd that
// stopped holding is forgotten, and a changed reason starts over. The change
// is saved with the pass (see flushState) rather than here, so a pass over
// many held files writes the state once.
func (s *Syncer) tickHolds(filePath string, held map[string]string, now time.Time) map[string]bool {
	fs := s.state.Files[filePath]
	if len(held) == 0 {
		if fs != nil && len(fs.Held) > 0 {
			fs.Held = nil
			s.stateDirty = true
		}
		return nil
	}
	if fs == nil {
		fs = &FileState{}
		s.state.Files[filePath] = fs
	}
	runs := make(map[string]HeldRun, len(held))
	expired := make(map[string]bool)
	for cwd, reason := range held {
		run, ok := fs.Held[cwd]
		if !ok || run.Reason != reason {
			run = HeldRun{Since: now, Reason: reason}
		} else if d := now.Sub(run.LastFailedAt); d > 0 {
			run.Observed += min(d, 2*lookupRetryInterval)
		}
		run.LastFailedAt = now
		runs[cwd] = run
		expired[cwd] = run.Observed >= heldExpiry
	}
	fs.Held = runs
	s.stateDirty = true
	return expired
}

// noteExpired is called by the save that moves the offset past
// tail.records[from:upTo]. It records the expired runs among them as the loss
// they have just become, and forgets every hold with no record left behind the
// offset: the next failure of that cwd starts a new clock.
//
// Here and nowhere earlier. A run found expired is only dropped once the
// offset passes it, and until then -- a request in front of it failed, the
// process stopped -- git can still answer and nothing is lost.
func (s *Syncer) noteExpired(filePath string, fs *FileState, tail judgedTail, from, upTo int) {
	for i := from; i < upTo; i++ {
		if tail.verdicts[i] != verdictExpired {
			continue
		}
		lost := HoldExpired{At: tail.now, Reason: fs.Held[tail.cwds[i]].Reason, CWD: tail.cwds[i]}
		if fs.HoldExpired == nil || *fs.HoldExpired != lost {
			fs.HoldExpired = &lost
			log.Printf("[syncer] %s: dropped records held over %s (%s)", filePath, heldExpiry, lost.Reason)
		}
	}
	for cwd := range fs.Held {
		if !tail.holdsFrom(upTo, cwd) {
			delete(fs.Held, cwd)
		}
	}
	if len(fs.Held) == 0 {
		fs.Held = nil
	}
}

// judgeRecord decides one record from its cwd, where its line starts, this
// pass's lookup of that cwd, and the file's taints.
//
// A record below a taint of its cwd may have been written under either the
// repository recorded then or the current one (see Taint), so it is sent only
// when both are allowed -- every one of them, when several marks are still
// above it. There is no sending it under the previous identity: nothing says
// it belongs there either.
func (s *Syncer) judgeRecord(cwd string, recordOffset int64, m *projectMeta, taints []Taint) (tailVerdict, string) {
	if cwd == "" {
		return verdictHold, HeldReasonCWDUnknown
	}
	if m.hold != "" {
		return verdictHold, m.hold
	}
	if !gitctx.AllowsRepository(m.repositoryID, s.collectPrefixes) {
		return verdictConsume, ""
	}
	for _, t := range taints {
		if t.CWD == cwd && recordOffset < t.Until && !gitctx.AllowsRepository(t.PrevRepositoryID, s.collectPrefixes) {
			return verdictConsume, ""
		}
	}
	return verdictSend, ""
}

// tailCWDs returns the cwd each record belongs to: its own, or the one before
// it when it carries none. ok is false when the tail starts at the beginning
// of the file and no record in it carries a cwd yet.
//
// What the first records continue from depends on where the tail starts.
//   - At the beginning of the file there is nothing before them, so they take
//     the first cwd that follows. Most real session files start this way.
//   - Otherwise they continue from the cwd the consumed bytes ended in
//     (FileState.CWD). A state written before that was recorded has none, and
//     it is read back from the file once.
//   - When that cwd could not be found (FileState.CWDUnknown) they get "", and
//     are held rather than guessed at; the head of the file is not consulted,
//     because it is where the session started, not where it is.
//
// A skipped oversized line breaks the chain wherever it is: it yielded no
// record, and may be the line where the session changed cwd. The records after
// it get "" until one names its cwd, and at the beginning of a file the
// records in front of it do too -- the first cwd that follows them may have
// been in that line.
func (s *Syncer) tailCWDs(filePath string, offset int64, records []*sessionlog.Record, skipped []sessionlog.SkippedSpan) (cwds []string, ok bool) {
	carry := ""
	if offset == 0 {
		first := slices.IndexFunc(records, func(r *sessionlog.Record) bool { return r.CWD != "" })
		skippedFirst := len(skipped) > 0 && (first < 0 || skipped[0].Start < records[first].Offset)
		if !skippedFirst {
			if first < 0 {
				return nil, false
			}
			carry = records[first].CWD
		}
	} else if fs := s.state.Files[filePath]; fs != nil {
		if fs.CWD == "" && !fs.CWDUnknown {
			s.recoverLastCWD(fs, filePath, offset)
			s.saveState()
		}
		carry = fs.CWD
	}
	cwds = make([]string, len(records))
	for i, r := range records {
		for len(skipped) > 0 && skipped[0].Start < r.Offset {
			carry, skipped = "", skipped[1:]
		}
		if r.CWD != "" {
			carry = r.CWD
		}
		cwds[i] = carry
	}
	return cwds, true
}

// judgedTail is a pending tail with the verdict on each of its records.
type judgedTail struct {
	records []*sessionlog.Record
	// skipped is where the scan passed over oversized lines.
	skipped  []sessionlog.SkippedSpan
	cwds     []string
	verdicts []tailVerdict
	metas    map[string]*projectMeta
	// end is how many records are consumed this pass: the index of the first
	// record still held, or len(records).
	end int
	// offset is where the tail starts and endOffset where records[:end] ends.
	offset, endOffset int64
	// now is when the tail was judged.
	now time.Time
}

// holdsFrom reports whether a record of cwd at or after index from is held or
// was dropped as an expired hold.
func (t judgedTail) holdsFrom(from int, cwd string) bool {
	for i := from; i < len(t.records); i++ {
		if t.cwds[i] == cwd && (t.verdicts[i] == verdictHold || t.verdicts[i] == verdictExpired) {
			return true
		}
	}
	return false
}

// skippedBetween reports whether the scan skipped an oversized line that
// starts at or after from and ends at or before to.
func (t judgedTail) skippedBetween(from, to int64) bool {
	for _, span := range t.skipped {
		if span.Start >= from && span.End <= to {
			return true
		}
	}
	return false
}

// sendJudged sends the records of tail.records[:tail.end] whose verdict is
// send and moves the offset past all of them.
//
// Consecutive sent records sharing an identity form one group, and a change of
// identity starts another with its own envelope. A record is not stamped
// with a repository inside a foreign envelope: the server fills a record's
// blank fields from the envelope, so a run at a repository root on a detached
// HEAD (no subpath, no branch) would inherit the other run's, and two
// worktrees of one remote would share whichever HEAD came first.
//
// The offset follows each group that gets through as a whole, to the end of
// what it carried, so a failure in a later group does not send the earlier
// groups again. A group is not one HTTP request: sendRecords sends it in
// batches of batchSize and halves a batch the server refuses as too large. A
// failure inside a group leaves the offset at the group's start, and the
// batches of it that did get through are sent again with it (the server
// deduplicates them, as it does for a tail sent without an allowlist).
//
// The records between two groups -- the consumed ones -- are passed with the
// group after them, or with the end of the tail: a group that fails leaves
// them, and the verdict on them, for the next pass.
func (s *Syncer) sendJudged(ctx context.Context, filePath, projectHash string, tail judgedTail) (int, error) {
	convert := s.newRecordConverter(filePath, projectHash)
	skill := s.activeAttributionSkill(filePath)
	consumed := tail.records[:tail.end]
	position, passed := tail.offset, 0
	advance := func(upTo int, skill string) {
		to := offsetOf(consumed, upTo, tail.endOffset)
		if to == position {
			return
		}
		// What the bytes up to the new offset end in: the cwd of the last
		// record passed, unless that is unknown or an oversized line was
		// skipped behind it.
		cwd, cwdUnknown := "", tail.skippedBetween(position, to)
		if upTo > 0 {
			cwd = tail.cwds[upTo-1]
			cwdUnknown = cwd == "" || tail.skippedBetween(consumed[upTo-1].Offset+1, to)
		}
		// Passing no record moves nothing over an unreadable line in front of
		// a held first record: an offset past the start of the file with no
		// cwd before it would stop the leading lines from taking the first cwd
		// that follows (see tailCWDs). A skipped oversized line is passed --
		// it need not be read again every retry -- and recorded as the break
		// in the chain it is.
		if upTo == 0 && !cwdUnknown {
			return
		}
		fs := s.advanceOffset(filePath, to, cwd, cwdUnknown)
		fs.LastAttributionSkill = skill
		for _, v := range tail.verdicts[passed:upTo] {
			if v == verdictConsume || v == verdictExpired {
				// The file now holds lines that were kept back, for good.
				fs.ExcludedSeen = true
			}
		}
		s.noteExpired(filePath, fs, tail, passed, upTo)
		position, passed = to, upTo
		s.saveState()
	}

	sent := 0
	var group []*store.SessionRecord
	var groupMeta *projectMeta
	groupCWD, groupEnd, groupSkill := "", 0, skill
	flush := func() error {
		if groupMeta == nil {
			return nil
		}
		n, err := s.sendRecords(ctx, filePath, projectHash, groupMeta, groupCWD, group)
		sent += n
		if err != nil {
			return err
		}
		advance(groupEnd, groupSkill)
		group, groupMeta = nil, nil
		return nil
	}
	for i, r := range consumed {
		attributionCommand := attributionSkillInvocationName(r, &skill)
		if tail.verdicts[i] != verdictSend {
			continue
		}
		m := tail.metas[tail.cwds[i]]
		if groupMeta != nil && !sameEnvelope(groupMeta, m) {
			if err := flush(); err != nil {
				return sent, err
			}
		}
		groupMeta, groupCWD = m, tail.cwds[i]
		if sr := convert(r, attributionCommand); sr != nil {
			group = append(group, sr)
		}
		groupEnd, groupSkill = i+1, skill
	}
	if err := flush(); err != nil {
		return sent, err
	}
	advance(len(consumed), skill)
	return sent, nil
}

// sameEnvelope reports whether two lookups put the same identity on a request.
func sameEnvelope(a, b *projectMeta) bool {
	return a.projectName == b.projectName &&
		a.gitRemoteURL == b.gitRemoteURL &&
		a.repositoryID == b.repositoryID &&
		a.repositoryIDSource == b.repositoryIDSource &&
		a.repositoryName == b.repositoryName &&
		a.repoSubpath == b.repoSubpath &&
		a.repoSubpathPresent == b.repoSubpathPresent &&
		a.commitSHA == b.commitSHA &&
		a.branch == b.branch
}

// offsetOf returns where records[i] starts, or end when i is past the last
// record.
func offsetOf(records []*sessionlog.Record, i int, end int64) int64 {
	if i < len(records) {
		return records[i].Offset
	}
	return end
}
