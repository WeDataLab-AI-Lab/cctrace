package syncer

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"cctrace/internal/gitctx"
	"cctrace/internal/sessionlog"
)

type commitLocation struct {
	root, repository string
	witness          string
	allowed          bool
	noAnswer         bool
}
type commitLogAnswer struct {
	log gitctx.CommitLog
	err error
}

// commitJob uses the existing in-flight registry. Abandoning a timed-out wait
// never starts another syscall under the same key while the old one is running.
func commitJob[T any](ctx context.Context, jobs *gitJobs, key string, compute func() T) (T, bool) {
	var zero T
	if ctx.Err() != nil {
		return zero, false
	}
	jobs.mu.Lock()
	busy := len(jobs.running) >= gitCountWorkers
	jobs.mu.Unlock()
	if busy {
		return zero, false
	}
	ch := make(chan T, 1)
	if !startGitJob(jobs, key, compute, func(v T) { ch <- v }) {
		return zero, false
	}
	select {
	case v := <-ch:
		return v, true
	case <-ctx.Done():
		return zero, false
	}
}

func (s *Syncer) locateCommitDir(ctx context.Context, dir string, now time.Time) (commitLocation, bool) {
	s.gitRoots.prune(now)
	if root, ok := s.gitRoots.get(dir); ok && root != "" {
		if top, ok := s.gitTops[root]; ok && now.Sub(top.at) < metaTTL {
			if repo, ok := s.gitRepos[top.value]; ok && now.Sub(repo.at) < metaTTL {
				return commitLocation{root: top.value, repository: repo.value, allowed: gitctx.AllowsRepository(repo.value, s.collectPrefixes)}, true
			}
		}
	}
	prefixes := slices.Clone(s.collectPrefixes)
	result, ok := commitJob(ctx, &s.gitJobs, "commit-dir\x00"+dir, func() commitLocation {
		real := resolvedPath(dir)
		var roots rootCache
		root, answered := findWorkTree(ctx, real, now, &roots)
		if !answered {
			return commitLocation{noAnswer: true}
		}
		if root == "" {
			return commitLocation{}
		}
		top, err := gitctx.WorkTreeRoot(ctx, real)
		if err != nil {
			return commitLocation{noAnswer: true}
		}
		if top == "" {
			// A removed child directory still has an existing, verified ancestor
			// worktree. Keep this identity separately from a readable call cwd.
			witness, err := gitctx.WorkTreeRoot(ctx, root)
			if err != nil {
				return commitLocation{noAnswer: true}
			}
			return commitLocation{witness: witness}
		}
		repo, err := gitctx.RepositoryID(ctx, top)
		if err != nil {
			return commitLocation{noAnswer: true}
		}
		return commitLocation{root: top, repository: repo, allowed: gitctx.AllowsRepository(repo, prefixes)}
	})
	if ok && !result.noAnswer && result.root != "" {
		s.gitRoots.put([]string{dir}, result.root, now)
		if s.gitTops == nil {
			s.gitTops = map[string]gitAnswer{}
		}
		if s.gitRepos == nil {
			s.gitRepos = map[string]gitAnswer{}
		}
		s.gitTops[result.root] = gitAnswer{value: result.root, at: now}
		s.gitRepos[result.root] = gitAnswer{value: result.repository, at: now}
	}
	return result, ok && !result.noAnswer
}

// collectSessionCommits never uses time or subject similarity as proof. The
// reflog window only bounds candidates which a call-specific receipt must name.
func (s *Syncer) collectSessionCommits(ctx context.Context, now time.Time) bool {
	home := normalizeHome(s.claudeDir)
	prefix := filepath.Join(home, "projects") + string(filepath.Separator)
	var paths []string
	var refs []AccountRef
	for path, fs := range s.state.Files {
		if fs == nil || fs.SessionCommits == nil || !strings.HasPrefix(path, prefix) || !isMainTranscript(path) {
			continue
		}
		cs := fs.SessionCommits
		if len(cs.Open)+len(cs.Done) == 0 {
			st := fs.SessionSnapshot
			if (!cs.Truncated && !cs.Partial) || (st != nil && slices.Contains(st.Snapshot.Truncated, "commits")) {
				continue
			}
		}
		if cs.NoAnswers > 0 && now.Sub(cs.TriedAt) < retryBackoff(cs.NoAnswers) {
			continue
		}
		paths = append(paths, path)
		for _, calls := range [][]GitCall{cs.Open, cs.Done} {
			for _, c := range calls {
				if c.AccountID != "" {
					refs = append(refs, AccountRef{BillingProvider: "anthropic", AccountID: c.AccountID})
				}
			}
		}
		if st := fs.SessionSnapshot; st != nil {
			if id := s.state.ClaudeAccountAt(home, st.NewestRecordAt); id != "" {
				refs = append(refs, AccountRef{BillingProvider: "anthropic", AccountID: id})
			}
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 || s.accounts.resolve(ctx, refs) != nil {
		return false
	}
	excluded := func(id string) bool {
		a := AccountRef{BillingProvider: "anthropic", AccountID: id}
		return id != "" && (s.client.excludedLocally(a) || s.accounts.excluded(a))
	}
	changed := false
	// Resolve all known directories first, including other calls' directories, so
	// nested cwd spellings do not hide a shared actual work tree.
	locations := map[string]commitLocation{}
	noAnswers := map[string]bool{}
	for _, path := range paths {
		fs := s.state.Files[path]
		cs := fs.SessionCommits
		if st := fs.SessionSnapshot; st != nil && excluded(s.state.ClaudeAccountAt(home, st.NewestRecordAt)) {
			cs.Open = nil
			cs.Done = nil
			changed = true
			continue
		}
		cs.Open = slices.DeleteFunc(cs.Open, func(c GitCall) bool {
			if excluded(c.AccountID) {
				changed = true
				return true
			}
			return false
		})
		cs.Done = slices.DeleteFunc(cs.Done, func(c GitCall) bool {
			if excluded(c.AccountID) {
				changed = true
				return true
			}
			return false
		})
		for _, calls := range [][]GitCall{cs.Open, cs.Done} {
			for _, call := range calls {
				if len(call.Ops) != 1 || !call.Ops[0].DirKnown {
					continue
				}
				dir := call.Ops[0].Dir
				if _, ok := locations[dir]; ok || noAnswers[dir] {
					continue
				}
				loc, answered := s.locateCommitDir(ctx, dir, now)
				if !answered {
					noAnswers[dir] = true
				} else {
					locations[dir] = loc
				}
			}
		}
	}
	// Freeze the competing call set before settling any session. Removing the
	// first contender must not turn the second into the winner of an overlap.
	var contenders []GitCall
	for path, fs := range s.state.Files {
		if strings.HasPrefix(path, prefix) && fs.SessionCommits != nil {
			contenders = append(contenders, fs.SessionCommits.Open...)
			contenders = append(contenders, fs.SessionCommits.Done...)
		}
	}
	logs := map[string]commitLogAnswer{}
	for _, path := range paths {
		fs := s.state.Files[path]
		cs := fs.SessionCommits
		var keepOpen, keepDone []GitCall
		output := &SnapshotCommits{}
		failed := false
		for _, call := range cs.Open {
			if commitCallExpired(call, now) {
				output.Unresolved = append(output.Unresolved, unresolvedCommit(call, "missing_result"))
			} else {
				keepOpen = append(keepOpen, call)
			}
		}
		for _, call := range cs.Done {
			reason := commitCallReason(call)
			// Candidate exploration is separate from attribution. In particular a
			// dynamic directory may find matching hashes but never proves a tree.
			var fallbackItems []gitctx.Commit
			var fallbackLoc commitLocation
			if len(call.OutputHashes) > 0 || call.StructuredHash != "" {
				own := commitLocation{}
				if len(call.Ops) == 1 {
					own = locations[call.Ops[0].Dir]
				}
				var pending bool
				fallbackItems, fallbackLoc, pending = s.exploreCommitCandidates(ctx, fs, call, own, locations, logs, now)
				if pending && len(fallbackItems) == 0 && !commitCallExpired(call, now) {
					keepDone = append(keepDone, call)
					failed = true
					continue
				}
			}
			if reason != "" {
				if len(call.Ops) == 1 {
					if loc, ok := locations[call.Ops[0].Dir]; ok && loc.root != "" && !loc.allowed {
						continue
					}
				}
				output.Unresolved = append(output.Unresolved, unresolvedCommit(call, reason))
				continue
			}
			dir := call.Ops[0].Dir
			loc, ok := locations[dir]
			if !ok {
				if commitCallExpired(call, now) {
					output.Unresolved = append(output.Unresolved, unresolvedCommit(call, "unavailable_tree"))
				} else {
					keepDone = append(keepDone, call)
					failed = true
				}
				continue
			}
			if loc.root == "" && len(fallbackItems) > 0 {
				loc = fallbackLoc
			}
			if loc.root == "" {
				output.Unresolved = append(output.Unresolved, unresolvedCommit(call, "unavailable_tree"))
				continue
			}
			if !loc.allowed {
				continue
			}
			answer, ok := logs[loc.root]
			if !ok {
				var answered bool
				answer, answered = commitJob(ctx, &s.gitJobs, "commit-log\x00"+loc.root, func() commitLogAnswer {
					log, err := gitctx.ReadCommitLog(ctx, loc.root)
					return commitLogAnswer{log, err}
				})
				if !answered {
					if commitCallExpired(call, now) {
						output.Unresolved = append(output.Unresolved, unresolvedCommit(call, "unavailable_reflog"))
					} else {
						keepDone = append(keepDone, call)
						failed = true
					}
					continue
				}
				logs[loc.root] = answer
			}
			if answer.err != nil {
				if gitctx.NoAnswer(answer.err) && !commitCallExpired(call, now) {
					keepDone = append(keepDone, call)
					failed = true
				} else {
					output.Unresolved = append(output.Unresolved, unresolvedCommit(call, "unavailable_reflog"))
				}
				continue
			}
			items, why := matchCommitReceipt(call, answer.log)
			if why != "" {
				output.Unresolved = append(output.Unresolved, unresolvedCommit(call, why))
				continue
			}
			if commitCallConflicts(call, loc.root, locations, contenders, items) {
				output.Unresolved = append(output.Unresolved, unresolvedCommit(call, "conflicting_call_hash"))
				continue
			}
			for _, item := range items {
				branch := ""
				if delta := now.Unix() - item.At; delta >= 0 && delta <= 600 {
					branch = answer.log.Branch
				}
				tree := CommitTree{Repository: loc.repository, Branch: branch}
				ti := slices.Index(output.Trees, tree)
				if ti < 0 {
					ti = len(output.Trees)
					output.Trees = append(output.Trees, tree)
				}
				if s.client.redact.Enabled() {
					call.ReadRedacted = true
					cs.rememberRedactedCall(call)
				}
				var subject *string
				if !call.WithholdSubject {
					text := cutAtRune(item.Subject, 200)
					if call.ReadRedacted || s.client.redact.Enabled() {
						text = redactedMarker
					}
					subject = &text
				}
				output.Items = append(output.Items, SnapshotCommit{Hash: item.Hash, At: item.At, Parents: item.Parents, Kind: item.Kind, Subject: subject, Call: call.ID, Transcript: commitTranscript(call.Transcript), Tree: ti, Proof: "output_hash", Replaces: item.Replaces})
			}
		}
		if len(output.Items)+len(output.Unresolved) > 0 || cs.Truncated || cs.Partial {
			previousRevision := int64(0)
			if fs.SessionSnapshot != nil {
				previousRevision = fs.SessionSnapshot.Revision
			}
			if !s.putSessionCommits(ctx, fs, path, output, now) {
				cs.TriedAt = now
				cs.NoAnswers++
				changed = true
				continue
			}
			if fs.SessionSnapshot != nil && fs.SessionSnapshot.Revision != previousRevision {
				changed = true
			}
		}
		if len(keepOpen) != len(cs.Open) || len(keepDone) != len(cs.Done) {
			changed = true
		}
		cs.Open, cs.Done = keepOpen, keepDone
		if failed {
			cs.TriedAt = now
			cs.NoAnswers++
			changed = true
		} else {
			cs.NoAnswers = 0
		}
	}
	return changed
}

// exploreCommitCandidates inspects bounded allowed alternatives, but returns
// items only when a known call path and the candidate share a verified root.
// A matching SHA in another worktree is discovery, never execution proof.
func (s *Syncer) exploreCommitCandidates(ctx context.Context, fs *FileState, call GitCall, own commitLocation, locations map[string]commitLocation, logs map[string]commitLogAnswer, now time.Time) ([]gitctx.Commit, commitLocation, bool) {
	if own.root != "" && !own.allowed {
		return nil, commitLocation{}, false
	}
	dirs := []string{}
	add := func(dir string) {
		if dir != "" && !slices.Contains(dirs, dir) {
			if len(dirs) < maxCommitDirs {
				dirs = append(dirs, dir)
			} else {
				fs.SessionCommits.Truncated = true
			}
		}
	}
	if len(call.Ops) == 1 && call.Ops[0].DirKnown {
		add(call.Ops[0].Dir)
	}
	for _, dir := range fs.SessionCommits.Dirs {
		add(dir)
	}
	if fs.SessionGit != nil {
		for _, path := range fs.SessionGit.Edited {
			add(filepath.Dir(path))
		}
	}
	witness := own.root
	if witness == "" {
		witness = own.witness
	}
	seen := map[string]bool{}
	pending := false
	for _, dir := range dirs {
		loc, ok := locations[dir]
		if !ok {
			var answered bool
			loc, answered = s.locateCommitDir(ctx, dir, now)
			if !answered {
				pending = true
				continue
			}
			locations[dir] = loc
		}
		if loc.root == "" || !loc.allowed || seen[loc.root] {
			continue
		}
		seen[loc.root] = true
		answer, ok := logs[loc.root]
		if !ok {
			var answered bool
			answer, answered = commitJob(ctx, &s.gitJobs, "commit-log\x00"+loc.root, func() commitLogAnswer {
				log, err := gitctx.ReadCommitLog(ctx, loc.root)
				return commitLogAnswer{log, err}
			})
			if !answered {
				pending = true
				continue
			}
			logs[loc.root] = answer
		}
		if answer.err != nil {
			pending = pending || gitctx.NoAnswer(answer.err)
			continue
		}
		items, why := matchCommitReceipt(call, answer.log)
		if why == "" && witness != "" && loc.root == witness && len(call.Ops) == 1 && call.Ops[0].DirKnown {
			return items, loc, false
		}
	}
	return nil, commitLocation{}, pending
}

// Drop the project directory encoding; only the main/subagent source filename
// distinguishes reused call ids on the wire, as it does for record uploads.
func commitTranscript(path string) string { return filepath.Base(path) }

func commitCallExpired(call GitCall, now time.Time) bool {
	at := call.ReadAt
	if at.IsZero() {
		at = later(call.Start, call.End)
	}
	return !at.IsZero() && now.Sub(at) > sessionGitWindow
}

func unresolvedCommit(c GitCall, reason string) UnresolvedCommit {
	at := c.End
	if at.IsZero() {
		at = c.Start
	}
	return UnresolvedCommit{Call: c.ID, Transcript: commitTranscript(c.Transcript), At: at.Unix(), Reason: reason}
}
func commitCallReason(c GitCall) string {
	switch {
	case c.Background:
		return "background"
	case c.Incomplete || len(c.Ops) != 1:
		return "incomplete_call"
	case !c.Ops[0].DirKnown:
		return "unknown_directory"
	case c.ResultError && len(c.OutputHashes) == 0 && c.StructuredHash == "":
		return "tool_error"
	case len(c.OutputHashes) == 0 && c.StructuredHash == "":
		return "no_call_hash"
	}
	return ""
}

func matchCommitReceipt(c GitCall, log gitctx.CommitLog) ([]gitctx.Commit, string) {
	if log.Truncated {
		return nil, "incomplete_reflog"
	}
	hashes := slices.Clone(c.OutputHashes)
	if c.StructuredHash != "" && !slices.Contains(hashes, c.StructuredHash) {
		hashes = append(hashes, c.StructuredHash)
	}
	var selected []gitctx.Commit
	for _, hash := range hashes {
		matches := map[string]gitctx.Commit{}
		for _, item := range log.Items {
			end := c.End.Unix()
			if c.End.Nanosecond() > 0 {
				end++
			}
			if item.ReflogAt < c.Start.Unix()-1 || item.ReflogAt > end+1 || !strings.HasPrefix(item.Hash, hash) {
				continue
			}
			matches[item.Hash] = item
		}
		if len(matches) != 1 {
			return nil, "ambiguous_or_missing_hash"
		}
		for _, item := range matches {
			if !slices.ContainsFunc(selected, func(v gitctx.Commit) bool { return v.Hash == item.Hash }) {
				selected = append(selected, item)
			}
		}
	}
	if len(selected) == 0 {
		return nil, "no_call_hash"
	}
	if c.StructuredHash != "" && len(selected) != 1 {
		return nil, "conflicting_hash_evidence"
	}
	return selected, ""
}

func commitCallConflicts(call GitCall, root string, locations map[string]commitLocation, contenders []GitCall, items []gitctx.Commit) bool {
	for _, other := range contenders {
		if call.ID == other.ID && call.Transcript == other.Transcript {
			continue
		}
		if !other.End.IsZero() && !other.Background && other.End.Before(call.Start) {
			continue
		}
		if !call.End.IsZero() && other.Start.After(call.End) {
			continue
		}
		for _, op := range other.Ops {
			loc, ok := locations[op.Dir]
			if ok && loc.root != root {
				continue
			}
			hashes := slices.Clone(other.OutputHashes)
			if other.StructuredHash != "" {
				hashes = append(hashes, other.StructuredHash)
			}
			for _, hash := range hashes {
				for _, item := range items {
					if strings.HasPrefix(item.Hash, hash) {
						return true
					}
				}
			}
		}
	}
	return false
}

// putSessionCommits puts a session's commit calls into its main transcript's
// snapshot, making one as putGitCounts does where there is none. It reports
// whether the calls are settled; when it does not, collectSessionCommits keeps
// them and tries again after a backoff. A main transcript nothing of which was
// collected yet settles them unsent -- unless what holds it back may yet clear
// (heldBackForNow), a run held from its first line among them: the calls then
// wait for the snapshot its lines will make, rather than being dropped.
func (s *Syncer) putSessionCommits(ctx context.Context, fs *FileState, path string, commits *SnapshotCommits, now time.Time) bool {
	project := sessionlog.ProjectHash(s.claudeDir, path)
	if fs.SessionSnapshot == nil {
		if collectedStart(fs) >= fs.Offset {
			return !s.heldBackForNow(path, fs)
		}
		if keptBackWhole(fs) {
			return true
		}
		if !fs.NoSessionSnapshot {
			s.advanceSessionSnapshot(ctx, fs, path, fs.Offset, project, nil)
		}
		if fs.SessionSnapshot == nil {
			if !fs.NoSessionSnapshot && !fs.RebuildDeclined {
				return false
			}
			fs.SessionSnapshot = &SessionSnapshotState{Snapshot: snapshotToCarry(fs), ProjectHash: project}
			fs.NoSessionSnapshot = false
		}
	}
	st := fs.SessionSnapshot
	for _, c := range fs.SessionCommits.Done {
		st.NewestRecordAt = later(st.NewestRecordAt, c.End)
	}
	next := st.Snapshot.clone()
	next.mergeCommits(commits)
	if fs.SessionCommits.Truncated || fs.SessionCommits.Partial {
		next.markTruncated("commits")
	}
	next.fit()
	if !sameSnapshot(st.Snapshot, next) {
		st.Revision++
		st.ObservedAt = now.UTC()
	}
	st.Snapshot = next
	return true
}
