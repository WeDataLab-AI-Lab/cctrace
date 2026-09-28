package otelrecv

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/jackc/pgx/v5/pgxpool"

	"cctrace/internal/auth"
	"cctrace/internal/buffer"
	"cctrace/internal/emailalias"
	"cctrace/internal/queue"
	"cctrace/internal/store"
)

// claimedLogsRequest builds a request whose payload names a user. Everything
// about who this data belongs to comes from these attributes -- which is the
// point of #535: the bearer token decides whether the request is accepted and
// has no say in whose data it becomes.
func claimedLogsRequest(profileEmail, userID string) *collogspb.ExportLogsServiceRequest {
	return &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				mkKV("user.profile.email", strAV(profileEmail)),
				mkKV("user.id", strAV(userID)),
			}},
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: 1_700_000_000_000_000_000,
					Attributes: []*commonpb.KeyValue{
						mkKV("event.name", strAV("claude_code.api_request")),
					},
				}},
			}},
		}},
	}
}

// A valid ingest token belongs to exactly one dashboard user, so the identity it
// resolves to is the only thing the server has actually verified. When the
// payload claims someone else, the verified identity has to win -- otherwise one
// leaked token can write telemetry under any address, and cost attribution and
// per-user charts all sit on that field.
func TestLogsIdentityOverridesTheClaimedAttribution(t *testing.T) {
	r := &LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}}

	events := r.buildEventsWithIdentity(
		claimedLogsRequest("victim@example.test", "victim"),
		&ClientIdentity{ProfileEmail: "holder@example.test", UserID: "holder"},
	)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	e := events[0]
	if e.ProfileEmail != "holder@example.test" {
		t.Errorf("profile_email = %q, want the token holder -- a claimed address must not decide attribution", e.ProfileEmail)
	}
	if e.UserID != "holder" {
		t.Errorf("user_id = %q, want the token holder", e.UserID)
	}
}

// login_email is the Anthropic account, which the cctrace token does not prove.
// Overwriting it would assert something the server never verified.
func TestLogsIdentityLeavesLoginEmailAlone(t *testing.T) {
	r := &LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}}

	req := claimedLogsRequest("claimed@example.test", "claimed")
	req.ResourceLogs[0].Resource.Attributes = append(
		req.ResourceLogs[0].Resource.Attributes, mkKV("user.email", strAV("login@example.test")))

	events := r.buildEventsWithIdentity(req, &ClientIdentity{
		ProfileEmail: "holder@example.test",
		LoginEmail:   "holder-login@example.test",
		UserID:       "holder",
	})
	if got := events[0].LoginEmail; got != "login@example.test" {
		t.Errorf("login_email = %q, want the payload's -- the cctrace token proves a dashboard account, not an Anthropic login", got)
	}
}

// Without a token there is nothing verified to substitute, so the payload stands
// and behaviour is exactly what it was. An unauthenticated path must not start
// dropping attribution.
func TestLogsWithoutIdentityKeepsThePayload(t *testing.T) {
	r := &LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}}

	events := r.buildEventsWithIdentity(claimedLogsRequest("payload@example.test", "payload"), nil)
	if got := events[0].ProfileEmail; got != "payload@example.test" {
		t.Errorf("profile_email = %q, want the payload's when no token resolved", got)
	}
	if got := events[0].UserID; got != "payload" {
		t.Errorf("user_id = %q, want the payload's when no token resolved", got)
	}
}

// exportThroughGRPC drives the real Export -- the method the gRPC server calls --
// and returns the events it produced. The queue points nowhere, so every Send
// fails and ExportWithIdentity falls back to the buffer, which is where we read
// the attribution it actually wrote. Going through Export is the point: asserting
// on buildEventsWithIdentity directly passes even when Export is not wired to the
// context at all.
func exportThroughGRPC(t *testing.T, r *LogsReceiver, ctx context.Context, req *collogspb.ExportLogsServiceRequest) []*store.OtelEvent {
	t.Helper()
	deadPool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deadPool.Close)
	r.queue = queue.New(deadPool)
	r.buffer = buffer.NewRing(16)

	if _, err := r.Export(ctx, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	var events []*store.OtelEvent
	for {
		data, ok := r.buffer.Pop()
		if !ok {
			return events
		}
		var e store.OtelEvent
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatalf("buffered event is not an OtelEvent: %v", err)
		}
		events = append(events, &e)
	}
}

// The gRPC path has no header to read: its interceptor verifies the token and
// leaves it in the context. Export must pick it up there, or gRPC keeps
// attributing purely from the payload while HTTP no longer does -- the same hole
// behind a different transport.
func TestLogsGRPCPathResolvesIdentityFromTheContext(t *testing.T) {
	r := (&LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}}).
		WithIdentityResolver(func(_ context.Context, token string) (*ClientIdentity, error) {
			if token != "tok-holder" {
				t.Errorf("resolver saw token %q", token)
			}
			return &ClientIdentity{ProfileEmail: "holder@example.test", UserID: "holder"}, nil
		})

	ctx := auth.ContextWithIngestToken(context.Background(), "tok-holder")
	events := exportThroughGRPC(t, r, ctx, claimedLogsRequest("victim@example.test", "victim"))
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if got := events[0].ProfileEmail; got != "holder@example.test" {
		t.Errorf("profile_email = %q, want the token holder -- the gRPC path did not bind identity", got)
	}
	if got := events[0].UserID; got != "holder" {
		t.Errorf("user_id = %q, want the token holder", got)
	}
}

// A lookup failure must not drop telemetry. Attribution falls back to the
// payload, which is where it was before any of this existed.
func TestLogsGRPCIdentityFailureKeepsTheData(t *testing.T) {
	r := (&LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}}).
		WithIdentityResolver(func(context.Context, string) (*ClientIdentity, error) {
			return nil, errors.New("database unavailable")
		})

	ctx := auth.ContextWithIngestToken(context.Background(), "tok")
	events := exportThroughGRPC(t, r, ctx, claimedLogsRequest("payload@example.test", "payload"))
	if len(events) != 1 {
		t.Fatalf("events = %d, want the data kept", len(events))
	}
	if got := events[0].ProfileEmail; got != "payload@example.test" {
		t.Errorf("profile_email = %q, want the payload's when the lookup failed", got)
	}
}

// No token means the global API key or an unauthenticated path, neither of which
// names a user. Nothing to substitute, so nothing changes.
func TestLogsGRPCWithoutATokenIsUnchanged(t *testing.T) {
	r := (&LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}}).
		WithIdentityResolver(func(context.Context, string) (*ClientIdentity, error) {
			t.Errorf("resolver must not be called without a token")
			return nil, nil
		})

	events := exportThroughGRPC(t, r, context.Background(), claimedLogsRequest("payload@example.test", "payload"))
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if got := events[0].ProfileEmail; got != "payload@example.test" {
		t.Errorf("profile_email = %q, want the payload's untouched", got)
	}
}

// claimedMetricsRequest is the metrics twin of claimedLogsRequest. Metrics carry
// token counts and cost, so a forged attribution here lands in someone else's
// usage and spend, not only their activity list.
func claimedMetricsRequest(profileEmail, userID string) *colmetricspb.ExportMetricsServiceRequest {
	return &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				mkKV("user.profile.email", strAV(profileEmail)),
				mkKV("user.id", strAV(userID)),
				mkKV("user.team", strAV("victim-team")),
			}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "claude_code.cost.usage",
					Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
						DataPoints: []*metricspb.NumberDataPoint{{
							TimeUnixNano: 1_700_000_000_000_000_000,
							Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 1.25},
						}},
					}},
				}},
			}},
		}},
	}
}

// exportMetricsThroughGRPC is the metrics twin of exportThroughGRPC.
func exportMetricsThroughGRPC(t *testing.T, r *MetricsReceiver, ctx context.Context, req *colmetricspb.ExportMetricsServiceRequest) []*store.OtelMetric {
	t.Helper()
	deadPool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deadPool.Close)
	r.queue = queue.New(deadPool)
	r.buffer = buffer.NewRing(16)

	if _, err := r.Export(ctx, req); err != nil {
		t.Fatalf("Export: %v", err)
	}

	var metrics []*store.OtelMetric
	for {
		data, ok := r.buffer.Pop()
		if !ok {
			return metrics
		}
		var m store.OtelMetric
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("buffered metric is not an OtelMetric: %v", err)
		}
		metrics = append(metrics, &m)
	}
}

func TestMetricsGRPCPathResolvesIdentityFromTheContext(t *testing.T) {
	r := (&MetricsReceiver{aliases: emailalias.Aliases{}}).
		WithIdentityResolver(func(_ context.Context, token string) (*ClientIdentity, error) {
			if token != "tok-holder" {
				t.Errorf("resolver saw token %q", token)
			}
			return &ClientIdentity{ProfileEmail: "holder@example.test", UserID: "holder", UserTeam: "holder-team"}, nil
		})

	ctx := auth.ContextWithIngestToken(context.Background(), "tok-holder")
	metrics := exportMetricsThroughGRPC(t, r, ctx, claimedMetricsRequest("victim@example.test", "victim"))
	if len(metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(metrics))
	}
	if got := metrics[0].ProfileEmail; got != "holder@example.test" {
		t.Errorf("profile_email = %q, want the token holder -- spend would land on the victim", got)
	}
	if got := metrics[0].UserID; got != "holder" {
		t.Errorf("user_id = %q, want the token holder", got)
	}
	// user_team is a property of the profile, so it must not keep the claim's
	// value once the profile has been replaced.
	if got := metrics[0].UserTeam; got != "holder-team" {
		t.Errorf("user_team = %q, want the token holder's team", got)
	}
}

func TestMetricsGRPCWithoutATokenIsUnchanged(t *testing.T) {
	r := (&MetricsReceiver{aliases: emailalias.Aliases{}}).
		WithIdentityResolver(func(context.Context, string) (*ClientIdentity, error) {
			t.Errorf("resolver must not be called without a token")
			return nil, nil
		})

	metrics := exportMetricsThroughGRPC(t, r, context.Background(), claimedMetricsRequest("payload@example.test", "payload"))
	if len(metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(metrics))
	}
	if got := metrics[0].ProfileEmail; got != "payload@example.test" {
		t.Errorf("profile_email = %q, want the payload's untouched", got)
	}
	if got := metrics[0].UserTeam; got != "victim-team" {
		t.Errorf("user_team = %q, want the payload's untouched", got)
	}
}
