package syncer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cctrace/internal/gitctx"
	"cctrace/internal/sessionlog"
)

// The tests in this file swap resolveGit, nowFn and heldExpiry, so they cannot
// run in parallel with the rest of the package.
//
// Under an allowlist a session's records are judged one by one, and what the
// client derives from a session's lines -- its snapshot, the paths it edited,
// its git commit calls -- follows the verdicts: it takes the records the
// offset passed that were sent, where the offset passes them, and no others.

// runLines is a run of session s1 in cwd: a line that names the cwd, then one
// line each of what is derived from collected lines -- a title, a PR link, an
// edit of a file called <name>.go, a git commit call and its result -- every
// one of them naming the run.
func runLines(cwd, name string) []string {
	at := nowFn()
	return []string{
		sessionLine(cwd, name),
		aiTitleLine("s1", "title-"+name),
		prLinkLine("s1", 1, "https://example.test/"+name+"/pull/1", name, at),
		// Absolute on every platform; nothing here touches the file.
		editLine("s1", "edit-"+name, at, "Edit", filepath.Join(os.TempDir(), "cctrace-runs", name+".go")),
		commitCallLine("call-"+name, "git commit -m "+name, cwd, at),
		commitResultLine("call-"+name, "[main abcdef1] "+name, at),
	}
}

// collectedOf returns which runs (see runLines) each kind of derived data in
// path's state was built from: the run of the snapshot's title, and the runs
// of its PR links, of the edited paths and of the git calls, each sorted.
func collectedOf(t *testing.T, s *Syncer, path string) (title string, prs, edits, calls []string) {
	t.Helper()
	fs := s.state.Files[path]
	if fs == nil {
		t.Fatalf("%s is not in the state", filepath.Base(path))
	}
	if st := fs.SessionSnapshot; st != nil {
		title = strings.TrimPrefix(deref(st.Snapshot.AITitle), "title-")
		for _, pr := range st.Snapshot.PRLinks {
			prs = append(prs, pr.Repository)
		}
	}
	if gs := fs.SessionGit; gs != nil {
		for _, edited := range gs.Edited {
			edits = append(edits, strings.TrimSuffix(filepath.Base(edited), ".go"))
		}
	}
	if cs := fs.SessionCommits; cs != nil {
		for _, call := range slices.Concat(cs.Open, cs.Done) {
			calls = append(calls, strings.TrimPrefix(call.ID, "call-"))
		}
	}
	slices.Sort(prs)
	slices.Sort(edits)
	slices.Sort(calls)
	return title, prs, edits, calls
}

// wantCollected fails unless path's derived data was built from exactly runs,
// collected in that order: the PR links, the edited paths and the git calls
// each name every one of them once, and the title is the last one's.
func wantCollected(t *testing.T, s *Syncer, path string, runs ...string) {
	t.Helper()
	title, prs, edits, calls := collectedOf(t, s, path)
	wantTitle := ""
	if len(runs) > 0 {
		wantTitle = runs[len(runs)-1]
	}
	want := slices.Sorted(slices.Values(runs))
	if title != wantTitle || !slices.Equal(prs, want) || !slices.Equal(edits, want) || !slices.Equal(calls, want) {
		t.Fatalf("derived data holds title %q, PR links %v, edits %v, git calls %v; want each list to be %v and the title %q",
			title, prs, edits, calls, want, wantTitle)
	}
}

// refuseRepository makes the server answer 503 to any request sent under the
// repository id, until the returned function is called.
func (fs *freshMetaServer) refuseRepository(id string) (stop func()) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.refuse = func(p SyncPayload) bool { return p.RepositoryID == id }
	return func() {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		fs.refuse = nil
	}
}

// collectedEarlier writes run "old" to path and leaves the state as a client
// from before session snapshots left it: the offset at the end of the run, the
// cwd it ended in, and nothing derived from it.
func collectedEarlier(t *testing.T, s *Syncer, path string) {
	t.Helper()
	appendLines(t, path, runLines("/work/a", "old")...)
	s.state.SetOffset(path, fileSize(t, path))
	s.state.Files[path].CWD = "/work/a"
}

// A session that goes from an allowed repository into an excluded one and back
// leaves the excluded run out of what is derived from it, as out of what is
// sent: the snapshot, the edited paths and the git calls hold what the allowed
// runs say and nothing of the excluded one, whether the runs are read in one
// pass or in a pass each.
func TestJudgedTail_derivedDataLeavesTheExcludedRunOut(t *testing.T) {
	for name, passPerRun := range map[string]bool{"one pass": false, "a pass per run": true} {
		t.Run(name, func(t *testing.T) {
			stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)})
			useFakeClock(t)
			srv, endpoint := newFreshMetaServer(t)
			s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
			p := filepath.Join(dir, "s1.jsonl")
			for _, run := range [][]string{runLines("/work/a", "first"), runLines("/work/b", "excluded"), runLines("/work/a", "last")} {
				appendLines(t, p, run...)
				if passPerRun {
					syncPass(t, s)
				}
			}
			syncPass(t, s)

			wantOffset(t, s, p, fileSize(t, p))
			wantCollected(t, s, p, "first", "last")
			fs := s.state.Files[p]
			if dirs := fs.SessionCommits.Dirs; !slices.Equal(dirs, []string{"/work/a"}) {
				t.Errorf("directories kept for the commit lookup = %v, want the allowed run's alone", dirs)
			}
			if fs.EditsFrom == nil || *fs.EditsFrom != 0 || fs.CallsFrom == nil || *fs.CallsFrom != 0 {
				t.Errorf("edits collected from %v, calls from %v; want both from the start of the file", fs.EditsFrom, fs.CallsFrom)
			}
			for _, post := range srv.recordPosts() {
				if post.RepositoryID != allowedRepo {
					t.Errorf("records were sent under %s", post.RepositoryID)
				}
			}
			// Stored by the save that stored the offset.
			wantCollected(t, restartSyncer(t, s), p, "first", "last")
		})
	}
}

// Records of an excluded account are dropped where they would be sent and
// consumed like sent ones: their verdict is still send, so what is derived
// from collected lines takes them, with an allowlist as without one. Whether
// an excluded account's snapshot goes is decided when it would be sent.
func TestExcludedAccountsRecordsAreCollectedLikeSentOnes(t *testing.T) {
	for name, allow := range map[string][]string{"no allowlist": nil, "allowlist": allowExampleOrg} {
		t.Run(name, func(t *testing.T) {
			stubResolveGit(t, func(string) gitctx.Context { return repoAt(allowedRepo) })
			claudeDir, sessionPath, state := exclusionFixture(t, "acct-personal")
			var payloads []SyncPayload
			var queries [][]AccountRef
			srv := exclusionServer(t, map[string]bool{}, &payloads, &queries)
			client := NewClient(srv.URL, "", "")
			client.SetExcludedAccounts([]string{"anthropic:acct-personal"})
			s := New(claudeDir, "p@example.com", "u1", state, client, allow)

			syncPass(t, s)

			if n := len(allRecords(payloads)); n != 0 {
				t.Fatalf("fixture: sent %d records of an excluded account", n)
			}
			wantOffset(t, s, sessionPath, fileSize(t, sessionPath))
			fs := reloadState(t, s).Files[sessionPath]
			if !fs.NoSessionSnapshot || fs.EditsFrom == nil || fs.CallsFrom == nil {
				t.Fatalf("snapshot mark %v, edits from %v, git calls from %v; want the consumed lines read for all three",
					fs.NoSessionSnapshot, fs.EditsFrom, fs.CallsFrom)
			}
		})
	}
}

// One tail goes out in a group per identity, and the offset follows each group
// that gets through. So does what is derived: when the first group is accepted
// and the second refused, it holds the first group's lines and none of the
// second's, however often the second is read and sent again, and takes those
// only when the group gets through and the offset passes them.
func TestJudgedTail_derivedDataFollowsEachAdvance(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/c": repoAt(allowedRepoTwo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, runLines("/work/a", "first")...)
	afterFirst := fileSize(t, p)
	appendLines(t, p, runLines("/work/c", "second")...)

	stop := srv.refuseRepository(allowedRepoTwo)
	for range 2 {
		syncPassFailing(t, s)
		wantOffset(t, s, p, afterFirst)
		wantCollected(t, s, p, "first")
		if got := s.state.Files[p].SessionSnapshot.Revision; got != 1 {
			t.Fatalf("snapshot revision %d with one group collected, want 1", got)
		}
		wantCollected(t, restartSyncer(t, s), p, "first")
	}

	stop()
	syncPass(t, s)
	wantOffset(t, s, p, fileSize(t, p))
	wantCollected(t, s, p, "first", "second")
	fs := s.state.Files[p]
	if got := fs.SessionSnapshot.Revision; got != 2 {
		t.Errorf("snapshot revision %d, want 2: one change for each group the offset passed", got)
	}
	if *fs.EditsFrom != 0 || *fs.CallsFrom != 0 {
		t.Errorf("edits collected from %d, calls from %d; want both from the start of the first group", *fs.EditsFrom, *fs.CallsFrom)
	}
}

// Records behind a held run are not passed, so they are not collected either.
// When the hold is released they are, with the run that held them. A run whose
// hold runs out is passed unsent and never collected, while the records behind
// it are.
func TestJudgedTail_heldRunIsCollectedOnceReleasedAndNeverOnceExpired(t *testing.T) {
	for name, release := range map[string]bool{"released": true, "expired": false} {
		t.Run(name, func(t *testing.T) {
			repos := map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)}
			stubRepos(t, repos)
			advance := useFakeClock(t)
			shortHeldExpiry(t, 2)
			_, endpoint := newFreshMetaServer(t)
			s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
			p := filepath.Join(dir, "s1.jsonl")
			appendLines(t, p, runLines("/work/a", "front")...)
			heldAt := fileSize(t, p)
			appendLines(t, p, slices.Concat(runLines("/work/c", "held"), runLines("/work/a", "behind"))...)

			syncPass(t, s)
			wantOffset(t, s, p, heldAt)
			wantCollected(t, s, p, "front")

			if release {
				repos["/work/c"] = repoAt(allowedRepoTwo)
				retryHeld(t, s, advance, 1)
				wantOffset(t, s, p, fileSize(t, p))
				wantCollected(t, s, p, "front", "held", "behind")
				return
			}
			retryHeld(t, s, advance, 2)
			wantOffset(t, s, p, fileSize(t, p))
			if fs := s.state.Files[p]; fs.HoldExpired == nil || !fs.ExcludedSeen {
				t.Fatalf("fixture: hold expired %v, excluded seen %v; want the held run dropped", fs.HoldExpired, fs.ExcludedSeen)
			}
			wantCollected(t, s, p, "front", "behind")
		})
	}
}

// Where a file's collected part starts moves when the file shrinks, and only
// then. Lines of an excluded repository do not move it: they can lie between
// allowed ones, and collection restarting behind them would give those up. The
// file is marked as holding them instead.
func TestCollectedFrom_movesOnAShrinkAndNotPastExcludedLines(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)})
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	mixed, excluded := filepath.Join(dir, "mixed.jsonl"), filepath.Join(dir, "excluded.jsonl")
	appendLines(t, mixed, sessionLine("/work/a", "1"), sessionLine("/work/b", "secret"), sessionLine("/work/a", "2"))
	appendLines(t, excluded, sessionLine("/work/b", "secret"), sessionLine("/work/b", "secret too"))
	syncPass(t, s)
	for _, p := range []string{mixed, excluded} {
		fs := reloadState(t, s).Files[p]
		if fs.Offset != fileSize(t, p) || !fs.ExcludedSeen || fs.CollectedFrom != 0 {
			t.Errorf("%s: offset %d of %d, excluded seen %v, collected from %d; want the lines passed and marked, the collected start unmoved",
				filepath.Base(p), fs.Offset, fileSize(t, p), fs.ExcludedSeen, fs.CollectedFrom)
		}
	}

	if err := os.WriteFile(mixed, nil, 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	appendLines(t, mixed, sessionLine("/work/a", "remnant"))
	syncPass(t, s)
	remnant := fileSize(t, mixed)
	if fs := reloadState(t, s).Files[mixed]; fs.Offset != remnant || fs.CollectedFrom != remnant {
		t.Fatalf("after a shrink: offset %d, collected from %d; want both at the end of the remnant, %d", fs.Offset, fs.CollectedFrom, remnant)
	}
	appendLines(t, mixed, sessionLine("/work/b", "secret again"))
	syncPass(t, s)
	if fs := reloadState(t, s).Files[mixed]; fs.Offset != fileSize(t, mixed) || fs.CollectedFrom != remnant {
		t.Fatalf("after more excluded lines: offset %d of %d, collected from %d; want it left at the shrink, %d", fs.Offset, fileSize(t, mixed), fs.CollectedFrom, remnant)
	}
}

// The readers of a file's collected lines, mark by mark, for a file every cwd
// of which reenrichAllowed would accept. Under an allowlist the backfills run
// only where reenrichAllowed lets re-enrichment through and keptBackForGood
// does not stop it, and the automatic rebuild never runs. Without an allowlist
// reenrichAllowed is not asked; the backfills and the rebuild stop only at a
// permanent mark, and the rebuild also at an earlier decline.
//
// They disagree in one place, on purpose. reenrichAllowed does not look at
// HoldExpired, which today never comes without ExcludedSeen: alone, it lets
// the file's stored records be updated again -- which creates nothing on the
// server -- but no backfill reads the file, since a snapshot is taken whole.
func TestReadersOfCollectedLinesAgreeOnTheMarks(t *testing.T) {
	type verdicts struct{ reenrich, backfill, rebuild bool }
	held := map[string]HeldRun{"/work/c": {Reason: HeldReasonGitUncertain}}
	expired := &HoldExpired{Reason: HeldReasonGitUncertain, CWD: "/work/c"}
	for _, tc := range []struct {
		name string
		mark func(s *Syncer, p string)
		// allowlist is what happens under allowExampleOrg, and open without an
		// allowlist, where reenrichAllowed is not asked (reenrich: true).
		allowlist, open verdicts
	}{
		{"no mark", func(*Syncer, string) {}, verdicts{true, true, false}, verdicts{true, true, true}},
		{"excluded seen", func(s *Syncer, p string) { s.state.Files[p].ExcludedSeen = true }, verdicts{false, false, false}, verdicts{true, false, false}},
		{"hold expired alone", func(s *Syncer, p string) { s.state.Files[p].HoldExpired = expired }, verdicts{true, false, false}, verdicts{true, false, false}},
		{"held now", func(s *Syncer, p string) { s.state.Files[p].Held = held }, verdicts{false, false, false}, verdicts{true, true, true}},
		{"last cwd unknown", func(s *Syncer, p string) { s.state.Files[p].CWDUnknown = true }, verdicts{false, false, false}, verdicts{true, true, true}},
		{"taint above the offset", func(s *Syncer, p string) {
			s.state.Taints = map[string][]Taint{p: {{CWD: "/work/a", Until: s.state.GetOffset(p) + 1, PrevRepositoryID: allowedRepoTwo}}}
		}, verdicts{false, false, false}, verdicts{true, true, true}},
		{"taint the offset has reached", func(s *Syncer, p string) {
			s.state.Taints = map[string][]Taint{p: {{CWD: "/work/a", Until: s.state.GetOffset(p), PrevRepositoryID: excludedRepo}}}
		}, verdicts{true, true, false}, verdicts{true, true, true}},
		{"rebuild declined", func(s *Syncer, p string) { s.state.Files[p].RebuildDeclined = true }, verdicts{true, true, false}, verdicts{true, true, false}},
	} {
		for _, configured := range []bool{true, false} {
			name := tc.name + ", no allowlist"
			allow, want := []string(nil), tc.open
			if configured {
				name, allow, want = tc.name+", allowlist", allowExampleOrg, tc.allowlist
			}
			t.Run(name, func(t *testing.T) {
				stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
				useFakeClock(t)
				_, endpoint := newFreshMetaServer(t)
				s, dir := newTailSyncer(t, endpoint, allow)
				p := filepath.Join(dir, "s1.jsonl")
				collectedEarlier(t, s, p)
				tc.mark(s, p)
				records, _, skipped, err := sessionlog.ScanFileWithSkips(context.Background(), p, 0)
				if err != nil {
					t.Fatal(err)
				}
				fs := s.state.Files[p]

				got := verdicts{reenrich: !configured || s.reenrichAllowed(p, records, skipped), rebuild: s.mayRebuild(fs)}
				got.backfill = got.reenrich && mayBackfill(fs)
				if got != want {
					t.Fatalf("reenrichAllowed, backfill, rebuild = %+v, want %+v", got, want)
				}
			})
		}
	}
}

// historyLines is what an earlier cctrace collected of session s1 in cwd: a
// title, a custom title, an agent name and a PR link, each saying HISTORY,
// and an edit of history.txt. Nothing of it may reach a snapshot unread.
func historyLines(cwd string) []string {
	at := nowFn()
	return []string{
		sessionLine(cwd, "history"),
		aiTitleLine("s1", "HISTORY-TITLE"),
		customTitleLine("s1", "HISTORY-CUSTOM"),
		agentNameLine("s1", "HISTORY-AGENT"),
		prLinkLine("s1", 1, "https://example.test/history/pull/1", "history", at),
		editLine("s1", "edit-history", at, "Edit", filepath.Join(cwd, "history.txt")),
	}
}

// collectedHistory writes historyLines to path and leaves the state as a
// cctrace from before per-record judgment left a file it consumed under an
// allowlist: the offset at the end, the cwd it ended in, no mark, no snapshot.
// Whether it sent those lines or consumed them unsent, nothing says.
func collectedHistory(t *testing.T, s *Syncer, path, cwd string) {
	t.Helper()
	appendLines(t, path, historyLines(cwd)...)
	s.state.SetOffset(path, fileSize(t, path))
	s.state.Files[path].CWD = cwd
}

// wantNoHistory fails if anything only historyLines say reached path's
// snapshot.
func wantNoHistory(t *testing.T, s *Syncer, path string) {
	t.Helper()
	if st := s.state.Files[path].SessionSnapshot; st != nil {
		if got := encodeSnapshot(t, st.Snapshot); strings.Contains(got, "HISTORY") || strings.Contains(got, "/history/") {
			t.Fatalf("snapshot %s holds what only the unread history says", got)
		}
	}
}

// wantTitlesLeftOut fails unless the custom title and the agent name of path's
// snapshot are left out -- unknown -- rather than carried, "" included.
func wantTitlesLeftOut(t *testing.T, s *Syncer, path string) {
	t.Helper()
	snap := s.state.Files[path].SessionSnapshot.Snapshot
	if snap.CustomTitle != nil || snap.AgentName != nil || snap.carriesTitles() {
		t.Fatalf("snapshot %s carries titles it never read", encodeSnapshot(t, snap))
	}
}

// S1. Under an allowlist the history an earlier cctrace collected is not read
// again. An advance of allowed records, metadata lines among them, makes the
// snapshot from its own lines alone: nothing only the history says, the
// titles it does not give left out rather than "", and the decline recorded
// with the offset.
func TestSnapshot_S1_historyIsNotReadUnderAnAllowlist(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	collectedHistory(t, s, p, "/work/a")
	appendLines(t, p, sessionLine("/work/a", "new"), aiTitleLine("s1", "NEW-TITLE"),
		prLinkLine("s1", 2, "https://example.test/new/pull/2", "new", nowFn()))

	syncPass(t, s)

	fs := s.state.Files[p]
	if fs.SessionSnapshot == nil || fs.NoSessionSnapshot || !fs.RebuildDeclined {
		t.Fatalf("snapshot %v, nothing-to-keep mark %v, declined %v; want a snapshot of the new lines and the decline recorded",
			fs.SessionSnapshot != nil, fs.NoSessionSnapshot, fs.RebuildDeclined)
	}
	snap := fs.SessionSnapshot.Snapshot
	if deref(snap.AITitle) != "NEW-TITLE" || len(snap.PRLinks) != 1 || snap.PRLinks[0].Repository != "new" {
		t.Fatalf("snapshot %s, want the new title and the new PR link", encodeSnapshot(t, snap))
	}
	wantTitlesLeftOut(t, s, p)
	wantNoHistory(t, s, p)
	if !reloadState(t, s).Files[p].RebuildDeclined {
		t.Error("the decline was not saved with the offset")
	}
}

// S2. The same history, and an advance without metadata lines: no snapshot,
// and no mark that the lines hold none -- they were not looked at. The flush
// then gives the session a snapshot to carry its counts, with the titles left
// out, without reading the file, and settles: no count is lost and nothing is
// asked again on every flush.
func TestSnapshot_S2_flushCarriesTheCountsWithoutReading(t *testing.T) {
	useFakeClock(t)
	repo := namedRepo(t, "allowed-repo", "https://github.com/example-org/cctrace.git")
	stubRepos(t, map[string]gitctx.Context{repo: repoAt(allowedRepo)})
	srv := newObservationServer(t)
	s, dir := newTailSyncer(t, srv.URL(), allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	collectedHistory(t, s, p, repo)
	appendLines(t, p, sessionLine(repo, "new"), editLine("s1", "edit-new", nowFn(), "Edit", filepath.Join(repo, "modified.txt")))

	syncPass(t, s)
	fs := s.state.Files[p]
	if fs.SessionSnapshot != nil || fs.NoSessionSnapshot || !fs.RebuildDeclined || fs.SessionGit == nil {
		t.Fatalf("snapshot %v, nothing-to-keep mark %v, declined %v, edits %v; want no snapshot, no mark, the decline and the edit",
			fs.SessionSnapshot != nil, fs.NoSessionSnapshot, fs.RebuildDeclined, fs.SessionGit != nil)
	}

	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.SessionSnapshot == nil {
		t.Fatal("the flush made no snapshot to carry the counts")
	}
	if got := fs.SessionSnapshot.Snapshot.Git; got == nil || got.EditedFiles != 1 || got.UncommittedEditedFiles != 1 {
		t.Fatalf("counts %+v, want the one edited file the new lines name", got)
	}
	if st := fs.SessionSnapshot.Snapshot; st.AITitle != nil {
		t.Fatalf("snapshot %s carries a title nothing read", encodeSnapshot(t, st))
	}
	wantTitlesLeftOut(t, s, p)
	wantNoHistory(t, s, p)
	if fs.NoSessionSnapshot || !fs.RebuildDeclined {
		t.Errorf("nothing-to-keep mark %v, declined %v; want the decline alone", fs.NoSessionSnapshot, fs.RebuildDeclined)
	}
	if gs := fs.SessionGit; gs.Pending || gs.CheckedAt.IsZero() || gs.NoAnswers != 0 {
		t.Errorf("the session is not settled and would be asked again: %+v", gs)
	}
	if got := sentGit(srv.requests(), "s1"); got == nil || got.EditedFiles != 1 {
		t.Errorf("counts sent %+v, want them in the session's snapshot", got)
	}
}

// S3. Re-enrichment with content, which under an allowlist gets to a file
// only after reenrichAllowed has looked every cwd of it up, fills in the
// titles the declined rebuild left out and forgets the decline: the file is
// vouched for now. Without content the titles stay left out.
func TestSnapshot_S3_reenrichmentFillsTheTitles(t *testing.T) {
	for name, includeContent := range map[string]bool{"with content": true, "without content": false} {
		t.Run(name, func(t *testing.T) {
			stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
			useFakeClock(t)
			_, endpoint := newFreshMetaServer(t)
			s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
			p := filepath.Join(dir, "s1.jsonl")
			collectedHistory(t, s, p, "/work/a")
			appendLines(t, p, sessionLine("/work/a", "new"), aiTitleLine("s1", "NEW-TITLE"))
			syncPass(t, s)
			if !s.state.Files[p].RebuildDeclined {
				t.Fatal("fixture: the rebuild was not declined")
			}

			if _, err := s.ReenrichOnce(context.Background(), includeContent); err != nil {
				t.Fatal(err)
			}

			fs := s.state.Files[p]
			if fs.RebuildDeclined {
				t.Error("the decline outlived a re-enrichment that vouched for the file")
			}
			if !includeContent {
				wantTitlesLeftOut(t, s, p)
				return
			}
			snap := fs.SessionSnapshot.Snapshot
			if deref(snap.AITitle) != "NEW-TITLE" || deref(snap.CustomTitle) != "HISTORY-CUSTOM" || deref(snap.AgentName) != "HISTORY-AGENT" {
				t.Fatalf("titles %q %q %q, want the newest title and the history's custom title and name",
					deref(snap.AITitle), deref(snap.CustomTitle), deref(snap.AgentName))
			}
		})
	}
}

// S4. Lines consumed unsent under an allowlist keep the file from being read
// again once the allowlist is gone: no rebuild and none of re-enrichment's
// three backfills. The new lines still fold in, and develop's re-enrichment of
// the stored records still runs.
func TestSnapshot_S4_marksOutliveTheAllowlist(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	collectedHistory(t, s, p, "/work/a")
	appendLines(t, p, runLines("/work/b", "excluded")...)
	syncPass(t, s)
	if fs := s.state.Files[p]; !fs.ExcludedSeen || fs.SessionSnapshot != nil {
		t.Fatalf("fixture: excluded seen %v, snapshot %v", fs.ExcludedSeen, fs.SessionSnapshot != nil)
	}

	open := New(s.claudeDir, s.profileEmail, s.userID, s.state, s.client, nil)
	appendLines(t, p, runLines("/work/a", "new")...)
	syncPass(t, open)
	before := len(srv.sentSyncs())
	if _, err := open.ReenrichOnce(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	wantCollected(t, open, p, "new")
	wantTitlesLeftOut(t, open, p)
	wantNoHistory(t, open, p)
	if n := open.SnapshotsBackfilled(); n != 0 {
		t.Errorf("%d snapshots backfilled from a file that kept lines back", n)
	}
	reenriched := 0
	for _, post := range srv.sentSyncs()[before:] {
		if post.Reenrich {
			reenriched += len(post.Records)
		}
	}
	if reenriched == 0 {
		t.Error("develop's re-enrichment of the stored records did not run")
	}
}

// S5. A declined rebuild stays declined once the allowlist is gone: nothing
// has vouched for the history since.
func TestSnapshot_S5_declineOutlivesTheAllowlist(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	collectedHistory(t, s, p, "/work/a")
	appendLines(t, p, sessionLine("/work/a", "new"))
	syncPass(t, s)
	if fs := s.state.Files[p]; !fs.RebuildDeclined || fs.SessionSnapshot != nil {
		t.Fatalf("fixture: declined %v, snapshot %v", fs.RebuildDeclined, fs.SessionSnapshot != nil)
	}

	open := New(s.claudeDir, s.profileEmail, s.userID, s.state, s.client, nil)
	appendLines(t, p, sessionLine("/work/a", "later"), aiTitleLine("s1", "LATER-TITLE"))
	syncPass(t, open)

	fs := open.state.Files[p]
	if fs.SessionSnapshot == nil || deref(fs.SessionSnapshot.Snapshot.AITitle) != "LATER-TITLE" {
		t.Fatalf("snapshot %v, want the later lines' title", fs.SessionSnapshot)
	}
	wantTitlesLeftOut(t, open, p)
	wantNoHistory(t, open, p)
	if !fs.RebuildDeclined {
		t.Error("the decline was forgotten without anything vouching for the history")
	}
}

// The automatic rebuild of a transcript that has no snapshot yet, case by
// case: it reads the collected part only without an allowlist, on a file with
// no permanent mark and no earlier decline. Where it does not read, the titles
// it would have read stay left out, and under an allowlist the decline is
// recorded.
func TestSnapshotRebuild_readsOnlyWithoutAnAllowlistOrMarks(t *testing.T) {
	newRun := func(t *testing.T, _ *Syncer, p string) { appendLines(t, p, runLines("/work/a", "new")...) }
	for _, tc := range []struct {
		name  string
		allow []string
		// arrive appends the lines of the pass that would rebuild, run "new"
		// among them, and marks the state.
		arrive       func(t *testing.T, s *Syncer, p string)
		wantRead     bool
		wantDeclined bool
	}{
		{"clean file", allowExampleOrg, newRun, false, true},
		{"excluded run in the pass", allowExampleOrg, func(t *testing.T, _ *Syncer, p string) {
			appendLines(t, p, slices.Concat(runLines("/work/b", "excluded"), runLines("/work/a", "new"))...)
		}, false, true},
		{"run held behind the pass's lines", allowExampleOrg, func(t *testing.T, s *Syncer, p string) {
			newRun(t, s, p)
			appendLines(t, p, sessionLine("/work/c", "held"))
		}, false, true},
		{"last cwd unknown past a skipped line", allowExampleOrg, func(t *testing.T, s *Syncer, p string) {
			newRun(t, s, p)
			appendLines(t, p, oversizedLine(t, "/work/a"))
		}, false, true},
		{"taint above the offset", allowExampleOrg, func(t *testing.T, s *Syncer, p string) {
			newRun(t, s, p)
			// A line still being written keeps the offset below the mark; the
			// repository the cwd held before is allowed too, so the run is sent.
			appendBytes(t, p, sessionLine("/work/a", "unfinished"))
			s.state.CWDIdentity = map[string]*CWDIdentity{"/work/a": {RepositoryID: allowedRepo, Source: "resolved"}}
			s.state.Taints = map[string][]Taint{p: {{CWD: "/work/a", Until: fileSize(t, p), PrevRepositoryID: allowedRepoTwo}}}
		}, false, true},
		{"clean file, no allowlist", nil, newRun, true, false},
		{"excluded lines consumed earlier, no allowlist", nil, func(t *testing.T, s *Syncer, p string) {
			s.state.Files[p].ExcludedSeen = true
			newRun(t, s, p)
		}, false, false},
		{"hold that ran out earlier, no allowlist", nil, func(t *testing.T, s *Syncer, p string) {
			s.state.Files[p].HoldExpired = &HoldExpired{Reason: HeldReasonGitUncertain, CWD: "/work/c"}
			newRun(t, s, p)
		}, false, false},
		{"rebuild declined earlier, no allowlist", nil, func(t *testing.T, s *Syncer, p string) {
			s.state.Files[p].RebuildDeclined = true
			newRun(t, s, p)
		}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)})
			useFakeClock(t)
			_, endpoint := newFreshMetaServer(t)
			s, dir := newTailSyncer(t, endpoint, tc.allow)
			p := filepath.Join(dir, "s1.jsonl")
			collectedEarlier(t, s, p)
			tc.arrive(t, s, p)

			syncPass(t, s)

			title, prs, edits, calls := collectedOf(t, s, p)
			if title != "new" || !slices.Equal(edits, []string{"new"}) || !slices.Equal(calls, []string{"new"}) {
				t.Fatalf("fixture: title %q, edits %v, git calls %v; want the pass's own run collected", title, edits, calls)
			}
			want := []string{"new"}
			if tc.wantRead {
				want = []string{"new", "old"}
			}
			if !slices.Equal(prs, want) {
				t.Fatalf("snapshot holds the PR links of %v, want %v", prs, want)
			}
			fs := s.state.Files[p]
			if got := fs.SessionSnapshot.Snapshot.carriesTitles(); got != tc.wantRead {
				t.Errorf("snapshot carries titles: %v, want %v -- titles it did not read are left out", got, tc.wantRead)
			}
			if fs.RebuildDeclined != tc.wantDeclined {
				t.Errorf("declined %v, want %v", fs.RebuildDeclined, tc.wantDeclined)
			}
		})
	}
}

// Re-enrichment fills a session's snapshot, edited paths and git calls from
// the part sync collected, and updates the stored records. Under an allowlist
// it gets to a file only where reenrichAllowed lets it, for both. A file some
// lines of which were kept back for good gets none of the three backfills,
// with an allowlist or without one, while its stored records are updated as
// before; a declined rebuild does not stop the backfills, and only a
// re-enrichment under an allowlist forgets it.
func TestReenrich_backfillsLeaveAKeptBackFileAlone(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		allow                     []string
		mark                      func(s *Syncer, p string)
		wantRecords, wantBackfill bool
		wantDeclined              bool
	}{
		{"clean file", allowExampleOrg, func(*Syncer, string) {}, true, true, false},
		{"excluded lines consumed", allowExampleOrg, func(s *Syncer, p string) { s.state.Files[p].ExcludedSeen = true }, false, false, false},
		{"taint above the offset", allowExampleOrg, func(s *Syncer, p string) {
			s.state.Taints = map[string][]Taint{p: {{CWD: "/work/a", Until: s.state.GetOffset(p) + 1, PrevRepositoryID: allowedRepoTwo}}}
		}, false, false, false},
		{"rebuild declined", allowExampleOrg, func(s *Syncer, p string) { s.state.Files[p].RebuildDeclined = true }, true, true, false},
		{"excluded lines consumed, no allowlist", nil, func(s *Syncer, p string) { s.state.Files[p].ExcludedSeen = true }, true, false, false},
		{"hold that ran out, no allowlist", nil, func(s *Syncer, p string) {
			s.state.Files[p].HoldExpired = &HoldExpired{Reason: HeldReasonGitUncertain, CWD: "/work/c"}
		}, true, false, false},
		{"rebuild declined, no allowlist", nil, func(s *Syncer, p string) { s.state.Files[p].RebuildDeclined = true }, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
			useFakeClock(t)
			srv, endpoint := newFreshMetaServer(t)
			s, dir := newTailSyncer(t, endpoint, tc.allow)
			p := filepath.Join(dir, "s1.jsonl")
			collectedEarlier(t, s, p)
			tc.mark(s, p)

			if _, err := s.ReenrichOnce(context.Background(), true); err != nil {
				t.Fatalf("ReenrichOnce: %v", err)
			}

			if posted := len(srv.sentSyncs()) > 0; posted != tc.wantRecords {
				t.Errorf("records updated: %v, want %v", posted, tc.wantRecords)
			}
			if got := s.SnapshotsBackfilled() == 1; got != tc.wantBackfill {
				t.Errorf("snapshots backfilled: %d", s.SnapshotsBackfilled())
			}
			if got := s.state.Files[p].RebuildDeclined; got != tc.wantDeclined {
				t.Errorf("declined %v, want %v", got, tc.wantDeclined)
			}
			if tc.wantBackfill {
				wantCollected(t, s, p, "old")
				return
			}
			wantCollected(t, s, p)
			if fs := s.state.Files[p]; fs.EditsFrom != nil || fs.CallsFrom != nil {
				t.Errorf("edits read from %v, git calls from %v; want neither read", fs.EditsFrom, fs.CallsFrom)
			}
		})
	}
}

// The observation flush gives a session that has no snapshot yet one for its
// counts and commits. It reads the main transcript's collected part for it
// where the automatic rebuild may; where it may not, the snapshot carries the
// counts with the titles left out, and under an allowlist the decline is
// recorded. A transcript kept back whole gets none. Each way the session is
// settled, so the flush does not come back to it every time.
func TestFlush_snapshotForTheCountsFollowsTheRebuildRules(t *testing.T) {
	const (
		none = iota
		unread
		read
	)
	for _, tc := range []struct {
		name         string
		allow        []string
		mark         func(fs *FileState)
		want         int
		wantDeclined bool
	}{
		{"clean file", allowExampleOrg, func(*FileState) {}, unread, true},
		{"clean file, no allowlist", nil, func(*FileState) {}, read, false},
		{"excluded lines consumed", allowExampleOrg, func(fs *FileState) { fs.ExcludedSeen = true }, none, false},
		{"excluded lines consumed, no allowlist", nil, func(fs *FileState) { fs.ExcludedSeen = true }, none, false},
		{"rebuild declined earlier, no allowlist", nil, func(fs *FileState) { fs.RebuildDeclined = true }, unread, true},
	} {
		for _, put := range []string{"git counts", "commits"} {
			t.Run(tc.name+", "+put, func(t *testing.T) {
				useFakeClock(t)
				_, endpoint := newFreshMetaServer(t)
				s, dir := newTailSyncer(t, endpoint, tc.allow)
				p := filepath.Join(dir, "s1.jsonl")
				collectedEarlier(t, s, p)
				fs := s.state.Files[p]
				tc.mark(fs)

				var settled bool
				if put == "git counts" {
					settled = s.putGitCounts(context.Background(), fs, p, SnapshotGit{EditedFiles: 1}, &SessionGitState{ActiveAt: nowFn()}, nowFn())
				} else {
					fs.SessionCommits = &SessionCommitsState{}
					unresolved := &SnapshotCommits{Unresolved: []UnresolvedCommit{{Call: "call-old", Reason: "no_call_hash"}}}
					settled = s.putSessionCommits(context.Background(), fs, p, unresolved, nowFn())
				}

				if !settled {
					t.Fatal("not settled: the flush would come back to the session every time")
				}
				if fs.RebuildDeclined != tc.wantDeclined {
					t.Errorf("declined %v, want %v", fs.RebuildDeclined, tc.wantDeclined)
				}
				if tc.want == none {
					if fs.SessionSnapshot != nil || fs.NoSessionSnapshot {
						t.Fatalf("snapshot %+v, mark %v; want the session left without either", fs.SessionSnapshot, fs.NoSessionSnapshot)
					}
					return
				}
				st := fs.SessionSnapshot
				if st == nil || (st.Snapshot.Git == nil && st.Snapshot.Commits == nil) {
					t.Fatalf("snapshot %+v, want one holding what was put", st)
				}
				title, prs, _, _ := collectedOf(t, s, p)
				if tc.want == read {
					if title != "old" || !slices.Equal(prs, []string{"old"}) {
						t.Fatalf("snapshot holds title %q and the PR links of %v, want the collected part's", title, prs)
					}
					return
				}
				if len(prs) != 0 || st.Snapshot.AITitle != nil || fs.NoSessionSnapshot {
					t.Fatalf("snapshot %s, nothing-to-keep mark %v; want the collected part unread and the titles left out",
						encodeSnapshot(t, st.Snapshot), fs.NoSessionSnapshot)
				}
				wantTitlesLeftOut(t, s, p)
			})
		}
	}
}

// S7. A commit call whose session's main transcript has nothing to put it in
// yet is not dropped while what holds that transcript back may clear: here
// its first line is held, so it has collected nothing. The call waits, is
// asked about again after a backoff, and goes into the snapshot the
// transcript's lines make once the hold is released. A main transcript with a
// collected history and a hold needs no waiting: the call goes into a snapshot
// made for it, the history unread.
func TestCommits_S7_callsWaitOnATransientMark(t *testing.T) {
	subagentWithCommit := func(t *testing.T, dir string) {
		t.Helper()
		sub := filepath.Join(dir, "s1", "subagents")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		at := nowFn()
		appendLines(t, filepath.Join(sub, "agent-a1.jsonl"), sessionLine("/work/a", "sub"),
			commitCallLine("call-sub", "git commit -m sub", "/work/a", at), commitResultLine("call-sub", "[main abcdef1] sub", at.Add(time.Second)))
	}
	committed := func(fs *FileState) bool {
		if st := fs.SessionSnapshot; st != nil && st.Snapshot.Commits != nil {
			for _, u := range st.Snapshot.Commits.Unresolved {
				if u.Call == "call-sub" {
					return true
				}
			}
		}
		return false
	}

	t.Run("nothing collected yet", func(t *testing.T) {
		repos := map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)}
		stubRepos(t, repos)
		advance := useFakeClock(t)
		srv := newObservationServer(t)
		s, dir := newTailSyncer(t, srv.URL(), allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		appendLines(t, p, sessionLine("/work/c", "held"))
		subagentWithCommit(t, dir)
		syncPass(t, s)
		fs := s.state.Files[p]
		if fs.Offset != 0 || len(fs.Held) == 0 || fs.SessionCommits == nil || len(fs.SessionCommits.Done) != 1 {
			t.Fatalf("fixture: offset %d, held %v, calls %+v", fs.Offset, fs.Held, fs.SessionCommits)
		}

		flushOnce(t, s)
		cs := fs.SessionCommits
		if len(cs.Done) != 1 || cs.NoAnswers != 1 || fs.SessionSnapshot != nil {
			t.Fatalf("calls %d, tries %d, snapshot %v; want the call kept for another try and no snapshot", len(cs.Done), cs.NoAnswers, fs.SessionSnapshot != nil)
		}

		repos["/work/c"] = repoAt(allowedRepoTwo)
		advance(lookupRetryInterval)
		syncPass(t, s)
		advance(retryBackoff(1))
		flushOnce(t, s)
		if !committed(fs) || len(cs.Done) != 0 {
			t.Fatalf("calls left %d, snapshot %v; want the call in the snapshot the released lines made", len(cs.Done), fs.SessionSnapshot)
		}
	})

	t.Run("collected history", func(t *testing.T) {
		stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
		useFakeClock(t)
		srv := newObservationServer(t)
		s, dir := newTailSyncer(t, srv.URL(), allowExampleOrg)
		p := filepath.Join(dir, "s1.jsonl")
		collectedHistory(t, s, p, "/work/a")
		appendLines(t, p, sessionLine("/work/c", "held"))
		subagentWithCommit(t, dir)
		syncPass(t, s)
		fs := s.state.Files[p]
		if len(fs.Held) == 0 || fs.SessionCommits == nil || len(fs.SessionCommits.Done) != 1 {
			t.Fatalf("fixture: held %v, calls %+v", fs.Held, fs.SessionCommits)
		}

		flushOnce(t, s)
		if !committed(fs) || len(fs.SessionCommits.Done) != 0 || fs.SessionCommits.NoAnswers != 0 {
			t.Fatalf("calls left %d, tries %d, snapshot %v; want the call in a snapshot made for it", len(fs.SessionCommits.Done), fs.SessionCommits.NoAnswers, fs.SessionSnapshot)
		}
		wantTitlesLeftOut(t, s, p)
		wantNoHistory(t, s, p)
		if !fs.RebuildDeclined {
			t.Error("the decline was not recorded")
		}
	})
}
