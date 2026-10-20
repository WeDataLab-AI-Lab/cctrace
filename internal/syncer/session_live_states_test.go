package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cctrace/internal/gitctx"
)

// sessionFileFields is a process file in the shape measured on a real machine
// (Claude Code 2.1.289). Its pidDomain is another platform's, so this client
// cannot check the process and reports the status as the file gives it; the
// liveness tests set their own.
func sessionFileFields(pid int, sessionID string) map[string]any {
	return map[string]any{
		"pid": pid, "sessionId": sessionID, "cwd": "/tmp/live-repo", "startedAt": 1791127608821,
		"procStart": "Sun Oct  4 15:26:48 2026", "version": "2.1.289", "peerProtocol": 1,
		"peerFeatures": []string{"notify_idle", "reply_across_default_dirs"}, "kind": "interactive",
		"entrypoint": "cli", "pidDomain": "elsewhere", "messagingSocketPath": "/tmp/claude.sock",
		"name": "LIVE-NAME", "nameSource": "user", "nameSince": 1791127608821, "status": "idle",
		"updatedAt": 1791208506977, "statusUpdatedAt": 1791208506977,
	}
}

// writeSessionFile writes a process file, <pid>.json, into claudeDir/sessions.
func writeSessionFile(t *testing.T, claudeDir string, fields map[string]any) string {
	t.Helper()
	dir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%v.json", fields["pid"]))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func liveStatesOf(reqs []SessionObservationsRequest) []SessionLiveStateItem {
	var out []SessionLiveStateItem
	for _, req := range reqs {
		out = append(out, req.LiveStates...)
	}
	return out
}

func flushOnce(t *testing.T, s *Syncer) {
	t.Helper()
	if err := s.FlushObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Only <pid>.json is read. The <pid>.<hex>.key file beside it is never opened --
// it is unreadable here, and opening it would fail and be logged. A file that
// does not parse, lacks a key field or holds an unknown status is skipped, and
// each kind of problem is logged once, not once per file per minute.
func TestLiveStatesReadOnlyProcessFilesAndSkipBadOnes(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	dir := filepath.Join(claudeDir, "sessions")
	writeSessionFile(t, claudeDir, sessionFileFields(101, "sess-good"))
	key := filepath.Join(dir, "101.0123456789abcdef.key")
	if err := os.WriteFile(key, []byte("KEY-MATERIAL"), 0o000); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"102.json": `{"pid":102,"sessionId":`, // cut off mid-write
		"103.json": `not json`,
		"104.json": `{"pid":104,"sessionId":"sess-nostatus","procStart":"Sun Oct  4 15:26:48 2026","cwd":"/tmp/live-repo"}`,
		"105.json": `{"pid":105,"procStart":"Sun Oct  4 15:26:48 2026","status":"idle","cwd":"/tmp/live-repo"}`,
		"106.json": `{"pid":106,"sessionId":"sess-odd","procStart":"Sun Oct  4 15:26:48 2026","status":"thinking","cwd":"/tmp/live-repo"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	flushOnce(t, s)
	flushOnce(t, s)
	states := liveStatesOf(srv.requests())
	if len(states) != 2 || states[0].SessionID != "sess-good" || states[1].SessionID != "sess-good" {
		t.Fatalf("live states %+v, want sess-good alone in each flush", states)
	}
	out := logs.String()
	for _, never := range []string{".key", "permission denied", "KEY-MATERIAL"} {
		if strings.Contains(out, never) {
			t.Errorf("the log mentions %q: a key file was opened\n%s", never, out)
		}
	}
	for _, body := range srv.bodies() {
		if strings.Contains(body, "KEY-MATERIAL") {
			t.Errorf("key material left the machine: %s", body)
		}
	}
	for problem, want := range map[string]int{"does not parse": 1, "lacks sessionId": 1, `holds status "thinking"`: 1} {
		if n := strings.Count(out, problem); n != want {
			t.Errorf("%q logged %d times over two flushes and two such files, want %d:\n%s", problem, n, want, out)
		}
	}
}

// A process file that is not a session's is skipped, each kind logged once:
// Claude Code's daemon and its workers, and a bg process started ahead of need
// (spare) until a session claims it. A bg session, an interactive one, and a
// file of a version that wrote no kind are sent.
func TestLiveStatesSkipProcessesThatAreNotSessions(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	for pid, kind := range map[int]any{701: "daemon", 702: "daemon-worker", 703: "bg", 704: "interactive", 705: nil, 706: "daemon"} {
		f := sessionFileFields(pid, fmt.Sprintf("sess-%d", pid))
		if kind == nil {
			delete(f, "kind")
		} else {
			f["kind"] = kind
		}
		writeSessionFile(t, claudeDir, f)
	}
	spare := sessionFileFields(707, "sess-707")
	spare["kind"], spare["spare"] = "bg", true
	writeSessionFile(t, claudeDir, spare)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	flushOnce(t, s)
	flushOnce(t, s)
	sent := map[string]int{}
	for _, st := range liveStatesOf(srv.requests()) {
		sent[st.SessionID]++
	}
	if fmt.Sprint(sent) != "map[sess-703:2 sess-704:2 sess-705:2]" {
		t.Errorf("sent %v, want the bg, interactive and kind-less sessions in each flush", sent)
	}
	if n := strings.Count(logs.String(), "not a session's"); n != 1 {
		t.Errorf("skipped processes logged %d times, want once:\n%s", n, logs.String())
	}
}

// Claude Code on Windows writes procStartFt in place of procStart. Its files are
// skipped, and the log says that is why, once.
func TestLiveStatesSayWhyWindowsFilesAreSkipped(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	for pid, domain := range map[int]string{1101: "win32:desktop-1", 1102: "win32:desktop-1"} {
		f := sessionFileFields(pid, fmt.Sprintf("sess-%d", pid))
		delete(f, "procStart")
		f["procStartFt"], f["pidDomain"] = "133731234567890123", domain
		writeSessionFile(t, claudeDir, f)
	}
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	flushOnce(t, s)
	flushOnce(t, s)
	if states := liveStatesOf(srv.requests()); len(states) != 0 {
		t.Errorf("sent %+v, want nothing from Windows", states)
	}
	out := logs.String()
	if n := strings.Count(out, "Claude Code on Windows writes procStartFt in its place"); n != 1 {
		t.Errorf("the Windows cause logged %d times, want once:\n%s", n, out)
	}
	if strings.Contains(out, "lacks sessionId") {
		t.Errorf("logged as an incomplete file instead:\n%s", out)
	}
}

// A process file is read up to Claude Code's own 256 KiB: one past it is cut
// short, does not parse, and is skipped.
func TestLiveStatesReadProcessFilesUpTo_256KiB(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	big := sessionFileFields(801, "sess-big")
	big["name"] = strings.Repeat("n", maxSessionFileBytes)
	writeSessionFile(t, claudeDir, big)
	fits := sessionFileFields(802, "sess-fits")
	fits["name"] = strings.Repeat("n", maxSessionFileBytes-1024)
	writeSessionFile(t, claudeDir, fits)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	flushOnce(t, s)
	states := liveStatesOf(srv.requests())
	if len(states) != 1 || states[0].SessionID != "sess-fits" {
		t.Errorf("sent %d live states, want sess-fits alone", len(states))
	}
}

// A field past the server's cap would have the whole request item refused, so
// the state is not sent, the condition is logged once, and nothing is settled:
// a project named by a working directory too long for the server is sent once
// the session's transcript names it.
func TestLiveStateFieldsPastTheServersCapsAreNotSent(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	long := sessionFileFields(901, "sess-long-proc-start")
	long["procStart"] = strings.Repeat("p", maxObservationKeyBytes+1)
	writeSessionFile(t, claudeDir, long)
	source := sessionFileFields(902, "sess-long-name-source")
	source["nameSource"] = strings.Repeat("s", maxObservationTagBytes+1)
	writeSessionFile(t, claudeDir, source)
	deep := sessionFileFields(903, "sess-deep-cwd")
	deep["cwd"] = "/" + strings.Repeat("d/", maxObservationKeyBytes/2+1)
	writeSessionFile(t, claudeDir, deep)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	flushOnce(t, s)
	flushOnce(t, s)
	if states := liveStatesOf(srv.requests()); len(states) != 0 {
		t.Fatalf("sent %+v, want nothing past a cap", states)
	}
	for _, field := range []string{"a proc_start of", "a name_source of", "a project_hash of"} {
		if n := strings.Count(logs.String(), field); n != 1 {
			t.Errorf("%q logged %d times, want once:\n%s", field, n, logs.String())
		}
	}
	if len(s.obs.live.done) != 0 {
		t.Errorf("done %v: a state not sent was settled", s.obs.live.done)
	}

	transcriptDir := filepath.Join(claudeDir, "projects", "-deep-project")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, "sess-deep-cwd.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	flushOnce(t, s)
	if states := liveStatesOf(srv.requests()); len(states) != 1 || states[0].ProjectHash != "-deep-project" {
		t.Errorf("once the transcript is there: %+v, want the session under its transcript's project", states)
	}
}

// A session whose end goes in a flush is not reported alive in the same one:
// its process file can outlast the SessionEnd hook by a moment.
func TestLiveStateNotSentBesideItsSessionEnd(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	writeSessionFile(t, claudeDir, sessionFileFields(1001, "sess-ending"))
	writeSessionFile(t, claudeDir, sessionFileFields(1002, "sess-going-on"))
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())
	recordEnd(t, state, SessionEnd{SessionID: "sess-ending", Reason: "prompt_input_exit", EndedAt: time.Now(), CWD: "/tmp/live-repo", ClaudeHome: claudeDir})

	flushOnce(t, s)
	reqs := srv.requests()
	if len(reqs) != 1 || len(reqs[0].Events) != 1 || reqs[0].Events[0].SessionID != "sess-ending" {
		t.Fatalf("sent %s, want the session end", srv.bodies())
	}
	if len(reqs[0].LiveStates) != 1 || reqs[0].LiveStates[0].SessionID != "sess-going-on" {
		t.Errorf("live states %+v, want the other session's alone", reqs[0].LiveStates)
	}
}

// A live state carries the file's status, its waitingFor as a waiting kind, its
// name and where the name came from, its pid and procStart as found, and the time
// the file says the status changed; the heartbeat is when it was read.
func TestLiveStateCarriesWhatTheProcessFileSays(t *testing.T) {
	fakeClock(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	fields := sessionFileFields(201, "sess-waiting")
	fields["status"], fields["waitingFor"], fields["statusUpdatedAt"] = "waiting", "permission prompt", 1791208506977
	writeSessionFile(t, claudeDir, fields)
	s, state := newSnapshotSyncer(t, claudeDir, srv.URL())

	flushOnce(t, s)
	states := liveStatesOf(srv.requests())
	if len(states) != 1 {
		t.Fatalf("%d live states, want 1", len(states))
	}
	got := states[0]
	wantChanged := time.UnixMilli(1791208506977).UTC()
	if got.Status != "waiting" || got.StatusSource != "claude_process_file" || got.WaitingKind != "permission" || got.Name != "LIVE-NAME" || got.NameSource != "user" ||
		got.PID != 201 || got.ProcStart != "Sun Oct  4 15:26:48 2026" || got.Agent != "claude" || got.BillingProvider != "anthropic" {
		t.Errorf("live state %+v", got)
	}
	if got.StatusChangedAt == nil || !got.StatusChangedAt.Equal(wantChanged) {
		t.Errorf("status_changed_at = %v, want statusUpdatedAt %v", got.StatusChangedAt, wantChanged)
	}
	if !got.HeartbeatAt.Equal(nowFn()) {
		t.Errorf("heartbeat_at = %v, want the flush's time %v", got.HeartbeatAt, nowFn())
	}
	if !strings.Contains(srv.bodies()[0], `"status_changed_at":"2026-10-05T13:55:06.977Z"`) {
		t.Errorf("status_changed_at is not sent as RFC3339Nano: %s", srv.bodies()[0])
	}
	// A heartbeat needs no persistence: nothing of it is in the state file.
	if data, err := os.ReadFile(state.path); err != nil || strings.Contains(string(data), "sess-waiting") {
		t.Errorf("the state file holds the live state (err %v): %s", err, data)
	}
}

func TestWaitingKindMapsWaitingFor(t *testing.T) {
	for waitingFor, want := range map[string]string{
		"input needed": "input", "permission prompt": "permission", "dialog open": "dialog",
		"sandbox request": "sandbox", "worker request": "worker", "goal proposal": "goal",
		"something new": "other", "Input Needed": "other", "": "",
	} {
		if got := waitingKind(waitingFor); got != want {
			t.Errorf("waitingKind(%q) = %q, want %q", waitingFor, got, want)
		}
	}
}

// The project is the directory of the session's transcript, as its records
// carry it, when the transcript is there; the working directory otherwise. With
// neither there is no project, which the server requires, so nothing is sent.
func TestLiveStateProjectHash(t *testing.T) {
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	transcriptDir := filepath.Join(claudeDir, "projects", "-Users-me-Code-Repo")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, "sess-transcript.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeSessionFile(t, claudeDir, sessionFileFields(301, "sess-transcript"))
	writeSessionFile(t, claudeDir, sessionFileFields(302, "sess-cwd"))
	nowhere := sessionFileFields(303, "sess-nowhere")
	delete(nowhere, "cwd")
	writeSessionFile(t, claudeDir, nowhere)
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	flushOnce(t, s)
	got := map[string]string{}
	for _, st := range liveStatesOf(srv.requests()) {
		got[st.SessionID] = st.ProjectHash
	}
	want := map[string]string{"sess-transcript": "-Users-me-Code-Repo", "sess-cwd": "-tmp-live-repo"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("project hashes %v, want %v", got, want)
	}
}

// A live state is read under the policy in force when it goes out: the prompts
// policy hides a non-empty name, the tools policy does not, an empty name stays
// empty, and the state the flush remembers keeps the name it read.
func TestLiveStateNameRedactedUnderThePromptsPolicy(t *testing.T) {
	for _, policy := range []RedactPolicy{{}, {UserPrompts: true}, {ToolDetails: true}} {
		t.Run(fmt.Sprintf("%+v", policy), func(t *testing.T) {
			srv := newObservationServer(t)
			claudeDir := t.TempDir()
			writeSessionFile(t, claudeDir, sessionFileFields(401, "sess-named"))
			unnamed := sessionFileFields(402, "sess-unnamed")
			unnamed["name"] = ""
			writeSessionFile(t, claudeDir, unnamed)
			s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())
			s.client.SetRedactPolicy(policy)

			flushOnce(t, s)
			names := map[string]string{}
			for _, st := range liveStatesOf(srv.requests()) {
				names[st.SessionID] = st.Name
			}
			want := "LIVE-NAME"
			if policy.UserPrompts {
				want = redactedMarker
			}
			if names["sess-named"] != want || names["sess-unnamed"] != "" {
				t.Errorf("names %v, want sess-named %q and sess-unnamed empty", names, want)
			}
			for _, seen := range s.obs.live.seen {
				if seen.item.Name == redactedMarker {
					t.Error("redaction reached the remembered state")
				}
			}
		})
	}
}

// A live state the server refuses alone is logged once and not sent again by
// this process -- its gone neither, once its file disappears: resending what was
// refused cannot succeed.
func TestLiveStateRefusedAloneIsNotSentAgain(t *testing.T) {
	logs := captureSyncLog(t)
	srv := newObservationServer(t)
	refusedSends := 0
	srv.setAnswer(func(w http.ResponseWriter, req SessionObservationsRequest) bool {
		for _, st := range req.LiveStates {
			if st.SessionID == "sess-refused" {
				refusedSends++
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "live_states[0]: name_source is 70 bytes (max 64)"})
				return true
			}
		}
		return false
	})
	claudeDir := t.TempDir()
	path := writeSessionFile(t, claudeDir, sessionFileFields(601, "sess-refused"))
	writeSessionFile(t, claudeDir, sessionFileFields(602, "sess-fine"))
	s, _ := newSnapshotSyncer(t, claudeDir, srv.URL())

	if err := s.FlushObservations(context.Background()); err == nil || !strings.Contains(err.Error(), "refused 1 session observation") {
		t.Fatalf("flush err = %v, want the refusal counted", err)
	}
	refusedThen := refusedSends
	flushOnce(t, s)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	flushOnce(t, s)
	if refusedSends != refusedThen {
		t.Errorf("the refused state, or its gone, went %d more times after the refusal", refusedSends-refusedThen)
	}
	reqs := srv.requests()
	for _, req := range reqs[len(reqs)-2:] {
		if len(req.LiveStates) != 1 || req.LiveStates[0].SessionID != "sess-fine" {
			t.Errorf("a later flush carried %+v, want the other process's state alone", req.LiveStates)
		}
	}
	if n := strings.Count(logs.String(), "the server refused the live state of session sess-refused (pid 601)"); n != 1 {
		t.Errorf("refusal logged %d times, want once:\n%s", n, logs.String())
	}
}

// Neither a live state nor a session end of an excluded account leaves the
// machine, nor of a repository outside options.collect_repository_prefixes.
// The pending file of such a session end is consumed, as its records would be.
// While the exclusion query fails both are held: the live state is read again
// next time and the session end stays.
func TestLiveStatesAndSessionEndsFollowExclusionAndAllowlist(t *testing.T) {
	setup := func(t *testing.T, srv *observationServer, prefixes []string, local []string) (*Syncer, string) {
		t.Helper()
		claudeDir := t.TempDir()
		writeClaudeConfig(t, claudeDir, "acct-personal")
		fields := sessionFileFields(501, "sess-private")
		fields["cwd"] = t.TempDir() // no git repository: a local id, not on the allowlist
		writeSessionFile(t, claudeDir, fields)
		state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := state.Save(); err != nil {
			t.Fatal(err)
		}
		client := NewClient(srv.URL(), "", "")
		client.SetExcludedAccounts(local)
		s := New(claudeDir, "p@example.com", "u1", state, client, prefixes)
		mustSync(t, s) // observes the home's account
		// Another session than the live state's: one whose end goes in a flush is
		// not reported alive in it.
		pendingPath := recordEnd(t, state, SessionEnd{SessionID: "sess-private-ended", Reason: "logout",
			EndedAt: time.Now(), CWD: fields["cwd"].(string), ClaudeHome: claudeDir})
		return s, pendingPath
	}
	for _, tc := range []struct {
		name     string
		prefixes []string
		local    []string
		server   map[string]bool
	}{
		{"excluded locally", nil, []string{"anthropic:acct-personal"}, map[string]bool{}},
		{"excluded by the server", nil, nil, map[string]bool{"anthropic:acct-personal": true}},
		{"repository not on the allowlist", []string{"github.com/allowed/"}, nil, map[string]bool{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newObservationServer(t)
			srv.setExclusions(http.StatusOK, tc.server)
			s, pendingPath := setup(t, srv, tc.prefixes, tc.local)

			flushOnce(t, s)
			flushOnce(t, s)
			for _, body := range srv.bodies() {
				if strings.Contains(body, "sess-private") || strings.Contains(body, "LIVE-NAME") {
					t.Errorf("sent: %s", body)
				}
			}
			if len(srv.requests()) != 2 {
				t.Errorf("%d requests, want the two heartbeats", len(srv.requests()))
			}
			if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
				t.Errorf("the pending session end was not consumed: %v", err)
			}
		})
	}
	t.Run("exclusion query failing", func(t *testing.T) {
		srv := newObservationServer(t)
		srv.setExclusions(http.StatusInternalServerError, nil)
		s, pendingPath := setup(t, srv, nil, nil)

		if err := s.FlushObservations(context.Background()); err == nil {
			t.Error("held observations were not reported")
		}
		if states, reqs := liveStatesOf(srv.requests()), srv.requests(); len(states) != 0 || len(reqs) != 1 || len(reqs[0].Events) != 0 {
			t.Fatalf("while the query failed: %s, want the heartbeat alone", srv.bodies())
		}
		if _, err := os.Stat(pendingPath); err != nil {
			t.Fatalf("a held session end was removed: %v", err)
		}

		srv.setExclusions(http.StatusOK, map[string]bool{})
		flushOnce(t, s)
		last := srv.requests()[len(srv.requests())-1]
		if len(last.LiveStates) != 1 || len(last.Events) != 1 || last.LiveStates[0].AccountID != "acct-personal" || last.Events[0].AccountID != "acct-personal" {
			t.Errorf("once answered: %s, want both under acct-personal", srv.bodies()[len(srv.bodies())-1])
		}
	})
}

// Under an allowlist a lookup that settled nothing is not a verdict on the
// repository: its identity is a fallback. While git gives no answer for a
// session's directory its live state is not sent and its session end stays
// pending, rather than being consumed as a repository left out, which the log
// says once for the pending file (S8); both go once git answers with an
// allowed one.
func TestLiveStatesAndSessionEndsWaitWhileTheRepositoryIsUncertain(t *testing.T) {
	logs := captureSyncLog(t)
	repos := map[string]gitctx.Context{}
	stubRepos(t, repos)
	advance := useFakeClock(t)
	srv := newObservationServer(t)
	claudeDir := t.TempDir()
	fields := sessionFileFields(501, "sess-live")
	fields["cwd"] = "/work/c"
	writeSessionFile(t, claudeDir, fields)
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	s := New(claudeDir, "p@example.com", "u1", state, NewClient(srv.URL(), "", ""), allowExampleOrg)
	pendingPath := recordEnd(t, state, SessionEnd{SessionID: "sess-ended", Reason: "logout", EndedAt: nowFn(), CWD: "/work/c", ClaudeHome: claudeDir})
	// Dated by the test's clock, which the pending file's age is measured on.
	if err := os.Chtimes(pendingPath, nowFn(), nowFn()); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		// The second flush is past the retry interval: git is asked again, and
		// again gives no answer.
		if i > 0 {
			advance(lookupRetryInterval)
		}
		flushOnce(t, s)
		if states, events := liveStatesOf(srv.requests()), eventsOf(srv.requests()); len(states) != 0 || len(events) != 0 {
			t.Fatalf("while git gave no answer: %s, want the heartbeat alone", srv.bodies())
		}
		if _, err := os.Stat(pendingPath); err != nil {
			t.Fatalf("the session end was consumed on a lookup that settled nothing: %v", err)
		}
	}
	if n := strings.Count(logs.String(), pendingPath+" waits until the repository"); n != 1 {
		t.Errorf("the wait was logged %d times, want once:\n%s", n, logs.String())
	}

	repos["/work/c"] = repoAt(allowedRepo)
	advance(lookupRetryInterval)
	flushOnce(t, s)
	if states, events := liveStatesOf(srv.requests()), eventsOf(srv.requests()); len(states) != 1 || len(events) != 1 {
		t.Fatalf("once git answered: %s, want the live state and the session end", srv.bodies())
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Errorf("the sent session end is still pending: %v", err)
	}
}
