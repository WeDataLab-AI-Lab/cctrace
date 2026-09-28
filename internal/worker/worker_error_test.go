package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"cctrace/internal/queue"
	"cctrace/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// errQueue is a mock queueReader that supports per-call error injection.
type errQueue struct {
	readMsgs     []*queue.Message
	readErr      error
	archiveTxErr error
	beginTxErr   error
	tx           pgx.Tx
	archived     []int64
	archivedTx   []int64
}

func (m *errQueue) Read(_ context.Context, _ string, _ int, _ int) ([]*queue.Message, error) {
	return m.readMsgs, m.readErr
}
func (m *errQueue) Archive(_ context.Context, _ string, msgID int64) error {
	m.archived = append(m.archived, msgID)
	return nil
}
func (m *errQueue) BeginTx(_ context.Context) (pgx.Tx, error) {
	if m.beginTxErr != nil {
		return nil, m.beginTxErr
	}
	return m.tx, nil
}
func (m *errQueue) ArchiveInTx(_ context.Context, _ pgx.Tx, _ string, msgID int64) error {
	m.archivedTx = append(m.archivedTx, msgID)
	return m.archiveTxErr
}

// errStore supports returning errors from insert calls.
type errStore struct {
	insertEventsErr   error
	insertMetricsErr  error
	insertEventsFunc  func([]*store.OtelEvent) error
	insertMetricsFunc func([]*store.OtelMetric) error
}

func (m *errStore) InsertEventsTx(_ context.Context, _ pgx.Tx, events []*store.OtelEvent) error {
	if m.insertEventsFunc != nil {
		return m.insertEventsFunc(events)
	}
	return m.insertEventsErr
}
func (m *errStore) InsertMetricsTx(_ context.Context, _ pgx.Tx, metrics []*store.OtelMetric) error {
	if m.insertMetricsFunc != nil {
		return m.insertMetricsFunc(metrics)
	}
	return m.insertMetricsErr
}

// commitErrTx wraps mockTx and always fails on Commit.
type commitErrTx struct{ mockTx }

func (t *commitErrTx) Commit(_ context.Context) error { return errors.New("commit failed") }

// newErrWorker builds a Worker from the errQueue/errStore mocks.
func newErrWorker(q *errQueue, s *errStore) *Worker {
	return &Worker{
		queue:        q,
		store:        s,
		batchSize:    50,
		pollInterval: time.Second,
	}
}

// ---------------------------------------------------------------------------
// processLogs — error paths
// ---------------------------------------------------------------------------

func TestProcessLogs_ReadError(t *testing.T) {
	mq := &errQueue{readErr: errors.New("db gone")}
	ms := &errStore{}
	w := newErrWorker(mq, ms)

	w.processLogs(context.Background())

	if len(mq.archivedTx) != 0 {
		t.Errorf("expected no tx archives on read error, got %d", len(mq.archivedTx))
	}
	if len(mq.archived) != 0 {
		t.Errorf("expected no archives on read error, got %d", len(mq.archived))
	}
}

func TestProcessLogs_BeginTxError(t *testing.T) {
	mq := &errQueue{
		readMsgs:   []*queue.Message{makeEventMsg(1, 1, store.OtelEvent{EventName: "api_request"})},
		beginTxErr: errors.New("pool exhausted"),
		tx:         &mockTx{},
	}
	ms := &errStore{}
	w := newErrWorker(mq, ms)

	w.processLogs(context.Background())

	if len(mq.archivedTx) != 0 {
		t.Errorf("expected no tx archives when BeginTx fails, got %d", len(mq.archivedTx))
	}
}

func TestProcessLogs_InsertError(t *testing.T) {
	mq := &errQueue{
		readMsgs: []*queue.Message{makeEventMsg(1, 1, store.OtelEvent{EventName: "api_request"})},
		tx:       &mockTx{},
	}
	ms := &errStore{insertEventsErr: errors.New("insert failed")}
	w := newErrWorker(mq, ms)

	w.processLogs(context.Background())

	// Tx rolled back — nothing archived in tx.
	if len(mq.archivedTx) != 0 {
		t.Errorf("expected no tx archives when insert fails, got %d", len(mq.archivedTx))
	}
}

func TestProcessLogs_ArchivesPersistentNonRowPoisonPill(t *testing.T) {
	mq := &errQueue{
		readMsgs: []*queue.Message{makeEventMsg(99, poisonPillReadCount+1, store.OtelEvent{EventName: "poison"})},
		tx:       &mockTx{},
	}
	ms := &errStore{insertEventsErr: &pgconn.PgError{Code: "42703", Message: "undefined column"}}

	newErrWorker(mq, ms).processLogs(context.Background())

	if got, want := mq.archived, []int64{99}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("archived messages = %v, want %v", got, want)
	}
	if len(mq.archivedTx) != 0 {
		t.Fatalf("transaction archives = %v, want none", mq.archivedTx)
	}
}

func TestProcessLogs_InsertErrorIsolatesPoisonMessage(t *testing.T) {
	mq := &errQueue{
		readMsgs: []*queue.Message{
			makeEventMsg(1, 1, store.OtelEvent{EventName: "valid-left"}),
			makeEventMsg(2, 1, store.OtelEvent{EventName: "poison"}),
			makeEventMsg(3, 1, store.OtelEvent{EventName: "valid-right"}),
		},
		tx: &mockTx{},
	}
	ms := &errStore{
		insertEventsFunc: func(events []*store.OtelEvent) error {
			for _, event := range events {
				if event.EventName == "poison" {
					return &pgconn.PgError{Code: "23514", Message: "constraint violation"}
				}
			}
			return nil
		},
	}

	newErrWorker(mq, ms).processLogs(context.Background())

	if len(mq.archivedTx) != 2 || mq.archivedTx[0] != 1 || mq.archivedTx[1] != 3 {
		t.Fatalf("archived valid message IDs = %v, want [1 3]", mq.archivedTx)
	}
	if len(mq.archived) != 1 || mq.archived[0] != 2 {
		t.Fatalf("archived rejected message IDs = %v, want [2]", mq.archived)
	}
}

func TestProcessLogs_TransientBatchErrorDoesNotBisect(t *testing.T) {
	mq := &errQueue{
		readMsgs: []*queue.Message{
			makeEventMsg(1, 1, store.OtelEvent{EventName: "left"}),
			makeEventMsg(2, 1, store.OtelEvent{EventName: "right"}),
		},
		tx: &mockTx{},
	}
	insertCalls := 0
	ms := &errStore{
		insertEventsFunc: func(events []*store.OtelEvent) error {
			insertCalls++
			return errors.New("connection lost")
		},
	}

	newErrWorker(mq, ms).processLogs(context.Background())

	if insertCalls != 1 {
		t.Fatalf("insert calls = %d, want 1 for a transient batch failure", insertCalls)
	}
	if len(mq.archivedTx) != 0 {
		t.Fatalf("archived message IDs = %v, want none", mq.archivedTx)
	}
}

func TestProcessLogs_ArchiveInTxError(t *testing.T) {
	mq := &errQueue{
		readMsgs: []*queue.Message{
			makeEventMsg(1, 1, store.OtelEvent{EventName: "api_request"}),
			makeEventMsg(2, 1, store.OtelEvent{EventName: "tool_result"}),
		},
		archiveTxErr: errors.New("archive tx failed"),
		tx:           &mockTx{},
	}
	ms := &errStore{}
	w := newErrWorker(mq, ms)

	w.processLogs(context.Background())

	// Returns on first ArchiveInTx error.
	if len(mq.archivedTx) != 1 {
		t.Errorf("expected 1 attempted tx archive before early return, got %d", len(mq.archivedTx))
	}
}

func TestProcessLogs_CommitError(t *testing.T) {
	mq := &errQueue{
		readMsgs: []*queue.Message{makeEventMsg(1, 1, store.OtelEvent{EventName: "api_request"})},
		tx:       &commitErrTx{},
	}
	ms := &errStore{}
	w := newErrWorker(mq, ms)

	w.processLogs(context.Background())

	// ArchiveInTx was attempted but commit failed — tx rolled back.
	if len(mq.archivedTx) != 1 {
		t.Errorf("expected 1 tx archive attempt before failed commit, got %d", len(mq.archivedTx))
	}
}

// ---------------------------------------------------------------------------
// processMetrics — error paths
// ---------------------------------------------------------------------------

func TestProcessMetrics_ReadError(t *testing.T) {
	mq := &errQueue{readErr: errors.New("network error")}
	ms := &errStore{}
	w := newErrWorker(mq, ms)

	w.processMetrics(context.Background())

	if len(mq.archivedTx) != 0 {
		t.Errorf("expected no tx archives on read error, got %d", len(mq.archivedTx))
	}
}

func TestProcessMetrics_BeginTxError(t *testing.T) {
	v := 1.0
	mq := &errQueue{
		readMsgs:   []*queue.Message{makeMetricMsg(1, 1, store.OtelMetric{MetricName: "cost", ValueDouble: &v})},
		beginTxErr: errors.New("pool exhausted"),
		tx:         &mockTx{},
	}
	ms := &errStore{}
	w := newErrWorker(mq, ms)

	w.processMetrics(context.Background())

	if len(mq.archivedTx) != 0 {
		t.Errorf("expected no tx archives when BeginTx fails, got %d", len(mq.archivedTx))
	}
}

func TestProcessMetrics_InsertError(t *testing.T) {
	v := 1.0
	mq := &errQueue{
		readMsgs: []*queue.Message{makeMetricMsg(1, 1, store.OtelMetric{MetricName: "cost", ValueDouble: &v})},
		tx:       &mockTx{},
	}
	ms := &errStore{insertMetricsErr: errors.New("constraint violation")}
	w := newErrWorker(mq, ms)

	w.processMetrics(context.Background())

	if len(mq.archivedTx) != 0 {
		t.Errorf("expected no tx archives when insert fails, got %d", len(mq.archivedTx))
	}
}

func TestProcessMetrics_InsertErrorIsolatesPoisonMessage(t *testing.T) {
	value := 1.0
	mq := &errQueue{
		readMsgs: []*queue.Message{
			makeMetricMsg(1, 1, store.OtelMetric{MetricName: "valid-left", ValueDouble: &value}),
			makeMetricMsg(2, 1, store.OtelMetric{MetricName: "poison", ValueDouble: &value}),
			makeMetricMsg(3, 1, store.OtelMetric{MetricName: "valid-right", ValueDouble: &value}),
		},
		tx: &mockTx{},
	}
	ms := &errStore{
		insertMetricsFunc: func(metrics []*store.OtelMetric) error {
			for _, metric := range metrics {
				if metric.MetricName == "poison" {
					return &pgconn.PgError{Code: "23514", Message: "constraint violation"}
				}
			}
			return nil
		},
	}

	newErrWorker(mq, ms).processMetrics(context.Background())

	if len(mq.archivedTx) != 2 || mq.archivedTx[0] != 1 || mq.archivedTx[1] != 3 {
		t.Fatalf("archived valid message IDs = %v, want [1 3]", mq.archivedTx)
	}
	if len(mq.archived) != 1 || mq.archived[0] != 2 {
		t.Fatalf("archived rejected message IDs = %v, want [2]", mq.archived)
	}
}

func TestProcessMetrics_ArchiveInTxError(t *testing.T) {
	v := 1.0
	mq := &errQueue{
		readMsgs: []*queue.Message{
			makeMetricMsg(10, 1, store.OtelMetric{MetricName: "latency", ValueDouble: &v}),
			makeMetricMsg(11, 1, store.OtelMetric{MetricName: "cost", ValueDouble: &v}),
		},
		archiveTxErr: errors.New("archive failed"),
		tx:           &mockTx{},
	}
	ms := &errStore{}
	w := newErrWorker(mq, ms)

	w.processMetrics(context.Background())

	if len(mq.archivedTx) != 1 {
		t.Errorf("expected 1 attempted tx archive before early return, got %d", len(mq.archivedTx))
	}
}

func TestProcessMetrics_CommitError(t *testing.T) {
	v := 1.0
	mq := &errQueue{
		readMsgs: []*queue.Message{makeMetricMsg(1, 1, store.OtelMetric{MetricName: "cost", ValueDouble: &v})},
		tx:       &commitErrTx{},
	}
	ms := &errStore{}
	w := newErrWorker(mq, ms)

	w.processMetrics(context.Background())

	if len(mq.archivedTx) != 1 {
		t.Errorf("expected 1 tx archive attempt before failed commit, got %d", len(mq.archivedTx))
	}
}
