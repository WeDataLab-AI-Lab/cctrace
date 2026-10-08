package syncer

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"

	"cctrace/internal/gitctx"
)

// The tests in this file swap resolveGit, nowFn and listSessionFiles, so they
// cannot run in parallel with the rest of the package.

// lookupInNewPass looks cwd up the way the first file of a new pass would.
func lookupInNewPass(s *Syncer, cwd string) *projectMeta {
	s.mu.Lock()
	clear(s.passMeta)
	s.mu.Unlock()
	return s.freshProjectMeta(cwd)
}

// reloadState reads back what the syncer saved.
func reloadState(t *testing.T, s *Syncer) *State {
	t.Helper()
	st, err := LoadState(s.state.path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	return st
}

// The first certain lookup of a cwd only writes down what it holds. Nothing is
// known about what it held before, so nothing is tainted: an upgrade, or a cwd
// never seen, starts like a restart always did.
func TestObserveIdentity_firstObservationRecordsAndTaintsNothing(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/r": repoAt(allowedRepo)})
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	appendLines(t, filepath.Join(dir, "s1.jsonl"), sessionLine("/work/r", "unsent"))

	lookupInNewPass(s, "/work/r")

	st := reloadState(t, s)
	want := &CWDIdentity{RepositoryID: allowedRepo, Source: "resolved"}
	if got := st.CWDIdentity["/work/r"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("CWDIdentity = %+v, want %+v", got, want)
	}
	if len(st.Taints) != 0 {
		t.Fatalf("first observation tainted %v", st.Taints)
	}
}

// When a certain lookup finds a cwd holding another repository than the one
// recorded, every session file's unsent bytes may have been written under
// either. Each such file is marked up to its size at that moment -- a byte
// position, not a time: an unfinished last line begins below it too. A file
// with nothing unsent is left alone, and a file the state has never seen (one
// created after the pass listed its files) is marked from its first byte.
func TestObserveIdentity_transitionTaintsUnsentBytes(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	lookupInNewPass(s, "/work/r")

	sent, pending, unseen := filepath.Join(dir, "sent.jsonl"), filepath.Join(dir, "pending.jsonl"), filepath.Join(dir, "unseen.jsonl")
	appendLines(t, sent, sessionLine("/work/r", "1"))
	s.state.SetOffset(sent, fileSize(t, sent))
	appendLines(t, pending, sessionLine("/work/r", "1"))
	s.state.SetOffset(pending, fileSize(t, pending))
	appendLines(t, pending, sessionLine("/work/r", "2"))
	appendBytes(t, pending, sessionLine("/work/r", "unfinished"))
	appendLines(t, unseen, sessionLine("/work/r", "1"))

	repos["/work/r"] = repoAt(allowedRepo)
	lookupInNewPass(s, "/work/r")

	st := reloadState(t, s)
	if got := st.CWDIdentity["/work/r"]; got == nil || got.RepositoryID != allowedRepo {
		t.Fatalf("CWDIdentity = %+v, want %s", got, allowedRepo)
	}
	want := map[string][]Taint{
		pending: {{CWD: "/work/r", Until: fileSize(t, pending), PrevRepositoryID: excludedRepo}},
		unseen:  {{CWD: "/work/r", Until: fileSize(t, unseen), PrevRepositoryID: excludedRepo}},
	}
	if !reflect.DeepEqual(st.Taints, want) {
		t.Fatalf("Taints = %+v\nwant     %+v", st.Taints, want)
	}

	// The same identity again is not a transition.
	lookupInNewPass(s, "/work/r")
	if got := reloadState(t, s).Taints; !reflect.DeepEqual(got, want) {
		t.Fatalf("a repeated lookup changed Taints to %+v", got)
	}
}

// A lookup that did not hear git says nothing about the cwd: it neither
// replaces the recorded identity nor taints anything.
func TestObserveIdentity_uncertainLookupChangesNothing(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(allowedRepo)}
	stubRepos(t, repos)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	appendLines(t, filepath.Join(dir, "s1.jsonl"), sessionLine("/work/r", "unsent"))
	lookupInNewPass(s, "/work/r")

	delete(repos, "/work/r")
	lookupInNewPass(s, "/work/r")

	st := reloadState(t, s)
	if got := st.CWDIdentity["/work/r"]; got == nil || got.RepositoryID != allowedRepo {
		t.Fatalf("CWDIdentity = %+v, want %s unchanged", got, allowedRepo)
	}
	if len(st.Taints) != 0 {
		t.Fatalf("an uncertain lookup tainted %v", st.Taints)
	}
}

// A transition is all or nothing. If the session files cannot be listed, or
// one of them cannot be sized, some unsent bytes would go unmarked -- so the
// recorded identity stays the old one and the next lookup finds the transition
// again.
func TestObserveIdentity_failedTransitionIsRetried(t *testing.T) {
	for name, list := range map[string]func(t *testing.T, real []string) ([]string, error){
		"listing fails": func(*testing.T, []string) ([]string, error) { return nil, errors.New("listing failed") },
		"a file cannot be sized": func(t *testing.T, real []string) ([]string, error) {
			return append(real, filepath.Join(lockedDir(t), "s.jsonl")), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
			stubRepos(t, repos)
			_, endpoint := newFreshMetaServer(t)
			s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
			p := filepath.Join(dir, "s1.jsonl")
			appendLines(t, p, sessionLine("/work/r", "unsent"))
			lookupInNewPass(s, "/work/r")

			failing := true
			prev := listSessionFiles
			listSessionFiles = func(claudeDir string) ([]string, error) {
				real, err := prev(claudeDir)
				if err != nil || !failing {
					return real, err
				}
				return list(t, real)
			}
			t.Cleanup(func() { listSessionFiles = prev })

			repos["/work/r"] = repoAt(allowedRepo)
			lookupInNewPass(s, "/work/r")
			if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != excludedRepo {
				t.Fatalf("CWDIdentity moved to %s although the transition failed", got)
			}
			if len(s.state.Taints) != 0 {
				t.Fatalf("a failed transition left Taints %v", s.state.Taints)
			}

			failing = false
			lookupInNewPass(s, "/work/r")
			if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != allowedRepo {
				t.Fatalf("CWDIdentity = %s after the retry, want %s", got, allowedRepo)
			}
			if got := s.state.Taints[p]; len(got) != 1 || got[0].Until != fileSize(t, p) {
				t.Fatalf("Taints[%s] = %+v after the retry, want one up to %d", p, got, fileSize(t, p))
			}
		})
	}
}

// lockedDir returns a directory nothing inside which can be reached, as a path
// on an unreadable volume is, and skips the test where that cannot be done.
// It is created once per test.
func lockedDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	unreadable(t, dir)
	return dir
}

// A listed name that leads nowhere has no bytes to mark: a file deleted since
// the listing, or a .jsonl symlink that dangles or points at itself. Waiting
// for it would wait forever -- the link is still there on every later lookup --
// so it is passed over, unlike a file that exists and cannot be sized.
func TestObserveIdentity_listedNameThatLeadsNowhereIsPassedOver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "unsent"))
	lookupInNewPass(s, "/work/r")
	for name, target := range map[string]string{"dead.jsonl": "gone", "self.jsonl": "self.jsonl"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
	}
	prev := listSessionFiles
	listSessionFiles = func(claudeDir string) ([]string, error) {
		files, err := prev(claudeDir)
		return append(files, filepath.Join(dir, "deleted-since.jsonl")), err
	}
	t.Cleanup(func() { listSessionFiles = prev })

	repos["/work/r"] = repoAt(allowedRepo)
	lookupInNewPass(s, "/work/r")

	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != allowedRepo {
		t.Fatalf("CWDIdentity = %s, want %s: a dead link deferred the transition", got, allowedRepo)
	}
	if got := s.state.Taints; len(got) != 1 || len(got[p]) != 1 {
		t.Fatalf("Taints = %+v, want only %s marked", got, p)
	}
}

// A cwd recorded with a resolved repository that now certainly reports none is
// not a transition yet: a volume not remounted after wake looks exactly like
// that. The recorded identity stays, nothing is marked, and the moment the
// fallback began is written down. When the repository answers again the grace
// is over and nothing happened; a fallback that lasts repositoryLostGrace is
// then recorded as the transition it turned out to be.
func TestObserveIdentity_lostRepositoryWaitsOutTheGrace(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(allowedRepo)}
	stubRepos(t, repos)
	advance := useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "unsent"))
	lookupInNewPass(s, "/work/r")

	repos["/work/r"] = notARepo()
	began := nowFn()
	lookupInNewPass(s, "/work/r")
	advance(repositoryLostGrace - time.Second)
	lookupInNewPass(s, "/work/r")
	st := reloadState(t, s)
	if id := st.CWDIdentity["/work/r"]; id.RepositoryID != allowedRepo || !id.FallbackSince.Equal(began) {
		t.Fatalf("CWDIdentity = %+v, want %s with the grace begun at %s", id, allowedRepo, began)
	}
	if len(st.Taints) != 0 {
		t.Fatalf("Taints = %+v inside the grace, want none", st.Taints)
	}

	repos["/work/r"] = repoAt(allowedRepo)
	lookupInNewPass(s, "/work/r")
	if id := reloadState(t, s).CWDIdentity["/work/r"]; id.RepositoryID != allowedRepo || !id.FallbackSince.IsZero() {
		t.Fatalf("CWDIdentity = %+v after the repository answered again, want the grace ended", id)
	}

	// A new loss starts a new grace, and one that lasts is believed.
	repos["/work/r"] = notARepo()
	lookupInNewPass(s, "/work/r")
	advance(repositoryLostGrace)
	lookupInNewPass(s, "/work/r")
	st = reloadState(t, s)
	if id := st.CWDIdentity["/work/r"]; id.RepositoryID != notARepo().RepositoryID || !id.FallbackSince.IsZero() {
		t.Fatalf("CWDIdentity = %+v after the grace, want the local fallback recorded", id)
	}
	if got := st.Taints[p]; len(got) != 1 || got[0].PrevRepositoryID != allowedRepo {
		t.Fatalf("Taints = %+v after the grace, want the file marked against %s", st.Taints, allowedRepo)
	}
}

// A cwd that never resolved to a repository gets no grace: there is nothing
// its "no repository" could be a brief loss of.
func TestObserveIdentity_neverResolvedCWDGetsNoGrace(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/plain": notARepo()}
	stubRepos(t, repos)
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, _ := newTailSyncer(t, endpoint, allowExampleOrg)
	lookupInNewPass(s, "/work/plain")

	other := notARepo()
	other.RepositoryID = "local:fedcba9876543210"
	repos["/work/plain"] = other
	lookupInNewPass(s, "/work/plain")
	if id := reloadState(t, s).CWDIdentity["/work/plain"]; id.RepositoryID != other.RepositoryID || !id.FallbackSince.IsZero() {
		t.Fatalf("CWDIdentity = %+v, want %s recorded at once", id, other.RepositoryID)
	}
}

// unreadable makes dir unreadable until the test ends, and skips the test
// where that cannot be done.
func unreadable(t *testing.T, dir string) (restore func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are POSIX")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads directories whatever their mode")
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	restore = func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Errorf("restore %s: %v", dir, err)
		}
	}
	t.Cleanup(restore)
	return restore
}

// A project directory that cannot be read right now is not an empty one. The
// ordinary listing would leave its files out without a word, the transition
// would be recorded with their unsent bytes unmarked, and once the directory
// came back they would be judged by the new repository alone. So the
// transition lists strictly, and waits.
func TestObserveIdentity_unreadableProjectDirectoryDefersTheTransition(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	lookupInNewPass(s, "/work/r")
	hidden := sessionDir(t, dir, "-proj-b")
	open, private := filepath.Join(dir, "s1.jsonl"), filepath.Join(hidden, "s2.jsonl")
	appendLines(t, open, sessionLine("/work/r", "unsent"))
	appendLines(t, private, sessionLine("/work/r", "private"))

	restore := unreadable(t, hidden)
	repos["/work/r"] = repoAt(allowedRepo)
	lookupInNewPass(s, "/work/r")
	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != excludedRepo {
		t.Fatalf("CWDIdentity moved to %s with a project directory unread", got)
	}
	if len(s.state.Taints) != 0 {
		t.Fatalf("a deferred transition left Taints %v", s.state.Taints)
	}

	restore()
	lookupInNewPass(s, "/work/r")
	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != allowedRepo {
		t.Fatalf("CWDIdentity = %s once the directory was readable, want %s", got, allowedRepo)
	}
	for _, f := range []string{open, private} {
		if got := s.state.Taints[f]; len(got) != 1 || got[0].Until != fileSize(t, f) {
			t.Fatalf("Taints[%s] = %+v, want one up to %d", f, got, fileSize(t, f))
		}
	}
}

// A file the state tracks and the disk still holds, but the listing does not
// name, means the listing is not the whole picture -- whatever the reason. A
// tracked file that is gone from disk is simply gone: session files are
// deleted all the time, and waiting for them would defer every transition
// forever.
func TestObserveIdentity_trackedFileMissingFromTheListing(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	lookupInNewPass(s, "/work/r")
	kept, left, deleted := filepath.Join(dir, "kept.jsonl"), filepath.Join(dir, "left-out.jsonl"), filepath.Join(dir, "deleted.jsonl")
	for _, f := range []string{kept, left, deleted} {
		appendLines(t, f, sessionLine("/work/r", "1"))
		s.state.SetOffset(f, 0)
	}
	if err := os.Remove(deleted); err != nil {
		t.Fatalf("remove: %v", err)
	}
	leaveOut := true
	prev := listSessionFiles
	listSessionFiles = func(claudeDir string) ([]string, error) {
		files, err := prev(claudeDir)
		if leaveOut {
			files = slices.DeleteFunc(files, func(f string) bool { return f == left })
		}
		return files, err
	}
	t.Cleanup(func() { listSessionFiles = prev })

	repos["/work/r"] = repoAt(allowedRepo)
	lookupInNewPass(s, "/work/r")
	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != excludedRepo {
		t.Fatalf("CWDIdentity moved to %s with a tracked file missing from the listing", got)
	}

	leaveOut = false
	lookupInNewPass(s, "/work/r")
	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != allowedRepo {
		t.Fatalf("CWDIdentity = %s with only a deleted file unlisted, want %s", got, allowedRepo)
	}
	if len(s.state.Taints[kept]) != 1 || len(s.state.Taints[left]) != 1 || len(s.state.Taints[deleted]) != 0 {
		t.Fatalf("Taints = %+v, want the two files on disk marked", s.state.Taints)
	}
}

// A tracked file that does not resolve is not always gone. When what cuts it
// off is a symlink that leads nowhere -- a project directory linked onto a
// volume that is detached right now -- its unsent lines come back with the
// volume, and a transition recorded meanwhile would leave them unmarked. So a
// tracked file behind a dead link defers the transition; a dead link with
// nothing tracked behind it is only a stray link.
func TestObserveIdentity_trackedFileBehindADeadLinkDefersTheTransition(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	lookupInNewPass(s, "/work/r")
	volume := filepath.Join(t.TempDir(), "volume")
	if err := os.Mkdir(volume, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	linked := filepath.Join(filepath.Dir(dir), "-proj-linked")
	if err := os.Symlink(volume, linked); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(volume, "never-there"), filepath.Join(filepath.Dir(dir), "-proj-stray")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	tracked := filepath.Join(linked, "s1.jsonl")
	appendLines(t, tracked, sessionLine("/work/r", "1"))
	s.state.SetOffset(tracked, fileSize(t, tracked))
	appendLines(t, tracked, sessionLine("/work/r", "private"))

	detached := volume + ".detached"
	if err := os.Rename(volume, detached); err != nil {
		t.Fatalf("detach: %v", err)
	}
	repos["/work/r"] = repoAt(allowedRepo)
	lookupInNewPass(s, "/work/r")
	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != excludedRepo {
		t.Fatalf("CWDIdentity moved to %s with a tracked file behind a dead link", got)
	}

	if err := os.Rename(detached, volume); err != nil {
		t.Fatalf("reattach: %v", err)
	}
	lookupInNewPass(s, "/work/r")
	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != allowedRepo {
		t.Fatalf("CWDIdentity = %s once the volume was back, want %s: the stray link must not defer it", got, allowedRepo)
	}
	if got := s.state.Taints[tracked]; len(got) != 1 || got[0].Until != fileSize(t, tracked) {
		t.Fatalf("Taints[%s] = %+v, want one up to %d", tracked, got, fileSize(t, tracked))
	}
}

// Two names for one file need not differ only in case. A hard link shows the
// same thing on any filesystem: the tracked name is left out of the listing,
// another name of the same file is in it, and that is not a missing file.
func TestObserveIdentity_trackedNameOfAListedFileIsNotMissing(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	lookupInNewPass(s, "/work/r")
	tracked, alias := filepath.Join(dir, "tracked.jsonl"), filepath.Join(dir, "alias.jsonl")
	appendLines(t, tracked, sessionLine("/work/r", "1"))
	s.state.SetOffset(tracked, 0)
	if err := os.Link(tracked, alias); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	prev := listSessionFiles
	listSessionFiles = func(claudeDir string) ([]string, error) {
		files, err := prev(claudeDir)
		return slices.DeleteFunc(files, func(f string) bool { return f == tracked }), err
	}
	t.Cleanup(func() { listSessionFiles = prev })

	repos["/work/r"] = repoAt(allowedRepo)
	lookupInNewPass(s, "/work/r")

	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != allowedRepo {
		t.Fatalf("CWDIdentity = %s, want %s: another name of a listed file deferred the transition", got, allowedRepo)
	}
	if got := s.state.Taints[alias]; len(got) != 1 || got[0].Until != fileSize(t, alias) {
		t.Fatalf("Taints[%s] = %+v, want the listed name marked up to %d", alias, got, fileSize(t, alias))
	}
}

// caseInsensitiveDir returns a directory on a filesystem that folds case, and
// skips the test where the temp directory is not on one.
func caseInsensitiveDir(t *testing.T, dir string) string {
	t.Helper()
	probe := filepath.Join(dir, "Probe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	defer os.Remove(probe)
	if _, err := os.Stat(filepath.Join(dir, "probe")); err != nil {
		t.Skip("the temp filesystem is case-sensitive")
	}
	return dir
}

// On a filesystem that folds case, a session file renamed only in case is
// still there under the spelling the state tracks: the old path stats fine and
// the listing names the new one. That is one file with two names, not a file
// the listing missed, and it must not defer the transition -- nothing would
// ever complete it, since every later listing looks the same.
//
// The file is marked under the name the listing gives it. What the old entry
// in the state means for that name -- it starts again from the first byte, as
// any renamed session file always has -- is not changed here.
func TestObserveIdentity_caseOnlyRenameIsNotAMissingFile(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	caseInsensitiveDir(t, dir)
	lookupInNewPass(s, "/work/r")
	tracked, renamed := filepath.Join(dir, "S1.jsonl"), filepath.Join(dir, "s1.jsonl")
	appendLines(t, tracked, sessionLine("/work/r", "1"))
	s.state.SetOffset(tracked, fileSize(t, tracked))
	if err := os.Rename(tracked, renamed); err != nil {
		t.Fatalf("rename: %v", err)
	}
	appendLines(t, renamed, sessionLine("/work/r", "2"))

	repos["/work/r"] = repoAt(allowedRepo)
	lookupInNewPass(s, "/work/r")

	if got := s.state.CWDIdentity["/work/r"].RepositoryID; got != allowedRepo {
		t.Fatalf("CWDIdentity = %s, want %s: a case-only rename deferred the transition", got, allowedRepo)
	}
	if got := s.state.Taints[renamed]; len(got) != 1 || got[0].Until != fileSize(t, renamed) {
		t.Fatalf("Taints[%s] = %+v, want the listed name marked up to %d", renamed, got, fileSize(t, renamed))
	}
}

// Without an allowlist nothing is judged, so nothing is recorded: the default
// path gains no state writes and no file listing. Turning an allowlist on
// later starts from a first observation.
func TestObserveIdentity_emptyAllowlistRecordsNothing(t *testing.T) {
	repos := map[string]gitctx.Context{"/work/r": repoAt(excludedRepo)}
	stubRepos(t, repos)
	listed := 0
	prev := listSessionFiles
	listSessionFiles = func(claudeDir string) ([]string, error) { listed++; return prev(claudeDir) }
	t.Cleanup(func() { listSessionFiles = prev })
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, nil)
	appendLines(t, filepath.Join(dir, "s1.jsonl"), sessionLine("/work/r", "unsent"))

	lookupInNewPass(s, "/work/r")
	repos["/work/r"] = repoAt(allowedRepo)
	lookupInNewPass(s, "/work/r")

	if st := reloadState(t, s); len(st.CWDIdentity) != 0 || len(st.Taints) != 0 {
		t.Fatalf("recorded CWDIdentity %v Taints %v without an allowlist", st.CWDIdentity, st.Taints)
	}
	if listed != 0 {
		t.Fatalf("listed session files %d times without an allowlist", listed)
	}
}

// A taint is dropped once the offset has passed it: there is nothing left
// below it to judge.
func TestTaint_droppedOnceOffsetPassesIt(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/r": repoAt(allowedRepo)})
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "1"))
	s.state.Taints = map[string][]Taint{p: {{CWD: "/work/r", Until: fileSize(t, p), PrevRepositoryID: excludedRepo}}}

	syncPass(t, s)
	if got := s.state.GetOffset(p); got != fileSize(t, p) {
		t.Fatalf("offset = %d, want the end of the file %d", got, fileSize(t, p))
	}
	syncPass(t, s)
	if got := reloadState(t, s).Taints; len(got) != 0 {
		t.Fatalf("Taints = %+v after the offset passed them", got)
	}
}

// A taint never reaches past the end of the file. When the file is rewritten
// shorter, bytes appended afterwards would otherwise begin below the old mark
// and be judged as if they had been there at the transition.
func TestTaint_clampedToAShorterFile(t *testing.T) {
	stubRepos(t, map[string]gitctx.Context{"/work/r": repoAt(allowedRepo)})
	useFakeClock(t)
	_, endpoint := newFreshMetaServer(t)
	s, dir := newTailSyncer(t, endpoint, allowExampleOrg)

	// Rewritten to less than the mark but not below the offset: the mark
	// follows the file down.
	p := filepath.Join(dir, "s1.jsonl")
	appendLines(t, p, sessionLine("/work/r", "1"), sessionLine("/work/r", "2"))
	s.state.SetOffset(p, 0)
	s.state.Taints = map[string][]Taint{p: {{CWD: "/work/r", Until: fileSize(t, p), PrevRepositoryID: excludedRepo}}}
	if err := os.WriteFile(p, []byte(sessionLine("/work/r", "1")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	syncPass(t, s)
	if got := reloadState(t, s).Taints[p]; len(got) != 1 || got[0].Until != fileSize(t, p) {
		t.Fatalf("Taints = %+v, want one clamped to %d", got, fileSize(t, p))
	}

	// Shrunk below the offset: the offset is reset to the new size, and the
	// mark, clamped to the same size, has nothing left below it.
	q := filepath.Join(dir, "s2.jsonl")
	appendLines(t, q, sessionLine("/work/r", "1"), sessionLine("/work/r", "2"))
	half := fileSize(t, q) / 2
	appendLines(t, q, sessionLine("/work/r", "3"))
	s.state.SetOffset(q, fileSize(t, q)-1)
	s.state.Taints[q] = []Taint{{CWD: "/work/r", Until: fileSize(t, q), PrevRepositoryID: excludedRepo}}
	if err := os.Truncate(q, half); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	syncPass(t, s)
	if got := reloadState(t, s).Taints[q]; len(got) != 0 {
		t.Fatalf("Taints = %+v after a shrink below the offset, want none", got)
	}
}
