package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIngestSessionObservationsProcessIOMonotoneEpoch(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)
	first := sessionIOState(base.Add(time.Second), "proc-start-1", "epoch-1", 4, 1)
	if got, err := s.IngestSessionObservations(ctx, SessionObservationBatch{LiveStates: []*SessionLiveState{first}}); err != nil || got.Stored != 1 {
		t.Fatalf("first report = %+v, %v; want one stored row", got, err)
	}
	read := func() (int64, int64, time.Time) {
		t.Helper()
		var inputBytes, outputBytes int64
		var asOf time.Time
		if err := s.pool.QueryRow(ctx, `SELECT input_bytes,output_bytes,io_as_of FROM session_live_states
			WHERE session_id='s-process-io' AND pid=4242 AND proc_start='proc-start-1'`).Scan(&inputBytes, &outputBytes, &asOf); err != nil {
			t.Fatal(err)
		}
		return inputBytes, outputBytes, asOf
	}
	if got, err := s.IngestSessionObservations(ctx, SessionObservationBatch{LiveStates: []*SessionLiveState{
		sessionIOState(base, "proc-start-1", "epoch-1", 1, 0),
	}}); err != nil || got.Stored != 0 {
		t.Errorf("older retry = %+v, %v; want no stored row", got, err)
	}
	if input, output, at := read(); input != 4 || output != 10 || !at.Equal(first.IOAsOf.UTC()) {
		t.Errorf("older retry changed metrics to %d/%d at %s", input, output, at)
	}
	conflict := sessionIOState(first.HeartbeatAt, "proc-start-1", "epoch-1", 5, 1)
	if _, err := s.IngestSessionObservations(ctx, SessionObservationBatch{LiveStates: []*SessionLiveState{conflict}}); !errors.Is(err, ErrSessionIOConflict) {
		t.Errorf("same-time changed counters error = %v, want conflict", err)
	}
	epochChange := sessionIOState(base.Add(2*time.Second), "proc-start-1", "epoch-2", 5, 1)
	if _, err := s.IngestSessionObservations(ctx, SessionObservationBatch{LiveStates: []*SessionLiveState{epochChange}}); !errors.Is(err, ErrSessionIOEpochMismatch) {
		t.Errorf("same-process epoch change error = %v, want epoch mismatch", err)
	}
	regression := sessionIOState(base.Add(2*time.Second), "proc-start-1", "epoch-1", 3, 1)
	if _, err := s.IngestSessionObservations(ctx, SessionObservationBatch{LiveStates: []*SessionLiveState{regression}}); !errors.Is(err, ErrSessionIOCounterRegression) {
		t.Errorf("counter regression error = %v, want regression", err)
	}
	restart := sessionIOState(base.Add(3*time.Second), "proc-start-2", "epoch-2", 1, 0)
	if got, err := s.IngestSessionObservations(ctx, SessionObservationBatch{LiveStates: []*SessionLiveState{restart}}); err != nil || got.Stored != 1 {
		t.Fatalf("restarted process = %+v, %v; want a distinct row", got, err)
	}
	if got := countWhere(t, s, "session_live_states", "session_id='s-process-io' AND pid=4242"); got != 2 {
		t.Errorf("process instances = %d, want old and restarted epochs separately", got)
	}
}

func TestProcessIOUnknownStatusDoesNotCreateOrOverwriteTrustedBusyStatus(t *testing.T) {
	s := acquireTestStore(t)
	truncateTables(t, s)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)
	unknown := sessionIOState(base.Add(time.Second), "proc-start-1", "epoch-1", 1, 1)
	unknown.Status, unknown.StatusSource = "unknown", "process_io_unknown"
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{unknown}}); n != 1 {
		t.Fatalf("unknown process status stored %d rows, want one I/O instance", n)
	}
	read := func(procStart string) (string, string) {
		t.Helper()
		var status, source string
		if err := s.pool.QueryRow(ctx, `SELECT status,status_source FROM session_live_states
			WHERE session_id='s-process-io' AND pid=4242 AND proc_start=$1`, procStart).Scan(&status, &source); err != nil {
			t.Fatal(err)
		}
		return status, source
	}
	if status, source := read("proc-start-1"); status != "unknown" || source != "process_io_unknown" {
		t.Fatalf("I/O-only observation created status/source %q/%q, want unknown/process_io_unknown", status, source)
	}
	native := sessionIOState(base.Add(2*time.Second), "proc-start-1", "", 0, 0)
	native.Status, native.StatusSource = "idle", "claude_process_file"
	native.IOAsOf, native.IOEpoch, native.IOSource, native.IOMode = nil, "", "", ""
	native.InputBytes, native.InputLines, native.OutputBytes, native.OutputLines = nil, nil, nil, nil
	native.StdoutBytes, native.StdoutLines, native.StderrBytes, native.StderrLines = nil, nil, nil, nil
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{native}}); n != 1 {
		t.Fatalf("native idle state stored %d rows, want one", n)
	}
	if status, source := read("proc-start-1"); status != "idle" || source != "claude_process_file" {
		t.Fatalf("native status/source = %q/%q, want idle/claude_process_file", status, source)
	}
	nextIO := sessionIOState(base.Add(3*time.Second), "proc-start-1", "epoch-1", 2, 2)
	nextIO.Status, nextIO.StatusSource = "unknown", "process_io_unknown"
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{nextIO}}); n != 1 {
		t.Fatalf("I/O update stored %d rows, want one", n)
	}
	if status, source := read("proc-start-1"); status != "idle" || source != "claude_process_file" {
		t.Fatalf("I/O update overwrote native status/source: %q/%q", status, source)
	}
	final := sessionIOState(base.Add(4*time.Second), "proc-start-1", "epoch-1", 2, 2)
	final.Status, final.StatusSource = "gone", "process_io_lifecycle"
	final.StatusChangedAt = &final.HeartbeatAt
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{final}}); n != 1 {
		t.Fatalf("final process status stored %d rows, want one", n)
	}
	if status, source := read("proc-start-1"); status != "gone" || source != "process_io_lifecycle" {
		t.Fatalf("final status/source = %q/%q, want gone/process_io_lifecycle", status, source)
	}
	restarted := sessionIOState(base.Add(5*time.Second), "proc-start-2", "epoch-2", 0, 0)
	restarted.Status, restarted.StatusSource = "unknown", "process_io_unknown"
	if n := ingestObservations(t, s, SessionObservationBatch{LiveStates: []*SessionLiveState{restarted}}); n != 1 {
		t.Fatalf("restart stored %d rows, want one", n)
	}
	if status, source := read("proc-start-2"); status != "unknown" || source != "process_io_unknown" {
		t.Fatalf("restart status/source = %q/%q, want unknown/process_io_unknown", status, source)
	}
}

func sessionIOState(at time.Time, procStart, epoch string, inputBytes, inputLines int64) *SessionLiveState {
	outputBytes, outputLines := int64(10), int64(2)
	stdoutBytes, stdoutLines := int64(7), int64(1)
	stderrBytes, stderrLines := int64(3), int64(1)
	return &SessionLiveState{
		SessionID: "s-process-io", UserID: "u-1", ProfileEmail: "p@example.com", Agent: "codex",
		ProjectHash: "ph", BillingProvider: "openai", AccountID: "acct-1", PID: 4242, ProcStart: procStart,
		IdentityProvenance: "personal_token", Status: "busy", HeartbeatAt: at,
		IOEpoch: epoch, IOAsOf: &at, IOSource: "process_stream", IOMode: "pipes",
		InputBytes: &inputBytes, InputLines: &inputLines, OutputBytes: &outputBytes, OutputLines: &outputLines,
		StdoutBytes: &stdoutBytes, StdoutLines: &stdoutLines, StderrBytes: &stderrBytes, StderrLines: &stderrLines,
	}
}
