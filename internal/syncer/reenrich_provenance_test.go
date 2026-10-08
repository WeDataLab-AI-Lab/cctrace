package syncer

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"cctrace/internal/gitctx"
)

// The tests in this file swap resolveGit, nowFn and heldExpiry, so they cannot
// run in parallel with the rest of the package.
//
// Re-enrichment reads a file from its first byte and posts every record under
// one identity -- the cwd's current one. The server only updates rows it
// already has, but the bodies have left the machine by then. So under an
// allowlist it must not touch a file any part of which was kept back.

// A run that could never be identified was held, expired, and dropped as "not
// allowed". Nothing about its repository was ever recorded. When the cwd later
// holds an allowed repository, the file still carries what was dropped.
func TestReenrich_skipsFileThatDroppedAnExpiredHold(t *testing.T) {
	repos := map[string]gitctx.Context{}
	stubRepos(t, repos)
	advance := useFakeClock(t)
	shortHeldExpiry(t, 2)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/c", "secret"))
	syncPass(t, s)
	retryHeld(t, s, advance, 2)
	wantOffset(t, s, p, fileSize(t, p))
	if !reloadState(t, s).Files[p].ExcludedSeen {
		t.Fatal("ExcludedSeen not recorded with the dropped run")
	}

	repos["/work/c"] = repoAt(allowedRepo)
	if got := srv.reenrich(t, restartSyncer(t, s)); len(got) != 0 {
		t.Fatalf("re-enriched %v from a file that dropped an expired hold", got)
	}
}

// The same for a run consumed because its repository was excluded, once that
// cwd holds an allowed repository. And a file whose first cwd is allowed while
// a later one is excluded now is not sent whole under the first, as it used to
// be.
//
// What this cannot cover is a file consumed by a cctrace that did not record
// what it kept back (older.jsonl below, once its cwd is allowed again): there
// is no record to go by, and it is re-enriched.
func TestReenrich_skipsFileWithAnExcludedRun(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)}
	stubRepos(t, repos)
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	judged, older := filepath.Join(dir, "judged.jsonl"), filepath.Join(dir, "older.jsonl")
	appendLines(t, judged, sessionLine("/work/a", "1"), sessionLine("/work/b", "secret"))
	appendLines(t, older, sessionLine("/work/a", "1"), sessionLine("/work/b", "older secret"))
	s.state.SetOffset(older, fileSize(t, older))
	syncPass(t, s)
	wantSent(t, srv, "1")

	if got := srv.reenrich(t, s); len(got) != 0 {
		t.Fatalf("re-enriched %v from files with a cwd that is excluded now", got)
	}

	repos["/work/b"] = repoAt(allowedRepoTwo)
	expireMetaCache(s)
	if got := srv.reenrich(t, s); slices.Contains(got, "secret") {
		t.Fatalf("re-enriched %v: the run consumed as excluded left under the repository that replaced it", got)
	}
}

// A file with a taint still above its offset holds lines that may belong to
// the repository the cwd held before. Here re-enrichment is itself the lookup
// that finds the replacement: the file was written under the excluded
// repository and no sync pass has seen it since.
func TestReenrich_skipsFileWithATaintAboveItsOffset(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo), "/work/seen": repoAt(allowedRepo)}
	stubRepos(t, repos)
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	lookupInNewPass(s, "/work/r")
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "pending"))

	repos["/work/r"] = repoAt(allowedRepo)
	expireMetaCache(s)
	if got := srv.reenrich(t, s); len(got) != 0 {
		t.Fatalf("re-enriched %v from a file with unsent lines from before the replacement", got)
	}
	if len(s.state.Taints[p]) == 0 {
		t.Fatal("the replacement found during re-enrichment left the file unmarked")
	}

	// Once the offset has passed the mark there is nothing below it left to
	// protect, whether or not a scan has dropped the mark yet.
	done := filepath.Join(dir, "s2.jsonl")
	appendLines(t, done, sessionLine("/work/seen", "sent"))
	s.state.SetOffset(done, fileSize(t, done))
	s.state.Taints[done] = []Taint{{CWD: "/work/r", Until: fileSize(t, done), PrevRepositoryID: excludedRepo}}
	if got := srv.reenrich(t, s); !slices.Equal(got, []string{"sent"}) {
		t.Fatalf("re-enriched %v, want the file whose mark the offset has passed", got)
	}
}

// When the replacement is found but the session files cannot be listed to mark
// it, the lookup's answer -- an allowed repository -- judges nothing, and the
// file is left alone.
func TestReenrich_skipsFileWhoseReplacementCouldNotBeMarked(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	lookupInNewPass(s, "/work/r")
	appendLines(t, filepath.Join(dir, "s1.jsonl"), sessionLine("/work/r", "pending"))
	prev := listSessionFiles
	listSessionFiles = func(string) ([]string, error) { return nil, errors.New("listing failed") }
	t.Cleanup(func() { listSessionFiles = prev })

	repos["/work/r"] = repoAt(allowedRepo)
	expireMetaCache(s)
	if got := srv.reenrich(t, s); len(got) != 0 {
		t.Fatalf("re-enriched %v although the replacement could not be marked", got)
	}
}

// A file with a cwd git cannot answer for holds lines nobody can place.
func TestReenrich_skipsFileWithAnUncertainCWD(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	appendLines(t, filepath.Join(dir, "s1.jsonl"), sessionLine("/work/a", "1"), sessionLine("/work/c", "2"))
	syncPass(t, s)
	wantSent(t, srv, "1")

	if got := srv.reenrich(t, s); len(got) != 0 {
		t.Fatalf("re-enriched %v from a file with a cwd git could not answer for", got)
	}
}

// Lines being held are not sent yet, and may never be. Here they carry no cwd
// and the cwd before them is unknown, so no lookup can speak for them: the
// only named cwd in the file is allowed, and judging the file by it would post
// the held line under that repository before the hold was ever resolved.
func TestReenrich_skipsFileWithAHeldRun(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newFirstRunSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("", "old meta"))
	syncPass(t, s)
	held := fileSize(t, p)
	appendLines(t, p, sessionLine("", "new-private-note"), sessionLine("/work/a", "known"))
	syncPass(t, s)
	wantSent(t, srv)

	fresh := restartSyncer(t, s)
	if got := srv.reenrich(t, fresh); len(got) != 0 {
		t.Fatalf("re-enriched %v from a file whose lines are still held", got)
	}
	wantOffset(t, fresh, p, held)
}

// The two facts that make that file unsafe are checked separately, because
// either can stand without the other: a hold on a named cwd whose lookup has
// since recovered is still a hold until a sync pass resolves it, and an
// unknown last cwd with nothing pending yet is still unknown.
func TestReenrich_skipsOnHeldOrUnknownCWDAlone(t *testing.T) {
	for name, mark := range map[string]func(*FileState){
		"held":        func(fs *FileState) { fs.Held = map[string]HeldRun{"/work/a": {Reason: HeldReasonGitUncertain}} },
		"cwd unknown": func(fs *FileState) { fs.CWDUnknown = true },
	} {
		t.Run(name, func(t *testing.T) {
			stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo)})
			useFakeClock(t)
			srv, endpoint := newFreshMetaServer(t)
			s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
			p := filepath.Join(dir, "s1.jsonl")
			appendLines(t, p, sessionLine("/work/a", "1"))
			syncPass(t, s)
			mark(s.state.Files[p])

			if got := srv.reenrich(t, s); len(got) != 0 {
				t.Fatalf("re-enriched %v", got)
			}
		})
	}
}

// Re-enrichment follows the cwd from line to line the way a sync pass does. A
// line the scanner skipped as too large breaks that chain, and the lines after
// it that name no cwd are not taken for the cwd before it.
func TestReenrich_skipsFileWithLinesBehindASkippedLine(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)})
	useFakeClock(t)
	srv, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/a", "1"), oversizedLine(t, "/work/b"), sessionLine("", "b-private-small"), sessionLine("/work/a", "2"))
	syncPass(t, s)

	// Collected without an allowlist, then one is turned on.
	judged := New(s.claudeDir, s.profileEmail, s.userID, reloadState(t, s), s.client, allowExampleOrg)
	if got := srv.reenrich(t, judged); len(got) != 0 {
		t.Fatalf("re-enriched %v across a skipped line", got)
	}
}

// A file whose every cwd is allowed and that kept nothing back is re-enriched,
// and without an allowlist nothing is skipped at all.
func TestReenrich_sendsWhatWasNeverKeptBack(t *testing.T) {
	for name, tc := range map[string]struct {
		allow []string
		want  []string
	}{
		"allowlist":    {allowExampleOrg, []string{"1", "2"}},
		"no allowlist": {nil, []string{"1", "2", "1", "other"}},
	} {
		t.Run(name, func(t *testing.T) {
			stubRepos(t, map[string]gitctx.Context{"/work/a": repoAt(allowedRepo), "/work/b": repoAt(excludedRepo)})
			useFakeClock(t)
			srv, endpoint := newFreshMetaServer(t)
			s, dir := newTailSyncer(t, endpoint, tc.allow)
			appendLines(t, filepath.Join(dir, "s1.jsonl"), sessionLine("/work/a", "1"), sessionLine("/work/a", "2"))
			appendLines(t, filepath.Join(dir, "s2.jsonl"), sessionLine("/work/a", "1"), sessionLine("/work/b", "other"))
			syncPass(t, s)

			if got := srv.reenrich(t, s); !slices.Equal(got, tc.want) {
				t.Fatalf("re-enriched %v, want %v", got, tc.want)
			}
		})
	}
}
