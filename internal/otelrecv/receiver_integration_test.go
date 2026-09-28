package otelrecv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cctrace/internal/buffer"
	"cctrace/internal/containertest"
	"cctrace/internal/emailalias"
	"cctrace/internal/queue"
	"cctrace/internal/store"

	tc "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// --- pgmq container (happy path) ---

var (
	pgmqOnce  sync.Once
	pgmqQueue *queue.Queue
	pgmqErr   error
)

func acquirePgmqQueue(t *testing.T) *queue.Queue {
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
			pgmqErr = fmt.Errorf("pgmq container: %w", err)
			return
		}

		host, err := ctr.Host(ctx)
		if err != nil {
			pgmqErr = err
			return
		}
		mapped, err := ctr.MappedPort(ctx, "5432/tcp")
		if err != nil {
			pgmqErr = err
			return
		}

		dsn := fmt.Sprintf("postgres://test:test@%s:%s/testdb?sslmode=disable", host, mapped.Port())
		pgStore, err := store.NewPgStore(ctx, dsn)
		if err != nil {
			pgmqErr = err
			return
		}

		q := queue.New(pgStore.Pool())
		if err := q.CreateQueues(ctx); err != nil {
			pgmqErr = fmt.Errorf("CreateQueues: %w", err)
			return
		}
		pgmqQueue = q
	})
	if pgmqErr != nil {
		containertest.SkipOrFail(t, "pgmq container", pgmqErr)
	}
	return pgmqQueue
}

// --- plain postgres container (no pgmq, for buffer fallback) ---

var (
	plainOnce  sync.Once
	plainQueue *queue.Queue
	plainErr   error
)

func acquirePlainQueue(t *testing.T) *queue.Queue {
	t.Helper()
	plainOnce.Do(func() {
		ctx := context.Background()
		req := tc.ContainerRequest{
			Image:        "postgres:16-alpine",
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
			plainErr = err
			return
		}

		host, err := ctr.Host(ctx)
		if err != nil {
			plainErr = err
			return
		}
		mapped, err := ctr.MappedPort(ctx, "5432/tcp")
		if err != nil {
			plainErr = err
			return
		}

		dsn := fmt.Sprintf("postgres://test:test@%s:%s/testdb?sslmode=disable", host, mapped.Port())
		pgStore, err := store.NewPgStore(ctx, dsn)
		if err != nil {
			plainErr = err
			return
		}
		// Do NOT call CreateQueues — pgmq extension is absent, Send will fail.
		plainQueue = queue.New(pgStore.Pool())
	})
	if plainErr != nil {
		containertest.SkipOrFail(t, "plain postgres container", plainErr)
	}
	return plainQueue
}

// --- OTLP request builders ---

func buildLogsRequest(sessionID, userEmail, eventName string, extraAttrs map[string]interface{}) *collogspb.ExportLogsServiceRequest {
	kvs := []*commonpb.KeyValue{
		{Key: "session.id", Value: strAV(sessionID)},
		{Key: "user.email", Value: strAV(userEmail)},
		{Key: "event.name", Value: strAV(eventName)},
	}
	for k, v := range extraAttrs {
		switch val := v.(type) {
		case string:
			kvs = append(kvs, &commonpb.KeyValue{Key: k, Value: strAV(val)})
		case float64:
			kvs = append(kvs, &commonpb.KeyValue{Key: k, Value: doubleAV(val)})
		case int64:
			kvs = append(kvs, &commonpb.KeyValue{Key: k, Value: intAV(val)})
		case bool:
			kvs = append(kvs, &commonpb.KeyValue{Key: k, Value: boolAV(val)})
		}
	}

	return &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "user.email", Value: strAV(userEmail)},
						{Key: "service.version", Value: strAV("1.0.0")},
					},
				},
				ScopeLogs: []*logspb.ScopeLogs{
					{
						LogRecords: []*logspb.LogRecord{
							{
								TimeUnixNano: uint64(time.Now().UnixNano()),
								Attributes:   kvs,
							},
						},
					},
				},
			},
		},
	}
}

func buildMetricsRequest(metricName string, value float64, userEmail string) *colmetricspb.ExportMetricsServiceRequest {
	asDouble := value
	return &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "user.email", Value: strAV(userEmail)},
						{Key: "user.team", Value: strAV("eng")},
					},
				},
				ScopeMetrics: []*metricspb.ScopeMetrics{
					{
						Metrics: []*metricspb.Metric{
							{
								Name: metricName,
								Data: &metricspb.Metric_Gauge{
									Gauge: &metricspb.Gauge{
										DataPoints: []*metricspb.NumberDataPoint{
											{
												TimeUnixNano: uint64(time.Now().UnixNano()),
												Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: asDouble},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// --- LogsReceiver tests ---

func TestLogsReceiver_Export_HappyPath(t *testing.T) {
	q := acquirePgmqQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewLogsReceiver(q, buf, emailalias.Aliases{})

	req := buildLogsRequest("ses-happy", "happy@test.com", "api_request", map[string]interface{}{
		"model":        "claude-3",
		"cost.usd":     float64(0.05),
		"input.tokens": int64(100),
	})

	depthBefore, err := q.Depth(ctx, queue.QueueOtelLogs)
	if err != nil {
		t.Fatalf("Depth before: %v", err)
	}

	resp, err := recv.Export(ctx, req)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if resp == nil {
		t.Fatal("Export: nil response")
	}

	depthAfter, err := q.Depth(ctx, queue.QueueOtelLogs)
	if err != nil {
		t.Fatalf("Depth after: %v", err)
	}

	if depthAfter <= depthBefore {
		t.Errorf("queue depth did not increase: before=%d after=%d", depthBefore, depthAfter)
	}

	// Buffer should be empty — no fallback needed
	if buf.Len() != 0 {
		t.Errorf("buffer should be empty on happy path, got %d items", buf.Len())
	}
}

func TestLogsReceiver_Export_AttributeParsing(t *testing.T) {
	q := acquirePgmqQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewLogsReceiver(q, buf, emailalias.Aliases{})

	req := buildLogsRequest("ses-attrs", "attr@test.com", "tool_result", map[string]interface{}{
		"tool_name":   "Read", // underscore notation
		"success":     true,   // logs.go checks "tool.success" or "success"
		"duration_ms": int64(250),
	})

	depthBefore, _ := q.Depth(ctx, queue.QueueOtelLogs)
	if _, err := recv.Export(ctx, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	depthAfter, _ := q.Depth(ctx, queue.QueueOtelLogs)
	if depthAfter <= depthBefore {
		t.Errorf("expected event in queue, depth unchanged: %d", depthAfter)
	}

	// Read the enqueued message and verify parsed fields
	msgs, err := q.Read(ctx, queue.QueueOtelLogs, 10, 30)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var found *store.OtelEvent
	for _, msg := range msgs {
		var e store.OtelEvent
		if err := json.Unmarshal(msg.Message, &e); err != nil {
			continue
		}
		if e.SessionID == "ses-attrs" {
			found = &e
			_ = q.Delete(ctx, queue.QueueOtelLogs, msg.MsgID)
			break
		}
	}
	if found == nil {
		t.Fatal("could not find enqueued event in queue")
	}
	if found.ToolName != "Read" {
		t.Errorf("ToolName: got %q, want %q", found.ToolName, "Read")
	}
	if found.ToolSuccess == nil || !*found.ToolSuccess {
		t.Errorf("ToolSuccess: got %v, want true", found.ToolSuccess)
	}
	if found.DurationMs == nil || *found.DurationMs != 250 {
		t.Errorf("DurationMs: got %v, want 250", found.DurationMs)
	}
}

func TestLogsReceiver_Export_ResourceAttributes(t *testing.T) {
	q := acquirePgmqQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewLogsReceiver(q, buf, emailalias.Aliases{})

	// Resource has user info; log record has event info
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "user.id", Value: strAV("uid-res")},
						{Key: "user.email", Value: strAV("resource@test.com")},
						{Key: "user.profile.email", Value: strAV("resource@test.com")},
						{Key: "user.team", Value: strAV("platform")},
						{Key: "org.id", Value: strAV("org-res")},
						{Key: "service.version", Value: strAV("2.0.0")},
					},
				},
				ScopeLogs: []*logspb.ScopeLogs{
					{
						LogRecords: []*logspb.LogRecord{
							{
								TimeUnixNano: uint64(time.Now().UnixNano()),
								Attributes: []*commonpb.KeyValue{
									{Key: "event.name", Value: strAV("user_prompt")},
									{Key: "session.id", Value: strAV("ses-resource")},
								},
							},
						},
					},
				},
			},
		},
	}

	depthBefore, _ := q.Depth(ctx, queue.QueueOtelLogs)
	if _, err := recv.Export(ctx, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	depthAfter, _ := q.Depth(ctx, queue.QueueOtelLogs)
	if depthAfter <= depthBefore {
		t.Error("event not enqueued")
	}

	// Read and verify resource fields propagated
	msgs, _ := q.Read(ctx, queue.QueueOtelLogs, 20, 30)
	for _, msg := range msgs {
		var e store.OtelEvent
		if err := json.Unmarshal(msg.Message, &e); err != nil {
			continue
		}
		if e.SessionID != "ses-resource" {
			continue
		}
		_ = q.Delete(ctx, queue.QueueOtelLogs, msg.MsgID)
		if e.ProfileEmail != "resource@test.com" {
			t.Errorf("ProfileEmail from resource: got %q", e.ProfileEmail)
		}
		if e.UserTeam != "platform" {
			t.Errorf("UserTeam from resource: got %q", e.UserTeam)
		}
		if e.ServiceVersion != "2.0.0" {
			t.Errorf("ServiceVersion from resource: got %q", e.ServiceVersion)
		}
		return
	}
	t.Error("enqueued event not found in queue")
}

func TestLogsReceiver_Export_Empty(t *testing.T) {
	q := acquirePgmqQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewLogsReceiver(q, buf, emailalias.Aliases{})

	depthBefore, _ := q.Depth(ctx, queue.QueueOtelLogs)
	resp, err := recv.Export(ctx, &collogspb.ExportLogsServiceRequest{})
	if err != nil {
		t.Fatalf("Export empty: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}

	depthAfter, _ := q.Depth(ctx, queue.QueueOtelLogs)
	if depthAfter != depthBefore {
		t.Errorf("empty request should not change queue depth: before=%d after=%d", depthBefore, depthAfter)
	}
	if buf.Len() != 0 {
		t.Errorf("buffer should remain empty: got %d", buf.Len())
	}
}

func TestLogsReceiver_Export_BufferFallback(t *testing.T) {
	q := acquirePlainQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewLogsReceiver(q, buf, emailalias.Aliases{})

	// Send multiple events — all should fail to enqueue and fall into buffer
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				ScopeLogs: []*logspb.ScopeLogs{
					{
						LogRecords: []*logspb.LogRecord{
							{
								TimeUnixNano: uint64(time.Now().UnixNano()),
								Attributes: []*commonpb.KeyValue{
									{Key: "event.name", Value: strAV("api_request")},
									{Key: "session.id", Value: strAV("buf-ses")},
								},
							},
							{
								TimeUnixNano: uint64(time.Now().UnixNano()),
								Attributes: []*commonpb.KeyValue{
									{Key: "event.name", Value: strAV("tool_result")},
									{Key: "session.id", Value: strAV("buf-ses")},
								},
							},
						},
					},
				},
			},
		},
	}

	resp, err := recv.Export(ctx, req)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}

	// Both events should be in the ring buffer since pgmq is absent
	if buf.Len() != 2 {
		t.Errorf("expected 2 items in buffer, got %d", buf.Len())
	}

	// Verify buffer contents are valid JSON events
	for i := 0; i < 2; i++ {
		data, ok := buf.Pop()
		if !ok {
			t.Fatalf("expected item %d in buffer", i)
		}
		var e store.OtelEvent
		if err := json.Unmarshal(data, &e); err != nil {
			t.Errorf("item %d not valid OtelEvent JSON: %v", i, err)
		}
		if e.SessionID != "buf-ses" {
			t.Errorf("item %d unexpected SessionID: %q", i, e.SessionID)
		}
	}
}

// --- MetricsReceiver tests ---

// requireMetricEnqueued finds a metric this test just exported in the queue and
// removes it, failing if it is not there.
//
// These tests used to compare pgmq depth before and after, which is a global
// counter: it answers "did the queue grow", not "did my metric reach it". Both
// readings also discarded their error, and Depth returns 0 on error, so a transient
// database hiccup produced `before=0 after=0` -- a failure naming neither the cause
// nor the metric. Three of them failed together on CI that way and passed on a
// rerun, which is the signature of an assertion measuring the environment.
//
// Reading the message back tests what the receiver is for, and when it is missing
// the ring buffer says why: the receiver falls back to buffering whenever the
// enqueue fails, so a non-empty buffer means the send failed rather than the metric
// being wrong.
func requireMetricEnqueued(t *testing.T, q *queue.Queue, buf *buffer.Ring, match func(*store.OtelMetric) bool) *store.OtelMetric {
	t.Helper()
	ctx := context.Background()
	msgs, err := q.Read(ctx, queue.QueueOtelMetrics, 50, 30)
	if err != nil {
		t.Fatalf("Read %s: %v", queue.QueueOtelMetrics, err)
	}
	for _, msg := range msgs {
		var m store.OtelMetric
		if err := json.Unmarshal(msg.Message, &m); err != nil {
			continue
		}
		if match(&m) {
			_ = q.Delete(ctx, queue.QueueOtelMetrics, msg.MsgID)
			return &m
		}
	}
	if buf.Len() > 0 {
		t.Fatalf("metric is not in the queue and %d item(s) sit in the ring buffer -- "+
			"the receiver fell back to buffering, so the enqueue itself failed", buf.Len())
	}
	t.Fatalf("metric not found among %d queued message(s)", len(msgs))
	return nil
}

func TestMetricsReceiver_Export_Gauge(t *testing.T) {
	q := acquirePgmqQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewMetricsReceiver(q, buf, emailalias.Aliases{})

	req := buildMetricsRequest("claude.token_count", 512.0, "gauge@test.com")

	resp, err := recv.Export(ctx, req)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}

	got := requireMetricEnqueued(t, q, buf, func(m *store.OtelMetric) bool {
		return m.MetricName == "claude.token_count" && m.ValueDouble != nil && *m.ValueDouble == 512.0
	})
	if got.ValueDouble == nil || *got.ValueDouble != 512.0 {
		t.Errorf("value = %v, want 512", got.ValueDouble)
	}
	if buf.Len() != 0 {
		t.Errorf("buffer should be empty, got %d", buf.Len())
	}
}

func TestMetricsReceiver_Export_Sum(t *testing.T) {
	q := acquirePgmqQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewMetricsReceiver(q, buf, emailalias.Aliases{})

	asInt := int64(99)
	req := &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "user.email", Value: strAV("sum@test.com")},
					},
				},
				ScopeMetrics: []*metricspb.ScopeMetrics{
					{
						Metrics: []*metricspb.Metric{
							{
								Name: "claude.api_calls",
								Data: &metricspb.Metric_Sum{
									Sum: &metricspb.Sum{
										DataPoints: []*metricspb.NumberDataPoint{
											{
												TimeUnixNano: uint64(time.Now().UnixNano()),
												Value:        &metricspb.NumberDataPoint_AsInt{AsInt: asInt},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if _, err := recv.Export(ctx, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	got := requireMetricEnqueued(t, q, buf, func(m *store.OtelMetric) bool {
		return m.MetricName == "claude.api_calls"
	})
	if got.ValueInt == nil || *got.ValueInt != asInt {
		t.Errorf("value = %v, want %d", got.ValueInt, asInt)
	}
}

func TestMetricsReceiver_Export_Histogram(t *testing.T) {
	q := acquirePgmqQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewMetricsReceiver(q, buf, emailalias.Aliases{})

	histSum := float64(1234.5)
	req := &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{
						{Key: "user.email", Value: strAV("hist@test.com")},
					},
				},
				ScopeMetrics: []*metricspb.ScopeMetrics{
					{
						Metrics: []*metricspb.Metric{
							{
								Name: "claude.latency_ms",
								Data: &metricspb.Metric_Histogram{
									Histogram: &metricspb.Histogram{
										DataPoints: []*metricspb.HistogramDataPoint{
											{
												TimeUnixNano: uint64(time.Now().UnixNano()),
												Count:        10,
												Sum:          &histSum,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	if _, err := recv.Export(ctx, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	requireMetricEnqueued(t, q, buf, func(m *store.OtelMetric) bool {
		return m.MetricName == "claude.latency_ms"
	})
}

func TestMetricsReceiver_Export_Empty(t *testing.T) {
	q := acquirePgmqQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewMetricsReceiver(q, buf, emailalias.Aliases{})

	// Errors are checked here, unlike before: Depth returns 0 on failure, so two
	// discarded errors made this assertion compare 0 to 0 and pass without having
	// measured anything.
	depthBefore, err := q.Depth(ctx, queue.QueueOtelMetrics)
	if err != nil {
		t.Fatalf("Depth before: %v", err)
	}
	resp, err := recv.Export(ctx, &colmetricspb.ExportMetricsServiceRequest{})
	if err != nil {
		t.Fatalf("Export empty: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}

	depthAfter, err := q.Depth(ctx, queue.QueueOtelMetrics)
	if err != nil {
		t.Fatalf("Depth after: %v", err)
	}
	if depthAfter != depthBefore {
		t.Errorf("empty request changed queue depth: before=%d after=%d", depthBefore, depthAfter)
	}
}

func TestMetricsReceiver_Export_BufferFallback(t *testing.T) {
	q := acquirePlainQueue(t)
	ctx := context.Background()

	buf := buffer.NewRing(100)
	recv := NewMetricsReceiver(q, buf, emailalias.Aliases{})

	req := buildMetricsRequest("claude.cost", 0.05, "buf-metric@test.com")

	resp, err := recv.Export(ctx, req)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}

	// pgmq absent → metric should be buffered
	if buf.Len() != 1 {
		t.Errorf("expected 1 item in buffer, got %d", buf.Len())
	}

	data, ok := buf.Pop()
	if !ok {
		t.Fatal("expected item in buffer")
	}
	var m store.OtelMetric
	if err := json.Unmarshal(data, &m); err != nil {
		t.Errorf("buffered data not valid OtelMetric JSON: %v", err)
	}
	if m.MetricName != "claude.cost" {
		t.Errorf("MetricName: got %q, want %q", m.MetricName, "claude.cost")
	}
}

// --- TracesReceiver ---

func TestTracesReceiver_Export(t *testing.T) {
	recv := NewTracesReceiver()
	resp, err := recv.Export(context.Background(), &coltracepb.ExportTraceServiceRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("status = %v, want Unimplemented", status.Code(err))
	}
	if resp != nil {
		t.Fatalf("response = %v, want nil", resp)
	}
}
