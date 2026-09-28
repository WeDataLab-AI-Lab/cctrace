package otelrecv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cctrace/internal/auth"
	"cctrace/internal/emailalias"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// unsupportedRequest builds an export whose metrics all get dropped. Nothing is
// enqueued for such a request, so the receiver can run without a queue.
func unsupportedRequest(names ...string) *colmetricspb.ExportMetricsServiceRequest {
	var ms []*metricspb.Metric
	for _, n := range names {
		ms = append(ms, &metricspb.Metric{
			Name: n,
			Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{
				DataPoints: []*metricspb.SummaryDataPoint{{Count: 1}},
			}},
		})
	}
	return &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: ms}},
		}},
	}
}

// The whole point of the change is that the sender learns about the loss, so
// the response itself has to carry it.
func TestExportReportsPartialSuccess(t *testing.T) {
	resetMetricDropCounts(t)
	recv := NewMetricsReceiver(nil, nil, emailalias.Aliases{})

	resp, err := recv.Export(context.Background(), unsupportedRequest("a.summary", "b.summary"))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	ps := resp.GetPartialSuccess()
	if ps == nil {
		t.Fatal("no partial_success; the sender would count the export as fully delivered")
	}
	if ps.RejectedDataPoints != 2 {
		t.Errorf("rejected_data_points = %d, want 2", ps.RejectedDataPoints)
	}
	for _, want := range []string{"a.summary", "b.summary"} {
		if !strings.Contains(ps.ErrorMessage, want) {
			t.Errorf("error_message %q does not mention %q", ps.ErrorMessage, want)
		}
	}
}

// A fully parsed export must stay a bare OK, or every sender starts logging
// warnings for traffic that was accepted.
func TestExportNoPartialSuccessWhenNothingDropped(t *testing.T) {
	resetMetricDropCounts(t)
	recv := NewMetricsReceiver(nil, nil, emailalias.Aliases{})

	resp, err := recv.Export(context.Background(),
		&colmetricspb.ExportMetricsServiceRequest{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if resp.GetPartialSuccess() != nil {
		t.Errorf("unexpected partial_success: %v", resp.GetPartialSuccess())
	}
}

// Regression: the HTTP handler used to answer a hardcoded "{}" and throw the
// response away. Codex sends every metric over this path, so that made the
// change a no-op for all Codex clients.
func TestHTTPMetricsReturnsPartialSuccess(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		encode      func(proto.Message) ([]byte, error)
		decode      func([]byte, proto.Message) error
	}{
		{
			name:        "protobuf",
			contentType: "application/x-protobuf",
			encode:      func(m proto.Message) ([]byte, error) { return proto.Marshal(m) },
			decode:      func(b []byte, m proto.Message) error { return proto.Unmarshal(b, m) },
		},
		{
			name:        "json",
			contentType: "application/json",
			encode:      func(m proto.Message) ([]byte, error) { return protojson.Marshal(m) },
			decode:      func(b []byte, m proto.Message) error { return protojson.Unmarshal(b, m) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetMetricDropCounts(t)
			recv := NewHTTPReceiver(nil, NewMetricsReceiver(nil, nil, emailalias.Aliases{}))
			srv := httptest.NewServer(recv.Handler())
			defer srv.Close()

			body, err := tc.encode(unsupportedRequest("codex.turn.summary"))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.Post(srv.URL+"/v1/metrics", tc.contentType, bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			var out colmetricspb.ExportMetricsServiceResponse
			raw := new(bytes.Buffer)
			if _, err := raw.ReadFrom(resp.Body); err != nil {
				t.Fatal(err)
			}
			if err := tc.decode(raw.Bytes(), &out); err != nil {
				t.Fatalf("decode response %q: %v", raw.String(), err)
			}
			ps := out.GetPartialSuccess()
			if ps == nil {
				t.Fatalf("no partial_success in HTTP response %q", raw.String())
			}
			if ps.RejectedDataPoints != 1 {
				t.Errorf("rejected_data_points = %d, want 1", ps.RejectedDataPoints)
			}
			if got := resp.Header.Get("Content-Type"); got != tc.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tc.contentType)
			}
		})
	}
}

func TestHTTPMetricsIdentityLookupFailureIsUnavailable(t *testing.T) {
	recv := NewHTTPReceiver(nil, NewMetricsReceiver(nil, nil, emailalias.Aliases{})).
		WithIdentityResolver(func(context.Context, string) (*ClientIdentity, error) {
			return nil, errors.New("database unavailable")
		})
	req := httptest.NewRequest(http.MethodPost, "/v1/metrics", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer per-user-token")
	rec := httptest.NewRecorder()

	recv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 rather than accepting unattributed telemetry", rec.Code)
	}
}

func TestHTTPReceiverRejectsOversizedBody(t *testing.T) {
	recv := NewHTTPReceiver(nil, NewMetricsReceiver(nil, nil, emailalias.Aliases{}))
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/metrics",
		io.LimitReader(zeroReader{}, maxRequestBodyBytes+1),
	)
	rec := httptest.NewRecorder()

	recv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestHTTPReceiverRejectsUnsupportedTraces(t *testing.T) {
	recv := NewHTTPReceiver(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader("{}"))
	rec := httptest.NewRecorder()

	recv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// Attribute values are client controlled and carry account identifiers and
// tokens. Container logs have looser access control than the database, so the
// drop log must name the keys without copying what they held.
func TestMetricSampleOmitsAttributeValues(t *testing.T) {
	secret := "sk-ant-SECRET123"
	m := &metricspb.Metric{
		Name: "claude_code.cost.usage",
		Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{
			DataPoints: []*metricspb.SummaryDataPoint{{
				Attributes: []*commonpb.KeyValue{
					{Key: "api.key", Value: &commonpb.AnyValue{
						Value: &commonpb.AnyValue_StringValue{StringValue: secret}}},
					{Key: "user.account", Value: &commonpb.AnyValue{
						Value: &commonpb.AnyValue_StringValue{StringValue: "someone"}}},
				},
			}},
		}},
	}

	sample := metricSample(m)
	if strings.Contains(sample, secret) || strings.Contains(sample, "someone") {
		t.Errorf("sample leaks attribute values: %s", sample)
	}
	for _, key := range []string{"api.key", "user.account"} {
		if !strings.Contains(sample, key) {
			t.Errorf("sample %q lost the attribute key %q, which is what diagnosis needs", sample, key)
		}
	}
}

// The drop counter is keyed by a client-supplied metric name and the HTTP OTLP
// port has no auth interceptor, so the map has to stop growing at some point.
//
// The bound is a memory guarantee, not an exact quota: dropCounter checks the
// cap and inserts non-atomically, so concurrent exports can overshoot by about
// the number of goroutines in flight. Asserting an exact cap would make this
// test flaky and would misdescribe the code.
func TestDropCounterCardinalityIsBounded(t *testing.T) {
	resetMetricDropCounts(t)

	const invented = metricDropKeyCap * 3
	for i := 0; i < invented; i++ {
		dropCounter(fmt.Sprintf("invented.name.%d", i))
	}

	n := 0
	metricDropCounts.Range(func(_, _ any) bool { n++; return true })
	if n > metricDropKeyCap {
		t.Errorf("retained %d keys, want at most %d (serial calls must not overshoot)", n, metricDropKeyCap)
	}
	if n >= invented {
		t.Errorf("retained %d of %d names: the map is not bounded at all", n, invented)
	}
	// Names past the cap are not tracked rather than sharing a counter, so no
	// single bucket can be inflated to push real drops past their next
	// milestone.
	if dropCounter("one.past.the.cap") != nil {
		t.Error("a name past the cap got a counter; the map is still growable")
	}
}

// Both the drop count and each metric name are caller controlled, so notes are
// capped instead of being joined in full and truncated afterwards.
func TestDropNotesAreCapped(t *testing.T) {
	resetMetricDropCounts(t)
	recv := NewMetricsReceiver(nil, nil, emailalias.Aliases{})

	names := make([]string, dropNotesMax*3)
	for i := range names {
		names[i] = fmt.Sprintf("%s.%d", strings.Repeat("n", 200), i)
	}
	resp, err := recv.Export(context.Background(), unsupportedRequest(names...))
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	msg := resp.GetPartialSuccess().GetErrorMessage()
	if len(msg) > dropMessageMaxBytes {
		t.Errorf("error_message is %d bytes, want at most %d", len(msg), dropMessageMaxBytes)
	}
	// Every drop still counts toward the rejected total even though only the
	// first few are named.
	if got := resp.GetPartialSuccess().GetRejectedDataPoints(); got != int64(len(names)) {
		t.Errorf("rejected_data_points = %d, want %d", got, len(names))
	}
	if !strings.Contains(msg, "more)") {
		t.Errorf("error_message does not say how many drops were omitted: %q", msg)
	}
	// Assert the cap itself, not just that the string is short. Only
	// dropNotesMax notes are collected, so the tail must account for exactly the
	// rest -- a raised cap would change this number.
	if want := fmt.Sprintf("(+%d more)", len(names)-dropNotesMax); !strings.Contains(msg, want) {
		t.Errorf("error_message %q does not end with %q", msg, want)
	}
	// Truncation may cut some of the collected notes, but never add more.
	if named := strings.Count(msg, ": unsupported metric type"); named > dropNotesMax {
		t.Errorf("error_message names %d drops, want at most %d", named, dropNotesMax)
	}
}

func TestHTTPBearerDoesNotRequireCSRFHeaders(t *testing.T) {
	receiver := NewHTTPReceiver(nil, NewMetricsReceiver(nil, nil, emailalias.Aliases{}))
	handler := auth.New("ingest-key").HTTPMiddleware(receiver.Handler())
	for _, origin := range []string{"", "https://evil.example.com"} {
		t.Run(origin, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/metrics", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer ingest-key")
			req.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
