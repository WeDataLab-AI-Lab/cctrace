package processio

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQueueKeepsOnlyLatestScalarSnapshotAndAcknowledgesConditionally(t *testing.T) {
	t.Parallel()
	queue := newTestQueue(t)
	first := queueReport("session-1", time.Now().UTC().Add(-time.Hour))
	second := first
	second.Report.AsOf = first.Report.AsOf.Add(time.Second)
	second.Report.Counts.InputBytes++
	if err := queue.Upsert(first); err != nil {
		t.Fatal(err)
	}
	if err := queue.Upsert(second); err != nil {
		t.Fatal(err)
	}
	items, err := queue.List()
	if err != nil || len(items) != 1 || items[0].Report.AsOf != second.Report.AsOf || items[0].Report.Counts.InputBytes != second.Report.Counts.InputBytes {
		t.Fatalf("queue after update = %+v, %v", items, err)
	}
	if err := queue.Ack(first); err != nil {
		t.Fatal(err)
	}
	items, err = queue.List()
	if err != nil || len(items) != 1 {
		t.Fatalf("ack of superseded snapshot removed current row: %+v, %v", items, err)
	}
	data, err := os.ReadFile(queue.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "input prompt") || strings.Contains(string(data), "output reply") {
		t.Fatalf("queue retained stream content: %s", data)
	}
	if err := queue.Ack(second); err != nil {
		t.Fatal(err)
	}
	items, err = queue.List()
	if err != nil || len(items) != 0 {
		t.Fatalf("queue after current acknowledgement = %+v, %v", items, err)
	}
}

func TestQueueCapsEntriesWithoutEvictingPendingReports(t *testing.T) {
	t.Parallel()
	queue := newTestQueue(t)
	base := time.Now().UTC().Add(-time.Hour)
	// Seed the unrelated rows once; exercise both sides of the capacity boundary
	// through Upsert without 127 redundant atomic fsyncs in fixture construction.
	pending := make([]QueuedReport, 0, maxQueuedProcessIOReports-1)
	for i := 0; i < maxQueuedProcessIOReports-1; i++ {
		pending = append(pending, queueReport("session-"+strconv.Itoa(i), base.Add(time.Duration(i)*time.Second)))
	}
	if err := queue.withLock(func() error { return queue.writeLocked(pending) }); err != nil {
		t.Fatal(err)
	}
	if err := queue.Upsert(queueReport("session-"+strconv.Itoa(maxQueuedProcessIOReports-1), base.Add(time.Minute))); err != nil {
		t.Fatalf("last available slot: %v", err)
	}
	if err := queue.Upsert(queueReport("overflow", base.Add(time.Hour))); err == nil {
		t.Fatal("full queue silently evicted or accepted an extra report")
	}
	items, err := queue.List()
	if err != nil || len(items) != maxQueuedProcessIOReports {
		t.Fatalf("queue size = %d, err %v", len(items), err)
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		seen[item.SessionID] = true
	}
	for i := 0; i < maxQueuedProcessIOReports; i++ {
		if !seen["session-"+strconv.Itoa(i)] {
			t.Fatalf("pending report %d was lost", i)
		}
	}
}

func newTestQueue(t *testing.T) Queue {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return Queue{Path: filepath.Join(dir, "process-io.json")}
}

func queueReport(sessionID string, at time.Time) QueuedReport {
	stdoutBytes, stdoutLines := int64(9), int64(2)
	stderrBytes, stderrLines := int64(4), int64(1)
	return QueuedReport{
		ProfileEmail: "owner@example.test", UserID: "user-1", SessionID: sessionID, Agent: "codex", ProjectHash: "repo-hash", BillingProvider: "openai", AccountID: "acct-test",
		PID: 41, ProcStart: "darwin:100:0", Status: "unknown", StatusSource: "process_io_unknown", IOEpoch: "epoch-1",
		Report: Report{AsOf: at, Mode: ModePipes, Counts: Snapshot{
			InputBytes: 4, InputLines: 1, OutputBytes: 13, OutputLines: 3,
			StdoutBytes: &stdoutBytes, StdoutLines: &stdoutLines, StderrBytes: &stderrBytes, StderrLines: &stderrLines,
		}},
	}
}
