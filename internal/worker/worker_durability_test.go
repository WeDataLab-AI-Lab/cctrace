package worker

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"cctrace/internal/queue"
	"cctrace/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// redeliveringQueue models the pgmq delivery semantics that the fixed-slice
// mockQueue cannot: a message stays visible, and its read_ct keeps growing,
// until something archives it.
type redeliveringQueue struct {
	live  map[int64]*queue.Message
	polls int

	archived   []int64
	archivedTx []int64
}

func newRedeliveringQueue(msgs ...*queue.Message) *redeliveringQueue {
	q := &redeliveringQueue{live: make(map[int64]*queue.Message, len(msgs))}
	for _, m := range msgs {
		q.live[m.MsgID] = m
	}
	return q
}

// Delivery order is msg_id ascending. Ranging over the map directly would make
// any multi-message case depend on Go's randomized map iteration.
func (q *redeliveringQueue) Read(_ context.Context, _ string, _ int, _ int) ([]*queue.Message, error) {
	q.polls++
	out := make([]*queue.Message, 0, len(q.live))
	for _, m := range q.live {
		m.ReadCount++
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MsgID < out[j].MsgID })
	return out, nil
}

func (q *redeliveringQueue) Archive(_ context.Context, _ string, msgID int64) error {
	delete(q.live, msgID)
	q.archived = append(q.archived, msgID)
	return nil
}

func (q *redeliveringQueue) BeginTx(_ context.Context) (pgx.Tx, error) { return &mockTx{}, nil }

func (q *redeliveringQueue) ArchiveInTx(_ context.Context, _ pgx.Tx, _ string, msgID int64) error {
	delete(q.live, msgID)
	q.archivedTx = append(q.archivedTx, msgID)
	return nil
}

func newRedeliveryWorker(q *redeliveringQueue, s *mockStore) *Worker {
	return &Worker{queue: q, store: s, batchSize: 50, pollInterval: time.Second}
}

// maxDeliveries bounds how many times the worker may re-read a single message
// before we call it stuck. The queue needs a terminal state for every message;
// where exactly that state sits is a product decision, so this has to sit above
// any defensible dead-letter threshold. The point is to separate "gives up
// eventually" from "never gives up", not to pin the retry count — an assertion
// tight enough to reject a deliberate threshold would be rejecting a fix.
const maxDeliveries = 2000

var errInsertUnavailable = errors.New("insert unavailable")

// A message whose INSERT fails the same way on every attempt must reach a
// terminal state instead of cycling forever. Without a bound the queue never
// drains, the message is re-read every poll interval indefinitely, and each
// delivery logs an identical error.
//
// The failures below are deliberately not all constraint or data-type errors.
// A dead-letter path that keys off specific SQLSTATE classes leaves every other
// persistent failure — a column dropped by a bad migration, a permission
// change, an error that never reaches the driver as a *pgconn.PgError — in an
// unbounded retry loop.
func TestProcessLogs_PersistentlyFailingMessageReachesTerminalState(t *testing.T) {
	failures := map[string]error{
		"undefined_column (42703)":       &pgconn.PgError{Code: "42703", Message: "column does not exist"},
		"insufficient_privilege (42501)": &pgconn.PgError{Code: "42501", Message: "permission denied"},
		"check_violation (23514)":        &pgconn.PgError{Code: "23514", Message: "check constraint violated"},
		"non-postgres error":             errInsertUnavailable,
	}

	for name, insertErr := range failures {
		t.Run(name, func(t *testing.T) {
			q := newRedeliveringQueue(makeEventMsg(1, 0, store.OtelEvent{EventName: "stuck"}))
			w := newRedeliveryWorker(q, &mockStore{insertEventsErr: insertErr})

			for i := 0; i < maxDeliveries && len(q.live) > 0; i++ {
				w.processLogs(context.Background())
			}

			if len(q.live) > 0 {
				t.Fatalf("message still queued after %d deliveries (read_ct=%d, archived=%v): "+
					"no terminal state for this failure, so the worker retries it forever",
					q.polls, q.live[1].ReadCount, q.archived)
			}
			t.Logf("terminal after %d deliveries", q.polls)
		})
	}
}

// The same bound applies to the metrics queue.
func TestProcessMetrics_PersistentlyFailingMessageReachesTerminalState(t *testing.T) {
	q := newRedeliveringQueue(makeMetricMsg(1, 0, store.OtelMetric{MetricName: "stuck"}))
	w := newRedeliveryWorker(q, &mockStore{
		insertMetricsErr: &pgconn.PgError{Code: "42703", Message: "column does not exist"},
	})

	for i := 0; i < maxDeliveries && len(q.live) > 0; i++ {
		w.processMetrics(context.Background())
	}

	if len(q.live) > 0 {
		t.Fatalf("metric still queued after %d deliveries (read_ct=%d): retries forever",
			q.polls, q.live[1].ReadCount)
	}
}

// Counterweight to the tests above: they would also pass if the worker simply
// discarded anything that failed once. A recoverable failure must leave the
// message queued so a later poll can still land it.
func TestProcessLogs_RecoverableFailureKeepsMessageQueued(t *testing.T) {
	q := newRedeliveringQueue(makeEventMsg(1, 0, store.OtelEvent{EventName: "retryable"}))
	ms := &mockStore{insertEventsErr: errInsertUnavailable}
	w := newRedeliveryWorker(q, ms)

	w.processLogs(context.Background())
	if len(q.live) != 1 {
		t.Fatalf("message discarded after a single failure: archived=%v", q.archived)
	}

	ms.insertEventsErr = nil // the outage clears
	ms.insertedEvents = nil  // mockStore records attempts, including failed ones
	w.processLogs(context.Background())

	if len(q.live) != 0 {
		t.Fatal("message not archived after the insert succeeded")
	}
	if len(ms.insertedEvents) != 1 {
		t.Fatalf("inserted events on the successful attempt = %d, want 1", len(ms.insertedEvents))
	}
	if len(q.archived) != 0 {
		t.Errorf("a successful insert must archive inside the tx, not outside: %v", q.archived)
	}
	if len(q.archivedTx) != 1 || q.archivedTx[0] != 1 {
		t.Errorf("tx archives = %v, want [1]", q.archivedTx)
	}
}
