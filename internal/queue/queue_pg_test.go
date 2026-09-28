package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/jackc/pgx/v5/pgxpool"

	"cctrace/internal/containertest"
)

func TestMain(m *testing.M) {
	ensureDockerHost()
	os.Exit(m.Run())
}

// inVMDockerSocket is the path Ryuk mounts to reach the Docker API from inside
// a container. It is deliberately NOT the DOCKER_HOST path: under Colima,
// DOCKER_HOST points at a socket on the macOS side
// (~/.colima/default/docker.sock) that cannot be bind-mounted into a container
// running in the VM ("operation not supported"). The daemon inside the VM
// exposes the same API at the standard path, so that is what Ryuk gets.
//
// Setting these to the same value is what previously made Ryuk fail to start,
// which is why the reaper used to be disabled and test containers accumulated.
const inVMDockerSocket = "/var/run/docker.sock"

func ensureDockerHost() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	sock := filepath.Join(home, ".colima", "default", "docker.sock")
	if _, err := os.Stat(sock); err != nil {
		return
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		os.Setenv("DOCKER_HOST", "unix://"+sock)
	} else if host != "unix://"+sock {
		// Pointing somewhere else entirely (remote daemon, Docker Desktop):
		// leave the reaper mount alone, the default is right for that setup.
		return
	}
	// Set this even when DOCKER_HOST was already exported. The testing guide
	// tells developers to export it, and that path used to skip the override,
	// which put the un-mountable macOS socket back in front of Ryuk.
	if os.Getenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE") == "" {
		os.Setenv("TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE", inVMDockerSocket)
	}
}

// --- Shared pgmq container ---

var (
	pgmqOnce    sync.Once
	sharedQueue *Queue
	pgmqInitErr error
)

func acquireQueue(t *testing.T) *Queue {
	t.Helper()
	pgmqOnce.Do(func() {
		ctx := context.Background()
		req := tc.ContainerRequest{
			Image:        "quay.io/tembo/pg16-pgmq:latest",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     "test",
				"POSTGRES_PASSWORD": "test",
				"POSTGRES_DB":       "testdb",
			},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60 * time.Second),
		}
		ctr, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		})
		if err != nil {
			pgmqInitErr = fmt.Errorf("pgmq container: %w", err)
			return
		}

		host, err := ctr.Host(ctx)
		if err != nil {
			pgmqInitErr = err
			return
		}
		mapped, err := ctr.MappedPort(ctx, "5432/tcp")
		if err != nil {
			pgmqInitErr = err
			return
		}

		dsn := fmt.Sprintf("postgres://test:test@%s:%s/testdb?sslmode=disable", host, mapped.Port())
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			pgmqInitErr = err
			return
		}
		if err := pool.Ping(ctx); err != nil {
			pgmqInitErr = err
			return
		}

		q := New(pool)
		if err := q.CreateQueues(ctx); err != nil {
			pgmqInitErr = fmt.Errorf("CreateQueues: %w", err)
			return
		}
		sharedQueue = q
	})
	if pgmqInitErr != nil {
		containertest.SkipOrFail(t, "pgmq container", pgmqInitErr)
	}
	return sharedQueue
}

// purgeQueue truncates the pgmq queue table for clean test isolation.
// Tests are in package queue so pool is accessible.
func purgeQueue(t *testing.T, q *Queue, queueName string) {
	t.Helper()
	ctx := context.Background()
	if _, err := q.pool.Exec(ctx, "TRUNCATE pgmq.q_"+queueName); err != nil {
		t.Fatalf("purgeQueue: %v", err)
	}
}

// --- Test payload type ---

type testPayload struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestQueue_Pool(t *testing.T) {
	q := acquireQueue(t)
	if q.Pool() == nil {
		t.Error("Pool() returned nil")
	}
}

func TestQueue_Send_And_Read(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	msgID, err := q.Send(ctx, QueueOtelLogs, testPayload{Name: "test", Value: 42})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if msgID <= 0 {
		t.Errorf("expected positive msg_id, got %d", msgID)
	}

	msgs, err := q.Read(ctx, QueueOtelLogs, 10, 30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	m := msgs[0]
	if m.MsgID != msgID {
		t.Errorf("MsgID: got %d, want %d", m.MsgID, msgID)
	}

	var p testPayload
	if err := json.Unmarshal(m.Message, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Name != "test" || p.Value != 42 {
		t.Errorf("payload: got %+v", p)
	}
}

func TestQueue_SendRaw_And_Read(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	raw := []byte(`{"name":"raw","value":99}`)
	msgID, err := q.SendRaw(ctx, QueueOtelLogs, raw)
	if err != nil {
		t.Fatalf("SendRaw: %v", err)
	}
	if msgID <= 0 {
		t.Errorf("expected positive msg_id from SendRaw, got %d", msgID)
	}

	msgs, err := q.Read(ctx, QueueOtelLogs, 5, 30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected at least 1 message")
	}

	var p testPayload
	_ = json.Unmarshal(msgs[0].Message, &p)
	if p.Name != "raw" || p.Value != 99 {
		t.Errorf("SendRaw payload: got %+v", p)
	}
}

func TestQueue_SendBatch(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	payloads := []interface{}{
		testPayload{Name: "batch-1", Value: 1},
		testPayload{Name: "batch-2", Value: 2},
		testPayload{Name: "batch-3", Value: 3},
	}
	if err := q.SendBatch(ctx, QueueOtelLogs, payloads); err != nil {
		t.Fatalf("SendBatch: %v", err)
	}

	depth, err := q.Depth(ctx, QueueOtelLogs)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 3 {
		t.Errorf("expected depth 3 after SendBatch, got %d", depth)
	}
}

func TestQueue_Depth(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelMetrics)
	ctx := context.Background()

	before, err := q.Depth(ctx, QueueOtelMetrics)
	if err != nil {
		t.Fatalf("Depth before: %v", err)
	}
	if before != 0 {
		t.Errorf("expected depth 0 after purge, got %d", before)
	}

	if _, err := q.Send(ctx, QueueOtelMetrics, testPayload{Name: "m1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	after, err := q.Depth(ctx, QueueOtelMetrics)
	if err != nil {
		t.Fatalf("Depth after: %v", err)
	}
	if after != 1 {
		t.Errorf("expected depth 1, got %d", after)
	}
}

func TestQueue_Delete(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	msgID, err := q.Send(ctx, QueueOtelLogs, testPayload{Name: "to-delete"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Read to make it visible for deletion
	msgs, err := q.Read(ctx, QueueOtelLogs, 5, 30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected message to be readable")
	}

	if err := q.Delete(ctx, QueueOtelLogs, msgID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify depth is 0 after delete
	depth, _ := q.Depth(ctx, QueueOtelLogs)
	if depth != 0 {
		t.Errorf("expected depth 0 after delete, got %d", depth)
	}
}

func TestQueue_Archive(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	msgID, err := q.Send(ctx, QueueOtelLogs, testPayload{Name: "to-archive"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Read first (required before archive in some pgmq versions)
	_, err = q.Read(ctx, QueueOtelLogs, 1, 30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if err := q.Archive(ctx, QueueOtelLogs, msgID); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// Queue should be empty after archive
	depth, _ := q.Depth(ctx, QueueOtelLogs)
	if depth != 0 {
		t.Errorf("expected depth 0 after archive, got %d", depth)
	}
}

func TestQueue_BeginTx_And_ArchiveInTx(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	msgID, err := q.Send(ctx, QueueOtelLogs, testPayload{Name: "tx-archive"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Read to claim the message
	_, err = q.Read(ctx, QueueOtelLogs, 1, 30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	tx, err := q.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	if err := q.ArchiveInTx(ctx, tx, QueueOtelLogs, msgID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("ArchiveInTx: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	depth, _ := q.Depth(ctx, QueueOtelLogs)
	if depth != 0 {
		t.Errorf("expected depth 0 after ArchiveInTx+Commit, got %d", depth)
	}
}

func TestQueue_BeginTx_Rollback(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelMetrics)
	ctx := context.Background()

	_, err := q.Send(ctx, QueueOtelMetrics, testPayload{Name: "rollback-test"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	tx, err := q.BeginTx(ctx)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	// Rollback without doing anything — should not change queue state.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	depth, _ := q.Depth(ctx, QueueOtelMetrics)
	if depth != 1 {
		t.Errorf("expected depth 1 after rollback, got %d", depth)
	}
}

func TestQueue_Read_RespectsLimit(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if _, err := q.Send(ctx, QueueOtelLogs, testPayload{Name: fmt.Sprintf("msg-%d", i)}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	msgs, err := q.Read(ctx, QueueOtelLogs, 3, 30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages (limit=3), got %d", len(msgs))
	}
}

func TestQueue_CreateQueues_Idempotent(t *testing.T) {
	q := acquireQueue(t)
	ctx := context.Background()

	// Call CreateQueues again — should not error (idempotent).
	if err := q.CreateQueues(ctx); err != nil {
		t.Errorf("CreateQueues second call: %v", err)
	}
}

func TestQueue_SendBatch_Empty(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	if err := q.SendBatch(ctx, QueueOtelLogs, []interface{}{}); err != nil {
		t.Fatalf("SendBatch with empty slice: %v", err)
	}

	depth, err := q.Depth(ctx, QueueOtelLogs)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 0 {
		t.Errorf("expected depth 0 after empty SendBatch, got %d", depth)
	}
}

func TestQueue_Read_EmptyQueue(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelMetrics)
	ctx := context.Background()

	msgs, err := q.Read(ctx, QueueOtelMetrics, 10, 30)
	if err != nil {
		t.Fatalf("Read on empty queue: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages from empty queue, got %d", len(msgs))
	}
}

func TestQueue_Delete_NonExistent(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	// Delete a msg_id that does not exist — pgmq.delete returns no error for missing IDs.
	err := q.Delete(ctx, QueueOtelLogs, 999999)
	if err != nil {
		t.Errorf("Delete non-existent msgID: expected no error, got %v", err)
	}
}

func TestQueue_Archive_AfterDelete(t *testing.T) {
	q := acquireQueue(t)
	purgeQueue(t, q, QueueOtelLogs)
	ctx := context.Background()

	msgID, err := q.Send(ctx, QueueOtelLogs, testPayload{Name: "archive-after-delete"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Read to make message visible.
	_, err = q.Read(ctx, QueueOtelLogs, 1, 30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Delete the message.
	if err := q.Delete(ctx, QueueOtelLogs, msgID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Archive the already-deleted message — should not panic; may return an error or succeed silently.
	err = q.Archive(ctx, QueueOtelLogs, msgID)
	// We don't assert a specific error; we just verify no panic and the queue is stable.
	_ = err

	depth, depthErr := q.Depth(ctx, QueueOtelLogs)
	if depthErr != nil {
		t.Fatalf("Depth after archive-after-delete: %v", depthErr)
	}
	if depth != 0 {
		t.Errorf("expected depth 0 after delete+archive, got %d", depth)
	}
}
