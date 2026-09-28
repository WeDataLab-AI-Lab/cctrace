package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	QueueOtelLogs    = "otel_logs"
	QueueOtelMetrics = "otel_metrics"

	// Messages invisible for 30s after read; if not deleted, they reappear.
	DefaultVisibilityTimeout = 30
)

// Message represents a PGMQ message.
type Message struct {
	MsgID      int64           `json:"msg_id"`
	ReadCount  int32           `json:"read_ct"`
	EnqueuedAt time.Time       `json:"enqueued_at"`
	VT         time.Time       `json:"vt"`
	Message    json.RawMessage `json:"message"`
}

// Queue wraps PGMQ operations.
type Queue struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Queue {
	return &Queue{pool: pool}
}

// CreateQueues creates the PGMQ extension and required queues.
func (q *Queue) CreateQueues(ctx context.Context) error {
	if _, err := q.pool.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pgmq`); err != nil {
		return fmt.Errorf("create pgmq extension: %w", err)
	}
	for _, name := range []string{QueueOtelLogs, QueueOtelMetrics} {
		if _, err := q.pool.Exec(ctx, `SELECT pgmq.create_non_partitioned($1)`, name); err != nil {
			// Queue may already exist
			if _, err2 := q.pool.Exec(ctx, `SELECT pgmq.create($1)`, name); err2 != nil {
				// Both failed — try to check if it exists
				var exists bool
				checkErr := q.pool.QueryRow(ctx,
					`SELECT EXISTS(SELECT 1 FROM pgmq.meta WHERE queue_name = $1)`, name,
				).Scan(&exists)
				if checkErr != nil || !exists {
					return fmt.Errorf("create queue %s: %w", name, err)
				}
			}
		}
	}
	return nil
}

// Send enqueues a JSON message.
func (q *Queue) Send(ctx context.Context, queueName string, payload interface{}) (int64, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal: %w", err)
	}
	var msgID int64
	err = q.pool.QueryRow(ctx,
		`SELECT pgmq.send($1, $2::jsonb)`, queueName, string(data),
	).Scan(&msgID)
	return msgID, err
}

// SendRaw enqueues a pre-marshaled JSON message ([]byte).
func (q *Queue) SendRaw(ctx context.Context, queueName string, data []byte) (int64, error) {
	var msgID int64
	err := q.pool.QueryRow(ctx,
		`SELECT pgmq.send($1, $2::jsonb)`, queueName, string(data),
	).Scan(&msgID)
	return msgID, err
}

// SendBatch enqueues multiple messages in a single pgmq.send_batch() call.
func (q *Queue) SendBatch(ctx context.Context, queueName string, payloads []interface{}) error {
	if len(payloads) == 0 {
		return nil
	}
	msgs := make([]string, len(payloads))
	for i, p := range payloads {
		data, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("marshal payload %d: %w", i, err)
		}
		msgs[i] = string(data)
	}
	_, err := q.pool.Exec(ctx,
		`SELECT pgmq.send_batch($1, $2::jsonb[])`, queueName, msgs,
	)
	return err
}

// Read reads up to limit messages with visibility timeout.
func (q *Queue) Read(ctx context.Context, queueName string, limit int, vtSeconds int) ([]*Message, error) {
	if vtSeconds <= 0 {
		vtSeconds = DefaultVisibilityTimeout
	}
	rows, err := q.pool.Query(ctx,
		`SELECT msg_id, read_ct, enqueued_at, vt, message FROM pgmq.read($1, $2, $3)`,
		queueName, vtSeconds, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.MsgID, &m.ReadCount, &m.EnqueuedAt, &m.VT, &m.Message); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// Delete removes a processed message from the queue.
func (q *Queue) Delete(ctx context.Context, queueName string, msgID int64) error {
	_, err := q.pool.Exec(ctx, `SELECT pgmq.delete($1::text, $2::bigint)`, queueName, msgID)
	return err
}

// Pool returns the underlying pgxpool for transaction management.
func (q *Queue) Pool() *pgxpool.Pool {
	return q.pool
}

// BeginTx starts a new transaction on the underlying pool.
func (q *Queue) BeginTx(ctx context.Context) (pgx.Tx, error) {
	return q.pool.Begin(ctx)
}

// Archive moves a message to the archive table (preserves raw data).
func (q *Queue) Archive(ctx context.Context, queueName string, msgID int64) error {
	_, err := q.pool.Exec(ctx, `SELECT pgmq.archive($1::text, $2::bigint)`, queueName, msgID)
	return err
}

// ArchiveInTx moves a message to the archive table within an existing transaction.
func (q *Queue) ArchiveInTx(ctx context.Context, tx pgx.Tx, queueName string, msgID int64) error {
	_, err := tx.Exec(ctx, `SELECT pgmq.archive($1::text, $2::bigint)`, queueName, msgID)
	return err
}

// Depth returns the number of messages in the queue.
func (q *Queue) Depth(ctx context.Context, queueName string) (int64, error) {
	var depth int64
	err := q.pool.QueryRow(ctx,
		`SELECT count(*) FROM pgmq.q_`+queueName,
	).Scan(&depth)
	return depth, err
}
