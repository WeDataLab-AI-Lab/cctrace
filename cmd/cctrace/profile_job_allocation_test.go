package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
	"cctrace/internal/usage"
)

// allocWorld is a profile that collects from every kind of source, with a session
// on disk in each source folder.
type allocWorld struct {
	home  string
	entry profile.Entry
	spec  localdaemon.Spec
	// Source folders and the session file in each.
	claudeDir, codexDefault, codexExtra, gjcDefault, gjcExtra, omoDefault, omoExtra string
	files                                                                           map[string]string // folder -> session file
}

func writeSession(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"user","timestamp":"2026-10-12T09:00:00Z","sessionId":"x","message":{"role":"user","content":"x"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newAllocWorld(t *testing.T, name string, edit ...func(*profile.Profile)) *allocWorld {
	t.Helper()
	w := &allocWorld{files: map[string]string{}}
	w.home = useTempSyncHome(t)
	w.claudeDir = filepath.Join(w.home, "claude-"+name)
	w.codexDefault = filepath.Join(w.home, ".codex")
	w.codexExtra = filepath.Join(w.home, "codex-extra-"+name)
	w.gjcDefault, w.gjcExtra = filepath.Join(w.home, ".gjc"), filepath.Join(w.home, "gjc-extra-"+name)
	w.omoDefault, w.omoExtra = filepath.Join(w.home, ".omo"), filepath.Join(w.home, "omo-extra-"+name)
	session := func(dir, rel string) {
		path := filepath.Join(dir, rel)
		writeSession(t, path)
		w.files[dir] = strings.TrimPrefix(path, w.home)
	}
	session(w.claudeDir, filepath.Join("projects", "p", "s.jsonl"))
	for _, dir := range []string{w.codexDefault, w.codexExtra} {
		session(dir, filepath.Join("sessions", "rollout-2026-04-23T11-30-10-aaaaaaaa-0000-0000-0000-000000000001.jsonl"))
	}
	for _, dir := range []string{w.gjcDefault, w.gjcExtra} {
		session(dir, filepath.Join("agent", "sessions", "v2-p", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000001.jsonl"))
	}
	for _, dir := range []string{w.omoDefault, w.omoExtra} {
		session(dir, filepath.Join("sessions", "--p--", "2026-01-01T00-00-00-000Z_019f0000-0000-7000-8000-000000000001.jsonl"))
	}
	p := profile.NewDefault()
	p.User = profile.UserInfo{ID: "user-" + name, Name: name, Email: name + "@fixture.invalid", Team: "fixture"}
	p.Server.Endpoint, p.Server.SyncEndpoint = "http://127.0.0.1:1", "http://127.0.0.1:1"
	p.ClaudeConfigDir = w.claudeDir
	p.Options.SyncEnabled, p.Options.CodexSyncEnabled, p.Options.GjcSyncEnabled, p.Options.OmoSyncEnabled = true, true, true, true
	p.Options.CodexDirs, p.Options.GjcDirs, p.Options.OmoDirs = []string{w.codexExtra}, []string{w.gjcExtra}, []string{w.omoExtra}
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
	w.entry, w.spec = entry, profileJobSpec(entry, daemonOverrides{})
	return w
}

func (w *allocWorld) line(kind, dir string) string { return kind + ":" + dir }

// pass takes a lease for the allocation, runs one pass and returns which session
// files the profile's states now know.
func (w *allocWorld) pass(t *testing.T, alloc localdaemon.Allocation) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := w.spec.Acquire(ctx, alloc)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.spec.Run(ctx, func(localdaemon.Progress) {}); err != nil {
		t.Fatal(err)
	}
	release()
	var known []string
	for _, name := range []string{"sync-state.json", "codex-sync-state.json", "gjc-sync-state.json", "omo-sync-state.json"} {
		state, err := syncer.LoadState(filepath.Join(w.entry.RuntimeDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for file := range state.Files {
			known = append(known, strings.TrimPrefix(file, w.home))
		}
	}
	slices.Sort(known)
	return known
}

func (w *allocWorld) filesOf(dirs ...string) []string {
	var out []string
	for _, d := range dirs {
		out = append(out, w.files[d])
	}
	slices.Sort(out)
	return out
}

func TestProfileJobReadsOnlyTheLinesItIsAllowed(t *testing.T) {
	tests := []struct {
		name  string
		allow func(w *allocWorld) []string
		reads func(w *allocWorld) []string
	}{
		{"every line", func(w *allocWorld) []string { return w.spec.Sources }, func(w *allocWorld) []string {
			return w.filesOf(w.claudeDir, w.codexDefault, w.codexExtra, w.gjcDefault, w.gjcExtra, w.omoDefault, w.omoExtra)
		}},
		{"no Claude line, so no Claude pass", func(w *allocWorld) []string {
			return slices.DeleteFunc(slices.Clone(w.spec.Sources), func(l string) bool { return l == w.line("claude", w.claudeDir) })
		}, func(w *allocWorld) []string {
			return w.filesOf(w.codexDefault, w.codexExtra, w.gjcDefault, w.gjcExtra, w.omoDefault, w.omoExtra)
		}},
		{"one of two Codex homes", func(w *allocWorld) []string {
			return []string{w.line("codex", w.codexExtra), w.line("claude", w.claudeDir)}
		}, func(w *allocWorld) []string { return w.filesOf(w.claudeDir, w.codexExtra) }},
		{"one of two gjc homes and one of two omo homes", func(w *allocWorld) []string {
			return []string{w.line("gjc", w.gjcDefault), w.line("omo", w.omoExtra)}
		}, func(w *allocWorld) []string { return w.filesOf(w.gjcDefault, w.omoExtra) }},
		{"nothing at all", func(w *allocWorld) []string { return nil }, func(w *allocWorld) []string { return nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newAllocWorld(t, "one")
			if !slices.Contains(w.spec.Sources, w.line("codex", w.codexDefault)) {
				t.Fatalf("fixture: %v", w.spec.Sources)
			}
			got := w.pass(t, localdaemon.Allocation{Allowed: tc.allow(w)})
			if want := tc.reads(w); !slices.Equal(got, want) && (len(got) != 0 || len(want) != 0) {
				t.Fatalf("the states know %v, want %v", got, want)
			}
		})
	}
}

// fakeUsage replaces the quota fetch for the rest of the test and counts calls.
func fakeUsage(t *testing.T) *int {
	t.Helper()
	calls := 0
	prev, prevInterval := fetchClaudeUsage, quotaInterval
	fetchClaudeUsage = func(context.Context, string) (*usage.Response, error) {
		calls++
		return nil, errors.New("no usage in tests")
	}
	quotaInterval = 0
	t.Cleanup(func() { fetchClaudeUsage, quotaInterval = prev, prevInterval })
	return &calls
}

// The Claude folder's quota is the folder's: a profile that does not read the
// folder does not ask for it.
func TestProfileJobAsksForClaudeQuotaOnlyWhenItReadsTheFolder(t *testing.T) {
	for _, tc := range []struct {
		name         string
		withClaude   bool
		wantPolled   bool
		sourceEnable bool
	}{
		{"reads the folder", true, true, true},
		{"the folder is held by another profile", false, false, true},
		{"Claude collection is off, as it always has been", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeUsage(t)
			w := newAllocWorld(t, "quota", func(p *profile.Profile) { p.Options.SyncEnabled = tc.sourceEnable })
			alloc := localdaemon.Allocation{Allowed: slices.DeleteFunc(slices.Clone(w.spec.Sources), func(l string) bool {
				return !tc.withClaude && l == w.line("claude", w.claudeDir)
			})}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			release, err := w.spec.Acquire(ctx, alloc)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			for range 2 { // the first pass only starts the quota clock
				if err := w.spec.Run(ctx, func(localdaemon.Progress) {}); err != nil {
					t.Fatal(err)
				}
			}
			if polled := *calls > 0; polled != tc.wantPolled {
				t.Fatalf("polled = %v, want %v", polled, tc.wantPolled)
			}
		})
	}
}

// A profile with nothing to upload still runs -- local analysis needs the lease --
// whether it never had a source or lost every one to another profile.
func TestProfileJobWithNoLineToReadStillRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(p *profile.Profile)
	}{
		{"local analysis only", func(p *profile.Profile) {
			p.Options.SyncEnabled, p.Options.CodexSyncEnabled, p.Options.GjcSyncEnabled, p.Options.OmoSyncEnabled = false, false, false, false
		}},
		{"every source held by another profile", func(p *profile.Profile) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newAllocWorld(t, "local", func(p *profile.Profile) {
				p.Options.LocalAnalysisEnabled = true
				tc.edit(p)
			})
			old := startManagedProfileRuntime
			t.Cleanup(func() { startManagedProfileRuntime = old })
			started := false
			startManagedProfileRuntime = func(context.Context, profile.Entry, string) (func(), error) {
				started = true
				return func() {}, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			release, err := w.spec.Acquire(ctx, localdaemon.Allocation{})
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			var progress localdaemon.Progress
			if err := w.spec.Run(ctx, func(p localdaemon.Progress) { progress = p }); err != nil {
				t.Fatal(err)
			}
			if !started || progress.State != "healthy" || progress.ErrorCode != "" || progress.Sent != 0 {
				t.Fatalf("runtime started %v, progress %+v", started, progress)
			}
		})
	}
}

// minimalProfileSpec saves a profile that collects Claude sessions only, in the
// home the test already has.
func minimalProfileSpec(t *testing.T, name string) localdaemon.Spec {
	t.Helper()
	p := profile.NewDefault()
	p.User = profile.UserInfo{ID: "user-" + name, Name: name, Email: name + "@fixture.invalid", Team: "fixture"}
	p.Server.Endpoint, p.Server.SyncEndpoint = "http://127.0.0.1:1", "http://127.0.0.1:1"
	p.ClaudeConfigDir = filepath.Join(os.Getenv("HOME"), "claude-"+name)
	if err := profile.SaveNamed(p, name); err != nil {
		t.Fatal(err)
	}
	entry, err := (profile.Registry{Root: profile.DefaultDir()}).Load(name)
	if err != nil {
		t.Fatal(err)
	}
	return profileJobSpec(entry, daemonOverrides{})
}

// The lease holds the locks of the lines it reads, and a line in use is told
// apart from a failure so the supervisor retries it quietly.
func TestProfileJobTakesTheLockOfEveryLineItReads(t *testing.T) {
	w := newAllocWorld(t, "lock")
	root := profile.DefaultDir()
	alloc := localdaemon.Allocation{Allowed: w.spec.Sources, Routes: []string{"0000000b", "0000000a"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := w.spec.Acquire(ctx, alloc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localdaemon.LockLines(ctx, root, []string{"0000000a"}, 0); !errors.Is(err, localdaemon.ErrLineBusy) {
		t.Fatalf("the lines of a lease are not locked: %v", err)
	}
	// Another run wanting one of the lines is refused, and takes nothing with it.
	other := minimalProfileSpec(t, "other")
	_, err = other.Acquire(ctx, localdaemon.Allocation{Allowed: other.Sources, Routes: []string{"0000000c", "0000000b"}})
	if !errors.Is(err, localdaemon.ErrLineBusy) {
		t.Fatalf("a line in use must be localdaemon.ErrLineBusy, got %v", err)
	}
	if free, err := localdaemon.LockLines(ctx, root, []string{"0000000c"}, 0); err != nil {
		t.Fatalf("the refused lease kept a lock it had taken: %v", err)
	} else {
		free()
	}
	leaseCtx, leaseCancel := context.WithTimeout(ctx, 2*time.Second)
	defer leaseCancel()
	again, err := localdaemon.AcquireProfile(leaseCtx, other.RuntimeDir)
	if err != nil {
		t.Fatalf("the refused lease kept the profile lock: %v", err)
	}
	again()
	release()
	free, err := localdaemon.LockLines(ctx, root, []string{"0000000a", "0000000b"}, 0)
	if err != nil {
		t.Fatalf("releasing the lease must free the lines: %v", err)
	}
	free()
}

// A source line is described by where the profile got its folder from, which
// is what a person reading "not collected here" is told. The explicit Codex home
// is told apart from the rest because its path is not to be shown.
func TestProfileJobSpecLabelsEachSourceByWhereItCameFrom(t *testing.T) {
	envHome := t.TempDir()
	explicit, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := newAllocWorld(t, "labels", func(p *profile.Profile) { p.CodexHome = explicit })
	t.Setenv("CODEX_HOME", envHome)
	spec := profileJobSpec(w.entry, daemonOverrides{})
	want := map[string]string{
		w.line("claude", w.claudeDir):   "home",
		w.line("codex", w.codexDefault): "default",
		w.line("codex", envHome):        "env",
		w.line("codex", w.codexExtra):   "configured",
		w.line("codex", explicit):       "explicit",
		w.line("gjc", w.gjcDefault):     "default",
		w.line("gjc", w.gjcExtra):       "configured",
		w.line("omo", w.omoDefault):     "default",
		w.line("omo", w.omoExtra):       "configured",
	}
	if !reflect.DeepEqual(spec.SourceLabels, want) {
		t.Fatalf("labels = %v\nwant     %v", spec.SourceLabels, want)
	}
	for line := range spec.SourceLabels {
		if !slices.Contains(spec.Sources, line) {
			t.Fatalf("a label for %s, which is not a source", line)
		}
	}
	// A folder both the default and the explicit home name is the explicit one,
	// since that is the label that hides the path.
	both, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", "")
	w2 := newAllocWorld(t, "labels2", func(p *profile.Profile) { p.CodexHome = both })
	t.Setenv("CODEX_CONFIG_DIR", both)
	spec2 := profileJobSpec(w2.entry, daemonOverrides{})
	if got := spec2.SourceLabels[w2.line("codex", both)]; got != "explicit" {
		t.Fatalf("label = %q, want explicit (labels %v)", got, spec2.SourceLabels)
	}
}
