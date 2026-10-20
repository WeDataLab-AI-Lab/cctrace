package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// recordEnd writes a session end into the pending directory beside state's file
// and returns its path.
func recordEnd(t *testing.T, state *State, end SessionEnd) string {
	t.Helper()
	dir := SessionEndDir(state.path)
	if err := RecordSessionEnd(dir, end); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, sessionEndEventID(end.SessionID, end.EndedAt)+".json")
}

func eventsOf(reqs []SessionObservationsRequest) []SessionObservationEventItem {
	var out []SessionObservationEventItem
	for _, req := range reqs {
		out = append(out, req.Events...)
	}
	return out
}

func pendingFiles(t *testing.T, state *State) []string {
	t.Helper()
	entries, err := os.ReadDir(SessionEndDir(state.path))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// The hook's parent writes a session end as one whole file, named by its event
// id, readable only by its owner, with nothing left beside it -- the write goes
// through a temporary file and a rename -- and with the home in the one spelling
// the flush compares.
func TestRecordSessionEndWritesOneWholeFile(t *testing.T) {
	claudeDir := t.TempDir()
	_, state := newSnapshotSyncer(t, claudeDir, "http://observations.invalid")
	endedAt := time.Date(2026, 10, 6, 1, 2, 3, 456789000, time.FixedZone("KST", 9*3600))
	path := recordEnd(t, state, SessionEnd{SessionID: "sess-end", Reason: "logout", EndedAt: endedAt,
		TranscriptPath: "/x/sess-end.jsonl", CWD: "/work", ClaudeHome: claudeDir + string(filepath.Separator) + "."})

	if names := pendingFiles(t, state); len(names) != 1 || names[0] != filepath.Base(path) {
		t.Fatalf("pending directory holds %v, want the one file %s", names, filepath.Base(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %v, want 0600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got SessionEnd
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := SessionEnd{SessionID: "sess-end", Reason: "logout", EndedAt: endedAt.UTC(),
		TranscriptPath: "/x/sess-end.jsonl", CWD: "/work", ClaudeHome: normalizeHome(claudeDir)}
	if got != want {
		t.Errorf("stored %+v, want %+v", got, want)
	}
	if !strings.Contains(string(data), `"ended_at":"2026-10-05T16:02:03.456789Z"`) {
		t.Errorf("ended_at is not stored as UTC RFC3339Nano: %s", data)
	}
}

// The write goes through internal/atomicfile: a temporary file renamed into
// place, which also never follows a link standing where the file is to go. A
// plain write would write through it into whatever it points at.
func TestRecordSessionEndNeverWritesThroughALink(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("UNTOUCHED"), 0o600); err != nil {
		t.Fatal(err)
	}
	end := SessionEnd{SessionID: "sess-link", Reason: "clear", EndedAt: time.Now(), ClaudeHome: t.TempDir()}
	if err := os.Symlink(victim, filepath.Join(dir, sessionEndEventID(end.SessionID, end.EndedAt)+".json")); err != nil {
		t.Fatal(err)
	}
	if err := RecordSessionEnd(dir, end); err == nil {
		t.Error("a session end was written over a link")
	}
	if data, _ := os.ReadFile(victim); string(data) != "UNTOUCHED" {
		t.Errorf("the link's target now holds %q", data)
	}
}

// Past 1,000 files the hook records nothing more, rather than letting a
// directory nothing drains grow without bound.
func TestRecordSessionEndRefusesPastTheCap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxPendingSessionEnds-1; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.json", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	end := SessionEnd{SessionID: "sess-cap", EndedAt: time.Now(), ClaudeHome: t.TempDir()}
	if err := RecordSessionEnd(dir, end); err != nil {
		t.Fatalf("the 1,000th file: %v", err)
	}
	end.EndedAt = end.EndedAt.Add(time.Second)
	if err := RecordSessionEnd(dir, end); err == nil || !strings.Contains(err.Error(), "the most it keeps") {
		t.Fatalf("the 1,001st file: err %v, want it refused", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != maxPendingSessionEnds {
		t.Errorf("%d files, want %d", len(entries), maxPendingSessionEnds)
	}
}

// A session end goes as a session_end event: its id is the first 32 hex digits
// of sha256(session id, NUL, end time in RFC3339Nano), observed when the session
// ended, under the account of that time, its reason cut to 64 bytes. Its project
// is its transcript's directory; without a transcript, its working directory's.
// A transcript outside this home's projects/ belongs to no session of this home,
// so that session end is not sent: it waits to expire, said once.
func TestSessionEndBecomesAnEvent(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	writeClaudeConfig(t, claudeDir, "acct-work")
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	mustSync(t, s) // observes the home's account
	endedAt := time.Now().Add(time.Millisecond).Round(0)
	transcript := filepath.Join(claudeDir, "projects", "-Users-me-Code-Repo", "sess-a.jsonl")
	recordEnd(t, state, SessionEnd{SessionID: "sess-a", Reason: strings.Repeat("r", 100), EndedAt: endedAt,
		TranscriptPath: transcript, CWD: "/elsewhere", ClaudeHome: claudeDir})
	outside := recordEnd(t, state, SessionEnd{SessionID: "sess-b", Reason: "clear", EndedAt: endedAt.Add(time.Second),
		TranscriptPath: "/elsewhere/sess-b.jsonl", CWD: "/Users/me/Other", ClaudeHome: claudeDir})
	recordEnd(t, state, SessionEnd{SessionID: "sess-c", Reason: strings.Repeat("가", 30), EndedAt: endedAt.Add(2 * time.Second),
		CWD: "/Users/me/Other", ClaudeHome: claudeDir})

	flushOnce(t, s)
	flushOnce(t, s)
	events := eventsOf(srv.requests())
	if len(events) != 2 || events[0].SessionID != "sess-a" || events[1].SessionID != "sess-c" {
		t.Fatalf("events %+v, want sess-a and sess-c: %s", events, srv.bodies())
	}
	sum := sha256.Sum256([]byte("sess-a\x00" + endedAt.UTC().Format(time.RFC3339Nano)))
	a, b := events[0], events[1]
	if a.EventID != hex.EncodeToString(sum[:])[:32] || a.Kind != "session_end" || !a.ObservedAt.Equal(endedAt) ||
		a.ProjectHash != "-Users-me-Code-Repo" || a.AccountID != "acct-work" || a.Agent != "claude" || a.BillingProvider != "anthropic" {
		t.Errorf("event %+v", a)
	}
	if a.Payload.Reason != strings.Repeat("r", 64) {
		t.Errorf("reason %q, want it cut to 64 bytes", a.Payload.Reason)
	}
	if b.ProjectHash != "-users-me-other" {
		t.Errorf("project without a transcript = %q, want the working directory's", b.ProjectHash)
	}
	if len(b.Payload.Reason) > 64 || !utf8.ValidString(b.Payload.Reason) || b.Payload.Reason == "" {
		t.Errorf("reason %q: want at most 64 bytes, cut on a rune boundary", b.Payload.Reason)
	}
	for _, body := range srv.bodies() {
		for _, local := range []string{"/elsewhere", "transcript_path", "/another/home"} {
			if strings.Contains(body, local) {
				t.Errorf("%s left the machine: %s", local, body)
			}
		}
	}
	if names := pendingFiles(t, state); fmt.Sprint(names) != "["+filepath.Base(outside)+"]" {
		t.Errorf("pending after the 200s: %v, want only the one outside this home", names)
	}
	if n := strings.Count(logs.String(), "names a transcript outside"); n != 1 {
		t.Errorf("the transcript outside this home was logged %d times, want once:\n%s", n, logs.String())
	}
}

// The home a session end is recorded under is the one its transcript sits in --
// <home>/projects/<dir>/<file>.jsonl -- and the hook's --claude-dir only when
// there is no transcript of that shape: a hook of one home can end a session of
// another, and that home's Syncer is the one to send it.
func TestRecordSessionEndTakesTheTranscriptsHome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	transcriptHome, hookHome := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		name, transcript, want string
	}{
		{"transcript of a home", filepath.Join(transcriptHome, "projects", "-p", "s1.jsonl"), transcriptHome},
		{"no transcript", "", hookHome},
		{"a transcript of another shape", filepath.Join(transcriptHome, "s3.jsonl"), hookHome},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end := SessionEnd{SessionID: tc.name, EndedAt: time.Now(), TranscriptPath: tc.transcript, ClaudeHome: hookHome}
			if err := RecordSessionEnd(dir, end); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, sessionEndEventID(end.SessionID, end.EndedAt)+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var got SessionEnd
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.ClaudeHome != normalizeHome(tc.want) {
				t.Errorf("home %q, want %q", got.ClaudeHome, tc.want)
			}
		})
	}
}

// A session id or project past the server's cap would have the whole item
// refused, so that session end is not sent: it waits to expire, said once.
func TestSessionEndPastTheServersCapIsNotSent(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	long := recordEnd(t, state, SessionEnd{SessionID: strings.Repeat("s", maxObservationKeyBytes+1), EndedAt: time.Now(), CWD: "/work", ClaudeHome: claudeDir})
	deep := recordEnd(t, state, SessionEnd{SessionID: "sess-deep", EndedAt: time.Now(), CWD: "/" + strings.Repeat("d/", maxObservationKeyBytes/2+1), ClaudeHome: claudeDir})

	flushOnce(t, s)
	flushOnce(t, s)
	if events := eventsOf(srv.requests()); len(events) != 0 {
		t.Errorf("sent %d session ends past a cap", len(events))
	}
	for _, path := range []string{long, deep} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was not kept: %v", filepath.Base(path), err)
		}
	}
	if n := strings.Count(logs.String(), "past the server's"); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, logs.String())
	}
}

// A directory that filled while nothing flushed -- sync stopped, the hook still
// recording -- frees itself: recording deletes what has expired before counting.
// Entries this client never writes, such as a directory, are not counted.
func TestRecordSessionEndFreesExpiredFilesBeforeCounting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// One file short of the cap that counts, ten expired ones and a directory:
	// counting either of the last two would refuse the next session end.
	old := time.Now().Add(-8 * 24 * time.Hour)
	for i := 0; i < maxPendingSessionEnds-1+10; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%04d.json", i))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if i < 10 {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "not-a-session-end"), 0o700); err != nil {
		t.Fatal(err)
	}
	end := SessionEnd{SessionID: "sess-after-the-cap", EndedAt: time.Now(), ClaudeHome: t.TempDir()}
	if err := RecordSessionEnd(dir, end); err != nil {
		t.Fatalf("recording into a full directory with expired files: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 999 younger files, the new one, and the directory.
	if len(entries) != maxPendingSessionEnds+1 {
		t.Errorf("%d entries, want the 10 expired files gone", len(entries))
	}
}

// A file is deleted only once the server has the event: a 200 says so whether
// it stored it, skipped it or had it already. A 404, a failure of the server or
// of the connection, or a refused envelope keeps it for a later flush.
func TestSessionEndIsDeletedOnlyAfter_A200(t *testing.T) {
	refuse := func(status int, body string) func(http.ResponseWriter, SessionObservationsRequest) bool {
		return func(w http.ResponseWriter, _ SessionObservationsRequest) bool {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return true
		}
	}
	answer := func(resp SessionObservationsResponse) func(http.ResponseWriter, SessionObservationsRequest) bool {
		return func(w http.ResponseWriter, _ SessionObservationsRequest) bool {
			_ = json.NewEncoder(w).Encode(resp)
			return true
		}
	}
	for _, tc := range []struct {
		name   string
		answer func(http.ResponseWriter, SessionObservationsRequest) bool
		down   bool
		kept   bool
	}{
		{"stored", answer(SessionObservationsResponse{Accepted: 1, Stored: 1}), false, false},
		{"skipped", answer(SessionObservationsResponse{Accepted: 1, Skipped: 1}), false, false},
		{"already there", answer(SessionObservationsResponse{Accepted: 1}), false, false},
		{"older server", refuse(http.StatusNotFound, "404 page not found"), false, true},
		{"server failing", refuse(http.StatusServiceUnavailable, "unavailable"), false, true},
		{"envelope refused", refuse(http.StatusBadRequest, `{"error":"user_id is 300 bytes (max 256)"}`), false, true},
		{"connection failing", nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newObservationServer(t)
			srv.setAnswer(tc.answer)
			endpoint := srv.URL()
			if tc.down {
				endpoint = "http://127.0.0.1:1" // nothing listens on port 1
			}
			claudeDir := t.TempDir()
			s, state := newSnapshotSyncer(t, claudeDir, endpoint)
			path := recordEnd(t, state, SessionEnd{SessionID: "sess-200", Reason: "clear", EndedAt: time.Now(), CWD: "/work", ClaudeHome: claudeDir})

			err := s.FlushObservations(context.Background())
			if (err != nil) != tc.kept {
				t.Errorf("flush err = %v", err)
			}
			if _, statErr := os.Stat(path); (statErr == nil) != tc.kept {
				t.Errorf("pending file present=%v, want %v", statErr == nil, tc.kept)
			}
		})
	}
}

// The server refusing one event alone -- not the envelope, which a heartbeat
// alone shows it accepts -- deletes that event's file with a line saying so,
// and lets the rest through.
func TestSessionEndRefusedAloneIsDeletedWithALogLine(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	const reason = "events[0]: session_id is 300 bytes (max 256)"
	srv.setAnswer(func(w http.ResponseWriter, req SessionObservationsRequest) bool {
		for _, ev := range req.Events {
			if ev.SessionID == "sess-bad" {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
				return true
			}
		}
		return false
	})
	claudeDir := t.TempDir()
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	now := time.Now()
	bad := recordEnd(t, state, SessionEnd{SessionID: "sess-bad", EndedAt: now, CWD: "/work", ClaudeHome: claudeDir})
	good := recordEnd(t, state, SessionEnd{SessionID: "sess-good", EndedAt: now.Add(time.Second), CWD: "/work", ClaudeHome: claudeDir})

	err := s.FlushObservations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refused 1 session observation") {
		t.Fatalf("flush err = %v, want the refusal counted", err)
	}
	for _, path := range []string{bad, good} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still pending: %v", filepath.Base(path), err)
		}
	}
	var stored []string
	for _, ev := range eventsOf(srv.requests()) {
		if ev.SessionID == "sess-good" {
			stored = append(stored, ev.SessionID)
		}
	}
	if len(stored) == 0 {
		t.Error("the other session end was not sent")
	}
	if line := "the server refused the session end of session sess-bad and its pending file is deleted"; strings.Count(logs.String(), line) != 1 || !strings.Contains(logs.String(), reason) {
		t.Errorf("log, want the refusal once with the server's reason:\n%s", logs.String())
	}
}

// The cap on lone refusals holds for session ends as for snapshots: a server
// refusing every one is enforcing a rule this client does not know, so past
// three the flush stops and parks, and the rest stay pending.
func TestFlushStopsAfterTooManyRefusedSessionEnds(t *testing.T) {
	advance := fakeClock(t)
	srv := newObservationServer(t)
	srv.setAnswer(func(w http.ResponseWriter, req SessionObservationsRequest) bool {
		if len(req.Events) == 0 {
			return false // the envelope is fine
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "events[0]: a rule this client does not know"})
		return true
	})
	claudeDir := t.TempDir()
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	for i := 0; i < 6; i++ {
		recordEnd(t, state, SessionEnd{SessionID: fmt.Sprintf("sess-%d", i), EndedAt: nowFn().Add(time.Duration(i) * time.Second), CWD: "/work", ClaudeHome: claudeDir})
	}

	if err := s.FlushObservations(context.Background()); !errors.Is(err, errRefusingItems) {
		t.Fatalf("flush err = %v, want it stopped at the refusal past %d", err, maxLoneRefusals)
	}
	if n := len(pendingFiles(t, state)); n != 6-maxLoneRefusals {
		t.Errorf("%d pending, want all but the %d refused alone", n, maxLoneRefusals)
	}
	reqs := len(srv.requests())
	advance(observationsRefusingPark - time.Second)
	if err := s.FlushObservations(context.Background()); err != nil || len(srv.requests()) != reqs {
		t.Errorf("within the hour: err %v, %d more requests; want it parked", err, len(srv.requests())-reqs)
	}
}

// A file older than seven days is deleted unsent, whichever home it is of; a
// younger one goes. A write in progress -- not yet renamed to .json -- is not
// read as a session end.
func TestPendingSessionEndsExpireAfterSevenDays(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	now := time.Now()
	old := recordEnd(t, state, SessionEnd{SessionID: "sess-old", EndedAt: now.Add(-8 * 24 * time.Hour), CWD: "/work", ClaudeHome: claudeDir})
	young := recordEnd(t, state, SessionEnd{SessionID: "sess-young", EndedAt: now.Add(-6 * 24 * time.Hour), CWD: "/work", ClaudeHome: claudeDir})
	other := recordEnd(t, state, SessionEnd{SessionID: "sess-other-home", EndedAt: now.Add(-8 * 24 * time.Hour), CWD: "/work", ClaudeHome: t.TempDir()})
	for path, age := range map[string]time.Duration{old: 8 * 24 * time.Hour, young: 6 * 24 * time.Hour, other: 8 * 24 * time.Hour} {
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	partial := filepath.Join(SessionEndDir(state.path), "0123.json.tmp-42")
	if err := os.WriteFile(partial, []byte(`{"session_id":"sess-par`), 0o600); err != nil {
		t.Fatal(err)
	}

	flushOnce(t, s)
	var sent []string
	for _, ev := range eventsOf(srv.requests()) {
		sent = append(sent, ev.SessionID)
	}
	if fmt.Sprint(sent) != "[sess-young]" {
		t.Errorf("sent %v, want the younger one alone", sent)
	}
	if names := pendingFiles(t, state); fmt.Sprint(names) != "["+filepath.Base(partial)+"]" {
		t.Errorf("pending %v, want only the write in progress", names)
	}
	if strings.Contains(logs.String(), "session ends:") {
		t.Errorf("a write in progress was read as a session end:\n%s", logs.String())
	}
}

// Several Claude homes can share a profile's pending directory, as they share
// its state file. A session end belongs to the home its hook ran for: one home's
// flush sends its own, under its own account, and leaves the other's file for
// that home's Syncer -- the other home's account may be excluded.
func TestFlushSendsOnlyItsOwnHomesSessionEnds(t *testing.T) {
	srv := newObservationServer(t)
	srv.setExclusions(http.StatusOK, map[string]bool{})
	homeA, homeB := t.TempDir(), t.TempDir()
	writeClaudeConfig(t, homeA, "acct-work")
	writeClaudeConfig(t, homeB, "acct-personal")
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	newClient := func() *Client {
		c := NewClient(srv.URL(), "", "")
		c.SetExcludedAccounts([]string{"anthropic:acct-personal"})
		return c
	}
	sA := New(homeA, "p@example.com", "u1", state, newClient(), nil)
	sB := New(homeB, "p@example.com", "u1", state, newClient(), nil)
	mustSync(t, sA)
	mustSync(t, sB)
	now := time.Now()
	recordEnd(t, state, SessionEnd{SessionID: "sess-work-a", Reason: "clear", EndedAt: now, CWD: "/work", ClaudeHome: homeA})
	pathB := recordEnd(t, state, SessionEnd{SessionID: "sess-personal-b", Reason: "clear", EndedAt: now, CWD: "/home", ClaudeHome: homeB})

	flushOnce(t, sA)
	events := eventsOf(srv.requests())
	if len(events) != 1 || events[0].SessionID != "sess-work-a" || events[0].AccountID != "acct-work" {
		t.Fatalf("home A's flush sent %s, want its own session end alone, as acct-work", srv.bodies())
	}
	if _, err := os.Stat(pathB); err != nil {
		t.Fatalf("home A's flush took home B's session end: %v", err)
	}

	flushOnce(t, sB)
	for _, body := range srv.bodies() {
		if strings.Contains(body, "sess-personal-b") {
			t.Errorf("the excluded account's session end was sent: %s", body)
		}
	}
	if _, err := os.Stat(pathB); !os.IsNotExist(err) {
		t.Errorf("home B's own flush did not consume its excluded session end: %v", err)
	}
}
