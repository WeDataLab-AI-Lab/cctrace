package otelrecv

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"cctrace/internal/emailalias"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

func TestLogsRetainSourceEnvelopeWithoutChangingVerifiedIdentity(t *testing.T) {
	t.Parallel()
	r := &LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}}
	req := claimedLogsRequest("claimed@example.test", "claimed")
	scope := req.ResourceLogs[0].ScopeLogs[0]
	scope.Scope = &commonpb.InstrumentationScope{Name: "native-agent", Version: "test"}
	lr := scope.LogRecords[0]
	lr.Body = strAV("native body")
	lr.TraceId = []byte{1, 2, 3}
	lr.SpanId = []byte{4, 5}
	lr.ObservedTimeUnixNano = 1700000000000000100
	lr.SeverityNumber = logspb.SeverityNumber_SEVERITY_NUMBER_INFO
	lr.SeverityText = "INFO"
	events := r.buildEventsWithIdentity(req, &ClientIdentity{ProfileEmail: "verified@example.test", UserID: "verified"})
	data, err := json.Marshal(events[0].Envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"native body", "native-agent", "010203", "0405", "INFO", "observed_time_unix_nano"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("envelope lost %s: %s", want, data)
		}
	}
	if events[0].ProfileEmail != "verified@example.test" {
		t.Fatal("source identity replaced verified attribution")
	}
}

func TestSourceEnvelopeExpansionHasARequestByteBudget(t *testing.T) {
	t.Parallel()
	const budget = 16 << 10
	r := &LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}, envelopeBudgetBytes: budget}
	req := claimedLogsRequest("claimed@example.test", "claimed")
	resource := req.ResourceLogs[0].Resource
	resource.Attributes = append(resource.Attributes, &commonpb.KeyValue{Key: "synthetic.large_shared_context", Value: strAV(strings.Repeat("x", 4<<10))})
	scope := req.ResourceLogs[0].ScopeLogs[0]
	prototype := scope.LogRecords[0]
	scope.LogRecords = nil
	for i := 0; i < 32; i++ {
		scope.LogRecords = append(scope.LogRecords, &logspb.LogRecord{TimeUnixNano: prototype.TimeUnixNano, Attributes: prototype.Attributes, Body: strAV(fmt.Sprintf("SOURCE-BODY-%d", i))})
	}
	req.ResourceLogs = append(req.ResourceLogs, proto.Clone(req.ResourceLogs[0]).(*logspb.ResourceLogs))
	events := r.buildEventsWithIdentity(req, &ClientIdentity{ProfileEmail: "verified@example.test", UserID: "verified"})
	if len(events) != 64 {
		t.Fatalf("source envelope budget dropped core log records: got %d", len(events))
	}
	total, partial := 0, 0
	for _, event := range events {
		total += len(event.Envelope)
		if event.UserID != "verified" || event.ProfileEmail != "verified@example.test" {
			t.Error("budget fallback changed verified attribution")
		}
		var envelope map[string]any
		if err := json.Unmarshal(event.Envelope, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["partial"] == true {
			partial++
			fields, _ := envelope["dropped_fields"].([]any)
			if len(fields) == 0 {
				t.Errorf("source loss has no field provenance: %s", event.Envelope)
			}
		}
	}
	if total > budget {
		t.Errorf("source envelope bytes=%d exceed aggregate budget=%d", total, budget)
	}
	if partial == 0 {
		t.Error("fixture never exercised explicit source omission")
	}
}

func TestSourceEnvelopeOrdinaryBodiesRemainComplete(t *testing.T) {
	t.Parallel()
	r := &LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}}
	req := claimedLogsRequest("claimed@example.test", "claimed")
	body := strings.Repeat("<SOURCE-BODY>한글", 4096)
	scope := req.ResourceLogs[0].ScopeLogs[0]
	scope.Scope = &commonpb.InstrumentationScope{Name: "synthetic-scope", Version: "1"}
	scope.SchemaUrl = "https://example.invalid/scope"
	req.ResourceLogs[0].SchemaUrl = "https://example.invalid/resource"
	scope.LogRecords[0].Body = strAV(body)
	events := r.buildEventsWithIdentity(req, &ClientIdentity{ProfileEmail: "verified@example.test", UserID: "verified"})
	var envelope map[string]any
	if err := json.Unmarshal(events[0].Envelope, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["partial"] == true || envelope["dropped_fields"] != nil {
		t.Errorf("ordinary source was cut: %s", events[0].Envelope)
	}
	record := envelope["log_record"].(map[string]any)
	if got := record["body"].(map[string]any)["stringValue"]; got != body {
		t.Error("ordinary log body changed")
	}
	if envelope["resource"] == nil || envelope["scope"] == nil || envelope["scope_schema_url"] != scope.SchemaUrl || envelope["resource_schema_url"] != req.ResourceLogs[0].SchemaUrl {
		t.Errorf("common source envelope missing: %v", envelope)
	}
	if !strings.Contains(string(events[0].Envelope), "claimed@example.test") || events[0].ProfileEmail != "verified@example.test" {
		t.Error("source claims and verified attribution no longer remain separate")
	}
}

func TestSourceEnvelopeBudgetDoesNotDropCoreLogsWhenMarkersCannotFit(t *testing.T) {
	t.Parallel()
	r := &LogsReceiver{uidEmailMap: make(map[string]string), aliases: emailalias.Aliases{}, envelopeBudgetBytes: 1}
	req := claimedLogsRequest("claimed@example.test", "claimed")
	events := r.buildEventsWithIdentity(req, &ClientIdentity{ProfileEmail: "verified@example.test", UserID: "verified"})
	if len(events) != 1 || len(events[0].Envelope) != 0 {
		t.Fatalf("insufficient marker budget must leave source explicitly not collected, events=%+v", events)
	}
	if events[0].ProfileEmail != "verified@example.test" || events[0].UserID != "verified" {
		t.Error("marker budget changed core attribution")
	}
}
