package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

func TestRouteNoticeText(t *testing.T) {
	const indent = "                   "
	const partialTail = indent + "나머지 경로는 정상 수집합니다. 맡길 프로필을 바꾸려면: cctrace profile claim <프로필> --source <id>"
	const allTail = indent + "cctrace profile claim <프로필> --all 로 이 프로필이 맡거나, 겹친 쪽 설정을 바꾸세요"
	const afterBlock = indent + "겹침으로 막혀 있던 기간의 세션은 수집되지 않았습니다 (수집을 시작한 시점부터 수집)"
	src := func(kind, label, id, holder, name, state string) localdaemon.SharedSource {
		return localdaemon.SharedSource{Source: id, Kind: kind, Label: label, Reason: localdaemon.ShareSame, Holder: holder, HolderName: name, HolderState: state}
	}
	cases := []struct {
		name       string
		shared     []localdaemon.SharedSource
		all, after bool
		want       string
	}{
		{
			name: "some sources are left out; the columns line up by what the eye sees",
			shared: []localdaemon.SharedSource{
				src("codex", "default", "3fa91c2d", "default", "default", "active"),
				src("codex", "explicit", "9b01d7e4", "named:work", "work", "stopped"),
			},
			want: "[!] 이 프로필에서 수집하지 않는 경로 2개 -- 같은 서버·사용자로 수집하는 다른 프로필이 맡고 있습니다\n" +
				indent + "codex  기본 폴더        3fa91c2d  맡은 프로필: default (수집 중)\n" +
				indent + "codex  [명시 Codex 홈]  9b01d7e4  맡은 프로필: work (중지됨)\n" +
				partialTail,
		},
		{
			name:   "every source is left out",
			shared: []localdaemon.SharedSource{src("claude", "home", "77aa10c2", "default", "default", "active")},
			all:    true,
			want: "[!] 이 프로필의 모든 경로가 다른 프로필과 겹쳐 수집이 멈춰 있습니다 (duplicate_route)\n" +
				indent + "claude  홈 폴더  77aa10c2  맡은 프로필: default (수집 중)\n" + allTail,
		},
		{
			name: "a holder that is gone, nobody's, or not known is named for what it is, and the other states have words",
			shared: []localdaemon.SharedSource{
				src("codex", "configured", "00000001", "named:gone", "", "removed"),
				src("gjc", "default", "00000002", "", "", "unowned"),
				src("omo", "env", "00000003", "", "", "unknown"),
				src("codex", "configured", "00000004", "named:a", "a", "disabled"),
				src("codex", "configured", "00000005", "named:b", "b", "blocked"),
				src("codex", "configured", "00000006", "named:c", "c", "transferring"),
			},
			want: fmtPartial(6) +
				indent + "codex  설정 폴더   00000001  맡은 프로필: (삭제됨)\n" +
				indent + "gjc    기본 폴더   00000002  맡은 프로필: (주인 없음)\n" +
				indent + "omo    CODEX_HOME  00000003  맡은 프로필: (알 수 없음)\n" +
				indent + "codex  설정 폴더   00000004  맡은 프로필: a (꺼짐)\n" +
				indent + "codex  설정 폴더   00000005  맡은 프로필: b (막힘)\n" +
				indent + "codex  설정 폴더   00000006  맡은 프로필: c (이전 중)\n" +
				partialTail,
		},
		{
			name: "a nested folder says some files may go uncollected",
			shared: []localdaemon.SharedSource{
				{Source: "00000007", Kind: "codex", Label: "configured", Reason: localdaemon.ShareNested, Holder: "default", HolderName: "default", HolderState: "active"},
			},
			want: fmtPartial(1) +
				indent + "codex  설정 폴더  00000007  맡은 프로필: default (수집 중) -- 일부 파일은 어느 프로필에도 수집되지 않을 수 있음\n" +
				partialTail,
		},
		{
			name:   "without route ids the folder names the source",
			shared: []localdaemon.SharedSource{{Kind: "codex", Label: "configured", Reason: localdaemon.ShareSame, HolderState: "unknown"}},
			want: fmtPartial(1) +
				indent + "codex  설정 폴더  -  맡은 프로필: (알 수 없음)\n" +
				indent + "나머지 경로는 정상 수집합니다. 예약 파일을 읽지 못해 id가 없으므로 폴더로 지정합니다: cctrace profile claim <프로필> --source <폴더>",
		},
		{
			name:   "sessions of the time the profile was blocked are said to be missing",
			shared: []localdaemon.SharedSource{src("codex", "default", "3fa91c2d", "default", "default", "active")},
			after:  true,
			want:   fmtPartial(1) + indent + "codex  기본 폴더  3fa91c2d  맡은 프로필: default (수집 중)\n" + partialTail + "\n" + afterBlock,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routeNotice(tc.shared, tc.all, tc.after); got != tc.want {
				t.Fatalf("notice:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
	if got := routeNotice(nil, false, true); got != "" {
		t.Fatalf("a profile left out of nothing is told about it: %q", got)
	}
}

func fmtPartial(n int) string {
	return strings.Replace("[!] 이 프로필에서 수집하지 않는 경로 N개 -- 같은 서버·사용자로 수집하는 다른 프로필이 맡고 있습니다\n", "N", string(rune('0'+n)), 1)
}

// useDaemonSnapshot pins what `cctrace status` believes the running daemon last
// published, so the notice can be read without starting one.
func useDaemonSnapshot(t *testing.T, jobs ...localdaemon.JobStatus) {
	t.Helper()
	previous := readDaemonSnapshotFn
	readDaemonSnapshotFn = func() (localdaemon.Snapshot, bool) {
		return localdaemon.Snapshot{ObservedAt: time.Now(), Complete: true, Jobs: jobs}, true
	}
	t.Cleanup(func() { readDaemonSnapshotFn = previous })
}

// twoProfilesSharingACodexFolder saves a default and a "work" profile that send
// to the same destination and both collect Codex from the default folder.
func twoProfilesSharingACodexFolder(t *testing.T, customize func(def, work *profile.Profile)) {
	t.Helper()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CODEX_CONFIG_DIR", "")
	reachableOTELEndpoint(t)
	def, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	def.Options.SyncEnabled = false
	def.Options.CodexSyncEnabled = true
	work := profile.NewDefault()
	work.User, work.Server, work.Options = def.User, def.Server, def.Options
	customize(def, work)
	if err := profile.Save(def); err != nil {
		t.Fatal(err)
	}
	if err := profile.SaveNamed(work, "work"); err != nil {
		t.Fatal(err)
	}
}

func TestStatusSaysWhichSourcesTheDaemonLeavesOut(t *testing.T) {
	// Not parallel: t.Setenv(HOME) and readDaemonSnapshotFn are process-wide.
	home := setupTestHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	twoProfilesSharingACodexFolder(t, func(def, work *profile.Profile) {})
	useDaemonSnapshot(t, localdaemon.JobStatus{ID: "named:work", Name: "work", Enabled: true, State: "healthy", SharedSources: []localdaemon.SharedSource{
		{Source: "3fa91c2d", Kind: "codex", Label: "default", Reason: localdaemon.ShareSame, Holder: "default", HolderName: "default", HolderState: "stopped"},
	}})

	out := captureStatus(t, "work")
	for _, want := range []string{
		"    수집 경로:     [!] 이 프로필에서 수집하지 않는 경로 1개 -- 같은 서버·사용자로 수집하는 다른 프로필이 맡고 있습니다\n",
		"                   codex  기본 폴더  3fa91c2d  맡은 프로필: default (중지됨)\n",
		"                   나머지 경로는 정상 수집합니다. 맡길 프로필을 바꾸려면: cctrace profile claim <프로필> --source <id>\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, filepath.Join(home, ".codex")) {
		t.Fatalf("the default folder's path is printed:\n%s", out)
	}
	// A profile the daemon leaves nothing out of says nothing about routes.
	if got := captureStatus(t, ""); strings.Contains(got, "수집 경로:") {
		t.Fatalf("the holder is told it is left out:\n%s", got)
	}
}

// With no daemon the answer is the plan the daemon would make, and an explicit
// Codex home is named as such, never by its path.
func TestStatusWithoutADaemonReadsTheOfflinePlanAndHidesAnExplicitCodexHome(t *testing.T) {
	// Not parallel: t.Setenv(HOME) and readDaemonSnapshotFn are process-wide.
	setupTestHome(t)
	dir := filepath.Join(t.TempDir(), "xhome-secret")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	explicit, err := profile.NormalizeCodexHome(dir)
	if err != nil {
		t.Fatal(err)
	}
	twoProfilesSharingACodexFolder(t, func(def, work *profile.Profile) {
		def.Options.CodexDirs = []string{explicit}
		work.CodexHome = explicit
	})

	out := captureStatus(t, "work")
	if !strings.Contains(out, "이 프로필의 모든 경로가 다른 프로필과 겹쳐 수집이 멈춰 있습니다 (duplicate_route)") ||
		!strings.Contains(out, "codex  [명시 Codex 홈]  ") || !strings.Contains(out, "맡은 프로필: default (수집 중)") {
		t.Fatalf("status:\n%s", out)
	}
	if strings.Contains(out, "xhome-secret") {
		t.Fatalf("an explicit Codex home path reached the screen:\n%s", out)
	}
}

func TestStatusStaysQuietWhenNothingIsLeftOut(t *testing.T) {
	// Not parallel: t.Setenv(HOME) and readDaemonSnapshotFn are process-wide.
	home := setupTestHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	twoProfilesSharingACodexFolder(t, func(def, work *profile.Profile) { work.Options.CodexSyncEnabled = false })
	if out := captureStatus(t, "work"); strings.Contains(out, "수집 경로:") {
		t.Fatalf("a profile that shares nothing was told it was left out:\n%s", out)
	}
}

func TestStatusDoesNotGuessForADaemonThatHasPublishedNothingCurrent(t *testing.T) {
	// Not parallel: t.Setenv(HOME) and readDaemonSnapshotFn are process-wide.
	home := setupTestHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	twoProfilesSharingACodexFolder(t, func(def, work *profile.Profile) {})
	// A daemon runs, but what it last published is not current: guessing from the
	// registry would answer for a daemon that decides for itself.
	root := profile.DefaultDir()
	lock := flock.New(filepath.Join(root, "daemon.lock"))
	if ok, err := lock.TryLock(); err != nil || !ok {
		t.Fatalf("fixture lock: %v %v", ok, err)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	previous := readDaemonSnapshotFn
	t.Cleanup(func() { readDaemonSnapshotFn = previous })
	readDaemonSnapshotFn = func() (localdaemon.Snapshot, bool) { return localdaemon.Snapshot{}, false }
	if out := captureStatus(t, "work"); strings.Contains(out, "수집 경로:") {
		t.Fatalf("a snapshot that is not the running daemon's current one still produced a notice:\n%s", out)
	}

	// The daemon does not know the profile (it is new), or leaves nothing out.
	useDaemonSnapshot(t, localdaemon.JobStatus{ID: "named:other", Name: "other"})
	if out := captureStatus(t, "work"); strings.Contains(out, "수집 경로:") {
		t.Fatalf("a profile the daemon does not list was reported:\n%s", out)
	}
	useDaemonSnapshot(t, localdaemon.JobStatus{ID: "named:work", Name: "work", Enabled: true, State: "healthy"})
	if out := captureStatus(t, "work"); strings.Contains(out, "수집 경로:") {
		t.Fatalf("a healthy profile was reported:\n%s", out)
	}
}

// The notice cannot live under the sync-endpoint block: a profile with only an
// OTEL endpoint is left out of a shared source just the same.
func TestStatusSaysWhatIsLeftOutOfAProfileWithoutASyncEndpoint(t *testing.T) {
	// Not parallel: t.Setenv(HOME) and readDaemonSnapshotFn are process-wide.
	home := setupTestHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	twoProfilesSharingACodexFolder(t, func(def, work *profile.Profile) {
		def.Server.SyncEndpoint, work.Server.SyncEndpoint = "", ""
	})
	out := captureStatus(t, "work")
	if !strings.Contains(out, "Sync:          (not configured)") || !strings.Contains(out, "수집 경로:     [!] 이 프로필의 모든 경로가") {
		t.Fatalf("status:\n%s", out)
	}
}

// A profile that began only when it was let in has no sessions of the time it
// was held back, and says so while it is still left out of something.
func TestStatusSaysSessionsOfTheBlockedTimeWereNotCollected(t *testing.T) {
	// Not parallel: t.Setenv(HOME) and readDaemonSnapshotFn are process-wide.
	home := setupTestHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	twoProfilesSharingACodexFolder(t, func(def, work *profile.Profile) {})
	paths := syncStatePaths("work")
	if len(paths) == 0 {
		t.Fatal("no state file for the profile")
	}
	state, err := syncer.LoadState(paths[len(paths)-1])
	if err != nil {
		t.Fatal(err)
	}
	state.MarkSource(filepath.Join(home, "somewhere"), syncer.SourceMark{Since: time.Now(), Via: syncer.SourceInitial, AfterBlock: true})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	out := captureStatus(t, "work")
	if !strings.Contains(out, "겹침으로 막혀 있던 기간의 세션은 수집되지 않았습니다 (수집을 시작한 시점부터 수집)") {
		t.Fatalf("status:\n%s", out)
	}
}
