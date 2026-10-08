package syncer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cctrace/internal/gitctx"
	"cctrace/internal/sessionlog"
)

// Under a repository allowlist a session record may leave only if the
// repository it was written in is allowed. The record does not say which
// repository that was -- it carries a cwd, and a cwd can come to hold another
// repository (a path reused for a different checkout, a worktree replaced)
// between the line being written and the pass that sends it.
//
// So the syncer writes down what each cwd was last certainly seen to hold
// (CWDIdentity), and when a certain lookup finds something else there, marks
// where every session file ended at that moment (Taint). Positions, not times:
// a line's place in its file does not depend on any clock, and a last line
// still being written starts below the mark like every line before it.

// CWDIdentity is the repository a working directory was last certainly seen to
// hold. Only certain lookups update it.
type CWDIdentity struct {
	RepositoryID string `json:"repository_id"`
	// Source is the lookup's RepositoryIDSource: "resolved" for a remote
	// identity, "fallback" for a local one.
	Source string `json:"source,omitempty"`
	// FallbackSince is when a cwd recorded with a resolved identity started
	// certainly reporting a local fallback. Zero outside that grace (see
	// repositoryLostGrace).
	FallbackSince time.Time `json:"fallback_since,omitempty"`
}

// repositoryLostGrace is how long a cwd that resolved to a repository may keep
// reporting none before that is believed.
const repositoryLostGrace = 15 * time.Minute

// Taint marks the part of one session file that was already on disk, unsent,
// when CWD was found holding another repository than before.
//
// Until is the file's size at that moment. It does not say the bytes below it
// were written under the previous repository -- the replacement happened some
// time before it was noticed, and lines written in between are below Until as
// well. It says only that a line of CWD starting below Until may belong to
// either, which is why PrevRepositoryID is kept: such a line is judged against
// both.
type Taint struct {
	CWD              string `json:"cwd"`
	Until            int64  `json:"until"`
	PrevRepositoryID string `json:"prev_repository_id"`
}

// listSessionFiles lists the session files a transition marks, indirected for
// tests. The strict listing, not the one a pass uses: a directory that cannot
// be read right now must stop the transition, not shorten the list (see
// taintUnsent).
var listSessionFiles = sessionlog.FindJSONLFilesStrict

// observeIdentity records the repository a certain lookup of cwd returned, and
// returns "" when the cwd's records can be judged by that lookup, or the
// reason they must be held instead.
//
// The first observation of a cwd only records it: what the cwd held before
// anyone looked is not known, the same as after any restart. A repository
// other than the recorded one is a transition, and before it is recorded every
// session file's unsent bytes are tainted (see taintUnsent). If that cannot be
// done completely the recorded identity is left alone, so the next lookup finds
// the transition again.
//
// One transition is not believed at once: a cwd recorded with a resolved
// identity that now certainly reports a local fallback. A volume not remounted
// after wake and a reconnecting share look exactly like a deleted repository,
// and believing them would drop an allowed repository's records for good. The
// fallback starts a grace instead (FallbackSince), the records are held, and
// only a fallback that has lasted repositoryLostGrace becomes the transition.
// A resolved lookup of the recorded repository ends the grace. A cwd that
// never resolved gets none.
//
// Taints and the new identity are saved together. If the save fails this pass
// goes on with what is in memory, and after a restart the transition is found
// again and marks each file at its then larger size.
//
// Called with s.mu held.
func (s *Syncer) observeIdentity(cwd string, g gitctx.Context) (hold string) {
	if cwd == "" || g.RepositoryID == "" {
		return ""
	}
	prev := s.state.CWDIdentity[cwd]
	if prev != nil && prev.RepositoryID == g.RepositoryID {
		if !prev.FallbackSince.IsZero() {
			prev.FallbackSince = time.Time{}
			s.saveState()
		}
		return ""
	}
	if prev != nil {
		if prev.Source == "resolved" && g.RepositoryIDSource == "fallback" {
			now := nowFn()
			if prev.FallbackSince.IsZero() {
				prev.FallbackSince = now
				s.saveState()
			}
			if now.Sub(prev.FallbackSince) < repositoryLostGrace {
				return HeldReasonRepositoryLostGrace
			}
		}
		n, err := s.taintUnsent(cwd, prev.RepositoryID)
		if err != nil {
			log.Printf("[syncer] %s: repository changed, but the unsent session records could not be marked (%v); will retry", cwd, err)
			return HeldReasonTransitionPending
		}
		log.Printf("[syncer] %s: repository changed; unsent records in %d session files are judged against both", cwd, n)
	}
	if s.state.CWDIdentity == nil {
		s.state.CWDIdentity = make(map[string]*CWDIdentity)
	}
	s.state.CWDIdentity[cwd] = &CWDIdentity{RepositoryID: g.RepositoryID, Source: g.RepositoryIDSource}
	s.saveState()
	return ""
}

// taintUnsent marks, in every session file with bytes past its offset, where
// the file ends now, and reports how many files it marked. The files are
// listed again rather than taken from the pass, so one created since the pass
// began is marked too.
//
// Nothing is marked unless the list is known to be whole and every file in it
// that exists could be sized. A file left out here keeps its unsent bytes
// unmarked, and once the new identity is recorded nothing would ever look for
// them again. So a directory that cannot be read is an error, and so is a file
// the state tracks under this Claude home that is still on disk and yet not
// listed.
//
// What cannot hold unsent bytes does not hold the transition up either --
// each of these would look the same on every later lookup, and waiting for
// them would wait forever:
//   - a listed name that leads nowhere: a file deleted since the listing, or a
//     .jsonl symlink that dangles or points at itself;
//   - a tracked file that is gone: session files are deleted all the time;
//   - a tracked path that is the same file as a listed one: on a filesystem
//     that folds case, a file renamed only in case still answers to the
//     spelling the state has. The file is marked under the name the listing
//     gives it, and the old entry needs no mark of its own.
//
// Gone is not the same as cut off. A tracked file that does not resolve
// because a symlink above it leads nowhere -- a project directory linked onto
// a volume that is detached right now -- comes back with its unsent lines when
// the volume does, so it defers the transition like an unreadable directory.
func (s *Syncer) taintUnsent(cwd, prevRepositoryID string) (int, error) {
	files, err := listSessionFiles(s.claudeDir)
	if err != nil {
		return 0, err
	}
	sizes := make(map[string]int64)
	listed := make(map[string]os.FileInfo, len(files))
	for _, f := range files {
		fi, err := os.Stat(f)
		if leadsNowhere(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		listed[f] = fi
		if fi.Size() > s.state.GetOffset(f) {
			sizes[f] = fi.Size()
		}
	}
	projects := filepath.Join(normalizeHome(s.claudeDir), "projects")
	for tracked := range s.state.Files {
		if listed[tracked] != nil || !strings.HasPrefix(tracked, projects+string(filepath.Separator)) {
			continue
		}
		fi, err := os.Stat(tracked)
		if errors.Is(err, fs.ErrNotExist) {
			if link := deadLinkAbove(projects, tracked); link != "" {
				return 0, fmt.Errorf("%s is tracked and cut off by the symlink %s, which leads nowhere", tracked, link)
			}
			continue
		}
		if err != nil {
			return 0, err
		}
		if !sameAsAny(fi, listed) {
			return 0, fmt.Errorf("%s is tracked and on disk but was not listed", tracked)
		}
	}
	if s.state.Taints == nil {
		s.state.Taints = make(map[string][]Taint)
	}
	for f, size := range sizes {
		s.state.Taints[f] = append(s.state.Taints[f], Taint{CWD: cwd, Until: size, PrevRepositoryID: prevRepositoryID})
	}
	return len(sizes), nil
}

// sameAsAny reports whether fi is the same file as one of infos.
func sameAsAny(fi os.FileInfo, infos map[string]os.FileInfo) bool {
	for _, other := range infos {
		if os.SameFile(fi, other) {
			return true
		}
	}
	return false
}

// leadsNowhere reports whether a stat error means there is nothing at the
// path and nothing can come to be there by waiting on a directory: it does not
// exist, or it is a symlink that loops.
func leadsNowhere(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP)
}

// deadLinkAbove returns the first symlink on the way from root down to path --
// path itself included -- that does not resolve, or "" when path is missing
// for the ordinary reason that it, or a directory above it, was removed.
func deadLinkAbove(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	at := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		at = filepath.Join(at, part)
		fi, err := os.Lstat(at)
		if err != nil {
			return ""
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			if _, err := os.Stat(at); err != nil {
				return at
			}
		}
	}
	return ""
}

// pruneTaints clamps filePath's taints to the file's current size and drops
// the ones offset has reached. Called before each scan, after a shrunk file's
// offset has been reset.
//
// The clamp matters when the file was rewritten shorter: bytes appended later
// would begin below the old mark and be judged as if they had been there at
// the transition. It costs one stat per scan of a file that still carries a
// taint; a file without one is not touched.
func (s *Syncer) pruneTaints(filePath string, offset int64) {
	taints := s.state.Taints[filePath]
	if len(taints) == 0 {
		return
	}
	size := int64(math.MaxInt64)
	if fi, err := os.Stat(filePath); err == nil {
		size = fi.Size()
	}
	var kept []Taint
	changed := false
	for _, t := range taints {
		if t.Until > size {
			t.Until = size
			changed = true
		}
		if t.Until <= offset {
			changed = true
			continue
		}
		kept = append(kept, t)
	}
	if !changed {
		return
	}
	if len(kept) == 0 {
		delete(s.state.Taints, filePath)
	} else {
		s.state.Taints[filePath] = kept
	}
	s.saveState()
}

// lastCWDWindow is how many bytes before the offset lastCWDBefore reads first,
// and lastCWDWindowMax the widest it reads before giving up. Vars so tests can
// reach both ends with a small file.
var (
	lastCWDWindow    int64 = 64 << 10
	lastCWDWindowMax int64 = 16 << 20
)

// lastCWDBefore returns the cwd of the last line before offset that carries
// one, or "" when there is none within lastCWDWindowMax.
//
// It reads a window ending at offset and looks through its lines from the
// last, widening the window fourfold while it finds nothing: most lines carry
// a cwd, so the first window nearly always answers, and a long run of metadata
// lines costs more reads rather than a wrong answer. A window that opens inside
// a line leaves that line to the next, wider one.
func lastCWDBefore(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	for window := lastCWDWindow; ; window *= 4 {
		start := max(offset-window, 0)
		buf := make([]byte, offset-start)
		if _, err := f.ReadAt(buf, start); err != nil {
			return ""
		}
		if start > 0 {
			_, buf, _ = bytes.Cut(buf, []byte("\n"))
		}
		for len(buf) > 0 {
			i := bytes.LastIndexByte(buf, '\n')
			line := buf[i+1:]
			buf = buf[:max(i, 0)]
			var r struct {
				CWD string `json:"cwd"`
			}
			if json.Unmarshal(line, &r) == nil && r.CWD != "" {
				return r.CWD
			}
		}
		if start == 0 || window >= lastCWDWindowMax {
			return ""
		}
	}
}

// advanceOffset moves filePath's offset past bytes that were scanned, and
// records the cwd they ended in: lastCWD is the cwd of the last consumed
// record that carries one, and "" -- none of them carried one -- leaves the
// recorded cwd standing. cwdUnknown says the chain was broken after that: the
// scan skipped an oversized line with no record naming a cwd behind it, so
// what the next lines continue from is not known. It returns the file's
// state; the caller saves.
func (s *Syncer) advanceOffset(filePath string, newOffset int64, lastCWD string, cwdUnknown bool) *FileState {
	s.state.SetOffset(filePath, newOffset)
	fs := s.state.Files[filePath]
	switch {
	case cwdUnknown:
		fs.CWD, fs.CWDUnknown = "", true
	case lastCWD != "":
		fs.CWD, fs.CWDUnknown = lastCWD, false
	}
	return fs
}

// resetLastCWD is for the two advances that move the offset without reading
// what they pass -- the first sync's skip and a shrink reset. Whatever cwd was
// recorded described other bytes, so it is cleared, and under an allowlist the
// cwd the bytes before offset end in is read back from the file. The head of
// the file is not a substitute: it is where the session started. The caller
// saves.
//
// Without an allowlist nothing reads the recorded cwd, and the file is not
// read for it; it is recovered when an allowlist first needs it.
func (s *Syncer) resetLastCWD(filePath string, offset int64) {
	fs := s.state.Files[filePath]
	fs.CWD, fs.CWDUnknown = "", false
	if len(s.collectPrefixes) > 0 {
		s.recoverLastCWD(fs, filePath, offset)
	}
}

// recoverLastCWD reads back the cwd the bytes before offset end in. When no
// line there carries one the cwd is marked unknown rather than guessed.
func (s *Syncer) recoverLastCWD(fs *FileState, filePath string, offset int64) {
	fs.CWD = lastCWDBefore(filePath, offset)
	fs.CWDUnknown = fs.CWD == ""
}

// lastScanCWD returns what a scan leaves for advanceOffset: the cwd of the
// last record that carries one, or unknown when an oversized line was skipped
// after it. The skipped line may be where the session changed cwd, so the
// records' last cwd no longer says where the scan ended.
func lastScanCWD(records []*sessionlog.Record, skipped []sessionlog.SkippedSpan) (cwd string, unknown bool) {
	named := int64(-1)
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].CWD != "" {
			cwd, named = records[i].CWD, records[i].Offset
			break
		}
	}
	if n := len(skipped); n > 0 && skipped[n-1].Start > named {
		return "", true
	}
	return cwd, false
}

// saveState saves the state, logging a failure: a state that cannot be saved
// must not stop collection.
func (s *Syncer) saveState() {
	s.stateDirty = false
	if err := s.state.Save(); err != nil {
		log.Printf("[syncer] failed to save state: %v", err)
	}
}

// flushState saves what a pass changed without saving it at once.
func (s *Syncer) flushState() {
	if s.stateDirty {
		s.saveState()
	}
}
