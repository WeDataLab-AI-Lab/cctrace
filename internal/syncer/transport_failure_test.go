package syncer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A daemon that cannot send anything has to leave that fact somewhere a later
// process can read: sync.log rotates, and in #712 six rotations of it were the
// only record that two days of collection had failed. The state file already
// carries BlockedByBodyLimit and RulesDenied for the same reason.
func TestSyncOnceRecordsTransportFailureWhenNothingGotThrough(t *testing.T) {
	srv, _ := countingServer(t, http.StatusInternalServerError)
	s := newPassSyncer(t, srv.URL, "s1.jsonl")

	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	rec := s.state.TransportFailure
	if rec == nil {
		t.Fatal("TransportFailure = nil, want a record of the failed pass")
	}
	if rec.Class != TransportFailureClassTransport {
		t.Errorf("Class = %q, want %q", rec.Class, TransportFailureClassTransport)
	}
	if rec.FilesFailed != 1 {
		t.Errorf("FilesFailed = %d, want 1", rec.FilesFailed)
	}
	if rec.LastError == "" {
		t.Error("LastError is empty, want the failure the pass ended on")
	}
	if rec.FirstFailedAt.IsZero() || rec.LastFailedAt.IsZero() {
		t.Errorf("timestamps = %v / %v, want both set", rec.FirstFailedAt, rec.LastFailedAt)
	}

	// Survives a restart: the process that reads this is usually not the one that
	// wrote it.
	reloaded, err := LoadState(s.state.path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if reloaded.TransportFailure == nil {
		t.Fatal("TransportFailure did not survive a reload")
	}
}

// A pass that sent something proves the path to the server is open. Recording a
// stall here would report a dead daemon over one held oversized session -- the
// false alarm this repository treats as worse than silence.
func TestSyncOnceDoesNotRecordTransportFailureOnPartialSuccess(t *testing.T) {
	s := newMixedPassSyncer(t, "bad.jsonl", "good.jsonl")
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if got := s.state.TransportFailure; got != nil {
		t.Fatalf("TransportFailure = %+v, want nil while records are getting through", got)
	}
}

// The record has to disappear on its own once sending works, or it becomes a
// warning about a problem that is over.
func TestSyncOnceClearsTransportFailureAfterASendingPass(t *testing.T) {
	srv, _ := countingServer(t, http.StatusInternalServerError)
	s := newPassSyncer(t, srv.URL, "s1.jsonl")
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("failing SyncOnce: %v", err)
	}
	if s.state.TransportFailure == nil {
		t.Fatal("no record after the failing pass; nothing to clear")
	}

	var payloads []SyncPayload
	ok := collectPayloads(t, &payloads)
	defer ok.Close()
	s.client = NewClient(ok.URL, "", "")

	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("recovered SyncOnce: %v", err)
	}
	if got := s.state.TransportFailure; got != nil {
		t.Fatalf("TransportFailure = %+v, want nil after a pass that sent records", got)
	}
	reloaded, err := LoadState(s.state.path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if reloaded.TransportFailure != nil {
		t.Error("cleared record came back from the state file")
	}
}

// 413 and 429 are refusals, not a broken path: the server answered. Restarting
// the daemon cannot fix either one, so they are recorded under a class that the
// stall exit refuses to act on.
func TestSyncOnceClassifiesServerRefusalsSeparately(t *testing.T) {
	srv, _ := countingServer(t, http.StatusTooManyRequests)
	s := newPassSyncer(t, srv.URL, "s1.jsonl")
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	rec := s.state.TransportFailure
	if rec == nil {
		t.Fatal("TransportFailure = nil, want a record for a 429 pass")
	}
	if rec.Class != TransportFailureClassServer {
		t.Errorf("Class = %q, want %q", rec.Class, TransportFailureClassServer)
	}
}

// The state file is about a megabyte on a working machine and the installed hook
// polls once a second. Rewriting it on every failing pass would turn a stalled
// daemon into a disk writer, so a continuing failure refreshes the record on an
// interval, not on every pass.
func TestContinuingFailureRefreshesRecordOnAnInterval(t *testing.T) {
	advance := useFakeClock(t)
	srv, _ := countingServer(t, http.StatusInternalServerError)
	s := newPassSyncer(t, srv.URL, "s1.jsonl")

	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	first := s.state.TransportFailure.LastFailedAt

	// Same failure moments later: the file must not be rewritten.
	advance(time.Minute)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	persisted, err := LoadState(s.state.path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !persisted.TransportFailure.LastFailedAt.Equal(first) {
		t.Errorf("LastFailedAt on disk = %v, want %v (no rewrite within the interval)",
			persisted.TransportFailure.LastFailedAt, first)
	}

	// Past the interval the record has to move, or a stall that outlives the
	// interval would look like it ended.
	advance(transportFailureRefresh + time.Minute)
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("third SyncOnce: %v", err)
	}
	persisted, err = LoadState(s.state.path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !persisted.TransportFailure.LastFailedAt.After(first) {
		t.Errorf("LastFailedAt on disk = %v, want later than %v", persisted.TransportFailure.LastFailedAt, first)
	}
	if !persisted.TransportFailure.FirstFailedAt.Equal(first) {
		t.Errorf("FirstFailedAt = %v, want the unchanged %v", persisted.TransportFailure.FirstFailedAt, first)
	}
}

// The notice a person reads says when collection last worked. That answer lives
// in the per-file offsets the syncer already keeps.
func TestTransportFailureCarriesTheLastSuccessfulSend(t *testing.T) {
	var payloads []SyncPayload
	ok := collectPayloads(t, &payloads)
	defer ok.Close()
	s := newPassSyncer(t, ok.URL, "s1.jsonl")
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sending SyncOnce: %v", err)
	}

	srv, _ := countingServer(t, http.StatusInternalServerError)
	s.client = NewClient(srv.URL, "", "")
	appendSessionLine(t, s, "s1.jsonl")
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("failing SyncOnce: %v", err)
	}

	rec := s.state.TransportFailure
	if rec == nil {
		t.Fatal("TransportFailure = nil")
	}
	if rec.LastSuccessAt.IsZero() {
		t.Error("LastSuccessAt is zero, want the time of the pass that worked")
	}
}

// A session file this process cannot read is a local problem: nothing was sent,
// so nothing says the path to the server is down. Counting it made one
// unreadable file stall -- and back off -- collection for the whole machine.
func TestLocalScanFailureIsNotATransportStall(t *testing.T) {
	var payloads []SyncPayload
	ok := collectPayloads(t, &payloads)
	defer ok.Close()
	s := newPassSyncer(t, ok.URL, "locked.jsonl")
	path := filepath.Join(s.claudeDir, "projects", "-tmp", "locked.jsonl")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	if f, err := os.Open(path); err == nil {
		f.Close()
		t.Skip("running with privileges that ignore file modes")
	}

	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if got := s.LastPass().FilesFailed; got != 0 {
		t.Errorf("FilesFailed = %d, want 0 for a local read failure", got)
	}
	if s.state.TransportFailure != nil {
		t.Errorf("TransportFailure = %+v, want nil for a local read failure", s.state.TransportFailure)
	}
}

// A session held at the body limit already has its own notice and its own
// remedy, scoped to that file. Recording it as a stall reported "the server is
// refusing transfers" for a machine whose every other session was collecting.
func TestBodyLimitHoldIsNotATransportStall(t *testing.T) {
	srv, _ := limitedSyncServer(t, 10)
	s := newPassSyncer(t, srv.URL, "big.jsonl")
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if s.state.TransportFailure != nil {
		t.Errorf("TransportFailure = %+v, want nil for a body-limit hold", s.state.TransportFailure)
	}
}

// When one file is rate-limited and another fails differently in the same pass,
// the refusal decides the class. Otherwise whichever file the directory walk
// reached last would, and a 429 could be filed as transport -- exactly what the
// stall exit refuses to act on.
func TestRefusalAnywhereInThePassMakesItAServerStall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "limited") {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Both orders, so the result cannot depend on which file is walked last.
	for _, names := range [][]string{{"a-limited.jsonl", "z-broken.jsonl"}, {"a-broken.jsonl", "z-limited.jsonl"}} {
		s := newPassSyncer(t, srv.URL, names...)
		if _, err := s.SyncOnce(context.Background()); err != nil {
			t.Fatalf("SyncOnce: %v", err)
		}
		rec := s.state.TransportFailure
		if rec == nil || rec.Class != TransportFailureClassServer {
			t.Errorf("%v: record = %+v, want class %q", names, rec, TransportFailureClassServer)
		}
	}
}

// A gap far longer than the refresh interval means nobody was trying -- the
// machine slept, or no daemon ran. Carrying FirstFailedAt across it told a
// person the daemon had been failing for eight hours when it had been awake for
// thirty seconds.
func TestFailureAfterALongGapStartsANewStretch(t *testing.T) {
	st := &State{}
	t0 := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	st.NoteTransportFailure(t0, TransportFailureClassTransport, 1, "boom")

	later := t0.Add(8 * time.Hour)
	if !st.NoteTransportFailure(later, TransportFailureClassTransport, 1, "boom") {
		t.Fatal("a failure after a long gap was not written")
	}
	if !st.TransportFailure.FirstFailedAt.Equal(later) {
		t.Errorf("FirstFailedAt = %v, want the new stretch to start at %v", st.TransportFailure.FirstFailedAt, later)
	}
}

// A class change is written at once. The stall exit reads the class from
// memory, but `cctrace status` runs in another process and reads only the file:
// a throttled change would show the transport remedy ("restart the daemon") for
// a 429 -- the one case where restarting does harm -- for up to an interval.
func TestClassChangeIsWrittenAtOnce(t *testing.T) {
	st := &State{}
	t0 := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	st.NoteTransportFailure(t0, TransportFailureClassTransport, 1, "dial")

	if !st.NoteTransportFailure(t0.Add(time.Minute), TransportFailureClassServer, 1, "429") {
		t.Error("a class change inside the interval was not written")
	}
	if st.TransportFailure.Class != TransportFailureClassServer {
		t.Errorf("class = %q, want %q", st.TransportFailure.Class, TransportFailureClassServer)
	}
	// The same class again inside the interval is still throttled.
	if st.NoteTransportFailure(t0.Add(2*time.Minute), TransportFailureClassServer, 1, "429") {
		t.Error("an unchanged class inside the interval asked for a rewrite")
	}
}

// A rate-limited pass carries how long the server asked the client to stay
// away. Per-file 429s never reach SyncOnce's error, so without this the watch
// loop backed off on its own schedule -- five seconds after "Retry-After: 600".
// The longest wait any file was given wins.
func TestRateLimitedPassCarriesTheRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "long") {
			w.Header().Set("Retry-After", "600")
		} else {
			w.Header().Set("Retry-After", "30")
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	s := newPassSyncer(t, srv.URL, "a-long.jsonl", "b-short.jsonl")
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if got := s.LastPass().RetryAfter; got != 600*time.Second {
		t.Errorf("RetryAfter = %s, want 10m0s (the longest wait asked for)", got)
	}
}
