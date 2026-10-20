package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// handover is a source folder that profile "prev" has been reading and profile
// "next" is being given. Both collect Codex only, so the folder is each one's
// single source line.
type handover struct {
	home, shared string
	prev, next   localdaemon.Spec
	prevEntry    profile.Entry
	nextEntry    profile.Entry
	old, newer   string // sessions: one written before the hand-over, one after
	unknown      string // a session the previous holder never read
}

const rolloutFmt = "rollout-2026-04-23T11-30-1%d-aaaaaaaa-0000-0000-0000-00000000000%d.jsonl"

func codexLine(text string) string {
	return `{"type":"response_item","timestamp":"2026-04-23T11:30:12.000Z","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}}` + "\n"
}

func codexProfile(t *testing.T, name, shared string, edit ...func(*profile.Profile)) (profile.Entry, localdaemon.Spec) {
	t.Helper()
	p := profile.NewDefault()
	p.User = profile.UserInfo{ID: "user-" + name, Name: name, Email: name + "@fixture.invalid", Team: "fixture"}
	p.Server.Endpoint, p.Server.SyncEndpoint = "http://127.0.0.1:1", "http://127.0.0.1:1"
	p.ClaudeConfigDir = filepath.Join(os.Getenv("HOME"), "claude-"+name)
	p.Options.SyncEnabled = false
	p.Options.CodexSyncEnabled = true
	p.Options.CodexDirs = []string{shared}
	for _, change := range edit {
		change(p)
	}
	if err := profile.SaveNamed(p, name); err != nil {
		t.Fatal(err)
	}
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return entry, profileJobSpec(entry, daemonOverrides{})
}

func newHandover(t *testing.T, editPrev ...func(*profile.Profile)) *handover {
	t.Helper()
	return newHandoverFor(t, editPrev, nil)
}

func newHandoverFor(t *testing.T, editPrev, editNext []func(*profile.Profile)) *handover {
	t.Helper()
	h := &handover{home: useTempSyncHome(t)}
	h.shared = filepath.Join(h.home, "shared-codex")
	sessions := filepath.Join(h.shared, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(n int, age time.Duration) string {
		path := filepath.Join(sessions, sprintf(rolloutFmt, n, n))
		if err := os.WriteFile(path, []byte(codexLine("hello")+codexLine("again")), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	h.old, h.unknown, h.newer = write(1, -2*time.Hour), write(2, -2*time.Hour), write(3, time.Hour)
	h.prevEntry, h.prev = codexProfile(t, "prev", h.shared, editPrev...)
	h.nextEntry, h.next = codexProfile(t, "next", h.shared, editNext...)
	return h
}

func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

// holderState saves the state the previous holder left: it had read the first
// line of old.
func (h *handover) holderState(t *testing.T, edit ...func(*syncer.State)) *syncer.State {
	t.Helper()
	state, err := syncer.LoadState(filepath.Join(h.prevEntry.RuntimeDir, "codex-sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	state.SetOffset(h.old, int64(len(codexLine("hello"))))
	state.Files[h.old].CWD = "/work/prev"
	for _, change := range edit {
		change(state)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	return state
}

func (h *handover) alloc(from, id string) localdaemon.Allocation {
	line := "codex:" + h.shared
	return localdaemon.Allocation{Allowed: []string{line}, Handoffs: []localdaemon.Handoff{{Line: line, From: from, ID: id}}}
}

// take acquires the lease for the allocation and releases it at once, which is
// all the arrangement of the cursor needs; it returns next's state as saved.
func (h *handover) take(t *testing.T, alloc localdaemon.Allocation) *syncer.State {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := h.next.Acquire(ctx, alloc)
	if err != nil {
		t.Fatal(err)
	}
	release()
	return h.nextState(t)
}

func (h *handover) nextState(t *testing.T) *syncer.State {
	t.Helper()
	state, err := syncer.LoadState(filepath.Join(h.nextEntry.RuntimeDir, "codex-sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestProfileJobAdoptsThePreviousHoldersCursorBeforeItsLeaseIsReported(t *testing.T) {
	h := newHandover(t)
	h.holderState(t, func(s *syncer.State) {
		s.ObserveCodexAccount(time.Now().Add(-time.Hour), h.shared, "acct-1")
	})
	state := h.take(t, h.alloc("named:prev", "h1"))

	got := state.Files[h.old]
	if got == nil || got.Offset != int64(len(codexLine("hello"))) || got.CWD != "/work/prev" {
		t.Fatalf("the cursor was not carried over: %+v", got)
	}
	mark, ok := state.SourceMark(h.shared)
	if !ok || mark.Via != syncer.SourceAdopted || mark.From != "named:prev" || mark.Handoff != "h1" || mark.AfterBlock {
		t.Fatalf("mark = %+v %v", mark, ok)
	}
	if fs := state.Files[h.unknown]; fs == nil || fs.SkippedAtFirstSync == 0 {
		t.Fatalf("a session the previous holder never read, from before the hand-over, is joined: %+v", fs)
	}
	if state.HasFile(h.newer) {
		t.Fatal("a session written after the hand-over began was skipped")
	}
	if state.CodexAccountAt(h.shared, time.Now()) != "acct-1" {
		t.Fatal("the account history of the folder did not come with it")
	}
	// The state on disk is what the supervisor will find when it is told the lease
	// is taken: the previous holder's own file is as it was.
	if prev, _, _ := syncer.LoadStateStrict(filepath.Join(h.prevEntry.RuntimeDir, "codex-sync-state.json")); prev.Files[h.old].Offset == 0 || len(prev.Sources) != 0 {
		t.Fatalf("the previous holder's state was changed: %+v", prev.Sources)
	}
}

func TestProfileJobJoinsWhenThereIsNoCursorToAdopt(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, h *handover)
		from  string
	}{
		{"the previous holder never had a state", func(*testing.T, *handover) {}, "named:prev"},
		{"the previous holder's state is damaged", func(t *testing.T, h *handover) {
			if err := os.WriteFile(filepath.Join(h.prevEntry.RuntimeDir, "codex-sync-state.json"), []byte(`{"files":{`), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "named:prev"},
		{"nobody is known to have held it", func(t *testing.T, h *handover) { h.holderState(t) }, ""},
		{"the previous holder's profile is deleted", func(t *testing.T, h *handover) {
			h.holderState(t)
			if err := os.RemoveAll(h.prevEntry.RuntimeDir); err != nil {
				t.Fatal(err)
			}
		}, "named:prev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandover(t)
			tc.setup(t, h)
			state := h.take(t, h.alloc(tc.from, "h2"))
			mark, ok := state.SourceMark(h.shared)
			if !ok || mark.Via != syncer.SourceJoined || mark.From != "" || mark.Handoff != "h2" {
				t.Fatalf("mark = %+v %v", mark, ok)
			}
			if state.GetOffset(h.old) == 0 || state.Files[h.old].SkippedAtFirstSync == 0 {
				t.Fatalf("what was there is skipped to its end: %+v", state.Files[h.old])
			}
			if state.HasFile(h.newer) {
				t.Fatal("a session written after the hand-over began was skipped")
			}
		})
	}
}

func TestProfileJobDoesNotAdoptTwice(t *testing.T) {
	h := newHandover(t)
	h.holderState(t)
	h.take(t, h.alloc("named:prev", "h1"))
	// The new holder moves on; a second lease for the same hand-over (a restart
	// before the daemon cleared it) must not bring the old cursor back.
	state := h.nextState(t)
	state.Files[h.old].Offset = 999
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if got := h.take(t, h.alloc("named:prev", "h1")).Files[h.old].Offset; got != 999 {
		t.Fatalf("the hand-over was applied again: offset %d", got)
	}
	// A later hand-over of the same folder is a new one.
	h.holderState(t, func(s *syncer.State) { s.Files[h.old].Offset = 777 })
	if got := h.take(t, h.alloc("named:prev", "h3")).Files[h.old].Offset; got != 777 {
		t.Fatalf("a new hand-over did not take the cursor: offset %d", got)
	}
}

func TestProfileJobFinishesAJoinThatWasInterrupted(t *testing.T) {
	h := newHandover(t)
	since := time.Now().Add(-90 * time.Minute)
	// A session written after the join began but before the lease that finishes
	// it is read, not skipped: the join's time is the one the first lease chose.
	between := filepath.Join(h.shared, "sessions", sprintf(rolloutFmt, 4, 4))
	if err := os.WriteFile(between, []byte(codexLine("x")), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	if err := os.Chtimes(between, at, at); err != nil {
		t.Fatal(err)
	}
	state := h.nextStateFor(t)
	state.MarkSource(h.shared, syncer.SourceMark{Since: since, Via: syncer.SourceJoining, Handoff: "h4"})
	state.SetOffset(h.old, 5) // already skipped before the interruption
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	got := h.take(t, h.alloc("", "h4"))
	if got.HasFile(between) {
		t.Fatal("a session written after the join's own start was skipped")
	}
	if fs := got.Files[h.unknown]; fs == nil || fs.SkippedAtFirstSync == 0 {
		t.Fatal("what was there before the join began is still skipped")
	}
	if got.Files[h.old].Offset != 5 {
		t.Fatal("a file the interrupted join had already handled was touched")
	}
	if mark, _ := got.SourceMark(h.shared); mark.Via != syncer.SourceJoined || !mark.Since.Equal(since) {
		t.Fatalf("mark = %+v", mark)
	}
}

func (h *handover) nextStateFor(t *testing.T) *syncer.State { return h.nextState(t) }

func TestProfileJobDeclinesSnapshotRebuildWhenTheHolderUsedAnAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(p *profile.Profile)
		want bool
	}{
		{"an allowlist", func(p *profile.Profile) { p.Options.CollectRepositoryPrefixes = []string{"host/org/"} }, true},
		{"no allowlist", func(p *profile.Profile) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := useTempSyncHome(t)
			claude := filepath.Join(home, "shared-claude")
			session := filepath.Join(claude, "projects", "p", "s.jsonl")
			writeSession(t, session)
			mk := func(name string, edit func(*profile.Profile)) profile.Entry {
				p := profile.NewDefault()
				p.User = profile.UserInfo{ID: "user-" + name, Name: name, Email: name + "@fixture.invalid", Team: "fixture"}
				p.Server.Endpoint, p.Server.SyncEndpoint = "http://127.0.0.1:1", "http://127.0.0.1:1"
				p.ClaudeConfigDir = claude
				edit(p)
				if err := profile.SaveNamed(p, name); err != nil {
					t.Fatal(err)
				}
				e, err := (profile.Registry{Root: profile.DefaultDir()}).Load(name)
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
			prev, next := mk("prev", tc.edit), mk("next", func(*profile.Profile) {})
			state, err := syncer.LoadState(filepath.Join(prev.RuntimeDir, "sync-state.json"))
			if err != nil {
				t.Fatal(err)
			}
			state.SetOffset(session, 5)
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}
			spec := profileJobSpec(next, daemonOverrides{})
			line := "claude:" + claude
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			release, err := spec.Acquire(ctx, localdaemon.Allocation{Allowed: []string{line}, Handoffs: []localdaemon.Handoff{{Line: line, From: "named:prev", ID: "h5"}}})
			if err != nil {
				t.Fatal(err)
			}
			release()
			got, err := syncer.LoadState(filepath.Join(next.RuntimeDir, "sync-state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if fs := got.Files[session]; fs == nil || fs.Offset != 5 || fs.RebuildDeclined != tc.want {
				t.Fatalf("adopted entry = %+v, want RebuildDeclined=%v", fs, tc.want)
			}
		})
	}
}

func TestProfileJobMarksANewSourceAndSkipsItsHistoryAsOnAFirstRun(t *testing.T) {
	h := newHandover(t)
	// A first lease with no hand-over: the state is made now, its history is not
	// collected, and the folder is marked as one the state began with.
	alloc := localdaemon.Allocation{Allowed: []string{"codex:" + h.shared}, AfterBlock: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := h.next.Acquire(ctx, alloc)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.next.Run(ctx, func(localdaemon.Progress) {}); err != nil {
		t.Fatal(err)
	}
	release()
	state := h.nextState(t)
	mark, ok := state.SourceMark(h.shared)
	if !ok || mark.Via != syncer.SourceInitial || !mark.AfterBlock || mark.Since.IsZero() {
		t.Fatalf("mark = %+v %v", mark, ok)
	}
	for _, f := range []string{h.old, h.unknown} {
		if fs := state.Files[f]; fs == nil || fs.SkippedAtFirstSync == 0 {
			t.Fatalf("history of a new state was backfilled or forgotten: %s %+v", f, fs)
		}
	}
	// Not blocked before: no mark of it.
	h2 := newHandover(t)
	rel2, err := h2.next.Acquire(ctx, localdaemon.Allocation{Allowed: []string{"codex:" + h2.shared}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.next.Run(ctx, func(localdaemon.Progress) {}); err != nil {
		t.Fatal(err)
	}
	rel2()
	if mark, _ := h2.nextState(t).SourceMark(h2.shared); mark.AfterBlock {
		t.Fatal("a profile that was not blocked is told it was")
	}
}

// A folder the state already knows from before simply gets its mark; nothing is
// skipped and nothing is read again.
func TestProfileJobLeavesAnOldStatesFilesAloneWhenMarkingASource(t *testing.T) {
	h := newHandover(t)
	state := h.nextState(t)
	state.SetOffset(h.old, 7)
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := h.next.Acquire(ctx, localdaemon.Allocation{Allowed: []string{"codex:" + h.shared}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.next.Run(ctx, func(localdaemon.Progress) {}); err != nil {
		t.Fatal(err)
	}
	release()
	got := h.nextState(t)
	if got.GetOffset(h.old) < 7 {
		t.Fatalf("offset of a known file went back: %d", got.GetOffset(h.old))
	}
	if mark, ok := got.SourceMark(h.shared); !ok || mark.Via != syncer.SourceInitial {
		t.Fatalf("mark = %+v %v", mark, ok)
	}
}

func TestProfileJobForgetsTheMarksOfSourcesItNoLongerReads(t *testing.T) {
	h := newHandover(t)
	state := h.nextState(t)
	gone := filepath.Join(h.home, "gone")
	state.MarkSource(gone, syncer.SourceMark{Since: time.Now(), Via: syncer.SourceAdopted, From: "x", Handoff: "h"})
	state.SetOffset(filepath.Join(gone, "sessions", "f.jsonl"), 3)
	state.MarkSource(h.shared, syncer.SourceMark{Since: time.Now(), Via: syncer.SourceInitial})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := h.next.Acquire(ctx, localdaemon.Allocation{Allowed: []string{"codex:" + h.shared}})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.next.Run(ctx, func(localdaemon.Progress) {}); err != nil {
		t.Fatal(err)
	}
	release()
	got := h.nextState(t)
	if _, ok := got.SourceMark(gone); ok {
		t.Fatal("the mark of a folder it stopped reading stays")
	}
	if !got.HasFile(filepath.Join(gone, "sessions", "f.jsonl")) {
		t.Fatal("its cursors went with the mark; a hand-over back would start over")
	}
}

// A hand-over saves its work before the lease is reported, and that save closes
// the first-run window of a state made just now. The other folders of the same
// state must still skip their history, as they would have on the first pass.
func TestProfileJobKeepsTheFirstRunSkipForOtherFoldersWhenAHandoverSavesFirst(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other-codex")
	otherFile := filepath.Join(other, "sessions", sprintf(rolloutFmt, 5, 5))
	if err := os.MkdirAll(filepath.Dir(otherFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherFile, []byte(codexLine("history")), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(otherFile, old, old); err != nil {
		t.Fatal(err)
	}
	h := newHandoverFor(t, nil, []func(*profile.Profile){func(p *profile.Profile) { p.Options.CodexDirs = append(p.Options.CodexDirs, other) }})
	h.holderState(t)
	alloc := h.alloc("named:prev", "h6")
	alloc.Allowed = append(alloc.Allowed, "codex:"+other)
	state := h.take(t, alloc)
	if fs := state.Files[otherFile]; fs == nil || fs.SkippedAtFirstSync == 0 {
		t.Fatalf("the history of the folder that was not handed over was backfilled: %+v", fs)
	}
	if mark, _ := state.SourceMark(other); mark.Via != syncer.SourceInitial {
		t.Fatalf("other folder's mark = %+v", mark)
	}
}

// A lease reports taken only the hand-overs it took. An explicit Codex home that
// fails its check when the lease is taken is not scanned and its cursor is not
// arranged, so its hand-over stays with the daemon for a later lease, which can
// still adopt the previous holder's cursor once the home is back.
func TestProfileJobReportsOnlyTheHandoversItTook(t *testing.T) {
	home := useTempSyncHome(t)
	configured := filepath.Join(home, "configured-codex")
	if err := os.MkdirAll(filepath.Join(configured, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	explicit, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entry, spec := codexProfile(t, "next", configured, func(p *profile.Profile) { p.CodexHome = explicit })
	kept, lost := "codex:"+configured, "codex:"+explicit
	if !slices.Contains(spec.Sources, kept) || !slices.Contains(spec.Sources, lost) {
		t.Fatalf("fixture: sources %v", spec.Sources)
	}
	// The home turns into a symlink after the spec was built.
	if err := os.RemoveAll(explicit); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, explicit); err != nil {
		t.Fatal(err)
	}
	alloc := localdaemon.Allocation{Allowed: []string{kept, lost}, Handoffs: []localdaemon.Handoff{
		{Line: kept, From: "named:prev", ID: "h1"},
		{Line: lost, From: "named:prev", ID: "h2"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := spec.Acquire(ctx, alloc)
	if err != nil {
		t.Fatal(err)
	}
	release()
	took := alloc.Handoffs // what the daemon takes as taken unless the job says otherwise
	if spec.TookOver != nil {
		took = spec.TookOver()
	}
	if !slices.Equal(took, alloc.Handoffs[:1]) {
		t.Fatalf("the lease reports %+v taken, want only the home it read", took)
	}
	state, err := syncer.LoadState(filepath.Join(entry.RuntimeDir, "codex-sync-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mark, ok := state.SourceMark(explicit); ok {
		t.Fatalf("the home that was not read is marked: %+v", mark)
	}
}

func TestAdoptionPathTakesOnlyWhatTheNewFoldersCollectorReads(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk := func(rel string) string {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	n := mk("n")
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(n, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	file := func(base, rel string) string { return filepath.Join(base, filepath.FromSlash(rel)) }
	check := func(name, newFolder, key, want string) {
		t.Helper()
		got, ok := adoptionPath("codex", newFolder)(key)
		if want == "" {
			if ok {
				t.Errorf("%s: %s was taken as %s", name, key, got)
			}
			return
		}
		if !ok || got != want {
			t.Errorf("%s: %s -> %q %v, want %q", name, key, got, ok, want)
		}
	}

	// One folder under two spellings: everything the collector reads, renamed.
	check("alias", n, file(alias, "sessions/rollout-a.jsonl"), file(n, "sessions/rollout-a.jsonl"))
	check("alias, deleted subfolders", n, file(alias, "sessions/2026/10/15/rollout-b.jsonl"), file(n, "sessions/2026/10/15/rollout-b.jsonl"))
	check("alias, not under a scan root", n, file(alias, "other/rollout-c.jsonl"), "")
	check("another folder", n, file(mk("elsewhere"), "sessions/rollout-d.jsonl"), "")

	// The previous holder's folder contains the new one: only the intersection.
	outer, inner := mk("outer"), mk("outer/sub")
	check("outer to inner, a file in both", inner, file(outer, "sub/sessions/rollout-e.jsonl"), file(inner, "sessions/rollout-e.jsonl"))
	check("outer to inner, a file only the outer reads", inner, file(outer, "sessions/rollout-f.jsonl"), "")

	// The new folder contains the previous holder's: its files are read from
	// below a folder, which this folder's collector does not look into.
	check("inner to outer", outer, file(inner, "sessions/rollout-g.jsonl"), "")
}

func TestAdoptionHomeNamesOnlyTheSameFolder(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n, sub := filepath.Join(root, "n"), filepath.Join(root, "n", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(n, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	home := adoptionHome(n)
	if got, ok := home(alias); !ok || got != n {
		t.Fatalf("same folder under another name: %q %v", got, ok)
	}
	for _, other := range []string{sub, root, filepath.Join(root, "absent")} {
		if got, ok := home(other); ok {
			t.Fatalf("%s was taken as the home %s", other, got)
		}
	}
}
