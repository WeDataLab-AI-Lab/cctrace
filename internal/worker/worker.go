package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"cctrace/internal/queue"
	"cctrace/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type batchItem[T any] struct {
	value     *T
	msgID     int64
	readCount int32
}

const poisonPillReadCount = 100

// queueReader abstracts queue read operations for testability.
type queueReader interface {
	Read(ctx context.Context, queueName string, limit int, vtSeconds int) ([]*queue.Message, error)
	Archive(ctx context.Context, queueName string, msgID int64) error
	BeginTx(ctx context.Context) (pgx.Tx, error)
	ArchiveInTx(ctx context.Context, tx pgx.Tx, queueName string, msgID int64) error
}

// eventInserter abstracts store write operations for testability.
type eventInserter interface {
	InsertEventsTx(ctx context.Context, tx pgx.Tx, events []*store.OtelEvent) error
	InsertMetricsTx(ctx context.Context, tx pgx.Tx, metrics []*store.OtelMetric) error
}

// Worker dequeues messages from PGMQ and persists them to Store.
type Worker struct {
	queue        queueReader
	store        eventInserter
	batchSize    int
	pollInterval time.Duration
}

func New(q *queue.Queue, s *store.PgStore) *Worker {
	return &Worker{
		queue:        q,
		store:        s,
		batchSize:    50,
		pollInterval: 500 * time.Millisecond,
	}
}

// Run starts the worker loop. Blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	log.Println("[worker] started")

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[worker] shutting down")
			return
		case <-ticker.C:
			w.processLogs(ctx)
			w.processMetrics(ctx)
		}
	}
}

func (w *Worker) processLogs(ctx context.Context) {
	processQueue(ctx, w, queue.QueueOtelLogs, "logs", "event", "log events", w.store.InsertEventsTx)
}

func (w *Worker) processMetrics(ctx context.Context) {
	processQueue(ctx, w, queue.QueueOtelMetrics, "metrics", "metric", "metrics", w.store.InsertMetricsTx)
}

func processQueue[T any](
	ctx context.Context,
	w *Worker,
	queueName string,
	queueLabel string,
	itemLabel string,
	batchLabel string,
	insert func(context.Context, pgx.Tx, []*T) error,
) {
	msgs, err := w.queue.Read(ctx, queueName, w.batchSize, queue.DefaultVisibilityTimeout)
	if err != nil {
		log.Printf("[worker] read %s queue: %v", queueLabel, err)
		return
	}
	if len(msgs) == 0 {
		return
	}

	items := make([]batchItem[T], 0, len(msgs))
	for _, msg := range msgs {
		value := new(T)
		if err := json.Unmarshal(msg.Message, value); err != nil {
			log.Printf("[worker] unmarshal %s msg_id=%d: %v", itemLabel, msg.MsgID, err)
			if err := w.queue.Archive(ctx, queueName, msg.MsgID); err != nil {
				log.Printf("[worker] archive bad msg_id=%d: %v", msg.MsgID, err)
			}
			continue
		}
		items = append(items, batchItem[T]{value: value, msgID: msg.MsgID, readCount: msg.ReadCount})
	}

	if len(items) == 0 {
		return
	}

	processed := persistBatch(ctx, w, queueName, batchLabel, items, insert)
	if processed > 0 {
		log.Printf("[worker] processed %d %s", processed, batchLabel)
	}
}

// persistBatch keeps INSERT + archive atomic while recursively splitting an
// insert-rejected batch. This lets valid neighbors commit and leaves only the
// isolated poison message visible for retry and eventual poison-pill handling.
func persistBatch[T any](
	ctx context.Context,
	w *Worker,
	queueName string,
	label string,
	items []batchItem[T],
	insert func(context.Context, pgx.Tx, []*T) error,
) int {
	tx, err := w.queue.BeginTx(ctx)
	if err != nil {
		log.Printf("[worker] begin tx for %s: %v", label, err)
		return 0
	}

	values := make([]*T, len(items))
	for i := range items {
		values[i] = items[i].value
	}
	if err := insert(ctx, tx, values); err != nil {
		_ = tx.Rollback(ctx)
		if len(items) == 1 {
			if isRowSpecificError(err) {
				if archiveErr := w.queue.Archive(ctx, queueName, items[0].msgID); archiveErr != nil {
					log.Printf("[worker] archive rejected %s msg_id=%d: %v",
						label, items[0].msgID, archiveErr)
				} else {
					log.Printf("[worker] archived rejected %s msg_id=%d: %v",
						label, items[0].msgID, err)
				}
				return 0
			}
			if items[0].readCount >= poisonPillReadCount {
				if archiveErr := w.queue.Archive(ctx, queueName, items[0].msgID); archiveErr != nil {
					log.Printf("[worker] archive persistent poison %s msg_id=%d: %v", label, items[0].msgID, archiveErr)
				} else {
					log.Printf("[worker] archived persistent poison %s msg_id=%d after %d reads: %v", label, items[0].msgID, items[0].readCount, err)
				}
				return 0
			}
			log.Printf("[worker] insert isolated %s msg_id=%d failed: %v (will retry, read_count=%d)",
				label, items[0].msgID, err, items[0].readCount)
			return 0
		}
		if !isRowSpecificError(err) {
			log.Printf("[worker] insert %d %s failed: %v (will retry)", len(items), label, err)
			return 0
		}
		mid := len(items) / 2
		return persistBatch(ctx, w, queueName, label, items[:mid], insert) +
			persistBatch(ctx, w, queueName, label, items[mid:], insert)
	}

	for _, item := range items {
		if err := w.queue.ArchiveInTx(ctx, tx, queueName, item.msgID); err != nil {
			log.Printf("[worker] archive in tx msg_id=%d: %v", item.msgID, err)
			_ = tx.Rollback(ctx)
			return 0
		}
	}
	if err := tx.Commit(ctx); err != nil {
		log.Printf("[worker] commit tx for %s: %v", label, err)
		_ = tx.Rollback(ctx)
		return 0
	}
	return len(items)
}

func isRowSpecificError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || len(pgErr.Code) < 2 {
		return false
	}
	switch pgErr.Code[:2] {
	case "22", "23":
		return true
	default:
		return false
	}
}
