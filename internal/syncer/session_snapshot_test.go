package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// ---- fixture lines, in the shapes measured on real transcripts ----

func aiTitleLine(sid, title string) string {
	return fmt.Sprintf(`{"type":"ai-title","aiTitle":%s,"sessionId":%q}`, strconvQuote(title), sid)
}

func customTitleLine(sid, title string) string {
	return fmt.Sprintf(`{"type":"custom-title","customTitle":%s,"sessionId":%q}`, strconvQuote(title), sid)
}

func agentNameLine(sid, name string) string {
	return fmt.Sprintf(`{"type":"agent-name","agentName":%s,"sessionId":%q}`, strconvQuote(name), sid)
}

func costStateLine(sid string, start int64, cost float64, added, removed int, unknown bool, models ...string) string {
	usage := map[string]any{}
	for _, m := range models {
		usage[m] = map[string]any{"inputTokens": 120, "outputTokens": 40, "cacheReadInputTokens": 9000, "costUSD": 0.01}
	}
	b, _ := json.Marshal(map[string]any{
		"type": "cost-state", "sessionId": sid, "startTime": start, "totalCostUSD": cost,
		"totalLinesAdded": added, "totalLinesRemoved": removed, "totalAPIDuration": 1200,
		"totalAPIDurationWithoutRetries": 1100, "totalToolDuration": 300, "totalDuration": 5000,
		"modelUsage": usage, "hasUnknownModelCost": unknown,
	})
	return string(b)
}

func prLinkLine(sid string, number int, url, repo string, at time.Time) string {
	return fmt.Sprintf(`{"type":"pr-link","sessionId":%q,"prNumber":%d,"prUrl":%s,"prRepository":%s,"timestamp":%q}`,
		sid, number, strconvQuote(url), strconvQuote(repo), at.UTC().Format(time.RFC3339))
}

func continuedInLine(sid, next string, at time.Time) string {
	return fmt.Sprintf(`{"type":"continued-in","sessionId":%q,"continuedInSessionId":%q,"timestamp":%q}`, sid, next, at.UTC().Format(time.RFC3339))
}

func convLine(sid, uuid string, at time.Time) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":%q,"sessionId":%q,"message":{"role":"user","content":"hello"}}`,
		uuid, at.UTC().Format(time.RFC3339), sid)
}

// padLine pads a JSON object line to n bytes with a filler member, so a test can
// rewrite a line in place without moving any offset.
func padLine(t *testing.T, line string, n int) string {
	t.Helper()
	const filler = `,"pad":""`
	if len(line)+len(filler) > n {
		t.Fatalf("line of %d bytes cannot be padded to %d", len(line), n)
	}
	return line[:len(line)-1] + `,"pad":"` + strings.Repeat("x", n-len(line)-len(filler)) + `"}`
}

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// writeTranscript writes a main transcript and returns its path and where each
// line starts.
func writeTranscript(t *testing.T, claudeDir, sessionID string, lines ...string) (string, []int64) {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", "-tmp-snapshot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var starts []int64
	var b strings.Builder
	for _, l := range lines {
		starts = append(starts, int64(b.Len()))
		b.WriteString(l + "\n")
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, starts
}

// rewriteAt overwrites bytes of a file in place, offsets unchanged.
func rewriteAt(t *testing.T, path string, at int64, b string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteAt([]byte(b), at); err != nil {
		t.Fatal(err)
	}
}

// newSnapshotSyncer returns a Syncer over claudeDir whose state is already saved
// once, so files it has not seen are read from the start rather than skipped.
func newSnapshotSyncer(t *testing.T, claudeDir, endpoint string) (*Syncer, *State) {
	t.Helper()
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	return New(claudeDir, "p@example.com", "u1", state, NewClient(endpoint, "", ""), nil), state
}

func mustSync(t *testing.T, s *Syncer) {
	t.Helper()
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if stats := s.LastPass(); stats.FilesFailed > 0 {
		t.Fatalf("SyncOnce failed %d file(s): %v", stats.FilesFailed, stats.LastErr)
	}
}

func snapshotState(t *testing.T, state *State, path string) *SessionSnapshotState {
	t.Helper()
	fs := state.Files[path]
	if fs == nil || fs.SessionSnapshot == nil {
		t.Fatalf("%s has no session snapshot", filepath.Base(path))
	}
	return fs.SessionSnapshot
}

func encodeSnapshot(t *testing.T, s SessionSnapshot) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---- extraction ----

// Every metadata type, as Claude Code writes it, ends up in the typed snapshot:
// the last title wins, cost-state keeps the last line per startTime, a PR link
// and a continuation are kept once, as first seen.
func TestSessionSnapshotFromMetadataLines(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-extract"
	path, _ := writeTranscript(t, claudeDir, sid,
		convLine(sid, "u-1", t0),
		aiTitleLine(sid, "First title"),
		aiTitleLine(sid, "Second title"),
		customTitleLine(sid, "Mine"),
		agentNameLine(sid, "agent-a"),
		costStateLine(sid, 1759300000000, 0.5, 3, 1, false, "claude-opus-5-5[1m]", "claude-haiku-4-5"),
		costStateLine(sid, 1759400000000, 0.1, 1, 0, false, "claude-opus-5-5[1m]"),
		costStateLine(sid, 1759300000000, 1.25, 10, 4, false, "claude-opus-5-5[1m]", "claude-haiku-4-5"),
		costStateLine(sid, 1759400000000, 0.2, 5, 0, true, "claude-opus-5-5[1m]"),
		prLinkLine(sid, 7, "https://github.com/o/r/pull/7", "o/r", t0.Add(time.Minute)),
		prLinkLine(sid, 7, "https://github.com/o/r/pull/7", "o/r", t0.Add(time.Hour)),
		continuedInLine(sid, "next-1", t0.Add(2*time.Minute)),
		continuedInLine(sid, "next-1", t0.Add(3*time.Minute)),
	)
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)

	st := snapshotState(t, state, path)
	want := `{"schema":1,"ai_title":"Second title","custom_title":"Mine","agent_name":"agent-a",` +
		`"cost_states":[` +
		`{"start_time_ms":1759300000000,"total_cost_usd":1.25,"lines_added":10,"lines_removed":4,"models":["claude-haiku-4-5","claude-opus-5-5[1m]"],"has_unknown_model_cost":false},` +
		`{"start_time_ms":1759400000000,"total_cost_usd":0.2,"lines_added":5,"lines_removed":0,"models":["claude-opus-5-5[1m]"],"has_unknown_model_cost":true}],` +
		`"pr_links":[{"url":"https://github.com/o/r/pull/7","number":7,"repository":"o/r","first_seen_at":"2026-10-01T09:01:00Z"}],` +
		`"continued_in":["next-1"]}`
	if got := encodeSnapshot(t, st.Snapshot); got != want {
		t.Errorf("snapshot\n got %s\nwant %s", got, want)
	}
	if st.Revision != 1 || st.SentRevision != 0 {
		t.Errorf("revision %d sent %d, want one change, unsent", st.Revision, st.SentRevision)
	}
	if st.ProjectHash != "-tmp-snapshot" {
		t.Errorf("project_hash = %q, want the records' -tmp-snapshot", st.ProjectHash)
	}
	if !st.NewestRecordAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("newest record at %v, want the newest line's, the repeated PR link's", st.NewestRecordAt)
	}
}

// ai-title lines repeat constantly with the same value. A repeat must not make
// the snapshot dirty; a different value, an empty one included, must.
func TestSessionSnapshotRevisionMovesOnlyOnChange(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-repeat"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", t0), aiTitleLine(sid, "Same"))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)
	if got := snapshotState(t, state, path).Revision; got != 1 {
		t.Fatalf("revision after the first title = %d, want 1", got)
	}

	for i := 0; i < 3; i++ {
		appendLines(t, path, aiTitleLine(sid, "Same"), convLine(sid, fmt.Sprintf("u-r%d", i), t0.Add(time.Duration(i+1)*time.Minute)))
		mustSync(t, s)
	}
	if got := snapshotState(t, state, path).Revision; got != 1 {
		t.Errorf("revision after repeated titles = %d, want 1", got)
	}

	appendLines(t, path, aiTitleLine(sid, ""))
	mustSync(t, s)
	st := snapshotState(t, state, path)
	if st.Revision != 2 || st.Snapshot.AITitle == nil || *st.Snapshot.AITitle != "" {
		t.Errorf("after an empty title: revision %d, snapshot %s; want 2 and the empty title kept", st.Revision, encodeSnapshot(t, st.Snapshot))
	}
}

// The lists stop at their caps and say so: cost_states keeps the newest runs,
// pr_links and continued_in the first ones.
func TestSessionSnapshotCapsAndTruncated(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-caps"
	lines := []string{convLine(sid, "u-1", t0)}
	for i := 1; i <= maxSnapshotCostStates+2; i++ {
		lines = append(lines, costStateLine(sid, int64(i), 0.01, 0, 0, false, "m"))
	}
	for i := 1; i <= maxSnapshotPRLinks+2; i++ {
		lines = append(lines, prLinkLine(sid, i, fmt.Sprintf("https://github.com/o/r/pull/%d", i), "o/r", t0))
	}
	for i := 1; i <= maxSnapshotContinuedIn+2; i++ {
		lines = append(lines, continuedInLine(sid, fmt.Sprintf("next-%d", i), t0))
	}
	path, _ := writeTranscript(t, claudeDir, sid, lines...)
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)

	snap := snapshotState(t, state, path).Snapshot
	if n := len(snap.CostStates); n != maxSnapshotCostStates || snap.CostStates[0].StartTimeMs != 3 || snap.CostStates[n-1].StartTimeMs != maxSnapshotCostStates+2 {
		t.Errorf("cost_states: %d entries from %d, want the newest %d", n, snap.CostStates[0].StartTimeMs, maxSnapshotCostStates)
	}
	if n := len(snap.PRLinks); n != maxSnapshotPRLinks || snap.PRLinks[n-1].Number != maxSnapshotPRLinks {
		t.Errorf("pr_links: %d entries, want the first %d", n, maxSnapshotPRLinks)
	}
	if n := len(snap.ContinuedIn); n != maxSnapshotContinuedIn || snap.ContinuedIn[n-1] != fmt.Sprintf("next-%d", maxSnapshotContinuedIn) {
		t.Errorf("continued_in: %v, want the first %d", snap.ContinuedIn, maxSnapshotContinuedIn)
	}
	if got := strings.Join(snap.Truncated, ","); got != "cost_states,pr_links,continued_in" {
		t.Errorf("truncated = %q, want all three lists named", got)
	}
}

// A title is cut to 1 KiB on a rune boundary, so a 2 KiB title of three-byte
// runes keeps whole runes and stays valid UTF-8.
func TestSessionSnapshotCutsALongTitleOnARuneBoundary(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-long-title"
	long := strings.Repeat("가", 700) // 2,100 bytes
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", t0), aiTitleLine(sid, long), customTitleLine(sid, long), agentNameLine(sid, long))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s)

	snap := snapshotState(t, state, path).Snapshot
	for name, title := range map[string]*string{"ai_title": snap.AITitle, "custom_title": snap.CustomTitle, "agent_name": snap.AgentName} {
		if title == nil || len(*title) != 1023 || !utf8.ValidString(*title) || !strings.HasPrefix(long, *title) {
			t.Errorf("%s cut to %d bytes, valid=%v; want the first 341 runes (1,023 bytes)", name, len(deref(title)), utf8.ValidString(deref(title)))
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// ---- persistence ----

// The snapshot is stored by the Save that stores the offset: a failed send
// moves neither, a successful one moves both, and a new process that loads the
// state carries on from the stored snapshot rather than starting over. A
// subagent's transcript contributes nothing.
func TestSessionSnapshotIsSavedWithTheOffset(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-persist"
	path, starts := writeTranscript(t, claudeDir, sid,
		convLine(sid, "u-1", t0),
		aiTitleLine(sid, "Persisted"),
		prLinkLine(sid, 1, "https://github.com/o/r/pull/1", "o/r", t0),
	)
	subDir := filepath.Join(claudeDir, "projects", "-tmp-snapshot", sid, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subPath := filepath.Join(subDir, "agent-a1.jsonl")
	sub := convLine(sid, "sub-1", t0) + "\n" + aiTitleLine(sid, "SUBAGENT-TITLE") + "\n" +
		prLinkLine(sid, 9, "https://github.com/o/r/pull/9", "o/r", t0) + "\n"
	if err := os.WriteFile(subPath, []byte(sub), 0o600); err != nil {
		t.Fatal(err)
	}
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	state.SetOffset(path, 0)

	srv.setSyncStatus(http.StatusInternalServerError)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if fs := state.Files[path]; fs.Offset != 0 || fs.SessionSnapshot != nil {
		t.Fatalf("a failed send moved offset %d / snapshot %v", fs.Offset, fs.SessionSnapshot != nil)
	}

	srv.setSyncStatus(http.StatusOK)
	mustSync(t, s)
	reloaded, err := LoadState(state.path)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if got := reloaded.GetOffset(path); got != fi.Size() {
		t.Fatalf("stored offset = %d, want %d", got, fi.Size())
	}
	st := snapshotState(t, reloaded, path)
	if deref(st.Snapshot.AITitle) != "Persisted" || len(st.Snapshot.PRLinks) != 1 {
		t.Fatalf("stored snapshot = %s", encodeSnapshot(t, st.Snapshot))
	}
	if fs := reloaded.Files[subPath]; fs == nil || fs.SessionSnapshot != nil {
		t.Errorf("subagent transcript: state %+v, want collected without a snapshot", fs)
	}

	// A later process: the stored snapshot is where it continues. Rewriting the
	// collected PR line in place would show if it re-read the file instead.
	prLine := prLinkLine(sid, 1, "https://github.com/o/r/pull/1", "o/r", t0)
	rewriteAt(t, path, starts[2], strings.Replace(prLine, "pull/1", "pull/X", 1))
	appendLines(t, path, prLinkLine(sid, 2, "https://github.com/o/r/pull/2", "o/r", t0.Add(time.Minute)))
	s2 := New(claudeDir, "p@example.com", "u1", reloaded, NewClient(srv.URL(), "", ""), nil)
	mustSync(t, s2)
	st = snapshotState(t, reloaded, path)
	if got := encodeSnapshot(t, st.Snapshot); !strings.Contains(got, "pull/1") || !strings.Contains(got, "pull/2") || strings.Contains(got, "pull/X") {
		t.Errorf("continued snapshot = %s, want the stored pull/1 and the new pull/2", got)
	}
	if st.Revision != 2 {
		t.Errorf("revision = %d, want 2", st.Revision)
	}

	if err := s2.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, body := range srv.bodies() {
		if strings.Contains(body, "SUBAGENT-TITLE") || strings.Contains(body, "pull/9") {
			t.Errorf("the subagent transcript's lines reached a snapshot: %s", body)
		}
	}
}

// ---- lazy rebuild ----

// A transcript collected before snapshots existed has no snapshot. The first
// pass that adds lines to it rebuilds one from the part sync collected, and
// only that part: a title before the first-sync skip, or before a shrunk file's
// remnant, must not appear. It is rebuilt once, also when that part holds no
// metadata line; after that, only new lines change it.
func TestSessionSnapshotLazyRebuild(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(fs *FileState, at int64)
	}{
		{"first-sync skip", func(fs *FileState, at int64) { fs.SkippedAtFirstSync = at }},
		{"shrunk remnant", func(fs *FileState, at int64) { fs.CollectedFrom = at }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newObservationServer(t)
			claudeDir := t.TempDir()
			const sid = "sess-rebuild"
			region := customTitleLine(sid, "IN-REGION")
			path, starts := writeTranscript(t, claudeDir, sid,
				aiTitleLine(sid, "BEFORE-START"),
				prLinkLine(sid, 1, "https://github.com/o/r/pull/1", "o/r", t0),
				convLine(sid, "u-2", t0.Add(time.Minute)),
				region,
				prLinkLine(sid, 2, "https://github.com/o/r/pull/2", "o/r", t0.Add(2*time.Minute)),
			)
			s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
			fi, _ := os.Stat(path)
			state.SetOffset(path, fi.Size())
			tc.start(state.Files[path], starts[2])

			appendLines(t, path, convLine(sid, "u-5", t0.Add(3*time.Minute)))
			mustSync(t, s)
			st := snapshotState(t, state, path)
			want := `{"schema":1,"ai_title":"","custom_title":"IN-REGION","agent_name":"",` +
				`"pr_links":[{"url":"https://github.com/o/r/pull/2","number":2,"repository":"o/r","first_seen_at":"2026-10-01T09:02:00Z"}]}`
			if got := encodeSnapshot(t, st.Snapshot); got != want {
				t.Fatalf("rebuilt snapshot\n got %s\nwant %s", got, want)
			}
			if st.Revision != 1 {
				t.Errorf("revision = %d, want 1", st.Revision)
			}

			// Once: a second pass does not read the collected part again. A PR
			// link rewritten in place there would join the list if it did.
			prLine := prLinkLine(sid, 2, "https://github.com/o/r/pull/2", "o/r", t0.Add(2*time.Minute))
			rewriteAt(t, path, starts[4], strings.Replace(prLine, "pull/2", "pull/Z", 1))
			appendLines(t, path, convLine(sid, "u-6", t0.Add(4*time.Minute)))
			mustSync(t, s)
			if got := encodeSnapshot(t, snapshotState(t, state, path).Snapshot); got != want {
				t.Errorf("after a second pass\n got %s\nwant %s: the collected part was read again", got, want)
			}
		})
	}

	t.Run("collected part without metadata", func(t *testing.T) {
		srv := newObservationServer(t)
		claudeDir := t.TempDir()
		const sid = "sess-rebuild-empty"
		plain := padLine(t, convLine(sid, "u-2", t0.Add(time.Minute)), 300)
		path, starts := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", t0), plain)
		s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
		fi, _ := os.Stat(path)
		state.SetOffset(path, fi.Size())

		appendLines(t, path, convLine(sid, "u-3", t0.Add(2*time.Minute)))
		mustSync(t, s)
		reloaded, err := LoadState(state.path)
		if err != nil {
			t.Fatal(err)
		}
		// Nothing to keep: a mark rather than an empty snapshot, which would
		// cost every such transcript some 300 bytes of a file rewritten on every
		// Save.
		fs := reloaded.Files[path]
		entry, _ := json.Marshal(fs)
		if fs.SessionSnapshot != nil || !fs.NoSessionSnapshot || strings.Contains(string(entry), `"session_snapshot":`) {
			t.Fatalf("stored entry %s: want no snapshot, only the mark that the collected part holds none", entry)
		}

		// A later process carries on from the mark: the collected part is not
		// read again, and a metadata line that arrives later starts a snapshot
		// of its own.
		rewriteAt(t, path, starts[1], padLine(t, prLinkLine(sid, 9, "https://github.com/o/r/pull/LATE", "o/r", t0), len(plain)))
		appendLines(t, path, convLine(sid, "u-4", t0.Add(3*time.Minute)))
		s2 := New(claudeDir, "p@example.com", "u1", reloaded, NewClient(srv.URL(), "", ""), nil)
		mustSync(t, s2)
		if fs := reloaded.Files[path]; fs.SessionSnapshot != nil || !fs.NoSessionSnapshot {
			t.Fatalf("after a restart: snapshot %v, mark %v; the collected part was read again", fs.SessionSnapshot != nil, fs.NoSessionSnapshot)
		}
		appendLines(t, path, prLinkLine(sid, 10, "https://github.com/o/r/pull/NEW", "o/r", t0.Add(4*time.Minute)))
		mustSync(t, s2)
		fs = reloaded.Files[path]
		if fs.NoSessionSnapshot || fs.SessionSnapshot == nil || fs.SessionSnapshot.Revision != 1 {
			t.Fatalf("after a metadata line: mark %v, state %+v", fs.NoSessionSnapshot, fs.SessionSnapshot)
		}
		if got := encodeSnapshot(t, fs.SessionSnapshot.Snapshot); !strings.Contains(got, "pull/NEW") || strings.Contains(got, "pull/LATE") {
			t.Errorf("snapshot %s, want the new link alone", got)
		}
	})
}

// The rebuild decodes only the lines that carry one of the six markers, and a
// marker quoted inside a string does not count.
func TestSnapshotMarkerPrefilter(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{aiTitleLine("s", "x"), true},
		{customTitleLine("s", "x"), true},
		{agentNameLine("s", "x"), true},
		{costStateLine("s", 1, 0, 0, 0, false, "m"), true},
		{prLinkLine("s", 1, "u", "r", t0), true},
		{continuedInLine("s", "n", t0), true},
		{convLine("s", "u-1", t0), false},
		{`{"type":"user","message":{"role":"user","content":"{\"type\":\"ai-title\",\"aiTitle\":\"QUOTED\"}"}}`, false},
		{`{"type":"assistant","message":{"content":[{"type":"text","text":"ai-title"}]}}`, false},
		{`{"type":"ai-titles"}`, false},
		{`{"type":"last-prompt","lastPrompt":"pr-link"}`, false},
	} {
		if got := hasSnapshotMarker([]byte(tc.line)); got != tc.want {
			t.Errorf("hasSnapshotMarker(%s) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// ---- allowlist ----

// A repository outside options.collect_repository_prefixes is not collected, so
// it gets no snapshot either, by sync or by re-enrichment.
func TestSessionSnapshotSkipsARepositoryThatIsNotCollected(t *testing.T) {
	repo := transportGitRepo(t, "https://github.com/org/elsewhere.git")
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-disallowed"
	head := fmt.Sprintf(`{"type":"user","uuid":"u-1","timestamp":%q,"sessionId":%q,"cwd":%s,"message":{"role":"user","content":"hi"}}`,
		t0.Format(time.RFC3339), sid, strconvQuote(repo))
	path, _ := writeTranscript(t, claudeDir, sid, head, aiTitleLine(sid, "NOT-COLLECTED"))
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), []string{"github.com/org/allowed"})
	mustSync(t, s)
	fi, _ := os.Stat(path)
	if fs := state.Files[path]; fs == nil || fs.Offset != fi.Size() || fs.SessionSnapshot != nil {
		t.Fatalf("state %+v: want the lines consumed and no snapshot", fs)
	}
	// Lines collected before the repository left the allowlist -- by a client
	// from before snapshots -- are not backfilled once it has.
	const sid2 = "sess-collected-earlier"
	head2 := fmt.Sprintf(`{"type":"user","uuid":"u-9","timestamp":%q,"sessionId":%q,"cwd":%s,"message":{"role":"user","content":"hi"}}`,
		t0.Format(time.RFC3339), sid2, strconvQuote(repo))
	earlier, _ := writeTranscript(t, claudeDir, sid2, head2, aiTitleLine(sid2, "COLLECTED-EARLIER"),
		prLinkLine(sid2, 2, "https://github.com/org/elsewhere/pull/2", "org/elsewhere", t0))
	fi, _ = os.Stat(earlier)
	state.SetOffset(earlier, fi.Size())
	if _, err := s.ReenrichOnce(context.Background(), true); err != nil {
		t.Fatalf("ReenrichOnce: %v", err)
	}
	for _, p := range []string{path, earlier} {
		if fs := state.Files[p]; fs.SessionSnapshot != nil {
			t.Errorf("%s: re-enrichment backfilled a snapshot for a repository that is not collected", filepath.Base(p))
		}
	}
}

// ---- backfill (cctrace sync reenrich) ----

// Re-enrichment gives a historical session its snapshot from the part sync
// collected. By default the titles and the name stay on the machine -- they are
// content -- and with --include-content they go, under the current policy.
func TestReenrichBackfillsTheSessionSnapshot(t *testing.T) {
	for _, includeContent := range []bool{false, true} {
		t.Run(fmt.Sprintf("include-content=%v", includeContent), func(t *testing.T) {
			srv := newObservationServer(t)
			claudeDir := t.TempDir()
			const sid = "sess-backfill"
			path, starts := writeTranscript(t, claudeDir, sid,
				aiTitleLine(sid, "PRE-START-TITLE"),
				prLinkLine(sid, 1, "https://github.com/o/r/pull/1", "o/r", t0),
				convLine(sid, "u-2", t0.Add(time.Minute)),
				aiTitleLine(sid, "REGION-TITLE"),
				agentNameLine(sid, "REGION-AGENT"),
				prLinkLine(sid, 2, "https://github.com/o/r/pull/2", "o/r", t0.Add(2*time.Minute)),
				costStateLine(sid, 1759300000000, 2.5, 7, 3, false, "claude-opus-5-5[1m]"),
			)
			s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
			fi, _ := os.Stat(path)
			state.SetOffset(path, fi.Size())
			state.Files[path].SkippedAtFirstSync = starts[2]

			if _, err := s.ReenrichOnce(context.Background(), includeContent); err != nil {
				t.Fatalf("ReenrichOnce: %v", err)
			}
			reloaded, err := LoadState(state.path)
			if err != nil {
				t.Fatal(err)
			}
			if st := snapshotState(t, reloaded, path); st.Revision != 1 || st.SentRevision != 0 {
				t.Errorf("stored backfill: revision %d sent %d, want dirty", st.Revision, st.SentRevision)
			}
			if err := s.FlushObservations(context.Background()); err != nil {
				t.Fatal(err)
			}
			body := strings.Join(srv.bodies(), "\n")
			for _, never := range []string{"PRE-START-TITLE", "pull/1"} {
				if strings.Contains(body, never) {
					t.Errorf("%s from before the collected part was sent: %s", never, body)
				}
			}
			for _, always := range []string{"pull/2", `"total_cost_usd":2.5`} {
				if !strings.Contains(body, always) {
					t.Errorf("%s missing from %s", always, body)
				}
			}
			for _, content := range []string{"REGION-TITLE", "REGION-AGENT", `"ai_title"`, `"custom_title":""`} {
				if strings.Contains(body, content) != includeContent {
					t.Errorf("%s present=%v, want %v: %s", content, !includeContent, includeContent, body)
				}
			}
		})
	}
}

// The server overwrites a snapshot whole, so a backfill without titles must never
// take the place of a snapshot with them -- in either order.
func TestSessionSnapshotBackfillAndSyncInEitherOrder(t *testing.T) {
	const sid = "sess-order"
	regionLines := func() []string {
		return []string{
			convLine(sid, "u-1", t0),
			aiTitleLine(sid, "THE-TITLE"),
			prLinkLine(sid, 1, "https://github.com/o/r/pull/1", "o/r", t0.Add(time.Minute)),
		}
	}
	flushOne := func(t *testing.T, s *Syncer, srv *observationServer) []SessionSnapshotItem {
		t.Helper()
		before := len(srv.requests())
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
		var items []SessionSnapshotItem
		for _, req := range srv.requests()[before:] {
			items = append(items, req.Snapshots...)
		}
		return items
	}

	t.Run("backfill then sync", func(t *testing.T) {
		srv := newObservationServer(t)
		claudeDir := t.TempDir()
		path, _ := writeTranscript(t, claudeDir, sid, regionLines()...)
		s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
		fi, _ := os.Stat(path)
		state.SetOffset(path, fi.Size())

		if _, err := s.ReenrichOnce(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		items := flushOne(t, s, srv)
		if len(items) != 1 || items[0].Snapshot.AITitle != nil {
			t.Fatalf("the backfill sent %+v, want one snapshot without titles", items)
		}

		// New lines arrive, none of them a title: the snapshot sync sends carries
		// the title the collected part holds.
		appendLines(t, path, convLine(sid, "u-2", t0.Add(time.Hour)))
		mustSync(t, s)
		items = flushOne(t, s, srv)
		if len(items) != 1 || deref(items[0].Snapshot.AITitle) != "THE-TITLE" || len(items[0].Snapshot.PRLinks) != 1 {
			t.Fatalf("after sync: sent %s, want THE-TITLE and the PR link", encodeItems(t, items))
		}
	})

	t.Run("sync then backfill", func(t *testing.T) {
		srv := newObservationServer(t)
		claudeDir := t.TempDir()
		path, _ := writeTranscript(t, claudeDir, sid, regionLines()...)
		s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
		mustSync(t, s)
		if items := flushOne(t, s, srv); len(items) != 1 || deref(items[0].Snapshot.AITitle) != "THE-TITLE" {
			t.Fatalf("sync sent %s, want the title", encodeItems(t, items))
		}

		if _, err := s.ReenrichOnce(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		for _, it := range flushOne(t, s, srv) {
			if it.Snapshot.AITitle == nil {
				t.Errorf("the backfill sent a snapshot without titles over one with them: %s", encodeSnapshot(t, it.Snapshot))
			}
		}

		// A backfill that does find something new merges it into the snapshot
		// with titles and sends that.
		st := snapshotState(t, state, path)
		st.Snapshot.PRLinks = nil
		st.Revision++
		st.SentRevision = st.Revision
		if _, err := s.ReenrichOnce(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		items := flushOne(t, s, srv)
		if len(items) != 1 || deref(items[0].Snapshot.AITitle) != "THE-TITLE" || len(items[0].Snapshot.PRLinks) != 1 {
			t.Fatalf("merged backfill sent %s, want the stored title with the PR link", encodeItems(t, items))
		}
	})
}

func encodeItems(t *testing.T, items []SessionSnapshotItem) string {
	t.Helper()
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---- size ----

// The server refuses a snapshot over 64 KiB. Identifiers are cut to 512 bytes on
// a rune boundary and a run keeps at most 32 models; past 60 KiB encoded, the
// oldest runs go first, then the last PR links, each cut named in truncated.
func TestSessionSnapshotStaysUnderTheServersCap(t *testing.T) {
	t.Run("identifiers", func(t *testing.T) {
		long := strings.Repeat("가", 700) // 2,100 bytes
		snap := newSessionSnapshot()
		snap.apply([]byte(prLinkLine("s", 1, long, long, t0)))
		// Two ids alike for their first 512 bytes, sorting ahead of forty short
		// ones so they are among those kept.
		models := []string{"a" + long + "x", "a" + long + "y"}
		for i := 0; i < 40; i++ {
			models = append(models, fmt.Sprintf("model-%02d", i))
		}
		snap.apply([]byte(costStateLine("s", 1, 0.1, 0, 0, false, models...)))
		p := snap.PRLinks[0]
		for name, v := range map[string]string{"url": p.URL, "repository": p.Repository} {
			if len(v) > maxSnapshotFieldBytes || !utf8.ValidString(v) || !strings.HasPrefix(long, v) {
				t.Errorf("%s kept %d bytes, valid=%v; want a rune-boundary cut at %d", name, len(v), utf8.ValidString(v), maxSnapshotFieldBytes)
			}
		}
		kept := snap.CostStates[0].Models
		if len(kept) != maxSnapshotModels {
			t.Errorf("models: %d kept, want %d", len(kept), maxSnapshotModels)
		}
		seen := map[string]bool{}
		for _, m := range kept {
			if len(m) > maxSnapshotFieldBytes || !utf8.ValidString(m) {
				t.Errorf("model id kept at %d bytes, valid=%v; want a rune-boundary cut at %d", len(m), utf8.ValidString(m), maxSnapshotFieldBytes)
			}
			if seen[m] {
				t.Error("two ids alike once cut were both kept")
			}
			seen[m] = true
		}
		if !strings.HasPrefix(kept[0], "a"+long[:30]) {
			t.Errorf("first model %.20q..., want the long id, cut", kept[0])
		}
	})

	for _, tc := range []struct {
		name  string
		lines func(sid string) []string
		check func(t *testing.T, snap SessionSnapshot)
	}{
		{"oldest runs go first", func(sid string) []string {
			var lines []string
			for run := 1; run <= maxSnapshotCostStates; run++ {
				var models []string
				for m := 0; m < maxSnapshotModels; m++ {
					models = append(models, fmt.Sprintf("m%02d-%s", m, strings.Repeat("x", 480)))
				}
				lines = append(lines, costStateLine(sid, int64(run), 0.1, 0, 0, false, models...))
			}
			return lines
		}, func(t *testing.T, snap SessionSnapshot) {
			n := len(snap.CostStates)
			if n == 0 || n == maxSnapshotCostStates || snap.CostStates[n-1].StartTimeMs != maxSnapshotCostStates {
				t.Errorf("%d runs kept, newest %d; want the newest few", n, snap.CostStates[n-1].StartTimeMs)
			}
			if !slices.Contains(snap.Truncated, "cost_states") {
				t.Error("cost_states cut without being named in truncated")
			}
		}},
		{"then the last PR links", func(sid string) []string {
			var lines []string
			for n := 1; n <= maxSnapshotPRLinks; n++ {
				url := fmt.Sprintf("https://github.com/o/r/pull/%03d/%s", n, strings.Repeat("u", 480))
				lines = append(lines, prLinkLine(sid, n, url, strings.Repeat("r", 600), t0))
			}
			return lines
		}, func(t *testing.T, snap SessionSnapshot) {
			n := len(snap.PRLinks)
			if n == 0 || n == maxSnapshotPRLinks || snap.PRLinks[0].Number != 1 {
				t.Errorf("%d PR links kept from %d; want the first few", n, snap.PRLinks[0].Number)
			}
			if !slices.Contains(snap.Truncated, "pr_links") {
				t.Error("pr_links cut without being named in truncated")
			}
		}},
	} {
		for _, via := range []string{"sync", "backfill"} {
			t.Run(tc.name+" by "+via, func(t *testing.T) {
				srv := newObservationServer(t)
				claudeDir := t.TempDir()
				sid := "sess-size"
				path, _ := writeTranscript(t, claudeDir, sid, append([]string{convLine(sid, "u-1", t0)}, tc.lines(sid)...)...)
				s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
				if via == "sync" {
					mustSync(t, s)
				} else {
					fi, _ := os.Stat(path)
					state.SetOffset(path, fi.Size())
					if _, err := s.ReenrichOnce(context.Background(), false); err != nil {
						t.Fatal(err)
					}
				}
				snap := snapshotState(t, state, path).Snapshot
				if b, _ := json.Marshal(snap); len(b) > maxSnapshotBytes {
					t.Fatalf("snapshot encodes to %d bytes, over %d", len(b), maxSnapshotBytes)
				}
				tc.check(t, snap)
			})
		}
	}
}

// ---- lines the allowlist skipped ----

// Lines of a repository the allowlist does not collect are consumed without
// being collected. If the repository is allowed later -- the allowlist removed,
// or widened to include it -- neither the snapshot rebuild nor re-enrichment
// may read them. The file is marked as holding them and is not read again;
// where its collected part starts does not move, since such lines can lie
// anywhere in a file, behind allowed ones too.
func TestAllowlistSkipIsNotReadAgainOnceAllowed(t *testing.T) {
	for name, later := range map[string][]string{"allowlist removed": nil, "allowlist widened": {"github.com/org/"}} {
		t.Run(name, func(t *testing.T) {
			repo := transportGitRepo(t, "https://github.com/org/elsewhere.git")
			srv := newObservationServer(t)
			claudeDir := t.TempDir()
			const sid = "sess-allowed-later"
			head := func(uuid string, at time.Time) string {
				return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":%q,"sessionId":%q,"cwd":%s,"message":{"role":"user","content":"hi"}}`,
					uuid, at.Format(time.RFC3339), sid, strconvQuote(repo))
			}
			path, _ := writeTranscript(t, claudeDir, sid, head("u-1", t0), aiTitleLine(sid, "NOT-COLLECTED-TITLE"),
				prLinkLine(sid, 1, "https://github.com/org/elsewhere/pull/1", "org/elsewhere", t0))
			state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := state.Save(); err != nil {
				t.Fatal(err)
			}
			denying := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), []string{"github.com/org/allowed"})
			mustSync(t, denying)
			fi, _ := os.Stat(path)
			if fs := state.Files[path]; fs.Offset != fi.Size() || !fs.ExcludedSeen || fs.CollectedFrom != 0 {
				t.Fatalf("offset %d, excluded seen %v, collected from %d; want the skipped lines passed (%d) and marked, the collected start unmoved", fs.Offset, fs.ExcludedSeen, fs.CollectedFrom, fi.Size())
			}

			allowing := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), later)
			appendLines(t, path, head("u-2", t0.Add(time.Minute)), aiTitleLine(sid, "COLLECTED-TITLE"))
			mustSync(t, allowing)
			if _, err := allowing.ReenrichOnce(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			got := encodeSnapshot(t, snapshotState(t, state, path).Snapshot)
			if strings.Contains(got, "NOT-COLLECTED") || strings.Contains(got, "pull/1") || !strings.Contains(got, "COLLECTED-TITLE") {
				t.Errorf("snapshot %s: want only what was collected after the repository was allowed", got)
			}
		})
	}
}

// ---- A, B, A ----

// The newest title wins, also when it returns to an earlier value between two
// sends: A, then B, then A again goes out as three snapshots, each dated later
// than the one before, so the server's newer-wins row ends at A.
func TestSessionSnapshotTitleABAWithSendsBetween(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	const sid = "sess-aba"
	path, _ := writeTranscript(t, claudeDir, sid, convLine(sid, "u-1", t0), aiTitleLine(sid, "TITLE-A"))
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
	for i, title := range []string{"TITLE-A", "TITLE-B", "TITLE-A"} {
		if i > 0 {
			advance(time.Second)
			appendLines(t, path, aiTitleLine(sid, title))
		}
		mustSync(t, s)
		if err := s.FlushObservations(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	reqs := srv.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want one per send", len(reqs))
	}
	var titles []string
	for i, req := range reqs {
		if len(req.Snapshots) != 1 {
			t.Fatalf("send %d carried %d snapshots", i, len(req.Snapshots))
		}
		titles = append(titles, deref(req.Snapshots[0].Snapshot.AITitle))
		if i > 0 && !req.Snapshots[0].ObservedAt.After(reqs[i-1].Snapshots[0].ObservedAt) {
			t.Errorf("send %d dated %v, not after the one before", i, req.Snapshots[0].ObservedAt)
		}
	}
	if got := strings.Join(titles, ","); got != "TITLE-A,TITLE-B,TITLE-A" {
		t.Errorf("sent %s, want TITLE-A,TITLE-B,TITLE-A", got)
	}
}
