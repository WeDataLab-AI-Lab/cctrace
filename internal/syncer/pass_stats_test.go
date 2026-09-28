package syncer

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newPassSyncer builds a Syncer over a fresh Claude home holding the named
// session files, with every one of them queued from offset 0.
//
// The state is saved before the pass so IsNew() is already false: the first-run
// window skips existing files to EOF, which would make every pass below a quiet
// one and hide exactly the distinction under test.
func newPassSyncer(t *testing.T, endpoint string, names ...string) *Syncer {
	t.Helper()
	claudeDir := t.TempDir()
	sessionDir := filepath.Join(claudeDir, "projects", "-tmp")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	state, err := LoadState(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, name := range names {
		sessionID := strings.TrimSuffix(name, ".jsonl")
		state.SetOffset(writeSessionAt(t, sessionDir, name, sessionID, time.Now()), 0)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save state: %v", err)
	}
	return New(claudeDir, "p@example.com", "u1", state, NewClient(endpoint, "", ""), nil)
}

// newMixedPassSyncer refuses only the records whose session id contains "bad",
// so one file fails while another gets through in the same pass.
func newMixedPassSyncer(t *testing.T, names ...string) *Syncer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "bad") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"inserted":1}`))
	}))
	t.Cleanup(srv.Close)
	return newPassSyncer(t, srv.URL, names...)
}

// appendSessionLine adds one more record to a session file the syncer already
// knows, giving the next pass something to attempt.
func appendSessionLine(t *testing.T, s *Syncer, name string) {
	t.Helper()
	path := filepath.Join(s.claudeDir, "projects", "-tmp", name)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open session for append: %v", err)
	}
	defer f.Close()
	line := `{"type":"user","timestamp":"` + nowFn().UTC().Format(time.RFC3339) +
		`","sessionId":"` + strings.TrimSuffix(name, ".jsonl") + `","uuid":"` +
		strings.TrimSuffix(name, ".jsonl") + `-u2","cwd":"/tmp",` +
		`"message":{"role":"user","content":"again"}}` + "\n"
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("append session line: %v", err)
	}
}

// A pass in which every file failed must not look like a quiet one.
//
// It did for two days (#712): SyncOnce logs each file's error and continues, so
// a pass that sent nothing at all still returned (0, nil) -- the same value an
// idle pass returns. The watch loop read that as success, reset its backoff and
// printed its cumulative total, and nothing anywhere said that no byte had left
// the machine. The error contract stays as it is: a single bad file must not
// abort the pass, and the non-watch caller turns an error into the SessionEnd
// hook's exit code. The failure count is what travels instead.
func TestSyncOnceReportsEveryFailedFile(t *testing.T) {
	srv, _ := countingServer(t, http.StatusInternalServerError)
	s := newPassSyncer(t, srv.URL, "s1.jsonl", "s2.jsonl")

	n, err := s.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce error = %v, want nil (per-file failures must not abort the pass)", err)
	}
	if n != 0 {
		t.Fatalf("records sent = %d, want 0", n)
	}

	stats := s.LastPass()
	if stats.FilesFailed != 2 {
		t.Errorf("FilesFailed = %d, want 2", stats.FilesFailed)
	}
	if stats.Sent != 0 {
		t.Errorf("Sent = %d, want 0", stats.Sent)
	}
	if stats.LastErr == nil {
		t.Error("LastErr = nil, want the failure that ended the last file")
	}
}

// An idle pass is the case a stall report must never fire on: a machine whose
// user is away sends nothing and that is correct. Nothing was attempted, so
// nothing failed.
func TestSyncOnceQuietPassReportsNoFailure(t *testing.T) {
	var payloads []SyncPayload
	srv := collectPayloads(t, &payloads)
	defer srv.Close()

	s := newPassSyncer(t, srv.URL, "s1.jsonl")
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	if got := s.LastPass(); got.Sent == 0 {
		t.Fatalf("first pass sent nothing; the second pass would not be quiet for the right reason")
	}

	// Second pass: the file is at EOF and no new bytes arrived.
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	stats := s.LastPass()
	if stats.FilesFailed != 0 {
		t.Errorf("FilesFailed = %d, want 0", stats.FilesFailed)
	}
	if stats.Sent != 0 {
		t.Errorf("Sent = %d, want 0 on a pass with no new bytes", stats.Sent)
	}
}

// One failing file among several that got through is not a transport stall: the
// path to the server is demonstrably open. Keeping these two apart is what stops
// a held oversized session from being reported as a dead daemon.
func TestSyncOnceMixedPassStillReportsWhatGotThrough(t *testing.T) {
	s := newMixedPassSyncer(t, "bad.jsonl", "good.jsonl")
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	stats := s.LastPass()
	if stats.FilesFailed != 1 {
		t.Errorf("FilesFailed = %d, want 1", stats.FilesFailed)
	}
	if stats.Sent == 0 {
		t.Error("Sent = 0, want the records of the file the server accepted")
	}
}

// captureSyncLog redirects the package logger for one test.
func captureSyncLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previousOut := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousOut)
		log.SetFlags(previousFlags)
	})
	return &buf
}

// The end-of-pass line is the one place sync.log can be read afterwards and
// still say whether anything got through. It has to carry the failure count, and
// it must not repeat itself once per second while nothing changes.
func TestPassSummaryLogsFailuresAndSuppressesRepeats(t *testing.T) {
	buf := captureSyncLog(t)
	srv, _ := countingServer(t, http.StatusInternalServerError)
	s := newPassSyncer(t, srv.URL, "s1.jsonl")

	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	first := buf.String()
	if !strings.Contains(first, "pass files=1 synced=0 failed=1") {
		t.Fatalf("pass summary missing from log: %q", first)
	}

	buf.Reset()
	if _, err := s.SyncOnce(context.Background()); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	if got := buf.String(); strings.Contains(got, "pass files=") {
		t.Errorf("unchanged summary repeated: %q", got)
	}
}
