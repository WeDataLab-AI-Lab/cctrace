package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cctrace/internal/queue"
	"cctrace/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------------------------------------------------------------------------
// Mock pgx.Tx
// ---------------------------------------------------------------------------

type mockTx struct {
	commitErr   error
	rollbackErr error
}

func (m *mockTx) Begin(ctx context.Context) (pgx.Tx, error) { return m, nil }
func (m *mockTx) Commit(ctx context.Context) error          { return m.commitErr }
func (m *mockTx) Rollback(ctx context.Context) error        { return m.rollbackErr }
func (m *mockTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag(""), nil
}
func (m *mockTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, nil
}
func (m *mockTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row { return nil }
func (m *mockTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (m *mockTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { return nil }
func (m *mockTx) LargeObjects() pgx.LargeObjects                               { return pgx.LargeObjects{} }
func (m *mockTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (m *mockTx) Conn() *pgx.Conn { return nil }

// ---------------------------------------------------------------------------
// Mock queueReader
// ---------------------------------------------------------------------------

type mockQueue struct {
	readMsgs   []*queue.Message
	readErr    error
	archived   []int64 // msg IDs archived outside tx
	archivedTx []int64 // msg IDs archived inside tx
	archiveErr error
	beginTxErr error
	tx         pgx.Tx
}

func (m *mockQueue) Read(ctx context.Context, queueName string, limit int, vtSeconds int) ([]*queue.Message, error) {
	return m.readMsgs, m.readErr
}

func (m *mockQueue) Archive(ctx context.Context, queueName string, msgID int64) error {
	m.archived = append(m.archived, msgID)
	return m.archiveErr
}

func (m *mockQueue) BeginTx(ctx context.Context) (pgx.Tx, error) {
	if m.beginTxErr != nil {
		return nil, m.beginTxErr
	}
	return m.tx, nil
}

func (m *mockQueue) ArchiveInTx(ctx context.Context, tx pgx.Tx, queueName string, msgID int64) error {
	m.archivedTx = append(m.archivedTx, msgID)
	return nil
}

// ---------------------------------------------------------------------------
// Mock eventInserter
// ---------------------------------------------------------------------------

type mockStore struct {
	insertedEvents   []*store.OtelEvent
	insertedMetrics  []*store.OtelMetric
	insertEventsErr  error
	insertMetricsErr error
}

func (m *mockStore) InsertEventsTx(ctx context.Context, tx pgx.Tx, events []*store.OtelEvent) error {
	m.insertedEvents = append(m.insertedEvents, events...)
	return m.insertEventsErr
}

func (m *mockStore) InsertMetricsTx(ctx context.Context, tx pgx.Tx, metrics []*store.OtelMetric) error {
	m.insertedMetrics = append(m.insertedMetrics, metrics...)
	return m.insertMetricsErr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeEventMsg(id int64, readCount int32, event store.OtelEvent) *queue.Message {
	data, _ := json.Marshal(event)
	return &queue.Message{
		MsgID:     id,
		ReadCount: readCount,
		Message:   data,
	}
}

func makeMetricMsg(id int64, readCount int32, metric store.OtelMetric) *queue.Message {
	data, _ := json.Marshal(metric)
	return &queue.Message{
		MsgID:     id,
		ReadCount: readCount,
		Message:   data,
	}
}

func newTestWorker(q *mockQueue, s *mockStore) *Worker {
	return &Worker{
		queue:        q,
		store:        s,
		batchSize:    50,
		pollInterval: time.Second,
	}
}

// ---------------------------------------------------------------------------
// Tests: processLogs
// ---------------------------------------------------------------------------

func TestProcessLogs_Normal(t *testing.T) {
	tx := &mockTx{}
	mq := &mockQueue{
		readMsgs: []*queue.Message{
			makeEventMsg(1, 1, store.OtelEvent{EventName: "api_request", SessionID: "s1"}),
			makeEventMsg(2, 1, store.OtelEvent{EventName: "tool_decision", SessionID: "s1"}),
		},
		tx: tx,
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processLogs(context.Background())

	if len(ms.insertedEvents) != 2 {
		t.Fatalf("expected 2 inserted events, got %d", len(ms.insertedEvents))
	}
	if ms.insertedEvents[0].EventName != "api_request" {
		t.Errorf("expected event_name api_request, got %s", ms.insertedEvents[0].EventName)
	}
	if len(mq.archivedTx) != 2 {
		t.Fatalf("expected 2 archived-in-tx, got %d", len(mq.archivedTx))
	}
	if mq.archivedTx[0] != 1 || mq.archivedTx[1] != 2 {
		t.Errorf("expected archived msg IDs [1,2], got %v", mq.archivedTx)
	}
	// Poison-pill archive (outside tx) should not be called
	if len(mq.archived) != 0 {
		t.Errorf("expected 0 out-of-tx archives, got %d", len(mq.archived))
	}
}

func TestProcessLogs_HighReadCountStillProcessesValidMessage(t *testing.T) {
	mq := &mockQueue{
		readMsgs: []*queue.Message{
			{MsgID: 10, ReadCount: 6, Message: json.RawMessage(`{"event_name":"bad"}`)},
		},
		tx: &mockTx{},
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processLogs(context.Background())

	if len(mq.archived) != 0 {
		t.Fatalf("out-of-tx archives = %v, want none", mq.archived)
	}
	if len(ms.insertedEvents) != 1 {
		t.Errorf("inserted events = %d, want 1", len(ms.insertedEvents))
	}
	if len(mq.archivedTx) != 1 || mq.archivedTx[0] != 10 {
		t.Errorf("tx archives = %v, want [10]", mq.archivedTx)
	}
}

func TestProcessLogs_BadJSON(t *testing.T) {
	tx := &mockTx{}
	mq := &mockQueue{
		readMsgs: []*queue.Message{
			{MsgID: 5, ReadCount: 1, Message: json.RawMessage(`not valid json`)},
			makeEventMsg(6, 1, store.OtelEvent{EventName: "api_request", SessionID: "s2"}),
		},
		tx: tx,
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processLogs(context.Background())

	// Bad message archived outside tx
	if len(mq.archived) != 1 || mq.archived[0] != 5 {
		t.Fatalf("expected bad msg 5 archived, got %v", mq.archived)
	}
	// Good message inserted and archived in tx
	if len(ms.insertedEvents) != 1 {
		t.Fatalf("expected 1 inserted event, got %d", len(ms.insertedEvents))
	}
	if ms.insertedEvents[0].EventName != "api_request" {
		t.Errorf("expected event_name api_request, got %s", ms.insertedEvents[0].EventName)
	}
	if len(mq.archivedTx) != 1 || mq.archivedTx[0] != 6 {
		t.Errorf("expected msg 6 archived in tx, got %v", mq.archivedTx)
	}
}

func TestProcessLogs_EmptyQueue(t *testing.T) {
	mq := &mockQueue{
		readMsgs: []*queue.Message{},
		tx:       &mockTx{},
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processLogs(context.Background())

	if len(ms.insertedEvents) != 0 {
		t.Errorf("expected 0 inserted events, got %d", len(ms.insertedEvents))
	}
	if len(mq.archived) != 0 {
		t.Errorf("expected 0 archives, got %d", len(mq.archived))
	}
}

func TestProcessLogs_HighReadCountsDoNotDiscardValidBatch(t *testing.T) {
	mq := &mockQueue{
		readMsgs: []*queue.Message{
			{MsgID: 20, ReadCount: 10, Message: json.RawMessage(`{"event_name":"x"}`)},
			{MsgID: 21, ReadCount: 7, Message: json.RawMessage(`{"event_name":"y"}`)},
		},
		tx: &mockTx{},
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processLogs(context.Background())

	if len(mq.archived) != 0 {
		t.Fatalf("out-of-tx archives = %v, want none", mq.archived)
	}
	if len(ms.insertedEvents) != 2 {
		t.Errorf("inserted events = %d, want 2", len(ms.insertedEvents))
	}
	if len(mq.archivedTx) != 2 {
		t.Errorf("tx archives = %v, want two messages", mq.archivedTx)
	}
}

// ---------------------------------------------------------------------------
// Tests: processMetrics
// ---------------------------------------------------------------------------

func TestProcessMetrics_Normal(t *testing.T) {
	tx := &mockTx{}
	v := 42.0
	mq := &mockQueue{
		readMsgs: []*queue.Message{
			makeMetricMsg(1, 1, store.OtelMetric{MetricName: "token_count", ValueDouble: &v}),
		},
		tx: tx,
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processMetrics(context.Background())

	if len(ms.insertedMetrics) != 1 {
		t.Fatalf("expected 1 inserted metric, got %d", len(ms.insertedMetrics))
	}
	if ms.insertedMetrics[0].MetricName != "token_count" {
		t.Errorf("expected metric_name token_count, got %s", ms.insertedMetrics[0].MetricName)
	}
	if len(mq.archivedTx) != 1 || mq.archivedTx[0] != 1 {
		t.Errorf("expected msg 1 archived in tx, got %v", mq.archivedTx)
	}
}

func TestProcessMetrics_HighReadCountStillProcessesValidMessage(t *testing.T) {
	mq := &mockQueue{
		readMsgs: []*queue.Message{
			{MsgID: 30, ReadCount: 8, Message: json.RawMessage(`{"metric_name":"x"}`)},
		},
		tx: &mockTx{},
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processMetrics(context.Background())

	if len(mq.archived) != 0 {
		t.Fatalf("out-of-tx archives = %v, want none", mq.archived)
	}
	if len(ms.insertedMetrics) != 1 {
		t.Errorf("inserted metrics = %d, want 1", len(ms.insertedMetrics))
	}
	if len(mq.archivedTx) != 1 || mq.archivedTx[0] != 30 {
		t.Errorf("tx archives = %v, want [30]", mq.archivedTx)
	}
}

func TestProcessMetrics_BadJSON(t *testing.T) {
	tx := &mockTx{}
	v := 1.0
	mq := &mockQueue{
		readMsgs: []*queue.Message{
			{MsgID: 40, ReadCount: 1, Message: json.RawMessage(`{invalid}`)},
			makeMetricMsg(41, 1, store.OtelMetric{MetricName: "cost", ValueDouble: &v}),
		},
		tx: tx,
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processMetrics(context.Background())

	if len(mq.archived) != 1 || mq.archived[0] != 40 {
		t.Fatalf("expected bad msg 40 archived, got %v", mq.archived)
	}
	if len(ms.insertedMetrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(ms.insertedMetrics))
	}
	if len(mq.archivedTx) != 1 || mq.archivedTx[0] != 41 {
		t.Errorf("expected msg 41 archived in tx, got %v", mq.archivedTx)
	}
}

func TestProcessMetrics_EmptyQueue(t *testing.T) {
	mq := &mockQueue{
		readMsgs: []*queue.Message{},
		tx:       &mockTx{},
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processMetrics(context.Background())

	if len(ms.insertedMetrics) != 0 {
		t.Errorf("expected 0 metrics, got %d", len(ms.insertedMetrics))
	}
}

// ---------------------------------------------------------------------------
// Tests: Run loop
// ---------------------------------------------------------------------------

func TestRun_ShutdownOnCancel(t *testing.T) {
	mq := &mockQueue{
		readMsgs: []*queue.Message{},
		tx:       &mockTx{},
	}
	ms := &mockStore{}
	w := &Worker{
		queue:        mq,
		store:        ms,
		batchSize:    50,
		pollInterval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Let it tick at least once
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// good — worker exited
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not shut down within timeout")
	}
}

// ---------------------------------------------------------------------------
// Tests: Edge cases — repeated delivery
// ---------------------------------------------------------------------------

func TestProcessLogs_ReadCountNeverClassifiesValidDataAsPoison(t *testing.T) {
	tx := &mockTx{}
	mq := &mockQueue{
		readMsgs: []*queue.Message{
			makeEventMsg(100, 5, store.OtelEvent{EventName: "ok"}),
			{MsgID: 101, ReadCount: 6, Message: json.RawMessage(`{"event_name":"poison"}`)},
		},
		tx: tx,
	}
	ms := &mockStore{}
	w := newTestWorker(mq, ms)

	w.processLogs(context.Background())

	if len(ms.insertedEvents) != 2 {
		t.Fatalf("inserted events = %d, want 2", len(ms.insertedEvents))
	}
	if len(mq.archived) != 0 {
		t.Errorf("out-of-tx archives = %v, want none", mq.archived)
	}
	if len(mq.archivedTx) != 2 {
		t.Errorf("tx archives = %v, want both messages", mq.archivedTx)
	}
}
