package main

import (
	"context"
	"fmt"
	"log"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type profile struct {
	email  string
	name   string
	team   string
	userID string
	orgID  string
}

var profiles = []profile{
	{email: "alice@example.com", name: "Alice Smith", team: "engineering", userID: "alice-uid-001", orgID: "org-example-001"},
	{email: "bob@example.com", name: "Bob Jones", team: "product", userID: "bob-uid-002", orgID: "org-example-001"},
	{email: "carol@example.com", name: "Carol Park", team: "product", userID: "carol-uid-003", orgID: "org-example-001"},
}

type apiRequest struct {
	sessionID   string
	promptID    string
	model       string
	inputTok    int
	outputTok   int
	cacheRead   int
	cacheCreate int
	costUSD     float64
	durationMs  int
	ts          time.Time
}

func toNano(t time.Time) uint64 {
	return uint64(t.UnixNano())
}

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func resourceAttrs(p profile) []*commonpb.KeyValue {
	return []*commonpb.KeyValue{
		strAttr("user.id", p.userID),
		strAttr("user.email", p.email),
		strAttr("user.name", p.name),
		strAttr("user.team", p.team),
		strAttr("organization.id", p.orgID),
		strAttr("service.name", "claude-code"),
		strAttr("service.version", "2.1.71"),
		strAttr("os.type", "darwin"),
		strAttr("host.arch", "arm64"),
		strAttr("terminal.type", "Apple_Terminal"),
	}
}

func makeApiRequestLog(p profile, req apiRequest) *logspb.LogRecord {
	ts := toNano(req.ts)
	attrs := []*commonpb.KeyValue{
		strAttr("event.name", "api_request"),
		strAttr("event.timestamp", req.ts.UTC().Format(time.RFC3339Nano)),
		strAttr("session.id", req.sessionID),
		strAttr("prompt.id", req.promptID),
		strAttr("model", req.model),
		strAttr("input_tokens", fmt.Sprintf("%d", req.inputTok)),
		strAttr("output_tokens", fmt.Sprintf("%d", req.outputTok)),
		strAttr("cache_read_tokens", fmt.Sprintf("%d", req.cacheRead)),
		strAttr("cache_creation_tokens", fmt.Sprintf("%d", req.cacheCreate)),
		strAttr("cost_usd", fmt.Sprintf("%f", req.costUSD)),
		strAttr("duration_ms", fmt.Sprintf("%d", req.durationMs)),
		strAttr("speed", "normal"),
	}
	return &logspb.LogRecord{
		TimeUnixNano:         ts,
		ObservedTimeUnixNano: ts,
		Attributes:           attrs,
	}
}

func makeUserPromptLog(p profile, sessionID, promptID string, ts time.Time) *logspb.LogRecord {
	nano := toNano(ts)
	attrs := []*commonpb.KeyValue{
		strAttr("event.name", "user_prompt"),
		strAttr("event.timestamp", ts.UTC().Format(time.RFC3339Nano)),
		strAttr("session.id", sessionID),
		strAttr("prompt.id", promptID),
		strAttr("prompt_length", "42"),
	}
	return &logspb.LogRecord{
		TimeUnixNano:         nano,
		ObservedTimeUnixNano: nano,
		Attributes:           attrs,
	}
}

func sendLogs(ctx context.Context, client collogspb.LogsServiceClient, p profile, records []*logspb.LogRecord) error {
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{
			{
				Resource: &resourcepb.Resource{Attributes: resourceAttrs(p)},
				ScopeLogs: []*logspb.ScopeLogs{
					{LogRecords: records},
				},
			},
		},
	}
	_, err := client.Export(ctx, req)
	return err
}

func sendCostMetric(ctx context.Context, client colmetricspb.MetricsServiceClient, p profile, sessionID, model string, cost float64, ts time.Time) error {
	nano := toNano(ts)
	req := &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: &resourcepb.Resource{Attributes: resourceAttrs(p)},
				ScopeMetrics: []*metricspb.ScopeMetrics{
					{
						Metrics: []*metricspb.Metric{
							{
								Name: "claude_code.cost.usage",
								Data: &metricspb.Metric_Sum{
									Sum: &metricspb.Sum{
										DataPoints: []*metricspb.NumberDataPoint{
											{
												Attributes: []*commonpb.KeyValue{
													strAttr("session.id", sessionID),
													strAttr("model", model),
												},
												TimeUnixNano: nano,
												Value: &metricspb.NumberDataPoint_AsDouble{
													AsDouble: cost,
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
		},
	}
	_, err := client.Export(ctx, req)
	return err
}

func main() {
	conn, err := grpc.NewClient("localhost:4317",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	logsClient := collogspb.NewLogsServiceClient(conn)
	metricsClient := colmetricspb.NewMetricsServiceClient(conn)
	ctx := context.Background()

	now := time.Now()
	day := func(d int) time.Time { return now.AddDate(0, 0, d) }

	// --- Alice: engineering, heavy sonnet + opus user ---
	aliceRequests := []apiRequest{
		{sessionID: "alice-s01", promptID: "alice-p01", model: "claude-sonnet-4-6", inputTok: 2500, outputTok: 800, cacheRead: 1200, cacheCreate: 300, costUSD: 0.0185, durationMs: 3200, ts: day(-1).Add(9 * time.Hour)},
		{sessionID: "alice-s01", promptID: "alice-p02", model: "claude-sonnet-4-6", inputTok: 3100, outputTok: 1200, cacheRead: 2000, cacheCreate: 500, costUSD: 0.0240, durationMs: 4100, ts: day(-1).Add(9*time.Hour + 15*time.Minute)},
		{sessionID: "alice-s02", promptID: "alice-p03", model: "claude-opus-4-6", inputTok: 5000, outputTok: 2000, cacheRead: 3000, cacheCreate: 800, costUSD: 0.1320, durationMs: 7500, ts: day(-2).Add(14 * time.Hour)},
		{sessionID: "alice-s02", promptID: "alice-p04", model: "claude-opus-4-6", inputTok: 4200, outputTok: 1800, cacheRead: 2500, cacheCreate: 600, costUSD: 0.1105, durationMs: 6800, ts: day(-2).Add(14*time.Hour + 30*time.Minute)},
		{sessionID: "alice-s03", promptID: "alice-p05", model: "claude-sonnet-4-6", inputTok: 1800, outputTok: 600, cacheRead: 900, cacheCreate: 200, costUSD: 0.0138, durationMs: 2400, ts: day(-3).Add(10*time.Hour + 30*time.Minute)},
		{sessionID: "alice-s04", promptID: "alice-p06", model: "claude-haiku-4-5-20251001", inputTok: 800, outputTok: 300, cacheRead: 400, cacheCreate: 100, costUSD: 0.0014, durationMs: 900, ts: day(-4).Add(16 * time.Hour)},
		{sessionID: "alice-s05", promptID: "alice-p07", model: "claude-sonnet-4-6", inputTok: 2200, outputTok: 750, cacheRead: 1100, cacheCreate: 250, costUSD: 0.0168, durationMs: 2900, ts: day(0).Add(9 * time.Hour)},
		{sessionID: "alice-s05", promptID: "alice-p08", model: "claude-sonnet-4-6", inputTok: 1900, outputTok: 650, cacheRead: 950, cacheCreate: 220, costUSD: 0.0145, durationMs: 2600, ts: day(0).Add(9*time.Hour + 45*time.Minute)},
	}

	// --- Bob: product, moderate sonnet + opus ---
	bobRequests := []apiRequest{
		{sessionID: "bob-s01", promptID: "bob-p01", model: "claude-sonnet-4-6", inputTok: 1500, outputTok: 500, cacheRead: 700, cacheCreate: 150, costUSD: 0.0115, durationMs: 2100, ts: day(0).Add(8*time.Hour + 30*time.Minute)},
		{sessionID: "bob-s02", promptID: "bob-p02", model: "claude-opus-4-6", inputTok: 4200, outputTok: 1800, cacheRead: 2500, cacheCreate: 600, costUSD: 0.1105, durationMs: 6800, ts: day(0).Add(11 * time.Hour)},
		{sessionID: "bob-s02", promptID: "bob-p03", model: "claude-opus-4-6", inputTok: 3800, outputTok: 1500, cacheRead: 2200, cacheCreate: 500, costUSD: 0.0980, durationMs: 6200, ts: day(0).Add(11*time.Hour + 25*time.Minute)},
		{sessionID: "bob-s03", promptID: "bob-p04", model: "claude-sonnet-4-6", inputTok: 2200, outputTok: 900, cacheRead: 1100, cacheCreate: 250, costUSD: 0.0168, durationMs: 2800, ts: day(-2).Add(9 * time.Hour)},
		{sessionID: "bob-s04", promptID: "bob-p05", model: "claude-haiku-4-5-20251001", inputTok: 600, outputTok: 200, cacheRead: 300, cacheCreate: 80, costUSD: 0.0010, durationMs: 700, ts: day(-5).Add(15 * time.Hour)},
		{sessionID: "bob-s05", promptID: "bob-p06", model: "claude-sonnet-4-6", inputTok: 1700, outputTok: 580, cacheRead: 850, cacheCreate: 190, costUSD: 0.0130, durationMs: 2200, ts: day(-3).Add(13 * time.Hour)},
	}

	// --- Carol: product, heavy user, mostly sonnet + some opus ---
	carolRequests := []apiRequest{
		{sessionID: "carol-s01", promptID: "carol-p01", model: "claude-sonnet-4-6", inputTok: 3500, outputTok: 1400, cacheRead: 1800, cacheCreate: 450, costUSD: 0.0280, durationMs: 4600, ts: day(0).Add(10 * time.Hour)},
		{sessionID: "carol-s01", promptID: "carol-p02", model: "claude-sonnet-4-6", inputTok: 2800, outputTok: 1100, cacheRead: 1400, cacheCreate: 350, costUSD: 0.0220, durationMs: 3800, ts: day(0).Add(13 * time.Hour)},
		{sessionID: "carol-s02", promptID: "carol-p03", model: "claude-opus-4-6", inputTok: 6000, outputTok: 2500, cacheRead: 3500, cacheCreate: 900, costUSD: 0.1620, durationMs: 9200, ts: day(-2).Add(11 * time.Hour)},
		{sessionID: "carol-s02", promptID: "carol-p04", model: "claude-opus-4-6", inputTok: 5500, outputTok: 2200, cacheRead: 3200, cacheCreate: 800, costUSD: 0.1480, durationMs: 8500, ts: day(-2).Add(11*time.Hour + 40*time.Minute)},
		{sessionID: "carol-s03", promptID: "carol-p05", model: "claude-sonnet-4-6", inputTok: 2000, outputTok: 750, cacheRead: 1000, cacheCreate: 280, costUSD: 0.0154, durationMs: 2700, ts: day(-3).Add(14 * time.Hour)},
		{sessionID: "carol-s04", promptID: "carol-p06", model: "claude-haiku-4-5-20251001", inputTok: 900, outputTok: 350, cacheRead: 450, cacheCreate: 120, costUSD: 0.0016, durationMs: 1100, ts: day(-4).Add(10 * time.Hour)},
		{sessionID: "carol-s05", promptID: "carol-p07", model: "claude-sonnet-4-6", inputTok: 1600, outputTok: 600, cacheRead: 800, cacheCreate: 200, costUSD: 0.0123, durationMs: 2100, ts: day(-5).Add(9 * time.Hour)},
		{sessionID: "carol-s06", promptID: "carol-p08", model: "claude-sonnet-4-6", inputTok: 2400, outputTok: 880, cacheRead: 1200, cacheCreate: 320, costUSD: 0.0185, durationMs: 3100, ts: day(-1).Add(15 * time.Hour)},
		{sessionID: "carol-s06", promptID: "carol-p09", model: "claude-sonnet-4-6", inputTok: 1950, outputTok: 700, cacheRead: 980, cacheCreate: 260, costUSD: 0.0150, durationMs: 2500, ts: day(-1).Add(15*time.Hour + 35*time.Minute)},
	}

	allData := []struct {
		p        profile
		requests []apiRequest
	}{
		{profiles[0], aliceRequests},
		{profiles[1], bobRequests},
		{profiles[2], carolRequests},
	}

	for _, d := range allData {
		p := d.p
		var logRecords []*logspb.LogRecord

		// Add user_prompt events for each unique session
		sessions := map[string]bool{}
		for _, req := range d.requests {
			if !sessions[req.sessionID] {
				sessions[req.sessionID] = true
				logRecords = append(logRecords, makeUserPromptLog(p, req.sessionID, req.promptID+"-up", req.ts.Add(-2*time.Second)))
			}
			logRecords = append(logRecords, makeApiRequestLog(p, req))
		}

		if err := sendLogs(ctx, logsClient, p, logRecords); err != nil {
			log.Fatalf("send logs for %s: %v", p.email, err)
		}
		fmt.Printf("Sent %d log records for %s\n", len(logRecords), p.email)

		// Send cost metrics per request
		for _, req := range d.requests {
			if err := sendCostMetric(ctx, metricsClient, p, req.sessionID, req.model, req.costUSD, req.ts); err != nil {
				log.Fatalf("send metric for %s: %v", p.email, err)
			}
		}
		fmt.Printf("Sent %d cost metrics for %s\n", len(d.requests), p.email)
	}

	fmt.Println("Done. Test profiles injected successfully.")
}
