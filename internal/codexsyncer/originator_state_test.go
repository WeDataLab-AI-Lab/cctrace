package codexsyncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cctrace/internal/gitctx"
	"cctrace/internal/syncer"
)

// A rollout names what launched it once, in the session_meta that opens the
// file, and every record's entrypoint is derived from that. A pass that
// resumes past the session_meta never reads it again, so the originator has
// to travel in the sync state with the offset. These tests split a session
// across passes and across a daemon restart and compare the entrypoints.

// originatorHead opens a session in /work/a on gpt-5. An empty originator
// leaves the field out, the way rollouts older than it do.
func originatorHead(originator string) []string {
	payload := `"cwd":"/work/a","model_provider":"openai"`
	if originator != "" {
		payload += `,"originator":"` + originator + `"`
	}
	return []string{
		`{"type":"session_meta","timestamp":"2026-08-24T09:00:00.000Z","payload":{` + payload + `}}`,
		scanEndTurnContext("2026-08-24T09:00:01.000Z", "/work/a", "gpt-5"),
	}
}

// originatorFixture is one rollout in a home of its own, synced through a
// state file a test can load again the way a restarted daemon does.
type originatorFixture struct {
	srvURL, home, statePath, path string
}

// newOriginatorFixture writes lines as that rollout.
//
// It replaces the package-level resolveGit, so the tests built on it are not
// parallel.
func newOriginatorFixture(t *testing.T, srvURL string, lines []string) *originatorFixture {
	t.Helper()
	prevResolve := resolveGit
	// No repository root: sendProjectRules returns before any rule scan.
	resolveGit = func(string) gitctx.Context { return gitctx.Context{RepositoryID: scanEndRepoA} }
	t.Cleanup(func() { resolveGit = prevResolve })

	home := t.TempDir()
	return &originatorFixture{
		srvURL:    srvURL,
		home:      home,
		statePath: filepath.Join(t.TempDir(), "state.json"),
		path:      writeRollout(t, home, "rollout-2026-08-24T09-00-00-019db82c-66c5-7160-a6a7-b76dc2dd72c5.jsonl", lines),
	}
}

// start loads the state file and builds a syncer on it, as a daemon start
// does. Nothing of an earlier syncer survives it but what that one saved.
func (f *originatorFixture) start(t *testing.T) (*CodexSyncer, *syncer.State) {
	t.Helper()
	state, err := syncer.LoadState(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	return New([]string{f.home}, testProfileEmail, "uid-001", state, syncer.NewClient(f.srvURL, "", ""), nil), state
}

// startTracked is start for a rollout the state knows at offset 0, so the
// first pass reads it instead of skipping it as history that predates the
// first sync.
func (f *originatorFixture) startTracked(t *testing.T) (*CodexSyncer, *syncer.State) {
	t.Helper()
	cs, state := f.start(t)
	state.SetOffset(f.path, 0)
	return cs, state
}

// assertEntrypoints checks the entrypoint of every record in one captured
// sync request.
func assertEntrypoints(t *testing.T, body map[string]interface{}, want string) {
	t.Helper()
	records, _ := body["records"].([]interface{})
	if len(records) == 0 {
		t.Fatal("request carried no records")
	}
	for i, r := range records {
		rec, _ := r.(map[string]interface{})
		// entrypoint is omitted from the payload when empty.
		if got, _ := rec["entrypoint"].(string); got != want {
			t.Errorf("record %d entrypoint = %q, want %q", i, got, want)
		}
	}
}

func TestSyncFile_laterPassesKeepTheEntrypointOfTheSessionMeta(t *testing.T) {
	for _, tc := range []struct {
		name, originator, want string
	}{
		{"headless exec", "codex_exec", "codex_exec"},
		{"sdk", "codex_sdk_ts", "codex_sdk_ts"},
		{"interactive tui", "codex-tui", "cli"},
		// A rollout older than the field has nothing to carry and stays on
		// the empty entrypoint.
		{"no originator", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, captured := newTestServer(t)
			f := newOriginatorFixture(t, srv.URL, append(originatorHead(tc.originator),
				scanEndUserTurn("2026-08-24T09:00:02.000Z"),
			))
			cs, state := f.startTracked(t)
			// sent runs a pass that sends one record and checks the request.
			sent := func(cs *CodexSyncer, requests int) {
				t.Helper()
				scanEndPass(t, cs, 1)
				if len(*captured) != requests {
					t.Fatalf("sync requests = %d, want %d", len(*captured), requests)
				}
				assertEntrypoints(t, (*captured)[requests-1], tc.want)
			}

			// The pass that consumes the session_meta.
			sent(cs, 1)

			appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:03.000Z"))
			sent(cs, 2)

			// A tail with no session_meta and no record: the scan has nothing
			// new to say about the originator, and what is saved must survive it.
			appendScanEndLines(t, f.path, scanEndTurnContext("2026-08-24T09:00:04.000Z", "/work/a", "gpt-5"))
			scanEndPass(t, cs, 0)
			if got := state.Files[f.path].CodexOriginator; got != tc.originator {
				t.Errorf("originator saved after a recordless tail = %q, want %q", got, tc.originator)
			}
			appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:05.000Z"))
			sent(cs, 3)

			restarted, _ := f.start(t)
			appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:06.000Z"))
			sent(restarted, 4)
		})
	}
}

// A rollout already on disk at the first sync is skipped to its end, and the
// metadata of the skipped bytes is all a later pass has of them.
func TestSyncFile_rolloutSkippedAtFirstSyncKeepsItsEntrypoint(t *testing.T) {
	srv, captured := newTestServer(t)
	f := newOriginatorFixture(t, srv.URL, append(originatorHead("codex_exec"),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	cs, state := f.start(t)

	scanEndPass(t, cs, 0)
	assertConsumed(t, state, f.path)
	if len(*captured) != 0 {
		t.Fatalf("sync requests = %d, want 0: the rollout predates the first sync", len(*captured))
	}
	if got := state.Files[f.path].CodexOriginator; got != "codex_exec" {
		t.Errorf("originator saved with the skip = %q, want codex_exec", got)
	}

	appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:03.000Z"))
	scanEndPass(t, cs, 1)
	if len(*captured) != 1 {
		t.Fatalf("sync requests = %d, want 1", len(*captured))
	}
	assertEntrypoints(t, (*captured)[0], "codex_exec")
}

// A failed quota send holds the offset in front of the session_meta. The
// records still go out under its originator, but nothing of the file is
// consumed, so nothing is saved for it until the offset moves.
func TestSyncFile_heldOffsetDoesNotSaveAnOriginatorItHasNotPassed(t *testing.T) {
	srv, captured, quotaUp := scanEndQuotaServer(t)
	reads := countHeadReads(t)
	f := newOriginatorFixture(t, srv.URL, append(originatorHead("codex_exec"),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
		scanEndRateLimit("2026-08-24T09:00:03.000Z", "null"),
	))
	writeCodexAuth(t, f.home, "acct-quota")
	cs, state := f.startTracked(t)

	for range 2 {
		scanEndPass(t, cs, 1)
		assertScanEndSaved(t, state, f.path, 0, "", "")
		if fs := state.Files[f.path]; fs.CodexOriginator != "" || fs.CodexOriginatorScanned {
			t.Errorf("saved at offset 0: originator=%q scanned=%v, want neither: nothing of the file is consumed", fs.CodexOriginator, fs.CodexOriginatorScanned)
		}
	}
	quotaUp.Store(true)
	scanEndPass(t, cs, 1)
	assertScanEndSaved(t, state, f.path, scanEndFileSize(t, f.path), "/work/a", "gpt-5")
	if got := state.Files[f.path].CodexOriginator; got != "codex_exec" {
		t.Errorf("originator saved at the end of the file = %q, want codex_exec", got)
	}

	if len(*captured) != 3 {
		t.Fatalf("sync requests = %d, want 3", len(*captured))
	}
	for _, body := range *captured {
		assertEntrypoints(t, body, "codex_exec")
	}
	// The scan that starts at the session_meta is its own source.
	assertHeadReads(t, reads, 0)
}

// An entry saved before the originator was kept has an offset past the
// session_meta and no originator. Every tracked rollout is in that condition
// after an upgrade, and most of them never grow again, so the head is read
// for it only by a pass that has records to stamp, and once. The tests below
// leave a rollout in that condition and count the reads.

// countHeadReads counts the calls to headOriginator.
//
// It replaces the package-level headOriginator, so the tests built on it are
// not parallel.
func countHeadReads(t *testing.T) *int {
	t.Helper()
	prev := headOriginator
	reads := 0
	headOriginator = func(ctx context.Context, path string) (string, error) {
		reads++
		return prev(ctx, path)
	}
	t.Cleanup(func() { headOriginator = prev })
	return &reads
}

// startWithoutOriginator syncs the rollout's one record, strips what that
// pass saved of the originator and starts again from the state file, which
// then reads as one written before the fields existed.
func (f *originatorFixture) startWithoutOriginator(t *testing.T) (*CodexSyncer, *syncer.State) {
	t.Helper()
	cs, state := f.startTracked(t)
	scanEndPass(t, cs, 1)
	fs := state.Files[f.path]
	fs.CodexOriginator, fs.CodexOriginatorScanned = "", false
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	cs, state = f.start(t)
	// Everything the prefix rescan looks for is there, so that rescan is not
	// what brings the originator back.
	fs = state.Files[f.path]
	if fs == nil || fs.Offset == 0 || fs.CWD == "" || fs.Model == "" || !fs.TokenUsageScanned || !fs.CodexForkMetadataScanned {
		t.Fatalf("fixture is not an entry that lacks only its originator: %+v", fs)
	}
	return cs, state
}

func lastRequest(t *testing.T, captured *[]map[string]interface{}) map[string]interface{} {
	t.Helper()
	if len(*captured) == 0 {
		t.Fatal("no sync request was sent")
	}
	return (*captured)[len(*captured)-1]
}

func assertHeadReads(t *testing.T, reads *int, want int) {
	t.Helper()
	if *reads != want {
		t.Fatalf("head reads = %d, want %d", *reads, want)
	}
}

func TestSyncFile_entryWithoutOriginatorReadsTheHeadOnceRecordsArrive(t *testing.T) {
	srv, captured := newTestServer(t)
	reads := countHeadReads(t)
	f := newOriginatorFixture(t, srv.URL, append(originatorHead("codex_exec"),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	cs, state := f.startWithoutOriginator(t)

	// Nothing new, then new bytes that hold no record: neither has an
	// entrypoint to decide.
	scanEndPass(t, cs, 0)
	appendScanEndLines(t, f.path, scanEndTurnContext("2026-08-24T09:00:03.000Z", "/work/a", "gpt-5"))
	scanEndPass(t, cs, 0)
	assertHeadReads(t, reads, 0)

	appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
	scanEndPass(t, cs, 1)
	assertEntrypoints(t, lastRequest(t, captured), "codex_exec")
	if fs := state.Files[f.path]; fs.CodexOriginator != "codex_exec" || !fs.CodexOriginatorScanned {
		t.Errorf("saved after the head was read: originator=%q scanned=%v, want codex_exec and true", fs.CodexOriginator, fs.CodexOriginatorScanned)
	}

	appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:05.000Z"))
	scanEndPass(t, cs, 1)
	assertEntrypoints(t, lastRequest(t, captured), "codex_exec")

	restarted, _ := f.start(t)
	appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:06.000Z"))
	scanEndPass(t, restarted, 1)
	assertEntrypoints(t, lastRequest(t, captured), "codex_exec")
	assertHeadReads(t, reads, 1)
}

func originatorOldFormatUserTurn(ts string) string {
	return `{"type":"message","timestamp":"` + ts + `","role":"user","content":[{"type":"input_text","text":"hello"}]}`
}

// A rollout with no originator leaves the same entry behind as one saved
// before the field existed. Having looked once is what tells them apart.
func TestSyncFile_rolloutWithoutOriginatorIsAskedOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		head []string
		turn func(ts string) string
	}{
		{"session_meta without one", append(originatorHead(""), scanEndUserTurn("2026-08-24T09:00:02.000Z")), scanEndUserTurn},
		{"old format", []string{originatorOldFormatUserTurn("2026-08-24T09:00:02.000Z")}, originatorOldFormatUserTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, captured := newTestServer(t)
			reads := countHeadReads(t)
			f := newOriginatorFixture(t, srv.URL, tc.head)
			cs, state := f.startTracked(t)

			// Read from its first byte: the scan itself saw whatever the
			// head has to say.
			scanEndPass(t, cs, 1)
			assertEntrypoints(t, lastRequest(t, captured), "")
			assertHeadReads(t, reads, 0)

			appendScanEndLines(t, f.path, tc.turn("2026-08-24T09:00:03.000Z"))
			scanEndPass(t, cs, 1)
			assertEntrypoints(t, lastRequest(t, captured), "")
			assertHeadReads(t, reads, 1)
			if fs := state.Files[f.path]; fs.CodexOriginator != "" || !fs.CodexOriginatorScanned {
				t.Errorf("saved after the head was read: originator=%q scanned=%v, want none and true", fs.CodexOriginator, fs.CodexOriginatorScanned)
			}

			appendScanEndLines(t, f.path, tc.turn("2026-08-24T09:00:04.000Z"))
			scanEndPass(t, cs, 1)
			restarted, _ := f.start(t)
			appendScanEndLines(t, f.path, tc.turn("2026-08-24T09:00:05.000Z"))
			scanEndPass(t, restarted, 1)
			assertEntrypoints(t, lastRequest(t, captured), "")
			assertHeadReads(t, reads, 1)
		})
	}
}

// A rollout synced from its first byte has its originator from the scan, and
// the entry does not say whether a scan from the start or a later tail put it
// there. So its head is read once as well, changes nothing, and is not read
// again.
func TestSyncFile_rolloutSyncedFromItsStartIsReadOnceForItsOriginator(t *testing.T) {
	srv, captured := newTestServer(t)
	reads := countHeadReads(t)
	f := newOriginatorFixture(t, srv.URL, append(originatorHead("codex_exec"),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	cs, state := f.startTracked(t)

	scanEndPass(t, cs, 1)
	assertHeadReads(t, reads, 0)
	for i, ts := range []string{"2026-08-24T09:00:03.000Z", "2026-08-24T09:00:04.000Z"} {
		appendScanEndLines(t, f.path, scanEndUserTurn(ts))
		scanEndPass(t, cs, 1)
		assertEntrypoints(t, lastRequest(t, captured), "codex_exec")
		if fs := state.Files[f.path]; fs.CodexOriginator != "codex_exec" || !fs.CodexOriginatorScanned {
			t.Errorf("saved after later pass %d: originator=%q scanned=%v, want codex_exec and true", i+1, fs.CodexOriginator, fs.CodexOriginatorScanned)
		}
		assertHeadReads(t, reads, 1)
	}
}

// The head lies in front of any offset greater than zero, so what is read
// from it belongs to the consumed prefix and is saved even while a failed
// quota send holds the offset. The retry then has no reason to read it again,
// and sends under what the held pass saved -- so the answer has to be settled
// in what is saved, not only in what the held pass itself sent. That holds
// for each state the entry can be in when the head is read: without an
// originator, with one a later session_meta put there that the head
// overrules, and with one the head has nothing to say against.
func TestSyncFile_heldOffsetSavesTheOriginatorReadFromItsPrefix(t *testing.T) {
	for _, tc := range []struct {
		name string
		// head is the originator the rollout opens with. tail is that of a
		// session_meta a recordless pass saves before the offset is held;
		// either may be empty for none.
		head, tail string
	}{
		{"entry without an originator", "codex_exec", ""},
		{"entry holding a later session_meta's against the head's", "codex_exec", "codex-tui"},
		{"entry holding a later session_meta's under a head without one", "", "codex_exec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, captured, quotaUp := scanEndQuotaServer(t)
			reads := countHeadReads(t)
			f := newOriginatorFixture(t, srv.URL, append(originatorHead(tc.head),
				scanEndUserTurn("2026-08-24T09:00:02.000Z"),
			))
			writeCodexAuth(t, f.home, "acct-quota")
			cs, state := f.startWithoutOriginator(t)
			if tc.tail != "" {
				appendScanEndLines(t, f.path, `{"type":"session_meta","timestamp":"2026-08-24T09:00:03.000Z","payload":{"cwd":"/work/a","model_provider":"openai","originator":"`+tc.tail+`"}}`)
				scanEndPass(t, cs, 0)
				if fs := state.Files[f.path]; fs.CodexOriginator != tc.tail || fs.CodexOriginatorScanned {
					t.Fatalf("fixture is not an entry holding the tail's originator with its head unread: %+v", fs)
				}
			}
			settled := state.GetOffset(f.path)

			appendScanEndLines(t, f.path,
				scanEndUserTurn("2026-08-24T09:00:04.000Z"),
				scanEndRateLimit("2026-08-24T09:00:05.000Z", "null"),
			)
			// Twice: the pass that reads the head and holds the offset, then
			// the retry that has only what that pass saved.
			for range 2 {
				scanEndPass(t, cs, 1)
				assertScanEndSaved(t, state, f.path, settled, "/work/a", "gpt-5")
				assertEntrypoints(t, lastRequest(t, captured), "codex_exec")
				if fs := state.Files[f.path]; fs.CodexOriginator != "codex_exec" || !fs.CodexOriginatorScanned {
					t.Errorf("saved with the held offset: originator=%q scanned=%v, want codex_exec and true", fs.CodexOriginator, fs.CodexOriginatorScanned)
				}
			}
			quotaUp.Store(true)
			scanEndPass(t, cs, 1)
			assertScanEndSaved(t, state, f.path, scanEndFileSize(t, f.path), "/work/a", "gpt-5")
			assertEntrypoints(t, lastRequest(t, captured), "codex_exec")
			assertHeadReads(t, reads, 1)
		})
	}
}

// The first originator in a rollout is the session's own: a scan from the
// start keeps it against any session_meta that follows. An entry saved
// without one has to come to the same answer wherever the pass boundary
// falls, whether the later session_meta arrives with the record or in a tail
// of its own that is saved before the head has been read.
func TestSyncFile_headOriginatorWinsOverOneMetInTheTail(t *testing.T) {
	const tailMeta = `{"type":"session_meta","timestamp":"2026-08-24T09:00:03.000Z","payload":{"cwd":"/work/a","model_provider":"openai","originator":"codex-tui"}}`
	for _, tc := range []struct {
		name  string
		split bool
	}{
		{"in the pass that sends the record", false},
		{"in a recordless pass before it", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, captured := newTestServer(t)
			reads := countHeadReads(t)
			f := newOriginatorFixture(t, srv.URL, append(originatorHead("codex_exec"),
				scanEndUserTurn("2026-08-24T09:00:02.000Z"),
			))
			cs, state := f.startWithoutOriginator(t)

			appendScanEndLines(t, f.path, tailMeta)
			if tc.split {
				scanEndPass(t, cs, 0)
				assertHeadReads(t, reads, 0)
			}
			appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
			scanEndPass(t, cs, 1)
			assertEntrypoints(t, lastRequest(t, captured), "codex_exec")
			if got := state.Files[f.path].CodexOriginator; got != "codex_exec" {
				t.Errorf("originator saved = %q, want codex_exec, the one the rollout opens with", got)
			}
			assertHeadReads(t, reads, 1)
		})
	}
}

// A head that names no originator has nothing to overrule: the first one in
// the rollout is then the one a later session_meta brought, and reading the
// head must not erase it.
func TestSyncFile_headWithoutOriginatorLeavesTheOneMetInTheTail(t *testing.T) {
	srv, captured := newTestServer(t)
	reads := countHeadReads(t)
	f := newOriginatorFixture(t, srv.URL, append(originatorHead(""),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	cs, state := f.startTracked(t)
	scanEndPass(t, cs, 1)

	appendScanEndLines(t, f.path, `{"type":"session_meta","timestamp":"2026-08-24T09:00:03.000Z","payload":{"cwd":"/work/a","model_provider":"openai","originator":"codex_exec"}}`)
	scanEndPass(t, cs, 0)
	for _, ts := range []string{"2026-08-24T09:00:04.000Z", "2026-08-24T09:00:05.000Z"} {
		appendScanEndLines(t, f.path, scanEndUserTurn(ts))
		scanEndPass(t, cs, 1)
		assertEntrypoints(t, lastRequest(t, captured), "codex_exec")
		if fs := state.Files[f.path]; fs.CodexOriginator != "codex_exec" || !fs.CodexOriginatorScanned {
			t.Errorf("saved after the head was read: originator=%q scanned=%v, want codex_exec and true", fs.CodexOriginator, fs.CodexOriginatorScanned)
		}
		assertHeadReads(t, reads, 1)
	}
}

// A head that could not be read is not a head without an originator: the
// entry stays unasked, and the next pass with records asks again.
func TestSyncFile_failedHeadReadIsAskedAgain(t *testing.T) {
	srv, captured := newTestServer(t)
	f := newOriginatorFixture(t, srv.URL, append(originatorHead("codex_exec"),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	cs, state := f.startWithoutOriginator(t)
	prev := headOriginator
	failing := true
	headOriginator = func(ctx context.Context, path string) (string, error) {
		if failing {
			return "", errors.New("read failed")
		}
		return prev(ctx, path)
	}
	t.Cleanup(func() { headOriginator = prev })

	appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:03.000Z"))
	scanEndPass(t, cs, 1)
	assertEntrypoints(t, lastRequest(t, captured), "")
	if state.Files[f.path].CodexOriginatorScanned {
		t.Error("a failed head read was saved as an answer")
	}

	failing = false
	appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
	scanEndPass(t, cs, 1)
	assertEntrypoints(t, lastRequest(t, captured), "codex_exec")
}

// A rollout that shrank is not the file the entry describes any more. The
// offset is reset to its new size, and an originator saved for the old
// content -- or the note that the head was read and had none -- would outlive
// the content it came from.

func TestSyncFile_rolloutCutToNothingAndRewrittenTakesItsNewOriginator(t *testing.T) {
	srv, captured := newTestServer(t)
	f := newOriginatorFixture(t, srv.URL, append(originatorHead("codex_exec"),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	cs, _ := f.startTracked(t)
	scanEndPass(t, cs, 1)
	assertEntrypoints(t, lastRequest(t, captured), "codex_exec")

	if err := os.Truncate(f.path, 0); err != nil {
		t.Fatal(err)
	}
	scanEndPass(t, cs, 0)
	appendScanEndLines(t, f.path, append(originatorHead("codex-tui"),
		scanEndUserTurn("2026-08-24T09:00:03.000Z"),
	)...)
	scanEndPass(t, cs, 1)
	assertEntrypoints(t, lastRequest(t, captured), "cli")
}

func TestSyncFile_rolloutReplacedAfterItsHeadWasReadIsReadAgain(t *testing.T) {
	srv, captured := newTestServer(t)
	reads := countHeadReads(t)
	f := newOriginatorFixture(t, srv.URL, append(originatorHead(""),
		scanEndUserTurn("2026-08-24T09:00:02.000Z"),
	))
	cs, state := f.startTracked(t)
	scanEndPass(t, cs, 1)
	appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:03.000Z"))
	scanEndPass(t, cs, 1)
	if fs := state.Files[f.path]; fs.CodexOriginator != "" || !fs.CodexOriginatorScanned {
		t.Fatalf("fixture is not an entry whose head was read and had no originator: %+v", fs)
	}

	// Shorter than what was consumed, and opening with an originator.
	replaced := strings.Join(originatorHead("codex_exec"), "\n") + "\n"
	if int64(len(replaced)) >= state.GetOffset(f.path) {
		t.Fatalf("fixture does not shrink the rollout: %d bytes at offset %d", len(replaced), state.GetOffset(f.path))
	}
	if err := os.WriteFile(f.path, []byte(replaced), 0o644); err != nil {
		t.Fatal(err)
	}
	scanEndPass(t, cs, 0)
	appendScanEndLines(t, f.path, scanEndUserTurn("2026-08-24T09:00:04.000Z"))
	scanEndPass(t, cs, 1)
	assertEntrypoints(t, lastRequest(t, captured), "codex_exec")
	assertHeadReads(t, reads, 2)
}
